// Package bash — Bash 安全检查（T2，27 项，对标同类 CLI bashSecurity.ts 23+ + Whale Code 新增）。
//
// 每项检查返回 *SecurityViolation（命中）或 nil（未命中）。
// RunSecurityChecks 汇总所有违规；RunSecurityChecksShortCircuit 发现 Critical 立即返回。
package bash

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Severity 严重性（与 sandbox.Severity 数值对齐）。
type Severity int

const (
	SeverityLow      Severity = 0
	SeverityMedium   Severity = 1
	SeverityHigh     Severity = 2
	SeverityCritical Severity = 3
)

// SecurityViolation 安全违规。
type SecurityViolation struct {
	CheckID  string
	Severity Severity
	Message  string
	Position int
}

// SecurityCheck 单项安全检查函数签名。
type SecurityCheck func(command string) *SecurityViolation

// SecurityChecks 27 项安全检查（顺序：Critical → High → Medium → Low → 新增）。
var SecurityChecks = []SecurityCheck{
	// === Critical (6 项) ===
	checkControlChars,         // 1. \x00-\x1f 控制字符
	checkShellQuoteBug,        // 2. shell-quote 库漏洞特征
	checkCarriageReturn,       // 3. \r 注入
	checkCommandSubstitution,  // 4. $() 命令替换
	checkBacktickSubstitution, // 5. `cmd` 反引号
	checkProcessSubstitution,  // 6. <() >() =()
	// === High (11 项) ===
	checkParameterExpansion, // 7. ${} 参数展开
	checkHeredocInjection,   // 8. <<EOF 注入
	checkIFSInjection,       // 9. IFS 篡改
	checkBackslashOperator,  // 10. \; \| 绕过分词
	checkBackslashSpace,     // 11. 反斜杠+空格绕过路径检查
	checkANSICQuoting,       // 12. $'...' 混淆
	checkLocaleQuoting,      // 13. $"..." 混淆
	checkBraceExpansion,     // 14. {a,b} 命令名混淆
	checkJqSystem,           // 15. jq system() 函数
	checkZshDangerous,       // 16. zmodload/sysopen/ztcp
	checkDangerousRedirect,  // 17. > /etc/... 敏感路径
	// === Medium (8 项) ===
	checkUnicodeSpace,          // 18. 全角空格绕过分词
	checkMidLineComment,        // 19. 行中 # 注入
	checkCommentQuoteDesync,    // 20. # 导致引号状态错乱
	checkQuoteNewlineAbuse,     // 21. 引号内 \n
	checkProcEnviron,           // 22. /proc/*/environ
	checkGitCommitInjection,    // 23. git commit -m 注入
	checkFormatStringInjection, // 24. printf 格式化字符串
	checkPathTraversal,         // 25. ../ 路径遍历
	// === Low (1 项) ===
	checkIncompleteCommand, // 26. 未闭合引号/括号
	// === Whale Code 新增 (1 项) ===
	checkSymlinkEscape, // 27. 符号链接逃逸（命令层提示）
}

// RunSecurityChecks 运行所有 27 项安全检查。
func RunSecurityChecks(command string) []*SecurityViolation {
	var violations []*SecurityViolation
	for _, check := range SecurityChecks {
		if v := check(command); v != nil {
			violations = append(violations, v)
		}
	}
	return violations
}

// RunSecurityChecksShortCircuit 短路版本：发现 Critical 立即返回。
func RunSecurityChecksShortCircuit(command string) []*SecurityViolation {
	var violations []*SecurityViolation
	for _, check := range SecurityChecks {
		if v := check(command); v != nil {
			violations = append(violations, v)
			if v.Severity == SeverityCritical {
				return violations
			}
		}
	}
	return violations
}

// HasCriticalViolation 判断是否存在 Critical 级违规。
func HasCriticalViolation(violations []*SecurityViolation) bool {
	for _, v := range violations {
		if v != nil && v.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

// === 具体检查实现 ===

// 1. 控制字符检测（\x00-\x1f，除 \t \n \r 由其他检查处理）。
var controlCharPattern = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)

func checkControlChars(command string) *SecurityViolation {
	if loc := controlCharPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "CTRL_CHAR", Severity: SeverityCritical, Message: "control character detected", Position: loc[0]}
	}
	return nil
}

// 2. shell-quote 库漏洞特征（已知漏洞模式的命令名拆分特征）。
var shellQuoteBugPattern = regexp.MustCompile(`["""'']`)

func checkShellQuoteBug(command string) *SecurityViolation {
	if loc := shellQuoteBugPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "SHELL_QUOTE_BUG", Severity: SeverityCritical, Message: "smart quote (non-ascii) detected, may bypass shell-quote parsing", Position: loc[0]}
	}
	return nil
}

// 3. 回车符 \r 注入（可用于截断命令显示/绕过日志）。
func checkCarriageReturn(command string) *SecurityViolation {
	if idx := strings.IndexByte(command, '\r'); idx >= 0 {
		return &SecurityViolation{CheckID: "CR_INJECTION", Severity: SeverityCritical, Message: "carriage return (\\r) detected", Position: idx}
	}
	return nil
}

// 4. 命令替换 $()。
var commandSubstPattern = regexp.MustCompile(`\$\(`)

func checkCommandSubstitution(command string) *SecurityViolation {
	if loc := commandSubstPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "CMD_SUBST", Severity: SeverityCritical, Message: "$() command substitution detected", Position: loc[0]}
	}
	return nil
}

// 5. 反引号命令替换 `cmd`。
func checkBacktickSubstitution(command string) *SecurityViolation {
	if idx := strings.IndexByte(command, '`'); idx >= 0 {
		return &SecurityViolation{CheckID: "BACKTICK_SUBST", Severity: SeverityCritical, Message: "backtick command substitution detected", Position: idx}
	}
	return nil
}

// 6. 进程替换 <() >() =()。
var processSubstPattern = regexp.MustCompile(`[<>=]\(`)

func checkProcessSubstitution(command string) *SecurityViolation {
	if loc := processSubstPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PROC_SUBST", Severity: SeverityCritical, Message: "process substitution detected", Position: loc[0]}
	}
	return nil
}

// 7. 参数展开 ${}（可执行算术 $(( ))、数组等）。
var paramExpansionPattern = regexp.MustCompile(`\$\{`)

func checkParameterExpansion(command string) *SecurityViolation {
	if loc := paramExpansionPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PARAM_EXPANSION", Severity: SeverityHigh, Message: "${} parameter expansion detected", Position: loc[0]}
	}
	return nil
}

// 8. heredoc 注入 <<EOF / <<<。
var heredocPattern = regexp.MustCompile(`<<[-~]?`)

func checkHeredocInjection(command string) *SecurityViolation {
	if loc := heredocPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "HEREDOC", Severity: SeverityHigh, Message: "heredoc (<<) detected", Position: loc[0]}
	}
	return nil
}

// 9. IFS 篡改（IFS= 赋值改变分词行为）。
func checkIFSInjection(command string) *SecurityViolation {
	// 匹配 IFS= 赋值（词首或空白后）
	idx := strings.Index(command, "IFS=")
	if idx < 0 {
		return nil
	}
	if idx == 0 || command[idx-1] == ' ' || command[idx-1] == '\t' || command[idx-1] == ';' || command[idx-1] == '\n' {
		return &SecurityViolation{CheckID: "IFS_INJECTION", Severity: SeverityHigh, Message: "IFS variable modification detected", Position: idx}
	}
	return nil
}

// 10. 反斜杠操作符绕过分词（\; \| \& 等转义的操作符）。
var backslashOperatorPattern = regexp.MustCompile(`\\[;|&><()]`)

func checkBackslashOperator(command string) *SecurityViolation {
	if loc := backslashOperatorPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "BACKSLASH_OP", Severity: SeverityHigh, Message: "escaped operator detected, may bypass tokenization", Position: loc[0]}
	}
	return nil
}

// 11. 反斜杠+空格绕过路径检查（路径中的 \ 空格）。
var backslashSpacePattern = regexp.MustCompile(`\\\s`)

func checkBackslashSpace(command string) *SecurityViolation {
	if loc := backslashSpacePattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "BACKSLASH_SPACE", Severity: SeverityHigh, Message: "escaped space detected, may bypass path checks", Position: loc[0]}
	}
	return nil
}

// 12. ANSI-C 引号 $'...'（可插入转义序列混淆）。
var ansiCQuotePattern = regexp.MustCompile(`\$'`)

func checkANSICQuoting(command string) *SecurityViolation {
	if loc := ansiCQuotePattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "ANSIC_QUOTE", Severity: SeverityHigh, Message: "$'...' ANSI-C quoting detected", Position: loc[0]}
	}
	return nil
}

// 13. 本地化引号 $"..."（可插入本地化字符混淆）。
var localeQuotePattern = regexp.MustCompile(`\$"`)

func checkLocaleQuoting(command string) *SecurityViolation {
	if loc := localeQuotePattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "LOCALE_QUOTE", Severity: SeverityHigh, Message: `$"..." locale quoting detected`, Position: loc[0]}
	}
	return nil
}

// 14. 大括号展开 {a,b}（命令名/参数混淆）。
var braceExpansionPattern = regexp.MustCompile(`\{[^}{}]+,[^}{}]+\}`)

func checkBraceExpansion(command string) *SecurityViolation {
	if loc := braceExpansionPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "BRACE_EXPANSION", Severity: SeverityHigh, Message: "brace expansion detected", Position: loc[0]}
	}
	return nil
}

// 15. jq system()/exec/input 等危险函数。
var jqSystemPattern = regexp.MustCompile(`jq[^|]*\b(system|exec|input|getpath|fork)\s*\(`)

func checkJqSystem(command string) *SecurityViolation {
	if loc := jqSystemPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "JQ_SYSTEM", Severity: SeverityHigh, Message: "jq dangerous function (system/exec) detected", Position: loc[0]}
	}
	return nil
}

// 16. zsh 危险内建（zmodload/sysopen/ztcp/zexec）。
var zshDangerousPattern = regexp.MustCompile(`\b(zmodload|sysopen|ztcp|zexec|zsocket)\b`)

func checkZshDangerous(command string) *SecurityViolation {
	if loc := zshDangerousPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "ZSH_DANGEROUS", Severity: SeverityHigh, Message: "zsh dangerous builtin detected", Position: loc[0]}
	}
	return nil
}

// 17. 危险重定向 > /etc/... /proc/ /sys/ /dev/。
var dangerousRedirectPattern = regexp.MustCompile(`>{1,2}\s*(/etc/|/proc/|/sys/|/dev/|/boot/|/usr/|/bin/|/sbin/)`)

func checkDangerousRedirect(command string) *SecurityViolation {
	if loc := dangerousRedirectPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "DANGEROUS_REDIRECT", Severity: SeverityHigh, Message: "dangerous redirect to system path detected", Position: loc[0]}
	}
	return nil
}

// 18. Unicode 空格绕过分词（全角空格 U+3000、不间断空格 U+00A0）。
func checkUnicodeSpace(command string) *SecurityViolation {
	for i, r := range command {
		if r == '\u3000' || r == '\u00A0' || r == '\u2007' || r == '\u202F' {
			return &SecurityViolation{CheckID: "UNICODE_SPACE", Severity: SeverityMedium, Message: "unicode space detected, may bypass tokenization", Position: i}
		}
	}
	return nil
}

// 19. 行中 # 注入（命令名后紧跟 # 注释截断，可能用于隐藏后续命令）。
var midLineCommentPattern = regexp.MustCompile(`\S\s+#\s`)

func checkMidLineComment(command string) *SecurityViolation {
	if loc := midLineCommentPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "MIDLINE_COMMENT", Severity: SeverityMedium, Message: "mid-line comment detected, may hide subsequent commands", Position: loc[0]}
	}
	return nil
}

// 20. 注释导致引号状态错乱（# 出现在引号内或引号未闭合）。
func checkCommentQuoteDesync(command string) *SecurityViolation {
	// 简化：单/双引号数量为奇数 → 未闭合
	single, double := 0, 0
	for _, c := range command {
		switch c {
		case '\'':
			single++
		case '"':
			double++
		}
	}
	if single%2 != 0 || double%2 != 0 {
		return &SecurityViolation{CheckID: "QUOTE_DESYNC", Severity: SeverityMedium, Message: "unclosed quote detected, may cause comment/quote desync", Position: 0}
	}
	return nil
}

// 21. 引号内换行（可能用于隐藏跨行命令）。
var quoteNewlinePattern = regexp.MustCompile(`"[^"\n]*\n[^"\n]*"|'[^'\n]*\n[^'\n]*'`)

func checkQuoteNewlineAbuse(command string) *SecurityViolation {
	if loc := quoteNewlinePattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "QUOTE_NEWLINE", Severity: SeverityMedium, Message: "newline inside quote detected", Position: loc[0]}
	}
	return nil
}

// 22. /proc/*/environ 读取（泄漏环境变量/密钥）。
var procEnvironPattern = regexp.MustCompile(`/proc/\d+/environ|/proc/self/environ`)

func checkProcEnviron(command string) *SecurityViolation {
	if loc := procEnvironPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PROC_ENVIRON", Severity: SeverityMedium, Message: "reading /proc/*/environ detected, may leak secrets", Position: loc[0]}
	}
	return nil
}

// 23. git commit -m 注入（-m 后引号未闭合，可注入额外命令）。
var gitCommitInjectionPattern = regexp.MustCompile(`git\s+commit\s+-m\s*["'].*["']\s*["']`)

func checkGitCommitInjection(command string) *SecurityViolation {
	if loc := gitCommitInjectionPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "GIT_COMMIT_INJECTION", Severity: SeverityMedium, Message: "git commit -m with suspicious quoting detected", Position: loc[0]}
	}
	return nil
}

// 24. printf 格式化字符串注入（%n %s 等）。
var formatStringPattern = regexp.MustCompile(`printf\s+[^|]*%[nspdf]`)

func checkFormatStringInjection(command string) *SecurityViolation {
	if loc := formatStringPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "FORMAT_STRING", Severity: SeverityMedium, Message: "printf format string detected, may cause format string attack", Position: loc[0]}
	}
	return nil
}

// 25. 路径遍历 ../（Whale Code 新增）。
var pathTraversalPattern = regexp.MustCompile(`(?:^|[\\/])\.\.(?:[\\/]|$)`)

func checkPathTraversal(command string) *SecurityViolation {
	if loc := pathTraversalPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PATH_TRAVERSAL", Severity: SeverityMedium, Message: "path traversal (..) detected", Position: loc[0]}
	}
	return nil
}

// 26. 未闭合命令（引号/括号未配对）。
func checkIncompleteCommand(command string) *SecurityViolation {
	// 引号未闭合已在 #20 覆盖，此处检查括号配对
	paren := 0
	for _, c := range command {
		switch c {
		case '(':
			paren++
		case ')':
			paren--
		}
	}
	if paren != 0 {
		return &SecurityViolation{CheckID: "INCOMPLETE_CMD", Severity: SeverityLow, Message: "unbalanced parentheses detected", Position: 0}
	}
	return nil
}

// 27. 符号链接逃逸（命令层提示，实际解析在 path_extractors 的符号链接双侧解析）。
func checkSymlinkEscape(command string) *SecurityViolation {
	// 命令层仅做提示：包含 ln -s 且目标为绝对路径时告警
	if loc := strings.Index(command, "ln -s"); loc >= 0 {
		rest := command[loc:]
		if strings.Contains(rest, "/etc/") || strings.Contains(rest, "/usr/") || strings.Contains(rest, "/root") {
			return &SecurityViolation{CheckID: "SYMLINK_ESCAPE", Severity: SeverityMedium, Message: "ln -s to sensitive path detected, may create symlink escape", Position: loc}
		}
	}
	return nil
}

// 字节偏移安全版本（utf8 安全）。当前 Position 使用字节索引，与 tree-sitter 对齐。
var _ = utf8.RuneLen
