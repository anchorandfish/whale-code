// Package bash — 路径提取器（T3，30+，对标同类 CLI pathValidation.ts 40+）。
//
// 从已解析命令提取被操作路径，供沙箱路径校验（sandbox.ValidatePath）使用。
// 不同命令的"路径参数位置"不同（cp 第二参数为目标，grep 第一个非 flag 是 pattern）。
package bash

import "strings"

// PathExtractor 单命令路径提取器。
type PathExtractor func(cmd *SingleCommand) []string

// pathExtractors 30+ 命令路径提取器。
var pathExtractors = map[string]PathExtractor{
	// 单路径（第一个非 flag）
	"cat": extractFirstPath, "vim": extractFirstPath, "vi": extractFirstPath,
	"nano": extractFirstPath, "emacs": extractFirstPath, "less": extractFirstPath,
	"more": extractFirstPath, "head": extractFirstPath, "tail": extractFirstPath,
	"find": extractFirstPath, "ls": extractFirstPath, "unzip": extractFirstPath,
	"patch": extractFirstPath, "sort": extractFirstPath, "uniq": extractFirstPath,
	"cut": extractFirstPath, "wc": extractFirstPath, "file": extractFirstPath,
	"stat": extractFirstPath, "du": extractFirstPath, "df": extractFirstPath,
	// 所有非 flag 路径
	"rm": extractAllPaths, "rmdir": extractAllPaths, "mkdir": extractAllPaths,
	"touch": extractAllPaths, "tee": extractAllPaths, "chmod": extractAllPaths,
	"chown": extractAllPaths, "zip": extractAllPaths, "diff": extractAllPaths,
	// 第二参数为目标（cp/mv/install/ln）
	"cp": extractSecondPath, "mv": extractSecondPath, "install": extractSecondPath,
	"ln": extractSecondPath, "rsync": extractSecondPath,
	// pattern + 文件（grep/awk，跳过第一个非 flag 作为 pattern）
	"grep": extractNonFlagArgs, "egrep": extractNonFlagArgs, "fgrep": extractNonFlagArgs,
	"awk": extractNonFlagArgs,
	// 表达式 + 输入文件（sed）
	"sed": extractInputFile,
	// of= 参数（dd）
	"dd": extractOfParam,
	// 输出参数（curl -o / wget -O）
	"curl": extractCurlOutput, "wget": extractWgetOutput,
	// tar
	"tar": extractTarPath,
	// git
	"git": extractGitPath,
	// xargs 不直接操作路径
	"xargs": extractNone,
}

// ExtractPaths 从单条命令提取被操作路径（基于命令名分发）。
func ExtractPaths(cmd *SingleCommand) []string {
	if cmd == nil {
		return nil
	}
	if ext, ok := pathExtractors[cmd.Name]; ok {
		return ext(cmd)
	}
	return nil
}

// ExtractAllPathsFromAST 从整个 AST 提取所有命令的路径（聚合）。
func ExtractAllPathsFromAST(ast *CommandAST) []string {
	if ast == nil {
		return nil
	}
	var all []string
	for _, cmd := range ast.Commands {
		all = append(all, ExtractPaths(cmd)...)
		// 重定向目标也是路径
		for _, r := range cmd.Redirects {
			if r.Target != "" {
				all = append(all, r.Target)
			}
		}
	}
	return all
}

// extractFirstPath 提取第一个非 flag 参数。
func extractFirstPath(cmd *SingleCommand) []string {
	for _, arg := range cmd.Args {
		if !strings.HasPrefix(arg, "-") {
			return []string{arg}
		}
	}
	return nil
}

// extractAllPaths 提取所有非 flag 参数（-- 之后的参数视为文件名）。
func extractAllPaths(cmd *SingleCommand) []string {
	var paths []string
	inFlags := false
	for _, arg := range cmd.Args {
		if arg == "--" {
			inFlags = true
			continue
		}
		if !inFlags && strings.HasPrefix(arg, "-") {
			continue
		}
		paths = append(paths, arg)
	}
	return paths
}

// extractSecondPath 提取第二个非 flag 参数（cp/mv 目标路径）。
func extractSecondPath(cmd *SingleCommand) []string {
	var nonFlagArgs []string
	inFlags := false
	for _, arg := range cmd.Args {
		if arg == "--" {
			inFlags = true
			continue
		}
		if !inFlags && strings.HasPrefix(arg, "-") {
			continue
		}
		nonFlagArgs = append(nonFlagArgs, arg)
	}
	if len(nonFlagArgs) >= 2 {
		return []string{nonFlagArgs[1]} // 返回目标路径
	}
	return nil
}

// extractNonFlagArgs 提取所有非 flag 参数（grep/awk，跳过第一个非 flag 作为 pattern）。
func extractNonFlagArgs(cmd *SingleCommand) []string {
	var paths []string
	inFlags := false
	skippedPattern := false
	for _, arg := range cmd.Args {
		if arg == "--" {
			inFlags = true
			continue
		}
		if !inFlags && strings.HasPrefix(arg, "-") {
			continue
		}
		if !skippedPattern {
			skippedPattern = true // 第一个非 flag 是 pattern，跳过
			continue
		}
		paths = append(paths, arg)
	}
	return paths
}

// extractInputFile 提取输入文件（sed 'expr' <file>，跳过第一个非 flag 作为表达式）。
func extractInputFile(cmd *SingleCommand) []string {
	var paths []string
	inFlags := false
	skippedExpr := false
	for _, arg := range cmd.Args {
		if arg == "--" {
			inFlags = true
			continue
		}
		if !inFlags && strings.HasPrefix(arg, "-") {
			continue
		}
		if !skippedExpr {
			skippedExpr = true
			continue
		}
		paths = append(paths, arg)
	}
	return paths
}

// extractOfParam 提取 of=<file> 参数（dd）。
func extractOfParam(cmd *SingleCommand) []string {
	for _, arg := range cmd.Args {
		if strings.HasPrefix(arg, "of=") {
			return []string{strings.TrimPrefix(arg, "of=")}
		}
	}
	return nil
}

// extractCurlOutput 提取 curl -o <file> / --output <file>。
func extractCurlOutput(cmd *SingleCommand) []string {
	var paths []string
	for i := 0; i < len(cmd.Args); i++ {
		arg := cmd.Args[i]
		if arg == "-o" && i+1 < len(cmd.Args) {
			paths = append(paths, cmd.Args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(arg, "--output=") {
			paths = append(paths, strings.TrimPrefix(arg, "--output="))
		}
	}
	return paths
}

// extractWgetOutput 提取 wget -O <file>。
func extractWgetOutput(cmd *SingleCommand) []string {
	for i := 0; i < len(cmd.Args); i++ {
		arg := cmd.Args[i]
		if arg == "-O" && i+1 < len(cmd.Args) {
			return []string{cmd.Args[i+1]}
		}
	}
	return nil
}

// extractTarPath 提取 tar 的 -f 参数路径（支持 -cf / -f / --file= 组合短选项）。
func extractTarPath(cmd *SingleCommand) []string {
	var paths []string
	for i := 0; i < len(cmd.Args); i++ {
		arg := cmd.Args[i]
		// -f <file>
		if arg == "-f" && i+1 < len(cmd.Args) {
			paths = append(paths, cmd.Args[i+1])
			i++
			continue
		}
		// 组合短选项：-cf <file> 或 -czf <file>，f 后的下一个参数为文件
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "f") {
			// f 是最后一个选项字母 → 下一个参数是文件
			if strings.HasSuffix(arg, "f") && i+1 < len(cmd.Args) {
				paths = append(paths, cmd.Args[i+1])
				i++
				continue
			}
			// f 后紧跟文件名（如 -ftest.tar）
			fIdx := strings.Index(arg, "f")
			if fIdx >= 0 && fIdx < len(arg)-1 {
				paths = append(paths, arg[fIdx+1:])
			}
			continue
		}
		if strings.HasPrefix(arg, "--file=") {
			paths = append(paths, strings.TrimPrefix(arg, "--file="))
		}
	}
	return paths
}

// extractGitPath 提取 git 命令中的路径（add/checkout/restore/rm/mv 等）。
func extractGitPath(cmd *SingleCommand) []string {
	if len(cmd.Args) == 0 {
		return nil
	}
	sub := cmd.Args[0]
	switch sub {
	case "add", "checkout", "restore", "rm":
		return extractAllPaths(&SingleCommand{Name: cmd.Name, Args: cmd.Args[1:]})
	case "mv":
		// git mv <src> <dst>，目标为 dst
		return extractSecondPath(&SingleCommand{Name: cmd.Name, Args: cmd.Args[1:]})
	}
	return nil
}

// extractNone 不提取路径（xargs 等）。
func extractNone(cmd *SingleCommand) []string { return nil }

// DangerousRemovalPaths 危险删除路径集合（rm/rmdir 目标不得为这些路径）。
var DangerousRemovalPaths = []string{
	"/", "/home", "/usr", "/var", "/etc", "/bin", "/sbin",
	"/boot", "/sys", "/proc", "/dev", "/tmp", "/var/tmp", "/root",
}

// CheckDangerousRemoval 检测危险删除路径（不解析符号链接，对标同类 CLI）。
// 返回命中的危险路径（若有）。
func CheckDangerousRemoval(paths []string, workDir string) []string {
	var hit []string
	dangerous := append([]string{}, DangerousRemovalPaths...)
	for _, p := range paths {
		absPath := p
		if !isAbsPath(p) && workDir != "" {
			absPath = workDir + "/" + p
		}
		absPath = cleanPath(absPath)
		for _, d := range dangerous {
			if absPath == d {
				hit = append(hit, p)
			}
		}
	}
	return hit
}

// isAbsPath 判断绝对路径（跨平台）。
func isAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':')
}

// cleanPath 简化路径（去除 ./ 与冗余分隔符，不解析符号链接）。
func cleanPath(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	for strings.Contains(p, "/./") {
		p = strings.ReplaceAll(p, "/./", "/")
	}
	// 保留根 "/"，仅当路径非根时去除末尾分隔符
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
