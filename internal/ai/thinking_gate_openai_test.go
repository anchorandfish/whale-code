package ai

import "testing"

// A3：`SupportsThinking` 必须**同源**。
//
// 缺陷（2026-10-04 实测）：四处 thinking 注入点里，DashScope 原生
// （adapter_dashscope.go:135）与 Gemini（adapter_gemini.go:142）都查
// `cap.SupportsThinking`，而 **OpenAI 兼容路径（provider.go:778）不查** ——
// 对模型能力表说不支持的模型也照发 `enable_thinking: true`。
// adapter_dashscope.go:131 的注释正是在警告这件事（"否则对不支持 thinking 的模型
// 下发 enable 会被服务端拒绝"）。我们生产走的恰好是 OpenAI 兼容路径。
//
// ★ 判别性：本文件落地时（未补门）case gpt-4o / 未知模型 两格必须红。
//
// ★ 断言成对写（R3-③ / R4-11）：负向断言（"不含 enable_thinking"）极易被
// 「更早短路」蒙对 ⇒ 必须配正向留痕 —— case qwen3.7-plus（支持）与
// ThinkingOff（任何模型都必须下发 false）两条绿，才证明 switch 分支真的走到了。

// thinkingGateBody 用**生产入口**构造请求体：NewSimpleClient（生产构造函数）
// + buildOpenAIBody（生产唯一 OpenAI body 构造点）。不手工拼 map。
// 注意 Request.Thinking 是 *ThinkingConfig —— nil 与「Enabled=nil」都表示未指定，
// 这里显式传 &cfg 以覆盖三态中的两个明确取值。
func thinkingGateBody(t *testing.T, model string, cfg ThinkingConfig) map[string]interface{} {
	t.Helper()
	// A2：端点必须是官方端点，否则端点降级会让所有正向断言假失败
	// （见 endpoint_capability_test.go 的 officialDashScopeURL 注释）。
	c := NewSimpleClient(model, "test-key", officialDashScopeURL, ProviderOpenAI)
	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Thinking: &cfg,
	}
	body := c.buildOpenAIBody(req, c.snapshot())
	// 前置留痕：证明真的走完了 builder（不是被更早的 return 短路）
	if body == nil || body["model"] == nil {
		t.Fatalf("前置不成立：body 未构造完整（%v）⇒ 后续断言不可信", body)
	}
	return body
}

// TestThinkingGate_OpenAIPathRespectsCapability 覆盖四格：能力支持与否 × 开/关。
func TestThinkingGate_OpenAIPathRespectsCapability(t *testing.T) {
	cases := []struct {
		name           string
		model          string
		thinking       ThinkingConfig
		wantKey        bool
		wantValue      interface{}
		wantCapability bool
	}{
		{
			name: "qwen3支持_开启_必须下发true", model: "qwen3.7-plus",
			thinking: ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 4096},
			wantKey:  true, wantValue: true, wantCapability: true,
		},
		{
			name: "gpt不支持_开启_必须不下发", model: "gpt-4o",
			thinking: ThinkingConfig{Enabled: ThinkingPtr(true)},
			wantKey:  false, wantCapability: false,
		},
		{
			name: "未知模型_开启_必须不下发（fail-safe）", model: "custom-vendor-model",
			thinking: ThinkingConfig{Enabled: ThinkingPtr(true)},
			wantKey:  false, wantCapability: false,
		},
		{
			name: "gpt不支持_显式关闭_仍须下发false", model: "gpt-4o",
			thinking: ThinkingConfig{Enabled: ThinkingPtr(false)},
			wantKey:  true, wantValue: false, wantCapability: false,
		},
		{
			name: "未指定_无论能力如何都不下发", model: "qwen3.7-plus",
			thinking: ThinkingConfig{},
			wantKey:  false, wantCapability: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// ★ 事实前置：能力表的判定必须与用例假设一致。
			// 若哪天能力表改了，这里先红并告诉你"前提变了"，而不是静默假通过。
			if got := GetModelCapability(tc.model).SupportsThinking; got != tc.wantCapability {
				t.Fatalf("前置不成立：GetModelCapability(%q).SupportsThinking = %v，用例假设 %v", tc.model, got, tc.wantCapability)
			}

			body := thinkingGateBody(t, tc.model, tc.thinking)
			raw, present := body["enable_thinking"]

			if tc.wantKey != present {
				t.Errorf("enable_thinking 存在性 = %v，期望 %v（body=%v）", present, tc.wantKey, body)
				return
			}
			if tc.wantKey {
				if raw != tc.wantValue {
					t.Errorf("enable_thinking = %v，期望 %v", raw, tc.wantValue)
				}
			}
			// 预算只在「真开了」时才该出现
			if _, ok := body["thinking_budget"]; ok && !(tc.wantKey && tc.wantValue == true) {
				t.Errorf("thinking_budget 不应出现（enable_thinking=%v）", raw)
			}
		})
	}
}

// TestThinkingGate_UnsupportedModelStillSendsExplicitOff 单独固化"显式关闭对任何模型都安全"。
//
// 这条是 `ThinkingOff` 语义的守卫：补门时最容易顺手把 Off 也一起门掉
// （"一刀切式假修复"，见 production-path-tdd R9），那会让 /think off 对
// 能力表不支持的模型失效 —— 而服务端默认往往是"开"。
func TestThinkingGate_UnsupportedModelStillSendsExplicitOff(t *testing.T) {
	for _, model := range []string{"gpt-4o", "deepseek-chat", "glm-5", "custom-vendor-model"} {
		t.Run(model, func(t *testing.T) {
			if GetModelCapability(model).SupportsThinking {
				t.Fatalf("前置不成立：%q 被判定为支持 thinking，本用例需要不支持形态", model)
			}
			body := thinkingGateBody(t, model, ThinkingConfig{Enabled: ThinkingPtr(false)})
			if body["enable_thinking"] != false {
				t.Errorf("显式关闭必须下发 enable_thinking=false，实际 %v", body["enable_thinking"])
			}
		})
	}
}
