package tools

import (
	"context"
	"testing"
)

// 本文件回答用户 2026-09-13 T-B 手测的疑问：
// "为什么每次都要先人工允许，然后才被权限拒绝？"
//
// 当时的结论（**改动前的现象**）：这是两个不同工具上的两次独立决策 ——
//   - 人工允许 → 来自 `Task` 工具本身（派生 sub-agent，按写档定级，
//     workspace-write 档下需确认 → NeedConfirm=true）。
//   - 权限拒绝 → 来自子代理内部的 `read_file`，命中 deny 规则，
//     在规则前置门（pipeline.go Execute 开头）直接短路，NeedConfirm=false。
//
// ★ 2026-09-19 更新 —— 上面那"第一次"（人工允许）**已取消**，这正是用户的诉求
// （"我开了 workspace write + 沙箱，连开子 agent 之类的都要弹窗，不合业内逻辑"）：
// 编排/委派族（Task / Agent / Skill / TaskStop）现在免问，判据见
// `permission.IsOrchestrationTool`，落点在 pipeline.go 的豁免出口 (b)。
//
// 依据是**委派不扩权**：子代理复用**同一个** Executor/Pipeline，其每次工具调用
// 强制走完整 7 阶段（agent.go:992），NeedConfirm 桥接到真实 TUI 确认框；
// ⇒ 在"启动子代理"这一步再问一次是**重复把关**，且没有信息量。
//
// 因此本文件现在钉住的新形态是：**豁免 ≠ 绕过 deny**。
func TestApprovalPromptSource_TaskIsExemptButReadFileStillDenied(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"]},"priorityMode":"posix"}`)

	// 1) Task：编排豁免 → 不再请求确认（改动前此处 NeedConfirm=true）。
	taskRes := exec.ExecuteWithConfirm(context.Background(), Call{
		ID:    "c-task",
		Name:  "Task",
		Input: `{"prompt":"读取 README 第一行","description":"读取README第一行"}`,
	})
	t.Logf("Task      -> Proceed=%v NeedConfirm=%v Rejection=%q",
		taskRes.Proceed, taskRes.NeedConfirm, taskRes.Rejection)
	if taskRes.NeedConfirm {
		t.Fatalf("编排豁免后 Task 不应再请求确认，实际 NeedConfirm=true")
	}

	// 2) read_file：命中 deny 规则 → 规则前置门直接拒绝，绝不请求确认。
	//    ★ 这一条同时证明**豁免没有碰到规则面**：deny 仍在更早的 rule_gate 短路，
	//    编排豁免只抑制 gate ①（权限档位）的确认诉求。
	readRes := exec.ExecuteWithConfirm(context.Background(), Call{
		ID:    "c-read",
		Name:  "read_file",
		Input: `{"path":"/tmp/whatever.md","limit":1}`,
	})
	t.Logf("read_file -> Proceed=%v NeedConfirm=%v Rejection=%q",
		readRes.Proceed, readRes.NeedConfirm, readRes.Rejection)
	if readRes.NeedConfirm {
		t.Fatalf("read_file 命中 deny 规则时不得请求人工确认，实际 NeedConfirm=true")
	}
	if readRes.Proceed {
		t.Fatalf("read_file 命中 deny 规则时必须拒绝，实际 Proceed=true")
	}
}

// 补充：Agent 的确认与否**曾经**取决于 agent_type（输入感知定级，permission.requiredForTool）。
// 那解释了为什么 2026-09-13 23:26 的"6 个 explore 子代理并行"用例没有弹确认框。
//
// ★ 2026-09-19 起 ww 档下**所有角色**都不再弹确认（编排豁免），故本用例改为
// 只钉"角色不影响确认"这一新形态。**输入感知定级并未失效** —— 它仍然决定
// **read-only 档**下谁能通过：explore/plan 放行、coder/verification 被拒，
// 那两条由 agent_permission_stage3_test.go 继续钉住（改本用例不应动它们）。
func TestApprovalPromptSource_AgentRolesAllExemptInWorkspaceWrite(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir, `{"permissions":{}}`)

	for _, at := range []string{"explore", "plan", "coder", "verification"} {
		res := exec.ExecuteWithConfirm(context.Background(), Call{
			ID:    "c-" + at,
			Name:  "Agent",
			Input: `{"agent_type":"` + at + `","description":"探测","prompt":"读 README"}`,
		})
		t.Logf("Agent(%-12s) -> Proceed=%v NeedConfirm=%v", at, res.Proceed, res.NeedConfirm)
		if res.NeedConfirm {
			t.Fatalf("编排豁免后 ww 档下 Agent(%s) 不应弹确认，实际 NeedConfirm=true", at)
		}
	}
}

// allow 规则仍能免问（规则面，与编排豁免正交）—— 保留为**规则面回归守卫**。
//
// 注：改动前这是"不改代码即可去掉 Task 那个允许框"的**变通方案**；
// 2026-09-19 起 Task 已被编排豁免覆盖，**不再需要**这条规则。
// 本用例保留的判别价值在于：证明 allow 规则这条独立通道**没有**被豁免改动破坏。
//
// allow 只免问、不扩权（permission.go ruleCheckLocked 的 P1 沙箱硬边界）。
func TestApprovalPromptSource_AllowRuleRemovesTaskPrompt(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"],"allow":["Task"]},"priorityMode":"posix"}`)

	res := exec.ExecuteWithConfirm(context.Background(), Call{
		ID:    "c-task",
		Name:  "Task",
		Input: `{"prompt":"读取 README 第一行","description":"读取README第一行"}`,
	})
	t.Logf("带 allow:[Task] 的 Task -> Proceed=%v NeedConfirm=%v Rejection=%q",
		res.Proceed, res.NeedConfirm, res.Rejection)
	if res.NeedConfirm {
		t.Fatalf("allow 规则命中后 Task 不应再弹确认框，实际 NeedConfirm=true")
	}
}
