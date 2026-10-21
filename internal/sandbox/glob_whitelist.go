// Package sandbox — M13-05 glob 白名单。
//
// 设计目标：
// - 为 SandboxConfig.AllowRead/AllowWrite 提供基于 glob 的精细白名单
// - 复用 C11 PathMatcher（path_matcher.go）的 .gitignore 语义
// - 支持目录前缀匹配（"src/" 匹配 src 下所有文件）和文件 glob（"*.go"）
// - 跨平台：路径自动归一化（Windows 反斜杠 → 正斜杠）
// - CGO_ENABLED=0：纯 Go，无外部依赖
//
// 复用契约（C11）：
// - PathMatcher.Match(path) 返回 true 表示路径在白名单内
// - PathMatcher 支持多 pattern（last-match-wins，与 .gitignore 一致）
// - GlobMatcher 支持 ** 通配、目录限定（trailing /）、! 取反
//
// 官方源对标：
// - .gitignore 语法: https://git-scm.com/docs/gitignore @2026-07-21
// - Go path/filepath.Match: https://pkg.go.dev/path/filepath#Match @2026-07-21
package sandbox

import (
	"path/filepath"
	"strings"
)

// GlobWhitelist 是基于 glob 的路径白名单（M13-05 SNAPSHOT）。
//
// 内部使用 C11 PathMatcher 实现，对外提供简化 API：
// - AddAllow(pattern): 添加允许 pattern
// - AddDeny(pattern): 添加拒绝 pattern（! 前缀语义）
// - IsAllowed(path): 路径是否在白名单内
//
// 匹配顺序（与 .gitignore 一致）：
// 1. 先按 AddAllow 添加的 pattern 累加允许
// 2. 后按 AddDeny 添加的 pattern 累加拒绝（可覆盖 Allow）
// 3. 默认拒绝（无 Allow pattern 命中）
//
// 注：与 .gitignore 不同，本实现是「白名单优先」语义（默认拒绝）。
type GlobWhitelist struct {
	// matcher 内部 GlobMatcher（C11 实现，支持 ! 取反）
	matcher *GlobMatcher
	// allowPatterns 已添加的允许 pattern 列表（按添加顺序）
	allowPatterns []string
	// denyPatterns 已添加的拒绝 pattern 列表（按添加顺序）
	denyPatterns []string
	// caseSensitive 是否大小写敏感（默认 false，与 .gitignore 一致）
	caseSensitive bool
}

// NewGlobWhitelist 创建空 glob 白名单（M13-05）。
//
// 默认行为：
// - 大小写不敏感（与 .gitignore 一致）
// - 默认拒绝所有路径（需显式 AddAllow）
func NewGlobWhitelist() *GlobWhitelist {
	return &GlobWhitelist{
		matcher:       NewGlobMatcher(),
		caseSensitive: false,
	}
}

// NewGlobWhitelistCaseSensitive 创建大小写敏感的 glob 白名单（Linux 用）。
func NewGlobWhitelistCaseSensitive() *GlobWhitelist {
	return &GlobWhitelist{
		matcher:       NewGlobMatcher(),
		caseSensitive: true,
	}
}

// AddAllow 添加允许 pattern（M13-05）。
//
// pattern 语法（C11 PathMatcher，对齐 .gitignore）：
// - "*.go" → 匹配所有 .go 文件（任意目录）
// - "src/" → 匹配 src 目录下所有文件（trailing / 表示目录）
// - "src/**/*.go" → 匹配 src 下任意深度的 .go 文件
// - "/etc/passwd" → 精确匹配 /etc/passwd
// - "/tmp/foo" → 匹配 /tmp/foo 本身 + 子树（/tmp/foo/file.txt）
//
// 多次 AddAllow 累加：任一 pattern 命中即视为允许。
// Pattern 编译失败时返回 error，调用方可选择忽略或终止。
//
// 自动子树扩展：对于不含通配符（*?[）且不以 / 结尾的 pattern，
// 自动追加 `pattern/**` 以支持子树匹配（与 AllowRead 语义一致）。
func (g *GlobWhitelist) AddAllow(pattern string) error {
	if g == nil || pattern == "" {
		return nil
	}
	if err := g.matcher.AddPattern(pattern); err != nil {
		return err
	}
	g.allowPatterns = append(g.allowPatterns, pattern)
	// 自动追加 /** 子树匹配（仅对纯路径 pattern）
	if !strings.HasSuffix(pattern, "/") && !containsGlobChars(pattern) {
		subtree := strings.TrimSuffix(pattern, "/") + "/**"
		if err := g.matcher.AddPattern(subtree); err != nil {
			return err
		}
	}
	return nil
}

// containsGlobChars 检查 pattern 是否包含 glob 通配符。
func containsGlobChars(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

// AddDeny 添加拒绝 pattern（M13-05）。
//
// 用于在 Allow 后追加例外（与 .gitignore 的 ! 语义一致）。
// 例如：AddAllow("src/"); AddDeny("src/secret/") → 允许 src 下除 secret/ 外的所有。
//
// Pattern 编译失败时返回 error。
func (g *GlobWhitelist) AddDeny(pattern string) error {
	if g == nil || pattern == "" {
		return nil
	}
	// 内部用 ! 前缀表达否定语义（C11 PathMatcher 支持）
	denyPattern := "!" + pattern
	if err := g.matcher.AddPattern(denyPattern); err != nil {
		return err
	}
	g.denyPatterns = append(g.denyPatterns, pattern)
	return nil
}

// IsAllowed 检查路径是否在白名单内（M13-05）。
//
// 参数：
// - path: 待检查的路径（绝对/相对均可，自动归一化）
//
// 返回：
// - allowed: true=允许访问，false=拒绝
//
// 匹配语义：
// - 路径自动归一化（filepath.Clean + Windows 反斜杠转正斜杠）
// - 大小写不敏感模式下统一转小写比较
// - 任一 Allow pattern 命中 → 允许
// - 任一 Deny pattern 命中 → 拒绝（覆盖 Allow）
// - 默认拒绝（无 pattern 命中）
func (g *GlobWhitelist) IsAllowed(path string) bool {
	if g == nil {
		return false
	}
	if path == "" {
		return false
	}
	normalized := normalizeGlobPath(path, g.caseSensitive)
	return g.matcher.Match(normalized)
}

// AllowPatterns 返回已添加的允许 pattern 列表（按添加顺序，只读副本）。
func (g *GlobWhitelist) AllowPatterns() []string {
	if g == nil {
		return nil
	}
	out := make([]string, len(g.allowPatterns))
	copy(out, g.allowPatterns)
	return out
}

// DenyPatterns 返回已添加的拒绝 pattern 列表（按添加顺序，只读副本）。
func (g *GlobWhitelist) DenyPatterns() []string {
	if g == nil {
		return nil
	}
	out := make([]string, len(g.denyPatterns))
	copy(out, g.denyPatterns)
	return out
}

// normalizeGlobPath 归一化路径用于 glob 匹配（M13-05）。
//
// 步骤：
// 1. filepath.Clean 清理 ../ 和 ./
// 2. 替换 Windows 反斜杠为正斜杠
// 3. 去除前导 / （与 compileGlob 行为一致：行首 / 在 .gitignore 中锚定仓库根，whale-code 中视为普通路径）
// 4. 大小写不敏感模式下转小写
func normalizeGlobPath(path string, caseSensitive bool) string {
	cleaned := filepath.Clean(path)
	// 统一为正斜杠（跨平台）
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	// 去除前导 /（与 compileGlob 的 TrimPrefix("/", ...) 一致）
	cleaned = strings.TrimPrefix(cleaned, "/")
	if !caseSensitive {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

// DefaultGlobWhitelist 返回 M13-05 推荐的默认白名单（开发场景）。
//
// 默认允许：
// - 项目源码：*.go *.ts *.js *.py *.md
// - 项目配置：go.mod go.sum .gitignore
// - 项目目录：src/ cmd/ internal/ pkg/ docs/
//
// 默认拒绝：
// - 凭证文件：.env* *.pem id_rsa*
// - 系统路径：/etc/* /var/* /usr/*
//
// 注：本函数忽略 Pattern 编译错误（默认 Pattern 都是字面量，不会失败）。
func DefaultGlobWhitelist() *GlobWhitelist {
	w := NewGlobWhitelist()
	// 允许源码
	_ = w.AddAllow("*.go")
	_ = w.AddAllow("*.ts")
	_ = w.AddAllow("*.js")
	_ = w.AddAllow("*.py")
	_ = w.AddAllow("*.md")
	_ = w.AddAllow("*.json")
	_ = w.AddAllow("*.yaml")
	_ = w.AddAllow("*.yml")
	// 允许配置
	_ = w.AddAllow("go.mod")
	_ = w.AddAllow("go.sum")
	_ = w.AddAllow(".gitignore")
	// 允许目录
	_ = w.AddAllow("src/")
	_ = w.AddAllow("cmd/")
	_ = w.AddAllow("internal/")
	_ = w.AddAllow("pkg/")
	_ = w.AddAllow("docs/")
	// 拒绝凭证
	_ = w.AddDeny(".env")
	_ = w.AddDeny(".env.*")
	_ = w.AddDeny("*.pem")
	_ = w.AddDeny("id_rsa*")
	// 拒绝系统路径
	_ = w.AddDeny("/etc/")
	_ = w.AddDeny("/var/")
	_ = w.AddDeny("/usr/")
	return w
}

// GlobWhitelistFromAllowRead 从 SandboxConfig.AllowRead 构建白名单（M13-05）。
//
// 工具函数：将旧版字符串切片配置转换为 glob 白名单。
// 保留原有精确路径语义（不添加 /* 后缀，由 PathMatcher 决定）。
// Pattern 编译失败时跳过该 pattern（best-effort）。
func GlobWhitelistFromAllowRead(allowRead []string) *GlobWhitelist {
	w := NewGlobWhitelist()
	for _, p := range allowRead {
		_ = w.AddAllow(p) // 编译失败忽略
	}
	return w
}
