package tui

// panel_scroll_keys_test.go — 「面板期间的对话区滚动」两条不变量（2026-10-06，P1 + P2）。
//
// # 真机报障与根因
//
// 用户批准一条需要确认的命令后，TUI 历史**停在更早的一屏**（上一个任务的输出），
// 在 mac 上按 Cmd+↓ 也毫无反应。
//
// 根因不是历史丢失、也不是回退：
// - StateWaitingConfirm 分支以裸 `return m, nil`（app.go:1817）收尾 ⇒ 其后的
// `pgup/pgdown/ctrl+↑↓/home/end` 处理点（app.go:2180-2202）**永远走不到**，
// 这些键在面板期间被静默丢弃；
// - 而滚轮走 `tea.MouseMsg`（app.go:2935），**不经**该分支 ⇒ 面板期间照样能滚。
//
// ⇒ **「滚轮能滚离底部、键盘却回不来」的不对称**。用户只要（哪怕无意地）让视口离开
// 底部，面板期间就没有任何键盘出路；批准后的新内容又按 口径（非底部不回跳）
// 追加到屏幕外 ⇒ 界面看起来「回到了上一屏」。
//
// # 修法（两条同批，缺一条仍不对称）
//
//	P1 面板期间放行视口导航键 —— 用户永远能按 End / PgDn 回到最新；
//	P2 面板期间滚轮不改动对话区视口 —— 面板是模态，视口只接受显式导航（见
// handleViewportNavKey），消除「手一碰触控板就漂离底部」这条路径。
//
// # 判据形态
//
// 全部从**生产入口**进入：NewModel → WindowSizeMsg → 生产拦截点
// （SetPendingConfirm / checkAskUserChoice）→ `Update(真实 KeyMsg / MouseMsg)`，
// 断言落在 viewport 的 YOffset / AtBottom 上（用户可见的那个量）。
//
// 覆盖四种面板承载体（R7：新语义必须覆盖每一个承载体，不能只覆盖顺手写的那一个）：
// 主会话确认 / 子代理确认 / MCP elicitation / AskUser 选择。
//
// 修前：本文件前两组（导航键）在四种面板下**整组必红**；第三组（滚轮冻结）在
// 四种面板下必红，而它的对照组（无面板）修前修后都必须绿 —— 对照组不可省，
// 否则「不动」可能是坐标算错造成的恒真（假绿）。

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// panelScrollFixtureHeight 取一个「装不下 80 行」的终端高度，保证视口可滚动。
	panelScrollFixtureHeight = 30
	panelScrollFixtureLines  = 80
)

// panelScrollFixture 造「可滚动对话 + 指定面板态」的模型。
// apply 为 nil 时保持 Idle（用作滚轮对照组）。
func panelScrollFixture(t *testing.T, apply func(t *testing.T, m *Model)) *Model {
	t.Helper()
	m, _ := guModelIsolatedHome(t)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: panelScrollFixtureHeight})
	mm := res.(Model)
	mm.reassignServices()

	lines := make([]string, 0, panelScrollFixtureLines)
	for i := 0; i < panelScrollFixtureLines; i++ {
		lines = append(lines, "R"+strconv.Itoa(i)+" transcript line")
	}
	mm.setViewportContent(strings.Join(lines, "\n"))

	if apply != nil {
		apply(t, &mm)
	}
	mm.recalcViewportHeight()
	mm.uiService.Viewport().GotoBottom()

	vp := mm.uiService.Viewport()
	if vp.TotalLineCount() <= vp.Height {
		t.Fatalf("夹具不健全：内容 %d 行未超过视口 %d 行，滚动键不可判别", vp.TotalLineCount(), vp.Height)
	}
	if !vp.AtBottom() {
		t.Fatalf("夹具不健全：初始不在底部（offset=%d total=%d h=%d）", vp.YOffset, vp.TotalLineCount(), vp.Height)
	}
	return &mm
}

// ---------------------------------------------------------------------------
// 四种面板承载体的构造（均走生产拦截点 / 生产类型）
// ---------------------------------------------------------------------------

// applyMainConfirmPanel 主会话内联确认（Kind=Exec，无 AgentReplyCh）。
func applyMainConfirmPanel(t *testing.T, m *Model) {
	t.Helper()
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:  "bash",
		ToolInput: `{"command":"go test ./... -count=1"}`,
		RiskLevel: "moderate",
		Kind:      ApprovalKindExec,
		Options:   buildApprovalOptions(ApprovalKindExec),
	})
	m.sessionService.SetState(StateWaitingConfirm)
}

// applyAgentConfirmPanel 子代理确认（AgentReplyCh != nil ⇒ 走 handleAgentConfirmKeyMsg）。
func applyAgentConfirmPanel(t *testing.T, m *Model) {
	t.Helper()
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:     "write_file",
		ToolInput:    `{"path":"a.txt"}`,
		Kind:         ApprovalKindFileModify,
		Options:      buildApprovalOptions(ApprovalKindFileModify),
		AgentReplyCh: make(chan tools.ConfirmOutcome, 1),
	})
	m.sessionService.SetState(StateWaitingConfirm)
}

// applyElicitationPanel MCP elicitation（走 handleElicitationKeyMsg）。
func applyElicitationPanel(t *testing.T, m *Model) {
	t.Helper()
	m.toolService.SetPendingConfirm(&PendingConfirm{
		Kind:          ApprovalKindMcpElicitation,
		Options:       buildApprovalOptions(ApprovalKindMcpElicitation),
		McpServerName: "test-server",
		McpRequestID:  json.RawMessage(`"req-1"`),
		McpMessage:    "Please provide your API key",
	})
	m.sessionService.SetState(StateWaitingConfirm)
}

// applyChoicePanel AskUser 选择面板 —— 走生产拦截点 checkAskUserChoice（与
// newAskUserPendingModel 同源），不手工拼 PendingChoice。
func applyChoicePanel(t *testing.T, m *Model) {
	t.Helper()
	raw, _ := json.Marshal(tools.AskUserResult{
		Question:      "用 JWT 还是 Session？",
		Category:      "dialogue-clarify",
		DefaultIdx:    0,
		AllowFreeText: true,
		Options: []tools.AskUserOption{
			{Label: "JWT", Value: "jwt"},
			{Label: "Session", Value: "session"},
		},
	})
	results := []tools.Result{{ID: "ask-1", Name: "AskUser", Output: string(raw)}}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 1 {
		t.Fatalf("夹具不健全：checkAskUserChoice 返回 %d 个 choice (ok=%v)", len(choices), ok)
	}
	m.toolService.SetPendingChoice(&choices[0])
	m.toolService.SetChoiceIdx(0)
	m.sessionService.SetState(StateWaitingChoice)
}

// panelKinds 是被判据覆盖的四种面板承载体的清单。
func panelKinds() []struct {
	name  string
	apply func(t *testing.T, m *Model)
} {
	return []struct {
		name  string
		apply func(t *testing.T, m *Model)
	}{
		{"confirm-main", applyMainConfirmPanel},
		{"confirm-subagent", applyAgentConfirmPanel},
		{"confirm-elicitation", applyElicitationPanel},
		{"choice-askuser", applyChoicePanel},
	}
}

// modelFromUpdate 从 Update 的返回值取回 *Model。
//
// 为什么不能直接 `res.(Model)`：既有实现里 `Update` 的返回类型在 **Model 与 *Model 之间摆荡**
// —— `handleAgentConfirmKeyMsg`（agent_confirm.go:71）与 `handleElicitationKeyMsg`
// （app.go:3586）返回的是 `*Model`。这是既有形态，与本轮改动无关；本夹具只做形态归一，不去改它。
func modelFromUpdate(t *testing.T, res tea.Model) *Model {
	t.Helper()
	switch v := res.(type) {
	case Model:
		v.reassignServices()
		return &v
	case *Model:
		v.reassignServices()
		return v
	default:
		t.Fatalf("Update 返回了非 Model 类型：%T", res)
		return nil
	}
}

func maxYOffset(m *Model) int {
	vp := m.uiService.Viewport()
	max := vp.TotalLineCount() - vp.Height
	if max < 0 {
		return 0
	}
	return max
}

// ---------------------------------------------------------------------------
// P1：面板期间视口导航键必须可用
// ---------------------------------------------------------------------------

// TestPanelScrollKeys_ViewportNavKeysWorkDuringPanels 是 P1 的核心判别器。
//
// 六个导航键在四种面板下逐一验证（每个键都要有可观察的位移方向，
// 不能只断言「没报错」）。修前：这些键被面板分支末尾的裸 return 吞掉 ⇒ 整组红。
func TestPanelScrollKeys_ViewportNavKeysWorkDuringPanels(t *testing.T) {
	keys := []struct {
		name string
		key  tea.KeyMsg
		// prep 把视口摆到「能观察到该键位移」的位置（夹具准备，不是替被测代码造前提）。
		prep func(m *Model)
		desc string
		ok   func(m *Model) bool
	}{
		{
			name: "pgup", key: tea.KeyMsg{Type: tea.KeyPgUp},
			desc: "上翻半屏 ⇒ 离开底部",
			ok:   func(m *Model) bool { return !m.uiService.Viewport().AtBottom() },
		},
		{
			name: "ctrl+up", key: tea.KeyMsg{Type: tea.KeyCtrlUp},
			desc: "上移 3 行 ⇒ 离开底部",
			ok:   func(m *Model) bool { return m.uiService.Viewport().YOffset < maxYOffset(m) },
		},
		{
			name: "home", key: tea.KeyMsg{Type: tea.KeyHome},
			desc: "跳到最旧 ⇒ YOffset==0",
			ok:   func(m *Model) bool { return m.uiService.Viewport().YOffset == 0 },
		},
		{
			name: "pgdown", key: tea.KeyMsg{Type: tea.KeyPgDown},
			prep: func(m *Model) { m.uiService.Viewport().GotoTop() },
			desc: "下翻半屏 ⇒ 离开顶部",
			ok:   func(m *Model) bool { return m.uiService.Viewport().YOffset > 0 },
		},
		{
			name: "ctrl+down", key: tea.KeyMsg{Type: tea.KeyCtrlDown},
			prep: func(m *Model) { m.uiService.Viewport().GotoTop() },
			desc: "下移 3 行 ⇒ 离开顶部",
			ok:   func(m *Model) bool { return m.uiService.Viewport().YOffset > 0 },
		},
		{
			name: "end", key: tea.KeyMsg{Type: tea.KeyEnd},
			prep: func(m *Model) { m.uiService.Viewport().GotoTop() },
			desc: "跳回最新 ⇒ AtBottom",
			ok:   func(m *Model) bool { return m.uiService.Viewport().AtBottom() },
		},
	}

	for _, p := range panelKinds() {
		for _, k := range keys {
			t.Run(p.name+"/"+k.name, func(t *testing.T) {
				m := panelScrollFixture(t, p.apply)
				if k.prep != nil {
					k.prep(m)
				}
				before := m.uiService.Viewport().YOffset

				res, _ := m.Update(k.key)
				got := modelFromUpdate(t, res)

				if !k.ok(got) {
					t.Fatalf("%s 面板期间按 %s 必须能滚动对话区（%s）：offset %d → %d，AtBottom=%v\n"+
						"（修前症状：面板分支末尾的裸 `return m, nil` 吞掉该键，而滚轮走 MouseMsg 不受影响\n"+
						" ⇒ 只能滚离底部、键盘回不来；用户按 Cmd+↓ 无反应后无从回到最新）",
						p.name, k.name, k.desc,
						before, got.uiService.Viewport().YOffset, got.uiService.Viewport().AtBottom())
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// P2：面板期间滚轮不得移动对话区 + 对照组
// ---------------------------------------------------------------------------

// wheelUpOnTranscript 造一条「落在对话区上」的滚轮消息。
func wheelUpOnTranscript(m *Model) tea.MouseMsg {
	return tea.MouseMsg{
		X:      5,
		Y:      m.lastInputAreaTop - 1,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
		Type:   tea.MouseWheelUp,
	}
}

// TestPanelScrollKeys_WheelDoesNotMoveTranscriptDuringPanels 是 P2 的判别器。
//
// 面板期间对话区视口只接受显式导航（键盘），滚轮不得改动它 ——
// 这条正是消除「手一碰触控板就漂离底部」的那条路径。修前必红。
func TestPanelScrollKeys_WheelDoesNotMoveTranscriptDuringPanels(t *testing.T) {
	for _, p := range panelKinds() {
		t.Run(p.name, func(t *testing.T) {
			m := panelScrollFixture(t, p.apply)
			if m.lastInputAreaTop <= 0 {
				t.Fatalf("夹具不健全：inputAreaTop=%d，坐标不可用", m.lastInputAreaTop)
			}
			before := m.uiService.Viewport().YOffset

			res, _ := m.Update(wheelUpOnTranscript(m))
			got := modelFromUpdate(t, res)

			if after := got.uiService.Viewport().YOffset; after != before {
				t.Fatalf("面板期间滚轮不得移动对话区视口：offset %d → %d\n"+
					"（面板是模态；修前滚轮不经 state 分流 ⇒ 用户可能无意间把视口滚离底部，\n"+
					" 之后批准的新内容落在屏幕外 = 报障现象「历史停在更早一屏」）",
					before, after)
			}
		})
	}
}

// TestPanelScrollKeys_WheelStillWorksWithoutPanel 是不可省的对照组：
// 同一条滚轮消息在**无面板**时必须真的滚动对话区。
//
// 没有这条，「面板期间不动」可能是坐标算错 / 消息形态不被识别造成的**恒真假绿**。
func TestPanelScrollKeys_WheelStillWorksWithoutPanel(t *testing.T) {
	m := panelScrollFixture(t, nil) // Idle：无面板
	before := m.uiService.Viewport().YOffset

	res, _ := m.Update(wheelUpOnTranscript(m))
	got := modelFromUpdate(t, res)

	if after := got.uiService.Viewport().YOffset; after >= before {
		t.Fatalf("对照组失败：无面板时滚轮应当滚动对话区视口（%d → %d）—— "+
			"否则「面板期间不动」这条断言不可判别", before, after)
	}
}
