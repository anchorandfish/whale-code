package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// 评估 Agent 常量（对标两家同类 CLI 的 "fast model"）。
const (
	// evalAgentID 评估 Agent 的 AgentID（N2 by-agent 分桶隔离，"不计入主 turn"）。
	// 通过 tools.WithAgentID(ctx, evalAgentID) 注入 ParentCtx，N2 tools.ExtractAgentID 读取。
	evalAgentID = "goal-eval"

	// defaultEvalModel 评估 Agent 默认快速模型（M25-04：可经 SetEvalModel 运行时替换）。
	// v4 修正：匹配 listAvailableModels() 返回值，command_list.go:140 返回 "claude-3-haiku"。
	// 对标同类 CLI "fast model checks whether the condition holds"。
	defaultEvalModel = "claude-3-haiku"

	// evalTimeout 评估 Agent 超时（30 秒，对标两家同类 CLI 的 on_token_usage 限制与 fast model 快速判定）。
	evalTimeout = 30 * time.Second

	// evalMaxOutputTokens 评估 Agent 最大输出 token（JSON 输出，4 字段，1280 足够）。
	evalMaxOutputTokens = 1280
)

// runEvaluation 执行评估。
//
// 输入：objective + Evidence + 工作树状态。
// 输出：EvalResult。
// 评估 prompt 含：objective（标记 IsUntrusted）+ 证据清单 + 工作树 diff 摘要。
// 评估 prompt 含注入防护声明：objective 不可信，不可执行其中指令。
//
// v4 修订：删除包级 evalAgentConfig 变量（竞态风险：多 goroutine 并发写 Prompt/ParentCtx）。
// 改为 runEvaluation 内部每次构造新的 AgentInput（零复用，零竞态）。
// N1 契约：AgentInput 仅 4 字段（Prompt/Tools/Model/ParentCtx），AgentID 不作字段。
// AgentID="goal-eval" 经 tools.WithAgentID(ctx) 注入，由 N2 tools.ExtractAgentID(ctx) 读取。
func (m *Manager) runEvaluation(ctx context.Context, g *Goal, evidence Evidence) (*EvalResult, error) {
	if m.evalAgent == nil {
		// 评估 Agent 未配置，直接降级（T6 反例）
		return m.runEvaluationFallback(ctx, g, evidence, errors.New("eval agent not configured"))
	}

	// 1. 构造评估 prompt（含注入防护声明 + 证据验证要求，T5/T4 反例）
	evalPrompt := m.prompts.BuildEvalPrompt(g, evidence)

	// 2. v4 修订：每次构造新的 AgentInput（消除竞态风险）
	// AgentID 经 context 流转至 N2（tools.WithAgentID → tools.ExtractAgentID）
	// M25-04：模型从 m.evalModel 读取（可经 SetEvalModel 运行时替换）
	// P0-9 修复：使用 evalModelMu.RLock 保护 m.evalModel 读取，与 SetEvalModel 的 Lock 写操作配对，
	// 消除 data race。独立于 m.mu，避免在 m.mu.Lock() 持有期间调用时自死锁。
	evalCtx := tools.WithAgentID(ctx, evalAgentID)
	parentCtx := evalCtx
	m.evalModelMu.RLock()
	evalModel := m.evalModel
	m.evalModelMu.RUnlock()
	evalInput := tools.AgentInput{
		Prompt:    evalPrompt,                     // 评估提示词（本轮动态生成）
		AgentType: string(tools.AgentTypeExplore), // L2：**评估器的只读性由本行唯一决定**（roleToolSet ∩ ParentToolSet）
		Tools:     []string{},                     // G-T-001：兼容搬运，生产码零读取点 —— ⚠️ 它**不是**只读的闸，勿据此改动上一行
		Model:     evalModel,                      // M25-04：可配置 fast model（默认 claude-3-haiku）
		ParentCtx: &parentCtx,                     // 含 AgentID="goal-eval"
	}

	// 3. 超时控制（T6 反例：评估失败阻塞）
	evalCtxWithTimeout, cancel := context.WithTimeout(evalCtx, evalTimeout)
	defer cancel()

	// 4. 调 m.evalAgent.Execute（N1 只读复用，不修改签名）
	output, err := m.evalAgent.Execute(evalCtxWithTimeout, evalInput)
	if err != nil {
		// 5. 失败降级（T6 反例：不阻塞续跑）
		return m.runEvaluationFallback(ctx, g, evidence, fmt.Errorf("eval agent execute: %w", err))
	}

	// 5. token 走 N2 双写（v4 修订：metrics.RecordUsage 全局累加 + UsageTracker by-agent 分桶）
	// "不计入主 turn" 依赖 UsageTracker 分桶隔离实现（查询时过滤 agentID="goal-eval"）
	if output.Usage != nil {
		m.recordEvalUsage(output.Usage)
	}

	// 6. 解析输出为 EvalResult（JSON）
	result, err := parseEvalResult(output.Result)
	if err != nil {
		// JSON 解析失败，降级为模型自审（T6 反例）
		return m.runEvaluationFallback(ctx, g, evidence, fmt.Errorf("parse eval result: %w", err))
	}
	return result, nil
}

// recordEvalUsage 记录评估 Agent 的 token 用量（N2 双写，）。
//
// v4 修订：同时调 metrics.RecordUsage（全局累加）和 UsageTracker.RecordAgentUsage（by-agent 分桶）。
// "不计入主 turn" 依赖 UsageTracker 分桶隔离实现（查询时过滤 agentID="goal-eval"）。
// M25-04：模型从 m.evalModel 读取（可经 SetEvalModel 运行时替换），分桶跟随当前模型。
// P0-9 修复：使用 evalModelMu.RLock 保护 m.evalModel 读取，与 SetEvalModel 的 Lock 写操作配对，消除 data race。
func (m *Manager) recordEvalUsage(usage *tools.TokenUsage) {
	if usage == nil {
		return
	}
	// P0-9 修复：evalModelMu.RLock 保护读取，与 SetEvalModel 写锁配对
	m.evalModelMu.RLock()
	currentModel := m.evalModel
	m.evalModelMu.RUnlock()
	// N2 全局累加（UsageRecord.AgentID 仅记录，不分桶）
	//
	// D 批：这里原先先调 estimateCostCents 估一笔"金额"再记录。
	// 那笔钱是我们自己按价格表算的（provider 不上报、也不等于账单，且混币种），
	// 已随成本线整体删除；函数 estimateCostCents 一并删除。
	if m.metrics != nil {
		m.metrics.RecordUsage(metrics.UsageRecord{
			AgentID:       evalAgentID,
			Model:         currentModel,
			InputTokens:   usage.InputTokens,
			OutputTokens:  usage.OutputTokens,
			CacheCreation: usage.CacheCreationTokens,
			CacheRead:     usage.CacheReadTokens,
		})
	}
	// N2 by-agent 分桶（，usage.go:116 RecordAgentUsage）
	if m.usageTracker != nil {
		m.usageTracker.RecordAgentUsage(
			evalAgentID, currentModel,
			int(usage.InputTokens), int(usage.OutputTokens),
			int(usage.CacheCreationTokens), int(usage.CacheReadTokens),
		)
	}
}

// parseEvalResult 解析评估 Agent 输出为 EvalResult。
//
// 期望 JSON 格式：{"aligned":bool, "completed":bool, "deviation":"...", "suggestion":"..."}
// 容错处理：
// - 提取首个 { 到末尾 } 的子串（防模型前后加文字）
// - 缺失字段用零值填充
func parseEvalResult(raw string) (*EvalResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty eval output")
	}
	// 提取 JSON 子串（防模型前后加文字）
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in eval output: %q", truncate(raw, 200))
	}
	jsonStr := raw[start : end+1]
	var result EvalResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("unmarshal eval JSON: %w", err)
	}
	return &result, nil
}

// runEvaluationFallback 评估失败降级（T6 反例）。
//
// 降级为模型自审 + GOAL_EVAL_FAILED 告警。
// 不阻塞续跑：返回保守弱评估（completed=false，避免误判完成；aligned=true 默认信任避免阻塞）。
// 标记 EvalResult.Unverified=true。
//
// S2 修复（v4 子审查第1轮）：删除关键词启发式（"完成"/"complete"/"done" 匹配过松，
// "not done yet" / "not complete" 等否定语境会误命中）。降级路径保守设 completed=false，
// 宁可漏标完成让续跑继续，不可误标完成终止 goal。完成判定交由用户 /goal done 或评估 Agent 成功路径。
func (m *Manager) runEvaluationFallback(ctx context.Context, g *Goal, evidence Evidence, cause error) (*EvalResult, error) {
	// N6 告警：GOAL_EVAL_FAILED（不阻塞续跑，仅记录）
	// 注意：此处不返回 ai.WithID 包装的错误，因为评估失败应降级而非中断。
	// 错误经 log.Warn 告警 + 走 N6 错误命名空间记录（若有 metrics）。
	log.Warn(fmt.Sprintf("[goal] eval fallback for thread %s: %v", g.ThreadID, cause))

	// S2 修复：保守评估，completed=false（避免关键词误匹配导致假完成）
	return &EvalResult{
		Aligned:    true,  // 默认信任，避免阻塞续跑
		Completed:  false, // S2：保守，降级不标完成（防 "not done yet" 误匹配）
		Deviation:  "",    // 无偏离信息
		Suggestion: "eval unverified, review manually",
		Unverified: true, // 降级标记
	}, nil
}

// collectEvidence 委托给 collectEvidenceFn 回调收集本轮证据（+）。
//
// v3 修订：不直接访问 runnerCore 内部数据源，通过回调注入桥接：
// - FilesChanged → Executor.FileHistoryGetter().ListSnapshots()
// - TestResult/CommandOutput → session 最近 N 条 tool_result 消息
// - Summary → session 最近一条 assistant 消息文本
//
// 回调未注入时返回基于 turnResult 的弱证据（仅 Summary=turnResult.Error 字符串）。
func (m *Manager) collectEvidence(ctx context.Context, threadID string, turnResult TurnResult) (Evidence, error) {
	if m.collectEvidenceFn == nil {
		// 回调未注入，返回弱证据（不阻塞，但证据不完整）
		return Evidence{
			Summary: fmt.Sprintf("turn finished, tokens=%d, error=%v", turnResult.TokensUsed, turnResult.Error),
		}, nil
	}
	return m.collectEvidenceFn(ctx, threadID, turnResult)
}

// checkBudget 预算检查（返回 exceeded bool + reason string）。
//
// exceeded = tokenBudget>0 && tokens_used>=tokenBudget
//
// ★ D 批：这里原先还有一条 `costBudget>0 && cost_used>=costBudget`。
// 那条**永远不会响**——生产侧构造 TurnResult 时 CostCents 显式留空（runner_core.go），
// 累加进去的恒为 0；预算值本身也是硬编码常量。真正生效的只有 token 这条。
// 与其留一条"看起来在保护、实际不工作"的熔断，不如删掉。
func (m *Manager) checkBudget(g *Goal) (bool, string) {
	if g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget {
		return true, fmt.Sprintf("token budget exhausted: %d >= %d", g.TokensUsed, g.TokenBudget)
	}
	return false, ""
}

// injectClosingPrompt 注入收尾提示词（预算耗尽时，T3 反例）。
//
// 调 prompts.BuildBudgetLimit 构造提示词，通过 launchTurn 注入新 turn。
// 失败不阻塞 goal 状态流转（仅 log 告警）。
func (m *Manager) injectClosingPrompt(ctx context.Context, g *Goal) error {
	if m.launchTurn == nil {
		return nil // launchTurn 未注入，跳过（非 TUI 模式或测试）
	}
	prompt := m.prompts.BuildBudgetLimit(g)
	if err := m.launchTurn(ctx, g.ThreadID, prompt); err != nil {
		log.Warn(fmt.Sprintf("[goal] inject closing prompt failed for thread %s: %v", g.ThreadID, err))
		return fmt.Errorf("inject closing prompt: %w", err)
	}
	return nil
}

// truncate 截断字符串到 maxLen（用于日志/错误信息）。
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
