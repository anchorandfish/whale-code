// Package session — L1 NFS/共享卷回退路径测试（§6.4）。
//
// 真实 NFS 无法在 CI 造 ⇒ 本用例证明的是**分支接线**，不是 NFS 行为：
//   - networkFS=true 时走 O_EXCL 回退路径（不 flock）；
//   - 回退路径上「存活即被夺锁」已被 §5.5.2 一并修掉。
//
// ⚠️ 勿把「分支被走到」当成「NFS 上正确」——NFS 的真实 flock 行为（本地成功但跨主机
// 无互斥）只能靠真实共享卷验证，本用例不声称覆盖。
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestL1_NetworkFSFallback_NoStealFromLiveHolder 回退路径（网络卷）同样不夺活人锁。
//
// networkFS=true ⇒ AcquireLock 走 O_EXCL 回退；此时「存活 + 锁龄 31s（带 ProcessStart）」
// 的锁必须仍被判 Held（不夺）—— §5.5.2 的年龄降级兜底对回退路径同样生效。
func TestL1_NetworkFSFallback_NoStealFromLiveHolder(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.networkFS = true // 强制回退路径

	livePID := os.Getpid()
	if !isProcessRunning(livePID) {
		t.Fatalf("前置失败：当前进程 PID %d 未判存活", livePID)
	}

	// 存活 + 31s 锁龄 + ProcessStart（S1 锁）⇒ 回退路径必须判 Held，不得夺锁。
	lockPath := filepath.Join(dir, "nfs.lock")
	data, _ := json.Marshal(SessionLock{
		PID:          livePID,
		AcquiredAt:   time.Now().Add(-31 * time.Second),
		ProcessStart: currentProcessStart(),
	})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatalf("预置锁文件: %v", err)
	}

	if _, err := store.AcquireLock("nfs"); err == nil {
		t.Fatalf("回退路径（网络卷）：持有者存活 + 锁龄 31s 却被夺锁成功 —— §5.5.2 未对回退路径生效")
	}
}

// TestL1_NetworkFSFallback_UsesOExcl 回退路径不用 flock：用 O_EXCL 的锁文件语义。
//
// 判据：networkFS=true 时，一个「损坏的锁文件」会让 AcquireLock 失败（Unknown ⇒ 不夺，
// O_EXCL 回退语义）；而 flock 主路径（networkFS=false）下损坏 JSON 不构成持有、直接成功。
func TestL1_NetworkFSFallback_UsesOExcl(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.networkFS = true

	lockPath := filepath.Join(dir, "s.lock")
	if err := os.WriteFile(lockPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AcquireLock("s"); err == nil {
		t.Fatalf("回退路径下损坏锁被抢占（应 Unknown ⇒ 不夺），说明 O_EXCL 回退未生效")
	}
	// 损坏锁原样保留（未删、未改写）。
	got, _ := os.ReadFile(lockPath)
	if string(got) != "{not-json" {
		t.Fatalf("回退路径损坏锁被改写/删除：got %q", got)
	}
}

// TestL1_NetworkFSFallback_ReleaseRemovesFile 回退路径 Release 走 os.Remove（held==nil）。
//
// flock 主路径 Release 只解锁不删 backing file；回退路径（held==nil）则删锁文件。
func TestL1_NetworkFSFallback_ReleaseRemovesFile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.networkFS = true

	lock, err := store.AcquireLock("s")
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if lock.held != nil {
		t.Fatalf("回退路径 lock.held 应为 nil（O_EXCL 无内核锁句柄）")
	}
	lockPath := filepath.Join(dir, "s.lock")
	lock.Release()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("回退路径 Release 应删除锁文件（held==nil 走 os.Remove）")
	}
}
