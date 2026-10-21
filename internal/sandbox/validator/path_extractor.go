// Package validator — 路径提取器（B6）。
//
// 修复 SANDBOX-006 关联的 tokenizeCommand 缺陷：原实现遇 |;& 直接截断返回，
// 导致复合命令 `echo ok; rm -rf /` 只检查 `echo ok`。本实现改为：
//   - splitSegments 按引号感知的方式将命令拆分为多个段（| ; & 分隔）
//   - Extract 对每个段分别提取路径，合并结果
package validator

import (
	"path/filepath"
	"strings"
)

// PathExtractor 路径提取器，按命令名注册提取策略。
type PathExtractor struct {
	extractors map[string]PathExtractFunc
}

// PathExtractFunc 从参数列表中提取路径。
type PathExtractFunc func(args []string) []string

// NewPathExtractor 创建带默认 30+ 命令提取器的实例。
func NewPathExtractor() *PathExtractor {
	pe := &PathExtractor{extractors: make(map[string]PathExtractFunc)}
	pe.registerDefaults()
	return pe
}

func (pe *PathExtractor) registerDefaults() {
	// 第一个非 flag 参数
	firstPath := func(args []string) []string {
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				return []string{a}
			}
		}
		return nil
	}
	// 所有非 flag 参数
	allPaths := func(args []string) []string {
		var paths []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				paths = append(paths, a)
			}
		}
		return paths
	}
	// 第二个非 flag 参数（目标路径）
	secondPath := func(args []string) []string {
		var nonFlag []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				nonFlag = append(nonFlag, a)
			}
		}
		if len(nonFlag) >= 2 {
			return []string{nonFlag[1]}
		}
		return nil
	}
	// 最后一个非 flag 参数（目标，如 ln -s src dst → dst）
	lastPath := func(args []string) []string {
		var nonFlag []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				nonFlag = append(nonFlag, a)
			}
		}
		if len(nonFlag) >= 1 {
			return []string{nonFlag[len(nonFlag)-1]}
		}
		return nil
	}
	// dd 的 of= 参数
	ddOutput := func(args []string) []string {
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				return []string{a[3:]}
			}
		}
		return nil
	}
	// -f / --file 参数值（tar/zip）
	fileFlag := func(args []string) []string {
		for i, a := range args {
			if a == "-f" || a == "--file" || a == "-o" || a == "--output" {
				if i+1 < len(args) {
					return []string{args[i+1]}
				}
			}
			if strings.HasPrefix(a, "--file=") {
				return []string{strings.TrimPrefix(a, "--file=")}
			}
			if strings.HasPrefix(a, "--output=") {
				return []string{strings.TrimPrefix(a, "--output=")}
			}
		}
		return nil
	}
	// chmod/chown：跳过模式参数，提取目标
	modeThenPaths := func(args []string) []string {
		var paths []string
		skippedMode := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if !skippedMode {
				skippedMode = true // 第一个非 flag 是模式（如 755/u+x）
				continue
			}
			paths = append(paths, a)
		}
		return paths
	}

	// 注册：第一个非 flag 参数
	for _, cmd := range []string{"cat", "vim", "vi", "nano", "less", "more", "head", "tail", "wc", "sort", "uniq", "file", "stat", "du", "df"} {
		pe.extractors[cmd] = firstPath
	}
	// 注册：所有非 flag 参数
	for _, cmd := range []string{"rm", "touch", "mkdir", "tee", "truncate"} {
		pe.extractors[cmd] = allPaths
	}
	// 注册：第二个非 flag 参数（目标）
	for _, cmd := range []string{"cp", "mv", "install", "rsync"} {
		pe.extractors[cmd] = secondPath
	}
	// 注册：最后一个非 flag 参数（目标）
	for _, cmd := range []string{"ln"} {
		pe.extractors[cmd] = lastPath
	}
	pe.extractors["dd"] = ddOutput
	for _, cmd := range []string{"tar", "zip", "unzip", "curl", "wget", "make"} {
		pe.extractors[cmd] = fileFlag
	}
	pe.extractors["chmod"] = modeThenPaths
	pe.extractors["chown"] = modeThenPaths

	// grep/sed/awk：跳过 pattern/program，提取文件
	pe.extractors["grep"] = func(args []string) []string {
		var paths []string
		afterPattern := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if !afterPattern {
				afterPattern = true
				continue
			}
			paths = append(paths, a)
		}
		return paths
	}
	pe.extractors["sed"] = func(args []string) []string {
		var paths []string
		seenExpr := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if !seenExpr {
				seenExpr = true // 表达式
				continue
			}
			paths = append(paths, a)
		}
		return paths
	}
	pe.extractors["awk"] = func(args []string) []string {
		var paths []string
		seenProgram := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if !seenProgram {
				seenProgram = true
				continue
			}
			paths = append(paths, a)
		}
		return paths
	}

	// find：第一个非 flag 参数（搜索根）
	pe.extractors["find"] = firstPath

	// python/node：脚本文件参数
	scriptFile := func(args []string) []string {
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			// 跳过 -c/-m 的值
			return []string{a}
		}
		return nil
	}
	for _, cmd := range []string{"python", "python3", "node"} {
		pe.extractors[cmd] = scriptFile
	}

	// git：子命令路由
	pe.extractors["git"] = gitPathExtractor
	// npm/yarn/pnpm：通常不含文件路径，返回 nil（保留注册以识别已知命令）
	pe.extractors["npm"] = func(args []string) []string { return nil }
	pe.extractors["yarn"] = func(args []string) []string { return nil }
	pe.extractors["pnpm"] = func(args []string) []string { return nil }
	pe.extractors["go"] = func(args []string) []string { return nil }
}

// Extract 提取命令中所有段涉及的路径（修复复合命令只检查第一段的缺陷）。
func (pe *PathExtractor) Extract(command string) []string {
	var allPaths []string
	for _, segment := range splitSegments(command) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		parts := tokenizeCommand(segment)
		if len(parts) == 0 {
			continue
		}
		cmdName := normalizeCmdName(parts[0])
		extractor, ok := pe.extractors[cmdName]
		if !ok {
			continue
		}
		allPaths = append(allPaths, extractor(parts[1:])...)
	}
	return allPaths
}

// gitPathExtractor git 命令路径提取。
func gitPathExtractor(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "add", "checkout", "rm", "mv", "restore":
		return args[1:]
	case "commit", "push", "pull", "fetch", "merge", "rebase":
		return nil
	default:
		return nil
	}
}

// normalizeCmdName 归一化命令名（小写、去 .exe 后缀、取 basename）。
func normalizeCmdName(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".exe")
	return filepath.Base(name)
}

// splitSegments 按引号感知的方式将命令拆分为多个段。
// 分隔符：| ; &（含 && ||）。引号内的分隔符不拆分。
// 这是 B6 修复的核心：不再只取第一段，而是返回所有段供 Extract 逐一处理。
func splitSegments(cmd string) []string {
	var segments []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == '"' || ch == '\'':
			if inQuote && ch == quoteChar {
				inQuote = false
				quoteChar = 0
				current.WriteByte(ch)
			} else if !inQuote {
				inQuote = true
				quoteChar = ch
				current.WriteByte(ch)
			} else {
				current.WriteByte(ch)
			}
		case (ch == '|' || ch == ';' || ch == '&') && !inQuote:
			segments = append(segments, current.String())
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

// tokenizeCommand 对单个段分词（处理引号），保留引号内容但不把引号作为分隔符。
// 不再遇 |;& 截断（已在 splitSegments 阶段拆分）。
func tokenizeCommand(cmd string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == '"' || ch == '\'':
			if inQuote && ch == quoteChar {
				inQuote = false
				quoteChar = 0
				// 不写入引号本身
			} else if !inQuote {
				inQuote = true
				quoteChar = ch
				// 不写入引号本身
			} else {
				current.WriteByte(ch)
			}
		case (ch == ' ' || ch == '\t') && !inQuote:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
