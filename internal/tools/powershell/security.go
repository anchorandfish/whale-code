// Package powershell — PowerShell 安全检查（T4）。
//
// 是否需要 AST？
// - 结论：P0 不引入 PowerShell AST。PowerShell 语法高度动态（cmdlet 绑定、
// 管道对象流、脚本块 { }、成员访问 .），社区无成熟 Go tree-sitter-powershell
// 绑定可用。引入 cgo 解析成本高于收益。
// - 替代：正则覆盖关键危险模式（iex/调用操作符 &/UNC/注册表/路径遍历/
// 破坏性 cmdlet/网络 cmdlet/脚本块），结合 PathValidator 的参数级路径校验。
// - 局限：正则无法解析变量赋值后的间接调用（$x='iex'; & $x 'cmd'），
// 该类混淆由三层权限（C19）+ 沙箱（C1）兜底，不依赖命令层正则。
//
// 正则覆盖范围（见每个 check 的注释）：
// - Critical: iex/Invoke-Expression、& 调用操作符、UNC 路径、注册表路径、路径遍历
// - High: 变量展开 ${}/$env:、双引号展开、破坏性 cmdlet、网络 cmdlet、
// 反引号转义、脚本块 { }、New-Alias 混淆
// - Medium: 盘符路径、WSMan/AD/Cert PSPath、行内注释、控制字符、管道
package powershell

import (
	"regexp"
	"strings"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
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

// PSSecurityChecks PowerShell 安全检查套件。
var PSSecurityChecks = []SecurityCheck{
	// === Critical（PS 特有） ===
	checkPSInvokeExpression, // iex / Invoke-Expression
	checkPSCallOperator,     // & 调用操作符
	checkPSUNCPath,          // \\server\share
	checkPSRegistryPath,     // HKLM:\ HKCU:\
	checkPSPathTraversal,    // ..\ 路径遍历
	// === High（PS 特有） ===
	checkPSVariableExpansion, // ${variable} $env:VAR
	checkPSDestructiveCmdlet, // Remove-Item/Format-Volume
	checkPSNetworkCmdlet,     // Invoke-WebRequest/Invoke-RestMethod
	checkPSBacktickEscape,    // ` 转义（PS 用反引号）
	checkPSScriptBlock,       // { ... } 脚本块
	checkPSNewAlias,          // New-Alias（混淆）
	// === Medium ===
	checkPSControlChars,     // 控制字符
	checkPSCommentInjection, // # 注入
	checkPSDrivePath,        // C:\ D:\
	checkPSWSManPath,        // WSMan:\
	checkPSADPath,           // AD:\
	checkPSCertPath,         // Cert:\
	checkPSPipeline,         // | 管道
}

// RunSecurityChecks 运行所有 PowerShell 安全检查。
func RunSecurityChecks(command string) []*SecurityViolation {
	var violations []*SecurityViolation
	for _, check := range PSSecurityChecks {
		if v := check(command); v != nil {
			violations = append(violations, v)
		}
	}
	return violations
}

// RunSecurityChecksShortCircuit 短路版本：发现 Critical 立即返回。
func RunSecurityChecksShortCircuit(command string) []*SecurityViolation {
	var violations []*SecurityViolation
	for _, check := range PSSecurityChecks {
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

// 1. iex / Invoke-Expression（Critical）。
var iexPattern = regexp.MustCompile(`(?i)\b(iex|Invoke-Expression)\b`)

func checkPSInvokeExpression(command string) *SecurityViolation {
	if loc := iexPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_IEX", Severity: SeverityCritical, Message: "Invoke-Expression detected, allows arbitrary code execution", Position: loc[0]}
	}
	return nil
}

// 2. & 调用操作符（Critical）。
var callOperatorPattern = regexp.MustCompile(`(?:^|[\s;|&])&\s`)

func checkPSCallOperator(command string) *SecurityViolation {
	if loc := callOperatorPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_CALL_OPERATOR", Severity: SeverityCritical, Message: "& call operator detected, can invoke arbitrary commands", Position: loc[0]}
	}
	return nil
}

// 3. UNC 路径（Critical，Windows 特有）。
var uncPathPattern = regexp.MustCompile(`\\\\[a-zA-Z0-9._-]+`)

func checkPSUNCPath(command string) *SecurityViolation {
	if loc := uncPathPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_UNC_PATH", Severity: SeverityCritical, Message: "UNC path detected, may access network resources", Position: loc[0]}
	}
	return nil
}

// 4. 注册表路径（Critical）。
var registryPathPattern = regexp.MustCompile(`(?i)\b(HKLM|HKCU|HKCR|HKU|HKCC):\\`)

func checkPSRegistryPath(command string) *SecurityViolation {
	if loc := registryPathPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_REGISTRY_PATH", Severity: SeverityCritical, Message: "registry path detected, may modify system registry", Position: loc[0]}
	}
	return nil
}

// 5. 路径遍历（Critical）。
var psPathTraversalPattern = regexp.MustCompile(`\.\.\\|\.\./`)

func checkPSPathTraversal(command string) *SecurityViolation {
	if loc := psPathTraversalPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_PATH_TRAVERSAL", Severity: SeverityCritical, Message: "path traversal detected", Position: loc[0]}
	}
	return nil
}

// 6. 变量展开 ${variable} / $env:VAR（High）。
var psVarExpansionPattern = regexp.MustCompile(`\$\{|\$env:`)

func checkPSVariableExpansion(command string) *SecurityViolation {
	if loc := psVarExpansionPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_VAR_EXPANSION", Severity: SeverityHigh, Message: "variable expansion detected, may hide intent", Position: loc[0]}
	}
	return nil
}

// 7. 破坏性 cmdlet（High）。
//
// ★ -R1：名单**不在此处声明** —— 唯一声明在
// `cmdrules.destructiveCmdlets`，本正则据它编译（此前与
// `tools/pipeline.destructivePSCmdlets` 各写一份逐字相同的 9 项清单）。
var destructiveCmdletPattern = regexp.MustCompile(
	`(?i)\b(` + strings.Join(quoteEach(cmdrules.DestructiveCmdlets()), "|") + `)\b`)

// quoteEach 对每个 cmdlet 名做正则转义，避免名字里的 `-` 等字符改变语义。
func quoteEach(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, regexp.QuoteMeta(n))
	}
	return out
}

func checkPSDestructiveCmdlet(command string) *SecurityViolation {
	if loc := destructiveCmdletPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_DESTRUCTIVE_CMDLET", Severity: SeverityHigh, Message: "destructive cmdlet detected", Position: loc[0]}
	}
	return nil
}

// 8. 网络 cmdlet（High）。
var networkCmdletPattern = regexp.MustCompile(`(?i)\b(Invoke-WebRequest|Invoke-RestMethod|Start-BitsTransfer|New-Object\s+System\.Net\.)`)

func checkPSNetworkCmdlet(command string) *SecurityViolation {
	if loc := networkCmdletPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_NETWORK_CMDLET", Severity: SeverityHigh, Message: "network cmdlet detected, may access external resources", Position: loc[0]}
	}
	return nil
}

// 9. 反引号转义（High，PS 用反引号而非反斜杠）。
var psBacktickPattern = regexp.MustCompile("`[^`a-zA-Z0-9]")

func checkPSBacktickEscape(command string) *SecurityViolation {
	if loc := psBacktickPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_BACKTICK", Severity: SeverityHigh, Message: "backtick escape detected, may bypass tokenization", Position: loc[0]}
	}
	return nil
}

// 10. 脚本块 { ... }（High）。
var scriptBlockPattern = regexp.MustCompile(`\{[^{}]*\b(Invoke|Exec|iex|Start-Process)\b`)

func checkPSScriptBlock(command string) *SecurityViolation {
	if loc := scriptBlockPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_SCRIPT_BLOCK", Severity: SeverityHigh, Message: "script block with execution cmdlet detected", Position: loc[0]}
	}
	return nil
}

// 11. New-Alias 混淆（High）。
var newAliasPattern = regexp.MustCompile(`(?i)\bNew-Alias\b`)

func checkPSNewAlias(command string) *SecurityViolation {
	if loc := newAliasPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_NEW_ALIAS", Severity: SeverityHigh, Message: "New-Alias detected, may be used for command obfuscation", Position: loc[0]}
	}
	return nil
}

// 12. 控制字符（Medium）。
var psControlCharPattern = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)

func checkPSControlChars(command string) *SecurityViolation {
	if loc := psControlCharPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_CTRL_CHAR", Severity: SeverityMedium, Message: "control character detected", Position: loc[0]}
	}
	return nil
}

// 13. 行内注释注入（Medium）。
var psCommentPattern = regexp.MustCompile(`\S\s+#\s`)

func checkPSCommentInjection(command string) *SecurityViolation {
	if loc := psCommentPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_COMMENT", Severity: SeverityMedium, Message: "mid-line comment detected, may hide subsequent commands", Position: loc[0]}
	}
	return nil
}

// 14. 盘符绝对路径（Medium）。
var drivePathPattern = regexp.MustCompile(`(?i)\b[a-z]:\\`)

func checkPSDrivePath(command string) *SecurityViolation {
	if loc := drivePathPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_DRIVE_PATH", Severity: SeverityMedium, Message: "absolute drive path detected", Position: loc[0]}
	}
	return nil
}

// 15. WSMan 路径（Medium）。
var wsManPathPattern = regexp.MustCompile(`(?i)\bWSMan:\\`)

func checkPSWSManPath(command string) *SecurityViolation {
	if loc := wsManPathPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_WSMAN_PATH", Severity: SeverityMedium, Message: "WSMan path detected", Position: loc[0]}
	}
	return nil
}

// 16. AD 路径（Medium）。
var adPathPattern = regexp.MustCompile(`(?i)\bAD:\\`)

func checkPSADPath(command string) *SecurityViolation {
	if loc := adPathPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_AD_PATH", Severity: SeverityMedium, Message: "ActiveDirectory path detected", Position: loc[0]}
	}
	return nil
}

// 17. Cert 路径（Medium）。
var certPathPattern = regexp.MustCompile(`(?i)\bCert:\\`)

func checkPSCertPath(command string) *SecurityViolation {
	if loc := certPathPattern.FindStringIndex(command); loc != nil {
		return &SecurityViolation{CheckID: "PS_CERT_PATH", Severity: SeverityMedium, Message: "Cert path detected", Position: loc[0]}
	}
	return nil
}

// 18. 管道（Medium，可能传递敏感数据）。
func checkPSPipeline(command string) *SecurityViolation {
	if idx := strings.IndexByte(command, '|'); idx >= 0 {
		return &SecurityViolation{CheckID: "PS_PIPELINE", Severity: SeverityMedium, Message: "pipeline detected", Position: idx}
	}
	return nil
}
