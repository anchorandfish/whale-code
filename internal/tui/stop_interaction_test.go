package tui

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var stopTimestampPattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\b`)

func activeMouseTestModel(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.SetMouseMode(false, true)
	if cmd := m.enableActiveMouse(); cmd == nil || cmd() == nil {
		t.Fatal("active turn should enable temporary mouse capture")
	}
	if !m.mouseEnabled {
		t.Fatal("temporary mouse capture state was not enabled")
	}
	return m
}

func TestAutomaticMouseCaptureRestoresAfterTurnEnds(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		m := activeMouseTestModel(t)
		m.state = StateStreaming
		m.aiService.SetRequestID("normal-request")
		cmd := m.handleStreamDone(streamDoneMsg{
			blocks:    []session.ContentBlock{{Type: "text", Text: "done"}},
			usage:     &session.TokenUsage{},
			model:     "test-model",
			requestID: "normal-request",
		})
		if m.mouseEnabled || cmd == nil || cmd() == nil {
			t.Fatal("normal completion should restore terminal-native selection")
		}
	})

	t.Run("stop", func(t *testing.T) {
		m := activeMouseTestModel(t)
		m.state = StateStreaming
		m.aiService.SetRequestID("stop-request")
		updatedModel, cmd := m.stopExecution()
		updated := updatedModel.(Model)
		if updated.mouseEnabled || cmd == nil || cmd() == nil {
			t.Fatal("Stop should restore terminal-native selection")
		}
	})

	t.Run("error", func(t *testing.T) {
		m := activeMouseTestModel(t)
		m.state = StateStreaming
		m.aiService.SetRequestID("error-request")
		updatedModel, cmd := m.Update(aiErrorMsg{err: errors.New("network failed"), requestID: "error-request"})
		updated := updatedModel.(Model)
		if updated.mouseEnabled || cmd == nil {
			t.Fatal("stream error should restore terminal-native selection")
		}
	})

	t.Run("stream event error", func(t *testing.T) {
		m := activeMouseTestModel(t)
		m.state = StateStreaming
		cmd := m.handleStreamEvent(ai.ErrorEvent{Err: errors.New("stream failed")}, nil)
		if m.mouseEnabled || cmd == nil || cmd() == nil {
			t.Fatal("an ErrorEvent inside the stream should restore terminal-native selection")
		}
	})
}

func TestPersistentMouseModeRemainsEnabledAfterCompletion(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.SetMouseMode(true, false)
	m.state = StateStreaming
	m.aiService.SetRequestID("request")
	cmd := m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "done"}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "request",
	})
	if !m.mouseEnabled || cmd != nil {
		t.Fatal("explicit /mouse on preference should survive turn completion")
	}
}

func TestNewTurnInvalidatesLateMouseDisable(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.SetMouseMode(false, true)
	if cmd := m.enableActiveMouse(); cmd == nil {
		t.Fatal("first active turn should schedule mouse enable")
	}
	lateDisable := m.restoreIdleMouse()
	if lateDisable == nil {
		t.Fatal("stopped turn should schedule mouse disable")
	}
	newEnable := m.enableActiveMouse()
	if newEnable == nil {
		t.Fatal("immediate next turn should schedule a new mouse enable")
	}
	if msg := lateDisable(); msg != nil {
		t.Fatalf("superseded mouse disable should be discarded, got %T", msg)
	}
	if msg := newEnable(); msg == nil {
		t.Fatal("latest mouse enable should remain effective")
	}
	if !m.mouseEnabled {
		t.Fatal("immediate next turn should finish with mouse capture enabled")
	}
}

func TestQueuedSlashCommandRestoresIdleMouseCapture(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.SetMouseMode(false, true)
	m.mouseEnabled = true
	m.textarea.SetValue("/help")
	updatedModel, cmd := m.sendInput()
	updated := updatedModel.(Model)
	if updated.state != StateIdle || updated.mouseEnabled || cmd == nil {
		t.Fatalf("non-streaming queued slash command should restore idle mouse mode: state=%d mouse=%v", updated.state, updated.mouseEnabled)
	}
}

func TestStopButtonMiddleRowCancelsActiveTurn(t *testing.T) {
	m := newTUIRegressionModel(t)
	resizedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	resized := resizedModel.(Model)
	resized.reassignServices()
	resized.state = StateStreaming
	resized.aiService.SetRequestID("click-request")
	resized.activeCell.AppendText("partial")
	cancelled := false
	resized.aiService.SetStreamCancel(func() { cancelled = true })
	resized.recalcViewportHeight()

	button := renderActionButton(resized.width-4, true, false, "")
	buttonY := resized.lastInputAreaTop + resized.lastPreviewHeight + resized.textarea.Height() + inputBoxStyle.GetVerticalFrameSize()
	buttonX := resized.width - 4 - lipgloss.Width(button)
	// 档B：按下只高亮、松开才开火（GUI 标准按钮语义）。
	pressModel, _ := resized.Update(tea.MouseMsg{
		X:      buttonX + 1,
		Y:      buttonY + 1,
		Type:   tea.MouseLeft,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	pressed := pressModel.(Model)
	if pressed.pressedBtn != "stop" {
		t.Fatal("按下 Stop 应登记按下态（高亮）")
	}
	if cancelled {
		t.Fatal("按下阶段不应取消回合（松开才开火）")
	}
	updatedModel, _ := pressed.Update(tea.MouseMsg{
		X:      buttonX + 1,
		Y:      buttonY + 1,
		Button: tea.MouseButtonNone,
		Action: tea.MouseActionRelease,
	})
	updated := updatedModel.(Model)
	if !cancelled || updated.state != StateIdle {
		t.Fatal("松开三行 Stop 按钮的中行应取消活动回合")
	}
}

func TestFooterClickDoesNotTriggerStop(t *testing.T) {
	m := newTUIRegressionModel(t)
	resizedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	resized := resizedModel.(Model)
	resized.reassignServices()
	resized.state = StateStreaming
	resized.aiService.SetRequestID("active-request")
	cancelled := false
	resized.aiService.SetStreamCancel(func() { cancelled = true })
	resized.recalcViewportHeight()

	button := renderActionButton(resized.width-4, true, false, "")
	buttonX := resized.width - 4 - lipgloss.Width(button)
	updatedModel, _ := resized.Update(tea.MouseMsg{
		X:      buttonX + 1,
		Y:      resized.height - 1,
		Type:   tea.MouseLeft,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	updated := updatedModel.(Model)
	view := updated.View()
	if got := lipgloss.Height(view); got != updated.height {
		t.Fatalf("rendered view height %d should fill terminal height %d", got, updated.height)
	}
	viewLines := strings.Split(view, "\n")
	status := updated.renderStatusBar()
	statusHeight := lipgloss.Height(status)
	if len(viewLines) < statusHeight || !strings.Contains(strings.Join(viewLines[len(viewLines)-statusHeight:], "\n"), "Workspace:") {
		t.Fatalf("status footer should occupy the final %d terminal rows: %q", statusHeight, view)
	}
	if cancelled || updated.state != StateStreaming {
		t.Fatal("clicking the fixed status footer must not activate Stop")
	}
}

func TestInputAreaHeightIncludesCompleteButton(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.width = 80
	m.height = 30
	m.state = StateStreaming
	m.suggestionState.Clear()
	buttonHeight := lipgloss.Height(renderActionButton(m.width-4, true, false, ""))
	want := m.textarea.Height() + inputBoxStyle.GetVerticalFrameSize() + buttonHeight
	if buttonHeight != 3 {
		t.Fatalf("rounded action button height = %d, want 3", buttonHeight)
	}
	if got := m.estimateInputAreaHeight(); got != want {
		t.Fatalf("input area height = %d, want %d including all button rows", got, want)
	}
}

func TestStopDisplaysOneEndingTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial string
	}{
		{name: "partial", partial: "partial response"},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTUIRegressionModel(t)
			m.state = StateStreaming
			m.aiService.SetRequestID("request")
			m.activeCell.AppendText(tc.partial)
			updatedModel, _ := m.stopExecution()
			updated := updatedModel.(Model)
			updated.reassignServices()
			// 2026-10-07（用户拍板）契约变更：中断轮的结束面收敛为**单行结束标志**
			// （`━━ ⏹ <YYYY-MM-DD HH:MM:SS> ━━`），原先独立的
			// `Stopped │ <日期> │ interrupted` 行已被合并 ⇒ 断言语义随之从
			// "恰好一个 Stopped 行"改为"恰好一个结束标志/结束时刻"（R10）。
			display := ansi.Strip(updated.renderMessages())
			if got := len(stopTimestampPattern.FindAllString(display, -1)); got != 1 {
				t.Fatalf("中断轮应恰好一个结束时刻，got %d: %q", got, display)
			}
			if !turnInterruptedMarkPattern.MatchString(display) {
				t.Fatalf("中断轮应留结束标志（━━ ⏹ + 完整时刻）：%q", display)
			}
			if strings.Contains(display, "Stopped") || strings.Contains(display, "interrupted") {
				t.Fatalf("中断行应已合并进结束标志，不得再有独立的 Stopped/interrupted 行: %q", display)
			}
		})
	}
}

func TestStopKeepsVisibleInterruptedTurnInOrder(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("request")
	m.addMessage(userStyle.Render("👤 You: ") + "写一首短诗")
	m.activeCell.AppendText("部分诗句")

	updatedModel, _ := m.stopExecution()
	updated := updatedModel.(Model)
	updated.reassignServices()
	// 顺序断言改用渲染转录：结束标志是投影（不在 Messages() 里），
	// 且 2026-10-07 起以 ⏹ 标志（而非独立 Stopped 行）承担中断结束面。
	display := ansi.Strip(updated.renderMessages())
	userAt := strings.Index(display, "写一首短诗")
	assistantAt := strings.Index(display, "部分诗句")
	markAt := strings.Index(display, "━━ ⏹ ")
	if userAt < 0 || assistantAt < 0 || markAt < 0 || userAt > assistantAt || assistantAt > markAt {
		t.Fatalf("interrupted turn display order is invalid: %q", display)
	}
}

func TestStopBalancesUnterminatedMarkdownFence(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("request")
	m.activeCell.AppendText("```\n代码如海，行行浪涌")

	updatedModel, _ := m.stopExecution()
	updated := updatedModel.(Model)
	messages := updated.session.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("expected one interrupted assistant message, got %d", len(messages))
	}
	text := messages[0].TextContent()
	if strings.Count(text, "```") != 2 {
		t.Fatalf("unterminated fence was not balanced: %q", text)
	}
	if !strings.Contains(text, "[Response interrupted by user]") {
		t.Fatalf("interruption marker missing: %q", text)
	}
	display := strings.Join(updated.Messages(), "\n")
	if strings.Contains(display, "```") {
		t.Fatalf("interrupted display leaked Markdown fence: %q", display)
	}
}

func TestStopAndCtrlCCommitEquivalentTurns(t *testing.T) {
	makeModel := func(t *testing.T) *Model {
		m := newTUIRegressionModel(t)
		m.state = StateStreaming
		m.aiService.SetRequestID("request")
		m.sessionService.CurrentSession().SetMessages([]session.Message{session.UserText("question")})
		m.activeCell.AppendText("partial")
		return m
	}

	stopModel, _ := makeModel(t).stopExecution()
	stopped := stopModel.(Model)
	ctrlModel, _ := makeModel(t).Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	ctrl := ctrlModel.(Model)
	if stopped.state != StateIdle || ctrl.state != StateIdle || !stopped.interrupted || !ctrl.interrupted {
		t.Fatal("Stop and Ctrl+C should both end in the interrupted Idle state")
	}
	if stopped.session.Metadata.TurnCount != 1 || ctrl.session.Metadata.TurnCount != 1 {
		t.Fatal("Stop and Ctrl+C should each commit exactly one completed turn")
	}
	stopMessages := stopped.session.GetMessages()
	ctrlMessages := ctrl.session.GetMessages()
	if len(stopMessages) != len(ctrlMessages) || len(stopMessages) != 2 {
		t.Fatalf("committed history differs: Stop=%d Ctrl+C=%d", len(stopMessages), len(ctrlMessages))
	}
	if stopMessages[1].Role != ctrlMessages[1].Role || stopMessages[1].TextContent() != ctrlMessages[1].TextContent() {
		t.Fatal("Stop and Ctrl+C should persist the same assistant interruption message")
	}
}

func TestStopDuringToolExecutionClosesToolUse(t *testing.T) {
	m := newTUIRegressionModel(t)
	toolUse := session.ContentBlock{Type: "tool_use", ID: "tool-1", Name: "shell", Input: "pwd"}
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText("run it"),
		session.AssistantWithTools([]session.ContentBlock{toolUse}, "test-model", nil),
	})
	m.toolService.SetPendingTools([]session.ContentBlock{toolUse})
	m.state = StateToolExec
	m.aiService.SetRequestID("tool-request")
	cancelled := false
	m.toolService.SetToolCancel(func() { cancelled = true })

	updatedModel, _ := m.stopExecution()
	updated := updatedModel.(Model)
	messages := updated.session.GetMessages()
	if !cancelled || len(messages) != 4 {
		t.Fatalf("tool Stop should cancel execution and close history, cancelled=%v messages=%d", cancelled, len(messages))
	}
	result := messages[2]
	if result.Role != session.RoleTool || len(result.Blocks) != 1 || result.Blocks[0].Type != "tool_result" ||
		result.Blocks[0].ToolUseID != "tool-1" || !result.Blocks[0].IsError {
		t.Fatalf("tool_use must receive a matching cancelled tool_result: %#v", result)
	}
	if messages[3].Role != session.RoleAssistant || !strings.Contains(messages[3].TextContent(), "interrupted") {
		t.Fatal("tool cancellation should finish with an assistant interruption message")
	}
}

func TestNewPromptAfterStopStartsFreshRequest(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("old-request")
	m.sessionService.CurrentSession().SetMessages([]session.Message{session.UserText("old question")})
	stoppedModel, _ := m.stopExecution()
	stopped := stoppedModel.(Model)
	stopped.reassignServices()
	stopped.textarea.SetValue("你好")

	nextModel, cmd := stopped.sendInput()
	next := nextModel.(Model)
	if cmd == nil || next.state != StateStreaming || next.requestID == "" || next.requestID == "old-request" {
		t.Fatalf("new input should start a fresh request, state=%d requestID=%q", next.state, next.requestID)
	}
	if next.interrupted {
		t.Fatal("new request must clear the prior interruption marker")
	}
	messages := next.session.GetMessages()
	if len(messages) != 3 || messages[2].Role != session.RoleUser || messages[2].TextContent() != "你好" {
		t.Fatalf("new input was not appended after the completed stopped turn: %#v", messages)
	}

	updatedModel, staleCmd := next.Update(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "old response"}},
		requestID: "old-request",
	})
	updated := updatedModel.(Model)
	if staleCmd != nil || updated.state != StateStreaming || len(updated.session.GetMessages()) != 3 {
		t.Fatal("late completion from the stopped request should be ignored")
	}
}

func TestCtrlCStopKeepsTUIAndInputHistoryAvailable(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("active-request")
	m.sessionService.SetInputHistory([]string{"first question", "second question"})
	m.sessionService.SetHistoryIdx(2)
	m.textarea.Focus()

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := updatedModel.(Model)
	// The default regression model does not request temporary mouse capture,
	// so restoring native selection may legitimately return no command.
	_ = cmd
	if updated.state != StateIdle || updated.sessionService.Quitting() {
		t.Fatal("Ctrl+C during a turn must return to the TUI idle state")
	}
	if strings.Contains(updated.View(), "Session saved. Goodbye!") {
		t.Fatal("stopping a turn must not render the TUI exit view")
	}
	if !updated.textarea.Focused() {
		t.Fatal("stopping a turn must keep the composer focused")
	}

	historyModel, _ := updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	history := historyModel.(Model)
	if got := history.textarea.Value(); got != "second question" {
		t.Fatalf("input history should remain navigable after Ctrl+C, got %q", got)
	}
}

func TestStopPreservesPriorTranscriptAfterNextTurn(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.ready = true
	m.width = 100
	m.height = 30
	m.viewport.Width = 96
	m.viewport.Height = 8
	priorTranscript := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		priorTranscript = append(priorTranscript, fmt.Sprintf("prior message %02d", i))
	}
	priorTranscript[0] = "prior user"
	priorTranscript[1] = "prior assistant"
	m.messages.SetAll(priorTranscript)
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()

	m.state = StateStreaming
	m.aiService.SetRequestID("old-request")
	m.activeCell.AppendText("partial old response")

	updatedModel, _ := m.stopExecution()
	stopped := updatedModel.(Model)
	if got := strings.Join(stopped.Messages(), "\n"); !strings.Contains(got, "prior user") || !strings.Contains(got, "prior assistant") {
		t.Fatalf("stopping a turn must retain the existing transcript: %q", got)
	}

	stopped.reassignServices()
	stopped.textarea.SetValue("new question")
	updatedModel, cmd := stopped.sendInput()
	if cmd == nil {
		t.Fatal("the next turn should start a stream")
	}
	stopped = updatedModel.(Model)
	stopped.reassignServices()
	stopped.aiService.SetRequestID("new-request")
	updatedModel, _ = stopped.Update(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "new response"}},
		model:     "test-model",
		usage:     &session.TokenUsage{},
		requestID: "new-request",
	})
	updated := updatedModel.(Model)
	transcript := strings.Join(updated.Messages(), "\n")
	for _, want := range []string{"prior user", "prior assistant", "partial old response", "new"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript after the next turn is missing %q: %q", want, transcript)
		}
	}
	updated.viewport.GotoBottom()
	before := updated.viewport.YOffset
	historyModel, _ := updated.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	history := historyModel.(Model)
	if history.viewport.YOffset >= before {
		t.Fatalf("conversation history should remain scrollable after an interrupted turn: before=%d after=%d", before, history.viewport.YOffset)
	}
}

func TestStopPreservesInputHistoryForSubsequentTurns(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("old-request")
	m.sessionService.SetInputHistory([]string{"first question"})
	m.sessionService.SetHistoryIdx(1)
	m.activeCell.AppendText("partial")

	updatedModel, _ := m.stopExecution()
	stopped := updatedModel.(Model)
	stopped.reassignServices()
	stopped.textarea.SetValue("second question")
	updatedModel, cmd := stopped.sendInput()
	if cmd == nil {
		t.Fatal("the subsequent turn should start a stream")
	}
	updated := updatedModel.(Model)
	updated.reassignServices()
	updated.textarea.Reset()
	updated.sessionService.SetHistoryIdx(len(updated.sessionService.InputHistory()))

	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = updatedModel.(Model)
	if got := updated.textarea.Value(); got != "second question" {
		t.Fatalf("latest input should remain available after an interrupted turn, got %q", got)
	}
	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = updatedModel.(Model)
	if got := updated.textarea.Value(); got != "first question" {
		t.Fatalf("prior input should remain available after an interrupted turn, got %q", got)
	}
}
