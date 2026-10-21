// Package session — 会话存储unit test
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// createTestStore 创建测试用的临时会话存储
func createTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return store
}

// createTestSession 创建包含指定数量消息的测试会话
func createTestSession(msgCount int) *Session {
	s := NewSession()
	for i := 0; i < msgCount; i++ {
		s.messages = append(s.messages, UserText("test message"))
	}
	return s
}

func TestStorePersistsCompactionCountMetadata(t *testing.T) {
	store := createTestStore(t)
	sess := NewSession()
	sess.Metadata.CompactionCount = 3
	sess.AddMessage(UserText("hello"))
	sess.IncrementTurn()
	if err := store.Save("compact-metadata", sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load("compact-metadata")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Metadata.CompactionCount != 3 {
		t.Fatalf("CompactionCount=%d, want 3", loaded.Metadata.CompactionCount)
	}
	if loaded.Metadata.TurnCount != sess.Metadata.TurnCount {
		t.Fatalf("TurnCount=%d, want %d; sidecar must not double-count", loaded.Metadata.TurnCount, sess.Metadata.TurnCount)
	}
}

// TestStore_AppendBatch 测试批量追加消息
func TestStore_AppendBatch(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-append"

	msgs := []Message{
		UserText("hello"),
		AssistantText("hi there", "test-model", &TokenUsage{InputTokens: 10, OutputTokens: 20}),
		UserText("how are you"),
	}

	err := store.AppendBatch(sessionID, msgs)
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// 验证文件存在
	if !store.Exists(sessionID) {
		t.Fatal("Session file should exist after AppendBatch")
	}

	// 验证内容
	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(loaded.messages))
	}

	if loaded.messages[0].TextContent() != "hello" {
		t.Fatalf("Expected first message 'hello', got '%s'", loaded.messages[0].TextContent())
	}
}

// TestStore_List 测试列出会话
func TestStore_List(t *testing.T) {
	store := createTestStore(t)

	// 空目录should return empty列表
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List on empty store failed: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("Expected 0 summaries for empty store, got %d", len(summaries))
	}

	// 创建多个会话
	sessionIDs := []string{"session-alpha", "session-beta", "session-gamma"}
	for i, id := range sessionIDs {
		msgs := []Message{UserText("msg for " + id)}
		err := store.AppendBatch(id, msgs)
		if err != nil {
			t.Fatalf("AppendBatch for %s failed: %v", id, err)
		}
		// 稍微延迟以确保时间顺序不同
		if i < len(sessionIDs)-1 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	summaries, err = store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(summaries) != 3 {
		t.Fatalf("Expected 3 summaries, got %d", len(summaries))
	}

	// 验证按更新时间降序排列
	for i := 1; i < len(summaries); i++ {
		if summaries[i].UpdatedAt > summaries[i-1].UpdatedAt {
			t.Fatalf("Summaries not sorted by UpdatedAt descending: [%d]=%d > [%d]=%d",
				i, summaries[i].UpdatedAt, i-1, summaries[i-1].UpdatedAt)
		}
	}

	// 验证会话 ID 包含在摘要中
	foundIDs := make(map[string]bool)
	for _, s := range summaries {
		foundIDs[s.ID] = true
	}
	for _, id := range sessionIDs {
		if !foundIDs[id] {
			t.Fatalf("Session ID '%s' not found in list", id)
		}
	}
}

// TestStore_Load 测试加载会话
func TestStore_Load(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-load"

	// 加载不存在的会话should return error
	_, err := store.Load("nonexistent")
	if err == nil {
		t.Fatal("Expected error when loading nonexistent session")
	}

	// 创建并保存会话
	msgs := []Message{
		UserText("user message 1"),
		AssistantText("assistant response 1", "model-a", &TokenUsage{InputTokens: 5, OutputTokens: 10}),
		UserText("user message 2"),
	}
	err = store.AppendBatch(sessionID, msgs)
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// 加载会话
	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 验证消息数量
	if len(loaded.messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(loaded.messages))
	}

	// 验证消息内容
	if loaded.messages[0].TextContent() != "user message 1" {
		t.Fatalf("Expected 'user message 1', got '%s'", loaded.messages[0].TextContent())
	}
	if loaded.messages[1].TextContent() != "assistant response 1" {
		t.Fatalf("Expected 'assistant response 1', got '%s'", loaded.messages[1].TextContent())
	}

	// 验证轮次计数（User消息数）
	if loaded.Metadata.TurnCount != 2 {
		t.Fatalf("Expected TurnCount=2, got %d", loaded.Metadata.TurnCount)
	}

	// 验证元数据名称
	if loaded.Metadata.Name != sessionID {
		t.Fatalf("Expected Name='%s', got '%s'", sessionID, loaded.Metadata.Name)
	}
}

// TestStore_Delete 测试删除会话
func TestStore_Delete(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-delete"

	// 创建会话
	err := store.AppendBatch(sessionID, []Message{UserText("to be deleted")})
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// 验证存在
	if !store.Exists(sessionID) {
		t.Fatal("Session should exist before delete")
	}

	// 删除
	err = store.Delete(sessionID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 验证已删除
	if store.Exists(sessionID) {
		t.Fatal("Session should not exist after delete")
	}

	// 删除不存在的会话should return error
	err = store.Delete("nonexistent")
	if err == nil {
		t.Fatal("Expected error when deleting nonexistent session")
	}
}

// TestStore_Rename 测试重命名会话
func TestStore_Rename(t *testing.T) {
	store := createTestStore(t)
	oldID := "test-old-name"
	newID := "test-new-name"

	// 创建会话
	err := store.AppendBatch(oldID, []Message{UserText("rename me")})
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// 验证旧会话存在
	if !store.Exists(oldID) {
		t.Fatal("Old session should exist before rename")
	}

	// 重命名
	err = store.Rename(oldID, newID)
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	// 验证旧会话不存在
	if store.Exists(oldID) {
		t.Fatal("Old session should not exist after rename")
	}

	// 验证新会话存在
	if !store.Exists(newID) {
		t.Fatal("New session should exist after rename")
	}

	// 验证内容完整
	loaded, err := store.Load(newID)
	if err != nil {
		t.Fatalf("Load renamed session failed: %v", err)
	}
	if loaded.messages[0].TextContent() != "rename me" {
		t.Fatalf("Expected 'rename me', got '%s'", loaded.messages[0].TextContent())
	}
}

// TestStore_Exists 测试检查会话是否存在
func TestStore_Exists(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-exists"

	// 不存在的会话
	if store.Exists(sessionID) {
		t.Fatal("Session should not exist before creation")
	}

	// 创建会话
	err := store.AppendBatch(sessionID, []Message{UserText("exists test")})
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// 应该存在
	if !store.Exists(sessionID) {
		t.Fatal("Session should exist after creation")
	}

	// 不存在的会话
	if store.Exists("no-such-session") {
		t.Fatal("Nonexistent session should not exist")
	}
}

// TestStore_GenerateID 测试会话 ID 生成
func TestStore_GenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()

	// ID should not be empty
	if id1 == "" {
		t.Fatal("Generated ID should not be empty")
	}
	if id2 == "" {
		t.Fatal("Generated ID should not be empty")
	}

	// 两次生成的 ID 应不同
	if id1 == id2 {
		t.Fatal("Two generated IDs should be different")
	}

	// ID 应以 "session-" 前缀开头
	if len(id1) < 10 {
		t.Fatalf("Generated ID seems too short: %s", id1)
	}
}

// TestStore_Append 测试单条追加消息
func TestStore_Append(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-append-single"

	// 逐条追加
	err := store.Append(sessionID, UserText("first"))
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	err = store.Append(sessionID, UserText("second"))
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// 验证
	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(loaded.messages))
	}
	if loaded.messages[0].TextContent() != "first" {
		t.Fatalf("Expected 'first', got '%s'", loaded.messages[0].TextContent())
	}
	if loaded.messages[1].TextContent() != "second" {
		t.Fatalf("Expected 'second', got '%s'", loaded.messages[1].TextContent())
	}
}

// TestStore_ListWithTurnCountAndCost 测试列表摘要包含轮次和费用
func TestStore_ListWithTurnCountAndCost(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-summary"

	msgs := []Message{
		UserText("question 1"),
		AssistantText("answer 1", "model", &TokenUsage{InputTokens: 100, OutputTokens: 200}),
		UserText("question 2"),
		AssistantText("answer 2", "model", &TokenUsage{InputTokens: 50, OutputTokens: 150}),
	}
	err := store.AppendBatch(sessionID, msgs)
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(summaries) != 1 {
		t.Fatalf("Expected 1 summary, got %d", len(summaries))
	}

	s := summaries[0]
	if s.TurnCount != 2 {
		t.Fatalf("Expected TurnCount=2, got %d", s.TurnCount)
	}
}

// TestStore_MultipleSessionsSorted 测试多会话按时间排序
func TestStore_MultipleSessionsSorted(t *testing.T) {
	store := createTestStore(t)

	// 按顺序创建会话，间隔一点时间
	sessions := []struct {
		id   string
		msgs int
	}{
		{"oldest-session", 1},
		{"middle-session", 2},
		{"newest-session", 3},
	}

	for _, s := range sessions {
		msgs := make([]Message, s.msgs)
		for i := range msgs {
			msgs[i] = UserText("msg " + s.id)
		}
		err := store.AppendBatch(s.id, msgs)
		if err != nil {
			t.Fatalf("AppendBatch failed for %s: %v", s.id, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(summaries) != 3 {
		t.Fatalf("Expected 3 summaries, got %d", len(summaries))
	}

	// 最新的应排在最前面
	if summaries[0].ID != "newest-session" {
		t.Fatalf("Expected newest session first, got '%s'", summaries[0].ID)
	}
	if summaries[2].ID != "oldest-session" {
		t.Fatalf("Expected oldest session last, got '%s'", summaries[2].ID)
	}
}

// TestStore_ToolResult 测试包含工具调用的会话存储和加载
func TestStore_ToolResult(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-tool-result"

	msgs := []Message{
		UserText("read a file"),
		AssistantWithTools([]ContentBlock{
			{Type: "text", Text: "Let me read that file"},
			{Type: "tool_use", ID: "tool-1", Name: "read_file", Input: `{"path":"/tmp/test.txt"}`},
		}, "model", &TokenUsage{InputTokens: 50, OutputTokens: 100}),
		ToolResult("tool-1", "read_file", "file content here", false),
	}

	err := store.AppendBatch(sessionID, msgs)
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(loaded.messages))
	}

	// 验证工具调用消息
	toolUses := loaded.messages[1].ToolUses()
	if len(toolUses) != 1 {
		t.Fatalf("Expected 1 tool use, got %d", len(toolUses))
	}
	if toolUses[0].Name != "read_file" {
		t.Fatalf("Expected tool name 'read_file', got '%s'", toolUses[0].Name)
	}

	// 验证工具结果消息
	if loaded.messages[2].Role != RoleTool {
		t.Fatalf("Expected RoleTool, got '%s'", loaded.messages[2].Role)
	}
}

// TestStore_SessionPath 测试会话文件路径生成
func TestStore_SessionPath(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	path := store.sessionPath("my-session")
	expected := filepath.Join(dir, "my-session.jsonl")
	if path != expected {
		t.Fatalf("Expected path '%s', got '%s'", expected, path)
	}
}

// TestStore_CurrentTimeMillis 测试时间毫秒获取
func TestStore_CurrentTimeMillis(t *testing.T) {
	before := time.Now().UnixMilli()
	result := CurrentTimeMillis()
	after := time.Now().UnixMilli()

	if result < before || result > after {
		t.Fatalf("CurrentTimeMillis()=%d not between %d and %d", result, before, after)
	}
}

// TestStore_MillisFromTime 测试从 time.Time 获取毫秒
func TestStore_MillisFromTime(t *testing.T) {
	// nil 参数should return当前时间
	resultNil := MillisFromTime(nil)
	if resultNil <= 0 {
		t.Fatalf("MillisFromTime(nil) should return positive value, got %d", resultNil)
	}

	// 指定时间
	t1 := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	result := MillisFromTime(&t1)
	expected := t1.UnixMilli()
	if result != expected {
		t.Fatalf("MillisFromTime()=%d, expected %d", result, expected)
	}
}

// TestStore_EmptyDirList 测试空目录下列表
func TestStore_EmptyDirList(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List on empty dir failed: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("Expected 0 summaries, got %d", len(summaries))
	}
}

// TestStore_NonJSONLFilesIgnored 测试非 JSONL 文件被忽略
func TestStore_NonJSONLFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 创建一个非 JSONL 文件
	err = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a session"), 0644)
	if err != nil {
		t.Fatalf("Failed to create non-JSONL file: %v", err)
	}

	// 创建一个子目录
	err = os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	if err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("Expected 0 summaries (non-JSONL files should be ignored), got %d", len(summaries))
	}
}

// TestStore_ListSortedByUpdatedAt 测试列表按更新时间降序排列
func TestStore_ListSortedByUpdatedAt(t *testing.T) {
	store := createTestStore(t)

	// 创建三个会话，间隔时间
	ids := []string{"session-a", "session-b", "session-c"}
	for _, id := range ids {
		err := store.AppendBatch(id, []Message{UserText("msg")})
		if err != nil {
			t.Fatalf("AppendBatch failed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// 验证降序排列
	for i := 1; i < len(summaries); i++ {
		if summaries[i].UpdatedAt > summaries[i-1].UpdatedAt {
			t.Fatalf("List not sorted descending: summaries[%d].UpdatedAt=%d > summaries[%d].UpdatedAt=%d",
				i, summaries[i].UpdatedAt, i-1, summaries[i-1].UpdatedAt)
		}
	}

	// 最后创建的应排在最前
	if summaries[0].ID != "session-c" {
		t.Fatalf("Expected 'session-c' first (most recent), got '%s'", summaries[0].ID)
	}
}

// TestStore_DeleteNonExistent 测试删除不存在的会话
func TestStore_DeleteNonExistent(t *testing.T) {
	store := createTestStore(t)

	err := store.Delete("nonexistent-session")
	if err == nil {
		t.Fatal("Expected error when deleting nonexistent session")
	}
}

// TestStore_RenameNonExistent 测试重命名不存在的会话
func TestStore_RenameNonExistent(t *testing.T) {
	store := createTestStore(t)

	err := store.Rename("nonexistent", "new-name")
	if err == nil {
		t.Fatal("Expected error when renaming nonexistent session")
	}
}

// TestStore_ConcurrentAppend 测试并发追加
func TestStore_ConcurrentAppend(t *testing.T) {
	store := createTestStore(t)
	sessionID := "test-concurrent"

	// 先创建会话
	err := store.AppendBatch(sessionID, []Message{UserText("init")})
	if err != nil {
		t.Fatalf("Initial AppendBatch failed: %v", err)
	}

	// 并发追加
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			err := store.Append(sessionID, UserText("concurrent msg"))
			if err != nil {
				t.Errorf("Concurrent Append %d failed: %v", idx, err)
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// 验证消息数量
	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 1 initial + 10 concurrent
	if len(loaded.messages) != 11 {
		t.Fatalf("Expected 11 messages, got %d", len(loaded.messages))
	}
}

// TestStore_SortSummaries 测试摘要排序辅助
func TestStore_SortSummaries(t *testing.T) {
	summaries := []SessionSummary{
		{ID: "old", UpdatedAt: 1000},
		{ID: "new", UpdatedAt: 3000},
		{ID: "mid", UpdatedAt: 2000},
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})

	if summaries[0].ID != "new" {
		t.Fatalf("Expected 'new' first, got '%s'", summaries[0].ID)
	}
	if summaries[1].ID != "mid" {
		t.Fatalf("Expected 'mid' second, got '%s'", summaries[1].ID)
	}
	if summaries[2].ID != "old" {
		t.Fatalf("Expected 'old' third, got '%s'", summaries[2].ID)
	}
}

// TestFileLockAcquire 测试文件锁的获取和释放
func TestFileLockAcquire(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-lock-session"

	// 创建会话文件
	err = store.Append(sessionID, Message{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// 获取锁
	lock, err := store.AcquireLock(sessionID)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}

	// 验证锁文件存在
	lockPath := filepath.Join(dir, sessionID+".lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("Lock file should exist after AcquireLock")
	}

	// 释放锁
	lock.Release()

	// ★ L1 修复（flock 迁移）：Release 只解锁、不删 backing file（filelock.go:15-16
	// 明写持锁期不得删 backing file，否则 unlink+flock 破坏互斥）。故断言改为
	// 「锁已释放 = 可再次 AcquireLock」，而非「锁文件已删除」。
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("Lock file should still exist after Release (flock keeps the backing file)")
	}
	// 释放后能再次获取锁（证明锁真的释放了，不是靠删文件伪装的）。
	lock2, err := store.AcquireLock(sessionID)
	if err != nil {
		t.Fatalf("Re-acquire after Release failed: %v", err)
	}
	lock2.Release()
}

// TestFileLockConflict 测试文件锁冲突（第二个锁应失败）
func TestFileLockConflict(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-conflict-session"

	// 创建会话文件
	store.Append(sessionID, Message{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "hello"}}})

	// 第一个锁应成功
	lock1, err := store.AcquireLock(sessionID)
	if err != nil {
		t.Fatalf("First AcquireLock failed: %v", err)
	}

	// 第二个锁应失败（冲突）
	_, err = store.AcquireLock(sessionID)
	if err == nil {
		t.Error("Second AcquireLock should fail when lock is held")
		lock1.Release()
	}

	lock1.Release()
}

// TestForkSession 测试会话分叉功能
func TestForkSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 创建包含多条消息的原始会话
	origID := "original-session"
	msgs := []Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "message 1"}}, Timestamp: 1000},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "response 1"}}, Timestamp: 2000},
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "message 2"}}, Timestamp: 3000},
	}
	for _, msg := range msgs {
		store.Append(origID, msg)
	}

	// 分叉会话
	forkID, err := store.ForkSession(origID)
	if err != nil {
		t.Fatalf("ForkSession failed: %v", err)
	}

	// 分叉 ID 应与原始 ID 不同
	if forkID == origID {
		t.Error("Fork session ID should be different from original")
	}

	// 分叉会话should contain相同数量的消息
	forkSession, err := store.Load(forkID)
	if err != nil {
		t.Fatalf("Load forked session failed: %v", err)
	}
	if len(forkSession.messages) != len(msgs) {
		t.Errorf("Forked session should have %d messages, got %d", len(msgs), len(forkSession.messages))
	}
}

// TestForkSessionIndependence 测试分叉会话的独立性
func TestForkSessionIndependence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	origID := "independence-orig"
	store.Append(origID, Message{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "original"}}, Timestamp: 1000})

	forkID, _ := store.ForkSession(origID)

	// 向分叉会话追加消息
	store.Append(forkID, Message{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "fork only"}}, Timestamp: 2000})

	// 原始会话应仍只有 1 条消息
	origSession, _ := store.Load(origID)
	if len(origSession.messages) != 1 {
		t.Errorf("Original session should have 1 message, got %d", len(origSession.messages))
	}

	// 分叉会话应有 2 条消息
	forkSession, _ := store.Load(forkID)
	if len(forkSession.messages) != 2 {
		t.Errorf("Forked session should have 2 messages, got %d", len(forkSession.messages))
	}
}

// TestConcurrentAppend 测试并发追加消息的线程security
func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "concurrent-session"

	// 创建初始会话
	store.Append(sessionID, Message{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "init"}}, Timestamp: 1000})

	// 多个 goroutine 并发追加
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := Message{
				Role:      RoleUser,
				Blocks:    []ContentBlock{{Type: "text", Text: fmt.Sprintf("msg-%d", idx)}},
				Timestamp: int64(1000 + idx),
			}
			if err := store.Append(sessionID, msg); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			t.Errorf("Concurrent append error: %v", err)
		}
	}

	// 验证所有消息均已写入
	session, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	// 1 init + 10 concurrent = 11
	if len(session.messages) != 11 {
		t.Errorf("Expected 11 messages, got %d", len(session.messages))
	}
}
