package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 判别器 + 反向守卫：workspace-write 档下「编排/委派族」不应弹确认。
//
// 缺陷签名（用户手测）：档位已是 workspace-write 且开了沙箱，但**启动子 agent**
// （以及同族的编排动作）仍每次弹框。
//
// 根因：gate ①（permission.checkModeLocked）在 ww 档的判据是
// `required > LevelReadOnly` ⇒ 只要"需要写能力"就必问，**不区分这次调用是否真的
// 会产生本机副作用**。Task/Agent/Skill/TaskStop 的声明档位都是 WW（因为委派出去
// 的角色是 coder，能力上限确实含写），于是它们被当成"写操作"来审。
//
// 但它们的可观察副作用**100% 由下游调用承担**：
//   - Task / Agent  → 子 agent 的每次工具调用强制走完整管线（agent.go:992
//     executeToolCall → ExecuteWithPipeline），其 NeedConfirm 经 AgentConfirmFn
//     桥接到真实 TUI 确认框（tui/agent_confirm.go），无通道时 fail-closed；
//   - Skill         → 信任门（executor.go:286 EnableTrustGate）独立把关，
//     且 B2 已删除其 AllowedTools 自提权注入（web.go:845）；
//   - TaskStop      → 只能终止我们自己在 TaskCreate 里起的后台任务。
//     ⇒ 免问「启动编排」**不扩权**：逃逸工作区 / 危险命令照样被拦下弹窗。
//
// 对标业内既有实现（同类 CLI，全仓可读）：
//   - `core/src/safety.rs:32 assess_patch_safety`：patch 落在可写根内即
//     `SafetyCheck::AutoApprove`（不问），只有**逃逸可写根**才 `AskUser`；
//   - `core/src/tools/handlers/multi_agents_spec.rs`：spawn_agent 的规格里
//     `approval` 出现 0 次 —— 启动子 agent 本身不触发授权；
//   - `core/src/codex_delegate.rs:72`：子 agent 的审批「handled via parent_session
//     and are **not surfaced**」—— 不额外弹窗。
//
// 采用范围刻意**不含** TaskCreate：其描述为 "Executes a command in the background"，
// 即它真的在本机执行任意命令 —— 与 Monitor 同类，属「按名字会分错」的工具，
// 保留原有确认。
//
// 生产入口：InitPipelineWithHooks → ExecuteWithConfirm
// （即 internal/tui/tool_exec.go executeToolsDirect 走的入口）。
func TestFix_WW_OrchestrationToolsNeedNoConfirm(t *testing.T) {
	workDir := t.TempDir()

	r := NewRegistry()
	e := NewExecutor(r, workDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(policy, workDir)
	if e.Pipeline == nil {
		t.Fatal("前提失效：生产装配下 Pipeline 必须非 nil")
	}

	// wasConfirming 标记该用例在**改动前**是否真的会弹窗（before 矩阵实测）——
	// 只有这类用例才必须观察到「编排豁免」标记，因为另一类（explore/plan）
	// 本来就是靠**输入感知定级**走通另一条免问路径的，不该要求同一标记。
	cases := []struct {
		name          string
		input         string
		wasConfirming bool
	}{
		{"Task", `{"prompt":"list the files in this repository"}`, true},
		{"Agent", `{"prompt":"implement the function","agent_type":"coder"}`, true},
		{"Agent", `{"prompt":"verify the implementation","agent_type":"verification"}`, true},
		{"Skill", `{"name":"zz-no-such-skill"}`, true},
		{"TaskStop", `{"task_id":"zz-no-such-task"}`, true},
		// 以下三条在改动前就免问（read-only 角色 ⇒ 输入感知定级），
		// 列在这里是**回归守卫**：本次改动不得把它们弄红。
		{"Agent", `{"prompt":"search the codebase","agent_type":"explore"}`, false},
		{"Agent", `{"prompt":"search the codebase","agent_type":"plan"}`, false},
		{"Agent", `{"prompt":"just do it"}`, false}, // 未声明 ⇒ 默认 explore
	}

	for _, c := range cases {
		pr := e.ExecuteWithConfirm(context.Background(), Call{
			ID:    "orc-probe",
			Name:  c.name,
			Input: c.input,
		})

		if pr.NeedConfirm {
			t.Fatalf("编排型工具在 ww 档不应弹确认（副作用由下游调用各自承担）: tool=%s input=%s rejection=%q stageLogs=%+v",
				c.name, c.input, pr.Rejection, pr.StageLogs)
		}
		if !c.wasConfirming {
			continue
		}
		// 证明是「被编排豁免抑制」而不是「压根没走到权限阶段」——否则断言可能被
		// 前置阶段的拒绝"蒙对"（假绿）。这道自检在本文件首次运行时确实抓到过
		// 一次误判（Agent explore 走的是另一条免问路径），说明它有判别力。
		if !orchestrationExemptionObserved(pr.StageLogs) {
			t.Fatalf("未观察到编排免问标记（管线可能提前拒绝/走了别的免问路径，断言不可信）: tool=%s input=%s reject=%v stageLogs=%+v",
				c.name, c.input, !pr.Proceed, pr.StageLogs)
		}
	}
}

// orchestrationExemptionObserved 报告管线日志里是否存在「编排免问」的留痕。
//
// 为什么要有它：只断言 `!NeedConfirm` 会被**提前拒绝**蒙对 —— 若某前置阶段先
// Proceed=false 短路，NeedConfirm 同样是 false，但那是"被拒"不是"免问"。
// 这个检查把两类情形区分开（判别器自检）。
func orchestrationExemptionObserved(logs []StageLog) bool {
	for _, l := range logs {
		if l.Stage == "permission" && strings.Contains(l.Message, "orchestration exemption") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 反向守卫

// 反向守卫（非判别器）：豁免是**单向收窄**，不得放大任何能力。
//
// 五条不变量，改动前后都必须成立：
//
//	① read-only 档下委派型工具仍被**拒绝**（豁免只影响"问不问"，不影响"允不允许"）；
//	② 逃逸工作区的 write_file 仍要确认；
//	③ bash 仍要确认；
//	④ 危险命令（rm -rf /）仍要确认；
//	⑤ 显式 deny 规则仍然绝对优先（连 Task 也拦得住）。
func TestGuard_WW_OrchestrationExemptionDoesNotWiden(t *testing.T) {
	ctx := context.Background()

	t.Run("read-only档仍拒绝委派", func(t *testing.T) {
		workDir := t.TempDir()
		e := NewExecutor(NewRegistry(), workDir)
		ro := permission.NewPolicy(permission.LevelReadOnly)
		e.InitPipelineWithHooks(ro, workDir)

		for _, c := range []struct{ name, input string }{
			{"Task", `{"prompt":"list files"}`},
			{"Agent", `{"prompt":"implement","agent_type":"coder"}`},
			{"Skill", `{"name":"x"}`},
			{"TaskStop", `{"task_id":"x"}`},
		} {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "ro", Name: c.name, Input: c.input})
			if pr.Proceed {
				t.Fatalf("read-only 档下委派型工具必须被拒绝（豁免不得改 allowed）: tool=%s stageLogs=%+v",
					c.name, pr.StageLogs)
			}
		}
	})

	t.Run("工作区外写不得放行", func(t *testing.T) {
		workDir := t.TempDir()
		e := NewExecutor(NewRegistry(), workDir)
		ww := permission.NewPolicy(permission.LevelWorkspaceWrite)
		e.InitPipelineWithHooks(ww, workDir)

		pr := e.ExecuteWithConfirm(ctx, Call{
			ID:    "esc",
			Name:  "write_file",
			Input: `{"path":"/tmp/zz-escape-outside.txt","content":"x"}`,
		})
		// 逃逸写要么被 CustomValidateStage 的边界校验**硬拒绝**，要么退化为一次确认；
		// 两者都可接受，但**绝不能**被放行 —— 编排豁免只覆盖编排族，不覆盖写类。
		if pr.Proceed {
			t.Fatalf("逃逸工作区的写入绝不能被放行: needConfirm=%v stageLogs=%+v", pr.NeedConfirm, pr.StageLogs)
		}
	})

	t.Run("bash与危险命令仍要确认", func(t *testing.T) {
		workDir := t.TempDir()
		e := NewExecutor(NewRegistry(), workDir)
		ww := permission.NewPolicy(permission.LevelWorkspaceWrite)
		e.InitPipelineWithHooks(ww, workDir)

		for _, cmd := range []string{"mkdir -p sh_probe", "go test ./...", "rm -rf /"} {
			pr := e.ExecuteWithConfirm(ctx, Call{
				ID:    "sh",
				Name:  "bash",
				Input: `{"command":"` + cmd + `"}`,
			})
			if !pr.NeedConfirm {
				t.Fatalf("bash(%q) 仍必须确认（本豁免不含 Exec 类）: proceed=%v stageLogs=%+v",
					cmd, pr.Proceed, pr.StageLogs)
			}
		}
	})

	t.Run("deny规则绝对优先", func(t *testing.T) {
		workDir := t.TempDir()
		// 生产同款装配：NewPolicy → WireRuleStore → InitPipelineWithHooks，
		// 规则经项目级 settings.json 注入（复用同包既有 helper，不另造脚手架）。
		e := newProductionExecutor(t, workDir,
			`{"permissions":{"deny":["Task(*)","Agent(*)"]},"priorityMode":"posix"}`)

		// deny 是绝对的：豁免不得绕过它。
		pr := e.ExecuteWithConfirm(ctx, Call{
			ID:    "deny",
			Name:  "Task",
			Input: `{"prompt":"list files"}`,
		})
		if pr.Proceed {
			t.Fatalf("deny 规则必须绝对优先于编排豁免: stageLogs=%+v", pr.StageLogs)
		}
		if pr.NeedConfirm {
			t.Fatalf("被 deny 拦截不应变成一次确认请求: stageLogs=%+v", pr.StageLogs)
		}
	})
}
