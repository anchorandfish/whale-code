// llm_adapter.go — H10 Phase 6 编排器 LLM 适配器（S3）。
// 把 *ai.SimpleClient 适配为三套接口，避免编排器直接依赖 ai.SimpleClient：
// - AIClient（LLMReranker 用，非流式 Complete）
// - tools.Agent（Reviewer / N1 等子任务用）
// - N1Agent（Dispatcher 派发用）
//
// §1 S3；落地：OrchestratorLLMAdapter 持有 *ai.SimpleClient，三接口共用同一 Stream 收集逻辑
// /goal_wiring.go:23-84 evalAgentAdapter 模式；落地：复用 Stream + TextDeltaEvent 收集模式
// 32 AIClient 接口定义；落地：本适配器实现 AIClient，无需新建接口
// 25 N1Agent 接口定义；落地：本适配器实现 N1Agent
// /agent.go:980 tools.Agent 接口定义；落地：本适配器实现 tools.Agent
// SNAPSHOT: pkg=internal/orchestrator; type=OrchestratorLLMAdapter; fields={client:*ai.SimpleClient}; ifaces={AIClient,tools.Agent,N1Agent}; stream_collect=TextDeltaEvent 拼接; usage=UsageEvent 解析; error=ErrorEvent 提前返回; max_tokens=1280(默认); model_fallback=client.Model()

package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// OrchestratorLLMAdapter 编排器 LLM 适配器，复用主对话 SimpleClient 实例（S3）。
// 同时实现 AIClient / tools.Agent / N1Agent 三套接口，避免编排器直接依赖 ai.SimpleClient。
// 不维护状态，每次 Execute / Complete 独立调用（消除竞态，对标 evalAgentAdapter v4 修订）。
type OrchestratorLLMAdapter struct {
	client *ai.SimpleClient
}

// NewOrchestratorLLMAdapter 构造编排器 LLM 适配器，复用主对话 SimpleClient 实例（S3）。
// client 为 nil 时所有方法返回 error（便于测试 mock，避免 nil panic）。
func NewOrchestratorLLMAdapter(client *ai.SimpleClient) *OrchestratorLLMAdapter {
	return &OrchestratorLLMAdapter{client: client}
}

// orchestratorDefaultMaxTokens 编排器 LLM 调用默认 MaxTokens（对标 evalMaxOutputTokens）。
const orchestratorDefaultMaxTokens = 1280

// Complete 实现 AIClient 接口：非流式调用，返回完整文本输出（S3）。
// 内部调 SimpleClient.Stream 收集 TextDeltaEvent 拼接为完整文本。
// model 为空时 fallback 到 client.Model()；client 为 nil 时返回 error。
func (a *OrchestratorLLMAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	if a == nil || a.client == nil {
		return "", fmt.Errorf("orchestrator_llm_adapter: AI client not configured")
	}

	req := ai.Request{
		Model:     a.client.Model(),
		MaxTokens: orchestratorDefaultMaxTokens,
		Messages: []ai.MessageEntry{
			{Role: "user", Content: prompt},
		},
	}

	stream, err := a.client.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("orchestrator_llm_adapter: stream: %w", err)
	}

	var sb strings.Builder
	for event := range stream {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			sb.WriteString(e.Content)
		case ai.ErrorEvent:
			return "", fmt.Errorf("orchestrator_llm_adapter: stream error: %w", e.Err)
		}
	}

	return sb.String(), nil
}

// Model 实现 AIClient 接口：返回当前模型名（用于诊断，S3）。
// client 为 nil 时返回空字符串。
func (a *OrchestratorLLMAdapter) Model() string {
	if a == nil || a.client == nil {
		return ""
	}
	return a.client.Model()
}

// Execute 实现 tools.Agent 接口：调用 AI client 流式输出并收集文本（S3）。
// 参考 goal_wiring.go:23-84 evalAgentAdapter 模式。
// input.Model 为空时 fallback 到 client.Model()；input.Prompt 为空时返回 error。
func (a *OrchestratorLLMAdapter) Execute(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
	if a == nil || a.client == nil {
		return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("orchestrator_llm_adapter: AI client not configured")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("orchestrator_llm_adapter: prompt must not be empty")
	}

	model := input.Model
	if model == "" {
		model = a.client.Model()
	}

	req := ai.Request{
		Model:     model,
		MaxTokens: orchestratorDefaultMaxTokens,
		Messages: []ai.MessageEntry{
			{Role: "user", Content: input.Prompt},
		},
	}

	stream, err := a.client.Stream(ctx, req)
	if err != nil {
		return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("orchestrator_llm_adapter: stream: %w", err)
	}

	var sb strings.Builder
	var usage tools.TokenUsage
	for event := range stream {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			sb.WriteString(e.Content)
		case ai.UsageEvent:
			usage.InputTokens = int64(e.InputTokens)
			usage.OutputTokens = int64(e.OutputTokens)
			usage.CacheCreationTokens = int64(e.CacheCreation)
			usage.CacheReadTokens = int64(e.CacheRead)
		case ai.ErrorEvent:
			return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("orchestrator_llm_adapter: stream error: %w", e.Err)
		}
	}

	return tools.AgentOutput{
		Result: sb.String(),
		Status: tools.TaskCompleted,
		Usage:  &usage,
	}, nil
}

// Execute 实现 N1Agent 接口：调用 AI client 流式输出并收集文本（S3）。
// 与 tools.Agent.Execute 类似但入参/出参类型不同（N1AgentInput/N1AgentOutput）。
// N1AgentOutput 字段映射：CodeDiff=Result 文本 / TransferDoc=空 / FilesTouched=空
// （编排器实际派发时由 Dispatcher 重新构造 ImplementOutput，本字段映射仅占位）。
func (a *OrchestratorLLMAdapter) ExecuteN1(ctx context.Context, input N1AgentInput) (N1AgentOutput, error) {
	if a == nil || a.client == nil {
		return N1AgentOutput{}, fmt.Errorf("orchestrator_llm_adapter: AI client not configured")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return N1AgentOutput{}, fmt.Errorf("orchestrator_llm_adapter: prompt must not be empty")
	}

	model := input.Model
	if model == "" {
		model = a.client.Model()
	}

	req := ai.Request{
		Model:     model,
		MaxTokens: orchestratorDefaultMaxTokens,
		Messages: []ai.MessageEntry{
			{Role: "user", Content: input.Prompt},
		},
	}

	stream, err := a.client.Stream(ctx, req)
	if err != nil {
		return N1AgentOutput{}, fmt.Errorf("orchestrator_llm_adapter: stream: %w", err)
	}

	var sb strings.Builder
	for event := range stream {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			sb.WriteString(e.Content)
		case ai.ErrorEvent:
			return N1AgentOutput{}, fmt.Errorf("orchestrator_llm_adapter: stream error: %w", e.Err)
		}
	}

	// N1AgentOutput 映射：CodeDiff 携带完整文本（供 Dispatcher.parseN1Output 进一步解析）
	return N1AgentOutput{
		CodeDiff:     sb.String(),
		TransferDoc:  "",
		FilesTouched: nil,
	}, nil
}

// Execute N1Agent 接口的方法（与 tools.Agent.Execute 同名但入参不同，Go 不允许直接组合）。
// 通过 ExecuteN1 显式方法名避免与 tools.Agent.Execute 冲突，由 N1AgentAdapter 适配。
// 本适配器 struct 同时满足 N1Agent 接口（N1Agent 接口要求 Execute(ctx, N1AgentInput) (N1AgentOutput, error)）。
// 实现方案：定义 wrapper 方法 Execute 满足 N1Agent 接口，内部调 ExecuteN1。
//
// 注意：Go 不允许同名方法不同签名同时存在于同一 struct，因此本适配器 struct
// 不能同时实现 tools.Agent.Execute 和 N1Agent.Execute（两者签名不同）。
// 解决：OrchestratorLLMAdapter 仅实现 AIClient + tools.Agent 两套接口；
// N1Agent 接口由独立的 n1AgentAdapter 包装 OrchestratorLLMAdapter 实现。

// n1AgentAdapter 包装 OrchestratorLLMAdapter 实现 N1Agent 接口（S3）。
// 因 Go 不允许同名方法不同签名同时存在于同一 struct，N1Agent 接口单独包装。
type n1AgentAdapter struct {
	adapter *OrchestratorLLMAdapter
}

// NewN1AgentAdapter 构造 N1Agent 适配器，包装 OrchestratorLLMAdapter（S3）。
func NewN1AgentAdapter(adapter *OrchestratorLLMAdapter) N1Agent {
	if adapter == nil {
		return nil
	}
	return &n1AgentAdapter{adapter: adapter}
}

// Execute 实现 N1Agent 接口：委托给 OrchestratorLLMAdapter.ExecuteN1（S3）。
func (n *n1AgentAdapter) Execute(ctx context.Context, input N1AgentInput) (N1AgentOutput, error) {
	if n == nil || n.adapter == nil {
		return N1AgentOutput{}, fmt.Errorf("n1_agent_adapter: adapter not configured")
	}
	return n.adapter.ExecuteN1(ctx, input)
}
