package image

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// 生成测试用 PNG 图片
func makeTestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// 生成测试用 JPEG 图片
func makeTestJPEG(w, h int, quality int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	return buf.Bytes()
}

// 生成测试用 BMP 图片（简单 2x2 24 位 BMP）
func makeTestBMP() []byte {
	return []byte{
		0x42, 0x4D, // "BM" magic
		0x46, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x36, 0x00, 0x00, 0x00,
		0x28, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00,
		0x01, 0x00,
		0x18, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x10, 0x00, 0x00, 0x00,
		0x13, 0x0B, 0x00, 0x00,
		0x13, 0x0B, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0xFF, 0x00, 0x00, 0x00,
		0x00, 0xFF, 0x00, 0x00,
		0x00, 0x00, 0xFF, 0x00,
		0xFF, 0xFF, 0xFF, 0x00,
	}
}

// makeTestBMPLarge 生成指定尺寸的 24 位 BMP 图片（未压缩，体积约 w*h*3 字节）
func makeTestBMPLarge(w, h int) []byte {
	rowSize := ((w*3 + 3) / 4) * 4 // 每行字节数，4 字节对齐
	pixelDataSize := rowSize * h
	fileSize := 54 + pixelDataSize // 54 字节头 + 像素数据

	buf := make([]byte, fileSize)

	// BMP 文件头 (14 字节)
	buf[0] = 0x42 // 'B'
	buf[1] = 0x4D // 'M'
	buf[2] = byte(fileSize)
	buf[3] = byte(fileSize >> 8)
	buf[4] = byte(fileSize >> 16)
	buf[5] = byte(fileSize >> 24)
	buf[10] = 54 // 像素数据偏移

	// DIB 头 (40 字节)
	buf[14] = 40 // DIB 头大小
	buf[18] = byte(w)
	buf[19] = byte(w >> 8)
	buf[20] = byte(w >> 16)
	buf[21] = byte(w >> 24)
	buf[22] = byte(h)
	buf[23] = byte(h >> 8)
	buf[24] = byte(h >> 16)
	buf[25] = byte(h >> 24)
	buf[26] = 1  // 颜色平面数
	buf[28] = 24 // 每像素位数

	// 填充像素数据（渐变色）
	offset := 54
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := offset + y*rowSize + x*3
			buf[idx] = byte(x % 256)         // B
			buf[idx+1] = byte(y % 256)       // G
			buf[idx+2] = byte((x + y) % 256) // R
		}
	}

	return buf
}

// base64Encode 辅助函数
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// TestDetectFormat_PNG 测试 PNG 格式检测
func TestDetectFormat_PNG(t *testing.T) {
	data := makeTestPNG(10, 10)
	format := DetectFormat(data)
	if format != "image/png" {
		t.Errorf("expected image/png, got %s", format)
	}
}

// TestDetectFormat_JPEG 测试 JPEG 格式检测
func TestDetectFormat_JPEG(t *testing.T) {
	data := makeTestJPEG(10, 10, 80)
	format := DetectFormat(data)
	if format != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", format)
	}
}

// TestDetectFormat_BMP 测试 BMP 格式检测
func TestDetectFormat_BMP(t *testing.T) {
	data := makeTestBMP()
	format := DetectFormat(data)
	if format != "image/bmp" {
		t.Errorf("expected image/bmp, got %s", format)
	}
}

// TestDetectFormat_GIF 测试 GIF 格式检测（magic bytes: 47 49 46）
func TestDetectFormat_GIF(t *testing.T) {
	// GIF87a 头部：47 49 46 38 37 61
	data := []byte{0x47, 0x49, 0x46, 0x38, 0x37, 0x61, 0x00, 0x00}
	format := DetectFormat(data)
	if format != "image/gif" {
		t.Errorf("expected image/gif, got %s", format)
	}
}

// TestDetectFormat_WebP 测试 WebP 格式检测（magic bytes: 52 49 46 46 ... 57 45 42 50）
func TestDetectFormat_WebP(t *testing.T) {
	// RIFF....WEBP
	data := []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50}
	format := DetectFormat(data)
	if format != "image/webp" {
		t.Errorf("expected image/webp, got %s", format)
	}
}

// TestDetectFormat_Unknown 测试未知格式检测
func TestDetectFormat_Unknown(t *testing.T) {
	data := []byte("this is not an image")
	format := DetectFormat(data)
	if format != "application/octet-stream" {
		t.Errorf("expected application/octet-stream, got %s", format)
	}
}

// TestDetectFormat_Empty 测试空数据格式检测
func TestDetectFormat_Empty(t *testing.T) {
	data := []byte{}
	format := DetectFormat(data)
	if format != "application/octet-stream" {
		t.Errorf("expected application/octet-stream for empty data, got %s", format)
	}
}

// TestDetectFormat_TooShort 测试数据过短的格式检测
func TestDetectFormat_TooShort(t *testing.T) {
	data := []byte{0x89, 0x50}
	format := DetectFormat(data)
	if format != "application/octet-stream" {
		t.Errorf("expected application/octet-stream for short data, got %s", format)
	}
}

// TestIsBMP 测试 IsBMP 函数
func TestIsBMP(t *testing.T) {
	bmpData := makeTestBMP()
	if !IsBMP(bmpData) {
		t.Error("expected IsBMP to return true for BMP data")
	}

	pngData := makeTestPNG(10, 10)
	if IsBMP(pngData) {
		t.Error("expected IsBMP to return false for PNG data")
	}

	if IsBMP([]byte{}) {
		t.Error("expected IsBMP to return false for empty data")
	}

	if IsBMP([]byte{0x42}) {
		t.Error("expected IsBMP to return false for too short data")
	}
}

// TestConvertToPNG_FromPNG 测试 PNG 转 PNG
func TestConvertToPNG_FromPNG(t *testing.T) {
	pngData := makeTestPNG(10, 10)
	converted, err := ConvertToPNG(pngData)
	if err != nil {
		t.Fatalf("ConvertToPNG failed: %v", err)
	}
	if len(converted) == 0 {
		t.Error("expected non-empty converted data")
	}
	if DetectFormat(converted) != "image/png" {
		t.Error("expected converted data to be PNG")
	}
}

// TestConvertToPNG_FromBMP 测试 BMP 转 PNG
func TestConvertToPNG_FromBMP(t *testing.T) {
	bmpData := makeTestBMP()
	converted, err := ConvertToPNG(bmpData)
	if err != nil {
		t.Fatalf("ConvertToPNG from BMP failed: %v", err)
	}
	if len(converted) == 0 {
		t.Error("expected non-empty converted data")
	}
	if DetectFormat(converted) != "image/png" {
		t.Error("expected converted data to be PNG")
	}
}

// TestConvertToPNG_Invalid 测试无效数据转 PNG
func TestConvertToPNG_Invalid(t *testing.T) {
	_, err := ConvertToPNG([]byte("not an image"))
	if err == nil {
		t.Error("expected error for invalid image data")
	}
}

// TestDecodeConfig_PNG 测试 PNG 尺寸解码
func TestDecodeConfig_PNG(t *testing.T) {
	pngData := makeTestPNG(32, 64)
	cfg, fmtType, err := DecodeConfig(pngData)
	if err != nil {
		t.Fatalf("DecodeConfig failed: %v", err)
	}
	if cfg.Width != 32 || cfg.Height != 64 {
		t.Errorf("expected 32x64, got %dx%d", cfg.Width, cfg.Height)
	}
	if fmtType != "image/png" {
		t.Errorf("expected image/png, got %s", fmtType)
	}
}

// TestDecodeConfig_Invalid 测试无效数据尺寸解码
func TestDecodeConfig_Invalid(t *testing.T) {
	_, _, err := DecodeConfig([]byte("not an image"))
	if err == nil {
		t.Error("expected error for invalid image data")
	}
}

// TestDecodeImage_PNG 测试 PNG 图片解码
func TestDecodeImage_PNG(t *testing.T) {
	pngData := makeTestPNG(16, 16)
	img, fmtType, err := DecodeImage(pngData)
	if err != nil {
		t.Fatalf("DecodeImage failed: %v", err)
	}
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 16 {
		t.Errorf("expected 16x16, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
	if fmtType != "image/png" {
		t.Errorf("expected image/png, got %s", fmtType)
	}
}

// TestDetectFormatFromBase64 测试 base64 格式检测
func TestDetectFormatFromBase64(t *testing.T) {
	pngData := makeTestPNG(10, 10)
	base64Str := base64Encode(pngData)

	format := DetectFormatFromBase64(base64Str)
	if format != "image/png" {
		t.Errorf("expected image/png, got %s", format)
	}

	dataUri := "data:image/png;base64," + base64Str
	format = DetectFormatFromBase64(dataUri)
	if format != "image/png" {
		t.Errorf("expected image/png with data URI, got %s", format)
	}
}
