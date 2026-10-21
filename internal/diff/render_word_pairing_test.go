package diff

import (
	"strings"
	"testing"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// 本文件是 的判别器。
//
// 缺陷现象：edit_file 的 diff 把「删除行」渲染成了新内容 —— `-` 行与 `+` 行文本完全相同。
// 根因两处（同在 internal/diff/render.go）：
// ① 词级渲染靠"第一个非 Equal 段是 Delete 还是 Insert"**推断**这行属于旧端还是新端，
// 方向反了 ⇒ 新增文本被写进删除行；
// ② Del/Ins 配对只看"紧邻"，遇 Del,Del,Ins,Ins 整块会串行（第 2 个 Del 配到第 1 个 Ins）。
//
// 判别力要求：本组测试在修复前必须**红**（不是编译错，是断言失败）。

// parseRenderedSides 从渲染结果中提取 `-` 行 / `+` 行的可见文本（│ 之后、符号之后的部分）。
func parseRenderedSides(t *testing.T, rendered string) (minus, plus []string) {
	t.Helper()
	for _, line := range strings.Split(stripANSI(rendered), "\n") {
		idx := strings.Index(line, "│")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("│"):]
		switch {
		case strings.HasPrefix(rest, "-"):
			minus = append(minus, rest[1:])
		case strings.HasPrefix(rest, "+"):
			plus = append(plus, rest[1:])
		}
	}
	return minus, plus
}

// normalizeTabs：lipgloss 渲染时把制表符展开为 4 个空格，期望值需做同样归一化后再比对。
func normalizeTabs(lines []string) []string {
	if lines == nil {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.ReplaceAll(l, "\t", "    ")
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestWordDiff_DeletedLineMustNotContainInsertedText 是本缺陷的主判别器。
// 不变式：渲染出的每一行删除行，必须**逐字等于**旧文件对应行；
//
//	每一行插入行，必须逐字等于新文件对应行。
//
// 违反即说明渲染把一侧的文本串到了另一侧。
func TestWordDiff_DeletedLineMustNotContainInsertedText(t *testing.T) {
	cases := []struct {
		name      string
		old       string
		new       string
		wantMinus []string
		wantPlus  []string
	}{
		{
			// 修复前必红：单 Del + 单 Ins 紧邻 ⇒ 配对正确 ⇒ 进词级路径 ⇒ isOld 判反 ⇒ 删除行含 ", 0"
			name:      "单行紧邻替换",
			old:       "package main\n\nfunc main() {\n\tprintln(Add(1, 2))\n\tprintln(ComputeTotal(5))\n}\n",
			new:       "package main\n\nfunc main() {\n\tprintln(Add(1, 2, 0))\n\tprintln(ComputeTotal(5))\n}\n",
			wantMinus: []string{"\tprintln(Add(1, 2))"},
			wantPlus:  []string{"\tprintln(Add(1, 2, 0))"},
		},
		{
			// Del,Del,Ins,Ins 整块：旧实现配对串行；修复后须仍逐行正确
			name:      "多行成块替换",
			old:       "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
			new:       "package main\n\nfunc Add(a, b, c int) int {\n\treturn a + b + c\n}\n",
			wantMinus: []string{"func Add(a, b int) int {", "\treturn a + b"},
			wantPlus:  []string{"func Add(a, b, c int) int {", "\treturn a + b + c"},
		},
		{
			// 删除数 > 插入数：多出的删除行不得越界配到别的东西
			name:      "删多插少",
			old:       "a\nkeep1\ndrop1\ndrop2\nkeep2\n",
			new:       "a\nkeep1\nmerged\nkeep2\n",
			wantMinus: []string{"drop1", "drop2"},
			wantPlus:  []string{"merged"},
		},
		{
			// 纯插入：不应出现任何删除行
			name:      "纯插入",
			old:       "a\nb\n",
			new:       "a\nx\ny\nb\n",
			wantMinus: nil,
			wantPlus:  []string{"x", "y"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMinus, gotPlus := parseRenderedSides(t, Render(FileChange{
				Path:       "case.go",
				OldContent: tc.old,
				NewContent: tc.new,
			}, DefaultConfig()))

			wantMinus, wantPlus := normalizeTabs(tc.wantMinus), normalizeTabs(tc.wantPlus)
			if !eqStrings(gotMinus, wantMinus) {
				t.Errorf("删除行渲染错\n got = %q\nwant = %q", gotMinus, wantMinus)
			}
			if !eqStrings(gotPlus, wantPlus) {
				t.Errorf("插入行渲染错\n got = %q\nwant = %q", gotPlus, wantPlus)
			}
		})
	}
}

// TestGutter_EachLineShowsOnlyItsOwnSideNumber 是 gutter 语义的判别器。
// 设计意图（见 renderLineWithGutter 注释）：
//
//	删除行 ` 42 │ - removed` —— 旧侧有号，新侧留空（删掉的行没有"新行号"）
//	新增行 ` 43 │ + added` —— 旧侧留空，新侧有号
//
// 旧实现把两侧行号一律填满（`42 42 │- ...`），与注释声明的语义不符。
func TestGutter_EachLineShowsOnlyItsOwnSideNumber(t *testing.T) {
	rendered := Render(FileChange{
		Path:       "test.go",
		OldContent: "a\nb\nc\n",
		NewContent: "a\nB\nc\n",
	}, DefaultConfig())

	var seenMinus, seenPlus bool
	for _, line := range strings.Split(stripANSI(rendered), "\n") {
		idx := strings.Index(line, "│")
		if idx < 0 {
			continue
		}
		gutterFields := strings.Fields(line[:idx])
		rest := line[idx+len("│"):]

		switch {
		case strings.HasPrefix(rest, "-"):
			seenMinus = true
			if len(gutterFields) != 1 {
				t.Errorf("删除行 gutter 只应有旧行号 1 个字段，实际 %q", gutterFields)
			}
		case strings.HasPrefix(rest, "+"):
			seenPlus = true
			if len(gutterFields) != 1 {
				t.Errorf("插入行 gutter 只应有新行号 1 个字段，实际 %q", gutterFields)
			}
		case strings.HasPrefix(rest, " "):
			if len(gutterFields) != 2 {
				t.Errorf("上下文行 gutter 应有旧/新两个行号，实际 %q", gutterFields)
			}
		}
	}
	if !seenMinus || !seenPlus {
		t.Fatalf("本用例应同时含删除行与插入行，实际 minus=%v plus=%v", seenMinus, seenPlus)
	}
}

// TestPairHunkLines_AlignsByOrdinalInBlock 直接判别配对函数：块内按序号对齐，而非"看紧邻"。
func TestPairHunkLines_AlignsByOrdinalInBlock(t *testing.T) {
	lines := []LineDiff{
		{Type: diffmatchpatch.DiffDelete, Text: "D1"},
		{Type: diffmatchpatch.DiffDelete, Text: "D2"},
		{Type: diffmatchpatch.DiffInsert, Text: "I1"},
		{Type: diffmatchpatch.DiffInsert, Text: "I2"},
	}
	pairs := pairHunkLines(lines)

	want := []string{"I1", "I2", "D1", "D2"}
	if !eqStrings(pairs, want) {
		t.Errorf("块内配对未按序号对齐\n got = %q\nwant = %q", pairs, want)
	}
}
