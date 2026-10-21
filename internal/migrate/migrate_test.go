package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/testutil"
)

// TestDetectCodexConfig_NotExists 验证检测不存在的 codex 配置目录。
// 期望：exists=false, configFile=""。
func TestDetectCodexConfig_NotExists(t *testing.T) {
	exists, configFile := detectCodexConfig("/nonexistent/.codex")
	if exists {
		t.Errorf("不存在的目录应返回 exists=false，实际 true")
	}
	if configFile != "" {
		t.Errorf("不存在的目录应返回 configFile=''，实际 %q", configFile)
	}
}

// TestDetectCodexConfig_Exists 验证检测存在的 codex 配置目录（含 config.toml）。
// 期望：exists=true, configFile 非空且含 config.toml 路径。
func TestDetectCodexConfig_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("创建 codex 目录失败: %v", err)
	}
	// 写入空 config.toml
	configFile := filepath.Join(codexDir, codexConfigPath)
	if err := os.WriteFile(configFile, []byte("# codex config"), 0644); err != nil {
		t.Fatalf("写入 config.toml 失败: %v", err)
	}

	exists, gotConfigFile := detectCodexConfig(codexDir)
	if !exists {
		t.Errorf("存在的目录应返回 exists=true，实际 false")
	}
	if gotConfigFile == "" {
		t.Errorf("存在的目录 + config.toml 应返回非空 configFile，实际空")
	}
	if !strings.HasSuffix(gotConfigFile, codexConfigPath) {
		t.Errorf("configFile 应以 %s 结尾，实际 %q", codexConfigPath, gotConfigFile)
	}
}

// TestDetectCodexConfig_DirOnlyNoConfigFile 验证目录存在但 config.toml 不存在的场景。
// 期望：exists=true（视为已配置，可能仅用环境变量），configFile=""。
func TestDetectCodexConfig_DirOnlyNoConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("创建 codex 目录失败: %v", err)
	}

	exists, configFile := detectCodexConfig(codexDir)
	if !exists {
		t.Errorf("目录存在时应返回 exists=true，实际 false")
	}
	if configFile != "" {
		t.Errorf("config.toml 不存在时应返回 configFile=''，实际 %q", configFile)
	}
}

// TestDetectClaudeCodeConfig_NotExists 验证检测不存在的 claude-code 配置目录。
// 期望：exists=false, configFile=""。
func TestDetectClaudeCodeConfig_NotExists(t *testing.T) {
	exists, configFile := detectClaudeCodeConfig("/nonexistent/.claude")
	if exists {
		t.Errorf("不存在的目录应返回 exists=false，实际 true")
	}
	if configFile != "" {
		t.Errorf("不存在的目录应返回 configFile=''，实际 %q", configFile)
	}
}

// TestDetectClaudeCodeConfig_Exists 验证检测存在的 claude-code 配置目录（含 settings.json）。
// 期望：exists=true, configFile 非空且含 settings.json 路径。
func TestDetectClaudeCodeConfig_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("创建 claude 目录失败: %v", err)
	}
	configFile := filepath.Join(claudeDir, claudeCodeConfigPath)
	if err := os.WriteFile(configFile, []byte(`{}`), 0644); err != nil {
		t.Fatalf("写入 settings.json 失败: %v", err)
	}

	exists, gotConfigFile := detectClaudeCodeConfig(claudeDir)
	if !exists {
		t.Errorf("存在的目录应返回 exists=true，实际 false")
	}
	if gotConfigFile == "" {
		t.Errorf("存在的目录 + settings.json 应返回非空 configFile，实际空")
	}
	if !strings.HasSuffix(gotConfigFile, claudeCodeConfigPath) {
		t.Errorf("configFile 应以 %s 结尾，实际 %q", claudeCodeConfigPath, gotConfigFile)
	}
}

// TestParseCodexAPIKey 验证从 codex config.toml 解析 api_key 字段。
// 覆盖：双引号、单引号、注释行、空文件。
func TestParseCodexAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "double_quote",
			content: `api_key = "sk-test-12345"`,
			want:    "sk-test-12345",
		},
		{
			name:    "single_quote",
			content: `api_key = 'sk-test-67890'`,
			want:    "sk-test-67890",
		},
		{
			name:    "with_comment",
			content: "# comment\napi_key = \"sk-real-key\"\nmodel = \"gpt-4\"",
			want:    "sk-real-key",
		},
		{
			name:    "no_api_key",
			content: "model = \"gpt-4\"\nprovider = \"openai\"",
			want:    "",
		},
		{
			name:    "empty_file",
			content: "",
			want:    "",
		},
		{
			name:    "commented_api_key",
			content: "# api_key = \"sk-should-not-match\"",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configFile := filepath.Join(tmpDir, "config.toml")
			if err := os.WriteFile(configFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("写入测试文件失败: %v", err)
			}
			got := parseCodexAPIKey(configFile)
			if got != tt.want {
				t.Errorf("parseCodexAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseClaudeCodeAPIKey 验证从 claude-code settings.json 解析 apiKey 字段。
// 覆盖：正常 JSON、无 apiKey 字段、空文件。
func TestParseClaudeCodeAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "with_api_key",
			content: `{"apiKey": "sk-ant-test123"}`,
			want:    "sk-ant-test123",
		},
		{
			name:    "no_api_key",
			content: `{"theme": "dark", "model": "claude-3"}`,
			want:    "",
		},
		{
			name:    "empty_api_key",
			content: `{"apiKey": ""}`,
			want:    "",
		},
		{
			name:    "invalid_json",
			content: `{invalid json`,
			want:    "",
		},
		{
			name:    "empty_file",
			content: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configFile := filepath.Join(tmpDir, "settings.json")
			if err := os.WriteFile(configFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("写入测试文件失败: %v", err)
			}
			got := parseClaudeCodeAPIKey(configFile)
			if got != tt.want {
				t.Errorf("parseClaudeCodeAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMaskKey 验证 API Key 脱敏逻辑。
// 覆盖：长 key、短 key（≤8）、空 key。
func TestMaskKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-1234567890abcdef", "sk-1***cdef"},
		{"short", "***"},
		{"12345678", "***"}, // 长度 = 8，应返回 ***
		{"123456789", "1234***6789"},
		{"", "***"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := maskKey(tt.input)
			if got != tt.want {
				t.Errorf("maskKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestMigrateFromCodex_NoCodexDir 验证 codex 目录不存在时返回错误。
// 期望：返回错误，错误消息含 "未检测到 codex 配置目录"。
func TestMigrateFromCodex_NoCodexDir(t *testing.T) {
	tmpHome := testutil.IsolatedHomeDir(t)
	imported, err := MigrateFromCodex(tmpHome)
	if err == nil {
		t.Errorf("codex 目录不存在时应返回错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "未检测到 codex 配置目录") {
		t.Errorf("错误消息应含 '未检测到 codex 配置目录'，实际 %q", err.Error())
	}
	if imported != nil {
		t.Errorf("codex 目录不存在时 imported 应为 nil，实际 %v", imported)
	}
}

// TestMigrateFromClaudeCode_NoClaudeDir 验证 claude-code 目录不存在时返回错误。
// 期望：返回错误，错误消息含 "未检测到 claude-code 配置目录"。
func TestMigrateFromClaudeCode_NoClaudeDir(t *testing.T) {
	tmpHome := testutil.IsolatedHomeDir(t)
	imported, err := MigrateFromClaudeCode(tmpHome)
	if err == nil {
		t.Errorf("claude-code 目录不存在时应返回错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "未检测到 claude-code 配置目录") {
		t.Errorf("错误消息应含 '未检测到 claude-code 配置目录'，实际 %q", err.Error())
	}
	if imported != nil {
		t.Errorf("claude-code 目录不存在时 imported 应为 nil，实际 %v", imported)
	}
}

// TestMigrateFromCodex_WithEnvAPIKey 验证 codex 目录存在 + 环境变量 OPENAI_API_KEY 时成功导入。
// 通过 t.Setenv 重定向 HOME 到临时目录，避免污染真实 ~/.whale。
// 期望：imported 列表含 "API Key (provider=openai" 前缀。
func TestMigrateFromCodex_WithEnvAPIKey(t *testing.T) {
	// 重定向 HOME 到临时目录，避免污染真实 ~/.whale
	tmpHome := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome) // Windows
	t.Setenv("OPENAI_API_KEY", "sk-test-codex-key-12345")

	// 创建 codex 目录（无 config.toml，仅用环境变量）
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("创建 codex 目录失败: %v", err)
	}

	imported, err := MigrateFromCodex(tmpHome)
	if err != nil {
		t.Fatalf("MigrateFromCodex 失败: %v", err)
	}
	if len(imported) == 0 {
		t.Fatalf("期望 imported 非空，实际为空")
	}
	// 第一项应为 API Key 导入项
	if !strings.HasPrefix(imported[0], "API Key (provider=openai") {
		t.Errorf("imported[0] 应以 'API Key (provider=openai' 开头，实际 %q", imported[0])
	}
	// 应含权限规则不迁移提示
	foundPermissionHint := false
	for _, item := range imported {
		if strings.Contains(item, "权限规则未自动迁移") {
			foundPermissionHint = true
			break
		}
	}
	if !foundPermissionHint {
		t.Errorf("imported 应含权限规则不迁移提示")
	}
	// 验证 whale 配置文件已写入
	whaleSettings := filepath.Join(tmpHome, ".whale", "settings.json")
	if _, err := os.Stat(whaleSettings); err != nil {
		t.Errorf("期望 ~/.whale/settings.json 已写入，实际不存在: %v", err)
	}
}

// TestMigrateFromClaudeCode_WithEnvAPIKey 验证 claude-code 目录存在 + 环境变量 ANTHROPIC_API_KEY 时成功导入。
// 通过 t.Setenv 重定向 HOME 到临时目录，避免污染真实 ~/.whale。
// 期望：imported 列表含 "API Key (provider=anthropic" 前缀。
func TestMigrateFromClaudeCode_WithEnvAPIKey(t *testing.T) {
	tmpHome := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-claude-key-67890")

	// 创建 claude-code 目录
	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("创建 claude 目录失败: %v", err)
	}

	imported, err := MigrateFromClaudeCode(tmpHome)
	if err != nil {
		t.Fatalf("MigrateFromClaudeCode 失败: %v", err)
	}
	if len(imported) == 0 {
		t.Fatalf("期望 imported 非空，实际为空")
	}
	if !strings.HasPrefix(imported[0], "API Key (provider=anthropic") {
		t.Errorf("imported[0] 应以 'API Key (provider=anthropic' 开头，实际 %q", imported[0])
	}
	// 验证 whale 配置文件已写入
	whaleSettings := filepath.Join(tmpHome, ".whale", "settings.json")
	if _, err := os.Stat(whaleSettings); err != nil {
		t.Errorf("期望 ~/.whale/settings.json 已写入，实际不存在: %v", err)
	}
}

// TestMigrateFromCodex_NoAPIKey 验证 codex 目录存在但无 API Key 时返回错误。
// 期望：返回错误，错误消息含 "未在 codex 配置或环境变量中找到 API Key"。
func TestMigrateFromCodex_NoAPIKey(t *testing.T) {
	tmpHome := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	// 确保环境变量未设置
	t.Setenv("OPENAI_API_KEY", "")

	// 创建 codex 目录（无 config.toml，无环境变量）
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("创建 codex 目录失败: %v", err)
	}

	imported, err := MigrateFromCodex(tmpHome)
	if err == nil {
		t.Errorf("无 API Key 时应返回错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "未在 codex 配置或环境变量中找到 API Key") {
		t.Errorf("错误消息应含 '未在 codex 配置或环境变量中找到 API Key'，实际 %q", err.Error())
	}
	// imported 应为空（未导入任何项）
	if len(imported) != 0 {
		t.Errorf("无 API Key 时 imported 应为空，实际 %v", imported)
	}
}
