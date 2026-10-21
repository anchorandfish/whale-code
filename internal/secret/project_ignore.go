// Package secret — 项目级 .whaleignore 自定义（/ M15-04，复用 C11 PathMatcher）。
//
// 设计目标（T0-2 §2 C11 仲裁 + M15-04 改造）：
// - 支持项目根目录 .whaleignore 文件自定义敏感文件 glob
// - 复用 C11 PathMatcher（GlobMatcher）实现统一匹配语义
// - 对标 .gitignore 多级（全局 + 项目级 + 工作区级）
// - 双层防护：glob 命中 → blocked；正则兜底（ScanSecrets）→ blocked
//
// 文件格式（对标 .gitignore，C11 GlobMatcher 语法）：
// - 每行一个 glob Pattern
// - # 开头为注释（行内 # 不识别为注释）
// - 空行忽略
// - ! 取反（先匹配后排除）
// - ** 递归目录
// - *.pem / .env* / .aws/credentials / secrets/** 等任意 glob
//
// 加载顺序（多级合并，对标 .gitignore）：
// 1. 全局 DefaultSensitivePatterns（whaleignore.go 中硬编码）
// 2. ~/.whale/whaleignore（用户全局自定义）
// 3. <projectRoot>/.whaleignore（项目级自定义）
//
// 性能：Pattern 编译时一次性，匹配时 O(n)（n=Pattern 数量 × path 长度）
package secret

import (
	"bufio"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// ProjectIgnore 是项目级 .whaleignore 自定义匹配器（SNAPSHOT）。
//
// 通过 CompositeMatcher 组合：
// - 内置 DefaultSensitivePatterns（GlobMatcher）
// - 项目级 .whaleignore 自定义（GlobMatcher）
//
// 不直接提供 ScanSecrets 正则兜底（调用方仍需调 ScanSecrets 做第二重检测）。
type ProjectIgnore struct {
	mu          sync.RWMutex
	matcher     sandbox.PathMatcher // 复合 matcher（C11 GlobMatcher）
	projectRoot string
	loaded      bool
}

// NewProjectIgnore 创建项目级 .whaleignore 匹配器。
//
// projectRoot 为项目根目录路径；若 .whaleignore 不存在，仅使用内置默认 Pattern。
func NewProjectIgnore(projectRoot string) *ProjectIgnore {
	matcher := sandbox.NewGlobMatcher()
	// 加载内置默认 Pattern（whaleignore.go 中的 DefaultSensitivePatterns）
	for _, p := range DefaultSensitivePatterns {
		_ = matcher.AddPattern(p)
	}
	pi := &ProjectIgnore{
		matcher:     matcher,
		projectRoot: projectRoot,
	}
	// 尝试加载项目级 .whaleignore
	_ = pi.loadProjectIgnore()
	// 尝试加载用户全局 ~/.whale/whaleignore
	_ = pi.loadUserGlobalIgnore()
	return pi
}

// IsSensitive 检查文件是否命中敏感 Pattern。
//
// 双层防护中的第一重（glob 匹配）；调用方仍应调用 ScanSecrets 做正则兜底。
func (p *ProjectIgnore) IsSensitive(file string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	// 转为 slash 路径以匹配 glob 语义
	cleaned := filepath.ToSlash(filepath.Clean(file))
	return p.matcher.Match(cleaned)
}

// Patterns 返回当前生效的所有 Pattern（内置 + 项目级 + 用户全局）。
func (p *ProjectIgnore) Patterns() []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.matcher.Patterns()
}

// AddPattern 动态追加 Pattern（运行时扩展，如配置热重载）。
func (p *ProjectIgnore) AddPattern(pattern string) error {
	if p == nil {
		return fmt.Errorf("ProjectIgnore is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.matcher.AddPattern(pattern)
}

// Reload 重新加载项目级 + 用户全局 .whaleignore。
//
// 用于配置文件变更后热重载（C14 HotReloader 触发）。
// 加载失败不报错，仅返回 error 供调用方记录日志。
func (p *ProjectIgnore) Reload() error {
	if p == nil {
		return fmt.Errorf("ProjectIgnore is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// 重置 matcher，仅保留内置默认
	matcher := sandbox.NewGlobMatcher()
	for _, pattern := range DefaultSensitivePatterns {
		_ = matcher.AddPattern(pattern)
	}
	p.matcher = matcher
	p.loaded = false
	// 重新加载
	if err := p.loadProjectIgnore(); err != nil {
		return err
	}
	return p.loadUserGlobalIgnore()
}

// loadProjectIgnore 加载 <projectRoot>/.whaleignore。
//
// 文件不存在不报错（返回 nil）。文件存在但解析失败时返回 error。
func (p *ProjectIgnore) loadProjectIgnore() error {
	if p.projectRoot == "" {
		return nil
	}
	path := filepath.Join(p.projectRoot, ".whaleignore")
	return p.loadIgnoreFile(path)
}

// loadUserGlobalIgnore 加载 ~/.whale/whaleignore。
//
// 文件不存在不报错（返回 nil）。
func (p *ProjectIgnore) loadUserGlobalIgnore() error {
	// 收口：Dir() 返回空（home 不可得）⇒ 跳过，行为同前。
	if whalehome.Dir() == "" {
		return nil
	}
	path := filepath.Join(whalehome.Dir(), "whaleignore")
	return p.loadIgnoreFile(path)
}

// loadIgnoreFile 解析单个 .whaleignore 文件并追加 Pattern。
//
// 文件格式：
// - 每行一个 Pattern（去空白）
// - # 开头为注释（行首 #）
// - 空行忽略
// - 末尾 \r\n 自动去除 \r
//
// 解析失败不中断（best-effort，与 .gitignore 行为一致）。
func (p *ProjectIgnore) loadIgnoreFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在不报错
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		// 跳过空行与注释
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 追加 Pattern（编译失败忽略，best-effort）
		if err := p.matcher.AddPattern(trimmed); err != nil {
			// 记录但不中断
			_ = err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	p.loaded = true
	return nil
}

// ScanWithProjectIgnore 是 ScanWhaleIgnore 的增强版，支持项目级 .whaleignore。
//
// 三重防护（M15-04）：
// 1. 全局 DefaultSensitivePatterns + 项目级 .whaleignore（C11 GlobMatcher）
// 2. 密钥特征正则兜底（ScanSecrets）
// 3. （可选）密钥强度评估提示（EvaluateStrength，仅当命中时）
//
// 使用场景：文件读写前的敏感文件检测，区分 safe/blocked 两组。
func ScanWithProjectIgnore(files []string, pi *ProjectIgnore) (safe, blocked []string) {
	safe = make([]string, 0, len(files))
	blocked = make([]string, 0)

	for _, file := range files {
		if isSensitiveWithProject(file, pi) {
			blocked = append(blocked, file)
			continue
		}
		safe = append(safe, file)
	}
	return safe, blocked
}

// isSensitiveWithProject 双重防护检测单个文件是否敏感。
func isSensitiveWithProject(file string, pi *ProjectIgnore) bool {
	// 第一重：glob 匹配（项目级 + 全局）
	if pi != nil && pi.IsSensitive(file) {
		return true
	}
	// 兼容 nil pi：回退到内置默认 Pattern
	if pi == nil {
		base := filepath.Base(file)
		for _, pattern := range DefaultSensitivePatterns {
			if matchSensitivePattern(base, pattern) || matchSensitivePattern(file, pattern) {
				return true
			}
		}
	}
	// 第二重：密钥特征正则兜底
	base := filepath.Base(file)
	if IsSecretLeak(base) {
		return true
	}
	return false
}

// === 全局 ProjectIgnore 单例（启动阶段注入） ===

// globalProjectIgnore 是全局 ProjectIgnore 实例。
var globalProjectIgnore *ProjectIgnore

// SetGlobalProjectIgnore 注入全局 ProjectIgnore 实例。
// 应在启动阶段调用一次（main.go / tui/app.go），projectRoot 为当前工作目录。
func SetGlobalProjectIgnore(projectRoot string) {
	globalProjectIgnore = NewProjectIgnore(projectRoot)
}

// GetGlobalProjectIgnore 返回全局 ProjectIgnore 实例（未注入时返回 nil）。
func GetGlobalProjectIgnore() *ProjectIgnore {
	return globalProjectIgnore
}

// IsSensitiveFile 是包级便捷函数：用全局 ProjectIgnore 检查文件是否敏感。
//
// 未注入全局实例时回退到内置默认 Pattern（与 ScanWhaleIgnore 行为一致）。
func IsSensitiveFile(file string) bool {
	pi := globalProjectIgnore
	if pi == nil {
		// 回退到内置默认
		base := filepath.Base(file)
		for _, pattern := range DefaultSensitivePatterns {
			if matchSensitivePattern(base, pattern) || matchSensitivePattern(file, pattern) {
				return true
			}
		}
		if IsSecretLeak(base) {
			return true
		}
		return false
	}
	return pi.IsSensitive(file)
}
