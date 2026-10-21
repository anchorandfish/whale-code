// Package image provides image processing pipeline: detection, compression,
// format conversion, size validation, and token estimation.
package image

// API 限制常量（对标同类 CLI apiLimits.ts）
const (
	// APIImageMaxBase64Size API 硬限：base64 字符串长度 5MB（对标 apiLimits.ts:22）
	APIImageMaxBase64Size = 5 * 1024 * 1024

	// ImageTargetRawSize 客户端原始字节目标 3.75MB（APIImageMaxBase64Size * 3/4，对标 apiLimits.ts:29-30）
	ImageTargetRawSize = (APIImageMaxBase64Size * 3) / 4

	// ImageMaxWidth 维度上限 2000px（对标 apiLimits.ts:42）
	ImageMaxWidth = 2000

	// ImageMaxHeight 维度上限 2000px（对标 apiLimits.ts:43）
	ImageMaxHeight = 2000

	// APIMaxMediaPerRequest 每请求媒体总数限制 100（对标 apiLimits.ts:94）
	APIMaxMediaPerRequest = 100

	// TokenPerBase64Char token/base64 字符换算系数 0.125（对标 imageResizer.ts:581）
	TokenPerBase64Char = 0.125
)
