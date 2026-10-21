// Package ai — 缓存感知请求构建器unit test
package ai

import (
	"strings"
	"testing"
)

// TestLayeredPrompt_OrderForCache 验证分层排序
func TestLayeredPrompt_OrderForCache(t *testing.T) {
	lp := NewLayeredPrompt()
	lp.SetLayer(LayerSessionState, "dynamic content")
	lp.SetLayer(LayerCoreIdentity, "core identity")
	lp.SetLayer(LayerToolDefs, "tool definitions")
	lp.SetLayer(LayerProjectCtx, "project context")

	rendered := lp.Render()

	// 验证核心身份在最前面
	if !strings.HasPrefix(rendered.Content, "core identity") {
		t.Errorf("Expected content to start with core identity, got: %s", rendered.Content[:50])
	}

	// 验证动态内容在最后面
	if !strings.HasSuffix(rendered.Content, "dynamic content") {
		t.Errorf("Expected content to end with dynamic content, got: %s", rendered.Content[len(rendered.Content)-50:])
	}
}

// TestLayeredPrompt_Render 渲染完整 system prompt
func TestLayeredPrompt_Render(t *testing.T) {
	lp := NewLayeredPrompt()
	lp.SetLayer(LayerCoreIdentity, "identity")
	lp.SetLayer(LayerToolDefs, "tools")
	lp.SetLayer(LayerProjectCtx, "project")
	lp.SetLayer(LayerSessionState, "session")

	rendered := lp.Render()

	expected := "identity\n\ntools\n\nproject\n\nsession"
	if rendered.Content != expected {
		t.Errorf("Expected %q, got %q", expected, rendered.Content)
	}
}

// TestLayeredPrompt_Fingerprint 相同内容产生相同fingerprint
func TestLayeredPrompt_Fingerprint(t *testing.T) {
	lp1 := NewLayeredPrompt()
	lp1.SetLayer(LayerCoreIdentity, "same content")
	lp1.SetLayer(LayerToolDefs, "tools")

	lp2 := NewLayeredPrompt()
	lp2.SetLayer(LayerCoreIdentity, "same content")
	lp2.SetLayer(LayerToolDefs, "tools")

	r1 := lp1.Render()
	r2 := lp2.Render()

	if r1.Fingerprint != r2.Fingerprint {
		t.Errorf("Same content should produce same fingerprint, got %s vs %s", r1.Fingerprint, r2.Fingerprint)
	}
}

// TestLayeredPrompt_Fingerprint_DifferentContent 不同内容产生不同fingerprint
func TestLayeredPrompt_Fingerprint_DifferentContent(t *testing.T) {
	lp1 := NewLayeredPrompt()
	lp1.SetLayer(LayerCoreIdentity, "content A")

	lp2 := NewLayeredPrompt()
	lp2.SetLayer(LayerCoreIdentity, "content B")

	r1 := lp1.Render()
	r2 := lp2.Render()

	if r1.Fingerprint == r2.Fingerprint {
		t.Error("Different content should produce different fingerprints")
	}
}

// TestLayeredPrompt_EmptyLayer 空层处理
func TestLayeredPrompt_EmptyLayer(t *testing.T) {
	lp := NewLayeredPrompt()
	lp.SetLayer(LayerCoreIdentity, "identity")
	// Layer 1 空
	lp.SetLayer(LayerProjectCtx, "project")

	rendered := lp.Render()

	// should not contain空层
	if strings.Contains(rendered.Content, "\n\n\n\n") {
		t.Error("Should not have double empty lines from empty layers")
	}

	// LayerSizes should not contain空层
	if _, ok := rendered.LayerSizes[LayerToolDefs]; ok {
		t.Error("Empty layer should not be in LayerSizes")
	}
}

// TestCacheAwareRequestBuilder_OrderForCache 稳定/动态排序
func TestCacheAwareRequestBuilder_OrderForCache(t *testing.T) {
	builder := NewCacheAwareRequestBuilder()

	components := []PromptComponent{
		{Name: "dynamic_session", Content: "session state", Stable: false, Layer: LayerSessionState},
		{Name: "stable_identity", Content: "core identity", Stable: true, Layer: LayerCoreIdentity},
		{Name: "stable_tools", Content: "tool defs", Stable: true, Layer: LayerToolDefs},
		{Name: "dynamic_context", Content: "project context", Stable: false, Layer: LayerProjectCtx},
	}

	ordered := builder.OrderForCache(components)

	// 验证层级顺序
	if ordered[0].Layer != LayerCoreIdentity {
		t.Errorf("First should be LayerCoreIdentity, got %v", ordered[0].Layer)
	}
	if ordered[len(ordered)-1].Layer != LayerSessionState {
		t.Errorf("Last should be LayerSessionState, got %v", ordered[len(ordered)-1].Layer)
	}
}

// TestCacheAwareRequestBuilder_BuildSystemPrompt 构建完整提示词
func TestCacheAwareRequestBuilder_BuildSystemPrompt(t *testing.T) {
	builder := NewCacheAwareRequestBuilder()

	components := []PromptComponent{
		{Name: "session", Content: "session state", Stable: false, Layer: LayerSessionState},
		{Name: "identity", Content: "core identity", Stable: true, Layer: LayerCoreIdentity},
	}

	result := builder.BuildSystemPrompt(components)

	if !strings.HasPrefix(result, "core identity") {
		t.Errorf("System prompt should start with stable content, got: %s", result[:30])
	}
}

// TestCacheInfo_CacheDeletionRatio 缓存删除率计算
func TestCacheInfo_CacheDeletionRatio(t *testing.T) {
	tests := []struct {
		name     string
		info     *CacheInfo
		expected float64
	}{
		{"nil", nil, 0},
		{"zero_total", &CacheInfo{}, 0},
		{"normal", &CacheInfo{CacheDeletedTokens: 60, TotalCachedTokens: 100}, 0.6},
		{"no_deletion", &CacheInfo{CacheDeletedTokens: 0, TotalCachedTokens: 100}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.CacheDeletionRatio()
			if got != tt.expected {
				t.Errorf("CacheDeletionRatio() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestCacheInfo_CacheHitRate 缓存命中率计算
func TestCacheInfo_CacheHitRate(t *testing.T) {
	tests := []struct {
		name     string
		info     *CacheInfo
		expected float64
	}{
		{"nil", nil, 0},
		{"zero", &CacheInfo{}, 0},
		{"all_hit", &CacheInfo{CacheReadTokens: 1000, CacheCreationTokens: 0}, 1.0},
		{"half_hit", &CacheInfo{CacheReadTokens: 500, CacheCreationTokens: 500}, 0.5},
		{"no_hit", &CacheInfo{CacheReadTokens: 0, CacheCreationTokens: 1000}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.CacheHitRate()
			if got != tt.expected {
				t.Errorf("CacheHitRate() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestCacheInfo_ShouldCompactBasedOnCache 缓存感知压缩决策
func TestCacheInfo_ShouldCompactBasedOnCache(t *testing.T) {
	tests := []struct {
		name         string
		info         *CacheInfo
		usagePercent float64
		expected     bool
	}{
		{"nil_info", nil, 0.8, false},
		{"no_cache_data", &CacheInfo{}, 0.8, false},
		{"high_deletion", &CacheInfo{CacheDeletedTokens: 600, TotalCachedTokens: 1000}, 0.5, true},
		{"low_deletion", &CacheInfo{CacheDeletedTokens: 200, TotalCachedTokens: 1000}, 0.5, false},
		{"low_hit_rate_high_usage", &CacheInfo{CacheReadTokens: 100, CacheCreationTokens: 900}, 0.75, true},
		{"low_hit_rate_low_usage", &CacheInfo{CacheReadTokens: 100, CacheCreationTokens: 900}, 0.5, false},
		{"high_hit_rate_high_usage", &CacheInfo{CacheReadTokens: 800, CacheCreationTokens: 200}, 0.75, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.ShouldCompactBasedOnCache(tt.usagePercent)
			if got != tt.expected {
				t.Errorf("ShouldCompactBasedOnCache() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestCacheInfo_HasCacheInfo 是否有有效缓存信息
func TestCacheInfo_HasCacheInfo(t *testing.T) {
	tests := []struct {
		name     string
		info     *CacheInfo
		expected bool
	}{
		{"nil", nil, false},
		{"empty", &CacheInfo{}, false},
		{"has_creation", &CacheInfo{CacheCreationTokens: 100}, true},
		{"has_read", &CacheInfo{CacheReadTokens: 100}, true},
		{"has_deleted", &CacheInfo{CacheDeletedTokens: 100}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.HasCacheInfo()
			if got != tt.expected {
				t.Errorf("HasCacheInfo() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestPromptLayer_String 层级名称
func TestPromptLayer_String(t *testing.T) {
	tests := []struct {
		layer    PromptLayer
		expected string
	}{
		{LayerCoreIdentity, "core_identity"},
		{LayerToolDefs, "tool_defs"},
		{LayerProjectCtx, "project_ctx"},
		{LayerSessionState, "session_state"},
		{PromptLayer(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.layer.String(); got != tt.expected {
				t.Errorf("String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestBuildLayeredPrompt 从组件列表构建分层提示词
func TestBuildLayeredPrompt(t *testing.T) {
	builder := NewCacheAwareRequestBuilder()

	components := []PromptComponent{
		{Name: "identity", Content: "I am Whale", Stable: true, Layer: LayerCoreIdentity},
		{Name: "tools", Content: "Tool: bash", Stable: true, Layer: LayerToolDefs},
		{Name: "project", Content: "WHALE.md content", Stable: false, Layer: LayerProjectCtx},
		{Name: "session", Content: "Current task", Stable: false, Layer: LayerSessionState},
	}

	lp := builder.BuildLayeredPrompt(components)
	rendered := lp.Render()

	// 验证层级顺序
	idxIdentity := strings.Index(rendered.Content, "I am Whale")
	idxSession := strings.Index(rendered.Content, "Current task")
	if idxIdentity >= idxSession {
		t.Error("Core identity should come before session state")
	}

	// 验证 LayerSizes
	if len(rendered.LayerSizes) != 4 {
		t.Errorf("Expected 4 layer sizes, got %d", len(rendered.LayerSizes))
	}
}
