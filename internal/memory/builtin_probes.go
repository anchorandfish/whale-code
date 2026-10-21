// builtin_probes.go — H11/N27 Phase A A4 内置 ProbeFn + N17 单向复用。
// /N27↔N17 协同；落地：builtin_probes 单向复用 N17 ProbeCapability 结果，避免重复探测
// Q2-方案 B 独立+复用；落地：H11/N27 ProbeFn 与 N17 ProbeCapability 各自独立实现，builtin_probes 单向复用 N17 结果
// SNAPSHOT: builtin_probes={init()注册 gcc_available/git_available/go_available; N17复用=若n17ProbeCapability非nil,优先委托N17; N17未注入=自实现shell探测}

package memory

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// 内置 ProbeFn key 常量（与 N17 doctor CheckResult 的 Category.Name 对齐）。
const (
	// BuiltinProbeGccAvailable gcc 可用性探测（C 编译器存在性）。
	BuiltinProbeGccAvailable = "builtin.gcc_available"
	// BuiltinProbeGitAvailable git 可用性探测（版本控制工具存在性）。
	BuiltinProbeGitAvailable = "builtin.git_available"
	// BuiltinProbeGoAvailable go 可用性探测（Go 工具链存在性）。
	BuiltinProbeGoAvailable = "builtin.go_available"
)

// init 注册内置 ProbeFn（init() 调用，进程级注册）。
// 对齐 02 §2.7.6 Q2 方案 B：H11/N27 builtin_probes 可单向复用 N17 ProbeCapability 结果。
// 同一环境项若 N17 已覆盖（通过 SetN17ProbeCapability 注入），builtin_probes 不重新实现探测逻辑，
// 直接调 N17 ProbeCapability 拿 bool，包装为 interface{} 返回。
// N17 未注入时降级为自实现 shell 探测（保证 builtin_probes 始终可用）。
func init() {
	// 注册 gcc 可用性探测
	_ = RegisterProbe(BuiltinProbeGccAvailable, probeGccAvailable)
	// 注册 git 可用性探测
	_ = RegisterProbe(BuiltinProbeGitAvailable, probeGitAvailable)
	// 注册 go 可用性探测
	_ = RegisterProbe(BuiltinProbeGoAvailable, probeGoAvailable)
}

// probeGccAvailable 探测 gcc 可用性（N17 委托优先，自实现降级）。
// 返回值：gcc --version 输出字符串（成功）/ error（失败）。
func probeGccAvailable(ctx context.Context) (interface{}, error) {
	return probeCommandViaN17OrShell(ctx, BuiltinProbeGccAvailable, "gcc --version")
}

// probeGitAvailable 探测 git 可用性（N17 委托优先，自实现降级）。
// 返回值：git --version 输出字符串（成功）/ error（失败）。
func probeGitAvailable(ctx context.Context) (interface{}, error) {
	return probeCommandViaN17OrShell(ctx, BuiltinProbeGitAvailable, "git --version")
}

// probeGoAvailable 探测 go 可用性（N17 委托优先，自实现降级）。
// 返回值：go version 输出字符串（成功）/ error（失败）。
func probeGoAvailable(ctx context.Context) (interface{}, error) {
	return probeCommandViaN17OrShell(ctx, BuiltinProbeGoAvailable, "go version")
}

// probeCommandViaN17OrShell N17 委托优先 + 自实现降级的统一探测逻辑。
// 若 N17 ProbeCapability 已注入（通过 SetN17ProbeCapability），优先委托 N17 拿 bool 结果；
// N17 未注入时降级为自实现 shell 命令探测。
//
// 降级策略（02 §2.7.6 Q2 方案 B）：
// - N17 已注入：返回 N17 探测的 bool 结果（包装为 "true"/"false" 字符串）
// - N17 未注入：自实现 shell 命令探测，返回命令输出字符串
//
// 注意：本函数不对 shell 命令走 H2 校验（内置 ProbeFn 是可信命令，不走用户自定义 shell 注册路径）。
// 用户自定义 shell 命令必须通过 RegisterShellProbe 走 H2 校验。
func probeCommandViaN17OrShell(ctx context.Context, n17Key string, shellCmd string) (interface{}, error) {
	// 优先委托 N17（02 §2.7.6 Q2 方案 B 单向复用）
	if n17ProbeCapability != nil {
		ok, err := n17ProbeCapability(ctx, n17Key)
		if err == nil {
			if ok {
				return "true (via N17)", nil
			}
			return "false (via N17)", nil
		}
		// N17 探测失败：降级为自实现 shell 探测（不阻塞，仅记录失败原因）
		// 注：此处不直接 return error，降级为 shell 探测保证 builtin_probes 始终可用
	}

	// 降级：自实现 shell 命令探测（不通过 RegisterShellProbe，因为是内置可信命令）
	return executeBuiltinShellProbe(ctx, shellCmd)
}

// executeBuiltinShellProbe 执行内置 shell 命令探测（不走 H2 校验，因为是可信内置命令）。
// 失败条件：命令不存在 / 返回非零退出码 / ctx 取消。
func executeBuiltinShellProbe(ctx context.Context, shellCmd string) (interface{}, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", shellCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", shellCmd)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.New("builtin probe command failed: " + strings.TrimSpace(string(output)) + ": " + err.Error())
	}
	return strings.TrimSpace(string(output)), nil
}
