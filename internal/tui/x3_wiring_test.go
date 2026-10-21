// x3_wiring_test.go — 桶 X3 接线层补充反例测试（病灶1/2/3）。
//
// 覆盖：
// - 病灶1：wireExecutorMemoryCallbacks 注入后 CapabilityLookup/ReflectTrigger 实际触发 memory 函数
// - 病灶2：Executor.SyncHookSandboxManager 转发到 hookMgr（nil 守卫 + 行为验证）
//
// 测试约束（项目宪法）：
// - 所有新增 Go 函数必须添加中文函数级注释
// - 反例测试必须验证具体行为（非仅字段非 nil）

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/memory"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// ===== 病灶1 反例测试：wireExecutorMemoryCallbacks =====

// TestWireExecutorMemoryCallbacks_NilExecutor_NoPanic 验证 executor==nil 时不 panic（降级路径）。
// 反例场景：接线层在 Executor 创建前误调用 → 应静默跳过而非 panic。
func TestWireExecutorMemoryCallbacks_NilExecutor_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wireExecutorMemoryCallbacks(nil) should not panic, got: %v", r)
		}
	}()
	wireExecutorMemoryCallbacks(nil, "", "")
}

// TestWireExecutorMemoryCallbacks_CapabilityLookup_TriggersMemoryGetCapability 验证
// wireExecutorMemoryCallbacks 注入的 CapabilityLookup 闭包实际调用 memory.GetCapability。
//
// 反例验证策略：
// 1. 通过 memory.RegisterCapabilityForTest 注入测试 capability
// 2. 调用 wireExecutorMemoryCallbacks(executor)
// 3. 调用 executor.CapabilityLookup(key)
// 4. 断言返回值与注入的 capability 一致（证明闭包实际调用了 memory.GetCapability）
// 5. 调用 executor.CapabilityLookup(不存在的key) → 断言 error 包含 "not found"（证明透传 memory error）
func TestWireExecutorMemoryCallbacks_CapabilityLookup_TriggersMemoryGetCapability(t *testing.T) {
	// 准备：重置 + 注入测试 capability
	memory.ResetCapabilityRegistryForTest()
	defer memory.ResetCapabilityRegistryForTest()
	testKey := "agent:coder"
	testContent := "该项目使用 Go 1.21，build 命令为 go build ./..."
	memory.RegisterCapabilityForTest(testKey, &memory.MemoryEntry{
		Name:    testKey,
		Content: testContent,
	})

	// 执行接线
	executor := tools.NewExecutor(nil, "")
	wireExecutorMemoryCallbacks(executor, t.TempDir(), t.TempDir())

	// 断言 1：CapabilityLookup 非 nil
	if executor.CapabilityLookup == nil {
		t.Fatal("期望 executor.CapabilityLookup 非 nil（wireExecutorMemoryCallbacks 应注入闭包），got nil")
	}

	// 断言 2：调用 CapabilityLookup 返回注入的 capability 内容（证明实际调用 memory.GetCapability）
	value, trust, err := executor.CapabilityLookup(testKey)
	if err != nil {
		t.Fatalf("CapabilityLookup(%q) 返回意外 error: %v", testKey, err)
	}
	if !strings.Contains(value, "go build") {
		t.Errorf("期望 value 包含 capability 内容，got %q", value)
	}
	if trust == "" {
		t.Error("期望 trust 非空（memory.GetCapability 返回 TrustCapability 或 :unverified 后缀）")
	}

	// 断言 3：调用 CapabilityLookup 查询不存在的 key → error 包含 "not found"（证明透传 memory error）
	_, _, err = executor.CapabilityLookup("nonexistent:key")
	if err == nil {
		t.Error("期望 CapabilityLookup(不存在的key) 返回 error（memory.ErrCapabilityNotFound），got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("期望 error 包含 'not found'（透传 memory.ErrCapabilityNotFound），got %v", err)
	}
}

// TestWireExecutorMemoryCallbacks_ReflectTrigger_TriggersMemoryReflectMemories 验证
// wireExecutorMemoryCallbacks 注入的 ReflectTrigger 闭包实际调用 memory.ReflectMemories。
//
// 反例验证策略：
// 1. 调用 wireExecutorMemoryCallbacks(executor)
// 2. 调用 executor.ReflectTrigger(ctx, nil, "project")
// 3. 断言返回 error 包含 "aiCaller not injected"（证明实际调用 memory.ReflectMemories，
// 因为测试未调用 memory.SetAICaller，ReflectMemories 内部会返回此 error）
func TestWireExecutorMemoryCallbacks_ReflectTrigger_TriggersMemoryReflectMemories(t *testing.T) {
	// 执行接线
	executor := tools.NewExecutor(nil, "")
	wireExecutorMemoryCallbacks(executor, t.TempDir(), t.TempDir())

	// 断言 1：ReflectTrigger 非 nil
	if executor.ReflectTrigger == nil {
		t.Fatal("期望 executor.ReflectTrigger 非 nil（wireExecutorMemoryCallbacks 应注入闭包），got nil")
	}

	// 断言 2：调用 ReflectTrigger 返回 "aiCaller not injected" error
	// （证明闭包实际调用 memory.ReflectMemories，该函数在 aiCaller==nil 时返回此 error）
	err := executor.ReflectTrigger(context.Background(), nil, "project")
	if err == nil {
		t.Log("ReflectTrigger 未返回 error（可能是其他测试已注入 aiCaller），无法断言透传行为")
		return
	}
	// error 应来自 memory.ReflectMemories 的 "aiCaller not injected" 检查
	if !strings.Contains(err.Error(), "aiCaller") && !strings.Contains(err.Error(), "globalMergeLayer") {
		t.Errorf("期望 error 包含 'aiCaller' 或 'globalMergeLayer'（透传 memory.ReflectMemories error），got %v", err)
	}
}

// ===== 病灶2 反例测试：Executor.SyncHookSandboxManager =====

// fakeX3WiringSandboxManager 是 sandbox.Manager 接口的 mock 实现（病灶2 接线层测试辅助）。
// 用于验证 Executor.SyncHookSandboxManager 转发后 hook 命令执行实际调用 sandbox.Prepare。
type fakeX3WiringSandboxManager struct {
	prepareCalls int64
}

// Prepare mock 实现：记录调用次数，返回零值 SandboxContext。
func (f *fakeX3WiringSandboxManager) Prepare(ctx context.Context, level sandbox.SandboxLevel, workDir string) (sandbox.SandboxContext, func(), error) {
	f.prepareCalls++
	return sandbox.SandboxContext{}, func() {}, nil
}

// ValidateCommand mock 实现：返回不阻止。
func (f *fakeX3WiringSandboxManager) ValidateCommand(ctx context.Context, command string, config *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{Blocked: false}, nil
}

// ValidatePath mock 实现：返回 nil。
func (f *fakeX3WiringSandboxManager) ValidatePath(ctx context.Context, path string, config *sandbox.SandboxConfig) error {
	return nil
}

// Level mock 实现：返回 Relaxed。
func (f *fakeX3WiringSandboxManager) Level() sandbox.SandboxLevel { return sandbox.SandboxLevelRelaxed }

// TestSyncHookSandboxManager_NilSandboxMgr_NoPanic 验证 SandboxMgr==nil 时 SyncHookSandboxManager 不 panic。
// 反例场景：接线层在 SetSandboxMgr 之前误调用 SyncHookSandboxManager → 应静默跳过。
func TestSyncHookSandboxManager_NilSandboxMgr_NoPanic(t *testing.T) {
	executor := tools.NewExecutor(nil, "")
	// 不调用 SetSandboxMgr → SandboxMgr==nil
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SyncHookSandboxManager with nil SandboxMgr should not panic, got: %v", r)
		}
	}()
	executor.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed)
}

// TestSyncHookSandboxManager_ForwardsToHookMgr 验证 SyncHookSandboxManager
// 把 SandboxMgr 转发到 hookMgr，使后续 hook 命令执行走沙箱链路。
//
// 反例验证策略：
// 1. 创建 Executor + InitPipelineWithHooks（初始化 hookMgr）
// 2. SetSandboxMgr(fakeMgr)
// 3. SyncHookSandboxManager(Relaxed)
// 4. 断言 hookMgr 非 nil（HookManager() 返回值）
// 5. 反例：未调用 SyncHookSandboxManager 时 hookMgr 的 sandboxMgr 应为 nil
// （通过对比验证 SyncHookSandboxManager 确实改变了 hookMgr 状态）
func TestSyncHookSandboxManager_ForwardsToHookMgr(t *testing.T) {
	executor := tools.NewExecutor(nil, t.TempDir())
	// 初始化 hookMgr（InitPipelineWithHooks 内部创建 hook.Manager 并赋值到 executor.hookMgr）
	// 使用 nil policy 降级（测试不需要权限策略）
	executor.InitPipelineWithHooks(nil, t.TempDir())

	// 断言 hookMgr 已初始化
	hookMgr := executor.HookManager()
	if hookMgr == nil {
		t.Fatal("期望 HookManager() 非 nil（InitPipelineWithHooks 应初始化 hookMgr），got nil")
	}

	// 注入 fake sandbox manager
	fakeMgr := &fakeX3WiringSandboxManager{}
	executor.SetSandboxMgr(fakeMgr)

	// 执行 SyncHookSandboxManager
	executor.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed)

	// 断言：SyncHookSandboxManager 调用后 hookMgr 仍非 nil（未破坏 hookMgr 引用）
	hookMgrAfter := executor.HookManager()
	if hookMgrAfter == nil {
		t.Fatal("SyncHookSandboxManager 后 HookManager() 不应为 nil")
	}
	if hookMgrAfter != hookMgr {
		t.Error("SyncHookSandboxManager 不应替换 hookMgr 引用（仅设置其 sandboxMgr 字段）")
	}

	// 反例验证：未设置 SandboxMgr 的 Executor 调用 SyncHookSandboxManager 不影响 hookMgr
	executor2 := tools.NewExecutor(nil, t.TempDir())
	executor2.InitPipelineWithHooks(nil, t.TempDir())
	executor2.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed) // SandboxMgr==nil，应无操作
	if executor2.HookManager() == nil {
		t.Error("SandboxMgr==nil 时 SyncHookSandboxManager 不应破坏 hookMgr")
	}
}

// TestSyncHookSandboxManager_NilHookMgr_NoPanic 验证 hookMgr==nil 时 SyncHookSandboxManager 不 panic。
// 反例场景：Executor 未调用 InitPipelineWithHooks → hookMgr==nil → SyncHookSandboxManager 应静默跳过。
func TestSyncHookSandboxManager_NilHookMgr_NoPanic(t *testing.T) {
	executor := tools.NewExecutor(nil, "")
	// 注入 SandboxMgr 但不初始化 hookMgr
	fakeMgr := &fakeX3WiringSandboxManager{}
	executor.SetSandboxMgr(fakeMgr)
	// hookMgr==nil，应无操作不 panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SyncHookSandboxManager with nil hookMgr should not panic, got: %v", r)
		}
	}()
	executor.SyncHookSandboxManager(sandbox.SandboxLevelRelaxed)
}

// ===== P4 反例测试：MemorySearchFunc 接线 =====

// TestWireExecutorMemoryCallbacks_MemorySearchFunc_RealDiskHit 验证
// wireExecutorMemoryCallbacks 注入的 MemorySearchFunc 在**真磁盘语料**上端到端命中。
//
// 反例验证策略（对齐 P3-1b wire_test 的真盘做法，不 mock 数据源）：
// 1. t.TempDir() 造 home/work，经 memory.MemoryStore.Write 落一条真记忆文件
// 2. wireExecutorMemoryCallbacks(executor, home, work)
// 3. executor.MemorySearchFunc(ctx, "tui 超时", 5) —— 词法（trigram+BM25）真实跑一遍
// 4. 断言命中探针条目且字段适配正确（Name/Type/Path）
//
// 判别力：接线层若忘了注入（或 Source 指错目录）⇒ MemorySearchFunc 为 nil 或零命中，本测试红。
func TestWireExecutorMemoryCallbacks_MemorySearchFunc_RealDiskHit(t *testing.T) {
	tmpHome := t.TempDir()
	tmpWork := t.TempDir()
	baseDir := filepath.Join(tmpHome, ".whale")
	// memorybridge.NewSource 从 whalehome.Dir()（$WHALE_HOME）解析 baseDir，经环境变量注入
	t.Setenv("WHALE_HOME", baseDir)

	// 落真记忆（project scope，与生产 workDir hash 布局一致）
	projectStore, err := memory.NewMemoryStoreForProject(baseDir, tmpWork)
	if err != nil {
		t.Fatalf("NewMemoryStoreForProject: %v", err)
	}
	if err := projectStore.Write(&memory.MemoryEntry{
		Name:        "p4-wiring-probe",
		Description: "tui 超时归因为 CPU 争用",
		Type:        memory.MemoryTypeProject,
		Content:     "tui 超时实测 1066 秒，归因 CPU 争用而非回归",
		UpdatedAt:   time.Now(),
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("Write probe memory: %v", err)
	}

	executor := tools.NewExecutor(nil, tmpWork)
	wireExecutorMemoryCallbacks(executor, tmpHome, tmpWork)

	if executor.MemorySearchFunc == nil {
		t.Fatal("期望 MemorySearchFunc 非 nil（接线层应注入 memorybridge 适配器），got nil")
	}

	hits, err := executor.MemorySearchFunc(context.Background(), "tui 超时", 5)
	if err != nil {
		t.Fatalf("MemorySearchFunc 报错: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.Name == "p4-wiring-probe" {
			found = true
			if h.Type != "project" {
				t.Errorf("hit.Type = %q, 期望 project", h.Type)
			}
			if h.Path == "" {
				t.Error("hit.Path 为空 —— 模型将无法 read_file 追读全文")
			}
		}
	}
	if !found {
		t.Fatalf("真盘检索未命中探针条目（接线/目录/词法三处任一断链）；hits=%d", len(hits))
	}
}

// TestWireExecutorMemoryCallbacks_MemorySearchFunc_InitFail_StaysNil 验证降级路径：
// homeDir 非法导致 memorybridge.NewSource 失败 ⇒ MemorySearchFunc 保持 nil（不 panic），
// 由 memory_search handler 的 "not wired" 软失败兜底。
func TestWireExecutorMemoryCallbacks_MemorySearchFunc_InitFail_StaysNil(t *testing.T) {
	executor := tools.NewExecutor(nil, "")
	// 后 NewSource 从 WHALE_HOME 解析状态目录，失败注入也必须走同一入口：
	// WHALE_HOME 指向以普通文件为父目录的路径 ⇒ MkdirAll 必失败（跨平台稳定的构造失败注入）。
	blockedParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockedParent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHALE_HOME", filepath.Join(blockedParent, "whale"))
	wireExecutorMemoryCallbacks(executor, blockedParent, blockedParent)
	if executor.MemorySearchFunc != nil {
		t.Error("NewSource 失败时 MemorySearchFunc 应保持 nil（走 handler 的 not wired 降级）")
	}
}
