package ai

import (
	"encoding/json"
	"strings"
	"sync"
)

// ============================================================
// C6 冲突解决：AI 模块权威定义缓存相关共享类型
//
// CacheControl / CachingStyle / CacheCapability / CacheCapabilityProbe
// 由 AI 模块权威定义，Context 模块引用，不重复定义。
// ============================================================

// CachingStyle 表示模型的缓存策略风格。
//
// C6 冲突解决：AI 模块权威定义四值枚举。
// 不同模型/提供商使用不同的缓存机制：
//   - None: 不支持缓存
//   - AnthropicEphemeral: claude/qwen（注入 cache_control ephemeral 标记）
//   - OpenAIPrefixMatch: gpt（不注入标记，cached_tokens 自动计费）
//   - PromptCacheKey: deepseek（注入 prompt_cache_key 字段）
type CachingStyle int

const (
	// CachingNone 不支持缓存。
	CachingNone CachingStyle = iota
	// CachingAnthropicEphemeral Anthropic 风格缓存（注入 cache_control ephemeral）。
	CachingAnthropicEphemeral
	// CachingOpenAIPrefixMatch OpenAI 风格缓存（前缀匹配，cached_tokens 自动计费）。
	CachingOpenAIPrefixMatch
	// CachingPromptCacheKey DeepSeek 风格缓存（注入 prompt_cache_key 字段）。
	CachingPromptCacheKey
)

// CacheControl 表示 cache_control 标记（Anthropic 风格）。
//
// C6 冲突解决：AI 模块权威定义，统一为 {type, ttl, scope} 格式。
// Context 模块的 GetCacheControl 返回此类型。
type CacheControl struct {
	Type  string `json:"type"`            // "ephemeral"
	TTL   string `json:"ttl,omitempty"`   // "5m" 或 "1h"
	Scope string `json:"scope,omitempty"` // 默认空（个人用户无 org）
}

// IsEmpty 判断 CacheControl 是否为空（未设置任何字段）。
func (cc CacheControl) IsEmpty() bool {
	return cc.Type == "" && cc.TTL == "" && cc.Scope == ""
}

// ToJSON 将 CacheControl 序列化为 json.RawMessage。
// 返回 nil 表示空值（不应注入到请求体中）。
func (cc CacheControl) ToJSON() json.RawMessage {
	if cc.IsEmpty() {
		return nil
	}
	data, _ := json.Marshal(cc)
	return data
}

// ToMap 将 CacheControl 转为 map[string]interface{}，用于注入到请求体。
// 始终包含 type/ttl/scope 三个键（即使 scope 为空字符串），
// 以满足 Anthropic API 的统一格式 {type, ttl, scope}（P0-6 / FACT-AI-002）。
func (cc CacheControl) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"type":  cc.Type,
		"ttl":   cc.TTL,
		"scope": cc.Scope,
	}
}

// DefaultCacheControl 根据模型缓存能力返回默认的 CacheControl（P0-6）。
// 统一格式：{type:"ephemeral", ttl:"5m"/"1h", scope:""}。
//   - ttl：支持 1h TTL 时用 "1h"，否则 "5m"
//   - scope：空（个人用户无 org scope）
//   - type："ephemeral"（Anthropic 风格）
//
// 当 cap.CachingStyle != CachingAnthropicEphemeral 时返回空值（不注入 cache_control）。
func DefaultCacheControl(cap CacheCapability) CacheControl {
	if cap.CachingStyle != CachingAnthropicEphemeral {
		return CacheControl{}
	}
	ttl := "5m"
	if cap.Supports1hTTL {
		ttl = "1h"
	}
	return CacheControl{
		Type:  "ephemeral",
		TTL:   ttl,
		Scope: "", // 个人用户无 org scope
	}
}

// ============================================================
// CacheCapability / CacheCapabilityProbe
// ============================================================

// CacheCapability describes cache capabilities
// Describes cache-related features supported by a specific model/provider
//
// C6 冲突解决：AI 模块权威定义。
// 新增字段：Supports1hTTL / CachingStyle / SupportsPromptCacheKey。
// 保留旧字段：SupportsCacheEditing / SupportsCacheReference / ProviderName（向后兼容）。
type CacheCapability struct {
	// SupportsCacheControl whether cache_control parameter is supported (Anthropic style)
	SupportsCacheControl bool
	// SupportsCacheEditing whether cache_editing is supported (incremental editing of cached content)
	SupportsCacheEditing bool
	// SupportsCacheReference whether cache_reference is supported (referencing existing cache)
	SupportsCacheReference bool
	// ProviderName is the provider name
	ProviderName string

	// --- C6 新增字段 ---

	// Supports1hTTL 是否支持 1 小时 TTL（仅 Anthropic 部分模型支持）。
	Supports1hTTL bool
	// CachingStyle 缓存策略风格（None/AnthropicEphemeral/OpenAIPrefixMatch/PromptCacheKey）。
	CachingStyle CachingStyle
	// SupportsPromptCacheKey 是否支持 prompt_cache_key 字段（DeepSeek）。
	SupportsPromptCacheKey bool
}

// CacheCapabilityProbe 是缓存能力探测接口。
//
// C6 冲突解决：AI 模块权威定义为接口。
// Context 模块的 GetCacheControl(probe, modelName) 接受此接口类型。
// 默认实现 DefaultCacheCapabilityProbe 满足此接口。
//
// 接口方法：
//
//	Probe(modelName string) CacheCapability — 返回模型的完整缓存能力信息。
type CacheCapabilityProbe interface {
	// Probe 探测指定模型的缓存能力，返回完整 CacheCapability。
	Probe(modelName string) CacheCapability
}

// DefaultCacheCapabilityProbe 是 CacheCapabilityProbe 的默认实现。
// 维护已知模型的缓存能力映射，对未知模型用名称前缀启发式推断。
//
// 历史说明：此类型原名为 CacheCapabilityProbe（结构体），
// C6 冲突解决后拆分为接口 + 默认实现。
type DefaultCacheCapabilityProbe struct {
	// knownCapabilities is the cache capability mapping for known models
	knownCapabilities map[string]CacheCapability
}

// newCacheCapabilityProbe creates a new cache capability probe
// Initialize cache capabilities for known models:
// - qwen series models: do not support cache_control/cache_editing/cache_reference (using DashScope API)
// - claude series models: support cache_control, do not support cache_editing (not yet implemented)
func newCacheCapabilityProbe() *DefaultCacheCapabilityProbe {
	p := &DefaultCacheCapabilityProbe{
		knownCapabilities: make(map[string]CacheCapability),
	}

	// Register qwen series models — using DashScope API, do not support Anthropic-style caching
	qwenModels := []string{
		"qwen-max",
		"qwen-plus",
		"qwen-turbo",
		"qwen-long",
		"qwen2.5-72b-instruct",
		"qwen2.5-32b-instruct",
		"qwen2.5-14b-instruct",
		"qwen2.5-7b-instruct",
		"qwen2.5-3b-instruct",
		"qwen2.5-1.5b-instruct",
		"qwen2.5-0.5b-instruct",
		"qwen3-235b-a22b",
		"qwen3-30b-a3b",
		"qwen3-32b",
		"qwen3-14b",
		"qwen3-8b",
		"qwen3-4b",
		"qwen3-1.7b",
		"qwen3-0.6b",
		"qwen3",
		"qwen3.5-flash",
		"qwen3.5-35b-a3b",
		"qwen3.6-27b",
		"qwen3.6-35b-a3b",
	}
	for _, model := range qwenModels {
		p.knownCapabilities[model] = CacheCapability{
			SupportsCacheControl:   false,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			ProviderName:           "dashscope",
			CachingStyle:           CachingNone,
		}
	}

	// Register claude series models — using Anthropic API, support cache_control
	claudeModels := []string{
		"claude-sonnet-4-20250514",
		"claude-sonnet-4",
		"claude-opus-4-20250514",
		"claude-opus-4",
		"claude-3-5-sonnet-20241022",
		"claude-3-5-sonnet-latest",
		"claude-3-5-haiku-20241022",
		"claude-3-5-haiku-latest",
		"claude-3-opus-20240229",
		"claude-3-haiku-20240307",
	}
	for _, model := range claudeModels {
		p.knownCapabilities[model] = CacheCapability{
			SupportsCacheControl:   true,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			Supports1hTTL:          true, // 方案 B（S3）：claude 支持 1h TTL，长会话不必每 5m 重建缓存
			ProviderName:           "anthropic",
			CachingStyle:           CachingAnthropicEphemeral,
		}
	}

	return p
}

// Probe 探测指定模型的缓存能力（CacheCapabilityProbe 接口实现）。
// 等价于 GetCapability，为接口契约方法。
func (p *DefaultCacheCapabilityProbe) Probe(modelName string) CacheCapability {
	return p.GetCapability(modelName)
}

// ProbeCacheControl probes whether the model supports cache_control
// Looks up known capability mapping by model name, returns false by default for unknown models
func (p *DefaultCacheCapabilityProbe) ProbeCacheControl(model string) bool {
	capability, ok := p.knownCapabilities[model]
	if !ok {
		return p.probeByPattern(model).SupportsCacheControl
	}
	return capability.SupportsCacheControl
}

// ProbeCacheEditing probes whether the model supports cache_editing
// Looks up known capability mapping by model name, returns false by default for unknown models
func (p *DefaultCacheCapabilityProbe) ProbeCacheEditing(model string) bool {
	capability, ok := p.knownCapabilities[model]
	if !ok {
		return p.probeByPattern(model).SupportsCacheEditing
	}
	return capability.SupportsCacheEditing
}

// ProbeCacheReference probes whether the model supports cache_reference
// Looks up known capability mapping by model name, returns false by default for unknown models
func (p *DefaultCacheCapabilityProbe) ProbeCacheReference(model string) bool {
	capability, ok := p.knownCapabilities[model]
	if !ok {
		return p.probeByPattern(model).SupportsCacheReference
	}
	return capability.SupportsCacheReference
}

// GetCapability gets the complete cache capability info for the model
// Looks up known capability mapping by model name, returns default values (all false) for unknown models
func (p *DefaultCacheCapabilityProbe) GetCapability(model string) CacheCapability {
	capability, ok := p.knownCapabilities[model]
	if !ok {
		return p.probeByPattern(model)
	}
	return capability
}

// probeByPattern infers cache capability by model name pattern
// For models not registered in known mapping, uses heuristic inference by name prefix
//
// C6 冲突解决：各 provider 的 CachingStyle 权威定义。
//   - qwen:   CachingNone（DashScope API，不支持 Anthropic 风格缓存）
//   - claude: CachingAnthropicEphemeral（注入 cache_control ephemeral）
//   - gpt:    CachingOpenAIPrefixMatch（前缀匹配，cached_tokens 自动计费，不注入标记）
//   - gemini: CachingNone（暂不支持）
//   - deepseek: CachingPromptCacheKey（注入 prompt_cache_key 字段，不是 cache_control）
func (p *DefaultCacheCapabilityProbe) probeByPattern(model string) CacheCapability {
	lower := strings.ToLower(model)

	// qwen prefix match — using DashScope API
	if strings.HasPrefix(lower, "qwen") {
		return CacheCapability{
			SupportsCacheControl:   false,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			ProviderName:           "dashscope",
			CachingStyle:           CachingNone,
		}
	}

	// claude prefix match — using Anthropic API
	if strings.HasPrefix(lower, "claude") {
		return CacheCapability{
			SupportsCacheControl:   true,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			Supports1hTTL:          true, // 方案 B：claude 前缀匹配也启用 1h TTL
			ProviderName:           "anthropic",
			CachingStyle:           CachingAnthropicEphemeral,
		}
	}

	// gpt prefix match — OpenAI 前缀匹配缓存，不注入 cache_control 标记
	if strings.HasPrefix(lower, "gpt") {
		return CacheCapability{
			SupportsCacheControl:   false,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			ProviderName:           "openai",
			CachingStyle:           CachingOpenAIPrefixMatch,
		}
	}

	// gemini prefix match — 暂不支持缓存
	if strings.HasPrefix(lower, "gemini") {
		return CacheCapability{
			SupportsCacheControl:   false,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			ProviderName:           "google",
			CachingStyle:           CachingNone,
		}
	}

	// deepseek prefix match — C6: 使用 CachingPromptCacheKey（不是 None）
	if strings.HasPrefix(lower, "deepseek") {
		return CacheCapability{
			SupportsCacheControl:   false,
			SupportsCacheEditing:   false,
			SupportsCacheReference: false,
			ProviderName:           "deepseek",
			CachingStyle:           CachingPromptCacheKey,
			SupportsPromptCacheKey: true,
		}
	}

	// Unknown models do not support any cache features by default
	return CacheCapability{
		SupportsCacheControl:   false,
		SupportsCacheEditing:   false,
		SupportsCacheReference: false,
		ProviderName:           "unknown",
		CachingStyle:           CachingNone,
	}
}

// ============================================================
// 全局单例
// ============================================================

// globalCapabilityProbe is the singleton cache capability probe
//
// 内部使用具体类型 *DefaultCacheCapabilityProbe（有全部方法）。
// 外部模块（Context）通过 CacheCapabilityProbe 接口引用（仅 Probe 方法）。
var globalCapabilityProbe *DefaultCacheCapabilityProbe
var globalCapabilityOnce sync.Once

// getGlobalCapabilityProbe returns the singleton cache capability probe
func getGlobalCapabilityProbe() *DefaultCacheCapabilityProbe {
	globalCapabilityOnce.Do(func() {
		globalCapabilityProbe = newCacheCapabilityProbe()
	})
	return globalCapabilityProbe
}
