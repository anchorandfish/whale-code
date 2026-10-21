package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// F11 L4 判别器（`session` 侧）—— 与 `config/lock_verdict_test.go` 成对。
//
// 修复前 `isLockStale` 对内容损坏 / PID 为 0 一律 `return true`（**立即夺锁**），
// 与 `config.isStaleLock` 对同一失败产物判「被持有」相反。现两侧共用同一份声明
// `util.LockVerdict`，「未知 ⇒ 不夺」由 `MaySteal()` 唯一放行 Stale 来保证。
//
// 反例注入：把未知并入可夺（`MaySteal` 放行 Unknown，或把 `storeLockVerdict`
// 的损坏分支改回 `util.LockVerdictStale`）⇒ 本文件全部用例 **必须红**。

func newTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return store
}

// TestStoreLockVerdict_CorruptedIsUnknownAndNotStealable 损坏（不可解析）⇒ 未知 ⇒ 不夺。
func TestStoreLockVerdict_CorruptedIsUnknownAndNotStealable(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	lockPath := filepath.Join(dir, "s.lock")
	if err := os.WriteFile(lockPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.storeLockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("损坏锁的判定 = %v，必须是 Unknown（未知 ⇒ 不夺）", got)
	}
	stale, err := store.isLockStale(lockPath)
	if err != nil {
		t.Fatalf("isLockStale: %v", err)
	}
	if stale {
		t.Errorf("损坏锁被判为 stale（可夺）：这正是 F11 L4 —— 与 config 侧相反、会立即夺锁覆盖他人会话")
	}
}

// TestStoreLockVerdict_ZeroByteIsUnknown 0 字节（半截锁）⇒ 未知 ⇒ 不夺。
//
// 修复前注释把它写成「Empty PID is treated as corrupted」后仍 `return true`（夺）。
func TestStoreLockVerdict_ZeroByteIsUnknown(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	lockPath := filepath.Join(dir, "s.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.storeLockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("0 字节锁的判定 = %v，必须是 Unknown", got)
	}
	// 第二层：不仅判定值要对，**放行结论**也要对（两层都断言，避免只验中间值）。
	if stale, err := store.isLockStale(lockPath); err != nil || stale {
		t.Errorf("0 字节锁 isLockStale = (%v, %v)，必须为 (false, nil)：未知 ⇒ 不夺", stale, err)
	}
}

// TestStoreLockVerdict_ZeroPIDIsUnknown 合法 JSON 但 PID=0（写入中途被打断）⇒ 未知 ⇒ 不夺。
func TestStoreLockVerdict_ZeroPIDIsUnknown(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	lockPath := filepath.Join(dir, "s.lock")
	data, _ := json.Marshal(SessionLock{PID: 0, AcquiredAt: time.Now()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.storeLockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("PID=0 的半截锁判定 = %v，必须是 Unknown（修复前是 Stale ⇒ 立即夺锁）", got)
	}
	// 第二层：放行结论（修复前此处会对同一个文件 `return true` = 夺）
	if stale, err := store.isLockStale(lockPath); err != nil || stale {
		t.Errorf("PID=0 半截锁 isLockStale = (%v, %v)，必须为 (false, nil)：未知 ⇒ 不夺", stale, err)
	}
}

// TestStoreLockVerdict_LiveHolderIsHeld 正向守卫：活着的持有者 ⇒ Held（不夺）。
func TestStoreLockVerdict_LiveHolderIsHeld(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	lockPath := filepath.Join(dir, "s.lock")
	data, _ := json.Marshal(SessionLock{PID: os.Getpid(), AcquiredAt: time.Now()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.storeLockVerdict(lockPath); got != util.LockVerdictHeld {
		t.Errorf("被本进程持有的新锁判定 = %v，必须是 Held", got)
	}
}

// TestStoreLockVerdict_DeadHolderIsStale 正向守卫：真陈旧必须仍可夺（证明不是恒 Unknown）。
func TestStoreLockVerdict_DeadHolderIsStale(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	lockPath := filepath.Join(dir, "s.lock")
	data, _ := json.Marshal(SessionLock{PID: 99999, AcquiredAt: time.Now()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.storeLockVerdict(lockPath); got != util.LockVerdictStale {
		t.Errorf("持有者已退出的锁判定 = %v，必须是 Stale", got)
	}
}

// TestAcquireLock_NeverStealsCorruptedLock 走**生产入口**：损坏的锁绝不被删除、不被抢占。
//
// 这是"未知不夺"最硬的证据：旧实现会 `os.Remove(lockPath)` 后重建（夺锁），
// 现在必须原样保留 + 返回错误。
//
// ★ L1 修复（flock 迁移）：本判别器针对的是 **O_EXCL 回退路径**（网络共享卷）——
// flock 主路径下「损坏的锁文件」不构成持有（内核锁才是权威，损坏 JSON 会被 flock
// 直接覆盖，这正是迁移后的正确行为）。故本用例显式把 Store 置为 networkFS 走回退路径。
func TestAcquireLock_NeverStealsCorruptedLock(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	store.networkFS = true // 走 O_EXCL 回退路径（本判别器针对该路径的「未知不夺」）
	lockPath := filepath.Join(dir, "s.lock")
	original := []byte("{not-json")
	if err := os.WriteFile(lockPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AcquireLock("s"); err == nil {
		t.Fatalf("损坏的锁被抢占了（AcquireLock 成功），必须失败：未知 ⇒ 不夺")
	}

	got, readErr := os.ReadFile(lockPath)
	if readErr != nil {
		t.Fatalf("锁文件被删除（%v）：未知态锁**不得**被夺 ⇒ 必须原样保留", readErr)
	}
	if string(got) != string(original) {
		t.Errorf("锁文件被改写：got %q, want %q", got, original)
	}
}

// TestIsLocked_CorruptedLockReportsLocked 与写入侧**同源**（：同一事实跨入口必须一致）。
//
// 修复前 `IsLocked` 对损坏锁返回 (false, 0, nil)="未锁"，同时 `AcquireLock` 判"陈旧可夺"
// —— 两者恰好同向（都放行）。若只改 `AcquireLock`，两者就会给出**相反**答案。
// 现在两侧都走 `storeLockVerdict`：Unknown ⇒ 写入被拒 ⇒ 查询必须报「被占」。
func TestIsLocked_CorruptedLockReportsLocked(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	lockPath := filepath.Join(dir, "test_session.lock")
	if err := os.WriteFile(lockPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, pid, err := store.IsLocked("test_session")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if !locked {
		t.Errorf("损坏的锁文件：IsLocked 报「未锁」，而 AcquireLock 会拒绝写入 ⇒ 同一事实两个相反答案")
	}
	if pid != 0 {
		t.Errorf("损坏锁无法指名持有者，pid 应为 0，got %d", pid)
	}
}
