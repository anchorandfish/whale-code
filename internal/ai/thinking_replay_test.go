// Package ai — thinking 回传声明表的判别器测试（2026-10-04，步骤 1）。
//
// 本轮的判据不是"函数能跑"，而是**声明与官方约定一致、且两张表不许互相打架**。
// 因此测试分四层：
//
//	① 已知面的逐条断言 —— 声明值对不对（外部事实）
//	② 最长片段优先 —— 匹配规则对不对（结构性质，不靠散文比对）
//	③ 结构不变式 —— 表自身是否自相矛盾（内部一致性）
//	④ 计划表 × 硬约束面一致 —— 两表漂移守卫（本轮最重要的守卫）
//
// ★ 这些判据全部可判别：把生产表任一条改错（例如 DeepSeek 的 Mode 改成 none），
// ① 或 ④ 会立刻变红。对抗性验证见回执。
package ai

import (
	"strings"
	"testing"
)

// replayProbeModels 是所有判据共用的探针集：覆盖 5 个已知面 + 网关别名 + 未知面 + 空串。
var replayProbeModels = []string{
	"deepseek-chat",
	"deepseek-reasoner",
	"deepseek-v4-pro", // 网关别名：包含匹配应命中，前缀匹配会漏
	"qwen3.7-plus",
	"qwen3.8-max",
	"qwen-max",
	"claude-sonnet-4-20250514",
	"gpt-4o",
	"glm-4-plus",
	"totally-unknown-model",
	"",
}

// ---------------------------------------------------------------
// ① 已知面逐条断言
// ---------------------------------------------------------------

func TestThinkingReplayPlanFor_KnownFaces(t *testing.T) {
	cases := []struct {
		model       string
		wantMode    ThinkingReplayMode
		wantTrigger ThinkingReplayTrigger
		wantCarrier string
		wantSwitch  string
	}{
		// DeepSeek：硬校验面（不回传 ⇒ 400）
		{"deepseek-chat", ThinkingReplayRequired, ThinkingReplayWithTools, "reasoning_content", ""},
		{"deepseek-reasoner", ThinkingReplayRequired, ThinkingReplayWithTools, "reasoning_content", ""},
		{"deepseek-v4-pro", ThinkingReplayRequired, ThinkingReplayWithTools, "reasoning_content", ""},
		{"DeepSeek-Chat", ThinkingReplayRequired, ThinkingReplayWithTools, "reasoning_content", ""}, // 大小写不敏感

		// 百炼 / Qwen：可选开关面
		{"qwen3.7-plus", ThinkingReplayOptIn, ThinkingReplayAlways, "reasoning_content", "preserve_thinking"},
		{"qwen3.8-max", ThinkingReplayOptIn, ThinkingReplayAlways, "reasoning_content", "preserve_thinking"},
		{"qwen-max", ThinkingReplayOptIn, ThinkingReplayAlways, "reasoning_content", "preserve_thinking"},

		// Anthropic：签名面 ⇒ 本仓必须不回传
		{"claude-sonnet-4-20250514", ThinkingReplayNone, ThinkingReplayAlways, "", ""},
		{"us.anthropic.claude-sonnet-4", ThinkingReplayNone, ThinkingReplayAlways, "", ""},

		// OpenAI 兼容 / GLM：无回传通道
		{"gpt-4o", ThinkingReplayNone, ThinkingReplayAlways, "", ""},
		{"glm-4-plus", ThinkingReplayNone, ThinkingReplayAlways, "", ""},

		// 未知面：fail-safe
		{"totally-unknown-model", ThinkingReplayNone, ThinkingReplayAlways, "", ""},
		{"", ThinkingReplayNone, ThinkingReplayAlways, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := ThinkingReplayPlanFor(tc.model)
			if got.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
			if got.Trigger != tc.wantTrigger {
				t.Errorf("Trigger = %q, want %q", got.Trigger, tc.wantTrigger)
			}
			if got.CarrierField != tc.wantCarrier {
				t.Errorf("CarrierField = %q, want %q", got.CarrierField, tc.wantCarrier)
			}
			if got.SwitchField != tc.wantSwitch {
				t.Errorf("SwitchField = %q, want %q", got.SwitchField, tc.wantSwitch)
			}
			if strings.TrimSpace(got.Basis) == "" {
				t.Error("Basis 不得为空（每条声明都要有可复核依据）")
			}
		})
	}
}

// ---------------------------------------------------------------
// ② 最长片段优先（结构性质，不比对散文）
// ---------------------------------------------------------------

func TestThinkingReplayPlanFor_LongestMatchWins(t *testing.T) {
	// `qwen3.8` 比 `qwen` 更具体：命中它才拿得到"服务端默认已开"这个事实。
	if p := ThinkingReplayPlanFor("qwen3.8-max"); !p.SwitchDefaultOn {
		t.Errorf("qwen3.8-max 应命中更具体的 qwen3.8 条目（SwitchDefaultOn=true），实际 false")
	}
	// `qwen3.7-plus` 不含 `qwen3.8` 片段 ⇒ 落到通用 qwen 条目（默认关）。
	if p := ThinkingReplayPlanFor("qwen3.7-plus"); p.SwitchDefaultOn {
		t.Errorf("qwen3.7-plus 应命中通用 qwen 条目（SwitchDefaultOn=false），实际 true")
	}
	// 反过来钉住：两个 qwen 条目若非"最长优先"，此断言必红。
	if p := ThinkingReplayPlanFor("qwen3.8-flash-2026-09-02"); !p.SwitchDefaultOn {
		t.Errorf("带版本后缀的 qwen3.8 模型仍应命中 qwen3.8 条目，实际 SwitchDefaultOn=false")
	}
}

// ---------------------------------------------------------------
// ③ 结构不变式（表自身不许自相矛盾）
// ---------------------------------------------------------------

func TestThinkingReplayPlan_StructuralInvariants(t *testing.T) {
	validModes := map[ThinkingReplayMode]bool{
		ThinkingReplayNone: true, ThinkingReplayRequired: true, ThinkingReplayOptIn: true,
	}
	validTriggers := map[ThinkingReplayTrigger]bool{
		ThinkingReplayAlways: true, ThinkingReplayWithTools: true,
	}

	for _, model := range replayProbeModels {
		p := ThinkingReplayPlanFor(model)
		t.Run(model, func(t *testing.T) {
			if !validModes[p.Mode] {
				t.Errorf("Mode = %q 不在三态内", p.Mode)
			}
			if !validTriggers[p.Trigger] {
				t.Errorf("Trigger = %q 不在两态内", p.Trigger)
			}
			// none ⇔ 无载体：没通道就没有字段名，反之亦然。
			if p.Mode == ThinkingReplayNone && p.CarrierField != "" {
				t.Errorf("Mode=none 却给了 CarrierField=%q", p.CarrierField)
			}
			if p.Mode != ThinkingReplayNone && p.CarrierField == "" {
				t.Errorf("Mode=%q 却无 CarrierField（回传无载体 = 空操作）", p.Mode)
			}
			// 无开关 ⇒ 默认值无意义，必须为 false（防"零值被当成声明"）。
			if p.SwitchField == "" && p.SwitchDefaultOn {
				t.Error("无 SwitchField 却 SwitchDefaultOn=true")
			}
		})
	}
}

// ---------------------------------------------------------------
// ④ 计划表 × 硬约束面一致（两表漂移守卫）
// ---------------------------------------------------------------

func TestThinkingReplay_HardFaceMatchesPlanTable(t *testing.T) {
	if len(thinkingReplayHardFaces) == 0 {
		t.Fatal("硬约束面清单为空：deepseek+claude 两条已知硬面必须登记")
	}
	for _, face := range thinkingReplayHardFaces {
		plan := ThinkingReplayPlanFor(face.Pattern)
		if plan.Mode != face.Required {
			t.Errorf("硬面 %q：Required=%q 与计划表 Mode=%q 不一致（两表漂移）",
				face.Pattern, face.Required, plan.Mode)
		}
		if plan.Trigger != face.Trigger {
			t.Errorf("硬面 %q：Trigger=%q 与计划表 Trigger=%q 不一致（两表漂移）",
				face.Pattern, face.Trigger, plan.Trigger)
		}
		if strings.TrimSpace(face.Reason) == "" {
			t.Errorf("硬面 %q：Reason 不得为空（必须能说明硬在哪）", face.Pattern)
		}
	}
}

func TestThinkingReplayIsHardFace(t *testing.T) {
	cases := []struct {
		model        string
		wantHard     bool
		wantRequired ThinkingReplayMode
		wantTrigger  ThinkingReplayTrigger
	}{
		{"deepseek-chat", true, ThinkingReplayRequired, ThinkingReplayWithTools},
		{"deepseek-v4-pro", true, ThinkingReplayRequired, ThinkingReplayWithTools},
		{"claude-sonnet-4-20250514", true, ThinkingReplayNone, ThinkingReplayAlways},
		// 百炼/qwen 是"可选"而非"硬"：不回传不报错 ⇒ 不得被误列为硬面。
		{"qwen3.7-plus", false, "", ""},
		{"qwen3.8-max", false, "", ""},
		{"gpt-4o", false, "", ""},
		{"glm-4-plus", false, "", ""},
		{"totally-unknown-model", false, "", ""},
		{"", false, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			face, ok := ThinkingReplayIsHardFace(tc.model)
			if ok != tc.wantHard {
				t.Fatalf("IsHardFace = %v, want %v", ok, tc.wantHard)
			}
			if !ok {
				return
			}
			if face.Required != tc.wantRequired {
				t.Errorf("Required = %q, want %q", face.Required, tc.wantRequired)
			}
			if face.Trigger != tc.wantTrigger {
				t.Errorf("Trigger = %q, want %q", face.Trigger, tc.wantTrigger)
			}
		})
	}
}

// ===============================================================
// 步骤 2：裁决落地 —— 从「声明」到「真的写进请求体」
// ===============================================================
//
// 步骤 1 的判据问「声明表对不对」；这里问另一件事：
// 「**本请求**到底把历史思考写进请求体了没有」。
//
// ★ ⑥ 直接打在 `buildOpenAIBody` 上 —— 它是 OpenAI 兼容路径的**生产函数本体**，
// 不是为测试造出来的路径。判据 before 必红（改前该函数根本不认 thinking 块）。

// ---------------------------------------------------------------
// ⑤ 纯函数裁决：ResolveThinkingReplay
// ---------------------------------------------------------------

// modeStr 把档位转成可读名。仅用于**失败信息**：断言红时要能一眼看出是哪个档位
// 出的问题，否则一个整数 `2` 无法定位（判别器的失败信息本身就是它的可用性）。
func modeStr(m ThinkingMode) string {
	switch m {
	case ThinkingOn:
		return "on"
	case ThinkingOff:
		return "off"
	default:
		return "unspecified"
	}
}

func TestResolveThinkingReplay(t *testing.T) {
	const think = "先读文件再改"
	cases := []struct {
		name         string
		model        string
		hasTools     bool
		mode         ThinkingMode
		texts        []string
		capSupported bool
		wantApply    bool
		wantField    string
		wantText     string
		wantSwitch   string
		wantSwitchOn bool
		wantReason   ThinkingReplayReason
	}{
		// DeepSeek：required + with_tools ⇒ 带 tools 必须回传。
		// ★ 下面三条**同一场景、三个不同档位、结果完全相同** —— 这是就地判据：
		// 把档位误用成"用户关了思考就可以少传"的理由，这三条会红。
		// 依据：`/think off` 只说"**本轮**别思考"，推翻不了"历史里已有的思考必须原样送回"。
		{"deepseek+同场景+On（档位无关之一）", "deepseek-v4-pro", true, ThinkingOn, []string{think}, true, true, "reasoning_content", think, "", false, ThinkingReplayApplied},
		{"deepseek+同场景+Off（档位无关之二）", "deepseek-chat", true, ThinkingOff, []string{think}, true, true, "reasoning_content", think, "", false, ThinkingReplayApplied},
		{"deepseek+同场景+未指定（档位无关之三）", "deepseek-chat", true, ThinkingUnspecified, []string{think}, true, true, "reasoning_content", think, "", false, ThinkingReplayApplied},
		// ★★ 能力门**不得**误伤 required 面：capSupported=false（能力未确认）时，
		// DeepSeek 的协议硬约束仍要求回传 —— 回传的是厂商自己产出的
		// reasoning_content，与"模型认不认客户端开关"无关。把能力门下移到
		// required 面（多一个 `if !capSupported { return ... }`）这条会红。
		{"deepseek+能力未确认仍回传（能力门只管 opt_in）", "deepseek-v4-pro", true, ThinkingOn, []string{think}, false, true, "reasoning_content", think, "", false, ThinkingReplayApplied},
		// 多段思考必须按序拼接（丢段 = 回传不完整）
		{"deepseek+多段思考拼接", "deepseek-chat", true, ThinkingOn, []string{"第一段", "第二段"}, true, true, "reasoning_content", "第一段第二段", "", false, ThinkingReplayApplied},
		// 空白段须剔除（空字符串在部分厂商侧被判非法）
		{"deepseek+空白段剔除", "deepseek-chat", true, ThinkingOn, []string{"", "  ", "有效"}, true, true, "reasoning_content", "有效", "", false, ThinkingReplayApplied},
		// ★ 不带 tools ⇒ 触发条件不满足（把 Trigger 判错这条会红）
		{"deepseek+本请求不带工具", "deepseek-v4-pro", false, ThinkingOn, []string{think}, true, false, "", "", "", false, ThinkingReplayToolsNotInPlay},
		// 没有可回传的思考
		{"deepseek+无思考内容", "deepseek-v4-pro", true, ThinkingOn, nil, true, false, "", "", "", false, ThinkingReplayNoThinking},
		// claude：硬约束面 ⇒ **禁止**回传（档位同样无关：禁止不因用户开了思考而解除）
		{"claude+禁止回传", "claude-sonnet-4-20250514", true, ThinkingOn, []string{think}, true, false, "", "", "", false, ThinkingReplayFaceForbids},

		// ★ qwen：可选面，接线后（2026-10-05 步骤 3 第 2 小步）On 档**写载体 + 发开关**。
		// 开关字段名/开关值都必须来自裁决（调用点不得硬写）—— 断言钉住这两项。
		{"qwen+已开思考+能力确认（正向）", "qwen3.7-plus", true, ThinkingOn, []string{think}, true, true, "reasoning_content", think, "preserve_thinking", true, ThinkingReplayApplied},
		// ★ A1 门：能力未确认 ⇒ 不发开关（与下发 enable_thinking 同源）
		{"qwen+已开思考+能力未确认（A1 门）", "qwen3.7-plus", true, ThinkingOn, []string{think}, false, false, "", "", "", false, ThinkingReplayCapabilityUnconfirmed},
		// 未开思考 / 未指定档位 ⇒ **终态**（设计上不启用可选回传，永不接线）
		{"qwen+未开思考（终态，非待办）", "qwen3.7-plus", true, ThinkingOff, []string{think}, true, false, "", "", "", false, ThinkingReplayOptInNotRequested},
		{"qwen+未指定档位（终态，非待办）", "qwen3.7-plus", true, ThinkingUnspecified, []string{think}, true, false, "", "", "", false, ThinkingReplayOptInNotRequested},
		// 无思考内容时，档位/能力不改变结论（先答"有没有东西可传"）
		{"qwen+无思考内容", "qwen3.7-plus", true, ThinkingOn, nil, true, false, "", "", "", false, ThinkingReplayNoThinking},
		// 未开思考**且**能力未确认 ⇒ 先落"未开思考"（顺序即优先级：有没有东西 → 要不要 → 认不认）
		{"qwen+未开思考且能力未确认（顺序）", "qwen3.7-plus", true, ThinkingOff, []string{think}, false, false, "", "", "", false, ThinkingReplayOptInNotRequested},

		// gpt / 未知面：无通道
		{"gpt+无回传通道", "gpt-4o", true, ThinkingOn, []string{think}, true, false, "", "", "", false, ThinkingReplayFaceForbids},
		{"未知面 fail-safe", "totally-unknown-model", true, ThinkingOn, []string{think}, true, false, "", "", "", false, ThinkingReplayFaceForbids},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := ResolveThinkingReplay(tc.model, tc.hasTools, tc.mode, tc.texts, tc.capSupported)
			if d.Apply != tc.wantApply {
				t.Errorf("Apply = %v, want %v (model=%s mode=%s reason=%q)",
					d.Apply, tc.wantApply, tc.model, modeStr(tc.mode), d.Reason)
			}
			if d.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", d.Field, tc.wantField)
			}
			if d.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", d.Text, tc.wantText)
			}
			if d.SwitchField != tc.wantSwitch {
				t.Errorf("SwitchField = %q, want %q（开关字段名必须来自裁决）", d.SwitchField, tc.wantSwitch)
			}
			if d.SwitchOn != tc.wantSwitchOn {
				t.Errorf("SwitchOn = %v, want %v", d.SwitchOn, tc.wantSwitchOn)
			}
			if d.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", d.Reason, tc.wantReason)
			}
			// 结构不变式①：Apply=false ⇒ 不得带半套结论（Field/Text/Switch 必须为空/假）
			if !d.Apply && (d.Field != "" || d.Text != "" || d.SwitchField != "" || d.SwitchOn) {
				t.Errorf("Apply=false 却带 Field=%q Text=%q SwitchField=%q SwitchOn=%v",
					d.Field, d.Text, d.SwitchField, d.SwitchOn)
			}
			// 结构不变式②：不发载体却发开关 = 谎报（声明表 SwitchField 与 CarrierField 脱钩）
			if d.SwitchField != "" && !d.Apply {
				t.Errorf("SwitchField=%q 却 Apply=false —— 只发开关不发载体", d.SwitchField)
			}
			// 结构不变式③：带开关就必须同时带载体字段
			if d.SwitchField != "" && d.Field == "" {
				t.Errorf("SwitchField=%q 却无 Field —— 开关没有对应的载体", d.SwitchField)
			}
		})
	}
}

// TestReplayDimension_RequiredNoneFaceInert 钉住：**硬约束面（required）与无通道面（none）
// 既不随档位变、也不随能力确认变**。
//
// 这是**永久不变量**（协议硬约束），与下面 opt_in 面的"接线后**会**变"相对照。
// 全组合遍历（face × mode × capSupported），逐**字段**比对（含 SwitchField/SwitchOn）——
// 只看一个布尔会漏掉"多带了一个开关"这类偏差。
func TestReplayDimension_RequiredNoneFaceInert(t *testing.T) {
	const think = "历史思考"
	modes := []ThinkingMode{ThinkingUnspecified, ThinkingOn, ThinkingOff}
	caps := []bool{true, false}

	for _, model := range []string{
		"deepseek-v4-pro", "deepseek-chat", // required：必须回传，与档位/能力无关
		"claude-sonnet-4-20250514",        // none：禁止回传，不因用户开了思考而解除
		"gpt-4o", "totally-unknown-model", // none：无通道 / 未知兜底
	} {
		t.Run(model, func(t *testing.T) {
			var first ThinkingReplayDecision
			firstSet := false
			for _, m := range modes {
				for _, cs := range caps {
					d := ResolveThinkingReplay(model, true, m, []string{think}, cs)
					if !firstSet {
						first, firstSet = d, true
						continue
					}
					if d != first {
						t.Errorf("model=%s mode=%s capSupported=%v 的结论与基准不同 —— "+
							"本面必须与这两维无关。\n got=%+v\nwant=%+v",
							model, modeStr(m), cs, d, first)
					}
				}
			}
		})
	}
}

// TestReplayDimension_OptInFaceWired 钉住接线后的 opt_in 面：**档位 × 能力确认**两维共同决定。
//
// ★ 本函数于 2026-10-05 **有意翻转**（步骤 3 第 2 小步接线）——**下一个人别当回归**：
// 翻转前钉的是「可选面 Apply 恒 false，On 档落 OptInPending」，因为当时"开关注入"
// 尚未接线，On 档只是个**待接线口**。接线后 On 档必须真的写载体 + 发开关，
// 于是口径本身变了（不是"某条门被改红"，是语义前进了）。
//
// 两维各自的**终态**：
// - 非 On 档 ⇒ OptInNotRequested（用户没要求 ⇒ 永不启用，不是待办）；
// - On + 能力未确认 ⇒ CapabilityUnconfirmed（A1 门：与下发 enable_thinking 同源）；
// - On + 能力确认 ⇒ Applied（载体 + 顶层开关都出）。
func TestReplayDimension_OptInFaceWired(t *testing.T) {
	const think = "历史思考"
	const model = "qwen3.7-plus"

	// On + 能力确认 ⇒ 真的写载体 + 发开关（字段名必须来自裁决）
	if d := ResolveThinkingReplay(model, true, ThinkingOn, []string{think}, true); !d.Apply ||
		d.Field != "reasoning_content" || d.Text != think ||
		d.SwitchField != "preserve_thinking" || !d.SwitchOn || d.Reason != ThinkingReplayApplied {
		t.Errorf("On+能力确认 应写载体并发开关，实际 %+v", d)
	}
	// On + 能力未确认 ⇒ A1 门挡住（不发开关）
	if d := ResolveThinkingReplay(model, true, ThinkingOn, []string{think}, false); d.Apply ||
		d.Reason != ThinkingReplayCapabilityUnconfirmed {
		t.Errorf("On+能力未确认 应被 A1 门挡住（reason 应=%q），实际 %+v",
			ThinkingReplayCapabilityUnconfirmed, d)
	}
	// 非 On 档 ⇒ 终态（与能力无关，两两都验）
	for _, m := range []ThinkingMode{ThinkingOff, ThinkingUnspecified} {
		for _, cs := range []bool{true, false} {
			if d := ResolveThinkingReplay(model, true, m, []string{think}, cs); d.Apply ||
				d.Reason != ThinkingReplayOptInNotRequested {
				t.Errorf("档位 %s（capSupported=%v）应落到终态 %q，实际 %+v",
					modeStr(m), cs, ThinkingReplayOptInNotRequested, d)
			}
		}
	}
	// required 面的档位/能力无关性由 TestReplayDimension_RequiredNoneFaceInert 覆盖。
}

// ---------------------------------------------------------------
// ⑥ 端到端：buildOpenAIBody（生产函数本体）
// ---------------------------------------------------------------

// buildThinkingProbeReq 构造一条「工具轮」历史：assistant 带 thinking + text + tool_use，
// 后跟 tool_result。这正是 DeepSeek 要求回传的场景，也是 whale 的真实形态。
func buildThinkingProbeReq(withTools bool) Request {
	req := Request{
		Messages: []MessageEntry{
			{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "先读文件再改"},
				map[string]interface{}{"type": "text", "text": "让我看看"},
				map[string]interface{}{"type": "tool_use", "id": "c1", "name": "read_file",
					"input": map[string]interface{}{"path": "/tmp/a"}},
			}},
			{Role: "tool", Content: []interface{}{
				map[string]interface{}{"tool_use_id": "c1", "output": "file content"},
			}},
		},
	}
	if withTools {
		req.Tools = []ToolDefinition{{
			Name:        "read_file",
			Description: "read a file",
			InputSchema: map[string]interface{}{"type": "object"},
		}}
	}
	return req
}

// assistantEntryOf 取 body["messages"] 里第一条 assistant 消息。
func assistantEntryOf(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	messages, ok := body["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("messages 类型不正确: %T", body["messages"])
	}
	for _, m := range messages {
		if m["role"] == "assistant" {
			return m
		}
	}
	t.Fatal("body 里没有 assistant 消息")
	return nil
}

// TestBuildOpenAIBody_DeepSeekReplaysReasoningContent 是步骤 2 的**主判据**。
func TestBuildOpenAIBody_DeepSeekReplaysReasoningContent(t *testing.T) {
	client := NewSimpleClient("deepseek-v4-pro", "test-key", "", ProviderOpenAI)
	defer client.Close()

	body := client.buildOpenAIBody(buildThinkingProbeReq(true), client.snapshot())
	entry := assistantEntryOf(t, body)

	got, ok := entry["reasoning_content"]
	if !ok {
		t.Fatal("assistant 消息缺少 reasoning_content：" +
			"DeepSeek 面不回传历史思考 ⇒ 服务端 HTTP 400（硬伤）")
	}
	if got != "先读文件再改" {
		t.Errorf("reasoning_content = %v, want %q", got, "先读文件再改")
	}
	// 回传不得顶掉同一消息的正文与工具调用
	if entry["content"] != "让我看看" {
		t.Errorf("content = %v, want %q", entry["content"], "让我看看")
	}
	if _, ok := entry["tool_calls"]; !ok {
		t.Error("tool_calls 被回传逻辑顶掉了")
	}
}

// TestBuildOpenAIBody_ReplayRespectsFace 钉住"不能到处乱写"：
// 只有 required 面（DeepSeek）+ 本请求带 tools 才写。
func TestBuildOpenAIBody_ReplayRespectsFace(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		withTools bool
	}{
		{"qwen 未开档位不写（档位=未指定）", "qwen3.7-plus", true},
		{"claude 无回传能力", "claude-sonnet-4-20250514", true},
		{"gpt 无回传通道", "gpt-4o", true},
		{"未知面 fail-safe", "totally-unknown-model", true},
		{"deepseek 但本请求不带 tools", "deepseek-v4-pro", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewSimpleClient(tc.model, "test-key", "", ProviderOpenAI)
			defer client.Close()
			body := client.buildOpenAIBody(buildThinkingProbeReq(tc.withTools), client.snapshot())
			entry := assistantEntryOf(t, body)
			if v, ok := entry["reasoning_content"]; ok {
				t.Errorf("本面/本请求不应写 reasoning_content，却写成 %v", v)
			}
		})
	}
}

// ---------------------------------------------------------------
// ⑧ 步骤 3 第 2 小步：可选面接线 —— 载体 + 顶层开关
// ---------------------------------------------------------------

// TestBuildOpenAIBody_QwenOptInReplay 是步骤 3 第 2 小步的**主判据**（生产函数本体）：
// 百炼可选面 + On 档 + 能力确认 ⇒ **载体写进历史 assistant 消息** +
// **请求体顶层贴 preserve_thinking=true**。
//
// ★ before 必红：接线前 opt_in 面 Apply=false，本判据两条（载体 / 开关）都取不到。
func TestBuildOpenAIBody_QwenOptInReplay(t *testing.T) {
	client := NewSimpleClient("qwen3.7-plus", "test-key", "", ProviderOpenAI)
	defer client.Close()

	req := buildThinkingProbeReq(true)
	req.Thinking = &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 1000}
	body := client.buildOpenAIBody(req, client.snapshot())

	// ① 载体：历史 assistant 消息上的 reasoning_content
	entry := assistantEntryOf(t, body)
	got, ok := entry["reasoning_content"]
	if !ok {
		t.Fatal("assistant 消息缺少 reasoning_content：可选面 On 档应回传历史思考（否则掉精度）")
	}
	if got != "先读文件再改" {
		t.Errorf("reasoning_content = %v, want %q", got, "先读文件再改")
	}
	// ② 开关：请求体**顶层**（与 enable_thinking 同位置）
	if v, ok := body["preserve_thinking"]; !ok || v != true {
		t.Errorf("顶层 preserve_thinking 应为 true，实际 present=%v value=%v（body 键=%v）",
			ok, v, bodyKeys(body))
	}
	// 回传不得顶掉正文与工具调用（R3⑤：同一消息的其它字段）
	if entry["content"] != "让我看看" {
		t.Errorf("content = %v, want %q", entry["content"], "让我看看")
	}
	if _, ok := entry["tool_calls"]; !ok {
		t.Error("tool_calls 被回传逻辑顶掉了")
	}
}

// TestBuildOpenAIBody_QwenOptInNotRequested 钉住"未开档位**不发**开关"：
// preserve_thinking 服务端默认 false（部分新模型 true）⇒ 只有 On 时才发 true；
// 其余档位**不发该字段**（显式发 false 会改掉那些默认 true 的模型本来在做的事）。
func TestBuildOpenAIBody_QwenOptInNotRequested(t *testing.T) {
	cases := []struct {
		name string
		cfg  *ThinkingConfig // nil = 未设置档位
	}{
		{"未设置档位", nil},
		{"显式关", &ThinkingConfig{Enabled: ThinkingPtr(false)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewSimpleClient("qwen3.7-plus", "test-key", "", ProviderOpenAI)
			defer client.Close()
			req := buildThinkingProbeReq(true)
			req.Thinking = tc.cfg
			body := client.buildOpenAIBody(req, client.snapshot())

			if v, ok := body["preserve_thinking"]; ok {
				t.Errorf("未开思考时不得下发 preserve_thinking，实际 %v", v)
			}
			entry := assistantEntryOf(t, body)
			if v, ok := entry["reasoning_content"]; ok {
				t.Errorf("未开思考时不得回传 reasoning_content，实际 %v", v)
			}
		})
	}
}

// TestBuildOpenAIBody_OptInReplayRespectsCapabilityGate 是 **A1 门**的端到端判据：
// 用户把 qwen 声明成"不支持 thinking" ⇒ 下发侧不发 enable_thinking，
// 回传侧**也不得**发 preserve_thinking（同一事实同源，）。
//
// ★ 与 TestBuildOpenAIBody_QwenOptInReplay 构成**成对判据**：
//
//	前者证明"接线接上了"，本条证明"门真的在门"（不是一律放行）。
//	—— 这正是 R9「修复引入分流时两支各做一次反向注入」要求的第二支守卫。
func TestBuildOpenAIBody_OptInReplayRespectsCapabilityGate(t *testing.T) {
	RegisterThinkingDeclaration("qwen3.7-plus", ThinkingCapUnsupported)
	t.Cleanup(ClearThinkingDeclarations)

	client := NewSimpleClient("qwen3.7-plus", "test-key", "", ProviderOpenAI)
	defer client.Close()
	req := buildThinkingProbeReq(true)
	req.Thinking = &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 1000}
	body := client.buildOpenAIBody(req, client.snapshot())

	// 下发侧：声明 unsupported ⇒ 不发 enable_thinking（B2 既有行为）
	if v, ok := body["enable_thinking"]; ok {
		t.Errorf("声明 unsupported 后不应下发 enable_thinking，实际 %v", v)
	}
	// 回传侧：同样不得发 preserve_thinking（A1 门：与 enable_thinking 同源）
	if v, ok := body["preserve_thinking"]; ok {
		t.Errorf("声明 unsupported 后不应下发 preserve_thinking（A1 门：须与 enable_thinking 同源），实际 %v", v)
	}
	entry := assistantEntryOf(t, body)
	if v, ok := entry["reasoning_content"]; ok {
		t.Errorf("声明 unsupported 后不应回传 reasoning_content，实际 %v", v)
	}
}

// ---------------------------------------------------------------
// ⑦ Anthropic 硬约束面护栏：无签名 thinking 块必须被剥离
// ---------------------------------------------------------------

func TestConvertMessagesForAnthropic_StripsUnsignedThinking(t *testing.T) {
	msgs := []MessageEntry{
		{Role: "assistant", Content: []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "无签名思考"},
			map[string]interface{}{"type": "text", "text": "正文"},
		}},
	}
	out := convertMessagesForAnthropic(msgs)
	if len(out) != 1 {
		t.Fatalf("消息条数变了：%d", len(out))
	}
	blocks, ok := out[0].Content.([]interface{})
	if !ok {
		t.Fatalf("Content 类型 = %T，应被回写为块数组（否则剥离根本没生效）", out[0].Content)
	}
	keptText := false
	for _, b := range blocks {
		m, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "thinking" || m["type"] == "redacted_thinking" {
			t.Fatalf("thinking 块未被剥离 ⇒ 无签名块进 Anthropic 请求体，必 400：%v", m)
		}
		if m["type"] == "text" && m["text"] == "正文" {
			keptText = true
		}
	}
	if !keptText {
		t.Error("剥离 thinking 时把正文一起弄丢了")
	}
}

// 回归：无图片、无 thinking 的消息，Content **保持原样**（类型也不许被改）。
func TestConvertMessagesForAnthropic_PlainMessageUntouched(t *testing.T) {
	const plain = "纯文本"
	msgs := []MessageEntry{{Role: "assistant", Content: plain}}
	out := convertMessagesForAnthropic(msgs)
	if out[0].Content != plain {
		t.Errorf("纯文本消息 Content 被改写：%v", out[0].Content)
	}
}
