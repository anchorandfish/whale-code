package context

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	aierrors "github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// MaxPTLRetries is the maximum number of prompt-too-long retries for compact requests.
// P0-3 (FACT-ERR-006): when the compact request itself returns prompt-too-long,
// truncate the conversation head and retry, up to this limit.
const MaxPTLRetries = 3

// FallbackLevel 标识 Compact 实际使用的 fallback 级别（候选 B）。
//
// 级别语义：
// - FallbackLevelNone：未触发 Compact（消息数不足）
// - FallbackLevelAISummary：L1 AI 摘要成功
// - FallbackLevelKeyContext：L2 关键上下文保留（AI 失败降级）
// - FallbackLevelRuleExtract：L3 规则提取（兜底）
type FallbackLevel int

const (
	FallbackLevelNone        FallbackLevel = iota // 未触发 Compact
	FallbackLevelAISummary                        // L1: AI 摘要成功
	FallbackLevelKeyContext                       // L2: 关键上下文保留（AI 失败降级）
	FallbackLevelRuleExtract                      // L3: 规则提取（兜底）
)

// String 返回级别的字符串表示（供可观测 / Metadata）。
func (l FallbackLevel) String() string {
	switch l {
	case FallbackLevelNone:
		return "none"
	case FallbackLevelAISummary:
		return "ai_summary"
	case FallbackLevelKeyContext:
		return "key_context"
	case FallbackLevelRuleExtract:
		return "rule_extract"
	default:
		return "unknown"
	}
}

// KeyContextConfig 关键上下文保留配置（候选 B）。
// 对标同类 CLI collect_user_messages（@2026-03-31快照）。
// 本设计扩展为 5 类关键信息（user 消息 + 决策 + 错误修复 + 文件路径 + 待办任务）。
type KeyContextConfig struct {
	PreserveUserMessages bool // 保留所有 user 消息原文（默认 true）
	PreserveDecisions    bool // 保留关键决策（含 decided/chose/approach/solution/decision）
	PreserveErrorFixes   bool // 保留错误修复（含 error/fix/fixed/bug 上下文）
	PreserveFilePaths    bool // 保留文件路径
	PreservePendingTasks bool // 保留待办任务（含 TODO/pending/next step）
	MaxPreservedMessages int  // 最多保留消息数（默认 20，避免摘要过长）
}

// DefaultKeyContextConfig 返回默认关键上下文配置（全开 + 20 条上限）。
// 对标同类 CLI collect_user_messages 全保留思想（@2026-03-31快照）。
func DefaultKeyContextConfig() KeyContextConfig {
	return KeyContextConfig{
		PreserveUserMessages: true,
		PreserveDecisions:    true,
		PreserveErrorFixes:   true,
		PreserveFilePaths:    true,
		PreservePendingTasks: true,
		MaxPreservedMessages: 20,
	}
}

// ExtractKeyContext 从消息列表提取关键上下文（候选 B）。
//
// 返回结构化关键上下文字符串，作为 fallback Level 2 摘要。
// 当所有提取项为空时返回空字符串，调用方降级到 L3 规则提取。
//
// 对标同类 CLI collect_user_messages（@2026-03-31快照）：
// 本设计扩展为 5 类关键信息（user 消息 + 决策 + 错误修复 + 文件路径 + 待办任务）。
// 当 cfg 为零值时回填默认配置（向后兼容）。
func ExtractKeyContext(messages []session.Message, cfg KeyContextConfig) string {
	if len(messages) == 0 {
		return ""
	}
	// 零值回填默认配置（向后兼容旧调用方）
	if cfg.MaxPreservedMessages == 0 {
		cfg = DefaultKeyContextConfig()
	}

	var parts []string
	parts = append(parts, "[Key Context Summary (AI fallback L2)]")

	var userMsgs []string
	var decisions []string
	var errorFixes []string
	var filePaths []string
	var pendingTasks []string

	decisionKeywords := []string{"decided", "chose", "approach", "solution", "decision"}
	errorKeywords := []string{"error", "fix", "fixed", "bug"}
	taskKeywords := []string{"todo", "pending", "next step", "next steps"}

	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if block.Type != "text" || block.Text == "" {
				continue
			}
			text := block.Text
			lower := strings.ToLower(text)

			if cfg.PreserveUserMessages && msg.Role == session.RoleUser {
				intent := truncateRunes(strings.TrimSpace(text), 120)
				if intent != "" && len(userMsgs) < cfg.MaxPreservedMessages {
					userMsgs = append(userMsgs, intent)
				}
			}

			if cfg.PreserveDecisions && containsAnyKeyword(lower, decisionKeywords) {
				d := truncateRunes(strings.TrimSpace(text), 150)
				if len(decisions) < 10 {
					decisions = append(decisions, d)
				}
			}

			if cfg.PreserveErrorFixes && containsAnyKeyword(lower, errorKeywords) {
				e := truncateRunes(strings.TrimSpace(text), 150)
				if len(errorFixes) < 10 {
					errorFixes = append(errorFixes, e)
				}
			}

			if cfg.PreserveFilePaths {
				extractPaths(text, &filePaths)
			}

			if cfg.PreservePendingTasks && containsAnyKeyword(lower, taskKeywords) {
				t := truncateRunes(strings.TrimSpace(text), 120)
				if len(pendingTasks) < 10 {
					pendingTasks = append(pendingTasks, t)
				}
			}
		}
	}

	if len(userMsgs) > 0 {
		parts = append(parts, "\nUser Messages:")
		for i, m := range userMsgs {
			parts = append(parts, fmt.Sprintf("  %d. %s", i+1, m))
		}
	}
	if len(decisions) > 0 {
		parts = append(parts, "\nKey Decisions:")
		for _, d := range decisions {
			parts = append(parts, fmt.Sprintf("  - %s", d))
		}
	}
	if len(errorFixes) > 0 {
		parts = append(parts, "\nError Fixes:")
		for _, e := range errorFixes {
			parts = append(parts, fmt.Sprintf("  - %s", e))
		}
	}
	if len(filePaths) > 0 {
		unique := uniqueStrings(filePaths)
		if len(unique) > 15 {
			unique = unique[:15]
		}
		parts = append(parts, "\nFile Paths:")
		parts = append(parts, fmt.Sprintf("  %s", strings.Join(unique, ", ")))
	}
	if len(pendingTasks) > 0 {
		// 防"历史被当成待办"：关键词提取不区分时态，历史中的 "TODO/next step"
		// 文本可能早已完成。标注为"候选/可能已完成"，要求读者结合 Current Work 判断，
		// 避免压缩后模型把旧指令重新执行。
		parts = append(parts, "\nPending Tasks: (keyword-extracted, may already be completed - verify against recent messages before acting)")
		for _, t := range pendingTasks {
			parts = append(parts, fmt.Sprintf("  - %s", t))
		}
	}

	// 若所有提取项为空，返回空字符串触发 L3 降级
	if len(userMsgs) == 0 && len(decisions) == 0 && len(errorFixes) == 0 &&
		len(filePaths) == 0 && len(pendingTasks) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// containsAnyKeyword 检查 s 是否包含 keywords 中任一关键词（大小写不敏感）。
func containsAnyKeyword(s string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// AICompactor is a context compactor that uses AI to generate summaries
// modeled after a comparable CLI compact: calls AI to generate structured summary
//
// P0-6 (X6) 并发保护：mu sync.Mutex 保护 circuitBreaker 访问与 PTL 重试状态，
// 避免并发压缩时的数据竞争（FACT-ERR-005）。
//
// P0-3 (X3) PTL 重试：ptlRetryCount / maxPTLRetries 控制 compact 请求自身
// prompt-too-long 时的截断重试（FACT-ERR-006）。
//
// 候选 B：3 级 fallback 链（L1 AI 摘要 → L2 关键上下文 → L3 规则提取）。
// keyContextCfg 配置 L2 提取规则；lastFallbackLevel 记录上次 Compact 实际级别。
type AICompactor struct {
	mu              sync.Mutex // P0-6: 保护 circuitBreaker 访问
	aiClient        ai.Client
	estimator       *ai.TokenEstimator
	circuitBreaker  *CircuitBreaker
	fallback        *Compactor
	coordinator     *ai.RecoveryCoordinator
	retryConfig     ai.RetryConfig
	fallbackTracker *ai.FallbackTracker
	maxPTLRetries   int // P0-3: compact 请求 PTL 重试上限（默认 MaxPTLRetries=3）

	// compactModel 压缩摘要专用模型（省 token 第 1 批，2026-10-14）。
	// 空字符串 = 沿用主模型（默认，行为与今天逐字相同）。非空时**只有摘要这一次调用**换模型；
	// 该模型失败会自动回退主模型重算一次（见 generateAISummaryWithCostRecorder）。
	compactModel string

	// SNAPSHOT-1：本轮新增字段
	// keyContextCfg 关键上下文保留配置（L2 用），零值时 Compact 入口回填 DefaultKeyContextConfig()
	keyContextCfg KeyContextConfig
	// SNAPSHOT-2：上次 Compact 实际 fallback 级别（供可观测，通过 LastFallbackLevel() 读取）
	lastFallbackLevel FallbackLevel
}

// NewAICompactor creates AI compactor
func NewAICompactor(aiClient ai.Client, estimator *ai.TokenEstimator) *AICompactor {
	return &AICompactor{
		aiClient:       aiClient,
		estimator:      estimator,
		circuitBreaker: NewCircuitBreaker(3),
		fallback:       NewCompactor(estimator),
		retryConfig:    ai.DefaultRetryConfig(),
		maxPTLRetries:  MaxPTLRetries,
	}
}

// SetKeyContextConfig 注入关键上下文保留配置（候选 B）。
// 配置 L2 fallback 的关键信息提取规则。
// 线程安全：通过 c.mu 保护。
func (c *AICompactor) SetKeyContextConfig(cfg KeyContextConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keyContextCfg = cfg
}

// SetCompactModel 注入压缩摘要专用模型（省 token 第 1 批，2026-10-14）。
//
// 语义：空串 = 沿用主模型（默认，行为不变）；非空时只有摘要这一次调用换用它，
// 失败自动回退主模型。线程安全：与 keyContextCfg 同规矩，持 c.mu。
func (c *AICompactor) SetCompactModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compactModel = model
}

// CompactModel 返回当前配置的压缩专用模型（空 = 沿用主模型）。供可观测/测试读取。
func (c *AICompactor) CompactModel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compactModel
}

// LastFallbackLevel 返回上次 Compact 实际使用的 fallback 级别（候选 B）。
// 供可观测模块订阅（metrics / 日志）。
// 线程安全：通过 c.mu 保护（落实审查建议优化 1）。
func (c *AICompactor) LastFallbackLevel() FallbackLevel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastFallbackLevel
}

// SetRecoveryConfig sets the recovery coordinator, retry config, and fallback tracker
func (c *AICompactor) SetRecoveryConfig(coordinator *ai.RecoveryCoordinator, retryConfig ai.RetryConfig, fallbackTracker *ai.FallbackTracker) {
	c.coordinator = coordinator
	c.retryConfig = retryConfig
	c.fallbackTracker = fallbackTracker
}

// estimateSessionMessagesTokens estimates token count for session messages
func estimateSessionMessagesTokens(estimator *ai.TokenEstimator, messages []session.Message) int {
	if estimator == nil {
		return 0
	}
	return estimator.EstimateSessionMessagesTokens(messages)
}

// Compact executes AI compaction without cost recording. Callers that own a
// provider request should pass an operation-scoped recorder explicitly.
func (c *AICompactor) Compact(messages []session.Message, config CompactionConfig) *CompactResult {
	return c.CompactWithCostRecorder(messages, config, nil)
}

// CompactWithCostRecorder keeps billing attribution private to one compaction
// operation even when a shared AICompactor is used concurrently.
func (c *AICompactor) CompactWithCostRecorder(messages []session.Message, config CompactionConfig, costRecorder *CompactCostRecorder) *CompactResult {
	if config.Trigger == "" {
		config.Trigger = CompactionTriggerAuto
	}
	effectivePreserve := config.EffectivePreserveCount()
	config.PreserveRecentMessages = effectivePreserve
	if len(messages) <= effectivePreserve {
		tokens := estimateSessionMessagesTokens(c.estimator, messages)
		c.mu.Lock()
		c.lastFallbackLevel = FallbackLevelNone
		c.mu.Unlock()
		return &CompactResult{
			CompactedMessages: messages,
			RemovedCount:      0,
			NoOpReason:        CompactionNoOpInsufficientMessages,
			EligibleCount:     len(messages),
			PreserveCount:     effectivePreserve,
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
			PreTokens:         tokens,
			PostTokens:        tokens,
		}
	}

	if c.circuitBreaker.IsOpen() {
		tokens := estimateSessionMessagesTokens(c.estimator, messages)
		c.mu.Lock()
		c.lastFallbackLevel = FallbackLevelNone
		c.mu.Unlock()
		return &CompactResult{
			CompactedMessages: messages,
			RemovedCount:      0,
			NoOpReason:        CompactionNoOpCircuitBreaker,
			EligibleCount:     len(messages),
			PreserveCount:     effectivePreserve,
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
			PreTokens:         tokens,
			PostTokens:        tokens,
		}
	}

	// 候选 B：读取 keyContextCfg（零值回填默认，落实审查建议优化 3）
	c.mu.Lock()
	keyCtxCfg := c.keyContextCfg
	c.mu.Unlock()
	if keyCtxCfg.MaxPreservedMessages == 0 {
		keyCtxCfg = DefaultKeyContextConfig()
	}

	preserved := messages[len(messages)-effectivePreserve:]
	compacted := messages[:len(messages)-effectivePreserve]

	// 候选 B：3 级 fallback 链
	// L1: AI 摘要（含 PTL 重试 + 断路器）
	// L2: 关键上下文保留（5 类关键信息，对标同类 CLI collect_user_messages）
	// L3: 规则提取（兜底，现有 fallback.generateSummary）
	var fallbackLevel FallbackLevel
	summary, err := c.generateAISummaryWithCostRecorder(compacted, costRecorder)
	if err != nil {
		c.circuitBreaker.RecordFailure()
		// L2：关键上下文保留
		keyCtx := ExtractKeyContext(compacted, keyCtxCfg)
		if keyCtx != "" {
			summary = keyCtx
			fallbackLevel = FallbackLevelKeyContext
		} else {
			// L3：规则提取（兜底）
			summary = c.fallback.generateSummary(compacted)
			fallbackLevel = FallbackLevelRuleExtract
		}
	} else {
		c.circuitBreaker.RecordSuccess()
		fallbackLevel = FallbackLevelAISummary
	}

	// 记录 fallback 级别（落实审查建议优化 1：加锁保护）
	c.mu.Lock()
	c.lastFallbackLevel = fallbackLevel
	c.mu.Unlock()

	summaryMsg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: summary},
		},
	}
	// 候选 B：摘要消息记录 fallback 级别 metadata（供可观测）
	if summaryMsg.Metadata == nil {
		summaryMsg.Metadata = make(map[string]interface{})
	}
	summaryMsg.Metadata["fallback_level"] = fallbackLevel.String()

	result := c.buildPreservedResult(summaryMsg, compacted, preserved, config)

	if config.MaxEstimatedTokens > 0 {
		estimatedTokens := estimateSessionMessagesTokens(c.estimator, result)
		maxPreserve := config.PreserveRecentMessages
		for estimatedTokens > config.MaxEstimatedTokens && maxPreserve > 1 {
			maxPreserve = maxPreserve / 2
			if maxPreserve < 1 {
				maxPreserve = 1
			}
			// G14 修复：maxPreserve 已收敛到 1 时无法继续缩减，break 避免死循环
			// （N11 改进方案 §4.2 G14）。
			if maxPreserve == 1 && len(messages) <= 2 {
				break
			}
			preserved = messages[len(messages)-maxPreserve:]
			compacted = messages[:len(messages)-maxPreserve]
			// 候选 B：循环内复用 3 级 fallback 链
			summary, err = c.generateAISummaryWithCostRecorder(compacted, costRecorder)
			if err != nil {
				keyCtx := ExtractKeyContext(compacted, keyCtxCfg)
				if keyCtx != "" {
					summary = keyCtx
					fallbackLevel = FallbackLevelKeyContext
				} else {
					summary = c.fallback.generateSummary(compacted)
					fallbackLevel = FallbackLevelRuleExtract
				}
			} else {
				fallbackLevel = FallbackLevelAISummary
			}
			c.mu.Lock()
			c.lastFallbackLevel = fallbackLevel
			c.mu.Unlock()
			summaryMsg = session.Message{
				Role: session.RoleSystem,
				Blocks: []session.ContentBlock{
					{Type: "text", Text: summary},
				},
			}
			if summaryMsg.Metadata == nil {
				summaryMsg.Metadata = make(map[string]interface{})
			}
			summaryMsg.Metadata["fallback_level"] = fallbackLevel.String()
			result = c.buildPreservedResult(summaryMsg, compacted, preserved, config)
			estimatedTokens = estimateSessionMessagesTokens(c.estimator, result)
			// G14 修复：若 maxPreserve 已为 1 且仍超预算，继续循环无意义，break
			if maxPreserve == 1 {
				break
			}
		}
	}
	finalTokens := estimateSessionMessagesTokens(c.estimator, result)
	if config.MaxEstimatedTokens > 0 && finalTokens > config.MaxEstimatedTokens {
		preTokens := estimateSessionMessagesTokens(c.estimator, messages)
		return &CompactResult{
			CompactedMessages: messages,
			NoOpReason:        CompactionNoOpBudgetUnachievable,
			PreTokens:         preTokens,
			PostTokens:        preTokens,
			EligibleCount:     len(messages),
			PreserveCount:     effectivePreserve,
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
		}
	}

	// P1-1: mark the summary message (result[0]) as a compact_boundary with
	// metadata. This lets future CompactionPipeline.Execute calls slice the
	// conversation at this boundary via GetMessagesAfterCompactBoundary,
	// avoiding re-compaction of the already-summarized prefix.
	if len(result) > 0 {
		preTokens := estimateSessionMessagesTokens(c.estimator, messages)
		postTokens := estimateSessionMessagesTokens(c.estimator, result)
		SetCompactBoundaryMetadata(&result[0], CompactMetadata{
			Trigger:            string(config.Trigger),
			PreTokens:          preTokens,
			PostTokens:         postTokens,
			MessagesSummarized: len(compacted),
		})
	}

	return &CompactResult{
		CompactedMessages:  result,
		RemovedCount:       len(compacted),
		Summary:            summary,
		Changed:            true,
		PreTokens:          estimateSessionMessagesTokens(c.estimator, messages),
		PostTokens:         estimateSessionMessagesTokens(c.estimator, result),
		EligibleCount:      len(messages),
		PreserveCount:      effectivePreserve,
		PreMessageCount:    len(messages),
		PostMessageCount:   len(result),
		MessagesSummarized: len(compacted),
	}
}

func (c *AICompactor) buildPreservedResult(summary session.Message, compacted, preserved []session.Message, cfg CompactionConfig) []session.Message {
	if cfg.Strategy != config.CompactPreserveUser {
		return append([]session.Message{summary}, preserved...)
	}
	users := c.fallback.extractUserMessages(compacted, cfg.PreservedUserTokenBudget())
	result := make([]session.Message, 0, 1+len(users)+len(preserved))
	result = append(result, summary)
	result = append(result, users...)
	return append(result, preserved...)
}

// generateAISummary calls AI to generate structured summary.
//
// P0-3 (FACT-ERR-006): implements prompt-too-long (PTL) retry for the compact
// request itself. When the AI returns a PTL error, the oldest 20% of the
// conversation being summarized is dropped and the request retried, up to
// maxPTLRetries times (default 3). This mirrors a comparable CLI's
// truncateHeadForPTLRetry strategy for compact requests.
//
// N11 修复（G1 + G2 + G11）：
// - G11：ctx 注入 agentIDKey{}="compact" 给 StreamWithRetry 路径上的
// cost 记账逻辑识别（02 §2.1.2 契约）
// - G1：event loop 将 ai.UsageEvent 交给本次调用私有的 costRecorder，
// 桥接到 N2 metrics + N1 UsageTracker（02 §2.1.2 契约）
// - G2：错误返回改为 aierrors.WithID(err, classifyCompactError(err))
// 走 N6 ErrorID（02 §2.3.2 契约）
func (c *AICompactor) generateAISummary(messages []session.Message) (string, error) {
	return c.generateAISummaryWithCostRecorder(messages, nil)
}

type compactAttemptClient struct {
	ai.Client
	mu          sync.Mutex
	streamModel string
}

func (c *compactAttemptClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	model := req.Model
	if model == "" {
		model = c.Client.Model()
	}
	events, err := c.Client.Stream(ctx, req)
	if err == nil {
		c.mu.Lock()
		c.streamModel = model
		c.mu.Unlock()
	}
	return events, err
}

func (c *compactAttemptClient) StreamModel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streamModel
}

func (c *AICompactor) generateAISummaryWithCostRecorder(messages []session.Message, costRecorder *CompactCostRecorder) (string, error) {
	// Nil-guard: if no AI client is configured, return an error so the
	// caller (Compact) falls back to the local summarizer instead of
	// panicking on c.aiClient.Model() (P0-9 / Phase 3 hook safety).
	if c.aiClient == nil {
		// G2 修复：走 N6 ErrorID（ErrAIFatal：配置错误不可重试）
		return "", aierrors.WithID(fmt.Errorf("AICompactor has no ai.Client configured"), aierrors.ErrAIFatal)
	}

	// 压缩专用模型（空 = 沿用主模型）。锁内取快照，避免与 SetCompactModel 竞态。
	c.mu.Lock()
	summaryModel := c.compactModel
	c.mu.Unlock()

	summary, err := c.runSummaryAttempts(messages, summaryModel, costRecorder)
	if err == nil || summaryModel == "" {
		return summary, err
	}
	// ★ 降级路径（省 token 第 1 批）：配了便宜摘要模型但它失败了
	// （端点写错 / 模型不可用 / 输出不合规）⇒ 用主模型重算一次，
	// 而不是让摘要质量或可用性打折。
	return c.runSummaryAttempts(messages, "", costRecorder)
}

// runSummaryAttempts 是压缩摘要的 PTL 重试主循环。
// modelOverride 为空时用主模型（c.aiClient.Model()）。
func (c *AICompactor) runSummaryAttempts(messages []session.Message, modelOverride string, costRecorder *CompactCostRecorder) (string, error) {
	c.mu.Lock()
	maxPTL := c.maxPTLRetries
	if maxPTL <= 0 {
		maxPTL = MaxPTLRetries
	}
	c.mu.Unlock()

	// currentMessages is truncated on PTL retry; starts as the full set.
	currentMessages := messages

	for ptlAttempt := 0; ptlAttempt <= maxPTL; ptlAttempt++ {
		prompt := BuildCompactPrompt(currentMessages)

		// G11 修复：ctx 注入 AgentID="compact"（02 §2.1.2 契约）。
		// D4 修复（N1 合入后）：迁移到 N1 公开接口 tools.WithAgentID/ExtractAgentID，
		// 复用 tools.agentIDKey 类型（避免 context 包私有 key 与 N1 key 类型不一致）。
		// 递归守卫：maybeTriggerAutoCompact 通过 tools.ExtractAgentID(ctx) 检查
		// agentID=="compact" 跳过 autocompact（改进方案 §3.14，D2 修复已实现）。
		baseCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		compactCtx := tools.WithAgentID(baseCtx, compactAgentID)

		requestModel := modelOverride
		if requestModel == "" {
			requestModel = c.aiClient.Model()
		}
		req := ai.Request{
			SystemPrompt: []string{
				"You are a conversation summary assistant. Your task is to generate precise, structured conversation summaries, preserving all key info。",
			},
			Messages: []ai.MessageEntry{
				{Role: "user", Content: prompt},
			},
			MaxTokens: 4096,
			Model:     requestModel,
		}

		rc := c.retryConfig
		if rc.MaxRetries == 0 {
			rc = ai.DefaultRetryConfig()
		}
		attemptClient := &compactAttemptClient{Client: c.aiClient}
		events, err := ai.StreamWithRetry(compactCtx, attemptClient, req, rc, c.fallbackTracker, c.coordinator)

		if err != nil {
			cancel()
			// P0-3: check if this is a prompt-too-long error and we can retry
			if isCompactPTLError(err) && ptlAttempt < maxPTL && len(currentMessages) > 1 {
				currentMessages = truncateHeadSessionMessages(currentMessages)
				continue
			}
			// G2 修复：wrap N6 ErrorID
			return "", aierrors.WithID(fmt.Errorf("AI summary generation failed: %w", err), classifyCompactError(err))
		}

		var result strings.Builder
		var streamErr error
		var requestUsage ai.RequestUsageAccumulator
		for evt := range events {
			switch e := evt.(type) {
			case ai.TextDeltaEvent:
				result.WriteString(e.Content)
			case ai.UsageEvent:
				requestUsage.Add(e)
			case ai.ErrorEvent:
				streamErr = e.Err
			}
		}
		// Provider-reported usage is a billing fact even when the stream later
		// fails. Record one aggregate per compact provider attempt.
		if usage, ok := requestUsage.Finalize(); ok && costRecorder != nil {
			usageModel := attemptClient.StreamModel()
			if usageModel == "" {
				usageModel = requestModel
			}
			costRecorder.RecordUsageForModel(usageModel, usage)
		}
		cancel()

		if streamErr != nil {
			if isCompactPTLError(streamErr) && ptlAttempt < maxPTL && len(currentMessages) > 1 {
				currentMessages = truncateHeadSessionMessages(currentMessages)
				continue
			}
			// G2 修复：wrap N6 ErrorID
			return "", aierrors.WithID(fmt.Errorf("AI stream error: %w", streamErr), classifyCompactError(streamErr))
		}

		summary := result.String()
		if summary == "" {
			// G2 修复：空摘要归为 ErrAIFatal（不可重试，需用户干预）
			return "", aierrors.WithID(fmt.Errorf("AI returned empty summary"), aierrors.ErrAIFatal)
		}
		return summary, nil
	}

	// G2 修复：PTL 重试耗尽归为 ErrAIRetryable（可重试，可能是临时 prompt 过长）
	return "", aierrors.WithID(fmt.Errorf("AI summary failed after %d PTL retries", maxPTL), aierrors.ErrAIRetryable)
}

// classifyCompactError 根据 AI 错误返回 N6 ErrorID（N11 修复 G2）。
//
// 契约：02 §2.3.2 要求 compact 失败走 ErrAIRetryable/ErrAIFatal，不新增 ErrorID。
// 分类规则：
// - 401/403/413 → ErrAIFatal（认证失败 / prompt 过长不可恢复）
// - 500/502/504/超时/网络错误 → ErrAIRetryable（服务端临时故障）
// - 其他（含 429/529） → ErrAIRetryable（保守可重试，让 retry.go 的 ErrorID 路径细分）
func classifyCompactError(err error) aierrors.ErrorID {
	if err == nil {
		return ""
	}
	var apiErr *ai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401, 403, 413:
			return aierrors.ErrAIFatal
		case 500, 502, 504:
			return aierrors.ErrAIRetryable
		default:
			return aierrors.ErrAIRetryable
		}
	}
	// 超时/网络错误归为可重试
	return aierrors.ErrAIRetryable
}

// isCompactPTLError checks whether an error from the compact AI request is a
// prompt-too-long error (P0-3 / FACT-ERR-006).
func isCompactPTLError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "prompt too long") ||
		strings.Contains(msg, "prompt-too-long")
}

// truncateHeadSessionMessages drops the oldest 20% of session messages to
// recover from prompt-too-long errors during compact (P0-3 / FACT-ERR-006).
// Always keeps at least 1 message. Mirrors ai.truncateHeadForPTLRetry but
// operates on []session.Message.
func truncateHeadSessionMessages(messages []session.Message) []session.Message {
	if len(messages) <= 1 {
		return messages
	}
	// Drop oldest 20%; keep the remaining 80%.
	keep := len(messages) * 4 / 5
	if keep < 1 {
		keep = 1
	}
	return messages[len(messages)-keep:]
}

// BuildCompactPrompt builds the compact prompt
// modeled after a comparable CLI compact prompt format, delegates history formatting to formatConversationHistory
//
// N11 修复（G7，P1-7）：5 段式 → 9 段式。
// 借鉴同类 CLI 的 8 段 + 同类 CLI
// 的 "All user messages" 全保留启发，9 段覆盖：
// 1. Primary Request and Intent（用户主要请求与意图）
// 2. Key Technical Concepts（关键技术概念）
// 3. Files and Code Sections（文件与代码段）
// 4. Errors and Fixes（错误与修复）
// 5. Problem Solving（问题解决过程）
// 6. All User Messages（所有用户消息摘要，同类 CLI 启发）
// 7. Pending Tasks（待办任务）
// 8. Current Work（当前工作状态）
// 9. Next Steps（下一步计划）
func BuildCompactPrompt(messages []session.Message) string {
	var sb strings.Builder

	sb.WriteString("Please generate a structured summary of the following conversation history. The summary must include the following sections：\n\n")
	sb.WriteString("<analysis>\n")
	sb.WriteString("Analyze the main content and direction of the conversation, identify key info, technical decisions and current state\n")
	sb.WriteString("</analysis>\n\n")
	sb.WriteString("<summary>\n")
	sb.WriteString("1. Primary Request and Intent: what the user primarily wants to accomplish and their core goal\n")
	sb.WriteString("2. Key Technical Concepts: key technical concepts, frameworks, and patterns involved\n")
	sb.WriteString("3. Files and Code Sections: which files were operated on, what modifications were made, key code snippets\n")
	sb.WriteString("4. Errors and Fixes: what errors were encountered, how they were fixed, root causes\n")
	sb.WriteString("5. Problem Solving: what problems were resolved, what approaches were used, key decisions made\n")
	sb.WriteString("6. All User Messages: list the core content of each user message in order (preserve original intent, mark as historical/past instructions NOT active tasks)\n")
	sb.WriteString("7. Pending Tasks: explicitly distinguish completed vs pending items; do NOT include historical commands or already-executed instructions as pending actions\n")
	sb.WriteString("8. Current Work: what work is currently in progress, last assistant action\n")
	sb.WriteString("9. Next Steps: recommended next actions, suggested direction\n")
	sb.WriteString("</summary>\n\n")
	sb.WriteString("--- Conversation History Start ---\n\n")

	sb.WriteString(formatConversationHistory(messages))

	sb.WriteString("\n--- Conversation History End ---\n\n")
	sb.WriteString("Please generate a structured summary following the <analysis> and <summary> format above：\n")
	// 防"历史被当成待办"：摘要读者是下一轮的模型，若历史指令被写进 Pending Tasks，
	// 压缩后模型会把已执行过的旧指令重新执行。收尾指令二次强调（attention 对
	// prompt 末尾权重更高），与 §6/§7 段内要求互为冗余保险。
	sb.WriteString("Important: messages in the history are PAST context. Only genuinely unfinished work goes in Pending Tasks; anything already done or superseded must be described as completed, never as a pending action.")

	return sb.String()
}
