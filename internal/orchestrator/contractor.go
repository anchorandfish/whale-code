// contractor.go — H10 Phase 6 contract Stage 业务逻辑（S1）。
// 把生成的方案展示给用户确认，用户拒绝时返回 ErrContractRejected。
//
// §2.3 S1 Contractor；落地：Contractor struct + Confirm + ConfirmResult
// ；落地：用户拒绝/超时进 StageInterrupted
// SNAPSHOT: pkg=internal/orchestrator; type=Contractor; fields={askUser:AskUserGateway,tracer:*TraceStore}; methods={Confirm(ctx,*GeneratedPlan)(ConfirmResult,error)}; reject=ErrContractRejected; timeout=30min; degrade=AskUser失败进StageInterrupted; gateway_iface=AskUserGateway.Ask(ctx,[]ClarifyQuestion)([]ClarifyAnswer,error)

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// ErrContractRejected 用户拒绝方案错误（S1）。
// 由 Contractor.Confirm 返回，HandleContract 收到后转 StageInterrupted。
var ErrContractRejected = errors.New("contractor: contract rejected by user")

// ErrContractTimeout 用户确认超时错误（S1，默认 30 分钟）。
var ErrContractTimeout = errors.New("contractor: contract confirmation timeout")

// ContractorConfirmTimeout contract Stage 用户确认超时时间（30 分钟，S1）。
const ContractorConfirmTimeout = 30 * time.Minute

// AskUserGateway 编排器侧的 AskUser 调用网关接口（S1 定义，S2 实现）。
// 编排器通过此接口向用户提问，TUI 实现通过 channel 与主循环通信。
// Ask 方法是同步阻塞的，调用方必须在 goroutine 中调用（避免阻塞 TUI 主循环）。
type AskUserGateway interface {
	// Ask 向用户提问并等待回答。
	// questions 为澄清问题列表；返回按 QuestionID 顺序的答案列表。
	// 用户取消或超时时返回 error。
	Ask(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error)

	// ConfirmContract 向用户展示方案并等待确认/拒绝/修改（S1）。
	// plan 为生成的方案；返回 ConfirmResult。
	// 用户拒绝时返回 ErrContractRejected，超时返回 ErrContractTimeout。
	ConfirmContract(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error)
}

// ConfirmResult contract Stage 用户确认结果（S1）。
type ConfirmResult struct {
	Accepted bool   `json:"accepted"`           // 用户是否接受方案
	Modified string `json:"modified,omitempty"` // 用户修改后的方案描述（接受但修改时填充）
}

// Contractor contract Stage 业务逻辑：把生成的方案展示给用户确认（S1）。
type Contractor struct {
	askUser AskUserGateway
	tracer  *TraceStore
}

// NewContractor 构造 Contractor（S1）。
// askUser 为 nil 时 Confirm 返回 error；tracer 为 nil 时跳过 trace 写入。
func NewContractor(askUser AskUserGateway, tracer *TraceStore) *Contractor {
	return &Contractor{askUser: askUser, tracer: tracer}
}

// Confirm 向用户展示方案，等待用户确认/拒绝/修改（S1）。
// 用户拒绝时返回 ErrContractRejected，由 HandleContract 转 StageInterrupted。
// 用户超时（30 分钟无响应）返回 ErrContractTimeout。
// plan 为空时直接返回 ConfirmResult{Accepted: true}（向后兼容，无需用户确认）。
func (c *Contractor) Confirm(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
	if c == nil {
		return ConfirmResult{}, fmt.Errorf("contractor: nil receiver")
	}
	if plan == nil || len(plan.SubTasks) == 0 {
		// 空方案无需确认，直接接受（向后兼容）
		return ConfirmResult{Accepted: true}, nil
	}
	if c.askUser == nil {
		return ConfirmResult{}, fmt.Errorf("contractor: askUser gateway not configured")
	}

	// 写 trace：contract 开始
	c.appendTrace("contract_started", map[string]interface{}{
		"subtasks_count": len(plan.SubTasks),
	})

	// 设置超时 context
	ctx, cancel := context.WithTimeout(ctx, ContractorConfirmTimeout)
	defer cancel()

	// 调 AskUserGateway.ConfirmContract 等待用户确认
	result, err := c.askUser.ConfirmContract(ctx, plan)
	if err != nil {
		// 写 trace：contract 失败
		c.appendTrace("contract_failed", map[string]interface{}{
			"error": err.Error(),
		})
		if errors.Is(err, context.DeadlineExceeded) {
			return ConfirmResult{}, ErrContractTimeout
		}
		return ConfirmResult{}, err
	}

	// 写 trace：contract 完成
	c.appendTrace("contract_completed", map[string]interface{}{
		"accepted": result.Accepted,
		"modified": result.Modified != "",
	})

	if !result.Accepted {
		return result, ErrContractRejected
	}

	return result, nil
}

// appendTrace 写 trace 事件（S1）。
func (c *Contractor) appendTrace(eventType string, payload map[string]interface{}) {
	if c.tracer == nil {
		return
	}
	_ = c.tracer.Append(PhaseTraceEvent{
		EventType: eventType,
		SubtaskID: "contract",
		Payload:   payload,
	})
}

// BuildContractQuestion 把 GeneratedPlan 转为 ClarifyQuestion 展示给用户（S1）。
// 用于 AskUserGateway 实现复用 Ask 通道（提问 → 答案）。
// 返回单个问题，包含 "接受/拒绝/修改" 三个选项。
//
// ★ ：问题文本走双语形态 SSOT（util.Bi）；选项 Label 英文主行。
// ⚠️ 本函数当前**只有测试调用**（生产活线是 tui/orch_bridge.go 的
// handleOrchContractRequest）—— 按技能 §4.5 纪律，活线已改，此处同步保持一致，
// 避免将来有人接线时拿到一套中文-only 文案。选项 Value 逐字不变（解析按 Value）。
func BuildContractQuestion(plan *GeneratedPlan) ClarifyQuestion {
	questionText := util.Bi("Please confirm the following plan:", "请确认以下方案：") + "\n\n" + FormatPlanForDisplay(plan)
	return ClarifyQuestion{
		ID:       "CONTRACT_CONFIRM",
		Question: questionText,
		Options: []ClarifyOption{
			{Label: "Accept plan", Value: "accept"},
			{Label: "Reject plan", Value: "reject"},
			{Label: "Modify plan", Value: "modify"},
		},
		Reason: "contract Stage 用户确认",
	}
}

// ParseContractAnswer 把用户对 contract 问题的回答解析为 ConfirmResult（S1）。
// answer.Value 为 "accept"/"reject"/"modify" 或自由输入（视为修改后的方案描述）。
//
// ★ ：modify 分支的默认 Modified 文案改为英文 —— 该串经
// orchestrate_command.go 的 "用户修改: " 行展示给用户（活线，app.go:2666 →
// ParseContractAnswer）。解析分支**不变**：用户手输中文（"接受"/"拒绝"/"修改"）
// 仍然照常识别，显示文案与输入解析是两件事，不互相耦合。
func ParseContractAnswer(answer ClarifyAnswer) ConfirmResult {
	switch strings.ToLower(strings.TrimSpace(answer.Value)) {
	case "accept", "接受", "y", "yes":
		return ConfirmResult{Accepted: true}
	case "reject", "拒绝", "n", "no":
		return ConfirmResult{Accepted: false}
	case "modify", "修改":
		return ConfirmResult{Accepted: true, Modified: "User requested a plan modification (no specific description provided)"}
	default:
		// 自由输入视为修改后的方案描述，默认接受
		if strings.TrimSpace(answer.Value) != "" {
			return ConfirmResult{Accepted: true, Modified: answer.Value}
		}
		return ConfirmResult{Accepted: false}
	}
}
