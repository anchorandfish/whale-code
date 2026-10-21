//go:build linux

// Package sandbox — SBX-018 判别性测试：Linux 沙箱必须启用 IPC 命名空间。
//
// # 病灶（修复前）
//
// 生产 namespace 集合只有 {pid,net,uts,mount}（production.go），没有 ipc
// ⇒ 沙箱内进程与宿主**共用**全局 System V IPC / POSIX 消息队列命名空间：
//   - shmat 可挂接宿主进程已有共享内存段（越界读他人数据）；
//   - 内核对 System V IPC 的配额是**命名空间级全局量**
//     （SEMMNI/SEMMNS/SEMOPM/MSGMNI/MSGMAX/MSGMNB），沙箱内命令把信号量/
//     消息队列占满，宿主上依赖它们的软件（数据库、桌面组件、构建工具）会起不来。
//
// 正解：namespace 集合加 "ipc" → CLONE_NEWIPC，IPC 对象随命名空间隔离，
// 配额按命名空间计数。对齐 同类 CLI bwrap 的默认 --unshare-ipc。
//
// # 判据设计（为什么这些可判别）
//
// 全部打在**结构面**（cloneflags 位掩码 / 生产 SSOT 变量），不打票面、不读文档：
//
//	判据① computeCloneflags("ipc") 必须产出 CLONE_NEWIPC 位。
//	   before 红：switch 无 "ipc" 分支 ⇒ 返回 "unknown namespace" 错误 ⇒ Fail
//	判据② defaultLinuxNamespaces（生产唯一来源）必须含 "ipc"。
//	   before 红：集合为 {pid,net,uts,mount} ⇒ Fail
//	判据③ 用**生产集合**走 Restrict 时 cmd.SysProcAttr.Cloneflags 必须含
//	   CLONE_NEWIPC —— 端到端把②的"集合里写了"接到"真的设进了 cmd"。
//	   before 红：computeCloneflags 对 "ipc" 报错 ⇒ Restrict 前构造即失败
//	判据④（防假绿对照）未知命名空间仍必须**报错**：证明①的 switch 没有为了
//	   让 "ipc" 通过而被放宽成"未知也放行"。before 与 after 都应通过。
//
// 反例注入（判别力自证）：把 computeCloneflags 的 "ipc" case 删掉 ⇒ 判据①③红；
// 把 defaultLinuxNamespaces 里的 "ipc" 去掉 ⇒ 判据②③红；判据④恒绿（对照）。
//
// # I-8.6 端到端（真起进程，见本文件末尾）
//
// 上面四条判据全部打在**结构面**（集合 / 位掩码 / cmd 字段），只能证明"标志位算出来了
// 并设进了 cmd"，不能证明"内核真的建出了另一个 IPC 命名空间"。人工用例 I-8.6 要求的
// 恰是后者：真起一个进程，读它 /proc/self/ns/ipc 的 inode，与宿主侧比对必须不同。
// 该判据已在 TestSBX018_E2E_IPCNSDiffersFromHost 内下移为容器可跑的 Go 端到端测试；
// 人工面只剩"目标发行版差异"（内核是否支持 CLONE_NEWIPC / 是否允许非特权 userns）。
package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// sbx018Sandbox 构造一个只用于计算 cloneflags 的 linuxNamespaceSandbox
// （不 Start 任何进程，因此不需要内核支持，也不需要 Enter）。
func sbx018Sandbox(namespaces []string) *linuxNamespaceSandbox {
	return &linuxNamespaceSandbox{
		cfg:    LinuxSandboxConfig{UseNamespaces: true, Namespaces: namespaces},
		handle: &linuxNamespaceHandle{Namespaces: namespaces},
	}
}

// TestSBX018_ComputeCloneflags_IPC 判据①：computeCloneflags 必须认得 "ipc"。
func TestSBX018_ComputeCloneflags_IPC(t *testing.T) {
	sb := sbx018Sandbox([]string{"ipc"})
	flags, err := sb.computeCloneflags()
	if err != nil {
		t.Fatalf("SBX-018 未修复：computeCloneflags 不认 \"ipc\" 命名空间: %v", err)
	}
	if flags&syscall.CLONE_NEWIPC == 0 {
		t.Fatalf("SBX-018 未修复：Cloneflags=%#x 缺 CLONE_NEWIPC(%#x)，IPC 未隔离",
			flags, syscall.CLONE_NEWIPC)
	}
}

// TestSBX018_ProductionNamespaceSetContainsIPC 判据②：生产 SSOT 必须含 "ipc"。
func TestSBX018_ProductionNamespaceSetContainsIPC(t *testing.T) {
	found := false
	for _, ns := range defaultLinuxNamespaces {
		if ns == "ipc" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SBX-018 未修复：生产 namespace 集合 %v 不含 \"ipc\"，沙箱与宿主共用 IPC 命名空间",
			defaultLinuxNamespaces)
	}
}

// TestSBX018_RestrictSetsIPCCloneflag 判据③：生产集合经 Restrict 后必须落到
// cmd.SysProcAttr.Cloneflags 上（把"集合里写了"接到"真的设进了 cmd"）。
func TestSBX018_RestrictSetsIPCCloneflag(t *testing.T) {
	// 复制一份：NewManager 在生产路径上会重写 Namespaces 字段。
	ns := append([]string(nil), defaultLinuxNamespaces...)
	sb := sbx018Sandbox(ns)

	flags, err := sb.computeCloneflags()
	if err != nil {
		t.Fatalf("生产 namespace 集合 %v 无法计算 cloneflags: %v", ns, err)
	}
	sb.handle = &linuxNamespaceHandle{Cloneflags: flags, Namespaces: ns}

	cmd := exec.Command("/bin/true")
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("Restrict 未设置 cmd.SysProcAttr")
	}
	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWIPC == 0 {
		t.Fatalf("SBX-018 未修复：cmd.SysProcAttr.Cloneflags=%#x 缺 CLONE_NEWIPC(%#x)",
			cmd.SysProcAttr.Cloneflags, syscall.CLONE_NEWIPC)
	}
}

// TestSBX018_UnknownNamespaceStillRejected 判据④：防假绿对照。
//
// 断言未知命名空间**仍**报错。它证明判据①的 switch 不是被放宽成"未知也当
// 已知处理"才通过 —— 那种放宽会让沙箱悄悄少装一个 namespace 而不报错。
func TestSBX018_UnknownNamespaceStillRejected(t *testing.T) {
	sb := sbx018Sandbox([]string{"pid", "definitely-not-a-namespace"})
	if _, err := sb.computeCloneflags(); err == nil {
		t.Fatal("未知命名空间必须报错（switch 不得被放宽成未知即放行）")
	}
}

// ===========================================================================
// I-8.6 端到端（真起进程）：内核产物面
// ===========================================================================

// sbx018ValidNSRef 报告 s 是否是合法的 namespace 引用（形如 "ipc:[4026532375]"）。
//
// 用途：**防假绿对照**。若宿主侧读数本身就不是合法引用（空串 / 报错文本 /
// 非 procfs 的伪链接），那么"子进程读数与宿主不同"可能只是两个垃圾值恰好不等
// —— 判据恒真、毫无判别力。先证明两侧读的都是"ipc:[数字]"，比较才有意义。
func sbx018ValidNSRef(s, wantKind string) bool {
	kind, rest, ok := strings.Cut(s, ":[")
	if !ok || kind != wantKind || !strings.HasSuffix(rest, "]") {
		return false
	}
	inode := strings.TrimSuffix(rest, "]")
	if inode == "" {
		return false
	}
	for _, c := range inode {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// TestSBX018_E2E_IPCNSDiffersFromHost 端到端（生产真实入口）：
// 沙箱内进程读到的 ipc namespace 必须与宿主**不同**。
//
// 与判据①②③④的分工：那四条打在结构面（集合里有没有 / 位掩码有没有 / cmd 上有没有），
// 本条打在**内核产物面**（内核真的为子进程建出了另一个 IPC 命名空间）。两类都绿，
// 才排除"标志位算出来了却没送到内核"这一类缺陷（Cloneflags 被后续赋值覆盖、
// helper 路径把它丢掉、内核静默忽略该位等）。
//
// 走生产同一条路：NewProductionSandboxManager（取生产 SSOT namespace 集合）
// → Manager.Prepare → OSSandbox.Restrict → StartCommandWithProcessGroup
// → 子进程用 re-exec 载荷自读 /proc/self/ns/ipc。
//
// 反例注入（判别力自证）：把 defaultLinuxNamespaces 里的 "ipc" 摘掉 ⇒ 子进程与
// 宿主共用 IPC 命名空间 ⇒ 两侧读数相等 ⇒ 本用例必红。namespace 集合取自**生产 SSOT
// 经生产入口**（不是本测试自带字面量），该注入才真的打得红它 —— 若这里写死
// []string{"ipc"}，注入就变成假通过。
//
// ★ 环境前提（必须记入回执，不得当成"通过"）：容器**默认** seccomp 档禁止
// unshare/clone 建立 namespace（本仓实测：默认档下 `unshare -U` / `unshare -i`
// 均 EPERM）⇒ 此时本用例显式 Skip 并说明解锁方式。该档下 I-8.6 视为**未验证**。
func TestSBX018_E2E_IPCNSDiffersFromHost(t *testing.T) {
	const nsFile = "/proc/self/ns/ipc"
	if _, err := os.Stat(nsFile); err != nil {
		t.Skipf("%s 不可读（非标准 procfs？），跳过 E2E: %v", nsFile, err)
	}

	// ---- 宿主侧读数（对照组的一半，不可省）----
	hostIPC, err := os.Readlink(nsFile)
	if err != nil {
		t.Skipf("读宿主 ipc namespace 失败: %v", err)
	}
	if !sbx018ValidNSRef(hostIPC, "ipc") {
		t.Fatalf("宿主 ipc namespace 读数 %q 不是合法引用（形如 ipc:[N]）⇒ 本判据无判别力", hostIPC)
	}
	t.Logf("宿主 ipc namespace: %s", hostIPC)

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh 不可用，跳过 E2E: %v", err)
	}

	workDir := t.TempDir()
	mgr := NewProductionSandboxManager(workDir)
	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelStrict, workDir)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer cleanup()

	osSB := sbCtx.OSSandbox()
	if osSB == nil || osSB.Platform() != "linux-namespaces" {
		t.Skipf("OSSandbox 已降级（platform=%v）⇒ namespace 未启用，IPC 隔离无从谈起", osSB)
	}

	selfExe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable 不可用: %v", err)
	}
	cmd := exec.Command(selfExe)
	cmd.Dir = workDir
	cmd.Env = append(append([]string{}, sbCtx.Env...), "WHALE_GUARD_CHILD=nsprobe")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := osSB.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Skipf("namespace 启动被环境限制（容器默认 seccomp 档需 "+
			"--security-opt seccomp=unconfined 才能建 namespace）: %v", err)
	}
	if werr := cmd.Wait(); werr != nil {
		t.Fatalf("沙箱内载荷退出失败: %v\n--- 输出 ---\n%s", werr, buf.String())
	}

	// ---- 解析沙箱内读数 ----
	out := buf.String()
	childIPC := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, found := strings.CutPrefix(line, "NS_IPC "); found {
			childIPC = strings.TrimSpace(v)
		}
		if strings.HasPrefix(line, "NS_IPC_ERR") {
			t.Fatalf("沙箱内读取 %s 失败: %s", nsFile, line)
		}
	}
	if childIPC == "" {
		t.Fatalf("沙箱内载荷未报告 ipc namespace（载荷没跑到？）:\n%s", out)
	}
	if !sbx018ValidNSRef(childIPC, "ipc") {
		t.Fatalf("沙箱内 ipc namespace 读数 %q 不是合法引用（形如 ipc:[N]）", childIPC)
	}
	if childIPC == hostIPC {
		t.Fatalf("SBX-018 未生效：沙箱内 ipc namespace = 宿主 ipc namespace (%s)\n"+
			"⇒ 沙箱进程与宿主共用全局 System V IPC / POSIX 消息队列：可 shmat 挂接宿主\n"+
			"   已有共享内存段（越界读他人数据），并可占满命名空间级全局配额。", hostIPC)
	}
	t.Logf("IPC 已隔离：宿主 %s → 沙箱内 %s", hostIPC, childIPC)
}
