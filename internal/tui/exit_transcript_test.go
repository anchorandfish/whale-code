package tui

import (
	"fmt"
	"strings"
	"testing"
)

// 守卫：退出回印的**取尾语义**。
//
// 判别性自检（before 必须红）：把 `ExitTranscriptTail` 里的
// `lines[len(lines)-maxLines:]` 改成 `lines[:maxLines]`（取头部而非尾部），
// 本用例的"应取最后 5 行"必须转红 —— 这正是最容易写反、且肉眼审不出来的地方。
func TestN138_ExitTranscriptTail_TakesTheTail(t *testing.T) {
	m := newTUIRegressionModel(t)

	if got := m.ExitTranscriptTail(10); got != "" {
		t.Errorf("N-138④: 无内容时应返回空串（调用方据此不打印空壳），got %q", got)
	}

	var sb strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&sb, "line-%02d\n", i)
	}
	m.viewportContent = sb.String()

	got := m.ExitTranscriptTail(5)
	want := "line-26\nline-27\nline-28\nline-29\nline-30"
	if got != want {
		t.Errorf("N-138④: 应取**最后** 5 行并保持原顺序\n got  %q\n want %q", got, want)
	}
	if strings.Contains(got, "line-01") {
		t.Error("N-138④: 回印头部说明取尾写反了（用户要的是退出前那屏，不是最早那屏）")
	}
}

func TestN138_ExitTranscriptTail_Boundaries(t *testing.T) {
	m := newTUIRegressionModel(t)

	// 行数不足 ⇒ 全部返回
	m.viewportContent = "a\nb\nc\n"
	if got := m.ExitTranscriptTail(10); got != "a\nb\nc" {
		t.Errorf("N-138④: 行数不足时应全部返回且剔除尾空行，got %q", got)
	}
	// 仅空白 ⇒ 空串
	m.viewportContent = "\n\n  \n"
	if got := m.ExitTranscriptTail(5); got != "" {
		t.Errorf("N-138④: 仅空白内容应返回空串，got %q", got)
	}
	// maxLines<=0 ⇒ 空串（可整体关闭回印）
	m.viewportContent = "x\ny"
	if got := m.ExitTranscriptTail(0); got != "" {
		t.Errorf("N-138④: maxLines<=0 应返回空串，got %q", got)
	}
}
