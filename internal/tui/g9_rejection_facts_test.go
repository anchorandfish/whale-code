// g9_rejection_facts_test.go — G9-①③（S13 新发现 /）在 internal/tui 一侧的判别性验收。
//
// 移交单：内部工作回执
// 决策项：集中拍板…md §2.7「G9」；现象/归族 → 新发现_分诊规则与登记表_2026-09-15.md §8 附录 B。
//
// 被修的是**同一个断点的两面**——「拒绝事实没有结构化出口」：
//
//	G9-① pipelineRejectionResult 的 default 分支不赋值 FailureKind ⇒ 除 policy_denied 外的
//
// 所有拒绝种类（当前 3 个：sandbox_denied / policy_denied / patch_conflict）
// 在 TUI 侧塌成零值 ⇒ R4 的权威判据 roundDeniedKind 对**越界写恒为空**
// （真机 2026-09-16 02:03：会话记录里 write_file 被拒绝，账本却零条）。
//
//	G9-③ turnObservation 只遍历 toolCalls 的 Input 提路径、**完全不看 results 的成败**
//
// ⇒ 被拒的写被计成"已改动"（真机 02:03:33 `agentguard: turn … "changed":1`，
// 而同一时刻被拒的目标文件并不存在）⇒ 无进展熔断 / 审查子循环上限拿到失真输入。
//
// 判别性怎么证明（overlay **正交** A/B，单独回退一项）：
//
//	只回退 ①（tool_exec.go）⇒ ① 的守卫精确打红，③ 的守卫**全绿**（不受影响）；
//	只回退 ③（agent_guard.go）⇒ ③ 的守卫精确打红，① 的守卫**全绿**。
//
// 反例守卫（两条，before/after 两态**都必须绿** —— 证明修复没有越界）：
//
//	TestG9_PolicyDeniedStaysOnItsOwnBranch：policy_denied 仍走专属文案分支，未被混同；
//	TestG9_SuccessfulWriteStillCounted：**成功**的写仍计 1，正常计数未被误伤。
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// g9BoundaryRejection 是工作区越界拒绝的**原文形态**（真机会话记录实读，见移交单"报文可判别性"行）。
const g9BoundaryRejection = `workspace boundary check: /tmp/whaletest/outside.txt is not within workspace "/tmp/whaletest/mcp/proj"`

// g9WriteCall 是一次越界写调用的完整入参（形态与真机会话记录一致）。
func g9WriteCall() session.ContentBlock {
	return session.ContentBlock{
		Type:  "tool_use",
		ID:    "w1",
		Name:  "write_file",
		Input: `{"path":"/tmp/whaletest/outside.txt","content":"Hello from Whale Code!"}`,
	}
}

// g9PipelineRejection 走**生产函数** pipelineRejectionResult 构造被拒结果 ——
// 不手工拼 tools.Result，否则测的是"我以为它长什么样"，而不是它实际长什么样。
func g9PipelineRejection(kind tools.FailureKind, rejection string) tools.Result {
	return pipelineRejectionResult(g9WriteCall(),
		&tools.PipelineResult{Proceed: false, Rejection: rejection, FailureKind: kind})
}

// ---------------------------------------------------------------------------
// G9-① · 原样透传 FailureKind
// ---------------------------------------------------------------------------

// TestG9_BoundaryRejectionReachesR4Ledger · ①主用例。
//
// 三段各锁一件事：结构化事实（字段透传）→ 生产漏斗入账（app.go:2016 的唯一写入点）
// → 用户可见现象（R4 合取成立 ⇒ ⛔ 分支）。
func TestG9_BoundaryRejectionReachesR4Ledger(t *testing.T) {
	res := g9PipelineRejection(tools.FailureKindSandboxDenied, g9BoundaryRejection)

	if res.FailureKind != tools.FailureKindSandboxDenied {
		t.Fatalf("越界拒绝丢失 FailureKind：%q —— default 分支必须**原样透传** result.FailureKind", res.FailureKind)
	}
	if !res.IsError {
		t.Fatalf("被拒结果必须 IsError=true，实际 %+v", res)
	}

	// 生产漏斗：toolResultMsg → noteRoundSecurityDenials（R4 唯一写入点）。
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{res}})
	mm := updated.(Model)
	if mm.roundDeniedKind != tools.FailureKindSandboxDenied {
		t.Fatalf("R4 权威判据对越界写恒为空：roundDeniedKind=%q（结构化拒绝没到入账点）", mm.roundDeniedKind)
	}

	// 用户可见现象：账本非空 ∧ 散文在谈安全约束 ⇒ 必须走 ⛔ 分支。
	mm.checkAISafety(r4SecurityProse, false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))
	if !strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("越界拒绝入账后应走 ⛔ 分支，实际 transcript: %q", transcript)
	}
}

// TestG9_UnenumeratedKindAlsoCarried · ①的"不得枚举"分水岭。
//
// patch_conflict 既不是 sandbox_denied 也不是 policy_denied ⇒ 枚举式补法
// （`else if kind == SandboxDenied`）必然漏掉它。透传是唯一的免漂移写法：
// 将来新增种类自动不再漏（本仓"两份清单漂移"的经典形态，见 tools.AllFailureKinds）。
func TestG9_UnenumeratedKindAlsoCarried(t *testing.T) {
	res := g9PipelineRejection(tools.FailureKindPatchConflict, "patch does not apply (hunk context mismatch)")
	if res.FailureKind != tools.FailureKindPatchConflict {
		t.Fatalf("未列入两个安全拒绝的种类也必须原样透传，实际 %q（枚举式补法会继续漏）", res.FailureKind)
	}

	// 未越界：透传 ≠ "什么都当安全拒绝"。R4 账本仍只认两类（noteRoundSecurityDenials 未改）。
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{res}})
	mm := updated.(Model)
	if mm.roundDeniedKind != "" {
		t.Fatalf("patch_conflict 是**可恢复的普通失败**，不得入 R4 账本，实际 %q", mm.roundDeniedKind)
	}
}

// TestG9_PolicyDeniedStaysOnItsOwnBranch · ①反例守卫（before/after 必须都绿）。
//
// 证两类**未被混同**：policy_denied 仍命中专属分支（专属文案 + 自身入账），
// 透传不得把它并进 default 的通用文案。
func TestG9_PolicyDeniedStaysOnItsOwnBranch(t *testing.T) {
	res := g9PipelineRejection(tools.FailureKindPolicyDenied, "permission rule denied: bash rm -rf")
	if res.FailureKind != tools.FailureKindPolicyDenied {
		t.Fatalf("policy_denied 应保持自身取值，实际 %q", res.FailureKind)
	}
	// ★ ：专属分支标记随双语文案改为英文主行
	// （形态 SSOT = util.Bi；改前是行内并列 "Permission denied / 权限拒绝"）。
	if !strings.Contains(res.Output, "Permission denied\n") {
		t.Fatalf("policy_denied 必须仍走专属文案分支，实际 %q", res.Output)
	}
	if strings.Contains(res.Output, "⚠️ Pipeline rejected") {
		t.Fatalf("policy_denied 不得落到 default 分支，实际 %q", res.Output)
	}

	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{res}})
	mm := updated.(Model)
	if mm.roundDeniedKind != tools.FailureKindPolicyDenied {
		t.Fatalf("policy_denied 应入账为 policy_denied，实际 %q", mm.roundDeniedKind)
	}
}

// ---------------------------------------------------------------------------
// G9-③ · 按 Result.ID 配对，只计成功
// ---------------------------------------------------------------------------

// TestG9_DeniedWriteNotCountedAsProgress · ③主用例。
//
// 三个形态都不得计入：被拒的写（有配对但 IsError）、结果**缺失**的写（无配对）、被拒的读（对称）。
func TestG9_DeniedWriteNotCountedAsProgress(t *testing.T) {
	calls := []session.ContentBlock{g9WriteCall()}
	denied := g9PipelineRejection(tools.FailureKindSandboxDenied, g9BoundaryRejection)

	if obs := turnObservation(calls, []tools.Result{denied}, "拒绝原因已说明"); len(obs.FilesChanged) != 0 {
		t.Fatalf("被拒的写不得计为进展（否则空转熔断失效），实际 FilesChanged=%v", obs.FilesChanged)
	}
	if obs := turnObservation(calls, nil, ""); len(obs.FilesChanged) != 0 {
		t.Fatalf("无配对结果不得计为进展（保守方向：宁可少计），实际 FilesChanged=%v", obs.FilesChanged)
	}

	readCall := session.ContentBlock{Type: "tool_use", ID: "r1", Name: "read_file", Input: `{"path":"/etc/hosts"}`}
	deniedRead := tools.Result{ID: "r1", Name: "read_file", IsError: true, Output: "sandbox denied / 沙箱拒绝"}
	if obs := turnObservation([]session.ContentBlock{readCall}, []tools.Result{deniedRead}, ""); len(obs.FilesRead) != 0 {
		t.Fatalf("被拒的读没读到文件，不得计为已读，实际 FilesRead=%v", obs.FilesRead)
	}
}

// TestG9_SuccessfulWriteStillCounted · ③反例守卫（before/after 必须都绿）。
//
// 证未误伤正常计数：存在配对结果且 !IsError ⇒ 写计 1、读计 1。
func TestG9_SuccessfulWriteStillCounted(t *testing.T) {
	writeCall := session.ContentBlock{Type: "tool_use", ID: "w2", Name: "write_file", Input: `{"path":"a.go"}`}
	readCall := session.ContentBlock{Type: "tool_use", ID: "r2", Name: "read_file", Input: `{"file_path":"b.go"}`}
	results := []tools.Result{
		{ID: "w2", Name: "write_file", Output: "written"},
		{ID: "r2", Name: "read_file", Output: "ok"},
	}
	obs := turnObservation([]session.ContentBlock{writeCall, readCall}, results, "")
	if len(obs.FilesChanged) != 1 || obs.FilesChanged[0] != "a.go" {
		t.Fatalf("成功的写仍须计 1，实际 FilesChanged=%v", obs.FilesChanged)
	}
	if len(obs.FilesRead) != 1 || obs.FilesRead[0] != "b.go" {
		t.Fatalf("成功的读仍须计 1，实际 FilesRead=%v", obs.FilesRead)
	}
}

// TestG9_GuardReportOmitsDeniedWrite · ③端到端：**生产入口** handleToolResults
// → guardAfterToolRound → turnObservation → StopReport.Completed。
//
// 为什么这里用 patch_conflict 而不是 sandbox_denied：`sandbox_denied` / `policy_denied`
// 是**终局**拒绝，handleToolResults 在 `terminalFailureKind != ""` 时会在
// guardAfterToolRound **之前** return（tool_exec.go）⇒ 防护器这一轮根本不跑，
// 那样断言"快照里没有它"会两态都绿 —— 正是 R4 说的**不可判别判据**。
// patch_conflict 是**可恢复的普通失败**（tools.go:84）⇒ 走完正常路径，
// 防护器**真的**被喂了这份观测，断言才有判别力。
func TestG9_GuardReportOmitsDeniedWrite(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 1
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()

	call := g9WriteCall()
	m.toolService.SetPendingTools([]session.ContentBlock{call})
	m.sessionService.SetState(StateToolExec)

	res := pipelineRejectionResult(call, &tools.PipelineResult{
		Proceed:     false,
		Rejection:   g9BoundaryRejection,
		FailureKind: tools.FailureKindPatchConflict,
	})
	if res.FailureKind.Terminal() {
		t.Fatalf("前置失败：非终局拒绝才走得到防护器，FailureKind=%q 是终局的 ⇒ 本用例失去判别力", res.FailureKind)
	}

	_ = m.handleToolResults([]tools.Result{res})

	d := m.guard.BeforeModelCall()
	if d.Report == nil {
		t.Fatalf("MaxTurns=1 应在下一轮停机并给出快照，实际 %+v", d)
	}
	for _, f := range d.Report.Completed {
		if strings.HasSuffix(f, "outside.txt") {
			t.Fatalf("被拒的写不得进快照「已完成」，实际 Completed=%v", d.Report.Completed)
		}
	}
}
