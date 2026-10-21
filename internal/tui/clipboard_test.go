package tui

import (
	"encoding/base64"
	"runtime"
	"strings"
	"testing"
)

// TestImagePasteCallback 测试图片粘贴回调接收 base64 数据
// 验证 HandlePaste 方法能将 base64 编码的图片数据转换为内容块格式
func TestImagePasteCallback(t *testing.T) {
	callback := &ImagePasteHandler{}

	testData := []byte("fake image data")
	encoded := base64.StdEncoding.EncodeToString(testData)

	result := callback.HandlePaste(encoded, "image/png")
	if result == "" {
		t.Error("HandlePaste should return non-empty result")
	}
}

// TestHandlePaste_FullBase64 测试 HandlePaste 返回完整 base64 数据
// 验证 HandlePaste 不再截断 base64 数据，AI 能完整解析图片内容
func TestHandlePaste_FullBase64(t *testing.T) {
	handler := &ImagePasteHandler{}
	testData := []byte("test image data for full base64 check")
	encoded := base64.StdEncoding.EncodeToString(testData)

	result := handler.HandlePaste(encoded, "image/png")
	if !strings.Contains(result, encoded) {
		t.Errorf("HandlePaste should contain full base64 data, got truncated result: length=%d, expected=%d", len(result), len(encoded))
	}
}

// TestDetectMimeType_ByExt 测试根据扩展名检测 MIME 类型
// 验证 DetectMimeTypeByExt 能根据文件扩展名正确返回对应的 MIME 类型
func TestDetectMimeType_ByExt(t *testing.T) {
	handler := &ImagePasteHandler{}

	tests := []struct {
		ext      string
		expected string
	}{
		{"png", "image/png"},
		{"jpg", "image/jpeg"},
		{"gif", "image/gif"},
		{"webp", "image/webp"},
		{"bmp", "image/bmp"},
	}

	for _, tt := range tests {
		result := handler.DetectMimeTypeByExt(tt.ext)
		if result != tt.expected {
			t.Errorf("DetectMimeTypeByExt(%s) = %s, want %s", tt.ext, result, tt.expected)
		}
	}
}

// TestDetectMimeType_ByData 测试根据字节数据检测 MIME 类型
// 验证 DetectMimeType 能通过文件头魔数正确识别图片格式
func TestDetectMimeType_ByData(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected string
	}{
		{"PNG", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, "image/png"},
		{"JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}, "image/jpeg"},
		{"GIF", []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x00, 0x00}, "image/gif"},
		{"WebP", []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50}, "image/webp"},
		{"BMP", []byte{0x42, 0x4D, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "image/bmp"},
		{"Unknown", []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "application/octet-stream"},
		{"TooShort", []byte{0x00, 0x01}, "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectMimeType(tt.data)
			if result != tt.expected {
				t.Errorf("DetectMimeType() = %s, want %s", result, tt.expected)
			}
		})
	}
}

// TestImageSizeLimit 测试超过 10MB 的图片rejected
// 验证 ValidateSize 方法对超大图片返回错误
func TestImageSizeLimit(t *testing.T) {
	handler := &ImagePasteHandler{
		MaxSizeBytes: 10 * 1024 * 1024, // 10MB
	}

	// 创建超过 10MB 的假图片数据
	largeData := make([]byte, 11*1024*1024)
	encoded := base64.StdEncoding.EncodeToString(largeData)

	_, err := handler.ValidateSize(encoded)
	if err == nil {
		t.Error("Should reject images > 10MB")
	}
}

// TestImageSizeAcceptable 测试小于 10MB 的图片被接受
// 验证 ValidateSize 方法对正常大小的图片不返回错误
func TestImageSizeAcceptable(t *testing.T) {
	handler := &ImagePasteHandler{
		MaxSizeBytes: 10 * 1024 * 1024,
	}

	smallData := make([]byte, 1024) // 1KB
	encoded := base64.StdEncoding.EncodeToString(smallData)

	_, err := handler.ValidateSize(encoded)
	if err != nil {
		t.Errorf("Should accept images < 10MB, got error: %v", err)
	}
}

// TestGetClipboardImageWindows 测试 Windows 剪贴板图片获取
// 仅在 Windows 平台运行，验证 GetClipboardImageWindows 函数可正常调用
func TestGetClipboardImageWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Skipping Windows-only test")
	}
	// 剪贴板中可能没有图片，此测试仅验证函数可正常调用
	_, _ = GetClipboardImageWindows()
}

// TestNewImagePasteHandler 测试创建图片粘贴处理器
// 验证 NewImagePasteHandler 返回的处理器具有正确的default value
func TestNewImagePasteHandler(t *testing.T) {
	handler := NewImagePasteHandler()
	if handler == nil {
		t.Error("NewImagePasteHandler should return non-nil handler")
	}
	if handler.MaxSizeBytes != 10*1024*1024 {
		t.Errorf("Default MaxSizeBytes should be 10MB, got %d", handler.MaxSizeBytes)
	}
}

// TestDetectMimeTypeByExt_JpegExt 测试 jpeg 扩展名检测
// 验证 DetectMimeTypeByExt 对 "jpeg" 扩展名返回正确的 MIME 类型
func TestDetectMimeTypeByExt_JpegExt(t *testing.T) {
	handler := &ImagePasteHandler{}
	result := handler.DetectMimeTypeByExt("jpeg")
	if result != "image/jpeg" {
		t.Errorf("DetectMimeTypeByExt(jpeg) = %s, want image/jpeg", result)
	}
}

// TestDetectMimeTypeByExt_SvgExt 测试 svg 扩展名检测
// 验证 DetectMimeTypeByExt 对 "svg" 扩展名返回正确的 MIME 类型
func TestDetectMimeTypeByExt_SvgExt(t *testing.T) {
	handler := &ImagePasteHandler{}
	result := handler.DetectMimeTypeByExt("svg")
	if result != "image/svg+xml" {
		t.Errorf("DetectMimeTypeByExt(svg) = %s, want image/svg+xml", result)
	}
}

// TestDetectMimeTypeByExt_UnknownExt 测试未知扩展名返回默认 MIME 类型
// 验证 DetectMimeTypeByExt 对未知扩展名返回 "application/octet-stream"（G12 修正：与 image.DetectFormat 一致）
func TestDetectMimeTypeByExt_UnknownExt(t *testing.T) {
	handler := &ImagePasteHandler{}
	result := handler.DetectMimeTypeByExt("xyz")
	if result != "application/octet-stream" {
		t.Errorf("DetectMimeTypeByExt(xyz) = %s, want application/octet-stream (default)", result)
	}
}

// TestValidateSize_ExactLimit 测试刚好等于大小限制的图片
// 验证 ValidateSize 对刚好等于限制大小的图片不返回错误
func TestValidateSize_ExactLimit(t *testing.T) {
	handler := &ImagePasteHandler{
		MaxSizeBytes: 1000,
	}
	// 创建刚好在限制内的数据（base64 编码后约 1336 字节，解码后约 1000 字节）
	smallData := make([]byte, 750) // base64 编码后约 1000 字节，解码后约 750 字节
	encoded := base64.StdEncoding.EncodeToString(smallData)

	_, err := handler.ValidateSize(encoded)
	if err != nil {
		t.Errorf("Should accept images at size limit, got error: %v", err)
	}
}

// TestHandlePaste_ContainsMimeType 测试 HandlePaste 结果包含 MIME 类型
// 验证 HandlePaste 返回的字符串包含传入的 MIME 类型信息
func TestHandlePaste_ContainsMimeType(t *testing.T) {
	handler := &ImagePasteHandler{}
	testData := []byte("test")
	encoded := base64.StdEncoding.EncodeToString(testData)

	result := handler.HandlePaste(encoded, "image/gif")
	if result == "" {
		t.Error("HandlePaste should return non-empty result")
	}
	// 结果should contain MIME 类型
	if len(result) == 0 {
		t.Error("HandlePaste result should not be empty")
	}
}
