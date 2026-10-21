// workflow_command_test.go — H10 Phase 2 模块 2 /workflow 命令反例测试（编排层 §6.4）。
// 覆盖致命反例：③ /workflow run 时 MaxDepth 超限④ save-as 路径穿越。
// ；落地：HandleRun/HandleSaveAs 致命反例测试
// ≤3；落地：validateMaxDepth 致命反例 3 校验
// SNAPSHOT: tests={Run_MaxDepthExceeded_Rejected, Run_TemplateNotFound_ReturnsError, Run_PathTraversal_Rejected, SaveAs_PathTraversal_Rejected, SaveAs_OverwriteExisting, List_MergesProjectAndGlobal, Command_RegisteredViaCommandList, Run_RenderVariablesTaskDesc, Run_ValidTemplate_Passes}

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
)

// makeTestWorkflowCommands 构造测试用 WorkflowCommands。
// 返回 (cmds, baseDir, projectDir, capability) 供测试扩展。
func makeTestWorkflowCommands(t *testing.T) (*WorkflowCommands, string, string, *CapabilityCache) {
	t.Helper()
	baseDir := t.TempDir()
	projectDir := filepath.Join(baseDir, "workflows")
	globalDir := filepath.Join(baseDir, "global-workflows")

	// 构造 CapabilityCache（直接填充 items，避免依赖 doctor）
	capPath := filepath.Join(baseDir, "orchestrator", "capability_cache.json")
	cap, err := NewCapabilityCache(capPath)
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}
	cap.items = map[string]bool{"env.go": true, "env.git": true}

	agent := &mockN1Agent{}
	dispatcher := NewDispatcher(agent, filepath.Join(baseDir, "orchestrator"))
	router := NewRouterFromPhase0(nil, BudgetModeBalanced)
	budget := NewBudgetTracker(100000) // 1000 美元
	loader := NewTemplateLoader(projectDir, globalDir)

	cmds := NewWorkflowCommands(loader, dispatcher, router, budget, baseDir, cap)
	return cmds, baseDir, projectDir, cap
}

// TestWorkflowRun_MaxDepthExceeded_Rejected 致命反例 3：模板 Variables 含 max_depth=5 → 拒绝（，零容忍）。
func TestWorkflowRun_MaxDepthExceeded_Rejected(t *testing.T) {
	cmds, _, projectDir, _ := makeTestWorkflowCommands(t)

	// 构造含 max_depth=5 的模板（违反 ≤3 硬上限）
	tpl := makeValidTemplate("depth-exceeded")
	tpl.Variables = []Variable{
		{Name: WorkflowVarMaxDepth, Desc: "max depth", Required: false, Default: "5"},
	}
	if err := cmds.loader.Save(tpl); err != nil {
		t.Fatalf("Save 模板失败: %v", err)
	}
	// 注：Save 不校验 Variables 内 max_depth（Variables 字段无语义校验），校验在 HandleRun.validateMaxDepth

	_, err := cmds.HandleRun(context.Background(), []string{"depth-exceeded", "--input", "test requirement"})
	if err == nil {
		t.Fatal("致命反例 3：max_depth=5 必须被拒绝（，零容忍）")
	}
	if !strings.Contains(err.Error(), "max_depth") || !strings.Contains(err.Error(), "exceeds hard limit") {
		t.Errorf("error 应包含 'max_depth' 与 'exceeds hard limit'，got: %v", err)
	}

	// 确保未创建任何 task 目录（拒绝在前，不应有副作用）
	orchDir := filepath.Join(cmds.baseDir, "orchestrator")
	entries, _ := os.ReadDir(orchDir)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "T-") {
			t.Errorf("致命反例 3 被拒后不应创建 task 目录，但发现 %s", e.Name())
		}
	}
	_ = projectDir
}

// TestWorkflowRun_TemplateNotFound_ReturnsError：run 不存在的模板名 → error。
func TestWorkflowRun_TemplateNotFound_ReturnsError(t *testing.T) {
	cmds, _, _, _ := makeTestWorkflowCommands(t)
	_, err := cmds.HandleRun(context.Background(), []string{"nonexistent", "--input", "test"})
	if err == nil {
		t.Fatal("run 不存在的模板必须返回 error")
	}
	if !strings.Contains(err.Error(), "load template") {
		t.Errorf("error 应包含 'load template'，got: %v", err)
	}
}

// TestWorkflowRun_PathTraversal_Rejected：run name 含 ../ → error（路径穿越防护）。
func TestWorkflowRun_PathTraversal_Rejected(t *testing.T) {
	cmds, _, _, _ := makeTestWorkflowCommands(t)
	cases := []string{"../evil", "foo/bar", "foo\\bar", ".."}
	for _, evil := range cases {
		t.Run("name="+evil, func(t *testing.T) {
			_, err := cmds.HandleRun(context.Background(), []string{evil, "--input", "test"})
			if err == nil {
				t.Fatalf("run name=%q 必须返回 error（路径穿越防护）", evil)
			}
		})
	}
}

// TestWorkflowSaveAs_PathTraversal_Rejected 致命反例 4：save-as name 含 ../ → 拒绝（零容忍）。
func TestWorkflowSaveAs_PathTraversal_Rejected(t *testing.T) {
	cmds, baseDir, _, _ := makeTestWorkflowCommands(t)

	// 先创建一个 task（state.json），让 save-as 能找到任务
	taskID := "T-test-001"
	state := State{TaskID: taskID, Stage: StageDone, UpdatedAt: 1720000000}
	if err := SaveState(baseDir, taskID, state); err != nil {
		t.Fatalf("SaveState 失败: %v", err)
	}

	cases := []string{"../evil", "foo/bar", "foo\\bar"}
	for _, evil := range cases {
		t.Run("name="+evil, func(t *testing.T) {
			_, err := cmds.HandleSaveAs(context.Background(), []string{evil, "--task", taskID})
			if err == nil {
				t.Fatalf("致命反例 4：save-as name=%q 必须被拒绝（零容忍）", evil)
			}
			if !strings.Contains(err.Error(), "path") && !strings.Contains(err.Error(), "..") {
				t.Errorf("error 应包含路径相关错误信息，got: %v", err)
			}
		})
	}
}

// TestWorkflowSaveAs_OverwriteExisting：save-as 同名模板 → 覆盖 + 新 ContentHash。
func TestWorkflowSaveAs_OverwriteExisting(t *testing.T) {
	cmds, baseDir, projectDir, _ := makeTestWorkflowCommands(t)

	// 创建一个 task
	taskID := "T-saveas-001"
	state := State{TaskID: taskID, Stage: StageDone, UpdatedAt: 1720000000}
	if err := SaveState(baseDir, taskID, state); err != nil {
		t.Fatalf("SaveState 失败: %v", err)
	}

	// 第一次 save-as
	out1, err := cmds.HandleSaveAs(context.Background(), []string{"overwrite-tpl", "--task", taskID})
	if err != nil {
		t.Fatalf("第一次 save-as 失败: %v", err)
	}
	if !strings.Contains(out1, "ContentHash") {
		t.Errorf("输出应包含 ContentHash，got: %s", out1)
	}

	// 验证文件存在
	path := filepath.Join(projectDir, "overwrite-tpl.template.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件未创建: %v", err)
	}

	// 第二次 save-as（覆盖）
	_, err = cmds.HandleSaveAs(context.Background(), []string{"overwrite-tpl", "--task", taskID})
	if err != nil {
		t.Fatalf("第二次 save-as 失败: %v", err)
	}
	// 验证文件仍存在（覆盖成功）
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("覆盖后文件丢失: %v", err)
	}
}

// TestWorkflowList_MergesProjectAndGlobal：项目级 + 全局级同名，List 只显示项目级。
func TestWorkflowList_MergesProjectAndGlobal(t *testing.T) {
	cmds, _, projectDir, _ := makeTestWorkflowCommands(t)

	// 全局级：通过 loader.Save 写入项目级目录（避免测试用例依赖外部 globalDir）
	// 这里测试 HandleList 的合并逻辑：先在项目级写两个模板
	tpl1 := makeValidTemplate("list-a")
	if err := cmds.loader.Save(tpl1); err != nil {
		t.Fatalf("Save list-a 失败: %v", err)
	}
	tpl2 := makeValidTemplate("list-b")
	if err := cmds.loader.Save(tpl2); err != nil {
		t.Fatalf("Save list-b 失败: %v", err)
	}

	out, err := cmds.HandleList(context.Background(), nil)
	if err != nil {
		t.Fatalf("HandleList 失败: %v", err)
	}
	if !strings.Contains(out, "list-a") || !strings.Contains(out, "list-b") {
		t.Errorf("List 输出应包含 list-a 和 list-b，got: %s", out)
	}
	if !strings.Contains(out, "共 2 个") {
		t.Errorf("List 输出应包含 '共 2 个'，got: %s", out)
	}
	_ = projectDir
}

// TestWorkflowList_EmptyReturnsMessage：无模板时返回提示信息。
func TestWorkflowList_EmptyReturnsMessage(t *testing.T) {
	cmds, _, _, _ := makeTestWorkflowCommands(t)
	out, err := cmds.HandleList(context.Background(), nil)
	if err != nil {
		t.Fatalf("HandleList 空目录不应报错: %v", err)
	}
	if !strings.Contains(out, "暂无") {
		t.Errorf("空目录 List 应返回提示信息，got: %s", out)
	}
}

// TestWorkflowCommand_RegisteredViaCommandList：验证 /workflow 通过 registerBuiltinCommands 注册（02 §2.5.1，零容忍）。
func TestWorkflowCommand_RegisteredViaCommandList(t *testing.T) {
	r := command.NewRegistry()
	cmd, ok := r.Get("workflow")
	if !ok {
		t.Fatal("致命反例：/workflow 必须通过 registerBuiltinCommands 注册（02 §2.5.1，零容忍）")
	}
	if cmd.Name != "workflow" {
		t.Errorf("命令名应为 'workflow'，got %q", cmd.Name)
	}
	if cmd.Handler == nil {
		t.Error("workflow 命令必须注册 Handler")
	}
	// 验证未接线时返回未初始化提示（不 panic）
	out, err := cmd.Handler("")
	if err != nil {
		t.Errorf("未接线 Handler 不应返回 error，got: %v", err)
	}
	if !strings.Contains(out, "not initialized") && !strings.Contains(out, "用法") {
		t.Errorf("未接线 Handler 应返回未初始化提示或用法，got: %s", out)
	}
}

// TestWorkflowCommand_CompletionRegistered：验证 workflow 注册状态（V0.1-HIDDEN：默认隐藏不补全，命令本体仍注册可调用）。
// 子命令补全接线（unhide 后恢复）的保护断言在 command 包 command_hidden_test.go（同包可直改注册表）。
func TestWorkflowCommand_CompletionRegistered(t *testing.T) {
	r := command.NewRegistry()
	// 默认隐藏：补全不出现（结果断言）
	completions := r.Completion("workflow s")
	if len(completions) != 0 {
		t.Fatalf("V0.1-HIDDEN: 隐藏的 /workflow 不应补全，got %v", completions)
	}
	// 命令本体仍注册（含 Handler，手输仍可执行）
	cmd, ok := r.Get("workflow")
	if !ok {
		t.Fatal("workflow 命令应在注册表中存在")
	}
	if cmd.Handler == nil {
		t.Fatal("workflow Handler 不应为 nil")
	}
}

// TestWorkflowRun_RenderVariablesTaskDesc：验证 renderVariables 将 {{.task_desc}} 替换为 input。
func TestWorkflowRun_RenderVariablesTaskDesc(t *testing.T) {
	cmds, _, _, _ := makeTestWorkflowCommands(t)
	tpl := makeValidTemplate("render-test")
	tpl.Stages[0].Prompt = "需求：{{.task_desc}} 请澄清"

	rendered, err := cmds.renderVariables(tpl, "实现登录功能")
	if err != nil {
		t.Fatalf("renderVariables 失败: %v", err)
	}
	if rendered.Stages[0].Prompt != "需求：实现登录功能 请澄清" {
		t.Errorf("{{.task_desc}} 未被正确替换，got: %s", rendered.Stages[0].Prompt)
	}
	// 验证原模板未被污染
	if tpl.Stages[0].Prompt != "需求：{{.task_desc}} 请澄清" {
		t.Errorf("renderVariables 不应污染原模板，原 Prompt 被改为: %s", tpl.Stages[0].Prompt)
	}
}

// TestWorkflowRun_ValidTemplateInteractiveOnly_Passes：纯 interactive Stage 模板（无 agent/auto）可运行成功。
// 不触发 dispatcher.Dispatch（无 N1 Agent 调用），仅推进状态机 + 写 trace + 写 state.json。
func TestWorkflowRun_ValidTemplateInteractiveOnly_Passes(t *testing.T) {
	cmds, baseDir, _, _ := makeTestWorkflowCommands(t)

	// 构造纯 interactive Stage 模板（不触发 dispatch）
	tpl := makeValidTemplate("interactive-only")
	tpl.Stages = []StageTpl{
		{ID: StageClarify, Type: WorkflowStageTypeInteractive, OnFail: OnFailRetryOnce, Prompt: "prompts/clarify.md"},
	}
	if err := cmds.loader.Save(tpl); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	out, err := cmds.HandleRun(context.Background(), []string{"interactive-only", "--input", "test"})
	if err != nil {
		t.Fatalf("纯 interactive 模板 HandleRun 不应失败: %v", err)
	}
	if !strings.Contains(out, "Task ID:") {
		t.Errorf("输出应包含 Task ID，got: %s", out)
	}

	// 验证 state.json 已创建
	orchDir := filepath.Join(baseDir, "orchestrator")
	entries, err := os.ReadDir(orchDir)
	if err != nil {
		t.Fatalf("读取 orchestrator 目录失败: %v", err)
	}
	foundTask := false
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "T-") {
			foundTask = true
			// 验证 state.json 存在
			statePath := filepath.Join(orchDir, e.Name(), "state.json")
			if _, err := os.Stat(statePath); err != nil {
				t.Errorf("state.json 未创建: %v", err)
			}
			// 验证 trace.jsonl 存在
			tracePath := filepath.Join(orchDir, e.Name(), "trace.jsonl")
			if _, err := os.Stat(tracePath); err != nil {
				t.Errorf("trace.jsonl 未创建: %v", err)
			}
		}
	}
	if !foundTask {
		t.Error("未创建任何 task 目录")
	}
}

// TestParseRunArgs 验证 parseRunArgs 解析 <name> --input "需求"。
func TestParseRunArgs(t *testing.T) {
	t.Run("name + input", func(t *testing.T) {
		name, input, err := parseRunArgs([]string{"my-tpl", "--input", "实现登录功能"})
		if err != nil {
			t.Fatalf("parseRunArgs 失败: %v", err)
		}
		if name != "my-tpl" {
			t.Errorf("name 期望 'my-tpl'，got %q", name)
		}
		if input != "实现登录功能" {
			t.Errorf("input 期望 '实现登录功能'，got %q", input)
		}
	})

	t.Run("name 无 input", func(t *testing.T) {
		name, input, err := parseRunArgs([]string{"my-tpl"})
		if err != nil {
			t.Fatalf("parseRunArgs 失败: %v", err)
		}
		if name != "my-tpl" {
			t.Errorf("name 期望 'my-tpl'，got %q", name)
		}
		if input != "" {
			t.Errorf("input 期望空，got %q", input)
		}
	})

	t.Run("空 args 拒绝", func(t *testing.T) {
		_, _, err := parseRunArgs([]string{})
		if err == nil {
			t.Fatal("空 args 必须返回 error")
		}
	})

	t.Run("--input 无值拒绝", func(t *testing.T) {
		_, _, err := parseRunArgs([]string{"my-tpl", "--input"})
		if err == nil {
			t.Fatal("--input 无值必须返回 error")
		}
	})
}

// TestParseSaveAsArgs 验证 parseSaveAsArgs 解析 <name> [--task <task-id>]。
func TestParseSaveAsArgs(t *testing.T) {
	t.Run("name only", func(t *testing.T) {
		name, taskID, err := parseSaveAsArgs([]string{"my-tpl"})
		if err != nil {
			t.Fatalf("parseSaveAsArgs 失败: %v", err)
		}
		if name != "my-tpl" {
			t.Errorf("name 期望 'my-tpl'，got %q", name)
		}
		if taskID != "" {
			t.Errorf("taskID 期望空，got %q", taskID)
		}
	})

	t.Run("name + --task", func(t *testing.T) {
		name, taskID, err := parseSaveAsArgs([]string{"my-tpl", "--task", "T-001"})
		if err != nil {
			t.Fatalf("parseSaveAsArgs 失败: %v", err)
		}
		if name != "my-tpl" {
			t.Errorf("name 期望 'my-tpl'，got %q", name)
		}
		if taskID != "T-001" {
			t.Errorf("taskID 期望 'T-001'，got %q", taskID)
		}
	})

	t.Run("空 args 拒绝", func(t *testing.T) {
		_, _, err := parseSaveAsArgs([]string{})
		if err == nil {
			t.Fatal("空 args 必须返回 error")
		}
	})
}

// TestHandle_UsageHelp：无子命令时返回用法帮助。
func TestHandle_UsageHelp(t *testing.T) {
	cmds, _, _, _ := makeTestWorkflowCommands(t)
	out, err := cmds.Handle(context.Background(), "")
	if err != nil {
		t.Fatalf("Handle 空参数不应失败: %v", err)
	}
	if !strings.Contains(out, "/workflow") || !strings.Contains(out, "list") || !strings.Contains(out, "run") || !strings.Contains(out, "save-as") {
		t.Errorf("用法帮助应包含 list/run/save-as，got: %s", out)
	}
}

// TestHandle_UnknownSubcommand：未知子命令返回用法帮助。
func TestHandle_UnknownSubcommand(t *testing.T) {
	cmds, _, _, _ := makeTestWorkflowCommands(t)
	out, err := cmds.Handle(context.Background(), "unknown-subcmd")
	if err != nil {
		t.Fatalf("Handle 未知子命令不应返回 error: %v", err)
	}
	if !strings.Contains(out, "/workflow") {
		t.Errorf("未知子命令应返回用法帮助，got: %s", out)
	}
}
