// Package ai — ModelCapability 与能力探测
//
// 落地：T1-A M04 Provider 多样性方案（候选 A 原生协议适配器）。
// 本文件定义 ModelCapability struct（内嵌 CacheCapability 7 字段 + 新增 9 字段 = 共 16 字段），
// 由 M04 维护，H10/H12 只读调用，禁止硬编码模型能力（T0 §3 冲突 1 落地）。
//
// 静态表覆盖 5 Provider 已知模型：Anthropic / OpenAI / Gemini / DashScope / DeepSeek（含 GLM 复用 OpenAI）。
// 未知模型降级为最保守值（CachingNone + SupportsThinking=false + ProtocolStyle="openai"）。
package ai

import (
	"strings"
	"sync"
)

// ============================================================
// 协议风格常量（§5.1）
// ============================================================
// 协议风格常量定义各 Provider 的协议族归属，决定 NewLLMClient 工厂创建哪种适配器。
const (
	// ProtocolStyleAnthropic 原生 Anthropic Messages API 协议。
	ProtocolStyleAnthropic = "anthropic"
	// ProtocolStyleOpenAI 原生 OpenAI Chat Completions 协议（GLM/DeepSeek 复用）。
	ProtocolStyleOpenAI = "openai"
	// ProtocolStyleGeminiNative 原生 Gemini generateContent 协议（JSON 数组流，非 SSE）。
	ProtocolStyleGeminiNative = "gemini-native"
	// ProtocolStyleDashScopeNative 原生 DashScope generation 协议（SSE 流式）。
	ProtocolStyleDashScopeNative = "dashscope-native"
)

// Provider 常量扩展（§5.1）：新增 Gemini/DashScope/DeepSeek 3 个 Provider 常量。
const (
	ProviderGemini    = "gemini"
	ProviderDashScope = "dashscope"
	ProviderDeepSeek  = "deepseek"
)

// providerNameUnknown 是「未知模型降级」分支的签名（内嵌 CacheCapability.ProviderName）。
// B2：resolveCapabilityWithSource 靠它区分
// capabilitySourceNone（压根没命中）与 capabilitySourceInferred（命中了前缀）。
// 它**不是** Provider 常量 —— 降级分支的外层 Provider 仍填 openai（协议族）。
const providerNameUnknown = "unknown"

// ============================================================
// ModelCapability struct（§5.2 / T0 §3 冲突 1 落地）
// ============================================================
// ModelCapability 描述单个模型支持的能力，由 M04 维护，H10/H12 只读。
// 共 17 字段（实测）：内嵌 CacheCapability 7 字段（向后兼容，Context 模块继续引用）
// + 显式 10 字段。
//
// ★ B4-A1已从显式字段中删除 5 个**零生产读取**的：
//
//	SupportsToolCall / SupportsJSON / SupportsStreaming / ContextWindow / MaxOutputTokens
//
// ⇒ 现存显式字段 = SupportsThinking / SupportsNativeSearch / Provider /
//
//	ProtocolStyle / PrivateFields（后两者**仅适配器层使用**，适配器层非生产路径）。
//
// SNAPSHOT: 原 CacheCapability struct 7 字段保留为内嵌子集，不破坏现有引用。
type ModelCapability struct {
	CacheCapability // 内嵌原 7 字段（SupportsCacheControl/SupportsCacheEditing/SupportsCacheReference/ProviderName/Supports1hTTL/CachingStyle/SupportsPromptCacheKey）
	// SupportsThinking 是否支持 thinking 字段。
	// claude-3.7+ 与 qwen3 系列支持；gpt/deepseek/gemini-1.5 不支持；qwen-max 不支持（仅 qwen3 系列）。
	SupportsThinking bool
	// SupportsNativeSearch 是否支持 **provider 侧原生检索**（注入 `enable_search`，即 L1）。
	//
	// 语义：**该模型在其原生服务端**支持服务端联网检索（DashScope 通义千问系列）。
	// 与"本会话到底开不开"是两件事 —— 开关判定唯一入口 = ai.NativeSearchEnabled（native_search.go），
	// 本字段只是它读的**能力位之一**。
	//
	// ★ 硬约束：判定**不得**退化为按 `provider` 字段猜。
	// 用户实际是 `provider:"openai"` + 自定义 `base_url`（私有 MaaS 网关）——
	// 按 provider 判会直接判错；按模型名（这里）判才对得上。
	//
	// ★ 为什么 DashScope 全系列都给 true 而不逐个考证：
	// `enable_search` 是通义千问系列的**通用**能力（官方文档按系列描述，不按版本逐个列）。
	// 误判的代价已被两件事兜住：① 全局开关**默认关**（不显式打开就永远不会注入）；
	// ② 端点不支持时**运行时自动降级 + 重试**（isNativeSearchUnsupportedError）。
	// 未列出的模型一律 false（零值）——包括未知模型降级路径，宁可少给不误给。
	SupportsNativeSearch bool
	// ★ B4-A1已删除 5 个**零生产读取**字段：
	// SupportsToolCall / SupportsJSON / SupportsStreaming / ContextWindow / MaxOutputTokens
	// 全仓实测无任何生产读取点（非测试、非适配器层）⇒ 它们是装饰性的：
	// 让人误以为「改表就能改行为」，而实际不参与任何决策。
	// 典型代价：曾以为补 qwen3.7 条目能改上下文窗口，实际预算唯一入口
	// NewBudgetFromConfig(cfg.ContextWindow, cfg) 根本不查本表。
	// ⚠️ ContextWindow/MaxOutputTokens 若真要参与决策，落点是 config 侧
	// （ModelConfig 的同名字段**参与生产预算**，与本表无关，勿混淆）。
	// 定案见 内部工作回执。
	// Provider 归属 Provider 名（anthropic / openai / gemini / dashscope / deepseek）。
	Provider string
	// ProtocolStyle 协议风格（anthropic / openai / gemini-native / dashscope-native）。
	// 决定 NewLLMClient 工厂创建哪种适配器。
	ProtocolStyle string
	// PrivateFields 该 Provider 的私有高级字段名列表（如 ["thinking"] / ["prompt_cache_key"]）。
	// InjectPrivateFields 时按此列表逐个注入；不支持时静默跳过。
	PrivateFields []string
}

// ============================================================
// 静态表 + 动态探测
// ============================================================
// modelCapabilityTable 是已知模型的能力映射表（编译期硬编码，进程生命周期不可变）。
// §5.5 TTL 规则：静态表永久有效，已知模型能力不变。
//
// 表条目来源（§1.3 / 03-实施prompt.md §1.2）：
// - Anthropic: claude-sonnet-4 / claude-opus-4 / claude-3-5-haiku 等
// - OpenAI: gpt-4o / gpt-4o-mini
// - Gemini: gemini-2.0-flash / gemini-1.5-pro
// - DashScope: qwen-max / qwen-plus / qwen3 系列（qwen-max 的 SupportsThinking=false，仅 qwen3 系列为 true）
// - DeepSeek: deepseek-chat / deepseek-reasoner
// - GLM: glm-4-plus（复用 OpenAI 协议，PrivateFields=[]）
var modelCapabilityTable = map[string]ModelCapability{
	// --- Anthropic claude 系列（原生 Anthropic Messages API） ---
	"claude-sonnet-4-20250514": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: true, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-sonnet-4": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: true, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-opus-4-20250514": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: true, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-opus-4": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: true, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-3-5-sonnet-20241022": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: false, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-3-5-sonnet-latest": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: false, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-3-5-haiku-20241022": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: false, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-3-5-haiku-latest": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: false, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-3-opus-20240229": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: false, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	"claude-3-haiku-20240307": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: false, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	// claude-sonnet-4-5 别名（向前兼容现有 cache_capability.go 的前缀匹配路径）
	"claude-sonnet-4-5": {
		CacheCapability: CacheCapability{
			SupportsCacheControl: true, Supports1hTTL: true,
			ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
		},
		SupportsThinking: true, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
		PrivateFields: []string{"thinking"},
	},
	// --- OpenAI gpt 系列（原生 OpenAI Chat Completions API） ---
	"gpt-4o": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
	"gpt-4o-mini": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
	"gpt-4-turbo": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
	"gpt-4": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
	// --- Google Gemini 系列（原生 generateContent API，JSON 数组流） ---
	"gemini-2.0-flash": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderGemini, CachingStyle: CachingNone,
		},
		SupportsThinking: true, Provider: ProviderGemini, ProtocolStyle: ProtocolStyleGeminiNative,
		PrivateFields: []string{"thinkingBudget"},
	},
	"gemini-2.0-flash-thinking-exp": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderGemini, CachingStyle: CachingNone,
		},
		SupportsThinking: true, Provider: ProviderGemini, ProtocolStyle: ProtocolStyleGeminiNative,
		PrivateFields: []string{"thinkingBudget"},
	},
	"gemini-1.5-pro": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderGemini, CachingStyle: CachingNone,
		},
		SupportsThinking: false, Provider: ProviderGemini, ProtocolStyle: ProtocolStyleGeminiNative,
		PrivateFields: []string{"thinkingBudget"},
	},
	"gemini-1.5-flash": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderGemini, CachingStyle: CachingNone,
		},
		SupportsThinking: false, Provider: ProviderGemini, ProtocolStyle: ProtocolStyleGeminiNative,
		PrivateFields: []string{"thinkingBudget"},
	},
	// --- DashScope 通义千问系列（原生 generation API，SSE 流式） ---
	// 优化 8 落地：qwen-max 的 SupportsThinking=false（仅 qwen3 系列支持 enable_thinking）。
	"qwen-max": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     false, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	"qwen-plus": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     false, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	"qwen-turbo": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     false, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	// qwen3 系列支持 enable_thinking（DashScope 官方文档）
	"qwen3-235b-a22b": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     true, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	"qwen3-30b-a3b": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     true, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	"qwen3-32b": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     true, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	"qwen3": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     true, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	"qwen3.5-flash": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDashScope, CachingStyle: CachingNone,
		},
		SupportsNativeSearch: true,
		SupportsThinking:     true, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
		PrivateFields: []string{"enable_thinking", "thinking_budget"},
	},
	// --- DeepSeek 系列（OpenAI 兼容协议 + 私有 prompt_cache_key 字段） ---
	"deepseek-chat": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDeepSeek, CachingStyle: CachingPromptCacheKey,
			SupportsPromptCacheKey: true,
		},
		SupportsThinking: false, Provider: ProviderDeepSeek, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{"prompt_cache_key"},
	},
	"deepseek-reasoner": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderDeepSeek, CachingStyle: CachingPromptCacheKey,
			SupportsPromptCacheKey: true,
		},
		// deepseek-reasoner 内置 reasoning（无需 enable_thinking 字段），不支持用户配置 thinking
		SupportsThinking: false, Provider: ProviderDeepSeek, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{"prompt_cache_key"},
	},
	// --- GLM 智谱系列（OpenAI 兼容协议，无私有字段） ---
	"glm-4-plus": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
	"glm-4": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
	"glm-4-flash": {
		CacheCapability: CacheCapability{
			ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
		},
		SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
		PrivateFields: []string{},
	},
}

// capabilityCacheOnce 确保 modelCapabilityTable 只读访问时的初始化一次性完成。
// 当前静态表为编译期 map，无需 sync.Once 初始化；保留变量供未来扩展（如运行时动态注册）。
var capabilityCacheMu sync.RWMutex

// GetModelCapability 返回指定模型的能力描述（§1.3 / §5.2）。
// 查找顺序：
// 1. 精确匹配静态表（大小写不敏感）
// 2. 名称前缀启发式推断（claude/gpt/gemini/qwen/deepseek/glm）
// 3. 未知模型降级为最保守值（CachingNone + SupportsThinking=false + ProtocolStyle="openai"）
//
// 未知模型降级不 panic，保证调用方安全。
// 返回值为值拷贝，调用方修改不影响静态表。
func GetModelCapability(modelName string) ModelCapability {
	cap, _ := resolveCapabilityWithSource(modelName)
	return cap
}

// capabilitySource 记录这次能力是**从哪条路径**得到的（B2，2026-10-05）。
//
// 为什么必须显式记录：三条路径的**可信度不同** —— 精确表是人工考证的，
// 前缀推断是猜的。只看最终的 bool 无法区分「已知不支持」与「猜成不支持」，
// 于是 thinking 三态无从判定（capability_declaration.go 的 ThinkingSupportFor）。
type capabilitySource int

const (
	// capabilitySourceNone 三条路径都没命中（未知模型降级）。
	capabilitySourceNone capabilitySource = iota
	// capabilitySourceExact 精确命中静态表（人工考证过，可信）。
	capabilitySourceExact
	// capabilitySourceInferred 前缀启发式推断（猜的，否定不可信）。
	capabilitySourceInferred
)

// resolveCapabilityWithSource 是 GetModelCapability 的实现体（保留命中来源）。
// 行为与拆分前**逐字等价**：先精确匹配、再前缀推断、末位降级。
func resolveCapabilityWithSource(modelName string) (ModelCapability, capabilitySource) {
	capabilityCacheMu.RLock()
	defer capabilityCacheMu.RUnlock()
	// 1. 精确匹配（大小写不敏感）
	for name, cap := range modelCapabilityTable {
		if strings.EqualFold(name, modelName) {
			return cloneCapability(cap), capabilitySourceExact
		}
	}
	// 2. 名称前缀启发式推断
	//
	// ★ 注意：inferCapabilityByPrefix 对**任何**输入都会返回一个值 ——
	// 末位还有一条"未知模型降级"分支。所以判断"是否真的命中了某个前缀"
	// 不能靠返回值零值，只能靠那条降级分支的签名。
	// ⚠️ 签名落在**内嵌的** CacheCapability.ProviderName（= providerNameUnknown），
	// 不是外层的 Provider 字段 —— 降级分支的外层 Provider 仍是 openai（协议族），
	// 这里判错会把"未知模型"误算成"命中前缀"，三态就退化了。
	inferred := inferCapabilityByPrefix(modelName)
	if inferred.ProviderName == providerNameUnknown {
		return inferred, capabilitySourceNone
	}
	return inferred, capabilitySourceInferred
}

// cloneCapability 返回 ModelCapability 的深拷贝，避免调用方修改 PrivateFields slice 污染静态表。
// PrivateFields 为 slice（引用类型），必须显式拷贝。
func cloneCapability(cap ModelCapability) ModelCapability {
	copied := cap
	if cap.PrivateFields != nil {
		copied.PrivateFields = append([]string(nil), cap.PrivateFields...)
	}
	return copied
}

// inferCapabilityByPrefix 按模型名前缀启发式推断能力（§1.2 步骤 5）。
// 未知前缀的模型降级为最保守值（CachingNone + SupportsThinking=false + ProtocolStyle="openai"）。
// 复用现有 probeByPattern 的命名约定（claude/gpt/gemini/qwen/deepseek/glm）。
func inferCapabilityByPrefix(modelName string) ModelCapability {
	lower := strings.ToLower(modelName)
	// claude 前缀 — 原生 Anthropic 协议 + thinking 支持（claude-3.7+ 默认 true）
	if strings.HasPrefix(lower, "claude") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				SupportsCacheControl: true, Supports1hTTL: true,
				ProviderName: ProviderAnthropic, CachingStyle: CachingAnthropicEphemeral,
			},
			SupportsThinking: true, Provider: ProviderAnthropic, ProtocolStyle: ProtocolStyleAnthropic,
			PrivateFields: []string{"thinking"},
		}
	}
	// gpt 前缀 — 原生 OpenAI 协议
	if strings.HasPrefix(lower, "gpt") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
			},
			SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
			PrivateFields: []string{},
		}
	}
	// gemini 前缀 — 原生 Gemini generateContent 协议
	if strings.HasPrefix(lower, "gemini") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				ProviderName: ProviderGemini, CachingStyle: CachingNone,
			},
			SupportsThinking: false, Provider: ProviderGemini, ProtocolStyle: ProtocolStyleGeminiNative,
			PrivateFields: []string{"thinkingBudget"},
		}
	}
	// qwen3 前缀（更具体） — 支持 enable_thinking（DashScope 官方文档：仅 qwen3 系列支持）
	// 必须在 qwen 前缀检查之前，否则 qwen3 前缀匹配会被 qwen 前缀吞掉。
	if strings.HasPrefix(lower, "qwen3") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				ProviderName: ProviderDashScope, CachingStyle: CachingNone,
			},
			SupportsNativeSearch: true,
			SupportsThinking:     true, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
			PrivateFields: []string{"enable_thinking", "thinking_budget"},
		}
	}
	// qwen 前缀 — 原生 DashScope 协议
	// 默认 SupportsThinking=false（仅 qwen3 系列明确支持 enable_thinking）
	if strings.HasPrefix(lower, "qwen") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				ProviderName: ProviderDashScope, CachingStyle: CachingNone,
			},
			SupportsNativeSearch: true,
			SupportsThinking:     false, Provider: ProviderDashScope, ProtocolStyle: ProtocolStyleDashScopeNative,
			PrivateFields: []string{"enable_thinking", "thinking_budget"},
		}
	}
	// deepseek 前缀 — OpenAI 兼容协议 + prompt_cache_key 私有字段
	if strings.HasPrefix(lower, "deepseek") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				ProviderName: ProviderDeepSeek, CachingStyle: CachingPromptCacheKey,
				SupportsPromptCacheKey: true,
			},
			SupportsThinking: false, Provider: ProviderDeepSeek, ProtocolStyle: ProtocolStyleOpenAI,
			PrivateFields: []string{"prompt_cache_key"},
		}
	}
	// glm 前缀 — OpenAI 兼容协议（智谱提供 OpenAI 兼容 endpoint）
	if strings.HasPrefix(lower, "glm") {
		return ModelCapability{
			CacheCapability: CacheCapability{
				ProviderName: ProviderOpenAI, CachingStyle: CachingOpenAIPrefixMatch,
			},
			SupportsThinking: false, Provider: ProviderOpenAI, ProtocolStyle: ProtocolStyleOpenAI,
			PrivateFields: []string{},
		}
	}
	// 未知模型降级为最保守值（§5.4 降级策略）
	// CachingNone + SupportsThinking=false + ProtocolStyle="openai"
	return ModelCapability{
		CacheCapability: CacheCapability{
			ProviderName: providerNameUnknown, CachingStyle: CachingNone,
		},
		SupportsThinking: false,
		Provider:         ProviderOpenAI,
		ProtocolStyle:    ProtocolStyleOpenAI,
		PrivateFields:    []string{},
	}
}

// HasPrivateField 判断 ModelCapability 是否包含指定私有字段名。
// 供 InjectPrivateFields 实现检查 cap 是否支持某私有字段（如 "thinking" / "prompt_cache_key"）。
// 大小写敏感匹配（私有字段名为 provider API 字段名，必须精确匹配）。
func (cap ModelCapability) HasPrivateField(name string) bool {
	for _, f := range cap.PrivateFields {
		if f == name {
			return true
		}
	}
	return false
}
