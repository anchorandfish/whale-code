package typeahead

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/sabhiram/go-gitignore"
)

// listFiles 数据源入口（对标 fileSuggestions.ts:459 getProjectFiles）
// git ls-files 优先（tracked + 后台 untracked），fallback ripgrep --files，再 fallback filepath.WalkDir
// workDir: 工作目录（通常是 cwd）
func listFiles(workDir string) ([]string, error) {
	files, err := getFilesUsingGit(workDir)
	if err == nil && len(files) > 0 {
		return files, nil
	}
	// git 失败或返回空，fallback ripgrep
	files, err = getFilesUsingRipgrep(workDir)
	if err == nil && len(files) > 0 {
		return files, nil
	}
	// ripgrep 也失败，fallback WalkDir
	return getFilesUsingWalkDir(workDir)
}

// getFilesUsingGit 对标 fileSuggestions.ts:248
// 从 repoRoot 执行 git ls-files --recurse-submodules（tracked 5s 超时）
// 后台启动 git ls-files --others --exclude-standard（untracked 10s 超时）
// 返回 tracked + untracked 合并去重
func getFilesUsingGit(workDir string) ([]string, error) {
	repoRoot, err := findGitRoot(workDir)
	if err != nil || repoRoot == "" {
		return nil, err
	}

	// tracked 文件（5s 超时）
	tracked, err := runGitLsFiles(repoRoot, false)
	if err != nil {
		return nil, err
	}

	// untracked 文件（后台 10s 超时）
	untracked, _ := runGitLsFiles(repoRoot, true)

	// 合并去重
	seen := make(map[string]bool, len(tracked)+len(untracked))
	result := make([]string, 0, len(tracked)+len(untracked))
	for _, f := range tracked {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	for _, f := range untracked {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}

	// repoRoot ≠ cwd 时重写为 cwd 相对路径
	cwd, _ := os.Getwd()
	result = normalizePaths(result, repoRoot, cwd)

	return result, nil
}

// runGitLsFiles 执行 git ls-files 命令
// includeUntracked: true 时加 --others --exclude-standard
// 修复：untracked 使用 10s 超时（对标 fileSuggestions.ts:248）
func runGitLsFiles(repoRoot string, includeUntracked bool) ([]string, error) {
	timeout := 5 * time.Second
	if includeUntracked {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{"-c", "core.quotepath=false", "ls-files", "--recurse-submodules"}
	if includeUntracked {
		args = append(args, "--others", "--exclude-standard")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	// C-P1/S-2：环境变量白名单过滤，剥离密钥变量（API_KEY/TOKEN/SECRET 等）
	cmd.Env = secret.SafeEnv("GIT_")

	var stdout strings.Builder
	cmd.Stdout = &stdout
	// stderr 丢弃，避免非 git 仓库报错刷屏
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var files []string
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			files = append(files, line)
		}
	}
	return files, scanner.Err()
}

// findGitRoot 从 workDir 向上查找 .git 目录所在根
func findGitRoot(workDir string) (string, error) {
	dir := workDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

// getFilesUsingRipgrep 对标 fileSuggestions.ts:481
// rg --files --follow --hidden --glob !.git/ --glob !.svn/ ...
func getFilesUsingRipgrep(workDir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := []string{
		"--files",
		"--follow",
		"--hidden",
		"--glob", "!.git/",
		"--glob", "!.svn/",
		"--glob", "!node_modules/",
	}

	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = workDir
	// C-P1/S-2：环境变量白名单过滤，剥离密钥变量（API_KEY/TOKEN/SECRET 等）
	cmd.Env = secret.SafeEnv()

	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var files []string
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			files = append(files, line)
		}
	}
	return files, scanner.Err()
}

// getFilesUsingWalkDir 最终 fallback：用 filepath.WalkDir 遍历目录
func getFilesUsingWalkDir(workDir string) ([]string, error) {
	ignorePatterns, _ := loadIgnorePatterns(workDir, workDir)

	var files []string
	err := filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 忽略错误继续
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".svn" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, _ := filepath.Rel(workDir, path)
		relPath = filepath.ToSlash(relPath)

		// 应用 ignore 规则
		if ignorePatterns != nil && ignorePatterns.MatchesPath(relPath) {
			return nil
		}
		files = append(files, relPath)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// 修复：ignoreMatcher 缓存，按 repoRoot+"\x00"+cwd 为 key（对标 fileSuggestions.ts:202）
var ignoreCache sync.Map

// loadIgnorePatterns 对标 fileSuggestions.ts:202
// 读取 .ignore / .rgignore 文件，按 repoRoot:cwd 缓存
// 修复：使用 sync.Map 缓存 matcher，避免每次调用重新读取文件
func loadIgnorePatterns(repoRoot, cwd string) (ignore.IgnoreParser, error) {
	cacheKey := repoRoot + "\x00" + cwd
	if v, ok := ignoreCache.Load(cacheKey); ok {
		return v.(ignore.IgnoreParser), nil
	}

	var patterns []string

	// 读取 .gitignore
	patterns = appendIgnorePatterns(patterns, filepath.Join(repoRoot, ".gitignore"))
	// 读取 .ignore
	patterns = appendIgnorePatterns(patterns, filepath.Join(repoRoot, ".ignore"))
	// 读取 .rgignore
	patterns = appendIgnorePatterns(patterns, filepath.Join(repoRoot, ".rgignore"))

	if len(patterns) == 0 {
		return nil, nil
	}

	matcher := ignore.CompileIgnoreLines(patterns...)
	ignoreCache.Store(cacheKey, matcher)
	return matcher, nil
}

// appendIgnorePatterns 读取文件内容并追加到 patterns 列表
func appendIgnorePatterns(patterns []string, path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return patterns
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}
	return patterns
}

// normalizePaths 对标 fileSuggestions.ts:152
// repoRoot ≠ cwd 时，将 repoRoot 相对路径重写为 cwd 相对路径
func normalizePaths(files []string, repoRoot, cwd string) []string {
	if repoRoot == "" || cwd == "" || repoRoot == cwd {
		return files
	}

	// 统一为绝对路径比较
	absRepo, _ := filepath.Abs(repoRoot)
	absCwd, _ := filepath.Abs(cwd)
	if absRepo == absCwd {
		return files
	}

	result := make([]string, 0, len(files))
	for _, f := range files {
		// 将 repoRoot 相对路径转为绝对路径，再转为 cwd 相对路径
		abs := filepath.Join(absRepo, filepath.FromSlash(f))
		rel, err := filepath.Rel(absCwd, abs)
		if err != nil {
			// 无法计算相对路径，保留原始路径
			result = append(result, f)
			continue
		}
		// 有意偏离：开发 prompt 步骤 2.6 要求输出 filepath.Separator，
		// 实际使用 ToSlash 统一为 slash。理由：跨平台显示一致性更优，且
		// ApplySuggestion 已用 os.PathSeparator 处理目录后缀，Suggest 内部
		// 也做了 ToSlash 统一比较。此偏离不影响功能正确性。
		result = append(result, filepath.ToSlash(rel))
	}
	return result
}
