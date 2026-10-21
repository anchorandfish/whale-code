package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/ai"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/typeahead"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

var tuiBenchmarkSink string

// newTUIRegressionModel 创建指针稳定的完整 TUI 回归测试模型，避免值返回后服务指针指向旧副本。
//
// ★ 收尾纪律（在夹具层统一挂，不依赖各用例自觉）：
// - NewModel 的生产装配会把 H11N27 的 globalMergeLayer / defaultMemoryStore / aiCaller
// 写成 internal/memory 的**包级单例**，其路径由当前 $HOME 推导；还起 ForgettingScanner 协程。
// - 用例若只 `t.Setenv("HOME", ...)` + 删临时目录而不复位单例，后续任何走
// memory.ReflectMemories → ListMerged 的用例都会读到「已删除的别人家目录」，
// 表现为与自己无关的断言失败（错误文本里出现 <tempdir>/w072home.../.whale/memory/_user）。
// - 判别性（实测）：`-run 'TestW072_TUIProductionPath_BlindOverwriteRejected|TestWireExecutorMemoryCallbacks_ReflectTrigger_TriggersMemoryReflectMemories'`
// 在未挂本收尾时**红**（x3_wiring_test.go:117 期望 aiCaller/globalMergeLayer），挂上后绿。
// - 顺序：本 t.Cleanup 晚于用例内 t.TempDir()/MkdirTemp 注册 ⇒ LIFO 先停协程复位单例、后删目录。
func newTUIRegressionModel(t *testing.T) *Model {
	t.Helper()
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.reassignServices()
	t.Cleanup(func() {
		m.CancelWiring()
		resetMemoryWiringGlobalsForTest()
	})
	return &m
}

// uniqueTestSessionID 给「会走 applyResumedSession（会话恢复唯一装载点）」的用例一个
// **进程内唯一**的会话 id。凡是要装载会话的用例都必须用它，不要写死 "test-session"。
//
// 为什么写死会红（/ 复发点，2026-10-22 CI 实测）：
// - TestMain 把整个测试进程的 HOME 指到同一个 whale-tui-test-home-*，于是**所有用例共享
// 同一个会话锁目录**； 占用闸门的 flock 是**文件级**的，锁随进程存活、只在切会话时
// 由上一把锁 release。
// - 同一进程里新建一个 Store 再去 AcquireLock 同一个文件时，`isLockHeldByActiveProcess`
// 会把「本进程上一次的持有」判成「别的活进程」⇒ 阻塞 2s 超时 → SessionInUseError →
// applyResumedSession 整体被拒，界面只剩欢迎屏（用例若不断言 error，就表现为
// 「渲染里没有压缩分界」这种与原因无关的断言失败）。
// - 判别性（实测）：`-race -run TestResumeDerivesBoundaryMarker -count=3` 用固定 id 时
// 第 2、3 轮红（2.01s），换成本函数后 `-count=5` 连绿。
//
// 命名带用例名 + PID + 纳秒：日志里能直接认领锁是谁的，且同一用例重复跑也不会撞。
func uniqueTestSessionID(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(t.Name())
	return fmt.Sprintf("%s-%d-%d", name, os.Getpid(), time.Now().UnixNano())
}

// TestTextareaLimitsMovedOutOfHotPath 验证 textarea 不再逐键扫描长度或截断逻辑行。
func TestTextareaLimitsMovedOutOfHotPath(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.textarea.CharLimit != 0 {
		t.Fatalf("CharLimit 应为 0，actual %d", m.textarea.CharLimit)
	}
	if m.textarea.MaxHeight != 0 {
		t.Fatalf("MaxHeight 应为 0，actual %d", m.textarea.MaxHeight)
	}
}

func TestTextareaDoesNotRenderDefaultLeftPrompt(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.textarea.Prompt != "" {
		t.Fatalf("输入框不应显示 bubbles 默认左侧竖线 prompt，got %q", m.textarea.Prompt)
	}
	if strings.Contains(m.textarea.View(), "┃") {
		t.Fatal("输入框内容仍包含默认的断开竖线")
	}
}

func TestConversationSpeakerLabelsUseOwnLine(t *testing.T) {
	user := ansi.Strip(formatUserMessage("ok", 0))
	if user != "👤 You:\nok" {
		t.Fatalf("用户标签和正文应分行，got %q", user)
	}

	assistant := ansi.Strip(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "text", Text: "ok"}}, "", 0, 76,
	))
	labelAt := strings.Index(assistant, "🐋 Whale:")
	bodyAt := strings.Index(assistant, "ok")
	if labelAt < 0 || bodyAt < 0 || !strings.Contains(assistant[labelAt:bodyAt], "\n") {
		t.Fatalf("助手标签和正文应分行，got %q", assistant)
	}
}

func TestAssistantFormatterSkipsInvisibleAskUserCall(t *testing.T) {
	got := formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "tool_use", Name: "AskUser", Input: `{"question":"continue?"}`}},
		"", time.Now().UnixMilli(), 76,
	)
	if got != "" {
		t.Fatalf("AskUser 的空白助手回合不应渲染 Whale 标签或时间戳，got %q", ansi.Strip(got))
	}
}

func TestAssistantFormatterGroupsToolOnlyEventWithoutWhaleHeader(t *testing.T) {
	got := ansi.Strip(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "tool_use", Name: "bash", Input: `{"command":"curl https://example.com"}`}},
		"", time.Now().UnixMilli(), 76,
	))
	if strings.Contains(got, "🐋 Whale:") {
		t.Fatalf("tool-only assistant event should not create a separate Whale reply header: %q", got)
	}
	if !strings.Contains(got, "bash") || !strings.Contains(got, "curl https://example.com") {
		t.Fatalf("tool-only event should still show the concrete call: %q", got)
	}
}

// TestThinkingBoxSurvivesViewportReflow 验证思考边框宽度与视口折行宽度一致。
// 根因 1：边框按 Viewport().Width（终端-4）绘制，而 wrapViewportContent 按 composer
// 宽度（再减 inputBoxStyle 横向 frame=3）硬折行；边框行超宽 3 列，右侧角标被
// ansi.Hardwrap 折到下一行，呈现 ──╮ / " │" / ──╯ 错位残片。
// 根因 2（对抗式审查 F1）：边框内长内容行未预折到框内宽度，真实模型思考文本
// （长段落）整行超宽，同样被 Hardwrap 折断右侧 │。
// 根因 3（对抗式审查 F3）：窄宽度阈值错误——顶行最小宽 = 5+label(11) = 16，
// 宽度不足 16 时画框必然残缺，应整体退化为纯文本。
// 从生产调度入口断言（handleStreamDone / renderStreamContent），禁止直调渲染函数造成假验证。
func TestThinkingBoxSurvivesViewportReflow(t *testing.T) {
	// 通用结果断言：以 │ 开头的每一行边框内容行都必须以 │ 闭合（未被 Hardwrap 拆行）；
	// 顶行以 ╮ 闭合；expectBottom 时底行以 ╯ 闭合（streaming 设计上为开放边框）。
	assertBoxIntact := func(t *testing.T, content string, expectBottom bool) {
		t.Helper()
		var topOK, bottomOK bool
		contentLines := 0
		for _, ln := range strings.Split(content, "\n") {
			trimmed := strings.TrimRight(ln, " ")
			switch {
			case strings.HasPrefix(trimmed, "╭─ 🧠"):
				if !strings.HasSuffix(trimmed, "╮") {
					t.Fatalf("顶边框被折行破坏（不以 ╮ 结尾）：%q", trimmed)
				}
				topOK = true
			case strings.HasPrefix(trimmed, "│"):
				contentLines++
				if !strings.HasSuffix(trimmed, "│") {
					t.Fatalf("内容行被折行破坏（不以 │ 结尾）：%q", trimmed)
				}
			case strings.HasPrefix(trimmed, "╰"):
				if !strings.HasSuffix(trimmed, "╯") {
					t.Fatalf("底边框被折行破坏（不以 ╯ 结尾）：%q", trimmed)
				}
				bottomOK = true
			}
		}
		if !topOK || contentLines == 0 || (expectBottom && !bottomOK) {
			t.Fatalf("思考边框缺失（top=%v contentLines=%d bottom=%v）：\n%s",
				topOK, contentLines, bottomOK, content)
		}
	}

	longThinking := strings.Repeat("用户只是在打招呼。", 30) // 单行 540 列，超出任何合理框宽

	t.Run("finalize", func(t *testing.T) {
		// 排版收敛（2026-09-25 用户拍板）：定稿思考**折叠**为 dim 单行（无边框），
		// 完整框只存在于流式态（streaming 子用例）。本子用例从「框完整」改锁
		// 「折叠行完整」：无框残片 + 🧠 Thinking 标识在 + 短思考文本在。
		m := newTUIRegressionModel(t)
		m.reassignServices()
		// 生产契约 app.go:1156：viewport.Width = 终端宽 - 4（此处模拟终端 120）
		m.uiService.Viewport().Width = 116
		m.state = StateStreaming
		m.sessionService.SetThinkingEnabled(wireTriPtr(true))
		_ = m.handleStreamDone(streamDoneMsg{
			blocks:    []session.ContentBlock{{Type: "text", Text: "你好！有什么可以帮你的吗？"}},
			usage:     &session.TokenUsage{},
			model:     "test-model",
			requestID: "thinking-box-req",
			thinking:  "用户只是在打招呼。",
		})
		content := ansi.Strip(m.viewportContent)
		if strings.Contains(content, "╭") || strings.Contains(content, "╮") ||
			strings.Contains(content, "╰") || strings.Contains(content, "╯") {
			t.Fatalf("定稿思考应折叠为单行，不得出现边框残片：\n%s", content)
		}
		if !strings.Contains(content, "🧠 Thinking") || !strings.Contains(content, "用户只是在打招呼。") {
			t.Fatalf("折叠行应保留思考标识与短思考文本：\n%s", content)
		}
	})

	t.Run("finalize-long-content", func(t *testing.T) {
		// 折叠后长思考截断为预览（… 收尾），不再整段呈现 —— 原「长内容框完整」
		// 判据随折叠失效，改锁「无框 + 标识在 + 截断留痕」。
		m := newTUIRegressionModel(t)
		m.reassignServices()
		m.uiService.Viewport().Width = 116
		m.state = StateStreaming
		m.sessionService.SetThinkingEnabled(wireTriPtr(true))
		_ = m.handleStreamDone(streamDoneMsg{
			blocks:    []session.ContentBlock{{Type: "text", Text: "你好！有什么可以帮你的吗？"}},
			usage:     &session.TokenUsage{},
			model:     "test-model",
			requestID: "thinking-box-req",
			thinking:  longThinking,
		})
		content := ansi.Strip(m.viewportContent)
		if strings.Contains(content, "╭") || strings.Contains(content, "╮") ||
			strings.Contains(content, "╰") || strings.Contains(content, "╯") {
			t.Fatalf("定稿长思考应折叠，不得出现边框：\n%s", content)
		}
		if !strings.Contains(content, "🧠 Thinking") || !strings.Contains(content, "…") {
			t.Fatalf("长思考折叠行应保留标识且以省略号截断：\n%s", content)
		}
		if strings.Contains(content, longThinking) {
			t.Fatalf("长思考不得整段进入转录：\n%s", content)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.reassignServices()
		m.uiService.Viewport().Width = 116
		m.state = StateStreaming
		m.sessionService.SetThinkingEnabled(wireTriPtr(true))
		m.activeCell = NewActiveCell()
		m.activeCell.AppendThinking("用户只是在打招呼。")
		m.activeCell.AppendText("你好！有什么可以帮你的吗？")
		m.renderStreamContent()
		assertBoxIntact(t, ansi.Strip(m.viewportContent), false)
	})

	t.Run("streaming-long-content", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.reassignServices()
		m.uiService.Viewport().Width = 116
		m.state = StateStreaming
		m.sessionService.SetThinkingEnabled(wireTriPtr(true))
		m.activeCell = NewActiveCell()
		m.activeCell.AppendThinking(longThinking)
		m.activeCell.AppendText("你好！有什么可以帮你的吗？")
		m.renderStreamContent()
		assertBoxIntact(t, ansi.Strip(m.viewportContent), false)
	})

	// 窄宽度（contentWrapWidth < 16）：不足以容纳最小顶行（5+label 11=16），
	// 必须整体退化为纯文本，不得输出残缺边框。
	t.Run("narrow-viewport-no-partial-box", func(t *testing.T) {
		t.Run("finalize", func(t *testing.T) {
			m := newTUIRegressionModel(t)
			m.reassignServices()
			// 终端 21 → viewport 17 → contentWrapWidth 14 < 16
			m.uiService.Viewport().Width = 17
			m.state = StateStreaming
			m.sessionService.SetThinkingEnabled(wireTriPtr(true))
			_ = m.handleStreamDone(streamDoneMsg{
				blocks:    []session.ContentBlock{{Type: "text", Text: "你好！有什么可以帮你的吗？"}},
				usage:     &session.TokenUsage{},
				model:     "test-model",
				requestID: "thinking-box-req",
				thinking:  "用户只是在打招呼。",
			})
			content := ansi.Strip(m.viewportContent)
			if strings.Contains(content, "╭") || strings.Contains(content, "╮") ||
				strings.Contains(content, "╰") || strings.Contains(content, "╯") {
				t.Fatalf("窄宽度下不应绘制残缺边框：\n%s", content)
			}
			// Hardwrap 只在折点插入换行，去换行符后应能还原连续文本
			if !strings.Contains(strings.ReplaceAll(content, "\n", ""), "用户只是在打招呼") {
				t.Fatalf("窄宽度退化时思考文本不应丢失：\n%s", content)
			}
		})

		t.Run("streaming", func(t *testing.T) {
			m := newTUIRegressionModel(t)
			m.reassignServices()
			m.uiService.Viewport().Width = 17
			m.state = StateStreaming
			m.sessionService.SetThinkingEnabled(wireTriPtr(true))
			m.activeCell = NewActiveCell()
			m.activeCell.AppendThinking("用户只是在打招呼。")
			m.activeCell.AppendText("你好！有什么可以帮你的吗？")
			m.renderStreamContent()
			content := ansi.Strip(m.viewportContent)
			if strings.Contains(content, "╭") || strings.Contains(content, "╮") ||
				strings.Contains(content, "╰") || strings.Contains(content, "╯") {
				t.Fatalf("窄宽度下不应绘制残缺边框：\n%s", content)
			}
			// Hardwrap 只在折点插入换行，去换行符后应能还原连续文本
			if !strings.Contains(strings.ReplaceAll(content, "\n", ""), "用户只是在打招呼") {
				t.Fatalf("窄宽度退化时思考文本不应丢失：\n%s", content)
			}
		})
	})
}

func TestForceExecuteConfirmationIsVisibleInTranscript(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("我确认强制执行")
	updatedModel, _ := m.sendInput()
	updated := updatedModel.(Model)
	transcript := ansi.Strip(strings.Join(updated.Messages(), "\n"))
	if !strings.Contains(transcript, "👤 You:\n我确认强制执行") {
		t.Fatalf("force confirmation should remain visible as user input: %q", transcript)
	}
	if !strings.Contains(transcript, "Force confirmation recorded") || !strings.Contains(transcript, "permissions and sandbox still apply") {
		t.Fatalf("force confirmation status must explain its limited scope: %q", transcript)
	}
	if updated.refusalHandler == nil || !updated.refusalHandler.IsForceExecuteMode() {
		t.Fatal("force confirmation should activate the one-shot AI refusal override")
	}
}

// TestSecurityProseAloneDoesNotClaimPolicyBlock 是 R4（S17 P3 / V3-6-3）的**负例，也是本 bug 本体**。
//
// 模型只是在**散文里提到**沙箱/网络隔离，而**本轮没有任何结构化的沙箱/权限拒绝事件**
// （`tools.Result.FailureKind` 全空）时，不得断言「执行被安全策略拦截」。
//
// R4 判据（有意翻转本用例，原断言见 R3_R4_交接_2026-09-14.md §3.3 第 3 行）：
//
//	⛔ 分支 = 本轮有**结构化**拒绝事件（Model.roundDeniedKind 非空，由 toolResultMsg
//	漏斗从真实 tools.Result.FailureKind 写入） ∧ 散文在谈安全约束（辅助判据）
//
// 正例见 r4_refusal_ledger_test.go（真实 FailureKind 驱动）。
//
// ★ （2026-09-19 · 方案 A）**再次有意翻转**本用例的后两条断言：
// 原先还要求"走普通拒绝分支 + 给出 force 路径"。那条分支已整体删除 ⇒ 现在的要求是
// **纯散文不得产生任何断言（静默）** —— 因为"模型主观拒绝"在本协议下没有结构化事实源，
// 而关键词判据会把 T-1 的期望输出（「我没有这个工具…所以我无法调用」）误判成拒绝。
// 详见 `internal/ai/refusal_handler.go` 包注释与 `.go`。
func TestSecurityProseAloneDoesNotClaimPolicyBlock(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.roundDeniedKind != "" {
		t.Fatalf("前置失败：新会话不应带上一轮的结构化拒绝账本，实际 %q", m.roundDeniedKind)
	}

	m.checkAISafety("抱歉，当前沙箱启用了网络隔离，我无法执行 curl。", false)

	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))
	if strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("纯散文（本轮无结构化拒绝事件）不得断言执行被安全策略拦截: %q", transcript)
	}
	if strings.Contains(transcript, "AI refusal") {
		t.Fatalf("散文关键词不得单独断言 AI 拒绝（：该判据会把如实报告工具不存在误判成拒绝）: %q", transcript)
	}
	if strings.Contains(transcript, "To override an AI-only refusal") {
		t.Fatalf("不得自动弹强制覆盖提示；该机制只由用户主动输入触发（可发现性见 /help）: %q", transcript)
	}
}

// ★ **有意删除**了原 `TestOrdinaryRefusalStatusIsBilingual`：
// 它唯一的作用是钉住「⚠️ AI refusal / AI 拒绝执行」这条**双语状态行**，
// 而那条状态行所属的分支已整体删除（散文关键词不得再作拒绝判据）。
// 用例不可能"翻转"成断言"不出现"——那与上一条用例重复，故直接删除。
// 承重判据收口在 `.go`（T1/T2/T3）。

// flattenWS 把任意空白（含换行）折叠成单个空格。
//
// 为什么断言前要先折叠：C-3/ 起，glamour 拿到的渲染宽度由 0 改为真实
// 内容宽度（format.go `renderTextContentWidth(b.Text, contentWidth)`），
// 于是它会对**普通段落**按词折行。同一条终局说明在不同宽度下会被折成
// 不同的行，若直接 `strings.Contains(含换行的整段文案)`，会被"原句被折成两行"
// 误判成"文案缺失"。本文件断言的只是**文案内容**（是否禁止改工具重试），
// 与折行位置无关 ⇒ 断言前统一折叠空白。施工单 §4 C-3 判据 2 亦要求
// "逐行比对，别只看 Contains"。
func flattenWS(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestSandboxDenialEndsTurnWithoutAlternateToolRetry(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateToolExec
	m.refusalHandler.SetForceExecuteMode(true)
	m.addMessage(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "tool_use", Name: "bash", Input: `{"command":"curl https://example.com > network-marker.txt"}`}},
		"", time.Now().UnixMilli(), 76,
	))

	m.handleToolResults([]tools.Result{{
		ID:          "bash-1",
		Name:        "bash",
		Output:      "sandbox denied / 沙箱拒绝\nCommand / 命令: curl https://example.com > network-marker.txt\nNo command was run / 命令未执行",
		IsError:     true,
		FailureKind: tools.FailureKindSandboxDenied,
	}})

	if m.state != StateIdle {
		t.Fatalf("sandbox denial should end the turn instead of starting another AI stream, state=%v", m.state)
	}
	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))
	flat := flattenWS(transcript)
	if got := strings.Count(flat, "🐋 Whale:"); got != 1 {
		t.Fatalf("blocked tool turn should contain one final Whale reply, got %d: %q", got, transcript)
	}
	if !strings.Contains(flat, "no alternate command or interpreter will be attempted") {
		t.Fatalf("terminal explanation should prohibit implicit Python/wget retries: %q", transcript)
	}
	if strings.Contains(strings.ToLower(flat), "python3") || strings.Contains(flat, "To override an AI-only refusal") {
		t.Fatalf("sandbox denial must not retry or offer force execution: %q", transcript)
	}
	if m.refusalHandler.IsForceExecuteMode() {
		t.Fatal("force override must be cleared at the completed turn boundary")
	}
}

func TestPolicyDenialEndsTurnWithoutAlternateToolRetry(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateToolExec
	m.addMessage(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "tool_use", Name: "write_file", Input: `{"path":"approval-marker.txt","content":"CHANGED"}`}},
		"", time.Now().UnixMilli(), 76,
	))

	m.handleToolResults([]tools.Result{{
		ID:          "write-1",
		Name:        "write_file",
		Output:      "Permission denied / 权限拒绝\nTool / 工具: write_file\nReason / 原因: insufficient permissions: tool \"write_file\" not allowed under current permission mode\nNo operation was run / 操作未执行",
		IsError:     true,
		FailureKind: tools.FailureKindPolicyDenied,
	}})

	if m.state != StateIdle {
		t.Fatalf("hard permission denial should end the turn, state=%v", m.state)
	}
	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))
	flat := flattenWS(transcript)
	if got := strings.Count(flat, "🐋 Whale:"); got != 1 {
		t.Fatalf("permission-denied turn should contain one final Whale reply, got %d: %q", got, transcript)
	}
	for _, unwanted := range []string{"bash echo", "edit_file approval-marker.txt", "AI refusal / AI 拒绝执行", "force confirmation cannot override it"} {
		if strings.Contains(flat, unwanted) {
			t.Fatalf("permission denial should not produce %q: %q", unwanted, transcript)
		}
	}
	if !strings.Contains(flat, "no alternate tool will be attempted") {
		t.Fatalf("terminal explanation should prohibit equivalent-tool retries: %q", transcript)
	}
}

func TestStreamDoneSkipsInvisibleAssistantDisplay(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.handleStreamDone(streamDoneMsg{
		blocks: []session.ContentBlock{{Type: "tool_use", ID: "ask-1", Name: "AskUser", Input: `{"question":"continue?"}`}},
		model:  "test-model",
	})
	for _, message := range m.Messages() {
		if strings.Contains(ansi.Strip(message), "🐋 Whale:") {
			t.Fatalf("不可见助手回合不应写入空白 Whale 条目，messages=%q", m.Messages())
		}
	}
	if got := len(m.CurrentSession().GetMessages()); got != 1 {
		t.Fatalf("不可见回合仍需保留在会话上下文，messages=%d", got)
	}
}

func TestStreamDoneSchedulesAutoCompactCommand(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.Config().AutoCompact = true
	m.SessionModel.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 10, Model: m.sessionService.ModelName(), Valid: true,
	}
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText(strings.Repeat("large context ", 100)),
	})
	// 按实际估算值落位：主力线（window-buffer）< estimated < 97% 线。
	// window = estimated+200、buffer=400 → 主力线 = estimated-200；
	// 0.97*(estimated+200) > estimated（对任意 estimated>0 成立）→ 不触发 Blocked 硬停。
	estimated := m.estimateCurrentInputContextTokens()
	if estimated <= 0 {
		t.Fatalf("estimated tokens = %d, want > 0", estimated)
	}
	m.aiService.SetBudget(ctxpkg.Budget{
		ContextWindow: estimated + 200, WarnPercent: 0.75, BlockPercent: 0.97,
		RecoveryBudget: 25, AutoCompactBufferTokens: 400,
	})
	m.sessionService.SetState(StateStreaming)

	cmd := m.handleStreamDone(streamDoneMsg{
		blocks: []session.ContentBlock{{Type: "text", Text: "done"}},
		model:  m.sessionService.ModelName(),
	})
	if cmd == nil {
		t.Fatal("stream completion discarded the auto-compact command")
	}
	scheduled := cmd()
	switch msg := scheduled.(type) {
	case autoCompactResultMsg:
		// tea.Batch executes its sole non-nil command directly.
	case tea.BatchMsg:
		if len(msg) == 0 {
			t.Fatal("stream completion returned an empty auto-compact batch")
		}
	default:
		t.Fatalf("stream completion did not return the scheduled auto-compact command: %T", scheduled)
	}
}

func TestMultilinePromptDoesNotLeaveBlankWhaleBeforeResponse(t *testing.T) {
	m := newTUIRegressionModel(t)
	multiline := strings.Join([]string{
		"PASTE-01 中文 alpha",
		"PASTE-02 中文 beta",
		"PASTE-03 中文 gamma",
		"PASTE-12 中文 omega SHORT-PASTE-AAAA",
	}, "\n")
	m.addMessage(formatUserMessage(multiline, time.Now().UnixMilli()))

	m.state = StateStreaming
	m.handleStreamDone(streamDoneMsg{
		blocks: []session.ContentBlock{{Type: "tool_use", ID: "ask-1", Name: "AskUser", Input: `{"question":"continue?"}`}},
		model:  "test-model",
	})
	m.state = StateStreaming
	m.handleStreamDone(streamDoneMsg{
		blocks: []session.ContentBlock{{Type: "text", Text: "AI response"}},
		model:  "test-model",
	})

	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))
	if got := strings.Count(transcript, "🐋 Whale:"); got != 1 {
		t.Fatalf("多行输入后不可见中间回合不应留下空白 Whale，count=%d transcript=%q", got, transcript)
	}
	if !strings.Contains(transcript, "AI response") {
		t.Fatalf("最终 AI 正文应保留，transcript=%q", transcript)
	}
}

func TestConversationWrapMatchesComposerTextWidth(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 12
	got := ansi.Strip(m.wrapViewportContent(formatUserMessage("1234567890", 0)))
	if !strings.Contains(got, "👤 You:\n123456789\n0") {
		t.Fatalf("历史正文应按输入框 9 列宽度换行，got %q", got)
	}
}

func TestSoftWrappedComposerNavigatesBeforeInputHistory(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetWidth(8)
	draft := "请回复 I-O，并继续补充"
	m.textarea.SetValue(draft)
	m.sessionService.SetInputHistory([]string{"older prompt"})
	m.sessionService.SetHistoryIdx(1)

	if composerAtVisualStart(&m.textarea) {
		t.Fatal("测试输入应在软换行后的可视行")
	}
	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := updatedModel.(Model)
	if got := updated.textarea.Value(); got != draft {
		t.Fatalf("软换行内部按 Up 不应切换历史，got %q", got)
	}
	if updated.historyIdx != 1 {
		t.Fatalf("软换行内部按 Up 不应改变历史索引，got %d", updated.historyIdx)
	}

	for steps := 0; !composerAtVisualStart(&updated.textarea) && steps < 20; steps++ {
		updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
		updated = updatedModel.(Model)
	}
	if !composerAtVisualStart(&updated.textarea) {
		t.Fatal("Up 应能移动到输入框第一条可视行")
	}
	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = updatedModel.(Model)
	if got := updated.textarea.Value(); got != "older prompt" {
		t.Fatalf("到达第一条可视行后再次按 Up 才应切换历史，got %q", got)
	}
}

func TestComposerMouseWheelScrollsOnlyInputViewport(t *testing.T) {
	m := newTUIRegressionModel(t)
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	updated := updatedModel.(Model)
	updated.reassignServices()

	lines := make([]string, 15)
	for i := range lines {
		lines[i] = fmt.Sprintf("input-line-%02d", i)
	}
	updated.textarea.SetValue(strings.Join(lines, "\n"))
	updated.adjustInputHeightAfterCoalesce()
	updated.viewport.SetContent(strings.Repeat("conversation\n", 40))
	updated.viewport.GotoBottom()

	beforeLine := updated.textarea.Line()
	beforeInputView := ansi.Strip(updated.textarea.View())
	beforeConversationOffset := updated.viewport.YOffset
	wheelY := updated.lastInputAreaTop + updated.lastPreviewHeight + 1

	updatedModel, _ = updated.Update(tea.MouseMsg{
		Y:      wheelY,
		Type:   tea.MouseWheelUp,
		Button: tea.MouseButtonWheelUp,
		Action: tea.MouseActionPress,
	})
	scrolled := updatedModel.(Model)
	scrolled.reassignServices()

	if got := scrolled.textarea.Line(); got >= beforeLine {
		t.Fatalf("input wheel-up should move to earlier input rows: before=%d after=%d", beforeLine, got)
	}
	if got := scrolled.viewport.YOffset; got != beforeConversationOffset {
		t.Fatalf("input wheel must not scroll conversation viewport: before=%d after=%d", beforeConversationOffset, got)
	}
	if afterInputView := ansi.Strip(scrolled.textarea.View()); afterInputView == beforeInputView {
		t.Fatal("input wheel-up should reveal different textarea content")
	}

	// Newer Bubble Tea events identify the wheel through Button; keep this path
	// working even when the deprecated Type field is left unset by a terminal.
	downBeforeLine := scrolled.textarea.Line()
	updatedModel, _ = scrolled.Update(tea.MouseMsg{
		Y:      wheelY,
		Button: tea.MouseButtonWheelDown,
		Action: tea.MouseActionPress,
	})
	down := updatedModel.(Model)
	down.reassignServices()
	if got := down.textarea.Line(); got <= downBeforeLine {
		t.Fatalf("button-only input wheel-down should move to later input rows: before=%d after=%d", downBeforeLine, got)
	}
}

func TestComposerHeightIncludesSoftWrappedRows(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetWidth(8)
	m.textarea.SetValue("12345678-abcdefgh-I-O")
	if got := m.desiredInputHeight(); got <= 3 {
		t.Fatalf("软换行内容应扩展输入框高度，got %d", got)
	}
	m.adjustInputHeightAfterCoalesce()
	if got := m.textarea.Height(); got <= 3 || got > maxInputHeight {
		t.Fatalf("输入框高度应包含软换行并受上限保护，got %d", got)
	}
}

func TestComposerNeverSplitsHyphenatedSuffixInsideInputBox(t *testing.T) {
	for width := 20; width <= 32; width++ {
		m := newTUIRegressionModel(t)
		updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		updated := updatedModel.(Model)
		updated.reassignServices()
		updated.textarea.SetValue("你好，请回复 I-O")
		updated.adjustInputHeightAfterCoalesce()
		plain := ansi.Strip(updated.renderInputArea())
		for _, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "I-") && !strings.Contains(line, "I-O") {
				t.Fatalf("terminal width %d split I-O after the hyphen: %q", width, plain)
			}
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("terminal width %d rendered an over-wide composer line (%d): %q", width, got, line)
			}
		}
	}
}

func TestComposerHyphensDoNotTriggerSecondaryWrap(t *testing.T) {
	newResizedModel := func(t *testing.T) Model {
		t.Helper()
		m := newTUIRegressionModel(t)
		updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		updated := updatedModel.(Model)
		updated.reassignServices()
		return updated
	}
	assertSingleVisualRow := func(t *testing.T, m Model, want string) {
		t.Helper()
		if got := m.textarea.Value(); got != want || strings.Contains(got, "\n") {
			t.Fatalf("输入值被意外改写：got %q, want %q", got, want)
		}
		plain := ansi.Strip(m.renderInputArea())
		for _, row := range strings.Split(plain, "\n") {
			if strings.Contains(row, want) {
				return
			}
		}
		t.Fatalf("连字符触发了额外换行：want %q in one row, rendered %q", want, plain)
	}

	t.Run("manual typing", func(t *testing.T) {
		m := newResizedModel(t)
		var typed strings.Builder
		for _, r := range "a-b-c" {
			// Model a normal typing cadence rather than the paste coalescer.
			m.inputCoalesce.lastTime = time.Time{}
			updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = updatedModel.(Model)
			m.reassignServices()
			typed.WriteRune(r)
			assertSingleVisualRow(t, m, typed.String())
		}
	})

	t.Run("bracketed paste", func(t *testing.T) {
		m := newResizedModel(t)
		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a-b-c"), Paste: true})
		updated := updatedModel.(Model)
		updated.reassignServices()
		assertSingleVisualRow(t, updated, "a-b-c")
	})

	t.Run("clipboard paste", func(t *testing.T) {
		m := newResizedModel(t)
		updatedModel, _ := m.Update(clipboardPasteMsg{text: "PAS-TE-12"})
		updated := updatedModel.(Model)
		updated.reassignServices()
		assertSingleVisualRow(t, updated, "PAS-TE-12")
	})
}

func TestWorkspaceStatusFieldNeverWraps(t *testing.T) {
	got := singleLineStatusField("Workspace: ", "/very/long/path/to/whale-code9", 24)
	if strings.Contains(got, "\n") {
		t.Fatalf("Workspace 字段不应换行，got %q", got)
	}
	if runewidth.StringWidth(got) > 24 {
		t.Fatalf("Workspace 字段宽度超过限制，got %d: %q", runewidth.StringWidth(got), got)
	}
	if !strings.Contains(got, "…") || !strings.HasSuffix(got, "whale-code9") {
		t.Fatalf("Workspace 截断应保留路径末尾，got %q", got)
	}
}

// TestBracketedPasteUsesEventRunes 验证 bracketed paste 直接插入终端事件内容并保留多行。
func TestBracketedPasteUsesEventRunes(t *testing.T) {
	m := newTUIRegressionModel(t)
	content := strings.Join([]string{
		"line-01", "line-02", "line-03", "line-04", "line-05", "line-06",
		"line-07", "line-08", "line-09", "line-10", "line-11", "line-12",
	}, "\n")

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(content), Paste: true})
	updated := updatedModel.(Model)
	if got := updated.textarea.Value(); got != content {
		t.Fatalf("bracketed paste 内容不一致\nwant: %q\n got: %q", content, got)
	}
	if updated.textarea.LineCount() != 12 {
		t.Fatalf("12 行粘贴不应被截断，actual %d 行", updated.textarea.LineCount())
	}
	if updated.textarea.Height() != maxInputHeight {
		t.Fatalf("视觉高度应限制为 %d，actual %d", maxInputHeight, updated.textarea.Height())
	}
}

// TestBracketedPasteAllowedDuringStreaming 验证流式期间仍可把文本粘贴到排队输入编辑器。
func TestBracketedPasteAllowedDuringStreaming(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("queued paste"), Paste: true})
	updated := updatedModel.(Model)
	if got := updated.textarea.Value(); got != "queued paste" {
		t.Fatalf("流式期间粘贴应进入输入框，actual %q", got)
	}
}

// TestExplicitPasteRuneLimit 验证显式粘贴仅做一次长度计算并截断到输入上限。
func TestExplicitPasteRuneLimit(t *testing.T) {
	m := newTUIRegressionModel(t)
	content := strings.Repeat("鲸", maxInputRunes+1)

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(content), Paste: true})
	updated := updatedModel.(Model)
	if got := utf8.RuneCountInString(updated.textarea.Value()); got != maxInputRunes {
		t.Fatalf("显式粘贴应截断到 %d rune，actual %d", maxInputRunes, got)
	}
	if len(updated.Messages()) == 0 || !strings.Contains(updated.Messages()[len(updated.Messages())-1], "50000") {
		t.Fatal("超限粘贴应显示明确的 50000 字符警告")
	}
}

// TestSendInputRejectsOverLimit 验证发送时拒绝超限输入且保留原文供用户编辑。
func TestSendInputRejectsOverLimit(t *testing.T) {
	m := newTUIRegressionModel(t)
	content := strings.Repeat("x", maxInputRunes+1)
	m.textarea.SetValue(content)

	updatedModel, cmd := m.sendInput()
	updated := updatedModel.(Model)
	if cmd != nil {
		t.Fatal("超限输入不应启动 AI 命令")
	}
	if updated.state != StateIdle {
		t.Fatalf("超限输入后应保持 Idle，actual %d", updated.state)
	}
	if updated.textarea.Value() != content {
		t.Fatal("超限输入应保留在编辑器中供用户缩短")
	}
}

// TestNonBracketedPasteEnterBecomesNewline 验证快速粘贴流中的 Enter 不会误发送。
func TestNonBracketedPasteEnterBecomesNewline(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("a")
	m.inputCoalesce = inputCoalesceState{
		buf:         []rune("bc"),
		active:      true,
		lastTime:    time.Now(),
		burstRunes:  3,
		burstEvents: 3,
	}

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	if cmd == nil {
		t.Fatal("粘贴换行应继续调度合并 flush")
	}
	if updated.state != StateIdle {
		t.Fatalf("粘贴换行不应触发发送，actual state %d", updated.state)
	}
	if got := string(updated.inputCoalesce.buf); got != "bc\n" {
		t.Fatalf("Enter 应进入合并缓冲，actual %q", got)
	}
}

// TestCoalesceEmptyFlushKeepsActiveWindow 验证空 flush 在活动窗口内不会过早结束粘贴分类。
func TestCoalesceEmptyFlushKeepsActiveWindow(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.inputCoalesce = inputCoalesceState{
		active:       true,
		lastTime:     time.Now(),
		flushPending: true,
		burstRunes:   3,
		flushSeq:     1,
	}

	cmd := m.handleCoalesceFlush(1)
	if cmd == nil {
		t.Fatal("活动窗口内的空 flush 应继续调度检查")
	}
	if !m.inputCoalesce.active || !m.inputCoalesce.flushPending {
		t.Fatal("活动窗口内不应重置输入突发状态")
	}
}

// TestSingleIMECommitEnterStillSends 验证单次多 rune 的 IME 提交不会被误判为多行粘贴。
func TestSingleIMECommitEnterStillSends(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("中文输入")
	m.inputCoalesce = inputCoalesceState{
		active:      true,
		lastTime:    time.Now(),
		burstRunes:  4,
		burstEvents: 1,
	}

	if m.shouldCoalesceEnter() {
		t.Fatal("单次 IME 提交后的 Enter 应发送，不应被转换为粘贴换行")
	}
}

// TestLongSingleChunkPasteEnterBecomesNewline 验证终端按整行事件投递长粘贴时仍可识别换行。
func TestLongSingleChunkPasteEnterBecomesNewline(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.inputCoalesce = inputCoalesceState{
		active:      true,
		lastTime:    time.Now(),
		burstRunes:  coalesceEnterSingleEventRunes,
		burstEvents: 1,
	}

	if !m.shouldCoalesceEnter() {
		t.Fatal("长单事件粘贴后的 Enter 应转换为换行")
	}
}

// TestCoalesceIgnoresStaleFlush 验证同步刷新后到达的旧 flush 不会处理新一轮缓冲。
func TestCoalesceIgnoresStaleFlush(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.inputCoalesce = inputCoalesceState{
		buf:      []rune("new"),
		active:   true,
		flushSeq: 2,
	}

	if cmd := m.handleCoalesceFlush(1); cmd != nil {
		t.Fatal("旧 flush 不应继续调度")
	}
	if got := string(m.inputCoalesce.buf); got != "new" {
		t.Fatalf("旧 flush 不应消费新一轮缓冲，actual %q", got)
	}
}

// TestTypeaheadDebounceOnlyForTriggers 验证普通输入不创建补全 timer，而 @/# 会启动世代化 debounce。
func TestTypeaheadDebounceOnlyForTriggers(t *testing.T) {
	plain := newTUIRegressionModel(t)
	plainModel, _ := plain.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if updated := plainModel.(Model); updated.typeaheadSeq != 0 {
		t.Fatalf("普通输入不应启动 typeahead debounce，actual seq %d", updated.typeaheadSeq)
	}

	trigger := newTUIRegressionModel(t)
	triggerModel, cmd := trigger.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})
	updated := triggerModel.(Model)
	if cmd == nil || updated.typeaheadSeq != 1 {
		t.Fatalf("@ 输入应启动一次 typeahead debounce，seq %d", updated.typeaheadSeq)
	}
}

// TestTypeaheadIgnoresStaleTick 验证旧世代补全 tick 不会执行扫描或继续调度。
func TestTypeaheadIgnoresStaleTick(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.typeaheadSeq = 2
	m.textarea.SetValue("@latest")

	updatedModel, cmd := m.Update(typeaheadTickMsg{seq: 1})
	updated := updatedModel.(Model)
	if cmd != nil {
		t.Fatal("旧 typeahead tick 不应产生后续命令")
	}
	if updated.suggestionState.Visible || updated.suggestionState.CurrentTrigger != 0 {
		t.Fatal("旧 typeahead tick 不应修改补全状态")
	}
}

// TestFinalizeStreamUsesActiveCell 验证 finalize 在清空 builder 前捕获文本和思考内容。
func TestFinalizeStreamUsesActiveCell(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.activeCell.AppendText("Hello World")
	m.activeCell.AppendThinking("reasoning")

	cmd := m.finalizeStream("")
	if cmd == nil {
		t.Fatal("finalizeStream 应返回完成消息命令")
	}
	msg, ok := cmd().(streamDoneMsg)
	if !ok {
		t.Fatal("finalizeStream 命令应产生 streamDoneMsg")
	}
	if len(msg.blocks) != 1 || msg.blocks[0].Text != "Hello World" {
		t.Fatalf("finalize 应保留 builder 文本，actual %#v", msg.blocks)
	}
	if msg.thinking != "reasoning" {
		t.Fatalf("finalize 应保留 builder 思考内容，actual %q", msg.thinking)
	}
	if m.activeCell.IsEmpty() {
		t.Fatal("streamDoneMsg 尚未提交前 ActiveCell 不应被清空")
	}
	m.handleStreamDone(msg)
	if !m.activeCell.IsEmpty() {
		t.Fatal("streamDoneMsg 提交后 ActiveCell 应被清空")
	}
}

func TestStopAfterFinalizePreservesCapturedText(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("request")
	m.activeCell.AppendText("completed before stop")

	cmd := m.finalizeStream("")
	if cmd == nil {
		t.Fatal("finalizeStream 应返回完成消息命令")
	}
	if m.activeCell.IsEmpty() {
		t.Fatal("完成消息尚未进入 Update 前必须保留 ActiveCell 文本")
	}

	updatedModel, _ := m.stopExecution()
	updated := updatedModel.(Model)
	transcript := strings.Join(updated.Messages(), "\n")
	if !strings.Contains(transcript, "completed before stop") {
		t.Fatalf("MessageStop 与 streamDoneMsg 之间中断不应丢失已生成文本: %q", transcript)
	}
}

// TestWaitingStateTickDoesNotReschedule 验证等待用户交互时不继续 125Hz tick。
func TestWaitingStateTickDoesNotReschedule(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateWaitingConfirm
	m.tickSeq = 1
	spinnerBefore := m.spinnerIdx

	updatedModel, cmd := m.Update(tickMsg{at: time.Now(), seq: 1})
	updated := updatedModel.(Model)
	if cmd != nil {
		t.Fatal("WaitingConfirm tick 不应继续调度")
	}
	if updated.spinnerIdx != spinnerBefore {
		t.Fatal("WaitingConfirm tick 不应推进 spinner")
	}
}

// TestSpinnerUsesLowerFrequency 验证 spinner 在 80ms 间隔内不会重复推进。
func TestSpinnerUsesLowerFrequency(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.frameLimiter = nil
	m.tickSeq = 1
	now := time.Now()

	firstModel, firstCmd := m.Update(tickMsg{at: now, seq: 1})
	first := firstModel.(Model)
	if firstCmd == nil {
		t.Fatal("Streaming tick 应继续调度")
	}
	firstSpinner := first.spinnerIdx

	secondModel, secondCmd := first.Update(tickMsg{at: now.Add(10 * time.Millisecond), seq: 1})
	second := secondModel.(Model)
	if secondCmd == nil {
		t.Fatal("Streaming tick 应持续调度")
	}
	if second.spinnerIdx != firstSpinner {
		t.Fatalf("80ms 内 spinner 不应推进，before %d after %d", firstSpinner, second.spinnerIdx)
	}
}

// TestTickIgnoresStaleGeneration 验证活动状态切换后旧 tick 链不会继续复制调度。
func TestTickIgnoresStaleGeneration(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.tickSeq = 2
	spinnerBefore := m.spinnerIdx

	updatedModel, cmd := m.Update(tickMsg{at: time.Now(), seq: 1})
	updated := updatedModel.(Model)
	if cmd != nil {
		t.Fatal("旧 tick 世代不应继续调度")
	}
	if updated.spinnerIdx != spinnerBefore {
		t.Fatal("旧 tick 世代不应推进 spinner")
	}
}

// TestRenderStreamContentPreservesScroll 验证用户上翻历史后流式渲染不强制跳到底部。
func TestRenderStreamContentPreservesScroll(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.ready = true
	m.viewport.Width = 76
	m.viewport.Height = 8
	for index := 0; index < 40; index++ {
		m.messages.Append(fmt.Sprintf("message-%02d", index))
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	m.viewport.LineUp(5)
	before := m.viewport.YOffset
	m.state = StateStreaming
	m.activeCell.AppendText("streaming delta")

	m.renderStreamContent()
	if m.viewport.YOffset != before {
		t.Fatalf("流式渲染应保留滚动位置，before %d after %d", before, m.viewport.YOffset)
	}
}

// TestWindowResizePreservesScroll 验证窗口 resize 不会劫持用户的历史浏览位置。
func TestWindowResizePreservesScroll(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.ready = true
	m.width = 80
	m.height = 24
	m.viewport.Width = 76
	m.viewport.Height = 8
	for index := 0; index < 40; index++ {
		m.messages.Append(fmt.Sprintf("message-%02d", index))
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	m.viewport.LineUp(5)
	beforeGap := m.viewport.TotalLineCount() - m.viewport.Height - m.viewport.YOffset

	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 81, Height: 24})
	updated := updatedModel.(Model)
	if updated.viewport.AtBottom() {
		t.Fatal("resize 不应把上翻历史的用户强制拉到底部")
	}
	afterGap := updated.viewport.TotalLineCount() - updated.viewport.Height - updated.viewport.YOffset
	if afterGap != beforeGap {
		t.Fatalf("resize 应保留距底部行数，before %d after %d", beforeGap, afterGap)
	}
}

// TestAddMessagePreservesScroll 验证后台或完成消息追加时不会劫持用户的历史浏览位置。
func TestAddMessagePreservesScroll(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 76
	m.viewport.Height = 8
	for index := 0; index < 40; index++ {
		m.messages.Append(fmt.Sprintf("message-%02d", index))
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	m.viewport.LineUp(5)
	before := m.viewport.YOffset

	m.addMessage("background-result")
	if m.viewport.YOffset != before {
		t.Fatalf("追加消息应保留滚动位置，before %d after %d", before, m.viewport.YOffset)
	}
}

// TestStaleStreamMessagesAreIgnored 验证旧请求的 delta、完成和错误消息不会污染新请求。
func TestStaleStreamMessagesAreIgnored(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("new-request")
	m.activeCell.AppendText("new")

	updatedModel, cmd := m.Update(streamEventMsg{
		event:     ai.TextDeltaEvent{Content: "-old"},
		requestID: "old-request",
	})
	updated := updatedModel.(Model)
	if cmd != nil || updated.activeCell.Text() != "new" {
		t.Fatal("旧请求 delta 不应进入当前 ActiveCell")
	}

	updatedModel, cmd = updated.Update(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "old"}},
		requestID: "old-request",
	})
	updated = updatedModel.(Model)
	if cmd != nil || updated.state != StateStreaming {
		t.Fatal("旧请求完成消息不应结束当前流")
	}

	updatedModel, cmd = updated.Update(aiErrorMsg{err: fmt.Errorf("old error"), requestID: "old-request"})
	updated = updatedModel.(Model)
	if cmd != nil || updated.state != StateStreaming {
		t.Fatal("旧请求错误不应重置当前流状态")
	}
}

// TestToastVisibilityRecalculatesViewport 验证 toast 出现和消失都会同步调整 viewport 高度。
func TestToastVisibilityRecalculatesViewport(t *testing.T) {
	m := newTUIRegressionModel(t)
	resizedModel, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	resized := resizedModel.(Model)
	resized.reassignServices()
	m = &resized
	baselineHeight := m.viewport.Height

	notification := notify.NewNotification("layout", "布局", "toast 占用一行")
	notification.TimeoutMs = 100
	m.notifier.Push(notification)
	updatedModel, _ := m.Update(toastPushMsg{})
	updated := updatedModel.(Model)
	if updated.viewport.Height >= baselineHeight {
		t.Fatalf("toast 出现后 viewport 应缩小，before %d after %d", baselineHeight, updated.viewport.Height)
	}

	updatedModel, _ = updated.Update(toastTimeoutMsg{seq: updated.toast.timeoutSeq})
	updated = updatedModel.(Model)
	if updated.viewport.Height != baselineHeight {
		t.Fatalf("toast 消失后 viewport 应恢复，want %d got %d", baselineHeight, updated.viewport.Height)
	}
}

// TestTypeaheadAppliesAtActualCursor 验证补全在中间光标处替换 token 并恢复光标，而不是强制跳到末尾。
func TestTypeaheadAppliesAtActualCursor(t *testing.T) {
	m := newTUIRegressionModel(t)
	original := "前缀 #old 后缀"
	m.textarea.SetValue(original)
	m.textarea.SetCursor(utf8.RuneCountInString("前缀 #old"))
	m.suggestionState = &SuggestionState{
		Visible:        true,
		TokenStart:     len("前缀 "),
		TokenLen:       len("old"),
		CurrentTrigger: typeahead.TriggerHash,
	}
	m.reassignServices()
	if got := textareaCursorByteOffset(&m.textarea); got != len("前缀 #old") {
		t.Fatalf("应读取实际中间光标，want %d got %d", len("前缀 #old"), got)
	}

	m.applyTypeaheadCompletion(typeahead.Completion{DisplayText: "新技能", Trigger: typeahead.TriggerHash})
	want := "前缀 #新技能  后缀"
	if got := m.textarea.Value(); got != want {
		t.Fatalf("补全应替换实际光标处 token\nwant: %q\n got: %q", want, got)
	}
	if got := textareaCursorByteOffset(&m.textarea); got != len("前缀 #新技能 ") {
		t.Fatalf("补全后光标位置错误，want %d got %d", len("前缀 #新技能 "), got)
	}
}

// TestCompletionPopupUnicodeAndNarrowWidth 验证中文描述不会被字节截断，弹窗也不会超过给定宽度。
func TestCompletionPopupUnicodeAndNarrowWidth(t *testing.T) {
	state := &SuggestionState{
		Visible:        true,
		CurrentTrigger: typeahead.TriggerHash,
		Completions: []typeahead.Completion{{
			DisplayText: "中文技能名称很长",
			Description: "这是一个很长的中文描述，用于验证显示宽度截断",
		}},
	}

	view := state.renderCompletionPopup(24)
	if !utf8.ValidString(view) {
		t.Fatal("补全弹窗不得产生无效 UTF-8")
	}
	if width := lipgloss.Width(view); width > 24 {
		t.Fatalf("补全弹窗不应超过请求宽度，actual %d", width)
	}
	if narrow := state.renderCompletionPopup(popupBorderStyle.GetHorizontalFrameSize() + 4); narrow != "" {
		t.Fatal("宽度不足以容纳边框时应隐藏弹窗，避免撑爆终端")
	}
}

// TestCtrlCCancelsInteractiveStates 验证 Ctrl+C 在确认、选择和文档导航状态下均执行安全取消。
func TestCtrlCCancelsInteractiveStates(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.SessionModel.CurrentSession().SetMessages([]session.Message{
			session.AssistantText("copy candidate", "test-model", nil),
		})
		beforeMessages := m.SessionModel.Messages()

		updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		updated := updatedModel.(Model)
		if cmd != nil {
			t.Fatal("空闲时 Ctrl+C 不应产生 Bubble Tea 命令")
		}
		if updated.state != StateIdle || updated.interrupted {
			t.Fatal("空闲时 Ctrl+C 应保持 Idle 且不标记中断")
		}
		if updated.pendingCommandCmd != nil {
			t.Fatal("空闲时 Ctrl+C 不应触发剪贴板复制")
		}
		if got := updated.SessionModel.Messages(); !reflect.DeepEqual(got, beforeMessages) {
			t.Fatalf("空闲时 Ctrl+C 不应追加复制提示，before=%q after=%q", beforeMessages, got)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.state = StateStreaming
		m.aiService.SetRequestID("active-request")
		m.activeCell.AppendText("partial")

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		updated := updatedModel.(Model)
		if updated.requestID != "" {
			t.Fatalf("Ctrl+C 应立即失效 requestID，actual=%q state=%d", updated.requestID, updated.state)
		}
		if updated.state != StateIdle || !updated.interrupted {
			t.Fatal("Ctrl+C 应中断流并回到 Idle")
		}
	})

	t.Run("confirm", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.state = StateWaitingConfirm
		m.pendingConfirm = &PendingConfirm{Kind: ApprovalKindExec}

		updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		updated := updatedModel.(Model)
		if updated.state != StateIdle || updated.pendingConfirm != nil {
			t.Fatal("Ctrl+C 应取消普通确认并回到 Idle")
		}
		result, ok := cmd().(confirmResultMsg)
		if !ok || result.approved {
			t.Fatal("Ctrl+C 应产生 approved=false 的确认结果")
		}
	})

	t.Run("choice", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.state = StateWaitingChoice
		m.pendingChoice = &PendingChoice{ToolID: "tool-1", Options: []ChoiceOption{{Label: "A", Value: "a"}}}

		updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		updated := updatedModel.(Model)
		if updated.pendingChoice != nil {
			t.Fatal("Ctrl+C 应清理等待选择")
		}
		result, ok := cmd().(choiceResultMsg)
		if !ok || !result.Cancelled {
			t.Fatal("Ctrl+C 应产生 Cancelled=true 的选择结果")
		}
	})

	t.Run("viewing-doc", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.state = StateViewingDoc
		m.docLinks = []FileLink{{Path: "README.md"}}

		updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		updated := updatedModel.(Model)
		if cmd != nil {
			t.Fatal("关闭文档导航不应触发 AI 中断命令")
		}
		if updated.state != StateIdle || len(updated.docLinks) != 0 || updated.interrupted {
			t.Fatal("Ctrl+C 应仅关闭文档导航并回到 Idle")
		}
	})
}

// TestViewingDocIgnoresComposerInput 验证文档导航期间普通字符不会污染隐藏输入框。
func TestViewingDocIgnoresComposerInput(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateViewingDoc
	m.textarea.SetValue("draft")

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	updated := updatedModel.(Model)
	if cmd != nil {
		t.Fatal("文档导航中的普通字符不应产生命令")
	}
	if got := updated.textarea.Value(); got != "draft" {
		t.Fatalf("文档导航不应修改 composer，actual %q", got)
	}
}

// TestElicitationCtrlCContract 验证 MCP elicitation 的 Ctrl+C 与 Esc 一样映射为 Cancel。
func TestElicitationCtrlCContract(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0)

	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || m.toolService.PendingConfirm() != nil {
		t.Fatal("Ctrl+C 应取消 elicitation 并清理 PendingConfirm")
	}
}

// TestToastIgnoresStaleTimeout 验证旧通知 timeout 不会关闭后续新通知。
func TestToastIgnoresStaleTimeout(t *testing.T) {
	notifier := notify.NewNotifier()
	toast := NewToastModel(notifier)
	first := notify.NewNotification("first", "First", "one")
	first.TimeoutMs = 100
	notifier.Push(first)
	if cmd := toast.showNext(); cmd == nil {
		t.Fatal("首个 toast 应调度 timeout")
	}
	staleSeq := toast.timeoutSeq

	second := notify.NewNotification("second", "Second", "two")
	second.TimeoutMs = 100
	notifier.Push(second)
	_, nextCmd := toast.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if nextCmd == nil {
		t.Fatal("关闭首个 toast 后应显示并调度第二个 toast")
	}
	if current := notifier.Current(); current == nil || current.Key != "second" {
		t.Fatal("第二个 toast 应成为当前通知")
	}

	_, _ = toast.Update(toastTimeoutMsg{seq: staleSeq})
	if current := notifier.Current(); current == nil || current.Key != "second" {
		t.Fatal("旧 timeout 不应关闭第二个 toast")
	}
}

// TestImmediateToastConsumesEscape 验证 Immediate 通知抢占 Esc 而不会同时中断流式任务。
func TestImmediateToastConsumesEscape(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.notifier.Push(notify.NewImmediateNotification("urgent", "Urgent", "review"))
	if cmd := m.toast.showNext(); cmd != nil {
		t.Fatal("Immediate toast 应为持久通知，不应启动 timeout")
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	if updated.state != StateStreaming {
		t.Fatal("Esc 关闭 Immediate toast 时不应同时中断流式任务")
	}
	if updated.toast.visible || updated.notifier.Current() != nil {
		t.Fatal("Esc 应关闭 Immediate toast")
	}
}

// TestLocalSlashCommandSkipsRegistryHandler 验证 TUI 本地命令不会先走 Registry 回调再重复执行。
func TestLocalSlashCommandSkipsRegistryHandler(t *testing.T) {
	m := newTUIRegressionModel(t)
	registryCalls := 0
	m.cmdRegistry.OnUndo = func() string {
		registryCalls++
		return "registry undo"
	}

	output := m.handleCommand("/undo")
	if registryCalls != 0 {
		t.Fatalf("本地 /undo 不应执行 Registry 回调，actual %d", registryCalls)
	}
	if output == "registry undo" {
		t.Fatal("本地 /undo 应使用当前 Model 的唯一处理路径")
	}
}

// TestNotifyDismissAdvancesQueueAndRecalculatesLayout 验证命令关闭通知后会显示下一条并保持布局同步。
func TestNotifyDismissAdvancesQueueAndRecalculatesLayout(t *testing.T) {
	m := newTUIRegressionModel(t)
	resizedModel, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	resized := resizedModel.(Model)
	resized.reassignServices()
	m = &resized
	baselineHeight := m.viewport.Height

	first := notify.NewNotification("first", "First", "one")
	first.TimeoutMs = 100
	second := notify.NewNotification("second", "Second", "two")
	second.TimeoutMs = 100
	m.notifier.Push(first)
	m.notifier.Push(second)
	_ = m.toast.showNext()
	m.recalcViewportHeight()
	visibleHeight := m.viewport.Height
	if visibleHeight >= baselineHeight {
		t.Fatal("首条通知显示后 viewport 应缩小")
	}

	output := m.handleCommand("/notify dismiss")
	if !strings.Contains(output, "First") {
		t.Fatalf("关闭提示应包含首条通知标题，actual %q", output)
	}
	if current := m.notifier.Current(); current == nil || current.Key != "second" {
		t.Fatal("关闭首条通知后第二条应立即成为当前通知")
	}
	if !m.toast.visible || m.viewport.Height != visibleHeight {
		t.Fatal("下一条通知显示期间 toast 和 viewport 布局应保持同步")
	}
	if m.pendingCommandCmd == nil {
		t.Fatal("下一条有超时时间的通知应把 timer 命令交回 Bubble Tea")
	}

	_ = m.handleCommand("/notify dismiss")
	if m.toast.visible || m.notifier.Current() != nil {
		t.Fatal("关闭最后一条通知后 toast 应完全隐藏")
	}
	if m.viewport.Height != baselineHeight {
		t.Fatalf("通知队列清空后 viewport 应恢复，want %d got %d", baselineHeight, m.viewport.Height)
	}
}

// TestExportCommandRunsOnceAndKeepsRequestedFormat 验证导出命令不再双执行或用 Markdown 覆盖 JSON。
func TestExportCommandRunsOnceAndKeepsRequestedFormat(t *testing.T) {
	m := newTUIRegressionModel(t)
	secretValue := "api_key=sk-test123456789012345678901234567890"
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText(secretValue),
		session.AssistantText("done", "test-model", nil),
	})
	registryCalls := 0
	m.cmdRegistry.OnExport = func(args string) string {
		registryCalls++
		return "registry export"
	}
	path := filepath.Join(t.TempDir(), "session.json")

	output := m.handleCommand("/export --format json " + path)
	if registryCalls != 0 {
		t.Fatalf("本地导出不应再执行 Registry 回调，actual %d", registryCalls)
	}
	if !strings.Contains(output, "json") {
		t.Fatalf("导出结果应报告 JSON 格式，actual %q", output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取导出文件失败: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(strings.TrimSpace(content), "{") {
		t.Fatalf("JSON 导出不应被 Markdown 覆盖，actual %q", content)
	}
	if strings.Contains(content, secretValue) || !strings.Contains(content, "[REDACTED]") {
		t.Fatal("导出文件必须在写盘前脱敏")
	}
}

// TestCopyCommandRedactsBeforeAsyncClipboard 验证复制命令在异步写剪贴板前先完成脱敏。
func TestCopyCommandRedactsBeforeAsyncClipboard(t *testing.T) {
	m := newTUIRegressionModel(t)
	secretValue := "api_key=sk-test123456789012345678901234567890"
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.AssistantText(secretValue, "test-model", nil),
	})

	output := m.handleCommand("/copy last")
	if strings.Contains(output, secretValue) || !strings.Contains(output, "脱敏") {
		t.Fatalf("复制提示不得泄露原始密钥并应报告脱敏，actual %q", output)
	}
	if m.pendingCommandCmd == nil {
		t.Fatal("复制写入应交给 Bubble Tea 异步执行")
	}
}

func TestCopyShortcutsStartClipboardWrite(t *testing.T) {
	newModel := func() *Model {
		m := newTUIRegressionModel(t)
		m.sessionService.CurrentSession().SetMessages([]session.Message{
			session.UserText("user prompt"),
			session.AssistantText("assistant reply", "test-model", nil),
		})
		return m
	}

	t.Run("ctrl+y copies last response", func(t *testing.T) {
		m := newModel()
		beforeMessages := len(m.Messages())
		beforeNotifications := len(m.notifier.List())
		updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
		updated := updatedModel.(Model)
		if cmd == nil || updated.pendingCommandCmd != nil {
			t.Fatal("Ctrl+Y 应立即返回剪贴板写入命令")
		}
		if len(updated.Messages()) != beforeMessages || len(updated.notifier.List()) != beforeNotifications+1 {
			t.Fatal("Ctrl+Y 应使用 toast 反馈且不改变对话历史")
		}
	})

	t.Run("alt+y copies all history", func(t *testing.T) {
		m := newModel()
		beforeMessages := len(m.Messages())
		beforeNotifications := len(m.notifier.List())
		updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}, Alt: true})
		updated := updatedModel.(Model)
		if cmd == nil || updated.pendingCommandCmd != nil {
			t.Fatal("Alt+Y 应立即返回完整历史的剪贴板写入命令")
		}
		if len(updated.Messages()) != beforeMessages || len(updated.notifier.List()) != beforeNotifications+1 {
			t.Fatal("Alt+Y 应使用 toast 反馈且不改变对话历史")
		}
	})
}

func TestCopyAllButtonCopiesHistory(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText("user prompt"),
		session.AssistantText("assistant reply", "test-model", nil),
	})
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	updated := updatedModel.(Model)
	updated.reassignServices()
	beforeMessages := len(updated.Messages())
	beforeNotifications := len(updated.notifier.List())

	row := ansi.Strip(renderInputActionRow(updated.width-4, false, false, ""))
	if !strings.Contains(row, "Copy all") {
		t.Fatalf("宽终端的输入工具栏应展示 Copy all，got %q", row)
	}
	buttonY := updated.lastInputAreaTop + updated.lastPreviewHeight + updated.textarea.Height() + inputBoxStyle.GetVerticalFrameSize()
	// 档B：点击 = 按下（只高亮）+ 松开（才开火）两个事件。
	pressModel, pressCmd := updated.Update(tea.MouseMsg{
		X:      1,
		Y:      buttonY + 1,
		Type:   tea.MouseLeft,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	pressed := pressModel.(Model)
	if pressCmd != nil || pressed.pressedBtn != "copy" {
		t.Fatal("按下 Copy all 应只登记按下态（高亮），不立即开火")
	}
	updatedModel, cmd := pressed.Update(tea.MouseMsg{
		X:      1,
		Y:      buttonY + 1,
		Button: tea.MouseButtonNone,
		Action: tea.MouseActionRelease,
	})
	clicked := updatedModel.(Model)
	if cmd == nil || clicked.pendingCommandCmd != nil {
		t.Fatal("点击 Copy all 应立即返回剪贴板写入命令")
	}
	if clicked.state != StateIdle {
		t.Fatal("点击 Copy all 不应改变会话状态")
	}
	if len(clicked.Messages()) != beforeMessages || len(clicked.notifier.List()) != beforeNotifications+1 {
		t.Fatal("Copy all 应使用 toast 反馈且不改变对话历史")
	}
}

// TestClipboardPasteResultsPreserveInvocationOrder 验证连续 Ctrl+V 即使倒序完成也按触发顺序插入。
func TestClipboardPasteResultsPreserveInvocationOrder(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.clipboardNextSeq = 2

	updatedModel, _ := m.Update(clipboardPasteMsg{seq: 2, text: "second"})
	updated := updatedModel.(Model)
	if updated.textarea.Value() != "" {
		t.Fatal("第二次粘贴先完成时应等待第一次结果")
	}

	updatedModel, _ = updated.Update(clipboardPasteMsg{seq: 1, text: "first"})
	updated = updatedModel.(Model)
	if got := updated.textarea.Value(); got != "firstsecond" {
		t.Fatalf("异步粘贴结果应按调用顺序应用，actual %q", got)
	}
	if updated.clipboardApplySeq != 2 || len(updated.clipboardPending) != 0 {
		t.Fatal("连续结果应用后排序缓冲应清空")
	}

	updatedModel, _ = updated.Update(clipboardPasteMsg{seq: 1, text: "duplicate"})
	updated = updatedModel.(Model)
	if got := updated.textarea.Value(); got != "firstsecond" {
		t.Fatalf("重复旧结果应被忽略，actual %q", got)
	}
}

// BenchmarkTextareaLongInputCharLimit 量化 4 万字符输入下 CharLimit 热路径的额外扫描成本。
func BenchmarkTextareaLongInputCharLimit(b *testing.B) {
	const insertions = 1000
	initial := strings.Repeat("x", 40000)
	for _, testCase := range []struct {
		name      string
		charLimit int
	}{
		{name: "CharLimit50000", charLimit: 50000},
		{name: "CharLimitDisabled", charLimit: 0},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				b.StopTimer()
				input := textarea.New()
				input.CharLimit = testCase.charLimit
				input.MaxHeight = 0
				input.SetValue(initial)
				b.StartTimer()
				for index := 0; index < insertions; index++ {
					input.InsertString("x")
				}
				tuiBenchmarkSink = input.Value()
			}
		})
	}
}

// BenchmarkStreamAccumulation 量化完整字符串相加与 strings.Builder 追加流式 delta 的差异。
func BenchmarkStreamAccumulation(b *testing.B) {
	const deltas = 5000
	const delta = "12345678"
	b.Run("StringConcat", func(b *testing.B) {
		b.ReportAllocs()
		for iteration := 0; iteration < b.N; iteration++ {
			current := ""
			for index := 0; index < deltas; index++ {
				current += delta
			}
			tuiBenchmarkSink = current
		}
	})
	b.Run("StringsBuilder", func(b *testing.B) {
		b.ReportAllocs()
		for iteration := 0; iteration < b.N; iteration++ {
			var builder strings.Builder
			for index := 0; index < deltas; index++ {
				builder.WriteString(delta)
			}
			tuiBenchmarkSink = builder.String()
		}
	})
}
