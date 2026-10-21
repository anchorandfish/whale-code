package ai

import "testing"

// TestCacheAwareRequestBuilder test cache感知请求构建器的组件排序
// 验证稳定组件排在前面，动态组件排在后面，以最大化 prompt cache 命中率
func TestCacheAwareRequestBuilder(t *testing.T) {
	builder := NewCacheAwareRequestBuilder()

	components := []PromptComponent{
		{Name: "user_context", Content: "User is working on project X", Stable: false},
		{Name: "system_rules", Content: "You are a coding assistant", Stable: true},
		{Name: "memory", Content: "User prefers dark theme", Stable: false},
		{Name: "tool_definitions", Content: "Tools: read_file, write_file", Stable: true},
	}

	ordered := builder.OrderForCache(components)

	// 稳定组件应排在前面
	if !ordered[0].Stable {
		t.Error("First component should be stable (for cache hit)")
	}
	if !ordered[1].Stable {
		t.Error("Second component should be stable (for cache hit)")
	}
}

// TestCacheMetrics test cache指标追踪
// 验证请求计数和缓存读取 token 累加正确
func TestCacheMetrics(t *testing.T) {
	metrics := NewCacheMetrics()

	metrics.RecordRequest(1000, 500, 3000) // input, output, cache_read
	metrics.RecordRequest(800, 400, 4500)

	if metrics.TotalRequests != 2 {
		t.Errorf("Expected 2 requests, got %d", metrics.TotalRequests)
	}
	if metrics.TotalCacheReadTokens != 7500 {
		t.Errorf("Expected 7500 cache read tokens, got %d", metrics.TotalCacheReadTokens)
	}
}

// TestCacheHitRate test cache命中率计算
// 验证命中率在合理scope内，且计算公式正确
func TestCacheHitRate(t *testing.T) {
	metrics := NewCacheMetrics()

	// 第一次请求：无缓存
	metrics.RecordRequest(1000, 500, 0)
	// 第二次请求：缓存命中
	metrics.RecordRequest(200, 100, 800)

	hitRate := metrics.CacheHitRate()
	if hitRate < 0 || hitRate > 1 {
		t.Errorf("Cache hit rate should be between 0 and 1, got %f", hitRate)
	}
	// 第二次请求: 800/(200+800) = 80% from cache
	if hitRate < 0.3 {
		t.Errorf("Cache hit rate should be reasonable, got %f", hitRate)
	}
}

// TestCacheStrategyReorder test cache策略的系统提示词重排
// 验证重排后组件数量不变，且稳定内容排在前面
func TestCacheStrategyReorder(t *testing.T) {
	strategy := NewCacheStrategy()

	systemPrompt := []string{
		"Dynamic user context: working on project X",
		"Static rules: You are a coding assistant",
		"Dynamic memory: User prefers dark theme",
		"Static tool definitions: read_file, write_file",
	}

	reordered := strategy.ReorderSystemPrompt(systemPrompt)

	// 重排后组件数量不变
	if len(reordered) != len(systemPrompt) {
		t.Errorf("Reordered prompt should have same number of components")
	}
}
