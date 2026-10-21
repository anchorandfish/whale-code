package platform

import (
	"os"
	"runtime"
	"testing"
)

// TestOS 验证 OS() 返回有效值
func TestOS(t *testing.T) {
	osName := OS()
	valid := map[string]bool{
		"windows": true,
		"linux":   true,
		"darwin":  true,
	}
	if !valid[osName] {
		t.Errorf("OS() 返回无效值 %q", osName)
	}
	if osName != runtime.GOOS {
		t.Errorf("OS() = %q，runtime.GOOS = %q，不一致", osName, runtime.GOOS)
	}
}

// TestIsWindows 验证 IsWindows() 与 runtime.GOOS 一致
func TestIsWindows(t *testing.T) {
	expected := runtime.GOOS == "windows"
	if got := IsWindows(); got != expected {
		t.Errorf("IsWindows() = %v，期望 %v", got, expected)
	}
}

// TestIsDarwin 验证 IsDarwin() 与 runtime.GOOS 一致
func TestIsDarwin(t *testing.T) {
	expected := runtime.GOOS == "darwin"
	if got := IsDarwin(); got != expected {
		t.Errorf("IsDarwin() = %v，期望 %v", got, expected)
	}
}

// TestIsLinux 验证 IsLinux() 与 runtime.GOOS 一致
func TestIsLinux(t *testing.T) {
	expected := runtime.GOOS == "linux"
	if got := IsLinux(); got != expected {
		t.Errorf("IsLinux() = %v，期望 %v", got, expected)
	}
}

// TestCaseInsensitiveFS 验证 CaseInsensitiveFS() 平台感知正确
func TestCaseInsensitiveFS(t *testing.T) {
	// 保存原始环境变量，测试后恢复
	orig := os.Getenv("WHALE_CASE_SENSITIVE")
	defer os.Setenv("WHALE_CASE_SENSITIVE", orig)

	// 默认情况
	os.Unsetenv("WHALE_CASE_SENSITIVE")
	expected := IsWindows() || IsDarwin()
	if got := CaseInsensitiveFS(); got != expected {
		t.Errorf("默认 CaseInsensitiveFS() = %v，期望 %v", got, expected)
	}

	// 覆盖为大小写敏感
	os.Setenv("WHALE_CASE_SENSITIVE", "1")
	if got := CaseInsensitiveFS(); got != false {
		t.Errorf("WHALE_CASE_SENSITIVE=1 时 CaseInsensitiveFS() = %v，期望 false", got)
	}
}
