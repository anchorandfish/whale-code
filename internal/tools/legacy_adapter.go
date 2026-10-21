// Package tools — LegacyTool 适配器（T10，迁移期兼容）。
//
// 将旧 Handler（func(ctx, raw) (string, error)）适配到新 Tool 接口。
// 旧工具无独立 ValidateInput/CheckPermissions，由 Schema 校验 + 默认 Ask 决策。
// C19：CheckPermissions 返回 PermissionAsk，交由 approval.Manager 决定是否询问。
package tools

import (
	"context"
	"encoding/json"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// LegacyHandler 旧工具的执行函数签名（与 Executor.Handler 对齐）。
type LegacyHandler func(ctx context.Context, input json.RawMessage) (string, error)

// LegacyDefinition 旧工具定义，用于适配到新 Tool 接口。
type LegacyDefinition struct {
	Name       string
	Schema     *JSONSchema
	Prompt     string
	Handler    LegacyHandler
	IsReadOnly bool // 只读工具 → SandboxLevelRelaxed；写工具 → SandboxLevelStrict
	// Exposure 工具暴露级别（F2 修复：消除双轨混乱，与 Spec.Exposure 保持单一真相源）。
	// 默认零值 ExposureDirect；延迟工具应在 AdaptLegacy 时显式设置 ExposureDeferred。
	Exposure ToolExposure
}

// AdaptLegacy 将旧 Handler 适配到新 Tool 接口。
//
// 旧工具无独立校验/权限逻辑：ValidateInput 始终放行（由 Schema 层校验），
// CheckPermissions 返回 PermissionAsk（C19 第一层初始决策，交由上层审批）。
func AdaptLegacy(def LegacyDefinition) Tool {
	return &legacyAdapter{def: def}
}

// legacyAdapter 旧工具适配器。
type legacyAdapter struct {
	def LegacyDefinition
}

func (a *legacyAdapter) Name() string        { return a.def.Name }
func (a *legacyAdapter) Schema() *JSONSchema { return a.def.Schema }
func (a *legacyAdapter) Prompt() string      { return a.def.Prompt }

func (a *legacyAdapter) IsEnabled() bool                      { return true } // 旧工具默认启用
func (a *legacyAdapter) IsConcurrencySafe() bool              { return a.def.IsReadOnly }
func (a *legacyAdapter) IsReadOnly() bool                     { return a.def.IsReadOnly }
func (a *legacyAdapter) IsDestructive() bool                  { return false }
func (a *legacyAdapter) InterruptBehavior() InterruptBehavior { return InterruptCancel }
func (a *legacyAdapter) Exposure() ToolExposure               { return a.def.Exposure }

func (a *legacyAdapter) RequiredSandboxLevel() sandbox.SandboxLevel {
	if a.def.IsReadOnly {
		return sandbox.SandboxLevelRelaxed
	}
	return sandbox.SandboxLevelStrict
}

// ValidateInput 旧工具无独立校验，由 Schema 层处理。
func (a *legacyAdapter) ValidateInput(ctx context.Context, input json.RawMessage) error {
	return nil
}

// CheckPermissions 旧工具默认询问（C19 第一层，交由 approval.Manager 决定）。
func (a *legacyAdapter) CheckPermissions(ctx context.Context, input json.RawMessage) (PermissionDecision, error) {
	return PermissionDecision{Action: PermissionAsk, Reason: "legacy tool: default ask", SandboxReq: a.RequiredSandboxLevel()}, nil
}

// Call 执行旧 Handler，封装为 ToolResult。
func (a *legacyAdapter) Call(ctx context.Context, input json.RawMessage) (<-chan ToolProgress, *ToolResult, error) {
	output, err := a.def.Handler(ctx, input)
	if err != nil {
		return nil, &ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return nil, &ToolResult{Content: output}, nil
}

func (a *legacyAdapter) Description(ctx context.Context) string { return a.def.Prompt }

func (a *legacyAdapter) MapToolResultToAPI(result *ToolResult) MessageContent {
	return MessageContent{
		Type:      "tool_result",
		ToolUseID: result.ToolUseID,
		Content:   result.Content,
		IsError:   result.IsError,
	}
}

func (a *legacyAdapter) RenderToolUseMessage(input json.RawMessage) string { return a.def.Name }

func (a *legacyAdapter) RenderToolResultMessage(result *ToolResult) string {
	if result.IsError {
		return "Error: " + result.Content
	}
	return result.Content
}
