// 状态说明：本文件已集成到渲染循环（TUI inline scrollback 批次2流式集成）。
// activeCell 作为流式内容真相源，记录待渲染文本、思考内容和 revision。
// 见 内部交付台账

package tui

import "strings"

// ActiveCell 流式活动 cell，对标同类 CLI chatwidget active_cell
// （REF-外部: 同类 CLI chatwidget/command_lifecycle.rs）。
//
// 流式过程中原地 mutation（AppendText/AppendThinking），完成后 Flush 提交为历史。
// revision 自增触发重绘（对标同类 CLI bump_active_cell_revision）。
//
// 设计：
// - text/thinking 用 strings.Builder 高效拼接 delta，避免每次分配
// - tools 存储工具调用预览
// - revision 用于 frame_limiter 节流时判断是否有新内容待渲染
//
// 并发安全：bubbletea Update 单 goroutine 串行，无需加锁。
type ActiveCell struct {
	text     strings.Builder
	thinking strings.Builder
	tools    []ToolPreview
	revision int
}

// ToolPreview 工具调用预览（对标同类 CLI ExecCell 的 call 信息）。
type ToolPreview struct {
	Name  string
	Input string
}

// NewActiveCell 创建空活动 cell。
func NewActiveCell() *ActiveCell {
	return &ActiveCell{}
}

// AppendText 追加流式文本 delta（原地 mutation）。
// 对标同类 CLI chatwidget on_exec_command_output_delta。
func (c *ActiveCell) AppendText(delta string) {
	c.text.WriteString(delta)
	c.revision++
}

// AppendThinking 追加思考内容 delta。
func (c *ActiveCell) AppendThinking(delta string) {
	c.thinking.WriteString(delta)
	c.revision++
}

// AddTool 添加工具调用预览。
func (c *ActiveCell) AddTool(name, input string) {
	c.tools = append(c.tools, ToolPreview{Name: name, Input: input})
	c.revision++
}

// Text 返回当前累积的流式文本。
func (c *ActiveCell) Text() string {
	return c.text.String()
}

// Thinking 返回当前累积的思考内容。
func (c *ActiveCell) Thinking() string {
	return c.thinking.String()
}

// Tools 返回工具调用预览列表。
func (c *ActiveCell) Tools() []ToolPreview {
	return c.tools
}

// Revision 返回当前修订号（用于判断是否有新内容待渲染）。
func (c *ActiveCell) Revision() int {
	return c.revision
}

// Flush 提交活动 cell 为历史，返回待写入 scrollback 的完整文本。
// 调用后 cell 重置为空，可复用于下次流式。
//
// 注意：flush 在 bubbletea Update 串行执行，无并发。
func (c *ActiveCell) Flush() string {
	content := c.text.String()
	c.text.Reset()
	c.thinking.Reset()
	c.tools = nil
	c.revision = 0
	return content
}

// IsEmpty 返回 cell 是否为空（无文本/思考/工具）。
func (c *ActiveCell) IsEmpty() bool {
	return c.text.Len() == 0 && c.thinking.Len() == 0 && len(c.tools) == 0
}
