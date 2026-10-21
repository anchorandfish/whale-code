// Package tools — buildTool 工厂（对标同类 CLI buildTool）。
//
// T9: fail-closed 工厂模式。任何必填字段缺失返回 error；
// IsEnabled 零值 false（忘记设置 → 工具不启用，而非 fail-open）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// ToolDefinition 工具定义（通过 buildTool 注册）。
type ToolDefinition struct {
	// === 必填 ===
	Name             string
	Schema           *JSONSchema
	Prompt           string
	Call             func(ctx context.Context, input json.RawMessage) (<-chan ToolProgress, *ToolResult, error)
	ValidateInput    func(ctx context.Context, input json.RawMessage) error
	CheckPermissions func(ctx context.Context, input json.RawMessage) (PermissionDecision, error)

	// === Capabilities（有默认值） ===
	IsEnabled         bool // 默认 false（fail-closed）
	IsConcurrencySafe bool
	IsReadOnly        bool
	IsDestructive     bool
	InterruptBehavior InterruptBehavior // 默认 InterruptCancel

	// === Exposure（有默认值） ===
	Exposure ToolExposure // 默认 ExposureDirect

	// === Sandbox（有默认值） ===
	RequiredSandbox sandbox.SandboxLevel // 默认 SandboxLevelRelaxed

	// === 可选 ===
	Description             func(ctx context.Context) string
	RenderToolUseMessage    func(input json.RawMessage) string
	RenderToolResultMessage func(result *ToolResult) string
}

// buildTool 工厂函数（fail-closed 模式）。
//
// 必填字段缺失返回 error；IsEnabled 零值 false 表示工具默认不启用，
// 避免"忘记设置"导致 fail-open。
func buildTool(def ToolDefinition) (Tool, error) {
	// fail-closed 校验：必填字段缺失即拒绝构建。
	if def.Name == "" {
		return nil, fmt.Errorf("buildTool: Name is required")
	}
	if def.Schema == nil {
		return nil, fmt.Errorf("buildTool %s: Schema is required", def.Name)
	}
	if def.Prompt == "" {
		return nil, fmt.Errorf("buildTool %s: Prompt is required", def.Name)
	}
	if def.Call == nil {
		return nil, fmt.Errorf("buildTool %s: Call is required", def.Name)
	}
	if def.ValidateInput == nil {
		return nil, fmt.Errorf("buildTool %s: ValidateInput is required", def.Name)
	}
	if def.CheckPermissions == nil {
		return nil, fmt.Errorf("buildTool %s: CheckPermissions is required", def.Name)
	}
	return &builtTool{def: def}, nil
}

// builtTool buildTool 生成的工具实例。
type builtTool struct {
	def ToolDefinition
}

func (t *builtTool) Name() string                               { return t.def.Name }
func (t *builtTool) Schema() *JSONSchema                        { return t.def.Schema }
func (t *builtTool) Prompt() string                             { return t.def.Prompt }
func (t *builtTool) IsEnabled() bool                            { return t.def.IsEnabled }
func (t *builtTool) IsConcurrencySafe() bool                    { return t.def.IsConcurrencySafe }
func (t *builtTool) IsReadOnly() bool                           { return t.def.IsReadOnly }
func (t *builtTool) IsDestructive() bool                        { return t.def.IsDestructive }
func (t *builtTool) InterruptBehavior() InterruptBehavior       { return t.def.InterruptBehavior }
func (t *builtTool) Exposure() ToolExposure                     { return t.def.Exposure }
func (t *builtTool) RequiredSandboxLevel() sandbox.SandboxLevel { return t.def.RequiredSandbox }

func (t *builtTool) ValidateInput(ctx context.Context, input json.RawMessage) error {
	return t.def.ValidateInput(ctx, input)
}

func (t *builtTool) CheckPermissions(ctx context.Context, input json.RawMessage) (PermissionDecision, error) {
	return t.def.CheckPermissions(ctx, input)
}

func (t *builtTool) Call(ctx context.Context, input json.RawMessage) (<-chan ToolProgress, *ToolResult, error) {
	return t.def.Call(ctx, input)
}

func (t *builtTool) Description(ctx context.Context) string {
	if t.def.Description != nil {
		return t.def.Description(ctx)
	}
	return t.def.Prompt
}

func (t *builtTool) MapToolResultToAPI(result *ToolResult) MessageContent {
	return MessageContent{
		Type:      "tool_result",
		ToolUseID: result.ToolUseID,
		Content:   result.Content,
		IsError:   result.IsError,
	}
}

func (t *builtTool) RenderToolUseMessage(input json.RawMessage) string {
	if t.def.RenderToolUseMessage != nil {
		return t.def.RenderToolUseMessage(input)
	}
	return t.def.Name
}

func (t *builtTool) RenderToolResultMessage(result *ToolResult) string {
	if t.def.RenderToolResultMessage != nil {
		return t.def.RenderToolResultMessage(result)
	}
	if result.IsError {
		return "Error: " + result.Content
	}
	return result.Content
}
