// Package ai — **端点维度**：模型能力表的适用边界（B4-A2，2026-10-05）。
//
// # 为什么能力判定需要端点这一维（第一性原理）
//
// `modelCapabilityTable`（capability.go）与 `inferCapabilityByPrefix` 都是
// **按模型名**给出结论的，而它们考证/假设的对象是**厂商官方端点**上的行为。
// 但用户可以 `--base-url` 指向任意网关：
//
//	聚合中转（已重命名模型 / 转发到别家）· vLLM 自部署 · 企业内网代理 · 私有 MaaS 域名
//
// 同一个模型名在那些端点上能力可能完全不同 ⇒ **表的结论不成立**。
// 改造前 `baseURL` 在能力判定里**零使用**（capability.go / native_search.go /
// capability_declaration.go 全无命中）⇒ 代码默认"所有端点都等同于官方端点"，
// 于是把厂商**私有**字段（`enable_thinking` / `enable_search` / `cache_control`）
// 发给不认识它们的端点。轻则 400，重则静默改变行为。这就是 A2 要修的根因。
//
// # 判据为什么是布尔而不是"厂商枚举"
//
// 判定只需要回答一个问题：**「按模型名考证的那张表，能不能套用到这个端点」**。
// 厂商身份本身在当前没有任何消费者 ⇒ 不建枚举（与 A1 删零读取字段同一条纪律：
// 加了没人读的东西就是下一轮的技术债）。将来若要按厂商细化策略，再从这张
// 白名单里长出类型也来得及。
//
// # ★ 为什么白名单是"域名后缀"而不是"精确主机"
//
// 官方端点有多个子域，且**本仓默认配置用的就是其中一个子域**：
// `~/.whale/.env` 的 `OPENAI_BASE_URL` 指向
// `llm-a0apmuqj39li7tey.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`（百炼 MaaS 网关，
// 2026-10-05 实测）。若白名单只写 `dashscope.aliyuncs.com`，**默认配置会被判成"非官方"**
// ⇒ `/think on` 静默失效。所以这里按 `aliyuncs.com` 后缀匹配 —— 这是**有意放宽**，不是笔误。
package ai

import (
	"net/url"
	"strings"
)

// officialEndpointDomains 是「厂商官方端点」域名白名单（**后缀**匹配，见文件头）。
//
// 收录标准：该域名由**模型厂商自己**运营，因而"按模型名考证"的能力结论成立。
// 未收录的域名一律按"未知端点"保守处理（不否定其能力，只是**不下结论**）。
//
// ⚠️ 放宽到 `aliyuncs.com` 整域是**有意**的（默认配置走 MaaS 子域，见文件头）。
// 若要收紧，必须同时处理"默认配置会被降级"这一后果。
var officialEndpointDomains = []string{
	"aliyuncs.com",                      // 阿里云：DashScope / 百炼 / 私有 MaaS 网关
	"anthropic.com",                     // Anthropic 官方
	"openai.com",                        // OpenAI 官方
	"generativelanguage.googleapis.com", // Google Gemini 官方
	"deepseek.com",                      // DeepSeek 官方
}

// IsTrustedEndpoint 报告 baseURL 是否指向**厂商官方端点**（白名单命中）。
//
// 返回 false 的三种情形（等价处理 —— 都表示"我们没法拿模型名去套那张表"）：
//
//	聚合网关 / 中转 · 自部署（vLLM / Ollama / 私有域名）· 解析不出主机（空串 / 非法 URL）
//
// ★ 空 baseURL 落在 false，**不是**"乐观地当成缺省端点"。调用方若想表达"用缺省端点"，
// 应先把缺省值解析出来（`defaultBaseURLForProvider`）—— 把"没配"与"官方端点"
// 混为一谈正是本仓反复踩的"缺省值被当成声明"那类坑（见 native_search.go 同一段注释）。
//
// 纯函数、不缓存：与 `ThinkingSupportFor` / `NativeSearchEnabledFor` 同理，
// 端点判定的输入就是参数，没有可以被缓存腐化的隐藏状态。
func IsTrustedEndpoint(baseURL string) bool {
	host := endpointHost(baseURL)
	if host == "" {
		return false
	}
	for _, d := range officialEndpointDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// endpointTrustLabel 返回端点在能力判定里的档位标签，供**展示**用（`/model info`）。
//
// 存在的理由：端点降级是**静默**的（不发参数 = 用户看不出差别），而"悄悄少了一个能力"
// 比"报错"更难查。把档位摆在 `/model info` 里，用户至少有一条可自查的线索。
// ★ 只做展示，**不得**用它做任何判定（判定唯一入口 = IsTrustedEndpoint）。
func endpointTrustLabel(baseURL string) string {
	if IsTrustedEndpoint(baseURL) {
		return "official"
	}
	return "untrusted(capability table not applied)"
}

// endpointHost 取出 URL 的**主机**（小写、去端口、去 userinfo），失败返回 ""。
//
// 容忍没有 scheme 的写法（`api.openai.com/v1`）：配置里手写 base-url 时很常见，
// 若直接 url.Parse 会把整串当成 path ⇒ Host 为空 ⇒ 官方端点被误判成"未知"
// （误判方向是"白丢能力"，正是最不该发生的那种）。
func endpointHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
