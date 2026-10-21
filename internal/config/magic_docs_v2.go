package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// InstructionFile represents an instruction file, containing path, type, content and depth
type InstructionFile struct {
	Path    string // Absolute file path
	Type    string // File type identifier (e.g. "whale", "claude", "cursorrules" etc.)
	Content string // File content
	Depth   int    // Depth relative to CWD, 0 means CWD itself
}

// ConditionalRule represents a conditional rule, determines loading based on target file path matching
type ConditionalRule struct {
	Path    string   // Rule file path
	Pattern []string // Match pattern list (from frontmatter paths field)
	Content string   // Rule content
}

// frontmatterData is the data structure for parsing YAML frontmatter
type frontmatterData struct {
	Paths []string `yaml:"paths"` // Applicable file path glob patterns
}

// includeRegex matches @include directive, supports ./ ../ ~/ relative and absolute path references
var includeRegex = regexp.MustCompile(`@([\./~]?[\w./-]+)`)

// readFile 是本文件「读文件内容」的唯一出口。
//
// 为什么抽成变量：判据要能**观测**「缓存是否真的省掉了重复读盘」。
// 若直接内联 os.ReadFile，判据只能靠耗时推断（在 1-10ms 量级上不可判别）。
// 这是 R6b 允许的注入面 —— 抽的是**取值来源**，判定逻辑一行不动。
//
// 生产行为零变化：默认就是 os.ReadFile。
var readFile = os.ReadFile

// ── ：指令文件内容缓存 ────────────────────────────────
//
// 问题：`DiscoverInstructionFiles` 从 CWD 逐层向上扫到根目录，每层读 6 个候选文件
// + `.whale/rules/`；`MatchConditionalRules` / `ResolveIncludes` / `DiscoverNestedMemory`
// 也各自读盘。它被**每次 AI 调用**（含每个工具轮次续轮）调用 ⇒ 同一份目录链被反复读。
//
// 修法：**按文件身份（路径 + mtime + 大小）复用内容**。命中时只 `os.Stat`（微秒级），
// 不再 `os.ReadFile`；文件一改（mtime 或大小变）⇒ 自动失效重读。
//
// 为什么不是"缓存整棵目录链的结果"：逐文件缓存粒度更细 —— 只改了一层里的一个文件时，
// 只重读那一个，其余仍命中；而链级缓存会因为一个文件变化把整条链作废。
//
// 诚实说明（避免夸大）：本项**不是**"卡半天"的主因（实测单次扫盘仅毫秒级，
// 见 magic_docs_cache_test.go 的基线取证）。它是消除"每次 AI 调用重复读同一批文件"
// 的结构性冗余，与主因（记忆选择的同步 LLM 调用）是两个独立的浪费。
//
// 失效粒度说明：靠 mtime + size 判定。同一纳秒内、且长度不变的内容改写不会被察觉
// （APFS mtime 为纳秒级，常规编辑必然变）—— 这与主流构建工具的做法一致，属可接受的折衷。
type fileContentEntry struct {
	mtime time.Time
	size  int64
	body  string
}

var (
	fileContentMu    sync.Mutex
	fileContentCache = map[string]fileContentEntry{}
)

// readFileCached 读文件内容：身份未变则复用缓存，未变时不触碰 readFile。
//
// 语义与 os.ReadFile 一致（返回内容 + 是否成功）。失败也缓存"不存在"这一事实，
// 免得每层对不存在的候选文件反复 stat+open。
func readFileCached(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	mtime := info.ModTime()
	size := info.Size()

	fileContentMu.Lock()
	if e, ok := fileContentCache[path]; ok && e.mtime.Equal(mtime) && e.size == size {
		fileContentMu.Unlock()
		return []byte(e.body), true
	}
	fileContentMu.Unlock()

	data, err := readFile(path)
	if err != nil {
		return nil, false
	}

	fileContentMu.Lock()
	// 读盘期间文件可能又被改：以**读盘前**取的 Stat 为身份即可，
	// 下次调用会因身份不符而重读（宁可多读一次，不可返回陈旧内容）。
	fileContentCache[path] = fileContentEntry{mtime: mtime, size: size, body: string(data)}
	fileContentMu.Unlock()

	return data, true
}

// DiscoverInstructionFiles traverses from CWD up to root directory, discovers instruction files by layer
// Checks at each layer: WHALE.md, WHALE.local.md, .whale/WHALE.md, .whale/instructions.md,
// .whale/rules/ (directory), CLAUDE.md, .cursorrules
// Deduplicates content via SHA256, sorts by depth (closest to CWD has highest priority, loaded last)
func DiscoverInstructionFiles(cwd string) []InstructionFile {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}

	var files []InstructionFile
	seen := make(map[string]bool) // SHA256 hash deduplication

	// Define files to check at each layer and their types
	type checkItem struct {
		relativePath string // Path relative to current layer
		fileType     string // File type identifier
	}

	checks := []checkItem{
		{"WHALE.md", "whale"},
		{"WHALE.local.md", "whale-local"},
		{".whale/WHALE.md", "whale-dot"},
		{".whale/instructions.md", "whale-instructions"},
		{"CLAUDE.md", "claude"},
		{".cursorrules", "cursorrules"},
	}

	current := absCwd
	depth := 0

	for {
		// Check files at current layer
		for _, check := range checks {
			fullPath := filepath.Join(current, check.relativePath)
			data, ok := readFileCached(fullPath)
			if !ok {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			// SHA256 content deduplication
			hash := sha256.Sum256([]byte(content))
			hashStr := hex.EncodeToString(hash[:])
			if seen[hashStr] {
				continue
			}
			seen[hashStr] = true

			absPath, _ := filepath.Abs(fullPath)
			files = append(files, InstructionFile{
				Path:    absPath,
				Type:    check.fileType,
				Content: content,
				Depth:   depth,
			})
		}

		// Check .whale/rules/ directory
		rulesDir := filepath.Join(current, ".whale", "rules")
		loadRulesDir(rulesDir, &files, seen)

		// Go up one directory
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root directory
			break
		}
		current = parent
		depth++
	}

	// Sort by depth: larger depth (farther from CWD) first, smaller depth (closer to CWD) after
	// This way files closest to CWD are loaded last, with highest priority
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Depth > files[j].Depth
	})

	return files
}

// MatchConditionalRules reads .md files from rules directory, parses paths field in YAML frontmatter,
// Uses doublestar for glob matching, returns list of rules matching target file
// If rule file has no paths field, it is treated as always loaded
func MatchConditionalRules(rulesDir string, targetFile string) []ConditionalRule {
	var rules []ConditionalRule

	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		fullPath := filepath.Join(rulesDir, entry.Name())
		data, ok := readFileCached(fullPath)
		if !ok {
			continue
		}

		content := string(data)
		fm, body := parseFrontmatter(content)

		// If no frontmatter or no paths field, always load
		if fm.Paths == nil || len(fm.Paths) == 0 {
			rules = append(rules, ConditionalRule{
				Path:    fullPath,
				Pattern: nil,
				Content: body,
			})
			continue
		}

		// Use doublestar to match any pattern in paths
		matched := false
		for _, pattern := range fm.Paths {
			// doublestar.Match requires forward slashes
			normalizedTarget := filepath.ToSlash(targetFile)
			ok, err := doublestar.Match(pattern, normalizedTarget)
			if err == nil && ok {
				matched = true
				break
			}
		}

		if matched {
			rules = append(rules, ConditionalRule{
				Path:    fullPath,
				Pattern: fm.Paths,
				Content: body,
			})
		}
	}

	return rules
}

// allowedIncludeRoots 返回 @include 允许的根目录白名单（3-C-1）。
// 包含：<baseDir>, <baseDir>/.whale, ~/.whale 三个根。
// baseDir 通常是当前指令文件所在目录；不在白名单内的 @path 引用保留原文本不替换，并记录日志。
// 根目录直接传原始路径，由 platform.IsPathAllowed 内部做规范化（EvalSymlinks+Abs），
// 与 isPathAllowed 保持对称，避免 baseDir 含短名/junction 时根目录与被检查路径形式不一致。
func allowedIncludeRoots(baseDir string) []string {
	var roots []string
	roots = append(roots, baseDir)
	roots = append(roots, filepath.Join(baseDir, ".whale"))
	// 收口：Dir() 返回空（home 不可得）⇒ 不追加，行为同前。
	if whalehome.Dir() != "" {
		roots = append(roots, whalehome.Dir())
	}
	return roots
}

// isPathAllowed 检查路径是否在白名单根目录下（3-C-1）。
// 复用 platform.IsPathAllowed（EvalSymlinks→Abs→前缀检查），
// 必须解析符号链接，否则 symlink 指向白名单外可绕过。
// 路径不存在时 fail-closed（返回 false），不存在的 @include 会在后续 os.ReadFile 失败时保留原文本，无信息泄露。
// 安全性：symlink 指向白名单外时，EvalSymlinks 解析后的真实路径不在白名单前缀内，仍被阻断。
func isPathAllowed(path string, roots []string) bool {
	return platform.IsPathAllowed(path, roots)
}

// ResolveIncludes resolves @include directives in content, recursively replaces with referenced file content
// baseDir is the directory of current file, used to resolve relative paths
// maxDepth limits recursion depth (default max 5), visited prevents circular references
// 3-C-1 修复：在 os.ReadFile 前加路径白名单检查，防止 @/etc/passwd、@~/.ssh/id_rsa、@../../../secret 等路径穿越。
func ResolveIncludes(content string, baseDir string, maxDepth int, visited map[string]bool) string {
	if maxDepth <= 0 {
		return content
	}
	if visited == nil {
		visited = make(map[string]bool)
	}

	// 3-C-1 新增：计算白名单根目录（基于当前 baseDir）
	roots := allowedIncludeRoots(baseDir)

	result := includeRegex.ReplaceAllStringFunc(content, func(match string) string {
		// Extract path part after @
		refPath := match[1:] // Remove @ prefix

		// Resolve reference path
		var resolvedPath string
		if strings.HasPrefix(refPath, "~/") {
			// User home directory relative path
			home, err := os.UserHomeDir()
			if err != nil {
				return match
			}
			resolvedPath = filepath.Join(home, refPath[2:])
		} else if strings.HasPrefix(refPath, "/") {
			// Absolute path
			resolvedPath = refPath
		} else {
			// Relative path (relative to baseDir)
			resolvedPath = filepath.Join(baseDir, refPath)
		}

		// Get absolute path for circular reference detection
		absPath, err := filepath.Abs(resolvedPath)
		if err != nil {
			return match
		}

		// 3-C-1 新增：路径白名单检查，防止路径穿越
		if !isPathAllowed(absPath, roots) {
			log.Printf("[magic_docs] @include 路径不在白名单内，跳过: %s", refPath)
			return match // 保留原 @path 文本不替换
		}

		// Circular reference detection
		if visited[absPath] {
			return fmt.Sprintf("<!-- 循环引用已跳过: %s -->", refPath)
		}
		visited[absPath] = true

		// Read referenced file
		data, ok := readFileCached(absPath)
		if !ok {
			return match
		}

		// Recursively resolve @include in referenced file
		includedContent := string(data)
		includedDir := filepath.Dir(absPath)
		resolved := ResolveIncludes(includedContent, includedDir, maxDepth-1, visited)

		// Clear visited mark, allow other paths to reference same file
		delete(visited, absPath)

		return resolved
	})

	return result
}

// DiscoverNestedMemory discovers nested WHALE.md files between CWD and target file path
// Used to load context instructions in the file path hierarchy when editing a specific file
func DiscoverNestedMemory(cwd string, targetFile string) []InstructionFile {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	absTarget, err := filepath.Abs(targetFile)
	if err != nil {
		return nil
	}

	// Ensure target file path is under its directory
	targetDir := filepath.Dir(absTarget)

	var files []InstructionFile
	seen := make(map[string]bool)

	// Start from CWD, search for WHALE.md along target file path level by level
	current := absCwd
	for {
		whalePath := filepath.Join(current, "WHALE.md")
		data, ok := readFileCached(whalePath)
		if ok {
			content := strings.TrimSpace(string(data))
			if content != "" {
				hash := sha256.Sum256([]byte(content))
				hashStr := hex.EncodeToString(hash[:])
				if !seen[hashStr] {
					seen[hashStr] = true
					depth := pathDepth(absCwd, current)
					absPath, _ := filepath.Abs(whalePath)
					files = append(files, InstructionFile{
						Path:    absPath,
						Type:    "nested-whale",
						Content: content,
						Depth:   depth,
					})
				}
			}
		}

		// If reached target directory or root, stop traversal
		if current == targetDir || current == filepath.Dir(current) {
			break
		}
		current = filepath.Dir(current)
	}

	// Sort by depth: far first, near last (higher priority)
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Depth > files[j].Depth
	})

	return files
}

// parseFrontmatter parses YAML frontmatter in Markdown file
// Returns frontmatter data and content with frontmatter removed
// Frontmatter format is YAML block surrounded by ---
func parseFrontmatter(content string) (frontmatterData, string) {
	var fm frontmatterData

	// Check if starts with ---
	if !strings.HasPrefix(content, "---") {
		return fm, content
	}

	// Find closing ---
	endIdx := strings.Index(content[3:], "---")
	if endIdx < 0 {
		return fm, content
	}

	fmContent := content[3 : endIdx+3]
	body := strings.TrimSpace(content[endIdx+6:])

	// Parse YAML frontmatter
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return fm, content
	}

	return fm, body
}

// loadRulesDir loads all .md files in rules directory into instruction file list
// Use seen map for SHA256 content deduplication
func loadRulesDir(path string, files *[]InstructionFile, seen map[string]bool) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		fullPath := filepath.Join(path, entry.Name())
		data, ok := readFileCached(fullPath)
		if !ok {
			continue
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		// SHA256 content deduplication
		hash := sha256.Sum256([]byte(content))
		hashStr := hex.EncodeToString(hash[:])
		if seen[hashStr] {
			continue
		}
		seen[hashStr] = true

		absPath, _ := filepath.Abs(fullPath)
		*files = append(*files, InstructionFile{
			Path:    absPath,
			Type:    "whale-rules",
			Content: content,
			Depth:   -1, // Rule directory file depth is set by caller
		})
	}
}

// pathDepth calculates dir depth relative to cwd
// If dir is cwd, return 0; depth increases by 1 for each level up
func pathDepth(cwd string, dir string) int {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return 0
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return 0
	}

	depth := 0
	current := absDir
	for {
		if current == absCwd {
			return depth
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root directory without match
			return depth
		}
		current = parent
		depth++
	}
}
