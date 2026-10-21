// r3_terminal_failurekind_test.go — R3（S17 的 D1/D2/D3 合并 S4 的 L3.2）
// 在 **internal/tools** 一侧的判别性验收，对应 `R1_FailureKind传递契约_2026-09-14.md` §6。
//
// 本文件只新增测试，不改动任何非测试源码。
//
// 被验收的生产链（行号随 A 的 L1/L2 增行而位移，以函数名为准）：
//
//	AgentTaskManager.Spawn
//	  → AgentTask.Execute            （agent_task.go）
//	    → AgentTask.runMultiRound    （t.terminalFailure = agentCtx.TerminalFailure()）
//	      → AgentContext.Run         （agent.go）
//	        → AgentContext.runMultiRound（noteTerminalFailure）
//	          → AgentContext.executeToolCall
//	            → Executor.ExecuteWithPipeline → ToolPipeline.Execute
//	              （规则前置门命中 deny → PipelineResult.FailureKind = policy_denied）
//
// 判据（可判别，非"构造出来的绿"）：
//
//  1. 正向（生产路径）：deny 规则 ⇒ 断言**下游 AgentOutput.FailureKind == "policy_denied"**。
//     ⚠️ 断言的是**结构化字段**，不是"文本里含关键字"—— 后者正是 R1 §4 C4 要消灭的判法。
//  2. 反例判别：把 kind 保留改回丢弃（`-overlay`，见本文件末尾注释），1 必须转红。
//     —— 只跑绿不作数；"注入反例能打红"才算证明断言打在生产路径上（production-path-tdd R1b）。
//  3. IK2 回归：普通失败（命令非零退出）⇒ FailureKind == ""。
//  4. IK3 回归：软拒（errSandboxDeniedSoft，file.go:252-256 的唯一生产产者）⇒ IsError 仍为 false。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// r3TwoRounds 构造两轮 mock 流：第一轮吐一个 tool_use（子 agent 的唯一一次工具调用），
// 第二轮纯文本收尾（让多轮循环自然结束）。复用既有的 subagentCaptureClient
// （定义于 subagent_audit_char_test.go，同包）。
func r3TwoRounds(ev ai.Event) []<-chan ai.Event {
	round1 := make(chan ai.Event, 3)
	round1 <- ev
	round1 <- ai.MessageStopEvent{}
	close(round1)

	round2 := make(chan ai.Event, 2)
	round2 <- ai.TextDeltaEvent{Content: "done"}
	round2 <- ai.MessageStopEvent{}
	close(round2)

	return []<-chan ai.Event{round1, round2}
}

// r3SpawnCoder 以生产方式装配 Executor 并跑一次 coder 子 agent（生产入口 Spawn）。
// addRule 在 InitPipelineWithHooks 之前把规则写进策略（与生产同序：规则先于管线装配）。
func r3SpawnCoder(t *testing.T, workDir string, addRule func(*permission.Policy), ev ai.Event) (*Executor, AgentOutput) {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	if addRule != nil {
		addRule(policy)
	}
	e.InitPipelineWithHooks(policy, workDir)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：前提失效，本用例无法代表生产路径")
	}

	e.AIClient = &subagentCaptureClient{responses: r3TwoRounds(ev)}

	mgr := NewAgentTaskManager(2, 8, e)
	parentCtx := context.Background()
	out, err := mgr.Spawn(context.Background(), AgentInput{
		Prompt:    "do the task",
		AgentType: string(AgentTypeCoder),
		ParentCtx: &parentCtx,
	}, "", 0)
	if err != nil {
		t.Fatalf("Spawn: %v（子 agent 自身的工具失败不应升级为任务失败）", err)
	}
	return e, out
}

// denyBashRm 往策略里写一条 `bash(rm:*)` 的 deny 规则（与生产 WireRuleStore 的落点同型）。
func denyBashRm(t *testing.T) func(*permission.Policy) {
	t.Helper()
	return func(p *permission.Policy) {
		rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
		if err != nil {
			t.Fatalf("ParseRuleString: %v", err)
		}
		if err := p.AddRule(*rule); err != nil {
			t.Fatalf("AddRule: %v", err)
		}
	}
}

// TestR3_TerminalFailureKindReachesAgentOutput 是 R1 契约 §6.1 的**正向判别**：
// 子 agent 的工具调用命中 deny 规则（终局拒绝）后，结构化种类必须经
// AgentContext.TerminalFailure() → AgentTask.newAgentOutput 到达 AgentOutput.FailureKind。
//
// 修复前（P1/P2 丢弃 FailureKind）该断言为红 —— 下游只能看到
// "agent tool rejected by pipeline: ..." 这行文本，无法区分"终局拒绝"与"普通失败"
// （IK1 的"禁止重试/升级"因此无从执行）。
//
// 本用例**不注册任何桩**：用真实 bash handler，并把"命令真被执行了会怎样"做成现实锚点
// （victim 文件存在）。若 deny 失效，victim 会真的被删 —— 这比桩更硬。
func TestR3_TerminalFailureKindReachesAgentOutput(t *testing.T) {
	tmp := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, out := r3SpawnCoder(t, tmp, denyBashRm(t), ai.ToolUseEvent{
		ID: "t1", Name: "bash", Input: fmt.Sprintf(`{"command":"rm -rf %s"}`, victim),
	})

	// ① C1/C2：结构化字段必须带出，不得只留在文本里（R1 §6.1）。
	if out.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("C1/C2 失败：子 agent 被 deny 规则终局拒绝后，AgentOutput.FailureKind 应为 %q，"+
			"实际 %q —— kind 在 runMultiRound / newAgentOutput 处又被丢了（下游无法禁止重试，IK1 失效）",
			FailureKindPolicyDenied, out.FailureKind)
	}

	// ② 现实锚点：deny 生效 ⇒ victim 必须还在（若不在，说明"拒绝"是假的，上面的 kind 也就无意义）。
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("deny 路径下 victim 被真实删除（%v）—— 拒绝并未真正阻止执行，本用例的结论无效", statErr)
	}
}

// TestR3_IK2_NormalFailureHasNoFailureKind 是 R1 契约 §6.3 的 **IK2 回归**：
// 普通执行失败（命令非零退出）**不得**被标成终局拒绝 —— 否则上层会放弃本可重试的失败。
//
// 同样不注册桩：用真实 bash 跑一个必然非零退出的命令。
func TestR3_IK2_NormalFailureHasNoFailureKind(t *testing.T) {
	tmp := t.TempDir()

	e, out := r3SpawnCoder(t, tmp, nil, ai.ToolUseEvent{
		ID: "t1", Name: "bash", Input: `{"command":"false"}`,
	})

	// 前置断言（防假绿）：工具必须真的执行过并失败 —— tool_result 里应出现 runMultiRound
	// 写下的 "Error: ..."。若这里为假，说明 bash 在更早的阶段就被挡下了，
	// 下面的 FailureKind=="" 只是"根本没执行"的副产品，结论无效。
	capClient, ok := e.AIClient.(*subagentCaptureClient)
	if !ok {
		t.Fatalf("AIClient 类型异常：%T", e.AIClient)
	}
	if !capClient.captureContainsToolResult("Error:") {
		t.Fatal("前置失败：tool_result 中没有 'Error:' —— 本用例没走到'普通执行失败'路径，结论无效")
	}

	if out.FailureKind != "" {
		t.Fatalf("IK2 失败：普通失败被标成终局拒绝 %q —— 会让上层放弃本可重试的失败", out.FailureKind)
	}
}

// TestR3_IK3_SoftSandboxDenyKeepsIsErrorFalse 是 R1 契约 §6.4 的 **IK3 回归**（守卫，非判别器）。
//
// 形态（R1 §2 已登记，勿改）：`file.go:252-256` 的 validatePathSandbox 出口返回
// errSandboxDeniedSoft，由 executeDirect 映射为 **IsError=false + FailureKind=sandbox_denied**
// —— "模型文本不变 + 结构化标记为拒绝"。R3 只允许**新增**结构化标记的传递，
// 绝不允许把这处 `IsError=false` 改成 `true`（那会改变模型的 tool_result 契约）。
func TestR3_IK3_SoftSandboxDenyKeepsIsErrorFalse(t *testing.T) {
	tmp := t.TempDir()
	e := NewExecutor(NewRegistry(), tmp)

	// 显式覆盖 handler。⚠️ 不能用 e.RegisterHandler —— 它对已存在的名字**返回错误且不覆盖**
	// （executor.go:930-932），而 NewExecutor 已注册全部内建 handler；忽略其返回值的调用方
	// 会得到一个永不生效的桩。这里直接替换，并由下面的断言证明桩确实被执行。
	stubToolHandler(e, "read_file", func(_ context.Context, _ json.RawMessage) (string, error) {
		return "Sandbox denied: path escapes workDir", errSandboxDeniedSoft
	})

	res := e.executeDirect(context.Background(), Call{
		ID: "c1", Name: "read_file", Input: `{"path":"/etc/passwd","limit":1}`,
	})

	if !strings.Contains(res.Output, "Sandbox denied") {
		t.Fatalf("前置失败：桩未被调用（Output=%q）—— 结论无效", res.Output)
	}
	if res.IsError {
		t.Fatal("IK3 失败：软拒（errSandboxDeniedSoft）形态必须保持 IsError=false（模型文本契约不变）")
	}
	if res.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("IK3 失败：软拒应打 sandbox_denied 结构化标记，实际 %q", res.FailureKind)
	}
}

// stubToolHandler 直接替换 Executor 的工具实现（测试夹具）。
//
// 存在的理由：Executor.RegisterHandler 对**已存在**的名字拒绝覆盖并返回 error，
// 而 NewExecutor 已把 11 个内建 handler 全部注册。忽略返回值的调用方会静默拿到
// 一个不生效的桩 —— 这类"看似在测 A、实际在测内建实现"的假绿必须先被堵住。
// 本函数显式替换，且调用方**必须**在断言里证明桩被执行过（见 TestR3_IK3_* 的前置断言）。
func stubToolHandler(e *Executor, name string, fn Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[name] = fn
}

// TestR3_D1_ExplainTextsAreMutuallyDistinct 是 S17 §4.2 / §5.3 对 D1 的**文案层**判别：
// 四种非批准成因的说明必须**互不相同**，且**只有 SourceUser 允许出现"用户拒绝"**。
// （production 调度路径上的三态留痕由 internal/tui 的 R3 用例断言。）
func TestR3_D1_ExplainTextsAreMutuallyDistinct(t *testing.T) {
	cases := []ConfirmOutcome{
		{Decision: DecisionDenied, Source: SourceUser},
		{Decision: DecisionCancelled, Source: SourceUser},
		{Decision: DecisionDenied, Source: SourceCtxCancel},
		{Decision: DecisionDenied, Source: SourceTimeout},
		{Decision: DecisionDenied, Source: SourceSlotGuard},
		{Decision: DecisionDenied, Source: SourceNoChannel},
	}

	seen := make(map[string]int, len(cases))
	for i, c := range cases {
		text := c.Explain("bash")
		if text == "" {
			t.Fatalf("case[%d] %+v：Explain 不得为空", i, c)
		}
		if prev, dup := seen[text]; dup {
			t.Fatalf("D1 失败：case[%d] 与 case[%d] 文案相同（%q）—— 各种成因必须可区分", i, prev, text)
		}
		seen[text] = i

		// 只有用户本人在确认框上做出决定，才允许说"by user"。
		if c.Source != SourceUser && strings.Contains(text, "by user") {
			t.Fatalf("D1 失败：source=%q 的文案不得写成'用户拒绝'（用户可能根本没被问过）：%q",
				c.Source, text)
		}
	}

	// 反向：SourceUser 必须明说是用户（否则用户根本看不出是谁做的决定）。
	if got := (ConfirmOutcome{Decision: DecisionDenied, Source: SourceUser}).Explain("bash"); !strings.Contains(got, "denied by user") {
		t.Fatalf("D1 失败：SourceUser 的文案应明说'denied by user'，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 反例判别（R1 §6.2）—— 不在本文件内自动执行，按下列命令手工跑：
//
//	# ① 复制 agent.go 并**撤销** P1 的 kind 保留（改回"只留文本"）
//	cp internal/tools/agent.go /tmp/r3_before_agent.go
//	# 把 `Kind: pr.FailureKind,` 改成 `Kind: "",`（P1 丢弃点）
//	# ② 让 go 用改后的副本编译（不改磁盘原文件）
//	#    overlay.json: {"Replace": {"<abs>/internal/tools/agent.go": "/tmp/r3_before_agent.go"}}
//	go test -mod=vendor -count=1 -overlay=/tmp/r3_overlay.json \
//	    ./internal/tools/ -run TestR3_TerminalFailureKindReachesAgentOutput
//
// 期望：**FAIL**（AgentOutput.FailureKind == ""）。若仍 PASS，说明断言没打在传递链上，
// 本文件的"绿"无判别力。参见 production-path-tdd 的 R1b。
// ---------------------------------------------------------------------------
