// Package hook — Hook 沙箱集成测试
//
// 测试 applySandbox 与 executeCommandHook 的沙箱集成：
// - sandboxMgr=nil 跳过沙箱（向后兼容）
// - Prepare 失败降级执行（fail-open）
// - Restrict 失败降级执行（fail-open）
// - per-hook SandboxLevel 配置
// - defaultSandboxLevel 全局默认
// - mock Windows Job Object 路径
// - 沙箱环境变量覆盖
// - cleanup 调用验证
// - 并发执行沙箱隔离
package hook

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// fakeOSSandbox 是 OSSandbox 接口的 mock 实现（测试辅助）。
//
// 用于验证 applySandbox 调用 OSSandbox.Restrict 的行为：
// - enterErr: 模拟 Enter 失败
// - restrictErr: 模拟 Restrict 失败
// - restrictCalls: 记录 Restrict 调用次数（atomic，并发安全）
// - platform: 模拟平台标识（windows-jobobject / linux-namespaces / darwin-seatbelt / fake）
type fakeOSSandbox struct {
	enterErr      error
	restrictErr   error
	restrictCalls int64
	platform      string
}

// Enter 进入 mock 沙箱（返回 nil 句柄 + noop cleanup）。
func (f *fakeOSSandbox) Enter() (interface{}, func(), error) {
	if f.enterErr != nil {
		return nil, nil, f.enterErr
	}
	return nil, func() {}, nil
}

// Exit 退出 mock 沙箱（noop，幂等）。
func (f *fakeOSSandbox) Exit() error { return nil }

// Restrict 应用 mock 隔离到 cmd（记录调用次数 + 可选返回错误）。
func (f *fakeOSSandbox) Restrict(cmd *exec.Cmd) error {
	atomic.AddInt64(&f.restrictCalls, 1)
	return f.restrictErr
}

// Platform 返回 mock 平台标识。
func (f *fakeOSSandbox) Platform() string {
	if f.platform == "" {
		return "fake"
	}
	return f.platform
}

// PostStartHook 返回 nil（mock 实现无需 cmd.Start 后钩子）。
func (f *fakeOSSandbox) PostStartHook() func(*exec.Cmd) error {
	return nil
}

// fakeSandboxManager 是 sandbox.Manager 接口的 mock 实现（测试辅助）。
//
// 用于验证 applySandbox 调用 Manager.Prepare 的行为：
// - os: 注入的 OSSandbox 实例（通过 NewSandboxContextForTesting 填充 _osSandbox）
// - prepareErr: 模拟 Prepare 失败
// - prepareCalls: 记录 Prepare 调用次数（atomic，并发安全）
// - envOverride: 模拟沙箱环境变量（覆盖 buildHookEnv）
// - workDirOverride: 模拟沙箱工作目录（覆盖 m.workDir）
// - cleanupCalled: 记录 cleanup 调用次数（atomic，并发安全）
type fakeSandboxManager struct {
	os              sandbox.OSSandbox
	prepareErr      error
	prepareCalls    int64
	envOverride     []string
	workDirOverride string
	cleanupCalled   int64
}

// Prepare 返回 mock SandboxContext（子审查优化 3 吸纳：用 NewSandboxContextForTesting 填充 _osSandbox）。
func (f *fakeSandboxManager) Prepare(ctx context.Context, level sandbox.SandboxLevel, workDir string) (sandbox.SandboxContext, func(), error) {
	atomic.AddInt64(&f.prepareCalls, 1)
	if f.prepareErr != nil {
		return sandbox.SandboxContext{}, nil, f.prepareErr
	}
	sctx := sandbox.NewSandboxContextForTesting(workDir, f.envOverride, f.os)
	if f.workDirOverride != "" {
		sctx.WorkDir = f.workDirOverride
	}
	cleanup := func() {
		atomic.AddInt64(&f.cleanupCalled, 1)
	}
	return sctx, cleanup, nil
}

// ValidateCommand mock 实现（返回不阻止）。
func (f *fakeSandboxManager) ValidateCommand(ctx context.Context, command string, config *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{Blocked: false}, nil
}

// ValidatePath mock 实现（返回 nil）。
func (f *fakeSandboxManager) ValidatePath(ctx context.Context, path string, config *sandbox.SandboxConfig) error {
	return nil
}

// Level 返回 mock 默认级别。
func (f *fakeSandboxManager) Level() sandbox.SandboxLevel { return sandbox.SandboxLevelRelaxed }

// TestHookSandbox_Disabled 测试 sandboxMgr=nil 时跳过沙箱（向后兼容， SNAPSHOT）。
//
// 验证：
// - applySandbox 返回 nil cleanup + nil err
// - cmd.Env / cmd.Dir 未被修改
func TestHookSandbox_Disabled(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	// 不调用 SetSandboxManager → sandboxMgr=nil → 跳过沙箱

	h := &HookCommand{
		HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelStrict},
		Command:  "echo hello",
	}
	cmd := exec.Command("echo", "hello")
	originalEnv := []string{"ORIGINAL=1"}
	cmd.Env = originalEnv
	cmd.Dir = "/original/dir"

	ctx := context.Background()
	cleanup, _, err := m.applySandbox(ctx, cmd, h)
	if cleanup != nil {
		t.Errorf("expected nil cleanup when sandboxMgr=nil, got non-nil")
		cleanup()
	}
	if err != nil {
		t.Errorf("expected nil err when sandboxMgr=nil, got: %v", err)
	}
	// cmd.Env 不应被修改
	if len(cmd.Env) != 1 || cmd.Env[0] != "ORIGINAL=1" {
		t.Errorf("cmd.Env should not be modified: got %v", cmd.Env)
	}
	// cmd.Dir 不应被修改
	if cmd.Dir != "/original/dir" {
		t.Errorf("cmd.Dir should not be modified: got %q", cmd.Dir)
	}
}

// TestHookSandbox_PrepareFail 测试 Prepare 失败时降级执行（fail-open， SNAPSHOT）。
//
// Relaxed 级别采用 fail-open 策略：Prepare 失败时返回 nil error（降级执行，不修改 cmd）。
// Strict/Isolated 级别才采用 fail-closed 策略（返回 non-nil error 拒绝执行）。
// fail-closed 策略由 TestHookSandbox_PrepareFail_Strict 覆盖。
//
// 验证：
// - applySandbox 返回 nil cleanup + nil err（fail-open 降级）
// - Prepare 被调用 1 次
// - cmd 未被修改（降级执行）
func TestHookSandbox_PrepareFail(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	fakeMgr := &fakeSandboxManager{prepareErr: errors.New("prepare failed")}
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	h := &HookCommand{
		HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelRelaxed},
		Command:  "echo hello",
	}
	cmd := exec.Command("echo", "hello")
	originalEnv := []string{"ORIGINAL=1"}
	cmd.Env = originalEnv

	ctx := context.Background()
	cleanup, _, err := m.applySandbox(ctx, cmd, h)
	if cleanup != nil {
		t.Errorf("expected nil cleanup on Prepare failure, got non-nil")
		cleanup()
	}
	if err != nil {
		t.Errorf("expected nil err on Prepare failure (fail-open for Relaxed): got %v", err)
	}
	if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
		t.Errorf("expected 1 Prepare call, got %d", fakeMgr.prepareCalls)
	}
	// cmd.Env 不应被修改（降级执行）
	if len(cmd.Env) != 1 || cmd.Env[0] != "ORIGINAL=1" {
		t.Errorf("cmd.Env should not be modified on Prepare failure: got %v", cmd.Env)
	}
}

// TestHookSandbox_RestrictFail 测试 Restrict 失败时降级执行（fail-open， SNAPSHOT）。
//
// Relaxed 级别采用 fail-open 策略：Restrict 失败时返回 nil error（降级执行，不修改 cmd）。
// Strict/Isolated 级别才采用 fail-closed 策略（返回 non-nil error 拒绝执行）。
// fail-closed 策略由 TestHookSandbox_RestrictFail_Strict 覆盖。
//
// 验证：
// - applySandbox 返回 nil cleanup + nil err（fail-open 降级）
// - Restrict 被调用 1 次
// - cleanup 被调用（prepCleanup 在 Restrict 失败时立即执行）
func TestHookSandbox_RestrictFail(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	fakeOS := &fakeOSSandbox{restrictErr: errors.New("restrict failed")}
	fakeMgr := &fakeSandboxManager{os: fakeOS}
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	h := &HookCommand{
		HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelRelaxed},
		Command:  "echo hello",
	}
	cmd := exec.Command("echo", "hello")

	ctx := context.Background()
	cleanup, _, err := m.applySandbox(ctx, cmd, h)
	if cleanup != nil {
		t.Errorf("expected nil cleanup on Restrict failure, got non-nil")
		cleanup()
	}
	if err != nil {
		t.Errorf("expected nil err on Restrict failure (fail-open for Relaxed): got %v", err)
	}
	if atomic.LoadInt64(&fakeOS.restrictCalls) != 1 {
		t.Errorf("expected 1 Restrict call, got %d", fakeOS.restrictCalls)
	}
	// prepCleanup 应在 Restrict 失败时被调用（释放已 Prepare 的资源）
	if atomic.LoadInt64(&fakeMgr.cleanupCalled) != 1 {
		t.Errorf("expected 1 cleanup call on Restrict failure, got %d", fakeMgr.cleanupCalled)
	}
}

// TestHookSandbox_PerHookLevel 测试 per-hook SandboxLevel 配置（SNAPSHOT）。
//
// 子审查优化 5 吸纳：覆盖 disabled 跳过 / strict 启用。
//
// 验证：
// - hook.SandboxLevel=disabled + default=disabled → 跳过沙箱
// - hook.SandboxLevel=strict + default=disabled → 用 hook 级别（strict）
// - hook.SandboxLevel=disabled + default=relaxed → 用默认级别（relaxed）
func TestHookSandbox_PerHookLevel(t *testing.T) {
	tmpDir := t.TempDir()

	// 场景 1：两者均 disabled → 跳过沙箱
	t.Run("BothDisabled_SkipSandbox", func(t *testing.T) {
		m := NewManager(tmpDir)
		fakeMgr := &fakeSandboxManager{}
		m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelDisabled)

		h := &HookCommand{
			HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelDisabled},
			Command:  "echo hello",
		}
		cmd := exec.Command("echo", "hello")
		ctx := context.Background()
		cleanup, _, _ := m.applySandbox(ctx, cmd, h)
		if cleanup != nil {
			t.Errorf("expected nil cleanup when both disabled")
			cleanup()
		}
		if atomic.LoadInt64(&fakeMgr.prepareCalls) != 0 {
			t.Errorf("expected 0 Prepare calls when both disabled, got %d", fakeMgr.prepareCalls)
		}
	})

	// 场景 2：hook.SandboxLevel=strict + default=disabled → 用 hook 级别
	t.Run("HookLevelStrict_DefaultDisabled_UseHookLevel", func(t *testing.T) {
		m := NewManager(tmpDir)
		fakeOS := &fakeOSSandbox{}
		fakeMgr := &fakeSandboxManager{os: fakeOS}
		m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelDisabled)

		h := &HookCommand{
			HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelStrict},
			Command:  "echo hello",
		}
		cmd := exec.Command("echo", "hello")
		ctx := context.Background()
		cleanup, _, err := m.applySandbox(ctx, cmd, h)
		if err != nil {
			t.Errorf("unexpected err: %v", err)
		}
		if cleanup == nil {
			t.Error("expected non-nil cleanup when hook.SandboxLevel=strict")
		} else {
			cleanup()
		}
		if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
			t.Errorf("expected 1 Prepare call, got %d", fakeMgr.prepareCalls)
		}
		if atomic.LoadInt64(&fakeOS.restrictCalls) != 1 {
			t.Errorf("expected 1 Restrict call, got %d", fakeOS.restrictCalls)
		}
	})

	// 场景 3：hook.SandboxLevel=disabled + default=relaxed → 用默认级别
	t.Run("HookLevelDisabled_DefaultRelaxed_UseDefault", func(t *testing.T) {
		m := NewManager(tmpDir)
		fakeOS := &fakeOSSandbox{}
		fakeMgr := &fakeSandboxManager{os: fakeOS}
		m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

		h := &HookCommand{
			HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelDisabled},
			Command:  "echo hello",
		}
		cmd := exec.Command("echo", "hello")
		ctx := context.Background()
		cleanup, _, err := m.applySandbox(ctx, cmd, h)
		if err != nil {
			t.Errorf("unexpected err: %v", err)
		}
		if cleanup == nil {
			t.Error("expected non-nil cleanup when default=relaxed")
		} else {
			cleanup()
		}
		if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
			t.Errorf("expected 1 Prepare call, got %d", fakeMgr.prepareCalls)
		}
	})
}

// TestHookSandbox_DefaultLevel 测试 defaultSandboxLevel 全局默认（SNAPSHOT）。
//
// 验证：
// - SetDefaultSandboxLevel 运行时调整默认级别
// - hook.SandboxLevel=disabled 时 fallback 到 defaultSandboxLevel
func TestHookSandbox_DefaultLevel(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	fakeOS := &fakeOSSandbox{}
	fakeMgr := &fakeSandboxManager{os: fakeOS}
	// 初始默认 disabled
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelDisabled)

	h := &HookCommand{
		HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelDisabled},
		Command:  "echo hello",
	}
	cmd := exec.Command("echo", "hello")
	ctx := context.Background()

	// 初始：两者均 disabled → 跳过沙箱
	cleanup, _, _ := m.applySandbox(ctx, cmd, h)
	if cleanup != nil {
		t.Error("expected nil cleanup when both disabled")
		cleanup()
	}

	// 运行时调整默认级别为 relaxed
	m.SetDefaultSandboxLevel(sandbox.SandboxLevelRelaxed)

	// 调整后：hook=disabled + default=relaxed → 用默认级别
	cleanup2, _, err := m.applySandbox(ctx, cmd, h)
	if err != nil {
		t.Errorf("unexpected err after SetDefaultSandboxLevel: %v", err)
	}
	if cleanup2 == nil {
		t.Error("expected non-nil cleanup after SetDefaultSandboxLevel(relaxed)")
	} else {
		cleanup2()
	}
	if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
		t.Errorf("expected 1 Prepare call after SetDefaultSandboxLevel, got %d", fakeMgr.prepareCalls)
	}
}

// TestHookSandbox_WindowsJobObject 测试 mock Windows Job Object 路径（SNAPSHOT）。
//
// 子审查优化 3 吸纳：覆盖 Windows Job Object 路径。
// 官方源：[learn.microsoft.com/en-us/windows/win32/procthread/job-objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects) @2026-07-21
//
// 验证：
// - OSSandbox.Platform() 返回 "windows-jobobject"
// - Restrict 被调用
func TestHookSandbox_WindowsJobObject(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	fakeOS := &fakeOSSandbox{platform: "windows-jobobject"}
	fakeMgr := &fakeSandboxManager{os: fakeOS}
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	h := &HookCommand{
		HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelRelaxed},
		Command:  "echo hello",
	}
	cmd := exec.Command("echo", "hello")
	ctx := context.Background()

	cleanup, _, err := m.applySandbox(ctx, cmd, h)
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if cleanup == nil {
		t.Error("expected non-nil cleanup")
	} else {
		cleanup()
	}
	if fakeOS.Platform() != "windows-jobobject" {
		t.Errorf("expected platform windows-jobobject, got %q", fakeOS.Platform())
	}
	if atomic.LoadInt64(&fakeOS.restrictCalls) != 1 {
		t.Errorf("expected 1 Restrict call, got %d", fakeOS.restrictCalls)
	}
}

// TestHookSandbox_EnvOverride 测试沙箱环境变量覆盖 buildHookEnv（SNAPSHOT）。
//
// 验证：
// - sctx.Env 非空时覆盖 cmd.Env（C15 隔离）
func TestHookSandbox_EnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	sandboxEnv := []string{"PATH=/sandbox/bin", "HOME=/sandbox/home", "HOOK_SANDBOXED=1"}
	fakeOS := &fakeOSSandbox{}
	fakeMgr := &fakeSandboxManager{os: fakeOS, envOverride: sandboxEnv}
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	h := &HookCommand{
		HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelRelaxed},
		Command:  "echo hello",
	}
	cmd := exec.Command("echo", "hello")
	cmd.Env = []string{"ORIGINAL=1"} // buildHookEnv 的结果
	ctx := context.Background()

	cleanup, _, err := m.applySandbox(ctx, cmd, h)
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if cleanup == nil {
		t.Error("expected non-nil cleanup")
	} else {
		defer cleanup()
	}
	// cmd.Env 应被沙箱环境变量覆盖
	if len(cmd.Env) != 3 {
		t.Fatalf("expected 3 env vars, got %d: %v", len(cmd.Env), cmd.Env)
	}
	expected := map[string]bool{"PATH=/sandbox/bin": false, "HOME=/sandbox/home": false, "HOOK_SANDBOXED=1": false}
	for _, e := range cmd.Env {
		expected[e] = true
	}
	for env, found := range expected {
		if !found {
			t.Errorf("expected env %q in cmd.Env, not found: %v", env, cmd.Env)
		}
	}
}

// TestHookSandbox_CleanupCalled 测试 cleanup 在 hook 执行后被调用（SNAPSHOT）。
//
// 验证：
// - ExecuteHook 执行后 cleanup 被调用 1 次
// - hook 正常执行（输出正确）
func TestHookSandbox_CleanupCalled(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	fakeOS := &fakeOSSandbox{}
	fakeMgr := &fakeSandboxManager{os: fakeOS}
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	h := &HookCommand{
		HookBase: HookBase{
			Matcher:      "*",
			Action:       ActionAllow,
			SandboxLevel: sandbox.SandboxLevelRelaxed,
		},
		Command: "echo cleanup_test",
	}

	result := m.ExecuteHook(h, "")
	if result.Error != nil {
		t.Errorf("hook execution failed: %v", result.Error)
	}
	// cleanup 应在 hook 执行后被调用
	if atomic.LoadInt64(&fakeMgr.cleanupCalled) != 1 {
		t.Errorf("expected 1 cleanup call after ExecuteHook, got %d", fakeMgr.cleanupCalled)
	}
	// Restrict 应被调用
	if atomic.LoadInt64(&fakeOS.restrictCalls) != 1 {
		t.Errorf("expected 1 Restrict call, got %d", fakeOS.restrictCalls)
	}
	// hook 输出应包含 "cleanup_test"
	output := strings.TrimSpace(result.Output)
	if !strings.Contains(output, "cleanup_test") {
		t.Errorf("expected output to contain 'cleanup_test', got %q", output)
	}
}

// TestHookSandbox_Concurrent 测试并发 hook 执行沙箱隔离（SNAPSHOT）。
//
// 验证：
// - 10 goroutine × 10 hook 并发执行
// - 无 race（-race 测试）
// - 所有 cleanup 被调用
// - 所有 Restrict 被调用
func TestHookSandbox_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	fakeOS := &fakeOSSandbox{}
	fakeMgr := &fakeSandboxManager{os: fakeOS}
	m.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	const goroutines = 10
	const hooksPerGoroutine = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < hooksPerGoroutine; j++ {
				h := &HookCommand{
					HookBase: HookBase{
						Matcher:      "*",
						Action:       ActionAllow,
						SandboxLevel: sandbox.SandboxLevelRelaxed,
					},
					Command: "echo concurrent",
				}
				_ = m.ExecuteHook(h, "")
			}
		}()
	}
	wg.Wait()

	totalHooks := int64(goroutines * hooksPerGoroutine)
	if atomic.LoadInt64(&fakeMgr.prepareCalls) != totalHooks {
		t.Errorf("expected %d Prepare calls, got %d", totalHooks, atomic.LoadInt64(&fakeMgr.prepareCalls))
	}
	if atomic.LoadInt64(&fakeOS.restrictCalls) != totalHooks {
		t.Errorf("expected %d Restrict calls, got %d", totalHooks, atomic.LoadInt64(&fakeOS.restrictCalls))
	}
	if atomic.LoadInt64(&fakeMgr.cleanupCalled) != totalHooks {
		t.Errorf("expected %d cleanup calls, got %d", totalHooks, atomic.LoadInt64(&fakeMgr.cleanupCalled))
	}
}

// TestHookSandbox_BackwardCompat 测试向后兼容：未启用沙箱时 ExecuteHook 行为不变（SNAPSHOT）。
//
// 验证：
// - 不调用 SetSandboxManager 时，hook 执行与现状一致
// - hook 输出正确
// - 无沙箱调用
func TestHookSandbox_BackwardCompat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("跳过 Windows 平台的 sh -c 命令测试")
	}
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	// 不调用 SetSandboxManager → sandboxMgr=nil → 向后兼容

	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "*",
			Action:  ActionAllow,
		},
		Command: "echo backward_compat",
	}

	result := m.ExecuteHook(h, "")
	if result.Error != nil {
		t.Errorf("hook execution failed: %v", result.Error)
	}
	output := strings.TrimSpace(result.Output)
	if !strings.Contains(output, "backward_compat") {
		t.Errorf("expected output to contain 'backward_compat', got %q", output)
	}
}
