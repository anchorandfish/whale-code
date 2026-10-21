//go:build linux

// Package sandbox — P1-4 端到端运行时验证测试（Linux 专属）。
//
// 本文件验证 Linux namespace 隔离与 RLIMIT_NOFILE 资源限制在真实进程上的内核级效果。
// 跨平台的进程组超时验证见 e2e_runtime_test.go（无 build tag，全平台编译）。
//
// 测试约束（P1-4）：
// - 启动真实进程（非 mock），验证内核级效果
// - 通过 WSL Ubuntu 验证（readlink/bash/sleep 等命令在 WSL 内可用）
// - 需内核 ≥ 3.8（USER namespace 支持），通过 syscallCompatCheck 检测
//
// 官方源：
// - Linux namespaces: https://man7.org/linux/man-pages/man7/namespaces.7.html @2026-07-21
// - Linux prlimit(2): https://man7.org/linux/man-pages/man2/prlimit.2.html @2026-07-21
// - Linux RLIMIT_NOFILE: https://man7.org/linux/man-pages/man2/setrlimit.2.html @2026-07-21

package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestE2E_LinuxNamespaceIsolation 验证 Linux namespace 隔离的内核级效果（P1-4 用例 1）。
//
// 测试流程：
// 1. 读取宿主 network namespace inode（os.Readlink /proc/self/ns/net）
// 2. 使用 sandbox.Manager.Prepare 创建启用 namespace 的沙箱上下文（CLONE_NEWPID|NEWNET|NEWUTS|NEWUSER）
// 3. 在沙箱内执行 readlink /proc/self/ns/net（通过 OSSandbox.Restrict 设置 Cloneflags）
// 4. 验证子进程的 namespace inode 与宿主不同（隔离生效）
//
// 内核级效果验证点：
// - CLONE_NEWNET 创建独立网络栈（新 net namespace inode）
// - CLONE_NEWPID 创建独立 PID namespace
// - CLONE_NEWUTS 创建独立 hostname
// - CLONE_NEWUSER 使非 root 用户可创建上述 namespace（rootless）
//
// 跳过条件：
// - 内核 < 3.8（不支持 USER namespace）
// - /proc/self/ns/net 不可读（非标准 Linux 环境）
// - namespace 创建被环境限制（部分 WSL/Docker 配置禁用 user namespace）
func TestE2E_LinuxNamespaceIsolation(t *testing.T) {
	// 前置条件 1：内核支持 USER namespace（≥ 3.8）
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}

	// 前置条件 2：readlink 命令可用
	if _, err := exec.LookPath("readlink"); err != nil {
		t.Skipf("readlink command not available: %v", err)
	}

	// Step 1: 读取宿主 network namespace inode
	// /proc/self/ns/net 是符号链接，目标形如 "net:[4026531992]"
	hostInode, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Skipf("Cannot readlink /proc/self/ns/net: %v (non-standard Linux procfs?)", err)
	}
	t.Logf("Host net namespace inode: %s", hostInode)

	// Step 2: 创建启用 namespace 的沙箱 Manager 并 Prepare 上下文
	workDir := t.TempDir()
	cfg := &SandboxConfig{
		WorkDir: workDir,
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces:    true,
				Namespaces:       []string{"pid", "net", "uts"},
				UseUserNamespace: true, // rootless 友好，自动叠加 CLONE_NEWUSER
			},
		},
	}
	mgr := NewManager(cfg, SandboxLevelIsolated)

	ctx := context.Background()
	sbCtx, cleanup, err := mgr.Prepare(ctx, SandboxLevelIsolated, workDir)
	if err != nil {
		t.Fatalf("Manager.Prepare failed: %v", err)
	}
	defer cleanup()

	// 验证 OSSandbox 已启用（非 degraded）
	osSB := sbCtx.OSSandbox()
	if osSB == nil {
		t.Fatal("OSSandbox is nil (namespace not enabled)")
	}
	if osSB.Platform() != "linux-namespaces" {
		t.Fatalf("Expected platform 'linux-namespaces', got %q (degraded?)", osSB.Platform())
	}

	// Step 3: 在沙箱内执行 readlink /proc/self/ns/net
	cmd := exec.Command("readlink", "/proc/self/ns/net")
	cmd.Env = sbCtx.Env
	cmd.Dir = sbCtx.WorkDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// 应用 namespace 限制（设置 cmd.SysProcAttr.Cloneflags）
	if err := osSB.Restrict(cmd); err != nil {
		t.Fatalf("OSSandbox.Restrict failed: %v", err)
	}

	// 启动进程组（StartCommandWithProcessGroup 设置 Setpgid，保留 Cloneflags）
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		// namespace 创建可能被环境限制（部分 WSL/Docker 配置禁用 user namespace）
		t.Skipf("Cannot start process with namespaces (environment restriction?): %v", err)
	}

	waitErr := cmd.Wait()
	childOutput := strings.TrimSpace(buf.String())
	t.Logf("Child readlink output: %s (wait err: %v)", childOutput, waitErr)

	if waitErr != nil {
		t.Fatalf("readlink command failed in sandbox: %v, output: %s", waitErr, buf.String())
	}
	if childOutput == "" {
		t.Fatalf("readlink produced empty output, stderr: %s", buf.String())
	}

	// Step 4: 验证子进程的 namespace inode 与宿主不同（隔离生效）
	if childOutput == hostInode {
		t.Errorf("Namespace isolation FAILED: host and child have same net namespace inode: %s", hostInode)
	} else {
		t.Logf("Namespace isolation OK: host=%s child=%s", hostInode, childOutput)
	}
}

// TestE2E_ResourceLimits_FDExhaustion 验证 RLIMIT_NOFILE 限制的内核级效果（P1-4 用例 2）。
//
// 测试流程：
// 1. 启动 bash 子进程（脚本内含 sleep 0.2 等待 prlimit 应用）
// 2. 调用 ApplyResourceLimits 设置 RLIMIT_NOFILE=10（fd 编号 0-9 允许，≥10 拒绝）
// 3. 子进程尝试用 exec N>/dev/null 打开 fd 3 到 30
// 4. 验证 fd 10 处返回 EMFILE（bash exec 失败，exit code 42）
//
// 内核级效果验证点：
// - prlimit(pid, RLIMIT_NOFILE, {10, 10}) 设置子进程 fd 上限
// - 后续 open()/dup() 调用 fd 编号 ≥ 10 时内核返回 EMFILE (errno 24)
// - 限制仅影响子进程，不影响父进程（Go 测试进程）
//
// 时序说明：
// - StartCommandWithProcessGroup 启动 bash（此时 bash 使用默认 NOFILE，如 1024）
// - ApplyResourceLimits 立即调用 prlimit 降低 NOFILE 到 10
// - bash 脚本内 sleep 0.2 确保 prlimit 在打开 fd 前已生效
// - bash exec N>/dev/null 从 fd 3 开始，fd 0-2 已被 stdin/stdout/stderr 占用
//
// 预期结果：
// - fd 3-9 可打开（7 个，fd 编号 < 10）
// - fd 10 打开失败（EMFILE，fd 编号 ≥ 10）
// - 脚本输出 "EMFILE at fd 10, opened=7" 并 exit 42
func TestE2E_ResourceLimits_FDExhaustion(t *testing.T) {
	// 前置条件：bash 可用
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}

	// bash 脚本：sleep 等待 prlimit → 尝试打开 fd 3-30 → EMFILE 时 exit 42
	// 使用 eval "exec ${fd}>/dev/null" 动态指定 fd 编号
	// exec 2>/dev/null 抑制 bash 的 exec 失败错误消息（不影响 if 返回值判断）
	script := `
sleep 0.2
exec 2>/dev/null
opened=0
fd=3
while [ "$fd" -le 30 ]; do
    if eval "exec ${fd}>/dev/null"; then
        opened=$((opened + 1))
        fd=$((fd + 1))
    else
        echo "EMFILE at fd ${fd}, opened=${opened}"
        exit 42
    fi
done
echo "opened=${opened} fds without EMFILE (limit not enforced?)"
exit 0
`

	cmd := exec.Command("bash", "-c", script)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	// Step 1: 启动 bash 子进程
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Fatalf("StartCommandWithProcessGroup failed: %v", err)
	}

	// Step 2: 应用 RLIMIT_NOFILE=10（fd 编号 0-9 允许，≥10 返回 EMFILE）
	limits := ResourceLimits{
		FileDescriptorMax: 10,
	}
	audit := NewAuditLog()
	if err := ApplyResourceLimits(cmd, limits, audit); err != nil {
		t.Fatalf("ApplyResourceLimits failed: %v", err)
	}

	// Step 3: 等待子进程完成
	waitErr := cmd.Wait()
	outputStr := strings.TrimSpace(output.String())
	t.Logf("Command output: %s", outputStr)
	t.Logf("Command wait error: %v", waitErr)

	// Step 4: 验证 EMFILE 被触发
	// 核心断言 1：输出包含 "EMFILE"
	if !strings.Contains(outputStr, "EMFILE") {
		t.Errorf("Expected output to contain 'EMFILE', got: %s", outputStr)
	}

	// 核心断言 2：exit code 为 42（脚本约定的 EMFILE 信号）
	if cmd.ProcessState == nil {
		t.Fatal("ProcessState is nil (cmd.Wait not completed?)")
	}
	exitCode := cmd.ProcessState.ExitCode()
	if exitCode != 42 {
		t.Errorf("Expected exit code 42 (EMFILE signal), got %d (output: %s)", exitCode, outputStr)
	}

	// 辅助断言：验证 EMFILE 发生在 fd 10（NOFILE=10 → fd 编号 ≥ 10 被拒）
	if strings.Contains(outputStr, "EMFILE at fd 10") {
		t.Logf("EMFILE correctly triggered at fd 10 (RLIMIT_NOFILE=10 enforced)")
	} else if strings.Contains(outputStr, "EMFILE") {
		t.Logf("EMFILE triggered but at unexpected fd: %s", outputStr)
	}
}

// TestE2E_ResourceLimits_NoAffectParent 验证资源限制仅影响子进程，不影响父进程（Go 测试进程）。
//
// 测试流程：
// 1. 启动 bash 子进程并设置 RLIMIT_NOFILE=10
// 2. 子进程退出后，在父进程（Go 测试）中打开 > 10 个 fd
// 3. 验证父进程不受影响（prlimit 仅作用于指定 PID）
//
// 此测试确保 ApplyResourceLimits 的隔离性（不会泄漏到父进程）。
func TestE2E_ResourceLimits_NoAffectParent(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}

	// 启动一个快速退出的 bash 子进程
	cmd := exec.Command("bash", "-c", "exit 0")
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Fatalf("StartCommandWithProcessGroup failed: %v", err)
	}

	// 对子进程设置 RLIMIT_NOFILE=10
	limits := ResourceLimits{
		FileDescriptorMax: 10,
	}
	if err := ApplyResourceLimits(cmd, limits, NewAuditLog()); err != nil {
		t.Fatalf("ApplyResourceLimits failed: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("bash exit failed: %v", err)
	}

	// 在父进程中打开 20 个 fd，验证不受子进程限制影响
	// prlimit(pid, ...) 仅影响指定 PID，不影响调用进程
	fds := make([]*os.File, 0, 20)
	for i := 0; i < 20; i++ {
		f, err := os.Open("/dev/null")
		if err != nil {
			// 清理已打开的 fd
			for _, fd := range fds {
				_ = fd.Close()
			}
			t.Fatalf("Parent process cannot open fd %d (expected no limit): %v", i, err)
		}
		fds = append(fds, f)
	}

	// 清理
	for _, f := range fds {
		_ = f.Close()
	}

	t.Logf("Parent process opened 20 fds without EMFILE (prlimit isolation verified)")
}
