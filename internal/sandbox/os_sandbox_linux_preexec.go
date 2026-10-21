//go:build linux

// Package sandbox — Linux pre-exec 加固阶段（helper 进程：限制装好后 execve 真实命令）。
//
// # 为什么要有这一层（以及它为什么不能再叫 "landlock helper"）
//
// 有两类限制在物理上**只能在 execve 之前、且在目标进程内部**装好：
//
//	Landlock  landlock_restrict_self(2) 作用于**调用线程**，且随 execve 继承
//	seccomp   SECCOMP_SET_MODE_FILTER 作用于**调用线程**，且跨 execve 保留、被后代继承
//
// Go 的 os/exec 不提供「fork 后 / exec 前」钩子，所以唯一通路是 re-exec 自身：
//
//	Restrict(cmd) 把命令改写为
//	  <exe> __whale-sandbox-exec -- <origPath> <origArgs...>
//	  env 追加 WHALE_SANDBOX_EXEC_SPEC=<JSON>
//
// 于是这个 helper 就是「沙箱的 exec 阶段」，而不是「Landlock 的搬运工」：
//
// ★ 召唤条件必须是「**任意**一种 pre-exec 机制被启用」，而不是「Landlock 被启用」。
//
//	Landlock 需内核 ≥ 5.13；seccomp 只需 ≥ 3.17。若仍以 UseLandlock 作条件，
//	则 5.13 以下的内核上 helper 根本不会被召唤 ⇒ seccomp 无处可装、静默缺席。
//	（这是本次改动修掉的真实缺陷，不是假想：工厂在 landlockSupported()==false 时
//	会把 UseLandlock 置 false。）
//
// ★ 信封（PreExecSpec）因此是**机制中立**的：两个字段各自可选，helper 按需施加。
//
//	同理，环境变量名不再叫 ..._LANDLOCK_SPEC，否则名字会长期误导后来者。
//
// # 与「降级可观测」纪律的关系
//
// 工厂期（NewPlatformSandbox）负责把「内核不支持」变成**用户可见的降级记录**；
// helper 期一旦信封要求了某项机制却装不上，属异常，直接 fail-closed 退出 126
// （打印原因），绝不「装不上就照常执行」——那会制造虚假的保护感。
//
// 官方源：
//   - man seccomp(2)（过滤器跨 execve 保留、只作用于调用线程）:
//     https://man7.org/linux/man-pages/man2/seccomp.2.html @2026-09-19
//   - docs.kernel.org Landlock（限制随 execve 继承）:
//     https://docs.kernel.org/userspace-api/landlock.html @2026-09-19
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// preExecSpecEnv 携带 pre-exec 加固信封（JSON）的环境变量名（helper 读取）。
const preExecSpecEnv = "WHALE_SANDBOX_EXEC_SPEC"

// PreExecSpec 是父进程（Restrict）传给 helper 的加固信封。
//
// 各字段独立可选；为 nil / false 表示该项不需要施加。
// 之所以不塞进一个 struct 里按机制分文件定义：信封是进程间**单一契约**，
// 分散会导致「加了新机制却忘了在 helper 里读取」这类静默漏装。
type PreExecSpec struct {
	// Landlock 文件系统规则（nil 表示不施加，例如内核 < 5.13）。
	Landlock *LandlockSpec `json:"landlock,omitempty"`
	// Seccomp 是否安装系统调用过滤器（denylist 由 seccompDeniedSyscalls 单一维护）。
	Seccomp bool `json:"seccomp,omitempty"`
	// ResourceLimits 资源上限（SBX-001；nil 表示不施加）。
	//
	// 为什么属于「pre-exec 加固」：setrlimit 作用于**调用进程**并随 execve 继承，
	// 因此这里是唯一能在「命令获得完整能力之前」设好限制的位置。
	// 反例：Start() 之后 prlimit 有窗口，进程炸弹可在窗口内无限 fork。
	//
	// 用指针而非值类型：ResourceLimits 的零值语义是"不限制"，
	// 必须与"未配置"（nil）区分，否则零值会被当成一个要施加的信封字段。
	ResourceLimits *ResourceLimits `json:"resource_limits,omitempty"`
}

// isEmpty 报告信封没要求任何加固（此时 helper 无事可做，应视为调用方 bug）。
//
// ★ 新增加固项时必须同步本函数：漏加会让 helper 把"只要求该项"的
// **有效请求**误判为空信封并 fail-closed 拒绝执行（SBX-001 回归判据）。
func (s *PreExecSpec) isEmpty() bool {
	return s == nil || (s.Landlock == nil && !s.Seccomp && s.ResourceLimits == nil)
}

// preExecSpecFromEnv 解析 cmd.Env 中的 pre-exec 信封；无信封或损坏时返回 nil。
//
// 用途：让 post-start 侧判断「限制是否已经在 execve 前装好了」（兜底让路），
// 见 carriesResourceLimits / platformApplyResourceLimits。
func preExecSpecFromEnv(cmd *exec.Cmd) *PreExecSpec {
	if cmd == nil {
		return nil
	}
	for _, kv := range cmd.Env {
		if !strings.HasPrefix(kv, preExecSpecEnv+"=") {
			continue
		}
		var spec PreExecSpec
		if err := json.Unmarshal([]byte(strings.TrimPrefix(kv, preExecSpecEnv+"=")), &spec); err != nil {
			return nil
		}
		return &spec
	}
	return nil
}

// carriesResourceLimits 报告「该 cmd 的信封是否已要求资源限制」。
//
// 语义 = 限制已由主路径在 execve 之前装好 ⇒ post-start 的 prlimit 必须让路。
// 判别依据是**信封**而不是 Restrict 的调用记录：信封是 helper 唯一读取的契约，
// 以此为准可保证「判定与 helper 实际行为同源」，不会出现
// "父进程以为装了、helper 其实没读到"的谎报（K1 型）。
func carriesResourceLimits(cmd *exec.Cmd) bool {
	spec := preExecSpecFromEnv(cmd)
	return spec != nil && spec.ResourceLimits != nil
}

// buildPreExecSpec 依据沙箱配置组装信封。
//
// 只读【归一化后】的配置（NewManager 已把网络开关归一化进 Namespaces），
// 保证 helper 侧与 banner/状态查询同源，不产生第二个开关真相。
//
// ★ 与 Restrict 的召唤条件必须同口径：Restrict 用「Landlock 或 seccomp 或
// 资源限制」召唤 helper，本函数就要能对这三者都产出非空信封 —— 否则会出现
// "召唤了 helper 但信封为空"，helper 按 isEmpty fail-closed 退出 126
// （有效请求被拒），这是新增加固项时最容易漏的一处（SBX-001 判据②）。
func buildPreExecSpec(cmd *exec.Cmd, cfg LinuxSandboxConfig) (*PreExecSpec, error) {
	spec := &PreExecSpec{}
	if cfg.UseLandlock {
		workspace := cmd.Dir
		if workspace == "" {
			var err error
			workspace, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("resolve workspace: %w", err)
			}
		}
		ll, err := buildLandlockSpec(workspace)
		if err != nil {
			return nil, err
		}
		spec.Landlock = ll
	}
	if cfg.UseSeccomp {
		spec.Seccomp = true
	}
	// 资源限制（SBX-001）：仅在**非零值**时进信封。
	// 零值语义是"不限制"，与"未配置"（nil）不同：把零值结构体塞进信封会让
	// helper 白跑一遍 setrlimit（且 isEmpty 语义被污染）。
	if cfg.ResourceLimits != (ResourceLimits{}) {
		limits := cfg.ResourceLimits
		spec.ResourceLimits = &limits
	}
	if spec.isEmpty() {
		return nil, fmt.Errorf("no pre-exec restriction requested")
	}
	return spec, nil
}

// wrapWithPreExecHelper 将 exec.Cmd 改写为 re-exec helper 形态。
//
// 改写后：
//
//	cmd.Path = <真实可执行文件路径>
//	cmd.Args = [<exePath> __whale-sandbox-exec -- <origPath> <origArgs...>]
//	cmd.Env += WHALE_SANDBOX_EXEC_SPEC=<json>
//
// 信封构造失败（工作区为空等）时返回错误，调用方 fail-closed。
func wrapWithPreExecHelper(cmd *exec.Cmd, cfg LinuxSandboxConfig) error {
	spec, err := buildPreExecSpec(cmd, cfg)
	if err != nil {
		return fmt.Errorf("pre-exec restrict: %w", err)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("pre-exec restrict: marshal spec: %w", err)
	}

	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	// 去掉可能存在的旧信封值再追加（防环境串扰）
	env := make([]string, 0, len(cmd.Env)+1)
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, preExecSpecEnv+"=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, preExecSpecEnv+"="+string(specJSON))

	// 用真实可执行文件路径而非 /proc/self/exe：
	// 新 USER namespace 下进程被内核标记 non-dumpable，/proc/<pid>/* 归属
	// root:root；部分发行版（Ubuntu 23.10+ AppArmor unprivileged_userns 策略）
	// 会对该组合下的 /proc/self/exe exec 返回 EACCES（VM 实测复现）。
	// 真实路径 exec 仅受常规 DAC 检查，且不依赖 /proc 挂载（最小环境友好）。
	exePath, exeErr := os.Executable()
	if exeErr != nil || exePath == "" {
		exePath = "/proc/self/exe" // 兜底：无法解析真实路径（极罕见）
	}

	origPath := cmd.Path
	cmd.Path = exePath
	cmd.Args = append([]string{exePath, SandboxExecHelperArg, "--", origPath}, cmd.Args[1:]...)
	return nil
}

// RunPreExecHelper 是 re-exec helper 入口（main.go 顶部分发调用，不返回）。
//
// 期望形态：
//
//	argv: <exe> __whale-sandbox-exec -- <targetPath> <targetArgs...>
//	env:  WHALE_SANDBOX_EXEC_SPEC=<PreExecSpec JSON>
//
// 流程（顺序有讲究，见各步注释）：
//
//  1. 解析并校验信封 / argv —— 全部校验放在任何不可逆动作之前
//  2. runtime.LockOSThread —— 保证「装限制的线程」就是「执行 execve 的线程」
//  3. 若处于未映射的新 USER namespace，先写 uid/gid 映射
//  4. 施加 Landlock（若要求）
//  5. 施加 seccomp（若要求）
//  6. 施加资源上限（若要求）—— 放在准备动作之后，让准备工作不受自设限制约束
//  7. 施加防 core dump 加固（SBX-020，**无条件**，不依赖信封字段）—— 只在
//     execve 之前才可能生效，因此必须放这里（见 core_dump_hardening_linux.go）
//  8. 剥离信封 env 后 execve 目标命令
//
// 任一步失败：stderr 打印原因并 exit 126（可观测，非静默）。
func RunPreExecHelper() {
	exitCode := 126 // EX_NOTUSABLE：helper 失败的约定退出码

	// ---- 1. 校验（全部前置）----
	spec, ok := loadPreExecSpec()
	if !ok {
		os.Exit(exitCode)
	}
	if len(os.Args) < 4 || os.Args[2] != "--" {
		fmt.Fprintf(os.Stderr, "whale-sandbox-exec: bad argv (want -- target...)\n")
		os.Exit(exitCode)
	}
	target := os.Args[3:]
	targetPath := target[0]

	// ---- 2. 线程绑定（必须在任何"作用于线程"的限制之前）----
	//
	// Landlock 与 seccomp **都是线程级**：限制只落在执行安装的那条 OS 线程上。
	// Go 调度器允许 goroutine 在 M 之间迁移，因此若在安装与 execve 之间发生
	// 迁移，限制会落在一条不再执行 execve 的线程上 ⇒ 静默 fail-open。
	// LockOSThread 把这段路径钉死在同一线程，把该窗口物理消除。
	// （不可逆性可接受：本函数以 execve 或 os.Exit 结束，不会归还线程。）
	runtime.LockOSThread()

	// ---- 3. USER namespace 映射 ----
	mapUserNamespace()

	// ---- 4/5. 施加限制 ----
	if spec.Landlock != nil {
		if err := applyLandlockSpec(spec.Landlock); err != nil {
			fmt.Fprintf(os.Stderr, "whale-sandbox-exec: %v\n", err)
			os.Exit(exitCode)
		}
	}
	if spec.Seccomp {
		// 顺序：seccomp 放在 Landlock 之后。
		// 过滤器一旦装好即不可移除，而 Landlock 建规则需要 openat/add_rule 等
		// 准备动作；把「覆盖面更宽、且与准备动作无关」的 seccomp 放后，
		// 可保证所有准备工作都在无自设限制的条件下完成，顺序本身也不改变
		// 最终策略（过滤器是叠加求交的）。
		if err := applySeccompFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "whale-sandbox-exec: %v\n", err)
			os.Exit(exitCode)
		}
	}
	// ---- 6. 资源上限（SBX-001）----
	//
	// 必须在 execve **之前**、且在上述准备工作**之后**：
	//   - 之前：这是"命令获得完整能力之前"的唯一位置，窗口物理归零；
	//   - 之后：Landlock 要取 fd、seccomp 要分配 cBPF 内存，而资源上限是
	//     三者中约束最强的（尤其 RLIMIT_NOFILE / RLIMIT_AS），先设会卡住准备。
	// setrlimit 本身不需要任何前置资源。
	//
	// 失败即 fail-closed：主路径装不上不能降级（降级会制造"以为有资源门"的
	// 虚假保护感），与 post-start 兜底的 best-effort 姿态刻意不同。
	if spec.ResourceLimits != nil {
		if err := applyResourceLimitsToSelf(*spec.ResourceLimits); err != nil {
			fmt.Fprintf(os.Stderr, "whale-sandbox-exec: %v\n", err)
			os.Exit(exitCode)
		}
	}

	// ---- 7. 防 core dump 加固（SBX-020）----
	//
	// ★ **无条件施加**，不看信封：core dump 在沙箱内没有正当用途（会把内存镜像
	// 落盘到工作区），因此它不是"用户可选的资源配额"，而是像 seccomp 一样不可
	// 关闭的加固。做成信封字段 = 给了一个会被关掉的开关 ⇒ 安全性归零。
	//
	// ★ 只能放在 execve 之前：RLIMIT_CORE 随 execve 继承到目标命令及其后代，
	// 放在 Start() 之后则存在"命令已能写 core"的窗口（同 SBX-001 的教训）。
	// 注意**不要**顺手加 prctl(PR_SET_DUMPABLE,0)：execve 会把它复位为 1，
	// 在此处加等于谎报（证据与说明见 core_dump_hardening_linux.go 文件头）。
	//
	// 失败即 fail-closed（exit 126），与资源上限同姿态：装不上不得静默收窄。
	if err := applyCoreDumpHardeningToSelf(); err != nil {
		fmt.Fprintf(os.Stderr, "whale-sandbox-exec: %v\n", err)
		os.Exit(exitCode)
	}

	// ---- 8. execve ----
	// exec.Command 构造时已解析 PATH；相对路径（./x.sh）以 cwd（=cmd.Dir）解析
	if !strings.Contains(targetPath, "/") {
		if lp, err := exec.LookPath(targetPath); err == nil {
			targetPath = lp
		}
	}

	// 剥离信封 env，避免泄漏给目标命令
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, preExecSpecEnv+"=") {
			continue
		}
		env = append(env, kv)
	}

	if err := unix.Exec(targetPath, target, env); err != nil {
		fmt.Fprintf(os.Stderr, "whale-sandbox-exec: exec %s: %v\n", targetPath, err)
		os.Exit(exitCode)
	}
}

// loadPreExecSpec 读取并校验信封；失败时已打印原因，返回 ok=false。
func loadPreExecSpec() (*PreExecSpec, bool) {
	specJSON := os.Getenv(preExecSpecEnv)
	if specJSON == "" {
		fmt.Fprintf(os.Stderr, "whale-sandbox-exec: missing %s env\n", preExecSpecEnv)
		return nil, false
	}
	var spec PreExecSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		fmt.Fprintf(os.Stderr, "whale-sandbox-exec: bad spec: %v\n", err)
		return nil, false
	}
	if spec.isEmpty() {
		// 空信封 = 调用方本不该召唤 helper。fail-closed：宁可明确失败，
		// 也不要「什么都没装就 exec」——那正是虚假保护。
		fmt.Fprintf(os.Stderr, "whale-sandbox-exec: spec requests no restriction\n")
		return nil, false
	}
	return &spec, true
}

// mapUserNamespace 在新 USER namespace 内写 uid/gid 映射（0 <ruid> 1）。
//
// 背景：clone(CLONE_NEWUSER) 后若不写 uid_map，进程对文件属主视图为
// overflowuid（65534），对用户属主文件的 DAC 写检查失败（EACCES），
// 连工作区都写不了。写入单行「自身 uid → 0」映射是 rootless 容器的标准做法。
//
// 幂等且无害：不在新 userns / 已映射时写入失败（EPERM/EINVAL/EBUSY），忽略。
// 顺序固定：先 setgroups=deny，再 gid_map，再 uid_map（内核要求）。
func mapUserNamespace() {
	uid := unix.Getuid()
	gid := unix.Getgid()

	_ = os.WriteFile("/proc/self/setgroups", []byte("deny"), 0o644)
	_ = os.WriteFile("/proc/self/gid_map", []byte(fmt.Sprintf("0 %d 1\n", gid)), 0o644)
	_ = os.WriteFile("/proc/self/uid_map", []byte(fmt.Sprintf("0 %d 1\n", uid)), 0o644)
}
