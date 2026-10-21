// Package onboarding 提供 whale-code 首次运行交互式引导流程。
// 对标同类 CLI Onboarding.tsx 状态机（2026-03-31 快照）与同类 CLI 首次运行自动配置，
// 适配个人 CLI 定位：4 步骤（环境检测 / Provider 选择 / API Key 输入 / 配置生成），
// 去掉同类 CLI 的 OAuth/主题/终端装饰步骤。
// 依赖 N17 doctor 做首次环境检测（复用 doctor.Run，不重复实现检测逻辑）。
package onboarding

import (
	"github.com/anchorandfish/whale-code/internal/doctor"
)

// StepId 标识 Onboarding 的单个步骤。
// 对标同类 CLI Onboarding.tsx 的 step id（preflight/theme/api-key/oauth/security/terminal-setup），
// whale-code 精简为 4 步。
type StepId int

const (
	// StepEnvironment 步骤 1：环境检测（复用 N17 doctor.Run）。
	StepEnvironment StepId = iota
	// StepProvider 步骤 2：交互式选择 AI Provider（Anthropic/OpenAI/自定义）。
	StepProvider
	// StepAPIKey 步骤 3：API Key 输入（term.ReadPassword 不回显）。
	StepAPIKey
	// StepConfig 步骤 4：生成 settings.json + 哨兵文件 + 完成标记。
	StepConfig
)

// stepCount Onboarding 总步骤数，用于进度渲染（步骤 x/N）。
const stepCount = 4

// Step 表示单个 Onboarding 步骤。
// Run 接收跨步骤共享状态 OnboardingState，返回 error 表示步骤失败。
// 对标同类 CLI OnboardingStep { id, component } 结构，Go 用函数替代 React 组件。
type Step struct {
	// ID 步骤标识。
	ID StepId
	// Name 步骤名称（英文主行，用于进度渲染与错误信息 —— 错误面英文单侧，）。
	Name string
	// NameZH 步骤名称中文辅行（批 5：进度行双语形态，与 util.Bi 对齐）。
	NameZH string
	// Run 步骤执行函数，操作 state 并返回错误。
	Run func(state *OnboardingState) error
}

// OnboardingState 跨步骤共享状态。
// 对标同类 CLI Onboarding 组件内的 useState 集合（skipOAuth/theme 等）。
type OnboardingState struct {
	// Provider 用户选择的 provider：anthropic / openai / custom。
	Provider string
	// APIKey 用户输入或从环境变量读取的 API Key。
	APIKey string
	// APIKeySource API Key 来源：env（环境变量）/ settings（交互输入）。
	APIKeySource string
	// Model 选择的模型名（如 claude-sonnet-4-5 / gpt-4o / 自定义）。
	Model string
	// CustomBaseURL 自定义 provider 的 base URL（仅 Provider=="custom" 时有效）。
	CustomBaseURL string
	// EnvVar 对应的环境变量名（ANTHROPIC_API_KEY / OPENAI_API_KEY），用于 os.Setenv 透传。
	EnvVar string
	// WorkDir 当前工作目录。
	WorkDir string
	// HomeDir 用户主目录（哨兵文件与 settings.json 写入位置）。
	HomeDir string
	// FatalErrors 步骤 1 环境检测的 fatal 结果列表（用于渲染修复建议）。
	FatalErrors []doctor.CheckResult
}
