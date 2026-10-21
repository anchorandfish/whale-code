// Package session — L1（会话文件锁 P1）修复的判别器与迁移后行为测试。
//
// 对应第五批回执（方案 A，2026-09-19）§6 的 TDD 规格：
//   - 三臂对照（§6.1）：持有者存活 + 锁龄 0s / 同一存活 PID + 锁龄 31s / 死亡 PID，
//     断言的不变量是「活着的持有者不可被夺锁」。
//   - 崩溃释放（§6.3）：进程级证明 flock 随进程退出自动释放，不能用同进程 goroutine
//     模拟（fd 级会冲突 ⇒ 假绿）。
//
// ★ before 必须红：三臂里 Arm B（存活 + 31s）在修复前会被判「陈旧可夺」，这是
// 第四批回执 §2.2 已实测钉死的 P1 形态。修复后 Arm B 必须转为「有效不可夺」。
package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestL1_LiveHolderNeverStolen 是 L1 的**核心判别器**：持有者仍存活时，
// 无论锁龄多大，都不得被夺锁。三臂对照（§6.1）：
//
//	Arm A  存活 PID + 锁龄 0s   → 不可夺（Held）
//	Arm B  存活 PID + 锁龄 31s  → 不可夺（Held）★ 修复前这里被判陈旧可夺 = P1 本体
//	Arm C  死亡 PID + 锁龄 0s   → 可夺（Stale）
//
// 前提由被测包自己的 isProcessRunning() 自证（沿用第四批 §2.2 的方法论）。
//
// ★ 判定入口用 storeLockVerdict：这是「这个锁能不能被夺走」的 O_EXCL 判定语义，
// 是回退路径（网络卷）与 IsLocked 读侧共用的判据。flock 主路径不读文件内容判定
// 陈旧（由内核锁决定），故三臂对照钉的是 O_EXCL 判定路径的不变量 —— 修复前
// isProcessStillOwner「先判年龄」会把 Arm B 判成 Stale（P1 本体）。
func TestL1_LiveHolderNeverStolen(t *testing.T) {
	livePID := os.Getpid()
	deadPID := 999999
	// 前提自证：活的必须被被测包判为活、死的必须被判为死，否则本用例不构成判别器。
	if !isProcessRunning(livePID) {
		t.Fatalf("前置失败：当前进程 PID %d 未被 isProcessRunning 判为存活", livePID)
	}
	if isProcessRunning(deadPID) {
		t.Skipf("前置失败：PID %d 意外存活，无法构造死亡对照臂", deadPID)
	}

	arms := []struct {
		name      string
		pid       int
		age       time.Duration
		procStart int64 // 0 = legacy 锁（无 ProcessStart）
		wantHeld  bool  // true = 期望判 Held（不可夺）；false = 期望判 Stale（可夺）
	}{
		{"ArmA_存活PID_锁龄0s", livePID, 0, currentProcessStart(), true},
		{"ArmB_存活PID_锁龄31s", livePID, 31 * time.Second, currentProcessStart(), true},
		{"ArmC_死亡PID_锁龄0s", deadPID, 0, 0, false},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewStore(dir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			// 预置一个锁文件，锁龄 = age（AcquiredAt 回拨）。
			lockPath := filepath.Join(dir, SanitizeSessionID("l1")+".lock")
			data, _ := json.Marshal(SessionLock{
				PID:          arm.pid,
				AcquiredAt:   time.Now().Add(-arm.age),
				ProcessStart: arm.procStart,
			})
			if err := os.WriteFile(lockPath, data, 0o600); err != nil {
				t.Fatalf("预置锁文件: %v", err)
			}

			// 从判定入口 storeLockVerdict 判定：Held = 不可夺，Stale = 可夺。
			got := store.storeLockVerdict(lockPath)
			held := got == util.LockVerdictHeld
			stale := got == util.LockVerdictStale

			if arm.wantHeld && stale {
				t.Fatalf("持有者存活（PID=%d 锁龄=%v）被判 Stale（可夺）：违反不变量「锁有效性判定不得丢弃存活信息」（判定=%v）", arm.pid, arm.age, got)
			}
			if arm.wantHeld && !held {
				t.Fatalf("持有者存活（PID=%d 锁龄=%v）应判 Held，实为 %v", arm.pid, arm.age, got)
			}
			if !arm.wantHeld && !stale {
				t.Fatalf("死亡持有者（PID=%d）的陈旧锁应判 Stale，实为 %v", arm.pid, got)
			}
		})
	}
}

// TestL1_CrashReleaseIsProcessLevel 是崩溃释放的**进程级**证明（§6.3）：
// helper 子进程持锁 → SIGKILL → 父进程立即 AcquireLock 必须成功，且不等 30s。
//
// ★ 不能用同进程 goroutine 模拟：flock 是 fd 级，同进程另一 fd 也会冲突 ⇒ 假绿。
const (
	l1CrashChildEnv = "WHALE_L1_CRASH_CHILD"
	l1CrashDirEnv   = "WHALE_L1_CRASH_DIR"
)

// TestL1_CrashChild 是崩溃释放测试的子进程序体：持锁后停住，等待被 SIGKILL。
func TestL1_CrashChild(t *testing.T) {
	if os.Getenv(l1CrashChildEnv) != "1" {
		t.Skip("not a crash-test child")
	}
	dir := os.Getenv(l1CrashDirEnv)
	store, err := NewStore(dir)
	if err != nil {
		os.Exit(9)
	}
	lock, err := store.AcquireLock("crashy")
	if err != nil {
		os.Exit(3)
	}
	_ = lock
	// 通知父进程已持锁。
	_ = os.WriteFile(filepath.Join(dir, "crash.ready"), []byte("held"), 0o600)
	// 停住，等父进程 SIGKILL（不主动 Release ⇒ 只有内核在进程退出时释放锁）。
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

func TestL1_CrashReleaseIsProcessLevel(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=TestL1_CrashChild", "-test.v")
	cmd.Env = append(os.Environ(),
		l1CrashChildEnv+"=1",
		l1CrashDirEnv+"="+dir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动持锁子进程: %v", err)
	}

	// 等子进程落地持锁标记。
	ready := filepath.Join(dir, "crash.ready")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("持锁子进程未能在 10s 内获取锁")
	}

	// SIGKILL：不触发任何 cleanup（cleanup 只挂 SIGINT/SIGTERM），锁只能靠内核在进程退出时释放。
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// 父进程立即 AcquireLock，必须成功（不等 30s）。
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	start := time.Now()
	lock, err := store.AcquireLock("crashy")
	if err != nil {
		t.Fatalf("持有者被 SIGKILL 后，AcquireLock 仍失败（%v）：flock 未随进程退出释放", err)
	}
	lock.Release()
	if elapsed := time.Since(start); elapsed >= 25*time.Second {
		t.Fatalf("AcquireLock 耗时 %v，疑似仍在等待 30s 陈旧窗口，而非内核即时释放", elapsed)
	}
}
