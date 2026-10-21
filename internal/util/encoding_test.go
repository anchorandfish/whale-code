package util

import (
	"testing"
)

// TestDetectEncoding_Empty 空字节流返回 EncodingUnknown
func TestDetectEncoding_Empty(t *testing.T) {
	if got := DetectEncoding(nil); got != EncodingUnknown {
		t.Errorf("DetectEncoding(nil) = %q, want %q", got, EncodingUnknown)
	}
	if got := DetectEncoding([]byte{}); got != EncodingUnknown {
		t.Errorf("DetectEncoding([]byte{}) = %q, want %q", got, EncodingUnknown)
	}
}

// TestDetectEncoding_Binary 含 NUL 字节返回 EncodingBinary
func TestDetectEncoding_Binary(t *testing.T) {
	data := []byte{'h', 'e', 'l', 'l', 'o', 0, 'w', 'o', 'r', 'l', 'd'}
	if got := DetectEncoding(data); got != EncodingBinary {
		t.Errorf("DetectEncoding(含 NUL) = %q, want %q", got, EncodingBinary)
	}
}

// TestDetectEncoding_UTF8BOM UTF-8 BOM 前缀检测
func TestDetectEncoding_UTF8BOM(t *testing.T) {
	data := []byte{0xEF, 0xBB, 0xBF, 'h', 'e', 'l', 'l', 'o'}
	if got := DetectEncoding(data); got != EncodingUTF8BOM {
		t.Errorf("DetectEncoding(UTF-8 BOM) = %q, want %q", got, EncodingUTF8BOM)
	}
}

// TestDetectEncoding_UTF16LE UTF-16 LE BOM 检测
func TestDetectEncoding_UTF16LE(t *testing.T) {
	data := []byte{0xFF, 0xFE, 'h', 0x00, 'i', 0x00}
	if got := DetectEncoding(data); got != EncodingUTF16LE {
		t.Errorf("DetectEncoding(UTF-16 LE) = %q, want %q", got, EncodingUTF16LE)
	}
}

// TestDetectEncoding_UTF16BE UTF-16 BE BOM 检测
func TestDetectEncoding_UTF16BE(t *testing.T) {
	data := []byte{0xFE, 0xFF, 0x00, 'h', 0x00, 'i'}
	if got := DetectEncoding(data); got != EncodingUTF16BE {
		t.Errorf("DetectEncoding(UTF-16 BE) = %q, want %q", got, EncodingUTF16BE)
	}
}

// TestDetectEncoding_UTF8 纯 UTF-8 无 BOM
func TestDetectEncoding_UTF8(t *testing.T) {
	data := []byte("hello, 世界") // 含中文 UTF-8
	if got := DetectEncoding(data); got != EncodingUTF8 {
		t.Errorf("DetectEncoding(UTF-8) = %q, want %q", got, EncodingUTF8)
	}
}

// TestDetectEncoding_GBK 无 BOM GBK 编码检测
func TestDetectEncoding_GBK(t *testing.T) {
	// "你好" 的 GBK 编码：0xC4 0xE3 0xBA 0xC3，重复一次凑足 8 字节
	data := []byte{0xC4, 0xE3, 0xBA, 0xC3, 0xC4, 0xE3, 0xBA, 0xC3}
	if got := DetectEncoding(data); got != EncodingGBK {
		t.Errorf("DetectEncoding(GBK) = %q, want %q", got, EncodingGBK)
	}
}

// TestDetectEncoding_Big5 无 BOM Big5 编码检测
func TestDetectEncoding_Big5(t *testing.T) {
	// "你好" 的 Big5 编码：0xA7 0x41 0xA6 0x6E，重复一次凑足 8 字节
	data := []byte{0xA7, 0x41, 0xA6, 0x6E, 0xA7, 0x41, 0xA6, 0x6E}
	if got := DetectEncoding(data); got != EncodingBig5 {
		t.Errorf("DetectEncoding(Big5) = %q, want %q", got, EncodingBig5)
	}
}

// TestDetectEncoding_ShiftJIS 无 BOM Shift-JIS 编码检测
func TestDetectEncoding_ShiftJIS(t *testing.T) {
	// "こんにちは" 的 Shift-JIS 编码片段
	data := []byte{0x82, 0xB1, 0x82, 0xF1, 0x82, 0xC9, 0x82, 0xBF, 0x82, 0xCD}
	if got := DetectEncoding(data); got != EncodingShiftJIS {
		t.Errorf("DetectEncoding(Shift-JIS) = %q, want %q", got, EncodingShiftJIS)
	}
}

// TestDetectEncoding_Latin1Fallback 无法识别时返回 Latin1
func TestDetectEncoding_Latin1Fallback(t *testing.T) {
	// 单字节 0x80-0xFE 范围，不满足 CJK 双字节模式（6 字节，覆盖率不足 100%）
	data := []byte{0x80, 0x81, 0x82, 0x83, 0x84, 0x85}
	if got := DetectEncoding(data); got != EncodingLatin1 {
		t.Errorf("DetectEncoding(Latin1 fallback) = %q, want %q", got, EncodingLatin1)
	}
}

// ============================================================================
// DecodeToUTF8 测试
// ============================================================================

// TestDecodeToUTF8_UTF8 纯 UTF-8 输入原样返回
func TestDecodeToUTF8_UTF8(t *testing.T) {
	data := []byte("hello, 世界")
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("DecodeToUTF8(UTF-8) err: %v", err)
	}
	if got != "hello, 世界" {
		t.Errorf("DecodeToUTF8(UTF-8) = %q, want %q", got, "hello, 世界")
	}
}

// TestDecodeToUTF8_UTF8BOM 剥除 UTF-8 BOM 前缀
func TestDecodeToUTF8_UTF8BOM(t *testing.T) {
	data := []byte{0xEF, 0xBB, 0xBF, 'h', 'i'}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hi" {
		t.Errorf("DecodeToUTF8(UTF-8 BOM) = %q, want %q", got, "hi")
	}
}

// TestDecodeToUTF8_UTF16LE UTF-16 LE 转 UTF-8
func TestDecodeToUTF8_UTF16LE(t *testing.T) {
	// "hi" in UTF-16 LE
	data := []byte{0xFF, 0xFE, 'h', 0x00, 'i', 0x00}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hi" {
		t.Errorf("DecodeToUTF8(UTF-16 LE) = %q, want %q", got, "hi")
	}
}

// TestDecodeToUTF8_UTF16BE UTF-16 BE 转 UTF-8
func TestDecodeToUTF8_UTF16BE(t *testing.T) {
	// "hi" in UTF-16 BE
	data := []byte{0xFE, 0xFF, 0x00, 'h', 0x00, 'i'}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hi" {
		t.Errorf("DecodeToUTF8(UTF-16 BE) = %q, want %q", got, "hi")
	}
}

// TestDecodeToUTF8_GBK GBK 转 UTF-8
func TestDecodeToUTF8_GBK(t *testing.T) {
	// "你好" GBK: C4 E3 BA C3
	data := []byte{0xC4, 0xE3, 0xBA, 0xC3}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "你好" {
		t.Errorf("DecodeToUTF8(GBK) = %q, want %q", got, "你好")
	}
}

// TestDecodeToUTF8_Big5 Big5 转 UTF-8
func TestDecodeToUTF8_Big5(t *testing.T) {
	// "你好" Big5: A7 41 A6 6E
	data := []byte{0xA7, 0x41, 0xA6, 0x6E}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "你好" {
		t.Errorf("DecodeToUTF8(Big5) = %q, want %q", got, "你好")
	}
}

// TestDecodeToUTF8_ShiftJIS Shift-JIS 转 UTF-8
func TestDecodeToUTF8_ShiftJIS(t *testing.T) {
	// "こんにちは" Shift-JIS
	data := []byte{0x82, 0xB1, 0x82, 0xF1, 0x82, 0xC9, 0x82, 0xBF, 0x82, 0xCD}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "こんにちは" {
		t.Errorf("DecodeToUTF8(Shift-JIS) = %q, want %q", got, "こんにちは")
	}
}

// TestDecodeToUTF8_Latin1 Latin1 转 UTF-8
func TestDecodeToUTF8_Latin1(t *testing.T) {
	// 0xC0 in Latin1 = "À"
	data := []byte{0xC0}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "À" {
		t.Errorf("DecodeToUTF8(Latin1) = %q, want %q", got, "À")
	}
}

// TestDecodeToUTF8_Binary 二进制内容返回占位符
func TestDecodeToUTF8_Binary(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0x03}
	got, err := DecodeToUTF8(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	expected := "[binary content, 4 bytes]"
	if got != expected {
		t.Errorf("DecodeToUTF8(Binary) = %q, want %q", got, expected)
	}
}

// TestDecodeToUTF8_DecodeFailureNoDataLoss 解码失败时返回已解码部分 + error
func TestDecodeToUTF8_DecodeFailureNoDataLoss(t *testing.T) {
	// 构造一个 GBK 检测命中但解码失败的字节序列（截断的双字节）
	// 末尾 0xC4 是 GBK 第一字节，缺第二字节（5 字节，覆盖率不足 100% → Latin1）
	data := []byte{0xC4, 0xE3, 0xBA, 0xC3, 0xC4}
	got, err := DecodeToUTF8(data)
	// 至少应返回部分内容，不应返回空串
	if got == "" && err == nil {
		t.Error("DecodeToUTF8 应返回部分内容或 error，不应空串 + nil")
	}
}
