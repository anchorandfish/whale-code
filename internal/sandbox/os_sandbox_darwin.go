//go:build darwin

// Package sandbox — macOS Seatbelt 沙箱实现（/ T1-P1-B9 / P1-SEC）。
//
// 纯 Go 实现，通过 os/exec 调用 /usr/bin/sandbox-exec 包装命令：
// - sandbox-exec -p <inline-profile> -- <cmd> <args...>（P1-SEC：内联 profile）
// - 自定义 profile 时保留 sandbox-exec -f <profile-file> -- <cmd> <args...>
//
// P1-SEC workspace-write 姿态（对齐业内既有实现/同类 CLI 的 default-deny 设计）：
// - (deny default)：未显式允许的操作一律内核层拒绝
// - (allow file-read*)：全局可读，敏感目录（~/.ssh 等）后置 deny
// - (allow file-write* workspace/tmp/cache)：写仅限工作区 + 临时目录 + 用户缓存
// - (deny file-write* .git/hooks)：防 git hook 注入（保留 git commit 可用性）
// - (deny network*)：网络全断
//
// 修复记录（P1-SEC）：
// - 修复前 profile 无 (deny default)，写保护仅覆盖系统目录（/etc /usr 等），
// 写 $HOME、工作区外任意用户路径全部放行；读全放行（含 ~/.ssh）。
// - 修复前 profile 写死在 /tmp/whale-sandbox-profile.sb（固定路径，
// 存在符号链接抢占/跨会话复用风险）；改为 -p 内联，无文件落盘。
//
// 3 级降级链：
// 1. 首选：sandbox-exec -p（内联 workspace-write profile）
// 2. sandbox-exec 不存在：degradedSandbox（setpgid 进程组）+ LogDegradation
// 3. 配置禁用：degradedSandbox + 不记 LogDegradation（用户意图）
//
// 官方源：
// - https://developer.apple.com/library/archive/documentation/Security/Conceptual/AppSandboxDesignGuide/ @2026-07-20
// - macOS man sandbox-exec(1)
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// darwinSeatbeltSandbox 是 macOS Seatbelt 沙箱实现。
//
// 并发安全：
// - Enter 用 sync.Once 保证幂等
// - Exit 加 mutex + 标志位保证幂等
// - Restrict 无状态（仅修改 cmd.Path/Args；profile 按 cmd.Dir 现场生成）
type darwinSeatbeltSandbox struct {
	cfg    DarwinSandboxConfig
	audit  *AuditLog
	once   sync.Once
	mu     sync.Mutex
	exited bool
	handle *darwinSeatbeltHandle
}

// darwinSeatbeltHandle 是 macOS Seatbelt 句柄。
//
// P1-SEC：内联 profile 模式下不持有文件路径；仅作为 Enter 已调用的契约标记。
type darwinSeatbeltHandle struct{}

// newPlatformSandbox 是 macOS 平台工厂（覆盖 os_sandbox_other.go 的默认实现）。
//
// 判定分两问，顺序不可颠倒：
// 1. `sandbox-exec` 这个**文件**在不在（`os.Stat`）—— 只排除"根本没装"；
// 2. **功能探针**：`sandbox_apply` 能不能真被内核接受（`SandboxExecUsable`）。
//
// ★ （2026-09-20 抽探针 → 2026-10-09 接线）：修复前只问第 1 问 ⇒
// 「文件在、但 `sandbox_apply` 被拒」（企业 MDM、被替换的二进制、受限安全上下文
// —— 本机 IDE 内嵌终端实测 `sandbox_apply: Operation not permitted`, rc=71）时，
// 工厂仍返回 `darwinSeatbeltSandbox` ⇒ 启动 banner 恒称 `seatbelt(workspace-write)`
// ⇒ **谎报隔离**（现象恒真：S2 的"沙箱生效了吗"没有判别力）。
// 接线后，上述情形**如实**落 `degraded`，原因经 `Enter()`→`AuditLog` 浮到
// 用户可见出口（banner 的降级行 / `EnforcementActive` 退回询问）。
//
// 判定与测试**共用**同一份实现（`SandboxExecUsable`）：此前"闸门装在测试里、
// 没装在判定里"（`sandbox_guard_attack_darwin_test.go` 探针过了才跑、否则 `t.Skip`）
// 的分叉，在结构上不再可能。
//
// ★ 判别性（反例注入）：把本函数判定改回 `sandboxExecAvailable()`（纯 `os.Stat`）
// ⇒ 受限机上"探针不过却宣称 darwin-seatbelt" ⇒ `TestN013_*` 打红。
func newPlatformSandbox(cfg OSLevelConfig, audit *AuditLog) OSSandbox {
	if !cfg.Darwin.UseSeatbelt {
		return newDegradedSandbox("darwin seatbelt disabled by config", audit)
	}
	if ok, why := SandboxExecUsable(); !ok {
		// 文件在但用不了：与"sandbox-exec 不存在"同一条降级链（第 2 级），
		// 只是**原因**不同 —— 用户看到的平台同为 degraded，但原因可区分。
		return newDegradedSandbox(why, audit)
	}
	return &darwinSeatbeltSandbox{
		cfg:   cfg.Darwin,
		audit: audit,
	}
}

// sandboxExecPath 是 seatbelt 包装器的**唯一**路径声明。
// 修复前它是散在三处的字面量（Stat 一处、Cmd.Path 两处）；
// 收成常量后「探针测的」与「真正跑的」在**类型上**无法分叉。
const sandboxExecPath = "/usr/bin/sandbox-exec"

// sandboxProbeProfile / sandboxProbeCommand 是功能探针的最小载荷：
// `(allow default)` ＋ `/usr/bin/true` —— 只回答"能不能把命令跑起来"。
const (
	sandboxProbeProfile = "(version 1)(allow default)"
	sandboxProbeCommand = "/usr/bin/true"
)

var (
	sandboxExecProbeOnce sync.Once
	sandboxExecProbeOK   bool
	sandboxExecProbeWhy  string
)

// SandboxExecUsable 报告 seatbelt **能否真正应用**一个最小 profile（功能探针）。
//
// ★ · 生产码单一实现点（判定 / status / 测试共用）
//
// # 缺陷面（修复前）
//
// 原判定只有 `os.Stat("/usr/bin/sandbox-exec")`。后果：「**文件在，但
// `sandbox_apply` 被拒**」（企业 MDM 策略、二进制被替换、受限安全上下文 ——
// 本机实测在本仓 IDE 内嵌终端下即 `sandbox_apply: Operation not permitted`, rc=71）
// 时，工厂仍返回真正的 `darwinSeatbeltSandbox` ⇒ `Platform()=="darwin-seatbelt"`
// ⇒ `IsolationStatusOf` 列出 `seatbelt(workspace-write)` ⇒ **谎报隔离**。
// ★ 这是**现象恒真**类缺陷：banner 恒称隔离 ⇒ S2 的"沙箱生效了吗"**没有判别力**。
//
// # 为什么是"抽出来"而不是"新写一个"
//
// 真实探针**本来就在仓库里**，只是装在**测试**里
// （`sandbox_guard_attack_darwin_test.go` 的探针：不过就 `t.Skip`）
// ⇒ 现状 = 「**闸门装在测试里、没装在判定里**」。本函数把它提为生产码，
// 判定侧与测试侧消费**同一份**实现，杜绝"测试里能识别、生产里识别不出"的错位。
//
// ★ **接线状态：已接线**。`newPlatformSandbox` 现在直接消费本函数
// （此前只消费 `sandboxExecAvailable()`＝`os.Stat`，本函数"抽出来了却没装上去"）。
// 接线后 `sandboxExecAvailable()` 已删除（唯一判据，不留第二份影子实现）。
//
// # 语义边界（避免过度宣称）
//
// 探针只回答 **sandbox-exec 能不能 apply**（沙箱内核可用性），**不**回答
// "profile 规则是否正确/够不够严" —— 后者是 `buildSeatbeltProfile` 的责任，
// 由既有攻击用例覆盖。故本函数通过 ≠ "隔离一定够严"。
//
// 结果缓存：探针要 fork 一个进程 ⇒ 每次启动只跑一次（`sync.Once`）。
// 缓存安全：它描述的是**本机内核能力**，与调用方、输入、配置无关。
//
// ★ 判别性：本函数的存在使「A（正常机）/ B（`sandbox_apply` 被拒）」给出
// **不同**的 banner（B 进 `degraded`），修前两者同报 ⇒ 可判别。反例注入 =
// 把 `newPlatformSandbox` 的判定改回 `os.Stat` ⇒ A/B 同报 ⇒ 打红。
func SandboxExecUsable() (ok bool, why string) {
	sandboxExecProbeOnce.Do(func() {
		if _, err := os.Stat(sandboxExecPath); err != nil {
			sandboxExecProbeOK = false
			// 措辞保留历史文案的语义（既有测试/文档按"not found in /usr/bin"检索）。
			sandboxExecProbeWhy = fmt.Sprintf("sandbox-exec not found in /usr/bin: %v", err)
			return
		}
		// 探针用**绝对路径**（与 Cmd.Path 同一常量）—— 刻意**不**走 exec.LookPath：
		// 真正执行的沙箱用的是绝对路径，探针若走 PATH，就可能测到"另一个
		// sandbox-exec"（这正是"探针与被执行者不是同一个东西"的经典假绿）。
		probe := exec.Command(sandboxExecPath, "-p", sandboxProbeProfile, "--", sandboxProbeCommand)
		out, err := probe.CombinedOutput()
		if err != nil {
			sandboxExecProbeOK = false
			sandboxExecProbeWhy = fmt.Sprintf(
				"sandbox-exec present but cannot apply a profile (sandbox_apply rejected): %v; output=%q",
				err, strings.TrimSpace(string(out)))
			return
		}
		sandboxExecProbeOK = true
		sandboxExecProbeWhy = "sandbox-exec probe ok"
	})
	return sandboxExecProbeOK, sandboxExecProbeWhy
}

// seatbeltProfileOptions 描述 workspace-write 姿态的 SBPL profile 输入（P1-SEC）。
type seatbeltProfileOptions struct {
	// Workspace 是可写工作区根（绝对路径，已解析符号链接）。
	Workspace string
	// ExtraWriteDirs 是额外可写根（临时目录、用户缓存等，已解析）。
	ExtraWriteDirs []string
	// SensitiveReadDirs 是禁读目录（如 ~/.ssh，已解析）。
	// 规则置于 (allow file-read*) 之后，靠 SBPL last-match-wins 生效。
	SensitiveReadDirs []string
	// AllowNetwork（门 3）：true 时省略 (deny network*)，沙箱内可访问网络。
	// 默认 false 网络全断；文件/进程隔离规则不受影响。
	AllowNetwork bool
}

// buildSeatbeltProfile 生成 workspace-write 姿态的 SBPL profile（P1-SEC）。
//
// 规则顺序即语义：SBPL 中同一操作命中多条规则时最后一条生效，
// 因此所有 deny 规则必须位于对应 allow 之后。
//
// 路径注入防护（fail-closed）：路径包含 SBPL 字符串字面量特殊字符
// （" 与 \）或控制字符时返回错误，拒绝生成可能被注入额外规则的 profile。

// resolveProfilePath 解析 profile 路径输入（v1 安全修复，E2E 实测驱动）。
//
// Seatbelt subpath 过滤匹配内核解析后的路径（symlink 已展开，如
// /var/folders/... 实以 /private/var/folders/... 呈现）。调用方传入未解析
// 路径时 deny 规则静默永不命中（真实终端 E2E：SensitiveReadDenied FAIL）。
// 在 buildSeatbeltProfile 单一入口统一解析，不依赖调用方记得解析。
// 解析失败（路径尚不存在）时保留原字面量：无 symlink 的字面路径仍可正确
// 匹配，且不因 ~/.ssh 等目录缺失而 fail-closed 拒绝整个 profile。
func resolveProfilePath(p string) string {
	if resolved, err := resolveSandboxPath(p); err == nil && resolved != "" {
		return resolved
	}
	return p
}

func buildSeatbeltProfile(opts seatbeltProfileOptions) (string, error) {
	if opts.Workspace == "" {
		return "", fmt.Errorf("seatbelt profile: workspace is empty")
	}
	if err := validateSeatbeltPath(opts.Workspace); err != nil {
		return "", fmt.Errorf("seatbelt profile: workspace: %w", err)
	}
	for _, d := range opts.ExtraWriteDirs {
		if err := validateSeatbeltPath(d); err != nil {
			return "", fmt.Errorf("seatbelt profile: extra write dir: %w", err)
		}
	}
	for _, d := range opts.SensitiveReadDirs {
		if err := validateSeatbeltPath(d); err != nil {
			return "", fmt.Errorf("seatbelt profile: sensitive read dir: %w", err)
		}
	}

	// 统一解析（解析后路径再过一遍注入校验，fail-closed）
	workspace := resolveProfilePath(opts.Workspace)
	if err := validateSeatbeltPath(workspace); err != nil {
		return "", fmt.Errorf("seatbelt profile: resolved workspace: %w", err)
	}
	extraWriteDirs := make([]string, len(opts.ExtraWriteDirs))
	for i, d := range opts.ExtraWriteDirs {
		extraWriteDirs[i] = resolveProfilePath(d)
		if err := validateSeatbeltPath(extraWriteDirs[i]); err != nil {
			return "", fmt.Errorf("seatbelt profile: resolved extra write dir: %w", err)
		}
	}
	sensitiveReadDirs := make([]string, len(opts.SensitiveReadDirs))
	for i, d := range opts.SensitiveReadDirs {
		sensitiveReadDirs[i] = resolveProfilePath(d)
		if err := validateSeatbeltPath(sensitiveReadDirs[i]); err != nil {
			return "", fmt.Errorf("seatbelt profile: resolved sensitive read dir: %w", err)
		}
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(import \"system.sb\")\n")
	// 全局可读（后置敏感目录 deny 收窄）
	b.WriteString("(allow file-read*)\n")
	// 进程执行/fork 与 sysctl（deny default 下不加这些基础命令无法运行）
	b.WriteString("(allow process*)\n")
	b.WriteString("(allow sysctl*)\n")
	// /dev/null 重定向是命令可用性基线
	b.WriteString("(allow file-write* (literal \"/dev/null\"))\n")
	// 可写根：工作区 + 额外可写根（tmp/cache）
	b.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", workspace))
	for _, d := range extraWriteDirs {
		b.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", d))
	}
	// .git/hooks 写拒绝（防 git hook 注入；置于 workspace allow 之后以生效）
	b.WriteString(fmt.Sprintf("(deny file-write* (subpath \"%s/.git/hooks\"))\n", workspace))
	// 敏感目录读拒绝（置于 allow file-read* 之后以生效）
	for _, d := range sensitiveReadDirs {
		b.WriteString(fmt.Sprintf("(deny file-read* (subpath \"%s\"))\n", d))
	}
	// 网络（Seatbelt 无域名级过滤，网络是二值开关）。
	// 门 3：默认 (deny network*) 全断（deny default 下本就拒绝，显式写出
	// 是防御纵深）；AllowNetwork=true 时必须显式 (allow network*)——
	// 仅省略 deny 规则在 (deny default) 下网络仍被拒，开关会变成静默空操作。
	if opts.AllowNetwork {
		b.WriteString("(allow network*)\n")
	} else {
		b.WriteString("(deny network*)\n")
	}
	return b.String(), nil
}

// validateSeatbeltPath 校验路径可安全嵌入 SBPL 字符串字面量。
//
// SBPL 字符串以 " 定界，\ 为转义字符；出现这两个字符或控制字符
// 都可能注入/破坏规则（CVE 形态：profile 注入）。fail-closed 返回错误。
func validateSeatbeltPath(p string) error {
	for _, r := range p {
		switch {
		case r == '"' || r == '\\':
			return fmt.Errorf("path contains SBPL literal special char %q: %s", r, p)
		case r < 0x20:
			return fmt.Errorf("path contains control char U+%04X: %s", r, p)
		}
	}
	return nil
}

// defaultDarwinProfileOptions 构造默认 workspace-write 选项（P1-SEC）。
//
// workspace 解析符号链接（如 /tmp → /private/tmp，内核按真实路径匹配 subpath）。
// 额外可写根：系统临时目录 + 用户缓存目录（GOCACHE 等构建缓存位于
// ~/Library/Caches，不放开会导致 go build 等常规命令不可用）。
// 敏感读目录：~/.ssh、~/.gnupg、~/.aws（密钥与凭证）。
//
// ★ ：额外可写根**不再在这里自己拼** —— 改为消费 `ExtraWritableRoots()`
// （writable_roots.go 的唯一声明），与审批面的三值判据同源。
// 在这里再拼一份正是"同一路径两个理由"的成因。
func defaultDarwinProfileOptions(workspace string) (seatbeltProfileOptions, error) {
	resolvedWs, err := resolveSandboxPath(workspace)
	if err != nil {
		return seatbeltProfileOptions{}, fmt.Errorf("resolve workspace: %w", err)
	}

	extraWriteDirs := ExtraWritableRoots()

	var sensitiveReadDirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, sub := range []string{".ssh", ".gnupg", ".aws"} {
			if resolved, rerr := resolveSandboxPath(filepath.Join(home, sub)); rerr == nil && resolved != "" {
				sensitiveReadDirs = append(sensitiveReadDirs, resolved)
			}
		}
	}

	return seatbeltProfileOptions{
		Workspace:         resolvedWs,
		ExtraWriteDirs:    extraWriteDirs,
		SensitiveReadDirs: sensitiveReadDirs,
	}, nil
}

// resolveSandboxPath 定义在共享文件 path_resolve.go（darwin/linux 沙箱共用）。

// Enter 创建 macOS Seatbelt 沙箱（P1-SEC：仅初始化契约句柄）。
//
// profile 不在此生成：内联 -p 模式下 profile 按 cmd.Dir（工作区）
// 在 Restrict 时现场生成，避免多工作区并发时的全局 profile 串扰。
func (d *darwinSeatbeltSandbox) Enter() (interface{}, func(), error) {
	var enterErr error
	d.once.Do(func() {
		d.handle = &darwinSeatbeltHandle{}
		if d.audit != nil {
			// 门 2：启用事件用 LogStatus（信息性），不污染降级计数
			d.audit.LogStatus("darwin seatbelt enabled: inline workspace-write profile")
			// v1 门 1：资源限制降级 canary（一次性，静默防护教训）
			//
			// ★ 这里是"RLIMIT_AS / RLIMIT_NPROC 在 macOS 被跳过"这条降级事实的
			// **唯一出口**（2026-09-30 B-2 收敛）。此前 helper 进程
			//（os_sandbox_darwin_rlimit.go 的 applyDarwinRlimits）也把同样的说明
			// Fprintf 到 os.Stderr，而 helper 的 stderr **被目标进程继承**
			// ⇒ whale 自己的诊断混进被监控命令的输出（真机 I-7.1：`node server.js`
			// 的输出里出现 `whale-rlimit-helper: RLIMIT_AS skipped…`）。
			// 现收敛到这里：审计文件持久留痕 + 内存级 DegradeReasons()（启动 banner 可见）。
			// ★ 守卫：internal/sandbox/b2_rlimit_notice_channel_test.go 判据 C ——
			// 谁删掉下面这行审计，它会红（防"顺手删出口"导致静默降级）。
			if d.cfg.ResourceLimits != (ResourceLimits{}) {
				d.audit.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict,
					"darwin rlimit: CPU/NOFILE/FSIZE enforced via re-exec helper; "+
						"RLIMIT_AS/RLIMIT_NPROC skipped (dyld VA reservations / per-UID counting)")
			}
		}
	})
	if enterErr != nil {
		return nil, nil, enterErr
	}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			// 内联 profile 无资源需释放
		})
	}
	return d.handle, cleanup, nil
}

// Exit 退出 macOS Seatbelt 沙箱（幂等，无操作）。
func (d *darwinSeatbeltSandbox) Exit() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exited = true
	return nil
}

// Restrict 用 sandbox-exec 包装 exec.Cmd（P1-SEC：workspace-write 内联 profile）。
//
// 工作区取 cmd.Dir（调用方 bash.go 在 Restrict 前已设置为沙箱工作目录）；
// cmd.Dir 为空时回退当前工作目录。生成失败 fail-closed（命令不启动）。
//
// 自定义 profile（cfg.SeatbeltProfile 非空）时保留 -f 文件模式。
func (d *darwinSeatbeltSandbox) Restrict(cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("darwin sandbox restrict: cmd is nil")
	}
	if cmd.Process != nil {
		return fmt.Errorf("darwin sandbox restrict: cmd already started")
	}
	if d.handle == nil {
		return fmt.Errorf("darwin sandbox restrict: Enter not called")
	}

	origPath := cmd.Path
	origArgs := cmd.Args

	if d.cfg.SeatbeltProfile != "" {
		// 自定义 profile（逃生开关）：保留 -f 文件模式
		cmd.Path = sandboxExecPath
		cmd.Args = append([]string{"sandbox-exec", "-f", d.cfg.SeatbeltProfile, "--", origPath}, origArgs[1:]...)
	} else {
		workspace := cmd.Dir
		if workspace == "" {
			var err error
			workspace, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("darwin sandbox restrict: resolve workspace: %w", err)
			}
		}
		opts, err := defaultDarwinProfileOptions(workspace)
		if err != nil {
			return fmt.Errorf("darwin sandbox restrict: %w", err)
		}
		// 门 3：网络开关透传（env → 工厂 → cfg → profile 单向流动）
		opts.AllowNetwork = d.cfg.AllowNetwork
		profile, err := buildSeatbeltProfile(opts)
		if err != nil {
			return fmt.Errorf("darwin sandbox restrict: %w", err)
		}

		cmd.Path = sandboxExecPath
		cmd.Args = append([]string{"sandbox-exec", "-p", profile, "--", origPath}, origArgs[1:]...)
	}

	// v1 门 1：资源限制 re-exec helper（最外层包装，setrlimit 经 execve
	// 链继承到 sandbox-exec 及最终目标命令）。零值配置不包装。
	if err := wrapWithRlimitHelper(cmd, d.cfg.ResourceLimits); err != nil {
		return fmt.Errorf("darwin sandbox restrict: %w", err)
	}
	return nil
}

// PostStartHook macOS Seatbelt 沙箱无 post-start 操作（P0-3）。
func (d *darwinSeatbeltSandbox) PostStartHook() func(*exec.Cmd) error {
	return nil
}

// Platform 返回 "darwin-seatbelt"。
func (d *darwinSeatbeltSandbox) Platform() string {
	return "darwin-seatbelt"
}

// describeIsolationFeatures 返回 macOS 实际生效的隔离特性（门 2 降级可见性）。
//
// rlimit 特性按配置列出（CPU/NOFILE/FSIZE 经 __whale-rlimit helper 生效；
// AS/NPROC 平台跳过——Enter 期审计 canary 记录，不在特性中宣称）。
func describeIsolationFeatures(cfg OSLevelConfig) []string {
	features := []string{"seatbelt(workspace-write)"}
	if cfg.Darwin.ResourceLimits != (ResourceLimits{}) {
		features = append(features, "rlimit(cpu,nofile,fsize)")
	}
	// 门 3：网络放行必须用户可见（安全降级不可静默）。
	// 例外：自定义 SeatbeltProfile（逃生开关）时网络规则由用户 profile
	// 自行控制，本开关被忽略——banner 不得宣称 network=on（宣称即说谎）。
	if cfg.Darwin.AllowNetwork && cfg.Darwin.SeatbeltProfile == "" {
		features = append(features, "network=on")
	}
	return features
}

// isLinuxLandlockBuild 报告当前构建是否含 Landlock 路径（仅 linux）。
func isLinuxLandlockBuild() bool { return false }
