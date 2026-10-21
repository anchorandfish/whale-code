package tools

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 阶段 3 · Agent 定级的**生产入口层**红测（R2）。
//
// 为什么这层是权威判据：internal/permission 包内直调 Policy.Check 只证明「判定函数对了」，
// 不证明「生产真的这么判」。生产唯一路径是：
//
//	Executor.ExecuteWithConfirm (tui/tool_exec.go:executeToolsDirect 实际调用)
//	  → Pipeline.Execute (pipeline.go:127)   ← 规则前置门 + stage 链
//	  → PermissionStage.Execute (pipeline.go:921) → Policy.CheckWithRules (permission.go:494)
//	  → checkModeLocked (permission.go:197)
//
// 用的是生产同款初始化 InitPipelineWithHooks（lifecycle.go:418 同款），不是手工拼装内部结构。
// 断言打印 Proceed/NeedConfirm/Rejection/stageLogs —— 失败时能指出是哪一个 stage 没走到。

func newAgentPipeline(t *testing.T, mode permission.Level) (*Executor, string) {
	t.Helper()
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.InitPipelineWithHooks(permission.NewPolicy(mode), ws)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例前提失效，无法代表生产路径")
	}
	return e, ws
}

// 核心目标：read-only 档下，只读子代理（explore/plan）不得被拒。
// 改动前：Agent 静态登记为 LevelWorkspaceWrite → read-only 下 required > RO → Proceed=false。
func TestStage3_ProductionPipeline_ReadOnlySubagent_NotBlockedInReadOnlyMode(t *testing.T) {
	for _, at := range []string{"explore", "plan"} {
		e, _ := newAgentPipeline(t, permission.LevelReadOnly)

		pr := e.ExecuteWithConfirm(context.Background(), Call{
			ID:    "agent-ro-" + at,
			Name:  "Agent",
			Input: `{"prompt":"分析一下这个仓库的结构","agent_type":"` + at + `"}`,
		})

		if !pr.Proceed {
			t.Errorf("[生产入口] read-only 档下只读子代理 agent_type=%q 不应被拒：Proceed=%v Rejection=%q stageLogs=%+v",
				at, pr.Proceed, pr.Rejection, pr.StageLogs)
		}
		if pr.NeedConfirm {
			t.Errorf("[生产入口] read-only 档下只读子代理 agent_type=%q 不应弹确认：NeedConfirm=%v stageLogs=%+v",
				at, pr.NeedConfirm, pr.StageLogs)
		}
	}
}

// 核心目标：workspace-write 档下，只读子代理（explore/plan）不再弹无效确认。
// 改动前：Agent=WW → needConfirm=true → 管线停在 PermissionStage（NeedConfirm=true, Proceed=false）。
func TestStage3_ProductionPipeline_ReadOnlySubagent_NoConfirmInWorkspaceWrite(t *testing.T) {
	for _, at := range []string{"explore", "plan"} {
		e, _ := newAgentPipeline(t, permission.LevelWorkspaceWrite)

		pr := e.ExecuteWithConfirm(context.Background(), Call{
			ID:    "agent-ww-" + at,
			Name:  "Agent",
			Input: `{"prompt":"分析一下这个仓库的结构","agent_type":"` + at + `"}`,
		})

		if pr.NeedConfirm {
			t.Errorf("[生产入口] workspace-write 档下只读子代理 agent_type=%q 不应弹无效确认：stageLogs=%+v",
				at, pr.StageLogs)
		}
		if !pr.Proceed {
			t.Errorf("[生产入口] workspace-write 档下只读子代理 agent_type=%q 应放行到执行：Proceed=%v Rejection=%q",
				at, pr.Proceed, pr.Rejection)
		}
	}
}

// 防过度放松：可写子代理在 workspace-write 下仍须确认（用户可见现象：仍然弹框）。
// ww 档下可写子代理（coder）不再弹确认 —— 与只读子代理对称。
//
// ⚠️ 2026-09-19 **有意的 fixture 迁移**（勿"修回"）：本条原名
// `..._WriteSubagent_StillConfirms`，断言 `NeedConfirm=true`（"可写子代理必须仍弹确认"）。
// 编排豁免（permission.IsOrchestrationTool）落地后，**委派动作本身不再审批**：
//   - 委派不扩权 —— 子 agent 复用**同一个** Executor/Pipeline（agent_task.go:474），
//     其每次工具调用强制走完整 7 阶段（agent.go:992），NeedConfirm 经 AgentConfirmFn
//     桥接到真实 TUI 确认框（tui/agent_confirm.go），无通道则 fail-closed；
//   - ⇒ 弹这次框是**重复把关**，且没有信息量：用户在框里看不到子代理要做什么，
//     而它真正做的敏感动作还会各自再弹一次。
//
// 「能力超出沙箱者仍被拒」这条安全面**不受影响**，由下一条
// TestStage3_ProductionPipeline_WriteSubagent_DeniedInReadOnly 继续钉住。
func TestStage3_ProductionPipeline_WriteSubagent_NoConfirmInWorkspaceWrite(t *testing.T) {
	e, _ := newAgentPipeline(t, permission.LevelWorkspaceWrite)

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "agent-ww-coder",
		Name:  "Agent",
		Input: `{"prompt":"帮我改一个 bug","agent_type":"coder"}`,
	})

	if pr.NeedConfirm {
		t.Errorf("[生产入口] 编排豁免后 ww 档下可写子代理(coder)不应再弹确认：Proceed=%v NeedConfirm=%v stageLogs=%+v",
			pr.Proceed, pr.NeedConfirm, pr.StageLogs)
	}
}

// 防越权：read-only 档下可写子代理仍须被拒（能力超出沙箱）。
func TestStage3_ProductionPipeline_WriteSubagent_DeniedInReadOnly(t *testing.T) {
	e, _ := newAgentPipeline(t, permission.LevelReadOnly)

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "agent-ro-coder",
		Name:  "Agent",
		Input: `{"prompt":"帮我改一个 bug","agent_type":"coder"}`,
	})

	if pr.Proceed {
		t.Errorf("[生产入口] read-only 档下可写子代理(coder)不得放行：Proceed=%v stageLogs=%+v",
			pr.Proceed, pr.StageLogs)
	}
	if pr.Rejection == "" {
		t.Errorf("[生产入口] read-only 档下可写子代理应给出拒绝原因")
	}
}
