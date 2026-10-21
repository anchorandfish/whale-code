// Package lsp — LSP 配置加载测试（T1-P1-A2 /）
package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultLSPConfig 验证默认配置包含 6 语言 7 server 项
// 验证字段：Version / Servers 数量 / Recovery 默认值
func TestDefaultLSPConfig(t *testing.T) {
	cfg := DefaultLSPConfig()
	if cfg.Version != CurrentLSPConfigVersion {
		t.Errorf("version = %d, want %d", cfg.Version, CurrentLSPConfigVersion)
	}
	if len(cfg.Servers) != 7 {
		t.Errorf("servers count = %d, want 7 (6 语言 + Python 2 候选)", len(cfg.Servers))
	}
	if cfg.Recovery.MaxRestarts != 5 {
		t.Errorf("maxRestarts = %d, want 5", cfg.Recovery.MaxRestarts)
	}
	if cfg.Recovery.BackoffDelayMs != 1000 {
		t.Errorf("backoffDelayMs = %d, want 1000", cfg.Recovery.BackoffDelayMs)
	}
	if cfg.Recovery.HealthCheckMs != 0 {
		t.Errorf("healthCheckMs = %d, want 0 (默认禁用)", cfg.Recovery.HealthCheckMs)
	}
}

// TestDefaultLSPConfig_CacheConsistent 验证 DefaultLSPConfig 多次调用返回同一指针（缓存生效）
func TestDefaultLSPConfig_CacheConsistent(t *testing.T) {
	cfg1 := DefaultLSPConfig()
	cfg2 := DefaultLSPConfig()
	if cfg1 != cfg2 {
		t.Error("DefaultLSPConfig should return cached pointer")
	}
}

// TestLoadLSPConfig_EmptyPath 验证空路径降级 DefaultLSPConfig
func TestLoadLSPConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadLSPConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != CurrentLSPConfigVersion {
		t.Errorf("version = %d, want %d", cfg.Version, CurrentLSPConfigVersion)
	}
}

// TestLoadLSPConfig_FileNotExist 验证文件不存在时降级 DefaultLSPConfig（无错误）
func TestLoadLSPConfig_FileNotExist(t *testing.T) {
	cfg, err := LoadLSPConfig("/nonexistent/path/lsp.json")
	if err != nil {
		t.Fatalf("file not exist should not return error: %v", err)
	}
	if cfg.Version != CurrentLSPConfigVersion {
		t.Errorf("version = %d, want %d (default)", cfg.Version, CurrentLSPConfigVersion)
	}
	if len(cfg.Servers) != 7 {
		t.Errorf("servers count = %d, want 7 (default)", len(cfg.Servers))
	}
}

// TestLoadLSPConfig_ValidJSON 验证合法 JSON 加载
func TestLoadLSPConfig_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "lsp.json")
	content := `{
		"version": 1,
		"servers": [
			{"name": "go-custom", "command": "/opt/gopls", "args": ["serve"], "filetypes": [".go"]}
		],
		"recovery": {"maxRestarts": 3, "backoffDelayMs": 500}
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLSPConfig(cfgPath)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Errorf("servers count = %d, want 1", len(cfg.Servers))
	}
	if cfg.Servers[0].Command != "/opt/gopls" {
		t.Errorf("command = %s, want /opt/gopls", cfg.Servers[0].Command)
	}
	if cfg.Recovery.MaxRestarts != 3 {
		t.Errorf("maxRestarts = %d, want 3", cfg.Recovery.MaxRestarts)
	}
	if cfg.Recovery.BackoffDelayMs != 500 {
		t.Errorf("backoffDelayMs = %d, want 500", cfg.Recovery.BackoffDelayMs)
	}
}

// TestLoadLSPConfig_InvalidJSON 验证损坏 JSON 降级 DefaultLSPConfig
func TestLoadLSPConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "lsp.json")
	if err := os.WriteFile(cfgPath, []byte("{invalid json}"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLSPConfig(cfgPath)
	if err != nil {
		t.Fatalf("should not error on invalid JSON (degrade silently): %v", err)
	}
	// 应回退到默认配置
	if cfg.Version != CurrentLSPConfigVersion {
		t.Errorf("version = %d, want %d (default)", cfg.Version, CurrentLSPConfigVersion)
	}
	if len(cfg.Servers) != 7 {
		t.Errorf("servers count = %d, want 7 (default)", len(cfg.Servers))
	}
}

// TestLoadLSPConfig_PartialRecovery 验证 recovery 部分字段缺省时补默认值
func TestLoadLSPConfig_PartialRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "lsp.json")
	// 仅设置 maxRestarts，backoffDelayMs 缺省
	content := `{
		"version": 1,
		"servers": [{"name": "go", "command": "gopls", "filetypes": [".go"]}],
		"recovery": {"maxRestarts": 10}
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLSPConfig(cfgPath)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if cfg.Recovery.MaxRestarts != 10 {
		t.Errorf("maxRestarts = %d, want 10", cfg.Recovery.MaxRestarts)
	}
	if cfg.Recovery.BackoffDelayMs != 1000 {
		t.Errorf("backoffDelayMs = %d, want 1000 (default)", cfg.Recovery.BackoffDelayMs)
	}
}

// TestLoadLSPConfig_UnlimitedRestarts 验证 maxRestarts=-1 表示无限重启
func TestLoadLSPConfig_UnlimitedRestarts(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "lsp.json")
	content := `{
		"version": 1,
		"servers": [{"name": "go", "command": "gopls", "filetypes": [".go"]}],
		"recovery": {"maxRestarts": -1, "backoffDelayMs": 100}
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLSPConfig(cfgPath)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if cfg.Recovery.MaxRestarts != -1 {
		t.Errorf("maxRestarts = %d, want -1 (unlimited)", cfg.Recovery.MaxRestarts)
	}
}

// TestValidate_DuplicateName 验证 name 重复检测
func TestValidate_DuplicateName(t *testing.T) {
	cfg := &LSPConfig{
		Version: 1,
		Servers: []ServerConfig{
			{Name: "go", Command: "gopls", Filetypes: []string{".go"}},
			{Name: "go", Command: "gopls2", Filetypes: []string{".go"}},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected validation errors for duplicate name")
	}
}

// TestValidate_EmptyCommand 验证 command 空检测
func TestValidate_EmptyCommand(t *testing.T) {
	cfg := &LSPConfig{
		Version: 1,
		Servers: []ServerConfig{
			{Name: "go", Command: "", Filetypes: []string{".go"}},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected validation errors for empty command")
	}
}

// TestValidate_EmptyFiletypes 验证 filetypes 空检测
func TestValidate_EmptyFiletypes(t *testing.T) {
	cfg := &LSPConfig{
		Version: 1,
		Servers: []ServerConfig{
			{Name: "go", Command: "gopls", Filetypes: nil},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected validation errors for empty filetypes")
	}
}

// TestValidate_InvalidMaxRestarts 验证 maxRestarts < -1 检测
func TestValidate_InvalidMaxRestarts(t *testing.T) {
	cfg := &LSPConfig{
		Version:  1,
		Servers:  []ServerConfig{{Name: "go", Command: "gopls", Filetypes: []string{".go"}}},
		Recovery: RecoveryConfig{MaxRestarts: -2, BackoffDelayMs: 1000},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected validation errors for maxRestarts < -1")
	}
}

// TestFindServersByFiletype 验证按扩展名查找
func TestFindServersByFiletype(t *testing.T) {
	cfg := DefaultLSPConfig()

	// Python 应返回 2 个候选（pylsp + pyright）
	pyServers := cfg.FindServersByFiletype(".py")
	if len(pyServers) != 2 {
		t.Errorf("py servers count = %d, want 2 (pylsp + pyright)", len(pyServers))
	}

	// Go 应返回 1 个
	goServers := cfg.FindServersByFiletype(".go")
	if len(goServers) != 1 {
		t.Errorf("go servers count = %d, want 1", len(goServers))
	}

	// 未知扩展名应返回 0
	unknownServers := cfg.FindServersByFiletype(".brainfuck")
	if len(unknownServers) != 0 {
		t.Errorf("unknown servers count = %d, want 0", len(unknownServers))
	}
}

// TestFindServersByFiletype_CaseInsensitive 验证扩展名大小写不敏感
func TestFindServersByFiletype_CaseInsensitive(t *testing.T) {
	cfg := DefaultLSPConfig()
	// ".GO" 与 ".go" 应返回相同结果
	goLower := cfg.FindServersByFiletype(".go")
	goUpper := cfg.FindServersByFiletype(".GO")
	if len(goLower) != len(goUpper) {
		t.Errorf("case insensitive failed: .go=%d .GO=%d", len(goLower), len(goUpper))
	}
}

// TestFindServersByFiletype_NoDotPrefix 验证无点前缀扩展名自动补点
func TestFindServersByFiletype_NoDotPrefix(t *testing.T) {
	cfg := DefaultLSPConfig()
	// "go" 与 ".go" 应返回相同结果
	withDot := cfg.FindServersByFiletype(".go")
	withoutDot := cfg.FindServersByFiletype("go")
	if len(withDot) != len(withoutDot) {
		t.Errorf("dot prefix normalization failed: .go=%d go=%d", len(withDot), len(withoutDot))
	}
}

// TestLanguageToExtension 验证语言名到扩展名映射
func TestLanguageToExtension(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"go", ".go"},
		{"python", ".py"},
		{"typescript", ".ts"},
		{"rust", ".rs"},
		{"c", ".c"},
		{"cpp", ".cpp"},
		{"brainfuck", ""}, // 不支持
		{"GO", ".go"},     // 大小写不敏感
		{"Python", ".py"},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			got := languageToExtension(tt.lang)
			if got != tt.want {
				t.Errorf("languageToExtension(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

// TestMigrateLSPConfig_V1 验证 v1 → v1 是 noop
func TestMigrateLSPConfig_V1(t *testing.T) {
	cfg := &LSPConfig{Version: 1}
	if err := migrateLSPConfig(cfg); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
}

// TestMigrateLSPConfig_V0ToV1 验证 v0 → v1 迁移
func TestMigrateLSPConfig_V0ToV1(t *testing.T) {
	cfg := &LSPConfig{Version: 0}
	if err := migrateLSPConfig(cfg); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1 (migrated)", cfg.Version)
	}
}

// TestAnyLanguageServerAvailable 能力探针（· W5）的边界语义（经可注入 lookPath）。
// 反例注入（应打红）：把"任一可用"改成"全部可用"，或漏判空 Command。
func TestAnyLanguageServerAvailable(t *testing.T) {
	errNotFound := os.ErrNotExist
	probe := func(cfg *LSPConfig, lookPath func(string) (string, error)) bool {
		return anyLanguageServerAvailableFrom(cfg, lookPath)
	}

	cases := []struct {
		name     string
		cfg      *LSPConfig
		lookPath func(string) (string, error)
		want     bool
	}{
		{
			name:     "空 server 列表",
			cfg:      &LSPConfig{Servers: []ServerConfig{}},
			lookPath: func(string) (string, error) { return "", errNotFound },
			want:     false,
		},
		{
			name: "两个 server 只要一个命中即 true",
			cfg: &LSPConfig{Servers: []ServerConfig{
				{Name: "a", Command: "gopls"},
				{Name: "b", Command: "pyright-langserver"},
			}},
			lookPath: func(cmd string) (string, error) {
				if cmd == "pyright-langserver" {
					return "/usr/bin/pyright-langserver", nil
				}
				return "", errNotFound
			},
			want: true,
		},
		{
			name:     "全部 miss 即 false",
			cfg:      &LSPConfig{Servers: []ServerConfig{{Name: "a", Command: "gopls"}}},
			lookPath: func(string) (string, error) { return "", errNotFound },
			want:     false,
		},
		{
			name: "只有空 command 不算可用",
			cfg:  &LSPConfig{Servers: []ServerConfig{{Name: "x", Command: ""}}},
			lookPath: func(cmd string) (string, error) {
				if cmd != "" {
					return "/x", nil
				}
				return "", errNotFound
			},
			want: false,
		},
		{
			name:     "nil cfg 降级默认且默认里有任一命中",
			cfg:      nil,
			lookPath: func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
			want:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := probe(c.cfg, c.lookPath); got != c.want {
				t.Errorf("探针=%v，期望 %v", got, c.want)
			}
		})
	}
}
