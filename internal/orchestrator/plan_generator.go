// plan_generator.go — H10 Phase 6 plan Stage 业务逻辑（S1）。
// 调 LLM 把 requirement 拆为子任务列表。
//
// §2.2 S1 PlanGenerator；落地：PlanGenerator struct + Generate
// ≤3；落地：生成的子任务 SubTaskID 必须唯一
// ；落地：空 SubTasks 降级为单子任务（不走 retry_once）
// SNAPSHOT: pkg=internal/orchestrator; type=PlanGenerator; fields={llm:AIClient,tracer:*TraceStore}; methods={Generate(ctx,requirement)(*GeneratedPlan,error)}; output_format=JSON; degrade=LLM失败返回error(调用方降级单子任务); empty_subtasks=降级单子任务(§11.2); max_subtasks=10(防LLM过拆)

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/util"
)

// PlanGeneratorMaxSubTasks plan Stage 单次最多生成的子任务数（防 LLM 过拆）。
const PlanGeneratorMaxSubTasks = 10

// GeneratedPlan plan Stage 生成的方案（S1）。
type GeneratedPlan struct {
	SubTasks []SubTaskDef `json:"subtasks"`
	Reason   string       `json:"reason"`   // LLM 拆解理由
	EstCost  int          `json:"est_cost"` // 预估 token 成本（0-1000，相对值）
}

// PlanGenerator plan Stage 业务逻辑：调 LLM 把 requirement 拆为子任务列表（S1）。
type PlanGenerator struct {
	llm    AIClient
	tracer *TraceStore
}

// NewPlanGenerator 构造 PlanGenerator（S1）。
// llm 为 nil 时 Generate 返回 error；tracer 为 nil 时跳过 trace 写入。
func NewPlanGenerator(llm AIClient, tracer *TraceStore) *PlanGenerator {
	return &PlanGenerator{llm: llm, tracer: tracer}
}

// planResponse LLM 返回的 JSON 结构。
type planResponse struct {
	SubTasks []SubTaskDef `json:"subtasks"`
	Reason   string       `json:"reason"`
	EstCost  int          `json:"est_cost"`
}

// Generate 调 LLM 生成子任务方案（S1）。
// 返回空 SubTasks 时不走 retry_once，降级为单子任务（边界 case，§11.2）。
// LLM 调用失败时返回 error，由调用方（HandlePlan）决定降级单子任务。
func (p *PlanGenerator) Generate(ctx context.Context, requirement string) (*GeneratedPlan, error) {
	if p == nil {
		return nil, fmt.Errorf("plan_generator: nil receiver")
	}
	if strings.TrimSpace(requirement) == "" {
		return nil, fmt.Errorf("plan_generator: requirement must not be empty")
	}
	if p.llm == nil {
		return nil, fmt.Errorf("plan_generator: llm client not configured")
	}

	// 写 trace：plan 开始
	p.appendTrace("plan_started", map[string]interface{}{
		"requirement_length": len(requirement),
	})

	// 构造 prompt
	prompt := p.buildPrompt(requirement)

	// 调 LLM
	output, err := p.llm.Complete(ctx, prompt)
	if err != nil {
		p.appendTrace("plan_failed", map[string]interface{}{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("plan_generator: llm complete: %w", err)
	}

	// 解析 LLM 输出
	plan, parseErr := p.parseResponse(output)
	if parseErr != nil {
		p.appendTrace("plan_parse_failed", map[string]interface{}{
			"error": parseErr.Error(),
		})
		return nil, fmt.Errorf("plan_generator: parse response: %w", parseErr)
	}

	// 写 trace：plan 完成
	p.appendTrace("plan_completed", map[string]interface{}{
		"subtasks_count": len(plan.SubTasks),
		"est_cost":       plan.EstCost,
	})

	return plan, nil
}

// buildPrompt 构造 plan prompt（S1）。
func (p *PlanGenerator) buildPrompt(requirement string) string {
	var sb strings.Builder
	sb.WriteString("你是任务拆解助手。请根据用户需求拆分为 1-10 个子任务，每个子任务含 ID/类型/复杂度/文件列表。\n\n")
	sb.WriteString("用户需求：\n")
	sb.WriteString(requirement)
	sb.WriteString("\n\n约束：\n")
	sb.WriteString(fmt.Sprintf("- 最多 %d 个子任务\n", PlanGeneratorMaxSubTasks))
	sb.WriteString("- SubTaskID 格式：S-001/S-002/...\n")
	sb.WriteString("- Type 取值：implement/review/plan/explore\n")
	sb.WriteString("- Complexity 取值：T1/T2/T3（T1 简单/T2 中等/T3 复杂）\n")
	sb.WriteString("- DependsOn 列出依赖的 SubTaskID（可空）\n\n")
	sb.WriteString("输出格式（严格 JSON，无其他文字）：\n")
	sb.WriteString(`{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1","files_to_read":[],"files_to_edit":[],"depends_on":[]}],"reason":"...","est_cost":500}`)
	sb.WriteString("\n")
	return sb.String()
}

// parseResponse 解析 LLM JSON 输出为 GeneratedPlan（S1）。
// 校验：子任务数 ≤10 / SubTaskID 非空且唯一 / Type 合法 / Complexity 合法。
// 空输出或无有效子任务时返回空 plan（调用方降级单子任务）。
func (p *PlanGenerator) parseResponse(output string) (*GeneratedPlan, error) {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		// LLM 输出不含 JSON，返回空 plan（调用方降级单子任务）
		return &GeneratedPlan{SubTasks: nil, Reason: "LLM 输出无 JSON"}, nil
	}

	var resp planResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return nil, fmt.Errorf("plan_generator: unmarshal: %w", err)
	}

	// 校验子任务数 ≤10
	if len(resp.SubTasks) > PlanGeneratorMaxSubTasks {
		resp.SubTasks = resp.SubTasks[:PlanGeneratorMaxSubTasks]
	}

	// 校验每个子任务：SubTaskID 非空 + Type 合法 + Complexity 合法
	seenIDs := make(map[string]bool)
	result := make([]SubTaskDef, 0, len(resp.SubTasks))
	for _, st := range resp.SubTasks {
		if strings.TrimSpace(st.SubTaskID) == "" {
			continue
		}
		if seenIDs[st.SubTaskID] {
			continue // 去重
		}
		if !isValidTaskType(st.Type) {
			st.Type = TaskTypeImplement // 默认类型
		}
		if !isValidComplexity(st.Complexity) {
			st.Complexity = ComplexityT2 // 默认复杂度
		}
		seenIDs[st.SubTaskID] = true
		result = append(result, st)
	}

	return &GeneratedPlan{
		SubTasks: result,
		Reason:   resp.Reason,
		EstCost:  resp.EstCost,
	}, nil
}

// DegradeToSingleTask 降级为单子任务（§11.2 边界 case，S1）。
// 当 LLM 失败或返回空 SubTasks 时，构造单个 implement 子任务兜底。
func (p *PlanGenerator) DegradeToSingleTask(requirement string) *GeneratedPlan {
	return &GeneratedPlan{
		SubTasks: []SubTaskDef{
			{
				SubTaskID:   "S-001",
				Type:        TaskTypeImplement,
				Complexity:  ComplexityT2,
				FilesToRead: []string{},
				FilesToEdit: []string{},
				DependsOn:   []string{},
			},
		},
		Reason:  "降级单子任务（LLM 生成失败或空方案）",
		EstCost: 500,
	}
}

// appendTrace 写 trace 事件（S1）。
func (p *PlanGenerator) appendTrace(eventType string, payload map[string]interface{}) {
	if p.tracer == nil {
		return
	}
	_ = p.tracer.Append(PhaseTraceEvent{
		EventType: eventType,
		SubtaskID: "plan",
		Payload:   payload,
	})
}

// isValidTaskType 校验 task type 合法性（S1）。
func isValidTaskType(t string) bool {
	switch t {
	case TaskTypeImplement, TaskTypeReview, TaskTypePlan, "explore":
		return true
	default:
		return false
	}
}

// isValidComplexity 校验 complexity 合法性（S1）。
func isValidComplexity(c string) bool {
	switch c {
	case ComplexityT1, ComplexityT2, ComplexityT3:
		return true
	default:
		return false
	}
}

// FormatPlanForDisplay 格式化方案为用户可读文本（S1）。
// 用于 contract Stage 展示给用户确认。
//
// ★ ：双语形态 SSOT = util.Bi（英文主行 + 缩进中文辅行）。
// 子任务 ID / 类型 / 文件路径是**语言中性的数据**，不套辅行 —— 形态门只管辖
// 我们自己写的文案行（技能 §4.7：别把形态门施加到调用方数据上）。
func FormatPlanForDisplay(plan *GeneratedPlan) string {
	if plan == nil || len(plan.SubTasks) == 0 {
		return util.Bi("(no plan)", "（无方案）")
	}
	var sb strings.Builder
	sb.WriteString(util.Bi(
		fmt.Sprintf("Subtask plan (%d subtasks):", len(plan.SubTasks)),
		fmt.Sprintf("子任务方案（共 %d 个子任务）：", len(plan.SubTasks)),
	))
	sb.WriteString("\n")
	if plan.Reason != "" {
		sb.WriteString(util.Bi(
			fmt.Sprintf("Decomposition reason: %s", plan.Reason),
			fmt.Sprintf("拆解理由: %s", plan.Reason),
		))
		sb.WriteString("\n")
	}
	if plan.EstCost > 0 {
		// EstCost 是**相对 token 成本估值**（0-1000，非钱）—— 与「不算价钱」拍板一致，
		// 文案按 token 口径译，不引入任何货币语义。
		sb.WriteString(util.Bi(
			fmt.Sprintf("Estimated token cost (relative 0-1000): %d", plan.EstCost),
			fmt.Sprintf("预估 token 成本（0-1000，相对值）：%d", plan.EstCost),
		))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	for _, st := range plan.SubTasks {
		sb.WriteString(fmt.Sprintf("- %s [%s/%s]\n", st.SubTaskID, st.Type, st.Complexity))
		if len(st.FilesToRead) > 0 {
			sb.WriteString(util.Bi(
				fmt.Sprintf("  Files to read: %s", strings.Join(st.FilesToRead, ", ")),
				fmt.Sprintf("读取文件: %s", strings.Join(st.FilesToRead, ", ")),
			))
			sb.WriteString("\n")
		}
		if len(st.FilesToEdit) > 0 {
			sb.WriteString(util.Bi(
				fmt.Sprintf("  Files to edit: %s", strings.Join(st.FilesToEdit, ", ")),
				fmt.Sprintf("修改文件: %s", strings.Join(st.FilesToEdit, ", ")),
			))
			sb.WriteString("\n")
		}
		if len(st.DependsOn) > 0 {
			sb.WriteString(util.Bi(
				fmt.Sprintf("  Depends on: %s", strings.Join(st.DependsOn, ", ")),
				fmt.Sprintf("依赖: %s", strings.Join(st.DependsOn, ", ")),
			))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// SerializePlan 序列化 GeneratedPlan 为 JSON 字符串（持久化到 state.json，S1）。
func SerializePlan(plan *GeneratedPlan) (string, error) {
	if plan == nil {
		return "", nil
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("plan_generator: serialize: %w", err)
	}
	return string(data), nil
}

// DeserializePlan 反序列化 JSON 字符串为 GeneratedPlan（从 state.json 恢复，S1）。
func DeserializePlan(jsonStr string) (*GeneratedPlan, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return nil, nil
	}
	var plan GeneratedPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return nil, fmt.Errorf("plan_generator: deserialize: %w", err)
	}
	return &plan, nil
}
