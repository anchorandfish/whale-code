package context

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestMessagesEqual_IdenticalMessages tests that identical messages are equal
func TestMessagesEqual_IdenticalMessages(t *testing.T) {
	a := session.Message{
		Role: session.RoleUser,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "hello"},
			{Type: "tool_use", ToolName: "read_file", Input: `{"path":"/tmp"}`},
		},
	}
	b := session.Message{
		Role: session.RoleUser,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "hello"},
			{Type: "tool_use", ToolName: "read_file", Input: `{"path":"/tmp"}`},
		},
	}
	if !messagesEqual(a, b) {
		t.Error("identical messages should be equal")
	}
}

// TestMessagesEqual_DifferentInput tests that messages with different Input are not equal
func TestMessagesEqual_DifferentInput(t *testing.T) {
	a := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "tool_use", ToolName: "read_file", Input: `{"path":"/a"}`},
		},
	}
	b := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "tool_use", ToolName: "read_file", Input: `{"path":"/b"}`},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different Input should not be equal")
	}
}

// TestMessagesEqual_DifferentToolUseID tests that messages with different ToolUseID are not equal
func TestMessagesEqual_DifferentToolUseID(t *testing.T) {
	a := session.Message{
		Role: session.RoleTool,
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "tool_001", Output: "result"},
		},
	}
	b := session.Message{
		Role: session.RoleTool,
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "tool_002", Output: "result"},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different ToolUseID should not be equal")
	}
}

// TestMessagesEqual_DifferentBase64Data tests that messages with different Base64Data are not equal
func TestMessagesEqual_DifferentBase64Data(t *testing.T) {
	a := session.Message{
		Role: session.RoleUser,
		Blocks: []session.ContentBlock{
			{Type: "image", MediaType: "image/png", Base64Data: "iVBORw0KGgo="},
		},
	}
	b := session.Message{
		Role: session.RoleUser,
		Blocks: []session.ContentBlock{
			{Type: "image", MediaType: "image/png", Base64Data: "R0lGODlhAQAB"},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different Base64Data should not be equal")
	}
}

// TestMessagesEqual_EmptyMessages tests that two empty messages are equal
func TestMessagesEqual_EmptyMessages(t *testing.T) {
	a := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{},
	}
	b := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{},
	}
	if !messagesEqual(a, b) {
		t.Error("two empty messages with same role should be equal")
	}
}

// TestMessagesEqual_DifferentRole tests that messages with different roles are not equal
func TestMessagesEqual_DifferentRole(t *testing.T) {
	a := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}},
	}
	b := session.Message{
		Role:   session.RoleAssistant,
		Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different roles should not be equal")
	}
}

// TestMessagesEqual_DifferentBlockCount tests that messages with different block counts are not equal
func TestMessagesEqual_DifferentBlockCount(t *testing.T) {
	a := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}},
	}
	b := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}, {Type: "text", Text: "there"}},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different block counts should not be equal")
	}
}

// TestMessagesEqual_DifferentMediaType tests that messages with different MediaType are not equal
func TestMessagesEqual_DifferentMediaType(t *testing.T) {
	a := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "image", MediaType: "image/png", Base64Data: "abc"}},
	}
	b := session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "image", MediaType: "image/jpeg", Base64Data: "abc"}},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different MediaType should not be equal")
	}
}

// TestMessagesEqual_DifferentToolName tests that messages with different ToolName are not equal
func TestMessagesEqual_DifferentToolName(t *testing.T) {
	a := session.Message{
		Role: session.RoleTool,
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "tool_001", ToolName: "read_file", Output: "result"},
		},
	}
	b := session.Message{
		Role: session.RoleTool,
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "tool_001", ToolName: "write_file", Output: "result"},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different ToolName should not be equal")
	}
}

// TestMessagesEqual_DifferentThinking tests that messages with different Thinking are not equal
func TestMessagesEqual_DifferentThinking(t *testing.T) {
	a := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "thinking", Thinking: "let me think about this"},
		},
	}
	b := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "thinking", Thinking: "different thought"},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different Thinking should not be equal")
	}
}

// TestMessagesEqual_DifferentToolUseBlockID tests that tool_use blocks with different ID are not equal
func TestMessagesEqual_DifferentToolUseBlockID(t *testing.T) {
	a := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "tool_use", ID: "toolu_001", Name: "bash", Input: `{"command":"ls"}`},
		},
	}
	b := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "tool_use", ID: "toolu_002", Name: "bash", Input: `{"command":"ls"}`},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different tool_use ID should not be equal")
	}
}

// TestMessagesEqual_DifferentToolUseName tests that tool_use blocks with different Name are not equal
func TestMessagesEqual_DifferentToolUseName(t *testing.T) {
	a := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "tool_use", ID: "toolu_001", Name: "bash", Input: `{"command":"ls"}`},
		},
	}
	b := session.Message{
		Role: session.RoleAssistant,
		Blocks: []session.ContentBlock{
			{Type: "tool_use", ID: "toolu_001", Name: "read_file", Input: `{"command":"ls"}`},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different tool_use Name should not be equal")
	}
}

// TestMessagesEqual_DifferentIsError tests that tool_result blocks with different IsError are not equal
func TestMessagesEqual_DifferentIsError(t *testing.T) {
	a := session.Message{
		Role: session.RoleTool,
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "tool_001", Output: "error", IsError: true},
		},
	}
	b := session.Message{
		Role: session.RoleTool,
		Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "tool_001", Output: "error", IsError: false},
		},
	}
	if messagesEqual(a, b) {
		t.Error("messages with different IsError should not be equal")
	}
}

// TestMessagesSliceEqual tests the slice comparison helper
func TestMessagesSliceEqual(t *testing.T) {
	t.Run("equal slices", func(t *testing.T) {
		a := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		}
		b := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		}
		if !messagesSliceEqual(a, b) {
			t.Error("equal message slices should be equal")
		}
	})

	t.Run("different length slices", func(t *testing.T) {
		a := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
		}
		b := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		}
		if messagesSliceEqual(a, b) {
			t.Error("slices with different lengths should not be equal")
		}
	})

	t.Run("different content slices", func(t *testing.T) {
		a := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
		}
		b := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "bye"}}},
		}
		if messagesSliceEqual(a, b) {
			t.Error("slices with different content should not be equal")
		}
	})
}
