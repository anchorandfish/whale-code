package tools

// G9-②（= 分诊表）· **管线拒绝必须留下「执行面」结构化留痕**
// G9-④ · `SourcePipeline` 常量与 Explain 文案守卫
//
// 移交单：内部工作回执
// 决策项：G9-② / G9-④（集中拍板_决策表与并行修复分组_2026-09-14.md §2.7 的 G9 行）
//
// ---------------------------------------------------------------------------
// 缺陷面（修改前）
//
// `[audit] tool: execute done`（executor.go:657）写在 `executeDirect` **内部**
// ⇒ **只有真的执行了才会走到**；而管线在任一阶段拒绝时是**短路返回**、**零留痕**。
// 真机佐证（2026-09-16 02:03）：`~/.whale/logs/whale.log` 中被拒的 write_file **零行**。
//
// ⇒ 落点 = 管线拒绝点（`ToolPipeline.Execute`），**不是** `executor.go`。
// 包边界不变（同在 internal/tools）。
//
// ---------------------------------------------------------------------------
// 判别性（A/B）
//
// 用 `-overlay` 把 pipeline.go 反向重建为「三个 logPipelineRejection 调用点全删」的副本
// （**只回退本次改的那几行**，其余逐字节保持当前）：
//
//	go test ./internal/tools/ -run 'TestGT_G9_2_' # after：全绿
//	go test -overlay=/tmp/g9/before_overlay.json ./internal/tools/ -run 'TestGT_G9_2_' # before：红
//
// ⚠️ 本文件的判别器**读的是被测程序写出的日志文件**，不是自己 `os.Open` 源码
// ⇒ `-overlay` 对它**物理有效**（与 R4-8 的"读盘型判别器"不同）。
//
// ---------------------------------------------------------------------------
// ★ 票面判据②（与调用方无关）
//
// 判别器**直接调 `ToolPipeline.Execute`**（管线层），不经 TUI / bridge / plain / 子 agent。
// 若某实现在 TUI 侧补 log，本文件**不会变绿** —— 那正是"挂错层"的判据。
//
// ---------------------------------------------------------------------------
// ⚠️ 观测面刻意选**磁盘日志文件**（生产真实出口），与 C9 同形（gt_c9_tool_exec_log_test.go）：
// 断言"调用了一个 Info 函数"属于接线断言假绿 —— 要证明的是"**默认级别（Info）下它真的落进日志**"。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	whalelog "github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// g9LogRec 一条 JSON 日志记录（只声明本文件用到的字段）。
type g9LogRec struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Phase  string `json:"phase"`
	Tool   string `json:"tool"`
	Stage  string `json:"stage"`
	Kind   string `json:"failure_kind"`
	Reason string `json:"reason"`
}

// g9WithAuditLog 起一套**生产形态**的文件日志 sink。
//
// 生产默认级别 = Info（internal/log/log.go:19 `level = slog.LevelInfo`）。
// t.Cleanup 是 LIFO ⇒ 本收尾注册在 t.Setenv/t.TempDir **之后**，
// 保证「先关 sink → 再删目录」（否则 lumberjack 往已删目录里写，产生非确定性红）。
func g9WithAuditLog(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", testutil.IsolatedHomeDir(t)) // 隔离 ~/.whale，防污染真实 HOME
	whalelog.SetLevel(slog.LevelInfo)             // 显式写死，防被同包其它用例改过

	logPath := filepath.Join(t.TempDir(), "whale.log")
	if err := whalelog.InitFileLogger(logPath, 10, 2, 1); err != nil {
		t.Fatalf("InitFileLogger: %v", err)
	}
	t.Cleanup(func() {
		whalelog.CloseFileLogger()
		whalelog.SetLevel(slog.LevelInfo) // 重建 handler，别让后续用例写进已关闭的 sink
	})
	return logPath
}

// g9ReadLog flush 并解析磁盘上的日志文件（**生产真实出口**）。
func g9ReadLog(t *testing.T, logPath string) []g9LogRec {
	t.Helper()
	whalelog.CloseFileLogger() // flush：关闭 sink 后再读文件

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", logPath, err)
	}
	var out []g9LogRec
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec g9LogRec
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("该行不是 JSON 日志（%v）：%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// g9FilterMsg 按 msg 子串筛选。
func g9FilterMsg(recs []g9LogRec, substr string) []g9LogRec {
	var out []g9LogRec
	for _, r := range recs {
		if strings.Contains(r.Msg, substr) {
			out = append(out, r)
		}
	}
	return out
}

func g9Dump(recs []g9LogRec) string {
	var b strings.Builder
	for i, r := range recs {
		fmt.Fprintf(&b, "  [%d] level=%s msg=%q tool=%q stage=%q failure_kind=%q reason=%q\n",
			i, r.Level, r.Msg, r.Tool, r.Stage, r.Kind, r.Reason)
	}
	if b.Len() == 0 {
		return "  （日志文件为空）"
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// G9-② 判据①② · 负→正跃迁 ＋ 与调用方无关
// ---------------------------------------------------------------------------

// TestGT_G9_2_PipelineRejectionIsLogged 票面验收判据①②。
//
// 用**工作区越界写**（真机复现的那条：CustomValidateStage 前置拒绝、FailureKind=SandboxDenied）
// 驱动判别器。★ 直接调 `ToolPipeline.Execute` ⇒ 与调用方（TUI/bridge/plain/子 agent）无关。
func TestGT_G9_2_PipelineRejectionIsLogged(t *testing.T) {
	logPath := g9WithAuditLog(t)

	ws := t.TempDir() // 工作区
	r := NewRegistry()
	e := NewExecutor(r, ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)

	// 工作区之外的兄弟目录（仍位于临时目录内，便于清理）
	outside := filepath.Join(filepath.Dir(ws), "g9_2_outside.txt")
	defer os.Remove(outside)

	pr := p.Execute(context.Background(), Call{
		ID:       "gt-g9-2-workspace-boundary",
		Name:     "write_file",
		Input:    fmt.Sprintf(`{"path":%q,"content":"hi"}`, outside),
		Approved: true,
	})

	// ---- 前置断言：这条调用**真的被管线拒绝了**，否则本用例退化成空测（R2/R3） ----
	if pr.Proceed {
		t.Fatalf("前提失效：越界写未被拒绝（Proceed=true）—— 本用例会变成空测。stageLogs=%+v", pr.StageLogs)
	}
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("前提失效：越界写应打 sandbox_denied，实际 %q（stageLogs=%+v）",
			pr.FailureKind, pr.StageLogs)
	}

	recs := g9ReadLog(t, logPath)
	rejected := g9FilterMsg(recs, "tool: rejected")

	// ---- 判据①：负→正跃迁（before = 0 条，after = 1 条） ----
	if len(rejected) != 1 {
		t.Fatalf("G9-② 负→正跃迁失败：`[audit] tool: rejected` 记录数 = %d，期望 1（before 态为 0）。\n"+
			"全部日志记录：\n%s", len(rejected), g9Dump(recs))
	}

	got := rejected[0]
	if !strings.EqualFold(got.Level, "INFO") {
		t.Fatalf("G9-② 留痕级别 = %q，期望 INFO（可审计级别；DEBUG 在默认级别下被过滤 ⇒ 等价零留痕）", got.Level)
	}
	if got.Phase != "tool_execute" {
		t.Fatalf("G9-② phase = %q，期望 tool_execute（须与 execute done 同形态）", got.Phase)
	}
	if got.Tool != "write_file" {
		t.Fatalf("G9-② tool = %q，期望 write_file", got.Tool)
	}
	// 票面约束②：必带 stage 与 failure_kind。
	if got.Stage != "custom_validate" {
		t.Fatalf("G9-② stage = %q，期望 custom_validate（越界写在 CustomValidateStage 被拒）", got.Stage)
	}
	if got.Kind != string(FailureKindSandboxDenied) {
		t.Fatalf("G9-② failure_kind = %q，期望 %q", got.Kind, FailureKindSandboxDenied)
	}
	if got.Reason == "" {
		t.Fatal("G9-② reason 为空：拒绝原因必须可还原（= sr.Rejection）")
	}

	// ---- 反向守卫：被拒调用**不得**留下 execute done ----
	// 证明留痕来自**管线拒绝点**，而不是"其实执行了"或"挂到执行层去了"。
	if n := len(g9FilterMsg(recs, "tool: execute done")); n != 0 {
		t.Fatalf("越界写被拒后出现了 %d 条 `tool: execute done` —— 说明工具真的执行了，"+
			"或留痕被挂到了执行层（挂错层）。\n全部日志记录：\n%s", n, g9Dump(recs))
	}
}

// TestGT_G9_2_ProductionWiredPipelineAlsoLogs 生产接线断言（R1）：
// 用**生产装配入口** `Executor.InitPipelineWithHooks`（tui/lifecycle.go:425 /
// runner_core.go:532 / bridge.go:274 三入口汇聚于此）装配出的管线，同样留痕。
//
// 与上一条的分工：上一条证明"留痕在**管线层**、与调用方无关"；
// 本条证明"**生产真正装配出来的那份管线**确实带着这个留痕"（防接线被摘掉）。
func TestGT_G9_2_ProductionWiredPipelineAlsoLogs(t *testing.T) {
	logPath := g9WithAuditLog(t)

	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(policy, ws) // 生产装配入口
	if e.Pipeline == nil {
		t.Fatal("前提失效：InitPipelineWithHooks 未装配 Pipeline —— 本用例前提不成立")
	}

	outside := filepath.Join(filepath.Dir(ws), "g9_2_wired_outside.txt")
	defer os.Remove(outside)

	pr := e.Pipeline.Execute(context.Background(), Call{
		ID:       "gt-g9-2-wired",
		Name:     "write_file",
		Input:    fmt.Sprintf(`{"path":%q,"content":"hi"}`, outside),
		Approved: true,
	})
	if pr.Proceed {
		t.Fatalf("前提失效：越界写未被拒绝（stageLogs=%+v）", pr.StageLogs)
	}

	recs := g9ReadLog(t, logPath)
	rejected := g9FilterMsg(recs, "tool: rejected")
	if len(rejected) != 1 {
		t.Fatalf("G9-② 生产装配路径：`[audit] tool: rejected` 记录数 = %d，期望 1。\n"+
			"全部日志记录：\n%s", len(rejected), g9Dump(recs))
	}
	if rejected[0].Stage != "custom_validate" || rejected[0].Kind != string(FailureKindSandboxDenied) {
		t.Fatalf("G9-② 生产装配路径：stage/failure_kind 不符：stage=%q kind=%q（期望 custom_validate / %q）",
			rejected[0].Stage, rejected[0].Kind, FailureKindSandboxDenied)
	}
}

// TestGT_G9_2_RuleGateRejectionIsLogged 覆盖**同源面**：`ToolPipeline.Execute` 顶部的
// 规则前置裁决拒绝点（`blocked by explicit deny rule`）。
//
// 与越界写是**同一个缺陷**（"管线拒绝零留痕"）的两个出口 —— 只修一处 = 没修到根（R3⑤）。
// 票面只点名了 CustomValidateStage 那处；本处为**同源面一并修**，已在回执显式标注待来源组追认。
func TestGT_G9_2_RuleGateRejectionIsLogged(t *testing.T) {
	logPath := g9WithAuditLog(t)

	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)
	pr := p.Execute(context.Background(), Call{
		ID:    "gt-g9-2-rule-gate",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-g9/victim.txt"}`,
	})

	// 前置：这条调用真的被 rule_gate 拒绝了。
	if pr.Proceed {
		t.Fatalf("前提失效：deny 规则命中的调用未被拒绝 —— 本用例会变成空测。stageLogs=%+v", pr.StageLogs)
	}
	if pr.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("前提失效：deny 规则命中的调用应打 policy_denied，实际 %q（stageLogs=%+v）",
			pr.FailureKind, pr.StageLogs)
	}

	recs := g9ReadLog(t, logPath)
	rejected := g9FilterMsg(recs, "tool: rejected")
	if len(rejected) != 1 {
		t.Fatalf("G9-②（同源面 rule_gate）：`[audit] tool: rejected` 记录数 = %d，期望 1。\n"+
			"全部日志记录：\n%s", len(rejected), g9Dump(recs))
	}
	got := rejected[0]
	if got.Stage != "rule_gate" {
		t.Fatalf("G9-② stage = %q，期望 rule_gate", got.Stage)
	}
	if got.Kind != string(FailureKindPolicyDenied) {
		t.Fatalf("G9-② failure_kind = %q，期望 %q", got.Kind, FailureKindPolicyDenied)
	}
	if got.Tool != "bash" {
		t.Fatalf("G9-② tool = %q，期望 bash", got.Tool)
	}
}

// TestGT_G9_2_SuccessPathLogsNoRejection 反例守卫（**不具判别性**，见下）。
//
// 目的：防止把 `tool: rejected` 打在**放行/成功**路径上（越界扩大留痕面）。
//
// ⚠️ 本条**在 before 与 after 两态均为绿** ⇒ **不具判别性**，不得当判别器读。
//
//	它的价值只在"防更糟形态"（把 rejected 打成无差别行），不能证明 G9-② 修对了。
func TestGT_G9_2_SuccessPathLogsNoRejection(t *testing.T) {
	logPath := g9WithAuditLog(t)

	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	allowRule, err := permission.ParseRuleString("write_file", permission.RuleAllow, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString(write_file allow): %v", err)
	}
	if err := policy.AddRule(*allowRule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)
	inside := filepath.Join(ws, "g9_2_inside.txt")
	pr := p.Execute(context.Background(), Call{
		ID:       "gt-g9-2-success",
		Name:     "write_file",
		Input:    fmt.Sprintf(`{"path":%q,"content":"hi"}`, inside),
		Approved: true,
	})

	// 前置：这条调用真的执行成功了（否则"零 rejected"是因为它也没被执行）。
	if !pr.Proceed {
		t.Fatalf("前提失效：工作区内写未被放行（rejection=%q，stageLogs=%+v）—— 本用例不能代表成功路径",
			pr.Rejection, pr.StageLogs)
	}

	recs := g9ReadLog(t, logPath)
	if n := len(g9FilterMsg(recs, "tool: rejected")); n != 0 {
		t.Fatalf("越界：成功路径上出现了 %d 条 `tool: rejected`（留痕面被扩大）。\n全部日志记录：\n%s",
			n, g9Dump(recs))
	}
	if n := len(g9FilterMsg(recs, "tool: execute done")); n != 1 {
		t.Fatalf("前提/回归失效：成功路径应恰好 1 条 `tool: execute done`，实际 %d 条。\n"+
			"全部日志记录：\n%s", n, g9Dump(recs))
	}
}

// ---------------------------------------------------------------------------
// G9-④ · SourcePipeline 常量与 Explain 文案
// ---------------------------------------------------------------------------

// TestGT_G9_4_SourcePipelineSemantics 钉死四件事：
//
//	① 取值字面量（G9-002 在 TUI 侧按**字符串**落账本 ⇒ 这是跨包契约）；
//	② 与既有 5 个取值不撞车；
//	③ Explain 必须**据实** —— 不得出现"用户拒绝"（用户从未被询问）；
//	④ 反向守卫：SourceUser **仍然**明说 "denied by user"（防为加新值改掉老语义）。
//
// 判别性：③ 的判据是「文案 != Explain 的**兜底**文案」——
// 删掉 `case SourcePipeline` 后兜底分支接管，文案**恰好**等于兜底 ⇒ 本条打红。
// （若只断言"非空 / 不含 by user"，兜底文案也满足 ⇒ 那是**恒绿假判别器**，R4-7。）
func TestGT_G9_4_SourcePipelineSemantics(t *testing.T) {
	// ① 跨包字面量契约（G9-002 依赖它）
	if got := string(SourcePipeline); got != "pipeline" {
		t.Fatalf("G9-④：SourcePipeline = %q，期望 \"pipeline\"（TUI 侧按该字面量落账本）", got)
	}

	// ② 不撞车 + ③ 据实文案
	all := []ConfirmSource{SourceUser, SourceCtxCancel, SourceTimeout, SourceSlotGuard, SourceNoChannel}
	seen := map[ConfirmSource]bool{SourcePipeline: true}
	for _, s := range all {
		if seen[s] {
			t.Fatalf("G9-④：SourcePipeline 与既有取值撞车（%q）—— 账本无法区分成因方", s)
		}
		seen[s] = true
	}

	const tool = "write_file"
	out := ConfirmOutcome{Decision: DecisionDenied, Source: SourcePipeline}.Explain(tool)

	if out == "" {
		t.Fatal("G9-④：SourcePipeline 的 Explain 不得为空")
	}
	// 判别性断言：必须**不等于**兜底文案（= case 存在且生效）
	fallback := fmt.Sprintf("tool %s was not run (decision=%s source=%s)", tool, DecisionDenied, SourcePipeline)
	if out == fallback {
		t.Fatalf("G9-④：SourcePipeline 落到了 Explain 的兜底分支 —— `case SourcePipeline` 缺失或未生效。文案=%q", out)
	}
	// 据实：必须点明"用户从未被询问"
	if !strings.Contains(out, "never asked") {
		t.Fatalf("G9-④：SourcePipeline 文案未据实说明'用户从未被询问'：%q", out)
	}
	// 硬纪律（S17 §4.2 / confirm.go:42）：非用户成因**不得**出现"用户拒绝"
	for _, bad := range []string{"by user", "by the user", "用户拒绝"} {
		if strings.Contains(out, bad) {
			t.Fatalf("G9-④：SourcePipeline 文案出现 %q（管线拒绝 ≠ 用户拒绝，用户从未被询问）：%q", bad, out)
		}
	}
	if !strings.Contains(out, tool) {
		t.Fatalf("G9-④：文案应包含工具名（便于审计定位）：%q", out)
	}

	// 与其余成因文案互不相同（沿用 R3 `TestR3_D1_ExplainTextsAreMutuallyDistinct` 的判据）
	others := map[string]ConfirmSource{}
	for _, s := range all {
		text := ConfirmOutcome{Decision: DecisionDenied, Source: s}.Explain(tool)
		if prev, dup := others[text]; dup && prev != s {
			t.Fatalf("G9-④：与 %q 的 Explain 文案相同（%q）—— 各种成因必须可区分", prev, text)
		}
		others[text] = s
	}
	if prev, dup := others[out]; dup {
		t.Fatalf("G9-④：SourcePipeline 的文案与 %q 相同（%q）—— 必须可区分", prev, out)
	}

	// ④ 反向守卫：SourceUser 的老语义**未被本次扩展改动**
	if got := (ConfirmOutcome{Decision: DecisionDenied, Source: SourceUser}).Explain(tool); !strings.Contains(got, "denied by user") {
		t.Fatalf("G9-④ 越界：SourceUser 的文案应仍明说 'denied by user'（只有 SourceUser 才是用户拒绝），实际 %q", got)
	}

	// ⑤ Involved() 必须为真 ⇒ ToolExecError.Error() 才会路由到 Explain（否则文案永不生效）
	if !(ConfirmOutcome{Decision: DecisionDenied, Source: SourcePipeline}).Involved() {
		t.Fatal("G9-④：SourcePipeline 必须让 Involved()=true，否则 ToolExecError.Error() 不会走 Explain 文案")
	}
}
