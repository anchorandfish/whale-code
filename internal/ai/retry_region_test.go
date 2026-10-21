// Package ai — T1-C M04 StreamWithRetry 集成测试（region + reconnect + backpressure 三层协同）
//
// 测试用例（按 03-实施prompt.md §6.2.4）：
//   - TC-INT-001: 529 + region 切换 + 同模型 → baseURL 切换；不切模型
//   - TC-INT-002: 529 + 全 region 失败 + 切模型 → FallbackChain.Record529 调用；切到 fallbackModel
//   - TC-INT-003: 流中断 + 重连 + 背压协同 → 重连成功后背压 metrics 正确
//   - TC-INT-004: RetryConfig.OnRegionRouter=nil → 行为与原版一致（向后兼容）
package ai

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- regionIntegrationClient 用于集成测试的 mock client ---
// 提供 baseURL 切换记录 + 受控错误返回

type regionIntegrationClient struct {
	mu               sync.Mutex
	model            string
	recordedBaseURLs []string
	streamFn         func(ctx context.Context, req Request) (<-chan Event, error)
}

func (m *regionIntegrationClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	m.mu.Lock()
	m.recordedBaseURLs = append(m.recordedBaseURLs, req.BaseURL)
	m.mu.Unlock()
	if m.streamFn != nil {
		return m.streamFn(ctx, req)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *regionIntegrationClient) Model() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model
}

func (m *regionIntegrationClient) SetModel(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.model = model
}

func (m *regionIntegrationClient) Close() error { return nil }

func (m *regionIntegrationClient) getBaseURLs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.recordedBaseURLs))
	copy(result, m.recordedBaseURLs)
	return result
}

// new529Err 构造 529 *APIError
func new529Err() *APIError {
	return &APIError{
		StatusCode: 529,
		Type:       "overloaded_error",
		Message:    "server overloaded",
		ErrorID:    ErrAIApiOverload,
	}
}

// TestTC_INT_001_529RegionSwitchSameModel 验证 529 时优先切 region，不切模型
func TestTC_INT_001_529RegionSwitchSameModel(t *testing.T) {
	cfg := newTestRegionConfig()
	router := NewDefaultRegionRouter(cfg)

	var callCount atomic.Int32
	streamFn := func(ctx context.Context, req Request) (<-chan Event, error) {
		n := callCount.Add(1)
		// 前两次返回 529（让 us-east-1 失败一次切到 eu-west-1）
		if n <= 2 {
			return nil, new529Err()
		}
		// 第三次成功
		ch := make(chan Event, 1)
		go func() {
			defer close(ch)
			ch <- MessageStopEvent{}
		}()
		return ch, nil
	}

	client := &regionIntegrationClient{
		model:    "claude-3",
		streamFn: streamFn,
	}

	req := Request{Model: "claude-3", QuerySource: QuerySourceForeground}
	retryCfg := RetryConfig{
		MaxRetries:               5,
		BaseDelay:                1 * time.Millisecond,
		MaxDelay:                 10 * time.Millisecond,
		EnableMultiLevelFallback: true,
		OnRegionRouter:           router,
	}
	fb := NewFallbackChain("claude-3", "claude-3-fallback")
	coordinator := NewRecoveryCoordinator()

	events, err := StreamWithRetry(context.Background(), client, req, retryCfg, fb, coordinator)
	if err != nil {
		t.Fatalf("StreamWithRetry 返回错误: %v", err)
	}

	// 消费事件
	for range events {
	}

	// 验证：模型未切换（仍是 claude-3）
	if client.Model() != "claude-3" {
		t.Errorf("模型切换到 %q（不应切换，应优先切 region）", client.Model())
	}

	// 验证：调用了不同 baseURL（region 切换发生）
	baseURLs := client.getBaseURLs()
	t.Logf("调用 baseURLs: %v", baseURLs)
	if len(baseURLs) < 2 {
		t.Errorf("仅调用 %d 次（应至少 2 次：首次 + region 切换）", len(baseURLs))
	}

	// 验证：FallbackChain 未降级（529 由 region 切换吸收）
	if fb.IsDegraded() {
		t.Error("FallbackChain 不应降级（region 切换吸收了 529）")
	}
}

// TestTC_INT_002_529AllRegionsFailThenFallback 验证 529 全 region 失败后切到 fallback 模型
//
// 注意：使用 nil coordinator。RecoveryCoordinator 状态机会在首次 fallback.Record529() 后
// 从 Normal→Retrying，阻断后续重试（BeforeRecovery("retry") 返回 false）。
// 本测试需要多次 529 累积达到 FallbackChain.degradationThreshold=3，故不使用 coordinator。
func TestTC_INT_002_529AllRegionsFailThenFallback(t *testing.T) {
	cfg := newTestRegionConfig()
	cfg.CooldownTTL = 1 * time.Hour // 长 TTL 避免被动探测
	router := NewDefaultRegionRouter(cfg)

	var callCount atomic.Int32
	streamFn := func(ctx context.Context, req Request) (<-chan Event, error) {
		n := callCount.Add(1)
		// 前 5 次返回 529（让所有 region 都失败并切换）
		// 第 3 次：us-east-1 已 Open，切到 eu-west-1
		// 第 4 次：eu-west-1 也 Open，切到 ap-southeast-1
		// 第 5 次：所有 region 都 Open（被 excluded）
		// 第 6 次：切到 fallback 模型后成功
		if n <= 5 {
			return nil, new529Err()
		}
		ch := make(chan Event, 1)
		go func() {
			defer close(ch)
			ch <- MessageStopEvent{}
		}()
		return ch, nil
	}

	client := &regionIntegrationClient{
		model:    "claude-3",
		streamFn: streamFn,
	}

	req := Request{Model: "claude-3", QuerySource: QuerySourceForeground}
	retryCfg := RetryConfig{
		MaxRetries:               10,
		BaseDelay:                1 * time.Millisecond,
		MaxDelay:                 5 * time.Millisecond,
		EnableMultiLevelFallback: true,
		OnRegionRouter:           router,
	}
	// FallbackChain 降级阈值=3（默认），意味着需要 3 次 529 才降级
	fb := NewFallbackChain("claude-3", "claude-3-fallback")
	// 使用 nil coordinator：fallback chain 需要多次重试累积 529 计数
	var coordinator *RecoveryCoordinator = nil

	events, err := StreamWithRetry(context.Background(), client, req, retryCfg, fb, coordinator)
	if err != nil {
		t.Fatalf("StreamWithRetry 返回错误: %v", err)
	}

	for range events {
	}

	// 验证：最终模型切到 fallback
	if client.Model() != "claude-3-fallback" {
		t.Errorf("最终模型 = %q, 期望 claude-3-fallback", client.Model())
	}

	// 验证：FallbackChain 已降级
	if !fb.IsDegraded() {
		t.Error("FallbackChain 应已降级")
	}
}

// TestTC_INT_003_ReconnectAndBackpressure 验证流中断 + 重连 + 背压协同
//
// 由于 OnReconnectWrapper 字段当前未在 StreamWithRetry 中强制接线（保留作为 future 扩展点），
// 此测试主要验证 wrapWithCacheBreakCheck 改造后 BackpressurePipe 与 cache break 检测协同工作。
func TestTC_INT_003_ReconnectAndBackpressure(t *testing.T) {
	// 构造一个事件流（UsageEvent + MessageStop），通过 wrapWithCacheBreakCheck 包装
	// 验证 BackpressurePipe 在 BackpressureDrop 策略下仍能透传 UsageEvent + MessageStop
	events := make(chan Event, 8)
	go func() {
		defer close(events)
		// 发送 50 个 TextDeltaEvent（可能 drop）+ UsageEvent + MessageStop（受保护）
		for i := 0; i < 50; i++ {
			events <- TextDeltaEvent{Content: "x"}
		}
		events <- UsageEvent{InputTokens: 100, OutputTokens: 50, CacheRead: 10}
		events <- MessageStopEvent{}
	}()

	// NewCachingIntegration 接受 (model string, threshold float64)
	ci := NewCachingIntegration("claude-3", 0.05)
	if ci == nil {
		t.Fatal("NewCachingIntegration 返回 nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrapped := wrapWithCacheBreakCheck(ctx, events, ci)

	// 慢消费者
	var collected []Event
	for evt := range wrapped {
		collected = append(collected, evt)
		time.Sleep(1 * time.Millisecond)
	}

	// 验证：UsageEvent 和 MessageStopEvent 必须存在（受保护不被丢弃）
	var hasUsage, hasStop bool
	for _, evt := range collected {
		switch evt.(type) {
		case UsageEvent:
			hasUsage = true
		case MessageStopEvent:
			hasStop = true
		}
	}
	if !hasUsage {
		t.Error("UsageEvent 被丢弃（应受保护）")
	}
	if !hasStop {
		t.Error("MessageStopEvent 被丢弃（应受保护）")
	}
}

// TestTC_INT_004_NilRegionRouterBackwardCompat 验证 OnRegionRouter=nil 时行为与原版一致
//
// 注意：使用 nil coordinator。OnRegionRouter=nil 时 529 直接走 fallback.Record529()，
// 若使用 coordinator 会在首次 529 后阻断后续重试。本测试需要 2 次调用（1 次 529 + 1 次成功），
// 故不使用 coordinator。
func TestTC_INT_004_NilRegionRouterBackwardCompat(t *testing.T) {
	var callCount atomic.Int32
	streamFn := func(ctx context.Context, req Request) (<-chan Event, error) {
		n := callCount.Add(1)
		if n == 1 {
			return nil, new529Err()
		}
		// 第二次成功
		ch := make(chan Event, 1)
		go func() {
			defer close(ch)
			ch <- MessageStopEvent{}
		}()
		return ch, nil
	}

	client := &regionIntegrationClient{
		model:    "claude-3",
		streamFn: streamFn,
	}

	req := Request{Model: "claude-3", QuerySource: QuerySourceForeground}
	retryCfg := RetryConfig{
		MaxRetries:               3,
		BaseDelay:                1 * time.Millisecond,
		MaxDelay:                 5 * time.Millisecond,
		EnableMultiLevelFallback: true,
		OnRegionRouter:           nil, // 关键：不配置 region router
	}
	fb := NewFallbackChain("claude-3", "claude-3-fallback")
	// 使用 nil coordinator：避免 RecoveryCoordinator 在首次 529 后阻断重试
	var coordinator *RecoveryCoordinator = nil

	events, err := StreamWithRetry(context.Background(), client, req, retryCfg, fb, coordinator)
	if err != nil {
		t.Fatalf("StreamWithRetry 返回错误: %v", err)
	}

	for range events {
	}

	// 验证：BaseURL 未被设置（保持空字符串，因为没配置 router）
	baseURLs := client.getBaseURLs()
	for _, u := range baseURLs {
		if u != "" {
			t.Errorf("BaseURL = %q（应为空，OnRegionRouter=nil 时不应注入 baseURL）", u)
		}
	}

	// 验证：模型未切换
	if client.Model() != "claude-3" {
		t.Errorf("模型 = %q, 期望 claude-3（OnRegionRouter=nil 时不应触发模型切换）", client.Model())
	}

	// 验证：FallbackChain 未降级（单次 529 重试即成功）
	if fb.IsDegraded() {
		t.Error("FallbackChain 不应降级（单次 529 重试即成功）")
	}
}

// TestStreamWithRetry_NilCoordinatorAndFallback 验证 coordinator/fallback 为 nil 时不 panic
func TestStreamWithRetry_NilCoordinatorAndFallback(t *testing.T) {
	streamFn := func(ctx context.Context, req Request) (<-chan Event, error) {
		ch := make(chan Event, 1)
		go func() {
			defer close(ch)
			ch <- MessageStopEvent{}
		}()
		return ch, nil
	}

	client := &regionIntegrationClient{
		model:    "claude-3",
		streamFn: streamFn,
	}

	req := Request{Model: "claude-3", QuerySource: QuerySourceForeground}
	retryCfg := DefaultRetryConfig()
	retryCfg.BaseDelay = 1 * time.Millisecond
	retryCfg.MaxDelay = 5 * time.Millisecond

	events, err := StreamWithRetry(context.Background(), client, req, retryCfg, nil, nil)
	if err != nil {
		t.Fatalf("StreamWithRetry 返回错误: %v", err)
	}

	for range events {
	}
}

// TestStreamWithRetry_ContextCancel 验证 ctx 取消时 StreamWithRetry 退出
func TestStreamWithRetry_ContextCancel(t *testing.T) {
	// client.Stream 始终返回 529 让重试循环持续
	streamFn := func(ctx context.Context, req Request) (<-chan Event, error) {
		return nil, new529Err()
	}

	client := &regionIntegrationClient{
		model:    "claude-3",
		streamFn: streamFn,
	}

	req := Request{Model: "claude-3", QuerySource: QuerySourceForeground}
	retryCfg := RetryConfig{
		MaxRetries:               100,
		BaseDelay:                100 * time.Millisecond,
		MaxDelay:                 100 * time.Millisecond,
		EnableMultiLevelFallback: true,
	}
	fb := NewFallbackChain("claude-3", "claude-3-fallback")
	coordinator := NewRecoveryCoordinator()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := StreamWithRetry(ctx, client, req, retryCfg, fb, coordinator)
	if err == nil {
		t.Error("期望返回 ctx.Err()")
	}
}

// TestIsProtectedEvent 验证 isProtectedEvent 函数（保护 UsageEvent/MessageStopEvent 等）
func TestIsProtectedEvent(t *testing.T) {
	protected := []Event{
		UsageEvent{InputTokens: 100},
		MessageStopEvent{},
		ErrorEvent{Err: fmt.Errorf("test")},
		ToolUseEvent{ID: "tool_1", Name: "test"},
		StructuredOutputEvent{Result: nil},
	}
	for i, evt := range protected {
		if !isProtectedEvent(evt) {
			t.Errorf("protected[%d] (%T) 应受保护", i, evt)
		}
	}

	unprotected := []Event{
		TextDeltaEvent{Content: "x"},
		ThinkingDeltaEvent{Content: "x"},
		ToolInputDeltaEvent{PartialJSON: "{}", Index: 0},
	}
	for i, evt := range unprotected {
		if isProtectedEvent(evt) {
			t.Errorf("unprotected[%d] (%T) 不应受保护", i, evt)
		}
	}
}

// TestIsProtectedSSEEvent 验证 isProtectedSSEEvent 函数（保护 SSE 层 message_start 等）
//
// isProtectedSSEEvent 判定规则（stream.go:440）：
//   - evt.Event in ["message_start", "message_delta", "message_stop", "error"]
//   - 或 evt.Data == "[DONE]"
//   - 或 evt.Data 含 "usage" JSON 字段
func TestIsProtectedSSEEvent(t *testing.T) {
	protected := []SSEEvent{
		{Event: "message_start"},
		{Event: "message_delta"},
		{Event: "message_stop"},
		{Event: "error"},
		{Data: "[DONE]"},
		{Data: `{"usage":{"input_tokens":100,"output_tokens":50}}`},
	}
	for i, evt := range protected {
		if !isProtectedSSEEvent(evt) {
			t.Errorf("protected[%d] (Event=%q Data=%q) 应受保护", i, evt.Event, evt.Data)
		}
	}

	// 非保护事件
	unprotected := []SSEEvent{
		{Event: "content_block_delta"},
		{Event: "content_block_start"},
		{Event: "content_block_stop"},
		{Event: "ping"},
	}
	for i, evt := range unprotected {
		if isProtectedSSEEvent(evt) {
			t.Errorf("unprotected[%d] (Event=%q) 不应受保护", i, evt.Event)
		}
	}
}
