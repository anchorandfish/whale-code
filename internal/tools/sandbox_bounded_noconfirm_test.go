package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// —— 审批判据从「工具能力档位」改为「这一次调用是否能被兜住」。
//
// 本轮新增的第三个免问出口：
//
//	(c) 进程类工具（bash / Monitor）× OS 沙箱**真的在强制** ⇒ 免问。
//
// 判据的三问：① 越界了吗 ② 不可逆吗 ③ **强制机制真的生效吗**。
// ① 由 CustomValidateStage / workspaceContainedWrite 回答；② 由 DestructiveCheckStage
// 回答（它排在 PermissionStage 之前且短路，所以破坏性命令根本到不了这里）；
// ③ 就是本文件针对的那一问 —— 此前 permission 层**完全拿不到**这个事实。
//
// 为什么 ③ 必须是硬前提（而不是"锦上添花"）：`degradedSandbox.Restrict()` 是
// **`return nil`**（零强制，os_sandbox.go:184）。若把 ③ 略去，在"沙箱装不上的机器"
// 上就变成「也不问，也没人兜」= 静默放行。对标 同类 CLI 同一条：
// core/src/safety.rs:78-98 —— 只有真能强制才 AutoApprove，否则退回 AskUser。

// n040Enforcement 是 EnforcementStatus 的测试替身。
type n040Enforcement struct{ active bool }

func (f n040Enforcement) OSEnforcementActive() bool { return f.active }

// n040Pipeline 经**生产入口**构造管线（InitPipelineWithHooks），
// 不使用 NewToolPipeline 直连 —— 保证走的是生产装配路径。
func n040Pipeline(t *testing.T, mode permission.Level) *Executor {
	t.Helper()
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(mode), workDir)
	return e
}

// n040StageMessage 取某阶段的留痕 Message。
func n040StageMessage(pr *PipelineResult, stage string) string {
	for _, l := range pr.StageLogs {
		if l.Stage == stage {
			return l.Message
		}
	}
	return ""
}

// n040PermissionTrail 取 permission 阶段留痕（用于"是否真的走到了那一层"的自检）。
func n040PermissionTrail(pr *PipelineResult) (string, bool) {
	for _, l := range pr.StageLogs {
		if l.Stage == "permission" {
			return l.Message, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 判别器
// ---------------------------------------------------------------------------

// TestFix_N040_SandboxBoundedNeedsNoConfirm 是主判别器：
// 沙箱真在强制 ⇒ 进程类命令免问。
//
// BEFORE（改前）必然红：`bash` 声明 WW ⇒ checkModeLocked 返回 (true,true) ⇒ NeedConfirm。
func TestFix_N040_SandboxBoundedNeedsNoConfirm(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"bash", `{"command":"ls -la"}`},
		{"bash", `{"command":"grep -rn TODO internal/"}`},
		{"bash", `{"command":"git status"}`},
		{"Monitor", `{"command":"echo hi"}`},
	}

	for _, c := range cases {
		e := n040Pipeline(t, permission.LevelWorkspaceWrite)
		e.SetEnforcementStatus(n040Enforcement{active: true})

		pr := e.ExecuteWithConfirm(context.Background(), Call{
			ID:    "n040-main",
			Name:  c.name,
			Input: c.input,
		})

		if pr.NeedConfirm {
			t.Fatalf("沙箱真在强制时进程类命令不应弹确认: tool=%s input=%s reject=%q logs=%+v",
				c.name, c.input, pr.Rejection, pr.StageLogs)
		}
		// 自检（防空绿）：必须看到 permission 阶段的「沙箱豁免」留痕。
		// 只断言 !NeedConfirm 会被"前置阶段的提前拒绝"蒙对 —— 那样测的不是本豁免。
		msg, reached := n040PermissionTrail(pr)
		if !reached || !strings.Contains(msg, "sandbox-bounded") {
			t.Fatalf("未观察到沙箱豁免留痕（断言可能被提前拒绝蒙对，不可信）: tool=%s reachedPermission=%v permissionMsg=%q logs=%+v",
				c.name, reached, msg, pr.StageLogs)
		}
	}
}

// TestFix_N040_BoundaryCheckFailsClosed 覆盖第 3 步：边界判据的「未知」必须是一等状态。
//
// BEFORE（改前）必然红：workDir=="" 时 checkWorkspaceBoundary 返回 `(true, "")` ——
// 「没有判定」被报成「在工作区内」，于是任何 `if ok` 的读法都会静默 fail-open。
//
// AFTER：报 `BoundaryUnknown`，且「未知」不可被任何读法当成「在内部」。
func TestFix_N040_BoundaryCheckFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		workDir string
	}{
		{"工作区未配置", "/tmp/n040-anything.txt", ""},
		{"工作区未配置且路径相对", "n040-relative.txt", ""},
	}
	for _, c := range cases {
		v, reason := checkWorkspaceBoundary(c.path, c.workDir)
		if v != BoundaryUnknown {
			t.Fatalf("%s：不可判定必须报 BoundaryUnknown，实际 %v reason=%q", c.name, v, reason)
		}
		if v.ProvablyInside() {
			t.Fatalf("%s：不可判定不得被读成「在边界内」（fail-open）", c.name)
		}
		if v.ProvablyOutside() {
			t.Fatalf("%s：不可判定也不得被读成「越界」（会把未知当越界拒绝用户）", c.name)
		}
	}
}

// TestGuard_N040_VerdictZeroValueIsUnknown 锁住三值判据的**零值语义**。
//
// 为什么这条守卫是承重的：`workspaceContainedWrite` 现在**已删掉** `workDir == ""`
// 的单独守卫，它的 fail-closed 完全依赖「未显式赋值的 verdict 落在安全侧」这一条
// （见 BoundaryVerdict 文档）。若有人重排 const 块、把 BoundaryInside 挪到零值位置，
// 豁免闸就会在「未知」时静默放行 —— 而那正是 唯一的真实风险点。
func TestGuard_N040_VerdictZeroValueIsUnknown(t *testing.T) {
	var zero BoundaryVerdict
	if zero != BoundaryUnknown {
		t.Fatalf("零值必须是 BoundaryUnknown（安全侧），实际 %v —— 重排 const 会破坏豁免闸的 fail-closed", zero)
	}
	if zero.ProvablyInside() {
		t.Fatal("零值不得被读成「在边界内」")
	}
	if zero.ProvablyOutside() {
		t.Fatal("零值不得被读成「越界」")
	}
}

// ---------------------------------------------------------------------------
// 端到端装配（走**生产入口**，不用测试替身）
// ---------------------------------------------------------------------------

// TestFix_N040_ProductionManagerWiring 是本轮最关键的**接线**判别器。
//
// 为什么非要有这一条：上面 `TestFix_N040_SandboxBoundedNeedsNoConfirm` 用测试替身
// 直接喂 `SetEnforcementStatus(active=true)` —— 它证明的是"**如果**事实被注入正确，
// 判定就对"，**完全没有**触碰"事实在生产上到底会不会被注入"。按本仓铁律
// （见技能 `production-path-tdd`）：**别把"单测能构造"当成"生产会发生"** ⇒
// 必须经真实入口（`sandbox.NewProductionSandboxManager` + `SetSandboxMgr`，
// 与 `tui/lifecycle.go:519` / `EnableProductionSandbox` 同源）跑一遍。
//
// 三个断言各承担一件事：
// 1. 正序装配（先建管线、后注入管理器 —— 与 lifecycle.go 相同的顺序）⇒ 免问生效；
// 2. 反序装配（先注入、后建管线）⇒ **同样**生效（这是 reconcileEnforcement 存在的理由：
// 否则"沙箱内免问"会时灵时不灵）；
// 3. 摘掉管理器（= `tui/sandbox_toggle.go:sandboxDisable` 的同一个调用）⇒ 立刻退回询问。
// 第 3 条是本轮真正的安全边：`/sandbox off` 之后若仍免问，那条命令就变成
// **审批轴上的静默放宽**（命令名承诺"减少限制"、实际在另一个轴上扩权）——
// 正是 sandbox_toggle.go 头注释里登记过的第 ③ 类事故。
//
// 本机无法强制（Windows / 内核无 Landlock / 缺 sandbox-exec / 用户放行网络）时跳过：
// 那是**环境限制，不是判据错误**，判别器刻意不把环境不具备当失败。
func TestFix_N040_ProductionManagerWiring(t *testing.T) {
	workDir := t.TempDir()
	mgr := sandbox.NewProductionSandboxManager(workDir)
	if !sandbox.EnforcementActive(mgr) {
		t.Skipf("本机生产装配不可强制，端到端装配不成立（环境限制，非判据错误）: %+v",
			sandbox.IsolationStatusOf(mgr))
	}

	// --- 1) 正序装配：先建管线，后注入管理器（lifecycle.go 的顺序） ---
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
	e.SetSandboxMgr(mgr)

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "n040-prod-on", Name: "bash", Input: `{"command":"git status"}`,
	})
	if pr.NeedConfirm {
		t.Fatalf("生产管理器已注入且本机可强制，进程类命令仍要确认 ⇒ 事实没被接到判定上: logs=%+v", pr.StageLogs)
	}
	// 自检（防空绿）：必须看到沙箱豁免留痕，否则可能是别的分支蒙对。
	if msg, reached := n040PermissionTrail(pr); !reached || !strings.Contains(msg, "sandbox-bounded") {
		t.Fatalf("未观察到沙箱豁免留痕（断言可能被其它分支蒙对，不可信）: reached=%v msg=%q logs=%+v",
			reached, msg, pr.StageLogs)
	}

	// --- 2) 反序装配：先注入管理器、后建管线 ---
	eRev := NewExecutor(NewRegistry(), workDir)
	eRev.SetSandboxMgr(mgr) // 此刻 Pipeline == nil ⇒ 全靠 InitPipeline 里的 reconcile 重放
	eRev.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
	prRev := eRev.ExecuteWithConfirm(context.Background(), Call{
		ID: "n040-prod-rev", Name: "bash", Input: `{"command":"git status"}`,
	})
	if prRev.NeedConfirm {
		t.Fatalf("反序装配丢失了强制状态 ⇒「沙箱内免问」会时灵时不灵（接线顺序敏感）: logs=%+v", prRev.StageLogs)
	}

	// --- 3) 摘掉管理器 ⇒ 必须立刻退回询问（`/sandbox off` 的安全边） ---
	e.SetSandboxMgr(nil)
	prOff := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "n040-prod-off", Name: "bash", Input: `{"command":"git status"}`,
	})
	if !prOff.NeedConfirm {
		t.Fatalf("沙箱被摘掉后仍免问 ⇒ `/sandbox off` 会变成审批轴上的静默放宽（既无边界也不问）: logs=%+v",
			prOff.StageLogs)
	}
}

// ---------------------------------------------------------------------------
// 守卫（防"改过头"）—— 这些在 BEFORE 态本来就应该绿，承担回归职责
// ---------------------------------------------------------------------------

// TestGuard_N040_SandboxBoundedStillConfirms_WhenSandboxAbsent 是本轮**最关键**的守卫：
// 沙箱不可强制（未注入 / degraded / 网络放行 / Windows）时必须退回"问"。
//
// 若这条守卫变红，含义是「在没有强制手段的机器上静默放行了任意命令」——
// 即 唯一真正的风险点。
func TestGuard_N040_SandboxBoundedStillConfirms_WhenSandboxAbsent(t *testing.T) {
	e := n040Pipeline(t, permission.LevelWorkspaceWrite)
	// 刻意不调用 SetEnforcementStatus：未注入 = 不可强制（fail-closed 默认）。
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n040-absent",
		Name:  "bash",
		Input: `{"command":"mkdir -p n040_probe"}`,
	})
	if !pr.NeedConfirm {
		t.Fatalf("沙箱不可强制时必须仍然询问，实际 NeedConfirm=false（静默放行！）logs=%+v", pr.StageLogs)
	}
}

// TestGuard_N040_ExplicitlyInactiveStillConfirms 显式 false（例如 Platform=degraded）同样必须问。
func TestGuard_N040_ExplicitlyInactiveStillConfirms(t *testing.T) {
	e := n040Pipeline(t, permission.LevelWorkspaceWrite)
	e.SetEnforcementStatus(n040Enforcement{active: false})
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n040-inactive",
		Name:  "bash",
		Input: `{"command":"mkdir -p n040_probe"}`,
	})
	if !pr.NeedConfirm {
		t.Fatalf("强制机制报告未生效时必须仍然询问，实际 NeedConfirm=false logs=%+v", pr.StageLogs)
	}
}

// TestGuard_N040_DestructiveStillConfirms_WithSandboxEnforced 护栏不被豁免：
// 不可逆命令即使沙箱在也仍要问（沙箱管不住"已经删掉了"）。
func TestGuard_N040_DestructiveStillConfirms_WithSandboxEnforced(t *testing.T) {
	e := n040Pipeline(t, permission.LevelWorkspaceWrite)
	e.SetEnforcementStatus(n040Enforcement{active: true})
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n040-destructive",
		Name:  "bash",
		Input: `{"command":"rm -rf /"}`,
	})
	if !pr.NeedConfirm {
		t.Fatalf("破坏性命令必须仍然确认（豁免不得覆盖不可逆护栏），实际 NeedConfirm=false logs=%+v", pr.StageLogs)
	}
}

// TestGuard_N040_ReadOnlyStillDenies 只读档下进程类仍被拒绝：豁免只抑制"问"，不改"拒"。
func TestGuard_N040_ReadOnlyStillDenies(t *testing.T) {
	e := n040Pipeline(t, permission.LevelReadOnly)
	e.SetEnforcementStatus(n040Enforcement{active: true})
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n040-ro",
		Name:  "bash",
		Input: `{"command":"ls -la"}`,
	})
	if pr.NeedConfirm {
		t.Fatalf("只读档应是拒绝而不是询问，实际 NeedConfirm=true logs=%+v", pr.StageLogs)
	}
	if pr.Proceed {
		t.Fatalf("只读档必须拒绝进程类命令，实际 Proceed=true logs=%+v", pr.StageLogs)
	}
	if pr.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("只读档拒绝的 FailureKind 应为 policy_denied，实际 %q", pr.FailureKind)
	}
}

// TestGuard_N040_DenyRuleBeatsSandboxExemption deny 规则绝对优先。
func TestGuard_N040_DenyRuleBeatsSandboxExemption(t *testing.T) {
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	// 显式 deny 规则（用生产入口 AddRule，而不是自造字段 —— 免得测的是一个
	// 只有测试能构造出来的状态）。
	if err := policy.AddRule(permission.PermissionRule{
		ToolName: "bash",
		Behavior: permission.RuleDeny,
		Source:   permission.SourceSession,
	}); err != nil {
		t.Fatalf("构造 deny 规则失败: %v", err)
	}
	e.InitPipelineWithHooks(policy, workDir)
	e.SetEnforcementStatus(n040Enforcement{active: true})

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n040-deny",
		Name:  "bash",
		Input: `{"command":"ls -la"}`,
	})
	if pr.Proceed {
		t.Fatalf("deny 规则必须绝对优先，实际 Proceed=true logs=%+v", pr.StageLogs)
	}
	if pr.NeedConfirm {
		t.Fatalf("deny 是拒绝而非询问，实际 NeedConfirm=true logs=%+v", pr.StageLogs)
	}
}

// TestGuard_N040_NonProcessToolsUnaffected 豁免只覆盖"效果全在沙箱内"的工具；
// 其它需要写档位的工具（有进程内副作用）不得被顺带免问。
func TestGuard_N040_NonProcessToolsUnaffected(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// 进程内写 ~/.whale/（沙箱管不到），且另有进程内持久化 → 仍须问
		{"TaskCreate", `{"command":"echo hi"}`},
		{"config_set", `{"key":"permission_level","value":"danger-full-access"}`},
		{"CronDelete", `{"job_id":"n040-none"}`},
	}
	for _, c := range cases {
		e := n040Pipeline(t, permission.LevelWorkspaceWrite)
		e.SetEnforcementStatus(n040Enforcement{active: true})
		pr := e.ExecuteWithConfirm(context.Background(), Call{
			ID:    "n040-nonprocess",
			Name:  c.name,
			Input: c.input,
		})
		if !pr.NeedConfirm {
			t.Fatalf("非进程类工具不应被沙箱豁免: tool=%s NeedConfirm=false logs=%+v", c.name, pr.StageLogs)
		}
	}
}

// TestGuard_N040_WorkspaceEscapeWriteStillNotExempt 越界写仍不被写豁免覆盖（回归）。
func TestGuard_N040_WorkspaceEscapeWriteStillNotExempt(t *testing.T) {
	e := n040Pipeline(t, permission.LevelWorkspaceWrite)
	e.SetEnforcementStatus(n040Enforcement{active: true})
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n040-escape",
		Name:  "write_file",
		Input: `{"path":"/tmp/n040-escape-outside.txt","content":"x"}`,
	})
	if pr.Proceed {
		t.Fatalf("越界写必须被拒（或至少不可直接放行），实际 Proceed=true logs=%+v", pr.StageLogs)
	}
}
