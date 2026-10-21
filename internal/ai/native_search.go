// Package ai — L1「原生检索」的**唯一开关与判定入口**（S6.md 落地）。
//
// # 它是什么
//
// L1 = provider 侧原生检索：把 `enable_search: true` 注入**模型请求体**，
// 由网关在**服务端**完成检索并把结果注入 prompt。用户**不需要任何额外凭据**
// —— 复用的就是他**已经配好的那条模型 Key**（这正是用户要的"搜索与主对话共用 key/api"）。
//
// 与 L2 的关系（两条**独立**通道，不要混为一谈）：
//
//	L1（本文件）: 服务端检索，模型侧**无法点名**"必须搜一次"（它不是工具）；
//
// 注入在服务端完成 ⇒ **不进入我们的会话历史** ⇒ 只计费一次、不随轮次重发。
//
//	L2（tools）: 本地 `SEARCH_API_KEY` + `WebSearch` 工具，按次调用、结果**留在历史**里。
//
// 两条可以同时存在：L1 已开且有本地 Key 时，工具面仍保留 `WebSearch`
// （它是模型唯一能"必须搜一次"的手段）；无本地 Key 时工具根本不注册（S3 已落）。
//
// # 地面真值（2026-09-18 探针实测，证据留档 docs/review0909/probe_native_search*）
//
// - **生效位置 = 请求体顶层** `enable_search: true`。`parameters.enable_search`
// 与 `extra_body.enable_search` 在本端点被**静默丢弃**（B3 返回 200 但 prompt_tokens 不变）。
// - **参数确被转发**：非法类型 → 400，报错路径写着 `parameters.enable_search`（V4/B1/B2）。
// - **与 function calling 并存**：E1（tools+flag，该调工具）与 E3（仅 tools）**逐项一致**
// ⇒ 本文档 §5.5 列为"官方未背书、必须先证伪"的一号风险，**实测不成立**。
// - **按需触发**：琐碎问题 + flag 仍 `prompt_tokens=21`（不需要实时信息就不检索、不计费）；
// 触发一次注入 ~4–6K token（D3=6019 / E2=6288）。
// - **无可核验来源**：响应体没有 `search_info`/`citations` ⇒ 拿不到引用链接。
//
// # 三条硬约束（照做，别"顺手优化"）
//
// 1. **默认关**。注入到不支持的端点会**直接 400**（V4 证明该校验是严格的）。
// 默认开 = 拿"整个会话起不来"的风险换便利 ⇒ 必须显式开启。
// 2. **能力位不得由 `provider` 字段判断**。用户实际是 `provider:"openai"`
// + 自定义 `base_url`（私有 MaaS 网关）⇒ 按 provider 判会**直接判错**。
// 判据只能是"模型能力位 + 显式覆盖 + 端点白名单"（第三项 A2 加入：
// 表只对厂商官方端点成立，见 endpoint_capability.go）。
// 3. **开关判定只有这一处**。注入点若各自判一次，就是
// 「同一事实跨入口不同源」那一族事故（本仓 G5–G7 / 同族）。
package ai

import (
	"os"
	"strings"
	"sync"
)

// NativeSearchEnvVar 是 L1 的开关环境变量名。
//
// 取值遵循本仓既有惯例（config.go / output_compressor.go 同款）：
// `1` 或 `true` = 开，其余（含未设置）= 关。
//
// ★ 之所以用环境变量而不是新配置键：裁定「(b) v0.1 只认环境变量、不新增 settings.json 配置键」
// 仍未撤销；且 env 能覆盖到 `~/.whale/.env`（`loadEnvFile` 是先到先得语义）
// ⇒ 「零重建改开关」这条路保持可用。
const NativeSearchEnvVar = "WHALE_NATIVE_SEARCH"

var (
	// nativeSearchOverrideMu 保护 nativeSearchOverride。
	nativeSearchOverrideMu sync.RWMutex

	// nativeSearchOverride 是**显式启用**表（模型名片段 → true，小写，最长匹配优先）。
	//
	// 它**只能用于启用，不能用于关闭**（故是 map 而不是 map[string]bool 的双向表）：
	// · 启用：模型名不像 qwen、但用户知道自己的网关支持 `enable_search`（如自建代理）；
	// · 关闭：用全局开关（`WHALE_NATIVE_SEARCH` 不设即为关），不需要第二套机制；
	// · 端点其实不支持：运行时由 `isNativeSearchUnsupportedError` 自动降级并重试
	// （见 provider.go），不需要用户预先声明。
	// 只做单向，是为了避免「用户只写了 supports_effort，却顺手把检索也关掉了」这类
	// 零值副作用（配置里的零值不是声明，是缺省）。
	nativeSearchOverride = map[string]bool{}
)

// SetNativeSearchModelOverride 由装配层推入"显式启用"的模型名片段（生产调用点唯一 = main.go）。
//
// key 语义与 `config.LookupModelCapabilities` 对齐（小写 + 最长包含匹配），
// 这样用户在 `settings.json` 的 `model_capabilities` 里怎么写，这里就怎么生效，不需要第二套心算。
func SetNativeSearchModelOverride(models []string) {
	nativeSearchOverrideMu.Lock()
	defer nativeSearchOverrideMu.Unlock()
	nativeSearchOverride = make(map[string]bool, len(models))
	for _, m := range models {
		if k := strings.ToLower(strings.TrimSpace(m)); k != "" {
			nativeSearchOverride[k] = true
		}
	}
}

// ResetNativeSearchModelOverride 清空显式启用表。只给测试用
// （每条判据都在干净的默认态上起跑，用例之间不互相污染）。
func ResetNativeSearchModelOverride() {
	nativeSearchOverrideMu.Lock()
	defer nativeSearchOverrideMu.Unlock()
	nativeSearchOverride = map[string]bool{}
}

// NativeSearchEnabledFor 是 L1 的**唯一对外判定入口**：该模型**在这个端点上**
// 此刻是否该注入 `enable_search`（A2 起带端点维度）。
//
// 语义（开关是硬前提，其余按优先级短路）：
//
//	① 全局开关未开（`WHALE_NATIVE_SEARCH` 非 1/true）→ false，不看其他
//	② 被**显式覆盖**表点名 → true（优先于端点判定）
//	③ 端点是厂商官方端点 **且** 能力位 `SupportsNativeSearch` 为真 → true
//	④ 其余（含"能力位为真但端点非官方"）→ false
//
// ★ 优先级：**用户显式覆盖 > 端点降级**（覆盖表示"我知道我接的这扇门支持"，
// 与 thinking 侧的 `--supports-thinking` 声明同级 —— 用户面对的是真实端点，
// 白名单面对的是域名后缀）。
//
// ★ 这是纯函数式的读（不缓存）：`SetModel` 之后结论必须立刻跟着变，
// 否则"切模型后行为不对"会变成只能靠重启解释的现象。
//
// ⚠️ 为什么用 `EnabledFor` 而不是给旧的 `NativeSearchEnabled(model)` 加可选参数：
// 端点**必须**由调用方声明（否则它就成了"忘了传 = 按域名猜"的隐藏缺省，属同一族
// 缺陷）。改名让全部调用点被编译器强制过一遍，比注释可靠。
func NativeSearchEnabledFor(model, baseURL string) bool {
	return nativeSearchEnabledFrom(model, baseURL, GetModelCapability(model), os.Getenv)
}

// nativeSearchEnabledFrom 是 NativeSearchEnabledFor 的可注入形态：
// 判据用固定 getenv 驱动，不依赖进程环境 ⇒ 同一条判据在任何人机器上结论一致
// （否则本包判据会随开发机的 WHALE_NATIVE_SEARCH 而飘，属"环境相关假绿"）。
func nativeSearchEnabledFrom(model, baseURL string, cap ModelCapability, getenv func(string) string) bool {
	if !nativeSearchSwitchOn(getenv) {
		return false
	}
	// 显式覆盖先于端点判定（见函数注释）。
	if nativeSearchOverridden(model) {
		return true
	}
	// A2 端点降级：`enable_search` 是厂商私有字段，对未知端点下发会被直接 400
	// （正文硬约束 ① 的实测依据）。表结论只对官方端点成立 ⇒ 非官方一律不发。
	if !IsTrustedEndpoint(baseURL) {
		return false
	}
	return cap.SupportsNativeSearch
}

// nativeSearchSwitchOn 只回答"用户开了开关没有"，不掺任何模型判断。
func nativeSearchSwitchOn(getenv func(string) string) bool {
	v := strings.ToLower(strings.TrimSpace(getenv(NativeSearchEnvVar)))
	return v == "1" || v == "true"
}

// nativeSearchOverridden 判断模型是否被显式启用表点名（小写 + 最长包含匹配优先）。
//
// 用 `strings.Contains` 而不是 `HasPrefix`：与 `config.LookupModelCapabilities` 同款，
// 因为模型名普遍带版本/前缀（`qwen3.7-flash-2026-07-15`），按前缀写会很难写对。
func nativeSearchOverridden(model string) bool {
	m := strings.ToLower(model)
	nativeSearchOverrideMu.RLock()
	defer nativeSearchOverrideMu.RUnlock()
	best := ""
	for k := range nativeSearchOverride {
		if len(k) > len(best) && strings.Contains(m, k) {
			best = k
		}
	}
	return best != ""
}
