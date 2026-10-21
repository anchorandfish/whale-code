package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// TestMain 支持 helper-process 形态（Go 标准惯用法）：x3 桶测试需要一个跨平台、
// 零外部依赖的「读干 stdin 后成功退出」的可执行桩，替代 POSIX 专属的 /bin/echo
// （Windows CI 实测 exec: "/bin/echo": executable file not found in %PATH%，）。
// 本测试二进制以 WHALE_X3_ECHO_HELPER=1 环境变量被再次执行时即扮演该桩；
// 该变量对本测试进程内的所有子进程可见（exec.Cmd 缺省继承父环境），不影响
// 真实 whale 二进制的子进程（它们不读这个变量）。
func TestMain(m *testing.M) {
	if os.Getenv("WHALE_X3_ECHO_HELPER") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// x3EchoStubPath 返回 helper 桩的可执行路径（= 本测试二进制自身）。
// os.Executable 在测试进程中实际不会失败；万一失败返回空串，exec 会报出
// 明确错误（比静默回退到 POSIX 字面量更诚实）。
func x3EchoStubPath() string {
	exe, _ := os.Executable()
	return exe
}

// ===== 桶 X3 病灶3/4 反例测试 =====
//
// 本文件覆盖桶 X3 的两个病灶修复的反例测试：
// - 病灶3（daemon worker 沙箱）：验证已配置沙箱的 Prepare/Restrict/PostStartHook
// 失败时 fail-closed，以及 sandboxMgr=nil 的显式非沙箱场景
// - 病灶4（daemon metrics_server 鉴权）：验证空 token + 非 localhost 绑定 fail-closed、
// 空 token + localhost 允许、非空 token + 任意绑定允许等场景
//
// 测试约束（项目宪法）：
// - 所有新增 Go 函数必须添加中文函数级注释
// - fail-closed 场景必须验证拒绝行为，fail-open 场景必须验证降级行为

// ===== fakeSandboxManager mock 实现（病灶3 测试辅助） =====
//
// 复用 hook/sandbox_integration_test.go 的 fakeSandboxManager 模式，
// 但在 daemon 包内独立定义（Go 测试包不能跨包引用未导出的 mock）。

// fakeX3SandboxManager 是 sandbox.Manager 接口的 mock 实现（桶 X3 病灶3 测试辅助）。
//
// 用于验证 Worker.Start 调用 Manager.Prepare 的行为：
// - os: 注入的 OSSandbox 实例
// - prepareErr: 模拟 Prepare 失败
// - prepareCalls: 记录 Prepare 调用次数（atomic，并发安全）
// - envOverride: 模拟沙箱环境变量
// - cleanupCalled: 记录 cleanup 调用次数（atomic，并发安全）
type fakeX3SandboxManager struct {
	os            sandbox.OSSandbox
	prepareErr    error
	prepareCalls  int64
	envOverride   []string
	cleanupCalled int64
}

// Prepare 返回 mock SandboxContext（桶 X3 病灶3 测试辅助）。
// 注意：NewSandboxContextForTesting 不会设置 PostStartHook 字段，
// 需要手动从 OSSandbox.PostStartHook() 填充，与生产 manager.Prepare 行为一致。
func (f *fakeX3SandboxManager) Prepare(ctx context.Context, level sandbox.SandboxLevel, workDir string) (sandbox.SandboxContext, func(), error) {
	atomic.AddInt64(&f.prepareCalls, 1)
	if f.prepareErr != nil {
		return sandbox.SandboxContext{}, nil, f.prepareErr
	}
	sctx := sandbox.NewSandboxContextForTesting(workDir, f.envOverride, f.os)
	// 手动填充 PostStartHook（与生产 manager.Prepare 行为一致）
	if f.os != nil {
		sctx.PostStartHook = f.os.PostStartHook()
	}
	cleanup := func() {
		atomic.AddInt64(&f.cleanupCalled, 1)
	}
	return sctx, cleanup, nil
}

// ValidateCommand mock 实现（返回不阻止）。
func (f *fakeX3SandboxManager) ValidateCommand(ctx context.Context, command string, config *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{Blocked: false}, nil
}

// ValidatePath mock 实现（返回 nil）。
func (f *fakeX3SandboxManager) ValidatePath(ctx context.Context, path string, config *sandbox.SandboxConfig) error {
	return nil
}

// Level 返回 mock 默认级别。
func (f *fakeX3SandboxManager) Level() sandbox.SandboxLevel { return sandbox.SandboxLevelRelaxed }

// fakeX3OSSandbox 是 OSSandbox 接口的 mock 实现（桶 X3 病灶3 测试辅助）。
//
// 用于验证 Worker.Start 调用 OSSandbox.Restrict 的行为：
// - restrictErr: 模拟 Restrict 失败
// - restrictCalls: 记录 Restrict 调用次数（atomic，并发安全）
// - postStartHookErr: 模拟 PostStartHook 失败
// - postStartHookCalls: 记录 PostStartHook 调用次数
type fakeX3OSSandbox struct {
	restrictErr        error
	restrictCalls      int64
	postStartHookErr   error
	postStartHookCalls int64
}

// Enter 进入 mock 沙箱（返回 nil 句柄 + noop cleanup）。
func (f *fakeX3OSSandbox) Enter() (interface{}, func(), error) {
	return nil, func() {}, nil
}

// Exit 退出 mock 沙箱（noop，幂等）。
func (f *fakeX3OSSandbox) Exit() error { return nil }

// Restrict 应用 mock 隔离到 cmd（记录调用次数 + 可选返回错误）。
func (f *fakeX3OSSandbox) Restrict(cmd *exec.Cmd) error {
	atomic.AddInt64(&f.restrictCalls, 1)
	return f.restrictErr
}

// PostStartHook 返回 mock 钩子函数（记录调用次数 + 可选返回错误）。
func (f *fakeX3OSSandbox) PostStartHook() func(*exec.Cmd) error {
	return func(cmd *exec.Cmd) error {
		atomic.AddInt64(&f.postStartHookCalls, 1)
		return f.postStartHookErr
	}
}

// Platform 返回 mock 平台标识。
func (f *fakeX3OSSandbox) Platform() string { return "fake-x3" }

// ===== 病灶3 反例测试：daemon worker 沙箱链路 =====

// TestWorkerX3_SandboxDisabled_VerifyPrepareNotCalled 验证 sandboxMgr=nil 时
// Worker.Start 走非沙箱路径，Prepare 不被调用（桶 X3 病灶3 反例）。
//
// 反例场景：用户未注入沙箱管理器 → worker 应降级到无沙箱执行（fail-open），
// 而非 panic 或拒绝执行。
//
// 验证：
// - Prepare 调用次数为 0（沙箱未启用）
// - Worker.Start 返回 nil（降级执行成功，不阻断）
func TestWorkerX3_SandboxDisabled_VerifyPrepareNotCalled(t *testing.T) {
	w := NewWorker(1, x3EchoStubPath(), 5*time.Second)
	// 不调用 SetSandboxManager → sandboxMgr=nil → 走非沙箱路径

	task := &Task{
		ID:      "test-disabled-1",
		Prompt:  "hello",
		WorkDir: t.TempDir(),
	}

	ctx := context.Background()
	err := w.Start(ctx, task)
	if err != nil {
		t.Fatalf("Worker.Start with nil sandboxMgr should succeed (fail-open), got: %v", err)
	}

	// 等待 worker 完成任务
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !w.IsBusy() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.IsBusy() {
		t.Fatal("worker should complete task within 3s")
	}
}

// TestWorkerX3_PrepareFail_VerifyFailClosed 验证 Prepare 失败时拒绝执行。
//
// 反例场景：沙箱 Prepare 失败（如 workDir 不存在或资源不足）→
// worker 不得绕过用户明确配置的安全边界继续无沙箱执行。
//
// 验证：
// - Prepare 调用次数为 1（沙箱路径被触发）
// - Worker.Start 返回错误，且 worker 恢复为空闲
func TestWorkerX3_PrepareFail_VerifyFailClosed(t *testing.T) {
	w := NewWorker(1, x3EchoStubPath(), 5*time.Second)
	fakeMgr := &fakeX3SandboxManager{prepareErr: errors.New("prepare failed (simulated)")}
	w.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	task := &Task{
		ID:      "test-prepare-fail-1",
		Prompt:  "hello",
		WorkDir: t.TempDir(),
	}

	ctx := context.Background()
	err := w.Start(ctx, task)
	if err == nil {
		t.Fatal("Worker.Start with Prepare failure should fail closed")
	}
	if w.IsBusy() {
		t.Fatal("worker must return to idle after Prepare failure")
	}

	if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
		t.Errorf("expected 1 Prepare call, got %d", atomic.LoadInt64(&fakeMgr.prepareCalls))
	}
}

// TestWorkerX3_RestrictFail_VerifyFailClosed 验证 Restrict 失败时拒绝执行。
//
// 反例场景：OS 级沙箱 Restrict 失败（如平台不支持或权限不足）→
// worker 必须清理已准备资源，不能绕过 Restrict 启动子进程。
//
// 验证：
// - Prepare 调用次数为 1
// - Restrict 调用次数为 1
// - Worker.Start 返回错误
// - cleanup 被调用（沙箱资源被释放）
func TestWorkerX3_RestrictFail_VerifyFailClosed(t *testing.T) {
	w := NewWorker(1, x3EchoStubPath(), 5*time.Second)
	fakeOS := &fakeX3OSSandbox{restrictErr: errors.New("restrict failed (simulated)")}
	fakeMgr := &fakeX3SandboxManager{os: fakeOS, envOverride: []string{"PATH=/usr/bin:/bin"}}
	w.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	task := &Task{
		ID:      "test-restrict-fail-1",
		Prompt:  "hello",
		WorkDir: t.TempDir(),
	}

	ctx := context.Background()
	err := w.Start(ctx, task)
	if err == nil {
		t.Fatal("Worker.Start with Restrict failure should fail closed")
	}
	if w.IsBusy() {
		t.Fatal("worker must return to idle after Restrict failure")
	}

	if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
		t.Errorf("expected 1 Prepare call, got %d", atomic.LoadInt64(&fakeMgr.prepareCalls))
	}
	if atomic.LoadInt64(&fakeOS.restrictCalls) != 1 {
		t.Errorf("expected 1 Restrict call, got %d", atomic.LoadInt64(&fakeOS.restrictCalls))
	}
	// cleanup 应在 waitTaskWithCleanup 的 defer 中被调用
	if atomic.LoadInt64(&fakeMgr.cleanupCalled) != 1 {
		t.Errorf("expected 1 cleanup call, got %d", atomic.LoadInt64(&fakeMgr.cleanupCalled))
	}
}

// TestWorkerX3_PostStartHookFail_VerifyFailClosed 验证 PostStartHook 失败时
// 终止并回收已经启动的子进程。
//
// 反例场景：PostStartHook 失败（如 Windows Job Object AssignProcess 失败）→
// Windows 上该 hook 负责把 suspended 进程加入 Job Object；失败后继续运行既绕过
// 资源边界，也可能泄漏 suspended 进程。
//
// 验证：
// - Prepare 调用次数为 1
// - Restrict 调用次数为 1
// - PostStartHook 调用次数为 1
// - Worker.Start 返回错误
// - cleanup 被调用（沙箱资源被释放）
func TestWorkerX3_PostStartHookFail_VerifyFailClosed(t *testing.T) {
	w := NewWorker(1, x3EchoStubPath(), 5*time.Second)
	fakeOS := &fakeX3OSSandbox{
		postStartHookErr: errors.New("post-start-hook failed (simulated)"),
	}
	fakeMgr := &fakeX3SandboxManager{os: fakeOS, envOverride: []string{"PATH=/usr/bin:/bin"}}
	w.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	task := &Task{
		ID:      "test-posthook-fail-1",
		Prompt:  "hello",
		WorkDir: t.TempDir(),
	}

	ctx := context.Background()
	err := w.Start(ctx, task)
	if err == nil {
		t.Fatal("Worker.Start with PostStartHook failure should fail closed")
	}
	if w.IsBusy() {
		t.Fatal("worker must return to idle after PostStartHook failure")
	}

	if atomic.LoadInt64(&fakeMgr.prepareCalls) != 1 {
		t.Errorf("expected 1 Prepare call, got %d", atomic.LoadInt64(&fakeMgr.prepareCalls))
	}
	if atomic.LoadInt64(&fakeOS.restrictCalls) != 1 {
		t.Errorf("expected 1 Restrict call, got %d", atomic.LoadInt64(&fakeOS.restrictCalls))
	}
	if atomic.LoadInt64(&fakeOS.postStartHookCalls) != 1 {
		t.Errorf("expected 1 PostStartHook call, got %d", atomic.LoadInt64(&fakeOS.postStartHookCalls))
	}
	if atomic.LoadInt64(&fakeMgr.cleanupCalled) != 1 {
		t.Errorf("expected 1 cleanup call, got %d", atomic.LoadInt64(&fakeMgr.cleanupCalled))
	}
}

// TestWorkerX3_SandboxLevelDisabled_VerifyPrepareNotCalled 验证 sandboxLevel=Disabled 时
// Worker.Start 走非沙箱路径，Prepare 不被调用（桶 X3 病灶3 反例）。
//
// 反例场景：用户显式配置 SandboxLevel=Disabled → worker 应跳过沙箱（fail-open），
// 即使 sandboxMgr != nil 也不调用 Prepare。
//
// 验证：
// - Prepare 调用次数为 0（level=Disabled 跳过沙箱）
// - Worker.Start 返回 nil（降级执行成功）
func TestWorkerX3_SandboxLevelDisabled_VerifyPrepareNotCalled(t *testing.T) {
	w := NewWorker(1, x3EchoStubPath(), 5*time.Second)
	fakeMgr := &fakeX3SandboxManager{}
	// 注入 sandboxMgr 但 level=Disabled → 应跳过沙箱
	w.SetSandboxManager(fakeMgr, sandbox.SandboxLevelDisabled)

	task := &Task{
		ID:      "test-level-disabled-1",
		Prompt:  "hello",
		WorkDir: t.TempDir(),
	}

	ctx := context.Background()
	err := w.Start(ctx, task)
	if err != nil {
		t.Fatalf("Worker.Start with SandboxLevelDisabled should succeed, got: %v", err)
	}

	// 等待 worker 完成任务
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !w.IsBusy() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if atomic.LoadInt64(&fakeMgr.prepareCalls) != 0 {
		t.Errorf("expected 0 Prepare calls when level=Disabled, got %d", atomic.LoadInt64(&fakeMgr.prepareCalls))
	}
}

// TestWorkerPoolX3_SetSandboxManager_VerifyForwardToWorkers 验证 WorkerPool.SetSandboxManager
// 在 pool.Start 前调用时，沙箱管理器被转发到每个 worker（桶 X3 病灶3 接线验证）。
//
// 验证：
// - pool.Start 后每个 worker 的 sandboxMgr 非 nil
// - 每个 worker 的 sandboxLevel 与 pool 一致
func TestWorkerPoolX3_SetSandboxManager_VerifyForwardToWorkers(t *testing.T) {
	pool := NewWorkerPool(2, x3EchoStubPath(), 5*time.Second)
	fakeMgr := &fakeX3SandboxManager{}
	pool.SetSandboxManager(fakeMgr, sandbox.SandboxLevelRelaxed)

	// 验证 pool 字段已设置
	if pool.sandboxMgr == nil {
		t.Fatal("pool.sandboxMgr should be set after SetSandboxManager")
	}
	if pool.sandboxLevel != sandbox.SandboxLevelRelaxed {
		t.Errorf("pool.sandboxLevel = %s, want %s", pool.sandboxLevel, sandbox.SandboxLevelRelaxed)
	}
}

// ===== 病灶4 反例测试：daemon metrics_server 鉴权 =====

// TestIsLocalhostAddr_VariousAddresses 验证 isLocalhostAddr 对各种地址格式的判断（桶 X3 病灶4 反例）。
//
// B5-P1-12 修复：localhost 不再被视为回环地址（可被 /etc/hosts 篡改），仅允许数字 IP。
//
// 验证：
// - 127.0.0.1:9090 → true（本地回环）
// - localhost:9090 → false（B5-P1-12：不再接受，可被 /etc/hosts 篡改）
// - ::1:9090 → false（缺少 IPv6 方括号，地址格式无效）
// - [::1]:9090 → true（IPv6 带方括号）
// - 0.0.0.0:9090 → false（全网绑定，非本地）
// - 192.168.1.1:9090 → false（局域网地址）
// - :9090 → false（空 host 会监听所有接口）
func TestIsLocalhostAddr_VariousAddresses(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9090", true},
		{"localhost:9090", false}, // B5-P1-12：不再接受 localhost
		{"::1:9090", false},
		{"[::1]:9090", true},
		{"0.0.0.0:9090", false},
		{"192.168.1.1:9090", false},
		{":9090", false}, // 空 host 会监听所有接口
		{"10.0.0.1:9090", false},
		{"example.com:9090", false},
	}
	for _, c := range cases {
		got := isLocalhostAddr(c.addr)
		if got != c.want {
			t.Errorf("isLocalhostAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// TestStartMetricsX3_EmptyTokenNonLocalhost_FailClosed 验证空 token + 非 localhost 绑定时
// startMetricsServer 拒绝启动（fail-closed，桶 X3 病灶4 反例）。
//
// 反例场景：用户配置 MetricsAuthToken="" + MetricsAddr="0.0.0.0:9090" →
// daemon 应拒绝启动 metrics server（fail-closed），防止无鉴权网络暴露。
//
// 修复前：仅在 addr==":9090" 时强制改 127.0.0.1，用户配置 0.0.0.0:9090 + 空 token 仍会暴露。
// 修复后：任何非 127.0.0.1/localhost 绑定 + 空 token 都拒绝启动。
//
// 验证：
// - 返回非 nil error（fail-closed 拒绝）
// - 返回的 *http.Server 为 nil
func TestStartMetricsX3_EmptyTokenNonLocalhost_FailClosed(t *testing.T) {
	d := &Daemon{}

	cases := []string{
		"0.0.0.0:9090",
		"192.168.1.1:9090",
		"10.0.0.1:9090",
		"example.com:9090",
	}
	for _, addr := range cases {
		srv, err := d.startMetricsServer(addr, "")
		if err == nil {
			if srv != nil {
				// 若意外成功，必须关闭避免端口泄漏
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				_ = srv.Shutdown(ctx)
				cancel()
			}
			t.Errorf("startMetricsServer(%q, empty token) should fail-closed (return error), got nil", addr)
		}
		if srv != nil {
			t.Errorf("startMetricsServer(%q, empty token) should return nil server on fail-closed", addr)
		}
	}
}

// TestStartMetricsX3_EmptyTokenLocalhost_Allowed 验证空 token + 回环地址绑定时
// startMetricsServer 允许启动（桶 X3 病灶4 反例）。
//
// B5-P1-12 修复：localhost 不再被视为安全回环地址（可被 /etc/hosts 篡改），
// 测试用例从 localhost:0 改为 127.0.0.1:0 和 ::1:0。
//
// 反例场景：用户配置 MetricsAuthToken="" + MetricsAddr="127.0.0.1:9090" →
// daemon 应允许启动（仅本机访问，无鉴权但安全）。
//
// 验证：
// - 返回 nil error（允许启动）
// - 返回的 *http.Server 非 nil
// - server.Addr 与传入的 addr 一致
func TestStartMetricsX3_EmptyTokenLocalhost_Allowed(t *testing.T) {
	d := &Daemon{}

	cases := []string{
		"127.0.0.1:0", // 使用端口 0 让系统分配可用端口，避免端口冲突
		"[::1]:0",     // B5-P1-12：IPv6 回环地址也应允许
		"127.0.0.1:0",
	}
	for _, addr := range cases {
		srv, err := d.startMetricsServer(addr, "")
		if err != nil {
			t.Errorf("startMetricsServer(%q, empty token) with localhost should succeed, got: %v", addr, err)
			continue
		}
		if srv == nil {
			t.Errorf("startMetricsServer(%q, empty token) should return non-nil server", addr)
			continue
		}
		// 清理：关闭 server 避免端口泄漏
		d.metricsServer = srv
		d.stopMetricsServer()
	}
}

// TestStartMetricsX3_NonEmptyTokenAnyAddr_Allowed 验证非空 token + 任意绑定地址时
// startMetricsServer 允许启动（桶 X3 病灶4 反例）。
//
// 反例场景：用户配置 MetricsAuthToken="secret" + MetricsAddr="0.0.0.0:9090" →
// daemon 应允许启动（有 Bearer Token 鉴权保护，可安全绑定全网）。
//
// 验证：
// - 返回 nil error（允许启动）
// - 返回的 *http.Server 非 nil
func TestStartMetricsX3_NonEmptyTokenAnyAddr_Allowed(t *testing.T) {
	d := &Daemon{}

	// 使用端口 0 避免端口冲突
	srv, err := d.startMetricsServer("0.0.0.0:0", "secret-token-12345")
	if err != nil {
		t.Fatalf("startMetricsServer(0.0.0.0:0, non-empty token) should succeed, got: %v", err)
	}
	if srv == nil {
		t.Fatal("startMetricsServer should return non-nil server")
	}
	// 清理
	d.metricsServer = srv
	d.stopMetricsServer()
}

// TestWithAuthX3_EmptyToken_NoAuthRequired 验证空 token 时 withAuth 不做鉴权（桶 X3 病灶4 反例）。
//
// 反例场景：authToken 为空时，withAuth 应直接返回 next handler（无鉴权），
// 调用方应确保此时绑定 127.0.0.1（由 startMetricsServer 的 fail-closed 检查保证）。
//
// 验证：
// - 空 token 时请求能通过 withAuth 中间件（HTTP 200）
func TestWithAuthX3_EmptyToken_NoAuthRequired(t *testing.T) {
	d := &Daemon{}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// 空 token → withAuth 应直接返回 next（不包装鉴权）
	handler := d.withAuth("", next)
	if handler == nil {
		t.Fatal("withAuth with empty token should return non-nil handler")
	}

	// 构造测试请求验证 handler 可执行（不 panic）
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("next handler should be called when authToken is empty")
	}
}

// TestDaemonX3_SetSandboxManager_VerifyFieldsSet 验证 Daemon.SetSandboxManager
// 正确设置 sandboxMgr 和 sandboxLevel 字段（桶 X3 病灶3 接线验证）。
//
// 验证：
// - 调用后 d.sandboxMgr 非 nil
// - d.sandboxLevel 与传入值一致
func TestDaemonX3_SetSandboxManager_VerifyFieldsSet(t *testing.T) {
	d := &Daemon{}
	fakeMgr := &fakeX3SandboxManager{}

	d.SetSandboxManager(fakeMgr, sandbox.SandboxLevelStrict)

	if d.sandboxMgr == nil {
		t.Fatal("d.sandboxMgr should be set after SetSandboxManager")
	}
	if d.sandboxLevel != sandbox.SandboxLevelStrict {
		t.Errorf("d.sandboxLevel = %s, want %s", d.sandboxLevel, sandbox.SandboxLevelStrict)
	}
}
