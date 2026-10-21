package ai

import (
	"sync"
	"testing"
)

// TestPlaceBreakpoint_ExactlyOne 测试放置断点时恰好只有一个 cache_control
// FACT-AI-002 修复后：cache_control 在 content block 级别，非 message 级别
func TestPlaceBreakpoint_ExactlyOne(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 1,
		CacheScope:               "org",
	})

	messages := []map[string]interface{}{
		{"role": "system", "content": "You are helpful."},
		{"role": "user", "content": "Hello"},
		{"role": "assistant", "content": "Hi there!"},
	}

	result := placement.PlaceBreakpoint(messages)

	// 统计 content block 级别 cache_control 出现次数
	count := 0
	for _, msg := range result {
		if blocks, ok := msg["content"].([]map[string]interface{}); ok {
			for _, b := range blocks {
				if _, ok := b["cache_control"]; ok {
					count++
				}
			}
		}
	}
	if count != 1 {
		t.Errorf("Expected exactly 1 cache_control in content blocks, got %d", count)
	}
}

// TestPlaceBreakpoint_OnLastMessage 测试断点放置在最后一条消息上
// FACT-AI-002 修复后：cache_control 在 content block 级别，非 message 级别
func TestPlaceBreakpoint_OnLastMessage(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 1,
		CacheScope:               "org",
	})

	messages := []map[string]interface{}{
		{"role": "system", "content": "You are helpful."},
		{"role": "user", "content": "Hello"},
		{"role": "assistant", "content": "Hi there!"},
	}

	result := placement.PlaceBreakpoint(messages)

	// 最后一条消息的 content block 应有 cache_control
	lastMsg := result[len(result)-1]
	blocks, ok := lastMsg["content"].([]map[string]interface{})
	if !ok {
		t.Fatal("Last message content should be converted to blocks")
	}
	lastBlock := blocks[len(blocks)-1]
	cc, ok := lastBlock["cache_control"].(map[string]interface{})
	if !ok {
		t.Fatal("Last content block should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control type should be 'ephemeral', got '%v'", cc["type"])
	}
	if cc["scope"] != "org" {
		t.Errorf("cache_control scope should be 'org', got '%v'", cc["scope"])
	}

	// 其他消息不应有 cache_control（在 content block 级别检查）
	for i := 0; i < len(result)-1; i++ {
		if blocks, ok := result[i]["content"].([]map[string]interface{}); ok {
			for _, b := range blocks {
				if _, ok := b["cache_control"]; ok {
					t.Errorf("Message %d should not have cache_control in blocks", i)
				}
			}
		}
	}

	// 原始消息不应被修改
	if _, ok := messages[len(messages)-1]["cache_control"]; ok {
		t.Error("Original messages should not be modified")
	}
}

// TestSplitSystemPrompt 测试按边界拆分系统提示词
func TestSplitSystemPrompt(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{Enabled: true})

	// 正常拆分
	prompt := "Static rules here\n---DYNAMIC---\nDynamic context here"
	static, dynamic := placement.SplitSystemPrompt(prompt, "---DYNAMIC---")
	if static != "Static rules here" {
		t.Errorf("Static part mismatch: got '%s', want 'Static rules here'", static)
	}
	if dynamic != "---DYNAMIC---\nDynamic context here" {
		t.Errorf("Dynamic part mismatch: got '%s'", dynamic)
	}

	// 未找到边界，全部为静态
	static, dynamic = placement.SplitSystemPrompt("No boundary here", "---BOUNDARY---")
	if static != "No boundary here" {
		t.Errorf("Static part should be full prompt when boundary not found, got '%s'", static)
	}
	if dynamic != "" {
		t.Errorf("Dynamic part should be empty when boundary not found, got '%s'", dynamic)
	}

	// 空边界
	static, dynamic = placement.SplitSystemPrompt("Some content", "")
	if static != "Some content" {
		t.Errorf("Static part should be full prompt with empty boundary, got '%s'", static)
	}
	if dynamic != "" {
		t.Errorf("Dynamic part should be empty with empty boundary, got '%s'", dynamic)
	}
}

// TestAssignCacheScope test cache作用域分配
func TestAssignCacheScope(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{Enabled: true})

	// 静态内容 + 提供商支持全局 → global
	scope := placement.AssignCacheScope(true, true)
	if scope != "global" {
		t.Errorf("Expected 'global' for static + global support, got '%s'", scope)
	}

	// 静态内容 + 提供商不支持全局 → org
	scope = placement.AssignCacheScope(true, false)
	if scope != "org" {
		t.Errorf("Expected 'org' for static + no global support, got '%s'", scope)
	}

	// 动态内容 + 提供商支持全局 → org（动态内容不适合全局缓存）
	scope = placement.AssignCacheScope(false, true)
	if scope != "org" {
		t.Errorf("Expected 'org' for dynamic + global support, got '%s'", scope)
	}

	// 动态内容 + 提供商不支持全局 → org
	scope = placement.AssignCacheScope(false, false)
	if scope != "org" {
		t.Errorf("Expected 'org' for dynamic + no global support, got '%s'", scope)
	}
}

// ============================================================================
// PlaceBreakpointHybrid 测试（混合策略：显式标记 + 分层 token 阈值推断）
// ============================================================================

// TestNewCacheBreakpointPlacement_Defaults 测试向后兼容构造函数的默认值。
//
// 验证：MaxBreakpointsPerRequest 默认值由 1 变更为 4（对齐 Anthropic API 上限）；
// MinCacheableTokens 默认值为 1024（DefaultMinCacheableTokens）。
func TestNewCacheBreakpointPlacement_Defaults(t *testing.T) {
	p := NewCacheBreakpointPlacement(CacheBreakpointConfig{Enabled: true})
	if p.config.MaxBreakpointsPerRequest != DefaultMaxBreakpointsPerRequest {
		t.Errorf("MaxBreakpointsPerRequest 默认值 = %d, 期望 %d", p.config.MaxBreakpointsPerRequest, DefaultMaxBreakpointsPerRequest)
	}
	if p.config.MinCacheableTokens != DefaultMinCacheableTokens {
		t.Errorf("MinCacheableTokens 默认值 = %d, 期望 %d", p.config.MinCacheableTokens, DefaultMinCacheableTokens)
	}
	if p.config.CacheScope != "org" {
		t.Errorf("CacheScope 默认值 = %s, 期望 org", p.config.CacheScope)
	}

	// 旧调用方显式传 1 时保留
	p2 := NewCacheBreakpointPlacement(CacheBreakpointConfig{Enabled: true, MaxBreakpointsPerRequest: 1})
	if p2.config.MaxBreakpointsPerRequest != 1 {
		t.Errorf("显式传 1 时应保留，got %d", p2.config.MaxBreakpointsPerRequest)
	}
}

// TestNewCacheBreakpointPlacementWithDeps_Defaults 测试依赖注入构造函数的默认值。
//
// 验证：构造函数注入 tokenSource / probe；默认值与 NewCacheBreakpointPlacement 一致。
func TestNewCacheBreakpointPlacementWithDeps_Defaults(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 0)
	p := NewCacheBreakpointPlacementWithDeps(
		CacheBreakpointConfig{Enabled: true},
		ts,
		nil, // 使用全局单例
	)
	if p.tokenSource == nil {
		t.Error("tokenSource 应被注入")
	}
	if p.config.MaxBreakpointsPerRequest != DefaultMaxBreakpointsPerRequest {
		t.Errorf("MaxBreakpointsPerRequest 默认值 = %d, 期望 %d", p.config.MaxBreakpointsPerRequest, DefaultMaxBreakpointsPerRequest)
	}
	if p.config.MinCacheableTokens != DefaultMinCacheableTokens {
		t.Errorf("MinCacheableTokens 默认值 = %d, 期望 %d", p.config.MinCacheableTokens, DefaultMinCacheableTokens)
	}
}

// TestPlaceBreakpointHybrid_ExplicitBoundary 测试显式 CacheBoundary=true 标记优先。
//
// 验证：CacheBoundary=true 的 block 优先放置 cache_control，即使 Stable=false 也放置。
// 混合策略：CacheBoundary=true 始终放置；Stable=true 且 tokenSource nil 时也放置。
func TestPlaceBreakpointHybrid_ExplicitBoundary(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 4,
		CacheScope:               "org",
	})

	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "identity", Stable: true, CacheBoundary: true},
		{Layer: LayerToolDefs, Text: "tools", Stable: true},
		{Layer: LayerProjectCtx, Text: "project", Stable: false, CacheBoundary: true},
		{Layer: LayerSessionState, Text: "session", Stable: false},
	}

	result := placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")

	// 显式标记的 block 应有 cache_control（优先级最高，即使 Stable=false 也放置）
	if result[0].CacheControl == nil {
		t.Error("block[0] (CacheBoundary=true) 应有 CacheControl")
	}
	if result[2].CacheControl == nil {
		t.Error("block[2] (CacheBoundary=true, Stable=false) 应有 CacheControl（显式标记优先于 Stable）")
	}

	// block[1] Stable=true 且 tokenSource nil → 也放置（混合策略）
	if result[1].CacheControl == nil {
		t.Error("block[1] (Stable=true, tokenSource nil) 应有 CacheControl（混合策略 Stable 推断）")
	}

	// block[3] Stable=false 且无 CacheBoundary → 不放置
	if result[3].CacheControl != nil {
		t.Error("block[3] (Stable=false 且无 CacheBoundary) 不应有 CacheControl")
	}

	// 验证 cache_control 字段格式
	cc := result[0].CacheControl
	if cc["type"] != "ephemeral" {
		t.Errorf("type 应为 ephemeral, got %v", cc["type"])
	}
	if cc["ttl"] != "1h" {
		t.Errorf("ttl 应为 1h, got %v", cc["ttl"])
	}
	if cc["scope"] != "org" {
		t.Errorf("scope 应为 org, got %v", cc["scope"])
	}

	// 原始 blocks 不应被修改
	if blocks[0].CacheControl != nil {
		t.Error("原始 blocks 不应被修改")
	}
}

// TestPlaceBreakpointHybrid_TokenSourceNil 测试 tokenSource nil 时降级。
//
// 验证：tokenSource nil 时跳过 token 阈值校验，仅按 Stable 推断放置 breakpoint。
func TestPlaceBreakpointHybrid_TokenSourceNil(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 4,
		CacheScope:               "org",
	})
	// 未注入 tokenSource（nil）

	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "id", Stable: true},
		{Layer: LayerToolDefs, Text: "tools", Stable: true},
		{Layer: LayerProjectCtx, Text: "proj", Stable: false},
		{Layer: LayerSessionState, Text: "sess", Stable: false},
	}

	result := placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")

	// Stable=true 的 block 应有 cache_control（无 token 阈值校验）
	if result[0].CacheControl == nil {
		t.Error("block[0] (Stable=true, tokenSource nil) 应有 CacheControl")
	}
	if result[1].CacheControl == nil {
		t.Error("block[1] (Stable=true, tokenSource nil) 应有 CacheControl")
	}
	// Stable=false 的 block 应无 cache_control
	if result[2].CacheControl != nil {
		t.Error("block[2] (Stable=false) 不应有 CacheControl")
	}
	if result[3].CacheControl != nil {
		t.Error("block[3] (Stable=false) 不应有 CacheControl")
	}
}

// TestPlaceBreakpointHybrid_TokenThreshold 测试 token 阈值校验。
//
// 验证：tokenSource 非 nil 时，前缀 token < MinCacheableTokens 跳过此层。
// 使用 mockTokenSource 精确控制返回的 token 数。
func TestPlaceBreakpointHybrid_TokenThreshold(t *testing.T) {
	ts := &mockTokenSource{
		countByLen: map[int]int{
			1: 500,  // 1 个 block：500 tokens < 1024
			2: 1500, // 2 个 block：1500 tokens ≥ 1024
			3: 2000, // 3 个 block：2000 tokens ≥ 1024
		},
	}
	placement := NewCacheBreakpointPlacementWithDeps(
		CacheBreakpointConfig{
			Enabled:                  true,
			MaxBreakpointsPerRequest: 4,
			CacheScope:               "org",
			MinCacheableTokens:       1024,
		},
		ts,
		nil,
	)

	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "a", Stable: true},
		{Layer: LayerToolDefs, Text: "b", Stable: true},
		{Layer: LayerProjectCtx, Text: "c", Stable: true},
	}

	result := placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")

	// block[0]: 前缀 500 tokens < 1024 → 跳过
	if result[0].CacheControl != nil {
		t.Error("block[0] (前缀 500 tokens < 1024) 应跳过 cache_control")
	}
	// block[1]: 前缀 1500 tokens ≥ 1024 → 放置
	if result[1].CacheControl == nil {
		t.Error("block[1] (前缀 1500 tokens ≥ 1024) 应有 CacheControl")
	}
	// block[2]: 前缀 2000 tokens ≥ 1024 → 放置
	if result[2].CacheControl == nil {
		t.Error("block[2] (前缀 2000 tokens ≥ 1024) 应有 CacheControl")
	}
}

// TestPlaceBreakpointHybrid_MaxBreakpointsLimit 测试 MaxBreakpointsPerRequest 上限保护（优化 2 吸纳）。
//
// 验证：超过 DefaultMaxBreakpointsPerRequest=4 的部分被截断；
// 超过 4 的配置值被截断到 4。
func TestPlaceBreakpointHybrid_MaxBreakpointsLimit(t *testing.T) {
	// 配置 MaxBreakpointsPerRequest=10（超过 Anthropic 上限 4）
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 10, // 超过上限，应截断到 4
		CacheScope:               "org",
	})

	// 5 个 Stable=true block（均显式标记，确保都能放置）
	blocks := make([]PromptLayerBlock, 5)
	for i := range blocks {
		blocks[i] = PromptLayerBlock{
			Layer:         PromptLayer(i),
			Text:          "x",
			Stable:        true,
			CacheBoundary: true, // 显式标记优先，不受 token 阈值影响
		}
	}

	result := placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")

	// 统计 cache_control 数量，应不超过 4
	placed := 0
	for _, blk := range result {
		if blk.CacheControl != nil {
			placed++
		}
	}
	if placed > DefaultMaxBreakpointsPerRequest {
		t.Errorf("放置数量 = %d, 期望 ≤ %d（Anthropic API 上限）", placed, DefaultMaxBreakpointsPerRequest)
	}
	if placed != 4 {
		t.Errorf("放置数量 = %d, 期望 4（截断到上限）", placed)
	}
}

// TestPlaceBreakpointHybrid_CachingStyleFilter 测试 CachingStyle 过滤（优化 5 吸纳）。
//
// 验证：CachingStyle != AnthropicEphemeral 时跳过 cache_control 注入。
// - gpt-4 (OpenAIPrefixMatch)：跳过
// - deepseek-chat (PromptCacheKey)：跳过
// - qwen-max (None)：跳过
func TestPlaceBreakpointHybrid_CachingStyleFilter(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 4,
		CacheScope:               "org",
	})

	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "id", Stable: true, CacheBoundary: true},
	}

	// gpt-4 (OpenAIPrefixMatch) → 跳过
	result := placement.PlaceBreakpointHybrid(blocks, "gpt-4")
	if result[0].CacheControl != nil {
		t.Error("gpt-4 (OpenAIPrefixMatch) 应跳过 cache_control 注入")
	}

	// deepseek-chat (PromptCacheKey) → 跳过
	result = placement.PlaceBreakpointHybrid(blocks, "deepseek-chat")
	if result[0].CacheControl != nil {
		t.Error("deepseek-chat (PromptCacheKey) 应跳过 cache_control 注入")
	}

	// qwen-max (None) → 跳过
	result = placement.PlaceBreakpointHybrid(blocks, "qwen-max")
	if result[0].CacheControl != nil {
		t.Error("qwen-max (None) 应跳过 cache_control 注入")
	}

	// claude-sonnet-4 (AnthropicEphemeral) → 注入
	result = placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")
	if result[0].CacheControl == nil {
		t.Error("claude-sonnet-4 (AnthropicEphemeral) 应注入 cache_control")
	}
}

// TestPlaceBreakpointHybrid_Disabled 测试 Enabled=false 时跳过。
//
// 验证：Enabled=false 时直接返回原 blocks，不放置任何 cache_control。
func TestPlaceBreakpointHybrid_Disabled(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  false,
		MaxBreakpointsPerRequest: 4,
	})

	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "id", Stable: true, CacheBoundary: true},
	}

	result := placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")
	if result[0].CacheControl != nil {
		t.Error("Enabled=false 时不应放置 cache_control")
	}
}

// TestPlaceBreakpointHybrid_Concurrent 测试 PlaceBreakpointHybrid 并发安全（优化 4 吸纳）。
//
// 10 goroutine 并发调用，-race 检测数据竞争。
// CacheBreakpointPlacement 构造后只读（无共享可变状态），多 goroutine 调用安全。
func TestPlaceBreakpointHybrid_Concurrent(t *testing.T) {
	placement := NewCacheBreakpointPlacement(CacheBreakpointConfig{
		Enabled:                  true,
		MaxBreakpointsPerRequest: 4,
		CacheScope:               "org",
	})
	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "identity", Stable: true, CacheBoundary: true},
		{Layer: LayerToolDefs, Text: "tools", Stable: true},
		{Layer: LayerProjectCtx, Text: "project", Stable: false},
		{Layer: LayerSessionState, Text: "session", Stable: false},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := placement.PlaceBreakpointHybrid(blocks, "claude-sonnet-4-20250514")
			if len(result) != len(blocks) {
				t.Errorf("result length mismatch: got %d, want %d", len(result), len(blocks))
			}
		}()
	}
	wg.Wait()
}

// TestComputePrefixTokens 测试前缀 token 计算。
//
// 验证：computePrefixTokens 正确将 PromptLayerBlock 转为 MessageEntry 并调用 TokenSource.ContextCount。
func TestComputePrefixTokens(t *testing.T) {
	ts := &mockTokenSource{
		fixedCount: 1234,
	}
	placement := NewCacheBreakpointPlacementWithDeps(
		CacheBreakpointConfig{Enabled: true},
		ts,
		nil,
	)

	blocks := []PromptLayerBlock{
		{Layer: LayerCoreIdentity, Text: "a", Stable: true},
		{Layer: LayerToolDefs, Text: "b", Stable: true},
	}

	// 调用 computePrefixTokens（通过私有方法测试，借助 PlaceBreakpointHybrid 间接验证）
	// 此处直接调用 computePrefixTokens（同包测试可访问私有方法）
	tokens := placement.computePrefixTokens(blocks, "claude-sonnet-4-20250514")
	if tokens != 1234 {
		t.Errorf("computePrefixTokens = %d, 期望 1234", tokens)
	}

	// 验证 mockTokenSource 被调用
	if !ts.called {
		t.Error("mockTokenSource.ContextCount 应被调用")
	}
}

// mockTokenSource 测试用 mock TokenSource。
//
// 支持两种模式：
// - fixedCount: 固定返回值（用于 TestComputePrefixTokens）
// - countByLen: 按 messages 长度返回不同值（用于 TestPlaceBreakpointHybrid_TokenThreshold）
type mockTokenSource struct {
	fixedCount int
	countByLen map[int]int
	called     bool
}

// PreCheckEstimate mock 实现（测试用，未实际调用）。
func (m *mockTokenSource) PreCheckEstimate(messages []MessageEntry, model string) int {
	return m.fixedCount
}

// PostCallActual mock 实现（测试用，未实际调用）。
func (m *mockTokenSource) PostCallActual(model string) (UnifiedUsage, bool) {
	return UnifiedUsage{}, false
}

// ContextCount mock 实现（测试用，按 messages 长度返回）。
func (m *mockTokenSource) ContextCount(messages []MessageEntry, model string) int {
	m.called = true
	if m.countByLen != nil {
		if count, ok := m.countByLen[len(messages)]; ok {
			return count
		}
	}
	return m.fixedCount
}

// RecordRemoteUsage mock 实现（测试用，未实际调用）。
func (m *mockTokenSource) RecordRemoteUsage(model string, actual UnifiedUsage, estimated int) {
	// no-op
}

// ApplyCalibration mock 实现（未采样场景，返回原值）。
func (m *mockTokenSource) ApplyCalibration(model string, estimated int) int {
	return estimated
}

// EstimateRequestInput mock 实现（B-P4，按 messages 数 ×100 粗估）。
func (m *mockTokenSource) EstimateRequestInput(req Request) int {
	return len(req.Messages) * 100
}

// CalibrationInfo mock 实现（B-P4 可观测性，未采样场景）。
func (m *mockTokenSource) CalibrationInfo(model string) (CalibrationOffset, bool) {
	return CalibrationOffset{}, false
}
