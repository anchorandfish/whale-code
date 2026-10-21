package ai

import (
	"testing"
)

// ---------------------------------------------------------------------------
// B4-A2：能力判定的**第二维 = 端点**
//
// 缺陷：`modelCapabilityTable` / 前缀推断都是**按模型名**给结论的，而结论只对
// **厂商官方端点**成立。`baseURL` 在能力判定里此前**零使用**（改造前 grep：
// capability.go / native_search.go / capability_declaration.go 全无命中）
// ⇒ 用户可以 `--base-url` 指向聚合网关/自部署，而我们把厂商**私有**字段
// （`enable_thinking` / `enable_search`）照发 ⇒ 轻则 400、重则静默改变行为。
//
// 定案见 内部工作回执 §3。
// 端点白名单口径与理由见 endpoint_capability.go 文件头。
// ---------------------------------------------------------------------------

// officialDashScopeURL 是**用户本机实际在用的那条端点形态**
// （`~/.whale/.env` 的 OPENAI_BASE_URL，2026-10-05 实测：百炼 MaaS 网关子域）。
// 判据用它而不是 `dashscope.aliyuncs.com`，因为后者恰恰会掩盖
// "白名单只写精确主机 ⇒ 默认配置被误降级"这个坑（见下 Test 的注释）。
const officialDashScopeURL = "https://llm-a0apmuqj39li7tey.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"

// untrustedGatewayURL 是典型的第三方聚合网关形态（自部署 / 中转）。
const untrustedGatewayURL = "https://openrouter.ai/api/v1"

// TestA2_IsTrustedEndpoint_Classification 固化端点分类。
func TestA2_IsTrustedEndpoint_Classification(t *testing.T) {
	trusted := []struct{ url, why string }{
		{officialDashScopeURL, "★ 默认配置实际在用的 MaaS 子域（只写 dashscope.aliyuncs.com 会漏掉它）"},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1", "百炼兼容端点"},
		{"https://api.anthropic.com", "Anthropic 官方"},
		{"https://api.openai.com/v1", "OpenAI 官方"},
		{"https://generativelanguage.googleapis.com/v1beta", "Gemini 官方"},
		{"https://api.deepseek.com", "DeepSeek 官方"},
		{"api.openai.com/v1", "无 scheme 的手写形态（配置里很常见）"},
		{"https://api.openai.com:443/v1", "带端口"},
	}
	for _, tc := range trusted {
		if !IsTrustedEndpoint(tc.url) {
			t.Errorf("应判为官方端点：%s（%s）", tc.url, tc.why)
		}
	}

	untrusted := []struct{ url, why string }{
		{"", "空串 = 没配端点，不得乐观当成缺省端点"},
		{untrustedGatewayURL, "第三方聚合网关"},
		{"http://127.0.0.1:8080/v1", "本机自部署（vLLM/Ollama）—— 恰恰是最不该套官方表的"},
		{"https://vllm.internal.corp/v1", "内网自部署"},
		{"https://api.openai.com.evil.example/v1", "★ 后缀伪装：主机其实是 evil.example"},
		{"https://notopenai.com/v1", "含 openai 字样但不是官方域"},
		{"https://myopenai.com/v1", "同上"},
		{"://bad", "非法 URL"},
	}
	for _, tc := range untrusted {
		if IsTrustedEndpoint(tc.url) {
			t.Errorf("不得判为官方端点：%q（%s）", tc.url, tc.why)
		}
	}
}

// TestA2_EndpointPredicateHasBothPoles 是**判别器自检**的常驻形态：
// 若 `IsTrustedEndpoint` 退化成常量（恒 true 或恒 false），下面所有"端点维度"用例
// 都会静默变成"测了另一个东西"⇒ 假绿。本判据先证明这个谓词**两端都有取值**。
//
// 反例注入点（实测见回执的 `-overlay` mutant）：把 `IsTrustedEndpoint` 改成恒 true
// ⇒ 本判据立刻红；同时 TestA2_ThinkingSupportFor_EndpointDimension 的
// "非官方 ⇒ Unknown" 那格也红（双向确认）。
func TestA2_EndpointPredicateHasBothPoles(t *testing.T) {
	if !IsTrustedEndpoint(officialDashScopeURL) {
		t.Fatal("判别器自检失败：官方端点未被判 true ⇒ 谓词可能恒 false，" +
			"「非官方 ⇒ 降级」的判据会变成假绿")
	}
	if IsTrustedEndpoint(untrustedGatewayURL) {
		t.Fatal("判别器自检失败：第三方网关被判 true ⇒ 谓词可能恒 true，" +
			"端点维度等于没接")
	}
}

// TestA2_ThinkingSupportFor_EndpointDimension 是本批的主判据之一：
// **同一个模型名**，端点不同 ⇒ 结论必须不同（官方→表/推断；非官方→Unknown）。
//
// 判别性：把 `ThinkingSupportFor` 里的端点判定短路掉（`if false &&`）⇒
// 非官方那几格立刻红。
func TestA2_ThinkingSupportFor_EndpointDimension(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	// 前置：这些模型在官方端点上的结论（证明"表/推断确实能给出非 Unknown"）。
	official := []struct {
		model string
		want  ThinkingCapability
	}{
		{"qwen3.7-flash", ThinkingCapSupported},            // 前缀 qwen3 推断 true
		{"claude-sonnet-4-20250514", ThinkingCapSupported}, // 精确表 true
		{"gpt-4o", ThinkingCapUnsupported},                 // ★ 精确表 false —— 也要降级
	}
	for _, tc := range official {
		if got := ThinkingSupportFor(tc.model, officialDashScopeURL); got != tc.want {
			t.Fatalf("前置不成立：官方端点上 %s = %v，期望 %v", tc.model, got, tc.want)
		}
		// 非官方端点：表结论**不成立** ⇒ 一律 Unknown（含"已知不支持"那一档 ——
		// 我们不再敢下任何结论，而不是"继续沿用官方端点的结论"）。
		if got := ThinkingSupportFor(tc.model, untrustedGatewayURL); got != ThinkingCapUnknown {
			t.Errorf("非官方端点上 %s = %v，期望 Unknown（端点降级）", tc.model, got)
		}
	}
}

// TestA2_DeclarationBeatsEndpointDowngrade 固化优先级：
// **用户声明 > 端点降级**。理由：用户面对的是他真实接的那扇门，
// 白名单面对的是域名后缀 —— 谁更可信不言而喻（与 B1 的同一原则）。
//
// 判别性：把声明查表挪到端点判定**之后** ⇒ 本判据红。
func TestA2_DeclarationBeatsEndpointDowngrade(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	// 前置：非官方端点上，未声明 ⇒ Unknown。
	if got := ThinkingSupportFor("kimi-k2", untrustedGatewayURL); got != ThinkingCapUnknown {
		t.Fatalf("前置不成立：非官方端点未声明时 = %v，期望 Unknown", got)
	}

	RegisterThinkingDeclaration("kimi-k2", ThinkingCapSupported)
	if got := ThinkingSupportFor("kimi-k2", untrustedGatewayURL); got != ThinkingCapSupported {
		t.Errorf("声明后应为 Supported（声明优先于端点降级），实际 %v", got)
	}
	// 反向也要成立：把官方端点上"支持"的模型声明成不支持。
	RegisterThinkingDeclaration("qwen3.7-flash", ThinkingCapUnsupported)
	if got := ThinkingSupportFor("qwen3.7-flash", officialDashScopeURL); got != ThinkingCapUnsupported {
		t.Errorf("声明 unsupported 后应为 Unsupported，实际 %v", got)
	}
}

// TestA2_NativeSearch_EndpointDimension 固化 L1 的端点维度，包括
// **显式覆盖 > 端点降级** 的优先级。
func TestA2_NativeSearch_EndpointDimension(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "1")
	ResetNativeSearchModelOverride()
	t.Cleanup(ResetNativeSearchModelOverride)

	// 官方端点 + qwen 系（表里 SupportsNativeSearch=true）⇒ 开。
	if !NativeSearchEnabledFor(l1QwenModel, officialDashScopeURL) {
		t.Fatalf("前置不成立：官方端点上 qwen 系应允许原生检索")
	}
	// 非官方端点 ⇒ 关（即便能力位为真）。判别性：短路端点判定 ⇒ 本行红。
	if NativeSearchEnabledFor(l1QwenModel, untrustedGatewayURL) {
		t.Errorf("非官方端点必须保守关闭原生检索（enable_search 是厂商私有字段）")
	}
	// 空 baseURL（没配端点）同样不得乐观放行。
	if NativeSearchEnabledFor(l1QwenModel, "") {
		t.Errorf("空 baseURL 不得判为可搜（不得把「没配」当成「官方端点」）")
	}
	// ★ 显式覆盖仍然生效（用户说"我这扇门支持"）—— 它是与 thinking 声明同级的
	// 用户表达，不能被端点降级吃掉。
	SetNativeSearchModelOverride([]string{"qwen3.7-flash"})
	if !NativeSearchEnabledFor(l1QwenModel, untrustedGatewayURL) {
		t.Errorf("显式覆盖应优先于端点降级")
	}
}

// ---------------------------------------------------------------------------
// 生产路径判据：降级必须**改变线上报文**，而不只是改变一个查询函数
// ---------------------------------------------------------------------------

// TestA2_EndpointDowngrade_ReachesOpenAIBody 走生产唯一 OpenAI body 构造点
// （`buildOpenAIBody`，`SimpleClient.Stream` 内部用的就是它）：
//
//	同一模型 + 同一 thinking=On ⇒ 官方端点**有** enable_thinking，非官方端点**没有**。
//
// 这是"端点维度真的接进生产"的证据；同时也是**面向用户的行为变更**的固化
// （见回执「爆炸半径」一节）。
func TestA2_EndpointDowngrade_ReachesOpenAIBody(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	on := ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 2048}

	bodyOfficial := a2Body(t, l1QwenModel, officialDashScopeURL, on)
	if bodyOfficial["enable_thinking"] != true {
		t.Fatalf("官方端点上 qwen + thinking=On 应下发 enable_thinking=true，实际 %v（body=%v）",
			bodyOfficial["enable_thinking"], bodyOfficial)
	}

	bodyUntrusted := a2Body(t, l1QwenModel, untrustedGatewayURL, on)
	if _, present := bodyUntrusted["enable_thinking"]; present {
		t.Errorf("非官方端点不得下发 enable_thinking（厂商私有字段），实际 %v（body=%v）",
			bodyUntrusted["enable_thinking"], bodyUntrusted)
	}
	// 反向半边：**显式关闭**仍然必须能下发（对任何模型、任何端点都安全），
	// 否则 /think off 会在非官方端点上失效（服务端默认是开）。
	bodyOff := a2Body(t, l1QwenModel, untrustedGatewayURL, ThinkingConfig{Enabled: ThinkingPtr(false)})
	if bodyOff["enable_thinking"] != false {
		t.Errorf("显式关闭不得被端点降级吃掉，实际 %v", bodyOff["enable_thinking"])
	}
}

// a2Body 用生产构造点造请求体（不手工拼 map）。
func a2Body(t *testing.T, model, baseURL string, cfg ThinkingConfig) map[string]interface{} {
	t.Helper()
	c := NewSimpleClient(model, "test-key", baseURL, ProviderOpenAI)
	defer c.Close()
	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &cfg,
	}
	body := c.buildOpenAIBody(req, c.snapshot())
	if body == nil || body["model"] == nil {
		t.Fatalf("前置不成立：body 未构造完整（%v）", body)
	}
	return body
}

// TestA2_DefaultBaseURLForProvider_SingleSource 固化 provider 缺省端点的**唯一**判定。
//
// 抽这个函数的理由（A2）：端点自本批起参与能力判定 ⇒ 显示面与运行面若各写一份
// 缺省逻辑，就会出现"显示按 A 端点判、运行按 B 端点判"这类只能靠猜解释的现象。
func TestA2_DefaultBaseURLForProvider_SingleSource(t *testing.T) {
	if got := defaultBaseURLForProvider(ProviderAnthropic); got != anthropicBaseURL {
		t.Errorf("anthropic 缺省端点 = %q，期望 %q", got, anthropicBaseURL)
	}
	for _, p := range []string{ProviderOpenAI, ProviderDashScope, ProviderGemini, ""} {
		if got := defaultBaseURLForProvider(p); got != openAIDefaultBase {
			t.Errorf("provider=%q 缺省端点 = %q，期望 %q（OpenAI 兼容为兜底）", p, got, openAIDefaultBase)
		}
	}
	// 缺省端点本身必须可信 —— 否则"没配 base_url"的默认用户会被静默降级。
	for _, p := range []string{ProviderAnthropic, ProviderOpenAI, ProviderDashScope, ProviderGemini, ""} {
		if !IsTrustedEndpoint(defaultBaseURLForProvider(p)) {
			t.Errorf("provider=%q 的缺省端点不在白名单里 ⇒ 默认用户会被静默降级", p)
		}
	}
}
