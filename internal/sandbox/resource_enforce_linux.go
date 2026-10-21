// Package sandbox — Linux 资源限制实现（P1-1 / SBX-001）。
//
// # 两条施加路径（SBX-001 之后：主路径在 execve 之前）
//
//	主路径 pre-exec helper 在本进程内 unix.Setrlimit，**execve 之前**生效
// （applyResourceLimitsToSelf；召唤见 os_sandbox_linux_preexec.go）
//	兜底 unix.Prlimit(pid, ...) 对**已启动**进程补设（platformApplyResourceLimits）
//
// ★ 为什么必须把主路径前移：Prlimit 只能作用于**已存在**的进程，因此兜底路径
// 必然存在「Start() 返回 → prlimit 生效」之间的**无限制窗口**，进程炸弹专打
// 这个窗口（原注释"调用时机：cmd.Start() 之后"就是这个缺陷的自述）。
// 放进 helper 后，限制在目标命令获得完整能力之前就已生效，窗口物理归零。
//
// ★ 两条路径共用 resourceLimitTargets 的**单一映射表**：同语义双实现会漏改
// （加一项限制只加一边 ⇒ 一边装一边漏， 型复发坑）。
//
// 官方源：
// - prlimit: https://man7.org/linux/man-pages/man2/prlimit.2.html @2026-07-21
// - setrlimit: https://man7.org/linux/man-pages/man2/setrlimit.2.html @2026-09-25

//go:build linux

package sandbox

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/unix"
)

// platformApplyResourceLimits 在 Linux 上通过 prlimit 设置**已启动**子进程的资源限制。
//
// 定位（SBX-001 之后）：**兜底路径**，不是主路径。主路径是 pre-exec helper 在
// execve 之前 setrlimit（applyResourceLimitsToSelf），无窗口。
//
// ★ 让路规则（SBX-001）：若信封已携带资源限制，本函数**直接返回 nil**。
// 两个理由：
// 1. 同一组限制不必施加两次；
// 2. 更关键 —— 兜底会**掩盖主路径是否真的装上了**：一旦 helper 静默失败，
// post-start 的 prlimit 会把它伪装成成功（虚假保护，参见 K1 谎报教训）。
//
// 判别标记用「信封」而非「数值相等」：数值相等无法区分"helper 装好了"与
// "helper 没装、只是配置恰好相同"。
//
// 失败时记 AuditLog 降级，不返回错误（best-effort）：兜底路径装不上不影响
// 主路径已生效的限制，属能力收窄而非失效。
func platformApplyResourceLimits(cmd *exec.Cmd, limits ResourceLimits, audit *AuditLog) error {
	if carriesResourceLimits(cmd) {
		return nil
	}
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("resource limits: cmd or process is nil")
	}
	pid := cmd.Process.Pid
	var errs []error

	for _, t := range resourceLimitTargets(limits) {
		if t.value == 0 {
			continue
		}
		rlimit := unix.Rlimit{Cur: t.value, Max: t.value}
		if err := unix.Prlimit(pid, t.resource, &rlimit, nil); err != nil {
			errs = append(errs, fmt.Errorf("RLIMIT_%s: %w", t.name, err))
		}
	}

	if len(errs) > 0 {
		if audit != nil {
			audit.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict,
				fmt.Sprintf("resource limits partial failure: %v", errs))
		}
		// best-effort：部分失败不阻断执行
		return nil
	}
	return nil
}

// resourceLimitTarget 描述一条「要施加的资源上限」（资源号 / 目标值 / 显示名）。
type resourceLimitTarget struct {
	resource int
	value    uint64
	name     string
}

// resourceLimitTargets 返回要施加的限制清单 —— **两条施加路径的单一维护点**。
//
// ★ 为什么必须单一：helper（execve 前 Setrlimit）与兜底（Prlimit）施加的是
// 同一组语义。各写一份映射表就会漏改：将来加一项限制只加一边 ⇒ 一边装、
// 一边漏（型「同语义双实现」复发坑）。
//
// 值为 0 表示该项不限制（由调用方跳过；ResourceLimits 的零值语义）。
func resourceLimitTargets(limits ResourceLimits) []resourceLimitTarget {
	// ★ SBX-021：RLIMIT_AS 已**退役**，刻意不在此表中。
	//
	// 退役理由（实测坐实，不是预防性移除）：RLIMIT_AS 限制的是**虚拟地址空间**，
	// 而"内存炸弹"要防的是**实际占用**。Go / JVM / .NET 普遍预留远大于实际使用的
	// 虚拟空间 ⇒ 生产默认 1GiB 下 `go build ./internal/...` 直接
	// `fatal error: out of memory`（含本仓 internal/tui，失败点 modernc.org/libc），
	// 无限制时正常；阈值扫描 1GiB FAIL / 2GiB OK ⇒ 调值属"在错的尺子上找阈值"。
	//
	// 与 macOS 取舍一致：os_sandbox_darwin_rlimit.go:16-21 早已因同类理由跳过 AS。
	// 内存门改由 cgroup v2 `memory.max` 承担（SBX-019 路线 C）。
	//
	// 退役事实必须对用户可见（不许静默降级）⇒ linuxRlimitDegradationNotice
	// 产出文案，由 Enter 记入 AuditLog（os_sandbox_linux.go 同位置）。
	return []resourceLimitTarget{
		{unix.RLIMIT_CPU, limits.CPUMaxSeconds, "CPU"},
		{unix.RLIMIT_NOFILE, limits.FileDescriptorMax, "NOFILE"},
		{unix.RLIMIT_NPROC, limits.ProcessMax, "NPROC"},
		{unix.RLIMIT_FSIZE, limits.FileSizeMaxBytes, "FSIZE"},
	}
}

// applyResourceLimitsToSelf 在 execve **之前**对调用进程自身施加资源上限（主路径）。
//
// 只能由 pre-exec helper 调用：setrlimit 作用于调用进程并随 execve 继承，
// 这是唯一能让限制在「目标命令获得完整能力之前」生效的位置。
//
// 姿态与兜底**刻意不同**：这里失败返回 error（helper 据此 fail-closed 退出 126），
// 因为这是主路径 —— 装不上必须出声，不能像兜底那样 best-effort 静默收窄。
// 理由：静默收窄会制造"以为有资源门"的虚假保护感（对照 SBX-014 谎报教训）。
func applyResourceLimitsToSelf(limits ResourceLimits) error {
	var errs []error
	for _, t := range resourceLimitTargets(limits) {
		if t.value == 0 {
			continue
		}
		rlimit := unix.Rlimit{Cur: t.value, Max: t.value}
		if err := unix.Setrlimit(t.resource, &rlimit); err != nil {
			errs = append(errs, fmt.Errorf("RLIMIT_%s: %w", t.name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("resource limits: %v", errs)
	}
	return nil
}

// linuxRlimitDegradationNotice 返回 Linux 资源门的**降级申报**文案（空串 = 无需申报）。
//
// SBX-021：RLIMIT_AS 在 Linux 侧退役（量纲错 + 误杀实证，见下方说明），
// 该事实必须让用户可见，**不许静默**（静默防护教训，见 status.go 文件头）。
//
// 位置选择：与 resourceLimitTargets 同文件 —— "施加"与"申报"的决策紧邻，
// 避免将来改了一边忘另一边（型同语义双实现漂移）。
// 形式对齐 darwin：os_sandbox_darwin_rlimit.go:152-157（helper 内打印）
// 与 os_sandbox_darwin.go:362-367（装配期 LogDegradation）。
//
// 触发条件与 darwin 一致（`!= ResourceLimits{}`）：只要配置声明了任何资源上限，
// 就说明用户期望有资源门 —— 此时必须告知"其中 AS 未生效"，否则就是虚假保护感。
func linuxRlimitDegradationNotice(limits ResourceLimits) string {
	if limits == (ResourceLimits{}) {
		return ""
	}
	return "linux rlimit: CPU/NOFILE/NPROC/FSIZE enforced via pre-exec helper; " +
		"RLIMIT_AS retired on linux (limits virtual address space, not real usage — " +
		"breaks Go/JVM builds; memory gate pending cgroup v2 memory.max, SBX-019)"
}
