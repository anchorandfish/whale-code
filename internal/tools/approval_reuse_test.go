package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// 请求内审批复用—— 「同一次用户请求内、逐字相同的调用只问一次」
// ============================================================================
//
// 缺陷签名（用户手测 12.7.1）：「我还是出现了确认框，还是两次确认」——
// 模型在同一次用户请求里对**逐字相同**的调用（`ls -la`）发了两次，确认层逐调用裁决
// ⇒ 用户被问了两次。第二次询问不提供任何新的决策信息：用户对同一个确切输入已经说过 Yes。
//
// 修法（用户 2026-09-20 拍板方案 (i)）：同一次用户请求内、`(Name, Input)` 逐字相同的
// 调用复用刚批准的决定。作用域 = **本次用户请求**（RequestID 生命周期）。
//
// ★ 判据落在 PermissionStage（pipeline.go 的 (r) 出口），**不是**折成 `call.Approved`：
// DangerousGuardStage(:991) 与 DestructiveCheckStage(:1080) 都读 `Approved` 并因此免问。
// 若把复用折成 Approved，危险命令会随复用**一起免问** ⇒ 突破 -R1
// 「危险档 = 强制确认永不免问」。两个危险阶段排在 PermissionStage **之前**且带 NeedConfirm
// ⇒ 管线在 :323 短路 ⇒ 危险面**结构性地**落在复用范围之外（不靠第二套"是否危险"判据）。
//
// 本文件在**生产入口**（`InitPipelineWithHooks` → `ExecuteWithConfirm`）上判别。

// approvalReusePipeline 经生产入口装配 ww 档管线，**刻意不注入 enforcement**。
//
// 为什么不注入：「进程类 × 沙箱真在强制 ⇒ 免问」这条出口 (c) 与复用是**两条独立的免问理由**
// （(c) 排在复用判定之后）。一旦注入 enforcement，`bash` 会被 (c) 抢先免问
// ⇒ 永远轮不到复用分支 ⇒ 本用例失去判别力。此处要单独考察复用那一条。
func approvalReusePipeline(t *testing.T, workDir string) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}
	return e
}

// approvalReuseExeOutside 注入「运行中的二进制不在 whale 自身树内」这一**生产输入**。
//
// 为什么必须注入：若 exe 被判在自身树内，PermissionStage 的 (0) 自指保护闸
// 排在复用分支**之前**，会把调用改判成"必须问" ⇒ 复用分支根本到不了。
// 注入的是**输入值**（exe 在哪），不是替换实现、不是置空守卫。
func approvalReuseExeOutside(t *testing.T) {
	t.Helper()
	old := permission.RunningExePath
	permission.RunningExePath = func() string { return filepath.Join(t.TempDir(), "whale") }
	t.Cleanup(func() { permission.RunningExePath = old })
}

// stageNames 把阶段留痕压成 "a>b>c" —— 供**结构面**断言用（R4：可判定的结构化事实
// 不得由散文关键词断言；「哪几个阶段跑过」本身就是一个结构化事实）。
func stageNames(pr *PipelineResult) string {
	names := make([]string, 0, len(pr.StageLogs))
	for _, l := range pr.StageLogs {
		names = append(names, l.Stage)
	}
	return strings.Join(names, ">")
}

// stageMessage 取某个阶段的留言（阶段未跑过则返回空串）。
func stageMessage(pr *PipelineResult, stage string) string {
	for _, l := range pr.StageLogs {
		if l.Stage == stage {
			return l.Message
		}
	}
	return ""
}

// approvalReuseCmd 返回本文件用的「逐字输入」样本。
//
// 历史：原缺陷（用户手测 12.7.1）里被问了两次的那条是 `ls -la`。
//
// ★ 2026-10-06 之后样本必须换：只读命令（`ls`/`echo`/`pwd`/`cat`/`seq`/`grep`/`sleep`）
// 由**只读免问出口 (e)** 直接放行（`cmdrules.AllSegmentsReadOnly`）⇒ `ls -la` **不再需要确认**
// ⇒ 拿它当样本，本文件所有断言都会失去判别力（实测：两个用例直接变红）。
//
// 新样本 `mkdir -p <workDir>/ar_probe` 满足两个必要条件：
//
//	· **不在**「任意参数下都只读」白名单内 ⇒ 出口 (e) 不生效；
//	· **不是**危险/破坏性命令（`rm -rf` / `git commit` 那类）⇒ 两个危险阶段不短路。
//
// ⇒ 「要不要问」只由**复用记忆**决定，正是本文件要考察的那条链。
func approvalReuseCmd(workDir string) string {
	return `{"command":"mkdir -p ` + filepath.Join(workDir, "ar_probe") + `"}`
}

// ---------------------------------------------------------------------------
// 主判别器
// ---------------------------------------------------------------------------

// TestFix_ApprovalReuse_IdenticalCallInSameRequestAskedOnce 是主判别器。
//
// 断言：同一次请求内、`(Name, Input)` 逐字相同的第二次调用**不得**再问。
//
// 判别力来自对照组：同样装配、同样输入，**唯一差异**是第二次置了 `ReusedApproval`
// ⇒ 第一次必须问、第二次必须不问。任何一侧不成立，本用例无判别力（会显式 FATAL）。
//
// ★ 本用例同时钉住 pipeline.go 里 `normalizedCall` 的字段透传：
// 该处是**重建**一个 Call，只挑了几个字段拷 —— 漏掉 `ReusedApproval` 会让 TUI 置的位
// 在进入 PermissionStage 之前被静默丢弃，表现为「改了但没生效」。若有人删掉那行透传，
// 本用例立刻转红（实测：删掉后报「应复用批准、不得二次询问」）。
func TestFix_ApprovalReuse_IdenticalCallInSameRequestAskedOnce(t *testing.T) {
	approvalReuseExeOutside(t)
	workDir := t.TempDir()
	e := approvalReusePipeline(t, workDir)
	cmd := approvalReuseCmd(workDir)

	// —— 对照组（前置）：未置位 ⇒ 必须问。这就是用户看到的"第一次确认"。
	first := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "ar-first", Name: "bash", Input: cmd,
	})
	if !first.NeedConfirm {
		t.Fatalf("前置不成立：首次调用本应要求确认，若不成立则本用例对「第二次是否复用」无判别力；"+
			"阶段链=%s", stageNames(first))
	}

	// —— 实验组：同请求内逐字相同 ⇒ 复用那次批准，不得再问。
	second := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "ar-second", Name: "bash", Input: cmd,
		ReusedApproval: true,
	})
	if second.NeedConfirm {
		t.Fatalf("同请求内逐字相同的调用应复用批准、不得二次询问（用户手测缺陷 12.7.1）；"+
			"阶段链=%s", stageNames(second))
	}
	if !second.Proceed {
		t.Fatalf("复用批准必须放行（不得只做到「不问」却仍不放行）；阶段链=%s", stageNames(second))
	}

	// 反假绿：钉住是**复用出口 (r)** 放的，而不是恰好被别的免问出口 (a)/(b)/(c) 放过。
	// 唯一能区分二者的是 permission 阶段的留言 —— 它同时也是该分支的结构化标识。
	if msg := stageMessage(second, "permission"); !strings.Contains(msg, "request-scoped approval reused") {
		t.Fatalf("放行必须来自复用出口 (r)；permission 阶段实际留言=%q，阶段链=%s", msg, stageNames(second))
	}
}

// ---------------------------------------------------------------------------
// 守卫：复用不得越过任何安全面
// ---------------------------------------------------------------------------

// TestGuard_ApprovalReuse_DangerousAndDestructiveStillAsk 是**安全性守卫**：
// 危险档 / 破坏性档的命令，即使调用方声称"复用"，也**必须仍然确认**。
//
// 为什么这条不会假绿：危险阶段排在 PermissionStage **之前**且带 NeedConfirm，
// 管线在 permission 阶段被调用之前就短路了 ⇒ 断言里额外要求**阶段链里不含 permission**
// （结构面），证明"危险面不在复用范围"是**靠 stage 顺序到不了**，
// 而不是靠某个出口"记得判断危险"。若有人把复用出口挪到危险阶段之前，本用例转红。
func TestGuard_ApprovalReuse_DangerousAndDestructiveStillAsk(t *testing.T) {
	cases := []struct {
		name      string
		command   string
		wantStage string
		why       string
	}{
		{
			name:      "不可逆-灾难级",
			command:   "rm -rf /",
			wantStage: "dangerous_guard",
			why:       "-R1：危险档 = 强制确认永不免问",
		},
		{
			name:      "破坏性-git",
			command:   "git reset --hard HEAD",
			wantStage: "destructive_check",
			why:       "破坏性档排在 PermissionStage 之前，复用判定到不了",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			approvalReuseExeOutside(t)
			e := approvalReusePipeline(t, t.TempDir())

			pr := e.ExecuteWithConfirm(context.Background(), Call{
				ID: "ar-guard-" + c.name, Name: "bash",
				Input:          `{"command":` + n077JSONString(c.command) + `}`,
				ReusedApproval: true, // ★ 即使声称"复用"，安全面也不得因此免问
			})

			if !pr.NeedConfirm {
				t.Fatalf("%s（%s）：必须仍然确认；阶段链=%s", c.name, c.why, stageNames(pr))
			}
			// 结构面：短路必须发生在 PermissionStage 之前 ⇒ 复用出口从未被咨询。
			if got := stageNames(pr); strings.Contains(got, "permission") {
				t.Fatalf("%s：安全面必须短路在 PermissionStage **之前**（否则复用出口会参与裁决）；"+
					"实际阶段链=%s", c.name, got)
			}
			if got := stageNames(pr); !strings.Contains(got, c.wantStage) {
				t.Fatalf("%s：必须由 %s 阶段短路；实际阶段链=%s", c.name, c.wantStage, got)
			}
			// 防空绿：确认请求不得伴随"已经执行过"的产物。
			if pr.Result != nil {
				t.Fatalf("%s：确认前不得到达执行端；Result=%+v", c.name, pr.Result)
			}
		})
	}
}

// TestGuard_ApprovalReuse_OnlyExplicitFlagSuppresses（负向守卫）：
// **不置** `ReusedApproval` 时行为必须与改动前逐字一致 —— 照问。
//
// 这条同时排除了「复用逻辑写成"凡 bash 都免问"」这种过度放宽。
func TestGuard_ApprovalReuse_OnlyExplicitFlagSuppresses(t *testing.T) {
	approvalReuseExeOutside(t)
	workDir := t.TempDir()
	e := approvalReusePipeline(t, workDir)

	// 三个样本**都必须是非只读命令**（否则会被只读免问出口 (e) 放行 ⇒ 本用例变成空测）。
	inputs := []struct {
		name  string
		tool  string
		input string
	}{
		{name: "bash-逐字相同", tool: "bash", input: approvalReuseCmd(workDir)},
		{name: "bash-其它命令", tool: "bash", input: `{"command":"mkdir -p ` + filepath.Join(workDir, "ar_other") + `"}`},
		{name: "bash-相似但不同", tool: "bash", input: `{"command":"mkdir -p ` + filepath.Join(workDir, "ar_probe2") + `"}`},
	}

	for _, c := range inputs {
		t.Run(c.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(context.Background(), Call{ID: "ar-neg-" + c.name, Name: c.tool, Input: c.input})
			if !pr.NeedConfirm {
				t.Fatalf("未置 ReusedApproval ⇒ 必须照常询问（不得被复用逻辑放宽）；阶段链=%s", stageNames(pr))
			}
		})
	}
}

// TestGuard_ApprovalReuse_DangerousStagesPrecedePermissionStage 钉住上面那条守卫所依赖的
// **结构前提**：危险/破坏性阶段必须排在 permission 阶段之前。
//
// 这是"复用不能免掉危险确认"的**唯一**保证来源。若有人调整阶段顺序（例如为了别的需求
// 把 PermissionStage 提前），上一条行为守卫可能仍绿（因为危险命令在别处被拦），
// 但复用出口已经能看见危险调用了 —— 本结构性守卫会先打红。
func TestGuard_ApprovalReuse_DangerousStagesPrecedePermissionStage(t *testing.T) {
	e := approvalReusePipeline(t, t.TempDir())

	idx := map[string]int{}
	for i, st := range e.Pipeline.stages {
		idx[st.Name()] = i
	}
	perm, ok := idx["permission"]
	if !ok {
		t.Fatal("前置不成立：阶段链里没有 permission 阶段 ⇒ 本守卫无判别力")
	}
	for _, dangerous := range []string{"dangerous_guard", "destructive_check"} {
		d, ok := idx[dangerous]
		if !ok {
			t.Fatalf("前置不成立：阶段链里没有 %s 阶段 ⇒ 本守卫无判别力", dangerous)
		}
		if d >= perm {
			t.Errorf("%s 必须排在 permission **之前**（复用出口位于 permission 内）——"+
				"否则危险面会随复用一起免问，突破 -R1；实际 %s=%d, permission=%d",
				dangerous, dangerous, d, perm)
		}
	}
}
