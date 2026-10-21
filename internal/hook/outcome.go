package hook

// outcome.go — 钩子结果的结构化出口（D3 / S7-P1-②）。
//
// 缺陷（修复前）：`HookResult.Messages` 被约 25 处写入（跳过 / 超时 / 非零退出 / deny 被忽略 /
// 改写特权未授予 …），但**全仓零消费** —— 生产代码里没有任何读者，只有测试用 Logf 打出来看过。
// 后果：hook 跳过、超时、失败全部静默，用户与 TUI 都不知道"hook 本来该拦但没拦"。
//
// 修法（对齐族 ：结构化事实必须有结构化出口，不得靠散文关键词反推）：
// - 出口唯一：所有执行路径汇聚到 ExecuteHookWithPayload（validator.go），在**同一个**兜底点记录；
// - 形态结构化：HookOutcome 携带事件 / hook 身份 / 来源 / 动作 / 跳过原因（枚举）等机器可读字段；
// - 两个消费者：① 进程内环形缓冲（TUI 用 DrainOutcomes 拉取并渲染）；
// ② internal/log（结构化日志，非交互场景/审计可查）。
//
// ⚠️ 边界：本文件只做"事实的出口"，不做展示决策（渲染属 TUI 域 ⇒ 见移交单）。

import (
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/log"
)

// HookOutcomeBufferSize 是环形缓冲容量（只保留最近 N 条，避免长会话内存无界增长 —— 族 E/I11）。
const HookOutcomeBufferSize = 64

// HookOutcome 是一次 hook 执行的**结构化**结果摘要。
//
// 字段均为机器可读：SkipReason 是枚举而不是"消息里含某个词"；
// Messages 保留原文仅用于人类阅读。
type HookOutcome struct {
	Time       time.Time
	Event      HookEventType
	ToolName   string
	HookID     string
	HookType   HookType
	Source     HookSource
	Action     Action
	Skipped    bool
	SkipReason HookSkipReason
	Reason     string
	Errored    bool
	Messages   []string
}

// hookOutcomeSink 是 Manager 的结果出口（环形缓冲）。
type hookOutcomeSink struct {
	mu      sync.Mutex
	ring    []HookOutcome
	dropped int
}

// outcomeFromResult 把一次执行结果归一成结构化结果（ExecuteHookWithPayload 的出口调用）。
func outcomeFromResult(h Hook, payload HookPayload, eventType HookEventType, r HookResult) HookOutcome {
	o := HookOutcome{
		Time:       time.Now(),
		Event:      eventType,
		ToolName:   payload.ToolName,
		Action:     r.Action,
		Skipped:    r.Skipped,
		SkipReason: r.SkipReason,
		Reason:     r.Reason,
		Errored:    r.Error != nil,
		Messages:   r.Messages,
	}
	if h != nil {
		b := h.Base()
		o.HookID = b.ID
		o.HookType = b.Type
		o.Source = b.Source
	}
	return o
}

// recordOutcome 记录一次 hook 结果：写入环形缓冲 + 结构化日志。
//
// 日志级别取舍：只有"产生了消息 / 被跳过 / 出错"时才发日志（正常 allow/block 且无消息的不发），
// 避免高频刷屏（每条工具调用都会跑 hook）。
func (m *Manager) recordOutcome(o HookOutcome) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.outcomeSink == nil {
		m.outcomeSink = &hookOutcomeSink{}
	}
	sink := m.outcomeSink
	m.mu.Unlock()

	sink.mu.Lock()
	if len(sink.ring) >= HookOutcomeBufferSize {
		// 丢弃最旧一条（滑动窗口），并计数以便发现"爆量"。
		sink.ring = sink.ring[1:]
		sink.dropped++
	}
	cp := o
	cp.Messages = append([]string(nil), o.Messages...)
	sink.ring = append(sink.ring, cp)
	sink.mu.Unlock()

	if len(o.Messages) == 0 && !o.Skipped && !o.Errored {
		return
	}
	attrs := []any{
		"event", string(o.Event),
		"hook_id", o.HookID,
		"hook_type", string(o.HookType),
		"source", string(o.Source),
		"action", string(o.Action),
		"skipped", o.Skipped,
		"skip_reason", string(o.SkipReason),
	}
	if o.Reason != "" {
		attrs = append(attrs, "reason", o.Reason)
	}
	if len(o.Messages) > 0 {
		attrs = append(attrs, "messages", o.Messages)
	}
	log.Warn("hook outcome", attrs...)
}

// DrainOutcomes 取走并清空缓冲内的结果（TUI 在每轮/每帧拉取后渲染）。
//
// 返回的是副本，调用方可安全持有。
func (m *Manager) DrainOutcomes() []HookOutcome {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	sink := m.outcomeSink
	m.mu.RUnlock()
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ring) == 0 {
		return nil
	}
	out := make([]HookOutcome, len(sink.ring))
	copy(out, sink.ring)
	sink.ring = nil
	return out
}

// PeekOutcomes 查看但不消费缓冲（测试与诊断用）。
func (m *Manager) PeekOutcomes() []HookOutcome {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	sink := m.outcomeSink
	m.mu.RUnlock()
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	out := make([]HookOutcome, len(sink.ring))
	copy(out, sink.ring)
	return out
}

// DroppedOutcomeCount 返回因缓冲溢出被丢弃的结果数（观测用；恒为 0 表示无爆量）。
func (m *Manager) DroppedOutcomeCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	sink := m.outcomeSink
	m.mu.RUnlock()
	if sink == nil {
		return 0
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.dropped
}
