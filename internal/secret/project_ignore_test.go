// Package secret — project_ignore.go 单元测试（M15-04）。
package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectIgnore_DefaultPatterns(t *testing.T) {
	// 无项目级 .whaleignore 文件时，仅使用内置默认 Pattern
	pi := NewProjectIgnore("")
	cases := []struct {
		path   string
		expect bool
	}{
		{".env", true},
		{".env.local", true},
		{"config.env", false}, // 默认 Pattern 不命中 config.env
		{"id_rsa", true},
		{"id_ed25519", true},
		{".aws/credentials", true},
		{"credentials.json", true},
		{"README.md", false},
		{"src/main.go", false},
	}
	for _, tc := range cases {
		got := pi.IsSensitive(tc.path)
		if got != tc.expect {
			t.Errorf("IsSensitive(%q) = %v, want %v", tc.path, got, tc.expect)
		}
	}
}

func TestProjectIgnore_LoadProjectFile(t *testing.T) {
	// 创建临时目录作为项目根
	tmpDir := t.TempDir()
	// 写入 .whaleignore 文件
	ignoreContent := `# 项目级 .whaleignore
secrets/**
*.key.local
config/sensitive.yaml
`
	ignorePath := filepath.Join(tmpDir, ".whaleignore")
	if err := os.WriteFile(ignorePath, []byte(ignoreContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pi := NewProjectIgnore(tmpDir)
	cases := []struct {
		path   string
		expect bool
	}{
		// 内置默认
		{".env", true},
		{"id_rsa", true},
		// 项目级自定义
		{"secrets/api.key", true},
		{"secrets/db/password.txt", true}, // ** 递归
		{"app.key.local", true},
		{"config/sensitive.yaml", true},
		// 非敏感
		{"README.md", false},
		{"src/main.go", false},
		{"config/app.yaml", false},
	}
	for _, tc := range cases {
		got := pi.IsSensitive(tc.path)
		if got != tc.expect {
			t.Errorf("IsSensitive(%q) = %v, want %v", tc.path, got, tc.expect)
		}
	}
}

func TestProjectIgnore_Reload(t *testing.T) {
	tmpDir := t.TempDir()
	pi := NewProjectIgnore(tmpDir)
	// 初始无 .whaleignore，仅默认 Pattern
	if pi.IsSensitive("custom.cfg") {
		t.Error("custom.cfg should not match default patterns")
	}

	// 写入 .whaleignore 包含 custom.cfg
	ignoreContent := "custom.cfg\n"
	ignorePath := filepath.Join(tmpDir, ".whaleignore")
	if err := os.WriteFile(ignorePath, []byte(ignoreContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Reload 后应命中
	if err := pi.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !pi.IsSensitive("custom.cfg") {
		t.Error("custom.cfg should match after reload")
	}
}

func TestProjectIgnore_Patterns(t *testing.T) {
	pi := NewProjectIgnore("")
	patterns := pi.Patterns()
	if len(patterns) == 0 {
		t.Error("Patterns() should return default patterns")
	}
	// 应包含 .env
	found := false
	for _, p := range patterns {
		if p == ".env" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Patterns() should include .env")
	}
}

func TestProjectIgnore_AddPattern(t *testing.T) {
	pi := NewProjectIgnore("")
	if err := pi.AddPattern("custom-secret.txt"); err != nil {
		t.Fatalf("AddPattern: %v", err)
	}
	if !pi.IsSensitive("custom-secret.txt") {
		t.Error("custom-secret.txt should match after AddPattern")
	}
}

func TestProjectIgnore_NilSafe(t *testing.T) {
	var pi *ProjectIgnore
	// nil 实例不应 panic
	if pi.IsSensitive("any.txt") {
		t.Error("nil ProjectIgnore should return false")
	}
	if pi.Patterns() != nil {
		t.Error("nil ProjectIgnore should return nil Patterns")
	}
}

func TestScanWithProjectIgnore(t *testing.T) {
	tmpDir := t.TempDir()
	ignoreContent := "secrets/**\n*.key.local\n"
	ignorePath := filepath.Join(tmpDir, ".whaleignore")
	if err := os.WriteFile(ignorePath, []byte(ignoreContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pi := NewProjectIgnore(tmpDir)
	files := []string{
		"README.md",
		"src/main.go",
		".env",
		"secrets/api.key",
		"app.key.local",
	}
	safe, blocked := ScanWithProjectIgnore(files, pi)
	if len(safe) != 2 {
		t.Errorf("safe count = %d, want 2 (README.md, src/main.go)", len(safe))
	}
	if len(blocked) != 3 {
		t.Errorf("blocked count = %d, want 3 (.env, secrets/api.key, app.key.local)", len(blocked))
	}
}

func TestIsSensitiveFile_GlobalFallback(t *testing.T) {
	// 未注入全局实例时回退到内置默认
	original := globalProjectIgnore
	globalProjectIgnore = nil
	defer func() { globalProjectIgnore = original }()

	if !IsSensitiveFile(".env") {
		t.Error("IsSensitiveFile(.env) should be true (default patterns)")
	}
	if IsSensitiveFile("README.md") {
		t.Error("IsSensitiveFile(README.md) should be false")
	}
}

func TestProjectIgnore_CommentLines(t *testing.T) {
	tmpDir := t.TempDir()
	ignoreContent := `# 这是一个注释
.env

# 另一个注释
*.pem
`
	ignorePath := filepath.Join(tmpDir, ".whaleignore")
	if err := os.WriteFile(ignorePath, []byte(ignoreContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pi := NewProjectIgnore(tmpDir)
	// 注释行不应被当作 Pattern
	patterns := pi.Patterns()
	for _, p := range patterns {
		if p == "# 这是一个注释" || p == "# 另一个注释" {
			t.Errorf("Comment line should not be a pattern: %q", p)
		}
	}
	// 但 .env 和 *.pem 应作为 Pattern
	hasEnv, hasPem := false, false
	for _, p := range patterns {
		if p == ".env" {
			hasEnv = true
		}
		if p == "*.pem" {
			hasPem = true
		}
	}
	if !hasEnv {
		t.Error("Patterns should include .env")
	}
	if !hasPem {
		t.Error("Patterns should include *.pem")
	}
}

func TestProjectIgnore_Negation(t *testing.T) {
	// ! 取反语义（.gitignore last-match-wins）
	pi := NewProjectIgnore("")
	_ = pi.AddPattern("*.env")
	_ = pi.AddPattern("!test.env") // 排除 test.env

	if !pi.IsSensitive("prod.env") {
		t.Error("prod.env should match *.env")
	}
	if pi.IsSensitive("test.env") {
		t.Error("test.env should be negated")
	}
}
