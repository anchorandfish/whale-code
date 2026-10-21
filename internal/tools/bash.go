package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/safebuf"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	bashast "github.com/anchorandfish/whale-code/internal/tools/bash"
	"github.com/anchorandfish/whale-code/internal/tools/powershell"
	"github.com/anchorandfish/whale-code/internal/util"
)

var errSandboxDenied = errors.New("sandbox denied")

// errSandboxExecutionDenied（，2026-10-14）：命令**已经跑过**、失败像 OS 沙箱造成
// （沙箱内执行 + 非零退出 + 输出含 EPERM 文本）。与上方 errSandboxDenied（执行**前**
// 拒绝，命令一行没跑）刻意分哨兵 —— 二者的用户语义与升级资格都不同：
// 只有本哨兵会被 ExecuteWithConfirm 的 on-failure 升级询问消费（Result.SandboxEPERM）。
var errSandboxExecutionDenied = errors.New("sandbox execution denied (EPERM)")

// BashInput Bash toolinput
type BashInput struct {
	Command         string `json:"command" desc:"Bash command to execute"`
	Timeout         int    `json:"timeout,omitempty" desc:"Timeout in seconds, default 120"`
	RunInBackground bool   `json:"run_in_background,omitempty" desc:"Whether to run in the background (recommended for long commands, auto-notify on completion)"`
}

// ★ 2026-09-20 批 D：原 `const defaultBashMaxOutput = 50000` 已删除。
// 它是「bash 工具最大输出字符数」的**第二处声明**（与 limits.go 的 DefaultBashMaxOutput 并存
// 且取值不同）⇒ 已收敛为 limits.go 的唯一声明，取值取实际生效的 50000（零行为变更）。
// 消费点见 bashHandler 尾部的 `GetOutputLimits().BashMaxOutput`。

// convertLinuxToPowerShell converts common Linux commands to PowerShell equivalents.
// T1/C12: 通过 bash AST 解析器归一化命令名，识别 shell 引号混淆
// （r”m / r""m / r\m → rm），避免子串匹配遗漏（FACT-TOOLS-001）。
func convertLinuxToPowerShell(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return cmd
	}

	// T1/C12: AST 归一化命令名（降级路径：纯 Go tokenizer，无需 cgo）
	trimmed = normalizeCommandViaAST(trimmed)

	// ls command conversion (exact match, avoiding false conversion of lsof/lsblk/lsusb)
	if isLsCommand(trimmed) {
		return convertLsCommand(trimmed)
	}

	// cat command conversion
	if strings.HasPrefix(trimmed, "cat ") || trimmed == "cat" {
		return strings.Replace(trimmed, "cat", "Get-Content", 1)
	}

	// grep command conversion
	if matched, _ := regexp.MatchString(`^grep\s`, trimmed); matched {
		return convertGrepCommand(trimmed)
	}

	// find command conversion
	if strings.HasPrefix(trimmed, "find ") {
		return convertFindCommand(trimmed)
	}

	// sed command conversion
	if strings.HasPrefix(trimmed, "sed ") {
		return convertSedCommand(trimmed)
	}

	// awk command conversion
	if strings.HasPrefix(trimmed, "awk ") {
		return convertAwkCommand(trimmed)
	}

	// head command conversion
	if strings.HasPrefix(trimmed, "head ") || trimmed == "head" {
		return convertHeadCommand(trimmed)
	}

	// tail command conversion
	if strings.HasPrefix(trimmed, "tail ") || trimmed == "tail" {
		return convertTailCommand(trimmed)
	}

	// wc command conversion
	if strings.HasPrefix(trimmed, "wc ") || trimmed == "wc" {
		return convertWcCommand(trimmed)
	}

	// rm command conversion
	if strings.HasPrefix(trimmed, "rm ") {
		return convertRmCommand(trimmed)
	}

	// cp command conversion
	if strings.HasPrefix(trimmed, "cp ") {
		return convertCpCommand(trimmed)
	}

	// mv command conversion
	if strings.HasPrefix(trimmed, "mv ") {
		return convertMvCommand(trimmed)
	}

	// mkdir command conversion
	if strings.HasPrefix(trimmed, "mkdir ") {
		return convertMkdirCommand(trimmed)
	}

	// touch command conversion
	if strings.HasPrefix(trimmed, "touch ") {
		return convertTouchCommand(trimmed)
	}

	// sort command conversion
	if strings.HasPrefix(trimmed, "sort ") || trimmed == "sort" {
		return convertSortCommand(trimmed)
	}

	// uniq command conversion
	if strings.HasPrefix(trimmed, "uniq ") || trimmed == "uniq" {
		return convertUniqCommand(trimmed)
	}

	// other common commands kept as-is, PowerShell may support or error to inform user
	return cmd
}

// normalizeCommandViaAST 用 bash AST 解析器归一化命令名（T1/C12）。
//
// shell 引号拼接的混淆命令（r”m / r""m / r\m）会被 AST 正确解析为 rm，
// 重建为标准形式后可被后续的子串匹配（strings.HasPrefix）识别。
//
// 降级策略：解析失败、多命令（复合命令）、或无参数时返回原命令，
// 保持向后兼容（FACT-TOOLS-001）。
func normalizeCommandViaAST(cmd string) string {
	ast, err := bashast.ParseCommand(context.Background(), cmd)
	if err != nil || ast == nil || len(ast.Commands) != 1 {
		return cmd // 降级：多命令或解析失败，保持原样
	}
	c := ast.Commands[0]
	if c.Name == "" || len(c.RawArgs) < 2 {
		return cmd // 无参数或解析异常，保持原样
	}
	// 重建命令：归一化名称（去引号/转义） + 原始参数文本（保留引号）
	parts := []string{c.Name}
	parts = append(parts, c.RawArgs[1:]...)
	return strings.Join(parts, " ")
}

// isLsCommand exactly determines if it's an ls command, excluding lsof/lsblk/lsusb
func isLsCommand(cmd string) bool {
	if !strings.HasPrefix(cmd, "ls") {
		return false
	}
	if len(cmd) == 2 {
		return true
	}
	next := cmd[2]
	return next == ' ' || next == '-' || next == '\t'
}

// convertLsCommand converts ls command to PowerShell format
func convertLsCommand(cmd string) string {
	baseCmd := "Get-ChildItem"

	// extract parameter part
	params := strings.TrimPrefix(cmd, "ls")
	params = strings.TrimSpace(params)

	// resolveoption
	showHidden := false
	longFormat := false
	var paths []string

	// simple resolve of -la, -l, -a etc.
	if strings.Contains(params, "-la") || strings.Contains(params, "-al") {
		showHidden = true
		longFormat = true
		params = strings.ReplaceAll(params, "-la", "")
		params = strings.ReplaceAll(params, "-al", "")
	} else {
		if strings.Contains(params, "-l") {
			longFormat = true
			params = strings.ReplaceAll(params, "-l", "")
		}
		if strings.Contains(params, "-a") || strings.Contains(params, "-A") {
			showHidden = true
			params = strings.ReplaceAll(params, "-a", "")
			params = strings.ReplaceAll(params, "-A", "")
		}
	}

	// extract path parameter
	params = strings.TrimSpace(params)
	if params != "" && !strings.HasPrefix(params, "-") {
		paths = strings.Fields(params)
	}

	// build PowerShell command
	var psArgs []string

	if showHidden {
		psArgs = append(psArgs, "-Force")
	}
	if longFormat {
		// long format output
		result := baseCmd
		if len(psArgs) > 0 {
			result += " " + strings.Join(psArgs, " ")
		}
		if len(paths) > 0 {
			for _, p := range paths {
				result += fmt.Sprintf(" \"%s\"", p)
			}
			result += " | Format-Table Mode, LastWriteTime, Length, Name -AutoSize"
		} else {
			result += " | Format-Table Mode, LastWriteTime, Length, Name -AutoSize"
		}
		return result
	}

	// short format
	result := baseCmd
	if len(psArgs) > 0 {
		result += " " + strings.Join(psArgs, " ")
	}
	if len(paths) > 0 {
		for _, p := range paths {
			result += fmt.Sprintf(" \"%s\"", p)
		}
	}
	return result
}

// convertGrepCommand converts grep command to PowerShell Select-String.
// Supports combined flags (-iRn), context lines (-A/-B/-C), -e pattern, -o, -c, -v, -l, -w.
func convertGrepCommand(cmd string) string {
	// Remove leading "grep"
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "grep"))

	var options []string
	var patterns []string
	var files []string
	var contextAfter, contextBefore, contextLines int

	// Parse options and arguments
	fields := tokenizeShell(rest)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-e" && i+1 < len(fields):
			patterns = append(patterns, fields[i+1])
			i++
		case strings.HasPrefix(f, "-e"):
			patterns = append(patterns, strings.TrimPrefix(f, "-e"))
		case f == "-A" && i+1 < len(fields):
			fmt.Sscanf(fields[i+1], "%d", &contextAfter)
			i++
		case f == "-B" && i+1 < len(fields):
			fmt.Sscanf(fields[i+1], "%d", &contextBefore)
			i++
		case f == "-C" && i+1 < len(fields):
			fmt.Sscanf(fields[i+1], "%d", &contextLines)
			i++
		case strings.HasPrefix(f, "-") && len(f) > 1 && f[1] != '-':
			// Combined short flags: -iRn -> i, R, n
			options = append(options, f)
		case len(patterns) == 0:
			// First non-flag argument is the pattern
			patterns = append(patterns, f)
		default:
			// Subsequent non-flag arguments are files
			files = append(files, f)
		}
	}

	if len(patterns) == 0 {
		return cmd
	}

	// Build Select-String command
	var psParts []string

	// Parse short flags
	hasRecurse := false
	hasCaseInsensitive := false
	onlyMatching := false
	countMode := false
	notMatch := false
	filesWithMatches := false
	wordMatch := false
	for _, opt := range options {
		for _, ch := range opt[1:] { // skip leading '-'
			switch ch {
			case 'r', 'R':
				hasRecurse = true
			case 'i':
				hasCaseInsensitive = true
			case 'n':
				// line numbers: Select-String includes by default
			case 'l':
				filesWithMatches = true
			case 'v':
				notMatch = true
			case 'w':
				wordMatch = true
			case 'o':
				onlyMatching = true
			case 'c':
				countMode = true
			}
		}
	}

	// File paths（-Path 需在 -Pattern 之前，与 PowerShell 惯例及测试用例一致）
	for _, file := range files {
		psParts = append(psParts, "-Path", fmt.Sprintf("\"%s\"", file))
	}

	// Pattern (join multiple patterns with | for -e support)
	pattern := strings.Join(patterns, "|")
	if wordMatch {
		pattern = `\b` + pattern + `\b`
	}
	psParts = append(psParts, "-Pattern", fmt.Sprintf("\"%s\"", pattern))

	// Options
	if hasRecurse {
		psParts = append(psParts, "-Recurse")
	}
	if hasCaseInsensitive {
		psParts = append(psParts, "-CaseSensitive:$false")
	}
	if notMatch {
		psParts = append(psParts, "-NotMatch")
	}
	if contextLines > 0 {
		psParts = append(psParts, "-Context", fmt.Sprintf("%d", contextLines))
	}
	if contextBefore > 0 {
		psParts = append(psParts, "-Context", fmt.Sprintf("%d,0", contextBefore))
	}
	if contextAfter > 0 {
		psParts = append(psParts, "-Context", fmt.Sprintf("0,%d", contextAfter))
	}

	// Output mode
	if filesWithMatches {
		psParts = append(psParts, "-List")
	}

	result := "Select-String " + strings.Join(psParts, " ")

	// Post-processing for count mode
	if countMode {
		if len(files) > 0 {
			result = fmt.Sprintf("(%s).Count", result)
		} else {
			result = fmt.Sprintf("(%s).Count", result)
		}
	}

	// Post-processing for only matching
	if onlyMatching {
		result = fmt.Sprintf("(%s).Matches.Value", result)
	}

	return result
}

// tokenizeShell splits a shell command string into tokens respecting quoted strings
func tokenizeShell(s string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote {
			if ch == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(ch)
			}
		} else if ch == '"' || ch == '\'' {
			inQuote = true
			quoteChar = ch
		} else if ch == ' ' || ch == '\t' {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// convertFindCommand converts find command to PowerShell Get-ChildItem
func convertFindCommand(cmd string) string {
	// Basic conversion: find <path> -name <pattern> -> Get-ChildItem -Path <path> -Filter <pattern> -Recurse
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "find"))
	path := "."
	fields := tokenizeShell(rest)
	if len(fields) > 0 && !strings.HasPrefix(fields[0], "-") {
		path = fields[0]
		fields = fields[1:]
	}

	var filter string
	for i := 0; i < len(fields); i++ {
		if fields[i] == "-name" && i+1 < len(fields) {
			filter = fields[i+1]
			i++
		} else if strings.HasPrefix(fields[i], "-name=") {
			filter = strings.TrimPrefix(fields[i], "-name=")
		}
	}

	if filter != "" {
		return fmt.Sprintf("Get-ChildItem -Path \"%s\" -Filter \"%s\" -Recurse -File", path, filter)
	}
	return strings.Replace(cmd, "find", "Get-ChildItem", 1) + " -Recurse"
}

// convertSedCommand converts sed command to PowerShell -replace
func convertSedCommand(cmd string) string {
	// Basic conversion: sed 's/old/new/g' file -> (Get-Content file) -replace 'old', 'new'
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "sed"))
	fields := tokenizeShell(rest)

	var expression string
	var inputFile string
	for _, f := range fields {
		if strings.HasPrefix(f, "'s/") || strings.HasPrefix(f, "s/") {
			expression = strings.Trim(f, "'")
		} else if !strings.HasPrefix(f, "-") {
			inputFile = f
		}
	}

	if expression != "" && inputFile != "" {
		// Parse s/pattern/replacement/flags
		parts := strings.SplitN(expression, "/", 4)
		if len(parts) >= 4 {
			pattern := parts[1]
			replacement := parts[2]
			return fmt.Sprintf("(Get-Content \"%s\") -replace '%s', '%s' | Set-Content \"%s\"", inputFile, pattern, replacement, inputFile)
		}
	}
	return cmd
}

// convertAwkCommand converts awk command to PowerShell Select-Object
func convertAwkCommand(cmd string) string {
	// Basic conversion: awk '{print $1}' file -> Get-Content file | ForEach-Object { ($_ -split '\\s+')[0] }
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "awk"))
	fields := tokenizeShell(rest)

	var program string
	var inputFile string
	for _, f := range fields {
		if strings.HasPrefix(f, "'{") || strings.HasPrefix(f, "{") {
			program = strings.Trim(f, "'")
		} else if !strings.HasPrefix(f, "-") {
			inputFile = f
		}
	}

	if program != "" && strings.Contains(program, "print") {
		// Extract field number from print $N
		re := regexp.MustCompile(`\$(\d+)`)
		matches := re.FindStringSubmatch(program)
		if len(matches) >= 2 {
			fieldNum := matches[1]
			psCmd := fmt.Sprintf("Get-Content \"%s\" | ForEach-Object { ($_ -split '\\s+')[%s] }", inputFile, fieldNum)
			return psCmd
		}
	}
	return cmd
}

// convertHeadCommand converts head command to PowerShell Select-Object -First
func convertHeadCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "head"))
	fields := tokenizeShell(rest)
	var nFlag string = "10"
	var inputFile string
	for i := 0; i < len(fields); i++ {
		if fields[i] == "-n" && i+1 < len(fields) {
			nFlag = fields[i+1]
			i++
		} else if strings.HasPrefix(fields[i], "-n") {
			nFlag = strings.TrimPrefix(fields[i], "-n")
		} else if !strings.HasPrefix(fields[i], "-") {
			inputFile = fields[i]
		}
	}
	if inputFile != "" {
		return fmt.Sprintf("Get-Content \"%s\" | Select-Object -First %s", inputFile, nFlag)
	}
	return fmt.Sprintf("Get-Content | Select-Object -First %s", nFlag)
}

// convertTailCommand converts tail command to PowerShell Select-Object -Last
func convertTailCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "tail"))
	fields := tokenizeShell(rest)
	var nFlag string = "10"
	var inputFile string
	for i := 0; i < len(fields); i++ {
		if fields[i] == "-n" && i+1 < len(fields) {
			nFlag = fields[i+1]
			i++
		} else if strings.HasPrefix(fields[i], "-n") {
			nFlag = strings.TrimPrefix(fields[i], "-n")
		} else if !strings.HasPrefix(fields[i], "-") {
			inputFile = fields[i]
		}
	}
	if inputFile != "" {
		return fmt.Sprintf("Get-Content \"%s\" | Select-Object -Last %s", inputFile, nFlag)
	}
	return fmt.Sprintf("Get-Content | Select-Object -Last %s", nFlag)
}

// convertWcCommand converts wc command to PowerShell measure
func convertWcCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "wc"))
	fields := tokenizeShell(rest)
	var inputFile string
	var flags []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			flags = append(flags, f)
		} else {
			inputFile = f
		}
	}
	// wc -l -> (Get-Content file).Count
	if inputFile != "" {
		hasLines := false
		for _, f := range flags {
			if strings.Contains(f, "l") {
				hasLines = true
				break
			}
		}
		if hasLines || len(flags) == 0 {
			return fmt.Sprintf("@(Get-Content \"%s\").Count", inputFile)
		}
	}
	return cmd
}

// convertRmCommand converts rm command to PowerShell Remove-Item.
// T2: 危险 rm 命令（--no-preserve-root、-rf /、强制删除系统路径等）不做转换，
// 直接返回原命令由上层沙箱/审批拦截，避免静默放行（对标同类 CLI 专用验证器）。
func convertRmCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "rm"))
	fields := tokenizeShell(rest)

	if isDangerousRmCommand(fields) {
		// 危险命令不转换为 Remove-Item，交由上层安全机制处理
		return cmd
	}

	var args []string
	recursive := false
	for _, f := range fields {
		if f == "-r" || f == "-rf" || f == "-fr" {
			recursive = true
		} else if f == "-f" {
			args = append(args, "-Force")
		} else if !strings.HasPrefix(f, "-") {
			args = append(args, fmt.Sprintf("\"%s\"", f))
		}
	}
	psArgs := args
	if recursive {
		psArgs = append(psArgs, "-Recurse")
	}
	if len(psArgs) > 0 {
		return "Remove-Item " + strings.Join(psArgs, " ")
	}
	return "Remove-Item"
}

// isDangerousRmCommand 检测 rm 命令是否为危险操作（T2 安全验证器）。
// 危险判定：含 --no-preserve-root；递归强制删除根/系统路径；强制删除关键系统文件。
func isDangerousRmCommand(fields []string) bool {
	hasNoPreserveRoot := false
	hasRecursive := false
	hasForce := false
	var targets []string
	for _, f := range fields {
		switch f {
		case "--no-preserve-root":
			hasNoPreserveRoot = true
		case "-r", "-R", "--recursive":
			hasRecursive = true
		case "-f", "--force":
			hasForce = true
		}
		// 合并短选项如 -rf / -fr
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && len(f) > 2 {
			for _, c := range f[1:] {
				if c == 'r' || c == 'R' {
					hasRecursive = true
				}
				if c == 'f' {
					hasForce = true
				}
			}
		}
		if !strings.HasPrefix(f, "-") {
			targets = append(targets, f)
		}
	}

	// --no-preserve-root 一律视为危险
	if hasNoPreserveRoot {
		return true
	}

	// 关键系统路径（根/系统目录/关键文件）
	systemPaths := map[string]bool{
		"/": true, "/etc": true, "/etc/passwd": true, "/etc/shadow": true,
		"/usr": true, "/boot": true, "/bin": true, "/sbin": true,
		"/proc": true, "/sys": true, "/dev": true, "/lib": true, "/lib64": true,
		"/var": true, "/root": true, "/home": true,
	}
	recursiveForce := hasRecursive && hasForce
	for _, t := range targets {
		// 递归强制删除任意系统路径 → 危险
		if recursiveForce && systemPaths[t] {
			return true
		}
		// 强制删除关键系统文件 → 危险
		if hasForce && (t == "/etc/passwd" || t == "/etc/shadow") {
			return true
		}
	}
	return false
}

// convertCpCommand converts cp command to PowerShell Copy-Item
func convertCpCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "cp"))
	fields := tokenizeShell(rest)
	var args []string
	recursive := false
	for _, f := range fields {
		if f == "-r" || f == "-R" {
			recursive = true
		} else if !strings.HasPrefix(f, "-") {
			args = append(args, fmt.Sprintf("\"%s\"", f))
		}
	}
	psArgs := args
	if recursive {
		psArgs = append(psArgs, "-Recurse")
	}
	if len(psArgs) >= 2 {
		return "Copy-Item " + strings.Join(psArgs, " ")
	}
	return cmd
}

// convertMvCommand converts mv command to PowerShell Move-Item
func convertMvCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "mv"))
	fields := tokenizeShell(rest)
	var args []string
	for _, f := range fields {
		if !strings.HasPrefix(f, "-") {
			args = append(args, fmt.Sprintf("\"%s\"", f))
		}
	}
	if len(args) >= 2 {
		return "Move-Item " + strings.Join(args, " ")
	}
	return cmd
}

// convertMkdirCommand converts mkdir command to PowerShell New-Item -ItemType Directory
func convertMkdirCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "mkdir"))
	fields := tokenizeShell(rest)
	var args []string
	for _, f := range fields {
		if !strings.HasPrefix(f, "-") {
			args = append(args, fmt.Sprintf("\"%s\"", f))
		}
	}
	if len(args) > 0 {
		return "New-Item -ItemType Directory " + strings.Join(args, ", ")
	}
	return "New-Item -ItemType Directory"
}

// convertTouchCommand converts touch command to PowerShell New-Item -ItemType File
func convertTouchCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "touch"))
	fields := tokenizeShell(rest)
	var args []string
	for _, f := range fields {
		if !strings.HasPrefix(f, "-") {
			args = append(args, fmt.Sprintf("\"%s\"", f))
		}
	}
	if len(args) > 0 {
		return "New-Item -ItemType File " + strings.Join(args, ", ")
	}
	return "New-Item -ItemType File"
}

// convertSortCommand converts sort command to PowerShell Sort-Object
func convertSortCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "sort"))
	fields := tokenizeShell(rest)
	var inputFile string
	unique := false
	for _, f := range fields {
		if f == "-u" {
			unique = true
		} else if !strings.HasPrefix(f, "-") {
			inputFile = f
		}
	}
	baseCmd := "Sort-Object"
	if unique {
		baseCmd += " -Unique"
	}
	if inputFile != "" {
		return fmt.Sprintf("Get-Content \"%s\" | %s", inputFile, baseCmd)
	}
	return baseCmd
}

// convertUniqCommand converts uniq command to PowerShell Get-Unique
func convertUniqCommand(cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "uniq"))
	fields := tokenizeShell(rest)
	var inputFile string
	for _, f := range fields {
		if !strings.HasPrefix(f, "-") {
			inputFile = f
		}
	}
	if inputFile != "" {
		return fmt.Sprintf("Get-Content \"%s\" | Get-Unique", inputFile)
	}
	return "Get-Unique"
}

// tryDecodeUTF8 委托 util.DecodeToUTF8，失败时降级到原 utf8.Valid + RuneError 替换逻辑。
// 降级路径仅在 DecodeToUTF8 返回 error 时触发（如 EncodingUnknown），正常情况不进入。
func tryDecodeUTF8(data []byte) string {
	if s, err := util.DecodeToUTF8(data); err == nil {
		return s
	}
	// 降级路径（与旧行为一致）
	if utf8.Valid(data) {
		return string(data)
	}
	var buf bytes.Buffer
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			buf.WriteRune('\uFFFD')
		} else {
			buf.WriteRune(r)
		}
		data = data[size:]
	}
	return buf.String()
}

// bashHandler 返回 Bash 命令执行处理器
func (e *Executor) bashHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input BashInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.Command == "" {
			return "", fmt.Errorf("command cannot be empty")
		}

		// B7: 沙箱验证 — 优先使用 Manager.Prepare 模式，回退到 legacy Sandbox.Validate
		sbCtx, cleanup, sandboxed, sandboxErr := e.prepareSandbox(ctx, input.Command, workDir)
		if sandboxErr != nil {
			// ★ ：双语 SSOT 形态（util.BiSame）—— 修前是
			// "sandbox denied / 沙箱拒绝\nCommand / 命令: …" 行内并列（中文行无缩进，过不了形态门）。
			// 英文主行**保留小写 "sandbox denied"**（plugin_integration_test.go:311 钉该子串）。
			return util.BiSame(
				"sandbox denied\nCommand: %s\nReason: %s\nNo command was run.",
				"沙箱拒绝\n命令：%s\n原因：%s\n命令未执行。",
				input.Command, sandboxErr.Error()), fmt.Errorf("%w: %s", errSandboxDenied, sandboxErr)
		}
		if cleanup != nil {
			defer cleanup()
		}

		// detect sleep pattern, guide to use Monitor or run_in_background
		// ★ ：秒数由 monitor.MaxAllowedSleepSeconds 派生（经 SleepBlock.LimitSeconds），
		// 本处**不得**写死 —— 与 internal/tools/monitor.go 同源（：两侧只薄转发，
		// 判据两侧都验）。措辞给**可机械读出的整数上界**（"to N second(s) or less"）；
		// 不要用 "under N seconds" 这类既像 < 又像 ≤ 的写法（读不出整数 = 不可判别）。
		//
		// ★ ：本条文案里点名的**每一个入口都必须是模型真能走的**。
		// 修复前它只说 "use run_in_background: true option"（且 `option;` 与 `for` 之间
		// **缺空格**、语义别扭），把模型推进后台后却没有任何读输出的工具 ⇒ 模型只能瞎编结果
		// （2026-10-09 18:07 用户直报）。现在补上 TaskOutput 这个真实出口并明说二者分工。
		if !input.RunInBackground {
			if sb, blocked := monitor.DetectBlockedSleep(input.Command); blocked {
				return fmt.Sprintf(
					"⚠️ Blocked: %s.\nTo run a long command in the background, use run_in_background: true, "+
						"then read what it printed with the TaskOutput tool (TaskList only shows status). "+
						"For streaming monitoring (e.g. view logs, poll API), use the Monitor tool. "+
						"If you truly need a delay (rate limiting etc.), keep it to %d second(s) or less.",
					sb.Reason, sb.LimitSeconds,
				), fmt.Errorf("sleep pattern blocked")
			}
		}

		// 超时：默认值兜底 ＋ 上界 clamp。语义与常量单一声明在 limits.go。
		// 修复前此处只有下界（`<=0 ⇒ 120s`）、**没有上界** ⇒ `input.Timeout = 36000`（10h）原样生效。
		timeoutSeconds := ClampTimeoutSeconds(input.Timeout, DefaultBashTimeoutSeconds, MaxBashTimeoutSeconds)
		// 可观测性由**生效值**派生（不是与常量比较）⇒ 若 clamp 被移除，本标记自然消失，
		// 判据因此真正可判别（反例注入见 .go 头部）。
		timeoutClamped := input.Timeout > timeoutSeconds
		timeout := time.Duration(timeoutSeconds) * time.Second

		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		commandToRun := input.Command
		if runtime.GOOS == "windows" {
			commandToRun = convertLinuxToPowerShell(input.Command)
		}

		// Handle run_in_background: execute as a background task
		if input.RunInBackground {
			task := e.TaskMgr.Create(input.Command, "background bash command", input.Timeout)
			// 后台不再可能「默认永久」—— Create 内部已 clamp（默认 600s / 上界 7200s）。
			// 生效超时随结果一并返回 ⇒ 「设了却被截断」不是静默行为改变（可观测）。
			// ★ ：回执必须把「状态」与「输出」两个入口分开点名 ——
			// 修复前只说 "使用 TaskList 查看状态"，而这正是用户直报里那句"模型看不到输出"的来源
			// （TaskList 的格式串只有 ID/status/command/duration，不含 Output）。
			// ★ ：双语 SSOT 形态（util.BiSame）—— 修前中文为主。
			msg := util.BiSame(
				"Background task started: %s\nCommand: %s\nTimeout: %ds\nStatus: TaskList; Output: TaskOutput",
				"后台任务已启动: %s\n命令: %s\n超时: %ds\n状态: TaskList；输出: TaskOutput",
				task.ID, input.Command, task.TimeoutSeconds)
			if input.Timeout > task.TimeoutSeconds {
				msg += fmt.Sprintf("\n[timeout clamped: %ds -> %ds]", input.Timeout, task.TimeoutSeconds)
			}
			return msg, nil
		}

		// R4: PowerShell 安全检查（fail-closed，Critical 命中拒绝执行）
		if runtime.GOOS == "windows" {
			if violations := powershell.RunSecurityChecksShortCircuit(commandToRun); len(violations) > 0 {
				return "", fmt.Errorf("powershell security blocked: %s", violations[0].Message)
			}
		}

		// 迭代③（2026-10-09 拍板）：bash 的**字面写重定向**目标入 filehistory track
		// ⇒ 轮末卡片能看到 `> out/t3.txt` 这类 bash 写的文件（此前只有 write_file /
		// edit_file / apply_unified_diff 三工具入账，真机实测 `>` 写的文件不显示）。
		//
		// 事实提取复用审批面的**唯一 AST 提取器** `validator.BashWriteTargets`
		// （tokenizer→AST，非正则：引号/heredoc/管道不误读；`2>&1` 等 fd 复制不落盘不取；
		// `/dev/null` 等 sink 已剔除；变量/命令替换标 unresolved ⇒ **不猜、不编造**）。
		// 路径是否在工作区内以 `filehistory.Track` 内部的 safePath 为唯一权威：
		// 工作区外的目标 Track 报错 ⇒ 逐路径跳过并记日志（展示功能不得让命令失败）。
		//
		// 诚实边界（漏报方向，已登记总账）：tee/mv/cp/sed -i/脚本内部写提不出来；
		// Windows PowerShell 重定向语义未取证，本轮不入账；run_in_background 走 TaskMgr
		// 独立执行链、Monitor 是另一个工具，均不入账。（2026-10-16 用户拍板：曾用
		// BashRan 旗标在卡片顶部加提示行兜底，因每轮必现太吵已整链取消。）
		//
		// ★ msgID 在执行**前**捕获一次、TrackAfter 复用同一个值 —— 聚合按 MsgId 配对，
		// 两处各取现值的话，轮边界一旦在长命令期间翻转，before/after 就配不上对。
		var bashTrackedPaths []string
		var bashMsgID string
		if e.FileHistory != nil && runtime.GOOS != "windows" {
			bashMsgID = e.CurrentMessageId()
			targets, _ := validator.BashWriteTargets(commandToRun)
			for _, tgt := range targets {
				p := tgt
				if !filepath.IsAbs(p) && workDir != "" {
					p = filepath.Join(workDir, p)
				}
				p = filepath.Clean(p)
				if err := e.FileHistory.Track(bashMsgID, ToolCallIDFromCtx(ctx), "bash", []string{p}); err != nil {
					// 工作区外（safePath 拒绝）等 ⇒ 该目标不入账；非致命，命令继续。
					log.Printf("[bash] redirect target not tracked (non-fatal): %s: %v", p, err)
					continue
				}
				bashTrackedPaths = append(bashTrackedPaths, p)
			}
		}

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", commandToRun)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", commandToRun)
		}
		if workDir != "" {
			cmd.Dir = workDir
		}

		// B7/B9: 沙箱上下文生效 — 设置工作目录、环境变量
		if sandboxed {
			if sbCtx.WorkDir != "" {
				cmd.Dir = sbCtx.WorkDir
			}
			if len(sbCtx.Env) > 0 {
				cmd.Env = sbCtx.Env
			}
		}

		var stdout, stderr safebuf.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		// B7/C11: **两条分支都用进程组启动**，确保可终止整个进程树。
		//
		// （2026-09-20 批 D）：非 sandboxed 分支原为裸 `cmd.Start()`（无 Setpgid）
		// ⇒ 既无超时约束、也不能调杀组（调了会命中**自身进程组**，PIT-005 自杀）。
		// 统一为 `StartCommandWithProcessGroup` 后，`WaitProcessGroupWithContext` 对两条分支都安全可用
		// ⇒ 超时语义不再依赖「沙箱是否开启」，同族剩余面一并消除（收敛为一份实现）。
		var startErr error
		if sandboxed {
			// P0-1 修复：执行命令前应用 OS 级隔离（设置 SysProcAttr.Cloneflags / Job Object）。
			// Restrict 失败则 fail-closed 不启动命令，避免静默降级导致 OS 沙箱被绕过。
			// 默认配置（OSLevel 全 false）下 OSSandbox 为 degradedSandbox，Restrict 为 noop，零行为变化。
			if osSb := sbCtx.OSSandbox(); osSb != nil {
				if restrictErr := osSb.Restrict(cmd); restrictErr != nil {
					return "", fmt.Errorf("sandbox: OS restrict failed (command not started for safety): %w", restrictErr)
				}
			}
		}
		startErr = sandbox.StartCommandWithProcessGroupContext(ctx, cmd)
		if startErr != nil {
			return "", fmt.Errorf("command start failed: %w", startErr)
		}

		// P0-3：Start 后调用 PostStartHook（Windows Job Object AssignProcess）。
		// 失败时 kill 进程并返回错误（fail-closed），避免进程在 Job 外运行无资源限制。
		if sandboxed && sbCtx.PostStartHook != nil {
			if hookErr := sbCtx.PostStartHook(cmd); hookErr != nil {
				_ = sandbox.KillProcessGroup(cmd)
				_ = cmd.Wait() // 释放僵尸进程（审查优化1采纳）
				return "", fmt.Errorf("sandbox: post-start hook failed: %w", hookErr)
			}
		}

		// P1-1：Start 后应用资源限制（Linux prlimit / macOS setrlimit）。
		// Windows 资源限制通过 Job Object 在 Restrict 时设置，无需此处调用。
		if sandboxed && sbCtx.ResourceLimits != (sandbox.ResourceLimits{}) {
			if err := sandbox.ApplyResourceLimits(cmd, sbCtx.ResourceLimits, e.getAuditLog()); err != nil {
				// 资源限制失败不阻断执行（best-effort），仅记日志
				// 原因：某些平台可能不支持特定 rlimit 类型
			}
		}

		// 等待命令完成；ctx 到期/取消时**先杀整个进程组**再收尾。
		//
		// 修复前这里是「先 cmd.Wait()、后 if ctx.Err()!=nil { KillProcessGroup }」：
		// 管道命令里持有 stdout 写端的孙进程会拖住 Wait，使其后的杀组**永不可达** ⇒ 超时失效。
		// 修复后两条分支共用 sandbox 的**单一实现**（cmd 已由 StartCommandWithProcessGroup 建组 ⇒ 武装）。
		_, err := sandbox.WaitProcessGroupWithContext(ctx, cmd)

		// 迭代③：命令真实启动后补记 after 状态（与执行前的 Track 配对）。
		// ★ 放在 err 判定**之前**：`>` 在 shell 拉起进程时就截断/创建文件，
		// 命令 exit≠0 不改变"重定向写入已发生"这一事实（如 `go test > out.txt`
		// 测试失败时 out.txt 照样写入）。命令没启动的路径（沙箱拒绝/启动失败）
		// 在上方已提前 return，走不到这里 ⇒ 不会把"没跑的命令"记成变更。
		if e.FileHistory != nil && len(bashTrackedPaths) > 0 {
			if err := e.FileHistory.TrackAfter(bashMsgID, ToolCallIDFromCtx(ctx), bashTrackedPaths); err != nil {
				log.Printf("[bash] redirect TrackAfter failed (non-fatal): %v", err)
			}
		}

		output := tryDecodeUTF8(stdout.Bytes())
		if stderr.Len() > 0 {
			if output != "" {
				output += "\n"
			}
			output += "[stderr]\n" + tryDecodeUTF8(stderr.Bytes())
		}

		if output == "" && err != nil {
			output = err.Error()
		}

		// ===== Output compression (before hard truncation) =====
		if e.Compressor != nil {
			result := e.Compressor.CompressWithProfile(output, input.Command)
			output = result.Output
		}

		// 输出上限：env 派生 SSOT（GetOutputLimits）为默认，运行时 Config 覆盖优先。
		// 修复前默认值是 bash.go 自有的 defaultBashMaxOutput(=50000)，与 limits.go 的
		// DefaultBashMaxOutput 并存（同一语义两处声明）⇒ 已收敛为 limits.go 的唯一声明。
		maxOutputLen := GetOutputLimits().BashMaxOutput
		if e.Config != nil {
			e.configMu.RLock()
			if v, ok := e.Config["bash_max_output"]; ok {
				if parsed, parseErr := fmt.Sscanf(v, "%d", &maxOutputLen); parseErr != nil || parsed != 1 {
					maxOutputLen = GetOutputLimits().BashMaxOutput
				}
			}
			e.configMu.RUnlock()
		}
		// 被 clamp 的事实必须可观测（避免"设了 10h 却只跑 2h"成为静默行为改变）。
		// ★ 附注必须在截断**之前**追加：截断标记（权威）恒须紧贴结尾，
		// 否则下游 TruncationDropped 不认，合并计数失效、标记可能被后续层咬掉。
		if timeoutClamped {
			output += fmt.Sprintf("\n[timeout clamped: %ds -> %ds]", input.Timeout, MaxBashTimeoutSeconds)
		}

		// 收口：砍尾走唯一权威 TruncateTail —— 标记带真实丢弃量（旧文案
		// "(output truncated)" 连丢弃量都不报；同值上限两层各砍一刀时，第二刀
		// 还会把这句话切掉，屏幕只报 23 —— 那是标记串自身的长度）。
		if len(output) > maxOutputLen {
			output = TruncateTail(output, maxOutputLen)
		}

		if err != nil {
			// ★ ：沙箱内失败 + EPERM 文本 ⇒ 这是**沙箱干的**，
			// 不是命令的错。改前模型只看到「exit 1」这个谎话（(c) 的 command_exit
			// 措辞），既不知道该改写命令，也没有路把用户请回来 —— 业内同构做法：
			// 沙箱内被拒 ⇒ 重新询问 ⇒ 批准后沙箱外重跑（设计拍板 §1）。
			//
			// 检测判据 = 输出模式匹配（`Operation not permitted`，EPERM 的 strerror 文本）
			// —— 与业内同类实现同构（它也靠错误文本识别 sandbox-caused）。
			// 保守边界：只在 `sandboxed && err != nil` 时判（exit 0 的输出含模式不算，
			// 非 EPERM 失败不算）；Linux landlock 的 EACCES 文本太泛，**不检测**（残留）。
			if sandboxed && epermOutput(output) {
				output += "\n" + util.BiSame(
					"sandbox kernel denied (EPERM): the command ran but the OS sandbox blocked an operation.\n  Ask the user to approve running this command outside the sandbox, or rewrite it to stay inside the workspace.",
					"沙箱内核拒绝（EPERM）：命令已运行，但 OS 沙箱拦下了某个操作。\n  可请求用户批准本条命令在沙箱外执行，或改写命令使其留在工作区内。")
				return output, fmt.Errorf("%w: %v", errSandboxExecutionDenied, err)
			}
			// ★ (c)：命令**跑完了**、它的答案是"失败" ⇒ 这是**结果数据**，
			// 不是"工具坏了"。修前这里是一条裸 fmt.Errorf ⇒ executor 的兜底包装把它与真故障
			// 合并成同一句 `tool execution failed: …`（R4：可用结构化种类表达的事实被压成散文）。
			// 现改为**结构化种类**经 error 通道携带（同 patch_conflict 的机制，
			// FailureKindOf 用 errors.As 提取）⇒ 措辞由 `ToolErrorOutput` 单点分层。
			// ★ 关键：Kind 登记为 **Terminal=false** —— 一次可自愈的命令失败不得掐掉整个回合。
			return output, &ToolExecError{
				Tool: "bash",
				Kind: FailureKindCommandExit,
				Msg:  commandExitMessage(err),
			}
		}
		return output, nil
	}
}

// prepareSandbox 统一沙箱准备入口（B7）。
//
// 优先使用 Manager.Prepare + ValidateCommand 模式（C1 契约）；
// Manager 不可用时回退到 legacy Sandbox.Validate（向后兼容）；
// 两者都不可用时返回不沙箱化执行。
//
// 返回值：
// - sbCtx: 沙箱上下文（含 Env/WorkDir），sandboxed=false 时为零值
// - cleanup: 清理函数（可能为 nil），调用方应 defer cleanup()
// - sandboxed: 是否在沙箱内执行
// - err: 命令被沙箱拒绝时返回非 nil
func (e *Executor) prepareSandbox(ctx context.Context, command, workDir string) (sandbox.SandboxContext, func(), bool, error) {
	// ★ ：用户已对**这一条**命令的沙箱可批准档按下 Yes。
	//
	// 语义 = 同类 CLI 的 `BypassSandboxFirstAttempt`（core/src/tools/sandboxing.rs:274）：
	// **批准后该条命令在沙箱外执行**。这是"允许了还会被拒绝"的结构性修法 ——
	// 审批链（SandboxPrecheckStage）已放行，若这里不复用同一事实，执行链会再否决一次，
	// 用户看到的就是"点了允许，命令照样失败"。
	//
	// 三条边界，缺一不可：
	// ① 标记**逐条**（context 携带，不写盘、不记忆）—— 见 sandbox_sanction.go；
	// ② 纵深防御：即便标记泄漏，也用非 sanctioned 口径之外的方式再确认一次
	// —— `ValidateSanctioned(cmd, true)` **只**放行 TierConfirm 档，
	// 硬拒档（rm/dd/mkfs/format、反向 shell、控制字符）照样拦；
	// ③ 没有可用的档位判定器时 **fail-closed**（不敢在无法判定档位的情况下放开沙箱）。
	if sandboxSanctioned(ctx) {
		if e.Sandbox == nil || !e.Sandbox.Enabled() {
			return sandbox.SandboxContext{}, nil, false,
				fmt.Errorf("sandbox sanction cannot be verified: sandbox classifier not enabled")
		}
		if allowed, reason := e.Sandbox.ValidateSanctioned(command, true); !allowed {
			return sandbox.SandboxContext{}, nil, false, fmt.Errorf("%s", reason)
		}
		// Manager 的 20 项安全检查套件（manager.go:314）里 **Critical** 级
		// （逃逸 / exfil 载体）不得被"一次批准"绕过 —— 那些不是"日常可逆"操作，
		// 与档位表里硬拒档同类。
		if e.SandboxMgr != nil {
			if res, err := e.SandboxMgr.ValidateCommand(ctx, command, nil); err == nil && res != nil {
				for _, v := range res.Violations {
					if v.Severity >= sandbox.SeverityCritical {
						return sandbox.SandboxContext{}, nil, false,
							fmt.Errorf("command blocked: %s", v.Message)
					}
				}
			}
		}
		// sandboxed=false ⇒ 沙箱外执行（OS 隔离与网络限制本次不施加）。
		return sandbox.SandboxContext{}, nil, false, nil
	}

	// Keep the user-controlled /sandbox on switch effective even when the
	// production Executor also has a Manager configured. The legacy validator
	// contains the cross-platform network deny list (curl/wget/nc/etc.), while
	// Manager provides the OS-level isolation boundary where available.
	if e.Sandbox != nil && e.Sandbox.Enabled() {
		if allowed, reason := e.Sandbox.Validate(command); !allowed {
			return sandbox.SandboxContext{}, nil, false, fmt.Errorf("%s", reason)
		}
	}

	// 路径 1: Manager 模式（C1/B7 优先）
	if e.SandboxMgr != nil {
		level := e.SandboxMgr.Level()
		if level == sandbox.SandboxLevelDisabled {
			return sandbox.SandboxContext{}, nil, false, nil
		}
		// 命令验证（B8: 返回 ValidationResult）
		result, vErr := e.SandboxMgr.ValidateCommand(ctx, command, nil)
		if vErr != nil {
			return sandbox.SandboxContext{}, nil, false, vErr
		}
		// ★ ：执行链只硬拒 **Critical（逃逸/不可逆载体）**。
		//
		// 旧实现按 result.Blocked（= 任一 Severity>=High）一刀切硬拒 ⇒ validator
		// 套件里的文本混淆**疑似项**（LOCALE-Q/ANSIC-Q/BRACE-EXP 等，High 级）在这里
		// 无声处死，而**审批侧 SandboxPrecheckStage 只认 ruleengine 七规则、从未见过
		// 这批检查** —— 用户连批准框都看不到，命令一行不跑。这是 在 ruleengine
		// 里修掉、却在 validator 这条腿上还活着的「执行审批层从未见过的拒绝」病
		// （真机 T-3 误伤实锤：`git ls-files | grep -E "\.go$"` 被 LOCALE-Q 拒）。
		//
		// 新口径（与上方 sanctioned 路径的 Critical-only 既有判据一致）：
		// - Critical（fork 炸弹 / 替换内层 rm / 危险重定向 / AST 逃逸载体）⇒ 硬拒；
		// - High 疑似项 ⇒ 照常进 OS 沙箱执行 —— 文本疑似 ≠ 实际危险，
		// 牢房真管住了就不必看长相枪毙（业内对标：牢房先跑、跑不动再请示）。
		//
		// ⚠️ ValidateCommand 本身的 Blocked 契约（Critical/High ⇒ Blocked）不变，
		// 但其 Severity 已由 改为**携带后果档**（TierHardDeny ⇒ Critical、
		// TierConfirm ⇒ High，见 manager.go）—— 执行链消费 Critical-only 即
		// 「只硬拒硬拒档」，与 SandboxPrecheckStage 的 Classify 分档同源。
		if result != nil {
			var criticals []string
			for _, v := range result.Violations {
				if v.Severity >= sandbox.SeverityCritical {
					criticals = append(criticals, fmt.Sprintf("[%s] %s", v.CheckID, v.Message))
				}
			}
			if len(criticals) > 0 {
				return sandbox.SandboxContext{}, nil, false,
					fmt.Errorf("command blocked: %s", strings.Join(criticals, "; "))
			}
		}
		// 准备沙箱上下文
		sbCtx, cleanup, pErr := e.SandboxMgr.Prepare(ctx, level, workDir)
		if pErr != nil {
			return sandbox.SandboxContext{}, nil, false, fmt.Errorf("sandbox prepare failed: %w", pErr)
		}
		return sbCtx, cleanup, true, nil
	}

	// 路径 2: Legacy Sandbox 模式（向后兼容）
	if e.Sandbox != nil && e.Sandbox.Enabled() {
		allowed, reason := e.Sandbox.Validate(command)
		if !allowed {
			return sandbox.SandboxContext{}, nil, false, fmt.Errorf("%s", reason)
		}
		return sandbox.SandboxContext{}, nil, true, nil
	}

	// 路径 3: 无沙箱
	return sandbox.SandboxContext{}, nil, false, nil
}

// epermOutput 判断命令输出是否含 OS 沙箱 EPERM 的 strerror 文本
// （检测判据的唯一声明；判据讨论见 bash.go 失败分支注释）。
func epermOutput(output string) bool {
	return strings.Contains(output, "Operation not permitted")
}

// commandExitMessage 把「命令非零退出」的 error 渲染成**数据口吻**的说明（(c)）。
//
// 要点（三句必须同时到）：
// 1. **退出码是几**（模型据此判断是"测试失败"还是"grep 没命中"）；
// 2. 命令**跑完了**（与"起不来 / 被拒"区分开）；
// 3. 这是**命令的结果**，不是**工具故障** —— 修前那句 `tool execution failed`
// 正是让模型以为自己用错了工具、进而放弃这条路的元凶。
//
// 拿不到具体码时退回 %v（如信号终止：`signal: killed`）—— 不退化成"故障"口径。
func commandExitMessage(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf(
			"command exited with status %d (non-zero exit; the command ran to completion — this is the command's result, not a tool malfunction)",
			exitErr.ExitCode())
	}
	return fmt.Sprintf(
		"command ended without a zero exit status (%v) — the command ran to completion; this is the command's result, not a tool malfunction",
		err)
}
