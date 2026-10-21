// probe.go — H11/N27 Phase A A4 ProbeFn 注册表 + 执行 + H2 委托 + 连续 3 次失败转 contradicted。
// 委托；落地：RegisterShellProbe 调 security.CheckCommand 校验 shell 命令，失败拒绝注册
// /N27↔N17 协同；落地：builtin_probes 单向复用 N17 ProbeCapability 结果，避免重复探测
// Q2-方案 B 独立+复用；落地：H11/N27 ProbeFn 与 N17 ProbeCapability 各自独立实现，builtin_probes 单向复用 N17 结果
// Q4-用户自定义 shell 走 H2；落地：RegisterShellProbe + 执行时 H2 校验失败按 ProbeFn 失败处理
// 连续 3 次失败；落地：executeProbe 累计失败次数，连续 3 次→TransitionVerification(contradicted)
// plugin Windows 不可用；落地：禁用 go plugin，用 Go init() + map 注册
// SNAPSHOT: probeRegistry={map[string]ProbeFunc, sync.RWMutex}; RegisterProbe(key,fn)=注册Go函数; RegisterShellProbe(key,shellCmd)=H2校验+注册; executeProbe={成功:重置ProbeFailCount=0+旁路持久化0, 失败:ProbeFailCount=restoreProbeFailCount+1+旁路持久化, 连续3次→contradicted}; builtin_probes=init()注册+单向复用N17 ProbeCapability; ProbeFailCount=旁路持久化到probe_fail.jsonl跨会话恢复(P0-3)

package memory

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// ErrProbeNotRegistered ProbeFn 未注册（key 不存在）。
var ErrProbeNotRegistered = errors.New("probe_fn not registered")

// ErrProbeExecutionFailed ProbeFn 执行失败（如 shell 命令返回非零退出码）。
var ErrProbeExecutionFailed = errors.New("probe_fn execution failed")

// ErrProbeH2Blocked ProbeFn shell 命令被 H2 安全校验拦截，拒绝注册。
var ErrProbeH2Blocked = errors.New("probe_fn shell command blocked by H2 security")

// MaxConsecutiveFailures 连续失败次数上限（PIT-001 防护：达到此阈值转 contradicted）。
const MaxConsecutiveFailures = 3

// ProbeFunc ProbeFn 函数类型，用户自定义或内置。
// 返回 (value, error)：error 非 nil 表示探测失败。
type ProbeFunc func(ctx context.Context) (interface{}, error)

// shellProbeCmd 持有 shell 命令字符串的 ProbeFunc 包装（用于执行时再走 H2 校验）。
// 注册时 H2 校验一次（防止注册已知危险命令），执行时再校验一次（防黑名单动态更新）。
type shellProbeCmd struct {
	cmd string // shell 命令字符串（注册时已通过 H2 校验）
}

// probeRegistry ProbeFn 注册表（内存 map，进程级）。
// 不用 go plugin（PIT-004，Windows 不可用），用 Go init() + map 注册。
// 并发安全：sync.RWMutex 保护 map 读写（02 §2.7.3 并发控制）。
type probeRegistry struct {
	mu        sync.RWMutex
	probes    map[string]ProbeFunc // key → ProbeFunc
	shellCmds map[string]string    // key → shell command 字符串（执行时再走 H2 校验）
}

// globalRegistry 全局 ProbeFn 注册表（init() 注册内置 ProbeFn）。
var globalRegistry = &probeRegistry{
	probes:    make(map[string]ProbeFunc),
	shellCmds: make(map[string]string),
}

// h2Checker H2 security.CheckCommand 的可注入回调（避免循环依赖）。
// 设置后 RegisterShellProbe 和 executeProbe 都会调用此回调做黑名单校验。
// nil 时降级为「基础危险命令字符串匹配」（防注册已知危险命令，但无完整 H2 能力）。
// 设置方式：main.go 启动时调用 SetH2Checker(security.CheckCommand)。
type h2CheckerFunc func(cmd string) (allowed bool, reason string)

// h2Checker 全局 H2 校验器回调（由 main.go 注入）。
var h2Checker h2CheckerFunc

// SetH2Checker 注入 H2 security.CheckCommand 回调（main.go 启动时调用）。
//
// ⚠️ 历史注释「不直接 import internal/security 避免循环依赖（security 可能间接依赖 memory）」
// 经实测**不成立**，已更正：
// - `go list -deps ./internal/security` 不含 internal/memory ⇒ 无环；
// - 反方向：memory 的依赖闭包本就包含 internal/security。
//
// 保留回调注入（而非直接调用 security.CheckCommand）的真实理由只有一个：
// H2 是**编排器**，未初始化时 fail-closed 拒绝一切命令；而 probe 注册发生在
// 启动早期 / 非 TUI 入口（headless、bridge、测试），此时 H2 往往尚未装配。
// 若直接委托，会把「未装配」误译为「命令危险」，造成功能性回归。
// 故这里降级到 cmdrules 的**纯静态规则**（无需初始化，规则与 H2 Layer 3 同源）。
func SetH2Checker(checker func(cmd string) (allowed bool, reason string)) {
	h2Checker = checker
}

// n17ProbeCapabilityFunc N17 ProbeCapability 的可注入回调（避免循环依赖）。
// 设置后 builtin_probes 会单向复用 N17 探测结果（02 §2.7.6 Q2 方案 B）。
type n17ProbeCapabilityFunc func(ctx context.Context, key string) (bool, error)

// n17ProbeCapability 全局 N17 探测回调（由 main.go 注入）。
var n17ProbeCapability n17ProbeCapabilityFunc

// SetN17ProbeCapability 注入 N17 ProbeCapability 回调（main.go 启动时调用）。
// builtin_probes.go 的内置 ProbeFn 会单向复用此回调拿 bool 结果（02 §2.7.6）。
func SetN17ProbeCapability(probe func(ctx context.Context, key string) (bool, error)) {
	n17ProbeCapability = probe
}

// RegisterProbe 注册用户自定义 ProbeFn（Go 函数）。
// 对齐 Q4：若 ProbeFn 是 shell 命令，应使用 RegisterShellProbe 走 H2 校验。
// 此接口仅注册 Go 函数，不走 H2 校验。
func RegisterProbe(key string, fn ProbeFunc) error {
	if key == "" {
		return errors.New("probe key must not be empty")
	}
	if fn == nil {
		return errors.New("probe function must not be nil")
	}

	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.probes[key] = fn
	// 注册 Go 函数 ProbeFn 不需要 shell 命令记录
	delete(globalRegistry.shellCmds, key)
	return nil
}

// RegisterShellProbe 注册用户自定义 shell 命令 ProbeFn（/memory probe add <key> <command>）。
// 对齐 02 §2.3.1 Q4：shell 命令执行前必须复用 H2 security.CheckCommand。
// 校验失败处理：拒绝注册并返回校验失败原因给用户。
//
// 已注册 ProbeFn 在执行时若 H2 校验失败（如黑名单动态更新），按 ProbeFn 执行失败处理
// （返回旧值 + stale 标记 + warn，不阻塞编排）。
func RegisterShellProbe(key string, shellCmd string) error {
	if key == "" {
		return errors.New("probe key must not be empty")
	}
	if strings.TrimSpace(shellCmd) == "" {
		return errors.New("shell command must not be empty")
	}

	// H2 校验（注册时一次，防注册已知危险命令）
	if h2Checker != nil {
		allowed, reason := h2Checker(shellCmd)
		if !allowed {
			return fmt.Errorf("%w: %s (reason: %s)", ErrProbeH2Blocked, shellCmd, reason)
		}
	} else {
		// H2 未注入，降级为基础危险命令字符串匹配（防注册已知危险命令）
		if err := basicDangerCheck(shellCmd); err != nil {
			return fmt.Errorf("%w: %s (reason: %v)", ErrProbeH2Blocked, shellCmd, err)
		}
	}

	// 包装为 ProbeFunc
	wrapped := func(ctx context.Context) (interface{}, error) {
		// 执行时再走 H2 校验（防黑名单动态更新）
		if h2Checker != nil {
			allowed, reason := h2Checker(shellCmd)
			if !allowed {
				return nil, fmt.Errorf("%w: %s (reason: %s)", ErrProbeH2Blocked, shellCmd, reason)
			}
		}
		return executeShellProbe(ctx, shellCmd)
	}

	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.probes[key] = wrapped
	globalRegistry.shellCmds[key] = shellCmd
	return nil
}

// UnregisterProbe 注销 ProbeFn。
func UnregisterProbe(key string) error {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	if _, ok := globalRegistry.probes[key]; !ok {
		return fmt.Errorf("%w: key=%s", ErrProbeNotRegistered, key)
	}
	delete(globalRegistry.probes, key)
	delete(globalRegistry.shellCmds, key)
	return nil
}

// executeProbe 执行 ProbeFn 探测，返回 (value, error)。
// 内部逻辑（PIT-001 防护）：
// 1. 查注册表拿 ProbeFunc
// 2. 调用 ProbeFunc(ctx)
// 3. 成功 → 重置 ProbeFailCount=0；若当前 verification=unverified，流转为 verified
// 4. 失败 → ProbeFailCount++，若连续 3 次失败 → TransitionVerification(name, contradicted, reason)
//
// P0-3 修复：ProbeFailCount 旁路持久化到 probe_fail.jsonl，跨会话恢复（不再仅内存重启归零）。
// 注：本函数需要 entry 的 name（即 capability key）来更新 ProbeFailCount 和触发 verification 流转。
// 调用方应传入与 ProbeFn key 相同的 capability name。
func executeProbe(ctx context.Context, name string) (interface{}, error) {
	globalRegistry.mu.RLock()
	fn, ok := globalRegistry.probes[name]
	globalRegistry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: key=%s", ErrProbeNotRegistered, name)
	}

	value, err := fn(ctx)
	if err != nil {
		// 失败：增加 ProbeFailCount
		entry, loadErr := loadEntryByName(name)
		if loadErr != nil {
			// entry 不存在，仅返回错误（无法记录失败次数）
			return nil, fmt.Errorf("%w: %v (and entry not found: %v)", ErrProbeExecutionFailed, err, loadErr)
		}

		// P0-3: 从旁路持久化恢复 ProbeFailCount（跨会话连续失败计数），再 +1
		entry.ProbeFailCount = restoreProbeFailCount(name) + 1
		failCount := entry.ProbeFailCount
		// P0-3: 旁路写入 probe_fail.jsonl（跨会话持久化）
		_ = persistProbeFailCount(name, failCount)

		// 连续 3 次失败 → 转 contradicted（PIT-001 防护）
		if failCount >= MaxConsecutiveFailures {
			_ = TransitionVerification(name, VerificationContradicted,
				fmt.Sprintf("probe_fn failed %d consecutive times", failCount))
		}

		return nil, fmt.Errorf("%w: %v (fail count: %d/%d)", ErrProbeExecutionFailed, err, failCount, MaxConsecutiveFailures)
	}

	// 成功：重置 ProbeFailCount
	entry, loadErr := loadEntryByName(name)
	if loadErr == nil {
		entry.ProbeFailCount = 0
		// P0-3: 旁路写入 probe_fail.jsonl（重置为 0）
		_ = persistProbeFailCount(name, 0)
		// 若当前是 unverified，流转为 verified（ProbeFn 成功验证）
		if entry.DefaultVerification() == VerificationUnverified {
			_ = TransitionVerification(name, VerificationVerified, "probe_fn success")
		}
	}

	return value, nil
}

// executeShellProbe 执行 shell 命令并返回输出（用于 RegisterShellProbe 包装）。
// 失败条件：命令不存在 / 返回非零退出码 / ctx 取消。
// 超时：默认 30 秒（防恶意命令 hang）。
func executeShellProbe(ctx context.Context, shellCmd string) (interface{}, error) {
	// 30 秒超时（防恶意命令 hang）
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", shellCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", shellCmd)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("shell command %q failed: %w (output: %s)", shellCmd, err, string(output))
	}
	return strings.TrimSpace(string(output)), nil
}

// basicDangerCheck 危险命令静态判定（H2 未注入时的降级防护）。
//
// ★ 模式表已收敛到 `internal/security/cmdrules`（**唯一真相源**）：
// 与安全链 Layer 3（`permission.AnalyzeBashCommand` → `security.CheckCommand`）
// **共用同一份规则**，本函数只保留「命中即拒绝注册」这一策略。
//
// 收敛前本函数自带一份 17 条字符串黑名单，与权威份（19 条）**逐条不同**，
// 实测同一批输入有 3 处结论分歧，且因子串匹配产生误报（`echo shutdown` 被判危险）。
//
// 原注释所称「不直接 import internal/security 避免循环依赖」经实测**不成立**：
// 1. `go list -deps ./internal/security` 不含 `internal/memory` ⇒ 直接 import 也无环；
// 2. `internal/memory` 的依赖闭包**本就已包含** `internal/security`
// （memory → command/orchestrator → hook|monitor → security）。
//
// 现改消费零依赖叶子 `cmdrules`：既拿到同一份规则，又不需要任何初始化。
func basicDangerCheck(cmd string) error {
	if p, ok := cmdrules.MatchDangerous(cmd); ok {
		return fmt.Errorf("contains dangerous pattern %s (%q)", p.ID, p.Value)
	}
	return nil
}
