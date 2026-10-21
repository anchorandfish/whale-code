package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// pathTraversalRe detects ".." as a path component (not inside filenames like "my..file.txt")
var pathTraversalRe = regexp.MustCompile(`(?:^|[\\/])\.\.(?:[\\/]|$)`)

// SafetyCheckResult safety path check result
type SafetyCheckResult struct {
	IsProtected          bool   // whether path is protected
	Reason               string // reason for protection
	ClassifierApprovable bool   // whether AI classifier can bypass (always false)
}

// ProtectedPaths protected directory and file list
// These paths need extra confirmation under any permission mode
var ProtectedPaths = []string{
	".git",
	".claude",
	".whale",
	".vscode",
	".env",
	".env.local",
	".env.development",
	".env.production",
	".env.staging",
	"id_rsa",
	"id_ed25519",
	"id_ecdsa",
	"id_dsa",
	"credentials",
	"credentials.json",
	"secrets",
	"secrets.yaml",
	"secrets.yml",
	".npmrc",
	".pypirc",
	"config.toml",
}

// ProtectedSystemPaths system-level protected paths
var ProtectedSystemPaths = []string{
	"~/.ssh/config",
	"~/.ssh/authorized_keys",
	"~/.ssh/known_hosts",
	"~/.gitconfig",
	"~/.netrc",
	"~/.aws/credentials",
	"~/.aws/config",
	"/etc/passwd",
	"/etc/shadow",
	"/etc/hosts",
	"/etc/hostname",
}

// CheckPathSafety check if specified path is protected
// returns nil if path is not protected
func CheckPathSafety(filePath, workDir string) *SafetyCheckResult {
	if filePath == "" {
		return nil
	}

	// normalize path
	absPath := filePath
	if !filepath.IsAbs(filePath) && workDir != "" {
		absPath = filepath.Join(workDir, filePath)
	}

	// clean path to avoid .. confusion
	cleanPath := filepath.Clean(absPath)

	// Resolve symlinks to prevent bypass via symbolic links. A new file does
	// not exist yet, so resolving the full path fails even when its parent is a
	// symlinked work directory. Resolve the deepest existing parent in that
	// case; preserve the stricter unresolved-symlink warning for an existing
	// symlink whose target cannot be resolved.
	resolvedPath, err := filepath.EvalSymlinks(cleanPath)
	if err == nil {
		cleanPath = resolvedPath
	} else if _, statErr := os.Lstat(cleanPath); os.IsNotExist(statErr) {
		if parentResolved, parentErr := resolveExistingParent(cleanPath); parentErr == nil {
			cleanPath = parentResolved
			err = nil
		}
	}

	// workDir 规范化：收敛到 platform.CanonicalizeRoot（SSOT），与包含判定对称。
	// resolved 语义保持原样（= EvalSymlinks 成功），因为下面的 dangling-symlink 分支依赖该区分。
	resolvedWorkDir := workDir
	workDirResolveOk := false
	if workDir != "" {
		if wd, resolved := platform.CanonicalizeRoot(workDir); wd != "" {
			resolvedWorkDir = wd
			workDirResolveOk = resolved
		}
	}

	// If filePath symlink resolution failed and workDir is set,
	// the file may not exist yet (e.g., a new file being created),
	// or it could be a symlink pointing outside workdir.
	// If workDir resolved successfully but filePath did not,
	// we still check path traversal using cleanPath against resolvedWorkDir below.
	// If both failed to resolve, we cannot reliably check traversal,
	// so we rely on the path traversal regex and cleanPath comparison only.
	//
	// Additional safety: if filePath resolution failed but workDir succeeded,
	// check if cleanPath (unresolved) escapes the workdir. This catches
	// dangling symlinks that point outside the workdir.
	if err != nil && workDirResolveOk {
		if !platform.IsWithinResolved(filepath.Clean(cleanPath), resolvedWorkDir) {
			return &SafetyCheckResult{
				IsProtected:          true,
				Reason:               fmt.Sprintf("path %q may escape work directory (unresolved symlink)", filePath),
				ClassifierApprovable: false,
			}
		}
	}

	// detect path traversal even without workDir
	// use regex to detect ".." as a path component, not inside filenames
	if pathTraversalRe.MatchString(filePath) {
		return &SafetyCheckResult{
			IsProtected:          true,
			Reason:               fmt.Sprintf("path traversal detected: %q contains '..'", filePath),
			ClassifierApprovable: false,
		}
	}

	// path traversal detection：判定收敛到 platform.IsWithinResolved（SSOT）。
	// 旧实现 `filepath.Rel + strings.HasPrefix(rel, "..")` 会把工作目录内**合法名为 `..foo` 的目录**
	// 误判为穿越（`..foo/b.go` 以 ".." 开头）。改用统一的分隔符边界判定。
	// 顺带修正：Rel 报错（如 Windows 跨盘）时旧实现**放行**，现在 fail-closed 拒绝。
	if resolvedWorkDir != "" {
		if !platform.IsWithinResolved(filepath.Clean(cleanPath), resolvedWorkDir) {
			return &SafetyCheckResult{
				IsProtected:          true,
				Reason:               fmt.Sprintf("path traversal detected: %q escapes work directory", filePath),
				ClassifierApprovable: false,
			}
		}
	}

	// check protected paths
	for _, protected := range ProtectedPaths {
		if matchProtectedPath(cleanPath, protected) {
			return &SafetyCheckResult{
				IsProtected:          true,
				Reason:               fmt.Sprintf("path contains protected %q", protected),
				ClassifierApprovable: false,
			}
		}
	}

	// Check system-level protected paths
	for _, protected := range ProtectedSystemPaths {
		expanded := expandHomeDir(protected)
		if matchProtectedPath(cleanPath, expanded) || matchProtectedPath(cleanPath, protected) {
			return &SafetyCheckResult{
				IsProtected:          true,
				Reason:               fmt.Sprintf("path matches protected system path %q", protected),
				ClassifierApprovable: false,
			}
		}
	}

	return nil
}

// matchProtectedPath check if path matches protected path name
func matchProtectedPath(cleanPath, protected string) bool {
	normalized := filepath.ToSlash(filepath.Clean(cleanPath))
	normalizedProtected := filepath.ToSlash(filepath.Clean(protected))

	lowerPath := strings.ToLower(normalized)
	lowerProtected := strings.ToLower(normalizedProtected)

	// Exact full path match (for system-level protected paths like ~/.ssh/config)
	if lowerPath == lowerProtected {
		return true
	}

	// Check if path suffix part is exact match (for absolute path scenarios)
	if strings.HasSuffix(lowerPath, "/"+lowerProtected) ||
		strings.HasSuffix(lowerPath, "\\"+lowerProtected) {
		return true
	}

	// Check if path suffix part is exact match (relative path scenarios)
	if strings.HasSuffix(lowerProtected, "/"+lowerPath) ||
		strings.HasSuffix(lowerProtected, "\\"+lowerPath) {
		return true
	}

	// Exact match last component
	if filepath.Base(lowerPath) == lowerProtected {
		if strings.HasPrefix(lowerProtected, ".") || strings.Contains(lowerProtected, ".") {
			return true
		}
		return false
	}

	// check if path contains protected components (e.g. .git, .whale)
	parts := strings.Split(lowerPath, "/")
	for i, part := range parts {
		if part == lowerProtected {
			// .git as directory component (has content after)
			if i < len(parts)-1 || strings.HasPrefix(lowerProtected, ".") {
				return true
			}
		}
	}

	return false
}

// expandHomeDir expand ~ to user home directory
func expandHomeDir(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// WriteToolSet 是「**目标受限写类工具**」集合 —— 即「落盘效果只经由**声明的目标路径**发生」的工具。
//
// ★ 这是本仓该语义的**唯一声明**（2026-09-20 批 C 收敛）。
//
// # 收敛前的两份（真分歧，非笔误）
//
// - 本包 `IsWriteTool`：含 `bash` / `powershell`，用 `strings.ToLower` 归一化；
// 缺 `export_session` / `git_*` / `LspRename`。
// - `internal/tools/pipeline.go` `isWriteTool`：含 `export_session` / `git_*` / `LspRename`，
// 精确匹配（`NotebookEdit` 大小写敏感）。
//
// 两份对 `bash` 的期望**直接相反**，且**各自的测试各自钉死了相反答案**
// （`permission/safety_test.go` 期望 true，`tools/pipeline_test.go` 期望 false）
// ⇒ 这不是"有人抄错一行"，而是"同一语义两套答案"。`apply_diff_prod_test.go` 的注释
// 也点名过这个隐患：「任一份漏登记，本工具就会**绕过**检查，且不会有编译错误」。
//
// # 为什么收敛到**不含** `bash` / `powershell`
//
// `bash` / `powershell` 是「**任意命令类**」：入参只有一个命令串，可以 `cd`、可以 `$(...)`、
// 可以再调别的程序 ⇒ **入参界定不了它的可达面**（详见 `tools/pipeline.go` 的 P3 分流注释）。
// 把它们并入「目标受限」会让工作区边界闸与出口 (a) 的免问**错误地对 bash 放行** ——
// 那是安全回退，故二者**必须留在表外**。
//
// # 为什么删 `bash` / `powershell` 是**行为中性**（实测，非推断）
//
// 本谓词在本仓**唯一的生产调用点**是 `SafetyCheckStage`（`tools/pipeline.go`）。
// 该阶段随后调 `ExtractFilePath`（只读 `file_path` / `path`，**刻意**不读 `command`），
// 而 bash / powershell 的入参里没有这两个字段 ⇒ 取到空串 ⇒ `CheckPathSafety("")` 直接返回 nil
// ⇒ 阶段照样 `Proceed`。
// ⇒ 删掉这两条**不改变任何判定结果**：它们本就是「声明了却不生效」的成员
// （“ 型"入口未接线却对外宣称"）。
//
// # ★ 与 `CanModifyLocalState` 的区别（**不要合并**）
//
// - `CanModifyLocalState` 答「这个工具**能不能改动本机**」（含 `CapExec` ⇒ **含 bash**）；
// - 本谓词答「它的写入是否**只经由声明的目标路径**」（⇒ **不含 bash**）。
//
// 前者服务 的"自指从严面"，后者服务边界闸 / 免问面 ⇒ 是**两个不同问题**。
//
// # 大小写
//
// 统一 `strings.ToLower` 归一化（历史上登记名两种写法都出现过，精确匹配更脆）。
//
// ⚠️ 本次收敛的**唯一行为变化**：`LspRename` 此前只被 tools 侧认作写类，
// 现在两侧一致 ⇒ `SafetyCheckStage` 从此也会检查它的目标路径（重命名受保护文件**确属写**，
// 判为**有意的收窄**）。其余成员的增删均**已证中性**（`bash`/`powershell` 见上；
// `export_session` / `git_*` 入参无 `file_path`/`path`，同理由）。
var WriteToolSet = map[string]bool{
	"write_file":     true,
	"edit_file":      true,
	"notebookedit":   true,
	"lsprename":      true,
	"git_commit":     true,
	"git_add":        true,
	"git_branch":     true,
	"export_session": true,
	// G5：补丁应用会写（含新建/修改/删除）文件，必须与 write_file 同走工作区边界校验。
	// ★ 键是字面量：`internal/tools` 的 `applyUnifiedDiffToolName` 值同为 `apply_unified_diff`，
	// 两者的一致性由 `tools/apply_diff_prod_test.go:TestApplyDiff_RegisteredAsWriteFace` 钉住
	// （permission 不能 import tools —— 方向相反，会成环）。
	"apply_unified_diff": true,
}

// IsWriteTool 报告 toolName 是否为「目标受限写类工具」。判定本体 = `WriteToolSet`（唯一声明）。
func IsWriteTool(toolName string) bool {
	return WriteToolSet[strings.ToLower(toolName)]
}

// ExtractFilePath extracts the file path from a tool input JSON.
// 仅提取 path-like 字段（"file_path" / "path"），不提取 "command" 字段。
// 原因：SafetyCheckStage 仅用于检测文件路径是否受保护；
// Bash/PowerShell 的 "command" 字段（如 "echo hello"）不应被当作文件路径校验，
// 否则会误报 "path \"echo hello\" may escape work directory"。
// 命令类规则的匹配仍由 rules.go 的 extractToolCommand 负责（其需要提取 command）。
func ExtractFilePath(input string) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return ""
	}
	if fp, ok := parsed["file_path"].(string); ok {
		return fp
	}
	if p, ok := parsed["path"].(string); ok {
		return p
	}
	return ""
}

// resolveExistingParent 解析 path 的最深已存在祖先，并拼回尚未存在的后缀。
// 用于"新建文件写入符号链接指向的工作目录"这类场景，同时不放松对已存在 symlink 文件的检查。
//
// ★ 2026-09-19 收敛（同一语义多处实现）：算法已提升为 **platform.ResolveExistingParent**（SSOT）。
// 本函数保留为薄转发，调用方无需改动（filehistory 侧曾另有一份更弱的独立实现，已一并收敛）。
func resolveExistingParent(path string) (string, error) {
	return platform.ResolveExistingParent(path)
}
