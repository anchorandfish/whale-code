//go:build darwin

// Package sandbox — macOS rlimit re-exec helper（v1 门 1）。
//
// macOS 不支持 prlimit(2) 作用于其他进程，setrlimit 仅影响调用进程；
// 因此资源限制通过 re-exec helper 应用（与 Linux Landlock helper 同模式）：
//
//	Restrict(cmd) 将（已完成 Seatbelt 包装的）cmd 最外层改写为：
//
// <exe> __whale-rlimit -- sandbox-exec -p <profile> -- <target>...
// env 追加 WHALE_SANDBOX_RLIMIT_SPEC=<ResourceLimits JSON>
//
//	helper 进程（main.go 顶部分发 → RunRlimitHelper）：
//
// 1. setrlimit：CPU / NOFILE / FSIZE（cur=max，写入超限触发 SIGXFSZ/SIGXCPU）
// 2. 剥离 spec env（防泄漏给目标命令）
// 3. execve 目标命令（限制随 execve 链继承到 sandbox-exec 及最终命令）
//
// 平台限制（跳过项，降级记录走审计通道，禁止静默）：
// - RLIMIT_AS：dyld 加载二进制时预留大块虚拟地址空间，设 AS 上限会
// 误杀正常命令（macOS 社区共识），内存失控由进程组超时 kill 兜底。
// - RLIMIT_NPROC：macOS 按 UID 全局计数（与 Linux VM 实测教训一致），
// 沙箱内 fork 失败与否取决于用户现有进程总数，低于该数会误杀沙箱内
// 所有 fork。fork bomb 由进程组超时 kill 兜底。
//
// ★ 这两项"被跳过"的**记录出口**（2026-09-30 B-2 起）= 沙箱初始化侧的审计通道
// （os_sandbox_darwin.go Enter → audit.LogDegradation），
// **不再写目标进程的 stderr**：helper 的 stderr 被目标进程继承，写在那里会被
// Monitor 当"子进程输出"回显给用户。理由与守卫见 applyDarwinRlimits 的注释。
//
// 官方源：
// - https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setrlimit.2.html @2026-07-21
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// rlimitSpecEnv 携带资源限制 JSON 的环境变量名（helper 读取）。
const rlimitSpecEnv = "WHALE_SANDBOX_RLIMIT_SPEC"

// wrapWithRlimitHelper 将 exec.Cmd 最外层包装为 rlimit re-exec helper。
//
// 前置：cmd 已完成 Seatbelt 包装（helper 在 sandbox-exec 外层，setrlimit
// 经 execve 链继承）。cmd 未启动（cmd.Process == nil，由调用方 Restrict 保证）。
func wrapWithRlimitHelper(cmd *exec.Cmd, limits ResourceLimits) error {
	if limits == (ResourceLimits{}) {
		return nil // 零值：未配置，不包装
	}
	specJSON, err := json.Marshal(limits)
	if err != nil {
		return fmt.Errorf("rlimit wrap: marshal spec: %w", err)
	}
	exePath, exeErr := os.Executable()
	if exeErr != nil || exePath == "" {
		return fmt.Errorf("rlimit wrap: resolve executable: %v", exeErr)
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	// 去掉可能存在的旧 spec 值再追加（防环境串扰）
	env := make([]string, 0, len(cmd.Env)+1)
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, rlimitSpecEnv+"=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, rlimitSpecEnv+"="+string(specJSON))

	origPath := cmd.Path
	cmd.Path = exePath
	cmd.Args = append([]string{exePath, RlimitHelperArg, "--", origPath}, cmd.Args[1:]...)
	return nil
}

// RunRlimitHelper 是 re-exec helper 入口（main.go 顶部分发调用，不返回）。
//
// 期望形态：
//
//	argv: <exe> __whale-rlimit -- <targetPath> <targetArgs...>
//	env: WHALE_SANDBOX_RLIMIT_SPEC=<ResourceLimits JSON>
//
// 任一步失败：stderr 打印原因并 exit 126（可观测，非静默）。
func RunRlimitHelper() {
	exitCode := 126 // EX_NOTUSABLE：helper 失败的约定退出码

	specJSON := os.Getenv(rlimitSpecEnv)
	if specJSON == "" {
		fmt.Fprintf(os.Stderr, "whale-rlimit-helper: missing %s env\n", rlimitSpecEnv)
		os.Exit(exitCode)
	}
	var limits ResourceLimits
	if err := json.Unmarshal([]byte(specJSON), &limits); err != nil {
		fmt.Fprintf(os.Stderr, "whale-rlimit-helper: bad spec: %v\n", err)
		os.Exit(exitCode)
	}

	applyDarwinRlimits(limits)

	if len(os.Args) < 4 || os.Args[2] != "--" {
		fmt.Fprintf(os.Stderr, "whale-rlimit-helper: bad argv (want -- target...)\n")
		os.Exit(exitCode)
	}
	target := os.Args[3:]
	targetPath := target[0]
	// exec.Command 构造时已解析 PATH；相对路径（./x.sh）以 cwd（=cmd.Dir）解析
	if !strings.Contains(targetPath, "/") {
		if lp, err := exec.LookPath(targetPath); err == nil {
			targetPath = lp
		}
	}

	// 剥离 spec env，避免泄漏给目标命令
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, rlimitSpecEnv+"=") {
			continue
		}
		env = append(env, kv)
	}

	if err := syscall.Exec(targetPath, target, env); err != nil {
		fmt.Fprintf(os.Stderr, "whale-rlimit-helper: exec %s: %v\n", targetPath, err)
		os.Exit(exitCode)
	}
}

// applyDarwinRlimits 在当前进程应用资源限制（随后 execve 继承）。
//
// 生效：RLIMIT_CPU / RLIMIT_NOFILE / RLIMIT_FSIZE（cur=max）。
// 跳过：RLIMIT_AS / RLIMIT_NPROC（平台限制，见文件头注释）。
// setrlimit 单项失败：fail-open + stderr 可观测（该限制缺失不阻断命令；
// 其余限制已生效，整体姿态仍优于修复前的全无限制）。
func applyDarwinRlimits(limits ResourceLimits) {
	type rlSpec struct {
		name  string
		res   int
		value uint64
	}
	specs := []rlSpec{
		{"RLIMIT_CPU", syscall.RLIMIT_CPU, limits.CPUMaxSeconds},
		{"RLIMIT_NOFILE", syscall.RLIMIT_NOFILE, limits.FileDescriptorMax},
		{"RLIMIT_FSIZE", syscall.RLIMIT_FSIZE, limits.FileSizeMaxBytes},
	}
	for _, s := range specs {
		if s.value == 0 {
			continue
		}
		limit := syscall.Rlimit{Cur: s.value, Max: s.value}
		if err := syscall.Setrlimit(s.res, &limit); err != nil {
			fmt.Fprintf(os.Stderr, "whale-rlimit-helper: setrlimit %s=%d failed: %v\n", s.name, s.value, err)
		}
	}
	// ★ 缺失的正是"RLIMIT_AS / RLIMIT_NPROC 在 macOS 被跳过"这条**降级说明**，
	// 它曾经在这里被**无条件** Fprintf 到 os.Stderr（B-2，2026-09-30）。
	//
	// 为什么删（不是"忘了写出口"）：
	// helper 是 whale **自己的二进制 re-exec**，其 stderr **被目标进程继承**
	// （:70 把 cmd.Path 改成 exePath ⇒ :120 syscall.Exec 目标命令）
	// ⇒ whale 自己的诊断被 Monitor 当成"子进程输出"回显给用户。
	// 真机实证：I-7.1，用户跑 `node server.js`，输出里混进
	// `📡 [mon_1] [stderr] whale-rlimit-helper: RLIMIT_AS skipped on darwin (…)` ×2。
	//
	// 这是**分类轴选错**：'macOS 不支持这两个 rlimit' 是**平台能力事实**，
	// 属于沙箱初始化，不属于每一次命令执行 —— 写在这里等于把一个平台事实
	// 重复到每一次执行上，而且写在**别人的** stderr 上。
	//
	// 降级记录的唯一出口 = os_sandbox_darwin.go Enter() 的
	// audit.LogDegradation(…, "…RLIMIT_AS/RLIMIT_NPROC skipped…")
	// （持久审计文件 + 内存计数 + 启动 banner 经 DegradeReasons() 可见）。
	// ★ 覆盖性可证：MemoryMaxBytes>0 ∨ ProcessMax>0 ⟹ ResourceLimits 非零
	// ⟹ manager.Prepare 必经 osSandbox.Enter()（manager.go:218）⟹ 该记录必被写；
	// 反方向：wrapWithRlimitHelper 在 limits == 零值时直接 return（:44-46）
	// ⇒ 不存在"helper 跑了而审计未记"的窗口。
	// ★ 守卫：internal/sandbox/b2_rlimit_notice_channel_test.go 的判据 C 钉住该出口
	// （谁删掉 Enter 侧那行审计，它会红）。
	//
	// ⚠️ 与"失败路径保留 stderr"的区别（别把两者混为一谈）：
	// 上方 setrlimit 失败、:87/:92/:99/:121 —— 这些是**本次执行真的出错了**
	// （异常：限制没生效 / spec 坏了 / exec 失败），必须即时可见，**一律保留**。
	// 而被删掉的这两条是**每次都会发生的常态事实**（macOS 必然跳过），
	// 它不该在每一次命令执行时重复刷进别人的 stderr。
}
