package image

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPaste_Success 测试 PNG 图片应成功返回 ImageRef
func TestPaste_Success(t *testing.T) {
	pngData := makeTestPNG(50, 50)
	ref, err := Paste(pngData)
	if err != nil {
		t.Fatalf("Paste failed: %v", err)
	}
	if ref.MediaType != "image/png" {
		t.Errorf("expected media type image/png, got %s", ref.MediaType)
	}
	if ref.Width != 50 || ref.Height != 50 {
		t.Errorf("expected 50x50, got %dx%d", ref.Width, ref.Height)
	}
	if len(ref.Data) == 0 {
		t.Error("expected non-empty data")
	}
	if len(ref.Base64) == 0 {
		t.Error("expected non-empty base64")
	}
	if ref.SourceSize != int64(len(pngData)) {
		t.Errorf("expected source size %d, got %d", len(pngData), ref.SourceSize)
	}
}

// TestPaste_EmptyData 测试空数据返回错误
func TestPaste_EmptyData(t *testing.T) {
	_, err := Paste([]byte{})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

// TestPaste_UnknownFormat 测试非图片数据返回错误
func TestPaste_UnknownFormat(t *testing.T) {
	_, err := Paste([]byte("this is not an image"))
	if err == nil {
		t.Error("expected error for unknown format")
	}
}

// TestPaste_BMPConversion 测试 BMP 图片自动转 PNG
func TestPaste_BMPConversion(t *testing.T) {
	bmpData := makeTestBMP()
	ref, err := Paste(bmpData)
	if err != nil {
		t.Fatalf("Paste with BMP failed: %v", err)
	}
	if ref.MediaType != "image/png" {
		t.Errorf("expected media type image/png after BMP conversion, got %s", ref.MediaType)
	}
	if DetectFormat(ref.Data) != "image/png" {
		t.Error("expected converted data to be PNG format")
	}
}

// TestPaste_EstTokens 测试返回的 EstTokens 应 > 0
func TestPaste_EstTokens(t *testing.T) {
	pngData := makeTestPNG(20, 20)
	ref, err := Paste(pngData)
	if err != nil {
		t.Fatalf("Paste failed: %v", err)
	}
	if ref.EstTokens <= 0 {
		t.Errorf("expected EstTokens > 0, got %d", ref.EstTokens)
	}
}

// TestPaste_MediaTypeConsistentWithActualData 测试压缩后 MediaType 与实际数据格式一致
// 回归验证：WebP/GIF 等格式经压缩策略后可能被转码为 PNG/JPEG，
// MediaType 必须反映实际编码格式而非输入格式
func TestPaste_MediaTypeConsistentWithActualData(t *testing.T) {
	pngData := makeTestPNG(50, 50)
	ref, err := Paste(pngData)
	if err != nil {
		t.Fatalf("Paste failed: %v", err)
	}
	// MediaType 应与 Data 的实际格式一致
	actualFormat := DetectFormat(ref.Data)
	if actualFormat != "application/octet-stream" && ref.MediaType != actualFormat {
		t.Errorf("MediaType %s doesn't match actual data format %s", ref.MediaType, actualFormat)
	}
}

// TestFromPath_NonExistent 测试不存在的文件返回错误
func TestFromPath_NonExistent(t *testing.T) {
	_, err := FromPath("nonexistent_file.png")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// TestFromPath_Success 测试存在的 PNG 文件应成功
func TestFromPath_Success(t *testing.T) {
	tmpDir := t.TempDir()
	tmpPath := filepath.Join(tmpDir, "test.png")

	pngData := makeTestPNG(30, 30)
	err := os.WriteFile(tmpPath, pngData, 0644)
	if err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	ref, err := FromPath(tmpPath)
	if err != nil {
		t.Fatalf("FromPath failed: %v", err)
	}
	if ref.MediaType != "image/png" {
		t.Errorf("expected media type image/png, got %s", ref.MediaType)
	}
	if ref.Width != 30 || ref.Height != 30 {
		t.Errorf("expected 30x30, got %dx%d", ref.Width, ref.Height)
	}
	if ref.SourceSize != int64(len(pngData)) {
		t.Errorf("expected source size %d, got %d", len(pngData), ref.SourceSize)
	}
}
