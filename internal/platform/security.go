package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// pathTraversalRegex 检测路径穿越的正则，同时匹配 / 和 \ 分隔符。
// 对标同类 CLI containsPathTraversal。
var pathTraversalRegex = regexp.MustCompile(`(?:^|[\\/])\.\.(?:[\\/]|$)`)

// windowsIllegalChars  Windows 非法文件名字符。
var windowsIllegalChars = []rune{':', '<', '>', '|', '?', '*', '"'}

// IsPathAllowed 检查路径是否在白名单根目录下。
// 复用 magic_docs_v2.go:198-213 已验证模式：EvalSymlinks→Abs→前缀检查。
// 必须先 EvalSymlinks 再 Abs，否则 symlink 指向白名单外可绕过（magic_docs_v2 注释）。
// EvalSymlinks 失败（路径不存在）视为不允许，返回 false（fail-closed）。
// roots 用 resolveCanonical 方式规范化（与 absPath 对称），
// 避免短名/junction 导致根目录与被检查路径形式不一致。
//
// ★ 规范化与「边界判定」已收敛到 path_contain.go 的 canonicalizeRoot / IsWithinResolved（SSOT）。
// 与收敛前相比**唯一的行为差异**：大小写比较改为跟随文件系统语义（CaseInsensitiveFS）——
// 在 macOS/Windows（大小写不敏感）上，仅大小写不同的路径此前被**误拒**，现正确判为"在内"；
// 在 Linux（大小写敏感）上行为不变（不折叠）。
// 若场景允许"路径尚不存在"（新建文件），不要用本函数（它 fail-closed），改用 ResolveWithin。
func IsPathAllowed(p string, roots []string) bool {
	// null 字节直接拒绝
	if strings.ContainsRune(p, '\x00') {
		return false
	}

	// 必须先 EvalSymlinks 再 Abs，否则 symlink 指向白名单外可绕过
	realPath, err := filepath.EvalSymlinks(p)
	if err != nil {
		// fail-closed：路径不存在视为不允许
		return false
	}

	absPath, err := filepath.Abs(realPath)
	if err != nil {
		return false
	}
	absPath = NormalizeUnicode(filepath.Clean(absPath))

	for _, root := range roots {
		// 规范化与「边界判定」已收敛到 path_contain.go（SSOT），此处不再自行比较：
		// 原先的本地位比较缺大小写语义，与其它实现（filehistory / permission）不一致。
		rootClean, _ := CanonicalizeRoot(root)
		if rootClean == "" {
			continue
		}
		if IsWithinResolved(absPath, rootClean) {
			return true
		}
	}
	return false
}

// ContainsTraversal 检测路径是否含 .. 穿越。
// 同时匹配 / 和 \ 分隔符（对标同类 CLI containsPathTraversal）。
// 正则：(?:^|[\\/])\.\.(?:[\\/]|$)
func ContainsTraversal(p string) bool {
	return pathTraversalRegex.MatchString(p)
}

// IsUNCPath 检测 UNC 路径（\\server\share 或 //server/share）。
// UNC 路径安全风险：可能触发 NTLM 凭据泄露（对标同类 CLI safeResolvePath）。
// 权限校验时应拒绝 UNC 路径（除非显式允许）。
func IsUNCPath(p string) bool {
	// \\server\share
	if len(p) >= 2 && p[0] == '\\' && p[1] == '\\' {
		return true
	}
	// //server/share
	if len(p) >= 2 && p[0] == '/' && p[1] == '/' {
		return true
	}
	return false
}

// SafeName 生成安全文件名（用于 snapshot/backup 文件命名）。
// 替换 os.PathSeparator 与 / 为 _（双分隔符，修复 snapshot.go:116 单分隔符问题）；
// 替换 Windows 非法文件名字符 : < > | ? * " 为 _；
// 保留中文/Unicode（不做 ASCII 化，对标同类 CLI sanitizePath 的缺陷——它会将中文全替换为 -）。
// 对 filepath.Base(absPath) 后的结果处理（输入应为文件名非完整路径）。
func SafeName(p string) string {
	// 替换两种分隔符为 _
	result := strings.ReplaceAll(p, string(os.PathSeparator), "_")
	result = strings.ReplaceAll(result, "/", "_")
	if os.PathSeparator != '\\' {
		// 非 Windows 系统也替换 \，确保跨平台安全
		result = strings.ReplaceAll(result, "\\", "_")
	}

	// 替换 Windows 非法文件名字符
	for _, ch := range windowsIllegalChars {
		result = strings.ReplaceAll(result, string(ch), "_")
	}

	return result
}
