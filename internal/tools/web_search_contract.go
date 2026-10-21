package tools

// ============================================================================
// SEARCH_API_FORMAT 统一契约（B4 裁定落地件，2026-09-18）
// ============================================================================
//
// 裁定（用户 2026-09-18 09:0x）：
// ① 请求形态并入 SEARCH_API_FORMAT —— 请求方法、请求体模板与本契约定义在**同一个结构体**里，
// 不再另起 SEARCH_API_METHOD。理由：后端的请求方式 / 参数结构 / 响应契约是强绑定的，
// 拆成多个配置项会产生"不同步"的 bug。
// ② 鉴权形态**显式配置**，不做 host 自动推断。理由：同一 host 可能有多种鉴权模式，
// 推断会误匹配；显式配置下"写错即拦截"，不会静默鉴权失败（fail-closed）。
//
// 一句话规格：**响应结构合规 ⇒ 放行（不看引擎名）；结构不合规 ⇒ fail-closed。**
//
// ⚠️ 安全边界不在本文件：内网/回环/云元数据地址由 createSSRFSafeTransport 独立把守
// （SSRF 防护与"结构契约校验"是两件事，**不耦合**）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- 配置键

const (
	searchAPIKeyEnv   = "SEARCH_API_KEY"
	searchAPIURLEnv   = "SEARCH_API_URL"
	searchFormatEnv   = "SEARCH_API_FORMAT"
	searchEngineIDEnv = "SEARCH_ENGINE_ID"
)

// maxSearchResults 单次检索返回给模型的条数上限。
// ★ 它是 S4（上限与描述对齐）要引用的具名常量，此前是 web.go 的两处字面量 5。
const maxSearchResults = 5

// autoSearchFormat 是 SEARCH_API_FORMAT 的显式"自动"取值，语义 = 未配置（走 legacy 兼容解析）。
const autoSearchFormat = "auto"

// ---------------------------------------------------------------- 契约结构体

// searchAuthMode 鉴权形态（显式；**不做 host 推断**）。
type searchAuthMode string

const (
	searchAuthNone        searchAuthMode = "none"         // 不鉴权（公网自建实例 / 内网代理）
	searchAuthQueryKey    searchAuthMode = "query-key"    // URL 查询参数 key=<apiKey>
	searchAuthBearer      searchAuthMode = "bearer"       // <Header>: Bearer <apiKey>
	searchAuthHeaderToken searchAuthMode = "header-token" // <Header>: <apiKey>
)

// searchAuth 描述一次请求如何带上凭据。显式配置，不猜。
type searchAuth struct {
	Mode       searchAuthMode
	QueryParam string   // query-key 用：参数名
	Headers    []string // bearer / header-token 用：头部名（可多个）
}

// searchQueryParam 描述一个 URL 查询参数如何取值（仅 GET 契约使用）。
type searchQueryParam struct {
	Name    string
	Literal string // 非空 = 固定值（如 SearXNG 的 format=json）；空 = 取用户查询
}

// searchAPIContract 是一次检索调用的**完整契约**：请求形态 + 鉴权形态 + 端点。
//
// ★ 新增引擎 = 往 builtinSearchContracts() 加一行，**不得**再写
//
//	`strings.Contains(apiURL, "...engine...")` 式的第三分支（那是"同一事实多源"的入口）。
type searchAPIContract struct {
	Name       string
	Method     string
	DefaultURL string // 内置端点；SEARCH_API_URL 覆盖它
	Endpoint   string // 本次实际使用的端点（由 resolveSearchContract 填）

	QueryParams   []searchQueryParam // GET：URL 查询参数
	EngineIDParam string             // 把 SEARCH_ENGINE_ID 下发到该参数（Google CSE 的 cx）
	EngineID      string
	Body          map[string]any // POST：JSON 体模板（含 {{query}} / {{count}} 占位符）

	Auth searchAuth
}

// requiresAPIKey 该契约是否需要 SEARCH_API_KEY。
// Auth=query-key/bearer/header-token ⇒ 需要；Auth=none ⇒ 不需要（无需伪造一个假 key）。
func (c searchAPIContract) requiresAPIKey() bool { return c.Auth.Mode != searchAuthNone }

// ---------------------------------------------------------------- 请求体模板

// bodyPlaceholder 是请求体模板中的占位符。
// ★ 用**独立类型**而不是魔法字符串：漏替换时 json.Marshal 会把 {{query}} 原样发出去（可见失败），
// 而不会静默把占位符当成了真实取值。
type bodyPlaceholder string

const (
	bodyPlaceholderQuery bodyPlaceholder = "{{query}}"
	bodyPlaceholderCount bodyPlaceholder = "{{count}}"
)

// fillBody 递归替换模板里的占位符。map[string]any 由 encoding/json 按键排序输出 ⇒ 载荷确定性。
func fillBody(tmpl any, query string, count int) any {
	switch v := tmpl.(type) {
	case bodyPlaceholder:
		switch v {
		case bodyPlaceholderQuery:
			return query
		case bodyPlaceholderCount:
			return count
		default:
			return string(v)
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = fillBody(val, query, count)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = fillBody(val, query, count)
		}
		return out
	default:
		return tmpl
	}
}

// ---------------------------------------------------------------- 内置契约表

// builtinSearchContracts 是所有内置引擎契约的 SSOT。
//
// 端点与请求形态来源（2026-09-18 核实）：
// - bocha : 官方 curl 示例 `POST https://api.bochaai.com/v1/web-search`，
// header `Authorization: Bearer <KEY>`，body {query, freshness, summary, count}
// - qianfan: 官方请求结构 `POST /v2/ai_search/web_search` HOST qianfan.baidubce.com，
// header `Authorization: Bearer`，body {messages[{content,role}], search_source, resource_type_filter[{type,top_k}]}
// - google / brave: 沿用改造前行为（GET + query），仅作兼容保留
// - searxng: 沿用改造前第三分支（GET + q + format=json，无鉴权）
//
// ★ Bing 不在表内：Bing Search API 已于 2025-08-11 完全退役，替代品 Grounding with Bing Search
//
//	不返回原始结果 JSON ⇒ **不得**把 Bing 写成可用引擎。需要 Bing 风格信封时用 bocha。
func builtinSearchContracts() map[string]searchAPIContract {
	return map[string]searchAPIContract{
		"google": {
			Name:       "google",
			Method:     http.MethodGet,
			DefaultURL: "https://www.googleapis.com/customsearch/v1",
			QueryParams: []searchQueryParam{
				{Name: "q"},
			},
			EngineIDParam: "cx",
			Auth:          searchAuth{Mode: searchAuthQueryKey, QueryParam: "key"},
		},
		"brave": {
			Name:        "brave",
			Method:      http.MethodGet,
			DefaultURL:  "https://api.search.brave.com/res/v1/web/search",
			QueryParams: []searchQueryParam{{Name: "q"}},
			Auth:        searchAuth{Mode: searchAuthHeaderToken, Headers: []string{"X-Subscription-Token"}},
		},
		"bocha": {
			Name:       "bocha",
			Method:     http.MethodPost,
			DefaultURL: "https://api.bochaai.com/v1/web-search",
			Body: map[string]any{
				"query":     bodyPlaceholderQuery,
				"freshness": "noLimit",
				"summary":   true,
				"count":     bodyPlaceholderCount,
			},
			Auth: searchAuth{Mode: searchAuthBearer, Headers: []string{"Authorization"}},
		},
		"qianfan": {
			Name:       "qianfan",
			Method:     http.MethodPost,
			DefaultURL: "https://qianfan.baidubce.com/v2/ai_search/web_search",
			Body: map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": bodyPlaceholderQuery},
				},
				"search_source": "baidu_search_v2",
				"resource_type_filter": []any{
					map[string]any{"type": "web", "top_k": bodyPlaceholderCount},
				},
			},
			// 百度官方文档自相矛盾：请求结构块写 `Authorization: Bearer`，curl 示例写
			// `X-Appbuilder-Authorization: Bearer`。两者都发（多余头被忽略），以消除"只有真机才发现"的失败面。
			Auth: searchAuth{Mode: searchAuthBearer, Headers: []string{"Authorization", "X-Appbuilder-Authorization"}},
		},
		"searxng": {
			Name:   "searxng",
			Method: http.MethodGet,
			// ★ 无默认端点：SearXNG 是自建/托管实例，必须显式给 SEARCH_API_URL。
			QueryParams: []searchQueryParam{
				{Name: "q"},
				{Name: "format", Literal: "json"},
			},
			Auth: searchAuth{Mode: searchAuthNone},
		},
	}
}

// searchFormatValues 返回 SEARCH_API_FORMAT 的全部合法取值（排序后，供错误文案使用）。
func searchFormatValues() []string {
	names := make([]string, 0, len(builtinSearchContracts())+1)
	for name := range builtinSearchContracts() {
		names = append(names, name)
	}
	sort.Strings(names)
	return append(names, autoSearchFormat)
}

// legacySearchFormat 是**未配置** SEARCH_API_FORMAT 时的兼容解析。
//
// 与改造前的分支顺序逐条等价（改造前 web.go:392-408 即按 host 判断），因此存量配置零行为变更。
// ★ 两点说明：
// - 本函数只在"未显式配置"时被调用；一旦显式配置，请求形态**完全不看 host**（这正是要消除的误匹配）。
// - 从改造前的 `strings.Contains(url, "brave.com")` 收紧为**精确 host 相等**：
// 子串匹配会把 `https://evil.example/?x=brave.com` 误判成 Brave（同一 host 也可能有多种鉴权模式）。
func legacySearchFormat(rawURL, engineID string) string {
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch {
	case host == "www.googleapis.com" || host == "googleapis.com":
		return "google"
	case host == "api.search.brave.com" || host == "brave.com":
		return "brave"
	case rawURL != "":
		return "searxng"
	case engineID != "":
		return "google"
	default:
		return "brave"
	}
}

// resolveSearchContract 决定本次检索使用的契约。
//
// 优先级：显式 SEARCH_API_FORMAT（写错 ⇒ 硬错，fail-closed）> legacy 兼容解析。
func resolveSearchContract(getenv func(string) string) (searchAPIContract, error) {
	rawURL := strings.TrimSpace(getenv(searchAPIURLEnv))
	engineID := strings.TrimSpace(getenv(searchEngineIDEnv))
	formatRaw := strings.TrimSpace(getenv(searchFormatEnv))

	format := strings.ToLower(formatRaw)
	if format == "" || format == autoSearchFormat {
		format = legacySearchFormat(rawURL, engineID)
	}

	contract, ok := builtinSearchContracts()[format]
	if !ok {
		// fail-closed：配置写错直接拦截，绝不退化成"猜一个形态"。
		return searchAPIContract{}, fmt.Errorf(
			"unknown %s %q (valid values: %s)",
			searchFormatEnv, formatRaw, strings.Join(searchFormatValues(), ", "))
	}

	contract.Endpoint = rawURL
	if contract.Endpoint == "" {
		contract.Endpoint = contract.DefaultURL
	}
	if contract.Endpoint == "" {
		return searchAPIContract{}, fmt.Errorf("%s=%s requires %s (this format has no built-in endpoint)",
			searchFormatEnv, format, searchAPIURLEnv)
	}
	contract.EngineID = engineID
	return contract, nil
}

// buildRequest 按契约构造请求：方法 / 请求体 / 查询参数 / 鉴权，全部来自同一结构体。
func (c searchAPIContract) buildRequest(ctx context.Context, apiKey, query string) (*http.Request, error) {
	var bodyReader *bytes.Reader
	var payload []byte
	if c.Method == http.MethodPost {
		filled := fillBody(c.Body, query, maxSearchResults)
		encoded, err := json.Marshal(filled)
		if err != nil {
			return nil, fmt.Errorf("encode search request body: %w", err)
		}
		payload = encoded
		bodyReader = bytes.NewReader(payload)
	}

	var req *http.Request
	var err error
	if bodyReader != nil {
		req, err = http.NewRequestWithContext(ctx, c.Method, c.Endpoint, bodyReader)
	} else {
		req, err = http.NewRequestWithContext(ctx, c.Method, c.Endpoint, nil)
	}
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "WhaleCode/0.1.0")
	if c.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	if len(c.QueryParams) > 0 || c.EngineIDParam != "" {
		q := req.URL.Query()
		for _, p := range c.QueryParams {
			if p.Literal != "" {
				q.Set(p.Name, p.Literal)
				continue
			}
			q.Set(p.Name, query)
		}
		if c.EngineIDParam != "" && c.EngineID != "" {
			q.Set(c.EngineIDParam, c.EngineID)
		}
		req.URL.RawQuery = q.Encode()
	}

	c.applyAuth(req, apiKey)
	return req, nil
}

// applyAuth 把凭据按**显式配置**的形态挂上去。apiKey 为空时不做任何事（无鉴权契约本就允许）。
func (c searchAPIContract) applyAuth(req *http.Request, apiKey string) {
	if apiKey == "" {
		return
	}
	switch c.Auth.Mode {
	case searchAuthQueryKey:
		q := req.URL.Query()
		q.Set(c.Auth.QueryParam, apiKey)
		req.URL.RawQuery = q.Encode()
	case searchAuthBearer:
		for _, h := range c.Auth.Headers {
			req.Header.Set(h, "Bearer "+apiKey)
		}
	case searchAuthHeaderToken:
		for _, h := range c.Auth.Headers {
			req.Header.Set(h, apiKey)
		}
	}
}

// ============================================================================
// 响应侧：统一结构契约（引擎无关）
// ============================================================================
//
// ★ 关键区分：**「空数组」是合规契约的实例**（⇒ IsError=false + "No results found"），
// **「找不到结果数组」才是不合规**（⇒ error ⇒ IsError=true）。
// 混淆这两者会把"上游真的没结果"变成假失败，诱发模型重试 —— 反而更贵。

var (
	// errSearchResponseNotJSON 响应不是 JSON 结果文档（例如 GET 到了登录页 HTML）。
	errSearchResponseNotJSON = errors.New("search response is not a JSON result document")
	// errSearchNoResultArray 是 JSON，但没有任何"元素可映射"的结果数组（例如错误信封）。
	errSearchNoResultArray = errors.New("search response contains no recognizable result array")
)

// searchFieldSynonyms 是字段别名表的 SSOT：新增引擎只许扩表，不许在分支里硬编码字段名。
// url 是**硬要求**（缺则该元素不算结果）；title / snippet 允许缺（上游常见）。
var searchFieldSynonyms = map[string][]string{
	"title":   {"title", "name", "headline"},
	"url":     {"url", "link", "href", "webUrl"},
	"snippet": {"snippet", "description", "content", "summary", "text"},
}

// searchResultPaths 是已知信封路径（按优先级试；命中即用，全不中则进兜底扫描）。
var searchResultPaths = [][]string{
	{"items"},                     // Google CSE
	{"web", "results"},            // Brave
	{"data", "webPages", "value"}, // 博查（Bing 兼容信封）
	{"webPages", "value"},         // Azure Bing 原生信封
	{"references"},                // 百度千帆 AI 搜索
	{"results"},                   // SearXNG / 多数通用引擎
}

// 兜底扫描的边界：够广（未知引擎不必改代码），但不是无界放行。
const (
	searchScanMaxDepth = 5
	searchScanMaxNodes = 5000
)

// searchResult 是规范化后的一条检索结果。
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// normalizeSearchResult 把一个候选元素映射成 searchResult；url 缺失则该元素不算结果。
func normalizeSearchResult(el any) (searchResult, bool) {
	m, ok := el.(map[string]any)
	if !ok {
		return searchResult{}, false
	}
	pick := func(field string) string {
		for _, name := range searchFieldSynonyms[field] {
			if s, isStr := m[name].(string); isStr && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	r := searchResult{Title: pick("title"), URL: pick("url"), Snippet: pick("snippet")}
	if r.URL == "" {
		return searchResult{}, false
	}
	return r, true
}

// mapSearchList 尝试把一个数组映射成结果列表。
// 返回 (结果, 是否命中契约)：空数组命中（0 条结果）；元素全不可映射则**不命中**（继续试下一条信封）。
func mapSearchList(node any) ([]searchResult, bool) {
	lst, isList := node.([]any)
	if !isList {
		return nil, false
	}
	if len(lst) == 0 {
		return []searchResult{}, true
	}
	out := make([]searchResult, 0, len(lst))
	for _, el := range lst {
		if r, ok := normalizeSearchResult(el); ok {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// deepScanSearchResults 兜底：找第一个「元素可映射」的数组。
// ★ 确定性：Go 的 map 迭代无序，这里按键排序遍历 ⇒ 同一输入必得同一结果（原型 sniff.py 依赖插入序，
// Go 侧必须显式排序，否则同一个响应可能这次命中 A、下次命中 B）。
func deepScanSearchResults(root any) []searchResult {
	budget := searchScanMaxNodes
	var walk func(any, int) []searchResult
	walk = func(node any, depth int) []searchResult {
		if depth > searchScanMaxDepth || budget <= 0 {
			return nil
		}
		budget--
		switch v := node.(type) {
		case []any:
			if items, hit := mapSearchList(v); hit {
				return items
			}
			for _, el := range v {
				if r := walk(el, depth+1); r != nil {
					return r
				}
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if r := walk(v[k], depth+1); r != nil {
					return r
				}
			}
		}
		return nil
	}
	return walk(root, 0)
}

// sniffSearchResults 判定响应是否满足契约。
// - 返回 (results, nil) ⇒ 契约成立（可能是空列表 ⇒ 0 条结果）
// - 返回 (nil, err) ⇒ 契约不成立 ⇒ 调用方必须 fail-closed
func sniffSearchResults(body []byte) ([]searchResult, error) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errSearchResponseNotJSON
	}
	switch doc.(type) {
	case map[string]any, []any:
	default:
		return nil, errSearchResponseNotJSON
	}

	for _, path := range searchResultPaths {
		var node any = doc
		ok := true
		for _, key := range path {
			m, isMap := node.(map[string]any)
			if !isMap {
				ok = false
				break
			}
			child, exists := m[key]
			if !exists {
				ok = false
				break
			}
			node = child
		}
		if !ok {
			continue
		}
		if items, hit := mapSearchList(node); hit {
			return items, nil
		}
		// 路径存在但元素不可映射 ⇒ 不是这个信封，继续试下一条（不立即判死）
	}

	if items := deepScanSearchResults(doc); items != nil {
		return items, nil
	}
	return nil, errSearchNoResultArray
}

// formatSearchResults 把结果渲染成模型可读文本（形态与改造前保持一致）。
func formatSearchResults(items []searchResult) string {
	if len(items) == 0 {
		return "No results found"
	}
	parts := make([]string, 0, maxSearchResults)
	for i, item := range items {
		if i >= maxSearchResults {
			break
		}
		parts = append(parts, fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, item.Title, item.URL, item.Snippet))
	}
	return strings.Join(parts, "\n\n")
}

// searchEndpointHost 取端点 host，用于错误归因。
// ★ 只取 host（不含 path/query）⇒ 顺带避免把 URL 里的 key=... 回显给模型。
func searchEndpointHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return "(unparsable endpoint)"
}

// parseSearchResults 是响应侧的唯一出口。
// ★ 单一出口（改造前是 3 个 `return string(body[:2000]), nil` 的原文直通 —— 那正是
// "把 HTML 汤 / 错误信封当检索结果"的幻觉入口）。
func parseSearchResults(body []byte, endpoint string) (string, error) {
	items, err := sniffSearchResults(body)
	if err != nil {
		// ★ 错误文案只含 host，**不回显 body**（body 可能是 HTML 汤，或含上游回显的凭据）。
		return "", fmt.Errorf("%w (endpoint %s)", err, searchEndpointHost(endpoint))
	}
	return formatSearchResults(items), nil
}

// ============================================================================
// 脱敏：上游报文与 HTTP 错误里可能回显凭据
// ============================================================================

var (
	// Authorization / X-Subscription-Token / X-Appbuilder-Authorization 头：**整段**掩码（含其值）。
	// ★ 值部分必须吃掉 "Bearer <token>" 里的 scheme 与 token 两段 —— 若只吃到第一个空格，
	// 剩下的 token 会留在原地（自测时第一版就踩了这个坑）。
	searchRedactHeaderPattern = regexp.MustCompile(`(?i)\b(authorization|x-subscription-token|x-appbuilder-authorization)\s*:\s*[^\r\n"',;]+`)
	// JSON 形态的凭据字段（上游 401 报文本体常见：{"key":"sk-..."}）。
	searchRedactJSONPattern = regexp.MustCompile(`(?i)"(key|api_key|apikey|access_token|token)"\s*:\s*"[^"]*"`)
	// 裸 Bearer <token>（例如出现在 URL 或错误文本里）。
	searchRedactBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	// 查询参数形态的凭据（改造前只覆盖了 key=）。
	searchRedactQueryPattern = regexp.MustCompile(`(?i)\b(key|api_key|apikey|access_token|token)=[^&\s"']+`)
)

// redactSearchSecrets 掩码文本中可能出现的凭据。
//
// 为什么要覆盖这么多形态：改 POST/Bearer 之后，凭据会出现在
//
//	① 上游 401 响应体里（被 :430 截取 200B 回显）
//	② net/http 的错误文本里（`Get "https://.../v1?key=SECRET": dial tcp ...` —— 客户端错误自带完整 URL）
//
// 只脱 `key=` 是不够的（改造前 web.go:424 就是只脱这一种）。
func redactSearchSecrets(s string) string {
	s = searchRedactHeaderPattern.ReplaceAllString(s, "$1: [REDACTED]")
	s = searchRedactJSONPattern.ReplaceAllString(s, `"$1":"[REDACTED]"`)
	s = searchRedactBearerPattern.ReplaceAllString(s, "Bearer [REDACTED]")
	s = searchRedactQueryPattern.ReplaceAllString(s, "$1=[REDACTED]")
	return s
}
