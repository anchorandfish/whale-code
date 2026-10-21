//go:build !windows

package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 判别器 —— 原缺陷域本体是 internal/session/process.go 的 `isProcessRunning`：
//
//	proc, err := os.FindProcess(pid) // Unix 上永不失败（不查存在性）
//	if err != nil { return false }
//	return proc.Signal(syscall.Signal(0)) == nil // EPERM ⇒ 判死 ✗
//
// 本文件的两条用例分别覆盖「直接判据」与「生产入口」两层面。

// epermAvailable 报告本机是否具备 EPERM 标本的判别性。
//
// 标本 pid=1（init/launchd）：一定存在；对非 root 一律 EPERM
// （本机 2026-09-20 实测 uid=501 ⇒ kill(1,0) -> EPERM）。
// 以 root 运行时 kill(1,0) 成功 ⇒ 本用例**无法区分**新旧实现 ⇒ 必须 Skip，
// 否则就是"看起来在守、其实守不住"的假绿。
func epermAvailable(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(1, 0); err == nil {
		t.Skip("当前以 root 运行：kill(1,0) 不返回 EPERM ⇒ 本用例失去判别性，跳过而非假绿")
	}
}

// TestIsProcessRunning_EPERMIsAlive 直接钉住判据：EPERM ⇒ alive。
func TestIsProcessRunning_EPERMIsAlive(t *testing.T) {
	epermAvailable(t)

	if !isProcessRunning(1) {
		t.Errorf("isProcessRunning(1) = false，但 pid 1 存在、只是本进程无权发信号（EPERM）⇒ 必须判 alive。\n" +
			"  修复前此处判死，正是  本体。")
	}
}

// TestIsLocked_EPERMHolderKeepsLock 走**生产入口**（不是直接调私有函数）：
//
//	Store.IsLocked → isProcessStillOwner(store.go:1192) → isProcessRunning
//
// 目的：证明「锁持有者存在但不可发信号」时锁被判**活跃**，因而**绝不夺锁**。
// 修复前的判死会让 isProcessStillOwner 返回 false ⇒ AcquireLock 走抢占分支
// ⇒ 两个会话同时写同一份会话存档（与 F11 L1「误夺锁 ⇒ 数据丢失」同向）。
func TestIsLocked_EPERMHolderKeepsLock(t *testing.T) {
	epermAvailable(t)

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	lockPath := filepath.Join(dir, "test_session.lock")
	lockData, err := json.Marshal(SessionLock{PID: 1, AcquiredAt: time.Now()})
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(lockPath, lockData, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	locked, pid, err := store.IsLocked("test_session")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if !locked {
		t.Errorf("锁持有者 PID=1 存在但不可发信号（EPERM）⇒ 必须判「锁仍活跃」；\n" +
			"  判死会走夺锁分支（isProcessStillOwner → AcquireLock 抢占）⇒ 与 F11 L1 数据丢失同向。")
	}
	if pid != 1 {
		t.Errorf("PID should be 1, got %d", pid)
	}
}

// TestIsProcessRunning_DeadPIDIsDead 反向守卫，替代原先被删掉的假绿用例
// （TestIsProcessRunning_NonexistentPID：只用 `99999 大概率不存在` 且只 t.Log、永不失败）。
//
// 这里用「先启动、再 wait 到退出」的真实 pid：wait 返回后进程已被回收，pid 切实不存在
// ⇒ 注入「判据恒返回 true」的反例必红。
func TestIsProcessRunning_DeadPIDIsDead(t *testing.T) {
	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动辅助进程，跳过: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("等待辅助进程退出失败: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessRunning(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("isProcessRunning(%d) 在进程 wait 退出 2 秒后仍为 true（应判 dead）。", pid)
}
