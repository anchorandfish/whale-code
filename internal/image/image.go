package image

import (
	"encoding/base64"
	"fmt"
	"os"
)

// ImageRef 图片引用，粘贴/读取后的规范化产物（对标同类 CLI CompressedImageResult）
// 已知限制：
//   - GIF 动画：DecodeImage 只取第一帧，GIF 动画粘贴后变成静态图
//   - SVG：DetectFormat 对 SVG 返回 "application/octet-stream"（SVG 是文本格式，无二进制魔数），Paste 会拒绝
//   - Data 与 Base64 同时保留，内存占用约 1.75× 压缩后数据大小；调用方使用完后应尽快释放
type ImageRef struct {
	Data       []byte // 压缩后原始字节（与 Base64 内容相同，内存占用约 0.75× Base64 长度）
	Base64     string // base64 编码（压缩后）
	MediaType  string // "image/png"/"image/jpeg"/"image/gif"/"image/webp"（压缩后实际格式）
	Width      int    // 像素宽（压缩后实际尺寸）
	Height     int    // 像素高（压缩后实际尺寸）
	SourceSize int64  // 原始字节数（压缩前）
	EstTokens  int    // 估算 token 数（ceil(len(base64) × 0.125)）
}

// Paste 粘贴处理入口，图片识别+压缩（契约 02 §2.2.1 L141 核心函数）
// 行为规约：
// 1. 空数据返回 error
// 2. DetectFormat 魔数识别格式（不信任外部传入的 mimeType）
// 3. BMP 自动转 PNG（对标同类 CLI）
// 4. Compress 4 段式压缩（尺寸+质量双约束）
// 5. ValidateSize 校验压缩后 base64 ≤ 5MB API 硬限
// 6. EstimateTokens 估算 token
// 7. 返回 ImageRef
func Paste(clipboard []byte) (ImageRef, error) {
	if len(clipboard) == 0 {
		return ImageRef{}, fmt.Errorf("clipboard image data is empty")
	}

	sourceSize := int64(len(clipboard))
	workingData := make([]byte, len(clipboard))
	copy(workingData, clipboard)

	// 魔数识别格式
	mediaType := DetectFormat(workingData)
	if mediaType == "application/octet-stream" {
		return ImageRef{}, fmt.Errorf("unsupported image format")
	}

	// BMP 自动转 PNG
	if IsBMP(workingData) {
		pngData, err := ConvertToPNG(workingData)
		if err != nil {
			return ImageRef{}, fmt.Errorf("BMP to PNG conversion failed: %w", err)
		}
		workingData = pngData
		mediaType = "image/png"
	}

	// 4 段式压缩
	compressed, w, h, err := Compress(workingData, mediaType)
	if err != nil {
		return ImageRef{}, fmt.Errorf("image compression failed: %w", err)
	}

	// 压缩后根据实际编码格式更新 mediaType
	// 原因：WebP/GIF 等格式经策略1 default 分支或策略3/4 后实际输出为 PNG/JPEG，
	// mediaType 必须与实际数据格式一致，否则 API 调用会出错
	actualMediaType := DetectFormat(compressed)
	if actualMediaType != "application/octet-stream" {
		mediaType = actualMediaType
	}

	// base64 编码
	base64Data := base64.StdEncoding.EncodeToString(compressed)

	// 校验 base64 大小 ≤ 5MB API 硬限
	if err := ValidateSize(base64Data); err != nil {
		return ImageRef{}, fmt.Errorf("compressed image too large: %w", err)
	}

	// 估算 token
	estTokens := EstimateTokens(base64Data)

	return ImageRef{
		Data:       compressed,
		Base64:     base64Data,
		MediaType:  mediaType,
		Width:      w,
		Height:     h,
		SourceSize: sourceSize,
		EstTokens:  estTokens,
	}, nil
}

// FromPath 从文件路径读取并压缩（供 mention.EncodeImageBase64 调用）
// 与 Paste 的区别：先 os.Stat 校验原始大小（防 OOM），再 os.ReadFile
func FromPath(path string) (ImageRef, error) {
	if path == "" {
		return ImageRef{}, fmt.Errorf("image path is empty")
	}

	// 先校验文件大小（防 OOM）
	info, err := os.Stat(path)
	if err != nil {
		return ImageRef{}, fmt.Errorf("failed to stat image file: %w", err)
	}
	if info.Size() == 0 {
		return ImageRef{}, fmt.Errorf("image file is empty")
	}
	// 原始文件超过 10MB 直接拒绝（防超大文件 OOM）
	if info.Size() > 10*1024*1024 {
		return ImageRef{}, fmt.Errorf("image too large: %d bytes (max 10MB)", info.Size())
	}

	// 读取文件
	data, err := os.ReadFile(path)
	if err != nil {
		return ImageRef{}, fmt.Errorf("failed to read image file: %w", err)
	}

	// 走 Paste 流程
	ref, err := Paste(data)
	if err != nil {
		return ImageRef{}, err
	}
	return ref, nil
}
