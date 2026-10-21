package ai

import (
	"sort"
	"testing"
)

// 本文件是「thinking 三态」族判据的**改后（green）形态**。
//
// ★ 红测记录（改前，同一批判据用旧的二值 API `Enabled: false` 表达「关」；
// 「编译失败不算判别性证据」，所以红测刻意写成**能编译**的形式）：
//
//	--- FAIL: TestThinkingOffIsExpressedOnOpenAICompatWire
//	    thinking 设为「关」时请求体必须显式下发 enable_thinking=false；实际字段缺失（body 键=[max_tokens messages model stream]）
//	--- FAIL: TestThinkingOffIsExpressedOnAnthropicWire
//	    thinking.type = "", want "disabled"
//	--- FAIL: TestThinkingOffIsExpressedOnDashScopeWire
//	    实际字段缺失（body 键=[input model parameters]）
//	--- FAIL: TestThinkingOffIsExpressedOnGeminiWire
//	    实际字段缺失（body 键=[contents]）
//	--- PASS: TestThinkingOnStillInjectsOnOpenAICompatWire   ← 反向半边：已有的「开」本来就能跑
//
// 改型后（*bool 三态）上面 4 条的输入换成 (nil / ThinkingPtr(true) / ThinkingPtr(false))，
// 全部转绿，且「未指定不下发」成为一条真实可判别的分支。

// openAIThinkingTri 从 OpenAI 兼容请求体读出 enable_thinking 的三态表示。
// 返回 (值, 字段是否存在)。
func openAIThinkingTri(body map[string]interface{}) (*bool, bool) {
	raw, ok := body["enable_thinking"]
	if !ok {
		return nil, false
	}
	b, _ := raw.(bool)
	return &b, true
}

// anthropicThinkingType 读出 Anthropic 请求体 thinking.type；缺失返回 ""。
func anthropicThinkingType(body map[string]interface{}) string {
	raw, ok := body["thinking"]
	if !ok {
		return ""
	}
	m, _ := raw.(map[string]interface{})
	s, _ := m["type"].(string)
	return s
}

// dashScopeThinkingTri 从 DashScope 原生请求体 parameters.enable_thinking 读三态。
func dashScopeThinkingTri(body map[string]interface{}) (*bool, bool) {
	params, ok := body["parameters"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	raw, ok := params["enable_thinking"]
	if !ok {
		return nil, false
	}
	b, _ := raw.(bool)
	return &b, true
}

// geminiThinkingConfig 从 Gemini 原生请求体取 generationConfig.thinkingConfig。
func geminiThinkingConfig(body map[string]interface{}) (map[string]interface{}, bool) {
	gc, ok := body["generationConfig"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	tc, ok := gc["thinkingConfig"].(map[string]interface{})
	return tc, ok
}

// bodyKeys 返回 body 的键集合（稳定排序），排障用。
func bodyKeys(body map[string]interface{}) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ============================================================
// 根缺陷判别器：thinking「显式关闭」必须能下发到线上
// ============================================================
//
// 缺陷（本次修复前）：ThinkingConfig.Enabled 是**二值 bool**，而四个协议注入点
// 都只在 `Enabled == true` 时注入字段。于是「false」被静默当成「不传」——
// 而 qwen3.7-flash 服务端默认**开**思考 ⇒ 客户端的「关」形同虚设（`/think off` 是假的）。
//
// 为什么这是**类型层**缺陷而不是漏了一行 if：二值 bool 无法区分
// 「显式关闭」与「未指定」，所以任何"只在 true 时注入"的写法都必然丢失前者。
// 业界对照：同类 CLI 用 `ReasoningEffort::None` 枚举变体
// （对标证据件）；同类 CLI 用判别联合
// `{type:'disabled'}` 显式构造（对标证据件）。
// 两类都把「关」当作**一个可表达的值**，而不是"字段缺席"。
//
// 判据走生产入口：下列 body 构造函数正是 Stream() 内部构造 HTTP 请求体的同一批函数。

func TestThinkingOffIsExpressedOnOpenAICompatWire(t *testing.T) {
	client := NewSimpleClient("qwen3.7-flash", "test-key", officialDashScopeURL, ProviderOpenAI)
	defer client.Close()

	req := Request{
		Model:    "qwen3.7-flash",
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(false), BudgetTokens: 4096},
	}
	body := client.buildOpenAIBody(req, client.snapshot())

	got, present := openAIThinkingTri(body)
	if !present {
		t.Fatalf("thinking 设为「关」时请求体必须显式下发 enable_thinking=false；实际字段缺失（body 键=%v）", bodyKeys(body))
	}
	if got == nil || *got {
		t.Fatalf("enable_thinking 线值 = %v, want false", got)
	}
}

func TestThinkingOffIsExpressedOnAnthropicWire(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "https://api.anthropic.com", ProviderAnthropic)
	defer client.Close()

	req := Request{
		Model:    "claude-sonnet-4",
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(false), BudgetTokens: 4096},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if got := anthropicThinkingType(body); got != "disabled" {
		t.Fatalf("thinking.type = %q, want \"disabled\"（显式关闭；学界/业界线值一致）", got)
	}
}

func TestThinkingOffIsExpressedOnDashScopeWire(t *testing.T) {
	cl := NewSimpleClient("qwen3", "sk-test", "", ProviderDashScope)
	defer cl.Close()
	adapter := NewDashScopeNativeAdapter("qwen3", "sk-test", "", ProviderDashScope)

	req := Request{
		Model:    "qwen3",
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(false), BudgetTokens: 4096},
	}
	body := adapter.buildDashScopeBody(req, cl.snapshot())

	got, present := dashScopeThinkingTri(body)
	if !present {
		t.Fatalf("thinking 设为「关」时 DashScope 原生请求体必须显式下发 parameters.enable_thinking=false；实际缺失（body 键=%v）", bodyKeys(body))
	}
	if got == nil || *got {
		t.Fatalf("parameters.enable_thinking 线值 = %v, want false", got)
	}
}

func TestThinkingOffIsExpressedOnGeminiWire(t *testing.T) {
	cl := NewSimpleClient("gemini-2.0-flash", "sk-test", "", ProviderGemini)
	defer cl.Close()
	adapter := NewGeminiNativeAdapter("gemini-2.0-flash", "sk-test", "", ProviderGemini)

	req := Request{
		Model:    "gemini-2.0-flash",
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(false), BudgetTokens: 4096},
	}
	body := adapter.buildGeminiBody(req, cl.snapshot())

	tc, present := geminiThinkingConfig(body)
	if !present {
		t.Fatalf("thinking 设为「关」时 Gemini 请求体必须显式关闭（thinkingBudget=0）；实际缺失（body 键=%v）", bodyKeys(body))
	}
	budget, _ := tc["thinkingBudget"].(int)
	if budget != 0 {
		t.Fatalf("thinkingConfig.thinkingBudget = %v, want 0（0 = 关闭，Gemini 线值语义）", tc["thinkingBudget"])
	}
}

// ============================================================
// 反向半边：已有的「开」不能回归
// ============================================================

func TestThinkingOnStillInjectsOnOpenAICompatWire(t *testing.T) {
	client := NewSimpleClient("qwen3.7-flash", "test-key", officialDashScopeURL, ProviderOpenAI)
	defer client.Close()

	req := Request{
		Model:    "qwen3.7-flash",
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 4096},
	}
	body := client.buildOpenAIBody(req, client.snapshot())

	got, present := openAIThinkingTri(body)
	if !present || got == nil || !*got {
		t.Fatalf("thinking 设为「开」时 enable_thinking 应为 true；实际 present=%v value=%v", present, got)
	}
}
