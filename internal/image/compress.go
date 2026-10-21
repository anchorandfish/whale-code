package image

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
)

// Compress 4 段式压缩 fallback（对标同类 CLI compressImageBuffer L498-577）
// 行为规约：
//  1. 策略1保格式渐进缩放：缩放因子 [1.0, 0.75, 0.5, 0.25] + 格式优化
//  2. 策略2 PNG 优化：resize 到 800×800 + compressionLevel:9
//  3. 策略3 JPEG 转码：resize 到 600×600 + quality:50
//     单一 quality:50 简化说明（v1 审查 P2 提示项 1）：
//     (1) Go 标准库 jpeg.Encode 不支持 sharp 的渐进式质量探测（每次编码需完整重新量化）
//     (2) 单一 quality:50 是压缩比与质量的折中（50≈(80+20)/2 中位数）
//     (3) 若 quality:50 仍不达标，由策略4 Ultra JPEG（quality:20）兜底，覆盖同类 CLI 循环末端
//     (4) 减少多次 jpeg.Encode 调用的 CPU 开销（每次编码 O(W×H)）
//  4. 策略4 Ultra JPEG：resize 到 400×400 + quality:20
//
// 每段达标（≤ ImageTargetRawSize）即返回
// 返回：压缩后数据、宽度、高度、错误
func Compress(data []byte, mediaType string) ([]byte, int, int, error) {
	if len(data) == 0 {
		return nil, 0, 0, fmt.Errorf("image data is empty")
	}

	// 步骤1：DecodeConfig 获取尺寸（防 OOM）
	cfg, fmtType, err := DecodeConfig(data)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode config failed: %w", err)
	}

	// 步骤2：早返回（尺寸+大小都达标）
	if len(data) <= ImageTargetRawSize &&
		cfg.Width <= ImageMaxWidth && cfg.Height <= ImageMaxHeight {
		return data, cfg.Width, cfg.Height, nil
	}

	// 步骤3：DecodeImage 载入像素
	img, _, err := DecodeImage(data)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode image failed: %w", err)
	}

	// 步骤4：维度超限先缩放
	currentImg := img
	if cfg.Width > ImageMaxWidth || cfg.Height > ImageMaxHeight {
		currentImg = resizeImage(img, ImageMaxWidth, ImageMaxHeight)
	}

	// 步骤5：策略1：保格式渐进缩放 [1.0, 0.75, 0.5, 0.25]
	if out, w, h, err := progressiveResizing(currentImg, fmtType, ImageTargetRawSize); err == nil {
		return out, w, h, nil
	}

	// 步骤6：策略2：PNG 优化（resize 800×800 + compressionLevel:9）
	pngImg := resizeImage(currentImg, 800, 800)
	if out, err := encodePNG(pngImg, 9); err == nil && len(out) <= ImageTargetRawSize {
		return out, pngImg.Bounds().Dx(), pngImg.Bounds().Dy(), nil
	}

	// 步骤7：策略3：JPEG 转码（resize 600×600 + quality:50）
	jpegImg := resizeImage(currentImg, 600, 600)
	if out, err := encodeJPEG(jpegImg, 50); err == nil && len(out) <= ImageTargetRawSize {
		return out, jpegImg.Bounds().Dx(), jpegImg.Bounds().Dy(), nil
	}

	// 步骤8：策略4：Ultra JPEG（resize 400×400 + quality:20）
	ultraImg := resizeImage(currentImg, 400, 400)
	out, err := encodeJPEG(ultraImg, 20)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("all compression strategies failed: %w", err)
	}
	return out, ultraImg.Bounds().Dx(), ultraImg.Bounds().Dy(), nil
}

// resizeImage 等比缩放图片（对标 imageResizer.ts:278-286）
// 使用 draw.CatmullRom（高质量缩放，对标 sharp 的 fit:'inside'）
func resizeImage(src image.Image, maxW, maxH int) image.Image {
	srcBounds := src.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	if srcW <= maxW && srcH <= maxH {
		return src
	}

	// 计算等比缩放后的尺寸
	ratio := float64(srcW) / float64(srcH)
	dstW := maxW
	dstH := int(float64(maxW) / ratio)
	if dstH > maxH {
		dstH = maxH
		dstW = int(float64(maxH) * ratio)
	}

	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, draw.Over, nil)
	return dst
}

// encodeJPEG 按 quality 编码 JPEG（对标 imageResizer.ts:686-688）
func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg failed: %w", err)
	}
	return buf.Bytes(), nil
}

// encodePNG 按 compressionLevel 编码 PNG（对标 imageResizer.ts:684-685）
func encodePNG(img image.Image, compressionLevel int) ([]byte, error) {
	var buf bytes.Buffer
	encoder := png.Encoder{
		CompressionLevel: png.CompressionLevel(compressionLevel),
	}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png failed: %w", err)
	}
	return buf.Bytes(), nil
}

// progressiveResizing 渐进缩放尝试（对标 imageResizer.ts:651）
// 缩放因子 [1.0, 0.75, 0.5, 0.25]，每个因子尝试格式优化，达标即返回
// 返回：压缩后数据、实际像素宽、实际像素高、错误
func progressiveResizing(src image.Image, mediaType string, targetSize int) ([]byte, int, int, error) {
	factors := []float64{1.0, 0.75, 0.5, 0.25}
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()

	for _, factor := range factors {
		w := int(float64(srcW) * factor)
		h := int(float64(srcH) * factor)
		if w < 1 || h < 1 {
			continue
		}

		var scaled image.Image
		if factor == 1.0 {
			scaled = src
		} else {
			scaled = resizeImage(src, w, h)
		}

		// 获取缩放后的实际像素尺寸（resizeImage 做等比缩放，实际尺寸可能不等于 w×h）
		actualW := scaled.Bounds().Dx()
		actualH := scaled.Bounds().Dy()

		// 按原格式编码尝试
		var out []byte
		var err error
		switch mediaType {
		case "image/jpeg":
			out, err = encodeJPEG(scaled, 80)
		case "image/png":
			out, err = encodePNG(scaled, 6)
		default:
			// 其他格式（WebP/GIF 等）统一转 PNG 尝试
			out, err = encodePNG(scaled, 6)
		}

		if err == nil && len(out) <= targetSize {
			return out, actualW, actualH, nil
		}
	}

	return nil, 0, 0, fmt.Errorf("progressive resizing failed to meet target size")
}
