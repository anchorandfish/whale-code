// orch_bridge.go — H10 Phase 6 TUI 桥接层（S2）。
// 把编排器 AskRequest/ContractRequest 转为 tea.Msg，通过 TUI 主循环异步处理。
//
// §3.3 S2 TUI 桥接；落地：OrchAskRequestMsg/OrchContractRequestMsg + waitForOrchAskRequest + Update 分支
// 1-TUI 桥接路径明确；落地：不复用 checkAskUserChoice（那是 LLM 工具调用），新增 orchestrator → TUI 通道
// ；落地：TUI 收到请求后进入 StateWaitingChoice，用户回答后通过 RespCh 回传
// SNAPSHOT: pkg=internal/tui; types={OrchAskRequestMsg,OrchContractRequestMsg,OrchBridge}; channels={askCh:chan orchestrator.AskRequest,contractCh:chan orchestrator.ContractRequest}; update_branches={StateWaitingChoice 复用}; resp=RespCh 同步回传; blocking=不阻塞TUI主循环(tea.Cmd 异步派发)

package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/orchestrator"
)

// OrchAskRequestMsg 编排器向 TUI 发送的提问请求消息（S2）。
// TUI 收到此消息后进入 StateWaitingChoice，展示问题列表等待用户回答。
// 用户回答后通过 RespCh 回传 orchestrator.AskResponse。
type OrchAskRequestMsg struct {
	Request orchestrator.AskRequest
}

// OrchContractRequestMsg 编排器向 TUI 发送的方案确认请求消息（S2）。
// TUI 收到此消息后进入 StateWaitingChoice，展示方案等待用户确认。
// 用户确认后通过 RespCh 回传 orchestrator.ContractResponse。
type OrchContractRequestMsg struct {
	Request orchestrator.ContractRequest
}

// OrchBridge 编排器与 TUI 之间的桥接器（S2）。
// 持有 askCh / contractCh 两个 channel，分别接收编排器的提问/确认请求。
// TUI 主循环通过 waitForOrchAskRequest / waitForOrchContractRequest tea.Cmd 监听 channel。
type OrchBridge struct {
	askCh      <-chan orchestrator.AskRequest      // 接收编排器提问请求
	contractCh <-chan orchestrator.ContractRequest // 接收编排器方案确认请求
}

// NewOrchBridge 构造桥接器（S2）。
// askCh / contractCh 由 main.go 创建并注入到 TUI Model 和 TUIAskUserGateway。
func NewOrchBridge(askCh <-chan orchestrator.AskRequest, contractCh <-chan orchestrator.ContractRequest) *OrchBridge {
	return &OrchBridge{
		askCh:      askCh,
		contractCh: contractCh,
	}
}

// waitForOrchAskRequest 等待编排器提问请求的 tea.Cmd（S2）。
// 阻塞监听 askCh，收到请求后返回 OrchAskRequestMsg。
// channel 关闭时返回 nil（TUI 退出场景）。
func waitForOrchAskRequest(askCh <-chan orchestrator.AskRequest) tea.Cmd {
	if askCh == nil {
		return nil
	}
	return func() tea.Msg {
		req, ok := <-askCh
		if !ok {
			return nil // channel 关闭
		}
		return OrchAskRequestMsg{Request: req}
	}
}

// waitForOrchContractRequest 等待编排器方案确认请求的 tea.Cmd（S2）。
func waitForOrchContractRequest(contractCh <-chan orchestrator.ContractRequest) tea.Cmd {
	if contractCh == nil {
		return nil
	}
	return func() tea.Msg {
		req, ok := <-contractCh
		if !ok {
			return nil
		}
		return OrchContractRequestMsg{Request: req}
	}
}

// handleOrchAskRequest 处理编排器提问请求（S2 + S4 接线）。
// 进入 StateWaitingChoice，把问题列表存入 pendingChoice，等待用户回答。
// 用户回答后通过 choiceResultMsg 处理分支调 sendOrchAskResponse 回传 AskResponse。
// 本方法同时把请求存入 m.pendingOrchAskReq，供 choiceResultMsg 分支提取 RespCh。
func (m *Model) handleOrchAskRequest(msg OrchAskRequestMsg) tea.Cmd {
	req := msg.Request
	if len(req.Questions) == 0 {
		// 无问题，直接返回空答案
		req.RespCh <- orchestrator.AskResponse{
			Answers:   nil,
			Cancelled: false,
			Error:     nil,
		}
		return nil
	}

	// 把 ClarifyQuestion 列表转为 PendingChoice（复用现有 TUI UI）
	// 取第一个问题展示（多问题分轮次处理，简化实现）
	firstQ := req.Questions[0]
	options := make([]ChoiceOption, 0, len(firstQ.Options))
	for _, opt := range firstQ.Options {
		options = append(options, ChoiceOption{
			Label: opt.Label,
			Value: opt.Value,
		})
	}

	// S4 接线：保存请求到 Model 字段，供 choiceResultMsg 分支提取 RespCh 回传响应
	m.pendingOrchAskReq = &req
	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt:  firstQ.Question,
		Options: options,
		ToolID:  "orch-ask-" + firstQ.ID,
	})
	m.sessionService.SetState(StateWaitingChoice)

	return nil
}

// handleOrchContractRequest 处理编排器方案确认请求（S2 + S4 接线）。
// 进入 StateWaitingChoice，展示方案等待用户确认。
// 本方法同时把请求存入 m.pendingOrchContractReq，供 choiceResultMsg 分支提取 RespCh。
func (m *Model) handleOrchContractRequest(msg OrchContractRequestMsg) tea.Cmd {
	req := msg.Request
	if req.Plan == nil || len(req.Plan.SubTasks) == 0 {
		// 空方案，直接接受
		req.RespCh <- orchestrator.ContractResponse{
			Result:    orchestrator.ConfirmResult{Accepted: true},
			Cancelled: false,
			Error:     nil,
		}
		return nil
	}

	// 把方案转为 PendingChoice（接受/拒绝/修改三选项）
	planText := orchestrator.FormatPlanForDisplay(req.Plan)
	// S4 接线：保存请求到 Model 字段，供 choiceResultMsg 分支提取 RespCh 回传响应
	m.pendingOrchContractReq = &req
	// ★ ：双语形态 SSOT —— 选项 Label 英文主行，
	// DescriptionZH 中文辅行（renderChoiceUI 在非空时缩进另起一行）；
	// Value 逐字不变（ParseContractAnswer 按 Value 匹配，与显示文案解耦）。
	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt: bi("Please confirm the plan:", "请确认方案：") + "\n\n" + planText,
		Options: []ChoiceOption{
			{Label: "Accept plan", Value: "accept", DescriptionZH: "接受方案"},
			{Label: "Reject plan", Value: "reject", DescriptionZH: "拒绝方案"},
			{Label: "Modify plan", Value: "modify", DescriptionZH: "修改方案"},
		},
		ToolID: "orch-contract",
	})
	m.sessionService.SetState(StateWaitingChoice)

	return nil
}

// sendOrchAskResponse 回传提问响应给编排器（S2）。
// 由 TUI 在用户回答后调用（如 choiceResultMsg 处理分支中）。
func sendOrchAskResponse(req orchestrator.AskRequest, answer orchestrator.ClarifyAnswer, cancelled bool) {
	if req.RespCh == nil {
		return
	}
	resp := orchestrator.AskResponse{
		Answers:   []orchestrator.ClarifyAnswer{answer},
		Cancelled: cancelled,
		Error:     nil,
	}
	select {
	case req.RespCh <- resp:
	default:
		// channel 已满或无人监听，丢弃（防阻塞）
	}
}

// sendOrchContractResponse 回传方案确认响应给编排器（S2）。
func sendOrchContractResponse(req orchestrator.ContractRequest, result orchestrator.ConfirmResult, cancelled bool) {
	if req.RespCh == nil {
		return
	}
	resp := orchestrator.ContractResponse{
		Result:    result,
		Cancelled: cancelled,
		Error:     nil,
	}
	select {
	case req.RespCh <- resp:
	default:
	}
}

// FormatOrchQuestionsForTUI 把编排器 ClarifyQuestion 列表格式化为 TUI 可读文本（S2）。
func FormatOrchQuestionsForTUI(questions []orchestrator.ClarifyQuestion) string {
	return orchestrator.FormatQuestionsForDisplay(questions)
}

// NewOrchChannels 创建编排器与 TUI 通信的 channel 对（S2）。
// 返回 (askCh, contractCh) 用于 TUI 监听，(askChSend, contractChSend) 用于 TUIAskUserGateway 发送。
// main.go 调用此函数获取 channel，注入到 OrchBridge 和 TUIAskUserGateway。
func NewOrchChannels() (askCh <-chan orchestrator.AskRequest, contractCh <-chan orchestrator.ContractRequest, askChSend chan<- orchestrator.AskRequest, contractChSend chan<- orchestrator.ContractRequest) {
	ask := make(chan orchestrator.AskRequest, 1)
	contract := make(chan orchestrator.ContractRequest, 1)
	return ask, contract, ask, contract
}
