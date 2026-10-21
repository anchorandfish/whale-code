package image

import "fmt"

// ImageSizeError 图片超限错误（对标 imageValidation.ts:16-35）
type ImageSizeError struct {
	Size    int64 // 当前大小（字节）
	MaxSize int64 // 最大限制（字节）
	Index   int   // 图片在消息中的序号（多图场景）
}

// Error 实现 error 接口
func (e *ImageSizeError) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("image #%d size %d bytes exceeds limit %d bytes", e.Index, e.Size, e.MaxSize)
	}
	return fmt.Sprintf("image size %d bytes exceeds limit %d bytes", e.Size, e.MaxSize)
}

// ValidateSize 校验压缩后 base64 大小 ≤ APIImageMaxBase64Size（对标 imageValidation.ts:65-104）
// 在 base64 编码后校验（最终防线）
func ValidateSize(base64Data string) error {
	size := int64(len(base64Data))
	if size > APIImageMaxBase64Size {
		return &ImageSizeError{
			Size:    size,
			MaxSize: APIImageMaxBase64Size,
			Index:   -1,
		}
	}
	return nil
}

// ValidateRawSize 校验原始字节大小 ≤ ImageTargetRawSize（防 OOM，压缩前）
func ValidateRawSize(rawSize int64) error {
	if rawSize > ImageTargetRawSize {
		return &ImageSizeError{
			Size:    rawSize,
			MaxSize: ImageTargetRawSize,
			Index:   -1,
		}
	}
	return nil
}

// ValidateDimensions 校验像素维度 ≤ ImageMaxWidth/Height（对标 imageResizer.ts:212-227）
func ValidateDimensions(w, h int) error {
	if w > ImageMaxWidth || h > ImageMaxHeight {
		return fmt.Errorf("image dimensions %dx%d exceed limit %dx%d", w, h, ImageMaxWidth, ImageMaxHeight)
	}
	return nil
}

// ValidateForAPI 批量校验消息中的图片是否超 API 限制（对标 imageValidation.ts:65-104 validateImagesForAPI）
// 遍历 user 消息的 image 块，校验 base64 长度
// messages 格式：[{"role":"user","content":[{"type":"image","source":{"data":"..."}}]}]
func ValidateForAPI(messages []map[string]interface{}) error {
	imgIndex := 0
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}
		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, block := range content {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			if blockType != "image" {
				continue
			}
			source, ok := blockMap["source"].(map[string]interface{})
			if !ok {
				continue
			}
			data, _ := source["data"].(string)
			if err := ValidateSize(data); err != nil {
				if sizeErr, ok := err.(*ImageSizeError); ok {
					sizeErr.Index = imgIndex
					return sizeErr
				}
				return err
			}
			imgIndex++
		}
	}
	return ValidateMediaCount(imgIndex)
}

// ValidateMediaCount 校验每请求媒体数 ≤ APIMaxMediaPerRequest（对标 apiLimits.ts:94）
func ValidateMediaCount(count int) error {
	if count > APIMaxMediaPerRequest {
		return fmt.Errorf("media count %d exceeds limit %d per request", count, APIMaxMediaPerRequest)
	}
	return nil
}
