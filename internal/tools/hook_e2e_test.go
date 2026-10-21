package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

func TestE2E_ConfigToExecution(t *testing.T) {
	tmpDir := t.TempDir()

	hooksDir := filepath.Join(tmpDir, ".whale")
	os.MkdirAll(hooksDir, 0755)
	hooksJSON := `{"pre_tool_use":[{"matcher":"test_e2e_bash","action":"block","reason":"e2e test block"}]}`
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	r.builtin["test_e2e_bash"] = Spec{
		Name: "test_e2e_bash",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelDangerFullAccess,
		ConcurrencySafe:    true,
	}
	e.RegisterHandler("test_e2e_bash", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "executed", nil
	})

	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipelineWithHooks(policy, tmpDir)

	// PR-A 信任门：hook 必须先被信任才会运行（对齐业内既有实现 /hooks review 后再 trust）。
	// 本 E2E 验证的是「已信任的 hook 能拦住工具」，因此先落一次信任（信任库指到临时目录，不污染 ~/.whale）。
	e.hookMgr.SetTrustStorePath(filepath.Join(tmpDir, "hooks.trust.json"))
	if err := e.hookMgr.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_bash",
		Input: `{"command":"echo hello"}`,
	})

	if pr.Proceed {
		t.Error("E2E: hooks.json 应阻止工具执行")
	}
	if pr.Rejection == "" {
		t.Error("E2E: 应有rejection reason")
	}
}

func TestE2E_DynamicAddAndTrigger(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	r.builtin["test_e2e_dynamic"] = Spec{
		Name: "test_e2e_dynamic",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}
	e.RegisterHandler("test_e2e_dynamic", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "read ok", nil
	})

	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// 初始：应通过
	pr1 := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_dynamic", Input: `{"path":"test"}`,
	})
	if !pr1.Proceed {
		t.Fatalf("初始应通过: %s", pr1.Rejection)
	}

	// 动态添加钩子阻止
	hookMgr.AddHook("PreToolUse", &hook.HookCommand{
		HookBase: hook.HookBase{Matcher: "test_e2e_dynamic", Action: hook.ActionBlock, Reason: "dynamic block", Enabled: true},
	})

	// 再次执行：should be blocked
	pr2 := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "2", Name: "test_e2e_dynamic", Input: `{"path":"test"}`,
	})
	if pr2.Proceed {
		t.Error("E2E: 动态添加的钩子应阻止工具执行")
	}
}

func TestE2E_DynamicRemoveAndTrigger(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	r.builtin["test_e2e_remove"] = Spec{
		Name: "test_e2e_remove",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}
	r.builtin["test_e2e_remove2"] = Spec{
		Name: "test_e2e_remove2",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}
	e.RegisterHandler("test_e2e_remove", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	})
	e.RegisterHandler("test_e2e_remove2", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	})

	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "test_e2e_remove", Action: hook.ActionBlock, Reason: "blocked"}},
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "test_e2e_remove2", Action: hook.ActionBlock, Reason: "also blocked"}},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// 移除第二个钩子
	hookMgr.RemoveHook("PreToolUse", 1)

	// test_e2e_remove 应仍被阻止
	pr1 := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_remove", Input: `{"path":"test"}`,
	})
	if pr1.Proceed {
		t.Error("test_e2e_remove 应仍被阻止")
	}

	// test_e2e_remove2 应通过（钩子已移除）
	pr2 := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "2", Name: "test_e2e_remove2", Input: `{"path":"*.go"}`,
	})
	if !pr2.Proceed {
		t.Errorf("test_e2e_remove2 应通过（钩子已移除），but was rejected: %s", pr2.Rejection)
	}
}

func TestE2E_PreHookUpdatedInput(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	r.builtin["test_e2e_updated"] = Spec{
		Name: "test_e2e_updated",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	var executedCommand string
	e.RegisterHandler("test_e2e_updated", func(ctx context.Context, input json.RawMessage) (string, error) {
		var parsed map[string]interface{}
		json.Unmarshal(input, &parsed)
		if cmd, ok := parsed["command"].(string); ok {
			executedCommand = cmd
		}
		return "ok", nil
	})

	hookMgr := hook.NewManager(tmpDir)
	// 使用 callback 类型钩子修改输入，避免子进程dependency
	updatedInputStr := `{"command":"ls -la"}`
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCallback{
				HookBase: hook.HookBase{Matcher: "test_e2e_updated", Action: hook.ActionAllow, Type: hook.HookTypeCallback},
				Callback: func(payload hook.HookPayload) hook.HookResult {
					return hook.HookResult{
						Action:       hook.ActionAllow,
						UpdatedInput: &updatedInputStr,
					}
				},
			},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_updated",
		Input: `{"command":"rm -rf /"}`,
	})

	if !pr.Proceed {
		t.Fatalf("管道应通过: %s", pr.Rejection)
	}
	if executedCommand != "ls -la" {
		t.Errorf("命令应被钩子修改为 'ls -la'，actual: %q", executedCommand)
	}
}

func TestE2E_PostHookModifiedOutput(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	r.builtin["test_e2e_output_mod"] = Spec{
		Name: "test_e2e_output_mod",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}
	e.RegisterHandler("test_e2e_output_mod", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "original output", nil
	})

	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PostToolUse: []hook.Hook{
			&hook.HookCommand{HookBase: hook.HookBase{Matcher: "test_e2e_output_mod", Action: hook.ActionAllow}, Command: "echo sanitized output"},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_output_mod", Input: `{}`,
	})

	if !pr.Proceed {
		t.Fatalf("管道应通过: %s", pr.Rejection)
	}
	if pr.Result == nil {
		t.Fatal("结果不should be nil")
	}
	output := pr.Result.Output
	output = strings.TrimSpace(output)
	output = strings.NewReplacer("\r", "", "\n", "").Replace(output)
	// Windows PowerShell echo 可能不保留空格格式，使用包含检查
	if !strings.Contains(output, "sanitized") {
		t.Errorf("输出应被修改为 'sanitized output'，actual: %q", output)
	}
}

func TestE2E_ConfigPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, ".whale")
	os.MkdirAll(hooksDir, 0755)

	// 第一轮：添加钩子并持久化
	m1 := hook.NewManager(tmpDir)
	m1.AddHook("PreToolUse", &hook.HookCommand{HookBase: hook.HookBase{Matcher: "bash", Action: hook.ActionBlock, Reason: "persisted", Enabled: true}})
	m1.SaveConfig(filepath.Join(hooksDir, "hooks.json"))

	// mock重启：新建 Manager 加载同一配置
	m2 := hook.NewManager(tmpDir)
	m2.LoadConfig(filepath.Join(hooksDir, "hooks.json"))

	cfg := m2.Config()
	if len(cfg.PreToolUse) != 1 || cfg.PreToolUse[0].Base().Reason != "persisted" {
		t.Error("重启后配置应保持")
	}
}

func TestE2E_UserProjectMerge(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	userHooks := `{"pre_tool_use":[{"matcher":"bash","action":"block","reason":"user block bash"}],"stop":[{"matcher":"*","action":"allow","command":"echo user_stop"}]}`
	os.WriteFile(filepath.Join(userDir, "hooks.json"), []byte(userHooks), 0644)

	projectHooksDir := filepath.Join(projectDir, ".whale")
	os.MkdirAll(projectHooksDir, 0755)
	projectHooks := `{"pre_tool_use":[{"matcher":"write_file","action":"block","reason":"project block writes"}],"pre_bash":[{"matcher":"bash","action":"block"}]}`
	os.WriteFile(filepath.Join(projectHooksDir, "hooks.json"), []byte(projectHooks), 0644)

	m := hook.NewManager(projectDir)
	m.LoadConfig(filepath.Join(userDir, "hooks.json"))
	m.LoadConfig(filepath.Join(projectHooksDir, "hooks.json"))

	cfg := m.Config()
	if len(cfg.PreToolUse) != 2 {
		t.Errorf("合并后 PreToolUse 应为 2, actual: %d", len(cfg.PreToolUse))
	}
	if len(cfg.Stop) != 1 {
		t.Errorf("合并后 Stop 应有User配置的钩子, actual: %d", len(cfg.Stop))
	}
	if len(cfg.PreBash) != 1 {
		t.Errorf("合并后 PreBash 应有项目配置的钩子, actual: %d", len(cfg.PreBash))
	}
}

func TestE2E_HookPriority(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	r.builtin["test_e2e_priority"] = Spec{
		Name: "test_e2e_priority",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}
	e.RegisterHandler("test_e2e_priority", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	})

	var executionOrder []string
	hookMgr := hook.NewManager(tmpDir)
	hookMgr.SetConfig(hook.HookConfig{
		PreToolUse: []hook.Hook{
			&hook.HookCallback{
				HookBase: hook.HookBase{Matcher: "test_e2e_priority", Action: hook.ActionAllow, Type: hook.HookTypeCallback, Priority: 10},
				Callback: func(payload hook.HookPayload) hook.HookResult {
					executionOrder = append(executionOrder, "low")
					return hook.HookResult{Action: hook.ActionAllow}
				},
			},
			&hook.HookCallback{
				HookBase: hook.HookBase{Matcher: "test_e2e_priority", Action: hook.ActionAllow, Type: hook.HookTypeCallback, Priority: 1},
				Callback: func(payload hook.HookPayload) hook.HookResult {
					executionOrder = append(executionOrder, "high")
					return hook.HookResult{Action: hook.ActionAllow}
				},
			},
		},
	})
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_priority", Input: `{}`,
	})

	if !pr.Proceed {
		t.Fatalf("管道应通过: %s", pr.Rejection)
	}

	// 高优先级钩子应先匹配（短路逻辑下只有第一个匹配的被执行）
	if len(executionOrder) != 1 || executionOrder[0] != "high" {
		t.Errorf("高优先级钩子应先执行，执行顺序: %v", executionOrder)
	}
}

// TestE2E_UntrustedHookDoesNotBlock 信任门的**生产层**证据（PR-A · Q1）。
//
// 生产入口：InitPipelineWithHooks（tui/lifecycle.go:425 / runner_core.go:532 / bridge.go:274 汇聚于此）。
// 用户可见现象：hooks.json 里配了 block 规则的 hook，只要没被信任，就**不参与决策**，工具照常执行。
// 这条用例存在的意义：防止 EnableTrustGate() 的接线被摘掉后没人发现（门变死代码）。
func TestE2E_UntrustedHookDoesNotBlock(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, ".whale")
	os.MkdirAll(hooksDir, 0755)

	// 一个会 block 的 hook，但从未被信任
	hooksJSON := `{"pre_tool_use":[{"id":"untrusted-block","matcher":"test_e2e_untrusted","action":"block","reason":"should not apply"}]}`
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)
	r.builtin["test_e2e_untrusted"] = Spec{
		Name: "test_e2e_untrusted",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
		},
		RequiredPermission: LevelDangerFullAccess,
		ConcurrencySafe:    true,
	}
	e.RegisterHandler("test_e2e_untrusted", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "executed", nil
	})

	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipelineWithHooks(policy, tmpDir)
	// 信任库指到临时目录（不污染 ~/.whale），并且**不做任何信任动作**
	e.hookMgr.SetTrustStorePath(filepath.Join(tmpDir, "hooks.trust.json"))

	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID: "1", Name: "test_e2e_untrusted",
		Input: `{"command":"echo hello"}`,
	})

	if !pr.Proceed {
		t.Errorf("untrusted hook must be skipped, tool should proceed; got Proceed=false, rejection=%q", pr.Rejection)
	}
	if pending := e.hookMgr.PendingReview(); len(pending) != 1 {
		t.Errorf("expected exactly 1 hook pending review, got %d", len(pending))
	}
}
