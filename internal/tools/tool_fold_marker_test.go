package tools

import (
	"fmt"
	"strings"
	"testing"
)

// tool_fold_marker_test.go — 钉住折叠的**三个产物同源**：
// 「显示文本」/「省略行字面」/「被省略的中段原文」。
//
// 为什么这层要单独钉：
// TUI 的鼠标拖选按 ToolFoldMarkerFor 给的省略行字面在渲染产物里定位折叠点，
// 再把中段还原成原文。若这层只给"显示文本"，复制侧就得自己再算一遍省略行与中段
// ⇒ 同一折叠语义出现第二份实现（同族），两侧必然漂移。
// 本文件保证：marker 一定是渲染产物里的那一行，hidden 一定是被它吃掉的那几行。

func TestToolFoldMarkerFor_MarkerIsExactlyTheRenderedLine(t *testing.T) {
	output := strings.Repeat("LINE\n", 30) // 30 行 > head5+tail5
	result := Result{Name: "bash", Output: output}
	hint := ToolFoldHint{Command: "/tool", Seq: 7}

	marker, _ := ToolFoldMarkerFor(result, MaxToolResultLength, hint)

	if marker == "" {
		t.Fatal("30 行输出必折叠，marker 不该为空")
	}
	if strings.Contains(marker, "\n") {
		t.Errorf("marker 必须是**单行**字面（复制侧按行比对），实际含换行：%q", marker)
	}
	rendered := FormatToolResultStructWithHint(result, MaxToolResultLength, hint)
	lines := strings.Split(rendered, "\n")
	var hits int
	for _, line := range lines {
		if line == marker {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("省略行字面必须在渲染产物里**恰好出现 1 次**（复制侧靠它定位折叠点），实际 %d 次\nmarker=%q\nrendered=\n%s",
			hits, marker, rendered)
	}
	if !strings.Contains(marker, "/tool 7") {
		t.Errorf("marker 应带上调用方的提示口径（/tool 7），实际 %q", marker)
	}
}

func TestToolFoldMarkerFor_HiddenIsTheOmittedMiddle(t *testing.T) {
	// 用**互不相同**的行内容，才能断言"被省略的正是中间那 20 行"：
	// 内容若有重复，切片边界写歪也可能恰好比对成功（判据失去判别力）。
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("L%02d", i))
	}
	output := strings.Join(lines, "\n")
	result := Result{Name: "bash", Output: output}

	_, hidden := ToolFoldMarkerFor(result, MaxToolResultLength, ToolFoldHint{Command: "/tool", Seq: 1})

	wantHidden := lines[ToolResultHeadLines : len(lines)-ToolResultTailLines]
	if len(hidden) != len(wantHidden) {
		t.Fatalf("中段应为 %d 行，实际 %d 行", len(wantHidden), len(hidden))
	}
	for i := range wantHidden {
		if hidden[i] != wantHidden[i] {
			t.Errorf("中段第 %d 行应为 %q，实际 %q", i, wantHidden[i], hidden[i])
		}
	}
	// head/tail 不得混进中段：中段首行必须紧接在 head 之后（lines[HeadLines]）。
	// 逐元素比对已覆盖这一点，此处再钉一次下标口径，防止将来有人把切片边界写歪
	// 却因"行内容恰好重复"而漏判（本夹具各行内容唯一，故可判别）。
	if hidden[0] != lines[ToolResultHeadLines] {
		t.Errorf("中段首行应为 head 之后的第 1 行 %q，实际 %q", lines[ToolResultHeadLines], hidden[0])
	}
}

func TestToolFoldMarkerFor_NoFoldCases(t *testing.T) {
	cases := []struct {
		name   string
		result Result
	}{
		{"短输出不折叠", Result{Name: "bash", Output: "a\nb\nc\n"}},
		{"恰好在阈值上不折叠", Result{Name: "bash", Output: strings.Repeat("x\n", ToolResultHeadLines+ToolResultTailLines)}},
		{"错误输出不折叠", Result{Name: "bash", Output: strings.Repeat("x\n", 30), IsError: true}},
		{"空输出占位不折叠", Result{Name: "bash", Output: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker, hidden := ToolFoldMarkerFor(tc.result, MaxToolResultLength, ToolFoldHint{Command: "/tool", Seq: 1})
			if marker != "" || hidden != nil {
				t.Errorf("不该产生折叠标记，实际 marker=%q hidden=%v", marker, hidden)
			}
		})
	}
}

// TestToolFoldMarkerFor_TrailingNewlineInvariant 钉住「以 \n 结尾的生产输出」这一形态
// （read_file 等真实工具的输出即此形态）：head 5 / tail 5 / omitted = N−10，
// 中段行数必须等于 marker 里声明的 +N。
func TestToolFoldMarkerFor_TrailingNewlineInvariant(t *testing.T) {
	output := strings.Repeat("ROW\n", 30) // 尾换行：真实生产形态
	marker, hidden := ToolFoldMarkerFor(Result{Name: "read_file", Output: output},
		MaxToolResultLength, ToolFoldHint{Command: "/tool", Seq: 3})

	if marker == "" {
		t.Fatal("30 行输出必折叠")
	}
	if want := "… +20 lines"; !strings.Contains(marker, want) {
		t.Errorf("省略行应声明 %q（30−5−5），实际 %q", want, marker)
	}
	if len(hidden) != 20 {
		t.Errorf("中段应为 20 行（与 marker 声明的 +20 一致），实际 %d 行", len(hidden))
	}
}
