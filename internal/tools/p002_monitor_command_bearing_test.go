package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// P-002 / ★S-1（2026-09-22 反缝补扫描 · `补丁台账_2026-09-22.md` §2.6）——
// 「这个工具是不是命令载体」这一事实被手抄成工具名对 `{bash, PowerShell}`，
// 而真实集合是 `{bash, monitor}`（monitor 是真工具、PowerShell 是幽灵名）。
//
// 后果（生产路径，逐段可查）：
//
//	Monitor({"command":"rm -rf build"}) × 沙箱真在强制
// ① DangerousGuardStage（pipeline.go:1069）按 `call.Name != "bash" && != "PowerShell"` 短路
// ⇒ 连 cmdrules.MatchDangerous 都不会被调用
// ② DestructiveCheckStage → checkDestructive（:1205）只有 `case "bash":` ⇒ Monitor 落 default
// ③ PermissionStage 出口 (c)（:1624）用的却是**真 SSOT**（含 monitor）⇒ 免问放行
// ⇒ 破坏性命令**零确认执行** —— 与 :1036-1039 记录并已修掉的 P0 同形，换了一条路径复现。
//
// ⚠️ **影响面更正（2026-09-22 实测，别把本条读成"命令会被执行"）**：
// 上面说的是**判定面**（四种确认机制确实都不再拦）。但 `Monitor` 的 handler 走
// `monitor.Manager.Start`，后者**独立**再调 `security.CheckCommand`（`monitor/monitor.go:250-257`）：
// `Deny` ⇒ 报错、`Ask` ⇒ **fail-closed 拒绝**。实测 `shutdown -h now` ⇒ Deny、
// `rm -rf build` / `npm run dev` ⇒ Ask ⇒ **危险/破坏性命令经 Monitor 本来就执行不了**。
// 所以本缺陷的真实后果 = ① **确认框被绕过**（用户失去知情与否决机会）② 危险命令可进审批记忆（★S-2）。
// 本文件验证的正是这两条**判定面**事实。详见 `补丁台账_2026-09-22.md` §2.6「影响面更正」。
//
// 修法（结构性，不是再补一处名对）：把「命令载体」这一事实收进
// `permission.IsCommandBearingTool`（由 CapExec 桶派生），10 处引用它。
//
// ⚠️ 安全：本文件**绝不真的执行** shutdown / rm。执行端 handler 被替换为 stub ——
// 只掐掉"真的跑命令"这一步；裁决链（rule_gate → schema_validate → custom_validate →
// pre_hook → dangerous_guard → destructive_check → …）全部是生产代码，未做任何替换。

// p002Enforcement 是 EnforcementStatus 的测试替身（各文件自带，避免耦合）。
// 注入的是**事实**（本机 OS 强制是否生效），不是判定 —— 判定链 100% 生产码。
type p002Enforcement struct{ active bool }

func (f p002Enforcement) OSEnforcementActive() bool { return f.active }

// p002Pipeline 经**生产入口**装配（InitPipelineWithHooks），并只替换执行端 sink。
func p002Pipeline(t *testing.T, mode permission.Level) *Executor {
	t.Helper()
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(mode), workDir)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}
	// 只替换 sink：证明「决策说放行」时不会真的执行系统命令。
	stub := func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "STUBBED-NOT-EXECUTED", nil
	}
	e.handlers["bash"] = stub
	e.handlers["Monitor"] = stub
	return e
}

func p002StageMessage(pr *PipelineResult, stage string) string {
	for _, l := range pr.StageLogs {
		if l.Stage == stage {
			return l.Message
		}
	}
	return ""
}

func p002HasStage(pr *PipelineResult, stage string) bool {
	for _, l := range pr.StageLogs {
		if l.Stage == stage {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 判别器
// ---------------------------------------------------------------------------

// TestFix_P002_MonitorDangerousCommandMustConfirm 是 ★S-1 的主判别器。
//
// BEFORE 必然红：沙箱生效组 `NeedConfirm=false` 且 `Proceed=true`（= 静默执行）。
//
// 断言分三层，缺一层都可能被蒙对：
// 1. 决定性事实：NeedConfirm=true 且 !Proceed；
// 2. 归因到**危险档**：留痕必须带 cmdrules 的**规则 ID**（结构化事实，R4）；
// 3. 结构性证明：出现 dangerous_guard，且**不出现** permission —— 后者是
// 「豁免出口根本没被走到」的机械证据（光看 NeedConfirm 分不清是谁拦下的）。
func TestFix_P002_MonitorDangerousCommandMustConfirm(t *testing.T) {
	// 覆盖三种沙箱事实：危险档必须在**任意**沙箱状态下都拦得住。
	envs := []struct {
		label  string
		active bool
		inject bool
	}{
		{"沙箱未注入", false, false},
		{"沙箱显式未生效", false, true},
		{"沙箱生效", true, true}, // ★ 就是这条路径上 Monitor 被免问放行
	}

	for _, env := range envs {
		for _, tool := range []string{"Monitor", "bash"} {
			t.Run(env.label+"/"+tool, func(t *testing.T) {
				e := p002Pipeline(t, permission.LevelWorkspaceWrite)
				if env.inject {
					e.SetEnforcementStatus(p002Enforcement{active: env.active})
				}

				pr := e.ExecuteWithConfirm(context.Background(), Call{
					ID:    "p002-dangerous",
					Name:  tool,
					Input: `{"command":"shutdown -h now"}`,
				})

				if !pr.NeedConfirm || pr.Proceed {
					t.Fatalf("危险命令必须强制确认（tool=%s）: NeedConfirm=%v Proceed=%v logs=%+v",
						tool, pr.NeedConfirm, pr.Proceed, pr.StageLogs)
				}
				msg := p002StageMessage(pr, "dangerous_guard")
				if !strings.Contains(msg, "rule=shutdown") {
					t.Fatalf("危险档留痕必须带规则 ID `shutdown`（tool=%s）: %q logs=%+v",
						tool, msg, pr.StageLogs)
				}
				if p002HasStage(pr, "permission") {
					t.Fatalf("危险档必须在 PermissionStage **之前**短路（否则说明它靠后面那道拦，"+
						"而后面那道含豁免出口）: logs=%+v", pr.StageLogs)
				}
			})
		}
	}
}

// TestFix_P002_MonitorDestructiveCommandMustConfirm 覆盖 ★S-1 的第二半（步骤 ②）：
// **破坏性档**（有副作用、需确认、非灾难级）同样必须认出 Monitor。
//
// BEFORE 必然红：`checkDestructive` 的 `case "bash":` 让 Monitor 落 default ⇒ 不命中，
// 随后 PermissionStage 出口 (c) 用真 SSOT 免问 ⇒ NeedConfirm=false。
//
// 命令选 `rm -rf build` 的理由（不猜，逐条核对 cmdrules）：
// - 命令位 `rm` ∈ confirmCommandNames ⇒ **破坏性**（本阶段负责的那一档）；
// - 不命中任何 dangerousPatterns（rm 族危险规则全部要求根/家目录路径，
// 且 `rm -rf /` 是"含子路径"的子串）⇒ **不是危险档** ⇒ 前一个阶段不会短路它。
//
// 这样才落在 DestructiveCheckStage 负责的那一维上（R4-12 的"红在哪一维"自检）。
func TestFix_P002_MonitorDestructiveCommandMustConfirm(t *testing.T) {
	e := p002Pipeline(t, permission.LevelWorkspaceWrite)
	e.SetEnforcementStatus(p002Enforcement{active: true})

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "p002-destructive",
		Name:  "Monitor",
		Input: `{"command":"rm -rf build"}`,
	})

	if !pr.NeedConfirm || pr.Proceed {
		t.Fatalf("破坏性命令必须要求确认（Monitor）: NeedConfirm=%v Proceed=%v logs=%+v",
			pr.NeedConfirm, pr.Proceed, pr.StageLogs)
	}
	msg := p002StageMessage(pr, "destructive_check")
	if !strings.Contains(msg, "destructive operation detected") {
		t.Fatalf("破坏性档留痕缺失（Monitor 可能仍落在 default 分支）: %q logs=%+v", msg, pr.StageLogs)
	}
	if p002HasStage(pr, "permission") {
		t.Fatalf("破坏性档必须在 PermissionStage 之前短路: logs=%+v", pr.StageLogs)
	}
}

// TestFix_P002_ProductionSandboxManagerWiring 走**真实生产装配**再验一遍。
//
// 为什么要有这条：上面两条用 `SetEnforcementStatus` 注入「本机在强制」这一**事实** ——
// 它证明的是"**如果**事实被注入正确，判定就对"，没触碰"事实在生产上会不会被注入"。
// 按本仓铁律（技能 `production-path-tdd` R2）不得把"单测能构造"当成"生产会发生"。
// 本机（或 CI 机）无法强制时**跳过**：那是环境限制，不是判据错误。
func TestFix_P002_ProductionSandboxManagerWiring(t *testing.T) {
	workDir := t.TempDir()
	mgr := sandbox.NewProductionSandboxManager(workDir)
	if !sandbox.EnforcementActive(mgr) {
		t.Skipf("本机生产装配不可强制，端到端装配不成立（环境限制，非判据错误）: %+v",
			sandbox.IsolationStatusOf(mgr))
	}

	for _, c := range []struct{ label, input string }{
		{"危险档", `{"command":"shutdown -h now"}`},
		{"破坏性档", `{"command":"rm -rf build"}`},
	} {
		t.Run(c.label, func(t *testing.T) {
			e := NewExecutor(NewRegistry(), workDir)
			e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
			e.SetSandboxMgr(mgr)
			e.handlers["Monitor"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
				return "STUBBED-NOT-EXECUTED", nil
			}

			pr := e.ExecuteWithConfirm(context.Background(), Call{
				ID: "p002-prod", Name: "Monitor", Input: c.input,
			})
			if !pr.NeedConfirm {
				t.Fatalf("真实生产装配下（沙箱可强制）Monitor 的%s命令仍被免问 ⇒ 静默执行: logs=%+v",
					c.label, pr.StageLogs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 守卫（防"改过头"）—— 本条在 BEFORE/AFTER 两态均绿，**不具判别性**，
// 只承担回归职责（防把 Monitor 的普通命令也拖进确认）
// ---------------------------------------------------------------------------

// TestGuard_P002_MonitorOrdinaryCommandStillExempt 普通监控命令（长跑进程）必须**保持免问** ——
// 否则 P-002 的修法就把「沙箱内免问」这条既有契约误伤了（用户会为 `npm run dev` 反复点确认）。
//
// ⚠️ 本条断言的是**管线面**（不弹确认框）。`npm run dev` 的**端到端**归宿另有一层：
// `security.CheckCommand` 把它判 `Ask` ⇒ `monitor.Manager.Start` fail-closed 拒绝
// （既有行为，见台账 §2.7 P-014）。⇒ 别把本条读成"npm run dev 能正常跑起来"。
func TestGuard_P002_MonitorOrdinaryCommandStillExempt(t *testing.T) {
	e := p002Pipeline(t, permission.LevelWorkspaceWrite)
	e.SetEnforcementStatus(p002Enforcement{active: true})

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "p002-ordinary",
		Name:  "Monitor",
		Input: `{"command":"npm run dev"}`,
	})
	if pr.NeedConfirm {
		t.Fatalf("普通监控命令应保持沙箱内免问: logs=%+v", pr.StageLogs)
	}
	// 自检（防空绿）：必须看到沙箱豁免留痕，否则可能是被别的分支蒙对。
	msg := p002StageMessage(pr, "permission")
	if !strings.Contains(msg, "sandbox-bounded") {
		t.Fatalf("未观察到沙箱豁免留痕（断言可能被其它分支蒙对，不可信）: %q logs=%+v", msg, pr.StageLogs)
	}
}

// TestGuard_P002_NonCommandToolsUnaffected 非命令载体不得被顺带拖进危险/破坏性判定。
func TestGuard_P002_NonCommandToolsUnaffected(t *testing.T) {
	for _, name := range []string{"write_file", "read_file"} {
		if permission.IsCommandBearingTool(name) {
			t.Fatalf("%q 不得被认作命令载体（否则它的入参会被当成命令去匹配危险规则）", name)
		}
	}
}
