//go:build linux

// Package sandbox — Linux namespaces 沙箱实现（/ T1-P1-B9）。
//
// 纯 Go 实现，使用 syscall.SysProcAttr.Cloneflags 设置 namespace：
// - CLONE_NEWPID: PID namespace（隔离进程 ID）
// - CLONE_NEWNET: Network namespace（隔离网络栈，仅 lo 接口）
// - CLONE_NEWUTS: UTS namespace（隔离 hostname）
// - CLONE_NEWNS: Mount namespace（隔离挂载点，P1 不启用 pivot_root）
// - CLONE_NEWIPC: IPC namespace（隔离 System V IPC / POSIX 消息队列，SBX-018）
// - CLONE_NEWUSER: User namespace（rootless，使非 root 用户能创建其他 namespace）
//
// 同包内的分工（三层判定面各自成文件，互不混杂）：
//
//	os_sandbox_linux_preexec.go execve 前的加固阶段（Landlock + seccomp 共用信封）
//	os_sandbox_linux_landlock.go 文件路径级规则（内核 ≥5.13）
//	os_sandbox_linux_seccomp.go syscall 级规则（内核 ≥3.17）
//	seccomp_program.go BPF 程序构造（平台无关，可本机单测）
//
// 3 级降级链：
// 1. 首选：syscall.SysProcAttr.Cloneflags（纯 Go，无外部依赖）
// 2. 内核版本不支持：degradedSandbox（仅 setpgid 进程组）+ LogDegradation
// 3. 配置禁用：degradedSandbox + 不记 LogDegradation（用户意图）
//
// 官方源：
// - Linux man-pages unshare(2): https://man7.org/linux/man-pages/man2/unshare.2.html @2026-07-20
// - Linux man-pages user_namespaces(7): https://man7.org/linux/man-pages/man7/user_namespaces.7.html @2026-07-20
// - Go syscall.SysProcAttr: https://pkg.go.dev/syscall#SysProcAttr @2026-07-20
package sandbox

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// linuxNamespaceSandbox 是 Linux namespaces 沙箱实现。
//
// 并发安全（子审查优化 2 吸纳）：
// - Enter 用 sync.Once 保证幂等
// - Exit 加 mutex + 标志位保证幂等
// - Restrict 无状态（只读 handle）
type linuxNamespaceSandbox struct {
	cfg    LinuxSandboxConfig
	audit  *AuditLog
	once   sync.Once
	mu     sync.Mutex
	exited bool
	// handle 在 Enter 时填充（P1 仅作为标记，不持有内核句柄；namespace 在 fork 时由内核创建）
	handle *linuxNamespaceHandle
}

// linuxNamespaceHandle 是 Linux namespace 句柄（P1 仅标记，无内核资源需释放）。
//
// P1 实现：namespace 在 Restrict(cmd) 设置 Cloneflags 后，cmd.Start() 时由内核创建；
// 子进程退出时 namespace 自动回收，无需主动 Exit。
// P2 可能改为 unshare(2) 主动进入 namespace（需调用 syscall.Unshare，且需 setns 回退）。
type linuxNamespaceHandle struct {
	// Cloneflags 是合并后的 namespace 标志位掩码（如 CLONE_NEWPID | CLONE_NEWNET | CLONE_NEWUTS | CLONE_NEWUSER）。
	Cloneflags uintptr
	// Namespaces 是启用的 namespace 名称列表（用于审计日志）。
	Namespaces []string
}

// newPlatformSandbox 是 Linux 平台工厂（覆盖 os_sandbox_other.go 的默认实现）。
//
// 决策顺序：
// 1. cfg.Linux.UseNamespaces == false → degradedSandbox（配置禁用，不记 LogDegradation）
// 2. !syscallCompatCheck() → degradedSandbox（内核不支持，记 LogDegradation）
// 3. 否则 → linuxNamespaceSandbox（UseLandlock / UseSeccomp 任一启用时，
// Restrict 会注入 pre-exec helper：helper 在 namespace 内施加
// Landlock（文件路径）+ seccomp（syscall）后 exec 真实命令，限制随
// execve 继承。内核不支持的那一项在此处置 false 并记 LogDegradation，
// 保证"降级不可静默"。）
//
// 两项能力探测彼此独立，因为门槛不同：Landlock 需 ≥5.13，seccomp 需 ≥3.17。
// 旧内核上 Landlock 会被关掉，但 seccomp 必须照常生效——这正是
// helper 召唤条件取"两者相或"的原因。
func newPlatformSandbox(cfg OSLevelConfig, audit *AuditLog) OSSandbox {
	if !cfg.Linux.UseNamespaces {
		return newDegradedSandbox("linux namespaces disabled by config", audit)
	}
	if !syscallCompatCheck() {
		return newDegradedSandbox("linux kernel does not support required namespaces (need >= 3.8 for user namespace)", audit)
	}
	if cfg.Linux.UseLandlock && !landlockSupported() {
		// fail-open 但可观测：Landlock 不支持时退回仅 namespaces，
		// 必须留降级痕迹（静默防护教训：无出口的降级不可见）。
		if audit != nil {
			audit.LogDegradation(SandboxLevelIsolated, SandboxLevelIsolated,
				"linux landlock unsupported by kernel (< 5.13?), filesystem isolation limited to namespaces")
		}
		cfg.Linux.UseLandlock = false
	}
	if cfg.Linux.UseSeccomp && !seccompSupported() {
		// 同上：seccomp 不可用时必须留痕，且**不得**在 banner 上继续宣称它生效。
		if audit != nil {
			audit.LogDegradation(SandboxLevelIsolated, SandboxLevelIsolated,
				"linux seccomp unsupported by kernel (< 3.17?) or architecture, syscall filtering disabled")
		}
		cfg.Linux.UseSeccomp = false
	}
	return &linuxNamespaceSandbox{
		cfg:   cfg.Linux,
		audit: audit,
	}
}

// syscallCompatCheck 检测 Linux 内核是否支持所需 namespace（子审查优化 7 吸纳）。
//
// 使用 syscall.Uname 获取内核版本（不依赖 /proc/sys/kernel/osrelease，避免文件系统依赖）。
// USER namespace 需内核 ≥ 3.8（Linux man-pages user_namespaces(7) @2026-07-20）。
//
// 官方源：https://man7.org/linux/man-pages/man7/user_namespaces.7.html @2026-07-20
func syscallCompatCheck() bool {
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return false // 检测失败，安全降级
	}
	release := utsnameToString(uname.Release[:])
	major, minor, ok := parseKernelVersion(release)
	if !ok {
		return false
	}
	// USER namespace 需要 ≥ 3.8
	return major > 3 || (major == 3 && minor >= 8)
}

// utsnameToString 将 syscall.Utsname 的 int8 数组转为字符串（去除 \0）。
func utsnameToString(b []int8) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}

// parseKernelVersion 解析内核版本字符串（如 "5.15.0-91-generic"）。
//
// 返回 (major, minor, ok)：解析失败时 ok=false。
func parseKernelVersion(release string) (major, minor int, ok bool) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	for i, p := range parts[:2] {
		n := 0
		parsed := false
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
			parsed = true
		}
		if !parsed {
			return 0, 0, false
		}
		if i == 0 {
			major = n
		} else {
			minor = n
		}
	}
	return major, minor, true
}

// Enter 进入 Linux namespace 沙箱。
//
// P1 实现：仅在 handle 中记录 Cloneflags，实际 namespace 在 Restrict(cmd) 时通过
// cmd.SysProcAttr.Cloneflags 设置（cmd.Start() 时由内核创建）。
// P2 可能改为 syscall.Unshare(2) 主动进入 namespace（需调用 syscall.Unshare，且需 setns 回退）。
//
// 返回：
// - handle: *linuxNamespaceHandle（含 Cloneflags）
// - cleanup: noop（P1 无需释放；namespace 在子进程退出时自动回收）
// - err: Cloneflags 计算失败错误
//
// 官方源：https://man7.org/linux/man-pages/man2/unshare.2.html @2026-07-20
func (l *linuxNamespaceSandbox) Enter() (interface{}, func(), error) {
	var enterErr error
	l.once.Do(func() {
		flags, err := l.computeCloneflags()
		if err != nil {
			enterErr = err
			return
		}
		l.handle = &linuxNamespaceHandle{
			Cloneflags: flags,
			Namespaces: l.cfg.Namespaces,
		}
		if l.audit != nil {
			// 门 2：启用事件用 LogStatus（信息性），不污染降级计数
			landlock := "off"
			if l.cfg.UseLandlock {
				landlock = "on"
			}
			seccomp := "off"
			if l.cfg.UseSeccomp {
				seccomp = "on"
			}
			l.audit.LogStatus(fmt.Sprintf("linux namespaces enabled: %s (cloneflags=%#x), landlock=%s, seccomp=%s",
				strings.Join(l.cfg.Namespaces, ","), flags, landlock, seccomp))
			// SBX-021：资源门降级 canary（一次性，静默防护教训）。
			// 形式对齐 darwin 同位置（os_sandbox_darwin.go:362-367）：配置了资源
			// 限制就申报"哪一项没装"，避免用户以为所有限制都在生效。
			if notice := linuxRlimitDegradationNotice(l.cfg.ResourceLimits); notice != "" {
				l.audit.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict, notice)
			}
		}
	})
	if enterErr != nil {
		return nil, nil, fmt.Errorf("linux sandbox enter: %w", enterErr)
	}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			// P1 无需释放（namespace 在子进程退出时自动回收）
		})
	}
	return l.handle, cleanup, nil
}

// computeCloneflags 计算要设置的 Cloneflags 位掩码。
//
// 根据 cfg.Namespaces 列表累积对应 CLONE_* 标志。
// 若 cfg.UseUserNamespace=true 且未显式包含 "user"，自动叠加 CLONE_NEWUSER
// （使非 root 用户能创建其他 namespace）。
//
// 支持的 namespace：
// - "pid" → CLONE_NEWPID
// - "net" → CLONE_NEWNET
// - "uts" → CLONE_NEWUTS
// - "mount"/"mnt" → CLONE_NEWNS
// - "ipc" → CLONE_NEWIPC（SBX-018）
// - "user" → CLONE_NEWUSER
func (l *linuxNamespaceSandbox) computeCloneflags() (uintptr, error) {
	var flags uintptr
	for _, ns := range l.cfg.Namespaces {
		switch strings.ToLower(strings.TrimSpace(ns)) {
		case "pid":
			flags |= syscall.CLONE_NEWPID
		case "net":
			flags |= syscall.CLONE_NEWNET
		case "uts":
			flags |= syscall.CLONE_NEWUTS
		case "mount", "mnt":
			flags |= syscall.CLONE_NEWNS
		case "ipc":
			// SBX-018：隔离 System V IPC / POSIX 消息队列命名空间。
			// 无它则与宿主共用全局 IPC 对象与配额（见 production.go 同名注释）。
			flags |= syscall.CLONE_NEWIPC
		case "user":
			flags |= syscall.CLONE_NEWUSER
		default:
			return 0, fmt.Errorf("unknown namespace: %s", ns)
		}
	}
	// USER namespace 自动叠加（非 root 用户需 USER namespace 才能创建其他 namespace）
	if l.cfg.UseUserNamespace && flags&syscall.CLONE_NEWUSER == 0 {
		flags |= syscall.CLONE_NEWUSER
	}
	return flags, nil
}

// Exit 退出 Linux namespace 沙箱（幂等）。
//
// P1 实现：无操作（namespace 在子进程退出时自动回收，无需主动 Exit）。
// P2 可能改为调用 setns(2) 回到原 namespace（需保存原 namespace fd）。
func (l *linuxNamespaceSandbox) Exit() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exited = true
	return nil
}

// Restrict 将 Linux namespace + pre-exec 加固（Landlock / seccomp / 资源上限）应用到 exec.Cmd。
//
// 步骤：
// 1. 设置 cmd.SysProcAttr.Cloneflags（namespace 在 cmd.Start() 时由内核创建）
// 2. Landlock / seccomp / 资源上限**任一**启用时改写 cmd 为 re-exec helper 形态
// （<exe> __whale-sandbox-exec -- <cmd>...）：helper 在新 namespace 内
// 依次施加 Landlock（文件路径）、seccomp（syscall）、setrlimit（资源上限），
// 随后 exec 真实命令，三类限制均随 execve 继承到目标命令及全部后代。
//
// ★ 条件是「三者相或」而不是只看 UseLandlock：Landlock 需内核 ≥5.13，
// seccomp 仅需 ≥3.17。工厂会在旧内核上把 UseLandlock 置 false，若此处只看
// UseLandlock，helper 就不会被召唤 ⇒ seccomp 在这类内核上静默缺席。
// 同理漏掉资源限制 ⇒ 限制退回 Start() 之后的 prlimit，出现无限制窗口（SBX-001）。
//
// 与 process_unix.go 的 Setpgid 兼容（同时设置 Cloneflags + Setpgid 安全，
// 内核在 fork 时先创建 namespace 再设置进程组）。
//
// 前置条件：
// - cmd 未启动（cmd.Process == nil）
// - Enter() 已调用（l.handle != nil）
//
// 官方源：https://pkg.go.dev/syscall#SysProcAttr @2026-07-20
func (l *linuxNamespaceSandbox) Restrict(cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("linux sandbox restrict: cmd is nil")
	}
	if cmd.Process != nil {
		return fmt.Errorf("linux sandbox restrict: cmd already started")
	}
	if l.handle == nil {
		return fmt.Errorf("linux sandbox restrict: Enter not called")
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// cmd.SysProcAttr 是 *syscall.SysProcAttr 具体类型（非接口），不能做类型断言。
	// 直接赋值 Cloneflags（T1-P1-B9 修复：移除非法类型断言）。
	cmd.SysProcAttr.Cloneflags = l.handle.Cloneflags

	// pre-exec 加固（Landlock + seccomp + 资源上限）：re-exec helper 包装（fail-closed）
	//
	// ★ 召唤条件必须覆盖**全部三种** pre-exec 机制（SBX-001 补第三项）：
	// 只看 UseLandlock 会让 seccomp 在旧内核上静默缺席（历史缺陷）；
	// 漏掉资源限制则会让限制退回 Start() 之后的 prlimit（有窗口）。
	// 这里的条件与 buildPreExecSpec 能产出的非空信封集合必须严格一致，
	// 否则 helper 会因空信封 fail-closed 拒绝执行有效请求。
	if l.cfg.UseLandlock || l.cfg.UseSeccomp || l.cfg.ResourceLimits != (ResourceLimits{}) {
		if err := wrapWithPreExecHelper(cmd, l.cfg); err != nil {
			return fmt.Errorf("linux sandbox restrict: %w", err)
		}
	}
	return nil
}

// PostStartHook Linux namespace 沙箱无 post-start 操作（P0-3）。
// namespace 在 cmd.Start() 时由内核自动创建（Cloneflags），无需 post-start。
func (l *linuxNamespaceSandbox) PostStartHook() func(*exec.Cmd) error {
	return nil
}

// Platform 返回 "linux-namespaces"。
func (l *linuxNamespaceSandbox) Platform() string {
	return "linux-namespaces"
}

// describeIsolationFeatures 返回 Linux 实际生效的隔离特性（门 2 降级可见性）。
//
// Landlock / seccomp 均按运行时探测结果列出：内核不支持时不宣称该特性，
// 降级原因由工厂期 LogDegradation 记录、经 IsolationStatusOf 呈现。
// 口径一致性：此处只列"实际生效"项，与审计降级记录不矛盾。
func describeIsolationFeatures(cfg OSLevelConfig) []string {
	var features []string
	if len(cfg.Linux.Namespaces) > 0 {
		features = append(features, "namespaces("+strings.Join(cfg.Linux.Namespaces, ",")+")")
	}
	if cfg.Linux.UseLandlock && landlockSupported() {
		features = append(features, "landlock")
	}
	if cfg.Linux.UseSeccomp && seccompSupported() {
		features = append(features, "seccomp")
	}
	// 门 3：网络放行必须用户可见（安全降级不可静默）。
	// AllowNetwork=true 时 NewManager 已过滤 "net"，namespaces(...) 特性
	// 不再含 net，此处显式标注 network=on 避免用户误判网络仍隔离。
	if cfg.Linux.AllowNetwork {
		features = append(features, "network=on")
	}
	return features
}

// isLinuxLandlockBuild 报告当前构建是否含 Landlock 路径（仅 linux）。
func isLinuxLandlockBuild() bool { return true }
