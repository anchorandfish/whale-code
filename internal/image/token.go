package image

import (
	"fmt"
	"math"
)

// EstimateTokens 估算 base64 图片的 token 数（对标 imageResizer.ts:581 + FileReadTool.ts:1137）
// 公式：ceil(len(base64) × 0.125)
// 与 Anthropic/OpenAI API 计费一致
func EstimateTokens(base64Data string) int {
	if base64Data == "" {
		return 0
	}
	tokens := float64(len(base64Data)) * TokenPerBase64Char
	return int(math.Ceil(tokens))
}

// BytesForTokens 将 token 预算转换为原始字节预算（对标 imageResizer.ts:581）
// 公式：maxBytes = maxTokens × 6（因 1 token = 8 base64 字符 = 6 原始字节）
func BytesForTokens(maxTokens int) int {
	if maxTokens <= 0 {
		return 0
	}
	return maxTokens * 6
}

// CompressWithTokenLimit 按 token 预算压缩（对标 imageResizer.ts:583-594 compressImageBufferWithTokenLimit）
// 将 maxTokens 转为 maxBytes，复用 Compress 的 4 段 fallback 链
// 返回：压缩后数据、宽度、高度、错误
func CompressWithTokenLimit(data []byte, mediaType string, maxTokens int) ([]byte, int, int, error) {
	if maxTokens <= 0 {
		return nil, 0, 0, fmt.Errorf("maxTokens must be positive")
	}
	// 复用 Compress 的 4 段 fallback，其最终输出（Ultra JPEG）远小于常规 token 预算
	compressed, w, h, err := Compress(data, mediaType)
	if err != nil {
		return nil, 0, 0, err
	}
	// 校验压缩后是否在 token 预算内
	base64Len := (len(compressed) + 2) / 3 * 4 // base64 编码后长度估算
	estTokens := int(math.Ceil(float64(base64Len) * TokenPerBase64Char))
	if estTokens > maxTokens {
		// 已走完 4 段 fallback 仍超预算，返回当前最优结果 + 警告
		// （调用方可决定是否使用）
		return compressed, w, h, fmt.Errorf("compressed image exceeds token budget: %d > %d", estTokens, maxTokens)
	}
	return compressed, w, h, nil
}
