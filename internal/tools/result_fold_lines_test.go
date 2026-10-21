package tools

import (
	"fmt"
	"strings"
	"testing"
)

// 守卫：工具结果折叠的**行定义不变量**。
//
// 不变量（I1，`大修方案_TUI历史归属与回执可见性_2026-09-23.md` §4）：
//
//	head 5 / tail 5 / omitted = N − 10 对**任意** N > 10 恒成立，
//	其中 N = 内容行数（**不含**结尾换行符产生的空元素）。
//
// 为什么必须带**尾换行**构造输入：
// 既有 TestFormatToolResultFold 全部用 `strings.Join(lines, "\n")` 构造（无尾换行），
// 而生产形态（read_file / bash 等）**以 "\n" 结尾**。裸 strings.Split 会多出一个空元素，
// 于是 ①省略数多算 1（显示 +11）②该空元素占掉 tail 一个名额 ⇒ 尾块只剩 4 行真内容。
// 换句话说：**这条生产路径此前零判据**，守卫必须钉在"带尾换行"这一格上。
//
// 判别性自检（before 必须红）：把 `splitResultLines(output)` 退回 `strings.Split(output, "\n")`
// 并把非折叠分支的 `strings.Join(lines, "\n")` 退回 `output`（见回执 §A/B），
// 本文件的两条用例必须**精确转红**。
func TestFormatToolResultStruct_TrailingNewline_HeadTailOmittedInvariant(t *testing.T) {
	const suffix = "_END"
	// 生产形态：20 行内容，**每行都以 "\n" 结尾**（即整个 output 以 "\n" 收尾）。
	var sb strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&sb, "%d→LINE_MARKER_%d%s\n", i, i, suffix)
	}
	got := FormatToolResultStruct(Result{ID: "1", Name: "read_file", Output: sb.String()})

	// ① omitted 恒 = N − (head+tail) = 10
	if !strings.Contains(got, "… +10 lines") {
		t.Errorf(": N=20 时省略数应为 10，got:\n%s", got)
	}
	if strings.Contains(got, "+11 lines") {
		t.Errorf(": 省略数不得为 11（结尾换行的空元素不参与行计数）:\n%s", got)
	}

	// ② head = 第 1..5 行
	for i := 1; i <= 5; i++ {
		want := fmt.Sprintf("LINE_MARKER_%d%s", i, suffix)
		if !strings.Contains(got, want) {
			t.Errorf(": head 应含第 %d 行 %s，got:\n%s", i, want, got)
		}
	}

	// ③ tail = 第 16..20 行（**5 行真内容** —— 旧实现只有 4 行）
	for i := 16; i <= 20; i++ {
		want := fmt.Sprintf("LINE_MARKER_%d%s", i, suffix)
		if !strings.Contains(got, want) {
			t.Errorf(": tail 应含第 %d 行 %s（尾块必须 5 行真内容），got:\n%s", i, want, got)
		}
	}

	// ④ 中间行不得出现
	for i := 6; i <= 15; i++ {
		if strings.Contains(got, fmt.Sprintf("LINE_MARKER_%d%s", i, suffix)) {
			t.Errorf(": 不应含中间行 %d，got:\n%s", i, got)
		}
	}

	// ⑤ 可见真内容行数恒 = head + tail = 10
	if n := strings.Count(got, "LINE_MARKER_"); n != 10 {
		t.Errorf(": 可见真内容行数应恒为 10（head5+tail5），got %d:\n%s", n, got)
	}
}

// TestFormatToolResultStruct_TrailingNewline_Boundaries 带尾换行的边界格（表驱动）。
func TestFormatToolResultStruct_TrailingNewline_Boundaries(t *testing.T) {
	const suffix = "_END"
	mk := func(n int) string {
		var sb strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&sb, "%d→LINE_MARKER_%d%s\n", i, i, suffix)
		}
		return sb.String()
	}

	cases := []struct {
		name        string
		n           int
		wantFold    bool
		wantOmitted int
		wantVisible int
	}{
		{"N=1 单行+尾换行 不折叠", 1, false, 0, 1},
		{"N=10 恰等 head+tail 不折叠", 10, false, 0, 10},
		{"N=11 边界 折叠且省略 1", 11, true, 1, 10},
		{"N=20 折叠且省略 10", 20, true, 10, 10},
		{"N=21 折叠且省略 11", 21, true, 11, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mk(tc.n)
			got := FormatToolResultStruct(Result{ID: "1", Name: "cat", Output: out})
			folded := strings.Contains(got, "… +")
			if folded != tc.wantFold {
				t.Fatalf(": 折叠与否不符：wantFold=%v got:\n%s", tc.wantFold, got)
			}
			if !tc.wantFold {
				// 非折叠分支也必须按"唯一定义"渲染：不得保留结尾换行带来的空行
				if strings.HasSuffix(got, "\n") {
					t.Errorf(": 非折叠输出不应以换行结尾（尾换行已归一），got %q", got)
				}
				if n := strings.Count(got, "LINE_MARKER_"); n != tc.wantVisible {
					t.Errorf(": 可见行数应 %d，got %d:\n%s", tc.wantVisible, n, got)
				}
				return
			}
			if want := fmt.Sprintf("… +%d lines", tc.wantOmitted); !strings.Contains(got, want) {
				t.Errorf(": 省略数应为 %d，got:\n%s", tc.wantOmitted, got)
			}
			if n := strings.Count(got, "LINE_MARKER_"); n != tc.wantVisible {
				t.Errorf(": 折叠后可见真内容行数应 %d，got %d:\n%s", tc.wantVisible, n, got)
			}
		})
	}
}

// TestFormatToolResultStruct_EmptyAndOnlyNewlines 边界：空输出 / 仅换行 不得触发折叠、
// 不得因归一化把非空输出吞成空。
func TestFormatToolResultStruct_EmptyAndOnlyNewlines(t *testing.T) {
	for _, in := range []string{"", "\n", "\n\n"} {
		got := FormatToolResultStruct(Result{ID: "1", Name: "cat", Output: in})
		if strings.Contains(got, "… +") {
			t.Errorf(": 空/仅换行输入不应折叠，input=%q got:\n%s", in, got)
		}
	}
	// "a\nb\n" ⇒ 2 行，内容保留
	got := FormatToolResultStruct(Result{ID: "1", Name: "cat", Output: "a\nb\n"})
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf(": 内容不得被归一化吞掉，got:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf(": 尾换行应被归一，got %q", got)
	}
}
