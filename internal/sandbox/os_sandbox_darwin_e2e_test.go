//go:build darwin

// Package sandbox — macOS Seatbelt workspace-write 端到端测试（P1-SEC）。
//
// 测试哲学：结果断言（哨兵文件 / 退出码），不 mock。
// 每个用例都通过真实 /usr/bin/sandbox-exec 在内核层执行命令，
// 断言文件系统副作用是否发生——UI/应用层说什么不算数。
//
// 覆盖：
// - 写工作区外失败 + 哨兵文件不存在
// - 写工作区内成功 + 哨兵文件存在
// - 网络被拒（对照组：沙箱外可连通，沙箱内失败）
// - 敏感目录读被拒（对照组同上）
// - .git/hooks 写被拒、.git/objects 写成功（git commit 可用）
// - /dev/null 重定向可用（基础可用性回归）
package sandbox

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runUnderSeatbelt 在真实 sandbox-exec 沙箱内执行 shell 脚本，返回执行错误。
func runUnderSeatbelt(t *testing.T, profile, dir, script string) error {
	t.Helper()
	return runShellScript(t, profile, dir, script, true)
}

// runWithoutSandbox 对照组：无沙箱执行同一脚本。
func runWithoutSandbox(t *testing.T, dir, script string) error {
	t.Helper()
	return runShellScript(t, "", dir, script, false)
}

func runShellScript(t *testing.T, profile, dir, script string, sandboxed bool) error {
	t.Helper()
	if sandboxed {
		return runSandboxedScript(t, profile, dir, script)
	}
	return runPlainScript(dir, script)
}

// skipIfNoSandboxExec 在「seatbelt 沙箱在本运行环境**真的用不了**」时跳过。
//
// ★ 2026-10-09（接线）：不再只看"文件在不在"。此前本 helper 用
// `sandboxExecAvailable()`（纯 `os.Stat`）、`skipIfSeatbeltApplyDenied` 又自带
// **一份影子探针**（同 profile 同命令各写一遍）—— 三处三种写法，正是
// "同一判定多处各写各的"的复发形态。现在唯一判据是生产码 `SandboxExecUsable`，
// 本 helper 与 `skipIfSeatbeltApplyDenied` 都只是它的**薄转发**。
func skipIfNoSandboxExec(t *testing.T) {
	t.Helper()
	if ok, why := SandboxExecUsable(); !ok {
		t.Skipf("seatbelt sandbox not usable in this test runner: %s", why)
	}
}

// skipIfSeatbeltApplyDenied 与 skipIfNoSandboxExec 同义（保留旧名：多处调用点）。
//
// 运行器自身处于 OS 沙箱内（如内嵌终端 / 某些 CI）时 `sandbox_apply` 返回 EPERM，
// 沙箱用例只会得到错误结论（拒绝类用例会"因错误原因通过"）⇒ 必须先跳过。
func skipIfSeatbeltApplyDenied(t *testing.T) {
	t.Helper()
	skipIfNoSandboxExec(t)
}

// TestSeatbeltE2E_WriteOutsideWorkspaceFails 结果断言：工作区外写被内核拒绝。
func TestSeatbeltE2E_WriteOutsideWorkspaceFails(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)

	ws := t.TempDir()
	outsideDir, err := os.MkdirTemp(os.Getenv("HOME"), "whale-sbx-out-*")
	if err != nil {
		t.Skipf("cannot create outside dir under HOME: %v", err)
	}
	defer os.RemoveAll(outsideDir)
	outsideFile := filepath.Join(outsideDir, "sentinel.txt")

	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	runErr := runUnderSeatbelt(t, profile, ws, "echo pwned > "+quoteShell(outsideFile))
	if runErr == nil {
		t.Errorf("write outside workspace should FAIL under sandbox, but exited 0")
	}
	if _, statErr := os.Stat(outsideFile); statErr == nil {
		t.Fatalf("SECURITY: sentinel file was created outside workspace despite seatbelt: %s", outsideFile)
	}
}

// TestSeatbeltE2E_WriteInsideWorkspaceSucceeds 结果断言：工作区内写正常。
func TestSeatbeltE2E_WriteInsideWorkspaceSucceeds(t *testing.T) {
	skipIfNoSandboxExec(t)

	ws := t.TempDir()
	inside := filepath.Join(ws, "ok.txt")

	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	if runErr := runUnderSeatbelt(t, profile, ws, "echo ok > "+quoteShell(inside)); runErr != nil {
		t.Errorf("write inside workspace should succeed, got: %v", runErr)
	}
	data, readErr := os.ReadFile(inside)
	if readErr != nil {
		t.Fatalf("sentinel inside workspace should exist: %v", readErr)
	}
	if strings.TrimSpace(string(data)) != "ok" {
		t.Errorf("sentinel content = %q, want %q", string(data), "ok")
	}
}

// TestSeatbeltE2E_NetworkDeniedWithControl 结果断言：网络被内核拒绝（带沙箱外对照组）。
//
// 对照组确保测试本身有效：同一 URL 沙箱外可连通、沙箱内失败——
// 失败只能归因于沙箱，而非网络环境。
func TestSeatbeltE2E_NetworkDeniedWithControl(t *testing.T) {
	skipIfNoSandboxExec(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	url := server.URL // 127.0.0.1 随机端口

	ws := t.TempDir()
	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	// 对照组：无沙箱时 curl 必须成功（证明 URL 可达、curl 可用）
	if ctrlErr := runWithoutSandbox(t, ws, "/usr/bin/curl -fsS -m 5 "+quoteShell(url)+" > /dev/null 2>&1"); ctrlErr != nil {
		t.Skipf("control group failed (network unavailable in test env): %v", ctrlErr)
	}
	// 实验组：沙箱内同一 curl 必须失败
	if runErr := runUnderSeatbelt(t, profile, ws, "/usr/bin/curl -fsS -m 5 "+quoteShell(url)+" > /dev/null 2>&1"); runErr == nil {
		t.Error("SECURITY: curl succeeded under sandbox with (deny network*) — network isolation is not enforced")
	}
}

// TestSeatbeltE2E_NetworkAllowedWithSwitch 门 3 第 3 层验证（结果断言）：
// AllowNetwork=true 的 profile 下沙箱内 curl 必须成功（对照组同上）。
//
// 防假验证：仅断言 profile 文本含 (allow network*) 不够——(deny default)
// 姿态下规则语义错误时文本正确但网络仍断；必须真跑 sandbox-exec 观察结果。
func TestSeatbeltE2E_NetworkAllowedWithSwitch(t *testing.T) {
	skipIfNoSandboxExec(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	url := server.URL

	ws := t.TempDir()
	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	opts.AllowNetwork = true
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	// 对照组：无沙箱时 curl 必须成功（证明 URL 可达、curl 可用）
	if ctrlErr := runWithoutSandbox(t, ws, "/usr/bin/curl -fsS -m 5 "+quoteShell(url)+" > /dev/null 2>&1"); ctrlErr != nil {
		t.Skipf("control group failed (network unavailable in test env): %v", ctrlErr)
	}
	// 实验组：AllowNetwork=true 沙箱内同一 curl 必须成功
	if runErr := runUnderSeatbelt(t, profile, ws, "/usr/bin/curl -fsS -m 5 "+quoteShell(url)+" > /dev/null 2>&1"); runErr != nil {
		t.Errorf("FUNCTIONAL: curl failed under sandbox with AllowNetwork=true (switch is a silent no-op?): %v", runErr)
	}
}

// TestSeatbeltE2E_SensitiveReadDeniedWithControl 结果断言：敏感目录读被内核拒绝。
func TestSeatbeltE2E_SensitiveReadDeniedWithControl(t *testing.T) {
	skipIfNoSandboxExec(t)

	ws := t.TempDir()
	sensDir := t.TempDir()
	sensFile := filepath.Join(sensDir, "secret.key")
	if err := os.WriteFile(sensFile, []byte("canary-secret-8f3a"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	opts.SensitiveReadDirs = append(opts.SensitiveReadDirs, sensDir)
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	// 对照组：无沙箱可读
	if ctrlErr := runWithoutSandbox(t, ws, "cat "+quoteShell(sensFile)+" > /dev/null"); ctrlErr != nil {
		t.Fatalf("control group should read sensitive file without sandbox: %v", ctrlErr)
	}
	// 实验组：沙箱内读必须失败
	if runErr := runUnderSeatbelt(t, profile, ws, "cat "+quoteShell(sensFile)+" > /dev/null 2>&1"); runErr == nil {
		t.Error("SECURITY: sensitive file was readable under sandbox — read protection is not enforced")
	}
}

// TestSeatbeltE2E_GitHooksWriteDeniedButObjectsAllowed 结果断言：
// .git/hooks 不可写（防 git hook 注入任意代码执行），.git 其余部分可写（git commit 可用）。
func TestSeatbeltE2E_GitHooksWriteDeniedButObjectsAllowed(t *testing.T) {
	skipIfNoSandboxExec(t)

	ws := t.TempDir()
	hooksDir := filepath.Join(ws, ".git", "hooks")
	objectsDir := filepath.Join(ws, ".git", "objects")
	for _, d := range []string{hooksDir, objectsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	hooksFile := filepath.Join(hooksDir, "pre-commit")
	objectsFile := filepath.Join(objectsDir, "obj.txt")

	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	// .git/hooks 写必须失败
	if runErr := runUnderSeatbelt(t, profile, ws, "echo evil > "+quoteShell(hooksFile)+" 2>/dev/null"); runErr == nil {
		t.Error("SECURITY: .git/hooks write succeeded under sandbox — git hook injection vector is open")
	}
	if _, statErr := os.Stat(hooksFile); statErr == nil {
		t.Fatalf("SECURITY: hook file was created: %s", hooksFile)
	}
	// .git/objects 写必须成功（正常 git commit 路径）
	if runErr := runUnderSeatbelt(t, profile, ws, "echo obj > "+quoteShell(objectsFile)); runErr != nil {
		t.Errorf(".git/objects write should succeed (git commit compatibility), got: %v", runErr)
	}
}

// TestSeatbeltE2E_DevNullRedirectAllowed 可用性回归：/dev/null 重定向正常。
func TestSeatbeltE2E_DevNullRedirectAllowed(t *testing.T) {
	skipIfNoSandboxExec(t)

	ws := t.TempDir()
	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	profile, err := buildSeatbeltProfile(opts)
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	if runErr := runUnderSeatbelt(t, profile, ws, "echo noise > /dev/null 2>&1"); runErr != nil {
		t.Errorf("redirect to /dev/null should be allowed, got: %v", runErr)
	}
}

// TestSeatbeltRestrict_WrapsCommandWithInlineProfile 验证 Restrict 用 -p 内联 profile 包装命令。
//
// 结果断言：包装后的命令在工作区外写会真实失败（经 Restrict 包装的路径与生产一致）。
func TestSeatbeltRestrict_WrapsCommandWithInlineProfile(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)

	ws := t.TempDir()
	outsideDir, err := os.MkdirTemp(os.Getenv("HOME"), "whale-sbx-out-*")
	if err != nil {
		t.Skipf("cannot create outside dir under HOME: %v", err)
	}
	defer os.RemoveAll(outsideDir)
	outsideFile := filepath.Join(outsideDir, "sentinel.txt")

	sb := &darwinSeatbeltSandbox{cfg: DarwinSandboxConfig{UseSeatbelt: true}}
	if _, _, enterErr := sb.Enter(); enterErr != nil {
		t.Fatalf("Enter: %v", enterErr)
	}

	cmd := newShellCmd(ws, "echo pwned > "+quoteShell(outsideFile)+" 2>/dev/null")
	if restrictErr := sb.Restrict(cmd); restrictErr != nil {
		t.Fatalf("Restrict: %v", restrictErr)
	}
	if cmd.Path != "/usr/bin/sandbox-exec" {
		t.Fatalf("cmd.Path = %q, want /usr/bin/sandbox-exec", cmd.Path)
	}
	if len(cmd.Args) < 2 || cmd.Args[1] != "-p" {
		t.Fatalf("cmd.Args[1] = %v, want -p (inline profile)", cmd.Args[1])
	}

	// 防假阳性：apply 失败（运行器自身被沙箱）≠ 内核拒绝写，见 runSandboxedScript 注释
	var wrapBuf bytes.Buffer
	cmd.Stdout = &wrapBuf
	cmd.Stderr = &wrapBuf
	runErr := cmd.Run()
	if runErr != nil && strings.Contains(wrapBuf.String(), "sandbox_apply: Operation not permitted") {
		t.Skipf("test runner itself is sandboxed (nested seatbelt apply denied); run in a normal terminal")
	}
	if runErr == nil {
		t.Error("write outside workspace should fail via Restrict-wrapped command")
	}
	if _, statErr := os.Stat(outsideFile); statErr == nil {
		t.Fatalf("SECURITY: sentinel created outside workspace via Restrict-wrapped command: %s", outsideFile)
	}
}

// quoteShell 为路径加单引号（shell 安全）。
func quoteShell(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// runSandboxedScript 通过真实 sandbox-exec 执行脚本（结果断言用，非 mock）。
//
// 关键防假阳性（结果断言反模式）：若 sandbox-exec 本身无法应用 profile
// （"sandbox_apply: Operation not permitted"，典型场景：测试运行器自身
// 处于 OS 沙箱内，如 TRAE/CI 的 sandboxed terminal），则所有命令都会非零退出，
// "写被拒绝"类用例会**因错误原因通过**（apply 失败 ≠ 内核拒绝写）。
// 此处显式识别该形态并 skip，保证测试只在内核真正应用了 profile 时下结论。
func runSandboxedScript(t *testing.T, profile, dir, script string) error {
	t.Helper()
	var buf bytes.Buffer
	cmd := newShellCmd(dir, script)
	cmd.Path = "/usr/bin/sandbox-exec"
	cmd.Args = []string{"sandbox-exec", "-p", profile, "--", "/bin/sh", "-c", script}
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil && strings.Contains(buf.String(), "sandbox_apply: Operation not permitted") {
		t.Skipf("test runner itself is sandboxed (nested seatbelt apply denied); "+
			"run in a normal terminal to get a real verdict. sandbox-exec output: %s",
			strings.TrimSpace(buf.String()))
	}
	return err
}

// runPlainScript 无沙箱直接执行脚本（对照组）。
func runPlainScript(dir, script string) error {
	return newShellCmd(dir, script).Run()
}

// newShellCmd 构造 sh -c 脚本命令。
func newShellCmd(dir, script string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", script)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd
}

// TestBuildSeatbeltProfile_ResolvesSymlinkedPaths 单元级结果断言（v1 安全修复）：
// profile 构建必须解析路径 symlink。Seatbelt subpath 过滤匹配内核解析后的
// 路径（/var/folders/... 实以 /private/var/folders/... 呈现），调用方传入
// 未解析路径时 deny 规则静默永不命中（真实终端 E2E 实测：
// TestSeatbeltE2E_SensitiveReadDeniedWithControl FAIL —— 读保护失效）。
// 修复：buildSeatbeltProfile 在单一入口统一 resolve，不再依赖调用方记得解析。
func TestBuildSeatbeltProfile_ResolvesSymlinkedPaths(t *testing.T) {
	realWs, base := t.TempDir(), t.TempDir()
	realSens := t.TempDir()
	linkWs := filepath.Join(base, "ws-link")
	linkSens := filepath.Join(base, "sens-link")
	if err := os.Symlink(realWs, linkWs); err != nil {
		t.Skipf("cannot create symlink in test env: %v", err)
	}
	if err := os.Symlink(realSens, linkSens); err != nil {
		t.Skipf("cannot create symlink in test env: %v", err)
	}

	profile, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace:         linkWs,
		SensitiveReadDirs: []string{linkSens},
	})
	if err != nil {
		t.Fatalf("buildSeatbeltProfile: %v", err)
	}

	resolvedSens, rerr := resolveSandboxPath(realSens)
	if rerr != nil {
		t.Fatalf("resolveSandboxPath(realSens): %v", rerr)
	}
	resolvedWs, rerr := resolveSandboxPath(realWs)
	if rerr != nil {
		t.Fatalf("resolveSandboxPath(realWs): %v", rerr)
	}

	// 断言 1（结果）：deny 规则使用解析后的真实路径
	if !strings.Contains(profile, fmt.Sprintf("(deny file-read* (subpath \"%s\"))", resolvedSens)) {
		t.Errorf("sensitive deny rule does not use resolved path %q.\nprofile:\n%s", resolvedSens, profile)
	}
	// 断言 2（结果）：workspace allow 使用解析后的真实路径
	if !strings.Contains(profile, fmt.Sprintf("(allow file-write* (subpath \"%s\"))", resolvedWs)) {
		t.Errorf("workspace allow rule does not use resolved path %q.\nprofile:\n%s", resolvedWs, profile)
	}
	// 断言 3（回归核心）：未解析的 symlink 路径不得出现在规则中
	if strings.Contains(profile, linkSens) {
		t.Errorf("unresolved symlink path %q leaked into profile rules", linkSens)
	}
}
