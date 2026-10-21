package tools

// —— 沙箱拒绝**分档**：可批准档的"允许"必须真正生效。
//
// 背景（用户真机感受，2026-10-05）：
// "我不想关沙箱，但 AI 改个代码还不行，允许了还会被拒绝。"
//
// 现有实现把两件不同的事塞进了同一条通道（`SandboxPrecheckStage` 命中即
// `Proceed:false` + `FailureKind=sandbox_denied`，**不可批准**）：
//
//	① 真不可逆：dd / mkfs / format、rm 灾难形态（rm -rf / 等）、反向 shell、终端注入
// ⇒ 硬拒是**对的**
//	② 日常可逆：rm（普通目标）/ chmod / chown / curl / wget、命令替换 $(...) ⇒ 硬拒是**错的**，
// 用户连批准的机会都没有
//
// ★ ：rm 已从硬拒档降为可批准档（对齐 同类 CLI"宁可问不禁"），
// 灾难形态（rm -rf / 等）由 validator.AssessRmRfDangerousTarget 升级守卫保持硬拒。
// 本文件钉旧硬拒档的样本随之由 rm 换为 dd（R10①：判别意图不变，样本换血）。
//
// 业内对照（同仓快照，见 内部工作回执 的「沙箱拒绝分档」对标证据件 2026-10-05）：
// - 同类 CLI：固定危险名单只有 `rm -f/rm -rf/sudo`，命中走 `Decision::Prompt`（**强制询问**）
// （`对标证据件`；
// `对标证据件`）；批准后在**沙箱外**执行该条命令
// （`对标证据件` `BypassSandboxFirstAttempt`）。
// - 同类 CLI：`Allow unsandboxed fallback` 开启时，沙箱失败可用
// `dangerouslyDisableSandbox` 重试
// （`src/components/sandbox/SandboxOverridesTab.tsx:164`）。
// ⇒ **两类都不设"日常命令硬拒且不可批准"这一档。**
//
// 分档表（唯一判据源：internal/sandbox 的 tier 分类，不在本测试里重列名单）：
//
//	放行 ：无规则命中
//	可批准 ：SUBSTITUTION / IFS / DENYLIST{rm,curl,wget,chmod,chown} / NETWORK_CMD / WRITE_REDIRECT
//	硬拒 ：DENYLIST{dd,mkfs,format} / rm 灾难形态 / NETWORK_CRITICAL / CONTROL_CHAR / ALLOWLIST 未命中
//
// 生产入口（R1）：`InitPipelineWithHooks` 装配 + `ExecuteWithConfirm`（用户按下 Yes 后
// 带 `Approved=true` **重跑整条管线**，executor.go:983）。
// 本文件只断言**行为面**（NeedConfirm / Proceed / FailureKind / 是否真的执行），
// 不引用"档位"这类新符号 ⇒ 可直接在改动前的源码上编译并跑红。

import (
	"context"
	"strings"
	"testing"
)

// n122ConfirmCmd 命中"可批准档"且执行无害、结果可观测。
// ★ 样本 2026-10-08 换为 IFS 形态： 残留修复后，`$(date)` 的良性内层
// 落**放行档**（不再询问），本判据需要一个修后仍是确认档的命令。
// 选用 IFS 而非 chmod/curl：后者依赖文件系统与网络，会让判据变环境依赖。
const n122ConfirmCmd = `IFS=:; echo tick`

// n122HardCmd 命中"硬拒档"（deny 命令位 = dd，真不可逆； 后 rm 已降可批准档，
// 硬拒代表样本换为 dd —— 两个理由：仍在 irreversibleDenyCommands；不依赖网络/文件系统状态）。
const n122HardCmd = `dd if=/dev/zero of=/tmp/whale-n122-junk bs=1 count=1`

// n122MixedCmd 同一条命令里**同时**含硬拒档与可批准档 —— 用于证明
// "批准不是万能钥匙"：硬拒档必须赢（deny-wins），不因批准而放行。
const n122MixedCmd = `dd if=/dev/zero of=/tmp/whale-n122-junk bs=1 count=1 && echo "tick $(date)"`

// TestN122_ConfirmTier_UserApprovalTakesEffect 是主判据。
//
// 改动前（before）预期**红**：`SandboxPrecheckStage` 直接 `Proceed:false`
// ⇒ 用户根本没有确认框可点（`OnConfirm` 一次都不会被调用）。
// 改动后（after）预期**绿**：弹出确认 → 用户按 Yes → 命令**真的执行**。
func TestN122_ConfirmTier_UserApprovalTakesEffect(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	// 前提自检：执行链确实拦这条命令（否则本测试失去意义）。
	// 本文件刻意**不引用**任何新增符号（档位常量等）⇒ 它能在改动前的源码上
	// 编译并跑红，A/B 两态可同源对比；"档位真的分开了"由 asked>0 这条断言判别。
	if allowed, reason := e.Sandbox.Validate(n122ConfirmCmd); allowed {
		t.Fatalf("前提不成立：该命令本应被沙箱拦下（reason=%q）", reason)
	}

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n122-confirm-approve",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": n122ConfirmCmd}),
	})

	if asked == 0 {
		t.Fatalf("用户从未被询问 —— 可批准档必须给出确认框（用户连批准的机会都没有）:\n"+
			"  proceed=%v failureKind=%q rejection=%q stageLogs=%v",
			pr.Proceed, pr.FailureKind, pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed {
		t.Fatalf("用户已批准，命令仍被拒 —— 这正是\"允许了还会被拒绝\":\n"+
			"  failureKind=%q rejection=%q stageLogs=%v", pr.FailureKind, pr.Rejection, pr.StageLogs)
	}
	if pr.FailureKind == FailureKindSandboxDenied {
		t.Fatalf("批准后仍报 sandbox_denied：%+v", pr)
	}
	if pr.Result == nil {
		t.Fatalf("批准后命令应真的执行，却无执行结果：%+v", pr)
	}
	if out := pr.Result.Output; !strings.Contains(out, "tick") {
		t.Fatalf("命令应真的跑出 tick，实际输出=%q", out)
	}
}

// TestN122_ConfirmTier_UserDenialStillBlocks 是配对反例。
//
// 分档**不等于放宽**：用户按 No 时必须照样拦住、且不执行。
// 本条在 before 态也绿（before 连问都不问就拒）⇒ 它不具判别性，
// 作用是钉住"after 态没有把确认框变成自动放行"。
func TestN122_ConfirmTier_UserDenialStillBlocks(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return false }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n122-confirm-deny",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": n122ConfirmCmd}),
	})

	if pr.Proceed {
		t.Fatalf("用户按 No，命令不得执行：%+v", pr)
	}
	if pr.Result != nil {
		t.Fatalf("用户按 No，却拿到了执行结果：%+v", pr.Result)
	}
	if pr.FailureKind != FailureKindPolicyDenied {
		t.Errorf("用户拒绝的 FailureKind 应为 %q，实际 %q（rejection=%q）",
			FailureKindPolicyDenied, pr.FailureKind, pr.Rejection)
	}
	_ = asked
}

// TestN122_HardTier_NeverBecomesAConfirmation 是**更危险形态**的守卫。
//
// 真不可逆的命令（rm/dd/mkfs/format）不得因为本次分档而变成
// "弹个框就能过" —— 它必须照旧直接拒，且**不浪费用户一次批准**
// （这正是 p0_sandbox_precheck_test.go 当初要防的形态，只把主体从
//
//	命令替换换成了真正的不可逆命令）。
func TestN122_HardTier_NeverBecomesAConfirmation(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	if allowed, reason := e.Sandbox.Validate(n122HardCmd); allowed {
		t.Fatalf("前提不成立：%q 本应被沙箱拒绝（reason=%q）", n122HardCmd, reason)
	}

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n122-hard",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": n122HardCmd}),
	})

	if asked != 0 {
		t.Fatalf("硬拒档不该弹确认框（用户会被白收一次批准）：asked=%d", asked)
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
	if pr.Result != nil {
		t.Errorf("命令不该被执行，却拿到执行结果：%+v", pr.Result)
	}
	if pr.ConfirmSource != SourcePipeline {
		t.Errorf("成因方应为 %q（用户从未被询问），实际 %q", SourcePipeline, pr.ConfirmSource)
	}
}

// TestN122_ApprovalIsNotAMasterKey 钉住 deny-wins 与"批准的作用域"。
//
// 同一条命令里既有可批准档（$(...)）又有硬拒档（rm -rf）：批准必须
// **只在可批准档生效**，硬拒档照样短路。否则分档会被当成"一次批准 = 全命令放行"，
// 比原来的"一律硬拒"更危险。
func TestN122_ApprovalIsNotAMasterKey(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	if allowed, reason := e.Sandbox.Validate(n122MixedCmd); allowed {
		t.Fatalf("前提不成立：混合命令本应被拒（reason=%q）", reason)
	}

	e.OnConfirm = func(string, string) bool { return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n122-mixed",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": n122MixedCmd}),
	})

	if pr.Proceed {
		t.Fatalf("含硬拒档的混合命令不得因批准而放行（deny-wins）：%+v", pr)
	}
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("FailureKind 应为 %q，实际 %q（rejection=%q）",
			FailureKindSandboxDenied, pr.FailureKind, pr.Rejection)
	}
	if pr.Result != nil {
		t.Errorf("命令不该被执行：%+v", pr.Result)
	}
}
