// Package config — model_registry.go 模型能力注册表（候选 B）。
// 对标同类 CLI MODEL_REGISTRY（@2026-03-31快照，内部研究）+
// a comparable CLI ModelInfo（@https://github.com/openai/a comparable CLI/blob/main/@2026-07-21）。
//
// 本文件提供配置化的模型能力注册表，替代 effort.go 中的硬编码字符串匹配。
// 查询顺序：用户注册表（settings.json model_capabilities）→ 内置注册表 → fallback 字符串匹配。
// 匹配规则：模型名小写后，按 key 长度降序遍历，最长前缀匹配优先
// （落实审查建议优化 2：避免 "sonnet-4" 误匹配 "sonnet-40"）。
//
// ★★ B5「三份模型清单」定角色 —— 本文件**不是**能力判定的 SSOT：
//
//	本文件（config/model_registry.go）→ 只服务两个消费者：
//
// ① SupportsEffort / SupportsMaxEffort（effort.go，给 /effort 用）
// ② SupportsNativeSearch → main.go 桥接进 internal/ai 的"显式启用"表
//
//	internal/ai/capability.go → **能力判定 SSOT**（thinking / native search / cache）
//	internal/ai/models.go → **定价 / 窗口 / 输出**的兜底表（FillDefaults 用）
//
// 本文件此前还有 `SupportsThinking` / `SupportsCache` 两个字段，但全仓**零读取**
// （wiring_test.go 早已把它登记为"孤岛字段"）⇒ B5 删除。它们与 internal/ai 的
// 同名能力位各写各的，属 「同一事实多源」——删掉 = 消除"看起来能配、其实
// 不生效"的假象（用户写这两个键的**行为前后一致**：都不生效）。
package config

import (
	"sort"
	"strings"
	"sync"
)

// ModelCapabilities 模型能力声明（候选 B）。
// 用户可通过 settings.json 的 model_capabilities 字段覆盖内置注册表。
type ModelCapabilities struct {
	// SupportsEffort 模型是否支持 effort 参数（low/medium/high/max）
	SupportsEffort bool `json:"supports_effort"`
	// SupportsMaxEffort 模型是否支持 max effort（仅高端模型如 opus-4）
	SupportsMaxEffort bool `json:"supports_max_effort"`
	// ★ B5已删两个**零读取**字段：`SupportsThinking` / `SupportsCache`。
	// 它们名为"能力位"，但 internal/ai 从不读它们、本包也没有第二处消费者
	// ⇒ 只是给用户一个"配了会生效"的错觉。thinking 能力的唯一入口 =
	// `internal/ai` 的声明层（`/model add|edit <m> --supports-thinking`）；
	// cache 能力由 `internal/ai` 的能力表 + 运行期探测决定，无用户覆盖入口。
	// settings.json 里若仍写着这两个键，会被**忽略并打印一条 log**（见 config.go）。
	// SupportsNativeSearch 模型是否支持 **provider 侧原生检索**（L1：注入 enable_search，
	// 由网关在服务端检索 ⇒ 复用模型 Key，零额外凭据）。对应 `internal/ai` 的同名能力位。
	//
	// ★ 语义是**单向启用**：这里写 true ⇒ 允许对该模型注入 enable_search；
	// 不写/false ⇒ 不表态（既不禁用也不启用），是否注入仍由内置能力位与全局开关决定。
	// 关闭请用全局开关（WHALE_NATIVE_SEARCH 不设即为关）——避免"用户只写了 supports_effort，
	// 却因为零值把检索一起关掉"这类缺省副作用。
	SupportsNativeSearch bool `json:"supports_native_search"`
}

// builtinModelRegistry 内置模型能力注册表（编译时初始化， 候选 B）。
// key 为模型名片段（小写），LookupModelCapabilities 使用 strings.Contains 最长匹配优先。
// 对标同类 CLI MODEL_REGISTRY（@2026-03-31快照）。
//
// 设计原则（向后兼容）：本注册表初始行为必须与 effort.go 原 fallback 字符串匹配完全一致，
// 即：含 "opus"/"sonnet-4"/"gpt-4"/"deepseek-reasoner" 的模型 SupportsEffort=true。
// 用户可通过 settings.json model_capabilities 覆盖（用户优先）。
// 注意：不单独声明 "gpt-4o" 条目，让 "gpt-4o" 通过 Contains 匹配到 "gpt-4"
// （与原 fallback `strings.Contains(m, "gpt-4")` 行为一致，避免破坏向后兼容）。
var builtinModelRegistry = map[string]ModelCapabilities{
	"opus-4":            {SupportsEffort: true, SupportsMaxEffort: true},
	"opus":              {SupportsEffort: true, SupportsMaxEffort: false},
	"sonnet-4":          {SupportsEffort: true, SupportsMaxEffort: false},
	"sonnet":            {SupportsEffort: false, SupportsMaxEffort: false},
	"gpt-4":             {SupportsEffort: true, SupportsMaxEffort: false},
	"deepseek-reasoner": {SupportsEffort: true, SupportsMaxEffort: false},
}

// 用户注册表相关变量（候选 B）。
// userRegistry 为 nil 时使用内置注册表（向后兼容）。
var (
	userRegistryMu sync.RWMutex
	userRegistry   map[string]ModelCapabilities
)

// RegisterModelCapabilities 注册用户自定义模型能力（候选 B）。
// 用户通过 settings.json 的 model_capabilities 字段调用。
// 覆盖内置注册表（用户优先）。
// 线程安全：通过 userRegistryMu 保护。
func RegisterModelCapabilities(modelPrefix string, caps ModelCapabilities) {
	userRegistryMu.Lock()
	defer userRegistryMu.Unlock()
	if userRegistry == nil {
		userRegistry = make(map[string]ModelCapabilities)
	}
	userRegistry[strings.ToLower(modelPrefix)] = caps
}

// RegisterModelCapabilitiesFromMap 批量注册用户自定义模型能力（候选 B）。
// 从 settings.json 解析的 map 注入，覆盖内置注册表。
// 线程安全：通过 userRegistryMu 保护。
func RegisterModelCapabilitiesFromMap(m map[string]ModelCapabilities) {
	userRegistryMu.Lock()
	defer userRegistryMu.Unlock()
	userRegistry = make(map[string]ModelCapabilities, len(m))
	for k, v := range m {
		userRegistry[strings.ToLower(k)] = v
	}
}

// ClearUserRegistry 清空用户注册表（候选 B，测试用）。
// 线程安全：通过 userRegistryMu 保护。
func ClearUserRegistry() {
	userRegistryMu.Lock()
	defer userRegistryMu.Unlock()
	userRegistry = nil
}

// LookupModelCapabilities 查询模型能力（候选 B）。
// 查询顺序：用户注册表 → 内置注册表 → 返回零值（不支持任何能力）。
// 匹配规则：模型名小写后，按 key 长度降序遍历，最长包含匹配优先
// （落实审查建议优化 2：避免 "sonnet-4" 误匹配 "sonnet-40"）。
// 使用 strings.Contains 而非 HasPrefix，以兼容 "claude-opus-4-xxx" 格式的模型名
// （模型名通常含 provider 前缀如 "claude-"/"gpt-"，HasPrefix 无法匹配）。
// 线程安全：用户注册表读取通过 userRegistryMu 保护。
// 返回 (caps, found)：found=false 表示注册表未命中（调用方可走 fallback）。
func LookupModelCapabilities(modelName string) (ModelCapabilities, bool) {
	m := strings.ToLower(modelName)

	// 1. 用户注册表优先（最长匹配）
	userRegistryMu.RLock()
	userReg := userRegistry
	userRegistryMu.RUnlock()
	if caps, ok := lookupWithLongestContains(userReg, m); ok {
		return caps, true
	}
	// 2. 内置注册表
	if caps, ok := lookupWithLongestContains(builtinModelRegistry, m); ok {
		return caps, true
	}
	// 3. 未命中 → 返回零值 + found=false
	return ModelCapabilities{}, false
}

// lookupWithLongestContains 按 key 长度降序遍历，返回最长包含匹配的结果（候选 B）。
// 使用 strings.Contains 而非 HasPrefix，兼容 "claude-opus-4-xxx" 格式。
// 落实审查建议优化 2：按 key 长度降序匹配，避免短 key 误匹配
// （如 "sonnet-4" 不会优先于 "sonnet-40" 匹配 "sonnet-40-future"）。
func lookupWithLongestContains(registry map[string]ModelCapabilities, modelLower string) (ModelCapabilities, bool) {
	if len(registry) == 0 {
		return ModelCapabilities{}, false
	}
	// 收集所有 key 并按长度降序排序
	keys := make([]string, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})
	// 最长包含匹配
	for _, k := range keys {
		if strings.Contains(modelLower, k) {
			return registry[k], true
		}
	}
	return ModelCapabilities{}, false
}
