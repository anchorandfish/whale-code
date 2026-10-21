// path_contain.go — 路径「是否在允许根之内」的**单一真相源**（SSOT）。
//
// ## 为什么需要这个文件（2026-09-19 · 收口「同一语义多处实现」）
//
// 在这个文件出现之前，同一语义（"这个路径在允许根之内吗"）在仓库里有**三套独立实现、三种技术**：
//
//	platform.IsPathAllowed 前缀 + 分隔符边界（正确）· EvalSymlinks 失败即拒（fail-closed）
//	filehistory.safePath **裸前缀**（无分隔符边界 ⇒ `proj-secret` 被判为在 `proj` 内）· ToLower
//	permission.CheckPathSafety filepath.Rel + HasPrefix(rel,"..")（`..foo` 这类合法文件名会误拒）
//
// 三套的分歧是**实测**出来的（见 `internal/filehistory/path_containment_convergence_test.go`）。
// 本文件把正确的机制收敛为唯一实现，各调用方只按**自己需要的策略**选择入口：
//
//	IsPathAllowed(p, roots) —— 路径必须**已存在**；否则 false（fail-closed，安全门用）
//	ResolveWithin(p, roots) —— 路径**可以尚不存在**（新文件）；解析最深已存在祖先
//
// 两者的**规范化 + 边界判定**共用本文件的 `CanonicalizeRoot` / `IsWithinResolved`，
// ⇒ 不可能再出现"这里一套、那里一套"。
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveExistingParent 解析 path 的**最深已存在祖先**（EvalSymlinks），再把尚未存在的后缀拼回。
//
// 用途："文件还没创建" 的场景（新建文件、fileHistory 追踪未创建文件）仍需做包含判定。
// 直接对整体 EvalSymlinks 会失败，若就此放行则 symlink 可绕过；若一律拒绝则新文件写不进来。
//
// 语义要点：
// - 后缀部分（不存在的段）不含 symlink（它们不存在）⇒ 逐段 Join 后 Clean 即等价；
// - 路径本身存在但**解析失败**（dangling symlink / 权限）⇒ 不走本函数，见 ResolveWithin 的 fail-closed 分支。
//
// 本函数原为 permission 包内未导出的 `resolveExistingParent`（safety.go），
// 现提升为共享实现，permission 侧改为薄转发。
func ResolveExistingParent(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			// 已到根仍不可解析：无任何可用锚点
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

// CanonicalizeRoot 规范化白名单根，与 IsWithinResolved 的入参约定对称。
//
// root 存在 ⇒ EvalSymlinks→Abs→Clean→NFC；不存在 ⇒ 降级 Abs→Clean→NFC。
// 第二个返回值为 **resolved**：true 表示 EvalSymlinks 成功（根确实存在且可解析）。
// 调用方可据此区分"已解析"与"降级"两种情形（例如 permission 的 dangling-symlink 分支需要这个区分）。
// 第一个返回值为空串表示该根不可用（空串或 Abs 失败）。
func CanonicalizeRoot(root string) (string, bool) {
	if root == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		return NormalizeUnicode(resolved), true
	}
	return NormalizeUnicode(abs), false
}

// IsWithinResolved 判断**已规范化的绝对路径** absPath 是否位于**已规范化的根** root 之内。
//
// 这是全仓唯一的包含判定，两条不变量：
//
// 1. **分隔符边界**：必须 `absPath == root` 或 `root + os.PathSeparator` 前缀。
// 裸前缀是错的 —— `proj-secret` 会以 `proj` 为前缀而被误判为"在内"（曾导致真实缺陷）。
// 2. **大小写只在文件系统大小写不敏感时折叠**（CaseInsensitiveFS）。
// 无条件 ToLower 在 Linux（大小写敏感）上会把 `/WORK/FOO` 判为在 `/work/foo` 内 ——
// 而那在 Linux 上是**另一个目录**，属越权放行。反之在 macOS/Windows 上不折叠则会误拒。
//
// 调用方需自行保证入参已规范化（Abs+Clean+EvalSymlinks）；未规范化时结果无意义。
func IsWithinResolved(absPath, root string) bool {
	if absPath == "" || root == "" {
		return false
	}
	a, r := absPath, root
	if CaseInsensitiveFS() {
		a, r = strings.ToLower(a), strings.ToLower(r)
	}
	if a == r {
		return true
	}
	// TrimSuffix 处理 root == "/"（否则会拼出 "//" 而永远不匹配）
	prefix := strings.TrimSuffix(r, string(os.PathSeparator)) + string(os.PathSeparator)
	return strings.HasPrefix(a, prefix)
}

// ResolveWithin 解析 p 为规范绝对路径，并断言它位于 roots 之一以内；不在则返回错误。
//
// 与 IsPathAllowed 的区别**只在缺失路径的策略**：
//
//	IsPathAllowed 路径不存在 ⇒ false（安全门：宁拒勿放）
//	ResolveWithin 路径不存在 ⇒ 解析最深已存在祖先，仍做包含判定（新文件场景）
//
// 两者共用同一套规范化与边界判定，故不会分歧。
//
// fail-closed 的两种情形：
// - **p 存在（Lstat 成功）但 EvalSymlinks 失败** ⇒ 可疑（dangling symlink 指向外部、权限问题），拒绝。
// 注意不能把它当作"尚未创建"放行 —— 那正是 symlink 逃逸的入口。
// - **p 与所有祖先都不存在** ⇒ 无锚点可判定，拒绝。
func ResolveWithin(p string, roots []string) (string, error) {
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("path contains null byte")
	}

	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("abs: %w", err)
	}
	abs = filepath.Clean(abs)

	var resolved string
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		resolved = r
	} else {
		_, statErr := os.Lstat(abs)
		if !os.IsNotExist(statErr) {
			// 存在却解析不了（dangling symlink / 权限）⇒ 不当作"新文件"，fail-closed
			return "", fmt.Errorf("cannot resolve existing path %q: %w", abs, rerr)
		}
		parent, perr := ResolveExistingParent(abs)
		if perr != nil {
			return "", fmt.Errorf("no existing ancestor for %q: %w", abs, perr)
		}
		resolved = parent
	}
	resolved = NormalizeUnicode(filepath.Clean(resolved))

	for _, root := range roots {
		rc, _ := CanonicalizeRoot(root)
		if rc == "" {
			continue
		}
		if IsWithinResolved(resolved, rc) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path %q is outside the allowed roots", resolved)
}
