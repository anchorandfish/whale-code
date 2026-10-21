package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// --- Test helpers ---

// mockOutputHandler records all handler calls for verification
type mockOutputHandler struct {
	texts     []string
	thinkings []string
	toolUses  []struct {
		name  string
		input json.RawMessage
	}
	toolResults []string
	errors      []error
}

func (m *mockOutputHandler) OnText(text string) {
	m.texts = append(m.texts, text)
}

func (m *mockOutputHandler) OnThinking(text string) {
	m.thinkings = append(m.thinkings, text)
}

func (m *mockOutputHandler) OnToolUse(name string, input json.RawMessage) {
	m.toolUses = append(m.toolUses, struct {
		name  string
		input json.RawMessage
	}{name: name, input: input})
}

func (m *mockOutputHandler) OnToolResult(result string) {
	m.toolResults = append(m.toolResults, result)
}

func (m *mockOutputHandler) OnError(err error) {
	m.errors = append(m.errors, err)
}

// eventChannel creates a channel from a slice of events and closes it
func eventChannel(events []ai.Event) <-chan ai.Event {
	ch := make(chan ai.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch
}

// --- Test cases ---

// TestParseStreamEventsText verifies that text events are correctly parsed
func TestParseStreamEventsText(t *testing.T) {
	core := newTestRunnerCore()
	events := []ai.Event{
		ai.TextDeltaEvent{Content: "Hello "},
		ai.TextDeltaEvent{Content: "World"},
	}

	parsed := core.ParseStreamEvents(events)

	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed events, got %d", len(parsed))
	}
	if parsed[0].Type != "text" {
		t.Errorf("expected type 'text', got %q", parsed[0].Type)
	}
	if parsed[0].Content != "Hello " {
		t.Errorf("expected content 'Hello ', got %q", parsed[0].Content)
	}
	if parsed[1].Content != "World" {
		t.Errorf("expected content 'World', got %q", parsed[1].Content)
	}
}

// TestParseStreamEventsThinking verifies that thinking events are correctly parsed
func TestParseStreamEventsThinking(t *testing.T) {
	core := newTestRunnerCore()
	events := []ai.Event{
		ai.ThinkingDeltaEvent{Content: "Let me think..."},
	}

	parsed := core.ParseStreamEvents(events)

	if len(parsed) != 1 {
		t.Fatalf("expected 1 parsed event, got %d", len(parsed))
	}
	if parsed[0].Type != "thinking" {
		t.Errorf("expected type 'thinking', got %q", parsed[0].Type)
	}
	if parsed[0].Content != "Let me think..." {
		t.Errorf("expected content 'Let me think...', got %q", parsed[0].Content)
	}
}

// TestParseStreamEventsToolUse verifies that tool_use events are correctly parsed
// with tool name and JSON input
func TestParseStreamEventsToolUse(t *testing.T) {
	core := newTestRunnerCore()
	events := []ai.Event{
		ai.ToolUseEvent{ID: "tool_1", Name: "read_file", Input: `{"path":"/tmp/test.go"}`},
	}

	parsed := core.ParseStreamEvents(events)

	if len(parsed) != 1 {
		t.Fatalf("expected 1 parsed event, got %d", len(parsed))
	}
	if parsed[0].Type != "tool_use" {
		t.Errorf("expected type 'tool_use', got %q", parsed[0].Type)
	}
	if parsed[0].ToolName != "read_file" {
		t.Errorf("expected tool name 'read_file', got %q", parsed[0].ToolName)
	}
	if string(parsed[0].ToolInput) != `{"path":"/tmp/test.go"}` {
		t.Errorf("expected tool input '{\"path\":\"/tmp/test.go\"}', got %q", string(parsed[0].ToolInput))
	}
}

// TestParseStreamEventsError verifies that error events are correctly parsed
func TestParseStreamEventsError(t *testing.T) {
	core := newTestRunnerCore()
	testErr := fmt.Errorf("stream error")
	events := []ai.Event{
		ai.ErrorEvent{Err: testErr},
	}

	parsed := core.ParseStreamEvents(events)

	if len(parsed) != 1 {
		t.Fatalf("expected 1 parsed event, got %d", len(parsed))
	}
	if parsed[0].Type != "error" {
		t.Errorf("expected type 'error', got %q", parsed[0].Type)
	}
	if parsed[0].Err == nil {
		t.Fatal("expected non-nil error")
	}
	if parsed[0].Err.Error() != "stream error" {
		t.Errorf("expected error 'stream error', got %q", parsed[0].Err.Error())
	}
}

// TestParseStreamEventsMixed verifies parsing a mix of event types
func TestParseStreamEventsMixed(t *testing.T) {
	core := newTestRunnerCore()
	events := []ai.Event{
		ai.TextDeltaEvent{Content: "I'll read the file. "},
		ai.ThinkingDeltaEvent{Content: "User wants to see test.go"},
		ai.ToolUseEvent{ID: "t1", Name: "read_file", Input: `{"path":"test.go"}`},
		ai.UsageEvent{InputTokens: 100, OutputTokens: 50},
		ai.TextDeltaEvent{Content: "Done."},
	}

	parsed := core.ParseStreamEvents(events)

	if len(parsed) != 5 {
		t.Fatalf("expected 5 parsed events, got %d", len(parsed))
	}

	// Verify types
	expectedTypes := []string{"text", "thinking", "tool_use", "usage", "text"}
	for i, expected := range expectedTypes {
		if parsed[i].Type != expected {
			t.Errorf("event %d: expected type %q, got %q", i, expected, parsed[i].Type)
		}
	}
}

// TestProcessStreamEventsPlainText verifies that processStreamEvents correctly
// dispatches text events to the handler and collects response text
func TestProcessStreamEventsPlainText(t *testing.T) {
	core := newTestRunnerCore()
	events := eventChannel([]ai.Event{
		ai.TextDeltaEvent{Content: "Hello "},
		ai.TextDeltaEvent{Content: "World"},
	})

	handler := &mockOutputHandler{}
	responseText, _, _, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responseText != "Hello World" {
		t.Errorf("expected response text 'Hello World', got %q", responseText)
	}
	if len(handler.texts) != 2 {
		t.Fatalf("expected 2 OnText calls, got %d", len(handler.texts))
	}
	if handler.texts[0] != "Hello " {
		t.Errorf("expected first text 'Hello ', got %q", handler.texts[0])
	}
	if handler.texts[1] != "World" {
		t.Errorf("expected second text 'World', got %q", handler.texts[1])
	}
}

// TestProcessStreamEventsToolUse verifies that tool_use events are dispatched
// to the handler with correct tool name and input
func TestProcessStreamEventsToolUse(t *testing.T) {
	core := newTestRunnerCore()
	events := eventChannel([]ai.Event{
		ai.ToolUseEvent{ID: "t1", Name: "read_file", Input: `{"path":"test.go"}`},
	})

	handler := &mockOutputHandler{}
	_, _, pendingTools, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pendingTools) != 1 {
		t.Fatalf("expected 1 pending tool, got %d", len(pendingTools))
	}
	if pendingTools[0].Name != "read_file" {
		t.Errorf("expected tool name 'read_file', got %q", pendingTools[0].Name)
	}
	if pendingTools[0].ID != "t1" {
		t.Errorf("expected tool ID 't1', got %q", pendingTools[0].ID)
	}
	if len(handler.toolUses) != 1 {
		t.Fatalf("expected 1 OnToolUse call, got %d", len(handler.toolUses))
	}
	if handler.toolUses[0].name != "read_file" {
		t.Errorf("expected tool name 'read_file', got %q", handler.toolUses[0].name)
	}
}

// TestProcessStreamEventsError verifies that error events are properly handled
func TestProcessStreamEventsError(t *testing.T) {
	core := newTestRunnerCore()
	testErr := fmt.Errorf("stream error")
	events := eventChannel([]ai.Event{
		ai.TextDeltaEvent{Content: "partial"},
		ai.ErrorEvent{Err: testErr},
	})

	handler := &mockOutputHandler{}
	responseText, _, _, err := core.processStreamEvents(context.Background(), events, handler)

	if err == nil {
		t.Fatal("expected error from processStreamEvents")
	}
	if err.Error() != "stream error" {
		t.Errorf("expected error 'stream error', got %q", err.Error())
	}
	if responseText != "partial" {
		t.Errorf("expected partial response text 'partial', got %q", responseText)
	}
	if len(handler.errors) != 1 {
		t.Fatalf("expected 1 OnError call, got %d", len(handler.errors))
	}
}

// TestProcessStreamEventsToolInputAccumulation verifies that tool input
// is correctly accumulated from ToolUseEvent and ToolInputDeltaEvent
func TestProcessStreamEventsToolInputAccumulation(t *testing.T) {
	core := newTestRunnerCore()
	events := eventChannel([]ai.Event{
		ai.ToolUseEvent{ID: "t1", Name: "write_file", Input: ""},
		ai.ToolInputDeltaEvent{PartialJSON: `{"path":"te`},
		ai.ToolInputDeltaEvent{PartialJSON: `st.go"}`},
	})

	handler := &mockOutputHandler{}
	_, _, pendingTools, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pendingTools) != 1 {
		t.Fatalf("expected 1 pending tool, got %d", len(pendingTools))
	}
	expectedInput := `{"path":"test.go"}`
	if pendingTools[0].Input != expectedInput {
		t.Errorf("expected tool input %q, got %q", expectedInput, pendingTools[0].Input)
	}
}

// TestProcessStreamEventsThinking verifies that thinking events are dispatched
func TestProcessStreamEventsThinking(t *testing.T) {
	core := newTestRunnerCore()
	events := eventChannel([]ai.Event{
		ai.ThinkingDeltaEvent{Content: "analyzing..."},
		ai.TextDeltaEvent{Content: "result"},
	})

	handler := &mockOutputHandler{}
	_, thinkingText, _, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thinkingText != "analyzing..." {
		t.Errorf("expected thinking text 'analyzing...', got %q", thinkingText)
	}
	if len(handler.thinkings) != 1 {
		t.Fatalf("expected 1 OnThinking call, got %d", len(handler.thinkings))
	}
}

// TestProcessStreamEventsUsageTracking verifies that usage events update token counts
func TestProcessStreamEventsUsageTracking(t *testing.T) {
	core := newTestRunnerCore()
	events := eventChannel([]ai.Event{
		ai.UsageEvent{InputTokens: 100, OutputTokens: 50},
		ai.UsageEvent{InputTokens: 200, OutputTokens: 100},
	})

	handler := &mockOutputHandler{}
	_, _, _, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Usage fields are cumulative snapshots within one request. Repeated or
	// split provider frames must be merged without double counting.
	if core.InputTokens != 200 {
		t.Errorf("expected cumulative InputTokens 200, got %d", core.InputTokens)
	}
	if core.OutputTokens != 100 {
		t.Errorf("expected OutputTokens 100, got %d", core.OutputTokens)
	}
	if core.LastInputTokens != 200 || core.LastOutputTokens != 100 {
		t.Errorf("last usage = %d/%d, want 200/100", core.LastInputTokens, core.LastOutputTokens)
	}
}

// TestOutputHandlerInterfacePlain verifies plainTextHandler implements OutputHandler
func TestOutputHandlerInterfacePlain(t *testing.T) {
	var _ OutputHandler = (*plainTextHandler)(nil)
}

// TestOutputHandlerInterfaceBridge verifies bridgeStreamHandler implements OutputHandler
func TestOutputHandlerInterfaceBridge(t *testing.T) {
	var _ OutputHandler = (*bridgeStreamHandler)(nil)
}

// TestParsedStreamEventToolInputType verifies that ParsedStreamEvent.ToolInput
// is json.RawMessage (not string)
func TestParsedStreamEventToolInputType(t *testing.T) {
	event := ParsedStreamEvent{
		Type:      "tool_use",
		ToolName:  "read_file",
		ToolInput: json.RawMessage(`{"path":"/tmp/test.go"}`),
	}

	// Verify ToolInput can be unmarshaled
	var input map[string]string
	if err := json.Unmarshal(event.ToolInput, &input); err != nil {
		t.Fatalf("failed to unmarshal ToolInput: %v", err)
	}
	if input["path"] != "/tmp/test.go" {
		t.Errorf("expected path '/tmp/test.go', got %q", input["path"])
	}
}

// TestProcessStreamEventsMultipleTools verifies handling of multiple tool calls
func TestProcessStreamEventsMultipleTools(t *testing.T) {
	core := newTestRunnerCore()
	events := eventChannel([]ai.Event{
		ai.ToolUseEvent{ID: "t1", Name: "read_file", Input: `{"path":"a.go"}`},
		ai.ToolUseEvent{ID: "t2", Name: "write_file", Input: `{"path":"b.go"}`},
	})

	handler := &mockOutputHandler{}
	_, _, pendingTools, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pendingTools) != 2 {
		t.Fatalf("expected 2 pending tools, got %d", len(pendingTools))
	}

	names := []string{pendingTools[0].Name, pendingTools[1].Name}
	if names[0] != "read_file" || names[1] != "write_file" {
		t.Errorf("expected tool names [read_file, write_file], got %v", names)
	}

	// Verify tool inputs are populated
	for _, pt := range pendingTools {
		if pt.Input == "" {
			t.Errorf("tool %s should have non-empty input", pt.Name)
		}
		if !strings.Contains(pt.Input, "path") {
			t.Errorf("tool %s input should contain 'path', got %q", pt.Name, pt.Input)
		}
	}
}

// TestProcessStreamEventsSessionUpdate verifies that assistant message
// is correctly built from stream events (integration with session)
func TestProcessStreamEventsSessionUpdate(t *testing.T) {
	core := newTestRunnerCore()
	initialMsgCount := len(core.Session.GetMessages())

	events := eventChannel([]ai.Event{
		ai.TextDeltaEvent{Content: "Hello!"},
		ai.UsageEvent{InputTokens: 10, OutputTokens: 5},
	})

	handler := &mockOutputHandler{}
	responseText, _, pendingTools, err := core.processStreamEvents(context.Background(), events, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Build assistant message (mimicking what handleStreamEvents does)
	blocks := make([]session.ContentBlock, 0)
	if responseText != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: responseText})
	}
	blocks = append(blocks, pendingTools...)

	usage := &session.TokenUsage{
		InputTokens:  core.InputTokens,
		OutputTokens: core.OutputTokens,
	}
	assistantMsg := session.AssistantWithTools(blocks, core.ModelName, usage)
	core.Session.SetMessages(append(core.Session.GetMessages(), assistantMsg))

	if len(core.Session.GetMessages()) != initialMsgCount+1 {
		t.Fatalf("expected %d messages, got %d", initialMsgCount+1, len(core.Session.GetMessages()))
	}

	lastMsg := core.Session.GetMessages()[len(core.Session.GetMessages())-1]
	if lastMsg.Role != session.RoleAssistant {
		t.Errorf("expected assistant role, got %v", lastMsg.Role)
	}
}
