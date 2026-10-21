package mock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/mcp"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestMockAIClientNormalResponse tests MockAIClient returns preset responses
func TestMockAIClientNormalResponse(t *testing.T) {
	events := []ai.Event{
		ai.TextDeltaEvent{Content: "hello"},
		ai.TextDeltaEvent{Content: " world"},
		ai.UsageEvent{InputTokens: 10, OutputTokens: 5},
	}
	client := NewMockAIClient(events)

	ch, err := client.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var received []ai.Event
	for event := range ch {
		received = append(received, event)
	}

	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d", len(received))
	}
	if td, ok := received[0].(ai.TextDeltaEvent); !ok || td.Content != "hello" {
		t.Errorf("expected first event TextDeltaEvent{hello}, got %v", received[0])
	}
	if td, ok := received[1].(ai.TextDeltaEvent); !ok || td.Content != " world" {
		t.Errorf("expected second event TextDeltaEvent{ world}, got %v", received[1])
	}
	if ue, ok := received[2].(ai.UsageEvent); !ok || ue.InputTokens != 10 {
		t.Errorf("expected third event UsageEvent{10,5}, got %v", received[2])
	}

	if client.CallCount() != 1 {
		t.Errorf("expected call count 1, got %d", client.CallCount())
	}
}

// TestMockAIClientErrorSimulation tests MockAIClient returns preset errors
func TestMockAIClientErrorSimulation(t *testing.T) {
	client := NewMockAIClient(nil)
	client.Errors = []error{errors.New("connection timeout")}

	_, err := client.Stream(context.Background(), ai.Request{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "connection timeout" {
		t.Errorf("expected 'connection timeout', got '%s'", err.Error())
	}

	// Second call should succeed (no more preset errors)
	ch, err := client.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	// Channel should be closed immediately (no events)
	for range ch {
		t.Error("expected no events on second call")
	}

	if client.CallCount() != 2 {
		t.Errorf("expected call count 2, got %d", client.CallCount())
	}
}

// TestMockAIClientModel tests Model and SetModel methods
func TestMockAIClientModel(t *testing.T) {
	client := NewMockAIClient(nil)
	if client.Model() != "mock-model" {
		t.Errorf("expected default model 'mock-model', got '%s'", client.Model())
	}
	client.SetModel("gpt-4")
	if client.Model() != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got '%s'", client.Model())
	}
}

// TestMockAIClientReset tests the Reset method
func TestMockAIClientReset(t *testing.T) {
	client := NewMockAIClient(nil)
	client.Stream(context.Background(), ai.Request{})
	if client.CallCount() != 1 {
		t.Errorf("expected call count 1, got %d", client.CallCount())
	}
	client.Reset()
	if client.CallCount() != 0 {
		t.Errorf("expected call count 0 after reset, got %d", client.CallCount())
	}
}

// TestMockStreamBuilder tests the MockStreamBuilder fluent API
func TestMockStreamBuilder(t *testing.T) {
	events := NewMockStreamBuilder().
		AddText("hello").
		AddToolUse("tool-1", "bash", `{"command":"ls"}`).
		AddUsage(100, 50).
		AddError(errors.New("test error")).
		Build()

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	if td, ok := events[0].(ai.TextDeltaEvent); !ok || td.Content != "hello" {
		t.Errorf("expected TextDeltaEvent{hello}, got %v", events[0])
	}
	if tu, ok := events[1].(ai.ToolUseEvent); !ok || tu.ID != "tool-1" || tu.Name != "bash" {
		t.Errorf("expected ToolUseEvent{tool-1,bash}, got %v", events[1])
	}
	if ue, ok := events[2].(ai.UsageEvent); !ok || ue.InputTokens != 100 || ue.OutputTokens != 50 {
		t.Errorf("expected UsageEvent{100,50}, got %v", events[2])
	}
	if ee, ok := events[3].(ai.ErrorEvent); !ok || ee.Err.Error() != "test error" {
		t.Errorf("expected ErrorEvent{test error}, got %v", events[3])
	}
}

// TestMockStreamBuilderBuildClient tests BuildClient method
func TestMockStreamBuilderBuildClient(t *testing.T) {
	client := NewMockStreamBuilder().
		AddText("response").
		BuildClient()

	ch, err := client.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var received []ai.Event
	for event := range ch {
		received = append(received, event)
	}
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if td, ok := received[0].(ai.TextDeltaEvent); !ok || td.Content != "response" {
		t.Errorf("expected TextDeltaEvent{response}, got %v", received[0])
	}
}

// TestMockSessionStoreNormalOps tests normal session store operations
func TestMockSessionStoreNormalOps(t *testing.T) {
	store := NewMockSessionStore()

	// Save a session
	s := session.NewSession()
	s.Metadata.Name = "test-session"
	s.Metadata.TurnCount = 3
	if err := store.Save("sess-1", s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load the session
	loaded, err := store.Load("sess-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Metadata.Name != "test-session" {
		t.Errorf("expected name 'test-session', got '%s'", loaded.Metadata.Name)
	}

	// List sessions
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 session, got %d", len(summaries))
	}
	if summaries[0].ID != "sess-1" {
		t.Errorf("expected ID 'sess-1', got '%s'", summaries[0].ID)
	}

	// Delete session
	if err := store.Delete("sess-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, err = store.Load("sess-1")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestMockSessionStoreRewriteCAS(t *testing.T) {
	store := NewMockSessionStore()
	live := session.NewSession()
	live.AddMessage(session.UserText("before"))
	if err := store.Save("rewrite", live); err != nil {
		t.Fatal(err)
	}
	version, err := store.TranscriptVersion("rewrite")
	if err != nil {
		t.Fatal(err)
	}
	_, _, revision, _ := live.Snapshot()
	candidate := session.NewSession()
	candidate.AddMessage(session.UserText("after"))
	if err := store.Rewrite("rewrite", version, live, revision, candidate); err != nil {
		t.Fatalf("Rewrite failed: %v", err)
	}
	if got := live.GetMessages(); len(got) != 1 || got[0].TextContent() != "after" {
		t.Fatalf("live session not replaced: %#v", got)
	}
	if err := store.Rewrite("rewrite", version, live, revision, candidate); !errors.Is(err, session.ErrSessionRewriteConflict) {
		t.Fatalf("stale Rewrite error = %v, want conflict", err)
	}
}

// TestMockSessionStoreAppendReturnsError tests that Append returns injected error
func TestMockSessionStoreAppendReturnsError(t *testing.T) {
	injectedErr := errors.New("disk full")
	store := NewMockSessionStore()
	store.Err = injectedErr

	if err := store.Append("x", session.Message{}); err != injectedErr {
		t.Errorf("expected injected error on Append, got %v", err)
	}
}

// TestMockSessionStoreErrorInjection tests error injection for all methods
func TestMockSessionStoreErrorInjection(t *testing.T) {
	injectedErr := errors.New("disk full")
	store := NewMockSessionStore()
	store.Err = injectedErr

	if err := store.Save("x", nil); err != injectedErr {
		t.Errorf("expected injected error on Save, got %v", err)
	}
	if _, err := store.Load("x"); err != injectedErr {
		t.Errorf("expected injected error on Load, got %v", err)
	}
	if _, err := store.List(); err != injectedErr {
		t.Errorf("expected injected error on List, got %v", err)
	}
	if err := store.Delete("x"); err != injectedErr {
		t.Errorf("expected injected error on Delete, got %v", err)
	}
	if err := store.Append("x", session.Message{}); err != injectedErr {
		t.Errorf("expected injected error on Append, got %v", err)
	}
}

// TestMockSessionStoreLoadNotFound tests loading a non-existent session
func TestMockSessionStoreLoadNotFound(t *testing.T) {
	store := NewMockSessionStore()
	_, err := store.Load("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent session, got nil")
	}
}

// TestMockSessionStoreAppendAndExists tests Append actually stores data and Exists works
func TestMockSessionStoreAppendAndExists(t *testing.T) {
	store := NewMockSessionStore()

	if store.Exists("sess-1") {
		t.Error("expected Exists to return false for non-existent session")
	}

	msg := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}}
	if err := store.Append("sess-1", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if !store.Exists("sess-1") {
		t.Error("expected Exists to return true after Append")
	}

	loaded, err := store.Load("sess-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.GetMessages()) != 1 {
		t.Errorf("expected 1 message, got %d", len(loaded.GetMessages()))
	}
}

// TestMockSessionStoreRename tests Rename operation
func TestMockSessionStoreRename(t *testing.T) {
	store := NewMockSessionStore()
	s := session.NewSession()
	s.Metadata.Name = "old-name"
	store.Save("old-id", s)

	if err := store.Rename("old-id", "new-id"); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	if store.Exists("old-id") {
		t.Error("expected old-id to not exist after rename")
	}
	if !store.Exists("new-id") {
		t.Error("expected new-id to exist after rename")
	}
}

// TestMockSessionStoreImplementsInterface verifies MockSessionStore satisfies session.SessionStore
func TestMockSessionStoreImplementsInterface(t *testing.T) {
	var _ session.SessionStore = NewMockSessionStore()
}

// TestMockMCPClientToolCall tests MockMCPClient tool listing and calling
func TestMockMCPClientToolCall(t *testing.T) {
	client := NewMockMCPClient()
	client.ToolsList = []mcp.ToolInfo{
		{Name: "bash", Description: "Run bash commands"},
		{Name: "read_file", Description: "Read file contents"},
	}
	client.CallResults["bash"] = &mcp.ToolResult{
		Content: []mcp.ToolResultContent{{Type: "text", Text: "output: hello"}},
	}
	client.CallResults["read_file"] = &mcp.ToolResult{
		Content: []mcp.ToolResultContent{{Type: "text", Text: "file contents here"}},
	}

	// List tools
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "bash" {
		t.Errorf("expected first tool 'bash', got '%s'", tools[0].Name)
	}

	// Call existing tool
	result, err := client.CallTool(context.Background(), "bash", map[string]interface{}{"command": "echo hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text() != "output: hello" {
		t.Errorf("expected 'output: hello', got '%s'", result.Text())
	}

	// Call non-existent tool
	_, err = client.CallTool(context.Background(), "unknown", nil)
	if err == nil {
		t.Error("expected error for unknown tool, got nil")
	}
}

// TestMockMCPClientErrorInjection tests MCP client error injection
func TestMockMCPClientErrorInjection(t *testing.T) {
	injectedErr := errors.New("server unavailable")
	client := NewMockMCPClient()
	client.Err = injectedErr
	client.CallResults["bash"] = &mcp.ToolResult{
		Content: []mcp.ToolResultContent{{Type: "text", Text: "result"}},
	}

	_, err := client.CallTool(context.Background(), "bash", nil)
	if err != injectedErr {
		t.Errorf("expected injected error, got %v", err)
	}
}

// TestMockMCPClientCapabilities tests capabilities and status methods
func TestMockMCPClientCapabilities(t *testing.T) {
	client := NewMockMCPClient()
	client.ToolsList = []mcp.ToolInfo{{Name: "bash"}}

	if !client.HasTools() {
		t.Error("expected HasTools to return true")
	}
	if client.HasResources() {
		t.Error("expected HasResources to return false")
	}
	if client.HasPrompts() {
		t.Error("expected HasPrompts to return false")
	}
	if !client.IsReady() {
		t.Error("expected IsReady to return true")
	}
	if client.Name() != "mock-mcp-server" {
		t.Errorf("expected name 'mock-mcp-server', got '%s'", client.Name())
	}
	if client.Transport() != "mock" {
		t.Errorf("expected transport 'mock', got '%s'", client.Transport())
	}
	if client.Tools()[0].Name != "bash" {
		t.Errorf("expected tool 'bash', got '%s'", client.Tools()[0].Name)
	}
}

// TestMockMCPClientImplementsInterface verifies MockMCPClient satisfies mcp.MCPClient
func TestMockMCPClientImplementsInterface(t *testing.T) {
	var _ mcp.MCPClient = NewMockMCPClient()
}

// TestMockAIClientImplementsInterface 验证 MockAIClient 满足 ai.Client 接口
// 主要价值在编译期：var _ ai.Client = (*MockAIClient)(nil) 在 ai_client.go 中
// 此测试为运行时双保险，确保接口满足（编译期断言失败时本测试根本无法编译）
func TestMockAIClientImplementsInterface(t *testing.T) {
	var _ ai.Client = (*MockAIClient)(nil)
	var _ ai.Client = NewMockAIClient(nil)
	// 若 ai.Client 接口新增方法而 MockAIClient 未实现，编译期即失败
}

// TestMockSessionStoreDirCrossPlatform 验证默认 dir 跨平台一致：
// 1) 以 os.TempDir() 为前缀（Linux 为 /tmp，Windows 为 %TEMP%，macOS 为 $TMPDIR）
// 2) 等于 filepath.Join(os.TempDir(), "mock-sessions")
// 不再检查 "/tmp/mock-sessions" 字面量：该字面量在 Linux 上是合法且预期的，
// 在 Windows 上由 prefix 检查保证不出现（Windows 的 os.TempDir() 不含 /tmp 前缀）。
// 原字面量检查在 Linux 上自相矛盾：filepath.Join(os.TempDir(), "mock-sessions")
// 在 Linux 上正好等于 "/tmp/mock-sessions"。
func TestMockSessionStoreDirCrossPlatform(t *testing.T) {
	store := NewMockSessionStore()
	dir := store.Dir()

	// 应以 os.TempDir() 为前缀（跨平台）
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Errorf("默认 dir 应以 os.TempDir() 为前缀，got %q, want prefix %q", dir, os.TempDir())
	}

	// 应等于 filepath.Join(os.TempDir(), "mock-sessions")
	expected := filepath.Join(os.TempDir(), "mock-sessions")
	if dir != expected {
		t.Errorf("默认 dir = %q, want %q", dir, expected)
	}
}

// TestMockSessionStoreSetDir 验证 SetDir 可注入自定义目录，Dir() 返回该值
// 测试应使用 t.TempDir()（自动清理）替代硬编码路径
func TestMockSessionStoreSetDir(t *testing.T) {
	store := NewMockSessionStore()
	tmpDir := t.TempDir()

	// 初始 dir 不应是 t.TempDir()
	if store.Dir() == tmpDir {
		t.Fatalf("初始 dir 不应等于 t.TempDir()，got %q", tmpDir)
	}

	store.SetDir(tmpDir)

	if got := store.Dir(); got != tmpDir {
		t.Errorf("SetDir 后 Dir() = %q, want %q", got, tmpDir)
	}
}

// TestMockSessionStoreDirInjectableTempDir 验证 SetDir(t.TempDir()) 后，
// 调用方（如 internal/search/search.go:245）按 Dir()/{sessionID}.jsonl 拼路径能读到测试预置的文件
func TestMockSessionStoreDirInjectableTempDir(t *testing.T) {
	store := NewMockSessionStore()
	tmpDir := t.TempDir()
	store.SetDir(tmpDir)

	// 在 t.TempDir() 下预置一个 .jsonl 文件，模拟真实 session 持久化
	sessionID := "sess-test"
	fileContent := `{"role":"user","content":"hello"}`
	filePath := filepath.Join(tmpDir, sessionID+".jsonl")
	if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	// 模拟 search.go:245 的拼路径读取
	// 真实代码：path := filepathJoin(s.store.Dir(), sessionID+".jsonl")
	readPath := filepath.Join(store.Dir(), sessionID+".jsonl")
	data, err := os.ReadFile(readPath)
	if err != nil {
		t.Fatalf("按 Dir() 拼路径读取文件失败: %v", err)
	}
	if string(data) != fileContent {
		t.Errorf("读取内容 = %q, want %q", string(data), fileContent)
	}
}
