// Package sandbox — 生产环境沙箱管理器工厂（P1-SEC + P2-1 CPU Rate）。
//
// 修复记录：修复前 OS 级沙箱仅在 TUI lifecycle.go 启用，
// plain/bridge（runner_core.go）路径 executor.Sandbox 默认 disabled、
// SandboxMgr 为 nil → 非 TUI 模式（whale -p / bridge）完全没有沙箱。
//
// 本工厂是唯一的生产启用入口：TUI lifecycle 与 runner_core 共用，
// 避免两处平台开关逻辑漂移（AI 实现的典型病灶：改一处忘另一处）。
package sandbox

import (
	"fmt"
	"runtime"
)

// defaultLinuxNamespaces 是 Linux 侧生产启用的 namespace 集合（**单一事实源**）。
//
// 为什么提成包级变量而不是留作内联字面量：SBX-018 要求把判据打在**结构面**
// （"生产确实启用了 ipc"），而不是打票面/文档。测试直接读这个变量即可断言，
// 从根上排除"改了注释、忘了改代码"这类假绿。
//
// 每一项的启用理由见 NewProductionSandboxManager linux 分支内的注释。
//
// SBX-018：加入 "ipc"（CLONE_NEWIPC）。
var defaultLinuxNamespaces = []string{"pid", "net", "uts", "mount", "ipc"}

// NewProductionSandboxManager 返回生产环境沙箱管理器（P1-SEC + P2-1）。
//
// ====== 平台策略（收敛为单一事实源）======
//
//		Linux: namespaces (pid/net/uts/mount/ipc) + USER namespace（rootless）
//	  - Landlock 文件系统沙箱（内核 ≥5.13，失败降级 canary）
//	  - seccomp-bpf 系统调用过滤（内核 ≥3.17，失败降级 canary）
//	    Windows: Job Object（内存/进程数/CPU Rate HARD_CAP）
//
// — P2-1 CPU Rate Control（HARD_CAP Win8+ 生效）
// — [GAP-WIN-P2 AppContainer] 原生安全沙箱，延后实现（见下）
//
//	Darwin: Seatbelt default-deny 工作区写/敏感读/网络拒绝
//
// ====== Windows 缺口文档（AppContainer 为 P2，需显式追踪不可静默）======
//
//	[GAP-WIN-P2: AppContainer] 原生细粒度沙箱（同类 CLI code 使用）
//
// 需要 PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES + 自定义 SID +
// 最小能力（capabilities）清单 + 命名对象/文件重定向矩阵。
// 复杂度高（跨进程 COM、剪贴板、字体、命名管道兼容）。
// 当前策略：Job Object 为主（资源限制 + KILL_ON_JOB_CLOSE 终结孤儿）。
// 对标同类 CLI AppContainer SID 自动生成 + 能力最小集。
//
//	[GAP-WIN-P3: 访问控制] Job Object 仅做资源限制，非文件系统/网络 DACL。
//
// 完整访问隔离需：Restricted Token (DenyOnly SIDs) + 目录 DACL 拒绝写入
// + WFP 网络过滤。延后 P3 独立方案。
//
//	降级约定：以上 GAP 在配置意图与实际实现不符时，Manager 会打 DEGRADE canary
//	（from==to same-level sub-cap degrade）保证可观测，禁止静默（B4-2）。
//
//	沙箱级别：Strict（与 TUI 路径一致）。
func NewProductionSandboxManager(workDir string) Manager {
	osCfg := DefaultOSLevelConfig()
	resLimits := DefaultResourceLimits()
	// 门 3：粗粒度网络开关（默认断网；env 开启时 NewManager 归一化过滤
	// Linux "net" namespace / macOS profile 改为 (allow network*)）。
	// Windows：Job Object 不隔离网络（GAP-WIN-P3），开关无操作且不宣称
	// network=on——默认姿态网络本就通，宣称"开关放行"即说谎。
	allowNetwork := networkAllowedByEnv()
	switch runtime.GOOS {
	case "linux":
		osCfg.Linux.UseNamespaces = true
		// P1-3：加 "mount" 启用 CLONE_NEWNS，隔离 mount namespace。
		// 注：不做 pivot_root（风险高），仅隔离 mount 操作防止影响宿主。
		// SBX-018：加 "ipc" 启用 CLONE_NEWIPC —— 无 IPC 命名空间时，沙箱内
		// 进程与宿主共用**全局** System V IPC / POSIX 消息队列：可 shmat 挂接
		// 宿主已有共享内存段（越界读他人数据），且能占满命名空间级全局配额
		// （SEMMNI/SEMMNS/SEMOPM/MSGMNI/MSGMAX/MSGMNB），把宿主上依赖信号量/
		// 消息队列的软件挤到起不来。对齐 同类 CLI bwrap 默认 --unshare-ipc。
		// 集合收敛到单一事实源 defaultLinuxNamespaces（可被测试直接断言）；
		// ★ 必须先复制再交给 cfg —— NewManager 在 AllowNetwork 时重写该字段
		// （manager.go:121），共享底层数组会让包级 SSOT 被就地改写。
		osCfg.Linux.Namespaces = append([]string(nil), defaultLinuxNamespaces...)
		// P1-SEC：Landlock 文件系统沙箱（内核 ≥5.13；不支持时工厂内
		// LogDegradation 可观测降级为仅 namespaces）。文件访问姿态：
		// 写仅工作区/tmp/缓存；读白名单（$HOME 非白名单子路径不可读，
		// ~/.ssh 等密钥目录内核层拒绝）；网络由 CLONE_NEWNET 隔离。
		osCfg.Linux.UseLandlock = true
		// P2-3：seccomp-bpf 系统调用过滤（内核 ≥3.17，与 Landlock 独立探测）。
		// 拒绝同类进程可主动发起、且能跨出隔离边界的原语（ptrace /
		// process_vm_readv/writev / io_uring_*）——这些是同 uid 逃逸面，
		// Landlock（只认路径）与 CLONE_NEWNET（只认 IP）在物理上都拦不到。
		// 拒绝清单见 os_sandbox_linux_seccomp.go（单一维护点）。
		osCfg.Linux.UseSeccomp = true
		// 门 3：env 开启 → 过滤 "net"（NewManager 归一化，单一事实源）
		osCfg.Linux.AllowNetwork = allowNetwork
		// SBX-001：资源限制经 pre-exec helper 在 **execve 之前** setrlimit
		//（RLIMIT_CPU/AS/NOFILE/NPROC/FSIZE）。与 :117 Darwin 同形：
		// 复用同一 resLimits 实例，顶层（post-start 兜底）与 OSLevel.Linux
		//（helper 主路径）同源同值，消除双源漂移。
		osCfg.Linux.ResourceLimits = resLimits
	case "windows":
		//
		// ===== Windows 生产启用入口：Job Object 为主（P0/P1/P2-1）=====
		//
		// [P0] KILL_ON_JOB_CLOSE：管理器结束时同时终结所有子进程（防孤儿）。
		// [P0] MemoryLimit：Process Memory Limit（防止 OOM 拖垮宿主）。
		// [P1] MaxProcesses：Active Process Limit（防止 fork bomb）。
		// [P2-1] CPUMaxPercent：CPU HARD_CAP（Win8+/Server 2012+ 内核级硬节流）。
		// 默认 70%，防止单命令占满宿主 CPU；老系统不支持时打降级 canary
		// 但继续运行（fail-open + 可观测）。
		//
		// [GAP-WIN-P2 AppContainer] 原生访问控制沙箱，需独立 SID + 能力声明。
		// 不在本次 Job Object 路径内实现；以结构注释 + canary 追踪。
		//
		// [GAP-WIN-P2 fd / file-size] 见 resource_enforce_windows.go canary。
		//
		osCfg.Windows.UseJobObject = true
		osCfg.Windows.MemoryLimit = int64(resLimits.MemoryMaxBytes)
		osCfg.Windows.MaxProcesses = uint32(resLimits.ProcessMax)
		//
		// P2-1 CPU Rate（HARD_CAP）默认值：
		// - 若 ResourceLimits 含 CPUMaxSeconds 预算，则尝试换算：
		// CPUMaxPercent = clamp(100 - slack, 20, 90)
		// 其中 slack 是为宿主留的余量（至少 20% 给宿主）。
		// - 无论 CPUMaxSeconds，默认 70% 是在生产环境安全的上限。
		//
		// 注：CPUMaxSeconds 是总 CPU 秒预算（进程生命周期上限）；
		// CPUMaxPercent 是实时速率上限（每 1 秒窗口内 CPU 时间占比）。
		// 二者正交且互补 — 一个限总时长、一个限瞬时占用。
		osCfg.Windows.CPUMaxPercent = 70
		//
		// P2-1 IORateMax：默认 0（不限制）。Win10+ 需要时可在高级配置中显式设置，
		// 或 P2 后阶段引入 Native_V2 结构后与 CPUMaxPercent 一起映射。
		osCfg.Windows.IORateMax = 0
		// 门 3：网络开关在 Windows 为无操作（Job Object 不隔离网络）。
		// 不宣称 network=on，但开关被忽略这件事必须可观测（canary，
		// fd 2 直写绕过 os.Stderr 日志重定向），否则用户以为开关生效。
		// 功能性无影响：网络本就通，用户意图已满足。
		if allowNetwork {
			fmt.Fprintf(stderrDirect(), "[sandbox] network switch ignored on windows (job object has no network isolation; GAP-WIN-P3)\n")
		}
	case "darwin":
		osCfg.Darwin.UseSeatbelt = true
		// v1 门 1：资源限制经 __whale-rlimit re-exec helper 应用
		//（CPU/NOFILE/FSIZE 生效；AS/NPROC 平台限制跳过，Enter 时 canary）。
		osCfg.Darwin.ResourceLimits = resLimits
		// 门 3：env 开启 → profile 用 (allow network*) 替代 (deny network*)
		osCfg.Darwin.AllowNetwork = allowNetwork
	}
	cfg := &SandboxConfig{
		WorkDir:        workDir,
		OSLevel:        osCfg,
		EnvConfig:      DefaultEnvConfig(),
		ResourceLimits: resLimits, // 复用 :43 同一实例：顶层（Linux prlimit / post-start）与 OSLevel.Darwin（helper）保证同源同值，消除双源漂移
	}
	return NewManager(cfg, SandboxLevelStrict)
}
