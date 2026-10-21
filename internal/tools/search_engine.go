package tools

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/bmatcuk/doublestar/v4"
	gitignore "github.com/sabhiram/go-gitignore"
)

// skipDirs: directory names to skip (as fallback for .gitignore)
var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "vendor": true,
	"__pycache__": true, ".tox": true,
	".idea": true, ".vscode": true,
	"dist": true, "build": true, "out": true,
	".next": true, ".nuxt": true,
}

// SearchEngine defines the search engine interface with Grep and Glob abstractions
type SearchEngine interface {
	// Grep searches file content with regex
	Grep(input *GrepInput) (*GrepOutput, error)
	// Glob modematchfile path
	Glob(input *GlobInput) (*GlobOutput, error)
}

// NewSearchEngine creates a search engine (prefers ripgrep, falls back to Go native)
func NewSearchEngine(workDir string) SearchEngine {
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return &RipgrepEngine{rgPath: rgPath, workDir: workDir}
	}
	log.Println("ripgrep not found, falling back to Go native search engine; search speed may be slower")
	return &GoSearchEngine{workDir: workDir}
}

// ============================================================================
// data structure definitions
// ============================================================================

// GrepInput is the grep_search tool input (extended version)
type GrepInput struct {
	Pattern         string `json:"pattern" desc:"Regex pattern to search for (required)"`                      // regex pattern (required)
	Path            string `json:"path,omitempty" desc:"Directory to search in (default: cwd)"`                // searchdirectory（default cwd）
	Glob            string `json:"glob,omitempty" desc:"File filter glob (e.g. *.go, src/**/*.ts)"`            // file filter glob (e.g. "*.go", "src/**/*.ts"）
	OutputMode      string `json:"output_mode,omitempty" desc:"Output mode: files_with_matches|content|count"` // outputmode：files_with_matches / content / count（default content）
	CaseInsensitive bool   `json:"case_insensitive,omitempty" desc:"Case insensitive search (-i)"`             // -i ignore case
	ContextLines    int    `json:"context_lines,omitempty" desc:"Context lines around match (-C)"`             // -C context line count
	BeforeContext   int    `json:"before_context,omitempty" desc:"Lines before match (-B)"`                    // -B before context line count
	AfterContext    int    `json:"after_context,omitempty" desc:"Lines after match (-A)"`                      // -A after context line count
	HeadLimit       int    `json:"head_limit,omitempty" desc:"Max results (default: 100)"`                     // max result count (default 100)
	Offset          int    `json:"offset,omitempty" desc:"Skip first N results"`                               // skip first N results
	Multiline       bool   `json:"multiline,omitempty" desc:"Enable multiline mode where . matches newlines"`  // multiline mode
	// Include is a compatible legacy field, mapped to Glob
	Include string `json:"include,omitempty" desc:"Legacy field, mapped to glob"`
}

// GlobInput is the glob_search tool input (extended version)
type GlobInput struct {
	Pattern string `json:"pattern" desc:"Glob pattern (e.g. **/*.go, src/**/*.ts)"`     // glob pattern (required, supports **)
	Path    string `json:"path,omitempty" desc:"Directory to search in (default: cwd)"` // searchdirectory（default cwd）
}

// GrepOutput grep searchoutput
type GrepOutput struct {
	Files []GrepFileMatch `json:"files"`
}

// GrepFileMatch is a file-level match result
type GrepFileMatch struct {
	Path       string          `json:"path"`
	Matches    []GrepLineMatch `json:"matches,omitempty"`
	MatchCount int             `json:"match_count,omitempty"`
}

// GrepLineMatch is a line-level match result
type GrepLineMatch struct {
	LineNumber int    `json:"line_number"`
	Text       string `json:"text"`
	IsContext  bool   `json:"is_context,omitempty"` // context line marker
}

// GlobOutput glob searchoutput
type GlobOutput struct {
	Files []string `json:"files"`
}

// ============================================================================
// Ripgrep engineimplementation
// ============================================================================

// RipgrepEngine uses system-installed ripgrep for search
type RipgrepEngine struct {
	rgPath  string
	workDir string
}

// Grep calls rg to execute regex search
func (e *RipgrepEngine) Grep(input *GrepInput) (*GrepOutput, error) {
	args := []string{
		"--json",
		"--color", "never",
		"-n",
	}

	// add skipDirs fallback exclusion (prevents ripgrep failure due to .gitignore encoding issues)
	args = appendRipgrepSkipDirGlobs(args)

	searchDir := input.Path
	if searchDir == "" {
		searchDir = e.workDir
	}

	// try passing BOM-stripped .gitignore to rg as extra ignore file
	cleanup, err := e.prepareIgnoreFile(searchDir, &args)
	if err == nil && cleanup != nil {
		defer cleanup()
	}

	headLimit := input.HeadLimit
	if headLimit <= 0 {
		headLimit = 100
	}
	if headLimit > 500 {
		headLimit = 500
	}
	args = append(args, "--max-count", fmt.Sprintf("%d", headLimit))

	if input.CaseInsensitive {
		args = append(args, "-i")
	}

	// prefer Glob, compatible with legacy Include field
	globFilter := input.Glob
	if globFilter == "" && input.Include != "" {
		globFilter = input.Include
	}
	if globFilter != "" {
		args = append(args, "--glob", globFilter)
	}

	if input.ContextLines > 0 {
		args = append(args, "-C", fmt.Sprintf("%d", input.ContextLines))
	} else {
		if input.BeforeContext > 0 {
			args = append(args, "-B", fmt.Sprintf("%d", input.BeforeContext))
		}
		if input.AfterContext > 0 {
			args = append(args, "-A", fmt.Sprintf("%d", input.AfterContext))
		}
	}

	if input.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}

	args = append(args, input.Pattern)

	args = append(args, searchDir)

	cmd := exec.Command(e.rgPath, args...)
	// C-P1/S-2：环境变量白名单过滤，剥离密钥变量（API_KEY/TOKEN/SECRET 等）
	cmd.Env = secret.SafeEnv()
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// rg exit code 1 = no match, not an error
			return &GrepOutput{Files: []GrepFileMatch{}}, nil
		}
		return nil, fmt.Errorf("rg executefailed: %w", err)
	}

	return parseRgJSONOutput(output, input)
}

// Glob calls rg to execute file path search
func (e *RipgrepEngine) Glob(input *GlobInput) (*GlobOutput, error) {
	searchDir := input.Path
	if searchDir == "" {
		searchDir = e.workDir
	}

	// rg --files lists all files, then filter with doublestar
	args := []string{"--files", "--color", "never"}

	// add skipDirs fallback exclusion (prevents ripgrep failure due to .gitignore encoding issues)
	args = appendRipgrepSkipDirGlobs(args)

	// try passing BOM-stripped .gitignore to rg as extra ignore file
	cleanup, err := e.prepareIgnoreFile(searchDir, &args)
	if err == nil && cleanup != nil {
		defer cleanup()
	}

	cmd := exec.Command(e.rgPath, args...)
	cmd.Dir = searchDir
	// C-P1/S-2：环境变量白名单过滤，剥离密钥变量（API_KEY/TOKEN/SECRET 等）
	cmd.Env = secret.SafeEnv()
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &GlobOutput{Files: []string{}}, nil
		}
		return nil, fmt.Errorf("rg --files executefailed: %w", err)
	}

	var matches []string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		matched, err := doublestar.Match(input.Pattern, line)
		if err == nil && matched {
			matches = append(matches, filepath.Join(searchDir, line))
		}
	}

	sort.Strings(matches)
	if len(matches) > 200 {
		matches = matches[:200]
	}

	return &GlobOutput{Files: matches}, nil
}

// parseRgJSONOutput resolve rg --json output
func parseRgJSONOutput(data []byte, input *GrepInput) (*GrepOutput, error) {
	outputMode := input.OutputMode
	if outputMode == "" {
		outputMode = "content"
	}

	fileMap := make(map[string]*GrepFileMatch)
	var fileOrder []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg rgMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "match":
			path := msg.Data.Path.Text
			if _, ok := fileMap[path]; !ok {
				fileMap[path] = &GrepFileMatch{Path: path}
				fileOrder = append(fileOrder, path)
			}
			ln, _ := msg.Data.LineNumber.Int64()
			fileMap[path].Matches = append(fileMap[path].Matches, GrepLineMatch{
				LineNumber: int(ln),
				Text:       msg.Data.Lines.Text,
			})
			fileMap[path].MatchCount++

		case "context":
			if outputMode != "content" {
				continue
			}
			path := msg.Data.Path.Text
			if _, ok := fileMap[path]; !ok {
				fileMap[path] = &GrepFileMatch{Path: path}
				fileOrder = append(fileOrder, path)
			}
			ln, _ := msg.Data.LineNumber.Int64()
			fileMap[path].Matches = append(fileMap[path].Matches, GrepLineMatch{
				LineNumber: int(ln),
				Text:       msg.Data.Lines.Text,
				IsContext:  true,
			})
		}
	}

	var files []GrepFileMatch
	totalMatches := 0
	for _, path := range fileOrder {
		fm := fileMap[path]
		switch outputMode {
		case "files_with_matches":
			files = append(files, GrepFileMatch{Path: fm.Path, MatchCount: fm.MatchCount})
		case "count":
			files = append(files, GrepFileMatch{Path: fm.Path, MatchCount: fm.MatchCount})
		default:
			files = append(files, *fm)
		}
		totalMatches += fm.MatchCount
		if input.HeadLimit > 0 && totalMatches >= input.HeadLimit {
			break
		}
	}

	// process offset
	if input.Offset > 0 && outputMode == "content" {
		for i := range files {
			if input.Offset >= len(files[i].Matches) {
				input.Offset -= len(files[i].Matches)
				files[i].Matches = nil
			} else {
				files[i].Matches = files[i].Matches[input.Offset:]
				input.Offset = 0
			}
		}
		// remove empty files
		var filtered []GrepFileMatch
		for _, f := range files {
			if len(f.Matches) > 0 {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}

	return &GrepOutput{Files: files}, nil
}

// rgMessage is the ripgrep JSON output message structure
type rgMessage struct {
	Type string        `json:"type"`
	Data rgMessageData `json:"data"`
}

// rgMessageData ripgrep JSON messagedata
type rgMessageData struct {
	Path       rgPath      `json:"path"`
	Lines      rgText      `json:"lines"`
	LineNumber json.Number `json:"line_number"`
}

// rgText is the ripgrep JSON text field
type rgText struct {
	Text string `json:"text"`
}

// rgPath is the ripgrep JSON path field (compatible with text and bytes formats)
type rgPath struct {
	Text string `json:"text"`
}

// prepareIgnoreFile reads .gitignore, strips BOM, writes to temp file，
// and passes temp file path to rg via --ignore-file, as fallback for .gitignore encoding issues
// returns cleanup function (deletes temp file) and possible error
func (e *RipgrepEngine) prepareIgnoreFile(searchDir string, args *[]string) (func(), error) {
	gitignorePath := filepath.Join(searchDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		// .gitignore does not exist, no processing needed
		return nil, err
	}

	// check if BOM stripping is needed
	stripped := stripBOM(data)
	if string(stripped) == string(data) {
		// no BOM, no need to create temp file, rg will auto-read .gitignore
		return nil, nil
	}

	// has BOM, need to create temp file for rg
	tmpFile, err := os.CreateTemp("", "whale-gitignore-*")
	if err != nil {
		return nil, err
	}

	if _, err := tmpFile.Write(stripped); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, err
	}
	tmpFile.Close()

	*args = append(*args, "--ignore-file", tmpFile.Name())
	return func() { os.Remove(tmpFile.Name()) }, nil
}

// ============================================================================
// Go native search engine (fallback)
// ============================================================================

// GoSearchEngine is the Go native search engine (fallback when rg is unavailable)
type GoSearchEngine struct {
	workDir string
}

// appendRipgrepSkipDirGlobs appends skipDirs as --glob exclusion patterns to rg parameters
// as fallback for .gitignore, prevents encoding issues from breaking ripgrep resolution
func appendRipgrepSkipDirGlobs(args []string) []string {
	for dir := range skipDirs {
		args = append(args, "--glob", "!"+dir+"/")
	}
	return args
}

// stripBOM 委托 util.DecodeToUTF8（同时完成 BOM 识别 + 转码）。
// 失败时返回原始字节（与旧行为一致，避免引入新错误路径）。
func stripBOM(data []byte) []byte {
	s, err := util.DecodeToUTF8(data)
	if err != nil {
		return data
	}
	return []byte(s)
}

// gitignoreMatcher is a .gitignore matcher
type gitignoreMatcher struct {
	ignores []gitignore.IgnoreParser
}

// newGitignoreMatcher loads all .gitignore files along the directory chain (traverses from root upward)
// auto-handles UTF-8 BOM and UTF-16 LE/BE encoding, ensures go-gitignore library can correctly resolve
func newGitignoreMatcher(root string) (*gitignoreMatcher, error) {
	var ignores []gitignore.IgnoreParser

	// traverse from root upward, loading .gitignore at each level
	dir := root
	for {
		gitignorePath := filepath.Join(dir, ".gitignore")
		if parser, err := compileGitignoreFile(gitignorePath); err == nil {
			ignores = append(ignores, parser)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return &gitignoreMatcher{ignores: ignores}, nil
}

// compileGitignoreFile compiles .gitignore file, auto-handles BOM and encoding issues
func compileGitignoreFile(path string) (gitignore.IgnoreParser, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// strip BOM and handle encoding
	data = stripBOM(data)
	lines := strings.Split(string(data), "\n")
	return gitignore.CompileIgnoreLines(lines...), nil
}

// loadSubDirGitignore loads subdirectory .gitignore and appends to matcher
func (m *gitignoreMatcher) loadSubDirGitignore(dir string) {
	gitignorePath := filepath.Join(dir, ".gitignore")
	if parser, err := compileGitignoreFile(gitignorePath); err == nil {
		m.ignores = append(m.ignores, parser)
	}
}

// ShouldSkip determines if a path should be skipped
func (m *gitignoreMatcher) ShouldSkip(path string, isDir bool) bool {
	// convert to path relative to working directory for matching
	for _, parser := range m.ignores {
		if parser.MatchesPath(path) {
			return true
		}
	}
	return false
}

// Grep executes Go native regex search
func (e *GoSearchEngine) Grep(input *GrepInput) (*GrepOutput, error) {
	outputMode := input.OutputMode
	if outputMode == "" {
		outputMode = "content"
	}

	headLimit := input.HeadLimit
	if headLimit <= 0 {
		headLimit = 100
	}
	if headLimit > 500 {
		headLimit = 500
	}

	const maxPatternLen = 1000
	if len(input.Pattern) > maxPatternLen {
		return nil, fmt.Errorf("regex pattern too long (max %d characters)", maxPatternLen)
	}

	// Check for ReDoS patterns: nested quantifiers like (a+)+ that can cause catastrophic backtracking
	if containsReDoSrisk(input.Pattern) {
		return nil, fmt.Errorf("regex pattern contains potentially unsafe nested quantifiers")
	}

	pattern := input.Pattern
	if input.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	if input.Multiline {
		pattern = "(?s)" + pattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}

	searchDir := input.Path
	if searchDir == "" {
		searchDir = e.workDir
	}

	// load .gitignore
	matcher, _ := newGitignoreMatcher(searchDir)

	var files []GrepFileMatch
	totalMatches := 0

	err = filepath.WalkDir(searchDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, relErr := filepath.Rel(searchDir, path)
		if relErr != nil {
			rel = path
		}

		if d.IsDir() {
			// check .gitignore
			if matcher != nil && matcher.ShouldSkip(rel, true) {
				return filepath.SkipDir
			}
			// preserve hardcoded skip as fallback
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			// dynamically load subdirectory .gitignore
			if matcher != nil {
				matcher.loadSubDirGitignore(path)
			}
			return nil
		}

		// check .gitignore
		if matcher != nil && matcher.ShouldSkip(rel, false) {
			return nil
		}

		// glob filter
		globFilter := input.Glob
		if globFilter == "" && input.Include != "" {
			globFilter = input.Include
		}
		if globFilter != "" {
			if !matchGlobPattern(globFilter, rel) {
				return nil
			}
		}

		// binaryfiledetect
		if isBinaryFileCached(path) {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		content := string(data)
		var matches []GrepLineMatch
		fileMatchCount := 0

		if input.Multiline {
			// multiline mode: match against entire file content
			contentStr := content
			locs := re.FindAllStringIndex(contentStr, headLimit+input.Offset-totalMatches)
			for _, loc := range locs {
				fileMatchCount++
				if outputMode == "content" {
					// calculate line number of match start position
					lineNum := strings.Count(contentStr[:loc[0]], "\n") + 1
					matchedText := contentStr[loc[0]:loc[1]]
					trimmed := strings.TrimSpace(matchedText)
					if len(trimmed) > 200 {
						trimmed = trimmed[:200] + "..."
					}
					matches = append(matches, GrepLineMatch{
						LineNumber: lineNum,
						Text:       trimmed,
					})
				}
			}
		} else {
			// line-by-line match mode
			lines := strings.Split(content, "\n")
			for i, line := range lines {
				if totalMatches+fileMatchCount >= headLimit+input.Offset {
					break
				}
				if re.MatchString(line) {
					// in count mode, count in-line match occurrences
					if outputMode == "count" {
						countMatches := re.FindAllString(line, -1)
						fileMatchCount += len(countMatches)
					} else {
						fileMatchCount++
						matches = append(matches, GrepLineMatch{
							LineNumber: i + 1,
							Text:       strings.TrimSpace(line),
						})
					}
				}
			}
		}

		if fileMatchCount == 0 {
			return nil
		}

		totalMatches += fileMatchCount

		switch outputMode {
		case "files_with_matches":
			files = append(files, GrepFileMatch{Path: rel, MatchCount: fileMatchCount})
		case "count":
			files = append(files, GrepFileMatch{Path: rel, MatchCount: fileMatchCount})
		default: // content
			// add context lines
			if input.ContextLines > 0 || input.BeforeContext > 0 || input.AfterContext > 0 {
				lines := strings.Split(content, "\n")
				matches = addContextLines(lines, matches, input)
			}
			// application offset
			if input.Offset > 0 && input.Offset < len(matches) {
				matches = matches[input.Offset:]
			} else if input.Offset > 0 {
				matches = nil
			}
			// truncate each line's text
			for i := range matches {
				if len(matches[i].Text) > 200 {
					matches[i].Text = matches[i].Text[:200] + "..."
				}
			}
			files = append(files, GrepFileMatch{Path: rel, Matches: matches, MatchCount: fileMatchCount})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("searchfailed: %w", err)
	}

	return &GrepOutput{Files: files}, nil
}

// addContextLines adds context lines for match lines
func addContextLines(lines []string, matches []GrepLineMatch, input *GrepInput) []GrepLineMatch {
	before := input.BeforeContext
	after := input.AfterContext
	if input.ContextLines > 0 {
		before = input.ContextLines
		after = input.ContextLines
	}

	// collect line numbers to include
	lineSet := make(map[int]bool)
	for _, m := range matches {
		for i := m.LineNumber - before; i <= m.LineNumber+after; i++ {
			if i >= 1 && i <= len(lines) {
				lineSet[i] = true
			}
		}
	}

	// build full results including context
	var result []GrepLineMatch
	for lineNum := 1; lineNum <= len(lines); lineNum++ {
		if !lineSet[lineNum] {
			continue
		}
		isMatch := false
		for _, m := range matches {
			if m.LineNumber == lineNum {
				isMatch = true
				break
			}
		}
		result = append(result, GrepLineMatch{
			LineNumber: lineNum,
			Text:       strings.TrimSpace(lines[lineNum-1]),
			IsContext:  !isMatch,
		})
	}

	return result
}

// Glob executes Go native glob search
func (e *GoSearchEngine) Glob(input *GlobInput) (*GlobOutput, error) {
	searchDir := input.Path
	if searchDir == "" {
		searchDir = e.workDir
	}

	// load .gitignore
	matcher, _ := newGitignoreMatcher(searchDir)

	matches, err := globWithDoubleStar(searchDir, input.Pattern, matcher)
	if err != nil {
		return nil, fmt.Errorf("searchfailed: %w", err)
	}

	sort.Strings(matches)
	if len(matches) > 200 {
		matches = matches[:200]
	}

	return &GlobOutput{Files: matches}, nil
}

// globWithDoubleStar uses doublestar library for correct ** matching
func globWithDoubleStar(root, pattern string, matcher *gitignoreMatcher) ([]string, error) {
	var matches []string

	err := doublestar.GlobWalk(os.DirFS(root), pattern, func(path string, d fs.DirEntry) error {
		if d.IsDir() {
			// check skipDirs fallback
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}

		// check if path contains skipDirs directory (compatible with / and \ separators)
		normalizedPath := strings.ReplaceAll(path, "\\", "/")
		parts := strings.Split(normalizedPath, "/")
		for _, part := range parts {
			if skipDirs[part] {
				return nil
			}
		}

		// check .gitignore
		if matcher != nil && matcher.ShouldSkip(path, false) {
			return nil
		}

		// checkbinaryfile
		fullPath := filepath.Join(root, path)
		if isBinaryFileCached(fullPath) {
			return nil
		}

		matches = append(matches, fullPath)
		return nil
	})

	return matches, err
}

// ============================================================================
// binary file detection (enhanced)
// ============================================================================
//
// 注：规范的 IsBinaryFile / IsBinaryExt / BinaryExts / isBinaryFileCached
// 实现已迁移至 binary_detect.go（T7 统一实现）。本文件仅通过包内别名
// isBinaryFile / isBinaryFileCached 调用，无需重复定义。

// ============================================================================
// searchresultbudgetcontrol
// ============================================================================

// SearchResultBudget searchresultbudgetconfig
type SearchResultBudget struct {
	MaxGrepLines   int    // max lines returned per Grep call (default 100)
	MaxResultChars int    // max characters per search result (default 30KB)
	TruncationHint string // prompt text when limit exceeded
}

// DefaultSearchResultBudget defaultsearchresultbudget
func DefaultSearchResultBudget() *SearchResultBudget {
	return &SearchResultBudget{
		MaxGrepLines:   100,
		MaxResultChars: 30 * 1024,
		TruncationHint: "\n[Results truncated. Use head_limit/offset to see more, or narrow your search pattern.]",
	}
}

// TruncateOutput truncatesearchoutput
func (b *SearchResultBudget) TruncateOutput(output string) string {
	// first check line count
	lines := strings.Split(output, "\n")
	if len(lines) > b.MaxGrepLines {
		return strings.Join(lines[:b.MaxGrepLines], "\n") + b.TruncationHint
	}

	// then check character count
	if len(output) > b.MaxResultChars {
		return output[:b.MaxResultChars] + b.TruncationHint
	}

	return output
}

// ============================================================================
// searchcontexttracing
// ============================================================================

// SearchContext is the search context, tracking the current session's search history
type SearchContext struct {
	mu             sync.RWMutex
	SearchPatterns []string          // previously searched patterns
	ReadFiles      []string          // previously read file paths
	DiscoveredDefs map[string]string // symbol name -> file path
}

// NewSearchContext createsearchcontext
func NewSearchContext() *SearchContext {
	return &SearchContext{
		DiscoveredDefs: make(map[string]string),
	}
}

// RecordSearch records a search
const maxSearchContextEntries = 100

func (sc *SearchContext) RecordSearch(pattern string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.SearchPatterns = append(sc.SearchPatterns, pattern)
	if len(sc.SearchPatterns) > maxSearchContextEntries {
		sc.SearchPatterns = sc.SearchPatterns[len(sc.SearchPatterns)-maxSearchContextEntries:]
	}
}

// RecordRead records a file read
func (sc *SearchContext) RecordRead(path string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.ReadFiles = append(sc.ReadFiles, path)
	if len(sc.ReadFiles) > maxSearchContextEntries {
		sc.ReadFiles = sc.ReadFiles[len(sc.ReadFiles)-maxSearchContextEntries:]
	}
}

// HasSearched checks if a pattern has been searched
func (sc *SearchContext) HasSearched(pattern string) bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	for _, p := range sc.SearchPatterns {
		if p == pattern {
			return true
		}
	}
	return false
}

// ContextForPrompt generates search context info for system prompt
func (sc *SearchContext) ContextForPrompt() string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if len(sc.SearchPatterns) == 0 && len(sc.ReadFiles) == 0 {
		return ""
	}

	var parts []string
	if len(sc.SearchPatterns) > 0 {
		parts = append(parts, "Searched patterns:")
		// show at most recent 20
		start := 0
		if len(sc.SearchPatterns) > 20 {
			start = len(sc.SearchPatterns) - 20
		}
		for _, p := range sc.SearchPatterns[start:] {
			parts = append(parts, fmt.Sprintf("  - %s", p))
		}
	}
	if len(sc.ReadFiles) > 0 {
		parts = append(parts, "Read files:")
		start := 0
		if len(sc.ReadFiles) > 20 {
			start = len(sc.ReadFiles) - 20
		}
		for _, f := range sc.ReadFiles[start:] {
			parts = append(parts, fmt.Sprintf("  - %s", f))
		}
	}

	return "=== searchcontext ===\n" + strings.Join(parts, "\n")
}

// LastReadFile safely fetches the most recently read file path
func (sc *SearchContext) LastReadFile() string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if len(sc.ReadFiles) == 0 {
		return ""
	}
	return sc.ReadFiles[len(sc.ReadFiles)-1]
}

// GetReadFiles safely fetches a copy of all read file paths
func (sc *SearchContext) GetReadFiles() []string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	result := make([]string, len(sc.ReadFiles))
	copy(result, sc.ReadFiles)
	return result
}

// ============================================================================
// search strategyprompt
// ============================================================================

// SearchStrategyPrompt is the search strategy prompt text
const SearchStrategyPrompt = `
## Search Strategy

When you need to find information in the codebase, use an iterative search approach:

1. **Start broad** — Use grep_search with output_mode=files_with_matches to locate candidate files
2. **Narrow down** — Use grep_search with output_mode=content and context_lines to inspect matches
3. **Read key files** — Use read_file to examine the most relevant files in detail
4. **Trace references** — Use LspReferences or grep_search to find callers and dependencies
5. **Avoid re-searching** — Check your previous search results before repeating a query

Tips:
- Prefer specific patterns: getUserById is better than user
- Use glob filters to narrow scope: search in src/ not the entire project
- If a search returns too many results, add more specific patterns or glob filters
- If a search returns nothing, try broader patterns or case_insensitive=true
`

// ============================================================================
// helper functions
// ============================================================================

// matchGlobPattern matches a glob pattern, supporting ! prefix negation and comma-separated multiple patterns
// e.g.: "!*_test.go" excludes test files, "*.go,!*_test.go" includes .go but excludes tests
func matchGlobPattern(pattern string, path string) bool {
	// support comma-separated multiple patterns
	patterns := strings.Split(pattern, ",")
	includePatterns := []string{}
	excludePatterns := []string{}

	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "!") {
			excludePatterns = append(excludePatterns, p[1:])
		} else {
			includePatterns = append(includePatterns, p)
		}
	}

	// if no include pattern, default match all
	if len(includePatterns) > 0 {
		matched := false
		for _, p := range includePatterns {
			if m, _ := doublestar.Match(p, path); m {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// check exclusion patterns
	for _, p := range excludePatterns {
		if m, _ := doublestar.Match(p, path); m {
			return false
		}
	}

	return true
}

// resolveSearchPath 把搜索结果的路径还原成「下游可直接使用」的路径。
//
// 为什么不再用 filepath.Rel：相对路径把搜索根丢掉了 —— 模型只拿到 `t10.txt`
// 时无从判断它在哪个目录下，只能**猜**一个绝对路径。真机 T-11 轨迹即为实例：
// 先猜 `<work>/out/t10.txt` 报 ENOENT、再猜 `<work>/t10.txt` 才对，白费一轮工具调用。
// 工具内部本来就持有绝对路径（引擎侧 filepath.Join(searchDir, …)），
// 输出面不该把它丢掉。
//
// 契约：p 已是绝对路径 ⇒ 原样返回（生产默认形态：searchDir = workDir）；
// p 为相对路径 ⇒ 相对 searchDir 拼接（保住子路径，且**不会**凭空造出一个
// 相对进程 CWD 解析的错误绝对路径 —— 这是刻意不用 filepath.Abs 的原因）。
func resolveSearchPath(searchDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(searchDir, p)
}

// formatGrepOutput formats Grep output as readable text
// countWord 返回「计数 + 单复数词」整段（W-046b 文案收口，2026-10-08）：
// countWord(1, "file") = "1 file"，countWord(3, "file") = "3 files"。
// 服务本文件两处 "Found …" 输出；github/tui 的 "Found %d PRs:" 等列表文案是
// 同族候选但属其它包，未收口。新增计数文案请走本助手，
// 静态门见 line_count_test.go 的 TestW046_SearchCopyGate_NoUnguardedPluralFormat。
func countWord(n int, word string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func formatGrepOutput(output *GrepOutput, searchDir string) string {
	if len(output.Files) == 0 {
		return "no matching content found"
	}

	var sb strings.Builder
	totalFiles := len(output.Files)
	totalMatches := 0
	for _, f := range output.Files {
		totalMatches += f.MatchCount
		if f.MatchCount == 0 && len(f.Matches) > 0 {
			totalMatches += len(f.Matches)
		}
	}

	sb.WriteString(fmt.Sprintf("Found %s in %s:\n", countWord(totalMatches, "match"), countWord(totalFiles, "file")))
	seenLines := make(map[string]bool)
	for _, f := range output.Files {
		rel := resolveSearchPath(searchDir, f.Path)

		if len(f.Matches) == 0 && f.MatchCount == 0 {
			sb.WriteString(fmt.Sprintf("  %s\n", rel))
		} else if f.MatchCount > 0 && len(f.Matches) == 0 {
			sb.WriteString(fmt.Sprintf("  %s: %d\n", rel, f.MatchCount))
		} else {
			for _, m := range f.Matches {
				dedupKey := fmt.Sprintf("%s:%d", rel, m.LineNumber)
				if seenLines[dedupKey] {
					continue
				}
				seenLines[dedupKey] = true
				prefix := "  "
				if m.IsContext {
					prefix = "  -"
				}
				text := m.Text
				if len(text) > 200 {
					text = text[:200] + "..."
				}
				sb.WriteString(fmt.Sprintf("%s%s:%d: %s\n", prefix, rel, m.LineNumber, text))
			}
		}
	}
	return sb.String()
}

// formatGlobOutput formats Glob output as readable text
func formatGlobOutput(output *GlobOutput, searchDir string) string {
	if len(output.Files) == 0 {
		return "no matching files found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %s:\n", countWord(len(output.Files), "file")))
	for _, m := range output.Files {
		rel := resolveSearchPath(searchDir, m)
		sb.WriteString(fmt.Sprintf("  %s\n", rel))
	}
	return sb.String()
}

// containsReDoSrisk checks if a regex pattern contains potentially unsafe nested quantifiers
// that could cause catastrophic backtracking (e.g., (a+)+, (a*)*, (a+)+b)
// Pre-compiled package-level regexes to avoid re-compilation on every call.
var (
	reNestedQuantifier = regexp.MustCompile(`\([^)]*[\+\*][^)]*\)[\+\*]`)
	reAltQuantifier    = regexp.MustCompile(`\([^)]*\|[^)]*\)[\+\*]`)
)

func containsReDoSrisk(pattern string) bool {
	// Check for nested quantifier patterns like (X+)+, (X*)*, (X+)* etc.
	if reNestedQuantifier.MatchString(pattern) {
		return true
	}
	// Check for alternation with overlapping suffixes (e.g., (a|aa)+)
	if reAltQuantifier.MatchString(pattern) {
		return true
	}
	return false
}
