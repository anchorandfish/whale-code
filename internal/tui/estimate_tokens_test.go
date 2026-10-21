package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestEstimateCurrentTokens_TextOnly tests token estimation for pure text messages
func TestEstimateCurrentTokens_TextOnly(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// 1000 characters of text should estimate to ~250 tokens
	text := strings.Repeat("a", 1000)
	runner.Session.SetMessages(append(runner.Session.GetMessages(), session.UserText(text)))

	tokens := runner.estimateCurrentTokens()
	expected := 250 // 1000 / 4 = 250
	if tokens != expected {
		t.Errorf("expected ~%d tokens for 1000 chars, got %d", expected, tokens)
	}
}

// TestEstimateCurrentTokens_WithImage tests token estimation for messages with base64 image + text
func TestEstimateCurrentTokens_WithImage(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// Add a message with text + base64 image
	text := strings.Repeat("b", 400) // 400/4 = 100 tokens
	// "iVBORw0KGgo=" has 12 chars, token estimation: ceil(12 * 0.125) = 2
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.UserWithImages(text, []session.ImageBlock{
			{MediaType: "image/png", Base64Data: "iVBORw0KGgo="},
		}),
	))

	tokens := runner.estimateCurrentTokens()
	// text tokens (100) + image tokens (ceil(12*0.125)=2) = 102
	expected := 100 + 2
	if tokens != expected {
		t.Errorf("expected %d tokens (text + image), got %d", expected, tokens)
	}
}

// TestEstimateCurrentTokens_MultipleImages tests token estimation for multiple images
func TestEstimateCurrentTokens_MultipleImages(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// Add a message with 3 base64 images
	// Each "dataX" has 5 chars, token estimation: ceil(5 * 0.125) = 1 per image
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.UserWithImages("", []session.ImageBlock{
			{MediaType: "image/png", Base64Data: "data1"},
			{MediaType: "image/png", Base64Data: "data2"},
			{MediaType: "image/jpeg", Base64Data: "data3"},
		}),
	))

	tokens := runner.estimateCurrentTokens()
	// 3 images × ceil(5*0.125)=1 = 3
	expected := 3 * 1
	if tokens != expected {
		t.Errorf("expected %d tokens (3 images), got %d", expected, tokens)
	}
}

// TestEstimateCurrentTokens_ToolResult tests token estimation for tool results with long output
func TestEstimateCurrentTokens_ToolResult(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// Add a tool result message with long output
	output := strings.Repeat("x", 2000) // 2000/4 = 500 tokens
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.ToolResult("tool_001", "read_file", output, false),
	))

	tokens := runner.estimateCurrentTokens()
	expected := 500 // 2000 / 4 = 500
	if tokens != expected {
		t.Errorf("expected ~%d tokens for tool result, got %d", expected, tokens)
	}
}

// TestEstimateCurrentTokens_EmptySession tests that empty session has 0 tokens
func TestEstimateCurrentTokens_EmptySession(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	tokens := runner.estimateCurrentTokens()
	if tokens != 0 {
		t.Errorf("expected 0 tokens for empty session, got %d", tokens)
	}
}

// TestEstimateCurrentTokens_URLImage tests token estimation for URL-referenced images (no base64)
// N8 改造：空 base64 数据返回 0 token（G14 删除不可达 URL 分支）
func TestEstimateCurrentTokens_URLImage(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// Add a message with a URL-referenced image (no Base64Data)
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.UserWithImages("", []session.ImageBlock{
			{MediaType: "image/png", Base64Data: ""}, // URL image, no base64 data
		}),
	))

	tokens := runner.estimateCurrentTokens()
	// N8 改造：空 base64 数据返回 0 token（不再固定 85）
	expected := 0
	if tokens != expected {
		t.Errorf("expected %d tokens for URL image (empty base64), got %d", expected, tokens)
	}
}

// TestEstimateCurrentTokens_ToolUse tests token estimation for tool_use blocks
func TestEstimateCurrentTokens_ToolUse(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// Add an assistant message with a tool_use block
	text := strings.Repeat("t", 200)  // 200/4 = 50 tokens
	input := strings.Repeat("i", 400) // 400/4 = 100 tokens
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.AssistantWithTools([]session.ContentBlock{
			{Type: "tool_use", Text: text, Input: input, Name: "read_file", ID: "tool_001"},
		}, "test-model", nil),
	))

	tokens := runner.estimateCurrentTokens()
	expected := 50 + 100 // text/4 + input/4
	if tokens != expected {
		t.Errorf("expected %d tokens for tool_use, got %d", expected, tokens)
	}
}

// TestEstimateCurrentTokens_ThinkingBlock tests token estimation for thinking blocks
func TestEstimateCurrentTokens_ThinkingBlock(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// Add a message with a thinking block
	thinking := strings.Repeat("z", 800) // 800/4 = 200 tokens
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.Message{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "thinking", Text: thinking},
			},
		},
	))

	tokens := runner.estimateCurrentTokens()
	expected := 200
	if tokens != expected {
		t.Errorf("expected %d tokens for thinking block, got %d", expected, tokens)
	}
}
