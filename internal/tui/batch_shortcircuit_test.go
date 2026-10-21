package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// 本文件是 **（批内不短路）** 的永久判别器。
//
// 缺陷本体：一次模型响应里的**批**调用中，第 1 条被判**终局拒绝**后，
// 后续调用仍会被**照常询问、照常执行**（两处批循环在 `!pr.Proceed` 时只记一条拒绝就 continue）。
//
// 修法：**短路** —— 后续调用不再进入管线，只补"未执行"占位结果。
//
// 判别性（★ 必须配**副作用面**，不能只看结果文案）：
// - ① 后续调用的结果必须显式声明"未执行"（Not run）；
// - ② **副作用面**：后续调用声明的目标文件**必须不存在**。
//
// 反例注入（打红）：去掉两处 `break`（回到 continue）② 会立刻变红 ——
// 第 2 条被真的执行、文件被创建。
//
// ★ 与「请求内审批复用」的关系（登记册 §4）：复用只在**同请求内逐字相同**的调用间成立，
// 本测试的两条调用**输入不同**（不同 path），因此不会被复用逻辑盖住 ⇒ 不是假绿。

// TestN118_BatchShortCircuit_DirectPath 覆盖**免确认直跑**那条批循环
// （executeToolsDirect，即 `executeToolsDirect` 的 Pipeline 分支）。
func TestN118_BatchShortCircuit_DirectPath(t *testing.T) {
	guIsolatedHome(t)
	ws := t.TempDir()
	t.Chdir(ws)
	outside := g9OutsideTarget(t)

	m := newMultiToolModel(t, []session.ContentBlock{
		// index 0：越界写 ⇒ 管线直接拒绝（CustomValidateStage，无需确认）
		{Type: "tool_use", ID: "n118_deny", Name: "write_file", Input: g9BoundaryCallInput(outside)},
		// index 1：工作区内写 —— 修好后**不得**被执行
		{Type: "tool_use", ID: "n118_second", Name: "write_file", Input: `{"path":"n118_second.txt","content":"SECOND"}`},
	})
	guTrackModelTeardown(t, m)

	msg := m.executeTools()()
	res, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("期望 toolResultMsg，实际 %T", msg)
	}
	assertBatchAbortedAfterDenial(t, res, ws, "n118_deny", "n118_second", "n118_second.txt")
}

// TestN118_BatchShortCircuit_AfterConfirm 覆盖**确认后恢复**那条批循环
// （executePipelineRemaining）。
//
// 走真实消息分发链（executeTools → pipelineConfirmMsg → Update → Enter → confirmResultMsg
// → 生产分发 executePipelineRemaining），不手工构造 PendingConfirm（否则字段透传漏掉会假绿）。
func TestN118_BatchShortCircuit_AfterConfirm(t *testing.T) {
	guIsolatedHome(t)
	ws := t.TempDir()
	t.Chdir(ws)
	outside := g9OutsideTarget(t)

	m := newMultiToolModel(t, []session.ContentBlock{
		// index 0：受保护路径 ⇒ 暂停等确认（SafetyCheckStage 排在 PermissionStage 之前）
		{Type: "tool_use", ID: "n118_confirm", Name: "write_file", Input: `{"path":".git/config","content":"AAA"}`},
		// index 1：越界 ⇒ 批准 index 0 后恢复执行时被管线拒绝
		{Type: "tool_use", ID: "n118_deny", Name: "write_file", Input: g9BoundaryCallInput(outside)},
		// index 2：工作区内写 —— 修好后**不得**被执行
		{Type: "tool_use", ID: "n118_third", Name: "write_file", Input: `{"path":"n118_third.txt","content":"THIRD"}`},
	})
	guTrackModelTeardown(t, m)

	out, pausedAt := driveConfirmsByProductionPath(t, m, m.executeTools()())
	res, ok := out.(toolResultMsg)
	if !ok {
		t.Fatalf("期望 toolResultMsg，实际 %T（pausedAt=%v）", out, pausedAt)
	}
	// 前置：只在 index 0 暂停过一次（index 1 是**直接拒绝**，不该再弹；
	// index 2 在短路后**不该被询问** ⇒ 若 pausedAt 出现 1 或 2 就是缺陷复现）。
	if len(pausedAt) != 1 || pausedAt[0] != 0 {
		t.Fatalf("【缺陷复现】应仅在 index=0 暂停一次；实际 pausedAt=%v"+
			"（出现 1 或 2 说明拒绝后仍在继续询问后续调用 = ）", pausedAt)
	}
	assertBatchAbortedAfterDenial(t, res, ws, "n118_deny", "n118_third", "n118_third.txt")
}

// assertBatchAbortedAfterDenial 断言「前一条被拒 ⇒ 后续调用未被询问、未被执行」。
func assertBatchAbortedAfterDenial(t *testing.T, res toolResultMsg, ws, deniedID, victimID, victimFile string) {
	t.Helper()

	find := func(id string) *tools.Result {
		for i := range res.results {
			if res.results[i].ID == id {
				return &res.results[i]
			}
		}
		return nil
	}

	denied := find(deniedID)
	if denied == nil || !denied.IsError {
		t.Fatalf("前置失败：越界写应产出错误结果（结果集 %+v）", res.results)
	}
	victim := find(victimID)
	if victim == nil {
		t.Fatalf("后续调用缺结果条目（每个 tool_use 都必须有配对的 tool_result）；结果集 %+v", res.results)
	}
	if !victim.IsError || !strings.Contains(victim.Output, "Not run") {
		t.Fatalf("【缺陷复现】后续调用的结果未声明「未执行」：%q", victim.Output)
	}
	// ★ 副作用面（主判据）：文件必须不存在。
	if _, err := os.Stat(filepath.Join(ws, victimFile)); err == nil {
		t.Fatalf("【缺陷复现】前一条被拒后，后续调用仍被**执行**了（%s 被创建）—— 批内未短路",
			victimFile)
	}
}
