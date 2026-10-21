//go:build windows

// process_windows_test.go — 批2 诊断判别器（Windows 真机分步取证）。
//
// 背景：CI 实证「子进程持锁时，另一进程 best-effort 写入仍成功（write-ok）」。
// 轮 2 诊断 [A] 一击命中根因：Windows 的 LockFileEx 独占锁覆盖整个锁文件，
// 其他进程 `os.ReadFile` 它报 sharing violation；而 `isLockHeldByActiveProcess`
// 旧逻辑把「读不到」当成「无人持有」→ 放行写入。POSIX flock 不阻止读，所以
// 这条只在 Windows 触发。修复 = 读失败且非 NotExist ⇒ fail-closed 视为被持有。
//
// 本测试保留为**回归判别器**：
// - [A] 实证 Windows 锁语义（sharing violation 是预期，不是故障）；
// - [B] ProcessAlive(holder)；
// - [C] getProcessStartTime API 可用；
// - [D] 聚合谓词 isLockHeldByActiveProcess == true（修复本体，before 红）；
// - [E] 端到端：写者子进程必须被拒。
package session

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestWindowsDiag_LockHolderLivenessChain(t *testing.T) {
	dir := t.TempDir()
	id := "diag-lock-chain"

	holder := startProbeChild(t, dir, id, "hold")
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitForReadyFile(t, dir)

	// 持有者 PID（hold 子进程写进 ready 文件）
	raw, err := os.ReadFile(filepath.Join(dir, probeReadyFile))
	if err != nil {
		t.Fatalf("[?] ready 文件读不到: %v", err)
	}
	holderPID, perr := strconv.Atoi(string(raw))
	if perr != nil || holderPID <= 0 {
		t.Fatalf("[?] ready 文件内容不是有效 PID: %q", string(raw))
	}

	lockPath := filepath.Join(dir, SanitizeSessionID(id)+".lock")

	// [A] Windows 锁语义实证：锁文件存在但**不可读**（sharing violation）。
	if _, err := os.ReadFile(lockPath); err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("[A] 锁文件不存在 —— hold 子进程没锁上")
		}
		t.Logf("[A] 锁文件不可读（预期，Windows LockFileEx 全区间独占）: %v", err)
	} else {
		t.Logf("[A] 锁文件可读（未触发锁语义，记录备查）")
	}

	// [B] 判活第一环：OpenProcess(SYNCHRONIZE)
	if !isProcessRunning(holderPID) {
		t.Fatalf("[B] ProcessAlive(%d)=false —— 持有者明明活着，判活第一环已断", holderPID)
	}

	// [C] start-time API 可用（PID 复用守卫的输入）
	start, err := getProcessStartTime(holderPID)
	if err != nil || start <= 0 {
		t.Fatalf("[C] getProcessStartTime(%d) = (%d, %v) —— API 不可用（主路径失败且 wmic fallback 在 Server 2025 缺失？）", holderPID, start, err)
	}

	// [D] 聚合谓词：与 Append 的 best-effort 分支同谓词。
	// 修复前此处 false（读锁文件 sharing violation 被当成"无人持有"）⇒ before 红。
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("[D] NewStore: %v", err)
	}
	if !store.isLockHeldByActiveProcess(id) {
		t.Fatalf("[D] isLockHeldByActiveProcess=false —— A/B/C 各环均通，聚合判定仍 false；检查 ReadFile 错误分类分支")
	}

	// [E] 端到端：写者子进程必须被拒（与三个生产红测试同一行为面）。
	if err := runProbeWriteChild(t, dir, id); err == nil {
		t.Fatalf("[E] 会话被活跃进程持有时写入仍成功 —— 修复未生效")
	}
}
