package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// clear_confirm_gate_test.go — C-4（发布1版分派清单块 C · 2026-10-10）
//
// 缺陷（源码实锤）：Usage 写 `/clear --confirm`（commands_core.go:35），
// 但 TUI 实现（app.go handleCommand case "clear"）不校验参数 —— 敲下立即清空上下文。
// 修复口径（拍板「补真闸」）：无参数 /clear 不再清空，进入二次确认态；
// 按 y 确认清空，任意其他键取消；带 --confirm 才立即清（原语义保留）。
//
// 判据全部走生产入口：命令分发 = handleCommand（stream.go sendInput 的唯一生产路径），
// 确认键 = Update（bubbletea 唯一事件入口）。不引用任何新字段，只断言可观测行为。

func newClearGateModel(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.addMessage("seed-message-keep-me")
	return m
}

func clearGateJoined(m *Model) string {
	return strings.Join(m.SessionModel.Messages(), "\n")
}

// TestClearWithoutConfirm_DoesNotClear 无参数 /clear 必须不清空 + 给出确认指引。
func TestClearWithoutConfirm_DoesNotClear(t *testing.T) {
	m := newClearGateModel(t)
	out := m.handleCommand("/clear")
	if !strings.Contains(out, "--confirm") {
		t.Fatalf("无参数 /clear 应提示需 --confirm 确认，got %q", out)
	}
	joined := clearGateJoined(m)
	if !strings.Contains(joined, "seed-message-keep-me") {
		t.Fatalf("无参数 /clear 不得清空上下文（种子消息丢了）:\n%.300s", joined)
	}
	if strings.Contains(joined, "Chat cleared") {
		t.Fatalf("无参数 /clear 不应显示已清空:\n%.300s", joined)
	}
}

// TestClearConfirmKey_ClearsOnY 二次确认态下按 y 才真正清空。
func TestClearConfirmKey_ClearsOnY(t *testing.T) {
	m := newClearGateModel(t)
	m.handleCommand("/clear")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	u := updated.(Model)
	joined := clearGateJoined(&u)
	if len(u.SessionModel.Messages()) != 1 || !strings.Contains(joined, "Chat cleared") {
		t.Fatalf("确认态下按 y 应清空上下文，got %d msgs:\n%.900s", len(u.SessionModel.Messages()), joined)
	}
	if strings.Contains(joined, "seed-message-keep-me") {
		t.Fatalf("确认清空后种子消息应消失:\n%.300s", joined)
	}
}

// TestClearConfirmKey_CancelOnAnyOtherKey 确认态下非 y 键（n/esc/普通字符）一律取消且不清空。
func TestClearConfirmKey_CancelOnAnyOtherKey(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"n", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"ordinary char", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newClearGateModel(t)
			m.handleCommand("/clear")
			updated, _ := m.Update(tc.key)
			u := updated.(Model)
			joined := clearGateJoined(&u)
			if !strings.Contains(joined, "seed-message-keep-me") {
				t.Fatalf("确认态下按 %q 只应取消、不得清空:\n%.300s", tc.name, joined)
			}
			if strings.Contains(joined, "Chat cleared") {
				t.Fatalf("取消后不应显示已清空:\n%.300s", joined)
			}
		})
	}
}

// TestClearWithConfirmFlag_ClearsImmediately 带 --confirm 立即清空（原语义回归守护）。
func TestClearWithConfirmFlag_ClearsImmediately(t *testing.T) {
	m := newClearGateModel(t)
	m.handleCommand("/clear --confirm")
	joined := clearGateJoined(m)
	if len(m.SessionModel.Messages()) != 1 || !strings.Contains(joined, "Chat cleared") {
		t.Fatalf("/clear --confirm 应立即清空，got %d msgs:\n%.300s", len(m.SessionModel.Messages()), joined)
	}
}
