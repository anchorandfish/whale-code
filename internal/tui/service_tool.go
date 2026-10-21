package tui

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/search"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// ExecuteTool executes a tool by name with the given input
func (t *ToolModel) ExecuteTool(ctx context.Context, name string, input json.RawMessage) (*tools.Result, error) {
	if t.executor == nil {
		return nil, fmt.Errorf("executor not initialized")
	}
	result := t.executor.Execute(ctx, tools.Call{Name: name, Input: string(input)})
	return &result, nil
}

// IsConcurrencySafe checks if a tool can run concurrently
func (t *ToolModel) IsConcurrencySafe(name string) bool {
	if t.registry == nil {
		return false
	}
	spec, ok := t.registry.Get(name)
	if !ok {
		return false
	}
	return spec.ConcurrencySafe
}

// ApprovalAudit returns the tool-approval audit sink (delta F, 2026-09-14). May be nil.
func (t *ToolModel) ApprovalAudit() bridge.ApprovalAudit {
	return t.approvalAudit
}

// Executor returns the tool executor
func (t *ToolModel) Executor() *tools.Executor {
	return t.executor
}

// Registry returns the tool registry
func (t *ToolModel) Registry() *tools.Registry {
	return t.registry
}

// CmdRegistry returns the command registry
func (t *ToolModel) CmdRegistry() *command.Registry {
	return t.cmdRegistry
}

// Policy returns the permission policy
func (t *ToolModel) Policy() *permission.Policy {
	return t.policy
}

// PendingTools returns the list of pending tool calls
func (t *ToolModel) PendingTools() []session.ContentBlock {
	return t.pendingTools
}

// SetPendingTools sets the list of pending tool calls
func (t *ToolModel) SetPendingTools(tools []session.ContentBlock) {
	t.pendingTools = tools
}

// CurrentToolID returns the current tool ID being processed
func (t *ToolModel) CurrentToolID() string {
	return t.currentToolID
}

// SetCurrentToolID sets the current tool ID
func (t *ToolModel) SetCurrentToolID(id string) {
	t.currentToolID = id
}

// ToolInputBuf returns the tool input buffer map
func (t *ToolModel) ToolInputBuf() map[string]string {
	return t.toolInputBuf
}

// SetToolInputBuf sets the tool input buffer map
func (t *ToolModel) SetToolInputBuf(buf map[string]string) {
	t.toolInputBuf = buf
}

// ToolCancel returns the tool cancellation function
func (t *ToolModel) ToolCancel() context.CancelFunc {
	return t.toolCancel
}

// SetToolCancel sets the tool cancellation function
func (t *ToolModel) SetToolCancel(cancel context.CancelFunc) {
	t.toolCancel = cancel
}

// MonitorMgr returns the monitor manager
func (t *ToolModel) MonitorMgr() *monitor.Manager {
	return t.monitorMgr
}

// Daemon returns the daemon instance
func (t *ToolModel) Daemon() *daemon.Daemon {
	return t.daemon
}

// GitHubTools returns the GitHub tools instance
func (t *ToolModel) GitHubTools() *github.GitHubTools {
	return t.githubTools
}

// FileHistory returns the unified file history instance from the executor
func (t *ToolModel) FileHistory() *filehistory.FileHistory {
	return t.executor.FileHistoryGetter()
}

// Searcher returns the search instance
func (t *ToolModel) Searcher() *search.Searcher {
	return t.searcher
}

// PendingConfirm returns the pending confirmation
func (t *ToolModel) PendingConfirm() *PendingConfirm {
	return t.pendingConfirm
}

// SetPendingConfirm sets the pending confirmation
func (t *ToolModel) SetPendingConfirm(confirm *PendingConfirm) {
	t.pendingConfirm = confirm
	// request 侧审计：本方法是所有确认面进入待决状态的唯一汇合点，
	// 在此记录「弹窗已置入」时刻，与决策侧（tool-approval.jsonl 的 decision 条目）
	// 经 ToolInputHash 配对 ⇒ pending 时长可归因（用户晚点 vs 弹窗晚弹）。
	// confirm == nil 是清除待决，不留痕（取消/拒绝已由决策侧覆盖）。
	if confirm != nil {
		t.recordApprovalRequestTool(confirm)
	}
}

// PendingChoice returns the pending choice
func (t *ToolModel) PendingChoice() *PendingChoice {
	return t.pendingChoice
}

// SetPendingChoice sets the pending choice
func (t *ToolModel) SetPendingChoice(choice *PendingChoice) {
	t.pendingChoice = choice
}

// ChoiceIdx returns the choice index
func (t *ToolModel) ChoiceIdx() int {
	return t.choiceIdx
}

// SetChoiceIdx sets the choice index
func (t *ToolModel) SetChoiceIdx(idx int) {
	t.choiceIdx = idx
}

// LoopScheduler returns the loop scheduler
func (t *ToolModel) LoopScheduler() *tools.LoopScheduler {
	return t.loopScheduler
}

// MonitorCh returns the monitor event channel
func (t *ToolModel) MonitorCh() chan monitorEvent {
	return t.monitorCh
}

// MonitorEscape 返回 monitor done 阻塞发送的逃生口（A-5）。
// 永不为 nil：零值 Model 走 monitorEscape 的 nil-receiver 兜底（语义见其文档）。
func (t *ToolModel) MonitorEscape() *monitorEscape {
	return t.monitorEscape
}

// LoopCh returns the loop fire channel
func (t *ToolModel) LoopCh() chan loopFireMsg {
	return t.loopCh
}
