package tools

// =============================================================================
// S4 子 agent / 委派 审计 — 表征测试（characterization）
//
// 日期：2026-09-09 域：S4（AUDIT_ROADMAP.md §2）
// 不变量焦点：I1（单一强制点） / I2（默认拒绝） / I8（外部信任域） / I10（抗间接注入） / I11（资源有界）
//
// 本文件目的：在「不动任何源码」前提下，钉死 whale-code 子 agent 工具执行链路的
// *当前* 真实行为。所有测试从生产真实入口进入：
// - 顶层 Task 工具：TaskTool.Call（G7 起 TaskTool 不再是 Tool 实现，
// T1 改为结构断言「它不是 Tool、也没有权限面方法」）
// - 子 agent 内部工具调用：AgentContext.Run → executeToolCall → ExecuteWithPipeline
// 规则（production-path-tdd）：这些测试当前必须全绿 = 已钉住当前行为；
// 后续 Fix 落地后，表征「缺口」的用例会被刻意翻转并补足新断言。
// ⚠️ T1 已于 G7**刻意翻转**：见该用例头部说明。
//
// 注意：本文件只新增测试，不修改任何非测试源码。
// =============================================================================

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// -----------------------------------------------------------------------------
// 捕获型 mock AI 客户端：记录最近一次 Stream 请求的 messages（含 tool_result），
// 用于断言「子 agent 的工具调用结果是否经过权限管线 + deny 是否阻断」。
// -----------------------------------------------------------------------------
type subagentCaptureClient struct {
	responses       []<-chan ai.Event
	idx             int
	lastReqMessages []ai.MessageEntry
}

func (m *subagentCaptureClient) Stream(_ context.Context, req ai.Request) (<-chan ai.Event, error) {
	m.lastReqMessages = req.Messages
	i := m.idx
	m.idx++
	if i >= len(m.responses) {
		ch := make(chan ai.Event, 2)
		ch <- ai.TextDeltaEvent{Content: "default response"}
		ch <- ai.MessageStopEvent{}
		close(ch)
		return ch, nil
	}
	return m.responses[i], nil
}
func (m *subagentCaptureClient) Model() string   { return "test-model" }
func (m *subagentCaptureClient) SetModel(string) {}
func (m *subagentCaptureClient) Close() error    { return nil }

// captureContainsToolResult 在最近一次请求的 messages 中查找 tool_result 文本是否含 substr。
func (m *subagentCaptureClient) captureContainsToolResult(substr string) bool {
	for _, msg := range m.lastReqMessages {
		content, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, item := range content {
			if m, ok := item.(map[string]interface{}); ok {
				if m["type"] == "tool_result" {
					if out, ok := m["output"].(string); ok && strings.Contains(out, substr) {
						return true
					}
				}
			}
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// T1 — I2（Task 工具层）：**G7 有意翻转**
//
// 原用例（G6 前）断言 TaskTool.CheckPermissions 对任何输入恒 Allow、且 IsReadOnly=true，
// 用来钉住「Task 工具本身永不被权限层拦截」这一当前行为。
//
// 为什么翻转：G7 把 TaskTool 降级为执行体 —— 删掉了 16 个**零调用点**的 Tool 面方法
// （含 CheckPermissions / IsReadOnly）⇒ 原断言的对象**已不存在**（连编译都过不去）。
// 保护意图以**更强的形式**保留：Task 工具现在根本不实现 tools.Tool 接口，
// 权限层/注册路径拿不到它 ⇒「Task 工具被权限层拦截」在**结构上不可能发生**。
// 这比「表征它恒 Allow」更硬：后者只说明当前行为，前者排除了整条路径。
//
// 判别性（结构面，非行为面）：把任一 Tool 面方法加回 TaskTool，本用例立刻红。
// -----------------------------------------------------------------------------
func TestChar_S4_I2_TaskToolIsNotATool(t *testing.T) {
	toolIface := reflect.TypeOf((*Tool)(nil)).Elem()
	ttType := reflect.TypeOf(&TaskTool{})

	if ttType.Implements(toolIface) {
		t.Fatal("G7 翻转前提失效：TaskTool 又实现了 tools.Tool 接口 —— " +
			"权限层/注册路径可能重新拿到它，本用例保护的属性不再由结构保证")
	}

	// 逐个点名：这些方法一旦回归，就是「双真相源」重建的第一步
	// （下一个读 task_tool.go 的人会再次以为它才是模型可见定义）。
	for _, m := range []string{
		"CheckPermissions", "ValidateInput", "IsReadOnly", "IsEnabled", "IsConcurrencySafe",
		"IsDestructive", "InterruptBehavior", "Exposure", "RequiredSandboxLevel",
		"Name", "Prompt", "Description", "Schema", "MapToolResultToAPI",
		"RenderToolUseMessage", "RenderToolResultMessage",
	} {
		if _, ok := ttType.MethodByName(m); ok {
			t.Errorf("G7 回归：TaskTool 又长回了 %s —— 该方法是 Tool 接口面且全仓零调用点，"+
				"删除它是 G7 的裁决（ → 方案 A）", m)
		}
	}

	// 反向守卫（别撤过头）：执行入口必须在
	if _, ok := ttType.MethodByName("Call"); !ok {
		t.Error("G7 撤过头：TaskTool.Call 不见了 —— Task 工具将无法执行")
	}
}

// -----------------------------------------------------------------------------
// T2 — I1（单一强制点）+ I2（默认拒绝）在子 agent 工具层的钉死：
// 子 agent 内部发起的 bash(rm) 调用，必须经由 ExecuteWithPipeline 的权限阶段，
// 且命中 deny 规则时被拒绝（fail-closed），拒绝信息回传给子 agent 模型。
// 生产入口：AgentContext.Run → executeToolCall → ExecuteWithPipeline（PermissionStage）。
//
// 注：选用 bash(rm:*) 而非 read_file，因为只读工具在权限层可能被默认放行（见 Gap G-?），
// bash 是 S3 已验证可被 deny 拦截的可写工具，能干净地钉住「子 agent 工具调用走管线 + deny 拦截」。
// -----------------------------------------------------------------------------
func TestChar_S4_I1_I2_SubagentToolGoesThroughPipelineDenyBlocks(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	sentinel := filepath.Join(tmp, "SHOULD_NOT_EXIST")
	// 自定义 bash handler：若权限层未拦截、handler 被执行，则创建 sentinel 文件。
	// 若权限层拦截（期望行为），handler 不会被调用，sentinel 不存在。
	e.RegisterHandler("bash", func(_ context.Context, _ json.RawMessage) (string, error) {
		_ = os.WriteFile(sentinel, []byte("ran"), 0644)
		return "ran", nil
	})

	// 捕获型 mock：第一轮让子 agent 调用 bash rm，第二轮给纯文本结束。
	round1 := make(chan ai.Event, 6)
	round1 <- ai.ToolUseEvent{ID: "t1", Name: "bash", Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`}
	round1 <- ai.ToolInputDeltaEvent{PartialJSON: `{"command":"rm /tmp/whale-ab/victim.txt"}`}
	round1 <- ai.MessageStopEvent{}
	close(round1)

	round2 := make(chan ai.Event, 3)
	round2 <- ai.TextDeltaEvent{Content: "done"}
	round2 <- ai.MessageStopEvent{}
	close(round2)

	capClient := &subagentCaptureClient{responses: []<-chan ai.Event{round1, round2}}
	e.AIClient = capClient

	// 生产初始化方式：管线 + deny 规则 bash(rm:*)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：无法代表生产路径")
	}

	// coder 子 agent 通过生产入口 Run 执行
	agentCtx := NewAgentContext(e, "delete a file", "", tmp, 1)
	if _, err := agentCtx.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 断言① I1：子 agent 的工具调用经管线权限阶段被拒绝，拒绝信息回传
	if !capClient.captureContainsToolResult("rejected by pipeline") {
		t.Fatalf("I1/I2 失败：子 agent 的 bash 调用未经过管线权限阶段被拒绝（tool_result 中无 'rejected by pipeline'）")
	}
	// 断言② I2：handler 不得被调用（无副作用泄漏）
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("I2 失败：deny 路径下子 agent 的 bash handler 仍被执行（副作用泄漏）")
	}
}

// -----------------------------------------------------------------------------
// T3 — I11（资源有界）：递归深度守卫经真实 Spawn 入口生效。
// 父深度=3 → 子深度=4 > maxAgentDepth(3) → Spawn 返回 ErrAgentRecursionLimit。
// 生产入口：AgentTaskManager.Spawn → AgentTask.Execute（深度守卫在 Execute 内）。
// -----------------------------------------------------------------------------
func TestChar_S4_I11_DepthLimitViaSpawn(t *testing.T) {
	m := NewAgentTaskManager(5, 20, nil) // executor=nil 仅用于触发深度守卫（深度检查在 executor 之前）

	parentCtx := context.Background()
	input := AgentInput{Prompt: "nested", ParentCtx: &parentCtx}

	// parentDepth=3 → 子任务 depth=4 > maxAgentDepth=3
	_, err := m.Spawn(context.Background(), input, "", 3)
	if err == nil {
		t.Fatal("I11 失败：深度超过 maxAgentDepth 时应被拒绝")
	}
	if ai.GetErrorID(err) != ai.ErrAgentRecursionLimit {
		t.Fatalf("I11 失败：超深应返回 ErrAgentRecursionLimit，实际: %v", err)
	}
}

// -----------------------------------------------------------------------------
// T4 — I1（双入口/影子实现）结构钉死：
// coder 子 agent 的工具集由 getToolNameList（只读列表）+ buildAgentTools 补 "Agent" 构成，
// 其中**包含 "Agent"（嵌套派生子 agent 的工具名）但不包含 "Task"（顶层 Task 工具）**。
// 这钉死「两套子 agent 机制并存」的现状：
// - "Agent" 工具名 → handleNestedAgentCall（绕过 AgentTaskManager 的 spawn/concurrency 上限）
// - "Task" 工具名 → TaskTool → AgentTaskManager（受上限约束）
//
// -----------------------------------------------------------------------------
func TestChar_S4_I1_DualMechanismToolset(t *testing.T) {
	tmp := t.TempDir()
	e := NewExecutor(NewRegistry(), tmp)

	ctx := NewAgentContext(e, "coder task", "", tmp, 1) // coder + 非最大深度 → 应带 "Agent"
	names := make(map[string]bool)
	for _, td := range ctx.buildAgentTools() {
		names[td.Name] = true
	}

	if !names["Agent"] {
		t.Fatalf("I1 结构表征前提：coder 子 agent 工具集应包含 'Agent'（嵌套派生入口）")
	}
	if names["Task"] {
		t.Fatalf("I1 结构表征前提：coder 子 agent 工具集不应含 'Task'（顶层工具名，仅主 agent 可调用）")
	}

	// 对照：getToolNameList 基础列表本身既不含 Agent 也不含 Task（二者均由 buildAgentTools 单独追加/顶层注册）
	base := ctx.getToolNameList()
	for _, n := range base {
		if n == "Agent" || n == "Task" {
			t.Fatalf("I1 结构表征前提：getToolNameList 基础列表不应含 Agent/Task，实际含 %q", n)
		}
	}
}
