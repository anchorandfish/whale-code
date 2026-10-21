package tui

import (
	"time"
)

// QueuedInputAction 排队输入的动作类型（对标同类 CLI QueuedInputAction 枚举）。
// P0 阶段仅 Plain 生效；ParseSlash / RunShell 为未来扩展预留。
type QueuedInputAction int

const (
	// QueuedInputPlain 普通用户消息（P0 唯一支持的类型）。
	QueuedInputPlain QueuedInputAction = iota
	// QueuedInputParseSlash / 命令（未来扩展，P0 未启用）。
	QueuedInputParseSlash
	// QueuedInputRunShell !shell 命令（未来扩展，P0 未启用）。
	QueuedInputRunShell
)

// QueuedUserMessage 排队的用户消息（对标同类 CLI QueuedUserMessage 结构体）。
// Text: 消息原文（不截断，完整存储）。
// Timestamp: 入队时间（用于排序与调试，不渲染）。
// Action: 动作类型（P0 仅 Plain）。
type QueuedUserMessage struct {
	Text      string
	Timestamp time.Time
	Action    QueuedInputAction
}

// drainQueuedInputMsg 排队输入 drain 钩子消息（异步触发 sendInput）。
// 由 handleStreamDone 在所有 turn 结束钩子（IncrementTurn/saveSession/FileHistory
// Snapshot/runTurnEndHooks/checkAutoCompact/auto memory extraction）执行完毕后发出，
// 由 Update 循环接收后调用 sendInput 路径提交下一轮排队消息。
// 采用异步消息而非直接 return m.sendInput() 是为了避免跳过 turn end 钩子
// （子审查建议优化1：handleStreamDone 必须先完成所有 turn 结束处理）。
type drainQueuedInputMsg struct{}

// DefaultQueuedInputsMax 排队输入队列默认上限（对标同类 CLI 无上限的反例，本方案超越同类 CLI 加防护）。
const DefaultQueuedInputsMax = 50

// EnqueueInput 将一条用户消息加入排队队列。
// 当队列已达上限（queuedInputsMax）时返回 false，调用方应提示用户。
// 入队后不立即触发 AI stream，需等待当前 turn 结束后由 drainNextQueuedInput 自动出队。
// 对标同类 CLI queue_user_message_with_options。
func (m *Model) EnqueueInput(msg QueuedUserMessage) bool {
	if len(m.queuedInputs) >= m.queuedInputsMax {
		return false
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	m.queuedInputs = append(m.queuedInputs, msg)
	return true
}

// DrainNextQueuedInput 从队首 pop 一条消息（FIFO）。
// 返回 (msg, true) 表示队列非空，(零值, false) 表示队列为空。
// 对标同类 CLI maybe_send_next_queued_input 的 pop_front 路径。
func (m *Model) DrainNextQueuedInput() (QueuedUserMessage, bool) {
	if len(m.queuedInputs) == 0 {
		return QueuedUserMessage{}, false
	}
	msg := m.queuedInputs[0]
	m.queuedInputs = m.queuedInputs[1:]
	return msg, true
}

// PopLatestQueuedForEdit 从队尾 pop 一条消息回 composer 编辑（LIFO）。
// 用于 Alt+Up 快捷键：用户希望编辑最后一条排队消息。
// 返回 (msg, true) 表示队列非空，(零值, false) 表示队列为空。
// 对标同类 CLI pop_latest_queued_composer_state。
func (m *Model) PopLatestQueuedForEdit() (QueuedUserMessage, bool) {
	if len(m.queuedInputs) == 0 {
		return QueuedUserMessage{}, false
	}
	msg := m.queuedInputs[len(m.queuedInputs)-1]
	m.queuedInputs = m.queuedInputs[:len(m.queuedInputs)-1]
	return msg, true
}

// ClearQueuedInputs 清空排队队列（会话切换 / 程序退出时调用）。
// 对标同类 CLI clear。
func (m *Model) ClearQueuedInputs() {
	m.queuedInputs = nil
}

// HasQueuedInputs 返回队列是否非空（用于渲染判断）。
// 值接收器版本：仅读不改，便于在 View() 等只读上下文中调用。
func (m Model) HasQueuedInputs() bool {
	return len(m.queuedInputs) > 0
}

// QueuedInputsCount 返回队列长度（用于窄终端降级渲染 "N messages queued..."）。
// 值接收器版本：仅读不改。
func (m Model) QueuedInputsCount() int {
	return len(m.queuedInputs)
}

// QueuedInputsSnapshot 返回队列快照（用于渲染层只读访问，避免外部修改底层切片）。
// 值接收器版本：返回切片副本，调用方可自由读取。
func (m Model) QueuedInputsSnapshot() []QueuedUserMessage {
	if len(m.queuedInputs) == 0 {
		return nil
	}
	snapshot := make([]QueuedUserMessage, len(m.queuedInputs))
	copy(snapshot, m.queuedInputs)
	return snapshot
}

// isTypeaheadVisible 返回 typeahead 补全弹窗是否可见。
// 用于 Enter 键路由判断：弹窗可见时 Enter 走 applyTypeaheadCompletion 而非 enqueueInput。
// 值接收器版本：仅读不改。
func (m Model) isTypeaheadVisible() bool {
	return m.suggestionState != nil && m.suggestionState.Visible
}
