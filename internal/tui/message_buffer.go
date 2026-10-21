package tui

import "sync"

// messageBuffer 是带 FIFO 裁剪的消息缓冲区。
//
// 对标同类 CLI ink.tsx resetPools + 对标分析 G-04。
// 当消息数超过 maxMessages 时，淘汰最旧的消息（FIFO），
// 保留最近 maxMessages 条，防止长会话内存线性增长。
//
// 关键约束：
// - maxMessages=10000（2026-10-08 由 1000 提高，覆盖长会话数百万轮场景，
// 防止「历史对话消失」；内存上限约 1MB 指针数组，可控）。
// - 裁剪仅影响显示层，session 持久化不变（session 模块不改）。
// - onEvict 回调用于失效对应 RenderCache 条目。
// - sync.Mutex 保护并发访问。
// - 存 **displayEntry**（文本 + 逐行 source map）而非裸 string：批 C 起，显示条目的
// "这一行代表什么"必须在**产生它的那一刻**随内容一起存下来（谁省略谁登记），
// 复制层才不必事后按屏幕字面反查（见 transcript_source.go）。
type messageBuffer struct {
	mu          sync.Mutex
	entries     []displayEntry
	maxMessages int
	onEvict     func(evicted string) // 淘汰回调（用于失效 RenderCache）
}

// NewMessageBuffer 构造消息缓冲区。
//
// maxMessages 上限；onEvict 在淘汰消息时调用（可为 nil）。
func NewMessageBuffer(maxMessages int, onEvict func(string)) *messageBuffer {
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	return &messageBuffer{
		entries:     make([]displayEntry, 0, maxMessages),
		maxMessages: maxMessages,
		onEvict:     onEvict,
	}
}

// Append 追加消息（无 source map），超限时淘汰最旧（FIFO）。
//
// 淘汰时触发 onEvict 回调，调用方据此失效对应 RenderCache 条目。
func (b *messageBuffer) Append(msg string) {
	b.AppendEntry(displayEntry{text: msg})
}

// AppendEntry 追加一条带 source map 的显示条目，超限时淘汰最旧（FIFO）。
func (b *messageBuffer) AppendEntry(entry displayEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, entry)
	if len(b.entries) > b.maxMessages {
		evicted := b.entries[0].text
		// 切片前移：避免频繁内存分配，按 maxMessages 稳定容量。
		// 使用 copy 而非 b.entries = b.entries[1:]，防止底层数组无限增长。
		copy(b.entries, b.entries[1:])
		b.entries = b.entries[:len(b.entries)-1]
		if b.onEvict != nil {
			b.onEvict(evicted)
		}
	}
}

// All 返回所有消息文本的副本（只读，调用方可安全使用）。
func (b *messageBuffer) All() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.entries))
	for i, e := range b.entries {
		out[i] = e.text
	}
	return out
}

// AllEntries 返回所有显示条目的副本（文本 + source map）。
func (b *messageBuffer) AllEntries() []displayEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]displayEntry, len(b.entries))
	copy(out, b.entries)
	return out
}

// Len 返回消息数。
func (b *messageBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// SetAll 用新消息列表重建缓冲区（FIFO 超限时淘汰旧消息）。
//
// 用于 SetMessages 等场景的批量替换。
// 重建过程中淘汰的消息同样触发 onEvict 回调。
//
// v1 审查 T-2：单次加锁批量插入，避免逐条 Append 的 Lock/Unlock 开销；
// 删除原 onEvict / max 局部变量的死代码。
// onEvict 在锁内调用，当前 onEvict 是 renderCache.Invalidate 不重入 messageBuffer，安全；
// 若未来 onEvict 回调重入 messageBuffer，应改为锁外触发（先收集 evicted 列表，解锁后逐个调）。
func (b *messageBuffer) SetAll(msgs []string) {
	entries := make([]displayEntry, len(msgs))
	for i, m := range msgs {
		entries[i] = displayEntry{text: m}
	}
	b.SetAllEntries(entries)
}

// SetAllEntries 用新的显示条目列表重建缓冲区（FIFO 超限时淘汰旧消息）。
func (b *messageBuffer) SetAllEntries(entries []displayEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = make([]displayEntry, 0, b.maxMessages)
	for _, e := range entries {
		b.entries = append(b.entries, e)
		if len(b.entries) > b.maxMessages {
			evicted := b.entries[0].text
			copy(b.entries, b.entries[1:])
			b.entries = b.entries[:len(b.entries)-1]
			if b.onEvict != nil {
				b.onEvict(evicted)
			}
		}
	}
}
