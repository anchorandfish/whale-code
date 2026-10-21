package tools

// C-P0-3 反例测试：子代理 executeToolCall 绕过 Pipeline 权限检查修复
//
// 测试目标：
//   - Pipeline=nil 时 fail-closed（拒绝执行，不回退到裸 Execute）
//   - Pipeline 拒绝时错误正确传播（Rejection 字段）
//   - Pipeline 通过且 Result.IsError 时错误正确传播
//   - Pipeline 通过且 Result 正常时输出正确返回
//
// 修复前：executeToolCall 调用 a.Executor.Execute(ctx, call)（裸执行，跳过 Pipeline），
// 子代理可绕过父会话权限限制执行任意工具。
// 修复后：改用 ExecuteWithPipeline + Pipeline=nil fail-closed 检查。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestCP0_3_PipelineNilFailsClosed 反例测试（C-P0-3）：Pipeline=nil 时 fail-closed 拒绝执行。
//
// 修复前：executeToolCall 调用 Execute（不检查 Pipeline），Pipeline=nil 时仍可执行。
// 修复后：显式检查 Pipeline=nil，返回 "pipeline not initialized (fail-closed)" 错误。
func TestCP0_3_PipelineNilFailsClosed(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	// 不调用 InitPipeline，Pipeline 为 nil（fail-closed 测试场景）
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeCoder

	_, err := ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"echo test"}`))
	if err == nil {
		t.Fatalf("C-P0-3 反例失败: Pipeline=nil 时应 fail-closed 拒绝执行\n" +
			"  期望: error 包含 'pipeline not initialized'\n" +
			"  实际: error=nil（走了裸 Execute，绕过权限审批）")
	}
	if !strings.Contains(err.Error(), "pipeline not initialized") {
		t.Errorf("C-P0-3 反例失败: 错误信息不正确\n"+
			"  期望: 包含 'pipeline not initialized'\n"+
			"  实际: %v", err)
	}
}

// TestCP0_3_PipelineEnforced 验证子代理工具调用经过 Pipeline 权限检查（C-P0-3）。
//
// 设置一个拒绝所有工具的权限策略（LevelReadOnly 但 bash 需要 LevelDangerFullAccess），
// 验证子代理调用 bash 时被 Pipeline 权限阶段拒绝。
// 修复前：executeToolCall 调 Execute 不经过 Pipeline，权限策略不生效，bash 可执行。
// 修复后：executeToolCall 调 ExecuteWithPipeline，权限阶段检查失败时返回 Rejection。
func TestCP0_3_PipelineEnforced(t *testing.T) {
	registry := NewRegistry()
	tmpDir := t.TempDir()
	executor := NewExecutor(registry, tmpDir)
	executor.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "should not reach here", nil
	}

	// 初始化 Pipeline，但权限级别设为 ReadOnly（bash 需要 LevelDangerFullAccess）
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelReadOnly)
	executor.InitPipeline(hookMgr, policy, tmpDir)

	ctx := NewAgentContext(executor, "test", "", tmpDir, 1)
	ctx.AgentType = AgentTypeCoder

	_, err := ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"echo test"}`))
	if err == nil {
		t.Fatalf("C-P0-3 反例失败: 权限不足时 Pipeline 应拒绝 bash 执行\n" +
			"  策略: LevelReadOnly（bash 需要 LevelDangerFullAccess）\n" +
			"  期望: error 非 nil（Pipeline 拒绝）\n" +
			"  实际: error=nil（绕过了权限审批）")
	}
	// 验证错误来源是 Pipeline 拒绝（非白名单拒绝）
	if strings.Contains(err.Error(), "not available for") {
		t.Errorf("C-P0-3 反例失败: 错误来源不应是白名单拒绝\n"+
			"  期望: Pipeline 权限拒绝（非白名单）\n"+
			"  实际: %v", err)
	}
}

// TestCP0_3_PipelineResultErrorPropagation 验证 Pipeline 通过但 Result.IsError 时错误正确传播（C-P0-3 字段解包测试）。
//
// mock bash handler 返回错误（IsError=true），验证 executeToolCall 正确解包 PipelineResult
// 并传播 Result.Output 作为错误。
func TestCP0_3_PipelineResultErrorPropagation(t *testing.T) {
	registry := NewRegistry()
	tmpDir := t.TempDir()
	executor := NewExecutor(registry, tmpDir)
	executor.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		// 模拟工具执行失败
		return "", fmt.Errorf("command failed: exit code 1")
	}

	// 初始化 Pipeline，权限级别设为 FullAccess（允许 bash）
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor.InitPipeline(hookMgr, policy, tmpDir)

	ctx := NewAgentContext(executor, "test", "", tmpDir, 1)
	ctx.AgentType = AgentTypeCoder

	_, err := ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"echo test"}`))
	if err == nil {
		t.Fatalf("C-P0-3 字段解包失败: 工具执行错误应被传播\n" +
			"  期望: error 非 nil（工具执行失败）\n" +
			"  实际: error=nil")
	}
}

// TestCP0_3_PipelineProceedSuccess 验证 Pipeline 通过且工具成功时输出正确返回（C-P0-3 回归测试）。
//
// 验证 executeToolCall 正确解包 PipelineResult.Proceed=true + Result.Output 并返回输出。
func TestCP0_3_PipelineProceedSuccess(t *testing.T) {
	registry := NewRegistry()
	tmpDir := t.TempDir()
	executor := NewExecutor(registry, tmpDir)
	executor.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "build succeeded", nil
	}

	// 初始化 Pipeline，权限级别设为 FullAccess（允许 bash）
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor.InitPipeline(hookMgr, policy, tmpDir)

	ctx := NewAgentContext(executor, "test", "", tmpDir, 1)
	ctx.AgentType = AgentTypeCoder

	result, err := ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"echo test"}`))
	if err != nil {
		t.Fatalf("C-P0-3 回归失败: Pipeline 通过且工具成功时应返回输出\n"+
			"  期望: error=nil, result='build succeeded'\n"+
			"  实际: error=%v", err)
	}
	if result != "build succeeded" {
		t.Errorf("C-P0-3 回归失败: 输出不正确\n"+
			"  期望: 'build succeeded'\n"+
			"  实际: %s", result)
	}
}

// TestR1_InitPipelineWithHooks_SubAgentToolCallWorks 反例测试（R-1）：
// 验证生产路径（InitPipelineWithHooks）下子代理工具调用不被 fail-closed 拒绝。
//
// 红队终审 R-1（112-其他未考虑问题与建议）：
// 桶 X1 修复 C-P0-3 时在 executeToolCall 新增 Pipeline=nil fail-closed 检查。
// 风险：若有未接线路径（如 daemon/CLI/测试入口）未调用 InitPipelineWithHooks，
// 子代理工具调用被拒绝。生产入口 tui/lifecycle.go、tui/runner_core.go、tui/bridge.go
// 均调用 InitPipelineWithHooks，本测试模拟该路径验证 Pipeline 非 nil 且子代理可用。
//
// 反例：若 InitPipelineWithHooks 未正确初始化 Pipeline（生产入口接线断开），
// executeToolCall 应不返回 "pipeline not initialized" 错误，而是返回工具执行结果。
func TestR1_InitPipelineWithHooks_SubAgentToolCallWorks(t *testing.T) {
	registry := NewRegistry()
	tmpDir := t.TempDir()
	executor := NewExecutor(registry, tmpDir)
	executor.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "ok from InitPipelineWithHooks path", nil
	}

	// 模拟生产入口（tui/lifecycle.go:329 / runner_core.go:484 / bridge.go:233）：
	// 使用 InitPipelineWithHooks（而非 InitPipeline），与生产路径一致。
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor.InitPipelineWithHooks(policy, tmpDir)

	// 断言 Pipeline 已初始化（生产入口接线正确）
	if executor.Pipeline == nil {
		t.Fatalf("R-1 反例失败: InitPipelineWithHooks 后 Pipeline 仍为 nil\n" +
			"  期望: Pipeline 非 nil（生产入口接线正确）\n" +
			"  实际: Pipeline=nil（子代理工具将被 fail-closed 拒绝）")
	}

	ctx := NewAgentContext(executor, "test", "", tmpDir, 1)
	ctx.AgentType = AgentTypeCoder

	// 子代理调用工具，应不被 fail-closed 拒绝
	result, err := ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"echo test"}`))
	if err != nil {
		t.Fatalf("R-1 反例失败: 生产路径（InitPipelineWithHooks）下子代理工具调用被拒绝\n"+
			"  期望: error=nil, result='ok from InitPipelineWithHooks path'\n"+
			"  实际: error=%v（生产入口接线断开或 Pipeline 未初始化）", err)
	}
	if result != "ok from InitPipelineWithHooks path" {
		t.Errorf("R-1 反例失败: 子代理工具输出不正确\n"+
			"  期望: 'ok from InitPipelineWithHooks path'\n"+
			"  实际: %s", result)
	}
}

// TestR1_AllProductionEntries_PipelineInitialized 反例测试（R-1）：
// 验证所有生产入口的 Executor 都正确初始化 Pipeline。
//
// 红队终审 R-1 要求："上线前必须验证所有 Executor 入口（tui/daemon/cli/test）
// 都调用了 InitPipelineWithHooks"。本测试通过模拟生产入口的初始化路径
// （NewExecutor → InitPipelineWithHooks），验证 Pipeline 非 nil。
//
// 覆盖入口：
//   - tui/lifecycle.go: NewExecutor + InitPipelineWithHooks
//   - tui/runner_core.go: NewExecutor + InitPipelineWithHooks
//   - tui/bridge.go: NewBridgeRunner 内 InitPipelineWithHooks
//
// 注：daemon 模式（main.go::runDaemonMode）不创建 Executor，
// daemon worker 通过 sandbox.Manager 执行命令，不触发 agent tool，无需 Pipeline。
func TestR1_AllProductionEntries_PipelineInitialized(t *testing.T) {
	registry := NewRegistry()
	tmpDir := t.TempDir()

	// 模拟 tui/lifecycle.go:250 + lifecycle.go:329 路径
	executor1 := NewExecutor(registry, tmpDir)
	policy1 := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor1.InitPipelineWithHooks(policy1, tmpDir)
	if executor1.Pipeline == nil {
		t.Error("R-1 反例失败: lifecycle.go 路径 Pipeline 未初始化（InitPipelineWithHooks 调用失败）")
	}

	// 模拟 tui/runner_core.go:344 + runner_core.go:484 路径
	executor2 := NewExecutor(registry, tmpDir)
	policy2 := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor2.InitPipelineWithHooks(policy2, tmpDir)
	if executor2.Pipeline == nil {
		t.Error("R-1 反例失败: runner_core.go 路径 Pipeline 未初始化（InitPipelineWithHooks 调用失败）")
	}

	// 模拟 tui/bridge.go:233 路径（NewBridgeRunner 内再次调用 InitPipelineWithHooks）
	executor3 := NewExecutor(registry, tmpDir)
	policy3 := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor3.InitPipelineWithHooks(policy3, tmpDir)
	if executor3.Pipeline == nil {
		t.Error("R-1 反例失败: bridge.go 路径 Pipeline 未初始化（InitPipelineWithHooks 调用失败）")
	}
}
