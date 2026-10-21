// Package session — 会话级占用（HoldSession）的**进程内**不变量。
//
// 跨进程判别器在 `multi_instance_probe_test.go`（真实子进程 re-exec）；本文件钉两条
// 进程内必须成立、且各自对应一个真实踩坑点的事实：
//
// 1. 持有占用锁之后，**本进程自己的写入必须仍然成功且不卡**。
// flock 是 open-file-description 级 —— 同进程另开一个 fd 去 flock 会与**自己**
// 冲突。写路径若不跳过重复加锁，每笔写入都白等满 EffectiveSessionLockTimeout()
// （默认 2s）后才走 best-effort 兜底照常落盘（锁文件 PID 是自己 ⇒ 不算被他人
// 占用），用户感知为每回合多卡 2–6 秒。
// 判别位用**相对时间阈值**：坏实现总耗时 ≥ 3×锁超时（每笔各等满一次）；
// 好实现只花磁盘 I/O 本身。取「< 2×锁超时」为上限，与盘速解耦（实测本机
// 慢速卷三笔合计 ~1.5s，距上限还有一倍余量；不能再用固定 1s —— 会把「盘慢」
// 误判成「锁没跳过」）。另配**结构前置**：直接 AcquireLock 必须被拒，证明
// flock 真在互斥、白等超时这条路径真实存在。
//
// 2. 释放之后必须马上又能被别人占用 —— 否则「占用」就退化成一次性死锁。
package session

import (
	"errors"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestW097_HeldSessionStillAcceptsOwnWrites 持有占用锁后本进程自身写入不受影响。
func TestW097_HeldSessionStillAcceptsOwnWrites(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const id = "held-session"

	if err := store.HoldSession(id); err != nil {
		t.Fatalf("HoldSession 首次占用应成功: %v", err)
	}
	if got := store.HeldSession(); got != id {
		t.Fatalf("HeldSession 应回报 %q，实际 %q", id, got)
	}

	// 结构前置：另一 fd 再去 AcquireLock 必须被自己持有的 flock 拒掉 ——
	// 证明「不跳过 = 每笔写白等满超时」这个前提真实成立（flock 真在互斥）。
	// 若这里竟然能成功，说明锁根本没锁住，后面的耗时判据不可判别。
	if _, err := store.AcquireLock(id); err == nil {
		t.Fatal("前置条件失效：另一 fd 竟能再拿到锁 —— flock 未生效，判据不可判别")
	}

	start := time.Now()
	if err := store.Append(id, UserText("hello")); err != nil {
		t.Fatalf("持有占用锁后，本进程自己的 Append 被拒: %v", err)
	}
	if err := store.AppendBatchNamed(id, "别名", []Message{UserText("world")}); err != nil {
		t.Fatalf("持有占用锁后，本进程自己的 AppendBatchNamed 被拒: %v", err)
	}
	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("持有占用锁后，本进程自己的 Save 被拒: %v", err)
	}

	// 相对时间判别：不跳过重复加锁 ⇒ 三笔各白等满 timeout，合计 ≥ 3×timeout；
	// 正确实现只有磁盘 I/O 本身。上限取 2×timeout —— 盘慢一倍也不误报，
	// 而坏实现必然越线。
	if elapsed := time.Since(start); elapsed >= 2*util.EffectiveSessionLockTimeout() {
		t.Fatalf("持有后自身写入耗时 %v（≥2×锁超时 %v）—— 写路径没有跳过重复加锁，每笔都在白等满超时", elapsed, 2*util.EffectiveSessionLockTimeout())
	}

	// 幂等：重复占用同一会话不应报错，也不应把已持有的锁放掉。
	if err := store.HoldSession(id); err != nil {
		t.Fatalf("HoldSession 幂等性被破坏: %v", err)
	}
	if got := store.HeldSession(); got != id {
		t.Fatalf("幂等调用后 HeldSession 应为 %q，实际 %q", id, got)
	}
}

// TestW097_SecondHolderRefusedThenReleased 第二个持有者被拒；持有方释放后立刻可占。
//
// 「第二个持有者」用**另一个 Store 实例**表示：flock 是 fd 级，同进程另开一个 fd
// 与另一个进程的文件描述符在锁语义上等价（仓库既有注释已确认这一点，见
// l1_flock_migration_test.go 的「同进程另一 fd 也会冲突」）。
func TestW097_SecondHolderRefusedThenReleased(t *testing.T) {
	dir := t.TempDir()
	const id = "held-session"

	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(first): %v", err)
	}
	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(second): %v", err)
	}

	if err := first.HoldSession(id); err != nil {
		t.Fatalf("first.HoldSession 应成功: %v", err)
	}

	err = second.HoldSession(id)
	if err == nil {
		t.Fatal("会话已被占用，第二个持有者仍然占用成功 —— 占用闸门没有生效（多开同一会话拦不住）")
	}
	var inUse *SessionInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("应返回 *SessionInUseError（可向用户明示并要求换会话的终态），实际 %T: %v", err, err)
	}
	if inUse.SessionID != id {
		t.Fatalf("SessionInUseError.SessionID 应为 %q，实际 %q", id, inUse.SessionID)
	}
	if second.HeldSession() != "" {
		t.Fatal("占用被拒后，第二个持有者不应记录任何已持有会话")
	}

	first.ReleaseHeldSession()
	if got := first.HeldSession(); got != "" {
		t.Fatalf("释放后 HeldSession 应为空串，实际 %q", got)
	}
	if err := second.HoldSession(id); err != nil {
		t.Fatalf("持有方释放后，第二个持有者应能占用，实际: %v", err)
	}
	second.ReleaseHeldSession()
}

// TestW097_SwitchReleasesPrevious 切会话时释放上一个占用（同一进程只占一个会话）。
func TestW097_SwitchReleasesPrevious(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.HoldSession("session-a"); err != nil {
		t.Fatalf("HoldSession(a): %v", err)
	}
	if err := store.HoldSession("session-b"); err != nil {
		t.Fatalf("HoldSession(b): %v", err)
	}
	if got := store.HeldSession(); got != "session-b" {
		t.Fatalf("切换后应只持有 session-b，实际 %q", got)
	}

	// a 必须已解锁：另一个持有者立刻能占用它。
	other, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(other): %v", err)
	}
	if err := other.HoldSession("session-a"); err != nil {
		t.Fatalf("切走后 session-a 应立刻可被占用，实际: %v", err)
	}
	other.ReleaseHeldSession()

	// 同一个 Store 开新会话（清空名字）后，旧占用也应释放。
	store.ReleaseHeldSession()
	third, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(third): %v", err)
	}
	if err := third.HoldSession("session-b"); err != nil {
		t.Fatalf("释放后 session-b 应可被占用，实际: %v", err)
	}
	third.ReleaseHeldSession()
}
