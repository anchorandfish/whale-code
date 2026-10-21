package tools

// 续—— **Monitor 路径**的"批准下传"判据。
//
// 为什么单独一条： 的 6 条判据全部走 `Name:"bash"`
// （.go / .go）。
// Monitor 与 bash **共用**同一个 `SandboxPrecheckStage`
// （`permission.IsCommandBearingTool` 的集合 = CapExec 桶 = {bash, monitor}）
// 与同一个 `prepareSandbox`（internal/tools/monitor.go:65 → bash.go:990），
// 但 **Monitor 侧此前没有显式判据** ⇒ "Monitor + 用户批准 → 沙箱外执行"只有
// 结构性推理，没有判据。本条把它钉死。
//
// 来源：`docs/人工测试记录/沙箱限制太多对话.md` 的对话停在"Monitor 的第二条闸口"
// 处 —— 当时判断要给 `monitor.Manager` 加一个"仅由用户批准驱动"的 StartOption。
//
// ★ 本条判据**实测证实那个判断是对的**：
// - `SandboxPrecheckStage` 已放行（stageLogs 里可见
// `sandbox rule SUBSTITUTION sanctioned by user approval`）；
// - `prepareSandbox` 也已放行（`sandboxSanctioned(ctx)` ⇒ `ValidateSanctioned`）；
// - **但** `monitor.Manager.Start` 内部还有一道 `security.CheckCommand`
// （bash 的执行链没有这一道），它用**另一套引擎**判 `$()` 为 L1 硬拒
// ⇒ 用户点了允许仍 `startmonitoringfailed: ... [CMD-SUBST]`。
// ⇒ 修复 = `monitor.WithUserApproval()`（由 `monitorHandler` 在
// `sandboxSanctioned(ctx)` 时注入），使 Monitor 的执行链闸口与 bash **同源**。
//
// 生产入口（R1）：`newProdExecutor`（NewExecutor 内建 Sandbox + MonitorMgr，
// executor.go:245/241）+ `InitPipelineWithHooks` + `ExecuteWithConfirm`。
//
// 判别性（A/B）：把 `internal/tools/monitor.go` 里注入 `monitor.WithUserApproval()`
// 的那两行去掉 ⇒ 本条**红**（`IsError=true`，报 IFS 注入被沙箱拦截）。

import (
	"context"
	"strings"
	"testing"
)

// n122MonitorCmd 与 bash 版同形（可批准档），但**刻意不带 sleep**：
// Monitor 会拦超过 `monitor.MaxAllowedSleepSeconds` 的 sleep（`monitor.DetectBlockedSleep`）。
// echo 无害、结果可观测（返回体含监控 id）。
// ★ 样本 2026-10-08 换为 IFS 形态： 残留修复后 `$(date)` 良性内层
// 落放行档（不再询问），本判据需要修后仍是确认档的命令。
const n122MonitorCmd = `IFS=:; echo tick`

// TestN122_Monitor_ConfirmTier_UserApprovalTakesEffect 是 Monitor 路径的主判据。
//
// 期望（after）：弹确认 → 用户按 Yes → Monitor **真的启动**（返回监控 id），
// 而不是在执行链 `prepareSandbox` 被二次否决。
func TestN122_Monitor_ConfirmTier_UserApprovalTakesEffect(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	if e.MonitorMgr == nil {
		t.Fatal("前提不成立：生产装配应内建 MonitorMgr（executor.go:241）")
	}
	t.Cleanup(e.MonitorMgr.StopAll)

	// 前提自检：执行链确实拦这条命令（否则本测试失去意义）。
	if allowed, reason := e.Sandbox.Validate(n122MonitorCmd); allowed {
		t.Fatalf("前提不成立：%q 本应被沙箱拦下（reason=%q）", n122MonitorCmd, reason)
	}

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n122-monitor-approve",
		Name:  "Monitor",
		Input: mustMarshal(map[string]interface{}{"command": n122MonitorCmd}),
	})

	if asked == 0 {
		t.Fatalf("Monitor 的可批准档命令也必须给出确认框（用户连批准的机会都没有）:\n"+
			"  proceed=%v failureKind=%q rejection=%q stageLogs=%v",
			pr.Proceed, pr.FailureKind, pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed {
		t.Fatalf("用户已批准，Monitor 仍被拒 —— 执行链的 prepareSandbox 二次否决了批准:\n"+
			"  failureKind=%q rejection=%q stageLogs=%v", pr.FailureKind, pr.Rejection, pr.StageLogs)
	}
	if pr.FailureKind == FailureKindSandboxDenied {
		t.Fatalf("批准后 Monitor 仍报 sandbox_denied：%+v", pr)
	}
	if pr.Result == nil {
		t.Fatalf("批准后 Monitor 应真的启动监控，却无执行结果：%+v", pr)
	}
	// ★★ 关键判别面（第一版测试漏了这一条 ⇒ 假绿）：
	//
	// 批准虽然穿过了 `prepareSandbox`，但 Monitor 的**第二道闸口**
	// （`monitor.Manager.Start` 内的 `security.CheckCommand`）用的是**另一套引擎**：
	// `$()` 在 sandbox 引擎里是"可批准档"，在 security 引擎里仍是 L1 硬拒
	// ⇒ 用户点了允许，命令仍以 `startmonitoringfailed: ... [CMD-SUBST]` 失败。
	//
	// 因此不能只断言"输出里没有 sandbox denied"（那只覆盖第一道闸口），
	// 必须断言**执行成功**：`IsError == false`。
	if pr.Result.IsError {
		t.Fatalf("批准后 Monitor 仍执行失败（存在未让路的第二道闸口）:\n  output=%q", pr.Result.Output)
	}
	if strings.Contains(strings.ToLower(pr.Result.Output), "sandbox denied") {
		t.Fatalf("批准后 Monitor 仍在执行链被沙箱否决（批准未下传）:\n  output=%q", pr.Result.Output)
	}
	// 监控确实被创建。进程可能瞬间退出（echo 立即结束），故此处只做**软校验**：
	// 有进程则记录；无进程也不算红（硬证据在上面两条）。
	if procs := e.MonitorMgr.List(); len(procs) > 0 {
		t.Logf("监控进程已创建：%d 个", len(procs))
	}
}

// TestN122_Monitor_HardTier_NeverBecomesAConfirmation 是**更危险形态**的守卫（Monitor 版）。
//
// 真不可逆命令（`rm` 命令位）在 Monitor 路径上必须照旧**直接拒、不弹框**，
// 不得因为"Monitor 也吃批准"而被降级成一次确认。
// 本条不具判别性（before/after 都应绿），作用是钉住 Monitor 版分档没有被放宽成"全可批准"。
func TestN122_Monitor_HardTier_NeverBecomesAConfirmation(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)
	t.Cleanup(e.MonitorMgr.StopAll)

	if allowed, reason := e.Sandbox.Validate(n122HardCmd); allowed {
		t.Fatalf("前提不成立：%q 本应被沙箱拒绝（reason=%q）", n122HardCmd, reason)
	}

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n122-monitor-hard",
		Name:  "Monitor",
		Input: mustMarshal(map[string]interface{}{"command": n122HardCmd}),
	})

	if asked != 0 {
		t.Fatalf("Monitor 的硬拒档不该弹确认框（用户会被白收一次批准）：asked=%d", asked)
	}
	if pr.NeedConfirm {
		t.Fatalf("硬拒档不得降级成确认：%+v", pr)
	}
	if pr.Proceed {
		t.Fatalf("不可逆命令应被拒，实际放行：%+v", pr)
	}
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("FailureKind 应为 %q，实际 %q", FailureKindSandboxDenied, pr.FailureKind)
	}
	if procs := e.MonitorMgr.List(); len(procs) != 0 {
		for _, p := range procs {
			_ = e.MonitorMgr.Stop(p.ID)
		}
		t.Errorf("硬拒档命令不该创建监控进程，实际 %d 个", len(procs))
	}
}
