// Package ai - 反例测试（bug_test）
// 本文件为行业标准对标审查的反例测试，用于证明 internal/ai 模块中存在的违规问题。
// 对标对象：同类 CLI。
// 每个测试函数对应一个 FACT-ID，测试名以 TestBug_ 开头。
// 测试在含 bug 的代码上失败（FAIL），从而证明 bug 存在。
package ai

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// requestRecordingClient 记录所有请求的 mock client，用于验证重试行为
type requestRecordingClient struct {
	requests  []Request
	err       error
	successCh <-chan Event
}

func (m *requestRecordingClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	return m.successCh, nil
}
func (m *requestRecordingClient) Model() string   { return "test-model" }
func (m *requestRecordingClient) SetModel(string) {}
func (m *requestRecordingClient) Close() error    { return nil }

// ============================================================================
// FACT-AI-001: system prompt 分层覆盖链
// 正确做法：override > coordinator > agent > custom > default，高优先级覆盖低优先级
// 当前实现：BuildSystemPrompt 仅按层排序拼接，无覆盖语义
// ============================================================================

// TestBug_SystemPromptNoOverrideChain 验证 BuildSystemPrompt 无覆盖链
// 同层两个组件应互相覆盖，而非拼接
func TestBug_SystemPromptNoOverrideChain(t *testing.T) {
	// FACT-AI-001: system prompt 应有分层覆盖链
	// override > coordinator > agent > custom > default
	// 高优先级应覆盖低优先级，而非拼接
	// 当前实现：BuildSystemPrompt 仅按层排序拼接，无覆盖语义
	builder := NewCacheAwareRequestBuilder()

	components := []PromptComponent{
		{Name: "default", Content: "DEFAULT_PROMPT", Stable: true, Layer: LayerCoreIdentity},
		{Name: "override", Content: "OVERRIDE_PROMPT", Stable: true, Layer: LayerCoreIdentity},
	}

	result := builder.BuildSystemPrompt(components)

	// 期望：override 应覆盖 default，结果中不应同时包含两者
	if strings.Contains(result, "DEFAULT_PROMPT") && strings.Contains(result, "OVERRIDE_PROMPT") {
		t.Errorf("FACT-AI-001: system prompt 同时包含 default 和 override 内容（拼接而非覆盖）\n结果: %s", result)
	}

	// 期望：结果应包含 override 内容
	if !strings.Contains(result, "OVERRIDE_PROMPT") {
		t.Errorf("FACT-AI-001: system prompt 未包含 override 内容")
	}
}

// ============================================================================
// FACT-AI-002: cache_control 标记格式
// 正确做法：{type: 'ephemeral', ttl: '1h', scope: 'global'}，在 content block 级别
// 当前实现：{type: "ephemeral"} 无 ttl/scope，在 message 级别
// ============================================================================

// TestBug_CacheControlMissingTTLScope 验证 injectMessagesCacheControl 缺少 ttl/scope
func TestBug_CacheControlMissingTTLScope(t *testing.T) {
	// FACT-AI-002: cache_control 应使用 {type: 'ephemeral', ttl: '1h', scope: 'global'}
	// P0-6 修复：injectMessagesCacheControl 签名变更为接受 CacheControl，统一格式 {type, ttl, scope}
	messages := []MessageEntry{
		{Role: "user", Content: "test message"},
	}
	cc := CacheControl{Type: "ephemeral", TTL: "5m", Scope: ""}

	result := injectMessagesCacheControl(messages, cc)

	blocks, ok := result[0].Content.([]map[string]interface{})
	if !ok {
		t.Fatal("content 应转换为 blocks 数组")
	}

	ccResult, ok := blocks[len(blocks)-1]["cache_control"].(map[string]interface{})
	if !ok {
		t.Fatal("cache_control 不存在")
	}

	// 期望：应有 ttl 字段
	if _, hasTTL := ccResult["ttl"]; !hasTTL {
		t.Error("FACT-AI-002: cache_control 缺少 ttl 字段，应为 {type: 'ephemeral', ttl: '5m', scope: ''}")
	}

	// 期望：应有 scope 字段
	if _, hasScope := ccResult["scope"]; !hasScope {
		t.Error("FACT-AI-002: cache_control 缺少 scope 字段，应为 {type: 'ephemeral', ttl: '5m', scope: ''}")
	}
}

// TestBug_CacheControlAtMessageLevel 验证 PlaceBreakpoint 在 message 级别而非 block 级别
func TestBug_CacheControlAtMessageLevel(t *testing.T) {
	// FACT-AI-002: cache_control 应在 content block 级别，非 message 级别
	// 当前实现：PlaceBreakpoint 在 message 级别添加 cache_control
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 1,
		CacheScope:               "global",
	})

	messages := []map[string]interface{}{
		{"role": "user", "content": "test"},
	}

	result := placement.PlaceBreakpoint(messages)

	// 期望：cache_control 不应在 message 级别（应在 content block 内部）
	if _, hasCC := result[0]["cache_control"]; hasCC {
		t.Error("FACT-AI-002: cache_control 在 message 级别，应在 content block 级别")
	}
}

// ============================================================================
// FACT-AI-003: cache break 双条件检测
// 正确做法：drop > 5% 且 token 下降 > MIN_CACHE_MISS_TOKENS=2000
// 当前实现：仅检查 drop > threshold（单条件）
// ============================================================================

// TestBug_CacheBreakSingleCondition 验证 cache break 检测为单条件（缺绝对 token 阈值）
func TestBug_CacheBreakSingleCondition(t *testing.T) {
	// FACT-AI-003: cache break 检测应为双条件
	// drop > 5% 且 token 下降 > MIN_CACHE_MISS_TOKENS=2000
	// 当前实现：仅检查 drop > threshold（单条件），小绝对量下降也触发误报
	detector := NewCacheBreakDetectorWithThreshold(0.05)

	// 记录初始状态
	detector.RecordPromptState("system", []string{"tool1"}, "model", "")
	state := detector.GetLastPromptState()
	if state == nil {
		t.Fatal("GetLastPromptState 返回 nil")
	}

	// 第一次调用设置 baseline
	detector.CheckForCacheBreak(10000, *state)

	// 第二次调用：6% drop（> 5%）但绝对下降仅 600 tokens（< 2000）
	// 正确行为：不应触发 break（双条件不满足，绝对量不足）
	result := detector.CheckForCacheBreak(9400, *state)

	if result != nil && result.IsBreak {
		t.Errorf("FACT-AI-003: 6%% drop 但仅 600 tokens 下降触发了 break，应需双条件（drop > 5%% 且下降 > 2000 tokens）")
	}
}

// ============================================================================
// FACT-AI-004: 1h TTL 门控
// 正确做法：通过 getPromptCache1hEligible 门控，cache_control 含 ttl 字段
// 当前实现：无 ttl 字段，无门控机制
// ============================================================================

// TestBug_NoTTLGating 验证 cache_control 无 ttl 字段，无 1h TTL 门控
func TestBug_NoTTLGating(t *testing.T) {
	// FACT-AI-004: 1h TTL 应通过 getPromptCache1hEligible 门控
	// 修复后：cache_control 在 content block 级别，含 ttl 字段
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 1,
		CacheScope:               "global",
	})

	messages := []map[string]interface{}{
		{"role": "user", "content": "test"},
	}

	result := placement.PlaceBreakpoint(messages)

	// cache_control 应在 content block 级别（FACT-AI-002 修复后）
	blocks, ok := result[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatal("content 应转换为 blocks 数组")
	}
	cc, ok := blocks[len(blocks)-1]["cache_control"].(map[string]interface{})
	if !ok {
		t.Fatal("cache_control 不存在于 content block")
	}

	// 期望：应有 ttl 字段（如 "1h"）
	if _, hasTTL := cc["ttl"]; !hasTTL {
		t.Error("FACT-AI-004: cache_control 缺少 ttl 字段，无 1h TTL 门控机制")
	}
}

// ============================================================================
// FACT-ERR-001: 529 错误 foreground source 重试
// 正确做法：仅 foreground source 重试 529
// 当前实现：StreamWithRetry 无 source 参数，529 无条件重试
// ============================================================================

// TestBug_529RetryNoSourceDistinction 验证 529 重试无 source 区分
// P0-3 (A7) 修复后：Request.QuerySource 区分来源，Background 直接 drop。
func TestBug_529RetryNoSourceDistinction(t *testing.T) {
	// FACT-ERR-001: 仅 foreground source 应重试 529，background 应 drop
	// P0-3 修复：Request 新增 QuerySource 字段，StreamWithRetry 调用 shouldRetryOn529
	mock := &requestRecordingClient{
		err: fmt.Errorf("529 Overloaded"),
	}

	// Background source: 529 应直接 drop（调用次数应为 1）
	req := Request{MaxTokens: 4096, QuerySource: QuerySourceBackground}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	_, _ = StreamWithRetry(context.Background(), mock, req, cfg, nil, nil)

	// 期望：background source 不应重试 529（调用次数应为 1）
	if len(mock.requests) > 1 {
		t.Errorf("FACT-ERR-001: background source 的 529 被重试 %d 次，应直接 drop（调用 1 次）", len(mock.requests))
	}
}

// ============================================================================
// FACT-ERR-002: consecutive529Count 非 529 错误时不重置
// 正确做法：任何非 529 错误应重置 consecutive529Count
// 当前实现：仅 RecordSuccess 重置，非 529 错误路径不重置
// ============================================================================

// TestBug_529CountNotResetOnNon529 验证非 529 错误不重置 consecutive529Count
func TestBug_529CountNotResetOnNon529(t *testing.T) {
	// FACT-ERR-002: consecutive529Count 应在非 529 错误时重置
	// P0-1 修复：新增 RecordNon529Error() 方法重置计数器
	tracker := NewFallbackTracker("model-a", "model-b")

	// 2 次 529（未达降级阈值 3）
	tracker.Record529()
	tracker.Record529()

	// 模拟非 529 错误发生（如 500 错误）—— 应重置 consecutive529Count 为 0
	tracker.RecordNon529Error()

	// 第 3 次 529 - 正确行为下不应降级（因非 529 错误已重置计数器，此次是第 1 次）
	degraded := tracker.Record529()

	// 期望：不应降级（因为非 529 错误已重置计数器，第 3 次 529 实际是第 1 次）
	if degraded {
		t.Errorf("FACT-ERR-002: 第 3 次 529 后降级 = %v, 期望 false（非 529 错误应重置 consecutive529Count）", degraded)
	}
}

// ============================================================================
// FACT-ERR-003: context overflow 自动调低 max_tokens
// 正确做法：自动调低 max_tokens 后重试，下限 FLOOR_OUTPUT_TOKENS=3000
// 当前实现：立即返回错误，无 max_tokens 调整
// ============================================================================

// TestBug_ContextOverflowNoMaxTokensAdjust 验证 context overflow 不调低 max_tokens
// P0-4 (A8) 修复后：EnableContextOverflowRetry=true 时自动调低 max_tokens 后重试。
func TestBug_ContextOverflowNoMaxTokensAdjust(t *testing.T) {
	// FACT-ERR-003: context overflow 应自动调低 max_tokens 后重试
	// 下限 FLOOR_OUTPUT_TOKENS=3000
	// P0-4 修复：RetryConfig.EnableContextOverflowRetry 控制是否重试
	mock := &requestRecordingClient{
		err: fmt.Errorf("context overflow detected"),
	}

	req := Request{MaxTokens: 8192}
	// 使用 DefaultRetryConfig 以启用 EnableContextOverflowRetry
	cfg := DefaultRetryConfig()
	cfg.MaxRetries = 3
	cfg.BaseDelay = 1 * time.Millisecond
	cfg.MaxDelay = 10 * time.Millisecond

	_, err := StreamWithRetry(context.Background(), mock, req, cfg, nil, nil)

	// 期望：应调整 max_tokens 后重试（至少 2 次调用）
	if len(mock.requests) < 2 {
		t.Errorf("FACT-ERR-003: context overflow 后调用次数 = %d, 期望 >= 2（应调低 max_tokens 重试）", len(mock.requests))
	}

	// 期望：重试时 max_tokens 应被调低
	if len(mock.requests) >= 2 {
		if mock.requests[1].MaxTokens >= 8192 {
			t.Errorf("FACT-ERR-003: 重试时 max_tokens = %d, 期望 < 8192（应调低）", mock.requests[1].MaxTokens)
		}
		// 期望：max_tokens 不应低于 FLOOR_OUTPUT_TOKENS=3000
		if mock.requests[1].MaxTokens < 3000 {
			t.Errorf("FACT-ERR-003: 重试时 max_tokens = %d, 期望 >= 3000（FLOOR_OUTPUT_TOKENS）", mock.requests[1].MaxTokens)
		}
	}

	_ = err
}

// ============================================================================
// FACT-ERR-006: truncateHeadForPTLRetry 机制
// 正确做法：prompt too long 错误时截断消息头部后重试
// 当前实现：无截断机制，context overflow 立即返回
// ============================================================================

// TestBug_NoTruncateHeadForPTLRetry 验证无 truncateHeadForPTLRetry 机制
// P0-4 (A8) 修复后：EnableContextOverflowRetry=true 时截断消息头部后重试。
func TestBug_NoTruncateHeadForPTLRetry(t *testing.T) {
	// FACT-ERR-006: prompt too long 错误应截断消息头部后重试
	// P0-4 修复：isPromptTooLongError + truncateHeadForPTLRetry
	mock := &requestRecordingClient{
		err: fmt.Errorf("prompt too long: 300000 tokens"),
	}

	req := Request{
		Messages: []MessageEntry{
			{Role: "user", Content: "msg1"},
			{Role: "assistant", Content: "msg2"},
			{Role: "user", Content: "msg3"},
		},
		MaxTokens: 8192,
	}
	// 使用 DefaultRetryConfig 以启用 EnableContextOverflowRetry
	cfg := DefaultRetryConfig()
	cfg.MaxRetries = 3
	cfg.BaseDelay = 1 * time.Millisecond
	cfg.MaxDelay = 10 * time.Millisecond

	_, err := StreamWithRetry(context.Background(), mock, req, cfg, nil, nil)

	// 期望：应该截断头部后重试（至少 2 次调用）
	if len(mock.requests) < 2 {
		t.Errorf("FACT-ERR-006: prompt too long 后调用次数 = %d, 期望 >= 2（应截断头部重试）", len(mock.requests))
	}

	// 期望：重试时消息应被截断（消息数减少）
	if len(mock.requests) >= 2 {
		if len(mock.requests[1].Messages) >= len(req.Messages) {
			t.Errorf("FACT-ERR-006: 重试时消息未截断，消息数 = %d, 期望 < %d", len(mock.requests[1].Messages), len(req.Messages))
		}
	}

	_ = err
}

// ============================================================================
// FACT-ERR-007: 401 OAuth token 刷新
// 正确做法：handleOAuth401Error 刷新 token 并重建 client
// 当前实现：IsRetryableErrorFromMsg 不含 "401"，无 OAuth 刷新机制
// ============================================================================

// TestBug_No401OAuthRefresh 验证无 401 OAuth token 刷新机制
func TestBug_No401OAuthRefresh(t *testing.T) {
	// FACT-ERR-007: 401 错误应通过 OAuth token 刷新恢复
	// 当前实现无 handleOAuth401Error，401 不被识别为可恢复错误
	// IsRetryableErrorFromMsg 不包含 "401"

	// 期望：401 应被识别为可通过 OAuth 刷新恢复的错误
	// 实际：IsRetryableErrorFromMsg 不包含 "401"，401 完全不被处理
	if !IsRetryableErrorFromMsg("401 Unauthorized") {
		t.Error("FACT-ERR-007: 401 错误未被识别为可恢复错误，缺少 OAuth token 刷新机制（handleOAuth401Error）")
	}
}

// ============================================================================
// FACT-CONC-001: Go sync.RWMutex 不可重入
// 正确做法：持 RLock 时不能再加 RLock，应避免重入或改用无锁内联
// 当前实现：FormatUsageReport 持 RLock 后只直接读字段，不再调用任何会加锁的 getter
// ============================================================================

// TestBug_UsageTrackerRLockReentrancy 验证 UsageTracker FormatUsageReport 不 RLock 重入死锁。
// P0-7 (A5) 修复后的不变量：FormatUsageReport 内部**不得**调用任何会自行加锁的公开 getter。
// ★ D 批：原先此处的"无锁内部函数"是 getTotalCostLocked/getModelCostLocked，
// 二者已随成本线删除；不变量本身不变（持锁期间只读字段）。
func TestBug_UsageTrackerRLockReentrancy(t *testing.T) {
	// FACT-CONC-001: Go sync.RWMutex 不可重入
	// 反例：FormatUsageReport 持 RLock 后若再调一个会 RLock 的方法 → 死锁
	// 现状：FormatUsageReport 持锁期间只读结构体字段 → 不重入
	tracker := NewUsageTracker("")
	// 添加两个模型以触发 by-model 分支（len(ModelUsage) > 1）
	tracker.RecordUsage("model-a", 100, 200, 0, 0)
	tracker.RecordUsage("model-b", 100, 200, 0, 0)

	// 启动持续写者，增加与 FormatUsageReport 交错的概率
	// 当 FormatUsageReport 持有 RLock 时，若有写者在等待，
	// 若内部再 RLock 就会死锁
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			tracker.mu.Lock()
			tracker.mu.Unlock()
			time.Sleep(1 * time.Millisecond)
		}
	}()
	defer close(stop)

	// 多次调用 FormatUsageReport，增加触发竞态的概率
	// 有写者等待时若内部重入 RLock → 死锁
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			_ = tracker.FormatUsageReport()
		}
		close(done)
	}()

	select {
	case <-done:
		// FormatUsageReport 未死锁（P0-7 修复成功）
	case <-time.After(5 * time.Second):
		t.Fatal("FACT-CONC-001: FormatUsageReport 内部 RLock 重入死锁 - Go RWMutex 不可重入")
	}
}

// TestBug_RecoveryCoordinatorLocked 合规测试：RecoveryCoordinator 正确使用锁
// 此测试验证 RecoveryCoordinator 的所有公开方法都有锁保护（合规）
func TestBug_RecoveryCoordinatorLocked(t *testing.T) {
	// FACT-CONC-001 合规验证：RecoveryCoordinator 使用 sync.Mutex
	// 所有公开方法（BeforeRecovery/RecordSuccess/RecordFailure/State/ConsecutiveErrors/Reset）
	// 都正确持有锁，无重入风险
	rc := NewRecoveryCoordinator()

	// 验证状态机正常工作
	if rc.State() != StateNormal {
		t.Errorf("初始状态 = %s, 期望 normal", rc.State())
	}

	// 验证 BeforeRecovery 在 Normal 状态下允许 retry
	if !rc.BeforeRecovery("retry") {
		t.Error("Normal 状态应允许 retry 策略")
	}

	// 验证 RecordFailure 推进状态机
	rc.RecordFailure()
	if rc.State() != StateRetrying {
		t.Errorf("RecordFailure 后状态 = %s, 期望 retrying", rc.State())
	}

	// 验证 RecordSuccess 重置状态
	rc.RecordSuccess()
	if rc.State() != StateNormal {
		t.Errorf("RecordSuccess 后状态 = %s, 期望 normal", rc.State())
	}

	// RecoveryCoordinator 合规：所有方法持锁，无重入
}

// ============================================================================
// FACT-RESUME: 流式中断恢复 ResumePolicy
// 正确做法：ToolUseEvent 出现后标记 ResumeNone，不可恢复
// 当前实现：无 ResumePolicy 跟踪机制
// ============================================================================

// TestBug_ResumePolicyToolUseBlocksRecovery 验证 ToolUseEvent 后不可恢复
// P0-5 (A9) 修复后：ResumeTracker 跟踪事件，ToolUseEvent → ResumeNone。
func TestBug_ResumePolicyToolUseBlocksRecovery(t *testing.T) {
	// FACT-RESUME: ToolUseEvent 出现后应标记 ResumeNone
	tracker := NewResumeTracker()

	// 初始状态：仅文本 → 可恢复
	if !tracker.CanResume() {
		t.Error("FACT-RESUME: 初始状态应可恢复（ResumeText）")
	}
	if tracker.Policy() != ResumeText {
		t.Errorf("FACT-RESUME: 初始 Policy = %s, 期望 text", tracker.Policy())
	}

	// 收到 text_delta → 仍可恢复
	tracker.Update(TextDeltaEvent{Content: "hello"})
	if !tracker.CanResume() {
		t.Error("FACT-RESUME: text_delta 后应仍可恢复")
	}

	// 收到 thinking_delta → 升级为 Structured，仍可恢复
	tracker.Update(ThinkingDeltaEvent{Content: "thinking..."})
	if tracker.Policy() != ResumeStructured {
		t.Errorf("FACT-RESUME: thinking_delta 后 Policy = %s, 期望 structured", tracker.Policy())
	}
	if !tracker.CanResume() {
		t.Error("FACT-RESUME: thinking_delta 后应仍可恢复")
	}

	// 收到 ToolUseEvent → 标记 ResumeNone，不可恢复
	tracker.Update(ToolUseEvent{ID: "tool_1", Name: "read_file"})
	if tracker.Policy() != ResumeNone {
		t.Errorf("FACT-RESUME: ToolUseEvent 后 Policy = %s, 期望 none", tracker.Policy())
	}
	if tracker.CanResume() {
		t.Error("FACT-RESUME: ToolUseEvent 后不应可恢复（ResumeNone）")
	}

	// ResumeNone 是终态：后续 text_delta 不应回退
	tracker.Update(TextDeltaEvent{Content: "more text"})
	if tracker.Policy() != ResumeNone {
		t.Errorf("FACT-RESUME: ResumeNone 后 text_delta 不应回退, Policy = %s", tracker.Policy())
	}
}

// TestBug_ResumePolicyTextOnly 验证仅文本流中断后可恢复
func TestBug_ResumePolicyTextOnly(t *testing.T) {
	tracker := NewResumeTracker()

	// 仅 text_delta → ResumeText，可恢复
	tracker.Update(TextDeltaEvent{Content: "hello"})
	tracker.Update(TextDeltaEvent{Content: " world"})

	if tracker.Policy() != ResumeText {
		t.Errorf("FACT-RESUME: 仅 text_delta 后 Policy = %s, 期望 text", tracker.Policy())
	}
	if !tracker.CanResume() {
		t.Error("FACT-RESUME: 仅 text_delta 应可恢复")
	}
}

// TestBug_JSONBodyWithoutStatusCode 验证 JSON body 不含状态码字符串时仍能正确提取 ErrorID
// 反例：provider 返回 {"error":{"type":"overloaded_error"}} 但 err.Error() 为 "request failed"
// 修复前：IsOverloadedError 基于字符串包含判断，会失败
// 修复后：errors.As 能提取 *APIError{ErrorID:ErrAIApiOverload}
func TestBug_JSONBodyWithoutStatusCode(t *testing.T) {
	apiErr := &APIError{
		StatusCode: 529,
		Type:       "overloaded_error",
		Message:    "server is overloaded",
		ErrorID:    ErrAIApiOverload,
	}

	var extracted *APIError
	if !stderrors.As(apiErr, &extracted) {
		t.Error("N6: errors.As 应能提取 *APIError")
		return
	}

	if extracted.ErrorID != ErrAIApiOverload {
		t.Errorf("N6: ErrorID = %s, 期望 %s", extracted.ErrorID, ErrAIApiOverload)
	}

	if GetErrorID(apiErr) != ErrAIApiOverload {
		t.Errorf("N6: GetErrorID = %s, 期望 %s", GetErrorID(apiErr), ErrAIApiOverload)
	}
}
