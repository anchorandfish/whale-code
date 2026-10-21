package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestIsPathAllowed_SymlinkEscape 反例：symlink 逃逸返回 false
func TestIsPathAllowed_SymlinkEscape(t *testing.T) {
	// Windows 上普通用户可能无法创建 symlink（需要管理员权限）
	if runtime.GOOS == "windows" {
		t.Skip("跳过 symlink 测试（Windows 需管理员权限）")
	}

	tmpDir := t.TempDir()
	allowedDir := filepath.Join(tmpDir, "allowed")
	outsideDir := filepath.Join(tmpDir, "outside")
	os.MkdirAll(allowedDir, 0755)
	os.MkdirAll(outsideDir, 0755)

	// 在 allowed 目录内创建 symlink 指向 outside
	symlinkPath := filepath.Join(allowedDir, "escape_link")
	err := os.Symlink(outsideDir, symlinkPath)
	if err != nil {
		t.Skipf("无法创建 symlink: %v", err)
	}

	// symlink 指向白名单外，应返回 false
	if IsPathAllowed(symlinkPath, []string{allowedDir}) {
		t.Error("symlink 指向白名单外应返回 false")
	}
}

// TestIsPathAllowed_NonExistentPath 反例：路径不存在返回 false（fail-closed）
func TestIsPathAllowed_NonExistentPath(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "nonexistent")

	if IsPathAllowed(nonExistent, []string{tmpDir}) {
		t.Error("不存在的路径应返回 false（fail-closed）")
	}
}

// TestIsPathAllowed_NullByteInjection 反例：路径含 \0 返回 false
func TestIsPathAllowed_NullByteInjection(t *testing.T) {
	if IsPathAllowed("path\x00with\x00null", []string{"/"}) {
		t.Error("含 null 字节的路径应返回 false")
	}
}

// TestIsPathAllowed_WithinRoot 验证根目录内路径返回 true
func TestIsPathAllowed_WithinRoot(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	if !IsPathAllowed(testFile, []string{tmpDir}) {
		t.Error("根目录内路径应返回 true")
	}
}

// TestIsPathAllowed_OutsideRoot 验证根目录外路径返回 false
func TestIsPathAllowed_OutsideRoot(t *testing.T) {
	tmpDir := t.TempDir()
	outsideDir := t.TempDir()
	testFile := filepath.Join(outsideDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	if IsPathAllowed(testFile, []string{tmpDir}) {
		t.Error("根目录外路径应返回 false")
	}
}

// TestContainsTraversal_ForwardSlash 验证 ../ 检出
func TestContainsTraversal_ForwardSlash(t *testing.T) {
	if !ContainsTraversal("../etc/passwd") {
		t.Error("../ 应被检出")
	}
	if !ContainsTraversal("foo/../bar") {
		t.Error("中间的 ../ 应被检出")
	}
	if ContainsTraversal("foo/bar/baz") {
		t.Error("普通路径不应被检出")
	}
}

// TestContainsTraversal_BackSlash 反例：..\ 检出
func TestContainsTraversal_BackSlash(t *testing.T) {
	if !ContainsTraversal(`..\etc\passwd`) {
		t.Error(`..\ 应被检出`)
	}
	if !ContainsTraversal(`foo\..\bar`) {
		t.Error(`中间的 ..\ 应被检出`)
	}
}

// TestContainsTraversal_BothSeparators 反例：../ 与 ..\ 都检出
func TestContainsTraversal_BothSeparators(t *testing.T) {
	cases := []string{
		"../etc/passwd",
		`..\etc\passwd`,
		"foo/../bar",
		`foo\..\bar`,
		"../",
		"..\\",
		"/..",
		`\..`,
	}
	for _, c := range cases {
		if !ContainsTraversal(c) {
			t.Errorf("%q 应被检出穿越", c)
		}
	}

	// 不应检出的情况
	notCases := []string{
		"foo/bar/baz",
		`foo\bar\baz`,
		"..foo",
		"foo..",
		"...",
	}
	for _, c := range notCases {
		if ContainsTraversal(c) {
			t.Errorf("%q 不应被检出穿越", c)
		}
	}
}

// TestIsUNCPath_Backslash 验证 \\server\share 检出
func TestIsUNCPath_Backslash(t *testing.T) {
	if !IsUNCPath(`\\server\share`) {
		t.Error(`\\server\share 应被检出 UNC`)
	}
}

// TestIsUNCPath_ForwardSlash 验证 //server/share 检出
func TestIsUNCPath_ForwardSlash(t *testing.T) {
	if !IsUNCPath("//server/share") {
		t.Error("//server/share 应被检出 UNC")
	}
}

// TestIsUNCPath_NonUNC 验证非 UNC 路径不检出
func TestIsUNCPath_NonUNC(t *testing.T) {
	cases := []string{
		"C:\\Users\\foo",
		"/home/user",
		"relative/path",
		`\Users\foo`,
		"/home",
	}
	for _, c := range cases {
		if IsUNCPath(c) {
			t.Errorf("%q 不应被检出 UNC", c)
		}
	}
}

// TestSafeName_DoubleSeparator 反例：\ 与 / 都替换为 _
func TestSafeName_DoubleSeparator(t *testing.T) {
	input := `foo\bar/baz.txt`
	result := SafeName(input)
	if strings.Contains(result, `\`) || strings.Contains(result, `/`) {
		t.Errorf("两种分隔符都应替换为 _，实际: %s", result)
	}
	if result == input {
		t.Error("结果不应与输入相同")
	}
}

// TestSafeName_WindowsIllegalChars 反例：: < > |? * " 替换为 _
func TestSafeName_WindowsIllegalChars(t *testing.T) {
	input := `file:name<with>|all?illegal*chars".txt`
	result := SafeName(input)
	illegalChars := []rune{':', '<', '>', '|', '?', '*', '"'}
	for _, ch := range illegalChars {
		if strings.ContainsRune(result, ch) {
			t.Errorf("非法字符 %c 应被替换，实际结果: %s", ch, result)
		}
	}
}

// TestSafeName_PreserveChinese 反例：中文保留不 ASCII 化
func TestSafeName_PreserveChinese(t *testing.T) {
	input := "中文文件.txt"
	result := SafeName(input)
	if !strings.Contains(result, "中文") {
		t.Errorf("中文应保留，实际结果: %s", result)
	}
}

// TestSafeName_PlainFilename 验证普通文件名不修改
func TestSafeName_PlainFilename(t *testing.T) {
	input := "normal_filename.txt"
	result := SafeName(input)
	if result != input {
		t.Errorf("普通文件名不应修改，期望 %q，实际 %q", input, result)
	}
}
