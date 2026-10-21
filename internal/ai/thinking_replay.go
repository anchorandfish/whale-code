// Package ai — thinking「回传（replay）」的**唯一声明与判定入口**。
//
// # 它回答什么问题
//
// 「同一个会话里，**前面的思考内容**要不要连同历史消息一起发回给服务端？」
//
// 这个问题与「本轮的思考开关下不下发」（`ThinkingConfig.Resolve` / 三个适配器）是**两件事**：
//
//	开关（injection）: 本轮要不要让模型思考 → enable_thinking / thinking.type
//	回传（replay） : 历史里的思考要不要带回去 → reasoning_content / 签名 thinking 块
//
// 落地进度（截至 2026-10-05）：
// - DeepSeek required 面：已接（`provider.go` 的 `buildOpenAIBody` 经本裁决写载体）；
// - 百炼 opt_in 面：已接（On 档写载体 + 顶层 `preserve_thinking`，带 A1 能力门）；
// - Anthropic：仍为"不回传"（本仓无签名位，回传必 400），并已加静态护栏（步骤 4）。
//
// ★ 尚未接的同源点：DashScope **原生**协议（`adapter_dashscope.go` 的 `buildDashScopeBody`）。
// 那条路径当前连 assistant 的 tool_use 都没接（只提取 text），属**另一处独立缺口** ——
// 故本裁决尚未在那儿生效；登记见 `内部工作回执`。生产实际走的是 OpenAI 兼容路径。
//
// # 为什么必须按「模型面」声明，不能按 provider 配置
//
// 与 `ModelCapability`（capability.go:61-63）同一条硬约束：用户常配
// `provider:"openai"` + 私有 `base_url`，甚至让同一个网关代理多家模型
// ⇒ 按 `provider` 字段判**必错**；判据只能来自**模型名**与厂商 API 约定。
//
// # 三面三种机制（这是本表存在的理由：没有统一做法可抄）
//
//	DeepSeek : 明文 `reasoning_content`，**硬校验** —— 带 tools 的请求不回传就是 400；
//	百炼/Qwen : 明文 `reasoning_content` + 独立布尔开关 `preserve_thinking`，
//
// 不回传**不报错**，只是静默掉精度（且开启后计入输入 Token 计费）；
//
//	Anthropic : 加密签名 thinking 块，回传必须**原样带 signature**；
//
// 本仓 `ContentBlock` 只存纯文本、没有签名位 ⇒ 回传必 400（硬约束面，见下）。
//
// # 依据（发布时逐条复核，不引转述）
//
// - DeepSeek《Thinking Mode》: "携带了 tools 参数的请求，在后续所有请求中，必须完整回传
// reasoning_content 给 API——即使该轮模型未实际进行工具调用。若您的代码中未正确回传
// reasoning_content，API 会返回 400 报错。"
// - 阿里云百炼 API 参考: `preserve_thinking`（boolean，可选，默认 false；
// 部分新模型默认 true）—— "是否将对话历史中 assistant 消息的 reasoning_content
// 拼接至模型输入……若历史消息中不包含 reasoning_content，开启此参数不会报错……"
// "开启后，历史对话中的 reasoning_content 会计入输入 Token 数量并计费。"
// - Anthropic《Extended thinking》: 回传的 thinking 块必须携带 `signature` 且**不得修改**
// （服务端用密码学签名校验真伪，缺失即被拒）。
//
// ★ 本文件是**纯声明**：只有数据与纯函数，没有 I/O、没有全局可变状态、不读环境变量。
// 落地（把字段真正写进请求体）属于后续步骤，且必须先读本文件的裁决，
// 不允许各适配器再各判一次 —— 否则就是 「同一事实跨入口不同源」那一族事故。
package ai

import "strings"

// ============================================================
// 一、回传形态（三态）
// ============================================================

// ThinkingReplayMode 声明**某个模型面**回传历史思考的正确形态。
//
// 三态而非布尔，与 `ThinkingMode` 同理："不回传"有三种截然不同的原因
// （本面不需要 / 本面禁止 / 本面可选），把它们压成一个 false 就无法区分
// "安全地不做" 与 "正在违规"。
type ThinkingReplayMode string

const (
	// ThinkingReplayNone 不回传。
	// 两种情形都归这里，靠 ThinkingReplayHardFaceFor 区分：
	// · 本面没有回传通道（如 OpenAI Chat Completions 的 gpt/glm 系列）；
	// · 本面**禁止**客户端回传（如 Anthropic 无签名位 ⇒ 回传必 400）。
	ThinkingReplayNone ThinkingReplayMode = "none"

	// ThinkingReplayRequired 必须回传。
	// 不回传 ⇒ 服务端**硬失败**（HTTP 4xx），不是掉质量。
	// 当前唯一已知面 = 携带 `tools` 的 DeepSeek 请求。
	ThinkingReplayRequired ThinkingReplayMode = "required"

	// ThinkingReplayOptIn 可选回传：由开关（SwitchField）控制。
	// 不回传**不报错**，但会掉质量；且回传会计入输入 Token 计费 ⇒ 做成显式开关。
	ThinkingReplayOptIn ThinkingReplayMode = "opt_in"
)

// ThinkingReplayTrigger 声明"什么时候需要回传"，即判据是常量还是依赖本请求形态。
type ThinkingReplayTrigger string

const (
	// ThinkingReplayAlways 与本请求形态无关（恒成立的约束）。
	ThinkingReplayAlways ThinkingReplayTrigger = "always"

	// ThinkingReplayWithTools 仅当**本次请求携带 tools 参数**时才需要回传。
	// ★ "本次请求带 tools" 是**会话级**判定的触发器：一旦会话里用过工具，
	// 后续所有请求都算（DeepSeek 文档明确"即使该轮模型未实际进行工具调用"）。
	ThinkingReplayWithTools ThinkingReplayTrigger = "with_tools"
)

// ThinkingReplayPlan 是**声明式**回传计划：某个模型面回传历史思考的正确形态。
//
// 它只描述"事实"，不描述"本请求到底做不做" —— 后者是后续步骤
// （读本计划 + 会话是否用过工具 + 用户档位）才产生的结论。
type ThinkingReplayPlan struct {
	// Mode 回传形态（none / required / opt_in）。
	Mode ThinkingReplayMode

	// Trigger 何时需要回传（always / with_tools）。
	Trigger ThinkingReplayTrigger

	// CarrierField 回传载体：**历史 assistant 消息上**的字段名（如 "reasoning_content"）。
	// 结构不变式：Mode == none ⇔ CarrierField == ""（无通道就无载体）。
	CarrierField string

	// SwitchField 控制回传的请求参数名（如 "preserve_thinking"）。
	// 空 = 本面无开关（Mode 为 required / none 时通常为空）。
	SwitchField string

	// SwitchDefaultOn 开关在**服务端**的默认值（SwitchField 为空时无意义，恒 false）。
	// 它决定"什么都不做"时会不会悄悄带回历史思考并计费 —— 是显式注入的必要性来源。
	SwitchDefaultOn bool

	// Basis 依据（官方文档要点摘要），供人工复核与回执引用；**不参与任何判定**。
	Basis string
}

// ============================================================
// 二、模型面 → 回传计划（声明表）
// ============================================================

// thinkingReplayPlanTable 是**模型名片段 → 回传计划**的声明表。
//
// 匹配方式与 `native_search.go` / `config.LookupModelCapabilities` 同款：
// 小写 + **包含匹配**、**最长片段优先**。用包含而非前缀，是因为模型名普遍带
// 版本/网关前缀（`deepseek-v4-pro`、`qwen3.7-plus-2026-05-26`、`us.anthropic.claude-...`），
// 按前缀写很难写对。
//
// ★ 表里只放**有官方依据**的面；未命中的模型一律 fail-safe 为 ThinkingReplayNone
// （未知面不回传 —— 因为未知面的错误代价从"掉精度"到"400"都有，保守值最安全）。
var thinkingReplayPlanTable = []struct {
	pattern string
	plan    ThinkingReplayPlan
}{
	// --- DeepSeek：硬校验面 ---
	//
	// 回传载体 = assistant 消息上的 `reasoning_content`（与 content 同级）。
	// 注意两个易错点（官方样例反推）：
	// ① 空字符串会被部分模型判为非法 ⇒ 占位需非空；
	// ② 不得截断/摘要后回传，必须原样。
	{
		pattern: "deepseek",
		plan: ThinkingReplayPlan{
			Mode:         ThinkingReplayRequired,
			Trigger:      ThinkingReplayWithTools,
			CarrierField: "reasoning_content",
			Basis: "DeepSeek 官方：携带 tools 的请求，后续所有请求必须完整回传 " +
				"reasoning_content（含未调工具的轮次），否则 HTTP 400。",
		},
	},

	// --- 百炼 / Qwen：可选开关面 ---
	//
	// ★ 顺序敏感：`qwen3.8-max` 比 `qwen` 长 ⇒ 由"最长片段优先"保证命中更具体的一条。
	// （两条当前计划相同，但分列是为了让"新模型的开关默认值变了"这类信息有地方落。）
	{
		pattern: "qwen3.8",
		plan: ThinkingReplayPlan{
			Mode:            ThinkingReplayOptIn,
			Trigger:         ThinkingReplayAlways,
			CarrierField:    "reasoning_content",
			SwitchField:     "preserve_thinking",
			SwitchDefaultOn: true,
			Basis: "阿里云百炼：preserve_thinking 默认 **true**（3.8-max/flash），" +
				"须完整回传历史 reasoning_content，且**不得**拼进 content 字段。",
		},
	},
	{
		pattern: "qwen",
		plan: ThinkingReplayPlan{
			Mode:            ThinkingReplayOptIn,
			Trigger:         ThinkingReplayAlways,
			CarrierField:    "reasoning_content",
			SwitchField:     "preserve_thinking",
			SwitchDefaultOn: false,
			Basis: "阿里云百炼：preserve_thinking 默认 false；不回传不报错但掉精度，" +
				"开启后历史 reasoning_content 计入输入 Token 并计费。",
		},
	},

	// --- Anthropic：签名面（本仓当前**没有**回传能力） ---
	//
	// CarrierField 留空是**有意的**：本仓 `ContentBlock` 只有纯文本，没有 signature 位，
	// 回传一个无签名的 thinking 块必被服务端拒绝。所以正确形态是"不回传"，
	// 而这条是**硬约束**（判错 = 400），见下文 thinkingReplayHardFaces。
	{
		pattern: "claude",
		plan: ThinkingReplayPlan{
			Mode:    ThinkingReplayNone,
			Trigger: ThinkingReplayAlways,
			Basis: "Anthropic：回传的 thinking 块必须带 signature 且不得修改；" +
				"本仓 ContentBlock 无签名位 ⇒ 回传必 400，故必须不回传（待补数据模型后可另议）。",
		},
	},

	// --- OpenAI 兼容 / GLM：无回传通道 ---
	//
	// Chat Completions 协议没有"历史思考"这个字段（`reasoning_effort` 是**本轮**的
	// 推理强度参数，不是历史载体）⇒ 无载体、无开关。
	{
		pattern: "gpt",
		plan: ThinkingReplayPlan{
			Mode:    ThinkingReplayNone,
			Trigger: ThinkingReplayAlways,
			Basis:   "OpenAI Chat Completions 无历史思考回传字段（reasoning_effort 是本轮参数）。",
		},
	},
	{
		pattern: "glm",
		plan: ThinkingReplayPlan{
			Mode:    ThinkingReplayNone,
			Trigger: ThinkingReplayAlways,
			Basis:   "GLM 走 OpenAI 兼容协议，无历史思考回传字段。",
		},
	},
}

// ThinkingReplayPlanFor 返回该模型面回传历史思考的**唯一判定**。
//
// 匹配规则：模型名小写后做**包含匹配**，**最长片段优先**；未命中 ⇒ ThinkingReplayNone。
//
// ★ 纯函数、不缓存：与 `NativeSearchEnabled` 同理，切模型后结论必须立刻跟着变。
func ThinkingReplayPlanFor(model string) ThinkingReplayPlan {
	lower := strings.ToLower(strings.TrimSpace(model))

	best := ThinkingReplayPlan{ // 兜底：未知面 fail-safe
		Mode:    ThinkingReplayNone,
		Trigger: ThinkingReplayAlways,
		Basis:   "未知模型面：fail-safe 不回传（保守值）。",
	}
	bestLen := 0
	for _, entry := range thinkingReplayPlanTable {
		if len(entry.pattern) <= bestLen {
			continue
		}
		if strings.Contains(lower, entry.pattern) {
			best = entry.plan
			bestLen = len(entry.pattern)
		}
	}
	return best
}

// ============================================================
// 三、已知硬约束面白名单
// ============================================================

// ThinkingReplayHardFace 描述一个"**判错 = 硬失败（HTTP 4xx）**"的模型面，
// 并给出该面上**必须**采取的动作。
//
// 存在的意义：`ThinkingReplayPlanFor` 的兜底是 fail-safe 的 `none`，
// 但有些面"静默跳过"本身就是错的 —— 那些面必须走白名单、不接受兜底。
// 后续接线步骤 **必须** 先查本白名单，再决定能不能用通用规则。
type ThinkingReplayHardFace struct {
	// Pattern 匹配模型名的片段（小写，包含匹配）。
	Pattern string

	// Required 该面**必须**采取的形态。这是白名单的核心：
	// 它必须与 ThinkingReplayPlanFor(model).Mode 一致（有守卫测试钉住）。
	Required ThinkingReplayMode

	// Trigger 何时强制（always / with_tools）。
	Trigger ThinkingReplayTrigger

	// Reason 为什么是硬约束（给回执与错误提示引用）。
	Reason string
}

// thinkingReplayHardFaces 是已知硬约束面清单。**只增不减地维护，且每条都要有官方依据。**
//
// 两个方向都算"硬"：
// - Required = required：**不回传** ⇒ 400（DeepSeek + tools）；
// - Required = none ：**回传** ⇒ 400（Anthropic 无签名位）。
var thinkingReplayHardFaces = []ThinkingReplayHardFace{
	{
		Pattern:  "deepseek",
		Required: ThinkingReplayRequired,
		Trigger:  ThinkingReplayWithTools,
		Reason: "DeepSeek 官方：带 tools 的请求，后续所有请求必须完整回传 reasoning_content，" +
			"否则 HTTP 400（错误原文：The \"reasoning_content\" in the thinking mode " +
			"must be passed back to the API）。",
	},
	{
		Pattern:  "claude",
		Required: ThinkingReplayNone,
		Trigger:  ThinkingReplayAlways,
		Reason: "Anthropic 官方：thinking 块回传必须带 signature 且不得修改；" +
			"本仓 ContentBlock 无签名位 ⇒ 回传必 400，故必须不回传。",
	},
}

// ThinkingReplayIsHardFace 判断该模型是否落在**已知硬约束面**上（命中即不接受兜底）。
//
// 匹配方式与计划表一致：小写 + 包含 + 最长片段优先（这里只关心布尔结论，
// 故最长匹配只用于在多个命中里选出**最具体**的一条返回）。
func ThinkingReplayIsHardFace(model string) (ThinkingReplayHardFace, bool) {
	lower := strings.ToLower(strings.TrimSpace(model))

	var found ThinkingReplayHardFace
	foundLen := 0
	for _, face := range thinkingReplayHardFaces {
		if len(face.Pattern) <= foundLen {
			continue
		}
		if strings.Contains(lower, face.Pattern) {
			found = face
			foundLen = len(face.Pattern)
		}
	}
	return found, foundLen > 0
}

// ============================================================
// 四、本请求裁决（读声明表 + 请求形态 ⇒ 唯一结论）
// ============================================================

// ThinkingReplayReason 是回传裁决的**机器可读**理由（供回执 / 日志 / 测试断言）。
//
// 用枚举而非自由文本：「没回传」有六种截然不同的原因，散文无法被判别器钉住，
// 也无法在回执里说实话（用户看到的必须是"为什么"，不是"什么都没发生"）。
type ThinkingReplayReason string

const (
	// ThinkingReplayApplied 已按声明表把载体写进请求。
	ThinkingReplayApplied ThinkingReplayReason = "applied"
	// ThinkingReplayNoThinking 本会话没有可回传的思考（模型未产出 / 用户 `/thinkback clear`）。
	ThinkingReplayNoThinking ThinkingReplayReason = "no_thinking"
	// ThinkingReplayFaceForbids 本面**禁止**回传（Anthropic 无签名位；gpt/glm 无通道）。
	ThinkingReplayFaceForbids ThinkingReplayReason = "face_forbids"
	// ThinkingReplayToolsNotInPlay 本面仅在"请求携带 tools"时要求回传，而本请求不带。
	ThinkingReplayToolsNotInPlay ThinkingReplayReason = "tools_not_in_play"
	// ThinkingReplayOptInNotRequested 本面为**可选**回传，而用户**没有要求思考**（档位非 On）
	// ⇒ 按设计不启用可选回传。★ 这是**正确终态**，不是待办。
	//
	// 依据：可选面的回传要计入输入 Token 并计费（百炼 `preserve_thinking` 官方原文），
	// 而「历史思考值不值得进上下文」的判断就是用户的思考档位 —— 没开思考的会话
	// 不该被静默加钱。故本 reason 永远不该被后续步骤改写成 Apply=true。
	ThinkingReplayOptInNotRequested ThinkingReplayReason = "opt_in_not_requested"
	// ThinkingReplayCapabilityUnconfirmed 本面为**可选**回传、用户**已要求**思考（档位 On），
	// 但**能力未确认支持**（`ThinkingSupportFor(model) != ThinkingCapSupported`）⇒ 不发开关。
	//
	// ★ 这就是 A1 门（下发侧 × 回传侧**同源**）：`preserve_thinking` 与 `enable_thinking`
	// 是同一件事（对同一模型面：认不认 thinking）的两处表现，必须由**同一个**
	// `ThinkingSupportFor` 决定。若用户把某模型声明成 unsupported —— 下发侧不发
	// `enable_thinking`，回传侧也**不得**照发 `preserve_thinking`，否则就是
	// 「同一事实两处判」。未知能力同理 fail-safe 不发（宁可掉精度，不可误发）。
	ThinkingReplayCapabilityUnconfirmed ThinkingReplayReason = "capability_unconfirmed"
	// ThinkingReplayNoCarrierField 声明表内部矛盾（required/opt_in 却没有载体字段）—— 不应发生。
	ThinkingReplayNoCarrierField ThinkingReplayReason = "no_carrier_field"
)

// ThinkingReplayDecision 是「**本请求**到底怎么处置历史思考」的唯一结论。
//
// ★ 各适配器**不得**自行 `switch` 模型名或做 `strings.Contains` —— 那是
// "同一语义多处实现"（/ 族：三处下发判据就是这么坏掉的）。
// 它们只能读本结论，再把结论**渲染**成各自协议的形态（字段名之差属协议差异）。
type ThinkingReplayDecision struct {
	// Apply 是否把 Text 写进历史 assistant 消息上的 Field 字段。
	Apply bool
	// Field 写入位置：assistant 消息上、与 content **同级**的字段名（如 reasoning_content）。
	// Apply 为 true 时非空。
	Field string
	// Text 写入内容：多段思考按发生顺序**原样**拼接（不截断、不摘要、不改写）。
	Text string
	// SwitchField 写入请求体**顶层**的开关字段名（如 "preserve_thinking"）；空 = 本裁决不带开关。
	//
	// ★ 调用点**不得**自己写这个字段名 —— 它必须来自声明表（Plan.SwitchField），
	// 否则又成了"同一事实多处实现"（族）。
	//
	// 结构不变式：SwitchField != "" ⇒ Apply == true（不发载体却发开关 = 谎报）。
	SwitchField string
	// SwitchOn 开关要写的布尔值。当前恒为 true：
	// 只有"用户要求思考"（OptIn 面 + On 档）才带开关，而该开关是**开**语义。
	SwitchOn bool
	// Reason 判定理由（见 ThinkingReplayReason）。
	Reason ThinkingReplayReason
}

// ResolveThinkingReplay 是"历史思考要不要带回请求"的**唯一裁决**。
//
// 输入五个事实，输出一个结论：
// - model ：模型名（判据只能来自模型名，见文件头 —— 不能按 provider 配置判）；
// - hasTools ：本请求**实际会携带** tools 参数（注意：结构化输出会跳过 tools，
// 调用方必须传"真的会发出去"的那个值，而不是 `len(req.Tools) > 0`）；
// - mode ：**用户本轮的思考档位**（`ThinkingConfig.Resolve()`，三态）。
// ★ 必须取自 `req.Thinking` —— 与"下发 enable_thinking"用的是**同一个来源**，
// 调用方不得另算一份（同源； 那族事故就是这么来的）；
// - thinkingTexts：历史 assistant 消息里的思考文本，按发生顺序；
// - capSupported：`ThinkingSupportFor(model) == ThinkingCapSupported` 的结果
// （A1 门）。★ 调用点必须传**同一个** `ThinkingSupportFor` 的结论 ——
// 它就是"下发 enable_thinking"所用的那一个（唯一权威入口）。把结论**作为输入**
// 传进来，而不是让本函数自己去读全局声明，是为了让本裁决保持**纯函数**
// （可全组合遍历、可 overlay 注入反例）；而"同一事实同源"由调用点只取一次保证。
//
// # 能力门（capSupported）为什么只作用于 opt_in 面
//
// `preserve_thinking` 与 `enable_thinking` 是同一个人（"这个模型面认不认 thinking"）
// 的两处表现 ⇒ 必须同源。但 **required 面（DeepSeek）不受它约束**：
// "带 tools 的请求必须回传 reasoning_content"是协议硬约束，与"模型认不认某个客户端开关"
// 无关 —— 回传的是厂商自己产出的 `reasoning_content`，DeepSeek 一定认。
// ⇒ 此处**故意**只让 opt_in 面读 capSupported；required 面照旧回传，
// 这条"能力门不得误伤 required 面"有独立判据钉住（thinking_replay_test.go）。
//
// # 档位这一维在两类面上作用**相反**（最容易写错，故写明）
//
// - **required 面（DeepSeek）：档位无关。**"带 tools 的请求必须回传 reasoning_content"
// 是协议硬约束，与用户本轮想不想思考无关 —— `/think off` 说的是"**本轮**别思考"，
// 它无法推翻"历史里已有的思考必须原样送回"（官方明写：即使该轮未实际调工具也算）。
// ⇒ 本面上档位**不参与判定**，绝不是"可以少传"的理由。
//
// - **opt_in 面（百炼）：档位决定。**`preserve_thinking` 问的正是"历史思考要不要
// 算进模型输入"，而这恰好就是用户思考档位要回答的问题；且它会计入输入 Token 计费
// ⇒ 没要求思考的会话不该被静默加钱。
//
// ★ 顺带记住：这两个开关的**服务端默认值相反**，因而注入形态也相反 ——
// `enable_thinking` 默认开 ⇒ `off` 必须**显式发 false**（否则关不掉）；
// `preserve_thinking` 默认 false（部分新模型 true）⇒ 只有 `on` 时**发 true**，
// 其余档位**不发该字段**（显式发 false 会改掉那些默认 true 的模型本来在做的事，
// 属另一种"静默改行为"）。这就是本函数只对 opt_in 面返回 `SwitchField`、
// 且 `SwitchOn` 恒为 true 的原因。
//
// 规则与依据**全部来自 thinkingReplayPlanTable** —— 本函数不新增任何判据，
// 只把声明表与请求形态求交。新增模型面 = 改表，不是改这里。
func ResolveThinkingReplay(model string, hasTools bool, mode ThinkingMode, thinkingTexts []string, capSupported bool) ThinkingReplayDecision {
	plan := ThinkingReplayPlanFor(model)
	text := joinThinkingTexts(thinkingTexts)

	switch plan.Mode {
	case ThinkingReplayRequired:
		if plan.Trigger == ThinkingReplayWithTools && !hasTools {
			return ThinkingReplayDecision{Reason: ThinkingReplayToolsNotInPlay}
		}
		if text == "" {
			return ThinkingReplayDecision{Reason: ThinkingReplayNoThinking}
		}
		if plan.CarrierField == "" {
			// 声明表内部矛盾（required 却无载体）：fail-safe 不回传，
			// 但用独立 Reason 让守卫测试能钉住"表被改坏"这种情形。
			return ThinkingReplayDecision{Reason: ThinkingReplayNoCarrierField}
		}
		return ThinkingReplayDecision{
			Apply:  true,
			Field:  plan.CarrierField,
			Text:   text,
			Reason: ThinkingReplayApplied,
		}
	case ThinkingReplayOptIn:
		// 可选面：载体（reasoning_content）+ 顶层开关（preserve_thinking）**一起**落地。
		// 分三步答，顺序即优先级（先答"有没有东西可传"，再答"用户要不要"，最后答"能力认不认"）：
		if text == "" {
			return ThinkingReplayDecision{Reason: ThinkingReplayNoThinking}
		}
		if mode != ThinkingOn {
			// 用户没要求思考 ⇒ **终态**（设计上就不启用可选回传），不是"待办"。
			// 依据：可选面的回传要计入输入 Token 并计费（百炼官方原文），
			// 没开思考的会话不该被静默加钱。
			return ThinkingReplayDecision{Reason: ThinkingReplayOptInNotRequested}
		}
		if !capSupported {
			// A1 门：能力未确认支持 ⇒ 不发开关（与下发 enable_thinking 同源）。
			return ThinkingReplayDecision{Reason: ThinkingReplayCapabilityUnconfirmed}
		}
		if plan.CarrierField == "" {
			// 声明表内部矛盾（opt_in 却无载体）：不应发生，fail-safe 不回传。
			return ThinkingReplayDecision{Reason: ThinkingReplayNoCarrierField}
		}
		return ThinkingReplayDecision{
			Apply:       true,
			Field:       plan.CarrierField,
			Text:        text,
			SwitchField: plan.SwitchField,
			SwitchOn:    true,
			Reason:      ThinkingReplayApplied,
		}
	default: // ThinkingReplayNone
		return ThinkingReplayDecision{Reason: ThinkingReplayFaceForbids}
	}
}

// joinThinkingTexts 按序拼接多段思考，跳过空白段；全为空白则返回 ""。
//
// 拼接而非只取第一段：一次工具往返可能产生多段思考（interleaved thinking 下尤甚），
// 只取首段 = 回传不完整 = 服务端校验仍可能失败。空白段须剔除，
// 因为"空字符串"在部分厂商侧被判为非法（见声明表注释）。
func joinThinkingTexts(texts []string) string {
	var sb strings.Builder
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		sb.WriteString(t)
	}
	return sb.String()
}
