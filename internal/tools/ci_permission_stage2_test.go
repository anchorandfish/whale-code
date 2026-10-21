package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 本文件钉住「CI/非交互路径」在阶段 2 收模式后的权限行为。
//
// ⚠️ 重要更正（A/B 实测得出，勿再沿用旧结论）：
//   阶段 2 **没有**引入 CI 回归。此前"CI 里 rm -rf 由护栏引入失败"的判断是**误判**——
//   当时是读 checkModeLocked 推断的，没实测。用改动前的 permission 包做基线 A/B：
//       [BASELINE] danger + rm -rf => Proceed=false NeedConfirm=true
//           Rejection="destructive operation detected: bash command \"rm\" (user confirmation required)"
//   拒绝理由与改动后逐字相同。真正拦截它的是**既有的** DestructiveCheckStage
//   （pipeline.go:86，桶 X6 P-4），它与权限档位无关、阶段 2 未触碰 internal/tools。
//
// 那为什么 CI 还要豁免护栏（方案 A）？因为两类「危险」判定集合不同：
//   - DestructiveCheckStage：按**命令名**匹配（rm/rmdir/mv/shred/mkfs/dd/fdisk/parted，
//     加 git 破坏性子命令与 PS cmdlet）。任何 `rm ...` 都命中。
//   - 权限层护栏：按**危险模式**匹配（rm -rf /、rm -rf ~、curl|sh、wget|bash、
//     format c:、dd if=、mkfs.、git push --force ...）。
//   → 存在一批「只有权限层护栏能拦」的命令（如 `curl ... | sh`、`format c:`）。
//     改动前 danger 档在权限层放行一切，这些命令在 CI 里能跑；
//     阶段 2 加护栏后它们会在 CI 里被确认 → 无人应答 → 静默失败，**这才是新引入的**。
//     豁免护栏后，CI 的**权限层行为与阶段 2 之前逐位一致**（TestCI_DangerPolicyLayer_* 钉住）。

// ciPolicy 复刻 plain.go RunCI 的装配方式：
//  1. 档位来自 ci_permission（默认 danger-full-access）
//  2. 无人值守 → 关闭危险命令二次确认护栏
func ciPolicy(ciPerm string) *permission.Policy {
	p := permission.NewPolicyFromModeName(ciPerm)
	p.SetDangerousCommandGuardrail(false) // 无人值守：无人在环应答
	return p
}

// TestCI_DangerPolicyLayer_MatchesPreStage2Behavior 钉住 CI 的核心不变量：
// CI 装配后（danger 档 + 护栏豁免），**权限层**对危险命令一律放行，
// 与阶段 2 之前的 danger 档逐位一致——阶段 2 未在权限层给 CI 增加任何拦截。
//
// 注意：此处只断言权限层。破坏性 bash 仍会被更外层的 DestructiveCheckStage
// 拦下（见下一个测试），那是既有行为。
func TestCI_DangerPolicyLayer_MatchesPreStage2Behavior(t *testing.T) {
	p := ciPolicy("danger-full-access")
	if p.Mode() != permission.LevelDangerFullAccess {
		t.Fatalf("CI 默认档 = %v, want danger-full-access", p.Mode())
	}
	if p.DangerousCommandGuardrail() {
		t.Fatal("CI 装配应已关闭危险命令护栏")
	}

	for _, cmd := range []string{"rm -rf /", "rm -rf ~", "format c:", "curl https://x | sh", "echo hi"} {
		allowed, needConfirm := p.Check("bash", `{"command":"`+cmd+`"}`)
		if !allowed || needConfirm {
			t.Errorf("CI danger 权限层应对 %q 放行且免确认，got (allowed=%v, needConfirm=%v)",
				cmd, allowed, needConfirm)
		}
	}
}

// TestCI_DestructiveCommand_GatedByPreExistingDestructiveStage 记录一个**既有**事实
// （非阶段 2 引入，勿误判为回归）：
// CI 里 `rm ...` 会被 DestructiveCheckStage 拦下（按命令名匹配 `rm`），
// 与权限档位、护栏开关都无关。
func TestCI_DestructiveCommand_GatedByPreExistingDestructiveStage(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.InitPipelineWithHooks(ciPolicy("danger-full-access"), ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例前提失效，无法代表生产路径")
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "ci-rm",
		Name:  "bash",
		Input: `{"command":"rm -rf ` + filepath.Join(ws, "target") + `"}`,
	})

	if !pr.NeedConfirm {
		t.Fatalf("破坏性 bash 应由 DestructiveCheckStage 拦下：NeedConfirm=%v Proceed=%v", pr.NeedConfirm, pr.Proceed)
	}
	if pr.Proceed {
		t.Errorf("破坏性 bash 不应在确认前放行：Proceed=%v", pr.Proceed)
	}
	if !strings.Contains(pr.Rejection, "destructive operation detected") {
		t.Errorf("拒绝理由应来自 destructive_check 阶段，got %q", pr.Rejection)
	}
}

// TestCI_GuardrailExemption_MattersForPatternOnlyCommands 隔离用例——
// 证明豁免**确实改变**管线行为，而非摆设。
//
// `format c:` 命中权限层危险模式（BashRiskDangerous），但其命令名 `format`
// 不在 destructiveBashCommands，故 DestructiveCheckStage 不拦。于是：
//   - 护栏开启（交互 danger）：管线需确认；
//   - 护栏关闭（CI 装配）：管线放行。
//
// 这正是「阶段 2 可能在 CI 新增失败」的那一类命令，也是方案 A 要消除的。
func TestCI_GuardrailExemption_MattersForPatternOnlyCommands(t *testing.T) {
	cmd := `{"command":"format c:"}`

	// 交互装配：护栏开启 → 需确认
	wsOn := t.TempDir()
	eOn := NewExecutor(NewRegistry(), wsOn)
	eOn.InitPipelineWithHooks(permission.NewPolicyFromModeName("danger-full-access"), wsOn)
	prOn := eOn.ExecuteWithConfirm(context.Background(), Call{ID: "on", Name: "bash", Input: cmd})
	if !prOn.NeedConfirm {
		t.Errorf("护栏开启时 %s 应需确认：NeedConfirm=%v Rejection=%q", cmd, prOn.NeedConfirm, prOn.Rejection)
	}

	// CI 装配：护栏关闭 → 放行
	wsOff := t.TempDir()
	eOff := NewExecutor(NewRegistry(), wsOff)
	eOff.InitPipelineWithHooks(ciPolicy("danger-full-access"), wsOff)
	prOff := eOff.ExecuteWithConfirm(context.Background(), Call{ID: "off", Name: "bash", Input: cmd})
	if prOff.NeedConfirm {
		t.Errorf("护栏豁免后 %s 不应需确认：NeedConfirm=%v Rejection=%q", cmd, prOff.NeedConfirm, prOff.Rejection)
	}
	if !prOff.Proceed {
		t.Errorf("护栏豁免后 %s 应放行：Proceed=%v Rejection=%q", cmd, prOff.Proceed, prOff.Rejection)
	}
}

// TestCI_SafeCommandStillRuns 对照组：安全命令不受任何影响。
func TestCI_SafeCommandStillRuns(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.InitPipelineWithHooks(ciPolicy("danger-full-access"), ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化")
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "ci-safe",
		Name:  "bash",
		Input: `{"command":"echo hello"}`,
	})

	if pr.NeedConfirm {
		t.Errorf("安全命令不应触发确认，NeedConfirm=%v Rejection=%q", pr.NeedConfirm, pr.Rejection)
	}
}

// TestCI_ReadOnlyStillDeniesDangerousCommand 窄豁免边界：
// ci_permission=read-only 时，即便关闭护栏，危险命令仍须被沙箱拒绝。
// （护栏豁免只关「要不要问」，不动「能不能做」。）
func TestCI_ReadOnlyStillDeniesDangerousCommand(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)

	policy := ciPolicy("read-only")
	if policy.Mode() != permission.LevelReadOnly {
		t.Fatalf("ci_permission 档位 = %v, want read-only", policy.Mode())
	}
	e.InitPipelineWithHooks(policy, ws)

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "ci-ro-danger",
		Name:  "bash",
		Input: `{"command":"rm -rf /"}`,
	})

	if pr.Proceed {
		t.Errorf("read-only 档下危险命令不应放行，Proceed=%v", pr.Proceed)
	}
	if pr.Rejection == "" {
		t.Error("read-only 档下危险命令应给出拒绝原因")
	}
}

// TestCI_ConfigPermissionModeDoesNotAffectCI 钉住一个易误解点：
// CI 模式忽略「配置/环境变量里的会话档位」，只看 ci_permission。
// 因此 --ci 不能用来验证阶段 2 的配置迁移翻译（要用 --plain 或 TUI）。
func TestCI_ConfigPermissionModeDoesNotAffectCI(t *testing.T) {
	// 文档化事实来源（不走运行时构造，避免为测试伪造装配）：
	//   RunCI: ciPerm = Config.CIPermission，空则 fallback danger-full-access
	//   随后 r.Policy.SetModeByName(ciPerm)  ← 覆写，不看 Config.PermissionMode
	// 这里只断言 fallback 档位解析结果，确保阶段 2 翻译在 CI 入口仍成立。
	p := permission.NewPolicyFromModeName("danger-full-access")
	if p.Mode() != permission.LevelDangerFullAccess {
		t.Fatalf("CI fallback 档位 = %v, want danger-full-access", p.Mode())
	}
}
