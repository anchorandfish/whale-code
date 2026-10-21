//go:build darwin

package sandbox

// ── B-2 判据：whale 自己的降级说明不得借道**目标进程的 stderr** ──────────────
//
// 机制（先看清，别急着删）：rlimit helper 是 whale **自己的二进制 re-exec**
// （os_sandbox.go:148 RlimitHelperArg；os_sandbox_darwin_rlimit.go:70 把 cmd.Path 改成 exePath），
// helper setrlimit 后 syscall.Exec 目标命令（:120）
// ⇒ **helper 的 stderr 被目标进程继承**。于是 whale 自己的诊断
//（"RLIMIT_AS skipped on darwin …"）被 Monitor 当成"子进程输出"回显给用户。
// 真机实证：2026-09-30 I-7.1 —— 用户跑 `node server.js`，输出里混进两条
// `📡 [mon_1] [stderr] whale-rlimit-helper: RLIMIT_AS/NPROC skipped on darwin (…)`
//
// 定位（分类轴选错）：'macOS 平台不支持 RLIMIT_AS/NPROC' 是**平台能力事实**，
// 属于**沙箱初始化**（Enter），不属于"命令执行"（helper）。写在 helper 里 =
// 把同一个平台事实重复到每一次命令执行，并且写在**别人的** stderr 上。
//
// 唯一正确出口**已存在**：os_sandbox_darwin.go:364 Enter() 的 audit.LogDegradation
// （持久文件 + 内存计数 + 启动 banner 经 DegradeReasons() 可见）。
// 覆盖性可证（R1）：
// MemoryMaxBytes>0 ∨ ProcessMax>0
// ⟹ ResourceLimits 非零
// ⟹ manager.Prepare 必经 osSandbox.Enter()（manager.go:218）
// ⟹ os_sandbox_darwin.go:363 的 if 成立 ⟹ 该审计必被写。
// （反方向：helper 的 wrapWithRlimitHelper 在 limits == 零值时直接 return（:44-46）
// ⇒ 不会出现"helper 写了而 Enter 没记"的窗口。）
//
// 判据：
// A（可判别）目标命令 stderr **不含** whale 自身诊断 ⇒ before 红（现状含）/ after 绿
// B（正向留痕）同一命令**确实跑起来了** ⇒ 防"命令没启动"把 A 蒙对
// C（护栏）审计通道仍记录该降级 ⇒ 防将来"顺手删出口"导致静默降级
// ⚠️ C **不具判别性**（before/after 均绿，该审计改动前就已存在），
// 它是"出口未消失"的守卫，不是本次改动的判别器 —— 按本仓 R4-7 纪律显式标注。
//
// ⚠️ 执行面说明（R6c：Skip ≠ 通过）：受限 runner 中 `sandbox-exec` 的
// `sandbox_apply` 被内核拒（EPERM，实测 exit 71）⇒ **完整链**（经 Restrict 的
// seatbelt + rlimit 组合）在本 runner 只能 SKIP。因此判据 A 的主用例走**直连生产 helper**
// （与既有 TestRlimitHelperDirect_NoSeatbelt 同一手法）——
// `wrapWithRlimitHelper` 正是 Restrict 内部调用的那个生产函数，
// helper 的 re-exec 与 **stderr 继承关系**在两条路径上完全一致 ⇒
// 判据的**观测对象不变**，故该直连路径对判据 A 有效。
// 完整链另设一条用例（正常终端下不 SKIP）作为交叉确认。

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/testutil"
)

// b2RlimitProbeLimits 携带 MemoryMaxBytes/ProcessMax ⇒ 改动前这两条
// **无条件**的降级说明（os_sandbox_darwin_rlimit.go:152-157）会被写进目标命令 stderr。
func b2RlimitProbeLimits() ResourceLimits {
	return ResourceLimits{
		CPUMaxSeconds:  120,
		MemoryMaxBytes: 1 << 30,
		ProcessMax:     256,
	}
}

// TestB2_RlimitSkipNotice_NotOnTargetStderr 判据 A + B（直连生产 helper）。
func TestB2_RlimitSkipNotice_NotOnTargetStderr(t *testing.T) {
	ws := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "echo b2-target-alive")
	cmd.Dir = ws
	if err := wrapWithRlimitHelper(cmd, b2RlimitProbeLimits()); err != nil {
		t.Fatalf("wrapWithRlimitHelper: %v", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper command failed: %v, output: %q", err, string(out))
	}

	// 判据 B（正向留痕，必须先满足）：目标命令真的执行了。
	// 否则"stderr 无 canary"可能是因为命令压根没启动 —— 负向断言被蒙对（R3-③）。
	if !strings.Contains(string(out), "b2-target-alive") {
		t.Fatalf("目标命令未执行 ⇒ 判据 A 会被提前失败蒙对；output=%q", string(out))
	}

	// 判据 A：目标命令的 stderr 不得混入 whale 自身诊断。
	if strings.Contains(string(out), "whale-rlimit-helper:") {
		t.Errorf("whale 自身降级诊断借道目标进程 stderr ⇒ 会被 Monitor 当'子进程输出'回显给用户；output=%q", string(out))
	}
}

// TestB2_RlimitSkipNotice_NotOnTargetStderr_FullChain 完整生产链交叉确认。
//
// 与上面同一判据，但走 Prepare → OSSandbox().Restrict（seatbelt + rlimit 组合）。
// 受限 runner 中 sandbox_apply EPERM ⇒ SKIP；正常终端下应绿。
// ★ 保留它的理由：直连路径少了 seatbelt 那一层，完整链能排除"差异恰在那层"。
func TestB2_RlimitSkipNotice_NotOnTargetStderr_FullChain(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)

	ws := t.TempDir()
	mgr := newRlimitTestManager(t, ws, b2RlimitProbeLimits())
	out, err := runInSandbox(t, mgr, ws, "echo b2-target-alive")
	if err != nil {
		t.Fatalf("script failed: %v, output: %q", err, string(out))
	}
	if !strings.Contains(string(out), "b2-target-alive") {
		t.Fatalf("目标命令未执行 ⇒ 判据 A 会被提前失败蒙对；output=%q", string(out))
	}
	if strings.Contains(string(out), "whale-rlimit-helper:") {
		t.Errorf("完整链：whale 自身降级诊断仍借道目标进程 stderr；output=%q", string(out))
	}
}

// TestB2_DegradationStillHasAuditOutlet 判据 C（护栏，不具判别性）。
func TestB2_DegradationStillHasAuditOutlet(t *testing.T) {
	// ★ 2026-10-09（接线）补前置：rlimit 通道**只存在于 seatbelt 沙箱内**
	//（diagnostic 由 darwinSeatbeltSandbox.Enter 记）。 接线后，在
	// `sandbox_apply` 被拒的机器上工厂如实返回 degraded ⇒ 那条 reason **不可能**出现
	// ⇒ 不跳过就会把"环境装不上沙箱"误读成"降级没有出口"。
	// 与本文件 `TestB2_RlimitSkipNotice_NotOnTargetStderr_FullChain` 的既有前置同源。
	skipIfSeatbeltApplyDenied(t)

	// 隔离审计落盘：NewManager 会调 NewAuditLog()（manager.go:128），
	// 它用 os.UserHomeDir()/.whale/logs ⇒ 必须先钉 HOME，否则探针会写真实用户文件。
	// ★ 必须走 testutil.IsolatedHomeDir（宽松清理），不得用 t.TempDir()：
	// 后台协程在用例结束后仍会写 $HOME，与其 RemoveAll 清理断言竞态
	// （仓库根卫生门 tempdir_home_gate_test.go 全仓禁止）。
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))

	ws := t.TempDir()
	mgr := newRlimitTestManager(t, ws, b2RlimitProbeLimits())
	m, ok := mgr.(*manager)
	if !ok {
		t.Fatal("NewManager must return *manager")
	}

	if _, _, err := m.osSandbox.Enter(); err != nil {
		t.Fatalf("Enter: %v", err)
	}

	// 精确匹配 rlimit 那条 reason：degradedSandbox.Enter 也会记降级
	//（os_sandbox.go:220，reason 形如 'darwin seatbelt disabled…'）
	// ⇒ 只断言 ContainsDegradation() 会被"平台降级"蒙对（R3-③ 同族）。
	const want = "RLIMIT_AS/RLIMIT_NPROC skipped"
	found := false
	for _, r := range m.audit.DegradeReasons() {
		if strings.Contains(r, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("降级无出口：审计通道未记录 %q ⇒ 静默降级（违反'降级必须有出口'教训）。"+
			" 现有 reasons=%q", want, m.audit.DegradeReasons())
	}

	// 反向对照（R6b）：证明上面的判据不是恒真 —— 同一实例上，
	// 一条**不存在**的 reason 必须查不到。
	for _, r := range m.audit.DegradeReasons() {
		if strings.Contains(r, "b2-must-not-exist-sentinel") {
			t.Fatalf("对照失败：审计里出现了不该有的 sentinel ⇒ 上面的查找不可信")
		}
	}
}
