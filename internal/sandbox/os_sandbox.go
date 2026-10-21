// Package sandbox — OS 级隔离统一接口（/ T1-P1-B9）。
package sandbox

import (
	"os/exec"
	"sync"
)

// OSSandbox 是 OS 级隔离的统一接口。
type OSSandbox interface {
	Enter() (handle interface{}, cleanup func(), err error)
	Exit() error
	Restrict(cmd *exec.Cmd) error
	PostStartHook() func(*exec.Cmd) error
	Platform() string
}

// OSLevelConfig 是 OS 级隔离配置。
type OSLevelConfig struct {
	Linux   LinuxSandboxConfig
	Windows WindowsSandboxConfig
	Darwin  DarwinSandboxConfig
}

// LinuxSandboxConfig 是 Linux namespace + Landlock + seccomp 沙箱配置。
//
// 三层判定面相互正交（各自拦得住的东西不同，不是"叠得越多越严"）：
//
//	namespaces 进程「看得见」什么（PID/NET/UTS/MNT 视图）
//	Landlock 「路径」能否读写/执行（路径级，内核 ≥5.13）
//	seccomp 「能否发起某类内核操作」（syscall 级，内核 ≥3.17）
type LinuxSandboxConfig struct {
	UseNamespaces    bool
	Namespaces       []string
	UseUserNamespace bool
	UseLandlock      bool
	// UseSeccomp（P2-3）启用 seccomp-bpf 系统调用过滤。
	//
	// 与 UseLandlock 相互**独立**：Landlock 需内核 ≥5.13，seccomp 只需 ≥3.17，
	// 因此内核 <5.13 时本项仍然生效（pre-exec helper 的召唤条件是二者相或，
	// 见 wrapWithPreExecHelper）——若把 seccomp 挂在 UseLandlock 之下，
	// 它会在旧内核上静默缺席。
	//
	// 与 UseNamespaces 的关系：网络断连由 CLONE_NEWNET 负责，seccomp 不重复
	// 承担网络类 syscall，避免出现"第二个网络开关"（单一事实源）。
	//
	// 拒绝清单与收录判据见 os_sandbox_linux_seccomp.go（单一维护点）。
	UseSeccomp bool
	// AllowNetwork（门 3 粗粒度网络开关）：true 时 NewManager 归一化过滤
	// Namespaces 中的 "net"（不创建 CLONE_NEWNET，沙箱内可访问宿主网络，
	// 供 pip install 类操作使用）；默认 false 网络全断。Landlock/其余
	// namespace 隔离不受影响。
	AllowNetwork bool
	// ResourceLimits（SBX-001）：经 pre-exec helper 在 **execve 之前** setrlimit
	// 施加（RLIMIT_CPU / AS / NOFILE / NPROC / FSIZE）。
	//
	// 为什么必须走 helper 而不是 Start() 之后的 prlimit：Prlimit 只能作用于
	// **已存在**的进程，因此必然存在「Start() 返回 到 prlimit() 生效」之间的
	// 无限制窗口，进程炸弹专打这个窗口。放进信封后窗口物理归零。
	//
	// 与 DarwinSandboxConfig.ResourceLimits 同形（同一语义 cross-platform 同名）。
	// 若因降级（namespaces 不可用等）未走 helper，platformApplyResourceLimits
	// 仍保留 prlimit 兜底路径并记降级，保证能力不静默消失。
	ResourceLimits ResourceLimits
}

// SandboxExecHelperArg 是 pre-exec 加固 helper 的分发参数
// （main.go 顶部拦截：os.Args[1] == SandboxExecHelperArg → RunPreExecHelper）。
//
// 命名刻意保持机制中立：该 helper 同时承载 Landlock 与 seccomp，
// 两者都必须在 execve 前装好。若沿用按机制命名（如 ...-landlock），
// 后来者会以为它只做文件系统限制。
const SandboxExecHelperArg = "__whale-sandbox-exec"

// WindowsSandboxConfig 是 Windows Job Object 沙箱配置。
//
// ====== 已实现（P0/P1/P2-1）======
// - UseJobObject: Job Object 资源限制容器（内存/进程数/CPU 速率）
// - MemoryLimit / MaxProcesses: P0 已实现
// - CPUMaxPercent: P2-1 实现 JobObjectCpuRateControlInformation HARD_CAP
// - IORateMax: P2-1 预留字段（结构存在，实现 Win10+ 才生效）
//
// ====== 文档缺口（P2/P3，未实现，需显式追踪不可静默）======
//
//	[GAP-WIN-P2: AppContainer] Windows 原生细粒度沙箱（同类 CLI code 使用）。
//
// 需要 PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES + SID/能力声明，
// 复杂度高，受跨进程 COM/文件重定向/命名对象隔离影响。
// 当前策略：Job Object 为主（资源限制 + kill on close），
// AppContainer 延后 P2（需独立创建 SID + 能力清单 + 兼容性矩阵）。
// 对标同类 CLI AppContainer SID 自动生成 + 最小能力集。
//
//	[GAP-WIN-P2: fd 限制] Windows 无 RLIMIT_NOFILE 直接等效。
//
// 间接方案：JOB_OBJECT_LIMIT_JOB_MEMORY 约束句柄占用的内核池内存；
// platformApplyResourceLimits 在 FileDescriptorMax>0 时打降级 canary
// 日志（可观测不静默）。用户可通过 MemoryLimit 间接收敛 fd 耗尽风险。
//
//	[GAP-WIN-P2: 文件大小限制] Windows 无 RLIMIT_FSIZE 直接等效。
//
// 间接方案：NTFS Quota（需管理员，单用户全局，不可行）或
// JobObjectIoRateControlInformation I/O 速率限制（Win10+）。
// 当前降级为 canary 日志 + Job Memory 限制兜底（写超大文件先占内存）。
//
//	[GAP-WIN-P3: 访问控制] Job Object 仅资源限制不是访问控制。
//
// 完整文件系统/网络隔离需：Restricted Token (DenyOnly SIDs) +
// 目录 DACL 拒绝写入 + WFP 网络过滤。延后 P3 独立方案。
//
// 降级约定：所有 [GAP-*] 项在对应 ResourceLimits 设置非零值时，
// platformApplyResourceLimits 必须打 canary 降级日志（fail-open + 可观测）
// ，禁止静默吞掉配置意图（静默防护教训：无出口的降级不可见）。
type WindowsSandboxConfig struct {
	// UseJobObject 是否启用 Job Object。
	UseJobObject bool
	// MemoryLimit 进程内存上限（字节，0=不限制）。
	MemoryLimit int64
	// MaxProcesses 最大进程数上限（0=不限制，防止 fork bomb）。
	MaxProcesses uint32
	// CPUMaxPercent CPU 速率上限（百分比 0-100，0=不限制）。
	// 使用 JOB_OBJECT_CPU_RATE_CONTROL_ENABLE | HARD_CAP（内核级硬节流，非通知）。
	// 生效条件：Win8+/Server 2012+。老版本设置失败时打降级 canary，
	// 不返回错误（fail-open：CPU 限制非致命，可由内存/进程数限制兜底）。
	CPUMaxPercent uint32
	// IORateMax I/O 速率上限（字节/秒，0=不限制）。
	// 使用 JobObjectIoRateControlInformation（Win10+/Server 2016+）。
	// 老版本设置失败时打降级 canary，不返回错误。
	IORateMax uint64
}

// DarwinSandboxConfig 是 macOS Seatbelt 沙箱配置。
type DarwinSandboxConfig struct {
	UseSeatbelt     bool
	SeatbeltProfile string
	// AllowNetwork（门 3 粗粒度网络开关）：true 时 Seatbelt profile 用显式
	// (allow network*) 替代 (deny network*)（(deny default) 姿态下仅省略
	// deny 规则网络仍被拒，必须显式 allow）；默认 false 网络全断。
	// 文件/进程隔离规则不受影响。
	AllowNetwork bool
	// ResourceLimits（v1 门 1）：经 __whale-rlimit re-exec helper 在 exec 前
	// setrlimit 应用（CPU/NOFILE/FSIZE）。RLIMIT_AS（dyld 虚拟地址预留会误杀
	// 正常二进制）与 RLIMIT_NPROC（按 UID 全局计数，低于用户现有进程数会
	// 误杀沙箱内所有 fork）在 macOS 跳过，Enter 时 canary 可观测。
	ResourceLimits ResourceLimits
}

const (
	DefaultLinuxNamespaces            = "pid,net,uts"
	DefaultWindowsMemoryLimit  int64  = 512 * 1024 * 1024
	DefaultWindowsMaxProcesses uint32 = 64
)

// RlimitHelperArg 是 macOS rlimit re-exec helper 的分发参数
// （main.go 顶部拦截：os.Args[1] == RlimitHelperArg → RunRlimitHelper）。
const RlimitHelperArg = "__whale-rlimit"

// IsSandboxHelperInvocation 判定 argv 是否表示「本进程被当作沙箱 re-exec helper 拉起」。
//
// ★ 唯一的 helper 分发判定点（SSOT）。生产入口（main.go）、本包 init 的结构性分发
// （helper_dispatch.go）、以及各测试二进制的守卫都必须调用它，不得各自手写
// `os.Args[1] == ...` 比较（：历史上四处各写一遍，漏掉一处就递归）。
func IsSandboxHelperInvocation(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return args[1] == SandboxExecHelperArg || args[1] == RlimitHelperArg
}

// HandleSandboxHelperArg 在 argv 命中沙箱 helper 参数时进入 helper 分支（不返回）；
// 否则立即返回。
//
// ★ 唯一的 helper 分发实现点（SSOT）。restrict 把命令最外层改写为
//
//	<exe> __whale-sandbox-exec -- <cmd> <args...> （linux pre-exec：Landlock + seccomp）
//	<exe> __whale-rlimit -- sandbox-exec ... （darwin：setrlimit 后 exec）
//
// 被 re-exec 的进程必须在**任何测试/CLI 初始化之前**命中这个分支，否则它会当成
// 普通进程继续跑 —— 而测试二进制没有 main.go，后果是**递归重跑整个测试套件并挂死**
// （；症状是"挂住"而不是变红，极难定位）。
//
// 本包 init（helper_dispatch.go）已自动完成这一层，覆盖**所有**链接了 internal/sandbox
// 的二进制，包括测试二进制；main.go 保留一次显式调用作为可读入口。
func HandleSandboxHelperArg(args []string) {
	if len(args) < 2 {
		return
	}
	switch args[1] {
	case SandboxExecHelperArg:
		RunPreExecHelper()
	case RlimitHelperArg:
		RunRlimitHelper()
	}
}

func DefaultOSLevelConfig() OSLevelConfig {
	return OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    false,
			Namespaces:       nil,
			UseUserNamespace: true,
		},
		Windows: WindowsSandboxConfig{
			UseJobObject: false,
		},
		Darwin: DarwinSandboxConfig{
			UseSeatbelt: false,
		},
	}
}

// degradedSandbox 是降级实现。
type degradedSandbox struct {
	reason string
	audit  *AuditLog
	exited bool
	mu     sync.Mutex
}

// newDegradedSandbox 构造降级实现，并**在构造点**把原因写进审计。
//
// ★ 为什么记录点是"构造"而不是"首次 Enter"（2026-10-09， 接线连带修）
//
// 启动 banner（`PrintIsolationBanner`）在工厂返回后**立即**打印，而 `Enter`
// 要到第一条命令才发生 ⇒ 记录放在 `Enter` 时，banner 只能印出平台名 `degraded`、
// **印不出原因**（用户看得到"没有隔离"、看不到"为什么/怎么办"）。
// 原因在工厂里就定了，记录点就应在工厂 —— 这样 banner 的降级行当场可见。
//
// 兼容：`ContainsDegradation()` 的既有断言只查"有没有"（不查"何时"），
// 记录前移后仍然为真；且 `Enter` 不再重复记录，避免同一条原因出现两遍。
func newDegradedSandbox(reason string, audit *AuditLog) *degradedSandbox {
	d := &degradedSandbox{reason: reason, audit: audit}
	if d.audit != nil {
		d.audit.LogDegradation(SandboxLevelIsolated, SandboxLevelRelaxed, d.reason)
	}
	return d
}

func (d *degradedSandbox) Enter() (interface{}, func(), error) {
	// 降级原因已在 newDegradedSandbox 记录（见上），此处不得重复记录。
	cleanup := func() {}
	return nil, cleanup, nil
}

func (d *degradedSandbox) Exit() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exited = true
	return nil
}

func (d *degradedSandbox) Restrict(_ *exec.Cmd) error {
	return nil
}

func (d *degradedSandbox) PostStartHook() func(*exec.Cmd) error {
	return nil
}

func (d *degradedSandbox) Platform() string {
	return "degraded"
}
