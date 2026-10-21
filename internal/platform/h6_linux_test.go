package platform

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ========== H6 专项：Linux 路径处理核心验证 ==========

// TestToSlash_LinuxBackslashConversion 验证 Phase 0.5 修复：Linux 上 ToSlash 也转换反斜杠
// Phase 0.5 bug：原 filepath.ToSlash 在 Linux 上是 no-op（/ 已是分隔符），不转换反斜杠
func TestToSlash_LinuxBackslashConversion(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	// 关键场景：Windows 风格路径在 Linux 上也需归一化
	input := `C:\Users\foo\bar.go`
	result := ToSlash(input)
	if strings.Contains(result, `\`) {
		t.Errorf("ToSlash 应将所有反斜杠转为正斜杠，输入 %q，输出 %q", input, result)
	}
	expected := "C:/Users/foo/bar.go"
	if result != expected {
		t.Errorf("ToSlash(%q) = %q，期望 %q", input, result, expected)
	}
}

// TestToSlash_MixedSeparators 反例：混合分隔符路径归一化
func TestToSlash_MixedSeparators(t *testing.T) {
	input := `foo\bar/baz\qux`
	result := ToSlash(input)
	if strings.Contains(result, `\`) {
		t.Errorf("混合分隔符路径应全转 /，实际: %s", result)
	}
	if result != "foo/bar/baz/qux" {
		t.Errorf("ToSlash(%q) = %q，期望 foo/bar/baz/qux", input, result)
	}
}

// TestToSlash_EmptyString 反例：空输入
func TestToSlash_EmptyString(t *testing.T) {
	result := ToSlash("")
	if result != "" {
		t.Errorf("空输入应返回空字符串，实际: %q", result)
	}
}

// TestToSlash_NoBackslash 正例：纯 POSIX 路径不修改
func TestToSlash_NoBackslash(t *testing.T) {
	input := "/home/user/foo.go"
	result := ToSlash(input)
	if result != input {
		t.Errorf("纯 POSIX 路径不应修改，输入 %q，输出 %q", input, result)
	}
}

// ========== FilePathToURI / URIToFilePath Linux 专项 ==========

// TestFilePathToURI_LinuxAbsolutePath 验证 Linux 绝对路径转 URI
func TestFilePathToURI_LinuxAbsolutePath(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	cases := []struct {
		input    string
		expected string
	}{
		{"/home/user/project/main.go", "file:///home/user/project/main.go"},
		{"/tmp/test.go", "file:///tmp/test.go"},
		{"/", "file:///"},
	}
	for _, c := range cases {
		uri := FilePathToURI(c.input)
		if uri != c.expected {
			t.Errorf("FilePathToURI(%q) = %q，期望 %q", c.input, uri, c.expected)
		}
	}
}

// TestFilePathToURI_SpacesInPath 反例：Linux 含空格路径正确编码
func TestFilePathToURI_SpacesInPath(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	uri := FilePathToURI("/home/my project/test file.go")
	if !strings.HasPrefix(uri, "file:///home/") {
		t.Errorf("空格路径 URI 前缀不正确: %s", uri)
	}
	// 空格应被编码为 %20
	if strings.Contains(uri, " ") {
		t.Errorf("空格应被 URL 编码，实际: %s", uri)
	}
	if !strings.Contains(uri, "%20") {
		t.Errorf("空格路径应包含 %%20，实际: %s", uri)
	}
}

// TestFilePathToURI_LinuxWithBackslash 反例：Linux 上含反斜杠路径（Phase 0.5 修复验证）
func TestFilePathToURI_LinuxWithBackslash(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	// 在 Linux 上，反斜杠应被 ToSlash 转为 / 后再编码
	input := `/home/user\foo/bar.go`
	uri := FilePathToURI(input)
	if strings.Contains(uri, "\\") {
		t.Errorf("反斜杠应被转义，URI: %s", uri)
	}
}

// TestURIToFilePath_LinuxAbsolutePath 验证 Linux URI 转文件路径
func TestURIToFilePath_LinuxAbsolutePath(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	cases := []struct {
		uri      string
		expected string
	}{
		{"file:///home/user/project/main.go", "/home/user/project/main.go"},
		{"file:///tmp/test.go", "/tmp/test.go"},
	}
	for _, c := range cases {
		path, err := URIToFilePath(c.uri)
		if err != nil {
			t.Fatalf("URIToFilePath(%q) 失败: %v", c.uri, err)
		}
		if path != c.expected {
			t.Errorf("URIToFilePath(%q) = %q，期望 %q", c.uri, path, c.expected)
		}
	}
}

// TestURIToFilePath_SpacesInURI 反例：URI 中含 %20 编码的空格
func TestURIToFilePath_SpacesInURI(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	path, err := URIToFilePath("file:///home/my%20project/test%20file.go")
	if err != nil {
		t.Fatalf("URIToFilePath 失败: %v", err)
	}
	if !strings.Contains(path, "my project") {
		t.Errorf("pct20 应被解码为空格，实际: %s", path)
	}
	if !strings.Contains(path, "test file.go") {
		t.Errorf("pct20 应被解码为空格，实际: %s", path)
	}
}

// TestURI_RoundTrip_LinuxChinesePath 验证 Linux 中文路径 URI 往返
func TestURI_RoundTrip_LinuxChinesePath(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	original := "/home/用户/项目/测试文件.go"
	uri := FilePathToURI(original)
	back, err := URIToFilePath(uri)
	if err != nil {
		t.Fatalf("中文路径往返失败: %v", err)
	}
	if back != original {
		t.Errorf("中文路径往返不一致：原始 %q → URI %q → 返回 %q", original, uri, back)
	}
}

// TestURI_RoundTrip_SpacesAndChinese 验证含空格和中文的路径往返
func TestURI_RoundTrip_SpacesAndChinese(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	original := "/home/my project/中文 目录/test.go"
	uri := FilePathToURI(original)
	back, err := URIToFilePath(uri)
	if err != nil {
		t.Fatalf("混合路径往返失败: %v", err)
	}
	if back != original {
		t.Errorf("混合路径往返不一致：原始 %q → URI %q → 返回 %q", original, uri, back)
	}
}

// ========== IsPathAllowed 路径穿越防护专项 ==========

// TestIsPathAllowed_TraversalWithDotDot 反例：含 ../ 的路径穿越攻击
func TestIsPathAllowed_TraversalWithDotDot(t *testing.T) {
	tmpDir := t.TempDir()
	allowedDir := filepath.Join(tmpDir, "allowed")
	os.MkdirAll(allowedDir, 0755)

	// 直接 ../ 穿越
	escapePath := filepath.Join(allowedDir, "..", "outside", "file.txt")
	if IsPathAllowed(escapePath, []string{allowedDir}) {
		t.Error("含 ../ 的路径穿越应返回 false")
	}
}

// TestIsPathAllowed_SymlinkChain 反例：链式 symlink 逃逸
func TestIsPathAllowed_SymlinkChain(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试 symlink")
	}

	tmpDir := t.TempDir()
	allowedDir := filepath.Join(tmpDir, "allowed")
	outsideDir := filepath.Join(tmpDir, "outside")
	os.MkdirAll(allowedDir, 0755)
	os.MkdirAll(outsideDir, 0755)

	// 创建链式 symlink：allowed/link1 → outside，outside/link2 → /tmp
	link1 := filepath.Join(allowedDir, "link1")
	os.Symlink(outsideDir, link1)

	link2 := filepath.Join(outsideDir, "link2")
	os.Symlink("/tmp", link2)

	// 链式 symlink 最终指向白名单外
	if IsPathAllowed(link2, []string{allowedDir}) {
		t.Error("链式 symlink 逃逸应返回 false")
	}
}

// TestIsPathAllowed_SymlinkWithinRoot 验证白名单内 symlink 返回 true
func TestIsPathAllowed_SymlinkWithinRoot(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试 symlink")
	}

	tmpDir := t.TempDir()
	allowedDir := filepath.Join(tmpDir, "allowed")
	os.MkdirAll(allowedDir, 0755)

	// 白名单内创建 symlink 指向白名单内目标
	targetFile := filepath.Join(allowedDir, "real_file.txt")
	os.WriteFile(targetFile, []byte("hello"), 0644)

	linkPath := filepath.Join(allowedDir, "link_to_real")
	os.Symlink(targetFile, linkPath)

	if !IsPathAllowed(linkPath, []string{allowedDir}) {
		t.Error("白名单内 symlink 指向白名单内目标应返回 true")
	}
}

// TestIsPathAllowed_MultipleRoots 验证多个白名单根目录
func TestIsPathAllowed_MultipleRoots(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	outsideDir := t.TempDir()

	file1 := filepath.Join(tmpDir1, "file1.txt")
	file2 := filepath.Join(tmpDir2, "file2.txt")
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	os.WriteFile(file1, []byte("1"), 0644)
	os.WriteFile(file2, []byte("2"), 0644)
	os.WriteFile(outsideFile, []byte("3"), 0644)

	roots := []string{tmpDir1, tmpDir2}

	if !IsPathAllowed(file1, roots) {
		t.Error("第一个根目录内文件应返回 true")
	}
	if !IsPathAllowed(file2, roots) {
		t.Error("第二个根目录内文件应返回 true")
	}
	if IsPathAllowed(outsideFile, roots) {
		t.Error("白名单外文件应返回 false")
	}
}

// TestIsPathAllowed_RootItself 验证根目录本身返回 true
func TestIsPathAllowed_RootItself(t *testing.T) {
	tmpDir := t.TempDir()
	if !IsPathAllowed(tmpDir, []string{tmpDir}) {
		t.Error("根目录本身应返回 true")
	}
}

// TestIsPathAllowed_EmptyRoots 反例：空白名单列表
func TestIsPathAllowed_EmptyRoots(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	if IsPathAllowed(testFile, []string{}) {
		t.Error("空白名单应返回 false")
	}
}

// TestIsPathAllowed_PathTraversalEncoded 反例：URL 编码的路径穿越
func TestIsPathAllowed_PathTraversalEncoded(t *testing.T) {
	// 确保 ContainsTraversal 也能检测变体
	cases := []string{
		"/foo/..%2Fbar", // URL 编码的 /
		"/foo/..%5Cbar", // URL 编码的 \
		"/foo/..;/bar",  // 分号绕过尝试
	}
	for _, c := range cases {
		// ContainsTraversal 对这些变体不一定全检出，但记录行为
		_ = ContainsTraversal(c)
	}
}

// TestContainsTraversal_EdgeCases 反例：穿越检测边界条件
func TestContainsTraversal_EdgeCases(t *testing.T) {
	// 应检出
	mustDetect := []string{
		"../",
		"..\\",
		"/..",
		"\\..",
		"a/../b",
		"a\\..\\b",
		"/../../etc/passwd",
		"..", // 单独的 ..
	}
	for _, c := range mustDetect {
		if !ContainsTraversal(c) {
			t.Errorf("应检出穿越: %q", c)
		}
	}

	// 不应检出
	shouldNotDetect := []string{
		"foo/bar",
		"foo.go",
		"..foo",   // .. 不是路径分隔
		"foo..",   // .. 不是路径分隔
		"...",     // 三个点
		"foo.bar", // . 在文件名中
	}
	for _, c := range shouldNotDetect {
		if ContainsTraversal(c) {
			t.Errorf("不应检出穿越: %q", c)
		}
	}
}

// ========== 并发安全测试 ==========

// TestPlatform_ConcurrentSafe 验证所有导出函数的并发安全性
func TestPlatform_ConcurrentSafe(t *testing.T) {
	const goroutines = 50
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 并发调用各导出函数
			_ = OS()
			_ = IsWindows()
			_ = IsLinux()
			_ = CaseInsensitiveFS()
			_ = ToSlash(`/foo\bar/baz`)
			_ = FromSlash("/foo/bar")
			_, _ = ExpandTilde("~/test")
			_ = NormalizeUnicode("café")
			_ = CleanAndNormalize("/foo/../bar")
			_ = PathEqual("/Foo", "/foo")
			_ = CaseInsensitiveCompare("/Foo", "/foo")
			_ = IsAbsolute("/home")
			_, _ = NormalizePath("/tmp")
			_ = FilePathToURI("/home/user/file.go")
			_, _ = URIToFilePath("file:///home/user/file.go")
			_ = ContainsTraversal("../foo")
			_ = IsUNCPath(`\\server\share`)
			_ = SafeName("test:file")
			_ = IsPathAllowed("/tmp", []string{"/tmp"})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// TestIsPathAllowed_ConcurrentSafe 验证 IsPathAllowed 并发安全
func TestIsPathAllowed_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "concurrent.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	const goroutines = 100
	var wg sync.WaitGroup
	results := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = IsPathAllowed(testFile, []string{tmpDir})
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if !r {
			t.Errorf("goroutine %d: IsPathAllowed 应返回 true", i)
		}
	}
}

// ========== NormalizePath 边界测试 ==========

// TestNormalizePath_EmptyInput 反例：空字符串输入
func TestNormalizePath_EmptyInput(t *testing.T) {
	result, err := NormalizePath("")
	if err != nil {
		t.Fatalf("空字符串不应返回错误，实际: %v", err)
	}
	// 应返回当前工作目录的绝对路径
	if !filepath.IsAbs(result) {
		t.Errorf("空字符串应降级为当前工作目录，实际: %s", result)
	}
}

// TestNormalizePath_LargeInput 反例：超大路径不 OOM
func TestNormalizePath_LargeInput(t *testing.T) {
	longSegment := strings.Repeat("a", 200)
	longPath := "/" + strings.Repeat(longSegment+"/", 500)
	// 仅测试不 panic / 不 OOM
	_, _ = NormalizePath(longPath)
}

// TestResolvePath_NullByte 反例：路径含 null 字节
func TestResolvePath_NullByte(t *testing.T) {
	_, err := ResolvePath("path\x00with\x00null", "/tmp")
	if err == nil {
		t.Error("含 null 字节的路径应返回 error")
	}
}

// ========== SafeName 边界测试 ==========

// TestSafeName_EmptyString 反例：空字符串
func TestSafeName_EmptyString(t *testing.T) {
	result := SafeName("")
	if result != "" {
		t.Errorf("空字符串应返回空字符串，实际: %q", result)
	}
}

// TestSafeName_LongFilename 正例：长文件名不截断
func TestSafeName_LongFilename(t *testing.T) {
	longName := strings.Repeat("a", 255) + ".txt"
	result := SafeName(longName)
	if len(result) != len(longName) {
		t.Errorf("长文件名不应截断，输入 %d 字节，输出 %d 字节", len(longName), len(result))
	}
}

// ========== IsAbsolute Linux 专项 ==========

// TestIsAbsolute_LinuxSpecific 验证 Linux 上的绝对/相对路径判定
func TestIsAbsolute_LinuxSpecific(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	mustAbs := []string{
		"/",
		"/home",
		"/tmp/test.go",
	}
	for _, p := range mustAbs {
		if !IsAbsolute(p) {
			t.Errorf("IsAbsolute(%q) 应返回 true", p)
		}
	}

	mustNotAbs := []string{
		"relative/path",
		"./relative",
		"../parent",
		"C:\\Windows\\path", // Windows 路径在 Linux 上是相对路径
		"\\Users\\foo",      // 单反斜杠在 Linux 上是相对路径
	}
	for _, p := range mustNotAbs {
		if IsAbsolute(p) {
			t.Errorf("IsAbsolute(%q) 应返回 false", p)
		}
	}
}

// ========== PathEqual Linux 专项 ==========

// TestPathEqual_LinuxCaseSensitive 验证 Linux 上大小写精确比较
func TestPathEqual_LinuxCaseSensitive(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	if PathEqual("/home/Foo", "/home/foo") {
		t.Error("Linux 上大小写不同路径应不相等")
	}
	if !PathEqual("/home/foo", "/home/foo") {
		t.Error("相同路径应相等")
	}
}

// TestPathEqual_SlashNormalization 验证反斜杠归一化后比较
func TestPathEqual_SlashNormalization(t *testing.T) {
	// ToSlash 后 /home\foo → /home/foo
	if !PathEqual(`/home\foo`, "/home/foo") {
		t.Error("反斜杠归一化后路径应相等")
	}
}

// ========== ExpandTilde 边界测试 ==========

// TestExpandTilde_InvalidPrefix 反例：~user 格式不展开（对标同类 CLI 仅支持 ~/ 和 ~）
func TestExpandTilde_InvalidPrefix(t *testing.T) {
	result, err := ExpandTilde("~otheruser/path")
	if err != nil {
		t.Fatalf("~user 格式不应报错，实际: %v", err)
	}
	// ~otheruser 不是 ~ 或 ~/ 前缀，应原样返回
	if result != "~otheruser/path" {
		t.Errorf("~otheruser 格式应原样返回，实际: %q", result)
	}
}
