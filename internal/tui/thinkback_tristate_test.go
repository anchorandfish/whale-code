package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
)

// TestThinkbackReportsThinkingTristate 验证 /thinkback 的回执与 /think 的**三态**同源。
//
// 生产入口：`Model.handleCommand("/thinkback")`
//   - 分发链：`handleCommand`（app.go:3101）→ `case "thinkback"`（app.go:3285）→ `handleThinkback`
//   - 该入口即真机唯一转发点（`stream.go:210` 的 `m.handleCommand(input)`），非手工构造。
//
// 判据落在**命令回执文本层**（用户直接看到的那段 facts），不在 viewport 渲染层。
// 留给人工：真 TUI 里宽/窄终端下的换行、高亮与滚动位置。
//
// 三态语义（与 /think 一致，文案源同为 `thinkingModeLabel`）：
//   - nil   → "default (server-decided)"，不下发 enable_thinking
//   - true  → "ON"，下发 enable_thinking=true
//   - false → "OFF"，下发 enable_thinking=false
//
// 判别性（before 必红）：改前 /thinkback 只按「有没有思考内容」分档，
// 三档回执**逐字相同**，且恒提示「使用 /think on 让 AI 输出思考过程」——
// 在 **已经 ON** 的那一档，这条提示指向一条用户已经走过的路（症状：以为没开）。
func TestThinkbackReportsThinkingTristate(t *testing.T) {
	// newThinkbackModel 复用仓库既有最小模型夹具，只把命令注册表换成
	// `command.NewRegistry()`（内建命令表）——否则零值 Registry 的 commands map 为 nil，
	// handleCommand 会在 `Get("thinkback")` 处返回 Unknown，根本走不到被测分支。
	// 这是补装配，不是改被测代码。
	newThinkbackModel := func(enabled *bool) *Model {
		m := createMinimalModel()
		m.cmdRegistry = command.NewRegistry()
		m.reassignServices()
		m.sessionService.SetThinkingEnabled(enabled)
		return m
	}

	t.Run("off：回显档位 OFF 并指向 /think on", func(t *testing.T) {
		out := newThinkbackModel(ai.ThinkingPtr(false)).handleCommand("/thinkback")
		if !strings.Contains(out, "Thinking: OFF") {
			t.Fatalf("应回显当前三态档位 OFF，actual:\n%s", out)
		}
		if !strings.Contains(out, "/think on") {
			t.Fatalf("OFF 档应提示 /think on，actual:\n%s", out)
		}
	})

	t.Run("default：回显 default (server-decided)（与 /think 同一文案源）", func(t *testing.T) {
		out := newThinkbackModel(nil).handleCommand("/thinkback")
		if !strings.Contains(out, "Thinking: default (server-decided)") {
			t.Fatalf("应回显 default (server-decided)，actual:\n%s", out)
		}
	})

	t.Run("on：不得再提示 /think on（它已经开着）", func(t *testing.T) {
		out := newThinkbackModel(ai.ThinkingPtr(true)).handleCommand("/thinkback")
		if !strings.Contains(out, "Thinking: ON") {
			t.Fatalf("应回显当前三态档位 ON，actual:\n%s", out)
		}
		if strings.Contains(out, "/think on") {
			t.Fatalf("ON 档已是开启态，回执不得再让用户「/think on」，actual:\n%s", out)
		}
	})

	t.Run("有当前思考内容：同样回显档位", func(t *testing.T) {
		m := newThinkbackModel(ai.ThinkingPtr(true))
		// 思考内容的真相源 = 会话存档（assistant 消息里的 thinking 块）。
		m.sessionService.SetCurrentSession(
			transcriptWithThinking("问题", "先拆约束，再验边界。", "答案"))
		out := m.handleCommand("/thinkback")
		if !strings.Contains(out, "Thinking: ON") {
			t.Fatalf("有记录分支也应回显档位，actual:\n%s", out)
		}
		if !strings.Contains(out, "最近一次思考过程") {
			t.Fatalf("有内容分支应保留原文回显，actual:\n%s", out)
		}
	})

	t.Run("all：列表回执同样回显档位", func(t *testing.T) {
		m := newThinkbackModel(nil)
		m.sessionService.SetCurrentSession(
			transcriptWithThinking("问题", "思考内容", "答案"))
		out := m.handleCommand("/thinkback all")
		if !strings.Contains(out, "Thinking: default (server-decided)") {
			t.Fatalf("/thinkback all 也应回显档位，actual:\n%s", out)
		}
		if !strings.Contains(out, "记录 #1") {
			t.Fatalf("/thinkback all 应保留列表，actual:\n%s", out)
		}
	})

	t.Run("clear：清空回执同样回显档位", func(t *testing.T) {
		m := newThinkbackModel(ai.ThinkingPtr(false))
		out := m.handleCommand("/thinkback clear")
		if !strings.Contains(out, "Thinking: OFF") {
			t.Fatalf("/thinkback clear 也应回显档位，actual:\n%s", out)
		}
		if !strings.Contains(out, "思考记录已清空") {
			t.Fatalf("clear 回执应保留确认文案，actual:\n%s", out)
		}
	})
}
