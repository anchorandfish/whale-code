package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// E2E 判别器（先红）：只读命令免问出口在生产入口上的行为。
//
// 生产入口 = `InitPipelineWithHooks` → `ExecuteWithConfirm`
// （与 internal/tui/tool_exec.go executeToolsDirect 走的是同一条链）。
//
// 现场动机（用户 2026-10-06 报）：ww 档下 gate ① 对**每一条** bash 调用都提确认
// —— 按「**工具**是否只读」定级（bash 能写 ⇒ 条条问），与**具体命令**无关。
// 实测：`ls -la` / `seq 1 9` / `sleep 70 && echo final_check` 在该档全部 needConfirm=true。
// 而分类器 `AnalyzeBashCommand` 早已把这些命令判成 `read-only` —— 这份「只读」没有
// 接到 bash 的免问路径上（只喂风险分级 / Monitor）。
//
// 因此本轮新增出口 (e)：**命令的每一段都在「任意参数下都只读」白名单内 ⇒ 免问**。
// 判据 `cmdrules.AllSegmentsReadOnly`（全段口径，不是 MatchReadOnly 的「任一段」）。
//
// 出口 (e) 与 (a)/(b)/(c)/(d) **并列**落在 `PermissionStage` 内部、**排在自指保护 (0) 之后**，
// 因此它自动继承 (0)：本文件最后几条守卫钉住这条顺序不变量与「豁免不得越过拒绝面」。

func w023Executor(t *testing.T, workDir string) (*Executor, *permission.Policy) {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(pol, workDir)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil")
	}
	return e, pol
}

// ① 只读**单一**命令免问，且**真的执行**（免问 ≠ 免执行）。
func TestW023_ReadOnlySingleCommandExempted(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	const marker = "w023_marker_file"
	if err := os.WriteFile(filepath.Join(workDir, marker), []byte("x"), 0o644); err != nil {
		t.Fatalf("precondition: 造标记文件失败: %v", err)
	}

	pr := e.ExecuteWithConfirm(ctx, Call{ID: "w023-1", Name: "bash", Input: bashInput("ls " + workDir)})
	if pr.NeedConfirm {
		t.Fatalf("W-023①：只读单一命令 `ls` 应免确认; rejection=%q stageLogs=%+v", pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed || pr.Result == nil {
		t.Fatalf("W-023①：免问 ≠ 免执行，命令必须真的跑; proceed=%v result=%+v", pr.Proceed, pr.Result)
	}
	// 真的执行了才有输出：输出里应能看到那个标记文件
	if !strings.Contains(fmt.Sprintf("%v", pr.Result), marker) {
		t.Fatalf("W-023①：只读命令应真正执行并产出 `ls` 输出（应含 %q）; result=%v", marker, pr.Result)
	}
}

// ② 用户原报形态：只读**复合**命令免问（`sleep 70 && echo final_check` 的结构）。
func TestW023_ReadOnlyCompoundExempted_UserCase(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	pr := e.ExecuteWithConfirm(ctx, Call{
		ID: "w023-user", Name: "bash",
		Input: bashInput("sleep 0 && echo final_check"),
	})
	if pr.NeedConfirm {
		t.Fatalf("W-023②：全段只读的复合命令应免确认（这正是用户报的形态）; rejection=%q stageLogs=%+v",
			pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed || pr.Result == nil {
		t.Fatalf("W-023②：免问 ≠ 免执行; proceed=%v result=%+v", pr.Proceed, pr.Result)
	}
}

// ③ 控制组：**非全段只读**的命令仍必须问（证明 (e) 不是「bash 一律免问」）。
// 断言用「不得静默放行」的形态 ⇒ 无论它落到"问"还是"拒"都算通过，
// 唯有 `Proceed && !NeedConfirm`（静默执行）算失败。
func TestGuard_W023_NonReadOnlyStillConfirms(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	cases := []struct{ name, cmd, why string }{
		{"nonreadonly-head", "mkdir -p " + filepath.Join(workDir, "d1") + " && echo hi", "首段非白名单"},
		{"nonreadonly-tail", "echo hi && mkdir -p " + filepath.Join(workDir, "d2"), "末段非白名单"},
		{"nonreadonly-single", "mkdir -p " + filepath.Join(workDir, "d3"), "单一非白名单"},
		{"redirect", "ls > " + filepath.Join(workDir, "o.txt"), "重定向 = 写入面"},
		{"pipe-to-shell", "echo 'safe text' | sh", "管道喂 shell（sh 非白名单）"},
		{"git-branch-delete", "git branch -D w023_nonexistent", "P-016：只读动词+破坏性开关，git 不在白名单"},
		{"interpreter-c", "sleep 0 && python3 -c 'pass'", "解释器不在白名单"},
		{"sudo", "sudo ls " + workDir, "wrapper 不剥离 ⇒ 头词 sudo 非白名单"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "w023-" + tc.name, Name: "bash", Input: bashInput(tc.cmd)})
			if pr.Proceed && !pr.NeedConfirm {
				t.Fatalf("W-023③：%q 不得被只读免问吞掉（%s）; stageLogs=%+v", tc.cmd, tc.why, pr.StageLogs)
			}
		})
	}
}

// ④ 顺序不变量：出口 (e) 必须排在自指保护 (0) **之后**。
//
// 同一只读命令：普通工作区免问；whale 自身源码树内**仍须确认**。
// 若把 (e) 挪到 (0) 之前（或让 checkModeLocked 直接返回 needConfirm=false），
// 本用例立刻变红 —— 那意味着「批准/免问只读」会顺带免掉「动 whale 自身源码」的确认。
func TestGuard_W023_ReadOnlyExemptionSitsAfterSelfProtection(t *testing.T) {
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

	// (i) 普通工作区（非自身树）：只读命令免问 —— 否则本用例不构成判别
	ePlain, _ := w023Executor(t, t.TempDir())
	prPlain := ePlain.ExecuteWithConfirm(ctx, Call{ID: "w023-plain", Name: "bash", Input: bashInput("ls -la")})
	if prPlain.NeedConfirm {
		t.Fatalf("W-023④ 对照组：普通工作区里只读命令应免确认（否则本用例不构成判别）; stageLogs=%+v",
			prPlain.StageLogs)
	}

	// (ii) whale 自身源码树内：同一命令**不再确认**。
	//
	// ★ 本断言已于 2026-10-08 **有意翻转**（，用户拍板「乙」）——**下一个人别当回归**：
	// 翻转前钉的是「自身树内 `ls -la` 仍须确认」（把 (e) 排在自指保护 (0) 之后的从严面）。
	// 翻转原因：I-13.11 真机取证证明该从严面只产生噪声 —— 全段只读命令**改不到树**
	// （N-040①：无越界），(0) 对它没有任何决策信息可提供，反而把开发件下的每条
	// bash 都变成弹窗。收窄后的 (0) 只拦「**实际可达**」的调用（能写且没人兜底）。
	// 新顺序 = (0) 内部先做「实际可达」判定 ⇒ 只读让路给 (e)。
	// 判别器：`selfsource_actual_reach_test.go`（含反例：能写的命令在树内仍必须问）。
	eSelf, _ := w023Executor(t, repoRoot)
	prSelf := eSelf.ExecuteWithConfirm(ctx, Call{ID: "w023-self", Name: "bash", Input: bashInput("ls -la")})
	if prSelf.NeedConfirm {
		t.Fatalf("W-023④：全段只读命令改不到自身树，自指闸应让路（ 收窄后 (e) 可达）; proceed=%v stageLogs=%+v",
			prSelf.Proceed, prSelf.StageLogs)
	}
}

// ⑤ 豁免不得越过**拒绝面**：显式 deny 规则（走生产入口 AddRule）必须绝对优先。
// 与 的同类守卫同款（`TestGuard_N040_DenyRuleBeatsSandboxExemption`）：
// 免问出口只改「问不问」，从不改「能不能做」。
func TestGuard_W023_DenyRuleBeatsReadOnlyExemption(t *testing.T) {
	workDir := t.TempDir()
	e, pol := w023Executor(t, workDir)
	if err := pol.AddRule(permission.PermissionRule{
		ToolName: "bash",
		Behavior: permission.RuleDeny,
		Source:   permission.SourceSession,
	}); err != nil {
		t.Fatalf("构造 deny 规则失败: %v", err)
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "w023-deny", Name: "bash", Input: bashInput("ls -la"),
	})
	if pr.Proceed {
		t.Fatalf("W-023⑤：deny 规则必须绝对优先于只读免问，实际 Proceed=true logs=%+v", pr.StageLogs)
	}
	if pr.NeedConfirm {
		t.Fatalf("W-023⑤：deny 是拒绝而非询问，实际 NeedConfirm=true logs=%+v", pr.StageLogs)
	}
}

// ⑥ 只读免问**不是**「凡是输入里有 `command` 字段就免问」：非命令载体工具不受影响。
//
// `extractCommand` 只按 JSON key 取 `command`（`permission.go:452`）——若出口 (e) 不加
// `IsCommandBearingTool` 门，就会把这类工具的确认也吞掉。
//
// 样本用 `TaskCreate`：它不是命令载体，但**会**走到 `PermissionStage` 且需要确认
// （与 的 `TestGuard_N040_NonProcessToolsUnaffected` 同源样本）。
// ⚠️ 不能用"工作区外的 `write_file`"做样本 —— 那会在更早的边界阶段被拦下，
// 根本走不到 `PermissionStage`，于是注入反例时**不会变红**（假判别，实测踩过）。
func TestGuard_W023_NonCommandToolsUnaffected(t *testing.T) {
	workDir := t.TempDir()
	e, _ := w023Executor(t, workDir)

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID: "w023-noncmd", Name: "TaskCreate",
		Input: `{"command":"ls -la"}`, // 字段名恰好叫 command，但工具不是命令载体
	})
	if !pr.NeedConfirm {
		t.Fatalf("W-023⑥：非命令载体工具的确认不得被只读免问吞掉; proceed=%v stageLogs=%+v",
			pr.Proceed, pr.StageLogs)
	}
}
