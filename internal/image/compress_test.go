package image

import (
	"sync"
	"testing"
)

// TestCompress_EarlyReturn 测试小图片直接返回（尺寸+大小都达标）
func TestCompress_EarlyReturn(t *testing.T) {
	pngData := makeTestPNG(10, 10)
	originalLen := len(pngData)

	compressed, w, h, err := Compress(pngData, "image/png")
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}
	if len(compressed) != originalLen {
		t.Errorf("expected early return with same data, got different length: %d vs %d", len(compressed), originalLen)
	}
	if w != 10 || h != 10 {
		t.Errorf("expected 10x10, got %dx%d", w, h)
	}
}

// TestCompress_EmptyData 测试空数据返回错误
func TestCompress_EmptyData(t *testing.T) {
	_, _, _, err := Compress([]byte{}, "image/png")
	if err == nil {
		t.Error("expected error for empty data")
	}
}

// TestCompress_InvalidData 测试无效数据返回错误
func TestCompress_InvalidData(t *testing.T) {
	_, _, _, err := Compress([]byte("not an image"), "image/png")
	if err == nil {
		t.Error("expected error for invalid data")
	}
}

// TestCompress_LargeImage 测试大图片应被压缩（大小超限制但尺寸未超）
func TestCompress_LargeImage(t *testing.T) {
	// 使用 BMP（未压缩），1200x1200 约 4.3MB，超过 ImageTargetRawSize(3.75MB)
	bmpData := makeTestBMPLarge(1200, 1200)
	originalLen := len(bmpData)

	compressed, w, h, err := Compress(bmpData, "image/bmp")
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}
	if len(compressed) >= originalLen {
		t.Errorf("expected compressed data to be smaller: %d vs %d", len(compressed), originalLen)
	}
	if w <= 0 || h <= 0 {
		t.Errorf("expected positive dimensions, got %dx%d", w, h)
	}
	// 验证压缩后大小不超过目标
	if len(compressed) > ImageTargetRawSize {
		t.Errorf("compressed size %d exceeds target %d", len(compressed), ImageTargetRawSize)
	}
}

// TestCompress_DimensionsExceedLimit 测试超大尺寸图片应被缩放
func TestCompress_DimensionsExceedLimit(t *testing.T) {
	pngData := makeTestPNG(ImageMaxWidth+100, ImageMaxHeight+100)

	compressed, w, h, err := Compress(pngData, "image/png")
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}
	if w > ImageMaxWidth {
		t.Errorf("expected width <= %d, got %d", ImageMaxWidth, w)
	}
	if h > ImageMaxHeight {
		t.Errorf("expected height <= %d, got %d", ImageMaxHeight, h)
	}
	if len(compressed) == 0 {
		t.Error("expected non-empty compressed data")
	}
}

// TestCompress_NonSquareAspectDimensions 测试非正方形图片压缩后返回等比缩放的实际尺寸
// 回归验证：策略2-4 应返回 resizeImage 的实际等比缩放结果，而非硬编码 800×800 / 600×600 / 400×400
func TestCompress_NonSquareAspectDimensions(t *testing.T) {
	// 创建一张 2000×1000 的 BMP（未压缩，约 6MB，超过 ImageTargetRawSize 和维度限制）
	// 经步骤4 缩放到 2000×1000 以内，再经策略2-4 进一步压缩
	bmpData := makeTestBMPLarge(2000, 1000)

	_, w, h, err := Compress(bmpData, "image/bmp")
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}
	// 非正方形图片，宽高不应相同（排除硬编码常量的回归）
	if w == h && w > 0 {
		t.Errorf("expected non-square dimensions for 2:1 aspect ratio, got %dx%d (likely hardcoded)", w, h)
	}
	// 宽高比应大致保持 2:1（允许缩放误差）
	if w > 0 && h > 0 {
		ratio := float64(w) / float64(h)
		if ratio < 1.8 || ratio > 2.2 {
			t.Errorf("expected aspect ratio ~2.0, got %.2f (%dx%d)", ratio, w, h)
		}
	}
}

// TestResizeImage_NoResize 测试小图片不缩放
func TestResizeImage_NoResize(t *testing.T) {
	pngData := makeTestPNG(50, 50)
	img, _, err := DecodeImage(pngData)
	if err != nil {
		t.Fatalf("DecodeImage failed: %v", err)
	}

	resized := resizeImage(img, 100, 100)
	if resized.Bounds().Dx() != 50 || resized.Bounds().Dy() != 50 {
		t.Errorf("expected no resize 50x50, got %dx%d", resized.Bounds().Dx(), resized.Bounds().Dy())
	}
}

// TestResizeImage_ScaleDown 测试大图片等比缩放
func TestResizeImage_ScaleDown(t *testing.T) {
	pngData := makeTestPNG(200, 100)
	img, _, err := DecodeImage(pngData)
	if err != nil {
		t.Fatalf("DecodeImage failed: %v", err)
	}

	resized := resizeImage(img, 100, 100)
	dstW := resized.Bounds().Dx()
	dstH := resized.Bounds().Dy()

	if dstW > 100 || dstH > 100 {
		t.Errorf("expected dimensions within 100x100, got %dx%d", dstW, dstH)
	}

	ratio := float64(200) / float64(100)
	dstRatio := float64(dstW) / float64(dstH)
	if abs(dstRatio-ratio) > 0.01 {
		t.Errorf("expected aspect ratio preserved: %.2f vs %.2f", ratio, dstRatio)
	}
}

// TestProgressiveResizing_Success 测试渐进缩放成功
func TestProgressiveResizing_Success(t *testing.T) {
	pngData := makeTestPNG(100, 100)
	img, _, err := DecodeImage(pngData)
	if err != nil {
		t.Fatalf("DecodeImage failed: %v", err)
	}

	targetSize := 50000
	out, w, h, err := progressiveResizing(img, "image/png", targetSize)
	if err != nil {
		t.Fatalf("progressiveResizing failed: %v", err)
	}
	if len(out) == 0 {
		t.Error("expected non-empty output")
	}
	if len(out) > targetSize {
		t.Errorf("expected output size <= %d, got %d", targetSize, len(out))
	}
	// 验证返回的宽高是实际缩放后的尺寸
	if w <= 0 || h <= 0 {
		t.Errorf("expected positive dimensions, got %dx%d", w, h)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestCompress_Concurrent 测试并发压缩无 data race（反例）
// 验证 Compress 是无状态纯函数，多图并发安全
// 需 go test -race ./internal/image/ -run TestCompress_Concurrent 验证（CI 补跑）
func TestCompress_Concurrent(t *testing.T) {
	data := makeTestPNG(500, 500)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := Compress(data, "image/png")
			if err != nil {
				t.Errorf("concurrent Compress failed: %v", err)
			}
		}()
	}
	wg.Wait()
	// 若有 data race，go test -race 会失败
}

// TestCompress_AllStrategiesFailed 测试所有压缩策略都失败的场景（反例）
// 由于正常图片总能被策略4 Ultra JPEG 兜底编码成功，
// 此用例通过构造无法解码的字节流触发 DecodeConfig 失败路径，
// 验证 Compress 返回 error 而非 panic
func TestCompress_AllStrategiesFailed(t *testing.T) {
	// 构造看似 PNG 头但实际无法解码的字节流（magic bytes 后跟无效数据）
	// 这样能通过 DetectFormat 检测（外部 Paste 拒绝）但 DecodeConfig 解码失败
	// Compress 内部 DecodeConfig 失败会返回 error
	fakePNG := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG magic
		0x00, 0x00, 0x00, 0x00, // IHDR length = 0（无效）
		0x49, 0x48, 0x44, 0x52, // "IHDR"
	}
	_, _, _, err := Compress(fakePNG, "image/png")
	if err == nil {
		t.Error("expected error when all compression strategies fail")
	}
}
