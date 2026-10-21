package tui

// table_width_test.go — ：表格必须按真实终端宽度排版（列不错位）。
//
// 根因：正文渲染走 glamour，而 `NewMarkdownRenderer` 把 WordWrap 硬写成 0
// ⇒ glamour 的表格按**内容自然宽度**铺开（`ansi/table.go` 的 `table.New().Width(0)`，
// 而 lipgloss 里 `MaxWidth(0)` 是 no-op），超宽处只能被 TUI 唯一折行点
// `ansi.Hardwrap` **拦腰硬切** ⇒ 三列表格的列分隔符错位（用户报障：拉宽终端即恢复）。
//
// 判据（先红后绿）：窄宽渲染一张已知三列表 ⇒
// ① 任意一行的**显示宽度** ≤ width（按显示宽度算，CJK 宽字符必须算 2 列 —— 本仓 G-05 踩过）；
// ② 表格各行里 `│` 出现的**列位置逐行相同**（同列对齐）。
//
// ★ 断言一律按 rune 数/列数不混用：①② 都用 runewidth 的显示宽度口径。

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const w051TableMarkdown = "| 字段 | 值 | 谁定的 |\n" +
	"|---|---|---|\n" +
	"| 权限收模式 | 8→3（两套危险命令别混淆） | 已拍板 |\n" +
	"| 模式名→Level | permission.LookupModeName | 装配层 |\n" +
	"| 空 token | 强制回环（S5） | daemon/auth.go |\n"

// w051TableSeparatorColumns 返回文本里含表格竖线的行、及其竖线的显示列位置。
func w051TableSeparatorColumns(rendered string) [][]int {
	var out [][]int
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.Contains(line, "│") {
			continue
		}
		var cols []int
		x := 0
		for _, r := range line {
			if r == '│' {
				cols = append(cols, x)
			}
			x += runewidth.RuneWidth(r)
		}
		out = append(out, cols)
	}
	return out
}

func TestTableRendersWithinWidthAndColumnsAlign(t *testing.T) {
	const width = 60

	rendered := ansi.Strip(renderTextContentWidth(w051TableMarkdown, width))
	if strings.TrimSpace(rendered) == "" {
		t.Fatal("夹具不健全：渲染结果为空，本判据不可判别")
	}

	// ① 任意一行显示宽度不得超过 width。
	for _, line := range strings.Split(rendered, "\n") {
		if w := runewidth.StringWidth(line); w > width {
			t.Errorf("行宽 %d > %d（表格被撑出内容宽度，后续必被 Hardwrap 拦腰硬切）：\n%q", w, width, line)
		}
	}

	// ② 表格各行的竖线列位置必须逐行一致（同列对齐）。
	rows := w051TableSeparatorColumns(rendered)
	if len(rows) < 3 {
		t.Fatalf("夹具不健全：只识别出 %d 行含竖线，本判据不可判别（渲染：\n%s）", len(rows), rendered)
	}
	want := rows[0]
	if len(want) < 2 {
		t.Fatalf("夹具不健全：首行只有 %d 条竖线，无法断言列对齐", len(want))
	}
	for i, got := range rows {
		if len(got) != len(want) {
			t.Errorf("第 %d 行竖线数 %d ≠ 首行 %d（列数不一致 ⇒ 表已错位）", i, len(got), len(want))
			continue
		}
		for j := range got {
			if got[j] != want[j] {
				t.Errorf("第 %d 行第 %d 条竖线在第 %d 列，首行在同一条为第 %d 列 ⇒ 列边界未对齐",
					i, j, got[j], want[j])
			}
		}
	}
}

// TestTableWidthFollowsTerminalWidth —— 宽度变化必须真的改变排版。
//
// 防"参数传了但没生效"（R4-13）：宽度是 glamour 的唯一排版输入，若 rendererForWidth
// 不按宽度分桶（例如永远返回同一个零宽渲染器），本判据会红。
func TestTableWidthFollowsTerminalWidth(t *testing.T) {
	narrow := ansi.Strip(renderTextContentWidth(w051TableMarkdown, 40))
	wide := ansi.Strip(renderTextContentWidth(w051TableMarkdown, 120))

	narrowMax := 0
	for _, line := range strings.Split(narrow, "\n") {
		if w := runewidth.StringWidth(line); w > narrowMax {
			narrowMax = w
		}
	}
	if narrowMax > 40 {
		t.Errorf("窄宽 40 下最大行宽 %d > 40（宽度未生效）", narrowMax)
	}
	if narrow == wide {
		t.Error("宽度 40 与 120 的渲染结果完全相同 ⇒ 宽度没有真正参与排版（rendererForWidth 未按宽度分桶）")
	}
}
