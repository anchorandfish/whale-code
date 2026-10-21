package prompt

import (
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/anchorandfish/whale-code/internal/config"
)

// MergeMagicDocs 合并 V2 指令文件 + conditional rules，返回合并后的 project context 文本。
// 保留 V2 现有层级合并逻辑（depth 大在前=低优先级），新增显式 override 覆盖语义。
// Windows 路径大小写归一化（，H6 横切主题）。
func MergeMagicDocs(workDir string, magicDocs *config.MagicDocs) string {
	instructionFiles := config.DiscoverInstructionFiles(workDir)
	if len(instructionFiles) > 0 {
		rulesDir := filepath.Join(workDir, ".whale", "rules")

		// 按目录层级分组实现 override 覆盖语义（，P1-6）：
		// 同层（同 filepath.Dir）若存在 override 文件，仅保留 override 内容，抑制非 override 文件。
		// DiscoverInstructionFiles 已按 depth 排序（depth 大在前=低优先级），保持该顺序。
		type groupEntry struct {
			file     config.InstructionFile
			resolved string
		}
		groups := make(map[string][]groupEntry)
		var groupOrder []string
		for _, f := range instructionFiles {
			dir := filepath.Dir(f.Path)
			if _, ok := groups[dir]; !ok {
				groupOrder = append(groupOrder, dir)
			}
			resolved := config.ResolveIncludes(f.Content, dir, 5, nil)
			for _, rule := range matchConditionalRulesNormalized(rulesDir, f.Path) {
				resolved += "\n" + rule.Content
			}
			groups[dir] = append(groups[dir], groupEntry{file: f, resolved: resolved})
		}

		var ctxParts []string
		for _, dir := range groupOrder {
			entries := groups[dir]
			// 判断该层是否存在 override 文件
			hasOverride := false
			for _, e := range entries {
				if isOverrideFile(e.file.Path) {
					hasOverride = true
					break
				}
			}
			for _, e := range entries {
				// 若该层有 override 文件，跳过非 override 文件（抑制，P1-6）
				if hasOverride && !isOverrideFile(e.file.Path) {
					log.Printf("[prompt] override 抑制同层非 override 文件: %s", e.file.Path)
					continue
				}
				ctxParts = append(ctxParts, e.resolved)
			}
		}
		return strings.Join(ctxParts, "\n\n")
	}
	// V1 兜底：V2 DiscoverInstructionFiles 未发现指令文件时回退 V1 MagicDocs.ProjectContext。
	// V1 已 deprecated（H4 方案文档 /052），保留作为 fallback 与简单项目场景。
	if magicDocs == nil {
		return ""
	}
	return magicDocs.ProjectContext() //nolint:staticcheck // H4 保留 V1 作为 fallback
}

// matchConditionalRulesNormalized 在 prompt 包内包装 config.MatchConditionalRules，
// 在匹配前对 target path 做 normalizePathForMatch（Windows ToLower + ToSlash）。
// 不修改 config 包签名，避免与 H4 V1/V2 双套、H6 Windows 路径横切主题边界冲突（P1-5）。
// 注意：pattern 侧的大小写归一化需规则文件编写约定（建议小写 pattern），
// 或待 H6 NormalizePath 实施后统一在 config.MatchConditionalRules 内处理。
func matchConditionalRulesNormalized(rulesDir, targetFile string) []config.ConditionalRule {
	return config.MatchConditionalRules(rulesDir, normalizePathForMatch(targetFile))
}

// isOverrideFile 判断文件是否为 override 文件（名称含 .override. 或 .local.）。
// 对标同类 CLI AGENTS.override.md。
func isOverrideFile(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, ".override.") || strings.Contains(base, ".local.")
}

// normalizePathForMatch 归一化路径用于 glob 匹配。
// Windows 下大小写不敏感，ToLower；所有平台 ToSlash。
// 决策：保留 doublestar glob + Windows 大小写归一化。
func normalizePathForMatch(path string) string {
	// 始终将反斜杠转为正斜杠，处理跨平台路径（Windows 路径在 Linux 上也需归一化）
	normalized := strings.ReplaceAll(path, "\\", "/")
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	return normalized
}
