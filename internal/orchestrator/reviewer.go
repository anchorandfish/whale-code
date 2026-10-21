// reviewer.go — H10 Phase 3 模块 3 独立审查子对话派发器（H10-Phase3-方案细化-v3.md §6.3）。
// REF-外部-同类CLI@2026-03-31快照-subagent 独立子对话；借鉴逻辑：Task tool 派发不继承父上下文；落地：Reviewer.Review 派发独立子对话
// prompt 设计；落地：ReviewRequest.NoPlan + SanitizeAuthorContext
// ；落地：NoPlan=true 时仅传代码 diff + 契约片段，SanitizeAuthorContext 屏蔽作者上下文
// /020-审查 1 轮硬上限；落地：MaxRound=1 + Review 返回 Round=1
// 复用-替换 review.go noop；落地：Reviewer struct 实现 ReviewSubconversation 接口（不修改 review.go）
// SNAPSHOT: pkg=internal/orchestrator; type=Reviewer; reuse=N1 Agent.Execute(不改签名); no_plan=(屏蔽作者上下文); max_round=1(/020); tier={checklist<50,mid 50-200,full>200或安全契约}; dimensions={3(mid)/8(full)}; sanitize=移除作者文件路径+commit author+方案文档引用; provider=primary降级(Phase3默认)

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// ReviewerConfig 审查配置（Phase 3 §6.3.4 provider 降级）。
type ReviewerConfig struct {
	PrimaryProvider       string // Phase 3 默认：全部走 primary provider
	CriticalDiffThreshold int    // 200 行（FullDim 触发阈值）
	MidDimThreshold       int    // 50 行（MidDim 触发阈值）
}

// DefaultReviewerConfig 默认审查配置（Phase 3 §6.3.4，全部走 primary provider）。
func DefaultReviewerConfig() ReviewerConfig {
	return ReviewerConfig{
		PrimaryProvider:       "primary",
		CriticalDiffThreshold: 200,
		MidDimThreshold:       50,
	}
}

// ReviewTier 审查分级（Phase 3 §6.3.2，依据 03 §3）。
type ReviewTier int

const (
	// ReviewTierChecklist < 50 行，linter + 编译 + 契约检查（无 AI 介入）。
	ReviewTierChecklist ReviewTier = iota
	// ReviewTierMidDim 50-200 行，3 维度审查。
	ReviewTierMidDim
	// ReviewTierFullDim > 200 行或安全/契约变化，8 维度审查。
	ReviewTierFullDim
)

// String 返回 ReviewTier 的字符串表示。
func (t ReviewTier) String() string {
	switch t {
	case ReviewTierChecklist:
		return "checklist"
	case ReviewTierMidDim:
		return "mid_dim"
	case ReviewTierFullDim:
		return "full_dim"
	default:
		return "unknown"
	}
}

// DiffStat 审查 diff 统计（Phase 3 §6.3.2）。
type DiffStat struct {
	TotalLines   int // diff 总行数（新增 + 删除）
	FilesChanged int // 变更文件数
}

// ChangeType 变更类型分类（Phase 3 §6.3.2，由 go/parser AST 检测）。
type ChangeType struct {
	IsInterfaceOrContract bool // 是否涉及接口/契约变化（FuncDecl/TypeSpec/Exported）
	IsSecuritySensitive   bool // 是否安全敏感（crypto/auth/password 等）
}

// Reviewer 独立审查子对话派发器（Phase 3 §6.3，替换 Phase 2 noop 骨架）。
// 核心原则：审查对话不接收方案文档，仅接收任务目标 + 代码 diff + 契约片段。
// 审查轮数（/020）：1 轮硬上限，审查者一次给出全部问题。
// 实现 ReviewSubconversation 接口（review.go 已定义接口，不修改 review.go）。
type Reviewer struct {
	agent  tools.Agent // 复用 N1 Agent.Execute（不改签名，）
	tracer *TraceStore // 审查 trace 存储
	config ReviewerConfig
}

// NewReviewer 创建审查派发器。
// agent 为 N1 Agent 实现（Execute 签名不改）；tracer 为 nil 时跳过 trace 记录。
func NewReviewer(agent tools.Agent, tracer *TraceStore, config ReviewerConfig) *Reviewer {
	if config.CriticalDiffThreshold == 0 {
		config = DefaultReviewerConfig()
	}
	return &Reviewer{
		agent:  agent,
		tracer: tracer,
		config: config,
	}
}

// Review 派发独立审查子对话，1 轮硬上限（/020）。
// 实现 ReviewSubconversation 接口（review.go 已定义接口签名，不改签名）。
// 流程：
// 1. 校验 MaxRound == 1（/020，否则返回 error）
// 2. SanitizeAuthorContext 屏蔽作者上下文（致命反例 3）
// 3. DetectChangeType 用 go/parser AST 识别接口/安全/契约变化
// 4. ClassifyReview 判定审查分级
// 5. 构造审查 prompt（任务目标 + 代码 diff，不含方案设计推理，）
// 6. 调用 agent.Execute(ctx, AgentInput) 派发独立子对话（不改 N1 签名）
// 7. 解析子对话输出为 ReviewReport
// 8. 写审查 trace 到 TraceStore
func (r *Reviewer) Review(ctx context.Context, req ReviewRequest) (*ReviewReport, error) {
	// ① 校验 MaxRound == 1（/020 硬上限）
	if err := ValidateReviewRequest(&req); err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}

	// ② SanitizeAuthorContext 屏蔽作者上下文（致命反例 3，零容忍）
	// 使用 req.AuthorFiles 屏蔽作者文件路径（Phase 3 修复审查 v1 致命漏洞 1）
	sanitized, err := r.SanitizeAuthorContext(req.Content, req.AuthorFiles)
	if err != nil {
		return nil, fmt.Errorf("reviewer: sanitize author context: %w", err)
	}

	// ③ DetectChangeType 用 go/parser AST 识别接口/安全/契约变化
	changeType := DetectChangeType(sanitized)

	// ④ ClassifyReview 判定审查分级
	diffStat := computeDiffStat(sanitized)
	tier := ClassifyReview(diffStat, changeType)

	// ⑤ 构造审查 prompt（：不喂方案，仅传代码 diff + 契约片段）
	reviewPrompt := r.buildReviewPrompt(req, sanitized, tier)

	// 写 review_started trace
	if r.tracer != nil {
		_ = r.tracer.Append(PhaseTraceEvent{
			EventType: Phase3EvtReviewStarted,
			SubtaskID: req.TaskID,
			Payload: map[string]interface{}{
				"tier":             tier.String(),
				"dimensions":       req.Dimensions,
				"content_length":   len(sanitized),
				"interface_change": changeType.IsInterfaceOrContract,
				"security_change":  changeType.IsSecuritySensitive,
			},
		})
	}

	// ⑥ 调用 agent.Execute(ctx, AgentInput) 派发独立子对话（不改 N1 签名，）
	//
	// L2：显式声明角色。子 agent 工具面已由声明的 agent_type 决定，
	// 未声明会落到默认只读角色（explore）。此处取与改动前**一致**的角色（原先按 prompt
	// 推断，审查提示词不含关键字 → coder），避免评审轮次顺带被收紧成只读。
	agentInput := tools.AgentInput{
		Prompt:    reviewPrompt,
		AgentType: string(tools.AgentTypeCoder),
	}
	if !req.NoPlan && req.PlanDoc != "" {
		// noPlan=false 时喂方案（审查方案本身），但仍走 Agent.Execute
		// 使用 sanitized 而非 req.Content（Phase 3 修复审查 v1 致命漏洞 1：noPlan=false 分支也必须屏蔽作者上下文）
		agentInput.Prompt = fmt.Sprintf("审查方案文档：\n%s\n\n任务目标：\n%s", req.PlanDoc, sanitized)
	}

	agentOutput, err := r.agent.Execute(ctx, agentInput)
	if err != nil {
		// 派发失败仍写 trace
		if r.tracer != nil {
			_ = r.tracer.Append(PhaseTraceEvent{
				EventType: Phase3EvtReviewCompleted,
				SubtaskID: req.TaskID,
				Payload: map[string]interface{}{
					"verdict": ReviewVerdictFail,
					"error":   err.Error(),
				},
			})
		}
		return nil, fmt.Errorf("reviewer: agent execute: %w", err)
	}

	// ⑦ 解析子对话输出为 ReviewReport
	report := r.parseReviewReport(agentOutput)
	report.Round = 1 // /020：1 轮硬上限

	// ⑧ 写审查 trace
	if r.tracer != nil {
		_ = r.tracer.Append(PhaseTraceEvent{
			EventType: Phase3EvtReviewCompleted,
			SubtaskID: req.TaskID,
			Payload: map[string]interface{}{
				"verdict":           report.Verdict,
				"fatal_flaws_count": len(report.FatalFlaws),
				"round":             report.Round,
			},
		})
	}

	return report, nil
}

// SanitizeAuthorContext 屏蔽作者上下文（致命反例 3，零容忍）。
// 移除 diff 中的：
// - 作者文件路径（authorFiles 列表中的路径替换为 <author_file_redacted>）
// - commit author 信息（Author: / Commit: 元数据行移除）
// - 方案文档引用（如 "依据 H10-Phase3-方案细化-v3.md" 替换为 <plan_ref_redacted>）
//
// authorFiles 为空时仅做 commit author 与方案文档引用清理。
func (r *Reviewer) SanitizeAuthorContext(diff string, authorFiles []string) (string, error) {
	if diff == "" {
		return "", nil
	}

	sanitized := diff

	// ① 移除作者文件路径（致命反例 3 核心）
	for _, af := range authorFiles {
		if af == "" {
			continue
		}
		// 替换所有出现的作者文件路径
		sanitized = strings.ReplaceAll(sanitized, af, "<author_file_redacted>")
		// 同时处理路径变体（正斜杠/反斜杠）
		alt := strings.ReplaceAll(af, "/", "\\")
		if alt != af {
			sanitized = strings.ReplaceAll(sanitized, alt, "<author_file_redacted>")
		}
	}

	// ② 移除 commit author 信息（Author: / Commit: 行）
	sanitized = removeMetadataLines(sanitized, "Author:")
	sanitized = removeMetadataLines(sanitized, "Commit:")
	sanitized = removeMetadataLines(sanitized, "author:")
	sanitized = removeMetadataLines(sanitized, "commit:")

	// ③ 移除方案文档引用（致命反例 3：方案文档引用泄露作者设计推理）
	sanitized = sanitizePlanDocRefs(sanitized)

	return sanitized, nil
}

// removeMetadataLines 移除以指定前缀开头的行（commit author/commit 元数据清理）。
func removeMetadataLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			continue // 跳过元数据行
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// planDocRefPattern 方案文档引用模式（匹配 .md 引用 + "依据/参考/见" 前缀）。
// 例：依据 H10-Phase3-方案细化-v3.md / 参考 02-跨方向冲突预防与接口契约.md
var planDocRefPatterns = []string{
	"依据",  // 中文「依据」
	"参考",  // 中文「参考」
	"见 ",  // 中文「见」
	"详见",  // 中文「详见」
	"REF", // REF 标签
}

// sanitizePlanDocRefs 移除方案文档引用（替换为 <plan_ref_redacted>）。
// 匹配模式：(依据|参考|见|详见|REF).*\.md
func sanitizePlanDocRefs(text string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		sanitized := line
		// 检测行中是否含方案文档引用
		if strings.Contains(sanitized, ".md") {
			for _, prefix := range planDocRefPatterns {
				if strings.Contains(sanitized, prefix) {
					// 整行替换为 <plan_ref_redacted>
					sanitized = "<plan_ref_redacted>"
					break
				}
			}
		}
		// 检测独立 .md 文件引用（无前缀但路径形如 xxx/yyy.md）
		if strings.Contains(sanitized, ".md") && strings.Contains(sanitized, "/") {
			sanitized = strings.ReplaceAll(sanitized, ".md", ".md<plan_ref_redacted>")
		}
		result = append(result, sanitized)
	}
	return strings.Join(result, "\n")
}

// ClassifyReview 分级判定（Phase 3 §6.3.2，用 go/parser AST 识别接口/安全/契约变化）。
// changeType.IsInterfaceOrContract || changeType.IsSecuritySensitive → FullDim（8 维度）
// diff.TotalLines > CriticalDiffThreshold → FullDim
// diff.TotalLines >= MidDimThreshold → MidDim（3 维度）
// 默认 → Checklist（linter + 编译 + 契约检查，无 AI 介入）
func ClassifyReview(diff DiffStat, changeType ChangeType) ReviewTier {
	// 接口/契约/安全敏感变化 → FullDim
	if changeType.IsInterfaceOrContract || changeType.IsSecuritySensitive {
		return ReviewTierFullDim
	}
	// diff 行数 > 200 → FullDim
	if diff.TotalLines > 200 {
		return ReviewTierFullDim
	}
	// diff 行数 >= 50 → MidDim
	if diff.TotalLines >= 50 {
		return ReviewTierMidDim
	}
	// 默认 → Checklist
	return ReviewTierChecklist
}

// DetectChangeType 用 go/parser AST 检测 diff 中的变更类型（接口/契约/安全敏感）。
// 解析 diff 中 Go 代码片段，识别 FuncDecl/TypeSpec/Exported 变化 → IsInterfaceOrContract
// 路径含 crypto/auth/password → IsSecuritySensitive
func DetectChangeType(diff string) ChangeType {
	ct := ChangeType{}

	// ① 安全敏感检测：路径含 crypto/auth/password
	if isSecuritySensitivePath(diff) {
		ct.IsSecuritySensitive = true
	}

	// ② 接口/契约变化检测：go/parser AST 识别 FuncDecl/TypeSpec
	if hasInterfaceChange(diff) {
		ct.IsInterfaceOrContract = true
	}

	return ct
}

// isSecuritySensitivePath 检测 diff 中是否含安全敏感路径关键词。
// 关键词：crypto / auth / password / secret / token / credential / permission
func isSecuritySensitivePath(diff string) bool {
	securityKeywords := []string{
		"crypto", "auth", "password", "passwd",
		"secret", "token", "credential", "permission",
	}
	lower := strings.ToLower(diff)
	for _, kw := range securityKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// hasInterfaceChange 用 go/parser AST 检测 diff 中的接口/契约变化。
// 识别 FuncDecl（函数声明变化）与 TypeSpec（类型/接口定义变化）。
// 由于 diff 不是完整 Go 文件，提取 diff 中 + 开头的代码行尝试解析。
func hasInterfaceChange(diff string) bool {
	// 从 diff 中提取新增的 Go 代码行（+ 开头）
	codeLines := extractAddedGoCode(diff)
	if codeLines == "" {
		return false
	}

	// 尝试用 go/parser 解析（容错：解析失败不报错，仅返回 false）
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "diff.go", codeLines, parser.ParseComments)
	if err != nil {
		// 解析失败：可能不是完整 Go 文件，降级为字符串检测 FuncDecl 关键字
		return strings.Contains(diff, "func ") && (strings.Contains(diff, "func (") || strings.Contains(diff, "interface"))
	}

	// 遍历 AST 检测 FuncDecl / TypeSpec / InterfaceType
	interfaceDetected := false
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			// 函数声明变化 → 接口契约变化
			if node.Name != nil && ast.IsExported(node.Name.Name) {
				interfaceDetected = true
				return false
			}
		case *ast.TypeSpec:
			// 类型/接口定义变化 → 契约变化
			if node.Name != nil && ast.IsExported(node.Name.Name) {
				interfaceDetected = true
				return false
			}
		case *ast.InterfaceType:
			// interface 类型定义 → 契约变化
			interfaceDetected = true
			return false
		}
		return true
	})

	return interfaceDetected
}

// extractAddedGoCode 从 diff 中提取新增的 Go 代码行（+ 开头，去掉 + 前缀）。
// 同时收集 @@ hunk 头之后的代码以保持上下文完整性。
func extractAddedGoCode(diff string) string {
	lines := strings.Split(diff, "\n")
	var codeLines []string
	for _, line := range lines {
		// 跳过 diff 元数据行
		if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "@@") {
			continue
		}
		// 提取新增行（+ 开头但非 ++）
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "++") {
			codeLines = append(codeLines, strings.TrimPrefix(line, "+"))
		}
		// 同时保留上下文行（空格开头）以保持代码可解析
		if strings.HasPrefix(line, " ") {
			codeLines = append(codeLines, strings.TrimPrefix(line, " "))
		}
	}
	return strings.Join(codeLines, "\n")
}

// computeDiffStat 计算 diff 的行数与文件数统计。
func computeDiffStat(diff string) DiffStat {
	stat := DiffStat{}
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		// 新增行（+ 开头但非 +++）
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			stat.TotalLines++
		}
		// 删除行（- 开头但非 ---）
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			stat.TotalLines++
		}
		// 文件变化行（diff --git 或 +++）
		if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "+++") {
			stat.FilesChanged++
		}
	}
	return stat
}

// buildReviewPrompt 构造审查 prompt（：不喂方案，仅传代码 diff + 契约片段）。
// NoPlan=true 时不含方案设计推理（致命反例 3 防护）。
func (r *Reviewer) buildReviewPrompt(req ReviewRequest, sanitizedContent string, tier ReviewTier) string {
	var prompt strings.Builder

	prompt.WriteString("你是独立审查子对话。请审查以下代码变更。\n\n")
	prompt.WriteString("审查原则（不可违反）：\n")
	prompt.WriteString("1. 0 个致命漏洞就是 0 个，不许凑数。\n")
	prompt.WriteString("2. 只跑 1 轮，一次给出全部问题。\n")
	prompt.WriteString("3. 可复现场景 + 官方源证据。\n\n")

	prompt.WriteString(fmt.Sprintf("审查分级：%s\n", tier.String()))
	if tier == ReviewTierFullDim {
		prompt.WriteString("8 维度审查：正确性/安全性/性能/可维护性/契约一致性/错误处理/资源管理/可测试性。\n\n")
	} else if tier == ReviewTierMidDim {
		prompt.WriteString("3 维度审查：正确性/安全性/契约一致性。\n\n")
	} else {
		prompt.WriteString("Checklist 审查：linter + 编译 + 契约检查。\n\n")
	}

	prompt.WriteString(fmt.Sprintf("任务 ID：%s\n", req.TaskID))
	prompt.WriteString(fmt.Sprintf("Stage：%s\n\n", req.Stage))

	// NoPlan=true 时不喂方案
	if req.NoPlan {
		prompt.WriteString("代码 diff（仅代码，不喂方案）：\n")
		prompt.WriteString("```\n")
		prompt.WriteString(sanitizedContent)
		prompt.WriteString("\n```\n")
	} else {
		// noPlan=false 时喂方案（审查方案本身）
		prompt.WriteString("审查对象（含方案文档）：\n")
		prompt.WriteString("```\n")
		prompt.WriteString(sanitizedContent)
		prompt.WriteString("\n```\n")
	}

	prompt.WriteString("\n输出格式（JSON）：\n")
	prompt.WriteString(`{"verdict":"pass|fail","fatal_flaws":[],"suggestions":[],"evidence_urls":[]}`)
	prompt.WriteString("\n")

	return prompt.String()
}

// parseReviewReport 解析 Agent 输出为 ReviewReport。
// Agent 输出应为 JSON 格式，解析失败时降级为基于文本的 verdict 判定。
func (r *Reviewer) parseReviewReport(output tools.AgentOutput) *ReviewReport {
	report := &ReviewReport{
		Verdict:      ReviewVerdictPass,
		FatalFlaws:   []string{},
		Suggestions:  []string{},
		EvidenceURLs: []string{},
		Round:        1, // /020：1 轮硬上限
	}

	// 尝试 JSON 解析
	result := strings.TrimSpace(output.Result)
	if result == "" {
		return report
	}

	// 提取 JSON 块（可能包裹在 ```json ... ``` 中）
	jsonStr := extractJSONBlock(result)
	if jsonStr == "" {
		// 降级：基于文本判定 verdict
		lower := strings.ToLower(result)
		if strings.Contains(lower, "verdict: fail") || strings.Contains(lower, `"verdict":"fail"`) {
			report.Verdict = ReviewVerdictFail
		}
		return report
	}

	// 解析 JSON
	var parsed struct {
		Verdict      string   `json:"verdict"`
		FatalFlaws   []string `json:"fatal_flaws"`
		Suggestions  []string `json:"suggestions"`
		EvidenceURLs []string `json:"evidence_urls"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		// 解析失败，降级为 pass
		return report
	}

	if parsed.Verdict == ReviewVerdictFail || parsed.Verdict == "fail" {
		report.Verdict = ReviewVerdictFail
	}
	if len(parsed.FatalFlaws) > 0 {
		report.FatalFlaws = parsed.FatalFlaws
	}
	if len(parsed.Suggestions) > 0 {
		report.Suggestions = parsed.Suggestions
	}
	if len(parsed.EvidenceURLs) > 0 {
		report.EvidenceURLs = parsed.EvidenceURLs
	}
	return report
}

// extractJSONBlock 从可能包含 markdown 代码块的文本中提取 JSON 字符串。
func extractJSONBlock(text string) string {
	// 尝试 ```json ... ``` 块
	if idx := strings.Index(text, "```json"); idx >= 0 {
		start := idx + len("```json")
		end := strings.Index(text[start:], "```")
		if end > 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	// 尝试 ``` ... ``` 块
	if idx := strings.Index(text, "```"); idx >= 0 {
		start := idx + len("```")
		// 跳过可能的语言标识
		if start < len(text) && text[start] != '\n' && text[start] != '`' {
			nl := strings.Index(text[start:], "\n")
			if nl > 0 {
				start = start + nl + 1
			}
		}
		end := strings.Index(text[start:], "```")
		if end > 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	// 尝试直接解析整个文本为 JSON
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed
	}
	return ""
}

// Compile-time assertion: Reviewer 实现 ReviewSubconversation 接口（review.go 已定义接口）。
// 不修改 review.go，仅通过实现接口方法 Review 来满足契约。
var _ ReviewSubconversation = (*Reviewer)(nil)
