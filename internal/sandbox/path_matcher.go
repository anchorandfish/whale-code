// Package sandbox — C11 统一路径匹配接口 PathMatcher（主导，M13-05/M14-05/M15-04 复用）。
//
// 设计目标（T0-2 §2 C11 仲裁）：
// - 统一 glob/路径前缀/正则 三类匹配语义
// - 对标 .gitignore 双层语义（含 ** 递归 + ! 取反）
// - 各模块（M13 沙箱白名单 / M14 路径穿越 / M15 .whaleignore）通过依赖注入复用
// - 纯 Go 实现（CGO_ENABLED=0 合规）+ 跨平台路径分隔符
//
// 官方源对标：
// - git pathspec: https://git-scm.com/docs/gitglossary#Documentation/gitglossary.txt-aiddefpathspecpathspec @2026-07-21
// - .gitignore: https://git-scm.com/docs/gitignore @2026-07-21
package sandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// PathMatcher 是 C11 仲裁的统一路径匹配接口（SNAPSHOT）。
//
// 三类实现通过 Match(path) bool 统一暴露：
// - GlobMatcher — 支持 *, ?, **, 字符类 [abc]，参考 filepath.Match + .gitignore 扩展
// - PrefixMatcher — 路径前缀匹配（dir/ 子树）
// - RegexMatcher — 正则匹配（PowerShell 风格，转义后编译）
//
// 调用方约定：
// - path 必须是已 Clean 且转 slash 的绝对/相对路径
// - 多 Pattern 命中取首个 AddPattern 顺序（保留 .gitignore 上下文顺序语义）
// - Pattern 编译失败返回 error，调用方决定 fail-closed 或忽略
type PathMatcher interface {
	// Match 返回 path 是否命中任一 Pattern。
	// path 必须是已规范化的路径（Clean + ToSlash）。
	Match(path string) bool

	// AddPattern 追加一条 Pattern，编译失败返回 error（不污染现有状态）。
	// Pattern 语法由具体实现解释（glob / prefix / regex）。
	AddPattern(pattern string) error

	// Patterns 返回已添加的 Pattern 列表（用于审计/调试）。
	Patterns() []string
}

// === GlobMatcher ===

// GlobMatcher 实现 .gitignore 风格的 glob 匹配（C11 默认实现）。
//
// 支持语法（对标 .gitignore + filepath.Match 超集）：
// - * 匹配除 / 外的任意字符序列
// - ** 匹配任意字符序列（含 /，递归目录）
// - ? 匹配除 / 外的任意单字符
// - [abc] 字符类（含 [!abc] 取反）
// - ! 行首 ! 表示取反（命中此 Pattern 表示不匹配）
// - / 路径分隔符；末尾 / 匹配目录（语义上等价 prefix/）
//
// 不支持：{a,b} brace expansion（保留给 bash，路径匹配无需）
//
// 并发安全：读多写少场景，sync.RWMutex 保护 patterns 切片。
type GlobMatcher struct {
	mu       sync.RWMutex
	patterns []compiledGlob
	raw      []string
}

// compiledGlob 是已编译的 glob Pattern。
type compiledGlob struct {
	raw      string
	negate   bool // 行首 ! 取反
	dirOnly  bool // 末尾 / 仅匹配目录
	segments []globSegment
}

// globSegment 是按 / 拆分后的单段 glob（不含 /）。
type globSegment struct {
	// literal 非空时直接子串匹配
	literal string
	// regex 非空时用正则匹配
	regex *regexp.Regexp
}

// NewGlobMatcher 创建空的 GlobMatcher。
func NewGlobMatcher() *GlobMatcher {
	return &GlobMatcher{}
}

// AddPattern 编译并追加 glob Pattern。
//
// 错误场景：
// - Pattern 含非法字符类（[abc 未闭合）→ 返回编译错误
// - 空 Pattern → 返回 ErrEmptyPattern
func (g *GlobMatcher) AddPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return ErrEmptyPattern
	}
	compiled, err := compileGlob(pattern)
	if err != nil {
		return fmt.Errorf("PathMatcher: compile glob %q: %w", pattern, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.patterns = append(g.patterns, compiled)
	g.raw = append(g.raw, pattern)
	return nil
}

// Match 检查 path 是否命中任一 Pattern（按 AddPattern 顺序短路）。
//
// 语义（对标 .gitignore last-match-wins）：
// - 同一 path 命中多个 Pattern 时，按 AddPattern 顺序遍历
// - 取反 Pattern（!foo）命中后清除之前的匹配结果
// - 全部 Pattern 遍历后，最终状态决定 Match 结果
//
// path 预处理：调用方应传入 Clean + ToSlash 路径；本方法不二次归一化
// 以避免与调用方语义冲突（如 allowlist 已是 slash 形式）。
func (g *GlobMatcher) Match(path string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	matched := false
	for _, p := range g.patterns {
		if globMatchCompiled(p, path) {
			if p.negate {
				matched = false
			} else {
				matched = true
			}
		}
	}
	return matched
}

// Patterns 返回已添加的 Pattern 原文列表。
func (g *GlobMatcher) Patterns() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, len(g.raw))
	copy(out, g.raw)
	return out
}

// globMatchCompiled 用编译后的 Pattern 匹配 path。
//
// 处理逻辑：
// 1. dirOnly（末尾 /）时仅匹配 path 在此目录下的子路径（前缀段全匹配 + 至少多一段）
// 2. Pattern 含 / 时按段匹配（** 递归）
// 3. Pattern 不含 / 时仅匹配末段（.gitignore 行为：单段 Pattern 匹配任意层级的同名 entry）
func globMatchCompiled(p compiledGlob, path string) bool {
	if len(p.segments) == 0 {
		return false
	}
	// dirOnly：pattern 表示目录，path 必须在此目录下（path 段数 > pattern 段数，前缀段全匹配）
	if p.dirOnly {
		return matchDirOnly(p.segments, path)
	}
	// 单段 Pattern：匹配末段（basename 语义）
	if len(p.segments) == 1 {
		return matchSegment(p.segments[0], pathBase(path))
	}
	// 多段 Pattern：全路径段匹配
	return matchSegments(p.segments, path)
}

// matchDirOnly 匹配目录前缀（pattern segments 表示目录路径，path 必须在此目录下）。
//
// 语义（对标 .gitignore foo/）：
// - path 段数必须严格大于 pattern 段数（path 在目录内，而非目录本身）
// - path 的前 N 段必须与 pattern 的 N 段全部匹配（N = pattern 段数）
//
// 例：pattern "foo/" → segments=["foo"]
// - "foo/bar" 命中（path 段数 2 > 1，首段匹配）
// - "bar/foo" 不命中（path 首段 "bar" 不匹配 "foo"）
// - "foo" 不命中（path 段数 1，不严格大于 1）
func matchDirOnly(patternSegs []globSegment, path string) bool {
	pathSegs := strings.Split(path, "/")
	if len(pathSegs) <= len(patternSegs) {
		return false
	}
	for i, seg := range patternSegs {
		if !matchSegment(seg, pathSegs[i]) {
			return false
		}
	}
	return true
}

// pathBase 返回 path 的末段（不含 /）。
func pathBase(path string) string {
	if path == "" {
		return ""
	}
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// matchSegments 按 / 拆分 path 后逐段匹配 Pattern 段（含 ** 递归）。
//
// ** 段匹配 0 个或多个 path 段（递归回溯）。
func matchSegments(patternSegs []globSegment, path string) bool {
	pathSegs := strings.Split(path, "/")
	return matchSegmentsRecursive(patternSegs, pathSegs, 0, 0)
}

// matchSegmentsRecursive 递归匹配 Pattern 段与 path 段。
//
// 参数：
// - pi: 当前 Pattern 段索引
// - ni: 当前 path 段索引
func matchSegmentsRecursive(patternSegs []globSegment, pathSegs []string, pi, ni int) bool {
	for pi < len(patternSegs) {
		// ** 段：匹配 0 个或多个 path 段
		if isDoubleStar(patternSegs[pi]) {
			// 末段 ** 匹配剩余所有 path 段
			if pi == len(patternSegs)-1 {
				return true
			}
			// 尝试匹配 0, 1, 2, ... 个 path 段
			for skip := ni; skip <= len(pathSegs); skip++ {
				if matchSegmentsRecursive(patternSegs, pathSegs, pi+1, skip) {
					return true
				}
			}
			return false
		}
		// 普通段：当前 path 段必须匹配
		if ni >= len(pathSegs) {
			return false
		}
		if !matchSegment(patternSegs[pi], pathSegs[ni]) {
			return false
		}
		pi++
		ni++
	}
	// Pattern 已匹配完，path 也必须恰好匹配完（避免 prefix 匹配）
	return ni == len(pathSegs)
}

// isDoubleStar 判断 Pattern 段是否为 **（递归通配）。
func isDoubleStar(seg globSegment) bool {
	return seg.literal == "**" && seg.regex == nil
}

// matchSegment 用单段 Pattern 匹配单段 path。
func matchSegment(seg globSegment, s string) bool {
	if seg.regex != nil {
		return seg.regex.MatchString(s)
	}
	if seg.literal != "" {
		return seg.literal == s
	}
	return false
}

// compileGlob 编译 glob Pattern 为 compiledGlob 结构。
//
// 处理：
// 1. 解析行首 !（取反）+ 末尾 /（dirOnly）
// 2. 按 / 拆分多段
// 3. 每段编译为 globSegment（literal 优先，含特殊字符走 regex）
func compileGlob(pattern string) (compiledGlob, error) {
	c := compiledGlob{raw: pattern}
	p := pattern
	// 取反
	if strings.HasPrefix(p, "!") {
		c.negate = true
		p = p[1:]
	}
	// 末尾 / 表示仅匹配目录
	if strings.HasSuffix(p, "/") {
		c.dirOnly = true
		p = strings.TrimSuffix(p, "/")
	}
	// 去除行首 / （.gitignore 中行首 / 锚定仓库根，whale-code 中工作区根等价于 path 根）
	p = strings.TrimPrefix(p, "/")
	// 按 / 拆段
	segments := strings.Split(p, "/")
	for _, seg := range segments {
		compiled, err := compileGlobSegment(seg)
		if err != nil {
			return compiledGlob{}, err
		}
		c.segments = append(c.segments, compiled)
	}
	return c, nil
}

// compileGlobSegment 编译单段 glob。
//
// 编译策略：
// - 纯字面量（无特殊字符）→ literal
// - ** → literal="**"（matchSegments 递归处理）
// - 含 *, ?, [ → 正则
func compileGlobSegment(seg string) (globSegment, error) {
	if seg == "" {
		return globSegment{literal: ""}, nil
	}
	if seg == "**" {
		return globSegment{literal: "**"}, nil
	}
	// 检测是否含特殊字符
	if !strings.ContainsAny(seg, "*?[]") {
		return globSegment{literal: seg}, nil
	}
	// 编译为正则
	re, err := globToRegex(seg)
	if err != nil {
		return globSegment{}, err
	}
	return globSegment{regex: re}, nil
}

// globToRegex 将单段 glob 转为 anchored 正则。
//
// 转换规则：
// - * → [^/]* （除 / 外任意字符）
// - ? → [^/] （除 / 外单字符）
// - [abc] → [abc]（保持原样）
// - [!abc] → [^abc]
// - 其他字符 → regexp.QuoteMeta
func globToRegex(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(glob) {
		c := glob[i]
		switch c {
		case '*':
			sb.WriteString("[^/]*")
			i++
		case '?':
			sb.WriteString("[^/]")
			i++
		case '[':
			// 字符类：找到匹配的 ]
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unclosed character class in glob: %q", glob)
			}
			classContent := glob[i+1 : i+1+end]
			// [!abc] → [^abc]
			if strings.HasPrefix(classContent, "!") {
				classContent = "^" + classContent[1:]
			}
			sb.WriteString("[")
			sb.WriteString(classContent)
			sb.WriteString("]")
			i = i + 1 + end + 1
		default:
			// 普通字符：转义后写入
			sb.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// === PrefixMatcher ===

// PrefixMatcher 实现路径前缀匹配（dir/ 子树）。
//
// 用于允许列表场景（如 AllowRead=["/tmp/foo"] 命中 /tmp/foo/bar）。
// 比 GlobMatcher 简单且 O(1) 性能更高。
type PrefixMatcher struct {
	mu       sync.RWMutex
	prefixes []string
}

// NewPrefixMatcher 创建空的 PrefixMatcher。
func NewPrefixMatcher() *PrefixMatcher {
	return &PrefixMatcher{}
}

// AddPattern 追加前缀 Pattern（自动 Clean + 反斜杠归一化为 slash）。
func (p *PrefixMatcher) AddPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return ErrEmptyPattern
	}
	cleaned := normalizePath(pattern)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prefixes = append(p.prefixes, cleaned)
	return nil
}

// Match 检查 path 是否在任一前缀子树下。
//
// 语义：
// - path == prefix → 命中
// - path 以 prefix+"/" 开头 → 命中
// - 否则不命中
//
// 跨平台：自动将 Windows 反斜杠归一化为 slash（即使运行在 Linux，
// 调用方传入 Windows 风格路径 "C:\\Users\\foo\\bar" 也能正确匹配）。
func (p *PrefixMatcher) Match(path string) bool {
	cleaned := normalizePath(path)
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, prefix := range p.prefixes {
		if cleaned == prefix {
			return true
		}
		if strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	return false
}

// normalizePath 跨平台路径归一化：Clean + 反斜杠转 slash。
//
// 单独提供以便 AddPattern/Match 共用同一归一化逻辑，避免不一致。
// 注：filepath.ToSlash 在 Linux 下是 no-op，故需显式 ReplaceAll 反斜杠。
func normalizePath(path string) string {
	cleaned := filepath.Clean(path)
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	return cleaned
}

// Patterns 返回已添加的前缀列表（已 Clean）。
func (p *PrefixMatcher) Patterns() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.prefixes))
	copy(out, p.prefixes)
	return out
}

// === 复合 Matcher ===

// CompositeMatcher 组合多个 PathMatcher（OR 语义：任一命中即 Match）。
//
// 用于 .whaleignore 多级（全局 + 项目级）合并场景：
// - 全局 DefaultSensitivePatterns（PrefixMatcher 或 GlobMatcher）
// - 项目级 .whaleignore 自定义（GlobMatcher）
type CompositeMatcher struct {
	mu       sync.RWMutex
	matchers []PathMatcher
}

// NewCompositeMatcher 创建空的 CompositeMatcher。
func NewCompositeMatcher(matchers ...PathMatcher) *CompositeMatcher {
	return &CompositeMatcher{matchers: matchers}
}

// AddMatcher 追加一个子 Matcher。
func (c *CompositeMatcher) AddMatcher(m PathMatcher) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchers = append(c.matchers, m)
}

// AddPattern 透传到所有子 Matcher（任一编译失败即返回 error，已成功的子 Matcher 保留状态）。
func (c *CompositeMatcher) AddPattern(pattern string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, m := range c.matchers {
		if err := m.AddPattern(pattern); err != nil {
			return err
		}
	}
	return nil
}

// Match 任一子 Matcher 命中即返回 true。
func (c *CompositeMatcher) Match(path string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, m := range c.matchers {
		if m.Match(path) {
			return true
		}
	}
	return false
}

// Patterns 合并所有子 Matcher 的 Pattern 列表。
func (c *CompositeMatcher) Patterns() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for _, m := range c.matchers {
		out = append(out, m.Patterns()...)
	}
	return out
}

// === 错误定义 ===

// ErrEmptyPattern 表示 Pattern 为空字符串。
var ErrEmptyPattern = errors.New("PathMatcher: empty pattern")

// === 便捷工厂 ===

// NewPathMatcher 是 C11 仲裁约定的默认工厂（返回 GlobMatcher）。
//
// 调用方约定：
// - 需 prefix 语义时显式 NewPrefixMatcher
// - 需组合语义时显式 NewCompositeMatcher
// - 默认 glob 语义走本工厂
func NewPathMatcher() PathMatcher {
	return NewGlobMatcher()
}

// MatchAny 是包级便捷函数：用一组 glob Pattern 检查 path 是否命中。
//
// 用于一次性检查场景（无需创建 Matcher 实例）。
// Pattern 编译失败时跳过该 Pattern（best-effort，与 .gitignore 行为一致）。
func MatchAny(path string, patterns []string) bool {
	m := NewGlobMatcher()
	for _, p := range patterns {
		_ = m.AddPattern(p) // 编译失败忽略
	}
	return m.Match(path)
}
