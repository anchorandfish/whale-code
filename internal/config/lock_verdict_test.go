package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// F11 L4 判别器（`config` 侧）。
//
// 修复前 `isStaleLock` 自带二值分支：内容无法解析 ⇒ `return false`（提前 return）。
// 现在判定统一走 `util.LockVerdict`，「未知 ⇒ 不夺」由那份声明的 `MaySteal()` 保证。
//
// 反例注入：把 `util.LockVerdict.MaySteal()` 改成 Unknown 也放行
//（或把 `lockVerdict` 的损坏分支改回 `util.LockVerdictStale`）⇒ 下面
// `TestLockVerdict_CorruptedIsUnknownAndNotStale` /
// `TestLockVerdict_ZeroByteIsUnknown` / `TestAcquireLock_NeverStealsUnknownLock`
// **必须红**。

// TestLockVerdict_CorruptedIsUnknownAndNotStale 损坏（不可解析）内容 ⇒ 未知 ⇒ 不夺。
func TestLockVerdict_CorruptedIsUnknownAndNotStale(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "settings.json.lock")
	// 单行、无换行 ⇒ 解析不出 PID/时间戳行
	if err := os.WriteFile(lockPath, []byte("fake-lock-pid-12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := lockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("损坏锁的判定 = %v，必须是 Unknown（未知 ⇒ 不夺）", got)
	}
	if isStaleLock(lockPath) {
		t.Errorf("损坏锁被判为 stale（可夺）：同一失败产物下 config 与 session 会再次给出相反决策（F11 L4 本体）")
	}
}

// TestLockVerdict_ZeroByteIsUnknown 0 字节锁文件 ⇒ 未知 ⇒ 不夺。
//
// 这一形态在真实竞态里会出现（`O_EXCL` 建文件后、写入内容前的窗口），
// 必须与"内容不可解析"同解，而不是被当成"没有锁"。
func TestLockVerdict_ZeroByteIsUnknown(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "settings.json.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := lockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("0 字节锁的判定 = %v，必须是 Unknown", got)
	}
	if isStaleLock(lockPath) {
		t.Errorf("0 字节锁被判为 stale（可夺），必须为 false")
	}
}

// TestLockVerdict_ExpiredLockIsStale 正向守卫：真陈旧必须仍可夺（证明不是恒 Unknown）。
func TestLockVerdict_ExpiredLockIsStale(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "settings.json.lock")
	content := fmt.Sprintf("999999\n%d", time.Now().Add(-10*time.Minute).Unix())
	if err := os.WriteFile(lockPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := lockVerdict(lockPath); got != util.LockVerdictStale {
		t.Errorf("超时 10 分钟且 PID 不存在的锁判定 = %v，必须是 Stale", got)
	}
	if !isStaleLock(lockPath) {
		t.Errorf("真陈旧的锁必须可夺（证明判定未退化为恒不夺）")
	}
}

// TestLockVerdict_LiveHolderIsHeld 正向守卫：活着的持有者 ⇒ Held（不夺）。
func TestLockVerdict_LiveHolderIsHeld(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "settings.json.lock")
	content := fmt.Sprintf("%d\n%d", os.Getpid(), time.Now().Unix())
	if err := os.WriteFile(lockPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := lockVerdict(lockPath); got != util.LockVerdictHeld {
		t.Errorf("由存活进程持有且未超时的锁判定 = %v，必须是 Held", got)
	}
	if isStaleLock(lockPath) {
		t.Errorf("被存活进程持有的锁不得被判为 stale")
	}
}

// TestLockVerdict_MissingFileIsStale 锁文件不存在 ⇒ 可夺（调用方随即重建）。
func TestLockVerdict_MissingFileIsStale(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "nonexistent.json.lock")
	if got := lockVerdict(lockPath); got != util.LockVerdictStale {
		t.Errorf("锁文件不存在时判定 = %v，必须是 Stale", got)
	}
}

// TestAcquireLock_NeverStealsUnknownLock 走**生产入口**：损坏的锁文件绝不被删除/抢占。
//
// 这是"未知不夺"最直接的证据 —— 旧实现（`isStaleLock` 对不可解析内容 `return true`）
// 会 `os.Remove` 掉这个锁文件；现在的实现必须让它**原样留在磁盘上**。
//
// 代价说明：`acquireLock` 的阻塞上限是 5 秒（生产值），故本用例耗时约 5 秒，
// 与既有 `TestAcquireLock_UnparseableContent_Blocks` 同量级。
func TestAcquireLock_NeverStealsUnknownLock(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.json")
	lockPath := tmpFile + ".lock"
	original := []byte("fake-lock-pid-12345")
	if err := os.WriteFile(lockPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	release, err := acquireLock(tmpFile)
	if err == nil {
		release()
		t.Fatalf("未知态锁被抢占了（acquireLock 成功），必须失败：未知 ⇒ 不夺")
	}

	got, readErr := os.ReadFile(lockPath)
	if readErr != nil {
		t.Fatalf("锁文件被删除或不可读（%v）：未知态锁**不得**被夺 ⇒ 必须原样保留", readErr)
	}
	if string(got) != string(original) {
		t.Errorf("锁文件内容被改写：got %q, want %q —— 未知态锁必须原样保留", got, original)
	}
	if !strings.Contains(err.Error(), "acquire lock") {
		t.Errorf("错误文案丢了既有前缀（`acquire lock`），既有断言依赖它：%v", err)
	}
	// 未知态的根因提示必须出现，否则用户只看到 "acquire lock timeout" 而不知道
	// 真正原因是一个损坏的锁文件（I-3.4 记录的后果：只能人工删锁、且不知道删哪个）。
	if !strings.Contains(err.Error(), "无法解析") {
		t.Errorf("未知态锁的错误文案未说明根因，用户无从修复：%v", err)
	}
}
