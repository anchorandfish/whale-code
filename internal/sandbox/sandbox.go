// Package sandbox — Bash commandsandboxisolation
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// Sandbox sandboxconfig
type Sandbox struct {
	mu              sync.RWMutex
	enabled         bool
	workDir         string
	allowDirs       []string
	denyCommands    []string
	allowCommands   []string
	networkIsolated bool
	// 资源限制（SANDBOX-001）：P0 仅存储配置，OS 级强制由 Phase C 的 cgroup/Job Object 实现。
	cpuLimit  float64
	memLimit  int64
	diskLimit int64
}

// NewSandbox createsandbox
//
// 门 3 网络开关（口径一致性）：与 OS 级开关（NewProductionSandboxManager）
// 消费同一 env（networkAllowedByEnv，单一事实源），保证 banner 宣称
// network=on 时应用层不再拦截网络命令——否则用户看到 network=on 后跑
// curl/ssh 仍被 networkIsolated=true 拒绝（三层断网机制各自为政）。
// env 开启时：networkIsolated=false（③ 网络检查与 Layer 2.5 静态检测
// 均站开）+ 默认 deny 列表移除 curl/wget（deny-wins 铁律下不移除则
// 网络检查站开也无用）。其余 deny 项（rm/dd/mkfs 等破坏性命令）不受影响。
func NewSandbox(workDir string) *Sandbox {
	allowNetwork := networkAllowedByEnv()
	deny := defaultDenyCommands()
	if allowNetwork {
		deny = removeNetworkDenyCommands(deny)
	}
	return &Sandbox{
		enabled:         false,
		workDir:         workDir,
		denyCommands:    deny,
		networkIsolated: !allowNetwork, // 默认开启应用层网络命令阻断（SANDBOX-002）
	}
}

// removeNetworkDenyCommands 从 deny 列表移除纯网络用途命令（门 3）。
// 仅当用户显式开启网络开关时调用；破坏性命令（rm/format/dd/mkfs/
// chmod/chown）保留。
func removeNetworkDenyCommands(deny []string) []string {
	filtered := make([]string, 0, len(deny))
	for _, cmd := range deny {
		switch cmd {
		case "curl", "wget":
			// 网络开关已放行网络，deny 列表继续拦截 curl/wget 会造成
			// "banner 说 network=on 但命令被拒" 的口径分叉
		default:
			filtered = append(filtered, cmd)
		}
	}
	return filtered
}

// SetNetworkIsolated 启用/关闭应用层网络命令阻断（SANDBOX-002）。
// OS 级网络隔离（seccomp/WFP）由 Phase C 实现。
func (s *Sandbox) SetNetworkIsolated(isolated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networkIsolated = isolated
}

// IsNetworkIsolated 返回是否启用了网络命令阻断（P1-2）。
func (s *Sandbox) IsNetworkIsolated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.networkIsolated
}

// SetCPULimit 设置 CPU 配额（核数，如 2.0）。P0 仅存储，Phase C 由 cgroup/Job Object 强制。
func (s *Sandbox) SetCPULimit(quota float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cpuLimit = quota
}

// SetMemoryLimit 设置内存上限（字节）。P0 仅存储，Phase C 由 cgroup/Job Object 强制。
func (s *Sandbox) SetMemoryLimit(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memLimit = bytes
}

// SetDiskLimit 设置磁盘写入上限（字节）。P0 仅存储，Phase C 由 cgroup io.max 强制。
func (s *Sandbox) SetDiskLimit(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diskLimit = bytes
}

// Enabled indicates if enabled
func (s *Sandbox) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// SetEnabled sets enabled state
//
// A4：启用时确保沙箱自建 tmp 根存在 —— ValidatePath 只放行该根
// （允许的根必须真实存在，见 sandbox_tmp.go）。停用时不动该根（残留目录无害）。
func (s *Sandbox) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
	if enabled {
		ensureSandboxTempRoot()
	}
}

// SetAllowDirs sets additionally allowed directories
func (s *Sandbox) SetAllowDirs(dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowDirs = dirs
}

// SetAllowCommands sets allowed commands
func (s *Sandbox) SetAllowCommands(cmds []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowCommands = cmds
}

// SetDenyCommands 设置拒绝命令列表（A-P0-1 新增，与 SetAllowCommands 对称）。
// 用于测试与运行时配置 deny 黑名单；deny-wins 铁律下 deny 全量扫描优先于 allow。
func (s *Sandbox) SetDenyCommands(cmds []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denyCommands = cmds
}

// Validate verifies if commands are allowed to execute in sandbox
// returns: allowed, reason
//
// B4 修复：
// - SANDBOX-003：extractCommandName 改为剥离引号（cu”rl → curl）
// - SANDBOX-004（A-P0-1 修订）：deny-wins 铁律——deny 全量扫描优先于 allow。
// 原实现 allow 命中后跳过 deny 与网络检查违反安全铁律（配置 allow: cat + deny: cat /etc/shadow
// 时 cat /etc/shadow 不被拦截）。修订后顺序：① deny 全量扫描 ② allow 白名单 ③ 命令替换/IFS/网络检查。
// allowHit 仅豁免网络命令检查（用户显式允许的命令不受网络隔离限制），不豁免 deny。
// - SANDBOX-007：containsCommandSubstitution 补齐 ${} <() >() =() $[] $(())
// - SANDBOX-002：networkIsolated=true 时阻断 nc/ssh/scp/python socket 等网络命令
//
// A3 收编：规则判定移交 `evaluateAppLayer`（单一实现），
// 与 `Manager.ValidateCommand` 共用同一引擎 ⇒ 两栈语义分歧（G3/G6）在结构上消除。
// 本方法签名与文案逐字未变（既有调用方零改动）；
// `enabled=false` 的能力门保持在此（其缺失由唯一门按族A T1 记录 Deficiency）。
func (s *Sandbox) Validate(command string) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.enabled {
		return true, ""
	}

	v := s.evaluateLocked(command)
	if v.Blocked {
		return false, v.Reason
	}
	return true, ""
}

// ValidateSanctioned 是 Validate 的「用户已批准这一条命令」变体。
//
// 语义（与 同类 CLI `BypassSandboxFirstAttempt` 同义，见 ruleengine.go VerdictTier 注释）：
// - 默认（sanctioned=false）与 Validate **逐字同行为** —— 可批准档照样"不允许"，
// 因此任何没有确认流程的调用路径（executeDirect / 插件 / 后台任务）仍是 fail-closed；
// - sanctioned=true 时**只**放行 TierConfirm 档；TierHardDeny（dd/mkfs/format、
// rm 灾难形态、反向 shell、控制字符、白名单未命中）**照样拦** —— 批准不是万能钥匙。
// （：rm 普通目标已降可批准档。）
//
// ⚠️ sanctioned 的唯一合法来源是 `Executor.ExecuteWithConfirm` 里用户按下 Yes 的那一分支；
// 规则推导出来的免问（Call.ApprovedByRule）**不得**折算成本参数（否则一条随仓库分发的
// allow 规则就能把沙箱打开）。
func (s *Sandbox) ValidateSanctioned(command string, sanctioned bool) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.enabled {
		return true, ""
	}
	v := s.evaluateLocked(command)
	if !v.Blocked {
		return true, ""
	}
	if sanctioned && v.Tier == TierConfirm {
		return true, ""
	}
	return false, v.Reason
}

// Classify 返回命令的应用层判定**后果档**（分档的唯一对外出口）。
//
// 调用方据此决定"拒绝 / 询问 / 放行"，**不得**自己从 Reason 文本反推档位
// （R4：可判定的结构化事实不得由散文关键词断言）。hit 是命中的具体 token，用于
// 让确认框与拒绝文案**指认**触发点（此前的文案只报规则名，用户只能猜）。
func (s *Sandbox) Classify(command string) (tier VerdictTier, rule, hit string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.enabled {
		return TierAllow, "", ""
	}
	v := s.evaluateLocked(command)
	if !v.Blocked {
		return TierAllow, "", ""
	}
	return v.Tier, v.Rule, v.Hit
}

// evaluateLocked 是「本实例配置 × 规则引擎」的**唯一装配点**（调用方须持锁）。
//
// 抽出来的理由：Validate / ValidateSanctioned / Classify / ValidateRule 四处
// 曾各自抄一份 appLayerRuleSet 字面量 —— 这是"同一语义多处实现"的典型温床
// （新增字段时漏改一处就会让某个出口用旧配置判定）。
func (s *Sandbox) evaluateLocked(command string) appLayerVerdict {
	return evaluateAppLayer(command, appLayerRuleSet{
		DenyCommands:    s.denyCommands,
		AllowCommands:   s.allowCommands,
		NetworkIsolated: s.networkIsolated,
		WorkDir:         s.workDir,
	})
}

// ValidateRule 返回命中的规则标识（机器可读），供审计/测试判别使用。
//
// 与 Validate 的区别：Validate 保持"能/不能 + 原因"的历史契约（调用方众多），
// ValidateRule 额外给出结构化规则名，避免下游从原因字符串反推判据（R4 收口同源要求）。
func (s *Sandbox) ValidateRule(command string) (allowed bool, rule, reason string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled {
		return true, "", ""
	}
	v := s.evaluateLocked(command)
	return !v.Blocked, v.Rule, v.Reason
}

// writeRedirectOps 是被视为"写文件重定向"的操作符 token。
// 刻意排除 fd 复制形式（2>&1 / >&2，写的是 fd 而非路径）——它们不会落盘到路径。
var writeRedirectOps = map[string]bool{
	">": true, ">>": true, ">|": true,
	"1>": true, "2>": true, "1>>": true, "2>>": true,
	"&>": true, "&>>": true,
}

// checkWriteRedirectBoundary 逐 token 解析写重定向目标并做工作目录边界检查（R4）。
// 任何越界写目标都会导致整条命令被拒。返回非空 reason 表示应拒绝。
func checkWriteRedirectBoundary(normalized, absWorkDir string) string {
	fields := strings.Fields(normalized)
	for i := 0; i < len(fields); i++ {
		tok := fields[i]

		// tee <target> 是文件写（原 tee 分支同样只覆盖绝对路径，一并修复相对路径）。
		if tok == "tee" {
			// 跳过选项（-a / --append 等）取首个路径目标。
			for j := i + 1; j < len(fields); j++ {
				arg := fields[j]
				if strings.HasPrefix(arg, "-") {
					continue
				}
				if reason := redirectOutsideWorkDir(arg, absWorkDir, "via tee"); reason != "" {
					return reason
				}
				break
			}
			continue
		}

		if !writeRedirectOps[tok] {
			continue
		}
		// 目标在下个 token；若无则忽略（如行尾悬空 `>`）。
		if i+1 < len(fields) {
			if reason := redirectOutsideWorkDir(fields[i+1], absWorkDir, ""); reason != "" {
				return reason
			}
			i++ // 目标已被消费，避免当作 op 二次解析
		}
	}
	return ""
}

// redirectOutsideWorkDir 判定单个重定向目标是否逃出 absWorkDir。
// 相对路径以 shell cwd（= absWorkDir）为基准解析；返回非空 reason 表示越界应拒绝。
func redirectOutsideWorkDir(target, absWorkDir, suffix string) string {
	if target == "" || target == "-" {
		return ""
	}
	// fd 复制/进程替换形态不产生文件写（进程替换 `>(...)` 已被前置命令替换检查拦截）。
	if strings.HasPrefix(target, "&") || strings.HasPrefix(target, ">(") {
		return ""
	}
	var absTarget string
	// Windows 上 `/x`（无盘符）锚到当前盘根而非工作目录，绝不能拼进工作区放行。
	// 锚定判定收口 platform.AnchorsToWorkDir（单一声明）。
	if platform.AnchorsToWorkDir(target) {
		absTarget = filepath.Join(absWorkDir, target)
	} else {
		absTarget, _ = filepath.Abs(target)
	}
	absTarget = resolvePath(absTarget)
	if !isPathWithin(absTarget, absWorkDir) {
		verb := "write outside working directory"
		if suffix != "" {
			verb += " " + suffix
		}
		return fmt.Sprintf("sandbox blocked %s: %s", verb, target)
	}
	return ""
}

// ValidatePath verifies if file path is within allowed range
func (s *Sandbox) ValidatePath(path string) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.enabled {
		return true, ""
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	absPath = resolvePath(absPath)

	absWorkDir, _ := filepath.Abs(s.workDir)
	absWorkDir = resolvePath(absWorkDir)

	if isPathWithin(absPath, absWorkDir) {
		return true, ""
	}

	for _, dir := range s.allowDirs {
		absDir, _ := filepath.Abs(dir)
		absDir = resolvePath(absDir)
		if isPathWithin(absPath, absDir) {
			return true, ""
		}
	}

	// A4：放行根从"整个 os.TempDir()"收窄为"沙箱自建 tmp"
	// （allowedSandboxTemp，单一实现在 sandbox_tmp.go）。
	// 改前此处无条件放行 OS 临时目录全量 ⇒ 任意本机进程可写区被当作可信区。
	if allowedSandboxTemp(absPath) {
		return true, ""
	}

	return false, fmt.Sprintf("sandbox blocked access to path outside working directory: %s", absPath)
}

// resolvePath resolves symlinks in path, falling back to resolving parent if target does not exist
func resolvePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return path
	}
	return filepath.Join(resolvedParent, filepath.Base(path))
}

// isPathWithin checks if path is within parent directory
func isPathWithin(path, parent string) bool {
	if parent == "" {
		return false
	}
	if path == parent {
		return true
	}
	if !strings.HasSuffix(parent, string(os.PathSeparator)) {
		parent += string(os.PathSeparator)
	}
	if !strings.HasPrefix(path, parent) {
		return false
	}
	rest := path[len(parent):]
	if len(rest) == 0 {
		return true
	}
	return !os.IsPathSeparator(path[len(parent)])
}

// defaultDenyCommands is the default set of blocked command patterns
func defaultDenyCommands() []string {
	return []string{
		"rm",
		"format",
		"dd",
		"mkfs",
		"curl",
		"wget",
		"chmod",
		"chown",
	}
}

func normalizeWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// matchDenyPattern 判定命令是否在**命令位**命中拒绝模式（同源补漏，2026-10-02）。
//
// 修复前本函数用 wordBoundaryMatch 对**整条命令串**做子串 + 词边界匹配 ⇒
// 判的是「命令文本里有没有 rm 这个词」，不是「命令名是不是 rm」。真机手测 I-8.4
// （S17 词表退场回归门）实测误拦：
//
//	`grep -rn rm . 2>/dev/null | head -50` ⇒ 拒（sandbox blocked execute (deny-wins): rm）
//	`grep -rn chmod Makefile` / `cat rm.md` / `man rm` / `echo rm` / `ls -la | grep dd` ⇒ 拒
//
// 这与 S17/ 在幻觉检测侧删掉的 `bashFileOpRe=(?i)\b(rm|mv|cp)\b` 是**同一判据**
// （对结构化事实起关键词判据，见 internal/ai/hallucination.go:434）—— 那边删了，
// 这边还在用。cmdrules 注释（第 44-47 行）亦已写明：词边界 `\b` 不够。
//
// 新判据 = 命令位（段首可执行名，取 basename）：
//
//	单 token 模式 `rm` ⇒ 段首 basename == rm
//	多词模式 `cat /tmp/secret` ⇒ 段首以「模式 + 空格」开头（同 MatchReadOnly 的段首前缀语义）
//
// 三项既有加固**逐条保留**（不得因本次改动丢失）：
// - SANDBOX-003 引号绕过：原串与 stripQuotes 后的串都判（`r"m -rf /` ⇒ rm）
// - 绝对路径形态：段首取 basename（`/bin/rm x` ⇒ rm；`/bin/rm.bak x` ⇒ rm.bak 不命中）
// - deny-wins：本函数只回答「是否命中」，命中即拒（全量扫描顺序由 evaluateAppLayer 保证）
func matchDenyPattern(normalized, pattern string) bool {
	lowerPattern := strings.ToLower(strings.TrimSpace(pattern))
	if lowerPattern == "" {
		return false
	}
	// 原串 + 剥离引号后的串（SANDBOX-003：cu''rl → curl）
	variants := []string{normalized}
	if stripped := stripQuotes(normalized); stripped != normalized {
		variants = append(variants, stripped)
	}
	for _, v := range variants {
		for _, region := range denyProbeRegions(strings.ToLower(v), 0) {
			if regionMatchesDenyPattern(region, lowerPattern) {
				return true
			}
		}
	}
	return false
}

// regionMatchesDenyPattern 判定一个「命令位区域」是否命中 deny 模式。
//
// 两种形态都判，各自有既有契约钉住：
// - 段首 basename 化：pattern `rm` 命中 `/bin/rm x`（TC-M13-022 的正面）
// - 段文本原样：pattern 被显式配成带路径形式时仍应命中
func regionMatchesDenyPattern(region, pattern string) bool {
	fields := strings.Fields(region)
	if len(fields) == 0 {
		return false
	}
	head := strings.ToLower(stripQuotes(fields[0]))
	if idx := strings.LastIndex(head, "/"); idx >= 0 {
		head = head[idx+1:]
	}
	basenameRegion := head
	if len(fields) > 1 {
		basenameRegion = head + " " + strings.Join(fields[1:], " ")
	}
	for _, candidate := range []string{region, basenameRegion} {
		if candidate == pattern || strings.HasPrefix(candidate, pattern+" ") {
			return true
		}
	}
	return false
}

// maxDenyProbeDepth 命令位下探的最大深度（防 `sh -c sh -c sh -c …` 构造的无限递归）。
const maxDenyProbeDepth = 3

// denyShellWrappers 需要**下探**的 shell 包装命令：真正被执行的命令在其 `-c` 参数里。
//
// ★ 与 `cmdrules.wrappers`（sudo/env/nohup/…）**刻意分开**，理由是两条消费方向的
// 安全性不对称：
// - 本表只服务 **deny（拦）**：下探只会让更多命令被拦 ⇒ 保守，安全；
// - cmdrules 的 wrappers 还服务**只读档（放行）**：若把 sh/bash 加进那张表，
// `sh -c "ls"` 会被判只读 ⇒ 误报方向是**放宽**（危险，见 P-015 的教训）。
//
// 段切分与前置剥离仍复用 cmdrules.CommandRegions（同一实现，不自写第二份）。
var denyShellWrappers = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true,
}

// denyProbeRegions 返回命令串中所有「可能被当作命令执行」的位置（小写）。
//
// 在 cmdrules.CommandRegions（段切分 + KEY=VALUE / sudo 等剥离，**单一实现**）之上
// 叠加一层**保守下探**：shell `-c`、xargs、find `-exec` 包装后的子命令。
//
// ⚠️ 已知边界（有意不做，与 cmdrules.commandHeads 同一条边界）：
// - 无 `-c` 的 shell 调用（`sh script.sh`、`printf 'rm x' | sh`）：被执行的脚本文本
// 不可静态判定 ⇒ 不下探。改前这些形态是靠「命令文本里恰好含 rm」被拦的 ——
// 那是**误证**（同一机制既拦真 rm 也误拦 `grep -rn rm .`）。本批取「判据诚实 +
// 边界登记」；实际屏障是 OS 级沙箱（darwin seatbelt / linux landlock+seccomp），
// 应用层 deny 是早期拦截，不是唯一防线。
// - 命令替换 `$(...)` / 反引号：由 containsCommandSubstitution 单独拦（既有能力，未动）。
func denyProbeRegions(lower string, depth int) []string {
	if depth > maxDenyProbeDepth {
		return nil
	}
	regions := cmdrules.CommandRegions(lower)
	out := make([]string, 0, len(regions)*2)
	for _, region := range regions {
		out = append(out, region)
		if payload := denyProbePayload(region); payload != "" {
			out = append(out, denyProbeRegions(payload, depth+1)...)
		}
	}
	return out
}

// denyProbePayload 返回该命令段里「被包装执行」的子命令串；无包装时返回 ""。
func denyProbePayload(region string) string {
	fields := strings.Fields(region)
	if len(fields) == 0 {
		return ""
	}
	head := strings.ToLower(stripQuotes(fields[0]))
	if idx := strings.LastIndex(head, "/"); idx >= 0 {
		head = head[idx+1:]
	}
	rest := fields[1:]
	switch {
	case denyShellWrappers[head]:
		// `-c` / `-lc` / `-ec` …：其后所有 token 构成被执行命令串。
		// 取全部而非仅首个 token：同时覆盖 `sh -c "rm -rf /"` 与 `sh -c rm -rf /`。
		for i, f := range rest {
			if isShellCommandFlag(f) {
				return strings.Join(rest[i+1:], " ")
			}
		}
		// 无 `-c`：位置参数是脚本文件路径 ⇒ 不下探（见 denyProbeRegions 已知边界）
		return ""
	case head == "xargs":
		return joinFromFirstCommandToken(rest)
	case head == "find":
		return findExecPayload(rest)
	}
	return ""
}

// isShellCommandFlag 判定 shell 参数是否为「执行字符串」开关
// （`-c` 及其合并短选项形态 `-lc` / `-ec` / `-xc`）。
func isShellCommandFlag(f string) bool {
	if !strings.HasPrefix(f, "-") || strings.HasPrefix(f, "--") {
		return false
	}
	return strings.Contains(f[1:], "c")
}

// joinFromFirstCommandToken 从 xargs 参数里取「第一个真正的命令 token」起到末尾。
// 跳过选项（`-0` / `-n1` / `-I{}`）与占位符（`{}` / `[]` / `%`）。
func joinFromFirstCommandToken(rest []string) string {
	for i, f := range rest {
		if strings.HasPrefix(f, "-") || isPlaceholderToken(f) {
			continue
		}
		return strings.Join(rest[i:], " ")
	}
	return ""
}

// isPlaceholderToken 判定 xargs / find 的占位符 token（`{}` / `{.}` / `{/}` / `[]` / `%`）。
func isPlaceholderToken(tok string) bool {
	t := stripQuotes(tok)
	if t == "{}" || t == "[]" || t == "%" {
		return true
	}
	return strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")
}

// findExecPayload 提取 find `-exec` / `-execdir` / `-ok` / `-okdir` 之后的子命令串
// （到 `;` / `\;` / `+` 终止符为止）。
func findExecPayload(rest []string) string {
	for i, f := range rest {
		switch f {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		payload := make([]string, 0, 4)
		for _, t := range rest[i+1:] {
			if t == ";" || t == `\;` || t == "+" {
				break
			}
			payload = append(payload, t)
		}
		if len(payload) > 0 {
			return strings.Join(payload, " ")
		}
	}
	return ""
}

// extractCommandName 提取命令名（SANDBOX-003：剥离引号，cu”rl → curl）。
//
// 同时支持绝对路径形式（/bin/rm → rm），并兼容 Windows 路径分隔符。
func extractCommandName(normalized string) string {
	parts := strings.Fields(normalized)
	if len(parts) == 0 {
		return ""
	}
	cmd := stripQuotes(parts[0]) // SANDBOX-003：剥离单/双引号
	cmd = filepath.Base(cmd)
	return cmd
}

func isCommandAllowed(cmdName string, allowList []string) bool {
	lower := strings.ToLower(cmdName)
	for _, allowed := range allowList {
		if strings.ToLower(allowed) == lower {
			return true
		}
	}
	return false
}

// containsIFSInjection 检测 IFS 变量赋值（潜在注入）。
// 匹配 "IFS=" 或 "IFS =" 形式（词边界感知）。
func containsIFSInjection(normalized string) bool {
	lower := strings.ToLower(normalized)
	// 检测 IFS= 赋值（含 IFS=''、IFS=: 等）
	ifsPatterns := []string{"ifs=", "ifs ="}
	for _, p := range ifsPatterns {
		idx := strings.Index(lower, p)
		for idx >= 0 {
			// 词边界检查：IFS 前面应为空格或行首
			beforeOK := idx == 0 || lower[idx-1] == ' '
			if beforeOK {
				return true
			}
			nextIdx := strings.Index(lower[idx+1:], p)
			if nextIdx < 0 {
				break
			}
			idx = idx + 1 + nextIdx
		}
	}
	// 检测 export IFS 形式
	if strings.Contains(lower, "export ifs") {
		return true
	}
	return false
}

// checkNetworkCommand 检测命令是否包含网络访问意图（SANDBOX-002 应用层阻断）。
// 返回非空字符串表示拒绝原因；空字符串表示放行。
//
// 对于 python/node/ruby 等通用解释器，仅当命令显式包含 socket/connect/requests/urllib/http.client
// 等网络相关关键字时才拒绝；纯本地脚本（如 python script.py）放行。
func checkNetworkCommand(normalized, cmdName string) string {
	lowerCmd := strings.ToLower(normalized)

	// 直接网络命令：无条件的网络访问能力
	directNetCmds := map[string]string{
		"nc": "netcat", "ncat": "netcat", "netcat": "netcat",
		"ssh": "ssh", "scp": "scp", "sftp": "sftp",
		"telnet": "telnet", "ftp": "ftp",
		"curl": "curl", "wget": "wget",
		"rsync": "rsync",
	}
	if desc, ok := directNetCmds[strings.ToLower(cmdName)]; ok {
		return fmt.Sprintf("sandbox blocked: network command blocked (networkIsolated=true): %s", desc)
	}

	// 通用解释器：仅在包含网络关键字时拒绝
	interpreterNetKeywords := []string{
		"socket", "connect", "requests", "urllib", "http.client",
		"net.createconnection", "net.connect", "io::socket",
		"stream_socket_client", "fsockopen",
	}
	switch strings.ToLower(cmdName) {
	case "python", "python3", "node", "ruby", "perl", "php":
		for _, kw := range interpreterNetKeywords {
			if strings.Contains(lowerCmd, kw) {
				return fmt.Sprintf("sandbox blocked: network keyword %q in interpreter command (networkIsolated=true)", kw)
			}
		}
	}
	return ""
}
