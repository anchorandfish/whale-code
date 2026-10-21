// Package sandbox — 门 2 降级可见性：实际隔离状态查询与用户可见 banner。
//
// 修复前：Landlock 不支持（内核 <5.13）等降级只有 LogDegradation 审计日志，
// TUI/CLI 完全不可见（静默防护教训：无出口的降级不可见）。
//
// 本文件提供：
// - IsolationStatusOf(mgr)：从 Manager 读出实际隔离状态
// （平台 / 生效特性 / 降级原因 / 级别）
// - FormatIsolationBanner(st)：单行 banner + 逐行降级原因，
// 由 TUI lifecycle 与 Executor.EnableProductionSandbox（plain/bridge）
// 在启动时打到 stderr。
//
// 特性按平台计算（describeIsolationFeatures，各平台文件内实现），
// 降级原因来自 AuditLog.DegradeReasons()（工厂期 + Enter 期累计）。
package sandbox

import (
	"fmt"
	"io"
	"strings"
	"syscall"
)

// IsolationStatus 是当前 Manager 的实际隔离状态快照。
type IsolationStatus struct {
	// Level 是配置的沙箱级别（Strict/Relaxed/Isolated）。
	Level SandboxLevel
	// Platform 是 OS 沙箱平台标识（linux-namespaces / darwin-seatbelt /
	// windows / degraded / unknown）。
	Platform string
	// Features 是实际生效的隔离特性（如 namespaces(pid,net)、landlock、
	// job-object(mem=512M)）。degraded 沙箱为空。
	Features []string
	// Degradations 是降级原因（来自审计日志，含 same-level 子能力降级）。
	Degradations []string
}

// IsolationStatusOf 返回 mgr 的实际隔离状态。
//
// mgr 为 nil 或非 *manager 实现（测试替身）时返回 Platform="unknown"
// （不 panic，降级路径有可观测出口）。
func IsolationStatusOf(mgr Manager) IsolationStatus {
	m, ok := mgr.(*manager)
	if !ok || m == nil {
		return IsolationStatus{Platform: "unknown"}
	}
	st := IsolationStatus{Level: m.Level()}
	if m.osSandbox == nil {
		st.Platform = "unknown"
	} else {
		st.Platform = m.osSandbox.Platform()
	}
	// degraded（配置禁用/内核不支持/平台不支持）：无 OS 级特性可宣称
	if st.Platform != "degraded" && st.Platform != "unknown" && m.config != nil {
		st.Features = describeIsolationFeatures(m.config.OSLevel)
	}
	if m.audit != nil {
		st.Degradations = m.audit.DegradeReasons()
	}
	return st
}

// EnforcementActive 报告 mgr 的 OS 级沙箱**是否真的能把进程关在边界内**。
//
// ★ 这是 同类 CLI `assess_patch_safety` 前提检查（core/src/safety.rs:78-98）的对应物：
// 自动免问**挂在「沙箱能否被真正强制」上**，装不上就退回询问（AskUser）。
// whale-code 此前**完全缺失这一问** —— 「沙箱是否真在强制」这个事实被算出来了，
// 却只喂给启动 banner（PrintIsolationBanner），审批层拿不到 ⇒ 本函数是那条断线的接线端。
//
// 判据（fail-closed，宁少不多；任一条不满足即 false）：
//
// 1. mgr 非 nil 且级别 != Disabled；
// 2. Platform 是**能真正限制文件系统写**的平台：
// - `darwin-seatbelt`：SBPL profile 限制可写根 + 敏感读 + 网络；
// - `linux-namespaces`：**且** Features 含 `landlock`（只有 namespaces 时文件系统
// 不受限；Landlock 不可用时 describeIsolationFeatures 根本不会列出它）；
// - ★ **`windows` 不在列**：Job Object 只做资源限制，非文件系统/网络 DACL
// （production.go:37-39 GAP-WIN-P3）⇒ 进程在 Windows 上**不受文件系统约束**，
// 把 Windows 算作"已强制"会直接变成"既无边界也不问"；
// 3. Features **不含** `network=on`（用户经 env 显式放行网络）：
// 网络是沙箱拦不住的**第二条通道**，进程可借它把边界内可读的内容送出边界
// ⇒ 此时不得宣称"效果全部落在边界内"。Windows 的"开关被忽略"同理落 false。
// 4. darwin 上**未启用用户自定义 Seatbelt profile**（`SeatbeltProfile != ""`）：
// 自定义 profile 是逃生开关，规则由用户自己写，本包**不解析**它 ⇒
// "文件系统是否真被限制、网络是否真被拒"变成**不可核实**。
// ⚠️ 特别值得注意：`describeIsolationFeatures` 在自定义 profile 下
// **刻意不宣称** `network=on`（banner 不得说谎，见 os_sandbox_darwin.go:361-365）
// ⇒ 光看 Features **看不出**用户的 profile 是否放行了网络。若不单独判这一条，
// 就等于把"无法核实"读成"已强制" ⇒ 静默放行。（`os_sandbox_darwin.go:305`
// 是 `sandbox-exec -f <profile>` 的写入点。）
//
// ★ 为什么**不**看 Degradations：darwin 生产装配恒会记一条 **rlimit 子能力**降级
// （os_sandbox_darwin.go 的 Enter() 里 `ResourceLimits != {}` 即 LogDegradation）
// ⇒ 把"有降级"当"无强制"会让本函数在**本机永远为 false**（等于恢复成"每次都问"，
// 白改一场）。降级的**粒度**必须区分：rlimit 是资源子能力，不动文件系统边界。
func EnforcementActive(mgr Manager) bool {
	if mgr == nil {
		return false
	}
	if mgr.Level() == SandboxLevelDisabled {
		return false
	}
	st := IsolationStatusOf(mgr)
	if customSeatbeltProfileActive(mgr, st.Platform) {
		return false
	}
	return osEnforcementSufficient(st)
}

// osEnforcementSufficient 是**纯粹判据**：给定一份隔离状态，「进程是否真的被关在边界内」。
//
// 为什么把判定从 EnforcementActive 里抽出来（而不是就地写在里面）：
// 安全相关的分支必须**在任何平台都能被断言**。最典型的是 Windows 分支
// —— 它必须为 false（Job Object 不动文件系统/网络，GAP-WIN-P3），
// 但在 macOS 的开发机与 CI 上**无法**通过真实 manager 触达（`newPlatformSandbox`
// 按构建平台选择实现）。留在 EnforcementActive 里 ⇒ 这条分支只能靠"人记得"
// 维持，改动时没有任何测试会红。抽成纯函数后，它由 table-driven 钉死。
//
// 判据（fail-closed，任一条不满足即 false）：
// - `darwin-seatbelt`：SBPL profile 限制可写根 + 敏感读 + 网络；
// - `linux-namespaces`：**且** Features 含 `landlock`（只有 namespaces 时文件系统
// 不受限；Landlock 不可用时 describeIsolationFeatures 根本不会列出它）；
// - 其余一律 false，其中 **`windows` 必须为 false**：Job Object 只做资源限制，
// 非文件系统/网络 DACL（production.go:37-39 GAP-WIN-P3）⇒ 进程在 Windows 上
// 不受文件系统约束，把 Windows 算作"已强制"会直接变成"既无边界也不问"；
// `degraded`（degradedSandbox.Restrict 是 return nil，零强制）同理。
// - Features **不含** `network=on`：网络是沙箱拦不住的**第二条通道**，
// 进程可借它把边界内可读的内容送出边界。
func osEnforcementSufficient(st IsolationStatus) bool {
	filesystemConfined := false
	switch st.Platform {
	case "darwin-seatbelt":
		filesystemConfined = true
	case "linux-namespaces":
		// 仅 namespaces 不限制文件系统 —— 必须有 Landlock 才算。
		for _, f := range st.Features {
			if strings.Contains(f, "landlock") {
				filesystemConfined = true
				break
			}
		}
	}
	if !filesystemConfined {
		return false
	}

	for _, f := range st.Features {
		if strings.Contains(f, "network=on") {
			return false
		}
	}
	return true
}

// customSeatbeltProfileActive 报告 **darwin seatbelt 装配**下是否在用用户自定义 profile。
//
// 为什么这条要参与「能否免问」的判定（而不是只影响 banner）：
// 自定义 profile 走 `sandbox-exec -f <文件>`（os_sandbox_darwin.go:305），规则由用户自己写，
// 本包**不解析**它 ⇒ "文件系统是否真被限制、网络是否真被拒"对本函数而言是**不可核实**。
// 而「不可核实」在审批判据里必须读成「未强制」（失败方向**不对称**：读错成"已强制"
// 的代价是静默执行任意命令；读错成"未强制"的代价只是多问一次）。
//
// ⚠️ 特别注意：`describeIsolationFeatures` 在自定义 profile 下**刻意不列** `network=on`
// （banner 不得说谎，os_sandbox_darwin.go:361-365）⇒ 光看 Features 看不出用户 profile
// 是否放行了网络。所以不能只靠 osEnforcementSufficient，必须单独判这一条。
//
// platform 参数由调用方传入（而非在函数内再查一次）—— EnforcementActive 已经取过
// 隔离状态，再查一次是浪费，也会让"同一个事实查两遍"成为漂移入口。
// 非 darwin-seatbelt 平台一律返回 false：该字段在其它平台本就被忽略。
func customSeatbeltProfileActive(mgr Manager, platform string) bool {
	if platform != "darwin-seatbelt" {
		return false
	}
	m, ok := mgr.(*manager)
	if !ok || m == nil || m.config == nil {
		return false
	}
	return m.config.OSLevel.Darwin.SeatbeltProfile != ""
}

// FormatIsolationBanner 格式化用户可见 banner。
//
// 正常：[sandbox] strict: linux-namespaces | namespaces(pid,net,uts,mount,ipc), landlock
// 降级：额外逐行 [sandbox] degraded: <reason>
//
// 口径一致性：banner 宣称的 Features 与 Degradations 由同一 IsolationStatus
// 计算（describeIsolationFeatures 只列实际生效项），不会同时宣称
// "landlock 生效" 与 "landlock 降级"。
func FormatIsolationBanner(st IsolationStatus) string {
	var b strings.Builder
	b.WriteString("[sandbox] ")
	b.WriteString(strings.ToLower(st.Level.String()))
	b.WriteString(": ")
	b.WriteString(st.Platform)
	if len(st.Features) > 0 {
		b.WriteString(" | ")
		b.WriteString(strings.Join(st.Features, ", "))
	}
	for _, reason := range st.Degradations {
		b.WriteString("\n[sandbox]   degraded: ")
		b.WriteString(reason)
	}
	return b.String()
}

// fd2Writer 经裸 syscall.Write 直写 fd 2，完全绕开 os.File 体系。
type fd2Writer struct{}

// Write 实现 io.Writer（EINTR 重试；其余错误上抛，由调用方决定忽略与否）。
func (fd2Writer) Write(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := syscall.Write(2, p[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// stderrDirect 返回真实终端 stderr（fd 2 直写）的 writer。
//
// 背景（门 2 修复）：main.go 在非 debug 模式下把 os.Stderr 变量重定向到
// 日志文件（防持续日志泄漏到 TUI 下方）。banner/canary 是一次性**用户可见**
// 状态输出，走 os.Stderr 变量会被吞进日志文件——终端永远看不到，
// "降级可见性"以另一种形式静默（真机验证：whale --plain 终端无 [sandbox]
// 行，banner 全部躺在 ~/.whale/logs/whale.log）。
//
// 为什么不用 os.NewFile(2)（2026-09-06 回归修复，两起事故）：
// 1. GC 终算子：每次 os.NewFile 返回的 *os.File 带终算子，对象回收时
// Close 底层 fd → **真实 fd 2 被静默关闭**。tui 测试包实测：数十个
// runner 构造各打印一次 banner → 垃圾 File 被 GC → fd 2 被关 →
// 测试二进制无 trace 崩溃（exit 非零、无 panic 栈）。
// 2. netpoller 重复注册：go test 框架的 os.Stderr 已把 fd 2 注册进
// poller，os.NewFile(2) 再次注册同一 fd → pollDesc 状态损坏 →
// write 随机阻塞/失败（banner 测试实测：单独跑挂死 600s 超时、
// 全量跑静默写失败返回空）。
//
// 裸 syscall.Write 不进 os.File/poller/GC 体系，三条全绕开。
func stderrDirect() io.Writer {
	return fd2Writer{}
}

// PrintIsolationBanner 把隔离 banner 打到真实终端 stderr（fd 2）。
//
// 单一入口：TUI lifecycle 与 Executor.EnableProductionSandbox 共用，
// 避免两处打印逻辑漂移（含前缀：FormatIsolationBanner 自带 [sandbox] 前缀，
// 调用方不得再包一层——历史上双写产生了 "[sandbox] [sandbox] " 双前缀）。
func PrintIsolationBanner(mgr Manager) {
	fmt.Fprintf(stderrDirect(), "%s\n", FormatIsolationBanner(IsolationStatusOf(mgr)))
}

// PrintStartupNotice 把一条启动提示打到真实终端 stderr（fd 2）；空串不打印。
//
// 与 PrintIsolationBanner 共用 stderrDirect()：os.Stderr 变量已被 main.go 重定向到
// 日志文件（走变量会被吞），且裸 syscall.Write 可绕开 Go runtime poller 的 fd 复用问题（见上）。
//
// 用途：hook 信任提示（S7 · PR-A2 拍板 ①/③ 「按需提示」）——
// 调用方先判空再调用，因此**无待审 hook 时不会打扰用户**。
func PrintStartupNotice(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintf(stderrDirect(), "%s\n", text)
}
