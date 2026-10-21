// Package image — M23-01 图像 quality 渐进式二分探测（SNAPSHOT）
//
// 本文件实现 JPEG quality 的二分搜索探测，替代原固定 quality:50/20 策略。
// 对标同类 CLI sharp 二分搜索 quality（https://sharp.pixelplumbing.com/）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：BinarySearchQuality 函数，输入目标大小+图片+quality范围
// - 结构体字段：QualitySearchConfig 含 TargetSize/MinQ/MaxQ/MaxIterations
// - 分层权责：本文件只做"quality 探测"，缩放由 compress.go
// - 降级策略：二分失败时回退到固定 quality（50）
// - TTL规则：无 TTL（每次压缩独立二分）
package image

import (
	"fmt"
	"image"
)

// defaultMinQuality 是二分搜索最低 quality（10，防画质过低）。
const defaultMinQuality = 10

// defaultMaxQuality 是二分搜索最高 quality（90，保画质上限）。
const defaultMaxQuality = 90

// defaultMaxIterations 是二分搜索最大迭代次数（7 次，2^7=128 精度足够）。
const defaultMaxIterations = 7

// QualitySearchConfig 是 quality 二分搜索配置（SNAPSHOT-1）。
type QualitySearchConfig struct {
	TargetSize    int // 目标字节数（如 ImageTargetRawSize）
	MinQuality    int // 最低 quality（默认 10）
	MaxQuality    int // 最高 quality（默认 90）
	MaxIterations int // 最大迭代次数（默认 7）
}

// DefaultQualitySearchConfig 返回默认二分搜索配置（SNAPSHOT-2）。
func DefaultQualitySearchConfig(targetSize int) QualitySearchConfig {
	return QualitySearchConfig{
		TargetSize:    targetSize,
		MinQuality:    defaultMinQuality,
		MaxQuality:    defaultMaxQuality,
		MaxIterations: defaultMaxIterations,
	}
}

// BinarySearchQuality 用二分搜索找到满足目标大小的最高 JPEG quality（SNAPSHOT-3）。
//
// 行为：
// - 在 [minQ, maxQ] 范围内二分搜索
// - 找到使输出 ≤ targetSize 的最高 quality
// - 若 maxQ 仍 ≤ targetSize，返回 maxQ（最高画质即可满足）
// - 若 minQ 仍 > targetSize，返回 minQ（最低画质仍超标，返回最小值）
// - 最多迭代 maxIterations 次
//
// 参数：
// - img: 已缩放的图像
// - cfg: 搜索配置
//
// 返回：最佳 quality + 编码后的字节 + error
func BinarySearchQuality(img image.Image, cfg QualitySearchConfig) ([]byte, int, error) {
	if img == nil {
		return nil, 0, fmt.Errorf("binary search quality: nil image")
	}
	if cfg.TargetSize <= 0 {
		return nil, 0, fmt.Errorf("binary search quality: invalid target size %d", cfg.TargetSize)
	}

	lo := cfg.MinQuality
	hi := cfg.MaxQuality
	var bestData []byte
	var bestQ int

	// 先尝试最高 quality
	hiData, err := encodeJPEG(img, hi)
	if err != nil {
		return nil, 0, fmt.Errorf("binary search quality: encode max: %w", err)
	}
	if len(hiData) <= cfg.TargetSize {
		// 最高 quality 即满足，直接返回
		return hiData, hi, nil
	}

	// 二分搜索
	for i := 0; i < cfg.MaxIterations && lo <= hi; i++ {
		mid := (lo + hi) / 2
		data, err := encodeJPEG(img, mid)
		if err != nil {
			return nil, 0, fmt.Errorf("binary search quality: encode mid=%d: %w", mid, err)
		}

		if len(data) <= cfg.TargetSize {
			// 满足目标，尝试更高 quality
			bestData = data
			bestQ = mid
			lo = mid + 1
		} else {
			// 超标，降低 quality
			hi = mid - 1
		}
	}

	if bestData == nil {
		// 所有 quality 都超标，用最低 quality 兜底
		data, err := encodeJPEG(img, cfg.MinQuality)
		if err != nil {
			return nil, 0, fmt.Errorf("binary search quality: fallback min: %w", err)
		}
		return data, cfg.MinQuality, nil
	}

	return bestData, bestQ, nil
}

// CompressWithBinarySearch 是 Compress 的二分搜索变体（SNAPSHOT-4）。
//
// 在策略3（JPEG 转码）阶段，用二分搜索替代固定 quality:50：
// - 先按原策略1（保格式渐进缩放）+ 策略2（PNG 优化）尝试
// - 策略3 改用 BinarySearchQuality 探测最佳 quality
// - 策略4 保留 Ultra JPEG（quality:20）作为最后兜底
//
// 返回值与 Compress 一致。
func CompressWithBinarySearch(data []byte, mediaType string) ([]byte, int, int, error) {
	if len(data) == 0 {
		return nil, 0, 0, fmt.Errorf("image data is empty")
	}

	// 步骤1：DecodeConfig 获取尺寸
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

	// 步骤5：策略1：保格式渐进缩放
	if out, w, h, err := progressiveResizing(currentImg, fmtType, ImageTargetRawSize); err == nil {
		return out, w, h, nil
	}

	// 步骤6：策略2：PNG 优化
	pngImg := resizeImage(currentImg, 800, 800)
	if out, err := encodePNG(pngImg, 9); err == nil && len(out) <= ImageTargetRawSize {
		return out, pngImg.Bounds().Dx(), pngImg.Bounds().Dy(), nil
	}

	// 步骤7：策略3：JPEG 转码 + 二分搜索 quality（M23-01 核心）
	jpegImg := resizeImage(currentImg, 600, 600)
	searchCfg := DefaultQualitySearchConfig(ImageTargetRawSize)
	if out, q, err := BinarySearchQuality(jpegImg, searchCfg); err == nil && len(out) <= ImageTargetRawSize {
		_ = q // quality 不返回给调用方，仅内部决策
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
