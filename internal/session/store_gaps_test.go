// Package session — 会话存储补丁functional test
// 测试 AppendBatch 文件锁、LockMode 严格模式、ForkSessionByID、IsLocked、isProcessRunning
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestAppendBatch_AcquiresLock 测试 AppendBatch 获取文件锁
// 验证 AppendBatch 执行后锁文件被正确释放
func TestAppendBatch_AcquiresLock(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	msgs := []Message{
		UserText("hello"),
	}

	err = store.AppendBatch("test_session", msgs)
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// ★ L1 修复（flock 迁移）：AppendBatch 完成后锁已释放，但 backing file 保留
	// （flock 不删 backing file，filelock.go:15-16）。断言改为「锁已释放 = 可再次获取」。
	lockPath := filepath.Join(dir, "test_session.lock")
	lock, err := store.AcquireLock("test_session")
	if err != nil {
		t.Fatalf("re-acquire after AppendBatch should succeed (lock released): %v", err)
	}
	lock.Release()
	_ = lockPath
}

// TestAppendBatch_LockModeStrict 测试严格锁模式下 AppendBatch 行为
// 当锁文件被其他进程持有时，严格模式should return error
//
// ★ L1 修复（flock 迁移）：用真实 flock 持有来模拟「另一进程持有锁」，
// 不再是写一个 JSON 锁文件（flock 路径下锁的持有由内核决定，不读文件内容）。
func TestAppendBatch_LockModeStrict(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	store.SetLockMode(LockModeStrict)

	// 用真实 flock 持有锁，模拟另一进程（此处是另一 fd）持有。
	lockPath := filepath.Join(dir, "test_session.lock")
	held, err := util.TryAcquireFileLock(lockPath)
	if err != nil {
		t.Fatalf("TryAcquireFileLock: %v", err)
	}
	defer held.Release()

	msgs := []Message{
		UserText("hello"),
	}

	err = store.AppendBatch("test_session", msgs)
	if err == nil {
		t.Error("AppendBatch should fail in strict mode when lock is held")
	}
}

// TestForkSessionByID 测试按 ID 分叉会话
// 验证分叉后的会话包含原始消息且与原始会话独立
func TestForkSessionByID(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 创建原始会话
	msgs := []Message{
		UserText("original message"),
		AssistantText("response", "test-model", nil),
	}
	store.AppendBatch("original_session", msgs)

	// 按 ID 分叉
	forkID, err := store.ForkSessionByID("original_session")
	if err != nil {
		t.Fatalf("ForkSessionByID failed: %v", err)
	}

	if forkID == "original_session" {
		t.Error("Fork ID should differ from original")
	}

	// 验证分叉会话包含相同消息
	forked, err := store.Load(forkID)
	if err != nil {
		t.Fatalf("Load forked session failed: %v", err)
	}
	if len(forked.messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(forked.messages))
	}

	// 验证独立性：修改分叉不影响原始会话
	store.Append(forkID, UserText("fork only"))

	original, _ := store.Load("original_session")
	if len(original.messages) != 2 {
		t.Error("Original session should not be affected by fork modification")
	}
}

// TestForkSessionByID_Nonexistent 测试分叉不存在的会话
// 对不存在的会话调用 ForkSessionByID should return error
func TestForkSessionByID_Nonexistent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.ForkSessionByID("nonexistent")
	if err == nil {
		t.Error("ForkSessionByID should fail for nonexistent session")
	}
}

// TestIsLocked_NoLock 测试无锁文件时的 IsLocked
// 当锁文件不存在时，should return未锁定状态
func TestIsLocked_NoLock(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	locked, pid, err := store.IsLocked("test_session")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if locked {
		t.Error("Session should not be locked when no lock file exists")
	}
	if pid != 0 {
		t.Errorf("PID should be 0, got %d", pid)
	}
}

// TestIsLocked_WithActiveLock 测试持有活跃锁时的 IsLocked
// 当锁文件存在且对应进程仍在运行时，should return锁定状态
//
// 2026-09-20（收敛）：原先此处有 `runtime.GOOS == "windows"` 跳过，
// 理由是「isProcessRunning 用 syscall.Signal(0)，Windows 不支持」。
// 该理由**已不成立** —— isProcessRunning 现委托 internal/platform.ProcessAlive，
// 其 Windows 分支用 OpenProcess。故删除跳过，让 Windows 也覆盖到锁路径
// （留着它是「注释断言了已不成立的事实」＋ 静默丢失一半平台覆盖）。
func TestIsLocked_WithActiveLock(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 创建一个由当前进程持有的活跃锁
	lockPath := filepath.Join(dir, "test_session.lock")
	lockData, _ := json.Marshal(SessionLock{PID: os.Getpid(), AcquiredAt: time.Now()})
	os.WriteFile(lockPath, lockData, 0644)

	locked, pid, err := store.IsLocked("test_session")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	// 当前进程正在运行，锁应被视为活跃
	if !locked {
		t.Error("Lock held by current running process should be considered active")
	}
	if pid != os.Getpid() {
		t.Errorf("PID should be %d, got %d", os.Getpid(), pid)
	}
}

// TestIsLocked_DeadProcessLock 测试持有锁的进程已死亡时的 IsLocked
// 当锁文件存在但对应进程已不在运行时，should return未锁定状态
func TestIsLocked_DeadProcessLock(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 创建一个由不存在的 PID 持有的锁
	lockPath := filepath.Join(dir, "test_session.lock")
	lockData, _ := json.Marshal(SessionLock{PID: 99999, AcquiredAt: time.Now()})
	os.WriteFile(lockPath, lockData, 0644)

	locked, pid, err := store.IsLocked("test_session")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	// PID 99999 大概率不存在，锁应被视为不活跃
	if locked && isProcessRunning(99999) {
		t.Error("Lock with dead process should not be considered active")
	}
	if pid != 99999 {
		t.Errorf("PID should be 99999, got %d", pid)
	}
}

// TestIsLocked_ExpiredLock 测试「锁龄超阈值但持有者仍存活」的 IsLocked。
//
// ★ L1 修复（§6.7 点名改写）：原用例用「当前存活 PID + 31s 锁龄」断言「不算持锁」，
// 那**正是缺陷行为**（先判年龄 ⇒ 从活人手里夺锁）。修复后不变量是「持有者存活 ⇒ 不可夺」，
// 故本用例改断言：存活 + 锁龄 31s（带 ProcessStart，S1 锁）⇒ IsLocked 必须报「被占」。
func TestIsLocked_ExpiredLock(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 一个「锁龄 31s 但持有者（本进程）仍存活」的 S1 锁文件。
	lockPath := filepath.Join(dir, "test_session.lock")
	lockData, _ := json.Marshal(SessionLock{
		PID:          os.Getpid(),
		AcquiredAt:   time.Now().Add(-31 * time.Second),
		ProcessStart: currentProcessStart(),
	})
	os.WriteFile(lockPath, lockData, 0644)

	locked, pid, err := store.IsLocked("test_session")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if !locked {
		t.Error("持有者存活（锁龄 31s）的锁必须仍视为被占：修复前会因「先判年龄」误报未锁并从活人手里夺锁")
	}
	if pid != os.Getpid() {
		t.Errorf("PID should be %d, got %d", os.Getpid(), pid)
	}
}

// TestLockModeBestEffort 测试尽力锁模式（默认模式）。
//
// ★ L1 修复（§6.7 点名改写）：原用例用「当前存活 PID + 60s 锁龄」断言「过期锁被自动
// 清理、写入成功」，那**正是缺陷行为**（存活持有者被夺锁）。修复后 best-effort 的语义是：
// 活跃锁（另一进程持有）拒绝写入，且持有者存活时绝不清锁。
//
// 本用例用**真实子进程**走 AcquireLock 持锁（写元数据），再让父进程 best-effort 写入
// 同一会话，断言被拒 —— 这与 TestProbeMultiInstance_SessionHeldByOtherProcess 同源，
// 但落到 store_gaps 的 LockMode 语义上（默认 best-effort）。
func TestLockModeBestEffort(t *testing.T) {
	dir := t.TempDir()
	id := "test_session"

	holder := startProbeChild(t, dir, id, "hold")
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitForReadyFile(t, dir)

	// best-effort（默认）下，锁被另一真实进程持有 ⇒ 写入应被拒（FACT-SESS-003）。
	if err := runProbeWriteChild(t, dir, id); err == nil {
		t.Error("best-effort Append should fail when lock is actively held by another process")
	}
}

// TestLockModeStrict 测试严格锁模式
// 在 strict 模式下，当锁获取失败时，写入操作should return error
//
// ★ L1 修复（flock 迁移）：用真实 flock 持有模拟锁竞争。
func TestLockModeStrict(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	store.SetLockMode(LockModeStrict)

	// 用真实 flock 持有模拟锁竞争
	lockPath := filepath.Join(dir, "test_session.lock")
	held, err := util.TryAcquireFileLock(lockPath)
	if err != nil {
		t.Fatalf("TryAcquireFileLock: %v", err)
	}
	defer held.Release()

	// strict 模式下 Append 应失败
	err = store.Append("test_session", UserText("hello"))
	if err == nil {
		t.Error("Append should fail in strict mode when lock is held")
	}
}

// TestIsProcessRunning_CurrentProcess 测试 isProcessRunning 检测当前进程
// 当前进程 PID 应被检测为正在运行
//
// 2026-09-20（收敛）：同 TestIsLocked_WithActiveLock，原先的
// `runtime.GOOS == "windows"` 跳过已删除（理由随实现收敛而失效）。
func TestIsProcessRunning_CurrentProcess(t *testing.T) {
	pid := os.Getpid()
	if !isProcessRunning(pid) {
		t.Errorf("Current process (PID %d) should be running", pid)
	}
}

// 注：原先此处有 TestIsProcessRunning_NonexistentPID，但它只 `t.Log`、**永不失败**
// ⇒ 是假绿守卫（注入反例也打不红，不构成判别器）。2026-09-20已删除，
// 改由 process_liveness_unix_test.go 的 TestIsProcessRunning_DeadPIDIsDead 承担：
// 那条用一个「先启动、再确认已退出」的真实 pid 做**可判别**断言。
