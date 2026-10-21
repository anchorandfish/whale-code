package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// mockSession 用于测试的会话读取器
type mockSession struct {
	messages []session.Message
	name     string
}

func (m *mockSession) GetMessages() []session.Message {
	return m.messages
}

func (m *mockSession) GetName() string {
	return m.name
}

// mockRedactor 用于测试的脱敏器（计数用）
type mockRedactor struct {
	count int
}

func (r *mockRedactor) Scrub(text string) (string, int) {
	r.count++
	return text + "[SCRUBBED]", 1
}

// testMessages 返回测试用消息列表
func testMessages() []session.Message {
	return []session.Message{
		{
			Role: session.RoleUser,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "你好，请帮我写一段代码 api_key=sk-test123456789012345678901234567890"},
			},
		},
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "好的，这是代码："},
				{Type: "thinking", Text: "思考过程：需要生成函数"},
			},
		},
		{
			Role: session.RoleTool,
			Blocks: []session.ContentBlock{
				{Type: "tool_result", ToolName: "write_file", Output: "文件写入成功 password=secret"},
			},
		},
		{
			Role:       session.RoleUser,
			Tombstoned: true,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "这是已删除的消息"},
			},
		},
	}
}

// TestExport_Markdown_Basic 测试基本 Markdown 导出
func TestExport_Markdown_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	msgs := testMessages()
	sess := &mockSession{messages: msgs, name: "test-session"}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format:     FmtMarkdown,
		OutputPath: filepath.Join(tmpDir, "test.md"),
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if result.Format != FmtMarkdown {
		t.Errorf("格式错误，期望 markdown，实际：%s", result.Format)
	}
	if result.MessageCount != 3 {
		t.Errorf("消息数错误，期望 3（跳过 tombstoned），实际：%d", result.MessageCount)
	}
	if result.Redacted == 0 {
		t.Errorf("脱敏数应为正数，实际：%d", result.Redacted)
	}

	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("读取导出文件失败: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "# Whale Code 导出") {
		t.Errorf("缺少导出标题")
	}
	if !strings.Contains(content, "## 用户") {
		t.Errorf("缺少用户消息")
	}
	if !strings.Contains(content, "## 助手") {
		t.Errorf("缺少助手消息")
	}
	if strings.Contains(content, "已删除的消息") {
		t.Errorf("Tombstoned 消息不应被导出")
	}
	if strings.Contains(content, "sk-test123456") {
		t.Errorf("API Key 明文不应出现在导出中")
	}
}

// TestExport_JSON_DefaultSkipMeta 测试 JSON 默认跳过 Metadata
func TestExport_JSON_DefaultSkipMeta(t *testing.T) {
	tmpDir := t.TempDir()
	msgs := testMessages()
	msgs[0].Metadata = map[string]interface{}{"key": "value"}
	msgs[0].Tags = []string{"test"}
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format:     FmtJSON,
		OutputPath: filepath.Join(tmpDir, "test.json"),
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("读取导出文件失败: %v", err)
	}

	var root exportJSONRoot
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	if root.Meta.MessageCount != 3 {
		t.Errorf("消息数错误，期望 3，实际：%d", root.Meta.MessageCount)
	}
	if len(root.Messages) == 0 {
		t.Fatalf("消息列表为空")
	}
	if root.Messages[0].Metadata != nil {
		t.Errorf("默认模式下 Metadata 应为 nil")
	}
	if len(root.Messages[0].Tags) != 0 {
		t.Errorf("默认模式下 Tags 应为空")
	}
}

// TestExport_JSON_FullMeta 测试 --full 保留 Metadata
func TestExport_JSON_FullMeta(t *testing.T) {
	tmpDir := t.TempDir()
	msgs := testMessages()
	msgs[0].Metadata = map[string]interface{}{"key": "value"}
	msgs[0].Tags = []string{"test"}
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format:     FmtJSON,
		FullMeta:   true,
		OutputPath: filepath.Join(tmpDir, "test_full.json"),
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("读取导出文件失败: %v", err)
	}

	var root exportJSONRoot
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	if root.Messages[0].Metadata == nil {
		t.Errorf("fullMeta 模式下 Metadata 不应为 nil")
	}
	if len(root.Messages[0].Tags) == 0 {
		t.Errorf("fullMeta 模式下 Tags 不应为空")
	}
}

// TestExport_UserOnly 测试仅用户消息导出
func TestExport_UserOnly(t *testing.T) {
	tmpDir := t.TempDir()
	msgs := testMessages()
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format:     FmtMarkdown,
		UserOnly:   true,
		OutputPath: filepath.Join(tmpDir, "user_only.md"),
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if result.MessageCount != 1 {
		t.Errorf("UserOnly 模式消息数应为 1，实际：%d", result.MessageCount)
	}

	data, _ := os.ReadFile(result.Path)
	content := string(data)
	if strings.Contains(content, "## 助手") {
		t.Errorf("UserOnly 模式不应包含助手消息")
	}
	if strings.Contains(content, "工具结果") {
		t.Errorf("UserOnly 模式不应包含工具消息")
	}
}

// TestExport_SkipTombstoned 测试跳过 Tombstoned 消息
func TestExport_SkipTombstoned(t *testing.T) {
	tmpDir := t.TempDir()
	msgs := testMessages()
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format:     FmtMarkdown,
		OutputPath: filepath.Join(tmpDir, "skip_tombstone.md"),
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if result.MessageCount != 3 {
		t.Errorf("应跳过 tombstoned 消息，期望 3 条，实际：%d", result.MessageCount)
	}
}

// TestExport_RedactedCount 测试脱敏计数正确
func TestExport_RedactedCount(t *testing.T) {
	tmpDir := t.TempDir()
	msgs := []session.Message{
		{
			Role: session.RoleUser,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "api_key=sk-test123456789012345678901234567890 password=secret123"},
			},
		},
	}
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format:     FmtMarkdown,
		OutputPath: filepath.Join(tmpDir, "redacted_count.md"),
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if result.Redacted < 2 {
		t.Errorf("至少应脱敏 2 处（api_key + password），实际：%d", result.Redacted)
	}
}

// TestExport_SanitizeFilename 测试恶意文件名被清理
func TestExport_SanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal-session", "normal-session"},
		{"../../etc/passwd", "etc_passwd"},
		{"hello world!", "hello_world"},
		{"<script>alert(1)</script>", "script_alert_1_script"},
		{"", ""},
		{"a/b\\c:d*e?f\"g<h>i|j", "a_b_c_d_e_f_g_h_i_j"},
	}

	for _, tt := range tests {
		result := sanitizeFilename(tt.input)
		if result != tt.expected {
			t.Errorf("sanitizeFilename(%q) = %q，期望 %q", tt.input, result, tt.expected)
		}
		if strings.ContainsAny(result, "/\\:*?\"<>| ") {
			t.Errorf("sanitizeFilename(%q) 结果包含不安全字符: %q", tt.input, result)
		}
	}
}

// TestExport_HTML_Supported 测试 HTML 格式导出成功（M23-04 已实现 HTML 导出，原 NotSupported 测试已过期）
func TestExport_HTML_Supported(t *testing.T) {
	tmpDir := t.TempDir()
	// 漏网补注：默认导出路径经 whalehome.Dir() 解析后，
	// 本用例没注入 WHALE_HOME ⇒ 会写真实 ~/.whale/exports（本机外层沙箱实测 rename EPERM 打红）。
	// 与下方 TestExport_DefaultPath 同款注入。
	t.Setenv("WHALE_HOME", filepath.Join(tmpDir, ".whale"))
	msgs := testMessages()
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format: FmtHTML,
	})
	if err != nil {
		t.Fatalf("HTML 导出应成功，实际失败：%v", err)
	}
	if result == nil {
		t.Fatal("导出结果不应为 nil")
	}
	if result.Format != FmtHTML {
		t.Errorf("格式应为 html，实际：%s", result.Format)
	}
	if !strings.HasSuffix(result.Path, ".html") {
		t.Errorf("导出路径应以 .html 结尾，实际：%s", result.Path)
	}
}

// TestExport_DefaultPath 测试默认导出路径
func TestExport_DefaultPath(t *testing.T) {
	tmpDir := t.TempDir()
	// 默认导出路径从 whalehome.Dir() 解析，测试经 WHALE_HOME 注入
	t.Setenv("WHALE_HOME", filepath.Join(tmpDir, ".whale"))
	msgs := testMessages()
	sess := &mockSession{messages: msgs, name: "我的会话"}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	result, err := exporter.Export(ExportOption{
		Format: FmtMarkdown,
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	expectedDir := filepath.Join(tmpDir, ".whale", "exports")
	if !strings.HasPrefix(result.Path, expectedDir) {
		t.Errorf("默认路径应在 %s 下，实际：%s", expectedDir, result.Path)
	}
	if !strings.HasSuffix(result.Path, ".md") {
		t.Errorf("默认路径应以 .md 结尾，实际：%s", result.Path)
	}
}

// TestExport_FilePermission 测试导出文件权限
//
// Windows 上 os.Chmod 仅支持设置只读属性，不支持 Unix 权限位，
// 因此该测试仅在非 Windows 平台运行。
func TestExport_FilePermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不支持 Unix 权限位，跳过权限测试")
	}
	tmpDir := t.TempDir()
	msgs := testMessages()
	sess := &mockSession{messages: msgs}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	outPath := filepath.Join(tmpDir, "perm_test.md")
	_, err := exporter.Export(ExportOption{
		Format:     FmtMarkdown,
		OutputPath: outPath,
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		t.Errorf("文件权限应限制为所有者，实际：%o", perm)
	}
}

// TestExport_EmptySession 测试空会话导出不 panic 且生成有效文件
//
// 边界 case：消息列表为空时，导出应成功并生成包含标题的空内容文件。
func TestExport_EmptySession(t *testing.T) {
	tmpDir := t.TempDir()
	sess := &mockSession{messages: []session.Message{}, name: "empty"}
	redactor := &DefaultRedactor{}
	exporter := NewExporter(sess, redactor, tmpDir)

	outPath := filepath.Join(tmpDir, "empty.md")
	result, err := exporter.Export(ExportOption{
		Format:     FmtMarkdown,
		OutputPath: outPath,
	})
	if err != nil {
		t.Fatalf("空会话导出应成功，实际失败: %v", err)
	}
	if result.MessageCount != 0 {
		t.Errorf("空会话消息数应为 0，实际：%d", result.MessageCount)
	}
	if result.Redacted != 0 {
		t.Errorf("空会话脱敏数应为 0，实际：%d", result.Redacted)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("读取导出文件失败: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "# Whale Code 导出") {
		t.Errorf("空会话导出仍应包含标题")
	}
	if strings.Contains(content, "## 用户") || strings.Contains(content, "## 助手") {
		t.Errorf("空会话导出不应包含消息内容")
	}
}

// TestRedact_CopyScenario 测试 /copy 场景下的脱敏
//
// 验证 Redactor.Scrub 对包含密钥的文本执行脱敏，
// 确保 /copy 复制到剪贴板的内容不含明文密钥。
func TestRedact_CopyScenario(t *testing.T) {
	r := &DefaultRedactor{}

	// 模拟助手回复中包含敏感信息
	texts := []string{
		"这是代码示例，API Key 为 sk-ant-api03-1234567890abcdef1234567890abcdef",
		"配置：password=mypassword123，请妥善保管",
		"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
	}

	for _, text := range texts {
		scrubbed, count := r.Scrub(text)
		if count == 0 {
			t.Errorf("文本应被脱敏，但未检测到敏感信息: %s", text)
		}
		if scrubbed == text {
			t.Errorf("文本未被修改，脱敏未生效: %s", text)
		}
		if strings.Contains(scrubbed, "sk-ant-api03") {
			t.Errorf("API Key 明文不应出现在脱敏结果中: %s", scrubbed)
		}
		if strings.Contains(scrubbed, "mypassword123") {
			t.Errorf("密码明文不应出现在脱敏结果中: %s", scrubbed)
		}
		if strings.Contains(scrubbed, "eyJhbGci") {
			t.Errorf("JWT 明文不应出现在脱敏结果中: %s", scrubbed)
		}
		if !strings.Contains(scrubbed, "[REDACTED]") {
			t.Errorf("脱敏结果应包含 [REDACTED]: %s", scrubbed)
		}
	}
}
