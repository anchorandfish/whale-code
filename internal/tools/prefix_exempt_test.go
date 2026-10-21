package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// E2E 判别器（先红）：「允许该命令前缀」在生产入口上的行为。
//
// 生产入口 = `InitPipelineWithHooks` → `ExecuteWithConfirm`
// （即 internal/tui/tool_exec.go executeToolsDirect 走的同一条链）。
// 免问出口 (d) 落在 `PermissionStage` **内部**的免问出口列表里（与 (a)/(b)/(c) 同处），
// 因此它**自动继承**出口 (0)（自指保护）——本文件的最后一个守卫钉住这条顺序不变量。
//
// 四条不可退让的语义见 internal/permission/prefix_memory_test.go 头部。

func w022Executor(t *testing.T, workDir string) (*Executor, *permission.Policy) {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(pol, workDir)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}
	return e, pol
}

// ① 批准前缀后，同头**单一简单命令**免确认，且**真的执行**（免问 ≠ 免执行）。
func TestW022_PrefixApprovalExemptsSameHeadSingleCommand(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, pol := w022Executor(t, workDir)

	a := filepath.Join(workDir, "w022_a")
	// 前置：未批准前缀时需确认
	if pr := e.ExecuteWithConfirm(ctx, Call{ID: "w022-pre", Name: "bash", Input: bashInput("mkdir -p " + a)}); !pr.NeedConfirm {
		t.Fatalf("precondition: 未批准前缀时 `mkdir -p` 应需确认; stageLogs=%+v", pr.StageLogs)
	}

	// 用户在面板上选择「允许该命令前缀」（对另一条 mkdir 命令）
	pol.RememberPrefix("bash", bashInput("mkdir -p "+filepath.Join(workDir, "w022_b")))

	pr := e.ExecuteWithConfirm(ctx, Call{ID: "w022-1", Name: "bash", Input: bashInput("mkdir -p " + a)})
	if pr.NeedConfirm {
		t.Fatalf("W-022①：批准 mkdir 前缀后，同头单一简单命令应免确认; rejection=%q stageLogs=%+v",
			pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed || pr.Result == nil {
		t.Fatalf("W-022①：免问 ≠ 免执行，命令必须真的跑; proceed=%v result=%+v", pr.Proceed, pr.Result)
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("W-022①：免问后应真正落盘（目录应存在）: %v", err)
	}
}

// ② 复合命令不得被前缀豁免（结构性：复合串根本进不了前缀范围）。
func TestGuard_W022_CompoundNotExemptedByPrefix(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, pol := w022Executor(t, workDir)
	pol.RememberPrefix("bash", bashInput("mkdir -p "+filepath.Join(workDir, "b")))

	cases := []struct{ name, cmd string }{
		{"and-chain", "mkdir -p " + filepath.Join(workDir, "a") + " && echo done"},
		{"semicolon", "mkdir -p " + filepath.Join(workDir, "a") + " ; echo done"},
		{"pipeline", "mkdir -p " + filepath.Join(workDir, "a") + " | cat"},
		{"redirect", "mkdir -p " + filepath.Join(workDir, "a") + " > /dev/null"},
		{"substitution", "echo $(mkdir -p " + filepath.Join(workDir, "a") + ")"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "w022-" + tc.name, Name: "bash", Input: bashInput(tc.cmd)})
			if pr.Proceed && !pr.NeedConfirm {
				t.Fatalf("W-022②：复合/含替换/含重定向的命令 %q 不得被前缀豁免而静默放行", tc.cmd)
			}
		})
	}
}

// ③ 危险 / 破坏性命令永不被前缀豁免（要么仍问，要么硬拒；绝不静默放行）。
func TestGuard_W022_DangerousNotExemptedByPrefix(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, pol := w022Executor(t, workDir)

	// 尝试用危险/破坏性命令建立前缀 —— 必须是 no-op
	pol.RememberPrefix("bash", bashInput("rm -rf "+filepath.Join(workDir, "x")))
	pol.RememberPrefix("bash", bashInput("rm "+filepath.Join(workDir, "y")))

	cases := []struct{ name, cmd string }{
		{"rm-rf", "rm -rf " + filepath.Join(workDir, "z")},
		{"rm-rel", "rm " + filepath.Join(workDir, "z")},
		{"fork-bomb", ":(){ :|:& };:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "w022-" + tc.name, Name: "bash", Input: bashInput(tc.cmd)})
			if pr.Proceed && !pr.NeedConfirm {
				t.Fatalf("W-022③：危险/破坏性命令 %q 不得被前缀豁免而静默放行; stageLogs=%+v",
					tc.cmd, pr.StageLogs)
			}
		})
	}
}

// ④ 前缀免问与自指保护 (0) 的**顺序不变量**：同一命令同一前缀，在普通工作区免问、
// 在 whale 自身源码树内仍必须确认 —— 证明出口 (d) 排在 (0) 之后（不放宽自指面）。
//
// 这一条是 的**关键守卫**：若把前缀免问挪到出口 (0) 之前（或在
// checkModeLocked 里直接返回 needConfirm=false），本用例立刻变红。
//
// ⚠️ 样本用 `mkdir`（**非只读**）而不是原先的 `seq`：★ 新增的
// 只读免问出口 (e) 会把 `seq 1 3` 直接放行 ⇒ 对照组会「因为别的原因」通过，
// 本用例对前缀机制就**失去判别力**（改用例前实测：把 (d) 挪到 (0) 前，本用例不再变红）。
// 凡 的样本，都必须选**非只读且非危险**的命令，才能只由前缀记忆决定问不问。
func TestGuard_W022_PrefixExemptionSitsAfterSelfProtection(t *testing.T) {
	t.Setenv("WHALE_ALLOW_SELF_MODIFY", "") // 关掉用户级自指豁免开关，保证判定确定性
	ctx := context.Background()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("precondition: 求仓库根失败: %v", err)
	}
	if !permission.IsSelfSourceTree(repoRoot) {
		t.Fatalf("precondition: %q 应被判定为 whale 自身源码树（go.mod module=%s）",
			repoRoot, permission.SelfModulePath)
	}

	// (i) 普通工作区（非自身树）：批准前缀后免问
	plain := t.TempDir()
	ePlain, polPlain := w022Executor(t, plain)
	polPlain.RememberPrefix("bash", bashInput("mkdir -p "+filepath.Join(plain, "p1")))
	prPlain := ePlain.ExecuteWithConfirm(ctx, Call{ID: "w022-plain", Name: "bash", Input: bashInput("mkdir -p " + filepath.Join(plain, "p2"))})
	if prPlain.NeedConfirm {
		t.Fatalf("W-022④ 对照组：普通工作区里批准前缀后应免确认（否则本用例不构成判别）; stageLogs=%+v", prPlain.StageLogs)
	}

	// (ii) whale 自身源码树内：同一命令、同一前缀，仍必须确认（自指保护优先）
	eSelf, polSelf := w022Executor(t, repoRoot)
	polSelf.RememberPrefix("bash", bashInput("mkdir -p "+filepath.Join(repoRoot, "p1")))
	prSelf := eSelf.ExecuteWithConfirm(ctx, Call{ID: "w022-self", Name: "bash", Input: bashInput("mkdir -p " + filepath.Join(repoRoot, "p2"))})
	if !prSelf.NeedConfirm {
		t.Fatalf("W-022④：前缀免问必须排在自指保护(0)之后 —— 自身树内同前缀命令仍须确认; proceed=%v stageLogs=%+v",
			prSelf.Proceed, prSelf.StageLogs)
	}
}

// ④b 会话级：换一个新 Policy（新会话）后前缀不生效（不落盘、不跨重启）。
//
// ⚠️ 同 ④：样本必须是**非只读**命令 —— 用 `seq` 会被 的只读免问 (e) 放行，
// 于是"未批准 ⇒ 仍问"这个断言会因为**别的原因**失败，而不是因为前缀是会话级的。
func TestGuard_W022_PrefixIsSessionScoped(t *testing.T) {
	workDir := t.TempDir()
	e, _ := w022Executor(t, workDir)

	// 未批准 ⇒ 仍问
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "w022-fresh", Name: "bash",
		Input: bashInput("mkdir -p " + filepath.Join(workDir, "fresh")),
	})
	if !pr.NeedConfirm {
		t.Fatalf("W-022④b：未批准前缀时应仍需确认; stageLogs=%+v", pr.StageLogs)
	}
}
