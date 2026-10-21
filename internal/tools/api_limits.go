// Package tools — N16 API limits 系统。
// 本文件定义 APILimits 静态 API 调用限制，
// 解决对标分析 G-06/G-07（无媒体限制 / 无 PDF 页数限制）。
// 值对齐业内既有实现 apiLimits.ts，whale-code 自定义项单独标注。
package tools

// APILimits 定义 API 调用的静态限制。
// 这些限制由 API 提供方强制，客户端预校验可减少无效请求。
type APILimits struct {
	MaxMediaPerRequest   int   // 单请求最大媒体附件数
	MaxImageBase64Size   int64 // 图像 base64 最大尺寸（字节）
	MaxImageDimension    int   // 图像最大宽高（像素）
	MaxPDFPages          int   // PDF 最大页数
	MaxToolResultsPerMsg int   // 单消息最大工具结果数
}

// DefaultAPILimits 默认 API 限制，对齐业内既有实现 apiLimits.ts。
// MaxToolResultsPerMsg 为 whale-code 自定义（同类 CLI 无此字段）。
var DefaultAPILimits = APILimits{
	MaxMediaPerRequest:   100,     // a comparable CLI
	MaxImageBase64Size:   5 << 20, // 5MB, a comparable CLI
	MaxImageDimension:    2000,    // a comparable CLI
	MaxPDFPages:          100,     // a comparable CLI
	MaxToolResultsPerMsg: 20,      // whale-code 自定义
}
