// Package ai — S6 / L1「原生检索」判据（批 4）。
//
// 判据只覆盖**我们真的决定了什么**，不去复述实现：
//
//	T1 默认关（未开开关 ⇒ 绝不注入） —— 约束①（默认关）的守门
//	T2 开了开关 + qwen 系 ⇒ 注入，且**只在顶层** —— 位置是探针实测出来的，不是随手挑的
//	T3 不支持该能力的模型 ⇒ 即便开了开关也不注入 —— 约束②（不得按 provider 猜）
//	T4 显式覆盖可**启用**内置表外的模型 —— R3 的第二半（settings.json 覆盖）
//	T5 运行时降级后停止注入 + 换模型复位 —— 端点 400 不得把会话打死
//	T6 错误判据收窄（不误降级） —— 误判代价 = 白丢能力
//	T7 DashScope 原生协议走 parameters —— 两套协议位置不同
//	T8 两条协议"该不该注入"结论一致（同一判定） —— 防 同族（跨入口不同源）
package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	// l1QwenModel 是**用户本机实际在跑的模型**（~/.whale/.env 的 WHALE_MODEL）。
	// 判据直接用它，而不是造一个虚构名字 —— 免得"判据绿、真机不生效"。
	l1QwenModel = "qwen3.7-flash-2026-07-15"
	l1GPTModel  = "gpt-4o"
)

// l1NewCompatClient 造一个走 OpenAI **兼容**协议的最小 client（用户实际就是这条路径）。
//
// ★ A2：端点必须是**官方端点**（`officialDashScopeURL`，见
// endpoint_capability_test.go）。原来的 `https://example.invalid/v1` 是"中性占位符"，
// 在端点维度加入后它变成**非官方端点** ⇒ 所有正向用例都会被端点降级吃掉，
// 判据会从"测注入"悄悄变成"测降级"（假绿）。换成用户真实在用的端点形态。
func l1NewCompatClient(t *testing.T, model string) *SimpleClient {
	t.Helper()
	return NewSimpleClient(model, "test-key", officialDashScopeURL, ProviderOpenAI)
}

// l1Body 用生产入口构造请求体（不手工拼 map —— 判据要打在生产真正执行的那条路径上）。
func l1Body(t *testing.T, c *SimpleClient) map[string]interface{} {
	t.Helper()
	return c.buildOpenAIBody(Request{Messages: []MessageEntry{{Role: "user", Content: "hi"}}}, c.snapshot())
}

// T1 默认关。
func TestL1_DefaultOff_NeverInjects(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "") // 显式清空：本判据要在"未开"的态上起跑
	c := l1NewCompatClient(t, l1QwenModel)

	if body := l1Body(t, c); body["enable_search"] != nil {
		t.Fatalf("未开开关时不得注入 enable_search，实际 body[enable_search]=%v", body["enable_search"])
	}
	if NativeSearchEnabledFor(l1QwenModel, officialDashScopeURL) {
		t.Fatalf("未开开关时 NativeSearchEnabledFor 必须为 false")
	}
}

// T2 开了开关 + qwen 系 ⇒ 注入；且**只能**出现在顶层。
//
// ★ 后半句是重点：探针实测 `parameters.enable_search` / `extra_body.enable_search`
// 在本端点上会被**静默丢弃**（200 但 prompt_tokens 不变）⇒ 如果哪天有人"顺手"把开关
// 搬到 parameters 里，行为会静默失效而**没有任何报错**。这条判据就是拦这个的。
func TestL1_SwitchOn_Qwen_InjectsTopLevelOnly(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "1")
	c := l1NewCompatClient(t, l1QwenModel)

	body := l1Body(t, c)
	if body["enable_search"] != true {
		t.Fatalf("开了开关 + qwen 系 ⇒ 顶层必须注入 enable_search=true，实际 %v", body["enable_search"])
	}
	if _, ok := body["parameters"]; ok {
		t.Errorf("兼容协议下不得写 parameters 段（本端点会静默丢弃 ⇒ 等于没接线）")
	}
	if _, ok := body["extra_body"]; ok {
		t.Errorf("兼容协议下不得写 extra_body 段（同上）")
	}
}

// T3 能力位为假的模型：即便开了开关也不注入（约束②：不得按 provider 猜）。
//
// 反例注入点：把 nativeSearchEnabledFrom 里的 cap.SupportsNativeSearch 判定删掉 ⇒ 本判据必须红。
func TestL1_UnsupportedModel_NoInjectionEvenWhenOn(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "1")

	for _, model := range []string{l1GPTModel, "claude-sonnet-4", "deepseek-chat", "llama-3-70b"} {
		c := l1NewCompatClient(t, model)
		if body := l1Body(t, c); body["enable_search"] != nil {
			t.Errorf("[%s] 能力位不支持 ⇒ 不得注入，实际 %v", model, body["enable_search"])
		}
	}
}

// T4 显式覆盖可**启用**内置表外的模型（R3 的第二半：settings.json `model_capabilities`）。
//
// 只测"启用"方向 —— 覆盖**不能**用于关闭（关闭走全局开关），这是刻意的单向设计。
func TestL1_ExplicitOverride_EnablesForeignModel(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "1")
	ResetNativeSearchModelOverride()
	defer ResetNativeSearchModelOverride()

	const foreign = "acme-search-gateway-v2" // 名字不像 qwen ⇒ 内置表判 false
	c := l1NewCompatClient(t, foreign)
	if body := l1Body(t, c); body["enable_search"] != nil {
		t.Fatalf("未被覆盖前不得注入（前置条件不成立，后面就测不出覆盖的作用）")
	}

	SetNativeSearchModelOverride([]string{"acme-search-gateway"}) // 片段匹配（与 config 同语义）
	if body := l1Body(t, c); body["enable_search"] != true {
		t.Fatalf("被显式覆盖后必须注入，实际 %v", body["enable_search"])
	}
}

// T5 运行时降级（端点回 400 后）必须停止注入；换模型必须复位。
func TestL1_RuntimeDowngrade_AndResetOnModelSwitch(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "1")
	c := l1NewCompatClient(t, l1QwenModel)

	if body := l1Body(t, c); body["enable_search"] != true {
		t.Fatalf("前置条件：降级前应当注入")
	}

	c.mu.Lock()
	c.disableNativeSearchLocked()
	c.mu.Unlock()
	if body := l1Body(t, c); body["enable_search"] != nil {
		t.Fatalf("降级后不得再注入，实际 %v", body["enable_search"])
	}

	// 换模型 ⇒ 能力位可能不同 ⇒ 降级标记必须复位（否则旧模型的失败会永久粘住新模型）
	c.SetModel(l1QwenModel)
	if body := l1Body(t, c); body["enable_search"] != true {
		t.Fatalf("SetModel 之后降级标记应复位，实际 %v", body["enable_search"])
	}
}

// T6 错误判据必须收窄：只有"点名 enable_search + 明确的不认识/非法"才算。
func TestL1_UnsupportedErrorPredicate(t *testing.T) {
	yes := []string{
		// V4/B1 实测报文（非法类型时网关的真实回话）
		`Input should be a valid boolean, unable to interpret input: parameters.enable_search`,
		`{"error":{"message":"enable_search is not supported by this model"}}`,
		`unknown parameter: enable_search`,
		`invalid value for enable_search`,
		`unsupported field enable_search`,
	}
	for _, m := range yes {
		if !isNativeSearchUnsupportedError(m) {
			t.Errorf("应判为「端点不认 enable_search」：%q", m)
		}
	}
	no := []string{
		``,
		`rate limit exceeded`,
		`enable_search`,                  // 只有字段名、没有"不认识"语义 ⇒ 不降级
		`context length exceeded`,        // 上下文超限与检索无关
		`invalid api key`,                // 含 invalid，但没点名 enable_search
		`cache_control is not supported`, // 别的能力的问题，不得连坐
	}
	for _, m := range no {
		if isNativeSearchUnsupportedError(m) {
			t.Errorf("不得判为检索不支持（会白丢能力）：%q", m)
		}
	}
}

// T7 DashScope **原生**协议：官方位置是 parameters.enable_search（与兼容协议不同）。
func TestL1_DashScopeNative_UsesParameters(t *testing.T) {
	t.Setenv(NativeSearchEnvVar, "1")
	a := NewDashScopeNativeAdapter(l1QwenModel, "test-key", "", ProviderDashScope)
	req := Request{Messages: []MessageEntry{{Role: "user", Content: "hi"}}}

	body := a.buildDashScopeBody(req, a.snapshot())
	params, ok := body["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("原生协议必须把参数放进 parameters 段，实际 body=%v", body)
	}
	if params["enable_search"] != true {
		t.Fatalf("原生协议应在 parameters.enable_search 注入，实际 %v", params["enable_search"])
	}
	if body["enable_search"] != nil {
		t.Errorf("原生协议**不**在顶层注入（那是兼容协议的位置）")
	}

	// 关掉开关后必须不注入（同一条判定）
	t.Setenv(NativeSearchEnvVar, "0")
	a2 := NewDashScopeNativeAdapter(l1QwenModel, "test-key", "", ProviderDashScope)
	params2, _ := a2.buildDashScopeBody(req, a2.snapshot())["parameters"].(map[string]interface{})
	if params2["enable_search"] != nil {
		t.Errorf("开关关掉后原生协议也不得注入，实际 %v", params2["enable_search"])
	}
}

// T8 两条协议"该不该注入"必须由**同一个判定**给出（防 ：同一事实跨入口不同源）。
//
// 做法：直接对判定函数做矩阵断言，再交叉验证两条协议的实际 body 与之**逐格一致**。
// 这样"有人只改了其中一条协议的判断"必然把本判据打红。
func TestL1_TwoProtocolsShareSinglePredicate(t *testing.T) {
	models := []string{l1QwenModel, l1GPTModel, "qwen-max", "claude-sonnet-4"}
	switches := []string{"", "1"}

	for _, sw := range switches {
		t.Setenv(NativeSearchEnvVar, sw)
		for _, m := range models {
			compat := l1NewCompatClient(t, m)
			ds := NewDashScopeNativeAdapter(m, "test-key", "", ProviderDashScope)
			req := Request{Messages: []MessageEntry{{Role: "user", Content: "hi"}}}

			want := compat.nativeSearchInjectAllowed(m, officialDashScopeURL)
			gotCompat := l1Body(t, compat)["enable_search"] == true

			dsParams, _ := ds.buildDashScopeBody(req, ds.snapshot())["parameters"].(map[string]interface{})
			gotDS := dsParams["enable_search"] == true

			if gotCompat != want {
				t.Errorf("[switch=%q model=%s] 兼容协议实际=%v 期望=%v", sw, m, gotCompat, want)
			}
			if gotDS != want {
				t.Errorf("[switch=%q model=%s] 原生协议实际=%v 期望=%v —— 两条协议判定漂移了", sw, m, gotDS, want)
			}
		}
	}
}

// l1RewriteTransport 把发往"官方端点"的请求改写到本地 httptest 服务器。
//
// ★ 为什么需要它（A2 加入端点维度后的判据构造问题）：本用例要同时满足两件事 ——
//
//	① `baseURL` 必须是**官方端点**，否则 A2 端点降级会让注入永不发生
//
// （判据就从"测注入"变成"测降级"）；
//
//	② 报文必须**真的发到本地**才能抓下来。
//
// 官方域名解析不到本地 ⇒ 用 Transport 层改目标主机，`c.baseURL` 保持官方不变。
// 这不改变被测代码：`doStream` 走的就是 `c.httpClient.Do`。
type l1RewriteTransport struct{ host string }

func (rt l1RewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = rt.host
	return http.DefaultTransport.RoundTrip(req)
}

// T9 端到端：必须证明参数**真的上到 HTTP 请求体**，而不只是"构造函数吐出了这个键"。
//
// 走的是生产入口 `SimpleClient.Stream`（main.go 造 client 用的就是它，不是 adapter 工厂），
// 由 httptest 服务器把线上报文抓下来断言。判据分三半，缺一不可：
//
//	开关开 + 官方端点 ⇒ 请求体里必须有 enable_search=true
//	开关关 + 官方端点 ⇒ **同一个入口**必须没有该键（否则"默认关"只是纸面承诺）
//	开关开 + 非官方端点 ⇒ 必须没有该键（A2 端点降级在**线上报文层**生效）
func TestL1_EndToEnd_ReachesHTTPBody(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	// call 用**官方端点**造 client，再把请求改写到本地 server（见 l1RewriteTransport）。
	call := func(t *testing.T) map[string]interface{} {
		t.Helper()
		captured = nil
		c := NewSimpleClient(l1QwenModel, "test-key", officialDashScopeURL, ProviderOpenAI)
		c.httpClient = &http.Client{Transport: l1RewriteTransport{host: server.Listener.Addr().String()}}
		events, err := c.Stream(context.Background(), Request{
			SystemPrompt: []string{"You are a helpful assistant."},
			Messages:     []MessageEntry{{Role: "user", Content: "今天有什么新闻"}},
		})
		if err != nil {
			t.Fatalf("Stream failed: %v", err)
		}
		for range events {
		}
		if len(captured) == 0 {
			t.Fatal("没有抓到请求体 —— 判据前提不成立，后面的断言都没有意义")
		}
		var body map[string]interface{}
		if err := json.Unmarshal(captured, &body); err != nil {
			t.Fatalf("请求体不是 JSON：%v", err)
		}
		return body
	}

	// ① 开关开 ⇒ 线上报文里必须有
	t.Setenv(NativeSearchEnvVar, "1")
	if body := call(t); body["enable_search"] != true {
		t.Fatalf("开了开关，但 HTTP 请求体里没有 enable_search=true（实际 %v）", body["enable_search"])
	}
	// 位置也必须在顶层（本端点会静默丢弃 parameters/extra_body 形态）
	if _, ok := json2map(t, captured)["parameters"]; ok {
		t.Errorf("兼容协议不得出现 parameters 段")
	}

	// ② 开关关 ⇒ 同一个入口必须没有
	t.Setenv(NativeSearchEnvVar, "")
	if body := call(t); body["enable_search"] != nil {
		t.Fatalf("开关关着，但 HTTP 请求体里出现了 enable_search=%v", body["enable_search"])
	}

	// ③ A2：开关开着、能力位也支持，但端点是**非官方**的 ⇒ 线上报文里必须没有。
	// 这里用裸 client（不改写 Transport）直接打本地 server：baseURL = server.URL
	// 天然就是"非官方端点"，正好是 A2 要处理的那一类。
	t.Setenv(NativeSearchEnvVar, "1")
	captured = nil
	c := NewSimpleClient(l1QwenModel, "test-key", server.URL, ProviderOpenAI)
	events, err := c.Stream(context.Background(), Request{
		Messages: []MessageEntry{{Role: "user", Content: "今天有什么新闻"}},
	})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	for range events {
	}
	if body := json2map(t, captured); body["enable_search"] != nil {
		t.Fatalf("非官方端点不得下发 enable_search（A2 端点降级应在线上报文层生效），实际 %v", body["enable_search"])
	}
}

// json2map 是判据内部的小工具：把已捕获的报文再解析一次供"位置断言"使用。
func json2map(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]interface{}{}
	}
	return m
}
