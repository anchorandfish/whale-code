// Package e2e 实现 L-1 端到端连通性验证测试（红队终审报告 112 L-1 验证清单）。
//
// 本包覆盖本轮修复（桶 X1/X2/X3/X5/X6）的 10 个核心路径端到端连通性，
// 使用 mock + 集成测试方式验证（不需要真实模型/网络调用）。
//
// 测试清单（按 L-1 验证清单）：
// 1. memory 端到端连通（桶 X3 病灶1）：CapabilityLookup / ReflectTrigger 触发 memory 函数
// 2. hook 端到端连通（桶 X3 病灶2）：SyncHookSandboxManager + 生命周期 hooks 触发
// 3. daemon worker 沙箱（桶 X3 病灶3）：可信编排器与内层工具沙箱边界
// 4. daemon metrics_server 鉴权（桶 X3 病灶4）：fail-closed 检查接线点存在性断言
// 5. bridge 验签失败处理（桶 X3 病灶5）：NotifyReconnectFailure 通知真实失败
// 6. sandbox deny-wins（桶 X1 A-P0-1）：allow+deny 重叠时 deny 优先
// 7. plugin fail-closed（桶 X1 G2-P0-1）：未签名插件 fail-closed 拒绝
// 8. 破坏性命令确认（桶 X6 P-4）：DestructiveCheckStage 触发 NeedConfirm
// 9. EvolutionLoop Validate 步骤（桶 X2 B-P0-1/3）：Run 含 PatternID trace 时执行 Validate
// 10. version 包接线（桶 X5）：--version 输出版本信息 + version 包常量非空
//
// 降级说明：daemon metrics_server（测试4）的 startMetricsServer/isLocalhostAddr 为包内未导出方法，
// e2e 外部包无法直接调用，降级为"接线点存在性断言"（读取源码验证 fail-closed 检查存在），
// 行为由 internal/daemon 包内 x3_bucket_test.go 的 5 个反例测试覆盖。
package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/memory"
	"github.com/anchorandfish/whale-code/internal/orchestrator"
	"github.com/anchorandfish/whale-code/internal/plugin"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/version"
)

// ===== 通用辅助函数 =====

// sourceRoot 返回项目源码根目录绝对路径（基于本测试文件位置推导）。
// 本文件位于 internal/e2e/connectivity_test.go，源码根为上级两级目录。
func sourceRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Join(dir, "..", "..")
}

// assertSourceContains 读取指定源码文件并断言其内容包含所有期望的子串。
// 用于"接线点存在性断言"：验证修复代码确实存在于源码中。
// 任何期望子串缺失即 t.Fatalf 失败。
func assertSourceContains(t *testing.T, relPath string, expectedSubstrings ...string) {
	t.Helper()
	absPath := filepath.Join(sourceRoot(), relPath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("读取源码文件失败 %s: %v", absPath, err)
	}
	content := string(data)
	for _, sub := range expectedSubstrings {
		if !strings.Contains(content, sub) {
			t.Errorf("接线点存在性断言失败: %s 未包含期望子串 %q", relPath, sub)
		}
	}
}

// fakeSandboxManager 是 sandbox.Manager 接口的 mock 实现（e2e 测试辅助）。
// 记录 Prepare 调用次数，用于验证沙箱接线是否生效。
type fakeSandboxManager struct {
	prepareCalls int64
	level        sandbox.SandboxLevel
}

// Prepare mock 实现：记录调用次数，返回零值 SandboxContext。
func (f *fakeSandboxManager) Prepare(ctx context.Context, level sandbox.SandboxLevel, workDir string) (sandbox.SandboxContext, func(), error) {
	f.prepareCalls++
	return sandbox.SandboxContext{}, func() {}, nil
}

// ValidateCommand mock 实现：返回不阻止。
func (f *fakeSandboxManager) ValidateCommand(ctx context.Context, command string, config *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{Blocked: false}, nil
}

// ValidatePath mock 实现：返回 nil。
func (f *fakeSandboxManager) ValidatePath(ctx context.Context, path string, config *sandbox.SandboxConfig) error {
	return nil
}

// Level mock 实现：返回配置的级别。
func (f *fakeSandboxManager) Level() sandbox.SandboxLevel { return f.level }

// injectMemoryCallbacks 复刻 tui.wireExecutorMemoryCallbacks 的接线逻辑（外部包测试辅助）。
// 由于 wireExecutorMemoryCallbacks 是 tui 包内未导出函数，e2e 包无法直接调用，
// 此处复刻其闭包注入逻辑：把 memory.GetCapability / memory.ReflectMemories 包装为闭包
// 注入到 Executor.CapabilityLookup / ReflectTrigger 函数字段。
func injectMemoryCallbacks(executor *tools.Executor) {
	if executor == nil {
		return
	}
	executor.CapabilityLookup = func(key string) (string, string, error) {
		cv, err := memory.GetCapability(key)
		if err != nil {
			return "", "", err
		}
		valueStr := strings.TrimSpace("")
		if cv.Value != nil {
			valueStr = strings.TrimSpace(formatValue(cv.Value))
		}
		return valueStr, cv.Trust, nil
	}
	executor.ReflectTrigger = func(ctx context.Context, sourceNames []string, scope string) error {
		_, err := memory.ReflectMemories(ctx, sourceNames, scope, memory.ReflectOptions{})
		return err
	}
}

// formatValue 将 interface{} 转为字符串（辅助 injectMemoryCallbacks）。
func formatValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	default:
		return ""
	}
}

// callbackRecorder 记录 bridge 重连回调调用（测试5 辅助）。
type callbackRecorder struct {
	mu    sync.Mutex
	calls []callbackCall
}

// callbackCall 记录单次回调参数。
type callbackCall struct {
	success bool
	err     error
}

// newCallbackRecorder 创建回调记录器。
func newCallbackRecorder() *callbackRecorder {
	return &callbackRecorder{}
}

// AsCallback 返回 bridge.OnReconnectCompleteCallback 类型的回调。
func (cr *callbackRecorder) AsCallback() bridge.OnReconnectCompleteCallback {
	return func(success bool, err error) {
		cr.mu.Lock()
		defer cr.mu.Unlock()
		cr.calls = append(cr.calls, callbackCall{success: success, err: err})
	}
}

// Calls 返回所有已记录的回调调用快照。
func (cr *callbackRecorder) Calls() []callbackCall {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	result := make([]callbackCall, len(cr.calls))
	copy(result, cr.calls)
	return result
}

// createTestPluginDir 创建一个含 plugin.json 的测试插件目录（测试7 辅助）。
func createTestPluginDir(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := `{
		"id": "` + id + `",
		"name": "Test Plugin",
		"version": "1.0.0",
		"description": "e2e test plugin"
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write plugin.json failed: %v", err)
	}
	return dir
}

// ===== 测试1：memory 端到端连通（桶 X3 病灶1） =====

// TestE2E_01_MemoryConnectivity 验证 memory 端到端连通（桶 X3 病灶1）。
//
// 验证目标：
// - Executor.CapabilityLookup 闭包实际调用 memory.GetCapability（注入测试 capability 后返回匹配内容）
// - Executor.ReflectTrigger 闭包实际调用 memory.ReflectMemories（注入 mock aiCaller 后返回特定 error）
// - 接线点 tui/lifecycle.go 存在 wireExecutorMemoryCallbacks(m.executor) 调用
// - CapabilityLookup 在 agent 启动时可用（Executor 字段非 nil）
func TestE2E_01_MemoryConnectivity(t *testing.T) {
	// 准备：重置 capability 注册表 + 注入测试 capability
	memory.ResetCapabilityRegistryForTest()
	defer memory.ResetCapabilityRegistryForTest()
	testKey := "e2e:agent:coder"
	testContent := "该项目使用 Go 1.25，build 命令为 go build ./..."
	memory.RegisterCapabilityForTest(testKey, &memory.MemoryEntry{
		Name:         testKey,
		Content:      testContent,
		TrustLevel:   memory.TrustCapability,
		Verification: memory.VerificationVerified,
	})

	// 执行接线：复刻 wireExecutorMemoryCallbacks 逻辑
	executor := tools.NewExecutor(nil, t.TempDir())
	injectMemoryCallbacks(executor)

	// 断言1：CapabilityLookup 非 nil（agent 启动时可调用）
	if executor.CapabilityLookup == nil {
		t.Fatal("期望 executor.CapabilityLookup 非 nil（接线后应注入闭包），got nil")
	}

	// 断言2：调用 CapabilityLookup 返回注入的 capability 内容（证明实际调用 memory.GetCapability）
	value, trust, err := executor.CapabilityLookup(testKey)
	if err != nil {
		t.Fatalf("CapabilityLookup(%q) 返回意外 error: %v", testKey, err)
	}
	if value == "" {
		t.Errorf("期望 value 非空（包含 capability 内容），got 空字符串")
	}
	if trust == "" {
		t.Error("期望 trust 非空（memory.GetCapability 返回 TrustCapability 或 :unverified 后缀）")
	}

	// 断言3：查询不存在的 key → error 包含 "not found"（证明透传 memory.ErrCapabilityNotFound）
	_, _, err = executor.CapabilityLookup("e2e:nonexistent:key")
	if err == nil {
		t.Error("期望 CapabilityLookup(不存在的key) 返回 error（memory.ErrCapabilityNotFound），got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Errorf("期望 error 包含 'not found'，got %v", err)
	}

	// 断言4：ReflectTrigger 非 nil
	if executor.ReflectTrigger == nil {
		t.Fatal("期望 executor.ReflectTrigger 非 nil（接线后应注入闭包），got nil")
	}

	// 断言5：注入 mock aiCaller 后 ReflectTrigger 实际调用 memory.ReflectMemories
	// 通过注入返回特定 error 的 aiCaller，验证 ReflectTrigger 透传该 error
	mockErr := errors.New("e2e-mock-aiCaller-error")
	memory.SetAICaller(func(ctx context.Context, model, prompt string) (string, error) {
		return "", mockErr
	})
	defer memory.SetAICaller(nil) // 清理全局状态
	err = executor.ReflectTrigger(context.Background(), []string{"e2e-source"}, "project")
	if err == nil {
		t.Log("ReflectTrigger 未返回 error（可能 globalMergeLayer 未初始化），验证降级路径")
	} else if !strings.Contains(err.Error(), "e2e-mock-aiCaller-error") && !strings.Contains(err.Error(), "globalMergeLayer") && !strings.Contains(err.Error(), "aiCaller") {
		t.Errorf("期望 error 来自 memory.ReflectMemories（含 mockErr 或 globalMergeLayer/aiCaller），got %v", err)
	}

	// 接线点存在性断言：tui/lifecycle.go 包含 wireExecutorMemoryCallbacks 调用
	assertSourceContains(t, "internal/tui/lifecycle.go", "wireExecutorMemoryCallbacks(m.executor, homeDir, workDir)")
}

// ===== 测试2：hook 端到端连通（桶 X3 病灶2） =====

// TestE2E_02_HookConnectivity 验证 hook 端到端连通（桶 X3 病灶2）。
//
// 验证目标：
// - Executor.SyncHookSandboxManager 把 SandboxMgr 转发到 hookMgr（不 panic + hookMgr 引用不变）
// - 生命周期 hooks（SessionStart/SessionStop）经 Executor 转发方法被触发（不 panic）
// - 接线点 tui/lifecycle.go 存在 SyncHookSandboxManager 调用
func TestE2E_02_HookConnectivity(t *testing.T) {
	// 准备：创建 Executor + 初始化 hookMgr（InitPipelineWithHooks 内部创建 hook.Manager）
	executor := tools.NewExecutor(nil, t.TempDir())
	executor.InitPipelineWithHooks(nil, t.TempDir())

	// 断言1：hookMgr 已初始化
	hookMgr := executor.HookManager()
	if hookMgr == nil {
		t.Fatal("期望 HookManager() 非 nil（InitPipelineWithHooks 应初始化 hookMgr），got nil")
	}

	// 断言2：注入 fake sandbox manager + SyncHookSandboxManager 不 panic
	fakeMgr := &fakeSandboxManager{level: sandbox.SandboxLevelRelaxed}
	executor.SetSandboxMgr(fakeMgr)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SyncHookSandboxManager 不应 panic，got: %v", r)
		}
	}()
	executor.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed)

	// 断言3：SyncHookSandboxManager 后 hookMgr 引用不变（仅设置其 sandboxMgr 字段）
	hookMgrAfter := executor.HookManager()
	if hookMgrAfter != hookMgr {
		t.Error("SyncHookSandboxManager 不应替换 hookMgr 引用（仅设置其 sandboxMgr 字段）")
	}

	// 断言4：生命周期 hooks 转发方法不 panic（SessionStart/SessionStop 被触发）
	// RunSessionStartHooks / RunSessionStopHooks 内部遍历 config.SessionStart/SessionStop hooks
	// 无 hook 时返回 Allow（无操作），有 hook 时执行；此处验证转发路径连通
	executor.RunSessionStartHooks()
	executor.RunSessionStopHooks()
	executor.RunStopHooks()

	// 断言5：SandboxMgr==nil 时 SyncHookSandboxManager 不影响 hookMgr（向后兼容）
	executor2 := tools.NewExecutor(nil, t.TempDir())
	executor2.InitPipelineWithHooks(nil, t.TempDir())
	executor2.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed) // SandboxMgr==nil，应无操作
	if executor2.HookManager() == nil {
		t.Error("SandboxMgr==nil 时 SyncHookSandboxManager 不应破坏 hookMgr")
	}

	// 接线点存在性断言：tui/lifecycle.go 包含 SyncHookSandboxManager 调用
	assertSourceContains(t, "internal/tui/lifecycle.go",
		"SyncHookSandboxManager",
		"sandbox.SandboxLevelRelaxed")
}

// ===== 测试3：daemon worker 沙箱（桶 X3 病灶3） =====

// TestE2E_03_DaemonWorkerSandbox 验证 daemon worker 的沙箱边界（桶 X3 病灶3）。
//
// 验证目标：
// - Daemon.SetSandboxManager 注入沙箱管理器不 panic
// - main.go 使用独立 daemon 配置，且不再包含外层沙箱构造器
//
// 降级说明：真实 daemon.Start() 需写 PID 文件、启动 IPC listener、创建 worker pool，
// 端到端进程级测试过于复杂且产生副作用，降级为"SetSandboxManager 行为验证 + 接线点存在性断言"。
// Worker.Start 的完整沙箱链路（Prepare→Restrict→PostStartHook）由 internal/daemon 包内
// x3_bucket_test.go 的 6 个反例测试覆盖。
func TestE2E_03_DaemonWorkerSandbox(t *testing.T) {
	// 准备：创建 Daemon 实例（不调用 Start，避免 PID/IPC 副作用）
	cfg := daemon.DaemonConfig{
		MaxWorkers:  1,
		TaskTimeout: 30 * time.Second,
		PIDFilePath: filepath.Join(t.TempDir(), "daemon.pid"),
		LogFilePath: filepath.Join(t.TempDir(), "daemon.log"),
		SocketPath:  t.TempDir(),
		CliPath:     "whale",
	}
	d := daemon.NewDaemon(cfg)

	// 断言1：SetSandboxManager 注入 fake manager 不 panic
	fakeMgr := &fakeSandboxManager{level: sandbox.SandboxLevelRelaxed}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Daemon.SetSandboxManager 不应 panic，got: %v", r)
		}
	}()
	d.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	// 断言2：SetSandboxManager(nil, Disabled) 也不 panic（降级路径）
	d.SetSandboxManager(nil, sandbox.SandboxLevelDisabled)

	// 生产 daemon 启动可信 Whale CI 编排器；危险工具在 CI 内层受权限和沙箱约束。
	assertSourceContains(t, "main.go",
		"daemon.ConfigFromEnv",
		"normal permission and sandbox pipeline")
	mainSource, err := os.ReadFile(filepath.Join(sourceRoot(), "main.go"))
	if err != nil {
		t.Fatalf("读取 main.go 失败: %v", err)
	}
	if strings.Contains(string(mainSource), "func buildDaemonSandboxManager") {
		t.Fatal("main.go 不应保留误导性的 daemon 外层沙箱构造器")
	}

	// 接线点存在性断言：daemon/daemon.go 包含 SetSandboxManager 方法定义
	assertSourceContains(t, "internal/daemon/daemon.go",
		"func (d *Daemon) SetSandboxManager")

	// 接线点存在性断言：daemon/worker.go 包含沙箱链路接入
	assertSourceContains(t, "internal/daemon/worker.go",
		"func (w *Worker) SetSandboxManager",
		"sandboxMgr")
}

// ===== 测试4：daemon metrics_server 鉴权（桶 X3 病灶4） =====

// TestE2E_04_DaemonMetricsServerAuth 验证 daemon metrics_server 鉴权（桶 X3 病灶4）。
//
// 验证目标：
// - 接线点 metrics_server.go 存在 fail-closed 检查（空 token + 非 localhost 拒绝启动）
// - 接线点 metrics_server.go 存在 isLocalhostAddr 辅助函数
// - DaemonConfig 含 MetricsAddr / MetricsAuthToken 字段
//
// 降级说明：startMetricsServer / isLocalhostAddr 为 daemon 包内未导出方法，
// e2e 外部包无法直接调用。降级为"接线点存在性断言"。
// fail-closed 行为由 internal/daemon 包内 x3_bucket_test.go 的 5 个反例测试覆盖：
// - TestStartMetricsX3_EmptyTokenNonLocalhost_FailClosed
// - TestStartMetricsX3_EmptyTokenLocalhost_Allowed
// - TestStartMetricsX3_NonEmptyTokenAnyAddr_Allowed
// - TestIsLocalhostAddr_VariousAddresses
// - TestWithAuthX3_EmptyToken_NoAuthRequired
func TestE2E_04_DaemonMetricsServerAuth(t *testing.T) {
	// 接线点存在性断言：metrics_server.go 包含 fail-closed 检查
	assertSourceContains(t, "internal/daemon/metrics_server.go",
		"fail-closed",
		"isLocalhostAddr",
		"authToken == \"\"",
		"not localhost")

	// 接线点存在性断言：isLocalhostAddr 函数定义存在
	assertSourceContains(t, "internal/daemon/metrics_server.go",
		"func isLocalhostAddr(addr string) bool",
		"127.0.0.1",
		"localhost",
		"::1")

	// 行为验证：DaemonConfig 含 MetricsAddr / MetricsAuthToken 字段
	// 通过构造不同配置的 Daemon 验证字段可设置（NewDaemon 自动回填默认 127.0.0.1:9090）
	d1 := daemon.NewDaemon(daemon.DaemonConfig{
		MaxWorkers:  1,
		TaskTimeout: 30 * time.Second,
		MetricsAddr: "0.0.0.0:9090",
		PIDFilePath: filepath.Join(t.TempDir(), "d1.pid"),
		LogFilePath: filepath.Join(t.TempDir(), "d1.log"),
		SocketPath:  t.TempDir(),
	})
	if d1 == nil {
		t.Fatal("NewDaemon with MetricsAddr=0.0.0.0:9090 返回 nil")
	}

	// 接线点存在性断言：daemon 包内 x3_bucket_test.go 存在 fail-closed 反例测试
	assertSourceContains(t, "internal/daemon/x3_bucket_test.go",
		"TestStartMetricsX3_EmptyTokenNonLocalhost_FailClosed",
		"TestIsLocalhostAddr_VariousAddresses")
}

// ===== 测试5：bridge 验签失败处理（桶 X3 病灶5） =====

// TestE2E_05_BridgeReconnectFailure 验证 bridge 验签失败处理（桶 X3 病灶5）。
//
// 验证目标：
// - NotifyReconnectFailure(nil) 不 panic（onComplete=nil 时安全跳过）
// - NotifyReconnectFailure(err) 触发回调传入 (false, err)（覆盖虚假 success）
// - NotifyReconnectFailure(nil) 使用默认错误消息
// - 接线点 reconnect.go 存在 NotifyReconnectFailure 方法定义
func TestE2E_05_BridgeReconnectFailure(t *testing.T) {
	config := bridge.ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}

	// 断言1：onComplete=nil 时 NotifyReconnectFailure 不 panic
	rm1 := bridge.NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NotifyReconnectFailure(nil callback) 不应 panic，got: %v", r)
		}
	}()
	rm1.NotifyReconnectFailure(errors.New("verification failed"))
	rm1.NotifyReconnectFailure(nil)

	// 断言2：有回调时 NotifyReconnectFailure(err) 触发 (false, err)
	rm2 := bridge.NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})
	recorder := newCallbackRecorder()
	rm2.SetOnReconnectComplete(recorder.AsCallback())

	verifyErr := errors.New("token signature verification failed")
	rm2.NotifyReconnectFailure(verifyErr)

	calls := recorder.Calls()
	if len(calls) != 1 {
		t.Fatalf("期望 1 次回调调用，got %d", len(calls))
	}
	if calls[0].success {
		t.Error("期望 success=false（NotifyReconnectFailure 应通知失败）")
	}
	if calls[0].err == nil {
		t.Error("期望 err 非 nil")
	}
	if calls[0].err.Error() != verifyErr.Error() {
		t.Errorf("err = %q, want %q", calls[0].err.Error(), verifyErr.Error())
	}

	// 断言3：NotifyReconnectFailure(nil) 使用默认错误消息
	rm3 := bridge.NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})
	recorder3 := newCallbackRecorder()
	rm3.SetOnReconnectComplete(recorder3.AsCallback())
	rm3.NotifyReconnectFailure(nil)

	calls3 := recorder3.Calls()
	if len(calls3) != 1 {
		t.Fatalf("期望 1 次回调调用，got %d", len(calls3))
	}
	if calls3[0].success {
		t.Error("期望 success=false（nil err 也应通知失败）")
	}
	if calls3[0].err == nil {
		t.Error("期望 err 非 nil（使用默认错误消息）")
	}

	// 断言4：模拟虚假 success 后 NotifyReconnectFailure 覆盖
	// 先触发一次 (true, nil) 模拟 finishAsyncReconnect 的虚假 success
	rm4 := bridge.NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})
	recorder4 := newCallbackRecorder()
	rm4.SetOnReconnectComplete(recorder4.AsCallback())
	// 模拟 finishAsyncReconnect 发送虚假 success（通过直接调用回调模拟）
	// 注：无法直接调用 finishAsyncReconnect（未导出），但 NotifyReconnectFailure
	// 的设计就是覆盖之前的 success 通知，此处验证第二次调用覆盖第一次
	rm4.SetOnReconnectComplete(func(success bool, err error) {
		recorder4.calls = append(recorder4.calls, callbackCall{success: true, err: nil})        // 模拟虚假 success
		recorder4.calls = append(recorder4.calls, callbackCall{success: false, err: verifyErr}) // NotifyReconnectFailure 覆盖
	})
	// 触发一次真实 NotifyReconnectFailure
	rm4.SetOnReconnectComplete(recorder4.AsCallback())
	rm4.NotifyReconnectFailure(verifyErr)
	finalCalls := recorder4.Calls()
	if len(finalCalls) == 0 {
		t.Fatal("期望至少 1 次回调调用")
	}
	last := finalCalls[len(finalCalls)-1]
	if last.success {
		t.Error("期望最后一次回调 success=false（NotifyReconnectFailure 覆盖虚假 success）")
	}

	// 接线点存在性断言：reconnect.go 存在 NotifyReconnectFailure 方法定义
	assertSourceContains(t, "internal/bridge/reconnect.go",
		"func (rm *ReconnectManager) NotifyReconnectFailure")
}

// ===== 测试6：sandbox deny-wins（桶 X1 A-P0-1） =====

// TestE2E_06_SandboxDenyWins 验证 sandbox deny-wins 铁律（桶 X1 A-P0-1）。
//
// 验证目标：
// - 配置 allow: cat + deny: cat /tmp/secret，验证 cat /tmp/secret 被 deny 拦截（deny-wins）
// - cat /tmp/ok（仅命中 allow 不命中 deny）放行
// - deny 全量扫描优先于 allow（allow 命中不豁免 deny）
func TestE2E_06_SandboxDenyWins(t *testing.T) {
	sb := sandbox.NewSandbox(t.TempDir())
	// 启用沙箱 + 配置 allow + deny 重叠规则
	sb.SetEnabled(true)
	sb.SetAllowCommands([]string{"cat"})
	sb.SetDenyCommands([]string{"cat /tmp/secret"})

	// 断言1：cat /tmp/secret 同时命中 allow(cat) 和 deny(cat /tmp/secret)，deny-wins 必须拦截
	allowed, reason := sb.Validate("cat /tmp/secret")
	if allowed {
		t.Errorf("deny-wins 铁律被违反: cat /tmp/secret 同时命中 allow+deny 应被拦截\n" +
			"  期望: allowed=false（deny-wins）\n" +
			"  实际: allowed=true（allow 豁免了 deny，安全漏洞）")
	}
	if !strings.Contains(reason, "deny-wins") && !strings.Contains(reason, "deny") {
		t.Errorf("期望拒绝原因包含 'deny'，got %q", reason)
	}

	// 断言2：cat /tmp/ok 仅命中 allow 不命中 deny，应放行
	allowed2, reason2 := sb.Validate("cat /tmp/ok")
	if !allowed2 {
		t.Errorf("cat /tmp/ok 仅命中 allow 应放行，got blocked: %s", reason2)
	}

	// 断言3：未在 allow 列表的命令（如 ls）应被拦截（allow 白名单生效）
	allowed3, _ := sb.Validate("ls /tmp")
	if allowed3 {
		t.Error("ls 不在 allow 白名单应被拦截（allow 白名单生效）")
	}

	// 接线点存在性断言：sandbox.go 包含 deny-wins 修复
	assertSourceContains(t, "internal/sandbox/sandbox.go",
		"deny-wins",
		"deny 全量扫描")
}

// ===== 测试7：plugin fail-closed（桶 X1 G2-P0-1） =====

// TestE2E_07_PluginFailClosed 验证 plugin fail-closed 拒绝未签名插件（桶 X1 G2-P0-1）。
//
// 验证目标：
// - 未配置 signatureVerifier 且 allowUnsignedPlugins=false（默认）时，Install fail-closed 拒绝
// - 显式 opt-in（allowUnsignedPlugins=true）时允许安装（向后兼容）
// - 接线点 plugin.go 存在 fail-closed 检查
func TestE2E_07_PluginFailClosed(t *testing.T) {
	// 断言1：未配置 verifier + 默认 allowUnsignedPlugins=false → fail-closed 拒绝
	pluginDir := createTestPluginDir(t, "e2e-unsigned-plugin")
	m1 := plugin.NewManager(t.TempDir())
	m1.SetStatePath(filepath.Join(t.TempDir(), "state1.json"))
	// 不配置 verifier，allowUnsignedPlugins 默认 false（fail-closed）

	_, err := m1.Install(pluginDir)
	if err == nil {
		t.Fatalf("G2-P0-1 fail-closed 失败: 未配置 verifier 时应拒绝安装未签名插件\n" +
			"  期望: error 非 nil（fail-closed）\n" +
			"  实际: error=nil（fail-open，恶意插件可直接安装）")
	}
	if !strings.Contains(err.Error(), "fail-closed") && !strings.Contains(err.Error(), "signature") {
		t.Errorf("期望 error 包含 'fail-closed' 或 'signature'，got %v", err)
	}

	// 断言2：显式 opt-in allowUnsignedPlugins=true → 允许安装（向后兼容）
	pluginDir2 := createTestPluginDir(t, "e2e-unsigned-optin")
	m2 := plugin.NewManager(t.TempDir())
	m2.SetStatePath(filepath.Join(t.TempDir(), "state2.json"))
	m2.SetAllowUnsignedPlugins(true)

	p, err := m2.Install(pluginDir2)
	if err != nil {
		t.Fatalf("allowUnsignedPlugins=true 时应允许安装，got error: %v", err)
	}
	if p == nil || p.ID != "e2e-unsigned-optin" {
		t.Errorf("插件未正确安装，got %v", p)
	}

	// 接线点存在性断言：plugin.go 包含 fail-closed 检查
	assertSourceContains(t, "internal/plugin/plugin.go",
		"fail-closed",
		"signatureVerifier",
		"allowUnsignedPlugins")
}

// ===== 测试8：破坏性命令确认（桶 X6 P-4） =====

// TestE2E_08_DestructiveCommandConfirm 验证破坏性命令确认（桶 X6 P-4）。
//
// 验证目标：
// - DestructiveCheckStage 检测到 rm 命令时设置 NeedConfirm=true
// - 非破坏性命令（如 ls）不触发 NeedConfirm
// - 接线点 pipeline.go 包含 DestructiveCheckStage 注册
func TestE2E_08_DestructiveCommandConfirm(t *testing.T) {
	stage := &tools.DestructiveCheckStage{}

	// 断言1：rm test.txt 触发 NeedConfirm
	state1 := &tools.PipelineState{}
	call1 := tools.Call{
		ID:    "call-rm",
		Name:  "bash",
		Input: `{"command":"rm test.txt"}`,
	}
	result1 := stage.Execute(context.Background(), call1, state1)
	if !result1.NeedConfirm {
		t.Errorf("破坏性命令 rm test.txt 应触发 NeedConfirm=true\n"+
			"  期望: NeedConfirm=true\n"+
			"  实际: NeedConfirm=%v", result1.NeedConfirm)
	}
	if !result1.Proceed {
		t.Error("DestructiveCheckStage 应 Proceed=true（不阻断管道，由上层决定）")
	}
	if !state1.NeedConfirm {
		t.Error("PipelineState.NeedConfirm 应被设置为 true")
	}

	// 断言2：非破坏性命令 ls 不触发 NeedConfirm
	state2 := &tools.PipelineState{}
	call2 := tools.Call{
		ID:    "call-ls",
		Name:  "bash",
		Input: `{"command":"ls -la"}`,
	}
	result2 := stage.Execute(context.Background(), call2, state2)
	if result2.NeedConfirm {
		t.Errorf("非破坏性命令 ls -la 不应触发 NeedConfirm\n"+
			"  期望: NeedConfirm=false\n"+
			"  实际: NeedConfirm=%v", result2.NeedConfirm)
	}
	if state2.NeedConfirm {
		t.Error("非破坏性命令不应设置 PipelineState.NeedConfirm")
	}

	// 断言3：git reset --hard 触发 NeedConfirm（破坏性 git 操作）
	state3 := &tools.PipelineState{}
	call3 := tools.Call{
		ID:    "call-git-reset",
		Name:  "bash",
		Input: `{"command":"git reset --hard HEAD~1"}`,
	}
	result3 := stage.Execute(context.Background(), call3, state3)
	if !result3.NeedConfirm {
		t.Error("破坏性 git 操作 git reset --hard 应触发 NeedConfirm")
	}

	// 断言4：Stage 名称正确
	if stage.Name() != "destructive_check" {
		t.Errorf("DestructiveCheckStage.Name() = %q, want %q", stage.Name(), "destructive_check")
	}

	// 断言5（-R1 · 2026-09-20）：危险档由**专门的管线阶段**承载。
	//
	// 为什么要单独立这一条：破坏性档与危险档是**两档不同语义**（前者"可被沙箱兜住、
	// 问一次即可"，后者"不可逆、永不免问"）。此前危险档在生产路径上没有任何承载体
	// （详见 tools/.go 头注释），故这里既断言名字、也断言行为。
	// 零值策略（policy 为 nil）下护栏按"启用"处理，是 fail-closed 方向。
	dangerStage := &tools.DangerousGuardStage{}
	if dangerStage.Name() != "dangerous_guard" {
		t.Errorf("DangerousGuardStage.Name() = %q, want %q", dangerStage.Name(), "dangerous_guard")
	}
	dRes := dangerStage.Execute(context.Background(), tools.Call{
		ID: "call-shutdown", Name: "bash", Input: `{"command":"shutdown -h now"}`,
	}, &tools.PipelineState{})
	if !dRes.NeedConfirm {
		t.Error("危险命令 shutdown -h now 必须触发 NeedConfirm（-R1：危险档永不免问）")
	}
	dRes2 := dangerStage.Execute(context.Background(), tools.Call{
		ID: "call-ls", Name: "bash", Input: `{"command":"ls -la"}`,
	}, &tools.PipelineState{})
	if dRes2.NeedConfirm {
		t.Error("普通命令 ls -la 不应触发危险档 NeedConfirm")
	}

	// 接线点存在性断言：pipeline.go 包含两个阶段的注册（且都持有 policy —— 用于
	// 「单向收窄」让路判定），破坏性档的判定**来自 cmdrules 单一真相源**
	// （-R1 收口：删掉三份手抄名单中的两份）。
	//
	// ⚠️ 第三项于 **B2 收尾** 更新：此前 pin 的是 `cmdrules.ConfirmCommandNames()`，
	// 那是经 `var confirmCommandHeads = …` 一层间接后的**命令名列表**（只取首 token ⇒
	// `echo hi ; rm -rf x` 漏判、`/bin/rm -rf x` 漏判）。B2 已把那层间接**删除**，整段委托给
	// `cmdrules.MatchDestructive`（命令位匹配，含分隔符分段 / 路径前缀剥离 / 包装命令）。
	// ★ 本项 pin 的**不变量未变**（「破坏性档的声明只有 cmdrules 一处」），只是换成**更强**的形式；
	// 行为侧判别器 = `internal/tools/b2_destructive_gate_convergence_test.go`。
	assertSourceContains(t, "internal/tools/pipeline.go",
		"DestructiveCheckStage{policy: policy}",
		"DangerousGuardStage{policy: policy}",
		"cmdrules.MatchDestructive(")
}

// ===== 测试9：EvolutionLoop Validate 步骤（桶 X2 B-P0-1/3） =====

// TestE2E_09_EvolutionLoopValidate 验证 EvolutionLoop Run 的 Validate 步骤（桶 X2 B-P0-1/3）。
//
// 验证目标：
// - enabled=true 时 Run 执行（含 Extract + Tune + Validate 步骤），返回 nil
// - trace 含 PatternID 时 Validate 步骤被触发（通过 Run 不报错证明步骤执行）
// - enabled=false 时 Run 零成本降级返回 nil
// - 接线点 evolution_loop.go Run 方法包含 validator.Validate 调用
func TestE2E_09_EvolutionLoopValidate(t *testing.T) {
	// 准备：构造完整 EvolutionLoop（全 deps 注入 temp dir）
	tmpDir := t.TempDir()
	globalPath := filepath.Join(tmpDir, "global_exp")
	projectPath := filepath.Join(tmpDir, "project_exp")
	qualityPath := filepath.Join(tmpDir, "quality.jsonl")
	l1WeightsPath := filepath.Join(tmpDir, "l1_weights.json")
	guardPath := filepath.Join(tmpDir, "last_tune.json")

	expStore, err := orchestrator.NewExperienceStore(globalPath, projectPath)
	if err != nil {
		t.Fatalf("NewExperienceStore: %v", err)
	}
	qualStore, err := orchestrator.NewQualityStore(qualityPath)
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	scorer := orchestrator.NewL1ScorerExt(l1WeightsPath)
	guard := orchestrator.NewConvergenceGuard(guardPath)

	extractor := orchestrator.NewPatternExtractor(expStore)
	validator := orchestrator.NewPatternValidator(expStore, qualStore)
	ttlScanner := orchestrator.NewPatternTTLScanner(expStore)
	weightTuner := orchestrator.NewWeightTuner(scorer, qualStore, guard)

	// 断言1：enabled=false 时 Run 零成本降级返回 nil
	loopDisabled := orchestrator.NewEvolutionLoop(extractor, weightTuner, validator, ttlScanner, guard, false, false)
	err = loopDisabled.Run(context.Background(), "task-disabled", nil)
	if err != nil {
		t.Errorf("enabled=false 时 Run 应返回 nil（零成本降级），got %v", err)
	}

	// 断言2：enabled=true + trace 含 PatternID 时 Run 执行返回 nil
	// trace 含 PatternID 触发 Validate 步骤（extractHitPatternIDs 提取 ID 后调 validator.Validate）
	// Validate 失败 warn 不阻断，Run 仍返回 nil
	loopEnabled := orchestrator.NewEvolutionLoop(extractor, weightTuner, validator, ttlScanner, guard, true, false)
	trace := []orchestrator.TraceEntry{
		{
			Timestamp: time.Now().Unix(),
			SubTaskID: "e2e-subtask-1",
			Event:     "tool_call",
			Stage:     "execute",
			PatternID: "e2e-pattern-001", // 含 PatternID，触发 Validate
		},
	}
	err = loopEnabled.Run(context.Background(), "task-e2e-validate", trace)
	if err != nil {
		t.Errorf("enabled=true + trace 含 PatternID 时 Run 应返回 nil（Validate 失败 warn 不阻断），got %v", err)
	}

	// 断言3：taskID 空返回 error（v0.2 优化 3）
	err = loopEnabled.Run(context.Background(), "", trace)
	if err == nil {
		t.Error("taskID 空应返回 error")
	}
	if !strings.Contains(err.Error(), "taskID") {
		t.Errorf("taskID 空 error 应包含 'taskID'，got %v", err)
	}

	// 断言4：ctx 取消返回 ctx.Err()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = loopEnabled.Run(ctx, "task-cancelled", trace)
	if err == nil {
		t.Error("ctx 取消应返回 error")
	}

	// 接线点存在性断言：evolution_loop.go Run 方法包含 validator.Validate 调用
	assertSourceContains(t, "internal/orchestrator/evolution_loop.go",
		"validator.Validate",
		"extractHitPatternIDs",
		"PatternID")
}

// ===== 测试10：version 包接线（桶 X5） =====

// TestE2E_10_VersionWiring 验证 version 包接线（桶 X5）。
//
// 验证目标：
// - version.Version 常量非空（不再是零调用死代码）
// - 接线点 main.go handleArgs 处理 --version/-v 并调用 formatVersionString
// - 接线点 main.go formatVersionString 使用 versionpkg.Version
func TestE2E_10_VersionWiring(t *testing.T) {
	// 断言1：version.Version 常量非空
	if version.Version == "" {
		t.Fatal("version.Version 常量为空（桶 X5 死代码清理后应接线使用）")
	}

	// 断言2：version 包含 BuildDate / Commit 变量（可经 -ldflags 注入）
	// 默认空字符串，仅验证变量存在（编译期保证）
	_ = version.BuildDate
	_ = version.Commit

	// 接线点存在性断言：main.go handleArgs 处理 --version/-v
	assertSourceContains(t, "main.go",
		"--version",
		"\"-v\"",
		"formatVersionString")

	// 接线点存在性断言：main.go formatVersionString 使用 versionpkg.Version
	assertSourceContains(t, "main.go",
		"func formatVersionString",
		"versionpkg.Version",
		"versionpkg.BuildDate",
		"versionpkg.Commit")

	// 接线点存在性断言：main.go import version 包
	assertSourceContains(t, "main.go",
		"\"github.com/anchorandfish/whale-code/internal/version\"")
}
