// askuser_gateway.go — H10 Phase 6 AskUserGateway 实现（S2）。
// TUIAskUserGateway 通过 channel 与 TUI 主循环通信，把 Ask 调用包装为 PendingChoiceMsg。
//
// §3.2 S2 AskUserGateway；落地：TUIAskUserGateway struct + Ask + ConfirmContract
// 1-TUI 桥接路径明确；落地：通过 askCh/ansCh channel 与 TUI 通信，不直接调 TUI 方法
// ；落地：AskUser 失败/超时进 StageInterrupted
// SNAPSHOT: pkg=internal/orchestrator; type=TUIAskUserGateway; fields={askCh:chan<- AskRequest,ansCh:<-chan AskResponse,timeout:time.Duration}; methods={Ask(ctx,[]ClarifyQuestion)([]ClarifyAnswer,error),ConfirmContract(ctx,*GeneratedPlan)(ConfirmResult,error)}; comm=channel(不直接调TUI); blocking=同步(调用方需goroutine); timeout=30min(默认); cancel=ctx.Done()返回error

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AskUserDefaultTimeout AskUser 默认超时时间（30 分钟，与 ContractorConfirmTimeout 对齐）。
const AskUserDefaultTimeout = 30 * time.Minute

// AskRequest TUIAskUserGateway 向 TUI 发送的提问请求（S2）。
// TUI 收到此请求后进入 StateWaitingChoice，用户回答后通过 RespCh 回传 AskResponse。
type AskRequest struct {
	TaskID    string            // 任务 ID（用于 TUI 显示上下文）
	Questions []ClarifyQuestion // 澄清问题列表
	RespCh    chan AskResponse  // TUI 回传响应的 channel
}

// AskResponse TUI 向 TUIAskUserGateway 回传的响应（S2）。
type AskResponse struct {
	Answers   []ClarifyAnswer // 用户回答列表（按 QuestionID 顺序）
	Cancelled bool            // 用户是否取消
	Error     error           // 错误信息（超时/异常等）
}

// ContractRequest TUIAskUserGateway 向 TUI 发送的方案确认请求（S2）。
type ContractRequest struct {
	TaskID string                // 任务 ID
	Plan   *GeneratedPlan        // 待确认方案
	RespCh chan ContractResponse // TUI 回传响应的 channel
}

// ContractResponse TUI 向 TUIAskUserGateway 回传的方案确认响应（S2）。
type ContractResponse struct {
	Result    ConfirmResult // 确认结果
	Cancelled bool          // 用户是否取消
	Error     error         // 错误信息
}

// TUIAskUserGateway TUI 实现，把 Ask 调用包装为 tea.Cmd 返回 PendingChoiceMsg（S2）。
// 通过 channel 与 TUI 主循环通信（不直接调 TUI 方法，避免循环依赖）。
// Ask 方法是同步阻塞的，调用方（如 HandleClarify）必须在 goroutine 中调用。
type TUIAskUserGateway struct {
	askCh      chan<- AskRequest      // 向 TUI 发送提问请求
	ansCh      <-chan AskResponse     // 接收 TUI 回传的响应（保留字段，实际通过 RespCh 同步等待）
	contractCh chan<- ContractRequest // 向 TUI 发送方案确认请求
	timeout    time.Duration          // 超时时间
}

// NewTUIAskUserGateway 构造 TUIAskUserGateway（S2）。
// askCh 为向 TUI 发送提问请求的 channel；contractCh 为向 TUI 发送方案确认请求的 channel。
// 两个 channel 都由 TUI 层创建并注入，TUI 主循环监听它们。
// timeout 为 0 时使用 AskUserDefaultTimeout（30 分钟）。
func NewTUIAskUserGateway(askCh chan<- AskRequest, contractCh chan<- ContractRequest, timeout time.Duration) *TUIAskUserGateway {
	if timeout <= 0 {
		timeout = AskUserDefaultTimeout
	}
	return &TUIAskUserGateway{
		askCh:      askCh,
		contractCh: contractCh,
		timeout:    timeout,
	}
}

// Ask 实现 AskUserGateway 接口（S2）。
// 同步阻塞：向 TUI 发送 AskRequest，等待 AskResponse 返回（通过 RespCh）。
// 调用方必须在 goroutine 中调用（避免阻塞 TUI 主循环）。
// 用户取消/超时/异常时返回 error。
func (g *TUIAskUserGateway) Ask(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
	if g == nil {
		return nil, fmt.Errorf("tui_askuser_gateway: nil receiver")
	}
	if g.askCh == nil {
		return nil, fmt.Errorf("tui_askuser_gateway: askCh not configured")
	}
	if len(questions) == 0 {
		return nil, nil // 无问题直接返回
	}

	// 创建响应 channel（每次调用独立，避免竞态）
	respCh := make(chan AskResponse, 1)

	// 构造请求
	req := AskRequest{
		TaskID:    "", // TaskID 由调用方在外层设置（此处为空，TUI 不强依赖）
		Questions: questions,
		RespCh:    respCh,
	}

	// 设置超时 context
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	// 向 TUI 发送请求（非阻塞 select，防 TUI 已退出）
	select {
	case g.askCh <- req:
		// 发送成功
	case <-ctx.Done():
		return nil, fmt.Errorf("tui_askuser_gateway: send request timeout: %w", ctx.Err())
	}

	// 等待响应
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, resp.Error
		}
		if resp.Cancelled {
			return nil, fmt.Errorf("tui_askuser_gateway: user cancelled")
		}
		return resp.Answers, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("tui_askuser_gateway: wait response timeout: %w", ctx.Err())
	}
}

// ConfirmContract 实现 AskUserGateway 接口（S2）。
// 同步阻塞：向 TUI 发送 ContractRequest，等待 ContractResponse 返回。
// 用户拒绝时返回 ErrContractRejected，超时返回 ErrContractTimeout。
func (g *TUIAskUserGateway) ConfirmContract(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
	if g == nil {
		return ConfirmResult{}, fmt.Errorf("tui_askuser_gateway: nil receiver")
	}
	if plan == nil {
		return ConfirmResult{Accepted: true}, nil // 空方案直接接受
	}
	if g.contractCh == nil {
		// contractCh 未配置，降级为直接接受（向后兼容）
		return ConfirmResult{Accepted: true}, nil
	}

	// 创建响应 channel
	respCh := make(chan ContractResponse, 1)

	// 构造请求
	req := ContractRequest{
		TaskID: "",
		Plan:   plan,
		RespCh: respCh,
	}

	// 设置超时 context
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	// 向 TUI 发送请求
	select {
	case g.contractCh <- req:
		// 发送成功
	case <-ctx.Done():
		return ConfirmResult{}, ErrContractTimeout
	}

	// 等待响应
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return ConfirmResult{}, resp.Error
		}
		if resp.Cancelled {
			return ConfirmResult{}, ErrContractRejected
		}
		if !resp.Result.Accepted {
			return resp.Result, ErrContractRejected
		}
		return resp.Result, nil
	case <-ctx.Done():
		return ConfirmResult{}, ErrContractTimeout
	}
}

// MockAskUserGateway 测试用 AskUserGateway mock 实现（S2）。
// 用于单元测试，不依赖 TUI channel。
type MockAskUserGateway struct {
	AskFunc     func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error)
	ConfirmFunc func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error)
}

// NewMockAskUserGateway 构造 mock（S2）。
func NewMockAskUserGateway() *MockAskUserGateway {
	return &MockAskUserGateway{}
}

// Ask 实现 AskUserGateway 接口（mock，S2）。
func (m *MockAskUserGateway) Ask(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
	if m == nil {
		return nil, errors.New("mock_askuser_gateway: nil receiver")
	}
	if m.AskFunc != nil {
		return m.AskFunc(ctx, questions)
	}
	// 默认行为：返回空答案（视为用户跳过）
	return nil, nil
}

// ConfirmContract 实现 AskUserGateway 接口（mock，S2）。
func (m *MockAskUserGateway) ConfirmContract(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
	if m == nil {
		return ConfirmResult{}, errors.New("mock_askuser_gateway: nil receiver")
	}
	if m.ConfirmFunc != nil {
		return m.ConfirmFunc(ctx, plan)
	}
	// 默认行为：接受方案
	return ConfirmResult{Accepted: true}, nil
}
