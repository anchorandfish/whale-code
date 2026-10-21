// Package bridge — M21 扩展功能测试
package bridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestJtiCachePersisted_LoadSave 测试 JtiCache 持久化加载/保存（M21-01）。
func TestJtiCachePersisted_LoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jti_cache.json")

	cache := NewJtiCachePersisted(path)

	// 记录 jti
	exp := time.Now().Add(1 * time.Hour).Unix()
	if cache.IsReplayed("jti-1", exp) {
		t.Error("first use of jti-1 should not be replayed")
	}

	// 保存
	if err := cache.Save(); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// 新建 cache 并加载（模拟重启）
	cache2 := NewJtiCachePersisted(path)
	if err := cache2.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}

	// 同一 jti 应被识别为重放
	if !cache2.IsReplayed("jti-1", exp) {
		t.Error("jti-1 should be replayed after load from disk")
	}
}

// TestJtiCachePersisted_EmptyPath 测试空路径降级（M21-01）。
func TestJtiCachePersisted_EmptyPath(t *testing.T) {
	cache := NewJtiCachePersisted("")
	if err := cache.Load(); err != nil {
		t.Errorf("Load with empty path should be no-op, got %v", err)
	}
	if err := cache.Save(); err != nil {
		t.Errorf("Save with empty path should be no-op, got %v", err)
	}
}

// TestJtiCachePersisted_ExpiredNotLoaded 测试过期条目不加载（M21-01）。
func TestJtiCachePersisted_ExpiredNotLoaded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jti_cache.json")

	cache := NewJtiCachePersisted(path)
	// 记录已过期的 jti（exp 为过去时间）
	pastExp := time.Now().Add(-1 * time.Hour).Unix()
	cache.IsReplayed("expired-jti", pastExp)
	if err := cache.Save(); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// 加载后过期 jti 不应存在
	cache2 := NewJtiCachePersisted(path)
	if err := cache2.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	// 过期 jti 加载后应被视为未使用（因为未加载）
	if cache2.IsReplayed("expired-jti", pastExp) {
		t.Error("expired jti should not be loaded from disk")
	}
}

// TestWSTransport_ConnectClose 测试 WS 传输连接/关闭（M21-03）。
func TestWSTransport_ConnectClose(t *testing.T) {
	tm := NewMockWSTransport()
	if err := tm.Connect(nil); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	if !tm.IsConnected() {
		t.Error("expected connected after Connect")
	}
	if err := tm.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if tm.IsConnected() {
		t.Error("expected disconnected after Close")
	}
}

// TestWSTransport_SendRecv 测试 WS 传输发送/接收（M21-03）。
func TestWSTransport_SendRecv(t *testing.T) {
	tm := NewMockWSTransport()
	tm.Connect(nil)

	// 注入接收消息
	tm.EnqueueRecv(&WSMessage{Type: "request", Method: "test", ID: 1})

	msg, err := tm.Recv()
	if err != nil {
		t.Fatalf("Recv error: %v", err)
	}
	if msg.Method != "test" {
		t.Errorf("expected method 'test', got %q", msg.Method)
	}

	// 发送消息
	if err := tm.Send(&WSMessage{Type: "response", ID: 1}); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	sent := tm.SentMsgs()
	if len(sent) != 1 {
		t.Errorf("expected 1 sent msg, got %d", len(sent))
	}
}

// TestWSTransport_HTTPConnect 测试 httpWSTransport 连接（M21-03）。
// B-2 修复：ws:// / wss:// URL 在构造期 fail-fast，使用 http:// 降级模式测试。
func TestWSTransport_HTTPConnect(t *testing.T) {
	tm, err := NewWSTransport(WSConfig{URL: "http://localhost:8080/bridge"})
	if err != nil {
		t.Fatalf("NewWSTransport error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := tm.Connect(ctx); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	if !tm.IsConnected() {
		t.Error("expected connected")
	}
	tm.Close()
}

// TestAuditLogger_File 测试文件审计日志（M21-05）。
func TestAuditLogger_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	logger := NewFileAuditLogger(path)

	// 记录成功条目
	LogAuditSuccess(logger, AuditOpSetToken, "jti-1", "sk-***1234", "local", "token set")
	LogAuditFailure(logger, AuditOpVerify, "jti-2", "sk-***5678", "remote", "invalid signature")
	LogAuditDenied(logger, AuditOpRefresh, "jti-3", "sk-***9012", "bridge", "replay detected")

	// 查询全部
	entries, err := logger.Query("", 0)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	// 按 operation 查询
	verifyEntries, _ := logger.Query(AuditOpVerify, 0)
	if len(verifyEntries) != 1 {
		t.Errorf("expected 1 verify entry, got %d", len(verifyEntries))
	}
	if verifyEntries[0].Result != AuditResultFailure {
		t.Errorf("expected failure result, got %s", verifyEntries[0].Result)
	}
}

// TestAuditLogger_Memory 测试内存审计日志（M21-05）。
func TestAuditLogger_Memory(t *testing.T) {
	logger := NewFileAuditLogger("") // 空路径降级到内存

	LogAuditSuccess(logger, AuditOpSetToken, "jti-1", "sk-***", "local", "")
	LogAuditFailure(logger, AuditOpVerify, "jti-2", "sk-***", "remote", "")

	entries, _ := logger.Query("", 0)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

// TestAuditLogger_Cleanup 测试审计日志清理（M21-05）。
//
// ★ 2026-09-15 有意翻转（决策项 C5 / S13-HG Q1=A 的派生硬要求）：
// 原断言期望"逐条过滤后只剩 1 条"（read→filter→rename 整文件重写）。
// 新语义：`Cleanup` **不得改写历史字节**，保留策略只在**整文件粒度**上生效 ——
// - 当前文件整体未过期 ⇒ 字节逐字节不变（旧条目留在文件里是预期，不是缺陷）；
// - 当前文件整体过期 / 归档文件日期过期 ⇒ **整个文件**删除。
//
// 逐条保留在此**故意不做**：那需要先读文件、再重写文件，与 append-only 直接冲突。
// 判别力：把 `Cleanup` 换回整文件重写 ⇒ 本用例打红。
func TestAuditLogger_Cleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	logger := NewFileAuditLogger(path)

	// 记录一个很旧的条目（手动构造时间戳）
	logger.Log(&AuditEntry{
		Timestamp: time.Now().Add(-60 * 24 * time.Hour).UnixMilli(), // 60 天前
		Operation: AuditOpSetToken,
		Result:    AuditResultSuccess,
	})
	// 记录一个新条目
	LogAuditSuccess(logger, AuditOpVerify, "jti-new", "sk-***", "local", "")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// 清理（保留 30 天）
	if err := logger.Cleanup(); err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("当前文件尚未整体过期，Cleanup 不得删除它: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Cleanup 改写了历史字节（append-only 已破）:\n before=%q\n after =%q", before, after)
	}

	// 两条都还在（append-only：不按条目过滤），顺序也不变。
	entries, _ := logger.Query("", 0)
	if len(entries) != 2 {
		t.Fatalf("append-only 下当前文件条目数不应变化，got %d", len(entries))
	}
	if entries[0].Operation != AuditOpVerify || entries[1].Operation != AuditOpSetToken {
		t.Errorf("条目顺序被改动: %s / %s", entries[0].Operation, entries[1].Operation)
	}
}
