// Package powershell — 路径提取与验证（T4）。
//
// 从 PowerShell 命令提取 -Path/-LiteralPath/-Destination/-NewName/-FilePath
// 等参数的路径值，供沙箱路径校验使用。正则匹配参数化的路径，无法解析变量间接引用
// （由三层权限 C19 + 沙箱 C1 兜底）。
package powershell

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// ExtractPaths 从命令提取所有路径参数值。
func ExtractPaths(command string) []string {
	var paths []string
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)-(?:Path|LiteralPath|PSPath)\s+["']?([^"'\s]+)["']?`),
		regexp.MustCompile(`(?i)-(?:Destination)\s+["']?([^"'\s]+)["']?`),
		regexp.MustCompile(`(?i)-(?:NewName)\s+["']?([^"'\s]+)["']?`),
		regexp.MustCompile(`(?i)-(?:FilePath|FullName)\s+["']?([^"'\s]+)["']?`),
		regexp.MustCompile(`(?i)-(?:Path|LiteralPath|Destination|FilePath|NewName|FullName)=["']?([^"'\s]+)["']?`),
	}
	for _, p := range patterns {
		for _, m := range p.FindAllStringSubmatch(command, -1) {
			if len(m) > 1 && m[1] != "" {
				paths = append(paths, m[1])
			}
		}
	}
	return paths
}

// PathValidator PowerShell 路径验证器。
type PathValidator struct {
	workDir string
}

// NewPathValidator 创建路径验证器。
func NewPathValidator(workDir string) *PathValidator {
	return &PathValidator{workDir: workDir}
}

// Validate 验证路径安全性。
// 顺序：UNC 拦截 → 路径遍历 → 注册表路径 → 工作目录约束。
func (v *PathValidator) Validate(path string) error {
	// 1. UNC 路径拦截（T8）
	if strings.HasPrefix(path, `\\`) {
		return fmt.Errorf("UNC path not allowed: %s", path)
	}
	// 2. 路径遍历检测（T8）
	if strings.Contains(path, `..\`) || strings.Contains(path, `../`) {
		return fmt.Errorf("path traversal detected: %s", path)
	}
	// 3. 注册表路径拦截
	if regexp.MustCompile(`(?i)^(HKLM|HKCU|HKCR|HKU|HKCC):`).MatchString(path) {
		return fmt.Errorf("registry path not allowed: %s", path)
	}
	// 4. 工作目录约束
	// ★ 2026-09-19 收敛（同一语义多处实现）：原实现为
	// strings.HasPrefix(strings.ToLower(absPath), strings.ToLower(workDir))
	// —— **裸前缀、无分隔符边界** + 无条件 ToLower。后果两条：
	// ① `…/proj-secret/x` 会被判为在 `…/proj` 之内（与 filehistory.safePath 同源缺陷）；
	// ② 在 Linux（大小写敏感）上会把 `/WORK/FOO` 当作 `/work/foo` —— 那其实是**另一个目录**。
	// 本函数位于**工具执行门**上，影响比 filehistory 更直接，故一并收敛。
	// 现复用 platform.ResolveWithin（SSOT）：分隔符边界 + 按文件系统的相对大小写 + symlink 解析。
	if v.workDir != "" {
		target := path
		// Windows 上 `/x`（无盘符）锚到当前盘根而非工作目录，
		// 锚定判定收口 platform.AnchorsToWorkDir（单一声明）。
		if platform.AnchorsToWorkDir(target) {
			target = filepath.Join(v.workDir, target)
		}
		if _, err := platform.ResolveWithin(target, []string{v.workDir}); err != nil {
			return fmt.Errorf("path outside workdir: %s: %w", path, err)
		}
	}
	return nil
}

// ValidateAll 批量验证路径，返回第一个错误。
func (v *PathValidator) ValidateAll(paths []string) error {
	for _, p := range paths {
		if err := v.Validate(p); err != nil {
			return err
		}
	}
	return nil
}
