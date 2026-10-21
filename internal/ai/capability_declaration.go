// Package ai — 模型能力的「声明层」与 thinking 三态（B1/B2，2026-10-05）。
//
// 为什么需要这一层（第一性原理）：
//
//	GetModelCapability 有三条产出路径 —— ① 精确命中静态表（人工考证过）
//	② 前缀启发式推断（**猜**）③ 都没命中（降级）。三条路径都把结果压进
//	一个 bool 的 SupportsThinking，于是「**已知**不支持」（gpt-4o）与
//	「**不知道**」（某个没见过的模型）变成同一个值。
//	下游只能 fail-safe（一律不发），这没错，但**信息丢了**：既没法如实
//	告诉用户「我不知道」，也没法让用户把「我知道」补进来。
//
// 本层做两件事，都不改静态表、不改既有判定：
//
//	B2 三态：把「已知不支持 / 已知支持 / 不知道」区分开（ThinkingCapability）。
//	B1 声明：用户可对具体模型声明能力（/model add --supports-thinking），
//
// **声明优先于任何推断** —— 因为用户面对的是他真实接的那个门，
// 而前缀推断面对的是模型名。
//
// ★ 与 config/model_registry.go 的关系（勿混淆， 类问题）：
// 那边也有一份 `SupportsThinking bool`（config.ModelCapabilities，来自
// settings.json 的 model_capabilities），但 internal/ai **从不读它** ——
// 那是给 effort 用的另一套判定。本文件是 internal/ai 侧的唯一声明入口；
// 两者合并属 B5（三清单收敛），未做。
//
// ★ 「不猜」原则（对齐 C4）：前缀推断**猜出来的 true** 仍按支持处理
// （否则 qwen3/claude 全系列会失去 thinking）；但**猜出来的 false 不可信**
// ⇒ 归入「不知道」而非「不支持」。宁可少给，不可误判为「确定不支持」。
//
// ★ A2补上的第二维：**端点**。判定入口从 `ThinkingSupportFor(model)`
// 变成 `ThinkingSupportFor(model, baseURL)` —— 静态表只在厂商官方端点上成立，
// 非官方端点（聚合网关/自部署）一律降级为 Unknown。理由见 endpoint_capability.go。
package ai

import (
	"strings"
	"sync"
)

// ============================================================
// B2：thinking 支持能力的三态
// ============================================================

// ThinkingCapability 是模型对 thinking 参数支持能力的**三态**判定（B2）。
//
// 零值必须是「不知道」：这样任何未初始化的字段/未登记的声明都自动落到
// 最保守的一档，而不是悄悄变成「支持」或「不支持」。
type ThinkingCapability int

const (
	// ThinkingCapUnknown 零值：**不知道**该模型是否支持 thinking。
	// 语义 = 不下发 enable_thinking（最坏「不生效」，绝不因误发厂商不认的
	// 私有字段而 400）。它不等于「不支持」——区别在于我们是否敢下结论。
	ThinkingCapUnknown ThinkingCapability = iota
	// ThinkingCapSupported 已知或已声明**支持**。
	ThinkingCapSupported
	// ThinkingCapUnsupported 已知或已声明**不支持**。
	ThinkingCapUnsupported
)

// ParseThinkingCapability 解析用户声明的字面量，返回三态与是否解析成功。
//
// 接受的字面量（大小写不敏感、两端空白容忍）：
//
//	支持 → "supported" / "support" / "on" / "true" / "yes"
//	不支持 → "unsupported" / "no" / "off" / "false"
//
// 空串或无法识别 ⇒ (ThinkingCapUnknown, false)：调用方据此拒绝写入
// （不要把「解析失败」静默当成「不知道」，那是把用户的输入错误藏起来）。
func ParseThinkingCapability(s string) (ThinkingCapability, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "supported", "support", "on", "true", "yes":
		return ThinkingCapSupported, true
	case "unsupported", "no", "off", "false":
		return ThinkingCapUnsupported, true
	}
	return ThinkingCapUnknown, false
}

// String 返回稳定的展示/持久化字面量，与 ParseThinkingCapability 互逆
// （往返测试依赖这一条）。
func (t ThinkingCapability) String() string {
	switch t {
	case ThinkingCapSupported:
		return "supported"
	case ThinkingCapUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// ============================================================
// B1：用户声明（覆盖推断）
// ============================================================

// userThinkingDecls 用户对模型 thinking 能力的声明，key 为模型名小写。
// nil 表示无任何声明（向后兼容：行为与引入本层之前完全一致）。
var (
	userThinkingDeclsMu sync.RWMutex
	userThinkingDecls   map[string]ThinkingCapability
)

// RegisterThinkingDeclaration 登记一条用户声明（B1）。
// 传 ThinkingCapUnknown 等于撤销该模型的声明（恢复为推断）。
// 线程安全。
func RegisterThinkingDeclaration(model string, t ThinkingCapability) {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return
	}
	userThinkingDeclsMu.Lock()
	defer userThinkingDeclsMu.Unlock()
	if userThinkingDecls == nil {
		userThinkingDecls = make(map[string]ThinkingCapability)
	}
	if t == ThinkingCapUnknown {
		delete(userThinkingDecls, key)
		return
	}
	userThinkingDecls[key] = t
}

// ClearThinkingDeclarations 清空全部声明（测试用 / 重载配置用）。线程安全。
func ClearThinkingDeclarations() {
	userThinkingDeclsMu.Lock()
	defer userThinkingDeclsMu.Unlock()
	userThinkingDecls = nil
}

// declaredThinking 查询某模型的用户声明（精确匹配，大小写不敏感）。
func declaredThinking(model string) (ThinkingCapability, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	userThinkingDeclsMu.RLock()
	defer userThinkingDeclsMu.RUnlock()
	if userThinkingDecls == nil {
		return ThinkingCapUnknown, false
	}
	t, ok := userThinkingDecls[key]
	return t, ok
}

// ============================================================
// 唯一权威判定
// ============================================================

// ThinkingSupportFor 是「该模型**在这个端点上**是否支持 thinking」的
// **唯一权威判定入口**（B2 + A2）。
//
// 判定顺序（先声明、再看端点、最后才查表）：
//
// 1. 用户声明存在 → 直接用声明（B1：声明优先，哪怕是"不支持"；
// 且**不受端点降级影响** —— 用户面对的是他真实接的那个门）
// 2. 端点**不在**官方白名单 → **Unknown**（A2 端点降级，见下）
// 3. 精确命中静态表 → 表里写的 true/false 都是人工考证的 ⇒ 直接采信
// 4. 前缀推断 → 推断为 true ⇒ Supported；
// 推断为 false ⇒ **Unknown**（猜的否定不可信，
// 典型：没见过的新模型落到某个前缀分支被当成 false）
// 5. 什么都没命中 → Unknown
//
// ★ 为什么端点要参与（A2，2026-10-05）：静态表是**按模型名**、针对**厂商官方端点**
// 考证的。用户 `--base-url` 指向聚合网关 / 自部署时，同一个模型名背后的东西
// 可能完全不同，而 `enable_thinking` 是厂商**私有**字段 ⇒ 保守到"不下结论"。
// 详细理由与白名单口径见 endpoint_capability.go。
//
// ★ 为什么 baseURL 是**必填参数**而不是可选/全局状态：这样"忘记传端点"是
// **编译期**错误（签名即约束），而不是运行期悄悄退回"按模型名猜"。本仓已有
// 一类事故就是"新增注入点忘了走唯一判据"，所以把判据收进**类型**里而不是注释里。
//
// 所有注入点都必须走本函数，不要再直接读 cap.SupportsThinking —— 那样会丢掉
// 「不知道」这一档（：同一事实必须同源）。
func ThinkingSupportFor(modelName, baseURL string) ThinkingCapability {
	if t, ok := declaredThinking(modelName); ok {
		return t
	}
	if !IsTrustedEndpoint(baseURL) {
		return ThinkingCapUnknown
	}
	cap, src := resolveCapabilityWithSource(modelName)
	switch src {
	case capabilitySourceExact:
		if cap.SupportsThinking {
			return ThinkingCapSupported
		}
		return ThinkingCapUnsupported
	case capabilitySourceInferred:
		if cap.SupportsThinking {
			return ThinkingCapSupported
		}
		// 前缀推断给出的否定 = 我们并不知道 ⇒ 不下结论。
		return ThinkingCapUnknown
	default:
		return ThinkingCapUnknown
	}
}
