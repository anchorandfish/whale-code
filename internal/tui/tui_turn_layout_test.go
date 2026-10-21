package tui

// tui_turn_layout_test.go — TUI 排版收敛批次（2026-09-25 用户拍板解锁）红测。
//
// 三条判据全部走生产入口 handleStreamDone（真实回合收尾），断言用户可见转录：
// A. 窄视口下 assistant 尾部分隔横线自适应宽度，不被 wrapViewportContent 折成多行；
// B. 消息间 join 分隔线（render_extras.go 单 ─）不再紧跟 assistant 自带横线，消除连续双线；
// C. 轮末 UI-only 结束标志（单行 `━━ ✓ <YYYY-MM-DD HH:MM:SS> ━━`）由 m.turn.marks 投影进显示列表，
// 且**不得**泄漏进模型侧 session（I5：UI-only 不进模型上下文）。
//
// before 自检：三条在改动前都必须红（分别由 45-横线折行 / join 双线 / 无标记驱动）。

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/session"
)

// newTurnLayoutModel 走生产构造函数建立指针稳定的测试模型，并设好视口宽度。
func newTurnLayoutModel(t *testing.T, viewportWidth int) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.reassignServices()
	m.uiService.Viewport().Width = viewportWidth
	m.state = StateStreaming
	return m
}

// A：窄视口下分隔横线不得被折行（折行 = 横线字符分布在 ≥2 行）。
func TestTurnLayout_SeparatorAdaptsToWrapWidth(t *testing.T) {
	m := newTurnLayoutModel(t, 24) // contentWrapWidth = 24-frame ≈ 20~21 < 45
	_ = m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "hi"}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "sep-width-req",
		thinking:  "",
	})
	content := ansi.Strip(m.viewportContent)
	dashLines := 0
	for _, ln := range strings.Split(content, "\n") {
		if isHorizontalRuleLine(ln) {
			dashLines++
		}
	}
	if dashLines != 1 {
		t.Fatalf("分隔横线应恰好占 1 行（不被窄视口折行），实际 %d 行：\n%q", dashLines, content)
	}
}

// B：消息间 join 不得出现连续两条横线（assistant 尾部横线 + join 单 ─）。
func TestTurnLayout_NoConsecutiveRuleLines(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	_ = m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "第一条回答"}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "join-dedup-req",
		thinking:  "",
	})
	// 模拟工具结果消息（生产中经 addMessage 进入显示列表）。
	m.addMessage("📋 ToolSearch:\nno tools found matching \"memory\"")

	content := ansi.Strip(m.viewportContent)
	// 夹具健全性：两条消息都必须在转录里，否则断言对象为空、判据失效。
	if !strings.Contains(content, "第一条回答") || !strings.Contains(content, "ToolSearch") {
		t.Fatalf("夹具不健全：转录缺消息正文，无法判别：\n%q", content)
	}
	lines := strings.Split(content, "\n")
	for i := 1; i < len(lines); i++ {
		if isHorizontalRuleLine(lines[i-1]) && isHorizontalRuleLine(lines[i]) {
			t.Fatalf("第 %d/%d 行出现连续分隔线（assistant 尾线 + join 线叠加）：\n%q",
				i, i+1, content)
		}
	}
}

// C：轮末标记 —— A-3**契约已变**：标记不再是一条消息，而是由
// m.turn（结构状态）投影到渲染转录；显示列表与模型侧 session 都不得含它。
//
// 旧契约（"显示列表末尾追加一条 Turn complete 消息"）正是缺陷的载体：
// 它与 Monitor 输出走同一函数、同一条流 ⇒ 物理上管不住随后到达的写入。
// 因此本条判据随 A-3 一起改（R4-12 成因③：修复若作用在判据的观测面上，判据必须同改）。
func TestTurnLayout_TurnCompleteMarkerAppended(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	_ = m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "回答正文"}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "turn-complete-req",
		thinking:  "",
	})

	// 1) 结构承载体：回合已结束（删掉 closeTurn 的赋值 ⇒ 此断言转红）。
	if !m.turnHasEnded() {
		t.Fatal("A-3：回合收尾后 turnHasEnded() 为假 ⇒ 轮末边界没有结构承载体")
	}

	// 2) 渲染面：标记必须在转录里，且带**回合结束时刻**（不是渲染时刻）。
	rendered := ansi.Strip(m.viewportContent)
	// ★ 2026-10-07（用户拍板）再收紧：结束标志收敛为**单行**
	// `━━ ✓ <YYYY-MM-DD HH:MM:SS> ━━`（去轮次号、去中英文重复、时刻带完整年月日）。
	// 用完整形态正则断言 —— 只断言前缀 `━━ ✓ ` 对旧的两行啰嗦形态也成立，判别力不足。
	if !turnDoneMarkPattern.MatchString(rendered) {
		t.Fatalf("轮末结束标志应出现在渲染转录里（单行 `━━ ✓ <YYYY-MM-DD HH:MM:SS> ━━`），实际：\n%q", rendered)
	}
	if strings.Contains(rendered, "Turn #") || strings.Contains(rendered, "轮回答已结束") {
		t.Fatalf("结束标志应已去掉轮次号与中英文重复，旧形态仍在：\n%q", rendered)
	}
	stamp := m.turn.closedAt.Format("15:04:05")
	if !strings.Contains(rendered, stamp) {
		t.Fatalf("结束标志应带回合结束时刻 %s：\n%q", stamp, rendered)
	}

	// 3) 反向断言（A-3 的核心）：标记**不占消息身份**。
	for i, msg := range m.sessionService.Messages() {
		if strings.Contains(ansi.Strip(msg), "━━") {
			t.Fatalf("A-3 违例：轮末标记仍占消息身份（显示列表第 %d 条）：%q", i, msg)
		}
	}
	// I5 反向断言：模型侧 session（真正喂给模型/落盘的内容）不得出现标记。
	for i, smsg := range m.sessionService.CurrentSession().GetMessages() {
		for _, b := range smsg.Blocks {
			if strings.Contains(b.Text, "━━") {
				t.Fatalf("I5 违例：标记泄漏进模型侧 session（第 %d 条消息）", i)
			}
		}
	}
}

// D：定稿消息的思考默认折叠 —— 无边框、保留 🧠 Thinking 标识、长思考截断为预览。
// 流式期间的完整框由 renderStreamContent 负责，本判据只盯 finalize 态。
func TestTurnLayout_FinalizedThinkingCollapsed(t *testing.T) {
	t.Run("long-thinking-truncated-no-box", func(t *testing.T) {
		m := newTurnLayoutModel(t, 116)
		long := strings.Repeat("思考内容。", 100) // 500 字，远超预览预算
		_ = m.handleStreamDone(streamDoneMsg{
			blocks:    []session.ContentBlock{{Type: "text", Text: "答案"}},
			usage:     &session.TokenUsage{},
			model:     "test-model",
			requestID: "collapse-long-req",
			thinking:  long,
		})
		content := ansi.Strip(m.viewportContent)
		if strings.Contains(content, "╭") || strings.Contains(content, "╰") {
			t.Fatalf("定稿消息的思考应折叠为单行，不得绘制边框：\n%s", content)
		}
		if !strings.Contains(content, "🧠 Thinking") {
			t.Fatalf("折叠态应保留 🧠 Thinking 标识（TestFinalizeKeepsThinkingBlock 契约）：\n%s", content)
		}
		if strings.Contains(content, long) {
			t.Fatalf("长思考应截断为预览，不得整段进入转录：\n%s", content)
		}
		if !strings.Contains(content, "…") {
			t.Fatalf("截断应以省略号收尾：\n%s", content)
		}
	})

	t.Run("short-thinking-preserved", func(t *testing.T) {
		m := newTurnLayoutModel(t, 116)
		_ = m.handleStreamDone(streamDoneMsg{
			blocks:    []session.ContentBlock{{Type: "text", Text: "答案正文"}},
			usage:     &session.TokenUsage{},
			model:     "test-model",
			requestID: "collapse-short-req",
			thinking:  "先看 A，再看 B。",
		})
		content := ansi.Strip(m.viewportContent)
		if !strings.Contains(content, "先看 A，再看 B。") {
			t.Fatalf("短思考内容不得因折叠而丢失：\n%s", content)
		}
	})
}
