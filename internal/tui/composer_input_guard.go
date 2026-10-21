// composer_input_guard.go — 输入框的「这是文本吗」闸门（唯一实现，2026-10-07）。
//
// # 报障
//
// 用户（2026-10-07 20:59–21:01 真机）：输入框里出现 `<32;34;15M<32;25;12M…` /
// `[<0;57;40M` 这类乱码，并且被当成用户消息发了出去（模型回「message appears to be
// garbled/corrupted」）。会话存档 `~/.whale/sessions/01a11672-….jsonl` 里那条 user
// 消息的原文就是 `[<0;57;40M`（含中括号、**不含 ESC**）。
//
// # 取证（`composer_input_guard_test.go`，走真 bubbletea 输入解析器 + 真 Update）
//
// 鼠标上报的标准形态是 SGR：`ESC [ < 按钮 ; 列 ; 行 M`。
// 当 tty 把这条序列拆成两次 read（头的 `ESC [` 先到），bubbletea 的 `detectOneMsg`
// 认不出「半个序列」：
// - 完整到达 ⇒ 解析成 `tea.MouseMsg`（正常路径，鼠标拖选靠它）；
// - `ESC [` 单独到达 ⇒ 它把 ESC 当作 Alt 修饰符、把 `[` 当作**一个普通 rune** 交给我们，
// 紧随其后的字节（`<0;57;` / `40M`）变成普通 `KeyRunes` ⇒ 终端协议碎片被当成用户文字打进输入框。
//
// 实测（探针，2026-10-07）：喂 `"\\x1b[<0;57;"` + `"40M"` ⇒ 输入框 = `[<0;57;40M`，
// 与用户存档**逐字节一致**。
//
// # 不变量（本文件是唯一承载体）
//
//	输入框只接受**文本**。转义序列的碎片（被拆散的 ESC 头 + 其载荷）一律不得进入编辑器；
//	一旦字节不再像转义序列载荷，**立刻放行** —— 宁可放行可疑文本，也绝不吞用户数据。
//
// # 为什么装在这里，而不是改 vendor / 关掉鼠标
//
// - 「什么东西有资格成为输入框里的文字」是**本应用的语义**，不是解析器的语义 ⇒ 闸门装在
// 最靠近编辑器的唯一入口（`routeKeyToComposerTextarea`， 收口点），而不是去 fork
// bubbletea 的 `detectOneMsg`（vendor 改动不可维护）。
// - 关掉鼠标上报能让碎片**不再产生**，但会一并失去 要的鼠标拖选；那是产品取舍，
// 不由本文件决定。
//
// # 代价（说清）
//
// 用户**真的**按 Alt+[ 或 Alt+O 时，那一个字符会被吃掉（判定为「可疑的被拆散序列头」）；
// 紧接着若输入的内容形如转义序列载荷（如 `40M`），也会被吃掉。两次都记账（heads/dropped），
// 并在日志留痕 —— 这类丢弃必须可计数、可见。
package tui

import (
	"log"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// composerResyncTimeout 是重同步窗口的**时间上界**：真实转义序列的载荷与它的头同时在
// 同一批字节里到达（微秒级），人类按键最快也就 ~100ms 一次 ⇒ 超过这个时间还没凑成序列，
// 就判定「那不是序列载荷，是用户在打字」，立刻放行。
// 这是终端解析器通用的 ESC 超时纪律，也是本闸门**不会长期吞字**的结构保证。
const composerResyncTimeout = 250 * time.Millisecond

// composerInputGuard 输入框的转义序列碎片闸门状态。
// 随 Model 值复制（与 inputCoalesce 同款：bubbletea 主循环单线程访问，无竞态）。
type composerInputGuard struct {
	// resyncing 表示刚吞掉一个「被拆散的 ESC 序列头」，正在等下文字节判断它是载荷还是用户文字。
	resyncing bool
	// headAt 是吞掉序列头的时刻（超时上界用）。
	headAt time.Time
	// pending 是重同步窗口内累积的候选载荷字节。
	pending []rune
	// heads 已吞掉的「可疑序列头」个数（可计数，供日志/取证）。
	heads int
	// dropped 已吞掉的载荷 rune 数（可计数，供日志/取证）。
	dropped int
}

// csiPayloadByte 判定 rune 是否可能出现在 CSI/SGR 序列的**参数区**：
// 数字、`;`、`:`（SGR 冒号分参数）、`<`/`>`/`?`（私有标记）、`=`。
// 这些字节在用户正常文字里也会出现，所以单独一个不能作为判据；判据是「连续一串都只有这些字节」。
func csiPayloadByte(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r == ';' || r == ':' || r == '<' || r == '>' || r == '?' || r == '=':
		return true
	default:
		return false
	}
}

// csiPayloadFinalByte 判定 rune 是否可能是 CSI 序列的**终结字节**：
// 字母（鼠标 SGR 用 M/m，其他 CSI 用 A-Za-z）或 `~`（键盘/功能键序列）。
func csiPayloadFinalByte(r rune) bool {
	if r == '~' {
		return true
	}
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

// looksLikeCSIPayloadPrefix 判定 s 是否「仍然只是一个转义序列载荷的前缀」。
// 空串算前缀（刚吞掉头，还没有任何字节）。
// 用 **rune** 遍历，不是字节——CJK 文字的多字节序列绝不能被误判成载荷。
func looksLikeCSIPayloadPrefix(rs []rune) bool {
	for _, r := range rs {
		if !csiPayloadByte(r) {
			return false
		}
	}
	return true
}

// looksLikeCompleteCSIPayload 判定 s 是否「已经是一条完整转义序列的载荷」
// （参数区 + 一个终结字节）。用于把它整段吞掉——这是碎片闸门唯一允许吞「像文字」的场合。
func looksLikeCompleteCSIPayload(rs []rune) bool {
	if len(rs) < 2 {
		return false
	}
	if !csiPayloadFinalByte(rs[len(rs)-1]) {
		return false
	}
	return looksLikeCSIPayloadPrefix(rs[:len(rs)-1])
}

// beginResync 记下「刚吞掉一个被拆散的 ESC 头」，并留一条可观测痕迹。
// 频率天然很低（一次拖动最多几条），不需要限流。
func (g *composerInputGuard) beginResync(head string) {
	g.resyncing = true
	g.headAt = time.Now()
	g.pending = g.pending[:0]
	g.heads++
	log.Printf("[tui] composer input guard: dropped split escape-sequence head %q (heads=%d)", head, g.heads)
}

// reset 结束重同步窗口（任何非文本键、或判定为真实文字时调用）。
func (g *composerInputGuard) reset() {
	g.resyncing = false
	g.headAt = time.Time{}
	g.pending = g.pending[:0]
}

// review 是闸门的**唯一入口**：由 routeKeyToComposerTextarea 对每个 KeyMsg 调用一次。
//
// 返回 (runes, ok)：
// - ok=false ⇒ 该键整体不得进入编辑器（被拆散的序列头，或确信是转义序列载荷）；
// - ok=true ⇒ 放行 runes（可能已被闸门收窄，也可能带回了重同步期间暂存的文字）。
func (g *composerInputGuard) review(msg tea.KeyMsg) ([]rune, bool) {
	if msg.Paste {
		// bracketed paste 是终端自己标记过的「一整块文本」，不是按键流 ⇒ 不进本闸门。
		// 其中的控制字符由 insertComposerText 的清洗负责（同一不变量，另一入口）。
		g.reset()
		return msg.Runes, true
	}
	if msg.Type != tea.KeyRunes {
		g.reset()
		return nil, true
	}

	// 控制字符（ESC/C0/C1）永不进输入框 —— 它们只可能来自终端协议本身。
	runes := stripControlRunes(msg.Runes)
	if len(runes) == 0 {
		return nil, false
	}

	// 被拆散的 ESC 头：ESC 被解析器当 Alt 吃掉，交到我们手里的只剩 '[' 或 'O'。
	// 这是本闸门存在的全部理由 —— 它不是用户想输入的 `[`，而是半个控制序列。
	if msg.Alt && len(runes) == 1 && (runes[0] == '[' || runes[0] == 'O') {
		g.beginResync(string(runes))
		return nil, false
	}

	if !g.resyncing {
		return runes, true
	}

	// 超时兜底：序列载荷不会姗姗来迟（与头同批到达）。等过头了 ⇒ 那是用户在打字，全部放行。
	if time.Since(g.headAt) > composerResyncTimeout {
		out := append([]rune(nil), g.pending...)
		out = append(out, runes...)
		g.reset()
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}

	// 重同步窗口：累积 + 判定
	g.pending = append(g.pending, runes...)
	switch {
	case looksLikeCompleteCSIPayload(g.pending):
		g.dropped += len(g.pending)
		log.Printf("[tui] composer input guard: dropped escape-sequence payload %q (dropped=%d)",
			string(g.pending), g.dropped)
		g.reset()
		return nil, false
	case looksLikeCSIPayloadPrefix(g.pending):
		// 仍只是前缀（如 `<0;57;`）⇒ 继续等，先不放进输入框。
		return nil, false
	default:
		// 不再是载荷 ⇒ 这是用户真的在打字。**原样放行**，绝不吞掉用户数据。
		out := append([]rune(nil), g.pending...)
		g.reset()
		return out, true
	}
}

// stripControlRunes 去掉不该出现在输入框里的控制字符：C0（含 ESC）、DEL、C1。
// 保留 `\n`（换行是输入框的合法内容）与 `\t`（缩进）。
// 与 review 同一不变量（输入框只接受文本），供显式粘贴等非按键入口调用。
func stripControlRunes(runes []rune) []rune {
	kept := runes[:0:0]
	for _, r := range runes {
		if r == '\n' || r == '\t' {
			kept = append(kept, r)
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
