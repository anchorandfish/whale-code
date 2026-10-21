package util

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// 反例测试：边界输入（空字符串/nil/越界索引）
// ============================================================================

// TestIsBinaryExt_EmptyString 空字符串路径返回 false
func TestIsBinaryExt_EmptyString(t *testing.T) {
	if IsBinaryExt("") {
		t.Error("IsBinaryExt(\"\") = true, want false")
	}
}

// TestIsBinaryExt_OnlyDot 仅有点号无扩展名返回 false
func TestIsBinaryExt_OnlyDot(t *testing.T) {
	if IsBinaryExt("file.") {
		t.Error("IsBinaryExt(\"file.\") = true, want false")
	}
}

// TestIsBinaryExt_NotInBinaryExts 非二进制扩展名返回 false
func TestIsBinaryExt_NotInBinaryExts(t *testing.T) {
	exts := []string{".go", ".txt", ".md", ".json", ".yaml", ".ts", ".py", ".rs"}
	for _, ext := range exts {
		if IsBinaryExt("test" + ext) {
			t.Errorf("IsBinaryExt(%q) = true, want false", "test"+ext)
		}
	}
}

// TestIsBinary_EmptyPath 空路径返回 false（os.Open 失败）
func TestIsBinary_EmptyPath(t *testing.T) {
	if IsBinary("") {
		t.Error("IsBinary(\"\") = true, want false")
	}
}

// TestIsBinary_DirectoryPath 目录路径返回 false（os.Open 成功但 Read 可能失败）
func TestIsBinary_DirectoryPath(t *testing.T) {
	dir := t.TempDir()
	// 目录打开成功但读取行为不确定，不应 panic
	_ = IsBinary(dir)
}

// TestDetectEncoding_NilInput nil 输入返回 EncodingUnknown
func TestDetectEncoding_NilInput(t *testing.T) {
	if got := DetectEncoding(nil); got != EncodingUnknown {
		t.Errorf("DetectEncoding(nil) = %q, want %q", got, EncodingUnknown)
	}
}

// TestDetectEncoding_SingleByte 单字节数据返回 Latin1（非 NUL）
func TestDetectEncoding_SingleByte(t *testing.T) {
	if got := DetectEncoding([]byte{0x41}); got != EncodingUTF8 {
		t.Errorf("DetectEncoding(0x41) = %q, want %q", got, EncodingUTF8)
	}
}

// TestDetectEncoding_LargeInput 超大 UTF-8 输入正常检测
func TestDetectEncoding_LargeInput(t *testing.T) {
	data := []byte(strings.Repeat("hello 世界 ", 1000)) // ~10KB UTF-8
	if got := DetectEncoding(data); got != EncodingUTF8 {
		t.Errorf("DetectEncoding(10KB UTF-8) = %q, want %q", got, EncodingUTF8)
	}
}

// TestDetectEncoding_NULAt8KBBoundary NUL 字节恰在 8KB 边界处
func TestDetectEncoding_NULAt8KBBoundary(t *testing.T) {
	// 8191 字节非 NUL + 1 字节 NUL = 8192 字节，NUL 在扫描范围内
	data := make([]byte, 8192)
	for i := 0; i < 8191; i++ {
		data[i] = 0x41 // 'A'
	}
	data[8191] = 0x00
	if got := DetectEncoding(data); got != EncodingBinary {
		t.Errorf("DetectEncoding(NUL at 8KB) = %q, want %q", got, EncodingBinary)
	}
}

// TestDetectEncoding_NULBeyond8KB NUL 字节超出 8KB 扫描范围不被检测
func TestDetectEncoding_NULBeyond8KB(t *testing.T) {
	// 8192 字节非 NUL + 1 字节 NUL = 8193 字节，NUL 超出扫描范围
	data := make([]byte, 8193)
	for i := 0; i < 8192; i++ {
		data[i] = 0x41
	}
	data[8192] = 0x00
	// 8192 字节 'A' 是有效 UTF-8，NUL 超出扫描范围
	if got := DetectEncoding(data); got != EncodingUTF8 {
		t.Errorf("DetectEncoding(NUL beyond 8KB) = %q, want %q", got, EncodingUTF8)
	}
}

// TestDecodeToUTF8_EmptyData 空数据返回 error
func TestDecodeToUTF8_EmptyData(t *testing.T) {
	got, err := DecodeToUTF8(nil)
	if err == nil {
		t.Error("err = nil, want non-nil（空数据应报错）")
	}
	if got != "" {
		t.Errorf("got = %q, want empty string", got)
	}
}

// TestDecodeToUTF8_SingleByte 单字节 Latin1 解码
func TestDecodeToUTF8_SingleByte(t *testing.T) {
	got, err := DecodeToUTF8([]byte{0xC0})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "À" {
		t.Errorf("got = %q, want %q", got, "À")
	}
}

// TestReadFileSafe_EmptyPath 空路径返回错误
func TestReadFileSafe_EmptyPath(t *testing.T) {
	_, err := ReadFileSafe("", 0)
	if err == nil {
		t.Error("err = nil, want non-nil（空路径应报错）")
	}
}

// TestReadFileSafe_DirectoryPath 目录路径返回错误
func TestReadFileSafe_DirectoryPath(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadFileSafe(dir, 0)
	if err == nil {
		t.Error("err = nil, want non-nil（目录路径应报错）")
	}
}

// TestReadFileSafe_NegativeMaxBytes 负数 maxBytes 等同不限制
func TestReadFileSafe_NegativeMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neg.txt")
	data := []byte("hello")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, -1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Skipped {
		t.Error("Skipped = true, want false（负数 maxBytes 应不限制）")
	}
	if result.Content != "hello" {
		t.Errorf("Content = %q, want %q", result.Content, "hello")
	}
}

// TestReadFileSafe_ZeroByteFile 零字节文件
func TestReadFileSafe_ZeroByteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zero.txt")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Content != "" {
		t.Errorf("Content = %q, want empty", result.Content)
	}
	if result.Bytes != 0 {
		t.Errorf("Bytes = %d, want 0", result.Bytes)
	}
}

// TestPathsEqual_BothEmpty 两个空路径返回 true（Abs 后相同）
func TestPathsEqual_BothEmpty(t *testing.T) {
	if !PathsEqual("", "") {
		t.Error("PathsEqual(\"\", \"\") = false, want true")
	}
}

// TestPathsEqual_SingleDot 单点路径与当前目录等价
func TestPathsEqual_SingleDot(t *testing.T) {
	if !PathsEqual(".", ".") {
		t.Error("PathsEqual(\".\", \".\") = false, want true")
	}
}

// TestPathsEqual_EmptyAndDot 空路径与点号路径经过 Abs 后等价
func TestPathsEqual_EmptyAndDot(t *testing.T) {
	if !PathsEqual("", ".") {
		t.Error("PathsEqual(\"\", \".\") = false, want true（Abs 后均为 cwd）")
	}
}

// TestPathsEqual_LongPath 长路径正常比较
func TestPathsEqual_LongPath(t *testing.T) {
	dir := t.TempDir()
	deep := strings.Repeat("subdir/", 20) + "file.txt"
	p1 := filepath.Join(dir, deep)
	p2 := filepath.Join(dir, deep)
	if !PathsEqual(p1, p2) {
		t.Error("PathsEqual(相同长路径) = false, want true")
	}
}

// ============================================================================
// 并发安全测试
// ============================================================================

// TestBinaryExts_ConcurrentRead 多 goroutine 并发读取 BinaryExts map
func TestBinaryExts_ConcurrentRead(t *testing.T) {
	t.Parallel()
	const goroutines = 100
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 并发读取 map
			_ = BinaryExts[".exe"]
			_ = BinaryExts[".png"]
			_ = BinaryExts[".go"]
			_ = IsBinaryExt("test.exe")
		}()
	}
	wg.Wait()
	// 如果 map 被并发写入会触发 panic，纯读取不触发
}

// TestPathsEqual_ConcurrentSafe 多 goroutine 并发调用 PathsEqual
func TestPathsEqual_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.txt")
	p2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(p1, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	const goroutines = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			PathsEqual(p1, p2)
			PathsEqual(p1, p1)
		}()
	}
	wg.Wait()
}

// TestDetectEncoding_ConcurrentSafe 多 goroutine 并发调用 DetectEncoding
func TestDetectEncoding_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	data := []byte("hello, 世界")
	const goroutines = 100
	var wg sync.WaitGroup
	errs := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if DetectEncoding(data) != EncodingUTF8 {
				errs[idx] = true
			}
		}(i)
	}
	wg.Wait()

	for i, failed := range errs {
		if failed {
			t.Errorf("goroutine %d: 检测结果不正确", i)
		}
	}
}
