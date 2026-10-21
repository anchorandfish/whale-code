package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ============================================================
// 步骤 4：Anthropic 两个静态护栏
//
// 3.1 结构化输出强制 `tool_choice:{type:"tool"}` 与 thinking enabled **互斥**
// （官方：manual extended thinking 只支持 tool_choice auto|none）⇒ 同开必 400。
// 3.2 `budget_tokens` 必须 **≥1024 且 < max_tokens**，否则 400。
//
// ★ 本文件判的是「**即将发出的 body** 上这两个不变量是否成立」，不判实现形状。
// 观测面 = `buildAnthropicBody`（生产请求体构造点，两条路径共用）
// ＋ `SimpleClient.Stream`（生产入口，抓真实 HTTP body）。
//
// ★ 前置事实（R1 生产接线）：本仓**当前没有任何生产调用点设置 `ResponseSchema`**
// （全仓非测试命中 0；`StreamStructured` 亦仅测试引用）⇒ 3.1 今日为**防御性**护栏，
// 它守的是「结构化输出一旦接线就不得与 thinking 同开」。3.2 则**现在就可达**
// （任一路径压低 `MaxTokens` + Anthropic provider + `/think on`）。
// ============================================================

// anthropicThinkingBudget 读出 Anthropic 请求体 `thinking.budget_tokens`。
func anthropicThinkingBudget(body map[string]interface{}) (int, bool) {
	raw, ok := body["thinking"]
	if !ok {
		return 0, false
	}
	m, _ := raw.(map[string]interface{})
	if m == nil {
		return 0, false
	}
	switch v := m["budget_tokens"].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

// anthropicForcedToolChoice 读出是否使用「强制 tool_choice」（type = tool | any）。
// Anthropic 的 thinking 只允许 tool_choice 为 auto|none ⇒ 这两种即"强制"。
func anthropicForcedToolChoice(body map[string]interface{}) bool {
	tc, ok := body["tool_choice"].(map[string]interface{})
	if !ok {
		return false
	}
	t, _ := tc["type"].(string)
	return t == "tool" || t == "any"
}

// anthropicMaxTokens 读 max_tokens。
//
// ★ 必须同时认 int 与 float64：走**线上抓包**时 body 经 `json.Decode` 往返，
// 数字一律是 float64；只认 int 会静默返回 0（实测踩到：断言变成 `budget >= 0` 恒真）。
func anthropicMaxTokens(body map[string]interface{}) int {
	switch v := body["max_tokens"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// captureAnthropicWire 走**生产入口** `SimpleClient.Stream`（provider = anthropic）
// 打到本地假端点，返回真实发出的 HTTP 请求体。
//
// 与 `tui.captureThinkingWire` 同款手法：全程生产构件，只把**终点**换成假端点。
func captureAnthropicWire(t *testing.T, req Request) map[string]interface{} {
	t.Helper()

	var mu sync.Mutex
	var captured map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		captured = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	client := NewSimpleClient("claude-sonnet-4", "test-key", srv.URL, ProviderAnthropic)
	defer client.Close()

	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}
	for range events {
		// 排空事件通道，确保请求已到达假端点。
	}

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("假端点未收到请求体：生产链路没有把请求发出去")
	}
	return captured
}

// ------------------------------------------------------------
// 3.1 结构化输出 × thinking 互斥
// ------------------------------------------------------------

// TestAnthropicGuard_StructuredOutputDisablesThinking 判 3.1：
// 强制 tool_choice 与 thinking enabled 不得同时出现在 body 里。
func TestAnthropicGuard_StructuredOutputDisablesThinking(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
		ResponseSchema: &ResponseSchema{
			Name:   "test_schema",
			Schema: map[string]interface{}{"type": "object"},
		},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	// 正向留痕：结构化输出必须仍被强制（护栏不得"顺手"把它拆掉）
	if !anthropicForcedToolChoice(body) {
		t.Fatalf("结构化输出必须保留强制 tool_choice；实际 tool_choice=%v（body 键=%v）",
			body["tool_choice"], bodyKeys(body))
	}
	// 主断言：不得同开
	if got := anthropicThinkingType(body); got == "enabled" {
		t.Fatalf("结构化输出（tool_choice.type=tool）与 thinking enabled 互斥，同开必被 Anthropic 400；"+
			"实际 thinking.type=%q thinking=%v（body 键=%v）", got, body["thinking"], bodyKeys(body))
	}
}

// ------------------------------------------------------------
// 3.2 budget_tokens 上下界
// ------------------------------------------------------------

// TestAnthropicGuard_BudgetBelowMaxTokens 判 3.2 上界：budget_tokens < max_tokens。
func TestAnthropicGuard_BudgetBelowMaxTokens(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
		MaxTokens: 8192,
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if anthropicThinkingType(body) != "enabled" {
		return // 降级为「不启用」也是合法消解；本判据只要求"若启用则必须合法"
	}
	budget, ok := anthropicThinkingBudget(body)
	if !ok {
		t.Fatalf("thinking.type=enabled 但缺 budget_tokens（thinking=%v）", body["thinking"])
	}
	if maxTok := anthropicMaxTokens(body); budget >= maxTok {
		t.Fatalf("Anthropic 要求 budget_tokens < max_tokens，否则 400；实际 budget=%d max_tokens=%d",
			budget, maxTok)
	}
}

// TestAnthropicGuard_BudgetRespectsMinimum 判 3.2 下界：budget_tokens >= 1024。
func TestAnthropicGuard_BudgetRespectsMinimum(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
		MaxTokens: 16384,
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 500},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if anthropicThinkingType(body) != "enabled" {
		return
	}
	budget, ok := anthropicThinkingBudget(body)
	if !ok {
		t.Fatalf("thinking.type=enabled 但缺 budget_tokens（thinking=%v）", body["thinking"])
	}
	if budget < 1024 {
		t.Fatalf("Anthropic 要求 budget_tokens >= 1024，否则 400；实际 budget=%d", budget)
	}
}

// TestAnthropicGuard_UnsolvableBudgetDegrades 判 3.2 的边界：
// max_tokens 小到无法同时满足「≥1024 且 <max_tokens」⇒ 必须降级为不启用，
// 而不是发出一个必 400 的 body。
func TestAnthropicGuard_UnsolvableBudgetDegrades(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
		MaxTokens: 1000,
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if got := anthropicThinkingType(body); got == "enabled" {
		budget, _ := anthropicThinkingBudget(body)
		t.Fatalf("max_tokens=%d 时无解（budget≥1024 且 <max_tokens 不能同时成立）⇒ 必须降级；"+
			"实际 thinking.type=%q budget=%d", anthropicMaxTokens(body), got, budget)
	}
}

// ------------------------------------------------------------
// 反向半边：既有「安全 / 关闭 / 未指定」形态不得被护栏改坏
// ------------------------------------------------------------

// TestAnthropicGuard_SafeBudgetUnchanged 判护栏**不越界**：
// 本来就合法的预算不得被改写（默认 max_tokens=16384、budget=10000）。
func TestAnthropicGuard_SafeBudgetUnchanged(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
		MaxTokens: 16384,
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if got := anthropicThinkingType(body); got != "enabled" {
		t.Fatalf("安全预算下 thinking 应保持 enabled，实际 %q", got)
	}
	if b, _ := anthropicThinkingBudget(body); b != 10000 {
		t.Fatalf("安全预算不应被改写：budget=%d want 10000", b)
	}
}

// TestAnthropicGuard_FallbackBudgetPreserved 判护栏**不越界**：
// 未给预算（BudgetTokens = 0）时仍按兜底启用（改前行为逐字保留）。
func TestAnthropicGuard_FallbackBudgetPreserved(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
		MaxTokens: 16384,
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true)}, // BudgetTokens 缺省 = 0
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if got := anthropicThinkingType(body); got != "enabled" {
		t.Fatalf("未给预算时应按兜底启用（thinking.type=enabled），实际 %q", got)
	}
	if b, _ := anthropicThinkingBudget(body); b != anthropicThinkingBudgetFallback {
		t.Fatalf("兜底预算 = %d，want %d", b, anthropicThinkingBudgetFallback)
	}
}

// TestAnthropicGuard_OffStillDisabled 判护栏不得把「显式关闭」也门掉。
func TestAnthropicGuard_OffStillDisabled(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(false), BudgetTokens: 500},
		ResponseSchema: &ResponseSchema{
			Name:   "s",
			Schema: map[string]interface{}{"type": "object"},
		},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if got := anthropicThinkingType(body); got != "disabled" {
		t.Fatalf("显式关闭必须仍下发 disabled（护栏不得把 Off 也门掉），实际 %q", got)
	}
}

// TestAnthropicGuard_UnspecifiedStaysAbsent 判护栏不引入新字段：
// 未指定档位时 thinking 仍不下发（由服务端默认决定）。
func TestAnthropicGuard_UnspecifiedStaysAbsent(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		ResponseSchema: &ResponseSchema{
			Name:   "s",
			Schema: map[string]interface{}{"type": "object"},
		},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if _, present := body["thinking"]; present {
		t.Fatalf("未指定档位时不得下发 thinking 字段（body 键=%v）", bodyKeys(body))
	}
}

// ------------------------------------------------------------
// 生产入口（Stream）级第二证：护栏发生在真正发出去的那份 body 上
// ------------------------------------------------------------

// TestAnthropicGuard_OnWire_Budget 走生产入口 `Stream`（3.2 族），抓真实 HTTP 请求体。
//
// 与 `…_Structured` **拆成两条**是刻意的：R4-10 要求两族的红集互不相交，
// 若两条子测试共用一个测试函数，任一族变红都会让整个函数红 ⇒ 归因不清。
func TestAnthropicGuard_OnWire_Budget(t *testing.T) {
	body := captureAnthropicWire(t, Request{
		Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
		MaxTokens: 8192,
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
	})
	if anthropicThinkingType(body) != "enabled" {
		return
	}
	b, _ := anthropicThinkingBudget(body)
	if m := anthropicMaxTokens(body); b >= m {
		t.Fatalf("线上 body budget_tokens=%d >= max_tokens=%d ⇒ 必 400（body=%v）", b, m, body)
	}
	if b < 1024 {
		t.Fatalf("线上 body budget_tokens=%d < 1024 ⇒ 必 400（body=%v）", b, body)
	}
}

// TestAnthropicGuard_OnWire_Structured 走生产入口 `Stream`（3.1 族），抓真实 HTTP 请求体。
func TestAnthropicGuard_OnWire_Structured(t *testing.T) {
	body := captureAnthropicWire(t, Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
		ResponseSchema: &ResponseSchema{
			Name:   "s",
			Schema: map[string]interface{}{"type": "object"},
		},
	})
	if anthropicForcedToolChoice(body) && anthropicThinkingType(body) == "enabled" {
		t.Fatalf("线上 body 同时带强制 tool_choice 与 thinking enabled ⇒ 必 400（body=%v）", body)
	}
}
