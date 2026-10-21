// Package doctor 提供 whale-code 的本地环境诊断能力。
// 对标同类 CLI doctorDiagnostic.ts（本地诊断，不依赖云服务），
// 适配 Windows + Linux 双平台，覆盖环境/配置/连通性/依赖/平台 5 类检测项。
package doctor

import (
	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
)

// Severity 表示单项检测结果的严重度。
// 对标同类 CLI Doctor.tsx 的颜色编码（warning/error/fatal）。
type Severity int

const (
	// SeverityOK 表示检测通过（对标同类 CLI 绿色 ✅）。
	SeverityOK Severity = iota
	// SeverityWarn 表示警告，不阻塞使用（对标同类 CLI 黄色 ⚠️）。
	SeverityWarn
	// SeverityError 表示错误，影响部分功能（对标同类 CLI 红色 ❌）。
	SeverityError
	// SeverityFatal 表示致命错误，阻塞使用（对标同类 CLI 💀）。
	SeverityFatal
)

// severityIcon 返回严重度对应的图标字符。
// 用于渲染层（render.go）的树形文本输出。
func (s Severity) icon() string {
	switch s {
	case SeverityOK:
		return "OK"
	case SeverityWarn:
		return "WARN"
	case SeverityError:
		return "ERROR"
	case SeverityFatal:
		return "FATAL"
	}
	return "UNKNOWN"
}

// CheckResult 表示单项检测结果。
// 对标同类 CLI DiagnosticInfo 中的单条 warning/error 结构。
type CheckResult struct {
	// Category 检测分类（Environment/Config/Dependencies/Platform/Recovery/Lock/Resource/Network/API）。
	// 动态渲染后全部 9 类均可见，顺序与 doctor.Run() 执行顺序对齐。
	Category string
	// Name 检测项名称（如 "API Key"、"Esafenet DLP"）。
	Name string
	// Severity 检测结果严重度。
	Severity Severity
	// Message 检测消息（人类可读的诊断信息）。
	Message string
	// Detail 详细信息（可选，用于补充错误分类等技术细节）。
	Detail string
	// FixSuggest 修复建议（可选，引导用户修复问题）。
	FixSuggest string
}

// Doctor 是诊断器主结构，聚合配置、AI 客户端与工作目录。
// 对标同类 CLI getDoctorDiagnostic 的数据采集层。
type Doctor struct {
	// cfg 只读引用 whale-code 配置（不修改 config 模块）。
	cfg *config.Config
	// aiClient 用于 API 连通性测试（复用 ai.Client.Stream，不自实现 HTTP）。
	aiClient ai.Client
	// workDir 当前工作目录（检测可读写性）。
	workDir string
	// settingsPath settings.json 路径（检测配置文件存在性）。
	settingsPath string
}
