package command

import (
	"strings"
	"testing"
)

// mockUsageTracker implements UsageTrackerInterface for testing
type mockUsageTracker struct {
	sessionStats SessionUsageStats
	totalStats   TotalUsageStats
}

func (m *mockUsageTracker) GetSessionStats(sessionName string) SessionUsageStats {
	return m.sessionStats
}

func (m *mockUsageTracker) GetTotalStats() TotalUsageStats {
	return m.totalStats
}

// mockSession implements SessionInterface for testing
type mockSession struct {
	messages []Message
}

func (m *mockSession) GetMessages() []Message {
	return m.messages
}

// TestHandleUsageWithData tests /usage with UsageTracker data
func TestHandleUsageWithData(t *testing.T) {
	r := NewRegistry()
	r.UsageTracker = &mockUsageTracker{
		sessionStats: SessionUsageStats{
			InputTokens:      1000,
			OutputTokens:     500,
			CacheReadTokens:  200,
			CacheWriteTokens: 100,
		},
	}
	r.SessionName = "test-session"

	result := r.handleUsage()

	if !strings.Contains(result, "test-session") {
		t.Errorf("expected session name in output, got: %s", result)
	}
	if !strings.Contains(result, "Input tokens:  1000") {
		t.Errorf("expected input tokens in output, got: %s", result)
	}
	if !strings.Contains(result, "Output tokens: 500") {
		t.Errorf("expected output tokens in output, got: %s", result)
	}
	if !strings.Contains(result, "Cache read:    200") {
		t.Errorf("expected cache read tokens in output, got: %s", result)
	}
	if !strings.Contains(result, "Cache write:   100") {
		t.Errorf("expected cache write tokens in output, got: %s", result)
	}
	// 本地估算费用已移除：provider 不返回费用详情，本地价格表估算与真实账单不符。
	if strings.Contains(result, "cost") {
		t.Errorf("expected no local cost estimate in output, got: %s", result)
	}
}

// TestHandleUsageNoData tests /usage when UsageTracker is nil
func TestHandleUsageNoData(t *testing.T) {
	r := NewRegistry()
	r.UsageTracker = nil

	result := r.handleUsage()

	if result != "Usage tracking not available" {
		t.Errorf("expected 'Usage tracking not available', got: %s", result)
	}
}

// TestHandleStatsNormal tests /stats with both session and usage data
func TestHandleStatsNormal(t *testing.T) {
	r := NewRegistry()
	r.UsageTracker = &mockUsageTracker{
		totalStats: TotalUsageStats{
			Calls: 42,
		},
	}
	r.Session = &mockSession{
		messages: []Message{
			{Role: "user"},
			{Role: "assistant"},
			{Role: "user"},
			{Role: "assistant"},
			{Role: "user"},
		},
	}

	result := r.handleStats()

	if !strings.Contains(result, "Session statistics:") {
		t.Errorf("expected header in output, got: %s", result)
	}
	if !strings.Contains(result, "Total API calls: 42") {
		t.Errorf("expected API calls in output, got: %s", result)
	}
	if strings.Contains(result, "cost") {
		t.Errorf("expected no local cost estimate in output, got: %s", result)
	}
	if !strings.Contains(result, "Messages: 5") {
		t.Errorf("expected message count in output, got: %s", result)
	}
	if !strings.Contains(result, "Turns: 3") {
		t.Errorf("expected turn count in output, got: %s", result)
	}
}

// TestHandleSummary tests /summary triggers AI summary generation
func TestHandleSummary(t *testing.T) {
	r := NewRegistry()

	result := r.handleSummary()

	// B7-P1-1 修复：桩层未接线 OnSummary 时返回诚实降级提示，不再返回假成功的 "Generating summary..."
	if result != "Summary not available in this mode (OnSummary callback not wired)" {
		t.Errorf("expected honest fallback, got: %s", result)
	}
}

// TestHandleOnboarding tests /onboarding displays guide text
func TestHandleOnboarding(t *testing.T) {
	r := NewRegistry()

	result := r.handleOnboarding()

	if !strings.Contains(result, "Welcome to whale-code!") {
		t.Errorf("expected welcome text in output, got: %s", result)
	}
	if !strings.Contains(result, "/help") {
		t.Errorf("expected /help reference in output, got: %s", result)
	}
	if !strings.Contains(result, "/config") {
		t.Errorf("expected /config reference in output, got: %s", result)
	}
	if !strings.Contains(result, "/model") {
		t.Errorf("expected /model reference in output, got: %s", result)
	}
}

// TestHandleShareWithExport tests /share with OnExport callback
func TestHandleShareWithExport(t *testing.T) {
	r := NewRegistry()
	r.OnExport = func(args string) string {
		return "Exported as markdown: conversation.md"
	}

	result := r.handleShare("")

	if result != "Exported as markdown: conversation.md" {
		t.Errorf("expected export result, got: %s", result)
	}
}

// TestHandleShareWithoutExport tests /share without OnExport callback
func TestHandleShareWithoutExport(t *testing.T) {
	r := NewRegistry()
	r.OnExport = nil

	result := r.handleShare("")

	if !strings.Contains(result, "Export the conversation first") {
		t.Errorf("expected fallback message, got: %s", result)
	}
}

// TestCountTurns tests the countTurns helper function
func TestCountTurns(t *testing.T) {
	tests := []struct {
		name     string
		msgs     []Message
		expected int
	}{
		{"empty", []Message{}, 0},
		{"no user messages", []Message{{Role: "assistant"}, {Role: "tool"}}, 0},
		{"mixed messages", []Message{
			{Role: "user"},
			{Role: "assistant"},
			{Role: "user"},
			{Role: "assistant"},
			{Role: "user"},
		}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countTurns(tt.msgs)
			if got != tt.expected {
				t.Errorf("countTurns() = %d, want %d", got, tt.expected)
			}
		})
	}
}
