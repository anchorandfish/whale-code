package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizePath 规范化路径：EvalSymlinks 优先，失败降级 Abs+Clean。
// 复用 magic_docs_v2.go:198-213 已验证模式：必须先 EvalSymlinks 再 Abs，
// 否则 symlink 指向白名单外可绕过。
// 路径不存在时（新文件、fileHistory track 未创建文件）EvalSymlinks 失败，
// 降级为 filepath.Abs + filepath.Clean，保留绝对路径语义。
// 返回规范化后的绝对路径，并统一 NFC 规范化。
func NormalizePath(p string) (string, error) {
	// 1. null 字节注入防护（对标同类 CLI）
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("path contains null byte")
	}

	// 2. ~ 展开
	expanded, err := ExpandTilde(p)
	if err != nil {
		return "", fmt.Errorf("expand tilde: %w", err)
	}

	// 3. EvalSymlinks 优先
	resolved, err := filepath.EvalSymlinks(expanded)
	if err == nil {
		// 必须先 EvalSymlinks 再 Abs（magic_docs_v2 注释）
		abs, err := filepath.Abs(resolved)
		if err != nil {
			return "", fmt.Errorf("abs: %w", err)
		}
		cleaned := filepath.Clean(abs)
		return NormalizeUnicode(cleaned), nil
	}

	// 4. 降级：路径不存在时走 Abs + Clean
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("abs: %w", err)
	}
	cleaned := filepath.Clean(abs)
	return NormalizeUnicode(cleaned), nil
}

// ResolvePath 基于 workDir 解析为绝对路径。
// 相对路径用 filepath.Join(workDir, p) 拼接后调 NormalizePath；
// 绝对路径直接调 NormalizePath。
// workDir 为空时用 os.Getwd()。
func ResolvePath(p, workDir string) (string, error) {
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		workDir = wd
	}

	if IsAbsolute(p) {
		return NormalizePath(p)
	}
	return NormalizePath(filepath.Join(workDir, p))
}

// IsAbsolute 判断绝对路径（跨平台）。
// Windows: C:\ / C:/ / \\server\share (UNC) / \\?\C:\ (verbatim)
// Unix: /path
// 复用 filepath.IsAbs 但补充 UNC 与 verbatim 检测。
func IsAbsolute(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	// Windows UNC: \\server\share 或 //server/share
	if IsWindows() && len(p) >= 2 {
		if (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/') {
			return true
		}
	}
	return false
}

// ToSlash 将路径中的 \ 转为 /（跨平台统一为 POSIX 风格）。
// 用于配置键规范化、日志输出、file:/// URI 拼接前置。
// 对标同类 CLI normalizePathForConfigKey。
func ToSlash(p string) string {
	// 始终将反斜杠转为正斜杠，处理跨平台路径（Windows 路径在 Linux 上也需归一化）
	return strings.ReplaceAll(p, "\\", "/")
}

// FromSlash 将路径中的 / 转为 os.PathSeparator（POSIX → 平台原生）。
// 用于从配置/日志读回路径后转为平台原生分隔符。
func FromSlash(p string) string {
	return filepath.FromSlash(p)
}

// ExpandTilde 展开 ~ 为家目录。
// ~ → homedir; ~/foo → homedir/foo;
// Windows 兼容 ~\foo（同类 CLI 仅 Unix ~/，H6 增强 Windows ~\）。
// 对标同类 CLI expandPath 的 tilde 分支。
func ExpandTilde(p string) (string, error) {
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}
		return home, nil
	}
	// ~/foo 或 ~\foo
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// NormalizeUnicode 对路径做 NFC 规范化（处理中文组合字符）。
// 对标同类 CLI path.ts 多处 .normalize('NFC')。
// 用 golang.org/x/text/unicode/norm.NFC.String(p)。
// NFC 规范化后，配合 \p{L}\p{N}\p{M} 正则进行 Unicode 安全匹配（见 §7.2 N5 协同条款）。
func NormalizeUnicode(p string) string {
	return norm.NFC.String(p)
}

// CleanAndNormalize 一键清理：filepath.Clean + NFC 规范化。
// 不做 EvalSymlinks（轻量版，用于非安全场景如日志/配置键）。
func CleanAndNormalize(p string) string {
	return NormalizeUnicode(filepath.Clean(p))
}

// pathEqual 是**全仓路径等价比较的唯一机制**。
//
// 归拢背景（2026-09-19 · 收口「同一语义多处实现」）：此前本包内有
// `PathEqual`（按文件系统折叠大小写）与 `CaseInsensitiveCompare`（一律折叠）
// 两份**各自实现**的比较代码，`util.PathsEqual` 又是第三份（Abs+EvalSymlinks+按 GOOS 折叠）。
// 三份的规范化步骤与折叠条件各不相同，等于把"什么时候折叠大小写"这一**策略**
// 偷偷编码进了实现。
//
// 现在的切法：**机制只有一份**（本函数），折叠与否由显式参数 `foldCase` 决定；
// 每个公开入口各自声明自己的策略。策略差异因此变成**可读的、写在签名上的**事实。
//
// 注意：本函数不做 EvalSymlinks（性能优先）；需要"解析到同一文件系统对象"的
// 等价判断，先用 NormalizePath（EvalSymlinks 优先），见 util.PathsEqual。
func pathEqual(a, b string, foldCase bool) bool {
	normA := CleanAndNormalize(ToSlash(a))
	normB := CleanAndNormalize(ToSlash(b))
	if foldCase {
		return strings.EqualFold(normA, normB)
	}
	return normA == normB
}

// PathEqual 平台感知路径等价判断—— **路径等价的全仓 SSOT**。
//
// 策略：折叠大小写**当且仅当**文件系统大小写不敏感（CaseInsensitiveFS()）。
// 无条件折叠是错的：Linux（大小写敏感）上 `/WORK/FOO` 是**另一个目录**，
// 把它判为等价属越权放行（同 IsWithinResolved 的不变量 2）。
//
// 注意：此函数不做 EvalSymlinks（性能优先）；安全敏感场景用 IsPathAllowed。
func PathEqual(a, b string) bool {
	return pathEqual(a, b, CaseInsensitiveFS())
}

// CaseInsensitiveCompare 权限检查专用比较：**强制**折叠大小写。
//
// 与 PathEqual 共用同一机制，差异只在 `foldCase` 被**显式**置为 true ——
// 这是有意的策略选择（权限匹配宁可过度匹配），不是"另一份实现"。
//
// ⚠️ 它因此**不是**路径等价判断：在大小写敏感文件系统上，本函数会把
// 两个不同目录判为等价。除权限匹配外不要用它。
func CaseInsensitiveCompare(a, b string) bool {
	return pathEqual(a, b, true)
}
