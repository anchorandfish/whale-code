package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestExecutor_InitPipeline 测试调用 InitPipeline 后 executor.Pipeline 不为 nil
func TestExecutor_InitPipeline(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	if e.Pipeline != nil {
		t.Error("新建 Executor 的 Pipeline should be nil")
	}

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	if e.Pipeline == nil {
		t.Error("调用 InitPipeline 后 Pipeline 不should be nil")
	}
}

// TestExecutor_ExecuteWithPipeline_Active 测试 Pipeline 初始化后 ExecuteWithPipeline 返回 7 个阶段日志（含 safety_check）
func TestExecutor_ExecuteWithPipeline_Active(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("active_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "active test result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry 以通过 Schema 验证
	r.builtin["active_test_tool"] = Spec{
		Name:        "active_test_tool",
		Description: "活跃管道test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"msg": map[string]interface{}{"type": "string"},
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
		Name:  "active_test_tool",
		Input: `{"msg":"hello"}`,
	})

	if !pr.Proceed {
		t.Errorf("管道应通过，but was rejected: %s", pr.Rejection)
	}
	if pr.Result == nil {
		t.Fatal("结果不should be nil")
	}
	if pr.Result.Output != "active test result" {
		t.Errorf("Output = %q, want %q", pr.Result.Output, "active test result")
	}

	// 验证 10 个阶段日志（含 destructive_check 破坏性检查 + safety_check 安全路径检查阶段
	// + -R1 新增的 dangerous_guard 危险档强制确认阶段
	// + 2026-10-04 新增的 sandbox_precheck 沙箱硬拒绝前置阶段）
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

// TestExecutor_ExecuteWithPipeline_SchemaReject 测试missing required field在 schema_validate 阶段rejected
func TestExecutor_ExecuteWithPipeline_SchemaReject(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// bash tool requires command 必填，缺少 command 应在 schema_validate 阶段rejected
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: `{"timeout": 30}`,
	})

	if pr.Proceed {
		t.Error("missing required field应在 schema_validate 阶段rejected")
	}
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

// TestExecutor_ExecuteWithPipeline_PathTraversal 测试路径包含 ".." 在 custom_validate 阶段rejected
// 使用绝对路径 "../../../etc/passwd" 确保 filepath.Clean 后仍包含 ".."
func TestExecutor_ExecuteWithPipeline_PathTraversal(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "write_file",
		Input: `{"path": "../../../etc/passwd", "content": "hack"}`,
	})

	if pr.Proceed {
		t.Error("path traversal应在 custom_validate 阶段rejected")
	}

	// 验证短路位置：schema_validate 通过，custom_validate 拒绝
	if len(pr.StageLogs) < 2 {
		t.Fatalf("至少应有 2 个阶段日志，actual有 %d 个: %+v", len(pr.StageLogs), pr.StageLogs)
	}
	if pr.StageLogs[0].Stage != "schema_validate" || pr.StageLogs[0].Status != "pass" {
		t.Errorf("schema_validate 应通过，actual: stage=%q status=%q", pr.StageLogs[0].Stage, pr.StageLogs[0].Status)
	}
	if pr.StageLogs[1].Stage != "custom_validate" || pr.StageLogs[1].Status != "reject" {
		t.Errorf("custom_validate 应拒绝，actual: stage=%q status=%q", pr.StageLogs[1].Stage, pr.StageLogs[1].Status)
	}
	// 不应有后续阶段
	if len(pr.StageLogs) > 2 {
		t.Errorf("path traversal拒绝应短路，但执行了 %d 个阶段", len(pr.StageLogs))
	}
}

// TestExecutor_ExecuteWithPipeline_PreHookBlock 测试 PreHook block 动作拒绝工具执行
func TestExecutor_ExecuteWithPipeline_PreHookBlock(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器，用于验证不会被执行
	executed := false
	err := e.RegisterHandler("prehook_block_itool", func(ctx context.Context, input json.RawMessage) (string, error) {
		executed = true
		return "should not reach here", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry 以通过 Schema 验证
	r.builtin["prehook_block_itool"] = Spec{
		Name:        "prehook_block_itool",
		Description: "pre-hook拦截test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"data": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelDangerFullAccess,
		ConcurrencySafe:    true,
	}

	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "prehook_block_itool", Action: hook.ActionBlock, Reason: "pre-hook拦截：forbidden to execute此工具"}},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "prehook_block_itool",
		Input: `{"data":"test"}`,
	})

	if pr.Proceed {
		t.Error("pre-hook block 应拒绝工具执行")
	}
	if executed {
		t.Error("pre-hook拦截后，工具处理器不应被执行")
	}

	// 验证短路位置：前两个阶段通过，pre_hook 拒绝
	if len(pr.StageLogs) < 3 {
		t.Fatal("至少应有 3 个阶段日志")
	}
	if pr.StageLogs[2].Stage != "pre_hook" || pr.StageLogs[2].Status != "reject" {
		t.Errorf("pre_hook 应拒绝，actual: stage=%q status=%q", pr.StageLogs[2].Stage, pr.StageLogs[2].Status)
	}
	// 不应有 execute 和 post_hook 阶段
	for _, log := range pr.StageLogs {
		if log.Stage == "execute" || log.Stage == "post_hook" {
			t.Errorf("pre-hook短路后不应有 %s 阶段日志", log.Stage)
		}
	}
}

// TestExecutor_ExecuteWithPipeline_PermissionReject 测试权限策略拒绝未授权工具
func TestExecutor_ExecuteWithPipeline_PermissionReject(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("perm_reject_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "should not reach here", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry，要求 WorkspaceWrite 权限
	r.builtin["perm_reject_tool"] = Spec{
		Name:        "perm_reject_tool",
		Description: "权限拒绝test tool",
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

	// 使用只读权限策略
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelReadOnly)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "perm_reject_tool",
		Input: `{"data":"test"}`,
	})

	if pr.Proceed {
		t.Error("read-only mode下 WorkspaceWrite 工具应被权限阶段拒绝")
	}

	// 验证短路位置：permission 阶段拒绝（七阶段管道含 safety_check，permission 位于第 5 个）
	// 通过阶段名查找而非硬编码索引，避免管道阶段增减导致测试脆弱
	var permLog *StageLog
	for i := range pr.StageLogs {
		if pr.StageLogs[i].Stage == "permission" {
			permLog = &pr.StageLogs[i]
			break
		}
	}
	if permLog == nil {
		stages := make([]string, len(pr.StageLogs))
		for i, l := range pr.StageLogs {
			stages[i] = l.Stage
		}
		t.Fatalf("未找到 permission 阶段日志，实际阶段: %v", stages)
	}
	if permLog.Status != "reject" {
		t.Errorf("permission 应拒绝，actual: stage=%q status=%q", permLog.Stage, permLog.Status)
	}
	// 不应有 execute 和 post_hook 阶段
	for _, log := range pr.StageLogs {
		if log.Stage == "execute" || log.Stage == "post_hook" {
			t.Errorf("权限拒绝短路后不应有 %s 阶段日志", log.Stage)
		}
	}
}

// TestExecutor_InitPipelineWithHooks 测试 InitPipelineWithHooks 方法正确初始化 Pipeline
func TestExecutor_InitPipelineWithHooks(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("hook_init_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "hook init result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	r.builtin["hook_init_tool"] = Spec{
		Name:        "hook_init_tool",
		Description: "钩子初始化test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"msg": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipelineWithHooks(policy, tmpDir)

	if e.Pipeline == nil {
		t.Error("调用 InitPipelineWithHooks 后 Pipeline 不should be nil")
	}

	// 验证管道可以正常执行
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "hook_init_tool",
		Input: `{"msg":"test"}`,
	})

	if !pr.Proceed {
		t.Errorf("管道应通过，but was rejected: %s", pr.Rejection)
	}
	if pr.Result == nil || pr.Result.Output != "hook init result" {
		t.Errorf("结果不正确，expected %q", "hook init result")
	}
}

// TestExecutor_InitPipelineWithHooks_LoadsHookConfig 测试 InitPipelineWithHooks 从工作目录加载hook config
func TestExecutor_InitPipelineWithHooks_LoadsHookConfig(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("hook_config_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "hook config result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	r.builtin["hook_config_tool"] = Spec{
		Name:        "hook_config_tool",
		Description: "hook configtest tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"msg": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelDangerFullAccess,
		ConcurrencySafe:    true,
	}

	// 在工作目录创建 hooks.json 配置
	hooksDir := filepath.Join(tmpDir, ".whale")
	os.MkdirAll(hooksDir, 0755)
	hooksJSON := `{
		"pre_tool_use": [
			{
				"matcher": "hook_config_tool",
				"action": "block",
				"reason": "工作目录钩子拦截"
			}
		]
	}`
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	if err := os.WriteFile(hooksPath, []byte(hooksJSON), 0644); err != nil {
		t.Fatalf("写入hook config失败: %v", err)
	}

	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipelineWithHooks(policy, tmpDir)

	// PR-A 信任门：hook 必须先被信任才会运行（对齐业内既有实现 /hooks review 后再 trust）。
	// 本用例验证的是「已信任的 hook 能拦住工具」，因此先落一次信任（信任库指到临时目录，不污染 ~/.whale）。
	e.hookMgr.SetTrustStorePath(filepath.Join(tmpDir, "hooks.trust.json"))
	if err := e.hookMgr.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending 失败: %v", err)
	}

	// 验证hook config被加载：hook_config_tool 应被pre-hook拦截
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "hook_config_tool",
		Input: `{"msg":"test"}`,
	})

	if pr.Proceed {
		t.Error("工作目录钩子应拦截 hook_config_tool")
	}
	if pr.Rejection == "" {
		t.Error("rejection reason should not be empty")
	}
}

// TestExecutor_InitPipelineWithHooks_SkipsMissingConfig 测试hook config文件不存在时静默跳过
func TestExecutor_InitPipelineWithHooks_SkipsMissingConfig(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("no_hook_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "no hook result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	r.builtin["no_hook_tool"] = Spec{
		Name:        "no_hook_tool",
		Description: "无hook configtest tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	// 不创建任何hook config文件
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipelineWithHooks(policy, tmpDir)

	if e.Pipeline == nil {
		t.Error("即使没有hook config文件，Pipeline 也应被初始化")
	}

	// 验证管道可以正常执行
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "no_hook_tool",
		Input: `{}`,
	})

	if !pr.Proceed {
		t.Errorf("无hook config时管道应通过，but was rejected: %s", pr.Rejection)
	}
}
