package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClaudeSupports1hTTL 验证 claude 模型启用 Supports1hTTL（方案 B）。
// 长会话不必每 5m 重建缓存，命中 1h TTL 降低成本。
func TestClaudeSupports1hTTL(t *testing.T) {
	probe := getGlobalCapabilityProbe()
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"} {
		cap := probe.GetCapability(model)
		if !cap.Supports1hTTL {
			t.Errorf("model %s 应启用 Supports1hTTL（方案 B），实际=%v", model, cap.Supports1hTTL)
		}
	}
}

// TestDeepSeekPromptCacheKeyInjection 验证 DeepSeek 路径注入 prompt_cache_key（方案 A）。
// buildOpenAIBody 后 body 应包含 prompt_cache_key 字段（16 hex 字符）。
func TestDeepSeekPromptCacheKeyInjection(t *testing.T) {
	c := NewSimpleClient("deepseek-chat", "k", "https://api.deepseek.com", ProviderOpenAI)
	c.SetSessionID("sess-123")
	c.SetUserID("user-456")
	snap := c.snapshot()
	body := c.buildOpenAIBody(Request{Messages: []MessageEntry{{Role: "user", Content: "hi"}}}, snap)
	key, ok := body["prompt_cache_key"].(string)
	if !ok {
		t.Fatalf("DeepSeek 路径应注入 prompt_cache_key（方案 A），实际 body=%v", body)
	}
	if len(key) != 16 {
		t.Errorf("prompt_cache_key 应为 16 hex 字符（64 bit 熵），实际 len=%d key=%s", len(key), key)
	}
}

// TestPromptCacheKeyStabilityWithinSession 验证同会话同模型 prompt_cache_key 稳定不变（方案 A）。
func TestPromptCacheKeyStabilityWithinSession(t *testing.T) {
	c := NewSimpleClient("deepseek-chat", "k", "https://api.deepseek.com", ProviderOpenAI)
	c.SetSessionID("sess-stable")
	c.SetUserID("user-x")
	snap := c.snapshot()
	body1 := c.buildOpenAIBody(Request{Messages: []MessageEntry{{Role: "user", Content: "q1"}}}, snap)
	body2 := c.buildOpenAIBody(Request{Messages: []MessageEntry{{Role: "user", Content: "q2"}}}, snap)
	if body1["prompt_cache_key"] != body2["prompt_cache_key"] {
		t.Errorf("同会话 prompt_cache_key 应稳定，key1=%v key2=%v", body1["prompt_cache_key"], body2["prompt_cache_key"])
	}
}

// TestPromptCacheKeyNoPII 验证 prompt_cache_key 不含原始 sessionID/userID（方案 A R-1）。
func TestPromptCacheKeyNoPII(t *testing.T) {
	sessionID := "sess-secret-789"
	userID := "user-private-abc"
	c := NewSimpleClient("deepseek-chat", "k", "https://api.deepseek.com", ProviderOpenAI)
	c.SetSessionID(sessionID)
	c.SetUserID(userID)
	snap := c.snapshot()
	body := c.buildOpenAIBody(Request{Messages: []MessageEntry{{Role: "user", Content: "x"}}}, snap)
	key, _ := body["prompt_cache_key"].(string)
	if strings.Contains(key, sessionID) || strings.Contains(key, userID) {
		t.Errorf("prompt_cache_key 不应包含 PII，key=%s sessionID=%s userID=%s", key, sessionID, userID)
	}
}

// TestDeepSeekSkipBreakDetection 验证 DeepSeek 路径跳过被动断裂检测（方案 C）。
// 即使 cache_read_tokens 大幅下降也不报告断裂（无数据基础）。
func TestDeepSeekSkipBreakDetection(t *testing.T) {
	detector := NewCacheBreakDetectorWithStyle(0.05, CachingPromptCacheKey)
	detector.RecordPromptState("sys", nil, "deepseek-chat", "cc-hash")
	detector.CheckForCacheBreak(10000, PromptState{})
	// 大幅下降应被跳过
	result := detector.CheckForCacheBreak(100, PromptState{})
	if result != nil {
		t.Errorf("DeepSeek 路径应跳过被动检测（方案 C），实际 result=%v", result)
	}
}

// TestUpdateCachingStyleResetsBaseline 验证模型切换时 UpdateCachingStyle 重置基线（方案 R-2）。
// 切换后基线重置，下次 CheckForCacheBreak 视为首次调用（返回 nil，不误判）。
func TestUpdateCachingStyleResetsBaseline(t *testing.T) {
	detector := NewCacheBreakDetectorWithStyle(0.05, CachingAnthropicEphemeral)
	detector.RecordPromptState("sys-claude", nil, "claude-sonnet-4-5", "cc-claude")
	// 切换到 DeepSeek 风格，基线应被重置
	detector.UpdateCachingStyle(CachingPromptCacheKey)
	// 切换后即使 token 大幅下降也不应报告断裂（基线已重置 + DeepSeek 跳过）
	result := detector.CheckForCacheBreak(100, PromptState{})
	if result != nil {
		t.Errorf("UpdateCachingStyle 后基线应重置，不应报告断裂（方案 R-2），实际 result=%v", result)
	}
}

// TestInjectToolsCacheControl_DynamicToolFallback 验证动态工具末位时 cache_control 回退（方案 H）。
// 末位为 web_search 时，cache_control 应注入到倒数第二个稳定 tool。
func TestInjectToolsCacheControl_DynamicToolFallback(t *testing.T) {
	cc := CacheControl{Type: "ephemeral", TTL: "5m"}
	tools := []map[string]interface{}{
		{"name": "read_file"},
		{"name": "edit_file"},
		{"name": "web_search"}, // 动态工具，末位
	}
	injectToolsCacheControl(tools, cc)
	if _, ok := tools[2]["cache_control"]; ok {
		t.Error("末位动态工具不应注入 cache_control（方案 H）")
	}
	if _, ok := tools[1]["cache_control"]; !ok {
		t.Error("倒数第二个稳定 tool 应注入 cache_control（方案 H）")
	}
}

// TestInjectToolsCacheControl_StableLastTool 验证末位为稳定 tool 时正常注入（方案 H 回归）。
func TestInjectToolsCacheControl_StableLastTool(t *testing.T) {
	cc := CacheControl{Type: "ephemeral", TTL: "5m"}
	tools := []map[string]interface{}{
		{"name": "read_file"},
		{"name": "edit_file"},
	}
	injectToolsCacheControl(tools, cc)
	if _, ok := tools[1]["cache_control"]; !ok {
		t.Error("末位稳定 tool 应注入 cache_control（方案 H 回归）")
	}
}

// TestRegisterToolResultsForCache 验证 tool_result 注册到 CacheReferenceRegistry（方案 F + T-1）。
// buildAnthropicBody 后 registry 应能 Lookup 到 cache prefix 内注册的 tool_use_id。
// T-1 修复：仅 cache prefix（最后一个 user 消息之前）的 tool_result 被注册，
// 故此处构造 [user(tool_result), user(current)] 两轮，tool_result 位于 cache prefix。
func TestRegisterToolResultsForCache(t *testing.T) {
	c := NewSimpleClient("claude-sonnet-4-5", "k", "", ProviderAnthropic)
	c.SetSessionID("sess-f")
	c.SetUserID("user-f")
	snap := c.snapshot()
	toolResultBlock := map[string]interface{}{
		"type":        "tool_result",
		"tool_use_id": "toolu_abc123",
		"content":     "result data",
	}
	req := Request{
		Messages: []MessageEntry{
			{Role: "user", Content: []interface{}{toolResultBlock}},
			{Role: "user", Content: "current question"}, // 当前轮（cache boundary），不计入 cache prefix
		},
	}
	_ = c.buildAnthropicBody(req, snap)
	entry := c.cacheRefRegistry.Lookup("toolu_abc123")
	if entry == nil {
		t.Fatalf("tool_result 应注册到 CacheReferenceRegistry（方案 F），Lookup 返回 nil")
	}
	if entry.MessageIndex != 0 || entry.ContentIndex != 0 {
		t.Errorf("注册位置应为 (0,0)，实际 (%d,%d)", entry.MessageIndex, entry.ContentIndex)
	}
}

// TestBuildAnthropicBody_LayeredSystemPrompt 验证方案 G 分层 system prompt（B-1 修复）+ B-P1 cache_control 修复。
// B-P1 修复后：仅稳定层（Stable=true）通过 PlaceBreakpointHybrid 注入 cache_control，动态层（Stable=false）不注入。
// 反例验证：session-state 层 Stable=false，不应含 cache_control（旧实现错误地给所有 block 注入）。
func TestBuildAnthropicBody_LayeredSystemPrompt(t *testing.T) {
	c := NewSimpleClient("claude-sonnet-4-5", "k", "", ProviderAnthropic)
	snap := c.snapshot()
	req := Request{
		SystemPrompt: []string{"core", "tool-defs", "session-state"},
		SystemPromptLayers: []PromptComponent{
			{Name: "identity", Content: "core", Stable: true, Layer: LayerCoreIdentity},
			{Name: "tools", Content: "tool-defs", Stable: true, Layer: LayerToolDefs},
			{Name: "session", Content: "session-state", Stable: false, Layer: LayerSessionState},
		},
	}
	body := c.buildAnthropicBody(req, snap)
	system, ok := body["system"].([]map[string]interface{})
	if !ok {
		t.Fatalf("分层 system 应为 []map[string]interface{}，实际 %T", body["system"])
	}
	// 3 层 → 3 个独立 block
	if len(system) != 3 {
		t.Fatalf("分层 system 应有 3 个 block（B-1 修复），实际 %d", len(system))
	}
	// B-P1 cache_control 修复：稳定层（block[0]=core, block[1]=tool-defs）应含 cache_control
	if _, ok := system[0]["cache_control"]; !ok {
		t.Errorf("block[0] (Stable=true) 应含 cache_control（B-P1 修复），实际 %v", system[0])
	}
	if _, ok := system[1]["cache_control"]; !ok {
		t.Errorf("block[1] (Stable=true) 应含 cache_control（B-P1 修复），实际 %v", system[1])
	}
	// 反例：动态层（block[2]=session-state, Stable=false）不应含 cache_control
	if _, ok := system[2]["cache_control"]; ok {
		t.Errorf("block[2] (Stable=false) 不应含 cache_control（B-P1 修复：动态层不注入），实际 %v", system[2])
	}
	// 验证层顺序：core → tool-defs → session-state
	if system[0]["text"] != "core" {
		t.Errorf("block[0] 应为 core 层，实际 %v", system[0]["text"])
	}
	if system[1]["text"] != "tool-defs" {
		t.Errorf("block[1] 应为 tool-defs 层，实际 %v", system[1]["text"])
	}
	if system[2]["text"] != "session-state" {
		t.Errorf("block[2] 应为 session-state 层，实际 %v", system[2]["text"])
	}
}

// TestInjectToolsCacheControl_MultipleDynamicTail 验证末位多个动态工具时锚定最后一个稳定工具（方案 H T-2 修复）。
// 反例 [core, dyn, dyn]：旧实现锚定 tools[1]（dyn），修复后锚定 tools[0]（core）。
func TestInjectToolsCacheControl_MultipleDynamicTail(t *testing.T) {
	cc := CacheControl{Type: "ephemeral", TTL: "5m"}
	tools := []map[string]interface{}{
		{"name": "read_file"},
		{"name": "dynamic_fetch"}, // 动态
		{"name": "web_search"},    // 动态
	}
	injectToolsCacheControl(tools, cc)
	// 应锚定 tools[0]（最后一个稳定工具）
	if _, ok := tools[0]["cache_control"]; !ok {
		t.Error("末位多个动态工具时应锚定 tools[0]（方案 H T-2 修复）")
	}
	if _, ok := tools[1]["cache_control"]; ok {
		t.Error("dynamic_fetch 不应注入 cache_control")
	}
	if _, ok := tools[2]["cache_control"]; ok {
		t.Error("web_search 不应注入 cache_control")
	}
}

// TestInjectToolsCacheControl_AllDynamicFallback 验证全为动态工具时降级为末位（方案 H T-2）。
func TestInjectToolsCacheControl_AllDynamicFallback(t *testing.T) {
	cc := CacheControl{Type: "ephemeral", TTL: "5m"}
	tools := []map[string]interface{}{
		{"name": "dynamic_fetch"},
		{"name": "web_search"},
	}
	injectToolsCacheControl(tools, cc)
	// 全为动态工具，降级为末位
	if _, ok := tools[1]["cache_control"]; !ok {
		t.Error("全为动态工具时应降级锚定末位（方案 H T-2）")
	}
}

// TestRegisterToolResultsForCache_BoundaryLimit 验证 cacheBoundary 限制注册范围（方案 F T-1）。
// 仅 cache prefix 内（最后一个 user 消息之前）的 tool_result 被注册。
func TestRegisterToolResultsForCache_BoundaryLimit(t *testing.T) {
	c := NewSimpleClient("claude-sonnet-4-5", "k", "", ProviderAnthropic)
	snap := c.snapshot()
	// 消息序列：[user(tool_result A), assistant, user(tool_result B)]
	// cacheBoundary = 最后一个 user 消息索引 = 2，仅遍历 [0, 2)
	toolResultA := map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_A", "content": "a"}
	toolResultB := map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_B", "content": "b"}
	req := Request{
		Messages: []MessageEntry{
			{Role: "user", Content: []interface{}{toolResultA}},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: []interface{}{toolResultB}},
		},
	}
	_ = c.buildAnthropicBody(req, snap)
	// tu_A 在 cache prefix 内，应注册
	if entry := c.cacheRefRegistry.Lookup("tu_A"); entry == nil {
		t.Error("tu_A 在 cache prefix 内应注册（方案 F T-1）")
	}
	// tu_B 在最后一个 user 消息（cache boundary），不应注册
	if entry := c.cacheRefRegistry.Lookup("tu_B"); entry != nil {
		t.Errorf("tu_B 在 cache boundary 不应注册（方案 F T-1），实际 entry=%v", entry)
	}
}

// TestDoStream_CacheProbeFailWrapErrorID 验证 cache 探测失败时 wrap ErrAIRetryable（方案 M）。
// 通过 httptest 模拟 Anthropic 返回 cache_control 不支持错误。
func TestDoStream_CacheProbeFailWrapErrorID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"cache_control is not supported"}}`))
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4-5", "k", server.URL, ProviderAnthropic)
	_, err := client.Stream(context.Background(), Request{
		SystemPrompt: []string{"hi"},
		Messages:     []MessageEntry{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("cache 探测失败应返回错误")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("应返回 *APIError，实际 %T", err)
	}
	if apiErr.ErrorID != ErrAIRetryable {
		t.Errorf("cache 探测失败应 wrap ErrAIRetryable（方案 M），实际 ErrorID=%v", apiErr.ErrorID)
	}
	// cache 应被禁用
	if client.SupportsCacheControl() {
		t.Error("cache 探测失败后应禁用 cache_control（方案 M）")
	}
}

// TestRetry_CacheProbeFailRetry 验证 retry.go 识别 cache 探测失败并重试（方案 M）。
func TestRetry_CacheProbeFailRetry(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// 首次：cache_control 错误
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"cache_control is not supported"}}`))
			return
		}
		// 重试：成功
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4-5", "k", server.URL, ProviderAnthropic)
	events, err := StreamWithRetry(context.Background(), client, Request{
		SystemPrompt: []string{"hi"},
		Messages:     []MessageEntry{{Role: "user", Content: "test"}},
	}, RetryConfig{MaxRetries: 3}, nil, nil)
	if err != nil {
		t.Fatalf("重试后应成功，实际 err=%v", err)
	}
	for range events {
	}
	if callCount < 2 {
		t.Errorf("cache 探测失败应触发重试（方案 M），实际调用 %d 次", callCount)
	}
}

// TestBuildPromptCacheKey_Length16 验证 key 长度为 16 hex 字符（R-1 §1.4.2）。
func TestBuildPromptCacheKey_Length16(t *testing.T) {
	key := buildPromptCacheKey("user", "session")
	if len(key) != 16 {
		t.Errorf("key 应为 16 字符，实际 %d key=%s", len(key), key)
	}
}

// TestBuildPromptCacheKey_DifferentUsers 验证不同 user/session 生成不同 key（R-1 §1.4.3）。
func TestBuildPromptCacheKey_DifferentUsers(t *testing.T) {
	key1 := buildPromptCacheKey("user1", "sess")
	key2 := buildPromptCacheKey("user2", "sess")
	if key1 == key2 {
		t.Errorf("不同 user 应生成不同 key，key1=%s key2=%s", key1, key2)
	}
	key3 := buildPromptCacheKey("user1", "sess2")
	if key1 == key3 {
		t.Errorf("不同 session 应生成不同 key，key1=%s key3=%s", key1, key3)
	}
}

// TestBuildPromptCacheKey_NoCollisionAcrossSessions 验证跨会话无碰撞（R-1 §1.4.3）。
func TestBuildPromptCacheKey_NoCollisionAcrossSessions(t *testing.T) {
	keys := make(map[string]bool)
	for i := 0; i < 100; i++ {
		k := buildPromptCacheKey("user", "sess-"+string(rune('a'+i)))
		if keys[k] {
			t.Errorf("跨会话 key 碰撞: %s", k)
		}
		keys[k] = true
	}
}
