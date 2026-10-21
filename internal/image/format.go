package image

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
)

// DetectFormat 基于 magic bytes 检测图片格式（对标 imageResizer.ts:769-812）
// 不信任扩展名/media_type 字符串，以字节内容为准
// 支持：png/jpeg/gif/webp/bmp，未知返回 "application/octet-stream"
func DetectFormat(data []byte) string {
	if len(data) < 8 {
		return "application/octet-stream"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return "image/gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 {
		if data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46 &&
			data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50 {
			return "image/webp"
		}
	}
	// BMP: 42 4D ("BM")
	if data[0] == 0x42 && data[1] == 0x4D {
		return "image/bmp"
	}
	return "application/octet-stream"
}

// DetectFormatFromBase64 从 base64 字符串检测格式（对标 imageResizer.ts:819-829）
func DetectFormatFromBase64(b64 string) string {
	// 去除 data URI 前缀（如 "data:image/png;base64,"）
	if commaIdx := strings.Index(b64, ","); commaIdx >= 0 {
		b64 = b64[commaIdx+1:]
	}
	// 解码前几个字节即可判断格式（取前 16 个 base64 字符 ≈ 12 字节原始数据）
	sampleLen := 16
	if len(b64) < sampleLen {
		sampleLen = len(b64)
	}
	// base64 长度必须是 4 的倍数
	padLen := (4 - sampleLen%4) % 4
	sample := b64[:sampleLen] + strings.Repeat("=", padLen)
	data, err := base64.StdEncoding.DecodeString(sample)
	if err != nil {
		return "application/octet-stream"
	}
	return DetectFormat(data)
}

// IsBMP 检测 BMP magic bytes（对标 imagePaste.ts:209-210）
// BMP: 0x42 0x4D ("BM")
func IsBMP(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	return data[0] == 0x42 && data[1] == 0x4D
}

// ConvertToPNG 将任意格式转 PNG（对标同类 CLI BMP→PNG）
// 用于 BMP 归一化 + 策略2 PNG 优化
func ConvertToPNG(data []byte) ([]byte, error) {
	img, _, err := DecodeImage(data)
	if err != nil {
		return nil, fmt.Errorf("convert to png: decode failed: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("convert to png: encode failed: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeImage 解码图片为 image.Image（供 Compress 使用）
// 支持 png/jpeg/gif/webp/bmp
func DecodeImage(data []byte) (image.Image, string, error) {
	img, fmtType, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode image failed: %w", err)
	}
	return img, "image/" + fmtType, nil
}

// DecodeConfig 仅读头部获取尺寸（防 OOM，对标 sharp.metadata()）
// 压缩前先调此函数，超 ImageMaxWidth/Height 提前缩放
func DecodeConfig(data []byte) (image.Config, string, error) {
	cfg, fmtType, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, "", fmt.Errorf("decode config failed: %w", err)
	}
	return cfg, "image/" + fmtType, nil
}

// jpeg/gif 解码器由标准库 image 包在导入时自动注册
// webp/bmp 解码器由 golang.org/x/image 包通过 blank import 注册
