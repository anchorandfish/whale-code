package tools

// ============================================================================
// S2′ 统一结构契约 · 离线端到端判据（0 token / 0 网络）
// ============================================================================
//
// 覆盖用户 2026-09-18 09:0x 裁定的三场景分叉：
//   场景 1 已知引擎正常
//   场景 2 未知但格式合规（含**博查 / 百度千帆的 POST 请求侧**）⇒ 放行
//   场景 3 格式非法 ⇒ fail-closed
//
// 全部走 **ExecuteBatch 生产入口**（= internal/tui/runner_core.go:1496 的同一条链路），
// 用 httptest 当上游 —— 判据必须落在生产真正执行的那条路径上，不能只测 handler。
//
// 前提：doWebSearch 的 transport 用 e.AllowLocalhostForTest（否则 httptest 会被自身 SSRF 拦成 SKIP）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------- 请求侧记录器

type capturedSearchRequest struct {
	Method  string
	Query   url.Values
	Headers http.Header
	Body    []byte
}

type searchRequestRecorder struct {
	mu  sync.Mutex
	got []capturedSearchRequest
}

func (rec *searchRequestRecorder) capture(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.got = append(rec.got, capturedSearchRequest{
		Method:  r.Method,
		Query:   r.URL.Query(),
		Headers: r.Header.Clone(),
		Body:    body,
	})
	rec.mu.Unlock()
}

func (rec *searchRequestRecorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.got)
}

func (rec *searchRequestRecorder) first() (capturedSearchRequest, bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.got) == 0 {
		return capturedSearchRequest{}, false
	}
	return rec.got[0], true
}

// ---------------------------------------------------------------- 请求侧断言

type requestCheck func(*testing.T, capturedSearchRequest)

func compose(checks ...requestCheck) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		for _, c := range checks {
			c(t, cr)
		}
	}
}

func wantMethod(method string) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		if cr.Method != method {
			t.Errorf("请求方法应为 %s，实际 %s", method, cr.Method)
		}
	}
}

func wantQuery(name, value string) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		if got := cr.Query.Get(name); got != value {
			t.Errorf("查询参数 %s 应为 %q，实际 %q（全部=%v）", name, value, got, cr.Query)
		}
	}
}

func wantNoQuery(name string) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		if _, exists := cr.Query[name]; exists {
			t.Errorf("不应出现查询参数 %s（全部=%v）", name, cr.Query)
		}
	}
}

func wantHeader(name, value string) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		if got := cr.Headers.Get(name); got != value {
			t.Errorf("请求头 %s 应为 %q，实际 %q", name, value, got)
		}
	}
}

func wantNoHeader(name string) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		if got := cr.Headers.Get(name); got != "" {
			t.Errorf("不应带上请求头 %s，实际 %q", name, got)
		}
	}
}

// wantJSONBody 断言 POST 请求体的某个字段。path 用点号分段，数组下标写成数字段。
func wantJSONBody(path string, want any) requestCheck {
	return func(t *testing.T, cr capturedSearchRequest) {
		t.Helper()
		var node any
		if err := json.Unmarshal(cr.Body, &node); err != nil {
			t.Fatalf("请求体不是合法 JSON（POST 契约必须带 JSON body）: %v（body=%q）", err, string(cr.Body))
		}
		for _, seg := range strings.Split(path, ".") {
			switch n := node.(type) {
			case map[string]any:
				v, ok := n[seg]
				if !ok {
					t.Fatalf("请求体缺少字段 %s（路径 %s）: %s", seg, path, string(cr.Body))
				}
				node = v
			case []any:
				idx, err := strconv.Atoi(seg)
				if err != nil || idx < 0 || idx >= len(n) {
					t.Fatalf("路径 %s 的数组下标 %q 越界: %s", path, seg, string(cr.Body))
				}
				node = n[idx]
			default:
				t.Fatalf("路径 %s 无法继续下钻（在 %q 处）: %s", path, seg, string(cr.Body))
			}
		}
		if got, wantStr := fmt.Sprintf("%v", node), fmt.Sprintf("%v", want); got != wantStr {
			t.Errorf("请求体 %s 应为 %v，实际 %v（body=%s）", path, wantStr, got, string(cr.Body))
		}
	}
}

// ---------------------------------------------------------------- 夹具定义

const (
	searchFixtureQuery = "golang generics"
	searchFixtureKey   = "dummy-key-for-test"
)

type searchFixture struct {
	id       string
	desc     string
	format   string // SEARCH_API_FORMAT（"" = 未配置，走 legacy 兼容解析）
	apiKey   string // "" = 未配置
	engineID string
	response string
	status   int // 0 ⇒ 200
	allow    bool
	wantOut  string // 输出必须包含
	checkReq requestCheck

	noLocalhostBypass bool // true ⇒ 不置 AllowLocalhostForTest（用于 SSRF 独立性断言）
	wantZeroRequests  bool // true ⇒ 上游不得收到任何请求
}

func contractFixtures() []searchFixture {
	return []searchFixture{
		// ---------------- 场景 1：已知引擎（回归护栏，不得误伤）----------------
		{
			id: "A-google", desc: "已知引擎·Google CSE（GET + query-key）",
			format: "google", apiKey: searchFixtureKey, engineID: "cx-fixture",
			response: `{"items":[
				{"title":"Go 1.25 Release Notes","link":"https://go.dev/doc/go1.25","snippet":"Go 1.25 is released..."},
				{"title":"Golang Release History","link":"https://go.dev/doc/devel/release","snippet":"Release history..."}]}`,
			allow: true, wantOut: "Go 1.25 Release Notes",
			checkReq: compose(
				wantMethod(http.MethodGet),
				wantQuery("q", searchFixtureQuery),
				wantQuery("key", searchFixtureKey),
				wantQuery("cx", "cx-fixture"),
			),
		},
		{
			id: "B-brave", desc: "已知引擎·Brave（GET + 自定义头 token）",
			format: "brave", apiKey: searchFixtureKey,
			response: `{"web":{"results":[
				{"title":"Go 1.25","url":"https://go.dev/doc/go1.25","description":"Release notes"}]}}`,
			allow: true, wantOut: "https://go.dev/doc/go1.25",
			checkReq: compose(
				wantMethod(http.MethodGet),
				wantQuery("q", searchFixtureQuery),
				wantHeader("X-Subscription-Token", searchFixtureKey),
				wantNoQuery("key"),
			),
		},

		// ---------------- 场景 2：未知引擎但格式合规（本次裁定要放行的）----------------
		{
			id: "C-bocha", desc: "未知引擎·博查（POST + Bearer + {query,count,summary}）",
			format: "bocha", apiKey: searchFixtureKey,
			response: `{"code":200,"log_id":"d71841ad20095f61","msg":null,
				"data":{"_type":"SearchResponse","webPages":{"totalEstimatedMatches":8912791,"value":[
					{"name":"阿里巴巴发布2024年ESG报告","url":"https://www.alibabagroup.com/document-175",
					 "snippet":"阿里巴巴集团发布《2024财年环境、社会和治理(ESG)报告》...","siteName":"www.alibabagroup.com"}]}}}`,
			allow: true, wantOut: "https://www.alibabagroup.com/document-175",
			checkReq: compose(
				wantMethod(http.MethodPost),
				wantHeader("Authorization", "Bearer "+searchFixtureKey),
				wantHeader("Content-Type", "application/json"),
				wantJSONBody("query", searchFixtureQuery),
				wantJSONBody("count", maxSearchResults),
				wantJSONBody("freshness", "noLimit"),
				wantJSONBody("summary", true),
			),
		},
		{
			id: "D-azurebing", desc: "未知引擎·Azure Bing 原生信封（GET 契约 + 跨引擎信封）",
			format: "searxng",
			response: `{"webPages":{"value":[
				{"name":"Go Programming Language","url":"https://go.dev","snippet":"Build simple, secure..."}]}}`,
			allow: true, wantOut: "Build simple, secure",
			checkReq: compose(
				wantMethod(http.MethodGet),
				wantQuery("q", searchFixtureQuery),
				wantQuery("format", "json"),
				wantNoHeader("Authorization"),
				wantNoQuery("key"),
			),
		},
		{
			id: "E-qianfan", desc: "未知引擎·百度千帆（POST + Bearer + messages/resource_type_filter）",
			format: "qianfan", apiKey: searchFixtureKey,
			response: `{"request_id":"req-1","is_safe":true,"references":[
				{"id":1,"title":"【河北天气】河北天气预报","url":"https://www.weather.com.cn/hebei",
				 "snippet":"河北天气预报，及时准确发布中央气象台天气信息...","type":"web"},
				{"id":2,"title":"中国天气网","url":"https://www.weather.com.cn","content":"中央气象台"}]}`,
			allow: true, wantOut: "https://www.weather.com.cn/hebei",
			checkReq: compose(
				wantMethod(http.MethodPost),
				wantHeader("Authorization", "Bearer "+searchFixtureKey),
				wantJSONBody("messages.0.content", searchFixtureQuery),
				wantJSONBody("messages.0.role", "user"),
				wantJSONBody("search_source", "baidu_search_v2"),
				wantJSONBody("resource_type_filter.0.type", "web"),
				wantJSONBody("resource_type_filter.0.top_k", maxSearchResults),
			),
		},
		{
			id: "F-searxng", desc: "未知引擎·SearXNG（GET + format=json）",
			format: "searxng",
			response: `{"query":"golang generics","number_of_results":1000000,"results":[
				{"title":"Go Generics Tutorial","url":"https://go.dev/blog/generics","content":"The Go Blog...","engine":"google","score":0.9}]}`,
			allow: true, wantOut: "Go Generics Tutorial",
			checkReq: compose(wantMethod(http.MethodGet), wantQuery("format", "json")),
		},
		{
			id: "G-deep", desc: "未知引擎·任意深层结构（兜底深度扫描）",
			format: "searxng",
			response: `{"status":"ok","payload":{"hits":{"list":[
				{"headline":"Some Page","href":"https://example.com/a","summary":"abstract text"}]}}}`,
			allow: true, wantOut: "https://example.com/a",
		},
		{
			id: "H-empty", desc: "合规但 0 条结果（必须保持成功，防假失败诱发重试）",
			format: "qianfan", apiKey: searchFixtureKey,
			response: `{"references":[]}`,
			allow:    true, wantOut: "No results found",
		},
		{
			id: "Z-legacy-custom-url", desc: "存量配置回归·未配置 SEARCH_API_FORMAT + 自定义端点（改造前第三分支）",
			format: "", apiKey: searchFixtureKey,
			response: `{"results":[{"title":"Legacy Path Still Works","url":"https://example.com/legacy","content":"text"}]}`,
			allow:    true, wantOut: "Legacy Path Still Works",
			checkReq: compose(
				wantMethod(http.MethodGet),
				wantQuery("q", searchFixtureQuery),
				wantQuery("format", "json"),
			),
		},

		// ---------------- 场景 3：结构不合规 ⇒ fail-closed ----------------
		{
			id: "I-html", desc: "非法·HTML 汤（GET 到了登录页）",
			format:   "searxng",
			response: `<!DOCTYPE html><html><head><title>Sign in</title></head><body><form>...</form></body></html>`,
			allow:    false,
		},
		{
			id: "J-errenv", desc: "非法·错误信封（博查 401）",
			format: "bocha", apiKey: searchFixtureKey,
			response: `{"code":401,"message":"Invalid API KEY","log_id":"c66aac17eab1bb7e"}`,
			allow:    false,
		},
		{
			id: "K-wrapped", desc: "非法·有数组但元素非结果对象",
			format:   "searxng",
			response: `{"data":{"list":[{"foo":1},{"bar":2}]}}`,
			allow:    false,
		},
		{
			id: "L-plain", desc: "非法·纯文本",
			format:   "searxng",
			response: `Search failed upstream, please retry later.`,
			allow:    false,
		},
		{
			id: "M-elem-no-url", desc: "非法·元素缺硬要求 url（判别力：url 是唯一硬要求）",
			format:   "searxng",
			response: `{"items":[{"title":"Some Page","snippet":"text but no url"}]}`,
			allow:    false,
		},
	}
}

// ---------------------------------------------------------------- 夹具执行器

func runSearchFixture(t *testing.T, fx searchFixture) {
	t.Helper()

	rec := &searchRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		status := fx.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(fx.response))
	}))
	defer server.Close()

	t.Setenv(searchFormatEnv, fx.format)
	t.Setenv(searchAPIURLEnv, server.URL)
	t.Setenv(searchEngineIDEnv, fx.engineID)
	t.Setenv(searchAPIKeyEnv, fx.apiKey)

	e := newWebSearchProdExecutor(t)
	if !fx.noLocalhostBypass {
		e.AllowLocalhostForTest = true
		t.Cleanup(func() { e.AllowLocalhostForTest = false })
	}

	results := e.ExecuteBatch(context.Background(), []Call{{
		ID:    "contract-" + fx.id,
		Name:  "WebSearch",
		Input: `{"query":"` + searchFixtureQuery + `"}`,
	}})
	if len(results) != 1 {
		t.Fatalf("期望 1 个结果，得到 %d", len(results))
	}
	r := results[0]

	if fx.allow {
		if r.IsError {
			t.Fatalf("[%s] %s 应放行，实际 IsError=true：%s", fx.id, fx.desc, r.Output)
		}
		if fx.wantOut != "" && !strings.Contains(r.Output, fx.wantOut) {
			t.Errorf("[%s] 输出应包含 %q，实际：%s", fx.id, fx.wantOut, r.Output)
		}
	} else {
		if !r.IsError {
			t.Fatalf("[%s] %s 应 fail-closed（IsError=true），实际放行：%s", fx.id, fx.desc, r.Output)
		}
	}

	if fx.checkReq != nil {
		cr, ok := rec.first()
		if !ok {
			t.Fatalf("[%s] 上游未收到任何请求", fx.id)
		}
		fx.checkReq(t, cr)
	}
	if fx.wantZeroRequests && rec.count() != 0 {
		t.Errorf("[%s] 不得向上游发请求，实际 %d 次", fx.id, rec.count())
	}
}

// TestSearchContractFixtures_EndToEnd 三场景夹具端到端（0 token / 0 网络）。
func TestSearchContractFixtures_EndToEnd(t *testing.T) {
	fixtures := contractFixtures()
	allow, block := 0, 0
	for _, fx := range fixtures {
		fx := fx
		if fx.allow {
			allow++
		} else {
			block++
		}
		t.Run(fx.id, func(t *testing.T) { runSearchFixture(t, fx) })
	}
	t.Logf("夹具合计 %d：放行 %d / fail-closed %d", len(fixtures), allow, block)
}

// ---------------------------------------------------------------- 配置面判据

// TestSearchContract_UnknownFormatFailsClosed SEARCH_API_FORMAT 写错 ⇒ 硬拦，且**不发任何请求**。
//
// 反例注入（应打红）：把 resolveSearchContract 的 `return …, fmt.Errorf(...)` 改成回落到某个默认契约。
func TestSearchContract_UnknownFormatFailsClosed(t *testing.T) {
	rec := &searchRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"title":"t","link":"https://e.com","snippet":"s"}]}`))
	}))
	defer server.Close()

	t.Setenv(searchFormatEnv, "hooli")
	t.Setenv(searchAPIURLEnv, server.URL)
	t.Setenv(searchAPIKeyEnv, searchFixtureKey)
	t.Setenv(searchEngineIDEnv, "")

	e := newWebSearchProdExecutor(t)
	e.AllowLocalhostForTest = true
	t.Cleanup(func() { e.AllowLocalhostForTest = false })

	r := e.ExecuteBatch(context.Background(), []Call{{
		ID: "unknown-format", Name: "WebSearch", Input: `{"query":"go"}`,
	}})[0]

	if !r.IsError {
		t.Fatalf("未知 SEARCH_API_FORMAT 必须 fail-closed，实际放行：%s", r.Output)
	}
	if !strings.Contains(r.Output, searchFormatEnv) {
		t.Errorf("错误文案应点名 %s，实际：%s", searchFormatEnv, r.Output)
	}
	if !strings.Contains(r.Output, "bocha") {
		t.Errorf("错误文案应列出合法取值（fail-closed 下用户要能自救），实际：%s", r.Output)
	}
	if n := rec.count(); n != 0 {
		t.Errorf("配置非法时不得向上游发请求（fail-closed 要在出口之前），实际 %d 次", n)
	}
}

// TestSearchContract_KeyedFormatRequiresAPIKey 有鉴权契约仍等价于改造前的「无 Key 即失败」。
func TestSearchContract_KeyedFormatRequiresAPIKey(t *testing.T) {
	t.Setenv(searchFormatEnv, "bocha")
	t.Setenv(searchAPIKeyEnv, "")

	e := newWebSearchProdExecutor(t)
	r := e.ExecuteBatch(context.Background(), []Call{{
		ID: "keyed-no-key", Name: "WebSearch", Input: `{"query":"go"}`,
	}})[0]

	if !r.IsError {
		t.Fatalf("有鉴权契约缺 Key 必须 fail-closed，实际放行：%s", r.Output)
	}
	if !strings.Contains(r.Output, searchAPIKeyEnv) {
		t.Errorf("错误输出应保留可执行的配置指引，实际：%s", r.Output)
	}
}

// TestSearchContract_SSRFStaysIndependent SSRF 防护与结构契约**不耦合**：
// AllowLocalhostForTest=false（生产零值）时，回环地址仍被拦。
func TestSearchContract_SSRFStaysIndependent(t *testing.T) {
	rec := &searchRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.Header().Set("Content-Type", "application/json")
		// 结构完全合规：若无 SSRF 防护，这条一定会被放行。
		_, _ = w.Write([]byte(`{"results":[{"title":"loopback","url":"https://example.com/x","content":"c"}]}`))
	}))
	defer server.Close()

	t.Setenv(searchFormatEnv, "searxng")
	t.Setenv(searchAPIURLEnv, server.URL)
	t.Setenv(searchAPIKeyEnv, "")

	e := newWebSearchProdExecutor(t) // 不置 AllowLocalhostForTest ⇒ 保持生产零值
	r := e.ExecuteBatch(context.Background(), []Call{{
		ID: "ssrf", Name: "WebSearch", Input: `{"query":"go"}`,
	}})[0]

	if !r.IsError {
		t.Fatalf("回环地址必须被 SSRF 拦下（AllowLocalhostForTest=false 时），实际放行：%s", r.Output)
	}
	if n := rec.count(); n != 0 {
		t.Errorf("SSRF 必须在建连前拦下，上游不得收到请求，实际 %d 次", n)
	}
}

// TestSearchContract_LegacyResolution 存量配置零行为变更 + 显式配置**完全不看 host**。
func TestSearchContract_LegacyResolution(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		engineID string
		format   string
		want     string
	}{
		{"未配置·URL 与 ENGINE_ID 都空 ⇒ Brave（原默认）", "", "", "", "brave"},
		{"未配置·仅 SEARCH_ENGINE_ID ⇒ Google", "", "cx1", "", "google"},
		{"未配置·googleapis 端点 ⇒ Google", "https://www.googleapis.com/customsearch/v1", "", "", "google"},
		{"未配置·Brave 端点 ⇒ Brave", "https://api.search.brave.com/res/v1/web/search", "", "", "brave"},
		{"未配置·自定义端点 ⇒ 通用 GET 契约（原第三分支）", "https://searx.example.com/search", "", "", "searxng"},
		{"★误匹配回归·URL 里含 brave.com 不得判成 Brave", "https://evil.example.com/?next=brave.com", "", "", "searxng"},
		{"★显式配置优先·URL 写着 brave 但格式写 bocha ⇒ bocha（不看 host）",
			"https://api.search.brave.com/res/v1/web/search", "", "bocha", "bocha"},
		{"显式 auto 等同未配置", "", "", "auto", "brave"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				searchAPIURLEnv:   tc.url,
				searchEngineIDEnv: tc.engineID,
				searchFormatEnv:   tc.format,
			}
			contract, err := resolveSearchContract(func(k string) string { return env[k] })
			if err != nil {
				t.Fatalf("解析失败（该组合不应报错）: %v", err)
			}
			if contract.Name != tc.want {
				t.Fatalf("契约解析错误：want %s，got %s", tc.want, contract.Name)
			}
		})
	}

	if _, err := resolveSearchContract(func(k string) string {
		if k == searchFormatEnv {
			return "hooli"
		}
		return ""
	}); err == nil {
		t.Fatal("未知 SEARCH_API_FORMAT 必须返回 error（fail-closed），实际 nil")
	}
}

// ---------------------------------------------------------------- 脱敏判据

// TestRedactSearchSecrets 凭据掩码必须覆盖"头 / JSON / 裸 Bearer / 查询串"四种形态。
//
// 为什么必须有这条：改 POST/Bearer 之后，凭据会出现在 ① 上游 401 报文体（被截 200B 回显）
// ② net/http 客户端错误文本（自带完整 URL，key 就在 query 里）。改造前只脱 `key=`。
//
// ★ 自测记录：第一版头正则只吃到第一个空格（`Bearer` 之后停下），token 会原样留下 —— 被本用例抓住。
func TestRedactSearchSecrets(t *testing.T) {
	const secret = "SECRET123"

	cases := []struct {
		name string
		in   string
	}{
		{"GET 客户端错误里的完整 URL（net/http 自带）",
			`Get "https://www.googleapis.com/customsearch/v1?key=SECRET123&cx=cx1&q=go": dial tcp 1.2.3.4:443: connect: connection refused`},
		{"Authorization 头（Bearer 形态）", "Authorization: Bearer SECRET123"},
		{"X-Subscription-Token 头", "X-Subscription-Token: SECRET123\nX-Other: keep-me"},
		{"X-Appbuilder-Authorization 头", "X-Appbuilder-Authorization: Bearer SECRET123"},
		{"JSON 报文体里的凭据字段", `{"code":401,"message":"Invalid API KEY","key":"SECRET123"}`},
		{"裸 Bearer（出现在散文里）", "upstream said: bearer SECRET123 is invalid"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out := redactSearchSecrets(tc.in)
			if strings.Contains(out, secret) {
				t.Fatalf("凭据未被掩码：%q → %q", tc.in, out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("应留下 [REDACTED] 标记便于归因：%q", out)
			}
		})
	}

	t.Run("不含凭据的文本必须原样通过（防过度脱敏把归因信息吃掉）", func(t *testing.T) {
		in := "search response contains no recognizable result array (endpoint example.com)"
		if out := redactSearchSecrets(in); out != in {
			t.Fatalf("无凭据文本不应被改动：%q → %q", in, out)
		}
	})
}

// TestSearchContract_RedactionOn401 端到端：上游把凭据回显在报错体里，模型不得看见。
func TestSearchContract_RedactionOn401(t *testing.T) {
	const secret = "sk-super-secret-1234567890"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w,
			`{"code":401,"message":"Invalid API KEY","key":"%s","hint":"Authorization: Bearer %s"}`, secret, secret)
	}))
	defer server.Close()

	t.Setenv(searchFormatEnv, "google")
	t.Setenv(searchAPIURLEnv, server.URL)
	t.Setenv(searchAPIKeyEnv, secret)
	t.Setenv(searchEngineIDEnv, "cx-fixture")

	e := newWebSearchProdExecutor(t)
	e.AllowLocalhostForTest = true
	t.Cleanup(func() { e.AllowLocalhostForTest = false })

	r := e.ExecuteBatch(context.Background(), []Call{{
		ID: "redact-401", Name: "WebSearch", Input: `{"query":"go"}`,
	}})[0]

	if !r.IsError {
		t.Fatalf("401 必须 fail-closed，实际放行：%s", r.Output)
	}
	if strings.Contains(r.Output, secret) {
		t.Fatalf("上游回显的凭据进了模型上下文：%s", r.Output)
	}
	if !strings.Contains(r.Output, "[REDACTED]") {
		t.Errorf("应保留 [REDACTED] 标记以便归因，实际：%s", r.Output)
	}
}
