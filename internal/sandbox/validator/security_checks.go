// Package validator 提供沙箱应用层安全检查与路径提取（B5/B6）。
//
// 本包是 sandbox 模块的子包，被 sandbox.Manager 调用。
// 不反向导入 sandbox 包，避免循环依赖。
//
// 安全检查按严重性分级（C13/B8）：
// - Critical/High：阻止执行
// - Medium：记录日志，不阻止
// - Low：仅记录
package validator

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// Severity 严重性级别（数值与 sandbox 包 SeverityLow/Medium/High/Critical 对齐）。
type Severity int

const (
	// SeverityLow 仅记录。
	SeverityLow Severity = iota
	// SeverityMedium 记录日志，不阻止。
	SeverityMedium
	// SeverityHigh 阻止执行。
	SeverityHigh
	// SeverityCritical 阻止执行。
	SeverityCritical
)

// SecurityResult 单项安全检查结果。
type SecurityResult struct {
	Passed   bool
	Message  string
	CheckID  string
	Severity Severity
}

// SecurityCheck 安全检查函数类型。
type SecurityCheck func(cmd string) *SecurityResult

// SecurityViolation 描述一条违规（供调用方收集）。
type SecurityViolation struct {
	CheckID  string
	Message  string
	Severity Severity
}

// Validator 应用层验证器，聚合安全检查套件。
//
// SNAPSHOT：原 checks []SecurityCheck 字段保留（21 项字符串匹配不变）；
// 新增 astChecker *ASTChecker 字段（AST 检查器，nil 时跳过 AST）。
type Validator struct {
	checks     []SecurityCheck // 原 21 项字符串匹配（不变）
	astChecker *ASTChecker     // 新增：AST 检查器（nil 时跳过 AST）
}

// NewValidator 创建带默认 21 项字符串匹配 + AST 检查的验证器（AST 默认启用，吸纳审查优化 1）。
//
// AST 检查默认启用，与字符串匹配串联运行。
// AST 失败时自动降级到字符串匹配（fail-open 字符串兜底）。
func NewValidator() *Validator {
	return &Validator{
		checks:     DefaultSecurityChecks(),
		astChecker: NewASTChecker(),
	}
}

// NewValidatorWithoutAST 创建仅字符串匹配的验证器（测试场景用，吸纳审查优化 1）。
//
// 跳过 AST 检查，仅运行 21 项字符串匹配。
// 用于测试场景需要纯字符串匹配行为时（如对比测试）。
func NewValidatorWithoutAST() *Validator {
	return &Validator{checks: DefaultSecurityChecks()}
}

// substitutionFamilyChecks 是「执行型替换」族的**声明**（唯一处）。
//
// 该族的判定自 起由 subst_inner.go 的 substitutionFamilyFindings
// **单点实现**（递归校验内层）。之所以要单独声明出来：内层递归判定必须**排除**本族，
// 否则 `$(…)` 里的 `$(…)` 会再次触发本族 ⇒ 自嵌套、深度失控。
var substitutionFamilyChecks = []SecurityCheck{checkCommandSubstitution}

// DefaultInnerSecurityChecks 返回**不含执行型替换族**的字符串套件。
//
// 两个消费方：
// - DefaultSecurityChecks()（完整套件）= 本套件 ∪ substitutionFamilyChecks；
// - 替换体内层判定（subst_inner.go）= 本套件 + 完整 AST 套件。
//
// ★ 这就是「内层按它直接执行的**同一套标准**判定」这句话的兑现方式：
// 内层少掉的只有"替换族自身"（嵌套由递归接管），其余检查一个不少。
func DefaultInnerSecurityChecks() []SecurityCheck {
	return []SecurityCheck{
		// P0: 命令注入防护
		checkControlCharacters,
		checkExpansionConservative,
		checkIFSInjection,
		checkBackslashEscapedOps,
		checkBackslashEscapedSpace,
		// P0: 混淆检测
		checkANSICQuoting,
		checkLocaleQuoting,
		checkBraceExpansion,
		checkUnicodeWhitespace,
		checkCarriageReturn,
		// P1: 危险命令
		checkZshDangerousCommands,
		checkJqSystemFunction,
		checkProcEnvironAccess,
		checkDangerousRedirections,
		checkGitCommitSafety,
		// P1: 语法检查
		checkIncompleteCommands,
		checkMidWordHash,
		checkCommentQuoteDesync,
		checkQuotedNewlineAbuse,
		checkMalformedTokenInjection,
		// M14-01: fork 炸弹显式检测（T1-D-P0 第 1 项）
		checkForkBomb,
		// M14-04: 环境变量泄漏检测
		checkEnvVarLeak,
		// M14-05: 路径穿越检测（，复用 C11 PathMatcher 白名单）
		checkPathTraversal,
	}
}

// DefaultSecurityChecks 返回默认的安全检查套件（P0 基线 20 项 + M14-01/04/05 显式检测）。
//
// ★ 结构改写：= DefaultInnerSecurityChecks() ∪ substitutionFamilyChecks。
// 刻意用**组合**而非手抄两份列表 —— 手抄必然漂移；这样新增检查只需放进内层列表，
// 完整套件自动包含它。
func DefaultSecurityChecks() []SecurityCheck {
	return append(DefaultInnerSecurityChecks(), substitutionFamilyChecks...)
}

// Run 对命令依次执行 AST 检查 + 字符串检查，返回所有未通过的结果。
//
// 执行顺序 — AST 在前，字符串匹配在后（吸纳审查优化 2）。
// AST 失败时仅运行字符串匹配（fail-open 字符串兜底）。
// 结果合并：不去重，AST 结果（CheckID 带 -AST 后缀）与字符串匹配结果合并（吸纳优化 2、3、4）。
// 双重命中时两条记录都保留，严重度取较高值（HasBlocking 已支持）。
func (v *Validator) Run(cmd string) []SecurityResult {
	var failed []SecurityResult
	// 1. AST 检查（在前，吸纳优化 2）
	if v.astChecker != nil {
		astResults := v.astChecker.Run(cmd)
		// AST 失败时 astResults 为 nil，不影响字符串匹配
		failed = append(failed, astResults...)
	}
	// 2. 字符串匹配（在后，吸纳优化 2）
	for _, c := range v.checks {
		r := c(cmd)
		if r != nil && !r.Passed {
			failed = append(failed, *r)
		}
	}
	return failed
}

// RunSecurityChecks 包级便捷函数：对命令运行默认检查套件，返回未通过结果。
func RunSecurityChecks(cmd string) []SecurityResult {
	return NewValidator().Run(cmd)
}

// HasBlocking 返回是否存在 Critical/High 级违规。
func HasBlocking(results []SecurityResult) bool {
	for _, r := range results {
		if r.Severity >= SeverityHigh {
			return true
		}
	}
	return false
}

// === P0: 命令注入防护 ===

var controlCharRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// checkControlCharacters 检测控制字符（\r 由 checkCarriageReturn 单独处理，此处兜底）。
func checkControlCharacters(cmd string) *SecurityResult {
	// 排除 \t（\t 已在 controlCharRe 范围内，但 \t 常见且无害，单独放行）
	for _, c := range cmd {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return &SecurityResult{
				Passed: false, CheckID: "CTRL-CHAR",
				Message:  fmt.Sprintf("command contains control character U+%04X", c),
				Severity: SeverityCritical,
			}
		}
	}
	if controlCharRe.MatchString(strings.ReplaceAll(strings.ReplaceAll(cmd, "\t", ""), "\n", "")) {
		// 仍可能命中 \r 之外的控制字符
		return &SecurityResult{
			Passed: false, CheckID: "CTRL-CHAR",
			Message:  "command contains control characters",
			Severity: SeverityCritical,
		}
	}
	return &SecurityResult{Passed: true}
}

var (
	// expansionBraceRe / legacyArithRe / arithSubstRe 是**非执行型展开**的三条保守判据。
	// 语义自 SANDBOX-007 起未变（本次 刻意不动这一半，理由见 checkExpansionConservative）。
	expansionBraceRe = regexp.MustCompile(`\$\{`)
	legacyArithRe    = regexp.MustCompile(`\$\[`)
	arithSubstRe     = regexp.MustCompile(`\$\(\(`)
)

// checkExpansionConservative 保守处理**非执行型展开**：`${…}` 参数展开、`$[…]` 旧式算术展开、
// `$((…))` 算术展开。
//
// ★ 刻意**不动**这一半。三条理由：
// 1. 本次拍板的口径只针对「命令替换」（`$(…)` / 反引号 / `<(…)` / `>(…)`）—— 这三种是
// **执行**（execute），而 `${…}` / `$[…]` / `$((…))` 是**展开**（expand），不是一回事；
// 2. `${!var}` / `${x@P}` 这类间接形式静态判不准，保守拒的代价远低于漏放；
// 3. 它**同时留在完整套件与内层套件里** ⇒ 「内层按同一套标准判定」不受影响。
func checkExpansionConservative(cmd string) *SecurityResult {
	if expansionBraceRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "CMD-SUBST",
			Message:  "command contains ${} parameter expansion",
			Severity: SeverityCritical,
		}
	}
	if legacyArithRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "CMD-SUBST",
			Message:  "command contains $[] legacy arithmetic expansion",
			Severity: SeverityCritical,
		}
	}
	if arithSubstRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "CMD-SUBST",
			Message:  "command contains $(()) arithmetic expansion",
			Severity: SeverityCritical,
		}
	}
	return &SecurityResult{Passed: true}
}

// checkCommandSubstitution 判定**执行型替换**是否危险 —— 递归校验内层。
//
// 判据口径（用户 2026-10-06 拍板）：
// - **维持**：替换体**内层**危险（经同套检查判定）⇒ 仍 Critical 硬拒；
// - **递归校验内层**：内层良性 ⇒ 放行（不再"看到 `$(` 就一刀切拒"）；
// - **fail-closed**：内层取不出配对（不平衡 / 嵌套超深）⇒ 拒。
//
// 为什么这是修正而不是"放宽"：改前 `cat /etc/shadow`（直接执行）**放行**，
// `cat <(cat /etc/shadow)` 却因"有进程替换"被拒 —— 同一条命令，换个写法就换个结论。
// 递归把内外拉平：**替换体里的命令，按它直接执行的同一套标准判定**。
//
// ★ 判定本体（唯一实现）在 subst_inner.go 的 substitutionFamilyFindings；
//
//	AST 层不再自带一份「看到 `$(` 就拒」的实现（此前两处各写一份，正是本仓最忌的形态）。
//
// ⚠️ 残留边界（如实登记，方向为多拦，本批不动）：载荷抽取不做引号感知，
// `echo '$(rm -rf /)'`（单引号内**不会执行**）也会被拒。
func checkCommandSubstitution(cmd string) *SecurityResult {
	findings, _ := substitutionFamilyFindings(cmd, maxSubstDepth)
	if len(findings) == 0 {
		return &SecurityResult{Passed: true}
	}
	return &SecurityResult{
		Passed:   false,
		CheckID:  "CMD-SUBST",
		Message:  findings[0].Message,
		Severity: findings[0].Severity,
	}
}

var ifsRe = regexp.MustCompile(`\bIFS\s*=`)

func checkIFSInjection(cmd string) *SecurityResult {
	if ifsRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "IFS-INJECT",
			Message:  "IFS variable assignment detected (potential injection)",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

var escapedOpRe = regexp.MustCompile(`\\[;|&<>()]`)

func checkBackslashEscapedOps(cmd string) *SecurityResult {
	if escapedOpRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ESC-OP",
			Message:  "backslash-escaped shell operator detected",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

var escapedSpaceRe = regexp.MustCompile(`\\ `)

func checkBackslashEscapedSpace(cmd string) *SecurityResult {
	if escapedSpaceRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ESC-SPACE",
			Message:  "backslash-escaped space detected",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

// === P0: 混淆检测 ===

// 引号位感知：ANSIC-Q / LOCALE-Q 改走 dollarQuoteAtOpenPos。
// 旧实现 `\$'` / `\$"` 纯子串匹配是**误伤源**：正则行尾锚点 `$` 紧贴闭引号
//（如 `grep -E "\.go$"`，真机 T-3 实锤）拼出字面 `$"`/`$'`，与引用语法毫无关系。
// 判定语义与测试（双向：误伤组 + 真混淆组）见 quote_open_position_test.go。

func checkANSICQuoting(cmd string) *SecurityResult {
	if dollarQuoteAtOpenPos(cmd, '\'') {
		return &SecurityResult{
			Passed: false, CheckID: "ANSIC-Q",
			Message:  "ANSI-C quoting ($'...') detected",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

// dollarQuoteAtOpenPos 检测 `$<quote>` 引用混淆（引号位感知版）。
//
// 仅当 `$` 处于**开启引号位**才判命中 —— 前一字符是 word 字符（字母/数字/下划线）
// 时，`$` 只可能是正则锚点或普通文本的一部分，不可能是引用的开始：
//
//	echo $"hello" ：$ 前是空格 ⇒ 命中（真 locale 引用）
//	FOO=$"x" ：$ 前是 `=` ⇒ 命中
//	grep -E "\.go$" ：$ 前是 `o`（word）⇒ 正则行尾锚点，不命中（误伤修复）
//	echo $$"x" ：$ 前是 `$` ⇒ 不命中（$$ 是 PID 展开，后面是普通字符串）
func dollarQuoteAtOpenPos(cmd string, quote byte) bool {
	seq := string([]byte{'$', quote})
	for from := 0; ; {
		idx := strings.Index(cmd[from:], seq)
		if idx < 0 {
			return false
		}
		pos := from + idx
		if pos == 0 || !isWordByte(cmd[pos-1]) {
			return true
		}
		from = pos + 1
	}
}

// isWordByte 判断是否 shell word 字符（：字母/数字/下划线）。
func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

func checkLocaleQuoting(cmd string) *SecurityResult {
	if dollarQuoteAtOpenPos(cmd, '"') {
		return &SecurityResult{
			Passed: false, CheckID: "LOCALE-Q",
			Message:  "locale quoting ($\"...\") detected",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

var braceExpandRe = regexp.MustCompile(`\{[^}]+,[^}]+\}`)

func checkBraceExpansion(cmd string) *SecurityResult {
	if braceExpandRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "BRACE-EXP",
			Message:  "brace expansion {a,b} detected",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

func checkUnicodeWhitespace(cmd string) *SecurityResult {
	for _, r := range cmd {
		if unicode.IsSpace(r) && r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return &SecurityResult{
				Passed: false, CheckID: "UNICODE-WS",
				Message:  fmt.Sprintf("unicode whitespace U+%04X detected", r),
				Severity: SeverityMedium,
			}
		}
	}
	return &SecurityResult{Passed: true}
}

func checkCarriageReturn(cmd string) *SecurityResult {
	if strings.Contains(cmd, "\r") {
		return &SecurityResult{
			Passed: false, CheckID: "CR-ABUSE",
			Message:  "carriage return (\\r) detected",
			Severity: SeverityCritical,
		}
	}
	return &SecurityResult{Passed: true}
}

// === P1: 危险命令 ===

var zshDangerousCmds = map[string]bool{
	"zmodload": true, "emulate": true, "sysopen": true,
	"sysread": true, "syswrite": true, "zpty": true,
	"ztcp": true, "zsocket": true, "mapfile": true,
	"zf_rm": true, "zf_mv": true, "zf_ln": true,
	"zf_chmod": true, "zf_chown": true,
}

func checkZshDangerousCommands(cmd string) *SecurityResult {
	for _, w := range strings.Fields(cmd) {
		if zshDangerousCmds[strings.ToLower(w)] {
			return &SecurityResult{
				Passed: false, CheckID: "ZSH-CMD",
				Message:  fmt.Sprintf("Zsh dangerous command: %s", w),
				Severity: SeverityHigh,
			}
		}
	}
	return &SecurityResult{Passed: true}
}

var jqSystemRe = regexp.MustCompile(`jq.*system\s*\(`)

func checkJqSystemFunction(cmd string) *SecurityResult {
	if jqSystemRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "JQ-SYSTEM",
			Message:  "jq system() function call detected",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

var procEnvironRe = regexp.MustCompile(`/proc/\d+/environ`)

func checkProcEnvironAccess(cmd string) *SecurityResult {
	if procEnvironRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "PROC-ENV",
			Message:  "access to /proc/*/environ detected",
			Severity: SeverityMedium,
		}
	}
	return &SecurityResult{Passed: true}
}

// systemWriteDenyPrefixes 系统敏感目录前缀（写到这些目录一律拒绝）。
// 覆盖常规文件与子目录；不含 /dev（/dev 单独按"白名单 sink 放行、其余默认拒绝"处理）。
var systemWriteDenyPrefixes = []string{
	"/etc/", "/usr/", "/bin/", "/sbin/", "/boot/",
	"/sys/", "/proc/", "/root/",
}

// devSafeWriteSinks /dev 下允许写入的字符/虚拟设备 sink。
// 判据：这些是"吞掉输出 / 瞬时 / 进程相关"设备，写入无害，且为命令可用性基线
// （丢弃 stderr 的 2>/dev/null、cat x >/dev/null 等），与底层 OS 沙箱
// (os_sandbox landlock/seatbelt 的可写设备白名单) 保持一致。
// /dev 下除这些 sink 外的目标（含块设备 /dev/sda /dev/disk* 等直接写盘）默认拒绝。
var devSafeWriteSinks = []string{
	"/dev/null", "/dev/zero", "/dev/full", "/dev/tty",
	"/dev/stdin", "/dev/stdout", "/dev/stderr",
	"/dev/fd/", "/dev/pts/", "/dev/shm/",
}

// isDangerousWriteTarget 单一权威判定：写重定向目标是否应被拒绝。
//
// 第一性语义（对齐业内既有实现/同类 CLI：能否写某路径由 OS 权限模型裁决，命令层只做
// "明显越界 + 默认拒绝"的保守过滤，避免用不完整的静态名单误伤合法命令）：
// - /etc/usr/bin 等系统目录前缀 → 拒绝（覆盖常规文件与子目录写）。
// - /dev 下：仅放行已知无害 sink（字符/虚拟设备），其余(含块设备磁盘)默认拒绝。
// - 其余路径不在此列（是否可写交 OS 沙箱白名单裁决，避免命令层重复且不完整的名单）。
func isDangerousWriteTarget(path string) bool {
	p := strings.TrimSpace(path)
	p = strings.Trim(p, `"'`)
	if p == "" {
		return true // 空/无法解析目标 → 保守拒绝
	}
	if strings.HasPrefix(p, "/dev/") {
		for _, s := range devSafeWriteSinks {
			if p == s || strings.HasPrefix(p, s) {
				return false // /dev 无害 sink → 放行
			}
		}
		return true // /dev 下非 sink（含块设备）→ 默认拒绝
	}
	for _, d := range systemWriteDenyPrefixes {
		if strings.HasPrefix(p, d) || p == strings.TrimSuffix(d, "/") {
			return true
		}
	}
	return false
}

var redirectRe = regexp.MustCompile(`(>>?|1?>|2>|&>)\s*(/\S+)`)

func checkDangerousRedirections(cmd string) *SecurityResult {
	matches := redirectRe.FindStringSubmatch(cmd)
	if len(matches) >= 3 {
		target := matches[2]
		if isDangerousWriteTarget(target) {
			return &SecurityResult{
				Passed: false, CheckID: "DANG-REDIR",
				Message:  fmt.Sprintf("write redirect to sensitive path: %s", target),
				Severity: SeverityHigh,
			}
		}
	}
	return &SecurityResult{Passed: true}
}

var gitCommitRe = regexp.MustCompile(`git\s+commit\s+.*-m\s+`)

func checkGitCommitSafety(cmd string) *SecurityResult {
	if gitCommitRe.MatchString(cmd) {
		if strings.Contains(cmd, "$(") || strings.Contains(cmd, "`") {
			return &SecurityResult{
				Passed: false, CheckID: "GIT-COMMIT",
				Message:  "command injection in git commit message",
				Severity: SeverityMedium,
			}
		}
	}
	return &SecurityResult{Passed: true}
}

// === P1: 语法检查 ===

func checkIncompleteCommands(cmd string) *SecurityResult {
	singleQuotes := strings.Count(cmd, "'")
	doubleQuotes := strings.Count(cmd, `"`)
	if singleQuotes%2 != 0 {
		return &SecurityResult{
			Passed: false, CheckID: "INCOMP-CMD",
			Message:  "unclosed single quote",
			Severity: SeverityLow,
		}
	}
	if doubleQuotes%2 != 0 {
		return &SecurityResult{
			Passed: false, CheckID: "INCOMP-CMD",
			Message:  "unclosed double quote",
			Severity: SeverityLow,
		}
	}
	return &SecurityResult{Passed: true}
}

var midWordHashRe = regexp.MustCompile(`\S+#\S+`)

func checkMidWordHash(cmd string) *SecurityResult {
	if midWordHashRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "MID-HASH",
			Message:  "mid-word # detected (potential comment injection)",
			Severity: SeverityMedium,
		}
	}
	return &SecurityResult{Passed: true}
}

func checkCommentQuoteDesync(cmd string) *SecurityResult {
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			}
		} else {
			if c == '"' || c == '\'' {
				inQuote = true
				quoteChar = c
			} else if c == '#' && i > 0 && cmd[i-1] == ' ' {
				rest := cmd[i+1:]
				if strings.ContainsAny(rest, `"'`) {
					return &SecurityResult{
						Passed: false, CheckID: "CMT-QSYNC",
						Message:  "comment-quote desynchronization detected",
						Severity: SeverityMedium,
					}
				}
			}
		}
	}
	return &SecurityResult{Passed: true}
}

func checkQuotedNewlineAbuse(cmd string) *SecurityResult {
	if !strings.Contains(cmd, "\n") {
		return &SecurityResult{Passed: true}
	}
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			} else if c == '\n' {
				return &SecurityResult{
					Passed: false, CheckID: "QNL-ABUSE",
					Message:  "newline inside quotes detected",
					Severity: SeverityMedium,
				}
			}
		} else {
			if c == '"' || c == '\'' {
				inQuote = true
				quoteChar = c
			}
		}
	}
	return &SecurityResult{Passed: true}
}

var formatTokenRe = regexp.MustCompile(`(printf|echo)\s+.*%[sdifneEgGxXoc]`)

func checkMalformedTokenInjection(cmd string) *SecurityResult {
	if formatTokenRe.MatchString(cmd) {
		if strings.Contains(cmd, "$") && !strings.Contains(cmd, "\"%s\"") {
			return &SecurityResult{
				Passed: false, CheckID: "FMT-INJECT",
				Message:  "potential format string injection",
				Severity: SeverityMedium,
			}
		}
	}
	return &SecurityResult{Passed: true}
}

// === M14-01: fork 炸弹显式检测（T1-D-P0 第 1 项） ===

// forkBombClassicRe 匹配经典 : 形式 fork 炸弹的函数定义部分。
// 经典形式：:(){ :|:& };: （函数名为 :，函数体内 :|:& 递归调用管道+后台）。
// 正则容忍任意空格变化：: ( ) { : | : & } 等变体均命中。
// 注：仅匹配函数定义部分（} 之前），不依赖尾部 ;: 调用，因为定义本身已构成危险。
//
// ★ 正则本体已收敛到 cmdrules（唯一声明处）：本包与 memory 的降级门
// 复用同一份模式，避免「同一条规则两处各写一遍、日后只改一处」。
var forkBombClassicRe = cmdrules.ForkBombClassicRe

// funcDefRe 匹配 Shell 函数定义头，提取函数名用于后续递归调用检测。
// 覆盖三种语法：
// - POSIX: name() { ... }
// - Bash keyword: function name { ... }
// - Bash full: function name() { ... }
//
// 捕获组 1 为函数名（不含 : ，: 形式由 forkBombClassicRe 单独覆盖）。
// 注：使用 function 关键字时 () 可省略，故 (?:\s*\(\s*\))? 让 () 变为可选。
var funcDefRe = regexp.MustCompile(`(?:function\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(\s*\))?\s*\{`)

// checkForkBomb 检测 fork 炸弹模式（M14-01）。
//
// 识别两类 fork 炸弹：
// 1. 经典 : 形式：:(){ :|:& };: （含空格变体 :(){ :|:&};: / : () { : | : & } ; : 等）
// 2. 命名函数形式：f(){ f|f& };f / function bomb { bomb|bomb& }; bomb
// （任何函数名，函数体内出现 NAME|NAME& 自递归管道+后台模式）
//
// 由于 Go regexp 包基于 RE2 不支持反向引用，命名函数形式采用「正则提取函数头 +
// 手动扫描函数体内 NAME|NAME& 模式」两段式检测。
//
// 严重度：Critical（fork 炸弹可在秒级耗尽系统资源，必须直接 Deny）。
func checkForkBomb(cmd string) *SecurityResult {
	// 1. 经典 : 形式 fork 炸弹（最常见，优先匹配）
	if forkBombClassicRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "FORK-BOMB",
			Message:  "fork bomb detected (classic :() { :|:& } form)",
			Severity: SeverityCritical,
		}
	}

	// 2. 命名函数形式 fork 炸弹（手动扫描）
	if detectNamedForkBomb(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "FORK-BOMB",
			Message:  "fork bomb detected (self-recursive function with pipe and background)",
			Severity: SeverityCritical,
		}
	}

	return &SecurityResult{Passed: true}
}

// detectNamedForkBomb 手动扫描命名函数 fork 炸弹。
//
// 检测逻辑：
// 1. 用 funcDefRe 找出所有函数定义头，提取函数名 NAME
// 2. 从函数头后提取函数体（{ 到匹配的 }，处理嵌套大括号）
// 3. 在函数体内查找 NAME|NAME& 模式（容忍空格：NAME | NAME &）
// 4. 命中即判定为 fork 炸弹
//
// 注意：函数名 : 已由 forkBombClassicRe 覆盖，此处跳过避免重复匹配。
func detectNamedForkBomb(cmd string) bool {
	matches := funcDefRe.FindAllStringSubmatchIndex(cmd, -1)
	for _, loc := range matches {
		// loc: [headStart, headEnd, nameStart, nameEnd]
		if len(loc) < 4 {
			continue
		}
		name := cmd[loc[2]:loc[3]]
		// 函数体起始位置（函数头之后）
		bodyStart := loc[1]
		body := extractFunctionBody(cmd[bodyStart:])
		if body == "" {
			continue
		}
		// 构造 NAME | NAME & 模式（容忍任意空格）
		escaped := regexp.QuoteMeta(name)
		bodyPattern := fmt.Sprintf(`%s\s*\|\s*%s\s*&`, escaped, escaped)
		if matched, _ := regexp.MatchString(bodyPattern, body); matched {
			return true
		}
	}
	return false
}

// extractFunctionBody 从函数头 { 之后扫描到匹配的 }，返回函数体内容（不含大括号）。
//
// 输入 s 是 funcDefRe 匹配结束位置之后的字符串（即 { 已经被 funcDefRe 消耗，s 从 { 之后开始）。
// 使用深度计数处理嵌套大括号（如函数体内含 ${...} 或 {a,b} 展开）。
// 若大括号未闭合，返回已扫描的全部内容（保守返回，避免漏检）。
func extractFunctionBody(s string) string {
	depth := 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i]
			}
		}
	}
	// 未闭合：保守返回已扫描内容
	return s
}

// === M14-04: 环境变量泄漏检测 ===
//
// 设计目标：扩展现有 checkProcEnvironAccess（仅 /proc/*/environ）覆盖更广的 env var 泄漏向量。
// 检测模式：
// - env 命令在管道/重定向/独立位置（环境变量转储）
// - printenv（任意参数，均输出 env vars）
// - set 命令在管道/重定向（bash builtin 列出所有变量）
// - declare -p / export -p / typeset（变量列举）
// - /proc/self/environ（self 进程 env 泄漏，比 /proc/<pid>/environ 更隐蔽）
//
// 不拦截（合法用法）：
// - env FOO=bar cmd （env 用于设置环境变量后执行命令）
// - env -i cmd （清空环境后执行）
// - env -u VAR cmd （取消变量后执行）
//
// 官方源对标：
// - bash man: https://man7.org/linux/man-pages/man1/bash.1.html @2026-07-21（set/declare/export -p）
// - env man: https://man7.org/linux/man-pages/man1/env.1.html @2026-07-21（env 无参数 = 打印环境）

// envDumpRe 匹配 env 命令独立调用（无后续命令参数，或被管道/重定向/分号截断）。
//
// 匹配模式：
// - ^env$ (整条命令就是 env)
// - ^env\s*[|>&] (env 后接管道/重定向/后台)
// - ^env\s*[;&] (env 后接分号/逻辑与)
// - \senv\s*[|>&] (命令中间 env | xxx)
// - \senv\s*[;&] (命令中间 env ; xxx)
// - \senv$ (命令以 env 结尾)
//
// 不匹配：
// - env FOO=bar cmd (env 后是 FOO=bar，不是管道/分号/重定向)
// - env -i cmd (env 后是 -i 选项)
// - env -u VAR cmd (env 后是 -u 选项)
var envDumpRe = regexp.MustCompile(`(?:^|\s)env(?:\s*[|>&;]|$)`)

// printenvRe 匹配 printenv 命令（任意形式都视为泄漏风险）。
var printenvRe = regexp.MustCompile(`(?:^|\s)printenv(?:\s|$)`)

// setDumpRe 匹配 set 命令在管道/重定向/独立位置（bash 内建，列出所有变量）。
// 不匹配 `set -e` / `set -o pipefail` 等选项设置形式（无管道/重定向）。
//
// 模式：(?:^|\s)set(?:\s*[|>&<]|$)
// - set 后接 | 管道（set | grep）
// - set 后接 > 重定向（set > file）
// - set 后接 < 输入重定向（罕见，但保守拦截）
// - set 后接 & 后台（set &）
// - set 单独（命令以 set 结尾）
//
// 不匹配：set -e / set -o pipefail / set -- args
var setDumpRe = regexp.MustCompile(`(?:^|\s)set(?:\s*[|>&<]|$)`)

// declareDumpRe 匹配 declare -p / typeset（bash/zsh 变量列举）。
var declareDumpRe = regexp.MustCompile(`(?:^|\s)(?:declare|typeset)\s+-p\b`)

// exportDumpRe 匹配 export -p（列出所有导出变量）。
var exportDumpRe = regexp.MustCompile(`(?:^|\s)export\s+-p\b`)

// procSelfEnvironRe 匹配 /proc/self/environ（自我进程环境泄漏，比 /proc/<pid>/environ 更隐蔽）。
var procSelfEnvironRe = regexp.MustCompile(`/proc/self/environ`)

// checkEnvVarLeak 检测环境变量泄漏风险（M14-04 /）。
//
// 检测向量：
// 1. env 命令独立/管道/重定向（环境变量转储）
// 2. printenv 命令
// 3. set 命令独立/管道（bash 变量列举）
// 4. declare -p / typeset
// 5. export -p
// 6. /proc/self/environ（自身进程环境，比 /proc/<pid>/environ 更隐蔽）
//
// 严重度：High（环境变量常含 API_KEY / DATABASE_URL / SECRET 等敏感信息，泄漏等同密钥泄漏）。
//
// 与 checkProcEnvironAccess 的关系：
// - checkProcEnvironAccess 仅检测 /proc/<pid>/environ，Medium 级
// - 本检测扩展到更广的 env var 泄漏向量，High 级
// - 两者并存，互补检测不同向量
func checkEnvVarLeak(cmd string) *SecurityResult {
	// 1. env 独立调用（含管道/重定向/分号/逻辑与/结尾）
	if envDumpRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ENV-LEAK",
			Message:  "env command in dump context (potential environment variable leak)",
			Severity: SeverityHigh,
		}
	}
	// 2. printenv（任意形式）
	if printenvRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ENV-LEAK",
			Message:  "printenv command detected (environment variable leak)",
			Severity: SeverityHigh,
		}
	}
	// 3. set 独立/管道/重定向（bash 内建变量列举）
	if setDumpRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ENV-LEAK",
			Message:  "set command in dump context (potential variable leak)",
			Severity: SeverityHigh,
		}
	}
	// 4. declare -p / typeset
	if declareDumpRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ENV-LEAK",
			Message:  "declare/typeset -p detected (variable enumeration)",
			Severity: SeverityHigh,
		}
	}
	// 5. export -p
	if exportDumpRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ENV-LEAK",
			Message:  "export -p detected (exported variable enumeration)",
			Severity: SeverityHigh,
		}
	}
	// 6. /proc/self/environ
	if procSelfEnvironRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "ENV-LEAK",
			Message:  "access to /proc/self/environ detected (self process environment leak)",
			Severity: SeverityHigh,
		}
	}
	return &SecurityResult{Passed: true}
}

// === M14-05: 路径穿越检测（，复用 C11 PathMatcher 白名单） ===
//
// 设计目标：在 shell 命令中检测路径穿越尝试（../../etc/passwd 等），
// 补齐 permission.CheckPathSafety（仅检测工具输入 file_path）的盲区。
//
// 检测策略（两层）：
// 1. 危险穿越：命令含 2 段以上 ../ 且目标为系统敏感路径（/etc /usr /var /root /sys /proc /dev /boot）
// 严重度：Critical（明显试图读取系统敏感文件）
// 2. 可疑穿越：命令含 2 段以上 ../ 但目标非系统路径
// 严重度：Medium（可能合法如 cd ../../project，但需提示）
//
// 不拦截（合法用法）：
// - cd .. （单段 ..，常见导航）
// - cd ../sibling （单段 ../ + 相对路径，常见导航）
// - ls .. （单段 ..）
// - ../../project/src/file （2 段以上但目标是工作区相对路径）—— 仍标记 Medium
//
// 官方源对标：
// - OWASP Path Traversal: https://owasp.org/www-community/attacks/Path_Traversal @2026-07-21
// - CWE-22: https://cwe.mitre.org/data/definitions/22.html @2026-07-21

// pathTraversalDepthRe 匹配 2 段以上连续 ../ 或 ..\（穿越深度 ≥ 2）。
//
// 模式分解：
// - (?:\.\./){2,} 匹配 2 段以上 ../ （Linux 风格）
// - (?:\.\.\\){2,} 匹配 2 段以上 ..\ （Windows 风格）
// - (?:\.\.[/\\]){2,} 合并两种分隔符
var pathTraversalDepthRe = regexp.MustCompile(`(?:\.\.[/\\]){2,}`)

// sensitiveSystemPaths 是 M14-05 检测的敏感系统路径前缀列表。
//
// 命中此列表的穿越尝试升级为 Critical（明显恶意）。
var sensitiveSystemPaths = []string{
	"/etc/", "/usr/", "/var/", "/root/",
	"/sys/", "/proc/", "/dev/", "/boot/",
	"/sbin/", "/bin/", "/lib/",
	"/Windows/System32/", "/Windows/System/",
	"C:/Windows/", "C:\\Windows\\",
}

// pathTraversalTargetRe 匹配穿越后紧跟的系统敏感路径。
//
// 例：../../../etc/passwd → 匹配 ../etc/ 段
// 例：..\..\..\Windows\System32 → 匹配 ..\Windows\System32 段
//
// 末尾分隔符 [/\\] 同时支持 / 与 \ 以兼容 Windows 路径风格。
var pathTraversalTargetRe = regexp.MustCompile(`(?:\.\.[/\\]){2,}(?:etc|usr|var|root|sys|proc|dev|boot|sbin|bin|lib|Windows)[/\\]`)

// checkPathTraversal 检测路径穿越尝试（M14-05 /）。
//
// 检测逻辑：
// 1. 命中 pathTraversalTargetRe（穿越 ≥ 2 段 + 系统敏感路径）→ Critical
// 2. 命中 pathTraversalDepthRe（穿越 ≥ 2 段，非系统路径）→ Medium
// 3. 单段 ../ 或 ..\ 不拦截（合法导航）
//
// 与 permission.CheckPathSafety 的关系：
// - CheckPathSafety 检测工具输入 file_path 字段的穿越（Layer 3）
// - 本检测覆盖 shell 命令内的穿越（Layer 1，更早拦截）
// - 两者互补：bash 命令 "cat ../../../etc/passwd" 不经过 file_path 字段，
// CheckPathSafety 无法检测，本检测补齐此盲区
func checkPathTraversal(cmd string) *SecurityResult {
	// 1. 穿越深度 ≥ 2 段 + 系统敏感路径 → Critical
	if pathTraversalTargetRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "PATH-TRAVERSAL",
			Message:  "path traversal targeting sensitive system path (../../etc/* etc.)",
			Severity: SeverityCritical,
		}
	}
	// 2. 穿越深度 ≥ 2 段（非系统路径）→ Medium
	if pathTraversalDepthRe.MatchString(cmd) {
		return &SecurityResult{
			Passed: false, CheckID: "PATH-TRAVERSAL",
			Message:  "deep path traversal detected (../../.. or deeper)",
			Severity: SeverityMedium,
		}
	}
	return &SecurityResult{Passed: true}
}
