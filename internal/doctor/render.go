package doctor

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	versionpkg "github.com/anchorandfish/whale-code/internal/version"
)

// Render 将检测结果渲染为树形文本格式。
// 对标同类 CLI Doctor.tsx 的树形 + 颜色渲染（└ 前缀），
// 适配 whale-code 的纯文本 TUI 输出（非 bubbletea 全屏组件）。
// 渲染格式示例见改进方案 §1.5。
func (d *Doctor) Render(results []CheckResult) string {
	var sb strings.Builder

	// 头部：版本信息
	sb.WriteString(d.renderHeader())
	sb.WriteString("\n")

	if len(results) == 0 {
		sb.WriteString("whale-code doctor: no checks completed (context canceled or all checks failed)\n")
		return sb.String()
	}

	// 按分类分组渲染（动态分组，不再硬编码白名单）
	// 修复：原硬编码 5 类白名单导致 Network/Recovery/Lock/Resource 4 类结果
	// 计入 Summary 统计但不显示，用户看到 "1 warnings" 却找不到来源。
	// 改为按 results 实际出现的 Category 动态分组，保留与 Run() 执行顺序一致的语义顺序。
	categoryResults := make(map[string][]CheckResult)
	for _, r := range results {
		categoryResults[r.Category] = append(categoryResults[r.Category], r)
	}

	// preferredOrder 与 doctor.Run() 执行顺序对齐（Environment→Config→Dependencies→Platform→Recovery→Lock→Resource→Network→API）
	preferredOrder := []string{"Environment", "Config", "Dependencies", "Platform", "Recovery", "Lock", "Resource", "Network", "API"}
	orderedCats := buildOrderedCategories(categoryResults, preferredOrder)
	for _, cat := range orderedCats {
		sb.WriteString(d.renderCategory(cat, categoryResults[cat]))
		sb.WriteString("\n")
	}

	// 汇总统计
	sb.WriteString(d.renderSummary(results))
	return sb.String()
}

// renderHeader 渲染头部信息（版本 + Go + 平台）。
// 版本号来自 internal/version.Version，与 --version / doctor 检查项同源。
func (d *Doctor) renderHeader() string {
	return fmt.Sprintf("whale-code doctor v%s (%s, %s/%s)",
		versionpkg.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// renderCategory 渲染单个分类区块（标题 + 检测项列表）。
func (d *Doctor) renderCategory(category string, items []CheckResult) string {
	var sb strings.Builder
	// 平台分类追加 OS 标注
	title := category
	if category == "Platform" {
		title = fmt.Sprintf("Platform (%s)", runtime.GOOS)
	}
	sb.WriteString(title + "\n")
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("└ [%s] %s: %s\n",
			item.Severity.icon(), item.Name, item.Message))
		if item.Detail != "" {
			sb.WriteString(fmt.Sprintf("    detail: %s\n", item.Detail))
		}
		if item.FixSuggest != "" {
			sb.WriteString(fmt.Sprintf("    fix: %s\n", item.FixSuggest))
		}
	}
	return sb.String()
}

// renderSummary 渲染底部汇总统计（fatal/errors/warnings/ok 计数）。
func (d *Doctor) renderSummary(results []CheckResult) string {
	var fatal, warn, errCount, ok int
	for _, r := range results {
		switch r.Severity {
		case SeverityFatal:
			fatal++
		case SeverityError:
			errCount++
		case SeverityWarn:
			warn++
		case SeverityOK:
			ok++
		}
	}
	parts := []string{}
	if fatal > 0 {
		parts = append(parts, fmt.Sprintf("%d fatal", fatal))
	}
	if errCount > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", errCount))
	}
	if warn > 0 {
		parts = append(parts, fmt.Sprintf("%d warnings", warn))
	}
	parts = append(parts, fmt.Sprintf("%d ok", ok))
	return fmt.Sprintf("Summary: %s\n", strings.Join(parts, ", "))
}

// buildOrderedCategories 构建分类的渲染顺序。
// 已知分类按 preferredOrder 排列（与 doctor.Run() 执行顺序对齐），
// 未知分类（未来新增）按字母序追加在末尾，确保不丢失且输出稳定。
// 替代原硬编码白名单，让所有检测类目均可见。
func buildOrderedCategories(categoryResults map[string][]CheckResult, preferredOrder []string) []string {
	// 收集 results 中实际出现的分类
	seen := make(map[string]bool, len(categoryResults))
	for cat := range categoryResults {
		seen[cat] = true
	}
	ordered := make([]string, 0, len(seen))
	// 先按 preferredOrder 输出已知分类
	for _, c := range preferredOrder {
		if seen[c] {
			ordered = append(ordered, c)
			delete(seen, c)
		}
	}
	// 未知分类按字母序追加（避免 map 遍历无序导致输出抖动）
	unknown := make([]string, 0, len(seen))
	for c := range seen {
		unknown = append(unknown, c)
	}
	sort.Strings(unknown)
	ordered = append(ordered, unknown...)
	return ordered
}
