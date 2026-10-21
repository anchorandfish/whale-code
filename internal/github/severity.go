package github

import (
	"fmt"
	"strings"
)

// Severity 严重度枚举，表示审查发现的严重程度。
// 用于多 Agent 审查结果的分级（对标同类 CLI 的 blocker/warning/suggestion 三级分级）。
type Severity string

const (
	// SeverityBlocker 阻塞级：会导致 bug、安全漏洞、数据丢失，必须修复。
	SeverityBlocker Severity = "blocker"
	// SeverityWarning 警告级：不影响功能但有风险，建议修复。
	SeverityWarning Severity = "warning"
	// SeveritySuggestion 建议级：代码质量、可读性、性能优化，可选。
	SeveritySuggestion Severity = "suggestion"
)

// Emoji 返回严重度对应的 emoji 标识。
// blocker → 🔴，warning → 🟡，suggestion → 🟢，未知 → ⚪。
func (s Severity) Emoji() string {
	switch s {
	case SeverityBlocker:
		return "🔴"
	case SeverityWarning:
		return "🟡"
	case SeveritySuggestion:
		return "🟢"
	}
	return "⚪"
}

// Rank 返回严重度排序权重，数值越大越严重。
// 用于 SeverityRanker 按严重度降序排序：blocker(3) > warning(2) > suggestion(1)。
func (s Severity) Rank() int {
	switch s {
	case SeverityBlocker:
		return 3
	case SeverityWarning:
		return 2
	case SeveritySuggestion:
		return 1
	}
	return 0
}

// String 返回严重度的字符串形式。
func (s Severity) String() string { return string(s) }

// NormalizeSeverity 将字符串归一化为合法的 Severity。
// 兼容 AI 可能输出的近义词（critical/error/warn/info/minor 等）；
// 未知字符串回退为 SeveritySuggestion（最保守，避免过度告警）。
func NormalizeSeverity(s string) Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocker", "critical", "error":
		return SeverityBlocker
	case "warning", "warn":
		return SeverityWarning
	case "suggestion", "info", "minor":
		return SeveritySuggestion
	}
	return SeveritySuggestion
}

// LegalSeverities 返回全部合法的严重度枚举值（blocker/warning/suggestion）。
// 用于校验与文档展示。
func LegalSeverities() []Severity {
	return []Severity{SeverityBlocker, SeverityWarning, SeveritySuggestion}
}

// ValidateSeverity 严格校验严重度是否为合法的三级之一。
// 与 NormalizeSeverity 不同：非法值返回错误而非静默回退，
// 用于在发布评论前拦截 AI 输出的异常严重度（P0-3 验收：三级分级正确）。
func ValidateSeverity(s Severity) error {
	switch s {
	case SeverityBlocker, SeverityWarning, SeveritySuggestion:
		return nil
	}
	return fmt.Errorf("invalid severity %q: must be one of blocker/warning/suggestion", s)
}

// ParseSeverity 严格解析严重度字符串，非法值返回错误。
// 用于解析 AI 的 JSON 输出时显式校验，避免静默吞掉异常值。
func ParseSeverity(s string) (Severity, error) {
	sev := Severity(strings.ToLower(strings.TrimSpace(s)))
	if err := ValidateSeverity(sev); err != nil {
		return "", err
	}
	return sev, nil
}

// ValidateFinding 校验单条发现是否符合结构化 JSON 契约。
// 契约（P0-3）：severity（合法三级）/ file（非空）/ line（>=0）/ title（非空）。
// body 与 suggestion 可空（允许仅标题的精简发现）。
func ValidateFinding(f Finding) error {
	if err := ValidateSeverity(f.Severity); err != nil {
		return fmt.Errorf("finding severity: %w", err)
	}
	if f.File == "" {
		return fmt.Errorf("finding file is empty")
	}
	if f.Line < 0 {
		return fmt.Errorf("finding line is negative: %d", f.Line)
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("finding title is empty")
	}
	return nil
}

// severityJSONSchema 描述 AI 必须输出的结构化 JSON 契约（P0-3 强制 JSON 输出）。
// 该 schema 已嵌入 agentSystemPromptTemplate，确保 AI 返回结构化字段而非 Markdown。
const severityJSONSchema = `{"severity":"blocker|warning|suggestion","file":"path","line":0,"column":0,"title":"short","body":"detail","suggestion":"fix"}`
