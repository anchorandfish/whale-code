// Package tools — AskUser 相关类型定义（T3 S1 落地，）。
// 对齐 T0 §3 冲突 4 草案 + T2-A AskUserGateway 接口契约（T2-A 留空，T3 补全）。
package tools

// AskUserInput AskUser 工具入参（REF-T0-§3冲突4 草案对齐，T3 S1 落地）。
// 用于编排器或 AI 调用 AskUser 工具时承载入参，与 tools.go 中 InputSchema 字段保持一致。
type AskUserInput struct {
	Question      string         `json:"question"`                  // 问题文本（≤500 字符）
	Options       []AskOption    `json:"options,omitempty"`         // 选项列表（allow_free_text=false 时非空）
	DefaultIdx    int            `json:"default_idx,omitempty"`     // 默认选项索引（0-based，-1 表示无默认）
	AllowFreeText bool           `json:"allow_free_text,omitempty"` // 是否允许自由文本输入
	Category      string         `json:"category"`                  // clarify/plan-confirm/cost-warning/scope-confirm/dialogue-clarify
	Context       map[string]any `json:"context,omitempty"`         // 上下文元数据（trace_id/task_id 等）
}

// AskOption AskUser 工具的选项（REF-T0-§3冲突4 草案对齐，T3 S1 落地）。
// 与 AskUserOption 区别：AskUserOption 用于工具返回结果（含 omitempty），AskOption 用于入参。
type AskOption struct {
	Label       string `json:"label"`       // 选项显示文本（≤20 字符）
	Description string `json:"description"` // 选项详细说明（≤80 字符）
	Value       string `json:"value"`       // 选项值（用户选择后回传）
}

// ClarifyQuestion 编排器侧澄清问题（REF-T2-A-AskUserGateway 接口契约，T2-A 留空 T3 补全）。
// 用于 AskUserGateway.Ask(ctx, []ClarifyQuestion) 接口的入参。
// 与 AskUserResult 共享底层字段，但服务于编排器侧场景（clarify/plan-confirm/...）。
type ClarifyQuestion struct {
	ID            string          `json:"id"`                        // 问题唯一 ID（用于答案匹配）
	Question      string          `json:"question"`                  // 问题文本
	Options       []AskUserOption `json:"options,omitempty"`         // 选项列表
	DefaultIdx    int             `json:"default_idx,omitempty"`     // -1 表示无默认
	Category      string          `json:"category"`                  // clarify/plan-confirm/cost-warning/scope-confirm/dialogue-clarify
	AllowFreeText bool            `json:"allow_free_text,omitempty"` // 是否允许自由文本输入
	Context       map[string]any  `json:"context,omitempty"`         // 上下文元数据
}

// ClarifyAnswer 编排器侧澄清答案（REF-T2-A-AskUserGateway 接口契约，T2-A 留空 T3 补全）。
// 用于 AskUserGateway.Ask 返回值，承载用户对单个 ClarifyQuestion 的回答。
// - SelectedIdx=-1 + FreeText="" + Cancelled=true：用户取消回答
// - SelectedIdx>=0：用户选择了某选项
// - FreeText!="" + SelectedIdx=-1：用户自由输入
type ClarifyAnswer struct {
	QuestionID  string `json:"question_id"`            // 对应 ClarifyQuestion.ID
	SelectedIdx int    `json:"selected_idx,omitempty"` // -1 表示用户自由输入或取消
	FreeText    string `json:"free_text,omitempty"`    // 自由输入文本（SelectedIdx=-1 时有效）
	Cancelled   bool   `json:"cancelled,omitempty"`    // 用户取消回答
}
