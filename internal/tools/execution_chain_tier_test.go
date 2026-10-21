package tools

// 止血：执行链档位对齐 —— High 级疑似项不得无声处死。
//
// 病灶（真机 T-3 实锤）：`SandboxPrecheckStage`（审批侧）只认 ruleengine 七规则，
// validator 的 20 项检查它**从未见过**；而执行链 `prepareSandbox` 的 Manager 路径
// 却按 validator 结果 Severity>=High 一刀切硬拒（bash.go 旧 :1142）。
// ⇒ 执行链在执行**审批层从未见过的拒绝**：用户连批准框都看不到，
// 命令一行不跑 —— 这正是 在 ruleengine 里修掉、却在 validator 这条腿上
// 还活着的病（"允许了还会被拒绝"的结构性成因之二）。
//
// 修复语义（对齐 分档哲学 + 业内对标：牢房先跑、硬拒只留逃逸载体）：
//	- Severity >= Critical（fork 炸弹、替换内层 rm、危险重定向、AST 逃逸载体）
// ⇒ 执行链保持硬拒（与 sanctioned 路径 bash.go:1107 既有口径一致）；
//	- High 级疑似项（LOCALE-Q/ANSIC-Q/BRACE-EXP 等文本混淆信号）
// ⇒ 不再在执行链无声处死，命令照常进 OS 沙箱执行 ——
// 文本疑似 ≠ 实际危险，牢房真管住了就不必看长相枪毙。
//
// 双向判别：误伤组（改前红）+ Critical 组（改前改后都绿，防放空）。

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// newW093Executor 构造带 Legacy Sandbox + Manager 双栈的 Executor（生产装配形态）。
func newW093Executor(t *testing.T) *Executor {
	t.Helper()
	workDir := t.TempDir()
	sb := sandbox.NewSandbox(workDir)
	sb.SetEnabled(true)
	mgr := sandbox.NewManager(nil, sandbox.SandboxLevelRelaxed)
	return &Executor{Sandbox: sb, SandboxMgr: mgr, workDir: workDir}
}

// TestW093_HighFindingNotSilentlyKilled 误伤组：High-only 发现不再在执行链硬拒。
func TestW093_HighFindingNotSilentlyKilled(t *testing.T) {
	e := newW093Executor(t)
	ctx := context.Background()

	// `echo $"hello"`：validator LOCALE-Q（High）命中，ruleengine 与 Critical 均不命中。
	// 改前：prepareSandbox 返回 "command blocked: [LOCALE-Q] ..."（一行不跑）⇒ 红。
	// 改后：照常准备沙箱、sandboxed=true。
	_, cleanup, sandboxed, err := e.prepareSandbox(ctx, `echo $"hello"`, e.workDir)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf(" 误伤: High 级疑似项不应在执行链无声处死（审批层从未询问过用户）\n  命令: echo $\"hello\"\n  错误: %v", err)
	}
	if !sandboxed {
		t.Fatal(": High-only 命令应在沙箱内执行（sandboxed=true），而不是绕过沙箱")
	}
}

// TestW093_T3OriginalCommandRuns T-3 真机误伤原文（管道 + 正则锚点）端到端放行。
func TestW093_T3OriginalCommandRuns(t *testing.T) {
	e := newW093Executor(t)
	ctx := context.Background()
	cmd := `git ls-files | grep -E "\.go$" | head -20`
	_, cleanup, _, err := e.prepareSandbox(ctx, cmd, e.workDir)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf(": T-3 误伤原文仍被拒: %v", err)
	}
}

// TestW093_CriticalStillBlocked 对抗组：Critical 逃逸载体必须仍被硬拒（防放空）。
func TestW093_CriticalStillBlocked(t *testing.T) {
	e := newW093Executor(t)
	ctx := context.Background()

	cases := []struct {
		name string
		cmd  string
	}{
		{"fork-bomb", ":(){ :|:& };:"},
		{"subst-inner-rm", `echo $(rm -rf /)`},
	}
	for _, tc := range cases {
		_, cleanup, _, err := e.prepareSandbox(ctx, tc.cmd, e.workDir)
		if cleanup != nil {
			cleanup()
		}
		if err == nil {
			t.Errorf(" 回退: %s（Critical）必须仍被执行链硬拒，不得因修误伤而放空（命令: %s）", tc.name, tc.cmd)
		}
	}
}
