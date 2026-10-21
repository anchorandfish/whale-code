package doctor

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
)

// mockAIClient 是 ai.Client 接口的 mock 实现，用于测试 API 连通性检测。
type mockAIClient struct {
	streamErr error // Stream 返回的错误（nil 表示成功）
}

// Stream 实现 ai.Client 接口，返回预设的错误或空事件流。
func (m *mockAIClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	events := make(chan ai.Event)
	close(events)
	return events, nil
}

// Model 实现 ai.Client 接口。
func (m *mockAIClient) Model() string { return "test-model" }

// SetModel 实现 ai.Client 接口。
func (m *mockAIClient) SetModel(model string) {}

// Close 实现 ai.Client 接口。
func (m *mockAIClient) Close() error { return nil }

// 反例 1：API key 缺失时不 panic（边界 case）
// 验证：API key 缺失时 /doctor 不 panic，标记 fatal 但继续其他检测。
func TestDoctor_APIKeyMissing_NoPanic(t *testing.T) {
	cfg := config.DefaultConfigPtr()
	cfg.Model = "test" // 有 model 但无 API key
	doc := NewDoctor(cfg, nil, t.TempDir(), "/nonexistent/settings.json")

	// 不应 panic
	results := doc.Run(context.Background())

	// 应有 fatal 项（API key 缺失）
	if !doc.HasFatal(results) {
		t.Errorf("期望 HasFatal=true（API key 缺失应标记 fatal），实际 HasFatal=false")
	}

	// 应继续其他检测（不 panic 且有结果）
	if len(results) == 0 {
		t.Errorf("期望 results 非空（应继续其他检测），实际为空")
	}

	// API 检测应跳过或返回 fatal（不 panic）
	for _, r := range results {
		if r.Category == "API" {
			// aiClient 为 nil 应返回 fatal "not initialized"
			if r.Severity != SeverityFatal {
				t.Errorf("API 检测项应为 fatal，实际 %v (msg: %s)", r.Severity, r.Message)
			}
		}
	}
}

// 反例 2：Windows DLP 检测正确执行（平台兼容）
// 验证：Windows 平台 DLP 检测项正确执行，Linux 跳过（返回 "not applicable"）。
func TestDoctor_EsafenetDLP_PlatformBranch(t *testing.T) {
	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.Run(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "Esafenet DLP" {
			found = true
			if runtime.GOOS != "windows" {
				// 非_windows 平台应返回 "not applicable"
				if r.Message != "not applicable (non-windows)" {
					t.Errorf("非 Windows 平台期望 'not applicable'，实际 %q", r.Message)
				}
			}
		}
	}
	if !found {
		t.Errorf("期望存在 Esafenet DLP 检测项，实际未找到")
	}
}

// 反例 3：Winsock 检测不依赖 httptest（技术可行性）
// 验证：Winsock 检测用 net.Listen 而非 httptest.NewServer。
func TestDoctor_Winsock_NoHTTPTest(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Winsock 检测仅在 Windows 平台测试")
	}

	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.Run(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "Winsock" {
			found = true
			// 不应包含 httptest 错误
			if strings.Contains(r.Message, "httptest") {
				t.Errorf("Winsock 检测不应依赖 httptest，消息含 'httptest': %q", r.Message)
			}
		}
	}
	if !found {
		t.Errorf("期望存在 Winsock 检测项，实际未找到")
	}
}

// 反例 4：配置缺失时引导修复（边界 case）
// 验证：settings.json 不存在时标记 warn 并建议 /onboarding。
func TestDoctor_ConfigMissing_SuggestOnboarding(t *testing.T) {
	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "/nonexistent/settings.json")
	results := doc.Run(context.Background())

	for _, r := range results {
		if r.Name == "settings.json" {
			if r.Severity != SeverityWarn {
				t.Errorf("settings.json 不存在期望 SeverityWarn，实际 %v", r.Severity)
			}
			if !strings.Contains(r.FixSuggest, "/onboarding") {
				t.Errorf("期望 FixSuggest 含 '/onboarding'，实际 %q", r.FixSuggest)
			}
		}
	}
}

// 反例 5：双平台分支覆盖（平台兼容）
// 验证：所有平台特定检测项在非目标平台返回 "not applicable"。
func TestDoctor_PlatformBranches_AllCovered(t *testing.T) {
	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.Run(context.Background())

	platformChecks := []string{"Esafenet DLP", "Winsock", "inotify"}
	for _, name := range platformChecks {
		found := false
		for _, r := range results {
			if r.Name == name {
				found = true
				// 非目标平台应返回 "not applicable"
				switch name {
				case "Esafenet DLP", "Winsock":
					if runtime.GOOS != "windows" {
						if r.Message != "not applicable (non-windows)" {
							t.Errorf("%s: 非 Windows 期望 'not applicable'，实际 %q", name, r.Message)
						}
					}
				case "inotify":
					if runtime.GOOS != "linux" {
						if r.Message != "not applicable (non-linux)" {
							t.Errorf("%s: 非 Linux 期望 'not applicable'，实际 %q", name, r.Message)
						}
					}
				}
			}
		}
		if !found {
			t.Errorf("期望存在平台检测项 %s，实际未找到", name)
		}
	}
}

// TestDoctor_NetworkCheckEndpointUsesOpenAIConfig 验证网络检测跟随实际 OpenAI 兼容地址。
func TestDoctor_NetworkCheckEndpointUsesOpenAIConfig(t *testing.T) {
	t.Setenv("WHALE_NETWORK_CHECK_ENDPOINT", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.invalid/compatible-mode/v1")

	cfg := config.DefaultConfigPtr()
	cfg.Provider = "openai"
	cfg.CustomBaseURL = "https://settings.invalid/v1"
	doc := NewDoctor(cfg, nil, t.TempDir(), "")

	if got := doc.networkCheckEndpoint(); got != "https://example.invalid/compatible-mode/v1" {
		t.Fatalf("networkCheckEndpoint() = %q, want OpenAI env base URL", got)
	}
}

// TestDoctor_NetworkCheckEndpointUsesSettingsFallback 验证无 Provider 环境变量时回退 settings.json。
func TestDoctor_NetworkCheckEndpointUsesSettingsFallback(t *testing.T) {
	t.Setenv("WHALE_NETWORK_CHECK_ENDPOINT", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")

	cfg := config.DefaultConfigPtr()
	cfg.Provider = "openai"
	cfg.CustomBaseURL = "https://settings.invalid/v1"
	doc := NewDoctor(cfg, nil, t.TempDir(), "")

	if got := doc.networkCheckEndpoint(); got != "https://settings.invalid/v1" {
		t.Fatalf("networkCheckEndpoint() = %q, want settings custom base URL", got)
	}
}

// TestDoctor_RenderEmptyResults 验证空结果渲染不 panic。
func TestDoctor_RenderEmptyResults(t *testing.T) {
	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	output := doc.Render(nil)
	if !strings.Contains(output, "no checks completed") {
		t.Errorf("空结果期望含 'no checks completed'，实际 %q", output)
	}
}

// TestDoctor_RenderWithResults 验证有结果时渲染含分类标题和汇总。
func TestDoctor_RenderWithResults(t *testing.T) {
	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := []CheckResult{
		{Category: "Environment", Name: "OS", Severity: SeverityOK, Message: "windows/amd64"},
		{Category: "Config", Name: "API Key", Severity: SeverityFatal, Message: "NOT SET"},
	}
	output := doc.Render(results)
	if !strings.Contains(output, "Environment") {
		t.Errorf("渲染应含 'Environment' 分类")
	}
	if !strings.Contains(output, "Summary") {
		t.Errorf("渲染应含 'Summary' 汇总")
	}
	if !strings.Contains(output, "1 fatal") {
		t.Errorf("渲染应含 '1 fatal' 计数，实际 %q", output)
	}
}

// TestDoctor_APIKeyMasked 验证 API key 在渲染时脱敏。
func TestDoctor_APIKeyMasked(t *testing.T) {
	// 通过环境变量设置 API key（t.Setenv 自动清理）
	t.Setenv("ANTHROPIC_API_KEY", "sk-1234567890abcdef")
	cfg := config.DefaultConfigPtr()
	cfg.Model = "test"
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.checkConfig()

	for _, r := range results {
		if r.Name == "API Key" {
			// 不应包含完整 key
			if strings.Contains(r.Message, "sk-1234567890abcdef") {
				t.Errorf("API key 应脱敏，实际含完整 key: %q", r.Message)
			}
			// 应含脱敏标记 ***
			if !strings.Contains(r.Message, "***") {
				t.Errorf("API key 应含脱敏标记 '***'，实际 %q", r.Message)
			}
		}
	}
}

// TestDoctor_ClassifyAPIError 验证 API 错误分类覆盖所有类别。
func TestDoctor_ClassifyAPIError(t *testing.T) {
	tests := []struct {
		errMsg string
		want   string
	}{
		{"HTTP 403 Forbidden", "403 Forbidden"},
		{"401 Unauthorized", "401 Unauthorized"},
		{"429 Too Many Requests", "429 Rate Limited"},
		{"context deadline exceeded", "Timeout"},
		{"lookup example.com: no such host", "DNS resolution failed"},
		{"connection refused", "Connection refused"},
		{"model overloaded", "Model overloaded"},
		{"some unknown error", "Unknown error"},
	}
	for _, tt := range tests {
		got := classifyAPIError(&testError{msg: tt.errMsg})
		if !strings.Contains(got, tt.want) {
			t.Errorf("classifyAPIError(%q) 期望含 %q，实际 %q", tt.errMsg, tt.want, got)
		}
	}
}

// testError 是 error 接口的测试实现。
type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
