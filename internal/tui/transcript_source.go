// transcript_source.go — 转录 source map（批 C / ＋）。
//
// 第一性问题：**复制的真值由谁定义？**
//
//	屏幕是**有损投影**：它会折叠（工具结果 head+tail）、截断（思考预览 60 列、
//	命令 160 rune）、硬折行（ansi.Hardwrap 插入真实 \n）。真值一旦定在屏幕，
//	每加一种压缩，复制就多欠一笔债 —— 这正是 「补了工具结果折叠，又漏了
//	思考 / 命令」的成因（同一病的三种长相）。
//
// 修法（换前提，不是补丁）：**谁做的省略，谁登记这一行的真值**；复制层只消费登记表，
// 不做任何字面匹配。登记表与显示行**一一对齐**，并随折行一起传播（N 条视觉行共享
// 同一条逻辑行的来源）⇒ 硬折行不需要单独规则：复制按**逻辑行**取数，
// 天然只取一次，读不到折后产物。
//
// 与旧实现的关系：旧 `mouse_selection_copy_source.go` 用**屏幕可见字面**
// （`… +N lines (/tool K)`）反查折叠点 —— 那是"手搓 source map 的雏形，且只覆盖
// 一种省略"。本文件取代它：登记发生在**产生省略的那段渲染代码**里（不是事后扫描
// 字符串重建），覆盖全部省略种类，且折行不再是问题（：同一语义两处各写一份
// 就是复发坑；R4：可判定的结构化事实不得由散文关键词断言）。
package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// lineSource 是渲染层为一条转录显示行登记的**真值来源**（source map 的一格）。
//
// 零值 = 普通行：该行的真值就是它自身的可见字面（复制按显示列切片）。
type lineSource struct {
	// full 非空 ⇒ 该显示行的可见字面是**省略摘要**，真值是 full（完整原文，可含换行）。
	// 复制时整段替换，**不按显示列切片** —— 摘要的列号与原文之间不存在对应关系。
	//
	// 取值与会话权威源同源（`/copy all`、`/tool N` 读同一份事实），由产生该省略的
	// 渲染代码在**同一次计算**里登记：工具结果折叠取 tools.ToolFoldMarkerFor 的 hidden，
	// 思考折叠取思考全文，命令截断取完整命令行。三处都只消费、不再各自重建。
	full string
}

// hasOverride 报告该行是否登记了「原文覆盖」（即该行是省略行）。
func (s lineSource) hasOverride() bool { return s.full != "" }

// displayEntry 是显示缓冲的一条目：**渲染文本** + 逐行的**真值来源**。
//
// sources 与 strings.Split(text, "\n") **等长**（同一坐标系）；nil ⇒ 全普通行。
// 与旧实现（复制时按屏幕字面反查）相比，这里把"这一行代表什么"在**产生它的那一刻**
// 就钉在数据上，事后无需再猜 —— 于是不会再出现「字面稍变 ⇒ 静默失配 ⇒ 退回修前」。
type displayEntry struct {
	text    string
	sources []lineSource
}

// sourceAt 取来源表的第 i 格；越界返回零值（普通行）。
func sourceAt(sources []lineSource, i int) lineSource {
	if i < 0 || i >= len(sources) {
		return lineSource{}
	}
	return sources[i]
}

// lineSourcesFromOverrides 把「行号 → 真值」稀疏表铺成与 lineCount 等长的来源表。
//
// 供「渲染产地」登记省略行时使用：产地知道自己产出的文本有几行、省略行落在第几行。
func lineSourcesFromOverrides(lineCount int, overrides map[int]string) []lineSource {
	if lineCount <= 0 || len(overrides) == 0 {
		return nil
	}
	out := make([]lineSource, lineCount)
	for i, full := range overrides {
		if i >= 0 && i < lineCount {
			out[i] = lineSource{full: full}
		}
	}
	return out
}

// lineCountOf 返回文本行数（与 strings.Split(text,"\n") 同口径）。
func lineCountOf(text string) int { return strings.Count(text, "\n") + 1 }

// displayMessages 返回**渲染用**的显示条目文本（displayEntries 的文本投影）。
//
// 保留本函数供"只要文本"的调用点使用；需要 source map 的调用点走 displayEntries。
func (m *Model) displayMessages() []string {
	entries := m.displayEntries()
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.text
	}
	return out
}

// displayEntries 返回带 source map 的显示条目：显示侧消息 ＋ 每轮的历史边界标记。
//
// 边界标记**不是消息**（不进 messageBuffer、更不进模型侧 session），只在这里被投影。
// 与本仓 A-3 / 切口③的既有契约一致；本函数只是把 displayMessages 的结果升级为
// 「文本 + 逐行来源」。两条渲染路径（虚拟滚动 / 全量拼接）都必须经过它。
func (m *Model) displayEntries() []displayEntry {
	entries := m.sessionService.MessageEntries()
	marks := m.turn.marks
	if len(marks) == 0 {
		return entries
	}
	out := make([]displayEntry, 0, len(entries)+len(marks))
	mi := 0
	// emit 是"一轮的收尾区"的唯一输出点：卡片（摘要行）＋结束标志（A-3），
	// 作为**同一个显示条目**输出 —— 不是两个条目（理由见 format.go displayMessages）。
	emit := func(mk turnMark) {
		card := formatTurnChangesCard(mk.changes, m.workDir)
		boundary := formatTurnCompleteBoundary(mk.at, mk.interrupted)
		if card != "" {
			out = append(out, displayEntry{text: card + "\n" + boundary})
			return
		}
		out = append(out, displayEntry{text: boundary})
	}
	for mi < len(marks) && marks[mi].after > len(entries) {
		mi++
	}
	for mi < len(marks) && marks[mi].after <= 0 {
		emit(marks[mi])
		mi++
	}
	for i := 0; i < len(entries); i++ {
		out = append(out, entries[i])
		for mi < len(marks) && marks[mi].after <= i+1 {
			emit(marks[mi])
			mi++
		}
	}
	return out
}

// lastNonBlankLine 返回多行文本中最后一条非空行的 trim 结果（渲染拼接用）。
func lastNonBlankLine(text string) string {
	last := ""
	for _, ln := range strings.Split(ansi.Strip(text), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			last = t
		}
	}
	return last
}
