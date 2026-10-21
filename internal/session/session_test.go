package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSession(t *testing.T) {
	s := NewSession()
	if s.Version != 1 {
		t.Errorf("Version = %d, want 1", s.Version)
	}
	if len(s.messages) != 0 {
		t.Errorf("Messages should be empty")
	}
	if s.Metadata.CreatedAt == 0 {
		t.Error("CreatedAt should not be 0")
	}
}

func TestUserText(t *testing.T) {
	msg := UserText("hello")
	if msg.Role != RoleUser {
		t.Errorf("Role = %s, want user", msg.Role)
	}
	if msg.TextContent() != "hello" {
		t.Errorf("TextContent = %s, want hello", msg.TextContent())
	}
}

func TestAssistantText(t *testing.T) {
	usage := &TokenUsage{InputTokens: 100, OutputTokens: 50}
	msg := AssistantText("response", "test-model", usage)
	if msg.Role != RoleAssistant {
		t.Errorf("Role = %s, want assistant", msg.Role)
	}
	if msg.Model != "test-model" {
		t.Errorf("Model = %s, want test-model", msg.Model)
	}
	if msg.Usage.InputTokens != 100 {
		t.Errorf("Usage.InputTokens = %d, want 100", msg.Usage.InputTokens)
	}
}

func TestToolResult(t *testing.T) {
	msg := ToolResult("tool-1", "bash", "output", false)
	if msg.Role != RoleTool {
		t.Errorf("Role = %s, want tool", msg.Role)
	}
	if msg.Blocks[0].ToolName != "bash" {
		t.Errorf("ToolName = %s, want bash", msg.Blocks[0].ToolName)
	}
	if msg.Blocks[0].IsError {
		t.Error("IsError should be false")
	}
}

func TestMessageTags(t *testing.T) {
	msg := UserText("test")
	msg.AddTag("important")
	if len(msg.Tags) != 1 || msg.Tags[0] != "important" {
		t.Error("AddTag failed")
	}

	msg.AddTag("important")
	if len(msg.Tags) != 1 {
		t.Error("重复添加标记应去重")
	}

	msg.RemoveTag("important")
	if len(msg.Tags) != 0 {
		t.Error("RemoveTag failed")
	}
}

func TestSessionIncrementTurn(t *testing.T) {
	s := NewSession()
	s.IncrementTurn()
	if s.Metadata.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", s.Metadata.TurnCount)
	}
}

func TestStoreAppendAndLoad(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "whale-test-store")
	defer os.RemoveAll(dir)

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-session"

	msg1 := UserText("hello")
	msg2 := AssistantText("hi there", "test-model", nil)

	if err := store.Append(sessionID, msg1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if err := store.Append(sessionID, msg2); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.messages) != 2 {
		t.Errorf("Messages count = %d, want 2", len(loaded.messages))
	}
	if loaded.messages[0].TextContent() != "hello" {
		t.Errorf("First message = %s, want hello", loaded.messages[0].TextContent())
	}
}

func TestStoreList(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "whale-test-list")
	defer os.RemoveAll(dir)

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	store.Append("session-1", UserText("msg1"))
	store.Append("session-2", UserText("msg2"))

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(summaries) != 2 {
		t.Errorf("List count = %d, want 2", len(summaries))
	}
}

func TestStoreDelete(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "whale-test-delete")
	defer os.RemoveAll(dir)

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	store.Append("to-delete", UserText("msg"))

	if !store.Exists("to-delete") {
		t.Error("Session should exist")
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if store.Exists("to-delete") {
		t.Error("Session should not exist after delete")
	}
}

func TestStoreRename(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "whale-test-rename")
	defer os.RemoveAll(dir)

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	store.Append("old-name", UserText("msg"))

	if err := store.Rename("old-name", "new-name"); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	if store.Exists("old-name") {
		t.Error("Old name should not exist")
	}
	if !store.Exists("new-name") {
		t.Error("New name should exist")
	}
}

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()
	if id1 == id2 {
		t.Error("GenerateID should produce unique IDs")
	}
}

func TestToolUses(t *testing.T) {
	msg := Message{
		Role: RoleAssistant,
		Blocks: []ContentBlock{
			{Type: "text", Text: "let me check"},
			{Type: "tool_use", ID: "1", Name: "bash", Input: `{"command":"ls"}`},
			{Type: "tool_use", ID: "2", Name: "read_file", Input: `{"path":"test.go"}`},
		},
	}

	tools := msg.ToolUses()
	if len(tools) != 2 {
		t.Errorf("ToolUses count = %d, want 2", len(tools))
	}
}

// TestAssistantWithTools 测试包含工具调用内容块的Assistant消息
func TestAssistantWithTools(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "text", Text: "我来帮你查看文件"},
		{Type: "tool_use", ID: "tool-1", Name: "bash", Input: `{"command":"ls -la"}`},
		{Type: "tool_use", ID: "tool-2", Name: "read_file", Input: `{"path":"main.go"}`},
	}

	usage := &TokenUsage{InputTokens: 200, OutputTokens: 150}
	msg := AssistantWithTools(blocks, "test-model", usage)

	// 验证角色
	if msg.Role != RoleAssistant {
		t.Errorf("Role = %s, expected assistant", msg.Role)
	}

	// 验证内容块数量
	if len(msg.Blocks) != 3 {
		t.Errorf("Blocks 数量 = %d, expected 3", len(msg.Blocks))
	}

	// 验证第一个内容块是文本
	if msg.Blocks[0].Type != "text" {
		t.Errorf("Blocks[0].Type = %s, expected text", msg.Blocks[0].Type)
	}
	if msg.Blocks[0].Text != "我来帮你查看文件" {
		t.Errorf("Blocks[0].Text = %s, expected '我来帮你查看文件'", msg.Blocks[0].Text)
	}

	// 验证工具调用内容块
	if msg.Blocks[1].Type != "tool_use" {
		t.Errorf("Blocks[1].Type = %s, expected tool_use", msg.Blocks[1].Type)
	}
	if msg.Blocks[1].Name != "bash" {
		t.Errorf("Blocks[1].Name = %s, expected bash", msg.Blocks[1].Name)
	}
	if msg.Blocks[1].ID != "tool-1" {
		t.Errorf("Blocks[1].ID = %s, expected tool-1", msg.Blocks[1].ID)
	}

	// 验证模型和用量
	if msg.Model != "test-model" {
		t.Errorf("Model = %s, expected test-model", msg.Model)
	}
	if msg.Usage.InputTokens != 200 {
		t.Errorf("Usage.InputTokens = %d, expected 200", msg.Usage.InputTokens)
	}

	// 验证 ToolUses 方法能正确提取工具调用
	toolUses := msg.ToolUses()
	if len(toolUses) != 2 {
		t.Errorf("ToolUses 数量 = %d, expected 2", len(toolUses))
	}
}

// TestTextContent 测试从混合内容中提取纯文本
func TestTextContent(t *testing.T) {
	// 测试纯文本消息
	textMsg := UserText("hello world")
	if textMsg.TextContent() != "hello world" {
		t.Errorf("纯文本 TextContent = %q, expected %q", textMsg.TextContent(), "hello world")
	}

	// 测试混合内容块消息
	mixedMsg := Message{
		Role: RoleAssistant,
		Blocks: []ContentBlock{
			{Type: "text", Text: "这是文本部分"},
			{Type: "tool_use", ID: "1", Name: "bash", Input: `{"command":"ls"}`},
			{Type: "text", Text: "这是另一段文本"},
			{Type: "tool_result", ToolUseID: "1", Output: "file1.txt"},
		},
	}

	text := mixedMsg.TextContent()
	if text != "这是文本部分这是另一段文本" {
		t.Errorf("混合内容 TextContent = %q, expected %q", text, "这是文本部分这是另一段文本")
	}

	// 测试无文本内容块的消息
	toolOnlyMsg := Message{
		Role: RoleTool,
		Blocks: []ContentBlock{
			{Type: "tool_result", ToolUseID: "1", Output: "result"},
		},
	}
	if toolOnlyMsg.TextContent() != "" {
		t.Errorf("无文本内容块 TextContent = %q, expected空字符串", toolOnlyMsg.TextContent())
	}

	// 测试空内容块
	emptyMsg := Message{Role: RoleUser, Blocks: []ContentBlock{}}
	if emptyMsg.TextContent() != "" {
		t.Errorf("空内容块 TextContent = %q, expected空字符串", emptyMsg.TextContent())
	}
}

// TestSession_ExportMarkdown 测试将 Session 导出为 Markdown 格式
// 创建包含User和Assistant消息的会话，导出为 Markdown，验证输出格式正确
func TestSession_ExportMarkdown(t *testing.T) {
	s := NewSession()

	// 添加User消息
	s.messages = append(s.messages, UserText("你好，请帮我写一个函数"))
	// 添加Assistant消息
	s.messages = append(s.messages, AssistantText("好的，这是一个示例函数", "test-model", nil))
	// 添加工具结果消息
	s.messages = append(s.messages, ToolResult("tool-1", "bash", "hello world", false))

	md := s.ExportMarkdown()

	// 验证包含 Markdown 标题
	if !strings.Contains(md, "# Whale Code Export") {
		t.Error("Markdown 导出should contain标题 '# Whale Code Export'")
	}

	// 验证包含User消息标题和内容
	if !strings.Contains(md, "## User") {
		t.Error("Markdown 导出should contain '## User' 标题")
	}
	if !strings.Contains(md, "你好，请帮我写一个函数") {
		t.Error("Markdown 导出should containUser消息内容")
	}

	// 验证包含Assistant消息标题和内容
	if !strings.Contains(md, "## Assistant") {
		t.Error("Markdown 导出should contain '## Assistant' 标题")
	}
	if !strings.Contains(md, "好的，这是一个示例函数") {
		t.Error("Markdown 导出should containAssistant消息内容")
	}

	// 验证包含工具结果标题和内容
	if !strings.Contains(md, "## Tool: bash") {
		t.Error("Markdown 导出should contain '## Tool: bash' 标题")
	}
	if !strings.Contains(md, "hello world") {
		t.Error("Markdown 导出should contain工具输出内容")
	}
	if !strings.Contains(md, "```") {
		t.Error("Markdown 导出的工具结果should contain代码块标记")
	}

	// 验证消息顺序：User 在 Assistant 之前
	userIdx := strings.Index(md, "## User")
	assistantIdx := strings.Index(md, "## Assistant")
	if userIdx >= assistantIdx {
		t.Error("Markdown 导出中 User 消息应在 Assistant 消息之前")
	}
}
