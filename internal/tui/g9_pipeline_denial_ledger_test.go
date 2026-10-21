// g9_pipeline_denial_ledger_test.go — G9-④（S13）在 internal/tui 一侧的判别性验收：
// 「管线校验阶段自动拒绝 ⇒ **决策面**留痕」。
//
// 移交单：内部工作回执
// 决策项：集中拍板…md §2.7「G9」；现象/归族 → 新发现_分诊规则与登记表_2026-09-15.md §8 附录 B。
//
// 被修的是什么（真机 2026-09-16 02:03）：工作区越界写被拒后
// - `~/.whale/logs/whale.log` —— G9-②（G9-001）才补的**执行面**留痕；
// - `~/.whale/audit/tool-approval.jsonl` —— **零条**（修复前只记批准/用户决策）
//
// ⇒ 账本实际上是"批准账本"，不是"完整账本"：**拒绝在决策面上不可追溯**。
// 两轴语义不同，不是同一件事的两种写法：
// - 执行面（whale.log）：这条调用**没被执行**；
// - 决策面（tool-approval.jsonl）：这次调用的**决策是 denied**。
//
// 两处落点**必须同改**（只挂一处 ⇒ 另一条生产路径静默漏记）：
//
//	executeToolsDirect（免确认直跑） tool_exec.go 的 !pr.Proceed 分支
//	executePipelineRemaining（批准后恢复） tool_exec.go 的 !pr.Proceed 分支
//
// 判别性怎么证明（overlay **正交** A/B：单独回退两处 recordApprovalDecision）：
//
//	只回退 executeToolsDirect 那处 ⇒ TestG9_PipelineDenialAudited_DirectPath 打红，
//
// …AfterConfirm **仍绿**；
//
//	只回退 executePipelineRemaining ⇒ 反之。
//	两处都回退（before）⇒ 两条都打红。
//
// 反例守卫（before/after 两态**都必须绿**，证修复没有越界）：
//
//	TestG9_AllowedWriteProducesNoDecisionEntry：正常成功的写不产生任何决策条目
//
// —— 不得把"没问过"伪造成一条 denied；
//
//	…AfterConfirm 里同时断言那条 **approved ∧ user** 仍在
//
// —— 新代码不得把既有批准记录改写成拒绝（成因方不得被覆盖）。
package tui

import (
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// g9OutsideTarget 返回一个**确定在工作区之外**的写入目标。
//
// 与真机证据（`/tmp/whaletest/outside.txt` vs 工作区 `/tmp/whaletest/mcp/proj`）同形：
// 同一父目录下的兄弟目录 ⇒ `filepath.Rel` 得到 "../…" ⇒ 命中
// `checkWorkspaceBoundary`（tools/pipeline.go，G5 的单一判据）。
func g9OutsideTarget(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "outside.txt")
}

// g9BoundaryCallInput 构造一次越界写的工具入参（形态与真机会话记录一致）。
func g9BoundaryCallInput(target string) string {
	return `{"path":"` + target + `","content":"Hello from Whale Code!"}`
}

// g9EntriesBy 按「决策 × 成因方」**合取**过滤账本条目。
//
// 刻意按两字段合取：只看 decision 会把"用户拒绝"（source=user）与"管线拒绝"
// （source=pipeline）混为一谈 —— 那正是本单要修的断点（拒绝事实没有正确的成因方）。
func g9EntriesBy(entries []map[string]any, decision, source string) []map[string]any {
	var out []map[string]any
	for _, e := range entries {
		if e["decision"] == decision && e["decision_source"] == source {
			out = append(out, e)
		}
	}
	return out
}

// newG9PipelineModel 构造一个「真实管线已接线 + HOME 隔离 + 工作区在临时目录」的 Model。
//
// 三条都不可省：
// - HOME 必须在 NewModel **之前**隔离（lifecycle.go 在构造期用 homeDir 拼审计路径，
// 构造后再改 HOME 对已建好的 sink 无效 —— 那会让断言读到真实 `~/.whale`）；
// - t.Chdir 必须在 NewModel 之前（workDir 取自 os.Getwd()）；
// - guTrackModelTeardown 必须注册（NewModel 会起 ForgettingScanner 后台协程，
// 否则与 t.TempDir() 的 RemoveAll 竞态 —— 见 gu_testinfra_test.go）。
func newG9PipelineModel(t *testing.T, target string) (*Model, string) {
	t.Helper()
	home := guIsolatedHome(t)
	t.Chdir(t.TempDir())
	if target == "" {
		target = g9OutsideTarget(t)
	}
	m := newMultiToolModel(t, []session.ContentBlock{
		{Type: "tool_use", ID: "call_out", Name: "write_file", Input: g9BoundaryCallInput(target)},
	})
	guTrackModelTeardown(t, m)
	return m, home
}

// ---------------------------------------------------------------------------
// 落点 · executeToolsDirect（免确认直跑）—— 生产入口 executeTools
// ---------------------------------------------------------------------------

// TestG9_PipelineDenialAudited_DirectPath 主用例：越界写被管线拒绝后，账本必须恰好一条
// `decision=denied ∧ decision_source=pipeline`。
//
// 走生产入口 `executeTools`（app.go 的分发点），并按 bubbletea 事件循环求值 tea.Cmd
// —— 留痕写在 cmd 闭包内部，跳过这一跳就只测到半条路径。
func TestG9_PipelineDenialAudited_DirectPath(t *testing.T) {
	m, home := newG9PipelineModel(t, "")

	cmd := m.executeTools()
	if cmd == nil {
		t.Fatal("前置失败：executeTools 未返回 cmd")
	}
	msg := cmd() // bubbletea 事件循环做的事
	res, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("期望 toolResultMsg，实际 %T", msg)
	}
	if len(res.results) != 1 || !res.results[0].IsError {
		t.Fatalf("前置失败：越界写应产出 1 条错误结果，实际 %+v", res.results)
	}
	if got := res.results[0].FailureKind; got != tools.FailureKindSandboxDenied {
		t.Fatalf("前置失败：越界拒绝的 FailureKind 应为 sandbox_denied，实际 %q"+
			"（这是 G9-① 的判据面：本用例要求它已成立，才谈得上 G9-④）", got)
	}

	entries := deltaFAuditEntries(t, home)
	denied := g9EntriesBy(entries, "denied", "pipeline")
	if len(denied) != 1 {
		t.Fatalf("管线拒绝未在**决策面**留痕：账本 `denied ∧ pipeline` 应恰好 1 条，"+
			"实际 %d 条（全量 %d 条：%v）\n"+
			"—— 修复前这里恒为 0，账本只是「批准账本」而不是「完整账本」",
			len(denied), len(entries), entries)
	}
	e := denied[0]
	if got := e["origin"]; got != "tui-main" {
		t.Errorf("origin = %v，want tui-main（主会话内联确认出口）", got)
	}
	if got := e["tool"]; got != "write_file" {
		t.Errorf("tool = %v，want write_file", got)
	}
	// ★ 反向判据：绝不允许落成"用户拒绝"。用户**从未被询问**
	// （CustomValidateStage 排在 PermissionStage 之前，直接短路，连确认框都没弹过）。
	if userDenied := g9EntriesBy(entries, "denied", "user"); len(userDenied) != 0 {
		t.Fatalf("成因方被误记成 user —— 用户从未被询问，这是审计里的谎言"+
			"（confirm.go:42 硬纪律：只有 SourceUser 才是用户拒绝）：%v", userDenied)
	}
}

// TestG9_AllowedWriteProducesNoDecisionEntry 反例守卫（before/after 必须都绿）：
// 工作区内的普通写**免确认直接执行** ⇒ 没有任何决策发生 ⇒ 账本必须**零条**。
//
// 守住"留痕不得退化成'每次调用都记一条'"：若有人把 recordApprovalDecision 挪到
// 执行分支上，这条会立刻翻红。
func TestG9_AllowedWriteProducesNoDecisionEntry(t *testing.T) {
	home := guIsolatedHome(t)
	t.Chdir(t.TempDir())
	m := newMultiToolModel(t, []session.ContentBlock{
		{Type: "tool_use", ID: "call_in", Name: "write_file", Input: `{"path":"a.txt","content":"AAA"}`},
	})
	guTrackModelTeardown(t, m)

	msg := m.executeTools()()
	res, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("期望 toolResultMsg，实际 %T", msg)
	}
	if len(res.results) != 1 || res.results[0].IsError {
		t.Fatalf("前置失败：工作区内普通写应成功（免确认），实际 %+v", res.results)
	}
	if entries := deltaFAuditEntries(t, home); len(entries) != 0 {
		t.Fatalf("未被询问的调用不得留决策条目（会污染账本语义），实际 %d 条：%v", len(entries), entries)
	}
}

// ---------------------------------------------------------------------------
// 落点 · executePipelineRemaining（批准后恢复）—— 生产消息分发链
// ---------------------------------------------------------------------------

// TestG9_PipelineDenialAudited_AfterConfirm 第二个拒绝点：用户在确认框上批准了**前一个**工具后，
// 恢复执行时**后一个**工具被管线拒绝 ⇒ 同样必须留一条 `denied ∧ pipeline`，
// 且既有的 `approved ∧ user`（前一个工具的批准）**必须仍在**。
//
// 驱动方式：走真实消息分发（executeTools → pipelineConfirmMsg → Update → 按键 → confirmResultMsg
// → 生产分发 executePipelineRemaining），见 multitool_confirm_result_loss_test.go 的注释：
// 手工构造 PendingConfirm 再直调会让"字段透传漏掉"这类缺陷假绿。
//
// fixture 的两个驱动源相互**正交**：index 0 用受保护路径 `.git/config`（SafetyCheckStage
// 排在 PermissionStage 之前 ⇒ 恒需确认），index 1 用越界目标（CustomValidateStage ⇒ 直接拒绝）。
func TestG9_PipelineDenialAudited_AfterConfirm(t *testing.T) {
	home := guIsolatedHome(t)
	t.Chdir(t.TempDir())
	outside := g9OutsideTarget(t)

	m := newMultiToolModel(t, []session.ContentBlock{
		// index 0：受保护路径 ⇒ 暂停等确认
		{Type: "tool_use", ID: "call_confirm", Name: "write_file", Input: `{"path":".git/config","content":"AAA"}`},
		// index 1：越界 ⇒ 批准 index 0 后由 executePipelineRemaining 跑到它时被拒绝
		{Type: "tool_use", ID: "call_out", Name: "write_file", Input: g9BoundaryCallInput(outside)},
	})
	guTrackModelTeardown(t, m)

	out, pausedAt := driveConfirmsByProductionPath(t, m, m.executeTools()())
	res, ok := out.(toolResultMsg)
	if !ok {
		t.Fatalf("期望 toolResultMsg，实际 %T（pausedAt=%v）", out, pausedAt)
	}
	if len(pausedAt) != 1 || pausedAt[0] != 0 {
		t.Fatalf("前置失败：应在 index=0 暂停一次（受保护路径），实际 %v", pausedAt)
	}
	// 越界那条必须真的被拒（否则"拒绝后留痕"的前提不成立）
	var deniedResult *tools.Result
	for i := range res.results {
		if res.results[i].ID == "call_out" {
			deniedResult = &res.results[i]
		}
	}
	if deniedResult == nil || !deniedResult.IsError {
		t.Fatalf("前置失败：越界写应产出错误结果，实际 %+v", res.results)
	}

	entries := deltaFAuditEntries(t, home)
	denied := g9EntriesBy(entries, "denied", "pipeline")
	if len(denied) != 1 {
		t.Fatalf("【缺陷复现】批准后恢复路径上的管线拒绝未留痕："+
			"`denied ∧ pipeline` 应恰好 1 条，实际 %d 条（全量 %d 条：%v）\n"+
			"—— 只挂 executeToolsDirect 一处必然漏掉本路径", len(denied), len(entries), entries)
	}
	if got := denied[0]["tool"]; got != "write_file" {
		t.Errorf("拒绝条目 tool = %v，want write_file", got)
	}
	// 反向判据：既有批准记录必须原样保留、且成因方不得被新代码覆盖成 pipeline。
	approved := g9EntriesBy(entries, "approved", "user")
	if len(approved) != 1 {
		t.Fatalf("既有批准留痕被破坏：`approved ∧ user` 应恰好 1 条，实际 %d 条（全量 %v）",
			len(approved), entries)
	}
}
