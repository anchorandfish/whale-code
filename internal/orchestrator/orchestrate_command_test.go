// orchestrate_command_test.go — 模块 5 /orchestrate 命令反例测试（TDD 先行，02 §2.5.1 / §2.7.5）。
// 覆盖致命反例：/orchestrate 必须通过 command_list.go 注册（非自定义入口）/ 命令描述中文 / start 初始状态 / resume 恢复中断任务。
// ；落地：通过 command.go registerBuiltinCommands 注册
// H10 新增 /orchestrate 命令；落地：本模块测试
// SNAPSHOT: tests={RegisteredViaCommandList(致命反例), NotRegisteredDirectly(致命反例), DescriptionChinese, HandleStart_InitialState, HandleResume_InterruptedTask}

package orchestrator

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/command"
)

// makeTestOrchestrator 构造测试用 Orchestrator（baseDir 为 t.TempDir()）。
func makeTestOrchestrator(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	baseDir := t.TempDir()
	agent := &mockN1Agent{}

	qualityPath := filepath.Join(baseDir, "orchestrator", "quality.jsonl")
	quality, err := NewQualityStore(qualityPath)
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	profile := NewProfileStore()
	capPath := filepath.Join(baseDir, "orchestrator", "capability_cache.json")
	capability, err := NewCapabilityCache(capPath)
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}

	orch := NewOrchestrator(baseDir, agent, quality, profile, capability, BudgetModeBalanced)
	return orch, baseDir
}

// TestOrchestrateCmd_RegisteredViaCommandList 致命反例：验证 /orchestrate 命令在 Registry.registerBuiltinCommands 后可通过 Registry 查到。
// 验证目标（02 §2.5.1）：/orchestrate 必须通过 command_list.go 的 orchestrateCommands() 注册，不绕过标准注册流程。
func TestOrchestrateCmd_RegisteredViaCommandList(t *testing.T) {
	// NewRegistry 内部调用 registerBuiltinCommands，包含 orchestrateCommands()
	reg := command.NewRegistry()

	cmd, ok := reg.Get("orchestrate")
	if !ok {
		t.Fatal("致命反例：/orchestrate 命令未通过 Registry.registerBuiltinCommands 注册（02 §2.5.1 零容忍）")
	}
	if cmd.Name != "orchestrate" {
		t.Errorf("expected command name 'orchestrate', got %q", cmd.Name)
	}
	if cmd.Handler == nil {
		t.Error("orchestrate command Handler must not be nil")
	}
}

// TestOrchestrateCmd_NotRegisteredDirectly 致命反例：验证 /orchestrate 不通过 main.go 自定义入口注册。
// 验证目标（02 §2.5.1）：/orchestrate 必须在 registerBuiltinCommands 内注册，不在 main.go 或其他入口单独注册。
// 此测试通过验证 NewRegistry()（仅调 registerBuiltinCommands）后即存在 /orchestrate，无需任何外部注册调用。
func TestOrchestrateCmd_NotRegisteredDirectly(t *testing.T) {
	// 仅通过 NewRegistry() 构造，不调用任何额外的注册函数
	reg := command.NewRegistry()

	// /orchestrate 必须已存在（来自 registerBuiltinCommands → orchestrateCommands()）
	// NewRegistry() 内部仅调用 registerBuiltinCommands，无任何外部注册入口
	cmd, ok := reg.Get("orchestrate")
	if !ok {
		t.Fatal("致命反例：/orchestrate 未在 registerBuiltinCommands 中注册，可能在 main.go 自定义入口注册（02 §2.5.1 零容忍）")
	}

	// 验证命令有 Category（说明走了标准注册流程，非裸 main.go 注入）
	if cmd.Category == "" {
		t.Error("orchestrate command Category 为空（应在 orchestrateCommands() 中设置 Category）")
	}
	if cmd.Category != "Automation" {
		t.Errorf("orchestrate command Category = %q, want 'Automation'", cmd.Category)
	}
}

// TestOrchestrateCmd_DescriptionChinese 验证 /orchestrate 命令描述包含中文（02 §2.5.1 N16 规范）。
func TestOrchestrateCmd_DescriptionChinese(t *testing.T) {
	reg := command.NewRegistry()
	cmd, ok := reg.Get("orchestrate")
	if !ok {
		t.Fatal("/orchestrate command not found")
	}

	// 描述必须非空
	if cmd.Description == "" {
		t.Error("orchestrate command Description must not be empty")
	}

	// 描述必须包含中文字符（N16 规范）
	hasChinese := false
	for _, r := range cmd.Description {
		if r >= 0x4e00 && r <= 0x9fff { // CJK Unified Ideographs
			hasChinese = true
			break
		}
	}
	if !hasChinese {
		t.Errorf("orchestrate command Description 必须包含中文（N16 规范），got: %q", cmd.Description)
	}
}

// TestOrchestrateCmd_CompletionRegistered 验证 /orchestrate 注册状态（V0.1-HIDDEN：默认隐藏不补全，命令本体仍注册可调用）。
// 子命令补全接线（unhide 后恢复）的保护断言在 command 包 command_hidden_test.go（同包可直改注册表）。
func TestOrchestrateCmd_CompletionRegistered(t *testing.T) {
	reg := command.NewRegistry()

	// 默认隐藏：补全不出现（结果断言）
	completions := reg.Completion("/orchestrate s")
	if len(completions) != 0 {
		t.Fatalf("V0.1-HIDDEN: 隐藏的 /orchestrate 不应补全，got %v", completions)
	}

	// 命令本体仍注册（含 Handler，手输仍可执行）
	cmd, ok := reg.Get("orchestrate")
	if !ok {
		t.Fatal("orchestrate 命令应在注册表中存在")
	}
	if cmd.Handler == nil {
		t.Fatal("orchestrate Handler 不应为 nil")
	}
}

// TestHandleStart_InitialState /orchestrate start 后 state.json stage=clarify。
func TestHandleStart_InitialState(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	output, err := cmd.HandleStart(context.Background(), "实现登录功能")
	if err != nil {
		t.Fatalf("HandleStart: %v", err)
	}

	// 验证输出包含 Task ID
	if !strings.Contains(output, "Task ID:") {
		t.Errorf("HandleStart 输出应包含 Task ID，got: %s", output)
	}

	// 从输出中提取 Task ID（格式 T-YYYYMMDD-HHMMSS）
	// 验证 state.json 已持久化且 stage=clarify
	tasks, err := cmd.scanAllTasks()
	if err != nil {
		t.Fatalf("scanAllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	state := tasks[0]
	if state.Stage != StageClarify {
		t.Errorf("state.Stage = %q, want %q (clarify)", state.Stage, StageClarify)
	}
	if state.TaskID == "" {
		t.Error("state.TaskID must not be empty")
	}
}

// TestHandleStart_EmptyRequirement_Rejected 空需求应拒绝。
func TestHandleStart_EmptyRequirement_Rejected(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	_, err := cmd.HandleStart(context.Background(), "")
	if err == nil {
		t.Fatal("空需求必须拒绝")
	}
}

// TestHandleResume_InterruptedTask 构造 interrupted 任务，resume 后能恢复到崩溃前 Stage。
func TestHandleResume_InterruptedTask(t *testing.T) {
	orch, baseDir := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	// 先创建一个任务（start）
	_, err := cmd.HandleStart(context.Background(), "测试需求")
	if err != nil {
		t.Fatalf("HandleStart: %v", err)
	}

	// 获取创建的 task
	tasks, err := cmd.scanAllTasks()
	if err != nil {
		t.Fatalf("scanAllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	taskID := tasks[0].TaskID

	// 模拟崩溃：将任务状态改为 interrupted（SaveState 内部拼接 orchestrator/）
	interruptedState := tasks[0]
	interruptedState.Stage = StageInterrupted
	if err := SaveState(baseDir, taskID, interruptedState); err != nil {
		t.Fatalf("SaveState interrupted: %v", err)
	}

	// 验证 state.json 已更新为 interrupted
	loaded, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state is nil")
	}
	if loaded.Stage != StageInterrupted {
		t.Fatalf("expected stage=interrupted after save, got %q", loaded.Stage)
	}

	// 调用 HandleResume 恢复
	output, err := cmd.HandleResume(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandleResume: %v", err)
	}

	// 验证输出包含恢复信息
	if !strings.Contains(output, "已恢复") {
		t.Errorf("HandleResume 输出应包含'已恢复'，got: %s", output)
	}
	if !strings.Contains(output, taskID) {
		t.Errorf("HandleResume 输出应包含 task ID %q，got: %s", taskID, output)
	}
}

// TestHandleAbort_MarksInterrupted /orchestrate abort 后任务标记为 interrupted，state.json 保留。
func TestHandleAbort_MarksInterrupted(t *testing.T) {
	orch, baseDir := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	// 先创建一个任务
	_, err := cmd.HandleStart(context.Background(), "测试需求")
	if err != nil {
		t.Fatalf("HandleStart: %v", err)
	}

	tasks, err := cmd.scanAllTasks()
	if err != nil {
		t.Fatalf("scanAllTasks: %v", err)
	}
	taskID := tasks[0].TaskID

	// 调用 HandleAbort
	output, err := cmd.HandleAbort(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandleAbort: %v", err)
	}

	// 验证输出
	if !strings.Contains(output, "已中止") {
		t.Errorf("HandleAbort 输出应包含'已中止'，got: %s", output)
	}

	// 验证 state.json 已更新为 interrupted
	state, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState after abort: %v", err)
	}
	if state == nil {
		t.Fatal("state.json should be preserved after abort")
	}
	if state.Stage != StageInterrupted {
		t.Errorf("state.Stage = %q, want 'interrupted'", state.Stage)
	}
}

// TestHandleStatus_ListsAllTasks /orchestrate status（无 task-id）列出所有任务。
func TestHandleStatus_ListsAllTasks(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	// 创建 2 个任务（间隔 1 秒以避免同一秒生成的 Task ID 冲突）
	_, _ = cmd.HandleStart(context.Background(), "需求1")
	time.Sleep(1100 * time.Millisecond) // Task ID 格式 T-YYYYMMDD-HHMMSS，需跨秒
	_, _ = cmd.HandleStart(context.Background(), "需求2")

	// 调用 HandleStatus（无 task-id）
	output, err := cmd.HandleStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("HandleStatus: %v", err)
	}

	// 验证输出包含 2 个任务
	if !strings.Contains(output, "共 2 个") {
		t.Errorf("HandleStatus 应显示 2 个任务，got: %s", output)
	}
}

// TestHandle_NoArgs_ReturnsHelp /orchestrate 无参数返回帮助。
func TestHandle_NoArgs_ReturnsHelp(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	output, err := cmd.Handle(context.Background(), "")
	if err != nil {
		t.Fatalf("Handle empty: %v", err)
	}

	if !strings.Contains(output, "子命令") {
		t.Errorf("Handle empty 应返回帮助（含'子命令'），got: %s", output)
	}
}

// TestOrchestrateCmd_Commands_ReturnsSubcommands Commands() 返回 8 个子命令（Phase 6 新增 clarify/plan/contract）。
func TestOrchestrateCmd_Commands_ReturnsSubcommands(t *testing.T) {
	orch, _ := makeTestOrchestrator(t)
	cmd := NewOrchestrateCmd(orch)

	subs := cmd.Commands()
	if len(subs) != 8 {
		t.Fatalf("expected 8 subcommands (Phase 6: 5+clarify/plan/contract), got %d", len(subs))
	}

	expected := map[string]bool{
		"start": false, "status": false, "list": false, "resume": false, "abort": false,
		"clarify": false, "plan": false, "contract": false, // Phase 6 新增
	}
	for _, s := range subs {
		if _, ok := expected[s.Name]; ok {
			expected[s.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("Commands() 缺少子命令 %q", name)
		}
	}
}

// === B-P0-4 反例测试（桶 X2 修复回核）===

// TestOrchestrator_NoSMField B-P0-4 反例：
// 验证 Orchestrator 结构体不再含 "sm" 死字段（B-P0-4 修复：删除 sm 死字段）。
// 验证目标（B-P0-4 修复）：sm 字段已从 Orchestrator struct 删除，避免死代码 + 内存浪费。
// 反例核心：若 sm 字段被重新加回，reflect 应检测到并测试失败。
func TestOrchestrator_NoSMField(t *testing.T) {
	t.Parallel()
	orchType := reflect.TypeOf(Orchestrator{})

	// 反例校验：sm 字段不应存在
	if _, ok := orchType.FieldByName("sm"); ok {
		t.Error("B-P0-4 反例：Orchestrator struct 不应含 'sm' 字段（死字段已删除）")
	}

	// 对照校验：已知字段应存在（确保 struct 没被整体破坏）
	expectedFields := []string{
		"baseDir", "router", "dispatcher", "scheduler", "budget",
		"quality", "profile", "capability",
		"clarifier", "planGenerator", "contractor", "askUser",
	}
	for _, name := range expectedFields {
		if _, ok := orchType.FieldByName(name); !ok {
			t.Errorf("B-P0-4 对照：Orchestrator struct 应含字段 %q（确保 struct 未被破坏）", name)
		}
	}
}

// TestOrchestrator_NoSMField_ByInstance B-P0-4 反例（实例视角）：
// 通过实例 reflect 验证 Orchestrator 不含 sm 字段，且字段总数符合预期（12 个，B-P0-4 删除 sm 后）。
// 反例核心：sm 字段被删除后字段数应减少 1，避免死字段残留。
func TestOrchestrator_NoSMField_ByInstance(t *testing.T) {
	t.Parallel()
	orch, _ := makeTestOrchestrator(t)
	v := reflect.ValueOf(orch).Elem()
	t.Logf("Orchestrator struct 字段数 = %d", v.NumField())

	// 反例校验：sm 字段不应存在
	zero := reflect.Value{}
	if f := v.FieldByName("sm"); f != zero {
		t.Error("B-P0-4 反例：Orchestrator 实例不应有 'sm' 字段（死字段已删除）")
	}

	// 字段数应为 12（baseDir/router/dispatcher/scheduler/budget/quality/profile/capability/
	// clarifier/planGenerator/contractor/askUser）
	// 若 sm 被加回，字段数会变为 13
	if v.NumField() != 12 {
		t.Errorf("B-P0-4 反例：Orchestrator struct 字段数应为 12（删除 sm 后），got %d", v.NumField())
	}
}
