package tui

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// 判据族：输入框鼠标拖选的「空输入」分支。
//
// 背景（2026-09-23 实测）：真机 whale.log 中 `[tui] composer mouse selection:
// window offset match failed (canary)` 成簇出现（单次 15 条）。探针实测定位到
// 唯一触发条件 = 输入为空且 Placeholder 非空：
// bubbles textarea.View() 在该条件下走 placeholderView 分支（vendor/.../textarea.go:1092-1094），
// 返回的是 Placeholder 文本，而 composerPointFromScreen 的窗口匹配用的
// visualRows 由 `composerValueLines(ta)`（= value）复刻 wrap 得到 ⇒ 两者必然失配。
// 实测对照（width=10）：view[0]="Enter=send" vs visualRows[0]=" "。
//
// ★ 前置：本判据族只在 Placeholder 非空时具判别力（生产恒非空，见 lifecycle.go:125）。

// TestN135_EmptyInputMousePressResolvesWithoutCanary
// 空输入框上按下左键：必须成功建立拖选（= 坐标映射成功），且不得打 canary。
// 改前：ok=false ⇒ dragging 不置位 + 每次点击写一行 canary 到 whale.log。
func TestN135_EmptyInputMousePressResolvesWithoutCanary(t *testing.T) {
	m := newComposerMouseModel(t)
	ta := m.uiService.Textarea()
	if ta.Placeholder == "" {
		t.Fatal("前置不成立：Placeholder 为空 ⇒ View() 不走 placeholder 分支，本判据无判别力")
	}
	ta.SetValue("")

	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	m = composerUpdateMouse(t, m, composerMousePress(5, 11))

	if strings.Contains(buf.String(), "window offset match failed") {
		t.Fatalf("空输入框点击不应打 canary（真机日志噪声源）；实际写入 %q", buf.String())
	}
	if !m.composerMouseDrag.dragging {
		t.Fatal("空输入框点击应成功映射并进入拖选态；改前因坐标映射 fail-closed 而跳过（ok=false）")
	}
}

// TestN135_NonEmptyInputUnaffected 反向半边：短路不得吞掉正常路径。
// ★ 本条改前改后皆绿 ⇒ 不具判别性，只作"修复未越界"的守卫。
func TestN135_NonEmptyInputUnaffected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		width int
	}{
		{"single", "hello", 40},
		{"multiline", "hello\nworld", 40},
		{"wrap", "aaa bbb ccc ddd eee fff", 10},
		{"cjk", "你好世界你好世界你好世界", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newComposerMouseModel(t)
			ta := m.uiService.Textarea()
			ta.SetWidth(tc.width)
			ta.SetValue(tc.value)
			if _, _, ok := m.composerPointFromScreen(5, 11); !ok {
				t.Fatalf("有内容时坐标映射必须成功（value=%q width=%d）", tc.value, tc.width)
			}
		})
	}
}
