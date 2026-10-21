// Package tools — Tool 接口四象限（对标同类 CLI Tool / 同类 CLI ToolHandler）。
//
// 本文件定义新 Tool 接口（Lifecycle / Capabilities / AI Facing / Exposure / Sandbox）。
// C2 冲突解决：RequiredSandboxLevel() 返回 sandbox.SandboxLevel，
// 不在 tools 包重复定义 SandboxLevel 枚举。
// C19 三层权限职责：Tool.CheckPermissions 计算初始决策，
// permission 模块规则覆写，approval.Manager 决定是否询问用户。
package tools

import (
	"context"
	"encoding/json"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// Tool 工具接口（四象限）。任何工具可通过 buildTool 工厂注册，
// 或通过 AdaptLegacy 从旧 Handler 适配。
type Tool interface {
	// === Lifecycle ===
	// ValidateInput 校验输入参数，提前拒绝坏参数（含 sandbox.ValidatePath，C14）。
	ValidateInput(ctx context.Context, input json.RawMessage) error
	// CheckPermissions 工具特定权限检查，计算初始决策（C19 第一层）。
	CheckPermissions(ctx context.Context, input json.RawMessage) (PermissionDecision, error)
	// Call 执行工具，返回进度 channel 和结果。
	Call(ctx context.Context, input json.RawMessage) (<-chan ToolProgress, *ToolResult, error)

	// === Capabilities ===
	IsEnabled() bool
	IsConcurrencySafe() bool
	IsReadOnly() bool
	IsDestructive() bool
	InterruptBehavior() InterruptBehavior

	// === AI Facing ===
	Name() string
	Prompt() string
	Description(ctx context.Context) string
	Schema() *JSONSchema
	MapToolResultToAPI(result *ToolResult) MessageContent

	// === Exposure（对标同类 CLI ToolExposure） ===
	Exposure() ToolExposure

	// === Sandbox（工具声明所需沙箱级别，C2） ===
	RequiredSandboxLevel() sandbox.SandboxLevel

	// === Rendering（TUI 层实现） ===
	RenderToolUseMessage(input json.RawMessage) string
	RenderToolResultMessage(result *ToolResult) string
}

// ToolExposure 工具暴露级别。
type ToolExposure int

const (
	// ExposureDirect 模型直接可见。
	ExposureDirect ToolExposure = iota
	// ExposureDeferred 延迟，配合 ToolSearch 检索。
	ExposureDeferred
	// ExposureHidden 隐藏，仅供内部分发。
	ExposureHidden
)

// InterruptBehavior 中断行为。
type InterruptBehavior int

const (
	// InterruptCancel 用户取消时中断。
	InterruptCancel InterruptBehavior = iota
	// InterruptBlock 阻塞直到完成。
	InterruptBlock
)

// PermissionAction 权限动作（C19 第一层 Tool 自评结果）。
type PermissionAction int

const (
	// PermissionAllow 允许执行。
	PermissionAllow PermissionAction = iota
	// PermissionDeny 拒绝执行。
	PermissionDeny
	// PermissionAsk 需要用户确认。
	PermissionAsk
)

// PermissionDecision 权限决策（Tool.CheckPermissions 返回）。
type PermissionDecision struct {
	Action     PermissionAction
	Reason     string
	SandboxReq sandbox.SandboxLevel // 所需沙箱级别
}

// ToolProgress 工具执行进度。
type ToolProgress struct {
	Message string
	Percent float64 // 0.0 - 1.0, -1 表示未知
}

// ToolResult 工具结果。
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
	Metadata  map[string]any // 耗时/降级原因/违规事件等
	// FailureKind 终局失败种类（R3/L3.2，2026-09-14）。
	// **仅当 IsError=true 时允许非空** —— 对应 IK3：IsError=false 不得携带终局标记，
	// 否则会污染"模型文本不变但结构化标记为拒绝"的判断。
	// 由 Tool 实现方设置，经 taskHandler → executeDirect 落到 Result.FailureKind。
	FailureKind FailureKind
	// Confirm 是确认交互的类型化结果（仅确认类失败设置；零值表示不涉及确认）。
	Confirm ConfirmOutcome
}

// MessageContent API 消息内容。
type MessageContent struct {
	Type      string // "tool_result"
	ToolUseID string
	Content   any // string 或 []ContentBlock
	IsError   bool
}

// JSONSchema 简化的 JSON Schema（工具入参描述）。
type JSONSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]*JSONSchema `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
	Items      *JSONSchema            `json:"items,omitempty"`
}
