package tui

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestHandlePaste_Format 测试 HandlePaste 返回格式
// 验证 HandlePaste 返回的字符串符合 [image:mimeType;base64,data] 格式
func TestHandlePaste_Format(t *testing.T) {
	handler := NewImagePasteHandler()
	result := handler.HandlePaste("dGVzdA==", "image/jpeg")

	// 验证返回格式为 [image:mimeType;base64,data]
	if !strings.HasPrefix(result, "[image:image/jpeg;base64,") {
		t.Errorf("HandlePaste result should have correct prefix, got: %s", result)
	}
	if !strings.HasSuffix(result, "]") {
		t.Errorf("HandlePaste result should end with ], got: %s", result)
	}
}

// TestPendingImage 测试 PendingImage 类型
// 验证 PendingImage 结构体的字段赋值和读取
func TestPendingImage(t *testing.T) {
	img := PendingImage{
		MimeType: "image/png",
		Base64:   "aGVsbG8=",
	}
	if img.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want %q", img.MimeType, "image/png")
	}
	if img.Base64 != "aGVsbG8=" {
		t.Errorf("Base64 = %q, want %q", img.Base64, "aGVsbG8=")
	}
}

// TestValidateSize_SmallImage 测试小图片大小验证通过
// 验证 ValidateSize 对小图片返回正确的大小且不报错
func TestValidateSize_SmallImage(t *testing.T) {
	handler := NewImagePasteHandler()

	smallBase64 := "aGVsbG8="
	size, err := handler.ValidateSize(smallBase64)
	if err != nil {
		t.Errorf("Small image should pass validation: %v", err)
	}
	if size <= 0 {
		t.Errorf("Size should be positive, got %d", size)
	}
}

// TestValidateSize_ExceedsLimit 测试超过大小限制的图片rejected
// 验证 ValidateSize 对超大图片返回错误
func TestValidateSize_ExceedsLimit(t *testing.T) {
	handler := NewImagePasteHandler()
	handler.MaxSizeBytes = 10 // 设置很小的限制

	// 生成超过限制的 base64 数据
	largeBase64 := strings.Repeat("A", 100)
	_, err := handler.ValidateSize(largeBase64)
	if err == nil {
		t.Error("Large image should fail validation")
	}
}

// TestEncodeImage 测试图片编码
// 验证 EncodeImage 方法能将原始字节数据编码为 base64 字符串
func TestEncodeImage(t *testing.T) {
	handler := NewImagePasteHandler()
	data := []byte{0x89, 0x50, 0x4E, 0x47} // PNG header
	result := handler.EncodeImage(data)
	if result == "" {
		t.Error("EncodeImage should return non-empty string")
	}
	// 验证 base64 编码正确性：解码后应与原始数据一致
	decoded, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		t.Errorf("EncodeImage result is not valid base64: %v", err)
	}
	if string(decoded) != string(data) {
		t.Errorf("Decoded data mismatch: got %v, want %v", decoded, data)
	}
}

// TestDetectMimeTypeByExt_Comprehensive test passed扩展名检测 MIME 类型（完整覆盖）
// 验证 DetectMimeTypeByExt 对所有支持的扩展名和未知扩展名返回正确结果
func TestDetectMimeTypeByExt_Comprehensive(t *testing.T) {
	handler := NewImagePasteHandler()

	tests := []struct {
		ext      string
		expected string
	}{
		{"png", "image/png"},
		{"jpg", "image/jpeg"},
		{"jpeg", "image/jpeg"},
		{"gif", "image/gif"},
		{"webp", "image/webp"},
		{"bmp", "image/bmp"},
		{"svg", "image/svg+xml"},
		{"unknown", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			result := handler.DetectMimeTypeByExt(tt.ext)
			if result != tt.expected {
				t.Errorf("DetectMimeTypeByExt(%q) = %q, want %q", tt.ext, result, tt.expected)
			}
		})
	}
}
