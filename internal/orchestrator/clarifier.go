// clarifier.go — H10 Phase 6 clarify Stage 业务逻辑（S1）。
// 调 LLM 分析需求生成 1-3 个澄清问题，合并用户答案到 requirement。
//
// §2.1 S1 Clarifier；落地：Clarifier struct + Generate + MergeAnswers
// ；落地：Options ≤4 个（防 LLM 列举攻击）
// 7-Prompt injection 防御；落地：输出前加 "[LLM 生成]" 标识行
// SNAPSHOT: pkg=internal/orchestrator; type=Clarifier; fields={llm:AIClient,tracer:*TraceStore}; methods={Generate(ctx,requirement)([]ClarifyQuestion,error),MergeAnswers(requirement,answers)string}; output_mark="[LLM 生成]"; max_questions=3; max_options=4; degrade=LLM失败返回error(调用方降级跳过clarify); empty_questions=直接进plan(§11.2边界case)

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ClarifyQuestionMaxNum clarify Stage 单次最多生成的问题数（红队约束，防 LLM 列举攻击）。
const ClarifyQuestionMaxNum = 3

// ClarifyOptionMaxNum clarify Stage 单个问题最多选项数（红队约束）。
const ClarifyOptionMaxNum = 4

// ClarifyPromptInjectionMark Clarifier 输出前的 prompt injection 防御标识（审查优化 7）。
const ClarifyPromptInjectionMark = "[LLM 生成] 以下问题由 AI 根据你的需求生成，请审阅："

// ClarifyQuestion 澄清问题结构（S1）。
// Options 为可选字段，≤4 个（红队约束）；Reason 记录 LLM 生成此问题的理由。
type ClarifyQuestion struct {
	ID       string          `json:"id"`
	Question string          `json:"question"`
	Options  []ClarifyOption `json:"options,omitempty"` // 可选，≤4 个
	Reason   string          `json:"reason"`            // LLM 生成此问题的理由
}

// ClarifyOption 澄清问题选项（S1）。
type ClarifyOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ClarifyAnswer 用户对澄清问题的回答（S1）。
// Value 为选项 Value 或自由输入文本（Options 为空时）。
type ClarifyAnswer struct {
	QuestionID string `json:"question_id"`
	Value      string `json:"value"`
}

// Clarifier clarify Stage 业务逻辑：调 LLM 分析需求生成澄清问题（S1）。
type Clarifier struct {
	llm    AIClient    // 复用 S3 适配器
	tracer *TraceStore // 写 trace.jsonl
}

// NewClarifier 构造 Clarifier（S1）。
// llm 为 nil 时 Generate 返回 error（调用方降级跳过 clarify）。
// tracer 为 nil 时跳过 trace 写入（不影响业务逻辑）。
func NewClarifier(llm AIClient, tracer *TraceStore) *Clarifier {
	return &Clarifier{llm: llm, tracer: tracer}
}

// clarifyResponse LLM 返回的 JSON 结构（parseClarifyResponse 解析）。
type clarifyResponse struct {
	Questions []ClarifyQuestion `json:"questions"`
}

// Generate 调 LLM 生成 1-3 个澄清问题（基于 requirement，S1）。
// 返回空 slice 时不走 retry_once，直接进 plan Stage（边界 case，§11.2）。
// LLM 调用失败时返回 error，由调用方（HandleClarify）决定降级跳过 clarify。
func (c *Clarifier) Generate(ctx context.Context, requirement string) ([]ClarifyQuestion, error) {
	if c == nil {
		return nil, fmt.Errorf("clarifier: nil receiver")
	}
	if strings.TrimSpace(requirement) == "" {
		return nil, fmt.Errorf("clarifier: requirement must not be empty")
	}
	if c.llm == nil {
		return nil, fmt.Errorf("clarifier: llm client not configured")
	}

	// 写 trace：clarify 开始
	c.appendTrace("clarify_started", map[string]interface{}{
		"requirement_length": len(requirement),
	})

	// 构造 prompt
	prompt := c.buildPrompt(requirement)

	// 调 LLM
	output, err := c.llm.Complete(ctx, prompt)
	if err != nil {
		// 写 trace：clarify 失败
		c.appendTrace("clarify_failed", map[string]interface{}{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("clarifier: llm complete: %w", err)
	}

	// 解析 LLM 输出
	questions, parseErr := c.parseResponse(output)
	if parseErr != nil {
		// 写 trace：clarify 解析失败
		c.appendTrace("clarify_parse_failed", map[string]interface{}{
			"error": parseErr.Error(),
		})
		return nil, fmt.Errorf("clarifier: parse response: %w", parseErr)
	}

	// 写 trace：clarify 完成
	c.appendTrace("clarify_completed", map[string]interface{}{
		"questions_count": len(questions),
	})

	return questions, nil
}

// buildPrompt 构造 clarify prompt（S1）。
// prompt 包含需求描述 + 输出格式约束（JSON + 问题数 ≤3 + 选项数 ≤4）。
func (c *Clarifier) buildPrompt(requirement string) string {
	var sb strings.Builder
	sb.WriteString("你是需求澄清助手。请根据用户需求生成 1-3 个澄清问题，帮助明确需求细节。\n\n")
	sb.WriteString("用户需求：\n")
	sb.WriteString(requirement)
	sb.WriteString("\n\n约束：\n")
	sb.WriteString(fmt.Sprintf("- 最多 %d 个问题\n", ClarifyQuestionMaxNum))
	sb.WriteString(fmt.Sprintf("- 每个问题最多 %d 个选项（无选项则用户自由输入）\n", ClarifyOptionMaxNum))
	sb.WriteString("- 每个问题必须有 id（格式 Q1/Q2/Q3）+ question + reason\n")
	sb.WriteString("- 选项需含 label + value\n\n")
	sb.WriteString("输出格式（严格 JSON，无其他文字）：\n")
	sb.WriteString(`{"questions":[{"id":"Q1","question":"...","options":[{"label":"...","value":"..."}],"reason":"..."}]}`)
	sb.WriteString("\n")
	return sb.String()
}

// parseResponse 解析 LLM JSON 输出为 ClarifyQuestion 列表（S1）。
// 校验：问题数 ≤3 / 选项数 ≤4 / id 非空 / question 非空。
func (c *Clarifier) parseResponse(output string) ([]ClarifyQuestion, error) {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		// LLM 输出不含 JSON，视为生成 0 个问题（边界 case，直接进 plan）
		return nil, nil
	}

	var resp clarifyResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return nil, fmt.Errorf("clarifier: unmarshal: %w", err)
	}

	// 校验问题数 ≤3
	if len(resp.Questions) > ClarifyQuestionMaxNum {
		resp.Questions = resp.Questions[:ClarifyQuestionMaxNum] // 截断（不报错，宽容处理）
	}

	// 校验每个问题：id 非空 + question 非空 + 选项数 ≤4
	result := make([]ClarifyQuestion, 0, len(resp.Questions))
	for _, q := range resp.Questions {
		if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Question) == "" {
			continue // 跳过无效问题
		}
		if len(q.Options) > ClarifyOptionMaxNum {
			q.Options = q.Options[:ClarifyOptionMaxNum] // 截断
		}
		result = append(result, q)
	}

	return result, nil
}

// MergeAnswers 把用户回答合并到 requirement，返回增强后的 requirement（S1）。
// 合并策略：在原 requirement 后追加 "[澄清问答]" 段，每条 Q&A 一行。
// answers 为空时返回原 requirement（向后兼容）。
func (c *Clarifier) MergeAnswers(requirement string, answers []ClarifyAnswer) string {
	if len(answers) == 0 {
		return requirement
	}

	var sb strings.Builder
	sb.WriteString(requirement)
	sb.WriteString("\n\n[澄清问答]\n")
	for _, a := range answers {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", a.QuestionID, a.Value))
	}
	return sb.String()
}

// appendTrace 写 trace 事件（tracer 为 nil 时跳过，S1）。
func (c *Clarifier) appendTrace(eventType string, payload map[string]interface{}) {
	if c.tracer == nil {
		return
	}
	_ = c.tracer.Append(PhaseTraceEvent{
		EventType: eventType,
		SubtaskID: "clarify",
		Payload:   payload,
	})
}

// FormatQuestionsForDisplay 格式化澄清问题列表为用户可读文本（含 prompt injection 防御标识，S1）。
// 输出前加 "[LLM 生成]" 标识行（审查优化 7）。
func FormatQuestionsForDisplay(questions []ClarifyQuestion) string {
	if len(questions) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(ClarifyPromptInjectionMark)
	sb.WriteString("\n\n")
	for i, q := range questions {
		sb.WriteString(fmt.Sprintf("%d. %s", i+1, q.Question))
		if q.Reason != "" {
			sb.WriteString(fmt.Sprintf("  (理由: %s)", q.Reason))
		}
		sb.WriteString("\n")
		if len(q.Options) > 0 {
			for j, opt := range q.Options {
				sb.WriteString(fmt.Sprintf("   %c. %s [%s]\n", 'A'+j, opt.Label, opt.Value))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
