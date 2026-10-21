package tools

// ① 批准与拒绝同源 —— 「沙箱硬拒绝不得被降级成一次用户确认」的生产入口判据。
//
// 真机取证（2026-10-04，用户报告）：TUI 里收到
// Monitor({"command":"while true; do echo \"tick $(date)\"; sleep 2; done"})
// 弹出确认框 → 用户点「允许」→ 工具却返回
// sandbox denied: command substitution not allowed
// 即用户批准被白收一次，批准账本还记 `decision=approved result:success`。
//
// 根因（两条链，规则来源不同）：
//
//	决策链：Pipeline 各阶段只查 `cmdrules`（危险/破坏性/需确认），
// **不含**命令替换/IFS 这类沙箱结构性规则；
//	执行链：ExecuteStage → handler → prepareSandbox(bash.go:990)
// → e.Sandbox.Validate(command) → evaluateAppLayer ⇒ 命中 RuleSubstitution。
//
// ⇒ 决策链放行并弹确认，执行链事后否决。两者对同一条命令给出相反结论。
//
// 本判据要求：决策链在**问用户之前**先问一次与执行链同源的裁决者
// （同一个 Sandbox 实例、同一个 Validate 方法）——硬拒绝 ⇒ 直接拒、不弹确认。
//
// 生产接线断言（R1）：
//
//	执行链：bash.go:990 prepareSandbox → bash.go:1001 `e.Sandbox.Validate(command)`
//	审批链：security.go:276 `c.sandbox.Validate(normalized)`（同一方法，归一化差异见下）
//	装配： executor.go:245 `Sandbox: sandbox.NewSandbox(workDir)`（恒非 nil）
// lifecycle.go:435 `SetEnabled(true)`（生产 TUI 路径）
//
// ⚠️ 归一化差异（如实标注，不改）：审批链走 `normalizeForCheck`，执行链走**原始命令**。
// 本判据要求决策链用**原始命令**（与执行链逐字一致），才能准确预测执行结局。
//
// 本测试只断言**行为面**（NeedConfirm / Proceed / FailureKind / ConfirmSource），
// 不引用任何新符号。
//
// ★ 2026-10-05 改规格（，**下一个人别当回归**）：
// 本文件主体原本是 `while true; do echo "tick $(date)"; sleep 2; done`（命令替换），
// 钉的语义是「沙箱拒绝一律不可批准、不弹框」。 把沙箱拒绝**按后果分档**后：
//
// 命令替换 $(...) ⇒ **可批准档**（正是本文件当初要防的那种"白批准"，
// 现在变成了"批准就真的生效"，判定见 .go）
// rm/dd/mkfs/format、反向 shell、监听态、裸 \r ⇒ **硬拒档**（本文件现在守这一档）
// （⚠️ 2026-10-14：rm 普通目标已降可批准档；硬拒档现为
// dd/mkfs/format、rm 灾难形态（rm -rf / 等）、反向 shell、监听态、裸 \r）
//
// 改规格的依据：用户 2026-10-05 拍板 + 业内对照（两家同类 CLI 都不设
// "日常命令硬拒且不可批准"这一档，见 内部工作回执 的「沙箱拒绝分档」对标证据件 2026-10-05）。
// ⚠️ 本文件因此从"判别器"变成**守卫**（改动前后都应绿）：它守住的不变量是
// 「硬拒档永不被降级成一次用户确认」。真正的判别器在 *。

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// hardDenyCmd 是**硬拒档**的代表命令（之后本文件的主体）。
//
// 选用 `nc -l 4444`（监听态）的两个理由：
// 1. 命中 ⑤-a 网络命令后由 `hardenNoApprovableEscape` 收窄为硬拒
// （`DetectNetworkAccess` 判 Inbound / Severity=High）；
// 2. 它**不在** `cmdrules` 危险档名单里 ⇒ 不会被 `DangerousGuardStage` 先抢答，
// 本测试才能干净地只测沙箱预检这一环。
const hardDenyCmd = `nc -l 4444`

// newProdExecutor 按生产方式装配：
// NewExecutor 内建 Sandbox（executor.go:245 恒非 nil）
// + lifecycle.go:435 的 SetEnabled(true) + InitPipelineWithHooks（生产装配入口）。
func newProdExecutor(t *testing.T, workDir string) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	if e.Sandbox == nil {
		t.Fatal("NewExecutor 应初始化 legacy sandbox")
	}
	e.Sandbox.SetEnabled(true) // 与生产 lifecycle.go:435 一致
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
	return e
}

// TestP0_SandboxHardDeny_IsRejectedBeforeUserIsAsked 守的是**硬拒档**的不变量。
//
// 之后本测试在改动前后**两态都绿**（它是守卫，不是判别器）：
// 「硬拒档永不被降级成一次用户确认、命令一行都不执行、成因方是管线」。
func TestP0_SandboxHardDeny_IsRejectedBeforeUserIsAsked(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	// 前提自检写进判据里：执行链确实会拒这条命令。
	// 若前提不成立（沙箱本就不拦），本测试失去意义 —— 它会先在这里红，
	// 而不是伪装成"修复生效"。
	if allowed, reason := e.Sandbox.Validate(hardDenyCmd); allowed {
		t.Fatalf("前提不成立：执行链沙箱本应拒绝 %q，实际放行（reason=%q）", hardDenyCmd, reason)
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "p0-hard",
		Name:  "Monitor", // 生产注册名；IsCommandBearingTool 内部 ToLower ⇒ 大小写不敏感
		Input: mustMarshal(map[string]interface{}{"command": hardDenyCmd}),
	})

	// 核心断言：不得把它变成一次确认。
	// 沙箱硬拒绝是「能不能做」的边界，不是「要不要问」的问题 ——
	// 与 pipeline.go:432-433（sandbox_denied 不触发 on-failure 审批）同一条语义。
	if pr.NeedConfirm {
		t.Fatalf("沙箱硬拒绝不可被批准绕过，决策链不该把它变成一次确认（用户会被白收一次批准）\n"+
			"  rejection=%q\n  stageLogs=%v", pr.Rejection, pr.StageLogs)
	}
	if pr.Proceed {
		t.Fatalf("硬拒档应被拒，实际放行：proceed=%v result=%+v", pr.Proceed, pr.Result)
	}
	// FailureKind 是 PipelineResult 的**顶层字段**（管线级失败原因），
	// 不是 pr.Result.FailureKind（那是工具执行结果里的字段，拒绝发生在执行之前）。
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("FailureKind 应为 %q，实际 %q（rejection=%q）",
			FailureKindSandboxDenied, pr.FailureKind, pr.Rejection)
	}
	// 命令**没有被执行**：拒绝短路在 ExecuteStage 之前 ⇒ pr.Result 必为 nil。
	// 这是"用户没白批准"的另一半证据 —— 不是"批准后又执行失败"，而是"压根没执行"。
	if pr.Result != nil {
		t.Errorf("命令不该被执行（沙箱预检应短路到 ExecuteStage 之前），却拿到执行结果：%+v", pr.Result)
	}
	// 成因方必须是"管线"（用户从未被询问），不得谎称用户拒绝。
	if pr.ConfirmSource != SourcePipeline {
		t.Errorf("成因方应为 %q（用户从未被询问），实际 %q", SourcePipeline, pr.ConfirmSource)
	}
}

// TestP0_SandboxAllowedCommand_NotOverRejected 是**反例守卫**（防一刀切）。
//
// 沙箱放行的命令不得被新判据误伤 —— 它必须照常走到 Permission/Execute。
// 本条在 before / after **两态都应绿**（不具判别性，只防"顺手全拒"这种更糟形态）。
func TestP0_SandboxAllowedCommand_NotOverRejected(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	cmd := "echo hello"
	if allowed, reason := e.Sandbox.Validate(cmd); !allowed {
		t.Fatalf("前提不成立：该命令本应被沙箱放行（reason=%q）", reason)
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "p0-ok",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": cmd}),
	})

	if pr.FailureKind == FailureKindSandboxDenied {
		t.Fatalf("误伤：沙箱放行的命令被决策链判成 sandbox_denied：%+v", pr)
	}
	if pr.Rejection == "" && !pr.Proceed && !pr.NeedConfirm {
		// 沙箱放行却仍在管线被拒 ⇒ 拒绝来自别的阶段，需打印出来以便归因。
		t.Logf("注意：该命令在管线被拒（非本阶段）：rejection=%q stageLogs=%v", pr.Rejection, pr.StageLogs)
	}
}

// TestP0_DenyWordInArgumentPosition_NotHardRejected 是**命令位语义**的交叉守卫。
//
// 与 （.go）同源：deny 判据必须落在**命令位**，
// 参数位出现的 `rm` 不代表这是一条删除命令。
//
// 本条钉住的是：SandboxPrecheckStage 复用的是**修好后的** Sandbox.Validate
// （命令位语义），而不是自己另起一套整串词匹配 ——
// 后者会把 `grep -rn rm .` 这类只读命令**连问都不问就硬拒**，
// 即把 修掉的过度拒绝又从决策链这条路引回来。
func TestP0_DenyWordInArgumentPosition_NotHardRejected(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	cmd := "grep -rn rm ."
	if allowed, reason := e.Sandbox.Validate(cmd); !allowed {
		t.Fatalf("前提不成立：只读命令的参数位含 deny 词，命令位语义下本应放行（reason=%q）", reason)
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "p0-denyword-arg",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": cmd}),
	})

	if pr.FailureKind == FailureKindSandboxDenied {
		t.Fatalf("误伤：参数位含 deny 词的只读命令被判成 sandbox_denied —— "+
			"说明预检脱离了命令位语义，把  修掉的过度拒绝又引了回来：%+v", pr)
	}
}
