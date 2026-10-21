package tui

import "github.com/anchorandfish/whale-code/internal/session"

// toolResultSeq 是会话内「工具结果序号」的**唯一口径**（1-based，按时间正序）。
//
// 为什么必须有这一个 SSOT（C1 切口②，2026-09-24）：
//
//	/tool 的 [N/M] 与折叠省略行里的「/tool <N>」指向的是同一个东西。若两处各自遍历计数，
//	就是同一语义的第二份实现 —— 任一侧改动（过滤条件、compact 行为、块粒度）都会让
//	提示与返回再次脱钩，回到「看着第 3 条折叠块、照提示输入却拿到第 11 条」的缺陷。
//	⇒ 两处**必须**都走这里；本文件是唯一遍历点。
//
// 粒度：与 /tool 一致，按 **tool_result 块**计数（一条 RoleTool 消息可含多个块）。
//
// ⚠️ 已知局限（不在本批治理范围，登记在
// `C1切口②_tool序号口径_核查回执_2026-09-23.md` §4）：compact 会移除早期消息 ⇒
// 序号随 live 会话漂移。同源只保证「提示与返回一致」，**不**保证跨压缩稳定。
type toolResultSeq struct {
	byMsg   map[string][]int // 消息 UUID → 该消息内各 tool_result 块的序号（按块序）
	entries []toolResultEntry
}

// toolResultEntry 是一条工具结果（/tool 的数据来源）。
type toolResultEntry struct {
	idx  int // 1-based 序号，与折叠提示里的 "/tool <N>" 同源
	name string
	out  string
	err  bool
}

// newToolResultSeq 遍历一次会话消息，同时产出「/tool 用的条目表」与「折叠提示用的序号表」。
// 两个消费方共享同一次遍历 —— 这是"同源"的结构保证，不是靠注释约定。
func newToolResultSeq(msgs []session.Message) toolResultSeq {
	s := toolResultSeq{byMsg: make(map[string][]int)}
	seq := 0
	for _, msg := range msgs {
		if msg.Role != session.RoleTool {
			continue
		}
		var seqs []int
		for _, b := range msg.Blocks {
			if b.Type != "tool_result" {
				continue
			}
			seq++
			seqs = append(seqs, seq)
			s.entries = append(s.entries, toolResultEntry{
				idx:  seq,
				name: b.ToolName,
				out:  b.Output,
				err:  b.IsError,
			})
		}
		if len(seqs) > 0 {
			s.byMsg[msg.UUID] = seqs
		}
	}
	return s
}

// total 返回会话内工具结果总数（/tool 的 [N/M] 里的 M）。
func (s toolResultSeq) total() int { return len(s.entries) }

// seqOf 返回 msg 内第 toolIdx 个 tool_result 块（0-based，只数 tool_result）的序号。
// 查不到 ⇒ 0，调用方按 0 退化为「无序号提示」（不猜测、不编造）。
func (s toolResultSeq) seqOf(msg session.Message, toolIdx int) int {
	seqs, ok := s.byMsg[msg.UUID]
	if !ok || toolIdx < 0 || toolIdx >= len(seqs) {
		return 0
	}
	return seqs[toolIdx]
}

// next 返回「下一条将被追加的工具结果」的序号（实时路径用：本条尚未写进 session）。
func (s toolResultSeq) next() int { return s.total() + 1 }
