package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 测试辅助函数 ----------

// newTestShardedStore 在临时目录创建 ShardedIndexStore 用于测试
func newTestShardedStore(t *testing.T) *ShardedIndexStore {
	t.Helper()
	shardDir := filepath.Join(t.TempDir(), "index")
	lockFile := filepath.Join(t.TempDir(), "flock")
	store, err := NewShardedIndexStore(shardDir, lockFile)
	if err != nil {
		t.Fatalf("NewShardedIndexStore: %v", err)
	}
	return store
}

// makeTestEntry 构造测试用 IndexEntry
func makeTestEntry(msgId, entryType string) IndexEntry {
	return IndexEntry{
		Type:      entryType,
		MsgId:     msgId,
		Timestamp: time.Now(),
		Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
	}
}

// computeStorageDirForTest 复现 NewFileHistory 的 storageDir 计算逻辑用于测试预置
func computeStorageDirForTest(t *testing.T, workDir, baseDir string) string {
	t.Helper()
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatalf("abs workDir: %v", err)
	}
	// 与 NewFileHistory 保持一致；macOS 上 /var 会解析为 /private/var，
	// 若测试不做同样规范化会预置到错误的 hash 目录。
	if resolved, err := filepath.EvalSymlinks(absWorkDir); err == nil {
		absWorkDir = resolved
	}
	sum := sha256.Sum256([]byte(absWorkDir))
	hex16 := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(baseDir, hex16)
}

// ---------- 跨平台路径测试 ----------

// TestSessionToFilename_EmptyReturnsDefault 验证空 sessionId 返回默认分片文件名
func TestSessionToFilename_EmptyReturnsDefault(t *testing.T) {
	got := sessionToFilename("")
	if got != defaultFilename {
		t.Errorf("empty sessionId: expected %s, got %s", defaultFilename, got)
	}
}

// TestSessionToFilename_HandlesSpecialChars 验证特殊字符经 SHA256 后变为合法文件名（防路径注入）
func TestSessionToFilename_HandlesSpecialChars(t *testing.T) {
	tests := []string{
		"../etc/passwd",
		"a:b",
		"a\\b",
		"a*b?c",
		"very/long/path/with/many/slashes",
		"session with spaces",
	}
	seen := map[string]bool{}
	for _, sid := range tests {
		got := sessionToFilename(sid)
		if !strings.HasPrefix(got, shardFilenamePrefix) {
			t.Errorf("sessionId %q: filename should have prefix %s, got %s", sid, shardFilenamePrefix, got)
		}
		if !strings.HasSuffix(got, shardFilenameSuffix) {
			t.Errorf("sessionId %q: filename should have suffix %s, got %s", sid, shardFilenameSuffix, got)
		}
		// 文件名不应包含原始特殊字符
		if strings.ContainsAny(got, ":\\*?") {
			t.Errorf("sessionId %q: filename %s contains special chars", sid, got)
		}
		if strings.Contains(got, "../") {
			t.Errorf("sessionId %q: filename %s contains path traversal", sid, got)
		}
		// 不同 sessionId 应产生不同文件名（除非碰撞，理论概率极低）
		if seen[got] {
			t.Errorf("sessionId %q: filename %s collided with previous sessionId", sid, got)
		}
		seen[got] = true
	}
}

// TestSessionToFilename_LegacyReturnsLegacy 验证 "legacy" 返回固定文件名
func TestSessionToFilename_LegacyReturnsLegacy(t *testing.T) {
	got := sessionToFilename(legacySession)
	if got != legacyFilename {
		t.Errorf("legacy sessionId: expected %s, got %s", legacyFilename, got)
	}
}

// ---------- ShardedIndexStore 核心测试 ----------

// TestShardedIndexStore_AppendEntry_RoutesBySession 验证不同 sessionId 路由到不同分片文件
func TestShardedIndexStore_AppendEntry_RoutesBySession(t *testing.T) {
	store := newTestShardedStore(t)

	// 写入 session-A
	if err := store.SetActiveSession("session-A"); err != nil {
		t.Fatalf("SetActiveSession A: %v", err)
	}
	if err := store.AppendEntry(makeTestEntry("msg-A1", "track")); err != nil {
		t.Fatalf("AppendEntry A1: %v", err)
	}

	// 写入 session-B
	if err := store.SetActiveSession("session-B"); err != nil {
		t.Fatalf("SetActiveSession B: %v", err)
	}
	if err := store.AppendEntry(makeTestEntry("msg-B1", "track")); err != nil {
		t.Fatalf("AppendEntry B1: %v", err)
	}

	// 验证两个分片文件都存在
	shardA := filepath.Join(store.shardDir, sessionToFilename("session-A"))
	shardB := filepath.Join(store.shardDir, sessionToFilename("session-B"))
	if _, err := os.Stat(shardA); err != nil {
		t.Errorf("shardA should exist: %v", err)
	}
	if _, err := os.Stat(shardB); err != nil {
		t.Errorf("shardB should exist: %v", err)
	}

	// 验证分片文件路径不同
	if shardA == shardB {
		t.Error("shardA and shardB should be different files")
	}
}

// TestShardedIndexStore_ScanEntries_OnlyActiveShard 验证 ScanEntries 仅返回当前活跃分片记录
func TestShardedIndexStore_ScanEntries_OnlyActiveShard(t *testing.T) {
	store := newTestShardedStore(t)

	// session-A 写入 3 条
	store.SetActiveSession("session-A")
	for i := 0; i < 3; i++ {
		if err := store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-A%d", i), "track")); err != nil {
			t.Fatalf("AppendEntry A%d: %v", i, err)
		}
	}

	// session-B 写入 2 条
	store.SetActiveSession("session-B")
	for i := 0; i < 2; i++ {
		if err := store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-B%d", i), "track")); err != nil {
			t.Fatalf("AppendEntry B%d: %v", i, err)
		}
	}

	// ScanEntries 应只返回 session-B 的 2 条
	entries, err := store.ScanEntries()
	if err != nil {
		t.Fatalf("ScanEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries (session-B), got %d", len(entries))
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.MsgId, "msg-B") {
			t.Errorf("expected msg-B*, got %s", e.MsgId)
		}
	}
}

// TestShardedIndexStore_ScanAllShards_ReturnsAll 验证 ScanAllShards 返回所有分片记录
func TestShardedIndexStore_ScanAllShards_ReturnsAll(t *testing.T) {
	store := newTestShardedStore(t)

	// session-A 3 条 + session-B 2 条 = 5 条
	store.SetActiveSession("session-A")
	for i := 0; i < 3; i++ {
		store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-A%d", i), "track"))
	}
	store.SetActiveSession("session-B")
	for i := 0; i < 2; i++ {
		store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-B%d", i), "track"))
	}

	all, err := store.ScanAllShards()
	if err != nil {
		t.Fatalf("ScanAllShards: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("expected 5 entries across all shards, got %d", len(all))
	}

	// 验证 entries 的 SessionId 被正确反查填入
	for _, e := range all {
		if e.SessionId == "" {
			t.Errorf("entry %s should have SessionId filled", e.MsgId)
		}
	}
}

// TestShardedIndexStore_ScanAllShards_SkipsCorruptShard 验证跨分片扫描跳过损坏分片
func TestShardedIndexStore_ScanAllShards_SkipsCorruptShard(t *testing.T) {
	store := newTestShardedStore(t)

	// 写入 2 个正常分片
	store.SetActiveSession("session-A")
	store.AppendEntry(makeTestEntry("msg-A1", "track"))
	store.SetActiveSession("session-B")
	store.AppendEntry(makeTestEntry("msg-B1", "track"))

	// 创建一个损坏的分片文件
	corruptPath := filepath.Join(store.shardDir, "index_corrupt1234567890.jsonl")
	if err := os.WriteFile(corruptPath, []byte("{this is not valid json!!!\n"), 0o600); err != nil {
		t.Fatalf("write corrupt shard: %v", err)
	}

	// ScanAllShards 应跳过损坏分片，返回 2 条
	all, err := store.ScanAllShards()
	if err != nil {
		t.Fatalf("ScanAllShards should not error on corrupt shards: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 entries (skip corrupt), got %d", len(all))
	}
}

// TestShardedIndexStore_SetActiveSession_SwitchesShard 验证 SetActiveSession 切换活跃分片
func TestShardedIndexStore_SetActiveSession_SwitchesShard(t *testing.T) {
	store := newTestShardedStore(t)

	// 写入 session-A
	store.SetActiveSession("session-A")
	store.AppendEntry(makeTestEntry("msg-A1", "track"))

	// 切换前 ScanEntries 返回 session-A 的记录
	entries, _ := store.ScanEntries()
	if len(entries) != 1 || entries[0].MsgId != "msg-A1" {
		t.Errorf("before switch: expected msg-A1, got %v", entries)
	}

	// 切换到 session-B
	store.SetActiveSession("session-B")
	store.AppendEntry(makeTestEntry("msg-B1", "track"))

	// 切换后 ScanEntries 返回 session-B 的记录
	entries, _ = store.ScanEntries()
	if len(entries) != 1 || entries[0].MsgId != "msg-B1" {
		t.Errorf("after switch: expected msg-B1, got %v", entries)
	}
}

// TestShardedIndexStore_SetActiveSession_EmptyUsesDefault 验证空 sessionId 路由到默认分片
func TestShardedIndexStore_SetActiveSession_EmptyUsesDefault(t *testing.T) {
	store := newTestShardedStore(t)

	if err := store.SetActiveSession(""); err != nil {
		t.Fatalf("SetActiveSession(\"\"): %v", err)
	}

	if err := store.AppendEntry(makeTestEntry("msg-default", "track")); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	// 验证默认分片文件存在
	defaultPath := filepath.Join(store.shardDir, defaultFilename)
	if _, err := os.Stat(defaultPath); err != nil {
		t.Errorf("default shard should exist: %v", err)
	}

	// 验证 ActiveShardPath 指向默认分片
	if store.ActiveShardPath() != defaultPath {
		t.Errorf("ActiveShardPath: expected %s, got %s", defaultPath, store.ActiveShardPath())
	}
}

// TestShardedIndexStore_FindEntry_OnlyInActiveShard 验证 FindEntry 仅在活跃分片查找（优化 1）
func TestShardedIndexStore_FindEntry_OnlyInActiveShard(t *testing.T) {
	store := newTestShardedStore(t)

	// session-A 写入 msg-001
	store.SetActiveSession("session-A")
	store.AppendEntry(makeTestEntry("msg-001", "track"))

	// 在 session-A 中查找 → 应找到
	found, err := store.FindEntry("msg-001", "track")
	if err != nil {
		t.Fatalf("FindEntry in session-A: %v", err)
	}
	if found == nil {
		t.Error("expected to find msg-001 in session-A")
	}

	// 切换到 session-B
	store.SetActiveSession("session-B")

	// 在 session-B 中查找 msg-001 → 应返回 nil（不在活跃分片）
	found, err = store.FindEntry("msg-001", "track")
	if err != nil {
		t.Fatalf("FindEntry in session-B: %v", err)
	}
	if found != nil {
		t.Error("expected nil for msg-001 in session-B (cross-shard find not supported)")
	}
}

// TestShardedIndexStore_RewriteIndex_RejectsForeignSession 验证 RewriteIndex 拒绝非本会话 entries（优化 2）
func TestShardedIndexStore_RewriteIndex_RejectsForeignSession(t *testing.T) {
	store := newTestShardedStore(t)
	store.SetActiveSession("session-A")

	// 提交 session-B 的 entries 到 session-A 活跃分片
	foreignEntries := []IndexEntry{
		{
			Type:      "track",
			MsgId:     "msg-B1",
			Timestamp: time.Now(),
			SessionId: "session-B", // 不匹配 activeSession
			Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
		},
	}

	err := store.RewriteIndex(foreignEntries)
	if err == nil {
		t.Fatal("RewriteIndex should reject entries with foreign sessionId")
	}
	if !strings.Contains(err.Error(), "does not match active session") {
		t.Errorf("error should mention session mismatch: %v", err)
	}
}

// TestShardedIndexStore_RewriteIndex_OverwritesActiveShard 验证 RewriteIndex 重写活跃分片
func TestShardedIndexStore_RewriteIndex_OverwritesActiveShard(t *testing.T) {
	store := newTestShardedStore(t)
	store.SetActiveSession("session-A")

	// 写入 3 条
	for i := 0; i < 3; i++ {
		store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-%d", i), "track"))
	}

	// RewriteIndex 为 2 条（SessionId 为空，由 store 校验通过）
	newEntries := []IndexEntry{
		{
			Type:      "track",
			MsgId:     "msg-new1",
			Timestamp: time.Now(),
			Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
		},
		{
			Type:      "track",
			MsgId:     "msg-new2",
			Timestamp: time.Now(),
			Files:     []FileChange{{Path: "/test2.go", Mode: 0644}},
		},
	}
	if err := store.RewriteIndex(newEntries); err != nil {
		t.Fatalf("RewriteIndex: %v", err)
	}

	// ScanEntries 应返回 2 条（重写后）
	entries, _ := store.ScanEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries after rewrite, got %d", len(entries))
	}
}

// TestShardedIndexStore_TruncateAfter_OnlyActiveShard 验证 TruncateAfter 仅截断活跃分片
func TestShardedIndexStore_TruncateAfter_OnlyActiveShard(t *testing.T) {
	store := newTestShardedStore(t)

	// session-A 写入 3 条
	store.SetActiveSession("session-A")
	for _, msgId := range []string{"msg-A1", "msg-A2", "msg-A3"} {
		store.AppendEntry(makeTestEntry(msgId, "track"))
	}

	// 切换到 session-B（不影响 session-A 的数据）
	store.SetActiveSession("session-B")
	store.AppendEntry(makeTestEntry("msg-B1", "track"))

	// 在 session-B 调用 TruncateAfter("msg-A1") - msgId 不在活跃分片
	// truncateAfter 找不到 msgId 时是 noop
	if err := store.TruncateAfter("msg-A1"); err != nil {
		t.Fatalf("TruncateAfter on session-B: %v", err)
	}

	// 切回 session-A，验证 3 条记录未被影响
	store.SetActiveSession("session-A")
	entries, _ := store.ScanEntries()
	if len(entries) != 3 {
		t.Errorf("session-A should still have 3 entries, got %d", len(entries))
	}

	// 在 session-A 调用 TruncateAfter("msg-A2") - 截断到 msg-A2（保留 A1, A2）
	if err := store.TruncateAfter("msg-A2"); err != nil {
		t.Fatalf("TruncateAfter on session-A: %v", err)
	}
	entries, _ = store.ScanEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries after truncate, got %d", len(entries))
	}
}

// ---------- MigrateFromLegacy 测试 ----------

// TestMigrateFromLegacy_RenamesIndex 验证老索引迁移到 index_legacy.jsonl
func TestMigrateFromLegacy_RenamesIndex(t *testing.T) {
	store := newTestShardedStore(t)

	// 创建老索引文件，写入 5 条
	legacyFile := filepath.Join(t.TempDir(), "index.jsonl")
	for i := 0; i < 5; i++ {
		if err := appendEntry(legacyFile, makeTestEntry(fmt.Sprintf("msg-%d", i), "track")); err != nil {
			t.Fatalf("appendEntry %d: %v", i, err)
		}
	}

	// 执行迁移
	if err := store.MigrateFromLegacy(legacyFile); err != nil {
		t.Fatalf("MigrateFromLegacy: %v", err)
	}

	// 原文件应不存在（已 rename）
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Errorf("legacy file should not exist after migration: %v", err)
	}

	// index_legacy.jsonl 应存在
	legacyShardPath := filepath.Join(store.shardDir, legacyFilename)
	if _, err := os.Stat(legacyShardPath); err != nil {
		t.Errorf("index_legacy.jsonl should exist: %v", err)
	}

	// ScanEntries 应返回 5 条（活跃会话已设为 "legacy"）
	entries, _ := store.ScanEntries()
	if len(entries) != 5 {
		t.Errorf("expected 5 entries in legacy shard, got %d", len(entries))
	}
}

// TestMigrateFromLegacy_Idempotent 验证迁移幂等（重复调用不报错）
func TestMigrateFromLegacy_Idempotent(t *testing.T) {
	store := newTestShardedStore(t)

	legacyFile := filepath.Join(t.TempDir(), "index.jsonl")
	appendEntry(legacyFile, makeTestEntry("msg-1", "track"))

	// 第一次迁移
	if err := store.MigrateFromLegacy(legacyFile); err != nil {
		t.Fatalf("first MigrateFromLegacy: %v", err)
	}

	// 第二次迁移（老文件已不存在，应返回 nil）
	if err := store.MigrateFromLegacy(legacyFile); err != nil {
		t.Errorf("second MigrateFromLegacy should be nil (idempotent): %v", err)
	}

	// 验证 index_legacy.jsonl 仍只有 1 条
	entries, _ := store.ScanEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after idempotent migration, got %d", len(entries))
	}
}

// TestMigrateFromLegacy_RollbackOnFailure 验证迁移失败时原文件保留
func TestMigrateFromLegacy_RollbackOnFailure(t *testing.T) {
	// 创建 store 后删除 shardDir，使 metaTmp 写入失败
	shardDir := filepath.Join(t.TempDir(), "index")
	lockFile := filepath.Join(t.TempDir(), "flock")
	store, err := NewShardedIndexStore(shardDir, lockFile)
	if err != nil {
		t.Fatalf("NewShardedIndexStore: %v", err)
	}

	// 创建老索引文件
	legacyFile := filepath.Join(t.TempDir(), "index.jsonl")
	appendEntry(legacyFile, makeTestEntry("msg-1", "track"))

	// 删除 shardDir 使 metaTmp 写入失败（模拟迁移失败场景）
	if err := os.RemoveAll(shardDir); err != nil {
		t.Fatalf("remove shardDir: %v", err)
	}

	// MigrateFromLegacy 应失败（metaTmp 写入失败）
	err = store.MigrateFromLegacy(legacyFile)
	if err == nil {
		t.Fatal("MigrateFromLegacy should fail when shardDir doesn't exist")
	}

	// 原文件应仍然存在（未被 rename）
	if _, err := os.Stat(legacyFile); err != nil {
		t.Errorf("legacy file should still exist after failed migration: %v", err)
	}

	// index_legacy.jsonl 不应存在
	legacyShardPath := filepath.Join(shardDir, legacyFilename)
	if _, err := os.Stat(legacyShardPath); err == nil {
		t.Error("index_legacy.jsonl should not exist after failed migration")
	}
}

// TestMigrateFromLegacy_DetectsInterruptedMigration 验证检测中断的迁移（metaTmp 残留）
func TestMigrateFromLegacy_DetectsInterruptedMigration(t *testing.T) {
	store := newTestShardedStore(t)

	// 模拟迁移中断：手动创建 metaTmpFile
	metaTmpData := `{"version":1,"activeSession":"legacy","shards":[],"lastMigratedAt":"0001-01-01T00:00:00Z"}`
	if err := os.WriteFile(store.metaTmpFile, []byte(metaTmpData), 0o600); err != nil {
		t.Fatalf("write metaTmp: %v", err)
	}

	// 同时创建一个 legacy shard（模拟迁移已 rename 但未提交 meta）
	legacyShardPath := filepath.Join(store.shardDir, legacyFilename)
	appendEntry(legacyShardPath, makeTestEntry("msg-legacy-1", "track"))

	// LoadMeta 应检测到 metaTmp 残留 → 删除 → 重建 meta
	if err := store.LoadMeta(); err != nil {
		t.Fatalf("LoadMeta should recover from interrupted migration: %v", err)
	}

	// metaTmp 应被删除
	if _, err := os.Stat(store.metaTmpFile); err == nil {
		t.Error("metaTmp should be deleted after recovery")
	}

	// meta 应被重建，包含 legacy shard 信息
	if store.meta == nil {
		t.Fatal("meta should not be nil after recovery")
	}
	found := false
	for _, s := range store.meta.Shards {
		if s.Filename == legacyFilename {
			found = true
			if s.EntryCount != 1 {
				t.Errorf("legacy shard entry count: expected 1, got %d", s.EntryCount)
			}
		}
	}
	if !found {
		t.Error("meta should contain legacy shard after recovery")
	}
}

// TestMigrateFromLegacy_UpdatesActiveSession 验证迁移后活跃会话设为 "legacy"
func TestMigrateFromLegacy_UpdatesActiveSession(t *testing.T) {
	store := newTestShardedStore(t)

	// 迁移前活跃会话为 "default"
	if store.activeSession != defaultSession {
		t.Errorf("before migration: expected activeSession=%s, got %s", defaultSession, store.activeSession)
	}

	// 创建并迁移老索引
	legacyFile := filepath.Join(t.TempDir(), "index.jsonl")
	appendEntry(legacyFile, makeTestEntry("msg-1", "track"))
	store.MigrateFromLegacy(legacyFile)

	// 迁移后活跃会话应设为 "legacy"
	if store.activeSession != legacySession {
		t.Errorf("after migration: expected activeSession=%s, got %s", legacySession, store.activeSession)
	}

	// ActiveShardPath 应指向 index_legacy.jsonl
	expectedPath := filepath.Join(store.shardDir, legacyFilename)
	if store.ActiveShardPath() != expectedPath {
		t.Errorf("ActiveShardPath: expected %s, got %s", expectedPath, store.ActiveShardPath())
	}
}

// ---------- SingleFileIndexStore 测试 ----------

// TestSingleFileIndexStore_AllMethods 验证所有方法委托包级函数正确
func TestSingleFileIndexStore_AllMethods(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")
	lockFile := filepath.Join(dir, "flock")
	store := NewSingleFileIndexStore(indexFile, lockFile)

	// AppendEntry
	if err := store.AppendEntry(makeTestEntry("msg-1", "track")); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	// ScanEntries
	entries, err := store.ScanEntries()
	if err != nil {
		t.Fatalf("ScanEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}

	// FindEntry
	found, err := store.FindEntry("msg-1", "track")
	if err != nil {
		t.Fatalf("FindEntry: %v", err)
	}
	if found == nil {
		t.Error("expected to find msg-1")
	}

	// RewriteIndex
	newEntries := []IndexEntry{
		makeTestEntry("msg-2", "track"),
		makeTestEntry("msg-3", "track"),
	}
	if err := store.RewriteIndex(newEntries); err != nil {
		t.Fatalf("RewriteIndex: %v", err)
	}
	entries, _ = store.ScanEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries after rewrite, got %d", len(entries))
	}

	// TruncateAfter
	if err := store.TruncateAfter("msg-2"); err != nil {
		t.Fatalf("TruncateAfter: %v", err)
	}
	entries, _ = store.ScanEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after truncate, got %d", len(entries))
	}

	// ScanAllShards（单文件模式下等同于 ScanEntries）
	all, err := store.ScanAllShards()
	if err != nil {
		t.Fatalf("ScanAllShards: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ScanAllShards: expected 1, got %d", len(all))
	}

	// SetActiveSession（noop，不报错）
	if err := store.SetActiveSession("any"); err != nil {
		t.Errorf("SetActiveSession should be noop: %v", err)
	}

	// ActiveShardPath 返回 indexFile
	if store.ActiveShardPath() != indexFile {
		t.Errorf("ActiveShardPath: expected %s, got %s", indexFile, store.ActiveShardPath())
	}

	// MigrateFromLegacy（noop，返回 nil）
	if err := store.MigrateFromLegacy("/nonexistent/file.jsonl"); err != nil {
		t.Errorf("MigrateFromLegacy should be noop: %v", err)
	}
}

// ---------- FileHistory 集成测试 ----------

// TestFileHistory_SetCurrentSession_ClearsCache 验证切换会话清空 indexCache（优化 3）
func TestFileHistory_SetCurrentSession_ClearsCache(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	// 默认会话写入 3 条 Track
	for i := 0; i < 3; i++ {
		if err := h.Track(fmt.Sprintf("msg-%d", i), fmt.Sprintf("tc-%d", i), "write_file", []string{file1}); err != nil {
			t.Fatalf("Track %d: %v", i, err)
		}
	}
	if len(h.indexCache) != 3 {
		t.Fatalf("expected 3 entries before switch, got %d", len(h.indexCache))
	}

	// 切换到新会话
	if err := h.SetCurrentSession("session-B"); err != nil {
		t.Fatalf("SetCurrentSession: %v", err)
	}

	// indexCache 应被清空（session-B 为空）
	if len(h.indexCache) != 0 {
		t.Errorf("expected 0 entries after switch, got %d", len(h.indexCache))
	}

	// trackedPaths / undoStack / redoStack 应被清空
	if len(h.trackedPaths) != 0 {
		t.Errorf("trackedPaths should be empty, got %d", len(h.trackedPaths))
	}
	if len(h.undoStack) != 0 {
		t.Errorf("undoStack should be empty, got %d", len(h.undoStack))
	}

	// 切换后再 Track 1 条
	if err := h.Track("msg-B1", "tc-B1", "write_file", []string{file1}); err != nil {
		t.Fatalf("Track after switch: %v", err)
	}
	if len(h.indexCache) != 1 {
		t.Errorf("expected 1 entry after Track in new session, got %d", len(h.indexCache))
	}

	// GetCurrentSession 应返回新会话
	if h.GetCurrentSession() != "session-B" {
		t.Errorf("GetCurrentSession: expected session-B, got %s", h.GetCurrentSession())
	}
}

// TestFileHistory_Rewind_RejectsForeignMsgId 验证 Rewind 拒绝跨会话 msgId（优化 4）
func TestFileHistory_Rewind_RejectsForeignMsgId(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	// 在默认会话创建快照
	h.Track("msg-A1", "tc-A1", "write_file", []string{file1})
	h.TrackAfter("msg-A1", "tc-A1", []string{file1})
	h.Snapshot("msg-A1", "first", 1)

	// 切换到 session-B
	if err := h.SetCurrentSession("session-B"); err != nil {
		t.Fatalf("SetCurrentSession: %v", err)
	}

	// Rewind 到 msg-A1（在 session-A 分片中，不在 session-B）→ 应返回错误
	_, err := h.Rewind("msg-A1", nil, "session-B")
	if err == nil {
		t.Fatal("Rewind should reject cross-session msgId")
	}
	if !strings.Contains(err.Error(), "cross-session rewind not supported") {
		t.Errorf("error should mention cross-session rewind: %v", err)
	}
}

// TestFileHistory_Track_FillsSessionId 验证 Track 自动填入 SessionId
func TestFileHistory_Track_FillsSessionId(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	// Track 前设置会话
	if err := h.SetCurrentSession("my-session"); err != nil {
		t.Fatalf("SetCurrentSession: %v", err)
	}

	// Track
	if err := h.Track("msg-1", "tc-1", "write_file", []string{file1}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// 从分片文件读取 entry，验证 SessionId
	entries, err := h.indexStore.ScanEntries()
	if err != nil {
		t.Fatalf("ScanEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].SessionId != "my-session" {
		t.Errorf("expected SessionId=my-session, got %s", entries[0].SessionId)
	}
}

// TestFileHistory_NewFileHistory_MigratesLegacyIndex 验证 NewFileHistory 自动迁移老索引
func TestFileHistory_NewFileHistory_MigratesLegacyIndex(t *testing.T) {
	workDir := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")

	// 计算 storageDir 路径
	storageDir := computeStorageDirForTest(t, workDir, baseDir)
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		t.Fatalf("mkdir storageDir: %v", err)
	}

	// 预置老索引文件，含 5 条记录
	legacyFile := filepath.Join(storageDir, "index.jsonl")
	for i := 0; i < 5; i++ {
		if err := appendEntry(legacyFile, makeTestEntry(fmt.Sprintf("msg-%d", i), "track")); err != nil {
			t.Fatalf("appendEntry %d: %v", i, err)
		}
	}

	// 调用 NewFileHistory 触发自动迁移
	h, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	// 验证老 index.jsonl 已被 rename（不存在）
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Errorf("legacy index.jsonl should not exist after migration: %v", err)
	}

	// 验证 index_legacy.jsonl 存在于 shardDir
	shardDir := filepath.Join(storageDir, "index")
	legacyShardPath := filepath.Join(shardDir, legacyFilename)
	if _, err := os.Stat(legacyShardPath); err != nil {
		t.Errorf("index_legacy.jsonl should exist in shardDir: %v", err)
	}

	// 验证 indexCache 加载了 5 条
	if len(h.indexCache) != 5 {
		t.Errorf("expected 5 entries in indexCache, got %d", len(h.indexCache))
	}

	// 验证当前会话为 "legacy"（迁移后）
	if h.GetCurrentSession() != legacySession {
		t.Errorf("expected current session=legacy, got %s", h.GetCurrentSession())
	}
}

// TestFileHistory_NewFileHistory_NoMigrationIfNoLegacy 验证无老索引时不迁移
func TestFileHistory_NewFileHistory_NoMigrationIfNoLegacy(t *testing.T) {
	workDir := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")

	// 不预置老索引文件
	h, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	// indexCache 应为空
	if len(h.indexCache) != 0 {
		t.Errorf("expected 0 entries, got %d", len(h.indexCache))
	}

	// 当前会话应为 "default"
	if h.GetCurrentSession() != defaultSession {
		t.Errorf("expected current session=default, got %s", h.GetCurrentSession())
	}

	// index_legacy.jsonl 不应存在
	storageDir := computeStorageDirForTest(t, workDir, baseDir)
	legacyShardPath := filepath.Join(storageDir, "index", legacyFilename)
	if _, err := os.Stat(legacyShardPath); err == nil {
		t.Error("index_legacy.jsonl should not exist when no legacy file")
	}
}

// ---------- 并发安全测试（-race） ----------

// TestShardedIndexStore_AppendEntry_ConcurrentSafe 验证多 goroutine 并发 AppendEntry 安全
func TestShardedIndexStore_AppendEntry_ConcurrentSafe(t *testing.T) {
	store := newTestShardedStore(t)
	store.SetActiveSession("session-concurrent")

	numGoroutines := 10
	entriesPerGoroutine := 5
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < entriesPerGoroutine; j++ {
				entry := IndexEntry{
					Type:       "track",
					MsgId:      fmt.Sprintf("msg-%d-%d", gid, j),
					ToolCallId: fmt.Sprintf("tc-%d-%d", gid, j),
					Timestamp:  time.Now(),
					Files:      []FileChange{{Path: "/test.go", Mode: 0644}},
				}
				if err := store.AppendEntry(entry); err != nil {
					t.Errorf("concurrent AppendEntry: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	// 验证所有记录都已写入
	entries, err := store.ScanEntries()
	if err != nil {
		t.Fatalf("ScanEntries: %v", err)
	}
	expected := numGoroutines * entriesPerGoroutine
	if len(entries) != expected {
		t.Errorf("expected %d entries, got %d", expected, len(entries))
	}
}

// TestShardedIndexStore_SetActiveSession_ConcurrentSafe 验证并发 SetActiveSession + AppendEntry 安全
func TestShardedIndexStore_SetActiveSession_ConcurrentSafe(t *testing.T) {
	store := newTestShardedStore(t)

	var wg sync.WaitGroup
	// 一组 goroutine 频繁切换会话
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sessionId := fmt.Sprintf("session-%d", idx%3)
			store.SetActiveSession(sessionId)
			store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-%d", idx), "track"))
		}(i)
	}
	wg.Wait()

	// 不发生 -race 报警即通过（不严格断言最终记录数，因 SetActiveSession 影响路由）
}

// TestShardedIndexStore_ScanAllShards_ConcurrentSafe 验证并发 ScanAllShards + AppendEntry 安全
func TestShardedIndexStore_ScanAllShards_ConcurrentSafe(t *testing.T) {
	store := newTestShardedStore(t)

	// 预写入一些数据
	store.SetActiveSession("session-A")
	for i := 0; i < 5; i++ {
		store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-A%d", i), "track"))
	}
	store.SetActiveSession("session-B")
	for i := 0; i < 3; i++ {
		store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-B%d", i), "track"))
	}

	var wg sync.WaitGroup

	// 并发 ScanAllShards
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ScanAllShards()
			if err != nil {
				t.Errorf("concurrent ScanAllShards: %v", err)
			}
		}()
	}

	// 并发 AppendEntry（在 session-C）
	store.SetActiveSession("session-C")
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := store.AppendEntry(makeTestEntry(fmt.Sprintf("msg-C%d", idx), "track")); err != nil {
				t.Errorf("concurrent AppendEntry: %v", err)
			}
		}(i)
	}

	wg.Wait()
	// 不发生 -race 报警即通过
}
