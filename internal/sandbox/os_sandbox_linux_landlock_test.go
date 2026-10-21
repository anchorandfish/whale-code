//go:build linux

// Package sandbox — Landlock 沙箱测试（P1-SEC）。
//
// 测试分层：
// 1. 单元测试（任何 Linux 可跑）：spec 构建姿态断言 + helper 改写断言
// 2. helper 分发冒烟：直接 exec 测试二进制模拟 re-exec，断言 fail-closed 退出码
// 3. e2e 哨兵测试（需内核 ≥ 5.13 + user namespace 可用，如 WSL2 Ubuntu）：
// 通过 TestMain 拦截 __whale-sandbox-exec 参数进入 helper 分支，
// 验证 Landlock 在真实进程上的内核级效果：
// - 工作区内写成功（同时验证 USER namespace uid/gid 映射修复）
// - 工作区外写失败且哨兵文件不存在（结果断言，非执行断言）
// - 非白名单 $HOME 路径读失败（secrets 天然被拒）
// - 网络访问失败（CLONE_NEWNET，参照既有 namespace e2e）
//
// 运行：go test ./internal/sandbox/ -run 'TestLandlock' -v
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/anchorandfish/whale-code/internal/testutil"
)

// TestMain 拦截 re-exec helper 调用。
//
// ★ 后这已是**冗余保险**：分发由本包 init 完成（helper_dispatch.go）。
func TestMain(m *testing.M) {
	HandleSandboxHelperArg(os.Args)
	// Guard 攻击子进程模式（sandbox_guard_attack_linux_test.go 的 re-exec 载荷）
	if mode := os.Getenv("WHALE_GUARD_CHILD"); mode != "" {
		guardChildMain(mode)
		return
	}
	os.Exit(m.Run())
}

// TestBuildLandlockSpec_Posture 断言 spec 的 workspace-write 姿态（结果断言）。
func TestBuildLandlockSpec_Posture(t *testing.T) {
	ws := t.TempDir()
	spec, err := buildLandlockSpec(ws)
	if err != nil {
		t.Fatalf("buildLandlockSpec failed: %v", err)
	}

	resolvedWs, err := resolveSandboxPath(ws)
	if err != nil {
		t.Fatalf("resolveSandboxPath: %v", err)
	}

	// 断言 1：工作区在可写根中
	if spec.Workspace != resolvedWs {
		t.Errorf("Workspace = %q, want resolved %q", spec.Workspace, resolvedWs)
	}

	// 断言 2：系统临时目录在可写根中
	tmpResolved, err := resolveSandboxPath(os.TempDir())
	if err == nil {
		found := false
		for _, d := range spec.ExtraWriteDirs {
			if d == tmpResolved {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ExtraWriteDirs missing system temp dir %q, got %v", tmpResolved, spec.ExtraWriteDirs)
		}
	}

	// 断言 3：/dev/null 在可写文件白名单（命令可用性基线）
	if _, statErr := os.Stat("/dev/null"); statErr == nil {
		found := false
		for _, f := range spec.WritableFiles {
			if f == "/dev/null" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("WritableFiles missing /dev/null, got %v", spec.WritableFiles)
		}
	}

	// 断言 4（安全核心）：$HOME 本身不得出现在任何白名单
	// （否则 ~/.ssh ~/.gnupg 等敏感目录随 subpath 授权变为可读）
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, dir := range append([]string{spec.Workspace}, spec.ExtraWriteDirs...) {
			if dir == home {
				t.Errorf("SECURITY: writable root must not be $HOME itself: %q", dir)
			}
		}
		for _, dir := range spec.ReadOnlyDirs {
			if dir == home {
				t.Errorf("SECURITY: readable root must not be $HOME itself: %q", dir)
			}
		}
	}

	// 断言 5（安全核心）：敏感目录不得被任何白名单路径覆盖
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		sensitive := []string{".ssh", ".gnupg", ".aws", ".kube", ".docker"}
		for _, s := range sensitive {
			sPath := filepath.Join(home, s)
			for _, dir := range append(append([]string{spec.Workspace}, spec.ExtraWriteDirs...), spec.ReadOnlyDirs...) {
				if dir == sPath || (strings.HasPrefix(sPath, dir+"/") && dir != "/") {
					t.Errorf("SECURITY: sensitive dir %q is covered by allow root %q", sPath, dir)
				}
			}
		}
	}
}

// TestBuildLandlockSpec_EmptyWorkspace 工作区为空必须 fail-closed。
func TestBuildLandlockSpec_EmptyWorkspace(t *testing.T) {
	if _, err := buildLandlockSpec(""); err == nil {
		t.Error("buildLandlockSpec('') should fail (fail-closed), got nil")
	}
	if _, err := buildLandlockSpec("   "); err == nil {
		t.Error("buildLandlockSpec(whitespace) should fail (fail-closed), got nil")
	}
}

// TestWrapWithPreExecHelper 断言 helper 改写的形态契约（argv + env 信封）。
func TestWrapWithPreExecHelper(t *testing.T) {
	ws := t.TempDir()
	cmd := exec.Command("sh", "-c", "echo hi")
	cmd.Dir = ws
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/root"}

	cfg := LinuxSandboxConfig{UseLandlock: true}
	// exec.Command 在构造时即把 "sh" 经 LookPath 解析为绝对路径（任何 Linux 上
	// 都不再是字面 "sh"；Ubuntu 24.04 usrmerge 下为 /usr/bin/sh）。生产实现把 re-exec
	// 目标设为解析后的 cmd.Path——确定性更强、不依赖沙箱内 PATH；断言对齐该真实契约，
	// 不再假设 argv[0] 原样保留。
	targetPath := cmd.Path
	if err := wrapWithPreExecHelper(cmd, cfg); err != nil {
		t.Fatalf("wrapWithPreExecHelper failed: %v", err)
	}

	exePath, exeErr := os.Executable()
	if exeErr != nil || exePath == "" {
		t.Skipf("os.Executable unavailable: %v", exeErr)
	}
	if cmd.Path != exePath {
		t.Errorf("cmd.Path = %q, want real exe path %q (/proc/self/exe 在 userns+AppArmor 下 exec 被拒)", cmd.Path, exePath)
	}
	wantArgs := []string{exePath, SandboxExecHelperArg, "--", targetPath, "-c", "echo hi"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], wantArgs[i])
		}
	}

	// env 必须携带信封，且原 env 保留
	spec := mustReadSpecFromEnv(t, cmd.Env)
	if spec.Landlock == nil {
		t.Fatal("envelope missing landlock rules (cfg.UseLandlock=true)")
	}
	resolvedWs, _ := resolveSandboxPath(ws)
	if spec.Landlock.Workspace != resolvedWs {
		t.Errorf("spec.Landlock.Workspace = %q, want %q", spec.Landlock.Workspace, resolvedWs)
	}
	if spec.Seccomp {
		t.Error("spec.Seccomp = true, want false (cfg.UseSeccomp 未开启时不得宣称)")
	}
}

// mustReadSpecFromEnv 从 env 中取出并解析 PreExecSpec；缺失即致命。
func mustReadSpecFromEnv(t *testing.T, env []string) PreExecSpec {
	t.Helper()
	var specJSON string
	for _, kv := range env {
		if strings.HasPrefix(kv, preExecSpecEnv+"=") {
			specJSON = strings.TrimPrefix(kv, preExecSpecEnv+"=")
		}
	}
	if specJSON == "" {
		t.Fatalf("env missing %s, got %v", preExecSpecEnv, env)
	}
	var spec PreExecSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		t.Fatalf("spec JSON invalid: %v\n%s", err, specJSON)
	}
	return spec
}

// TestLandlockHelper_MissingSpecFails helper 缺信封必须 fail-closed（exit 126 + stderr）。
//
// 直接 exec 测试二进制（/proc/self/exe）模拟生产 re-exec 路径，
// 验证 TestMain 分发链路端到端可用。
func TestLandlockHelper_MissingSpecFails(t *testing.T) {
	cmd := exec.Command("/proc/self/exe", SandboxExecHelperArg, "--", "true")
	// 不设置 preExecSpecEnv（从父进程继承的环境里也没有）
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatal("helper without spec should fail, got exit 0 (command ran unsandboxed?)")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if code := exitErr.ExitCode(); code != 126 {
		t.Errorf("exit code = %d, want 126 (helper fail-closed convention)", code)
	}
	if !strings.Contains(stderr.String(), preExecSpecEnv) {
		t.Errorf("stderr should mention missing %s for observability, got: %s", preExecSpecEnv, stderr.String())
	}
}

// newLandlockTestSandbox 构造启用 namespaces + Landlock 的沙箱上下文。
func newLandlockTestSandbox(t *testing.T, workDir string) (Manager, func()) {
	t.Helper()
	if !landlockSupported() {
		t.Skip("kernel does not support landlock (need >= 5.13)")
	}
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	cfg := &SandboxConfig{
		WorkDir: workDir,
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces:    true,
				Namespaces:       []string{"pid", "net", "uts", "mount"},
				UseUserNamespace: true,
				UseLandlock:      true,
			},
		},
	}
	mgr := NewManager(cfg, SandboxLevelIsolated)
	return mgr, func() {}
}

// runSandboxedCommand 在沙箱内运行命令（helper re-exec + namespaces + landlock）。
// 返回 (exitErr, combinedOutput)。
func runSandboxedCommand(t *testing.T, mgr Manager, workDir, command string) (error, string) {
	t.Helper()
	// 每条命令独立 Prepare（Restrict 需要 Enter 过的 OSSandbox；复用 mgr 会因
	// sync.Once 复用 handle，但 Prepare 每次 Enter 幂等，安全）
	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelIsolated, workDir)
	if err != nil {
		t.Fatalf("Manager.Prepare failed: %v", err)
	}
	defer cleanup()

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	if len(sbCtx.Env) > 0 {
		cmd.Env = sbCtx.Env
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if osSb := sbCtx.OSSandbox(); osSb != nil {
		if err := osSb.Restrict(cmd); err != nil {
			t.Fatalf("OSSandbox.Restrict failed: %v", err)
		}
	}
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		// namespace 创建可能被环境限制（部分 Docker/WSL 配置禁用 userns）
		t.Skipf("cannot start sandboxed process (environment restriction?): %v", err)
	}
	waitErr := cmd.Wait()
	return waitErr, strings.TrimSpace(buf.String())
}

// TestLandlockE2E_WriteInsideWorkspaceSucceeds 哨兵用例 1：工作区内写必须成功。
//
// 此测试同时捕获 USER namespace 未映射 bug：clone(CLONE_NEWUSER) 后不写
// uid_map，文件属主显示为 overflowuid，DAC 写检查 EACCES（连工作区都写不了）。
func TestLandlockE2E_WriteInsideWorkspaceSucceeds(t *testing.T) {
	ws := t.TempDir()
	mgr, cleanup := newLandlockTestSandbox(t, ws)
	defer cleanup()

	sentinel := filepath.Join(ws, "inside.txt")
	waitErr, out := runSandboxedCommand(t, mgr, ws, "echo ok > "+sentinel)

	if waitErr != nil {
		t.Fatalf("write INSIDE workspace should succeed, got error: %v, output: %s", waitErr, out)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel file should exist inside workspace: %v", err)
	}
	if strings.TrimSpace(string(data)) != "ok" {
		t.Errorf("sentinel content = %q, want %q", string(data), "ok")
	}
}

// TestLandlockE2E_WriteOutsideWorkspaceFails 哨兵用例 2：工作区外写必须失败。
//
// 结果断言（非执行断言）：命令必须非零退出，且哨兵文件确实不存在。
func TestLandlockE2E_WriteOutsideWorkspaceFails(t *testing.T) {
	ws := t.TempDir()
	mgr, cleanup := newLandlockTestSandbox(t, ws)
	defer cleanup()

	// 工作区外目标：$HOME 下（Landlock 未授权 + DAC 均应拒绝）
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("cannot resolve HOME: %v", err)
	}
	outsideDir, err := os.MkdirTemp(home, "whale-landlock-out-*")
	if err != nil {
		t.Skipf("cannot create dir under HOME (read-only home?): %v", err)
	}
	defer os.RemoveAll(outsideDir)
	sentinel := filepath.Join(outsideDir, "sentinel.txt")

	waitErr, out := runSandboxedCommand(t, mgr, ws, "echo pwned > "+sentinel)
	if waitErr == nil {
		t.Errorf("SECURITY: write outside workspace exited 0 (landlock not enforced), output: %s", out)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatalf("SECURITY: sentinel file created outside workspace despite landlock: %s", sentinel)
	}
}

// TestLandlockE2E_ReadNonWhitelistedHomePathFails 哨兵用例 3：非白名单 $HOME 路径读必须失败。
//
// Landlock 读白名单不含 $HOME 本身（只含工具子目录），
// 白名单外的 home 路径（含 ~/.ssh 形态路径）内核层拒绝。
func TestLandlockE2E_ReadNonWhitelistedHomePathFails(t *testing.T) {
	ws := t.TempDir()
	mgr, cleanup := newLandlockTestSandbox(t, ws)
	defer cleanup()

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("cannot resolve HOME: %v", err)
	}
	secretDir, err := os.MkdirTemp(home, "whale-landlock-secret-*")
	if err != nil {
		t.Skipf("cannot create dir under HOME: %v", err)
	}
	defer os.RemoveAll(secretDir)
	secretFile := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("SECRET"), 0o600); err != nil {
		t.Skipf("cannot create secret file: %v", err)
	}

	waitErr, out := runSandboxedCommand(t, mgr, ws, "cat "+secretFile)
	if waitErr == nil {
		t.Errorf("SECURITY: read of non-whitelisted home path exited 0, output: %s", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Fatalf("SECURITY: secret content leaked through sandbox: %s", out)
	}
}

// TestLandlockE2E_ExecPathMatrix 诊断用例：定位 fork/exec EACCES 的确切成因。
//
// 背景（UTM Ubuntu 24.04 实测）：全 namespace 组合下 exec /proc/self/exe 返回
// EACCES，导致 Landlock E2E 全部 SKIP。本用例逐项测试 4 种组合并输出矩阵，
// 区分成因是 "/proc/self/exe 路径" 还是 "CLONE_NEWNS (mount namespace)"。
//
// 断言：仅对生产路径组合（all-ns + 真实路径）硬断言；其余组合仅诊断输出。
func TestLandlockE2E_ExecPathMatrix(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		t.Skipf("os.Executable unavailable: %v", err)
	}

	const allNS = syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER
	const noMountNS = syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWUTS | syscall.CLONE_NEWUSER

	type combo struct {
		name       string
		flags      uintptr
		path       string
		production bool // 生产路径组合 → 硬断言
	}
	combos := []combo{
		{"all-ns + real-exe-path", allNS, exe, true}, // 生产路径（Fix 后）
		{"all-ns + /proc/self/exe", allNS, "/proc/self/exe", false},
		{"no-mount-ns + /proc/self/exe", noMountNS, "/proc/self/exe", false},
		{"no-ns + real-exe-path", 0, exe, false},
	}

	for _, c := range combos {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// exec 测试二进制本身，-test.run=NONE 使其快速退出 0
			cmd := exec.Command(c.path, "-test.run=TestNothingMatches___")
			cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: c.flags}
			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			runErr := cmd.Run()
			if runErr != nil {
				msg := fmt.Sprintf("exec 失败: %v", runErr)
				if c.production {
					t.Errorf("[生产路径] %s — 真实路径 exec 也被拒，需进一步排查 CLONE_NEWNS/AppArmor", msg)
				} else {
					t.Logf("[DIAG] %s", msg)
				}
				return
			}
			t.Logf("exec OK（%d bytes output）", buf.Len())
		})
	}
}

// TestLandlockE2E_EmptyHomeStillStarts 回归用例（VM 实测 bug 锁定）：
// 全新家目录（无 ~/.local ~/.cache ~/.cargo 等工具目录）时沙箱必须仍能启动。
//
// 背景：UTM Ubuntu 24.04 全新家目录实测，buildLandlockSpec 曾把不存在的
// ~/.local 加入只读白名单，helper 打规则 fd 时 ENOENT 硬失败 → 整个沙箱
// 起不来（"landlock: read dir /home/dev/.local: no such file"）。
// 本地开发机 ~/.local 天然存在，故须伪造空 HOME 复现。
//
// 结果断言：工作区内写成功（helper 正常启动并通过 Landlock）。
func TestLandlockE2E_EmptyHomeStillStarts(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	// 伪造全新家目录：空 tempdir，不含任何工具子目录
	fakeHome := testutil.IsolatedHomeDir(t)
	t.Setenv("HOME", fakeHome)
	// 顺带清掉 XDG 覆盖，确保 UserCacheDir 指向 fakeHome/.cache（不存在）
	t.Setenv("XDG_CACHE_HOME", "")

	ws := t.TempDir()
	mgr, cleanup := newLandlockTestSandbox(t, ws)
	defer cleanup()

	sentinel := filepath.Join(ws, "inside.txt")
	waitErr, out := runSandboxedCommand(t, mgr, ws, "echo ok > "+sentinel)
	if waitErr != nil {
		t.Fatalf("空 HOME 下沙箱应仍能启动（曾因 ~/.local 不存在硬失败）: %v, output: %s", waitErr, out)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("工作区写未生效: %v", err)
	}
	t.Log("空 HOME 回归通过：不存在的工具目录被跳过（fail-closed），沙箱正常启动")
}
