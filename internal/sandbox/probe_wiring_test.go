//go:build darwin

// Package sandbox — 判定接线判别器（TDD：红先行）。
//
// # 缺陷面
//
// `newPlatformSandbox` 修复前只问「`sandbox-exec` 文件在不在」（`os.Stat`）：
// 「**文件在、但 `sandbox_apply` 被内核拒**」（企业 MDM、被替换的二进制、
// 或本机实测的**受限安全上下文**：`sandbox_apply: Operation not permitted`, rc=71）
// 时，工厂仍返回 `darwinSeatbeltSandbox` ⇒ `Platform()=="darwin-seatbelt"`
// ⇒ 启动 banner 列出 `seatbelt(workspace-write)` ⇒ **谎报隔离**。
// ★ 这是**现象恒真**类缺陷：宣称恒称隔离 ⇒ 「沙箱生效了吗」**没有判别力**。
//
// # 为什么本判据不硬编码任何「主机事实」
//
// 「本机一定能沙箱」与「本机一定不能沙箱」**都是环境差异**，把它写进断言
// 就是把环境灌进判别器（本仓「判绿真伪」纪律明令避免）。因此本文件只断言
// **code 侧的条件不变式**：
//
//	探针过 ⇒ 工厂/生产状态必须给出真平台（非 degraded）＋非空特性；
//	探针不过 ⇒ 工厂/生产状态必须如实落 degraded（不得谎报），且不得声称可强制。
//
// 该式在**能力机**（防"误降级"，即真隔离被当成无隔离）与**受限机**
// （防"谎报"，即 本体）**两个世界都成立且有判别力** —— 这正是
// 「不判环境、只判关系」带来的好处：不必到真实 `Terminal.app` 才能测。
// 反例注入：把工厂判定改回 `os.Stat` ⇒ 受限机上"探针不过却宣称隔离" ⇒ 打红。
package sandbox

import (
	"strings"
	"testing"
)

// TestN013_FactoryClaimAgreesWithProbe —— 工厂（非导出入口）的宣称必须与探针一致。
func TestN013_FactoryClaimAgreesWithProbe(t *testing.T) {
	ok, why := SandboxExecUsable()
	t.Logf("本机功能探针：ok=%v why=%q", ok, why)

	sb := newPlatformSandbox(OSLevelConfig{Darwin: DarwinSandboxConfig{UseSeatbelt: true}}, NewAuditLog())
	got := sb.Platform()

	if ok {
		if got != "darwin-seatbelt" {
			t.Fatalf("探针通过（%s）⇒ 工厂必须给出 darwin-seatbelt，got=%q（误降级：真隔离被当成无隔离）", why, got)
		}
		return
	}
	if got != "degraded" {
		t.Fatalf("探针不过（%s）⇒ 工厂必须落 degraded，got=%q —— 这是  本体：**谎报隔离**", why, got)
	}
}

// TestN013_ProductionManagerStatusAgreesWithProbe —— 经**生产入口**读出的状态必须与探针一致。
//
// 与上一条的分工：上一条钉"工厂宣称"，本条钉"经 NewProductionSandboxManager →
// IsolationStatusOf 后，用户可见的那份事实"是否同样诚实（含 Features 与
// EnforcementActive 两个下游消费者）。
func TestN013_ProductionManagerStatusAgreesWithProbe(t *testing.T) {
	ok, why := SandboxExecUsable()
	mgr := NewProductionSandboxManager(t.TempDir())
	st := IsolationStatusOf(mgr)

	if ok {
		if st.Platform == "degraded" {
			t.Fatalf("探针通过（%s）⇒ 生产状态不得为 degraded（误降级）: %+v", why, st)
		}
		if len(st.Features) == 0 {
			t.Fatalf("探针通过 ⇒ 必须列出生效特性，got 空: %+v", st)
		}
		return
	}

	if st.Platform != "degraded" {
		t.Fatalf("探针不过（%s）⇒ 生产状态必须如实为 degraded；got platform=%q features=%v —— 谎报隔离",
			why, st.Platform, st.Features)
	}
	if EnforcementActive(mgr) {
		t.Fatalf("探针不过 ⇒ 不得声称「可强制」（否则 shell 免问出口 (c) 会静默放开）: %+v", st)
	}
}

// TestN013_DegradeReasonIsUserVisible —— 降级不得静默：原因必须出现在用户可见出口。
//
// 与 I-3.1「B 格必须与 A 格不同、且不得再称隔离」同一口径：只把平台标成 degraded
// 而**不给原因**，用户仍不知道"为什么没有隔离、该怎么办"。
//
// ★ 观测点刻意选在**工厂返回之后、任何 Prepare 之前** —— 那正是启动 banner
// （`PrintIsolationBanner`）读状态的那一刻。若原因要等到首次 `Enter` 才记录，
// banner 就只能印 `degraded`、印不出原因（本条即会红）。
func TestN013_DegradeReasonIsUserVisible(t *testing.T) {
	ok, _ := SandboxExecUsable()
	if ok {
		t.Skip("本机沙箱可被真实应用，无降级原因可验（环境限制，非判据错误）")
	}
	mgr := NewProductionSandboxManager(t.TempDir())
	st := IsolationStatusOf(mgr)
	// ★ 不能只断言 len(Degradations) > 0 —— darwin 生产装配**恒**会记一条 rlimit
	// 子能力降级（Enter() 里 ResourceLimits != {} 即记），只数条数会被它**蒙对**
	// （实测：接线前该断言已绿 ⇒ 不是判别器）。必须钉"原因里出现了本次缺陷的语义"。
	found := false
	for _, d := range st.Degradations {
		if strings.Contains(d, "cannot apply") || strings.Contains(d, "sandbox_apply") {
			found = true
		}
	}
	if !found {
		t.Fatalf("平台=%q 但降级原因里没有「探针失败」语义（只有 rlimit 等子能力项）⇒ "+
			"用户看不到「为什么没隔离」: %+v", st.Platform, st.Degradations)
	}
}
