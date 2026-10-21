// Package image — M23-01 二分搜索 quality 测试
package image

import (
	"image"
	"image/color"
	"testing"
)

// TestBinarySearchQuality_Basic 测试基本二分搜索（M23-01）。
func TestBinarySearchQuality_Basic(t *testing.T) {
	// 创建一个测试图像
	img := createTestImage(100, 100)
	cfg := DefaultQualitySearchConfig(ImageTargetRawSize)

	data, q, err := BinarySearchQuality(img, cfg)
	if err != nil {
		t.Fatalf("BinarySearchQuality error: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty data")
	}
	if q < cfg.MinQuality || q > cfg.MaxQuality {
		t.Errorf("quality %d out of range [%d, %d]", q, cfg.MinQuality, cfg.MaxQuality)
	}
}

// TestBinarySearchQuality_SmallTarget 测试小目标大小（M23-01）。
func TestBinarySearchQuality_SmallTarget(t *testing.T) {
	img := createTestImage(200, 200)
	cfg := QualitySearchConfig{
		TargetSize:    1000, // 很小的目标，应该选低 quality
		MinQuality:    10,
		MaxQuality:    90,
		MaxIterations: 7,
	}

	data, q, err := BinarySearchQuality(img, cfg)
	if err != nil {
		t.Fatalf("BinarySearchQuality error: %v", err)
	}
	if len(data) > cfg.TargetSize && q != cfg.MinQuality {
		t.Errorf("expected min quality for small target, got q=%d", q)
	}
}

// TestBinarySearchQuality_LargeTarget 测试大目标大小（M23-01）。
func TestBinarySearchQuality_LargeTarget(t *testing.T) {
	img := createTestImage(50, 50)
	cfg := QualitySearchConfig{
		TargetSize:    10 * 1024 * 1024, // 10MB，几乎任何 quality 都满足
		MinQuality:    10,
		MaxQuality:    90,
		MaxIterations: 7,
	}

	_, q, err := BinarySearchQuality(img, cfg)
	if err != nil {
		t.Fatalf("BinarySearchQuality error: %v", err)
	}
	if q != cfg.MaxQuality {
		t.Errorf("expected max quality for large target, got q=%d", q)
	}
}

// TestCompressWithBinarySearch 测试二分搜索压缩变体（M23-01）。
func TestCompressWithBinarySearch(t *testing.T) {
	// 创建一个较大的 PNG 图像
	img := createTestImage(500, 500)
	data := encodeTestPNG(img)

	out, w, h, err := CompressWithBinarySearch(data, "image/png")
	if err != nil {
		t.Fatalf("CompressWithBinarySearch error: %v", err)
	}
	if len(out) == 0 {
		t.Error("expected non-empty output")
	}
	if w <= 0 || h <= 0 {
		t.Errorf("expected positive dimensions, got w=%d h=%d", w, h)
	}
}

// createTestImage 创建测试图像。
func createTestImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: 128,
				A: 255,
			})
		}
	}
	return img
}

// encodeTestPNG 编码 PNG 用于测试。
func encodeTestPNG(img image.Image) []byte {
	out, _ := encodePNG(img, 6)
	return out
}
