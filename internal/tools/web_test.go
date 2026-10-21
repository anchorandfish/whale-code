package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestStripHTMLTags 测试 HTML 标签移除功能
// 验证 script/style 内容被移除，HTML 标签被去除，文本内容被保留
// 注意：当前 stripHTMLTags implementation存在已知行为特征：
// - 关闭标签后添加空格
// - script/style 跳转后 continue 导致 i++ 多跳一位，可能影响后续标签状态
// - 闭合标签搜索区分大小写（</SCRIPT> 无法匹配 </script>）
func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "纯文本不变",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "移除简单标签",
			input: "<p>hello</p>",
			want:  "hello",
		},
		{
			name:  "移除嵌套标签",
			input: "<div><p>hello</p></div>",
			want:  "hello",
		},
		{
			name:  "移除script标签及内容",
			input: "<script>alert('xss')</script>hello",
			want:  "hello",
		},
		{
			name:  "移除style标签及内容",
			input: "<style>body{color:red}</style>hello",
			want:  "hello",
		},
		{
			name:  "保留标签外文本",
			input: "before<b>bold</b>after",
			want:  "before bold after", // 关闭标签后添加空格
		},
		{
			name:  "空字符串",
			input: "",
			want:  "",
		},
		{
			name:  "只有标签无文本",
			input: "<div></div>",
			want:  "",
		},
		{
			name:  "script标签大写",
			input: "<SCRIPT>var x=1;</SCRIPT>text",
			want:  "text",
		},
		{
			name:  "无script/style的普通标签",
			input: "<h1>Title</h1><p>Content</p>",
			want:  "Title  Content", // 每个关闭标签后添加空格，</h1> 和 <p> 开标签各产生空格
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripHTMLTags(tt.input)
			// 压缩连续空白后比较
			gotCompressed := strings.TrimSpace(got)
			wantCompressed := strings.TrimSpace(tt.want)
			if gotCompressed != wantCompressed {
				t.Errorf("stripHTMLTags() = %q, want %q", gotCompressed, wantCompressed)
			}
		})
	}
}

// TestWebFetch_InvalidURL 测试无效 URL 返回错误
// 验证当传入无效 URL 时，webFetchHandler 返回错误
func TestWebFetch_InvalidURL(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.webFetchHandler()

	tests := []struct {
		name  string
		input string
	}{
		{"空URL", `{"url":""}`},
		{"无效协议", `{"url":"ftp://invalid"}`},
		{"格式错误的URL", `{"url":"not a url at all"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler(context.Background(), json.RawMessage(tt.input))
			if err == nil {
				t.Error("expected返回错误，但得到了 nil")
			}
		})
	}
}

// TestWebFetch_Timeout test timeout处理
// 使用极短的超时时间，验证 webFetchHandler 能正确处理超时
func TestWebFetch_Timeout(t *testing.T) {
	// 创建一个延迟响应的 HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "should not see this")
	}))
	defer server.Close()

	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.webFetchHandler()

	// 使用极短超时的 context
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	input := fmt.Sprintf(`{"url":"%s"}`, server.URL)
	_, err := handler(ctx, json.RawMessage(input))
	if err == nil {
		t.Error("expected因超时返回错误，但得到了 nil")
	}
}

// TestWebSearch_NoAPIKey 测试无 API Key 时必须 fail-closed（返回 error）
//
// 契约变更（止血）：未配置 SEARCH_API_KEY 时**不得**把"我没有能力"编码成
// (配置提示文本, nil) —— 那会让工具层上报 IsError=false，模型把配置提示当成
// 一次成功的搜索结果，进而诱发幻觉/空转。失败必须走 error 通道。
func TestWebSearch_NoAPIKey(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")

	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.webSearchHandler()

	result, err := handler(context.Background(), json.RawMessage(`{"query":"test query"}`))
	if err == nil {
		t.Fatalf("未配置 SEARCH_API_KEY 时必须返回 error（不得伪装成成功），实际 result=%q, err=nil", result)
	}
	if result != "" {
		t.Errorf("失败路径不应同时返回结果文本，得到: %s", result)
	}
	if !strings.Contains(err.Error(), "SEARCH_API_KEY") {
		t.Errorf("错误信息应包含配置指引（SEARCH_API_KEY），得到: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "test query") {
		t.Errorf("错误信息应包含查询词，得到: %s", err.Error())
	}
}

// TestWebSearch_UpstreamFailure 测试上游检索失败时同样 fail-closed
//
// 同一处的第二类软失败：SEARCH_API_URL 不可用时返回 ("Search failed: ...", nil)。
// 用畸形 URL 使其在构造请求阶段即失败（离线可复现，不依赖网络）。
func TestWebSearch_UpstreamFailure(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "dummy-key-for-test")
	t.Setenv("SEARCH_API_URL", "://not-a-valid-url")

	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.webSearchHandler()

	result, err := handler(context.Background(), json.RawMessage(`{"query":"test query"}`))
	if err == nil {
		t.Fatalf("检索上游失败时必须返回 error（不得伪装成成功），实际 result=%q, err=nil", result)
	}
	if result != "" {
		t.Errorf("失败路径不应同时返回结果文本，得到: %s", result)
	}
}

// TestParseSearchResults 测试搜索结果解析（响应侧统一结构契约，S2′）
//
// ★ 2026-09-18 变更说明（B4 裁定）：本用例原先要求"未知 API 返回原始内容"（`{"custom":"data"}` ⇒
// 把 body 前 2000 字节当结果吐出去）。那个断言**正是**本次要推翻的旧行为：
// 它让 HTML 登录页、`{"code":401,"message":"Invalid API KEY"}` 这类报文以"成功"的身份进上下文，
// 模型会把它们读成检索结果（/ 同族幻觉入口）。
// 现语义：结构合规 ⇒ 放行（不看引擎名）；结构不合规 ⇒ error（fail-closed）。
func TestParseSearchResults(t *testing.T) {
	t.Run("Google搜索结果", func(t *testing.T) {
		googleJSON := `{
			"items": [
				{"title": "Go语言入门", "link": "https://golang.org", "snippet": "Go是一种开源编程语言"},
				{"title": "Go教程", "link": "https://example.com/go", "snippet": "学习Go编程"}
			]
		}`
		result, err := parseSearchResults([]byte(googleJSON), "https://www.googleapis.com/customsearch/v1")
		if err != nil {
			t.Fatalf("合规响应不得报错，得到: %v", err)
		}
		if !strings.Contains(result, "Go语言入门") {
			t.Error("expected结果包含标题 'Go语言入门'")
		}
		if !strings.Contains(result, "https://golang.org") {
			t.Error("expected结果包含链接")
		}
		if !strings.Contains(result, "Go是一种开源编程语言") {
			t.Error("expected结果包含摘要")
		}
	})

	t.Run("Brave搜索结果", func(t *testing.T) {
		braveJSON := `{
			"web": {
				"results": [
					{"title": "Rust语言", "url": "https://rust-lang.org", "description": "Rust是一种系统编程语言"},
					{"title": "Rust教程", "url": "https://example.com/rust", "description": "学习Rust编程"}
				]
			}
		}`
		result, err := parseSearchResults([]byte(braveJSON), "https://api.search.brave.com/res/v1/web/search")
		if err != nil {
			t.Fatalf("合规响应不得报错，得到: %v", err)
		}
		if !strings.Contains(result, "Rust语言") {
			t.Error("expected结果包含标题 'Rust语言'")
		}
		if !strings.Contains(result, "https://rust-lang.org") {
			t.Error("expected结果包含链接")
		}
		if !strings.Contains(result, "Rust是一种系统编程语言") {
			t.Error("expected结果包含描述")
		}
	})

	t.Run("空Google结果", func(t *testing.T) {
		emptyJSON := `{"items": []}`
		result, err := parseSearchResults([]byte(emptyJSON), "https://www.googleapis.com/customsearch/v1")
		if err != nil {
			t.Fatalf("空数组是合规契约的实例（0 条结果），不得报错，得到: %v", err)
		}
		if !strings.Contains(result, "No results found") {
			t.Errorf("expected空结果提示，得到: %s", result)
		}
	})

	t.Run("空Brave结果", func(t *testing.T) {
		emptyJSON := `{"web": {"results": []}}`
		result, err := parseSearchResults([]byte(emptyJSON), "https://api.search.brave.com/res/v1/web/search")
		if err != nil {
			t.Fatalf("空数组是合规契约的实例（0 条结果），不得报错，得到: %v", err)
		}
		if !strings.Contains(result, "No results found") {
			t.Errorf("expected空结果提示，得到: %s", result)
		}
	})

	t.Run("未知引擎但结构合规必须放行", func(t *testing.T) {
		// 博查（Bing 兼容信封）：引擎名不在任何白名单里，但结构合规 ⇒ 放行。
		bochaJSON := `{
			"code": 200,
			"log_id": "d71841ad20095f61",
			"data": {
				"webPages": {"value": [
					{"name": "阿里巴巴发布2024年ESG报告", "url": "https://www.alibabagroup.com/document-175",
					 "snippet": "阿里巴巴集团发布《2024财年环境、社会和治理(ESG)报告》..."}
				]}
			}
		}`
		result, err := parseSearchResults([]byte(bochaJSON), "https://unknown-engine.example.com/search")
		if err != nil {
			t.Fatalf("未知引擎但结构合规时必须放行，得到: %v", err)
		}
		if !strings.Contains(result, "阿里巴巴发布2024年ESG报告") {
			t.Errorf("应渲染出规范化后的标题，得到: %s", result)
		}
	})

	t.Run("结构性失败必须fail-closed而非吐原文", func(t *testing.T) {
		// ★ 本条是上面那段说明的判别器：改造前这三个样本**全部**以"成功"身份返回原文。
		cases := []struct {
			name string
			body string
		}{
			{"HTML汤", `<!DOCTYPE html><html><head><title>Sign in</title></head></html>`},
			{"错误信封", `{"custom": "data"}`},
			{"上游401信封", `{"code": 401, "message": "Invalid API KEY", "log_id": "c66aac17eab1bb7e"}`},
			{"有数组但元素非结果对象", `{"data": {"list": [{"foo": 1}, {"bar": 2}]}}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				result, err := parseSearchResults([]byte(tc.body), "https://unknown-api.example.com/search")
				if err == nil {
					t.Fatalf("结构不合规必须返回 error（不得把原文当检索结果），实际 result=%q", result)
				}
				if strings.Contains(err.Error(), "custom") || strings.Contains(err.Error(), "DOCTYPE") ||
					strings.Contains(err.Error(), "Invalid API KEY") {
					t.Errorf("错误文案不得回显响应体（可能是 HTML 汤或含凭据），得到: %v", err)
				}
				if !strings.Contains(err.Error(), "unknown-api.example.com") {
					t.Errorf("错误文案应含端点 host 便于归因，得到: %v", err)
				}
			})
		}
	})
}

// TestAgentHandler_InvalidType 测试无效的 agent_type 返回错误
// 验证传入不支持的 agent_type 时，agentHandler 返回明确的错误信息
func TestAgentHandler_InvalidType(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.agentHandler()

	tests := []struct {
		name      string
		agentType string
	}{
		{"无效类型unknown", "unknown"},
		{"无效类型admin", "admin"},
		{"无效类型root", "root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := fmt.Sprintf(`{"prompt":"test task","agent_type":"%s"}`, tt.agentType)
			_, err := handler(context.Background(), json.RawMessage(input))
			if err == nil {
				t.Error("expected返回错误，但得到了 nil")
			}
			// L2：文案与同类 CLI `unknown agent_type '...'` 对齐，
			// 并附上合法取值清单（fail-closed，不静默降级为 coder）。
			if !strings.Contains(err.Error(), "unknown agent_type") {
				t.Errorf("expected错误包含 'unknown agent_type'，得到: %v", err)
			}
			if !strings.Contains(err.Error(), tt.agentType) {
				t.Errorf("错误文案应回显非法取值 %q，得到: %v", tt.agentType, err)
			}
		})
	}
}

// TestTodoWriteHandler_Create 测试创建新的 todo 列表
// 验证 todoWriteHandler 能正确创建新的 todo 项并返回更新信息
func TestTodoWriteHandler_Create(t *testing.T) {
	// 使用临时目录作为 HOME，避免影响真实 User 数据
	// 跨平台：Windows 读 USERPROFILE，Linux/macOS 读 HOME，两个都设置以保证
	// os.UserHomeDir() 在所有平台返回 tmpDir。
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	originalUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tmpDir)
	os.Setenv("USERPROFILE", tmpDir)
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
	}()

	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.todoWriteHandler()

	input := `{
		"todos": [
			{"id": "1", "content": "implementation登录功能", "status": "pending", "priority": "high"},
			{"id": "2", "content": "编写unit test", "status": "in_progress", "priority": "medium"}
		]
	}`

	result, err := handler(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("不expected返回错误: %v", err)
	}
	if !strings.Contains(result, "Todo list updated") {
		t.Errorf("expected结果包含 'Todo list updated'，得到: %s", result)
	}
	if !strings.Contains(result, "2 items") {
		t.Errorf("expected结果包含 '2 items'，得到: %s", result)
	}
	if !strings.Contains(result, "1 active") {
		t.Errorf("expected结果包含 '1 active'（一个 in_progress），得到: %s", result)
	}

	// 验证文件已创建
	todoFile := filepath.Join(tmpDir, ".whale", "todos.json")
	if _, err := os.Stat(todoFile); os.IsNotExist(err) {
		t.Error("expected todo 文件已创建")
	}
}

// TestTodoWriteHandler_Update 测试更新已有的 todo 项
// 验证 todoWriteHandler 能正确合并更新已有的 todo 项
func TestTodoWriteHandler_Update(t *testing.T) {
	// 使用临时目录作为 HOME（跨平台：Windows 读 USERPROFILE，Linux/macOS 读 HOME）
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	originalUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tmpDir)
	os.Setenv("USERPROFILE", tmpDir)
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
	}()

	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.todoWriteHandler()

	// 先创建两个 todo
	createInput := `{
		"todos": [
			{"id": "1", "content": "任务一", "status": "pending", "priority": "high"},
			{"id": "2", "content": "任务二", "status": "pending", "priority": "medium"}
		]
	}`
	result, err := handler(context.Background(), json.RawMessage(createInput))
	if err != nil {
		t.Fatalf("创建时不expected返回错误: %v", err)
	}
	if !strings.Contains(result, "2 items") {
		t.Errorf("创建后expected 2 items，得到: %s", result)
	}

	// 更新第一个 todo 状态为 in_progress
	updateInput := `{
		"todos": [
			{"id": "1", "content": "任务一", "status": "in_progress", "priority": "high"}
		]
	}`
	result, err = handler(context.Background(), json.RawMessage(updateInput))
	if err != nil {
		t.Fatalf("更新时不expected返回错误: %v", err)
	}
	if !strings.Contains(result, "2 items") {
		t.Errorf("更新后expected仍有 2 items，得到: %s", result)
	}
	if !strings.Contains(result, "1 active") {
		t.Errorf("更新后expected 1 active，得到: %s", result)
	}

	// 完成第一个 todo（completed 的项会被移除）
	completeInput := `{
		"todos": [
			{"id": "1", "content": "任务一", "status": "completed", "priority": "high"}
		]
	}`
	result, err = handler(context.Background(), json.RawMessage(completeInput))
	if err != nil {
		t.Fatalf("完成时不expected返回错误: %v", err)
	}
	if !strings.Contains(result, "1 items") {
		t.Errorf("完成后expected 1 items（completed 被移除），得到: %s", result)
	}
}

// TestAskUserHandler_Options 测试带选项的User提问
// 验证 askUserHandler 在提供选项时返回包含选项信息的 JSON
func TestAskUserHandler_Options(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	t.Run("带选项", func(t *testing.T) {
		input := `{
			"question": "请选择部署环境",
			"options": [
				{"label": "开发环境", "value": "dev"},
				{"label": "生产环境", "value": "prod"}
			]
		}`

		result, err := handler(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatalf("不expected返回错误: %v", err)
		}

		// 验证返回的是有效的 JSON
		var askResult AskUserResult
		if err := json.Unmarshal([]byte(result), &askResult); err != nil {
			t.Fatalf("返回结果不是有效的 JSON: %v, 结果: %s", err, result)
		}
		if askResult.Question != "请选择部署环境" {
			t.Errorf("expected问题为 '请选择部署环境'，得到: %s", askResult.Question)
		}
		if len(askResult.Options) != 2 {
			t.Fatalf("expected 2 个选项，得到: %d", len(askResult.Options))
		}
		if askResult.Options[0].Label != "开发环境" || askResult.Options[0].Value != "dev" {
			t.Errorf("第一个选项不匹配，得到: %+v", askResult.Options[0])
		}
		if askResult.Options[1].Label != "生产环境" || askResult.Options[1].Value != "prod" {
			t.Errorf("第二个选项不匹配，得到: %+v", askResult.Options[1])
		}
	})

	t.Run("无选项", func(t *testing.T) {
		// T3 S1 后：无 options 时必须显式 allow_free_text=true，否则返回 error。
		// 旧用例仅传 question 会触发 "options is required when allow_free_text=false" 错误。
		input := `{"question": "这是一个简单问题", "allow_free_text": true, "category": "dialogue-clarify"}`

		result, err := handler(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatalf("不expected返回错误: %v", err)
		}
		if !strings.Contains(result, "这是一个简单问题") {
			t.Errorf("expected结果包含问题文本，得到: %s", result)
		}
	})
}

// TestAskUserHandler_ValidInput 验证合法输入返回正确 AskUserResult（含新字段）。
// 覆盖 T3 S1 新增的 allow_free_text + context 字段。
func TestAskUserHandler_ValidInput(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	input := `{
		"question": "认证方式选哪种？",
		"options": [
			{"label": "JWT", "value": "jwt", "description": "无状态 Token"},
			{"label": "Session", "value": "session", "description": "服务端存储"}
		],
		"default_idx": 0,
		"category": "dialogue-clarify",
		"allow_free_text": false,
		"context": {"trace_id": "abc123"}
	}`
	result, err := handler(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("合法输入不应返回错误: %v", err)
	}
	var askResult AskUserResult
	if err := json.Unmarshal([]byte(result), &askResult); err != nil {
		t.Fatalf("返回结果不是有效 JSON: %v, 结果: %s", err, result)
	}
	if askResult.Category != "dialogue-clarify" {
		t.Errorf("Category mismatch: got %q want dialogue-clarify", askResult.Category)
	}
	if askResult.DefaultIdx != 0 {
		t.Errorf("DefaultIdx mismatch: got %d want 0", askResult.DefaultIdx)
	}
	if askResult.AllowFreeText != false {
		t.Errorf("AllowFreeText mismatch: got %v want false", askResult.AllowFreeText)
	}
	if askResult.Options[0].Description != "无状态 Token" {
		t.Errorf("Option.Description mismatch: got %q want '无状态 Token'", askResult.Options[0].Description)
	}
	if askResult.Context["trace_id"] != "abc123" {
		t.Errorf("Context mismatch: %+v", askResult.Context)
	}
}

// TestAskUserHandler_EmptyQuestion 验证 question 为空返回 error。
func TestAskUserHandler_EmptyQuestion(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	input := `{"question": "", "category": "dialogue-clarify", "allow_free_text": true}`
	_, err := handler(context.Background(), json.RawMessage(input))
	if err == nil {
		t.Fatal("空 question 应返回 error")
	}
	if !strings.Contains(err.Error(), "question is required") {
		t.Errorf("错误信息应包含 'question is required'，得到: %v", err)
	}
}

// TestAskUserHandler_InvalidCategory 验证非法 category 降级为 dialogue-clarify。
func TestAskUserHandler_InvalidCategory(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	input := `{
		"question": "测试问题",
		"options": [{"label": "A", "value": "a"}, {"label": "B", "value": "b"}],
		"category": "admin-bypass"
	}`
	result, err := handler(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("不应返回错误（非法 category 应降级）: %v", err)
	}
	var askResult AskUserResult
	if err := json.Unmarshal([]byte(result), &askResult); err != nil {
		t.Fatalf("返回结果不是有效 JSON: %v", err)
	}
	if askResult.Category != "dialogue-clarify" {
		t.Errorf("非法 category 应降级为 dialogue-clarify，得到: %q", askResult.Category)
	}
}

// TestAskUserHandler_DefaultIdxOutOfRange 验证 default_idx 越界降级为 -1。
func TestAskUserHandler_DefaultIdxOutOfRange(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	tests := []struct {
		name       string
		defaultIdx int
	}{
		{"负数", -5},
		{"超出上限", 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := fmt.Sprintf(`{
				"question": "测试",
				"options": [{"label": "A", "value": "a"}, {"label": "B", "value": "b"}],
				"default_idx": %d,
				"category": "dialogue-clarify"
			}`, tt.defaultIdx)
			result, err := handler(context.Background(), json.RawMessage(input))
			if err != nil {
				t.Fatalf("不应返回错误: %v", err)
			}
			var askResult AskUserResult
			if err := json.Unmarshal([]byte(result), &askResult); err != nil {
				t.Fatalf("返回结果不是有效 JSON: %v", err)
			}
			if askResult.DefaultIdx != -1 {
				t.Errorf("越界 default_idx 应降级为 -1，得到: %d", askResult.DefaultIdx)
			}
		})
	}
}

// TestAskUserHandler_LongQuestionTruncation 验证 question >500 字符截断。
func TestAskUserHandler_LongQuestionTruncation(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	longQuestion := strings.Repeat("a", 600)
	input := fmt.Sprintf(`{
		"question": "%s",
		"options": [{"label": "A", "value": "a"}, {"label": "B", "value": "b"}],
		"category": "dialogue-clarify"
	}`, longQuestion)
	result, err := handler(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	var askResult AskUserResult
	if err := json.Unmarshal([]byte(result), &askResult); err != nil {
		t.Fatalf("返回结果不是有效 JSON: %v", err)
	}
	if len(askResult.Question) > 503 {
		t.Errorf("截断后 question 长度应 ≤503（500 + '...'），得到: %d", len(askResult.Question))
	}
	if !strings.HasSuffix(askResult.Question, "...") {
		t.Errorf("截断后应以 '...' 结尾")
	}
}

// TestAskUserHandler_NoOptionsNoFreeText 验证 allow_free_text=false 且 options 空返回 error。
func TestAskUserHandler_NoOptionsNoFreeText(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	input := `{"question": "测试", "category": "dialogue-clarify", "allow_free_text": false}`
	_, err := handler(context.Background(), json.RawMessage(input))
	if err == nil {
		t.Fatal("allow_free_text=false 且 options 空应返回 error")
	}
	if !strings.Contains(err.Error(), "options is required when allow_free_text=false") {
		t.Errorf("错误信息不匹配，得到: %v", err)
	}
}

// ============================================================================
// 新增 Web 工具测试
// ============================================================================

// TestWebFetch_Normal 测试使用 httptest mockserver进行正常 fetch
func TestWebFetch_Normal(t *testing.T) {
	// 创建mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h1>Hello World</h1><p>Test content</p></body></html>")
	}))
	defer server.Close()

	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)
	e.AllowLocalhostForTest = true
	defer func() { e.AllowLocalhostForTest = false }()

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "WebFetch",
		Input: mustMarshal(map[string]string{"url": server.URL}),
	})

	if result.IsError {
		t.Fatalf("WebFetch 正常请求失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Hello World") {
		t.Errorf("结果should contain 'Hello World'，actual: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Test content") {
		t.Errorf("结果should contain 'Test content'，actual: %s", result.Output)
	}
}

// TestWebFetch_LargeContent 测试响应超过 1MB 时被截断
func TestWebFetch_LargeContent(t *testing.T) {
	// 创建返回大内容的mockserver
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// 生成超过 1MB 的内容
		largeContent := strings.Repeat("x", 2*1024*1024) // 2MB
		fmt.Fprint(w, largeContent)
	}))
	defer server.Close()

	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)
	e.AllowLocalhostForTest = true
	defer func() { e.AllowLocalhostForTest = false }()

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "WebFetch",
		Input: mustMarshal(map[string]string{"url": server.URL}),
	})

	if result.IsError {
		t.Fatalf("WebFetch 大内容请求失败: %s", result.Output)
	}
	// 输出应被截断到合理长度
	if len(result.Output) > 2*1024*1024 {
		t.Errorf("大内容应被截断，actual长度: %d", len(result.Output))
	}
}

// TestWebSearch_Google 测试 Google 搜索（无 API Key 时跳过）
func TestWebSearch_Google(t *testing.T) {
	apiKey := os.Getenv("GOOGLE_SEARCH_API_KEY")
	cx := os.Getenv("GOOGLE_SEARCH_CX")
	if apiKey == "" || cx == "" {
		t.Skip("跳过：未设置 GOOGLE_SEARCH_API_KEY 或 GOOGLE_SEARCH_CX environment variable")
	}

	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "WebSearch",
		Input: mustMarshal(map[string]string{"query": "Go programming language"}),
	})

	if result.IsError {
		t.Fatalf("Google 搜索失败: %s", result.Output)
	}
}

// TestWebSearch_Brave 测试 Brave 搜索（无 API Key 时跳过）
func TestWebSearch_Brave(t *testing.T) {
	apiKey := os.Getenv("BRAVE_SEARCH_API_KEY")
	if apiKey == "" {
		t.Skip("跳过：未设置 BRAVE_SEARCH_API_KEY environment variable")
	}

	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "WebSearch",
		Input: mustMarshal(map[string]string{"query": "Rust programming language"}),
	})

	if result.IsError {
		t.Fatalf("Brave 搜索失败: %s", result.Output)
	}
}

// TestWebSearch_SearXNG 测试 SearXNG 搜索（无 URL 时跳过）
func TestWebSearch_SearXNG(t *testing.T) {
	searxngURL := os.Getenv("SEARXNG_URL")
	if searxngURL == "" {
		t.Skip("跳过：未设置 SEARXNG_URL environment variable")
	}

	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "WebSearch",
		Input: mustMarshal(map[string]string{"query": "Python programming"}),
	})

	if result.IsError {
		t.Fatalf("SearXNG 搜索失败: %s", result.Output)
	}
}

// ============================================================================
// 止血判据（生产入口）：WebSearch 失败必须上报 IsError=true
// ============================================================================
//
// 为什么不能用"直接调 handler"当判据：handler 返回 (string, error) 只是边界契约，
// 真正决定模型看到什么的是 ExecuteBatch → Execute → ToolPipeline 的映射
// （executor.go:730-769：err != nil ⇒ Result{IsError:true}）。
// 本判据走 internal/tui/runner_core.go:1496 的同一条入口，断言端到端 Result.IsError。

func newWebSearchProdExecutor(t *testing.T) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), t.TempDir())
	e.InitPipeline(
		hook.NewManager(t.TempDir()),
		permission.NewPolicy(permission.LevelDangerFullAccess),
		t.TempDir(),
	)
	return e
}

// TestWebSearch_NoAPIKey_IsErrorThroughProductionPipeline 无 Key 时必须 IsError=true。
// 反例注入（应打红）：把 web.go 的 `return ..., fmt.Errorf(...)` 改回 `return ..., nil`。
func TestWebSearch_NoAPIKey_IsErrorThroughProductionPipeline(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")

	e := newWebSearchProdExecutor(t)
	results := e.ExecuteBatch(context.Background(), []Call{{
		ID:    "ws-nokey-1",
		Name:  "WebSearch",
		Input: `{"query":"golang generics"}`,
	}})

	if len(results) != 1 {
		t.Fatalf("期望 1 个结果，得到 %d", len(results))
	}
	r := results[0]
	if !r.IsError {
		t.Fatalf("未配置 SEARCH_API_KEY 时 Result.IsError 必须为 true（否则模型会把配置提示当成成功结果）\n实际: IsError=%v\nOutput=%s", r.IsError, r.Output)
	}
	if !strings.Contains(r.Output, "SEARCH_API_KEY") {
		t.Errorf("错误输出应保留可执行的配置指引，得到: %s", r.Output)
	}
	if !strings.Contains(r.Output, "golang generics") {
		t.Errorf("错误输出应保留查询词，得到: %s", r.Output)
	}
}

// TestWebSearch_UpstreamFailure_IsErrorThroughProductionPipeline 上游失败也必须 IsError=true。
func TestWebSearch_UpstreamFailure_IsErrorThroughProductionPipeline(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "dummy-key-for-test")
	t.Setenv("SEARCH_API_URL", "://not-a-valid-url")

	e := newWebSearchProdExecutor(t)
	results := e.ExecuteBatch(context.Background(), []Call{{
		ID:    "ws-upstream-1",
		Name:  "WebSearch",
		Input: `{"query":"golang generics"}`,
	}})

	if len(results) != 1 {
		t.Fatalf("期望 1 个结果，得到 %d", len(results))
	}
	r := results[0]
	if !r.IsError {
		t.Fatalf("检索上游失败时 Result.IsError 必须为 true，实际: IsError=%v\nOutput=%s", r.IsError, r.Output)
	}
}

// ============================================================================
// 判据：无 Key 提示不得推荐"自建走回环"的方案
// ============================================================================
//
// 判据来源：检索路径的 transport 由 isIPBlocked 把守，覆盖 Loopback/Private/LinkLocal ⇒
// 提示里曾推荐的 "SearXNG (self-hosted)"（典型部署在 127.0.0.1 或内网）**必被自身 SSRF 拦**，
// 属于"建议不可执行"（文档 §3）。
//
// ★ 2026-09-18 更新（S2′）：transport 的 allowLocalhost 已由硬编码 false 改为
// `e.AllowLocalhostForTest`（生产零值仍是 false，边界不变；改的是"离线判据不能被自身 SSRF 拦成 SKIP"）。
// 本判据的**断言与理由都不变**：自建 SearXNG 的典型部署仍是回环/内网，仍不可执行。
// 只是理由不再挂在"硬编码"这个已消失的实现细节上 —— 换实现不该让判据的理由失真。
// 另注：SEARCH_API_FORMAT 的合法取值里确实有 searxng（公网实例可用，且无需 key），
// 那一份清单只在**报错文案**里出现；本判据盯的是"无 Key 时的配置指引"，它只许列本机可达方案。
//
// 反例注入（应打红）：把 "SearXNG (self-hosted): https://github.com/searxng/searxng"
// 那一行加回 web.go 的配置提示文案。
func TestWebSearch_NoAPIKey_HintMustNotRecommendUnreachableOption(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")

	e := newWebSearchProdExecutor(t)
	results := e.ExecuteBatch(context.Background(), []Call{{
		ID:    "ws-hint-1",
		Name:  "WebSearch",
		Input: `{"query":"golang generics"}`,
	}})

	if len(results) != 1 {
		t.Fatalf("期望 1 个结果，得到 %d", len(results))
	}
	out := results[0].Output
	if strings.Contains(strings.ToLower(out), "searxng") || strings.Contains(out, "self-hosted") {
		t.Fatalf("无 Key 提示不得推荐自建/回环方案：检索路径的 transport 由 isIPBlocked 把守，"+
			"环回与内网地址会被自身 SSRF 拦截 ⇒ 该建议不可执行\n实际: %s", out)
	}
	if !strings.Contains(out, "Google Programmable Search") {
		t.Errorf("应保留可执行方案（Google Programmable Search），得到: %s", out)
	}
}

// TestAskUserHandler_OptionsCountEnforced 验证「有选项时必须 2-3 个」约束（对齐 同类 CLI）。
// 背景（2026-09-21 · AskUser 对齐 同类 CLI request_user_input）：强制互斥选项 2-3 个，
// 逼模型先想清楚「到底有几条路」，而不是丢一个开放式/单选项问题把认知负担转嫁给用户。
// 兼容边界：allow_free_text=true 且 options 空 = 纯开放式提问，仍保留（编排器专用）。
// 本判据只约束「提供了 options 时」的数量，不约束纯开放式。
func TestAskUserHandler_OptionsCountEnforced(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	handler := executor.askUserHandler()

	t.Run("单选项应拒绝", func(t *testing.T) {
		input := `{"question":"测试","options":[{"label":"A","value":"a"}],"category":"dialogue-clarify"}`
		_, err := handler(context.Background(), json.RawMessage(input))
		if err == nil {
			t.Fatal("单选项应返回 error（须 2-3 个）")
		}
		if !strings.Contains(err.Error(), "2-3") {
			t.Errorf("错误信息应提示 2-3 个选项，得到: %v", err)
		}
	})

	t.Run("四选项应拒绝", func(t *testing.T) {
		input := `{"question":"测试","options":[{"label":"A","value":"a"},{"label":"B","value":"b"},{"label":"C","value":"c"},{"label":"D","value":"d"}],"category":"dialogue-clarify"}`
		_, err := handler(context.Background(), json.RawMessage(input))
		if err == nil {
			t.Fatal("四选项应返回 error（须 2-3 个）")
		}
		if !strings.Contains(err.Error(), "2-3") {
			t.Errorf("错误信息应提示 2-3 个选项，得到: %v", err)
		}
	})

	t.Run("两选项应通过", func(t *testing.T) {
		input := `{"question":"测试","options":[{"label":"A","value":"a"},{"label":"B","value":"b"}],"category":"dialogue-clarify"}`
		_, err := handler(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatalf("两选项应通过，得到错误: %v", err)
		}
	})

	t.Run("三选项应通过", func(t *testing.T) {
		input := `{"question":"测试","options":[{"label":"A","value":"a"},{"label":"B","value":"b"},{"label":"C","value":"c"}],"category":"dialogue-clarify"}`
		_, err := handler(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatalf("三选项应通过，得到错误: %v", err)
		}
	})

	t.Run("纯开放式仍保留", func(t *testing.T) {
		// allow_free_text=true 且无 options = 纯开放式，兼容编排器专用，仍应通过。
		input := `{"question":"请描述","allow_free_text":true,"category":"dialogue-clarify"}`
		_, err := handler(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatalf("纯开放式应保留（allow_free_text=true 且无 options），得到错误: %v", err)
		}
	})
}
