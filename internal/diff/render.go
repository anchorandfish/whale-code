package diff

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// diffStyles 一次 diff 渲染用到的全部样式。
//
// 为什么收口成结构体而不是散落的包级变量：
//
//	颜色是**渲染层**的关注点，工具结果（数据层）里不该出现 ANSI（见 PlainConfig）。
//	同一个 Render 要同时服务两种输出：彩色（终端展示）与纯文本（工具结果 / 剪贴板 /
//	模型上下文）。若写成两份渲染逻辑，gutter 几何、行号推进、词级配对这些必然漂移 ——
//	那是「同语义两处实现」的老坑。
//
// 收口后的可变维度只剩「样式表」一个：纯文本模式把所有字段换成空样式，
// 渲染逻辑一行不改。样式表的解析点唯一，见 stylesFor。
type diffStyles struct {
	header  lipgloss.Style // --- a/ +++ b/ @@ 头
	added   lipgloss.Style // + 行底色
	removed lipgloss.Style // - 行底色
	context lipgloss.Style // 上下文行与 gutter 行号
	wordAdd lipgloss.Style // 词级新增高亮（亮绿粗体）
	wordDel lipgloss.Style // 词级删除高亮（亮红粗体）
	statAdd lipgloss.Style // 统计 +N
	statDel lipgloss.Style // 统计 -M
}

// plainStyleSet 纯文本样式表：全空样式，Render 后字符串逐字等于输入。
//
// 依赖 lipgloss 的一条性质：无 Foreground/Background/Bold/Width/Padding 的样式
// 不对文本做任何改写。这条性质由 TestPlainStylesAreIdentity 钉住 ——
// 上游 lipgloss 一旦改变该行为，守卫测试先红，而不是等用户在剪贴板里发现乱码。
var plainStyleSet = diffStyles{
	header:  lipgloss.NewStyle(),
	added:   lipgloss.NewStyle(),
	removed: lipgloss.NewStyle(),
	context: lipgloss.NewStyle(),
	wordAdd: lipgloss.NewStyle(),
	wordDel: lipgloss.NewStyle(),
	statAdd: lipgloss.NewStyle(),
	statDel: lipgloss.NewStyle(),
}

// colorStyleSet 彩色样式表（终端展示用）。
// ColorBlind 色盲主题若将来启用，分支只在这里加 —— 不要在渲染函数里散落 if。
func colorStyleSet() diffStyles {
	return diffStyles{
		header:  lipgloss.NewStyle().Foreground(lipgloss.Color("36")),
		added:   lipgloss.NewStyle().Background(lipgloss.Color("22")),
		removed: lipgloss.NewStyle().Background(lipgloss.Color("52")),
		context: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		wordAdd: lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true),
		wordDel: lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true),
		statAdd: lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		statDel: lipgloss.NewStyle().Foreground(lipgloss.Color("204")),
	}
}

// stylesFor 样式表的**唯一**解析点：渲染函数一律从这里取样式，不得自行 NewStyle。
func stylesFor(cfg RenderConfig) diffStyles {
	if cfg.Plain {
		return plainStyleSet
	}
	return colorStyleSet()
}

// Render 核心渲染入口，统一 diff 渲染逻辑
// 输出格式：--- a/file / +++ b/file 头 + @@ hunk 分组 + 行级内容
func Render(change FileChange, cfg RenderConfig) string {
	if int64(len(change.OldContent)+len(change.NewContent)) > cfg.MaxFileSize {
		return fmt.Sprintf("Large file - diff exceeds %.1f MB limit", float64(cfg.MaxFileSize)/1024/1024)
	}

	if change.OldContent == change.NewContent {
		return ""
	}

	// 查缓存
	ck := cacheKey(change, cfg)
	if cached, ok := globalCache.Get(ck); ok {
		return cached.rendered
	}

	baseName := change.Path
	if idx := strings.LastIndex(baseName, "/"); idx >= 0 {
		baseName = baseName[idx+1:]
	}

	st := stylesFor(cfg)

	var sb strings.Builder

	// ★ \n 必须放在 Render 之外：lipgloss 对**多行**输入会按最宽行补齐尾随空格，
	// 那些空格落在下一行行首 ⇒ 终端上表现为"前导空格忽多忽少"（2026-10-05 报障）。
	// 逐行 Render + 自行拼 \n，既消去补齐，也让彩色与纯文本逐字一致（见 render_plain_test.go）。
	sb.WriteString(st.header.Render(fmt.Sprintf("--- a/%s", baseName)) + "\n")
	sb.WriteString(st.header.Render(fmt.Sprintf("+++ b/%s", baseName)) + "\n")

	lineDiffs := ComputeLineDiffs(change.OldContent, change.NewContent)
	hunks := groupHunks(lineDiffs, cfg.ContextLines)

	lineCount := 0
	for _, hunk := range hunks {
		if lineCount >= cfg.MaxLines {
			sb.WriteString(fmt.Sprintf("\n... (%d more lines)\n", len(lineDiffs)-lineCount))
			return sb.String()
		}

		sb.WriteString(st.header.Render(fmt.Sprintf("@@ -%d,%d +%d,%d @@", hunk.OldStart, hunk.OldCount, hunk.NewStart, hunk.NewCount)) + "\n")

		// 计算行号列宽（基于 hunk 内最大行号）
		maxLineNum := max(hunk.OldStart+hunk.OldCount-1, hunk.NewStart+hunk.NewCount-1)
		width := digitCount(maxLineNum)

		oldLineNum := hunk.OldStart
		newLineNum := hunk.NewStart

		// 词级高亮的配对文本：与 hunk.Lines 索引一一对齐（见 pairHunkLines）
		pairs := pairHunkLines(hunk.Lines)

		for li := 0; li < len(hunk.Lines); li++ {
			if lineCount >= cfg.MaxLines {
				sb.WriteString(fmt.Sprintf("\n... (%d more lines)\n", len(lineDiffs)-lineCount))
				return sb.String()
			}

			ld := hunk.Lines[li]

			// 配对文本来自预计算表（按块内序号对齐）
			pairText := pairs[li]

			// 渲染 gutter + 行内容（含词级高亮）
			sb.WriteString(renderLineWithGutter(ld, oldLineNum, newLineNum, width, pairText, cfg.WordThreshold, st))

			// 行号推进按行类型：Equal 两侧都进，Delete 只进旧侧，Insert 只进新侧。
			// （旧实现用 hunk 级的 oldAdvance/newAdvance 判据；当 OldCount/NewCount==0 时
			// 该侧本就没有任何行，推进与否不可见，故按行类型判定与旧行为等价且更严格。）
			switch ld.Type {
			case diffmatchpatch.DiffEqual:
				oldLineNum++
				newLineNum++
			case diffmatchpatch.DiffDelete:
				oldLineNum++
			case diffmatchpatch.DiffInsert:
				newLineNum++
			}
			lineCount++
		}
	}

	result := sb.String()
	// 写缓存
	globalCache.Set(ck, cacheEntry{rendered: result, lineDiffs: lineDiffs})
	// 写内容哈希索引，供 CountChanges 查找 cacheEntry.lineDiffs
	contentHashIndex.Add(change.Hash(), ck)
	return result
}

// Hunk 表示一个 diff hunk（变更块 + 上下文）。
//
// G5：由 unexported `hunk` 提升为导出类型 —— unified diff 的**解析**侧
// （unified.go 的 ParseUnifiedDiff）与**渲染**侧现在共用同一个 hunk 模型，
// 不再各造一个结构体（两套同形结构必然漂移）。
type Hunk struct {
	OldStart int        // 旧文件起始行号（1-based）
	OldCount int        // 旧文件行数
	NewStart int        // 新文件起始行号（1-based）
	NewCount int        // 新文件行数
	Lines    []LineDiff // hunk 包含的行
}

// groupHunks 将 lineDiffs 按 contextLines 分组为 hunk 列表
// 算法：识别变更块 → 合并相邻变更块（间距 ≤ 2*contextLines）→ 扩展上下文 → 构建 hunk
func groupHunks(lineDiffs []LineDiff, contextLines int) []Hunk {
	if len(lineDiffs) == 0 {
		return nil
	}

	// 第一遍：识别变更块
	type changeGroup struct {
		start, end int // lineDiffs 中的索引范围 [start, end)
	}
	var groups []changeGroup
	inChange := false
	groupStart := 0
	for i, ld := range lineDiffs {
		if ld.Type != diffmatchpatch.DiffEqual {
			if !inChange {
				groupStart = i
				inChange = true
			}
		} else {
			if inChange {
				groups = append(groups, changeGroup{start: groupStart, end: i})
				inChange = false
			}
		}
	}
	if inChange {
		groups = append(groups, changeGroup{start: groupStart, end: len(lineDiffs)})
	}

	if len(groups) == 0 {
		return nil
	}

	// 第二遍：合并相邻变更块（间距 ≤ 2*contextLines 的 Equal 行）
	merged := []changeGroup{groups[0]}
	for i := 1; i < len(groups); i++ {
		last := &merged[len(merged)-1]
		gap := groups[i].start - last.end
		if gap <= 2*contextLines {
			last.end = groups[i].end
		} else {
			merged = append(merged, groups[i])
		}
	}

	// 第三遍：为每个合并后的变更块构建 hunk（扩展上下文 + 计算行号）
	var hunks []Hunk
	for _, mg := range merged {
		hunkStart := max(0, mg.start-contextLines)
		hunkEnd := min(len(lineDiffs), mg.end+contextLines)

		oldStart, newStart := 1, 1
		// 计算 hunkStart 之前的行号偏移
		for i := 0; i < hunkStart; i++ {
			switch lineDiffs[i].Type {
			case diffmatchpatch.DiffEqual:
				oldStart++
				newStart++
			case diffmatchpatch.DiffDelete:
				oldStart++
			case diffmatchpatch.DiffInsert:
				newStart++
			}
		}

		oldCount, newCount := 0, 0
		for i := hunkStart; i < hunkEnd; i++ {
			switch lineDiffs[i].Type {
			case diffmatchpatch.DiffEqual:
				oldCount++
				newCount++
			case diffmatchpatch.DiffDelete:
				oldCount++
			case diffmatchpatch.DiffInsert:
				newCount++
			}
		}

		// count=0 的特殊表示（对齐 git diff 规范）
		if oldCount == 0 {
			oldStart = 0
		}
		if newCount == 0 {
			newStart = 0
		}

		hunks = append(hunks, Hunk{
			OldStart: oldStart,
			OldCount: oldCount,
			NewStart: newStart,
			NewCount: newCount,
			Lines:    lineDiffs[hunkStart:hunkEnd],
		})
	}

	return hunks
}

// pairHunkLines 为 hunk 内的 Del/Ins 变更块计算「配对文本」，返回与 lines 索引一一对齐的切片。
// 配对文本只用于词级高亮（判断哪些字符变了），为空则该行退化为整行色。
//
// 配对规则：把变更块视作「一段连续 Delete + 紧随的一段连续 Insert」，
// 两侧**按块内序号一一对齐**（第 k 个删除行 ↔ 第 k 个插入行），多出的一侧不留配对。
//
// 为什么不能"只看紧邻"：对 Del,Del,Ins,Ins 这种成块替换，紧邻判据会把
// 第 2 个删除行配到第 1 个插入行（串行），词级 diff 随即算错，
// 进而导致删除行显示成插入内容。按序号对齐是该问题的结构性修法。
func pairHunkLines(lines []LineDiff) []string {
	pairs := make([]string, len(lines))
	for i := 0; i < len(lines); {
		if lines[i].Type != diffmatchpatch.DiffDelete {
			i++
			continue
		}

		delStart := i
		for i < len(lines) && lines[i].Type == diffmatchpatch.DiffDelete {
			i++
		}
		delEnd := i

		insStart := i
		for i < len(lines) && lines[i].Type == diffmatchpatch.DiffInsert {
			i++
		}
		insEnd := i

		n := min(delEnd-delStart, insEnd-insStart)
		for k := 0; k < n; k++ {
			pairs[delStart+k] = lines[insStart+k].Text
			pairs[insStart+k] = lines[delStart+k].Text
		}
	}
	return pairs
}

// renderLineWithGutter 渲染单行 diff，带 gutter 行号和词级高亮
// 格式：oldLine newLine │ 符号 行内容
// 上下文行：` 42 43 │ lineContent`
// 删除行： ` 42 │ - removedContent`（词级高亮：变更词用亮红粗体前景）
// 新增行： ` 43 │ + addedContent`（词级高亮：变更词用亮绿粗体前景）
// pairText：配对行文本，用于词级 diff 计算（空则不做词级高亮）
// threshold：词级高亮阈值，变化比例 > threshold 时退化为整行色
// st：样式表，由 stylesFor(cfg) 解析（纯文本模式下为空样式 ⇒ 输出不含 ANSI）
func renderLineWithGutter(ld LineDiff, oldLine, newLine, width int, pairText string, threshold float64, st diffStyles) string {
	var gutter strings.Builder

	// 旧文件行号：上下文行与删除行才有（删掉的行没有"新行号"）
	if ld.Type == diffmatchpatch.DiffEqual || ld.Type == diffmatchpatch.DiffDelete {
		gutter.WriteString(fmt.Sprintf("%*d", width, oldLine))
	} else {
		gutter.WriteString(strings.Repeat(" ", width))
	}
	gutter.WriteString(" ")

	// 新文件行号：上下文行与新增行才有（新增的行没有"旧行号"）
	if ld.Type == diffmatchpatch.DiffEqual || ld.Type == diffmatchpatch.DiffInsert {
		gutter.WriteString(fmt.Sprintf("%*d", width, newLine))
	} else {
		gutter.WriteString(strings.Repeat(" ", width))
	}
	gutter.WriteString(" │")

	gutterStr := st.context.Render(gutter.String())

	switch ld.Type {
	case diffmatchpatch.DiffEqual:
		return gutterStr + st.context.Render(" "+ld.Text) + "\n"
	case diffmatchpatch.DiffDelete:
		content := renderWordContent(ld.Text, pairText, threshold, st.removed, st.wordDel)
		return gutterStr + st.removed.Render("-") + content + "\n"
	case diffmatchpatch.DiffInsert:
		content := renderWordContent(ld.Text, pairText, threshold, st.added, st.wordAdd)
		return gutterStr + st.added.Render("+") + content + "\n"
	}
	return ""
}

// renderWordContent 渲染行内容的词级高亮
// 若 pairText 非空且变化比例 ≤ threshold，变更词用 wordStyle 渲染，其余用 lineStyle 渲染
// 若 pairText 为空或变化比例 > threshold，退化为整行色（lineStyle）
//
// 段归属由 diff 的**方向**决定，不需要额外参数：
// ComputeWordDiff(text, pairText) 以 text 为源端，故
//
//	DiffDelete = 本行有、配对行没有的字符 ⇒ 属于本行，渲染并高亮；
//	DiffInsert = 配对行有、本行没有的字符 ⇒ 属于配对行，必须**跳过**。
//
// 旧实现把这段归属交给了"第一段是 Delete 还是 Insert"的推断，在
// 「本行相对配对行只少不多」（DiffInsert 开头）时判反，把对方独有的字符写进了本行 ——
// 这正是 edit_file 的删除行显示成新内容（- 行 == + 行）的根因。
//
// 注：删除行与新增行**都是**本函数，只是 text/pairText 的角色互换；
// 两侧都取 Delete 段并非笔误 —— 该段永远指"本行独有"。
func renderWordContent(text, pairText string, threshold float64, lineStyle, wordStyle lipgloss.Style) string {
	if pairText == "" || threshold <= 0 {
		return lineStyle.Render(text)
	}

	// 计算词级 diff
	// ComputeWordDiff(text, pairText) 的结果只用于判断哪些部分是变更的
	diffs := ComputeWordDiff(text, pairText)
	if diffs == nil || len(diffs) == 0 {
		return lineStyle.Render(text)
	}

	// 计算变化比例
	totalLen := len(text) + len(pairText)
	changedLen := 0
	for _, d := range diffs {
		if d.Type == diffmatchpatch.DiffInsert || d.Type == diffmatchpatch.DiffDelete {
			changedLen += len(d.Text)
		}
	}

	if totalLen > 0 && float64(changedLen)/float64(totalLen) > threshold {
		return lineStyle.Render(text)
	}

	// 词级高亮渲染：Equal 原样、Delete（本行独有）高亮、Insert（配对行独有）跳过。
	var sb strings.Builder
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			sb.WriteString(lineStyle.Render(d.Text))
		case diffmatchpatch.DiffDelete:
			sb.WriteString(wordStyle.Render(d.Text))
		case diffmatchpatch.DiffInsert:
			// 属于配对行，不渲染 —— 渲染它就会把对方独有的文本写进本行
		}
	}

	result := sb.String()
	if result == "" {
		return lineStyle.Render(text)
	}
	return result
}

// digitCount 计算正整数的位数
func digitCount(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}

// RenderWordLevel 已废弃，词级高亮已集成到 Render 中
// Deprecated: 使用 Render(change, cfg) 替代，Render 会根据 WordThreshold 自动应用词级高亮
func RenderWordLevel(oldText, newText string, threshold float64) string {
	diffs := ComputeWordDiff(oldText, newText)
	if diffs == nil || len(diffs) == 0 {
		return ""
	}

	var sb strings.Builder
	totalLen := len(oldText) + len(newText)
	changedLen := 0

	for _, d := range diffs {
		if d.Type == diffmatchpatch.DiffInsert || d.Type == diffmatchpatch.DiffDelete {
			changedLen += len(d.Text)
		}
	}

	if totalLen > 0 && float64(changedLen)/float64(totalLen) > threshold {
		return ""
	}

	sw := colorStyleSet()
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			sb.WriteString(sw.wordAdd.Render(d.Text))
		case diffmatchpatch.DiffDelete:
			sb.WriteString(sw.wordDel.Render(d.Text))
		default:
			sb.WriteString(d.Text)
		}
	}

	return sb.String()
}

// RenderTurn 渲染 turn 级别多文件 diff 汇总。
//
// 接线注记：本函数此前**生产零调用点**（N9 批只落了渲染器），
// 现由轮末折叠卡片与 `/diff` 消费。两处差异只在"知不知道轮次号"：
// - 卡片/实时路径知道（`TurnIndex > 0`）⇒ 打 `=== Turn N ===` 头；
// - `/diff` 在**会话恢复后**不知道（marks 不持久化，只能从索引反查锚点）⇒
// 传 `TurnIndex <= 0` 时**不打轮次头**（不假装知道，也不打 "Turn 0" 这种假信息）。
func RenderTurn(turn TurnDiff, cfg RenderConfig) string {
	st := stylesFor(cfg)

	var sb strings.Builder

	if turn.TurnIndex > 0 {
		sb.WriteString(fmt.Sprintf("=== Turn %d ===\n", turn.TurnIndex))
	}
	if turn.PromptPreview != "" {
		sb.WriteString(fmt.Sprintf("Prompt: %s\n", truncate(turn.PromptPreview, 100)))
	}

	var totalAdded, totalRemoved int
	for _, change := range turn.Files {
		rendered := Render(change, cfg)        // 先渲染，写缓存
		added, removed := CountChanges(change) // 再统计，走缓存快路径
		totalAdded += added
		totalRemoved += removed

		sb.WriteString(fmt.Sprintf("\n--- %s %s ---\n", change.Path, renderStatsWith(Stats{Insertions: added, Deletions: removed}, st)))
		sb.WriteString(rendered)
	}

	sb.WriteString(fmt.Sprintf("\n=== Total: %s ===\n", renderStatsWith(Stats{Insertions: totalAdded, Deletions: totalRemoved}, st)))

	return sb.String()
}

// RenderStats 渲染统计信息，+N -M 格式，绿+/红-
// 无 RenderConfig 可取样式，固定走彩色（终端展示场景）。
func RenderStats(stats Stats) string {
	return renderStatsWith(stats, colorStyleSet())
}

// renderStatsWith 按给定样式表渲染统计信息。
// 与 Render 共用同一份样式表 ⇒ turn 汇总在纯文本模式下同样不吐 ANSI。
func renderStatsWith(stats Stats, st diffStyles) string {
	var sb strings.Builder
	if stats.Insertions > 0 {
		sb.WriteString(st.statAdd.Render(fmt.Sprintf("+%d", stats.Insertions)))
	}
	if stats.Deletions > 0 {
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(st.statDel.Render(fmt.Sprintf("-%d", stats.Deletions)))
	}
	if sb.Len() == 0 {
		sb.WriteString("0")
	}
	return sb.String()
}

// CountChanges 从 FileChange 计算增删行数
// 快路径：从 globalCache 的 cacheEntry.lineDiffs 取值直接统计
// 慢路径：独立计算 ComputeLineDiffs（不写缓存，CountChanges 无 RenderConfig 参数无法构造完整 cacheKey）
func CountChanges(change FileChange) (added, removed int) {
	if change.OldContent == change.NewContent {
		return 0, 0
	}

	// 快路径：从 contentHashIndex 找 cacheKey，再从 globalCache 取 cacheEntry.lineDiffs
	if ck, ok := contentHashIndex.Get(change.Hash()); ok {
		if entry, ok := globalCache.Get(ck); ok && entry.lineDiffs != nil {
			for _, ld := range entry.lineDiffs {
				switch ld.Type {
				case diffmatchpatch.DiffInsert:
					added++
				case diffmatchpatch.DiffDelete:
					removed++
				}
			}
			return
		}
	}

	// 慢路径：独立计算（不写缓存）
	lineDiffs := ComputeLineDiffs(change.OldContent, change.NewContent)
	for _, ld := range lineDiffs {
		switch ld.Type {
		case diffmatchpatch.DiffInsert:
			added++
		case diffmatchpatch.DiffDelete:
			removed++
		}
	}
	return
}

// truncate 截断字符串到指定长度（UTF-8 安全，按 rune 计数）
func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen-3]) + "..."
}
