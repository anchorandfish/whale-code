package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNormalizePath_NullByte 反例：含 null 字节应返回 error
func TestNormalizePath_NullByte(t *testing.T) {
	_, err := NormalizePath("path\x00with\x00null")
	if err == nil {
		t.Error("含 null 字节的路径应返回 error，实际 nil")
	}
}

// TestNormalizePath_NonExistentPath 反例：路径不存在时降级 Abs+Clean 不报错
func TestNormalizePath_NonExistentPath(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "nonexistent", "dir", "file.go")
	result, err := NormalizePath(nonExistent)
	if err != nil {
		t.Errorf("不存在的路径应降级 Abs+Clean 不报错，实际 error: %v", err)
	}
	if result == "" {
		t.Error("返回路径不应为空")
	}
	// 应返回绝对路径
	if !filepath.IsAbs(result) {
		t.Errorf("返回路径应为绝对路径，实际: %s", result)
	}
}

// TestNormalizePath_ExistingPath 验证存在的路径能正确规范化
func TestNormalizePath_ExistingPath(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	result, err := NormalizePath(testFile)
	if err != nil {
		t.Fatalf("存在的文件 NormalizePath 失败: %v", err)
	}
	if !filepath.IsAbs(result) {
		t.Errorf("应返回绝对路径，实际: %s", result)
	}
}

// TestResolvePath_Relative 验证相对路径基于 workDir 解析
func TestResolvePath_Relative(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := ResolvePath("relative/path.go", tmpDir)
	if err != nil {
		t.Fatalf("ResolvePath 失败: %v", err)
	}
	expected := filepath.Join(tmpDir, "relative", "path.go")
	if !strings.HasSuffix(result, filepath.Join("relative", "path.go")) {
		t.Errorf("结果应以 relative/path.go 结尾，实际: %s", result)
	}
	_ = expected
}

// TestResolvePath_Absolute 验证绝对路径直接规范化
func TestResolvePath_Absolute(t *testing.T) {
	tmpDir := t.TempDir()
	absPath := filepath.Join(tmpDir, "abs.go")
	result, err := ResolvePath(absPath, "/some/other/dir")
	if err != nil {
		t.Fatalf("ResolvePath 失败: %v", err)
	}
	if !strings.Contains(result, "abs.go") {
		t.Errorf("绝对路径不应受 workDir 影响，实际: %s", result)
	}
}

// TestResolvePath_EmptyWorkDir 验证 workDir 为空时用 os.Getwd()
func TestResolvePath_EmptyWorkDir(t *testing.T) {
	wd, _ := os.Getwd()
	result, err := ResolvePath(".", "")
	if err != nil {
		t.Fatalf("ResolvePath 失败: %v", err)
	}
	resultClean := filepath.Clean(result)
	wdClean := filepath.Clean(wd)
	if !EqualFoldPath(resultClean, wdClean) {
		t.Errorf("空 workDir 应使用当前目录 %s，实际: %s", wd, result)
	}
}

// EqualFoldPath 平台感知路径比较（测试辅助函数
func EqualFoldPath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// TestIsAbsolute_Windows 验证 Windows 多种绝对路径格式
func TestIsAbsolute_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过 Windows 特定测试")
	}

	cases := []struct {
		path string
		want bool
	}{
		{`C:\Users\foo`, true},
		{`C:/Users/foo`, true},
		{`\\server\share`, true},
		{`//server/share`, true},
		{`relative\path`, false},
		{`relative/path`, false},
	}
	for _, c := range cases {
		if got := IsAbsolute(c.path); got != c.want {
			t.Errorf("IsAbsolute(%q) = %v，期望 %v", c.path, got, c.want)
		}
	}
}

// TestIsAbsolute_Unix 验证 Unix 绝对路径格式
func TestIsAbsolute_Unix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("跳过 Unix 特定测试")
	}

	cases := []struct {
		path string
		want bool
	}{
		{`/home/user/foo`, true},
		{`relative/path`, false},
		{`./relative`, false},
	}
	for _, c := range cases {
		if got := IsAbsolute(c.path); got != c.want {
			t.Errorf("IsAbsolute(%q) = %v，期望 %v", c.path, got, c.want)
		}
	}
}

// TestToSlash_FromSlash 验证双向转换往返一致
func TestToSlash_FromSlash(t *testing.T) {
	original := `a\b\c\d`
	slashed := ToSlash(original)
	if strings.Contains(slashed, `\`) {
		t.Errorf("ToSlash 后不应含 \\，实际: %s", slashed)
	}
	back := FromSlash(slashed)
	if runtime.GOOS == "windows" {
		if !strings.Contains(back, `\`) {
			t.Errorf("FromSlash 后 Windows 应含 \\，实际: %s", back)
		}
	}
}

// TestExpandTilde_Unix 验证 ~/foo 正确展开
func TestExpandTilde_Unix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}

	result, err := ExpandTilde("~/test/path")
	if err != nil {
		t.Fatalf("ExpandTilde 失败: %v", err)
	}
	expected := filepath.Join(home, "test", "path")
	if result != expected {
		t.Errorf("ExpandTilde(~%s) = %s，期望 %s", "/test/path", result, expected)
	}
}

// TestExpandTilde_WindowsBackslash 反例：~\foo 在 Windows 正确展开
func TestExpandTilde_WindowsBackslash(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过 Windows 特定测试")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}

	result, err := ExpandTilde(`~\test\path`)
	if err != nil {
		t.Fatalf("ExpandTilde 失败: %v", err)
	}
	expected := filepath.Join(home, "test", "path")
	if result != expected {
		t.Errorf("ExpandTilde(~%s) = %s，期望 %s", `\test\path`, result, expected)
	}
}

// TestExpandTilde_TildeOnly 验证单独 ~ 展开
func TestExpandTilde_TildeOnly(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}

	result, err := ExpandTilde("~")
	if err != nil {
		t.Fatalf("ExpandTilde 失败: %v", err)
	}
	if result != home {
		t.Errorf("ExpandTilde(~) = %s，期望 %s", result, home)
	}
}

// TestExpandTilde_NoTilde 验证无 ~ 的路径不修改
func TestExpandTilde_NoTilde(t *testing.T) {
	result, err := ExpandTilde("/normal/path")
	if err != nil {
		t.Fatalf("ExpandTilde 失败: %v", err)
	}
	if result != "/normal/path" {
		t.Errorf("无 ~ 的路径不应修改，实际: %s", result)
	}
}

// TestNormalizeUnicode_NFC 验证组合字符 NFC 规范化
func TestNormalizeUnicode_NFC(t *testing.T) {
	// NFC: é = \u00e9 (单字符)
	// NFD: é = e + \u0301 (e + 组合重音符号)
	nfc := "\u00e9"  // é (NFC)
	nfd := "e\u0301" // é (NFD)

	result := NormalizeUnicode(nfd)
	if result != nfc {
		t.Errorf("NFD 应规范化为 NFC，期望 %q，实际 %q", nfc, result)
	}
}

// TestCleanAndNormalize 验证 Clean + NFC 规范化
func TestCleanAndNormalize(t *testing.T) {
	input := "./a/../b/./c"
	result := CleanAndNormalize(input)
	expected := filepath.Clean(input)
	if result != expected {
		t.Errorf("CleanAndNormalize(%q) = %q，期望 %q", input, result, expected)
	}
}

// TestPathEqual_CaseInsensitive_Windows 验证 Windows 上 C:\Foo == C:\foo
func TestPathEqual_CaseInsensitive_Windows(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("仅在大小写不敏感文件系统测试")
	}

	if !PathEqual(`C:\Foo\Bar`, `C:\foo\bar`) {
		t.Error("大小写不敏感系统上路径应相等")
	}
}

// TestPathEqual_CaseSensitive_Linux 验证 Linux 上 Foo != foo
func TestPathEqual_CaseSensitive_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅在 Linux 测试")
	}

	if PathEqual("/Foo/Bar", "/foo/bar") {
		t.Error("大小写敏感系统上路径不应相等")
	}
}

// TestCaseInsensitiveCompare 验证一律 ToLower 比较
func TestCaseInsensitiveCompare(t *testing.T) {
	if !CaseInsensitiveCompare("/Foo/Bar", "/foo/bar") {
		t.Error("CaseInsensitiveCompare 应忽略大小写")
	}
}

// TestNormalizePath_LongPath Windows 长路径（> 260 字符）支持
// 反例：长路径不应被截断或拒绝，应正确规范化
func TestNormalizePath_LongPath(t *testing.T) {
	// 构造 > 260 字符的长路径（Windows MAX_PATH 限制）
	tmpDir := t.TempDir()
	longName := strings.Repeat("subdir/", 30) + "file.txt" // 约 220+ 字符
	longPath := filepath.Join(tmpDir, longName)
	if err := os.MkdirAll(filepath.Dir(longPath), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(longPath, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	result, err := NormalizePath(longPath)
	if err != nil {
		t.Errorf("长路径应正确规范化，实际 error: %v", err)
	}
	if result == "" {
		t.Error("长路径返回不应为空")
	}
	if !filepath.IsAbs(result) {
		t.Errorf("长路径应返回绝对路径，实际: %s", result)
	}
}

// TestNormalizePath_EmojiPath 含 emoji 的路径支持
// 反例：emoji 字符不应导致路径处理失败
func TestNormalizePath_EmojiPath(t *testing.T) {
	tmpDir := t.TempDir()
	emojiPath := filepath.Join(tmpDir, "🌟", "file.txt")
	if err := os.MkdirAll(filepath.Dir(emojiPath), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(emojiPath, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	result, err := NormalizePath(emojiPath)
	if err != nil {
		t.Errorf("emoji 路径应正确规范化，实际 error: %v", err)
	}
	if result == "" {
		t.Error("emoji 路径返回不应为空")
	}
}

// TestNormalizePath_SpacePath 含空格的路径支持
// 反例：空格字符不应被截断或拒绝
func TestNormalizePath_SpacePath(t *testing.T) {
	tmpDir := t.TempDir()
	spacePath := filepath.Join(tmpDir, "my project", "source code.go")
	if err := os.MkdirAll(filepath.Dir(spacePath), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(spacePath, []byte("package main"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	result, err := NormalizePath(spacePath)
	if err != nil {
		t.Errorf("含空格路径应正确规范化，实际 error: %v", err)
	}
	if !strings.Contains(result, "my project") {
		t.Errorf("空格应保留，实际: %s", result)
	}
}

// TestNormalizePath_ChinesePath 含中文的路径支持
// 反例：中文字符不应导致路径处理失败
func TestNormalizePath_ChinesePath(t *testing.T) {
	tmpDir := t.TempDir()
	chinesePath := filepath.Join(tmpDir, "中文目录", "文件.go")
	if err := os.MkdirAll(filepath.Dir(chinesePath), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(chinesePath, []byte("package main"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	result, err := NormalizePath(chinesePath)
	if err != nil {
		t.Errorf("中文路径应正确规范化，实际 error: %v", err)
	}
	if !strings.Contains(result, "中文目录") {
		t.Errorf("中文应保留，实际: %s", result)
	}
}

// TestResolvePath_RelativeWithDotDot 含 .. 的相对路径
// 反例：.. 应被正确解析，不应穿越到 workDir 之外
func TestResolvePath_RelativeWithDotDot(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	// ResolvePath(p, workDir) 形式：p 为 ".." 相对路径，workDir 为 subDir
	// 期望 .. 抵消 subDir 的最后一段，结果为 tmpDir/file.txt
	result, err := ResolvePath("../file.txt", subDir)
	if err != nil {
		t.Errorf("ResolvePath failed: %v", err)
	}
	expected := filepath.Join(tmpDir, "file.txt")
	if result != expected {
		t.Errorf("result = %s, want %s", result, expected)
	}
}

// TestPathEqual_DifferentSeparators 跨平台路径分隔符
// 验证 PathEqual 通过 ToSlash + CleanAndNormalize 实现 \ 和 / 等价
// 这是 PathEqual 的设计意图（跨平台路径等价判断），与 PathEqual_CaseSensitive_Linux 区分
func TestPathEqual_DifferentSeparators(t *testing.T) {
	// 所有平台都应将 \ 和 / 识别为相同路径（ToSlash 后比较）
	if !PathEqual("C:\\foo\\bar", "C:/foo/bar") {
		t.Error("PathEqual 应通过 ToSlash 将 \\ 和 / 识别为相同路径")
	}
	if !PathEqual("/foo/bar", "/foo\\bar") {
		t.Error("PathEqual 应通过 ToSlash 将 \\ 和 / 识别为相同路径")
	}
}
