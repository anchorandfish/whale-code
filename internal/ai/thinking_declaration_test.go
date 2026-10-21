package ai

import (
	"strings"
	"testing"
)

// B1/B2：thinking 能力必须能被**用户声明**，且判定必须是**三态**。
//
// 两个缺陷（对应方案 B 批 B1/B2）：
//
//	B2：GetModelCapability 的三条路径（精确表 / 前缀推断 / 未命中）把结果压进
// 一个 bool，于是「**已知**不支持」（gpt-4o）与「**不知道**」（kimi-k2、
// 或某个被前缀猜成 false 的新模型）变成同一个值。下游只能一律不发 ——
// 行为是安全的，但**信息丢了**：既不能如实告知，也不能被用户纠正。
//	B1：`/model add` 无法声明能力 ⇒ 用户自加的模型只能吃前缀推断。
// 用户面对的是他真实接的那个门，推断面对的是模型名 —— 谁更可信不言而喻。
//
// ★ 判别性自检（R4-7）：本文件的"声明"用例与"未知档"用例都必须能被打红。
// 见文件末 TestB2_Mutant 注释描述的两次 mutant；已实测两向各自转红。

// ---------------------------------------------------------------------------
// Part 1：三态判定（B2）
// ---------------------------------------------------------------------------

// TestThinkingSupportFor_ThreeStates 固化三态，并**逐格声明该格的数据来源**，
// 避免"碰巧对"：精确表来的 Unsupported 与推断来的 Unknown 语义完全不同。
func TestThinkingSupportFor_ThreeStates(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	cases := []struct {
		model     string
		want      ThinkingCapability
		wantSrc   capabilitySource
		srcReason string
	}{
		{"gpt-4o", ThinkingCapUnsupported, capabilitySourceExact, "静态表显式条目，false"},
		{"deepseek-chat", ThinkingCapUnsupported, capabilitySourceExact, "静态表显式条目，false"},
		{"claude-3-5-sonnet-20241022", ThinkingCapUnsupported, capabilitySourceExact, "静态表显式条目，false"},
		{"claude-sonnet-4-20250514", ThinkingCapSupported, capabilitySourceExact, "静态表显式条目，true"},
		{"qwen-max", ThinkingCapUnsupported, capabilitySourceExact, "静态表显式条目，false"},
		{"qwen3.7-flash", ThinkingCapSupported, capabilitySourceInferred, "前缀 qwen3 推断 true"},
		{"claude-9-future", ThinkingCapSupported, capabilitySourceInferred, "前缀 claude 推断 true"},
		// ★ 下面两条是 B2 的核心新行为：**猜出来的 false 不可信** ⇒ Unknown。
		// 修复前它们与 gpt-4o 同值（都被压成"不支持"）。
		{"glm-5-future", ThinkingCapUnknown, capabilitySourceInferred, "前缀 glm 推断 false，但那是猜的 ⇒ 不知道"},
		{"gpt-5-future", ThinkingCapUnknown, capabilitySourceInferred, "前缀 gpt 推断 false，但那是猜的 ⇒ 不知道"},
		// 什么都没命中 ⇒ 不知道。
		{"kimi-k2", ThinkingCapUnknown, capabilitySourceNone, "无任何前缀命中"},
		{"", ThinkingCapUnknown, capabilitySourceNone, "空模型名"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			// 事实前置：先证明这一格走的是我以为的那条路径。
			// 若哪天表/前缀分支改了，这里先红并说明"前提变了"，而不是静默假通过。
			if _, src := resolveCapabilityWithSource(tc.model); src != tc.wantSrc {
				t.Fatalf("前置不成立：%q 命中来源 = %v，用例假设 %v（%s）",
					tc.model, src, tc.wantSrc, tc.srcReason)
			}
			if got := ThinkingSupportFor(tc.model, officialDashScopeURL); got != tc.want {
				t.Errorf("ThinkingSupportFor(%q) = %v，期望 %v（%s）",
					tc.model, got, tc.want, tc.srcReason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 2：声明优先于推断（B1）
// ---------------------------------------------------------------------------

// TestThinkingDeclaration_OverridesInference 是 B1 的主判据：
// 声明必须**双向**覆盖 —— 既能给"表里说不支持"的模型开支持，
// 也能给"推断说支持"的模型判不支持。只测一向会放过"声明只在 false→true 生效"的半成品。
func TestThinkingDeclaration_OverridesInference(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	// 前置：未声明时，两个模型分别处于"已知不支持"与"推断支持"。
	if got := ThinkingSupportFor("gpt-4o", officialDashScopeURL); got != ThinkingCapUnsupported {
		t.Fatalf("前置不成立：gpt-4o 未声明时应为 Unsupported，实际 %v", got)
	}
	if got := ThinkingSupportFor("qwen3.7-flash", officialDashScopeURL); got != ThinkingCapSupported {
		t.Fatalf("前置不成立：qwen3.7-flash 未声明时应为 Supported，实际 %v", got)
	}

	// 方向一：把"已知不支持"改成支持。
	RegisterThinkingDeclaration("gpt-4o", ThinkingCapSupported)
	if got := ThinkingSupportFor("gpt-4o", officialDashScopeURL); got != ThinkingCapSupported {
		t.Errorf("声明 supported 后 ThinkingSupportFor(gpt-4o) = %v，期望 Supported", got)
	}
	// 方向二：把"推断支持"改成不支持。
	RegisterThinkingDeclaration("qwen3.7-flash", ThinkingCapUnsupported)
	if got := ThinkingSupportFor("qwen3.7-flash", officialDashScopeURL); got != ThinkingCapUnsupported {
		t.Errorf("声明 unsupported 后 ThinkingSupportFor(qwen3.7-flash) = %v，期望 Unsupported", got)
	}
	// 邻位不串味：别的模型不受影响。
	if got := ThinkingSupportFor("claude-sonnet-4-20250514", officialDashScopeURL); got != ThinkingCapSupported {
		t.Errorf("声明不得波及邻位：claude-sonnet-4 = %v，期望 Supported", got)
	}
	// 未被声明的未知模型仍是 Unknown（声明不是"一键全开"）。
	if got := ThinkingSupportFor("kimi-k2", officialDashScopeURL); got != ThinkingCapUnknown {
		t.Errorf("未声明的 kimi-k2 = %v，期望 Unknown", got)
	}

	// 撤销声明：传 Unknown 应当删除声明、回到推断。
	RegisterThinkingDeclaration("gpt-4o", ThinkingCapUnknown)
	if got := ThinkingSupportFor("gpt-4o", officialDashScopeURL); got != ThinkingCapUnsupported {
		t.Errorf("撤销声明后 gpt-4o = %v，期望回到 Unsupported（推断）", got)
	}
}

// TestThinkingDeclaration_CaseAndSpaceInsensitive 声明按模型名匹配，
// 大小写与空白不得成为"声明没生效"的隐形原因（用户从 /model add 敲进来的名字
// 未必与 /think 时用的完全同形）。
func TestThinkingDeclaration_CaseAndSpaceInsensitive(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	RegisterThinkingDeclaration("  Kimi-K2  ", ThinkingCapSupported)
	for _, q := range []string{"kimi-k2", "KIMI-K2", "Kimi-K2"} {
		if got := ThinkingSupportFor(q, officialDashScopeURL); got != ThinkingCapSupported {
			t.Errorf("ThinkingSupportFor(%q) = %v，期望 Supported（声明应大小写/空白不敏感）", q, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Part 3：声明字面量的往返（持久化前提）
// ---------------------------------------------------------------------------

// TestThinkingCapability_RoundTrip 固化 Parse/String 互逆 ——
// `/model add --supports-thinking` 写进 models.json、重启后再读回来，
// 必须还原成同一态。字面量一旦漂移，用户的声明会在重启后静默失效。
func TestThinkingCapability_RoundTrip(t *testing.T) {
	// 只有两个"可声明"的态参与往返：未知档不参与 —— 见下。
	for _, want := range []ThinkingCapability{ThinkingCapSupported, ThinkingCapUnsupported} {
		s := want.String()
		got, ok := ParseThinkingCapability(s)
		if !ok {
			t.Errorf("ParseThinkingCapability(%q) 解析失败", s)
			continue
		}
		if got != want {
			t.Errorf("往返失败：%v → %q → %v", want, s, got)
		}
	}
	// 未知档的展示字面量是 "unknown"，但它**不得**被解析成"成功"：
	// 调用方要靠 ok 区分「用户给了非法值」（应报错）与「用户没给」（继承推断）。
	// 若这里放开，"--supports-thinking=supoprted" 这类拼写错误会被静默吞掉。
	if got := ThinkingCapUnknown.String(); got != "unknown" {
		t.Errorf("ThinkingCapUnknown.String() = %q，期望 \"unknown\"", got)
	}
	if got, ok := ParseThinkingCapability("unknown"); ok {
		t.Errorf("ParseThinkingCapability(\"unknown\") 不应报成功，实际 (%v,true)", got)
	}
	// 常见别名都要认（否则用户写 on/off 会被判"非法值"）。
	for _, s := range []string{"supported", "SUPPORT", "On", "true", "yes"} {
		if got, ok := ParseThinkingCapability(s); !ok || got != ThinkingCapSupported {
			t.Errorf("ParseThinkingCapability(%q) = (%v,%v)，期望 (Supported,true)", s, got, ok)
		}
	}
	for _, s := range []string{"unsupported", "No", "off", "false"} {
		if got, ok := ParseThinkingCapability(s); !ok || got != ThinkingCapUnsupported {
			t.Errorf("ParseThinkingCapability(%q) = (%v,%v)，期望 (Unsupported,true)", s, got, ok)
		}
	}
}

// ---------------------------------------------------------------------------
// Part 4：端到端（生产入口 buildOpenAIBody）
// ---------------------------------------------------------------------------

// TestThinkingDeclaration_ReachesOpenAIBody 是本文件最关键的一条：
// 声明必须真的改变**生产路径**发出的报文，而不只是改变一个查询函数。
// 走 NewSimpleClient + buildOpenAIBody（生产唯一 OpenAI body 构造点）。
func TestThinkingDeclaration_ReachesOpenAIBody(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	on := ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 2048}

	// ① 未声明：kimi-k2 是"不知道" ⇒ 不下发。
	body := thinkingGateBody(t, "kimi-k2", on)
	if _, present := body["enable_thinking"]; present {
		t.Errorf("未声明的 kimi-k2 不应下发 enable_thinking，实际 body=%v", body)
	}

	// ② 声明 supported 后：必须下发 true + 预算。
	RegisterThinkingDeclaration("kimi-k2", ThinkingCapSupported)
	body = thinkingGateBody(t, "kimi-k2", on)
	if body["enable_thinking"] != true {
		t.Errorf("声明 supported 后必须下发 enable_thinking=true，实际 %v（body=%v）",
			body["enable_thinking"], body)
	}
	if body["thinking_budget"] != 2048 {
		t.Errorf("声明 supported 后应带上 thinking_budget=2048，实际 %v", body["thinking_budget"])
	}

	// ③ 反向：给"已知不支持"的 gpt-4o 声明 supported ⇒ 也要下发
	// （声明优先于静态表，否则用户无法纠正错的表）。
	RegisterThinkingDeclaration("gpt-4o", ThinkingCapSupported)
	body = thinkingGateBody(t, "gpt-4o", on)
	if body["enable_thinking"] != true {
		t.Errorf("gpt-4o 声明 supported 后必须下发 enable_thinking=true，实际 %v", body["enable_thinking"])
	}

	// ④ 反向的另一向：给"推断支持"的 qwen3.7-flash 声明 unsupported ⇒ 不得下发。
	RegisterThinkingDeclaration("qwen3.7-flash", ThinkingCapUnsupported)
	body = thinkingGateBody(t, "qwen3.7-flash", on)
	if _, present := body["enable_thinking"]; present {
		t.Errorf("声明 unsupported 的 qwen3.7-flash 不应下发 enable_thinking，实际 %v", body)
	}

	// ⑤ 声明 unsupported 也不得影响「显式关闭」——Off 对任何模型都必须能下发，
	// 否则 /think off 会在被声明为 unsupported 的模型上失效（服务端默认往往是开）。
	body = thinkingGateBody(t, "qwen3.7-flash", ThinkingConfig{Enabled: ThinkingPtr(false)})
	if body["enable_thinking"] != false {
		t.Errorf("显式关闭必须仍下发 enable_thinking=false，实际 %v", body["enable_thinking"])
	}
}

// ---------------------------------------------------------------------------
// Part 5：/model add|edit 的接线（flag → ModelConfig → 声明层）
// ---------------------------------------------------------------------------

// TestSupportsThinkingFlag_ParsedAndApplied 固化 `--supports-thinking` 在
// add / edit 两条解析路径上的行为。两条路径是**分开实现**的（ParseModelAddArgs /
// ParseModelEditArgs），只测一条会放过"另一条忘了加"——这正是本仓的复发坑。
func TestSupportsThinkingFlag_ParsedAndApplied(t *testing.T) {
	// add 路径
	cfg, err := ParseModelAddArgs("kimi-k2 --supports-thinking supported")
	if err != nil {
		t.Fatalf("add 解析失败: %v", err)
	}
	if cfg.ThinkingSupport != "supported" {
		t.Errorf("add: ThinkingSupport = %q，期望 %q", cfg.ThinkingSupport, "supported")
	}

	cfg, err = ParseModelAddArgs("kimi-k2 --supports-thinking unsupported")
	if err != nil {
		t.Fatalf("add 解析失败: %v", err)
	}
	if cfg.ThinkingSupport != "unsupported" {
		t.Errorf("add: ThinkingSupport = %q，期望 %q", cfg.ThinkingSupport, "unsupported")
	}

	// edit 路径
	_, updates, err := ParseModelEditArgs("kimi-k2 --supports-thinking unsupported")
	if err != nil {
		t.Fatalf("edit 解析失败: %v", err)
	}
	if updates["thinking_support"] != "unsupported" {
		t.Errorf("edit: updates[thinking_support] = %q，期望 %q", updates["thinking_support"], "unsupported")
	}

	// ★ 拼错必须**报错**，不能静默当成"未声明"：静默会把一条假生效的声明
	// 留给用户（他以为声明过了，实际什么都没发生）。
	for _, bad := range []string{"supoprted", "yes-please", "1"} {
		if _, err := ParseModelAddArgs("kimi-k2 --supports-thinking " + bad); err == nil {
			t.Errorf("add: --supports-thinking %q 应报错，实际 nil", bad)
		}
		if _, _, err := ParseModelEditArgs("kimi-k2 --supports-thinking " + bad); err == nil {
			t.Errorf("edit: --supports-thinking %q 应报错，实际 nil", bad)
		}
	}

	// 缺值也要报错（而不是把下一个 flag 当值吞掉）。
	if _, err := ParseModelAddArgs("kimi-k2 --supports-thinking"); err == nil {
		t.Error("add: --supports-thinking 缺值应报错，实际 nil")
	}
	if _, _, err := ParseModelEditArgs("kimi-k2 --supports-thinking"); err == nil {
		t.Error("edit: --supports-thinking 缺值应报错，实际 nil")
	}
}

// TestSupportsThinking_ApplyModelUpdates 固化 edit 的赋值与应用层复用校验。
func TestSupportsThinking_ApplyModelUpdates(t *testing.T) {
	cfg := &ModelConfig{Name: "kimi-k2"}
	if err := ApplyModelUpdates(cfg, map[string]string{"thinking_support": "supported"}); err != nil {
		t.Fatalf("ApplyModelUpdates 失败: %v", err)
	}
	if cfg.ThinkingSupport != "supported" {
		t.Errorf("ThinkingSupport = %q，期望 supported", cfg.ThinkingSupport)
	}
	// 公开函数的二次校验：绕过解析器直接塞非法值也必须被拒。
	if err := ApplyModelUpdates(cfg, map[string]string{"thinking_support": "bogus"}); err == nil {
		t.Error("ApplyModelUpdates 应拒绝非法 thinking_support，实际 nil")
	}
	if cfg.ThinkingSupport != "supported" {
		t.Errorf("被拒的更新不得改坏原值，实际 %q", cfg.ThinkingSupport)
	}
}

// TestFormatModelInfo_ShowsDeclaredAndEffective 固化 /model info 的展示：
// 声明与最终判定**并列**，否则用户无法判断"我声明的东西到底有没有生效"。
func TestFormatModelInfo_ShowsDeclaredAndEffective(t *testing.T) {
	ClearThinkingDeclarations()
	t.Cleanup(ClearThinkingDeclarations)

	// ★ A2：baseURL 用**官方端点**。若用中性占位符（`x.invalid`），
	// `effective=unknown` 会因为**端点降级**成立，而不是本条想钉的
	// 「kimi-k2 无任何前缀命中 ⇒ unknown」⇒ 判据变成假绿。
	cfg := &ModelConfig{Name: "kimi-k2", Provider: ProviderOpenAI, BaseURL: officialDashScopeURL}
	out := FormatModelInfo(cfg)
	if !strings.Contains(out, "declared=(undeclared)") {
		t.Errorf("未声明时应显示 declared=(undeclared)，实际:\n%s", out)
	}
	if !strings.Contains(out, "effective=unknown") {
		t.Errorf("kimi-k2 未声明时 effective 应为 unknown，实际:\n%s", out)
	}

	cfg.ThinkingSupport = "supported"
	out = FormatModelInfo(cfg)
	if !strings.Contains(out, "declared=supported") {
		t.Errorf("应显示 declared=supported，实际:\n%s", out)
	}
	if !strings.Contains(out, "effective=unknown") {
		// ★ 注意：FormatModelInfo 只读 cfg，不读声明层 —— 声明层是进程级全局状态。
		// 这里刻意断言 effective 仍为 unknown，用来**钉住**这条边界：
		// 让"展示"与"生效"的耦合关系显式，避免以后有人误以为显示=已登记。
		t.Errorf("FormatModelInfo 不应擅自读全局声明层；effective 应为 unknown，实际:\n%s", out)
	}
}
