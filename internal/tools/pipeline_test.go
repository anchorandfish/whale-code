package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/approval"
	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// Stage 1: SchemaValidateStage 测试
// ============================================================================

// TestSchemaValidateStage_Required tests returning error when required field is missing
func TestSchemaValidateStage_Required(t *testing.T) {
	r := NewRegistry()
	stage := &SchemaValidateStage{registry: r}

	// bash tool requires "command" 必填
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"timeout": 30}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Error("missing required field command should return Proceed=false")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestSchemaValidateStage_TypeCheck tests returning error when field type is wrong
func TestSchemaValidateStage_TypeCheck(t *testing.T) {
	r := NewRegistry()
	stage := &SchemaValidateStage{registry: r}

	// bash 的 command field expected string，传入 integer
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": 123}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Error("field type error should return Proceed=false")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestSchemaValidateStage_Pass tests valid input passes Schema validation
func TestSchemaValidateStage_Pass(t *testing.T) {
	r := NewRegistry()
	stage := &SchemaValidateStage{registry: r}

	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls -la"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if !sr.Proceed {
		t.Errorf("valid input应通过validation, but was rejected: %s", sr.Rejection)
	}
}

// ============================================================================
// Stage 2: CustomValidateStage 测试
// ============================================================================

// TestCustomValidateStage_PathTraversal 测试path traversal attack is blocked
func TestCustomValidateStage_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &CustomValidateStage{workDir: tmpDir}

	state := &PipelineState{FilePath: filepath.Join(tmpDir, "..", "..", "etc", "passwd")}
	call := Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s"}`, filepath.Join(tmpDir, "..", "..", "etc", "passwd")),
	}

	sr := stage.Execute(context.Background(), call, state)
	if sr.Proceed {
		t.Error("path traversal should be blocked")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestCustomValidateStage_WorkspaceBoundary 测试writing outside workspace path is blocked
func TestCustomValidateStage_WorkspaceBoundary(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &CustomValidateStage{workDir: tmpDir}

	// attempt to write to path outside workspace
	outsidePath := filepath.Join(filepath.VolumeName(tmpDir), "\\", "tmp", "outside.txt")
	state := &PipelineState{FilePath: outsidePath}
	call := Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s", "content": "hacked"}`, outsidePath),
	}

	sr := stage.Execute(context.Background(), call, state)
	if sr.Proceed {
		t.Error("out-of-workspace path should be blocked")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestCustomValidateStage_BinaryFile 测试binary file is blocked
func TestCustomValidateStage_BinaryFile(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &CustomValidateStage{workDir: tmpDir}

	// attempt to write .exe file（binary extension）
	binPath := filepath.Join(tmpDir, "malware.exe")
	state := &PipelineState{FilePath: binPath}
	call := Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s", "content": "binary"}`, binPath),
	}

	sr := stage.Execute(context.Background(), call, state)
	if sr.Proceed {
		t.Error("binary file should be blocked")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestCustomValidateStage_Pass tests valid input passes custom validation
func TestCustomValidateStage_Pass(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &CustomValidateStage{workDir: tmpDir}

	validPath := filepath.Join(tmpDir, "hello.txt")
	state := &PipelineState{FilePath: validPath}
	call := Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s", "content": "hello"}`, validPath),
	}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("valid input应通过validation, but was rejected: %s", sr.Rejection)
	}
}

// TestCustomValidateStage_BlocksSymlinkEscapeForNewFile 验证不存在的新文件也不能经祖先 symlink 逃逸工作区。
func TestCustomValidateStage_BlocksSymlinkEscapeForNewFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation may require elevated privileges")
	}
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	linkPath := filepath.Join(workDir, "outside-link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	filePath := filepath.Join(linkPath, "nested", "new.txt")
	stage := &CustomValidateStage{workDir: workDir}
	result := stage.Execute(context.Background(), Call{Name: "write_file"}, &PipelineState{FilePath: filePath})
	if result.Proceed {
		t.Fatal("symlink escape path should be rejected")
	}
	if !strings.Contains(result.Rejection, "workspace boundary check") {
		t.Fatalf("unexpected rejection: %s", result.Rejection)
	}
}

// ============================================================================
// Stage 3: PreHookStage 测试
// ============================================================================

// TestPreHookStage_Block tests pre-hook block action stops pipeline
func TestPreHookStage_Block(t *testing.T) {
	tmpDir := t.TempDir()
	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "bash", Action: hook.ActionBlock, Reason: "forbidden to execute bash"}},
		},
	})

	stage := &PreHookStage{hookMgr: hookMgr}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "rm -rf /"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Error("pre-hook block should stop pipeline")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestPreHookStage_Pass tests pre-hook pass action continues pipeline
func TestPreHookStage_Pass(t *testing.T) {
	tmpDir := t.TempDir()
	hookMgr := hook.NewManager(tmpDir)
	// no hook config, default pass
	hookMgr.SetConfig(hook.HookConfig{})

	stage := &PreHookStage{hookMgr: hookMgr}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if !sr.Proceed {
		t.Errorf("no matching hook should pass, but was rejected: %s", sr.Rejection)
	}
}

// ============================================================================
// Stage 4: PermissionStage 测试
// ============================================================================

// TestPermissionStage_Denied tests blocking pipeline when permission insufficient
func TestPermissionStage_Denied(t *testing.T) {
	// in read-only mode, bash is not allowed
	policy := permission.NewPolicy(permission.LevelReadOnly)
	stage := &PermissionStage{policy: policy}

	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Error("bash should be rejected in read-only mode")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestPermissionStage_Allowed tests continuing pipeline when permission allowed
func TestPermissionStage_Allowed(t *testing.T) {
	// in full access mode, all tools are allowed
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	stage := &PermissionStage{policy: policy}

	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if !sr.Proceed {
		t.Errorf("full access mode下 bash 应被允许，but was rejected: %s", sr.Rejection)
	}
}

// ============================================================================
// Stage 5: ExecuteStage 测试
// ============================================================================

// TestExecuteStage_Success 测试处理器执行成功返回结果
func TestExecuteStage_Success(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("exec_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "exec success", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	stage := &ExecuteStage{executor: e, registry: r}
	call := Call{
		ID:    "1",
		Name:  "exec_test_tool",
		Input: `{}`,
	}
	state := &PipelineState{}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("执行成功应continue pipeline，but was rejected: %s", sr.Rejection)
	}
	if state.Result == nil {
		t.Fatal("执行后 state.Result 不should be nil")
	}
	if state.Result.Output != "exec success" {
		t.Errorf("Output = %q, want %q", state.Result.Output, "exec success")
	}
}

// TestExecuteStage_Error 测试处理器执行错误时仍continue pipeline（错误信息记录在结果中）
func TestExecuteStage_Error(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("error_exec_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "", fmt.Errorf("execution failed")
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	stage := &ExecuteStage{executor: e, registry: r}
	call := Call{
		ID:    "1",
		Name:  "error_exec_tool",
		Input: `{}`,
	}
	state := &PipelineState{}

	sr := stage.Execute(context.Background(), call, state)
	// ExecuteStage 即使处理器报错也返回 Proceed=true（错误信息在 Result 中）
	if !sr.Proceed {
		t.Errorf("执行阶段即使处理器报错也应 Proceed=true，but was rejected: %s", sr.Rejection)
	}
	if state.Result == nil {
		t.Fatal("执行后 state.Result 不should be nil")
	}
	if !state.Result.IsError {
		t.Error("处理器报错时 Result.IsError 应为 true")
	}
}

// ============================================================================
// Stage 6: PostHookStage 测试
// ============================================================================

// TestPostHookStage_Modified 测试post-hook修改输出
func TestPostHookStage_Modified(t *testing.T) {
	tmpDir := t.TempDir()
	hookMgr := hook.NewManager(tmpDir)

	// 配置post-hook：匹配所有工具，执行命令并修改输出
	hookMgr.SetConfig(hook.HookConfig{
		PostToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "*", Action: hook.ActionAllow}, Command: "echo modified_output"},
		},
	})

	stage := &PostHookStage{hookMgr: hookMgr}
	call := Call{
		ID:    "1",
		Name:  "read_file",
		Input: `{"path": "/some/file"}`,
	}
	state := &PipelineState{
		Result: &Result{
			ID:     "1",
			Name:   "read_file",
			Output: "original output",
		},
	}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("post-hook allow 应continue pipeline，but was rejected: %s", sr.Rejection)
	}
	// post-hook执行了命令，输出被修改
	if state.Result == nil || state.Result.Output == "original output" {
		t.Error("post-hook应修改输出内容")
	}
}

// TestPostHookStage_Pass 测试post-hook直接通过（无matching hook）
func TestPostHookStage_Pass(t *testing.T) {
	tmpDir := t.TempDir()
	hookMgr := hook.NewManager(tmpDir)
	// 无hook config
	hookMgr.SetConfig(hook.HookConfig{})

	stage := &PostHookStage{hookMgr: hookMgr}
	call := Call{
		ID:    "1",
		Name:  "read_file",
		Input: `{"path": "/some/file"}`,
	}
	state := &PipelineState{
		Result: &Result{
			ID:     "1",
			Name:   "read_file",
			Output: "original output",
		},
	}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("no matching hook should pass, but was rejected: %s", sr.Rejection)
	}
	if state.Result.Output != "original output" {
		t.Errorf("no hook时输出不应被修改，got %q", state.Result.Output)
	}
}

// ============================================================================
// 完整管道integration test
// ============================================================================

// TestPipeline_FullFlow 测试完整七阶段管道执行（含 safety_check）
func TestPipeline_FullFlow(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义工具
	err := e.RegisterHandler("full_flow_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "full flow result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry 以通过 Schema 验证
	r.builtin["full_flow_tool"] = Spec{
		Name:        "full_flow_tool",
		Description: "完整流程test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	// 初始化管道
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "full_flow_tool",
		Input: `{"message":"test"}`,
	})

	if !pr.Proceed {
		t.Errorf("完整流程应通过，but was rejected: %s", pr.Rejection)
	}
	if pr.Result == nil {
		t.Fatal("结果不should be nil")
	}
	if pr.Result.Output != "full flow result" {
		t.Errorf("Output = %q, want %q", pr.Result.Output, "full flow result")
	}

	// 验证九个阶段都执行了（含 destructive_check 破坏性检查 + safety_check 安全路径检查阶段）
	// -R1 新增 dangerous_guard（危险档强制确认），标准阶段数从 8 增至 9。
	// 2026-10-04 新增 sandbox_precheck（沙箱硬拒绝前置，避免"批准了却被拒"），9 增至 10。
	expectedStages := []string{
		"schema_validate",
		"custom_validate",
		"pre_hook",
		"sandbox_precheck",
		"dangerous_guard",
		"destructive_check",
		"safety_check",
		"permission",
		"execute",
		"post_hook",
	}
	if len(pr.StageLogs) != len(expectedStages) {
		t.Fatalf("expected %d 个阶段日志，得到 %d 个", len(expectedStages), len(pr.StageLogs))
	}
	for i, expected := range expectedStages {
		if pr.StageLogs[i].Stage != expected {
			t.Errorf("StageLogs[%d].Stage = %q, want %q", i, pr.StageLogs[i].Stage, expected)
		}
		if pr.StageLogs[i].Status != "pass" {
			t.Errorf("StageLogs[%d].Status = %q, want %q", i, pr.StageLogs[i].Status, "pass")
		}
	}
}

// TestPipeline_ShortCircuit 测试管道在某一阶段短路（拒绝后不再执行后续阶段）
func TestPipeline_ShortCircuit(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义工具
	err := e.RegisterHandler("short_circuit_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "should not reach here", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry
	r.builtin["short_circuit_tool"] = Spec{
		Name:        "short_circuit_tool",
		Description: "短路test tool",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"data"},
			"properties": map[string]interface{}{
				"data": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	}

	// 使用只读权限策略，该工具需要 WorkspaceWrite 权限，应在权限阶段rejected
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelReadOnly)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "short_circuit_tool",
		Input: `{"data":"test"}`,
	})

	if pr.Proceed {
		t.Error("read-only mode下 WorkspaceWrite 工具should be rejected")
	}
	if pr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}

	// 验证管道短路：只执行到 permission 为止
	// （schema_validate, custom_validate, pre_hook, sandbox_precheck, dangerous_guard,
	// destructive_check, safety_check, permission —— -R1 后 permission 位于第 7 个；
	// 2026-10-04 新增 sandbox_precheck 后排第 8 个）
	if len(pr.StageLogs) > 8 {
		t.Errorf("管道应在权限阶段短路，但执行了 %d 个阶段", len(pr.StageLogs))
	}
	if len(pr.StageLogs) >= 8 {
		lastStage := pr.StageLogs[len(pr.StageLogs)-1]
		if lastStage.Stage != "permission" {
			t.Errorf("最后一个阶段应为 permission，actual为 %q", lastStage.Stage)
		}
		if lastStage.Status != "reject" {
			t.Errorf("权限阶段状态应为 reject，actual为 %q", lastStage.Status)
		}
	}
}

// ============================================================================
// 边界情况测试
// ============================================================================

// TestSchemaValidateStage_UnregisteredTool 测试未注册工具 fail-closed 拒绝（T9）
func TestSchemaValidateStage_UnregisteredTool(t *testing.T) {
	r := NewRegistry()
	stage := &SchemaValidateStage{registry: r}

	call := Call{
		ID:    "1",
		Name:  "totally_unknown_tool",
		Input: `{"anything": "goes"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Errorf("未注册工具应 fail-closed 拒绝, but was allowed")
	}
}

// TestSchemaValidateStage_NoInputSchema 测试无 InputSchema 时跳过验证
func TestSchemaValidateStage_NoInputSchema(t *testing.T) {
	r := NewRegistry()
	// 注册一个没有 InputSchema 的工具
	r.builtin["no_schema_tool"] = Spec{
		Name:        "no_schema_tool",
		Description: "无 Schema 工具",
	}

	stage := &SchemaValidateStage{registry: r}
	call := Call{
		ID:    "1",
		Name:  "no_schema_tool",
		Input: `{"anything": "goes"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if !sr.Proceed {
		t.Errorf("无 InputSchema 应跳过validation, but was rejected: %s", sr.Rejection)
	}
}

// TestSchemaValidateStage_InvalidJSON test input不是有效 JSON 对象时返回错误
func TestSchemaValidateStage_InvalidJSON(t *testing.T) {
	r := NewRegistry()
	stage := &SchemaValidateStage{registry: r}

	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `not a json`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Error("无效 JSON 输入should be rejected")
	}
}

// TestCustomValidateStage_NoFilePath 测试无文件路径时跳过自定义验证
func TestCustomValidateStage_NoFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &CustomValidateStage{workDir: tmpDir}

	// 无文件路径（如 bash 工具）
	state := &PipelineState{FilePath: ""}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("无文件路径应跳过自定义validation, but was rejected: %s", sr.Rejection)
	}
}

// TestCustomValidateStage_ReadToolOutsideWorkspace 测试读取工具不受工作区边界限制
func TestCustomValidateStage_ReadToolOutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &CustomValidateStage{workDir: tmpDir}

	// read_file 不受工作区边界限制（只有 write_file 和 edit_file 受限）
	outsidePath := filepath.Join(filepath.VolumeName(tmpDir), "\\", "tmp", "outside.txt")
	state := &PipelineState{FilePath: outsidePath}
	call := Call{
		ID:    "1",
		Name:  "read_file",
		Input: fmt.Sprintf(`{"path": "%s"}`, outsidePath),
	}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("读取工具不应受工作区边界限制，but was rejected: %s", sr.Rejection)
	}
}

// TestPreHookStage_NilManager 测试no hook管理器时直接通过
func TestPreHookStage_NilManager(t *testing.T) {
	stage := &PreHookStage{hookMgr: nil}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if !sr.Proceed {
		t.Errorf("no hook管理器应直接通过，but was rejected: %s", sr.Rejection)
	}
}

// TestPermissionStage_NilPolicy 测试无权限策略时 fail-closed 拒绝（C19）
func TestPermissionStage_NilPolicy(t *testing.T) {
	stage := &PermissionStage{policy: nil}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Errorf("无权限策略应 fail-closed 拒绝, but was allowed")
	}
}

// TestPostHookStage_NilManager 测试no hook管理器时post-hook直接通过
func TestPostHookStage_NilManager(t *testing.T) {
	stage := &PostHookStage{hookMgr: nil}
	call := Call{
		ID:    "1",
		Name:  "read_file",
		Input: `{"path": "/some/file"}`,
	}
	state := &PipelineState{
		Result: &Result{
			ID:     "1",
			Name:   "read_file",
			Output: "original",
		},
	}

	sr := stage.Execute(context.Background(), call, state)
	if !sr.Proceed {
		t.Errorf("no hook管理器应直接通过，but was rejected: %s", sr.Rejection)
	}
	if state.Result.Output != "original" {
		t.Errorf("no hook管理器时输出不应被修改，got %q", state.Result.Output)
	}
}

// TestExecuteStage_NilExecutor test execution器未初始化时返回错误
func TestExecuteStage_NilExecutor(t *testing.T) {
	stage := &ExecuteStage{executor: nil, registry: nil}
	call := Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "ls"}`,
	}

	sr := stage.Execute(context.Background(), call, &PipelineState{})
	if sr.Proceed {
		t.Error("执行器未初始化should return Proceed=false")
	}
}

// TestPipeline_NeedConfirm 测试管道中 NeedConfirm 标志的传递
// 当工具需要确认时，管道应暂停执行，返回 Proceed=false, NeedConfirm=true
func TestPipeline_NeedConfirm(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("confirm_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "confirmed result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	r.builtin["confirm_tool"] = Spec{
		Name:        "confirm_tool",
		Description: "需要确认的工具",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
		RequiredPermission: LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	}

	// WorkspaceWrite 模式下，WorkspaceWrite 权限工具需要确认
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "confirm_tool",
		Input: `{}`,
	})

	// 管道应暂停执行，等待User确认
	if pr.Proceed {
		t.Error("需要确认的工具不应直接执行，Proceed 应为 false")
	}
	if !pr.NeedConfirm {
		t.Error("NeedConfirm 应为 true")
	}
	// 工具尚未执行，结果should be nil
	if pr.Result != nil {
		t.Error("需要确认时结果should be nil，工具尚未执行")
	}
	// 验证权限阶段日志中包含确认信息
	var permLog *StageLog
	for i := range pr.StageLogs {
		if pr.StageLogs[i].Stage == "permission" {
			permLog = &pr.StageLogs[i]
			break
		}
	}
	if permLog == nil {
		t.Fatal("应有权限阶段日志")
	}
	if permLog.Status != "pass" {
		t.Errorf("权限阶段状态应为 pass，actual为 %q", permLog.Status)
	}
}

// TestPostHookStage_Block 测试post-hook block 动作stop pipeline
func TestPostHookStage_Block(t *testing.T) {
	tmpDir := t.TempDir()
	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PostToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "write_file", Action: hook.ActionBlock, Reason: "禁止写入后操作"}},
		},
	})

	stage := &PostHookStage{hookMgr: hookMgr}
	call := Call{
		ID:    "1",
		Name:  "write_file",
		Input: `{"path": "/some/file", "content": "test"}`,
	}
	state := &PipelineState{
		Result: &Result{
			ID:     "1",
			Name:   "write_file",
			Output: "file written",
		},
	}

	sr := stage.Execute(context.Background(), call, state)
	if sr.Proceed {
		t.Error("post-hook block 应stop pipeline")
	}
	if sr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestPipeline_SchemaRejectShortCircuit 测试 Schema 验证阶段拒绝导致短路
func TestPipeline_SchemaRejectShortCircuit(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// bash tool requires command 必填，缺少 command 应在 Schema 阶段rejected
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"timeout": 30}`,
	})

	if pr.Proceed {
		t.Error("missing required field应在 Schema 阶段rejected")
	}
	// 应该只在第一个阶段就短路
	if len(pr.StageLogs) < 1 {
		t.Fatal("至少应有一个阶段日志")
	}
	if pr.StageLogs[0].Stage != "schema_validate" {
		t.Errorf("第一个阶段应为 schema_validate，actual为 %q", pr.StageLogs[0].Stage)
	}
	if pr.StageLogs[0].Status != "reject" {
		t.Errorf("schema_validate 状态应为 reject，actual为 %q", pr.StageLogs[0].Status)
	}
	// 短路：不应有后续阶段
	if len(pr.StageLogs) > 1 {
		t.Errorf("Schema 验证失败应短路，但执行了 %d 个阶段", len(pr.StageLogs))
	}
}

// TestCheckType 测试类型检查helper functions
func TestCheckType(t *testing.T) {
	tests := []struct {
		val      interface{}
		expected string
		want     bool
	}{
		{"hello", "string", true},
		{123.0, "integer", true},
		{12.5, "integer", false},
		{12.5, "number", true},
		{true, "boolean", true},
		{[]interface{}{1, 2}, "array", true},
		{map[string]interface{}{"key": "val"}, "object", true},
		{"hello", "number", false},
		{42, "unknown_type", true}, // 未知类型默认通过
	}

	for _, tt := range tests {
		got := checkType(tt.val, tt.expected)
		if got != tt.want {
			t.Errorf("checkType(%v, %q) = %v, want %v", tt.val, tt.expected, got, tt.want)
		}
	}
}

// TestActualType test actual类型名称helper functions
func TestActualType(t *testing.T) {
	tests := []struct {
		val  interface{}
		want string
	}{
		{"hello", "string"},
		{42.5, "number"},
		{true, "boolean"},
		{[]interface{}{1}, "array"},
		{map[string]interface{}{}, "object"},
		{nil, "null"},
	}

	for _, tt := range tests {
		got := actualType(tt.val)
		if got != tt.want {
			t.Errorf("actualType(%v) = %q, want %q", tt.val, got, tt.want)
		}
	}
}

// TestIsWriteTool 测试写入工具判断函数
func TestIsWriteTool(t *testing.T) {
	if !isWriteTool("write_file") {
		t.Error("write_file 应为写入工具")
	}
	if !isWriteTool("edit_file") {
		t.Error("edit_file 应为写入工具")
	}
	if isWriteTool("read_file") {
		t.Error("read_file 不应为写入工具")
	}
	if isWriteTool("bash") {
		t.Error("bash 不应为写入工具")
	}
}

// TestPipelineExtractPath 测试从工具输入中提取路径
func TestPipelineExtractPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"path": "/tmp/test.txt"}`, "/tmp/test.txt"},
		{`{"command": "ls"}`, ""},
		{`not json`, ""},
		{`{}`, ""},
	}

	for _, tt := range tests {
		got := pipelineExtractPath(tt.input)
		if got != tt.want {
			t.Errorf("pipelineExtractPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ============================================================================
// 新增管道integration test
// ============================================================================

// TestPipeline_SchemaValidate_Pass tests valid input passes Schema validation，Proceed=true
func TestPipeline_SchemaValidate_Pass(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("schema_pass_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	r.builtin["schema_pass_tool"] = Spec{
		Name:        "schema_pass_tool",
		Description: "test tool",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"data"},
			"properties": map[string]interface{}{
				"data": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "schema_pass_tool",
		Input: `{"data":"hello"}`,
	})

	if !pr.Proceed {
		t.Errorf("valid input应通过 Schema validation, but was rejected: %s", pr.Rejection)
	}
}

// TestPipeline_SchemaValidate_MissingRequired 测试missing required field时 Proceed=false
func TestPipeline_SchemaValidate_MissingRequired(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// bash tool requires command 必填
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"timeout": 30}`,
	})

	if pr.Proceed {
		t.Error("missing required fieldshould be rejected")
	}
}

// TestPipeline_SchemaValidate_WrongType 测试field type error时验证失败
func TestPipeline_SchemaValidate_WrongType(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// bash 的 command field expected string，传入 integer
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": 123}`,
	})

	if pr.Proceed {
		t.Error("field type errorshould be rejected")
	}
}

// TestPipeline_CustomValidate_PathTraversal 测试path traversal检测，Proceed=false
func TestPipeline_CustomValidate_PathTraversal(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s", "content": "hack"}`, filepath.Join(tmpDir, "..", "..", "etc", "passwd")),
	})

	if pr.Proceed {
		t.Error("path traversal should be blocked")
	}
}

// TestPipeline_CustomValidate_WorkspaceBoundary 测试写入outside workspace路径，Proceed=false
func TestPipeline_CustomValidate_WorkspaceBoundary(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	outsidePath := filepath.Join(filepath.VolumeName(tmpDir), "\\", "tmp", "outside.txt")
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s", "content": "hack"}`, outsidePath),
	})

	if pr.Proceed {
		t.Error("out-of-workspace path should be blocked")
	}
}

// TestPipeline_CustomValidate_BinaryFile 测试二进制文件扩展名被阻止
func TestPipeline_CustomValidate_BinaryFile(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	binPath := filepath.Join(tmpDir, "malware.exe")
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path": "%s", "content": "binary"}`, binPath),
	})

	if pr.Proceed {
		t.Error("binary file should be blocked")
	}
}

// TestPipeline_PreHook_Block 测试 PreToolUse 钩子 Action=block 阻止工具执行
func TestPipeline_PreHook_Block(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "bash", Action: hook.ActionBlock, Reason: "forbidden to execute bash"}},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "echo hello"}`,
	})

	if pr.Proceed {
		t.Error("pre-hook block 应阻止工具执行")
	}
}

// TestPipeline_PreHook_Ask 测试 PreToolUse 钩子 Action=ask，阶段日志标记需要确认
func TestPipeline_PreHook_Ask(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "bash", Action: hook.ActionAsk, Reason: "需要确认才能执行 bash"}},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "echo hello"}`,
	})

	// ask 动作允许继续执行
	if !pr.Proceed {
		t.Errorf("ask 动作应允许继续执行，but was rejected: %s", pr.Rejection)
	}

	// 验证 pre_hook 阶段日志包含确认信息
	var preHookLog *StageLog
	for i := range pr.StageLogs {
		if pr.StageLogs[i].Stage == "pre_hook" {
			preHookLog = &pr.StageLogs[i]
			break
		}
	}
	if preHookLog == nil {
		t.Fatal("应有 pre_hook 阶段日志")
	}
	if preHookLog.Status != "pass" {
		t.Errorf("pre_hook 阶段应通过，状态: %q", preHookLog.Status)
	}
}

// TestPipeline_Permission_Allowed 测试 danger-full-access 模式下所有工具被允许
func TestPipeline_Permission_Allowed(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "echo hello"}`,
	})

	// 权限阶段应通过（后续可能因 bash 执行失败，但权限不应阻止）
	var permLog *StageLog
	for i := range pr.StageLogs {
		if pr.StageLogs[i].Stage == "permission" {
			permLog = &pr.StageLogs[i]
			break
		}
	}
	if permLog == nil {
		t.Fatal("应有权限阶段日志")
	}
	if permLog.Status != "pass" {
		t.Errorf("danger-full-access 模式下权限应通过，状态: %q", permLog.Status)
	}
}

// TestPipeline_Permission_NeedConfirm 测试 workspace-write 模式下 bash 需要确认
// 当工具需要确认时，管道应暂停执行，返回 Proceed=false, NeedConfirm=true
// 注意：使用非破坏性命令 ls -la（而非 rm -rf /），避免触发 DestructiveCheckStage
// （桶 X6 P-4 新增的破坏性检查阶段会在 PermissionStage 之前设置 NeedConfirm）
func TestPipeline_Permission_NeedConfirm(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"command": "mkdir -p nc_probe"}`,
	})

	// workspace-write 模式下，bash 的权限级别为 LevelWorkspaceWrite，
	// required <= LevelWorkspaceWrite，所以 allowed=true
	// 但 bash 不在 allowedTools 中，且不在 memory 中，
	// 所以 needConfirm = (required > LevelReadOnly) = true
	// 管道应暂停执行，等待User确认
	if pr.Proceed {
		t.Error("需要确认的工具不应直接执行，Proceed 应为 false")
	}
	if !pr.NeedConfirm {
		t.Error("NeedConfirm 应为 true")
	}

	// 验证权限阶段日志
	var permLog *StageLog
	for i := range pr.StageLogs {
		if pr.StageLogs[i].Stage == "permission" {
			permLog = &pr.StageLogs[i]
			break
		}
	}
	if permLog == nil {
		t.Fatal("应有权限阶段日志")
	}
	if permLog.Status != "pass" {
		t.Errorf("权限阶段应通过（需确认），状态: %q", permLog.Status)
	}
	// 验证权限阶段消息包含确认信息（兼容中英文实现：确认 / confirmation）
	msgLower := strings.ToLower(permLog.Message)
	if !strings.Contains(permLog.Message, "确认") && !strings.Contains(msgLower, "confirmation") {
		t.Errorf("权限阶段消息应提及确认，actual: %q", permLog.Message)
	}
}

// TestPipeline_PostHook_Modified 测试 PostToolUse 钩子修改 Result 输出

// TestPipeline_PreHookBlockShortCircuit 测试管道在阶段3（pre-hookblock）短路，
// 验证管道不会到达执行阶段，且阶段日志正确记录短路位置
func TestPipeline_PreHookBlockShortCircuit(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义工具，用于验证执行阶段不会被触发
	executed := false
	err := e.RegisterHandler("prehook_block_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		executed = true
		return "should not reach here", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry 以通过 Schema 验证
	r.builtin["prehook_block_tool"] = Spec{
		Name:        "prehook_block_tool",
		Description: "pre-hook短路test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"data": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelDangerFullAccess,
		ConcurrencySafe:    true,
	}

	// 配置pre-hook：匹配 prehook_block_tool，动作为 block
	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "prehook_block_tool", Action: hook.ActionBlock, Reason: "pre-hook拦截：forbidden to execute此工具"}},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "prehook_block_tool",
		Input: `{"data":"test"}`,
	})

	// 验证管道被阻止
	if pr.Proceed {
		t.Error("pre-hook block should stop pipeline，Proceed 应为 false")
	}

	// 验证rejection reason包含钩子拦截信息
	if pr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
	if !strings.Contains(pr.Rejection, "pre-hook拦截") {
		t.Errorf("rejection reasonshould contain 'pre-hook拦截'，actual: %q", pr.Rejection)
	}

	// 验证管道短路：只执行了前 3 个阶段（schema_validate, custom_validate, pre_hook）
	if len(pr.StageLogs) > 3 {
		t.Errorf("管道应在pre-hook阶段短路，但执行了 %d 个阶段", len(pr.StageLogs))
	}

	// 验证最后一个阶段是 pre_hook 且状态为 reject
	if len(pr.StageLogs) >= 3 {
		lastStage := pr.StageLogs[len(pr.StageLogs)-1]
		if lastStage.Stage != "pre_hook" {
			t.Errorf("最后一个阶段应为 pre_hook，actual为 %q", lastStage.Stage)
		}
		if lastStage.Status != "reject" {
			t.Errorf("pre-hook阶段状态应为 reject，actual为 %q", lastStage.Status)
		}
	}

	// 验证前两个阶段通过
	if len(pr.StageLogs) >= 2 {
		if pr.StageLogs[0].Stage != "schema_validate" || pr.StageLogs[0].Status != "pass" {
			t.Errorf("schema_validate 阶段应通过，actual: stage=%q status=%q", pr.StageLogs[0].Stage, pr.StageLogs[0].Status)
		}
		if pr.StageLogs[1].Stage != "custom_validate" || pr.StageLogs[1].Status != "pass" {
			t.Errorf("custom_validate 阶段应通过，actual: stage=%q status=%q", pr.StageLogs[1].Stage, pr.StageLogs[1].Status)
		}
	}

	// 验证执行阶段未被触发
	if executed {
		t.Error("pre-hook短路后，工具处理器不应被执行")
	}

	// 验证没有 execute 和 post_hook 阶段日志
	for _, log := range pr.StageLogs {
		if log.Stage == "execute" {
			t.Error("pre-hook短路后不应有 execute 阶段日志")
		}
		if log.Stage == "post_hook" {
			t.Error("pre-hook短路后不应有 post_hook 阶段日志")
		}
	}
}

// TestPipeline_PostHook_Modified 测试 PostToolUse 钩子修改 Result 输出
func TestPipeline_PostHook_Modified(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("posthook_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "original output", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	r.builtin["posthook_test_tool"] = Spec{
		Name:        "posthook_test_tool",
		Description: "post-hooktest tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PostToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "*", Action: hook.ActionAllow}, Command: "echo modified_output"},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "posthook_test_tool",
		Input: `{}`,
	})

	if !pr.Proceed {
		t.Errorf("post-hook allow 应继续，but was rejected: %s", pr.Rejection)
	}
	if pr.Result == nil {
		t.Fatal("结果不should be nil")
	}
	// post-hook执行了命令，输出应被修改
	if pr.Result.Output == "original output" {
		t.Error("post-hook应修改输出内容")
	}
}

// TestPipeline_SchemaValidationFailure_ThenCorrection 测试 Schema 验证失败时返回可纠正的错误信息
// 当工具输入missing required field时，管道应在 schema_validate 阶段短路，
// 返回的 Rejection 消息应足够描述性，使模型能够据此修正输入
func TestPipeline_SchemaValidationFailure_ThenCorrection(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("validation_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "should not reach here", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry，定义带有必填字段的 Schema
	r.builtin["validation_test_tool"] = Spec{
		Name:        "validation_test_tool",
		Description: "验证test tool",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"path", "content"},
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string", "description": "文件路径"},
				"content": map[string]interface{}{"type": "string", "description": "文件内容"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// 第一次调用：missing required field "content"
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "validation_test_tool",
		Input: `{"path": "/tmp/test.txt"}`,
	})

	// 验证管道在 Schema 阶段rejected
	if pr.Proceed {
		t.Error("missing required field应被 Schema 验证拒绝")
	}

	// 验证 Rejection 消息描述了缺失的字段，模型可据此修正
	if pr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
	if !strings.Contains(pr.Rejection, "content") {
		t.Errorf("rejection reasonshould contain缺失字段名 'content'，actual: %q", pr.Rejection)
	}
	if !strings.Contains(pr.Rejection, "必填") && !strings.Contains(pr.Rejection, "缺少") {
		t.Errorf("rejection reasonshould contain描述性关键词（必填/缺少），actual: %q", pr.Rejection)
	}

	// 验证短路：只有 schema_validate 阶段执行
	if len(pr.StageLogs) != 1 {
		t.Errorf("Schema 验证失败应短路，但执行了 %d 个阶段", len(pr.StageLogs))
	}
	if len(pr.StageLogs) > 0 && pr.StageLogs[0].Stage != "schema_validate" {
		t.Errorf("第一个阶段应为 schema_validate，actual为 %q", pr.StageLogs[0].Stage)
	}
	if len(pr.StageLogs) > 0 && pr.StageLogs[0].Status != "reject" {
		t.Errorf("schema_validate 状态应为 reject，actual为 %q", pr.StageLogs[0].Status)
	}

	// mock模型修正输入后重新调用：提供完整输入
	pr2 := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "2",
		Name:  "validation_test_tool",
		Input: `{"path": "/tmp/test.txt", "content": "hello world"}`,
	})

	// 修正后应通过 Schema 验证
	if !pr2.Proceed {
		t.Errorf("修正输入后应通过validation, but was rejected: %s", pr2.Rejection)
	}
	if pr2.Result == nil {
		t.Fatal("修正后结果不should be nil")
	}
	if pr2.Result.Output != "should not reach here" {
		t.Errorf("修正后 Output = %q, want %q", pr2.Result.Output, "should not reach here")
	}
}

// ============================================================================
// D3: NeedConfirm 对接测试
// ============================================================================

// TestPipelineConfirmCallback 测试当 Pipeline 返回 NeedConfirm=true 时 OnConfirm 回调被调用
func TestPipelineConfirmCallback(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	callbackCalled := false
	var callbackToolName string
	e.OnConfirm = func(toolName, input string) bool {
		callbackCalled = true
		callbackToolName = toolName
		return true // User确认
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	call := Call{
		ID:    "test-cb-1",
		Name:  "bash",
		Input: `{"command": "mkdir -p cb_probe"}`,
	}

	result := e.ExecuteWithConfirm(context.Background(), call)

	// OnConfirm 回调应被调用
	if !callbackCalled {
		t.Error("OnConfirm 回调应被调用")
	}
	if callbackToolName != "bash" {
		t.Errorf("回调中工具名 = %q, want %q", callbackToolName, "bash")
	}

	// User确认后，工具应被执行
	if !result.Proceed {
		t.Errorf("User确认后应继续执行，but was rejected: %s", result.Rejection)
	}
	if result.Result == nil {
		t.Fatal("User确认后结果不should be nil")
	}
}

// TestConfirmDenied 测试User拒绝确认时工具不被执行
func TestConfirmDenied(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	e.OnConfirm = func(toolName, input string) bool {
		return false // User拒绝
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	call := Call{
		ID:    "test-deny-1",
		Name:  "bash",
		Input: `{"command": "mkdir -p dn_probe"}`,
	}

	// ExecuteWithConfirm 应在User拒绝时返回拒绝结果
	result := e.ExecuteWithConfirm(context.Background(), call)
	if result.Proceed {
		t.Error("User拒绝确认时不应继续执行")
	}
	if result.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestConfirmAccepted 测试User接受确认时工具正常执行
func TestConfirmAccepted(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	e.OnConfirm = func(toolName, input string) bool {
		return true // User接受
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	call := Call{
		ID:    "test-accept-1",
		Name:  "bash",
		Input: `{"command": "mkdir -p ac_probe"}`,
	}

	// ExecuteWithConfirm 应在User接受时正常执行
	result := e.ExecuteWithConfirm(context.Background(), call)
	if !result.Proceed {
		t.Errorf("User接受确认后应继续执行，but was rejected: %s", result.Rejection)
	}
	if result.Result == nil {
		t.Fatal("User接受确认后结果不should be nil")
	}
}

// TestPipelineApprovedCallSkipsOnlyPrompt verifies that an approved call can
// re-enter the full pipeline without asking the same confirmation again.
//
// ⚠️ 2026-09-15 有意翻转（记录在案，勿"修回"）：
// 本用例原先用**工作区内的 write_file** 当「需要确认」的 fixture，断言
// "workspace-write file modification should require one confirmation"。
// 该断言已随「工作区内的写类调用免问」一并推翻（新规格见
// pipeline_ww_write_noconfirm_test.go，对齐业内既有实现 assess_patch_safety）。
// 为**保住本用例的判别力**（证明 Approved / Remember 真的免掉了那次询问，
// 而不是本来就没人问），fixture 改为 bash：它不在写类工具表内，在所有档位下仍需确认。
func TestPipelineApprovedCallSkipsOnlyPrompt(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hook.NewManager(tmpDir), policy, tmpDir)

	// 新规格：工作区内的写入不再需要确认。
	writeInput := fmt.Sprintf(`{"path":%q,"content":"AFTER"}`, filepath.Join(tmpDir, "permission-write-marker.txt"))
	if pr := e.ExecuteWithPipeline(context.Background(), Call{ID: "write-1", Name: "write_file", Input: writeInput}); pr.NeedConfirm {
		t.Fatalf("工作区内的 write_file 不应弹确认（2026-09-15 新规格）: rejection=%q logs=%v", pr.Rejection, pr.StageLogs)
	}

	input := `{"command":"printf approved"}`
	first := e.ExecuteWithPipeline(context.Background(), Call{ID: "bash-1", Name: "bash", Input: input})
	if !first.NeedConfirm {
		t.Fatal("baseline: bash 在 workspace-write 下应请求确认")
	}

	second := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "bash-1", Name: "bash", Input: input, Approved: true,
	})
	if second.NeedConfirm || !second.Proceed {
		t.Fatalf("approved call should continue without a second prompt: proceed=%v needConfirm=%v rejection=%q", second.Proceed, second.NeedConfirm, second.Rejection)
	}
	if second.Result == nil || !strings.Contains(second.Result.Output, "approved") {
		t.Fatalf("approved call should execute once: result=%v rejection=%q logs=%v", second.Result, second.Rejection, second.StageLogs)
	}
	policy.Remember("bash", input, true)
	remembered := e.ExecuteWithPipeline(context.Background(), Call{ID: "bash-2", Name: "bash", Input: input})
	if remembered.NeedConfirm || !remembered.Proceed {
		t.Fatalf("session approval should suppress the next identical prompt: proceed=%v needConfirm=%v rejection=%q", remembered.Proceed, remembered.NeedConfirm, remembered.Rejection)
	}
}

// TestApprovedCallDoesNotBypassReadOnly verifies that approval skips only the
// interactive prompt and cannot override the active permission mode.
func TestApprovedCallDoesNotBypassReadOnly(t *testing.T) {
	policy := permission.NewPolicy(permission.LevelReadOnly)
	stage := &PermissionStage{policy: policy}
	state := &PipelineState{NeedConfirm: true}
	sr := stage.Execute(context.Background(), Call{
		Name: "write_file", Input: `{"path":"marker.txt","content":"AFTER"}`, Approved: true,
	}, state)
	if sr.Proceed {
		t.Fatal("approved write must still be denied in read-only mode")
	}
	if sr.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("read-only denial kind = %q, want %q", sr.FailureKind, FailureKindPolicyDenied)
	}
}

func TestReadOnlyDenialIsNotMisreportedAsExplicitRule(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	e := NewExecutor(r, workDir)
	e.InitPipeline(hook.NewManager(workDir), permission.NewPolicy(permission.LevelReadOnly), workDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "read-only-denial",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"CHANGED"}`, filepath.Join(workDir, "approval-marker.txt")),
	})

	if pr.Proceed || pr.NeedConfirm {
		t.Fatalf("read-only write should be denied directly: proceed=%v needConfirm=%v", pr.Proceed, pr.NeedConfirm)
	}
	if pr.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("pipeline failure kind = %q, want %q", pr.FailureKind, FailureKindPolicyDenied)
	}
	if !strings.Contains(pr.Rejection, "current permission mode") {
		t.Fatalf("read-only denial should name the permission mode, got %q", pr.Rejection)
	}
	if strings.Contains(pr.Rejection, "explicit deny rule") {
		t.Fatalf("read-only denial must not be misreported as an explicit rule: %q", pr.Rejection)
	}
}

func TestExplicitDenyRuleIsReportedAsRule(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	if err := policy.AddRule(permission.PermissionRule{
		ToolName: "write_file",
		Behavior: permission.RuleDeny,
		Source:   permission.SourceSession,
	}); err != nil {
		t.Fatalf("add deny rule: %v", err)
	}
	e := NewExecutor(r, workDir)
	e.InitPipeline(hook.NewManager(workDir), policy, workDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "explicit-rule-denial",
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"CHANGED"}`, filepath.Join(workDir, "approval-marker.txt")),
	})

	if pr.Proceed || pr.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("explicit deny rule should produce policy denial: %+v", pr)
	}
	if !strings.Contains(pr.Rejection, "explicit deny rule") {
		t.Fatalf("explicit rule denial should name the rule, got %q", pr.Rejection)
	}
}

// TestExecuteWithConfirm_NoPipeline 测试 Pipeline 未初始化时 ExecuteWithConfirm 降级执行
func TestExecuteWithConfirm_NoPipeline(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)
	// 不初始化 Pipeline

	callbackCalled := false
	e.OnConfirm = func(toolName, input string) bool {
		callbackCalled = true
		return true
	}

	call := Call{
		ID:    "test-nopipe-1",
		Name:  "read_file",
		Input: `{"path": "nonexistent.txt"}`,
	}

	result := e.ExecuteWithConfirm(context.Background(), call)

	// Pipeline 未初始化时，OnConfirm 不应被调用
	if callbackCalled {
		t.Error("Pipeline 未初始化时 OnConfirm 不应被调用")
	}
	if !result.Proceed {
		t.Errorf("Pipeline 未初始化时应直接执行，but was rejected: %s", result.Rejection)
	}
}

// TestExecuteWithConfirm_NoConfirmNeeded 测试不需要确认的工具直接执行
func TestExecuteWithConfirm_NoConfirmNeeded(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	callbackCalled := false
	e.OnConfirm = func(toolName, input string) bool {
		callbackCalled = true
		return true
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	call := Call{
		ID:    "test-noconfirm-1",
		Name:  "read_file",
		Input: `{"path": "nonexistent.txt"}`,
	}

	// read_file 在 danger-full-access 模式下不需要确认
	result := e.ExecuteWithConfirm(context.Background(), call)
	if callbackCalled {
		t.Error("不需要确认时 OnConfirm 不应被调用")
	}
	if !result.Proceed {
		t.Errorf("不需要确认时应直接执行，but was rejected: %s", result.Rejection)
	}
}

// TestExecuteWithConfirm_NoOnConfirm 测试 NeedConfirm 但无 OnConfirm 回调时返回确认请求
func TestExecuteWithConfirm_NoOnConfirm(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)
	// 不设置 OnConfirm 回调

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	call := Call{
		ID:    "test-nocallback-1",
		Name:  "bash",
		Input: `{"command": "mkdir -p noc_probe"}`,
	}

	// 没有 OnConfirm 回调时，should return NeedConfirm=true 但不执行
	result := e.ExecuteWithConfirm(context.Background(), call)
	if result.Proceed {
		t.Error("没有 OnConfirm 回调时不应自动继续执行")
	}
	if !result.NeedConfirm {
		t.Error("NeedConfirm 应为 true")
	}
}

// TestConfirmDenied_RejectionMessage 测试拒绝确认时返回的拒绝消息
func TestConfirmDenied_RejectionMessage(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	e.OnConfirm = func(toolName, input string) bool {
		return false
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, tmpDir)

	call := Call{
		ID:    "test-msg-1",
		Name:  "bash",
		Input: `{"command": "mkdir -p msg_probe"}`,
	}

	result := e.ExecuteWithConfirm(context.Background(), call)
	if !strings.Contains(result.Rejection, "拒绝") {
		t.Errorf("拒绝消息should contain '拒绝'，actual: %q", result.Rejection)
	}
}

// ============================================================================
// B3: on-failure 审批接入测试（CheckWithResult 在 pipeline on-failure 路径接入）
// ============================================================================

// setupOnFailurePipeline 创建带失败工具的测试管道，可选注入 approval.Manager。
// askFn 用于控制 CheckWithResult 的决策返回值。
func setupOnFailurePipeline(t *testing.T, mgr *approval.Manager) (*Executor, Call) {
	t.Helper()
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册始终失败的测试工具
	err := e.RegisterHandler("onfail_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "", fmt.Errorf("simulated execution failure")
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry 以通过 Schema 验证
	r.builtin["onfail_test_tool"] = Spec{
		Name:               "onfail_test_tool",
		Description:        "on-failure 测试工具",
		InputSchema:        map[string]interface{}{"type": "object"},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	// 使用 LevelDangerFullAccess 确保权限阶段直接放行（needConfirm=false），
	// 这样 on-failure 检查是 approvalMgr 在执行失败后的唯一接入点
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	if mgr != nil {
		e.Pipeline.SetApprovalManager(mgr)
	}

	call := Call{
		ID:    "test-onfail-1",
		Name:  "onfail_test_tool",
		Input: `{}`,
	}
	return e, call
}

// findStageLog 在 StageLogs 中查找指定阶段名称的日志，返回指针与是否找到。
func findStageLog(logs []StageLog, stageName string) (*StageLog, bool) {
	for i := range logs {
		if logs[i].Stage == stageName {
			return &logs[i], true
		}
	}
	return nil, false
}

// TestPipeline_OnFailure_CheckWithResultCalled 验证 approvalMgr 非 nil + OnFailure 策略 + 执行失败时调用 CheckWithResult。
// 通过 AskFn 回调被调用来验证 CheckWithResult 已接入 on-failure 路径。
func TestPipeline_OnFailure_CheckWithResultCalled(t *testing.T) {
	askCalled := false
	var askedToolName string
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		askCalled = true
		askedToolName = toolName
		return approval.DecisionAllow, "user allowed retry"
	}
	mgr := approval.NewManager(approval.PolicyOnFailure, askFn)

	e, call := setupOnFailurePipeline(t, mgr)
	pr := e.ExecuteWithPipeline(context.Background(), call)

	// AskFn 应被调用（证明 CheckWithResult 在 on-failure 路径被调用）
	if !askCalled {
		t.Error("OnFailure 策略 + 执行失败时应调用 AskFn（经 CheckWithResult）")
	}
	if askedToolName != "onfail_test_tool" {
		t.Errorf("AskFn 收到的 toolName = %q，期望 %q", askedToolName, "onfail_test_tool")
	}

	// DecisionAllow → 管道应继续完成（Proceed=true）
	if !pr.Proceed {
		t.Errorf("DecisionAllow 应继续管道，但被拒绝: %s", pr.Rejection)
	}
	// 工具执行失败的结果应存在
	if pr.Result == nil {
		t.Fatal("DecisionAllow 后结果不应为 nil")
	}
	if !pr.Result.IsError {
		t.Error("工具执行失败的结果 IsError 应为 true")
	}

	// 应有 on_failure_approval 阶段日志，状态为 pass
	failLog, ok := findStageLog(pr.StageLogs, "on_failure_approval")
	if !ok {
		t.Fatal("应有 on_failure_approval 阶段日志")
	}
	if failLog.Status != "pass" {
		t.Errorf("on_failure_approval 状态 = %q，期望 %q", failLog.Status, "pass")
	}
}

// TestPipeline_OnFailure_NilApprovalManager 验证 approvalMgr 为 nil 时 on-failure 行为与接入前一致。
// 管道应正常完成（Proceed=true），不产生 on_failure_approval 阶段日志。
func TestPipeline_OnFailure_NilApprovalManager(t *testing.T) {
	e, call := setupOnFailurePipeline(t, nil)
	pr := e.ExecuteWithPipeline(context.Background(), call)

	// 管道应继续完成（与接入前行为一致）
	if !pr.Proceed {
		t.Errorf("nil approvalMgr 时管道应继续完成，但被拒绝: %s", pr.Rejection)
	}
	// 工具执行失败的结果应存在
	if pr.Result == nil {
		t.Fatal("nil approvalMgr 时结果不应为 nil")
	}
	if !pr.Result.IsError {
		t.Error("工具执行失败的结果 IsError 应为 true")
	}

	// 不应有 on_failure_approval 阶段日志（approvalMgr 为 nil，跳过 on-failure 审批）
	if _, ok := findStageLog(pr.StageLogs, "on_failure_approval"); ok {
		t.Error("nil approvalMgr 时不应有 on_failure_approval 阶段日志（向后兼容）")
	}

	// 验证阶段日志数量与接入前一致（无 on_failure_approval）
	// 桶 X6 P-4 新增 destructive_check 阶段（7→8）；-R1 新增 dangerous_guard（8→9）；
	// 2026-10-04 新增 sandbox_precheck（沙箱硬拒绝前置，9→10）。
	expectedStages := []string{
		"schema_validate", "custom_validate", "pre_hook",
		"sandbox_precheck", "dangerous_guard", "destructive_check", "safety_check", "permission", "execute", "post_hook",
	}
	if len(pr.StageLogs) != len(expectedStages) {
		t.Errorf("nil approvalMgr 时阶段日志数 = %d，期望 %d（与接入前一致）", len(pr.StageLogs), len(expectedStages))
	}
}

// TestPipeline_OnFailure_DecisionAllow 验证 DecisionAllow 时管道继续执行后续阶段。
func TestPipeline_OnFailure_DecisionAllow(t *testing.T) {
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		return approval.DecisionAllow, "user allowed retry"
	}
	mgr := approval.NewManager(approval.PolicyOnFailure, askFn)

	e, call := setupOnFailurePipeline(t, mgr)
	pr := e.ExecuteWithPipeline(context.Background(), call)

	// Allow → 管道继续，Proceed=true
	if !pr.Proceed {
		t.Errorf("DecisionAllow 应继续管道，但被拒绝: %s", pr.Rejection)
	}
	// 工具失败结果应存在
	if pr.Result == nil || !pr.Result.IsError {
		t.Error("DecisionAllow 后应包含失败的工具结果")
	}
	// NeedConfirm 应为 false
	if pr.NeedConfirm {
		t.Error("DecisionAllow 时 NeedConfirm 应为 false")
	}
	// on_failure_approval 日志状态为 pass
	failLog, ok := findStageLog(pr.StageLogs, "on_failure_approval")
	if !ok {
		t.Fatal("应有 on_failure_approval 阶段日志")
	}
	if failLog.Status != "pass" {
		t.Errorf("DecisionAllow 时 on_failure_approval 状态 = %q，期望 %q", failLog.Status, "pass")
	}
	// 后续 post_hook 阶段应执行
	if _, ok := findStageLog(pr.StageLogs, "post_hook"); !ok {
		t.Error("DecisionAllow 后应继续执行 post_hook 阶段")
	}
}

// TestPipeline_OnFailure_DecisionDeny 验证 DecisionDeny 时管道终止并返回拒绝原因。
func TestPipeline_OnFailure_DecisionDeny(t *testing.T) {
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		return approval.DecisionDeny, "user denied retry after failure"
	}
	mgr := approval.NewManager(approval.PolicyOnFailure, askFn)

	e, call := setupOnFailurePipeline(t, mgr)
	pr := e.ExecuteWithPipeline(context.Background(), call)

	// Deny → 管道终止，Proceed=false
	if pr.Proceed {
		t.Error("DecisionDeny 应终止管道，Proceed 应为 false")
	}
	// NeedConfirm 应为 false
	if pr.NeedConfirm {
		t.Error("DecisionDeny 时 NeedConfirm 应为 false")
	}
	// 拒绝原因应包含 denied
	if pr.Rejection == "" {
		t.Error("DecisionDeny 时拒绝原因不应为空")
	}
	if !strings.Contains(strings.ToLower(pr.Rejection), "denied") {
		t.Errorf("拒绝原因应包含 'denied'，actual: %q", pr.Rejection)
	}
	// on_failure_approval 日志状态为 reject
	failLog, ok := findStageLog(pr.StageLogs, "on_failure_approval")
	if !ok {
		t.Fatal("应有 on_failure_approval 阶段日志")
	}
	if failLog.Status != "reject" {
		t.Errorf("DecisionDeny 时 on_failure_approval 状态 = %q，期望 %q", failLog.Status, "reject")
	}
	// Deny 后不应执行 post_hook 阶段
	if _, ok := findStageLog(pr.StageLogs, "post_hook"); ok {
		t.Error("DecisionDeny 后不应执行 post_hook 阶段")
	}
}

// TestPipeline_OnFailure_DecisionAsk 验证 DecisionAsk 时管道暂停等待用户确认。
func TestPipeline_OnFailure_DecisionAsk(t *testing.T) {
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		return approval.DecisionAsk, "user needs to confirm retry"
	}
	mgr := approval.NewManager(approval.PolicyOnFailure, askFn)

	e, call := setupOnFailurePipeline(t, mgr)
	pr := e.ExecuteWithPipeline(context.Background(), call)

	// Ask → 管道暂停，Proceed=false, NeedConfirm=true
	if pr.Proceed {
		t.Error("DecisionAsk 应暂停管道，Proceed 应为 false")
	}
	if !pr.NeedConfirm {
		t.Error("DecisionAsk 时 NeedConfirm 应为 true")
	}
	// 工具已执行，结果应存在（失败结果）
	if pr.Result == nil {
		t.Fatal("DecisionAsk 时结果不应为 nil（工具已执行）")
	}
	if !pr.Result.IsError {
		t.Error("DecisionAsk 时工具结果 IsError 应为 true")
	}
	// on_failure_approval 日志应存在
	failLog, ok := findStageLog(pr.StageLogs, "on_failure_approval")
	if !ok {
		t.Fatal("应有 on_failure_approval 阶段日志")
	}
	if failLog.Status != "pass" {
		t.Errorf("DecisionAsk 时 on_failure_approval 状态 = %q，期望 %q", failLog.Status, "pass")
	}
	// Ask 后不应执行 post_hook 阶段
	if _, ok := findStageLog(pr.StageLogs, "post_hook"); ok {
		t.Error("DecisionAsk 后不应执行 post_hook 阶段")
	}
}

// TestPipeline_OnFailure_NonOnFailurePolicySkipped 验证非 OnFailure 策略时跳过 on-failure 审批。
// OnRequest 策略下，执行失败不应触发 CheckWithResult（已在执行前由 Check 决策）。
func TestPipeline_OnFailure_NonOnFailurePolicySkipped(t *testing.T) {
	askCalled := false
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		askCalled = true
		return approval.DecisionAllow, "should not be called"
	}
	// 使用 OnRequest 策略（非 OnFailure）
	mgr := approval.NewManager(approval.PolicyOnRequest, askFn)

	e, call := setupOnFailurePipeline(t, mgr)
	pr := e.ExecuteWithPipeline(context.Background(), call)

	// AskFn 不应被调用（非 OnFailure 策略，跳过 on-failure 审批）
	if askCalled {
		t.Error("非 OnFailure 策略时不应调用 AskFn（应跳过 on-failure 审批）")
	}
	// 管道应继续完成
	if !pr.Proceed {
		t.Errorf("非 OnFailure 策略时管道应继续完成，但被拒绝: %s", pr.Rejection)
	}
	// 不应有 on_failure_approval 阶段日志
	if _, ok := findStageLog(pr.StageLogs, "on_failure_approval"); ok {
		t.Error("非 OnFailure 策略时不应有 on_failure_approval 阶段日志")
	}
}

// TestPipeline_OnFailure_SuccessNoCheck 验证工具执行成功时不触发 on-failure 审批。
func TestPipeline_OnFailure_SuccessNoCheck(t *testing.T) {
	askCalled := false
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		askCalled = true
		return approval.DecisionAllow, "should not be called"
	}
	mgr := approval.NewManager(approval.PolicyOnFailure, askFn)

	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册成功的工具
	err := e.RegisterHandler("success_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "success output", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}
	r.builtin["success_test_tool"] = Spec{
		Name:               "success_test_tool",
		Description:        "成功测试工具",
		InputSchema:        map[string]interface{}{"type": "object"},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)
	e.Pipeline.SetApprovalManager(mgr)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "test-success-1",
		Name:  "success_test_tool",
		Input: `{}`,
	})

	// AskFn 不应被调用（执行成功，无 on-failure 检查）
	if askCalled {
		t.Error("工具执行成功时不应调用 AskFn")
	}
	// 管道应成功完成
	if !pr.Proceed {
		t.Errorf("工具执行成功时管道应继续，但被拒绝: %s", pr.Rejection)
	}
	// 不应有 on_failure_approval 阶段日志
	if _, ok := findStageLog(pr.StageLogs, "on_failure_approval"); ok {
		t.Error("工具执行成功时不应有 on_failure_approval 阶段日志")
	}
	// 结果应为成功输出
	if pr.Result == nil || pr.Result.Output != "success output" {
		t.Errorf("工具结果 = %v，期望 output %q", pr.Result, "success output")
	}
}

// TestPipeline_SandboxDenialSkipsOnFailureApproval verifies that a strict
// sandbox rejection is terminal for the current tool call. It must not enter
// the generic on-failure approval path, because approving a retry cannot make
// a command allowed by the active sandbox.
//
// ★ 2026-10-05改主体：命令由 `curl … > network-marker.txt` 换成 `nc -l 4444`。
//
//	原因：沙箱拒绝**分档**后 `curl` 落**可批准档**（"批准即沙箱外执行"），
//	它已不再是"注定装不上"的命令，用它测"不得进入 on-failure 审批"会测错对象。
//	`nc -l 4444`（监听态）经 `hardenNoApprovableEscape` 落**硬拒档**，
//	且不在 cmdrules 危险档名单里 ⇒ 干净地只测"硬拒档 → 终局拒绝"。
//	可批准档的行为判据在 .go。
func TestPipeline_SandboxDenialSkipsOnFailureApproval(t *testing.T) {
	askCalled := false
	askFn := func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		askCalled = true
		return approval.DecisionAllow, "should not be called for sandbox denial"
	}
	mgr := approval.NewManager(approval.PolicyOnFailure, askFn)

	r := NewRegistry()
	workDir := t.TempDir()
	e := NewExecutor(r, workDir)
	e.Sandbox.SetEnabled(true)

	hookMgr := hook.NewManager(workDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, workDir)
	e.Pipeline.SetApprovalManager(mgr)

	command := "nc -l 4444"
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "sandbox-deny-on-failure",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": command}),
	})

	if askCalled {
		t.Fatal("sandbox denial must not trigger on-failure approval")
	}
	// ★ 本函数断言于 2026-10-04 **有意翻转**（沙箱硬拒绝前移到管线）——**下一个人别当回归**：
	// 翻转前钉的是「管线放行（Proceed=true）、沙箱拒绝发生在**执行期**、结果保留在 pr.Result」；
	// 现在 `SandboxPrecheckStage` 在询问用户之前就终局拒绝 ⇒ Proceed=false、Result=nil，
	// **命令根本没有进入执行链**。翻转原因：旧结构下用户会为一条注定被拒的命令白批准一次
	//（真机取证 2026-10-04：`Monitor({"command":"… $(date) …"})` 批准后返回 sandbox denied）。
	// 本测试的**核心不变量未变**：沙箱拒绝不得触发 on-failure approval（askCalled 仍须 false）。
	if pr.Proceed {
		t.Fatalf("沙箱硬拒绝应在管线终局，实际放行到了执行：%+v", pr)
	}
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("failure kind = %q, want %q", pr.FailureKind, FailureKindSandboxDenied)
	}
	// 命令未被执行的证据：拒绝短路在 ExecuteStage 之前 ⇒ Result 保持 nil。
	if pr.Result != nil {
		t.Errorf("沙箱硬拒绝不该产生执行结果（命令未执行），got %+v", pr.Result)
	}
	// 成因方必须是"管线" —— 用户从未被询问。
	if pr.ConfirmSource != SourcePipeline {
		t.Errorf("ConfirmSource = %q, want %q（用户从未被询问）", pr.ConfirmSource, SourcePipeline)
	}
	if _, ok := findStageLog(pr.StageLogs, "on_failure_approval"); ok {
		t.Fatal("sandbox denial must not add an on_failure_approval stage log")
	}
	// ★ 信息不退化（填补被翻转掉的"Output 含原始命令"断言）：
	// 管线拒绝仍必须能转成一条**模型可见**的工具结果 —— `DenialResult` 是调用方的唯一
	// 转换点（`tui/runner_core.go` 与 `tui/plain.go` 都走它）。旧断言钉的是
	// `pr.Result.Output` 含命令原文；新结构下同样的信息由 `Rejection` → `DenialResult.Output` 承载。
	dr := DenialResult("sandbox-deny-on-failure", "bash", pr)
	if !dr.IsError || dr.FailureKind != FailureKindSandboxDenied || strings.TrimSpace(dr.Output) == "" {
		t.Fatalf("管线拒绝必须能转成模型可见的工具结果，got %+v", dr)
	}
}

// TestExecuteWithConfirm_SandboxDenialIsNeverShownToUser（原 TestExecuteWithConfirm_SandboxDenialPromptsOnlyOnce）
//
// ★ 本函数已于 2026-10-04 有意翻转（沙箱硬拒绝前移到管线 —— 新增 `SandboxPrecheckStage`）。
//
// 翻转理由：本用例的命令 `curl https://example.com > network-marker.txt` 在默认姿态下命中沙箱
// **DENYLIST 硬拒绝**（`internal/sandbox/network_switch_test.go` 明确断言 "默认：networkIsolated=true +
// curl 被 deny 列表拦截"；`sandbox.Validate` 只有「能/不能」二元，无「可升级」第三态）。
// 旧结构下 `PermissionStage` 先问、`ExecuteStage` 再拒 ⇒ 用户对一个**注定装不上**的命令白点了一次
// 「允许」，这正是 ①「批准与拒绝不同源」的现象。新结构下 `SandboxPrecheckStage` 在**任何询问之前**
// 就把硬拒绝判定出来 ⇒ 不再打扰用户。旧断言 `confirmCount == 1` 钉的正是那个**错误**行为，故改为 0。
//
// 本测试仍守住的**核心不变量**：硬拒绝**永不**弹给用户（0 次），且不得回弹到确认态
// （`NeedConfirm=false`）；同时信息不退化（管线拒绝仍能转成模型可见结果）。
//
// ★ 2026-10-05改主体与更正一处过时描述：命令由
// `curl https://example.com > network-marker.txt` 换成 `nc -l 4444`。
// - `curl` 现落**可批准档**（"批准即沙箱外执行"，语义见 ruleengine.go VerdictTier）
// ⇒ 用它测"硬拒永不弹给用户"会测错对象；
// - 上面那句「`sandbox.Validate` 只有「能/不能」二元，无「可升级」第三态」**已过时**：
// 现在 `Validate` 仍保持二元历史契约（可批准档也是"不允许"），但**新增**了
// 档位出口 `Classify` 与已批准变体 `ValidateSanctioned`——这正是 的落点。
func TestExecuteWithConfirm_SandboxDenialIsNeverShownToUser(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	e := NewExecutor(r, workDir)
	e.Sandbox.SetEnabled(true)

	hookMgr := hook.NewManager(workDir)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipeline(hookMgr, policy, workDir)

	command := "nc -l 4444"

	// 前置自检：先证明执行链确实会拒绝这条命令，否则本用例的判别前提不成立（规则/环境变更须显式暴露）。
	if ok, reason := e.Sandbox.Validate(command); ok {
		t.Fatalf("前置自检失败：执行链本应硬拒绝该命令，got allow (%s) —— 规则或环境已变，须重审本用例", reason)
	}

	confirmCount := 0
	e.OnConfirm = func(toolName, input string) bool {
		confirmCount++
		return true
	}

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "sandbox-deny-never-prompt",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": command}),
	})

	// ★ 新语义：硬拒绝的命令**从不**出现在用户面前（不是 1 次、也不是 2 次，而是 0 次）。
	if confirmCount != 0 {
		t.Fatalf("沙箱硬拒绝的命令不得询问用户，confirmation count = %d, want 0", confirmCount)
	}
	if pr.NeedConfirm {
		t.Fatal("approved command must not return to the confirmation state after sandbox denial")
	}
	if pr.Proceed || pr.Result != nil {
		t.Fatalf("沙箱硬拒绝应在执行前拦下（Proceed=false、Result=nil），got %+v", pr)
	}
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("failure kind = %q, want %q", pr.FailureKind, FailureKindSandboxDenied)
	}
	if pr.ConfirmSource != SourcePipeline {
		t.Fatalf("ConfirmSource = %q, want %q（拒绝来自管线，而非用户）", pr.ConfirmSource, SourcePipeline)
	}
	// ★ 信息不退化：管线拒绝仍必须能转成一条模型可见的工具结果（否则模型只看到"什么都没有"）。
	dr := DenialResult("sandbox-deny-never-prompt", "bash", pr)
	if !dr.IsError || dr.FailureKind != FailureKindSandboxDenied || strings.TrimSpace(dr.Output) == "" {
		t.Fatalf("管线拒绝必须能转成模型可见的工具结果，got %+v", dr)
	}
}
