// 检索域 · 工具面（S3）与上限 SSOT（S4）的判据 —— S5 判据矩阵中属于本批的部分。
//
// 纪律（与 web_search_contract_test.go 同）：
// - 全部离线：0 token / 0 网络（httptest 走 e.AllowLocalhostForTest 的既有测试缝）；
// - 走**生产入口** ExecuteBatch（= internal/tui/runner_core.go 的同一条链路），
// 不手工构造前提（不 mock 守卫、不置空依赖、不替换成空实现）；
// - 每条判据都配一条"反例注入应当打红"的记录（见文件尾注与 docs 回执）。
//
// ★ 本文件刻意**不**把"无通道"做成默认态：toolAvailable 的默认是"全部可用"（零行为变更），
// 所以每个"隐藏"用例都显式 SetToolAvailability(false) 并在 Cleanup 里复位 ——
// 否则用例之间会通过包级状态互相污染（G11 记录的同类事故）。
package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- 面①：通道判定

// TestS3_ChannelResolution 判定入口 WebSearchChannel 的取值表（§11.3：v0.1 只有 LocalKey|None）。
//
// 同时钉住"判定 = handler 真正会走的闸门"：把解析不出来的情形断言成 None，
// 并断言同一情形下 handler 确实失败（否则"工具面说没能力、实际却能跑"= 反向不同源）。
func TestS3_ChannelResolution(t *testing.T) {
	type tc struct {
		name string
		env  map[string]string
		want SearchChannel
	}
	cases := []tc{
		{
			name: "零配置（legacy 默认 brave：需 key 而 key 为空）",
			env:  map[string]string{},
			want: ChannelNone,
		},
		{
			name: "显式 google + key",
			env:  map[string]string{searchFormatEnv: "google", searchAPIKeyEnv: "k-fixture"},
			want: ChannelLocalKey,
		},
		{
			name: "searxng（Auth=none）+ 显式 URL ⇒ 无需 key",
			env:  map[string]string{searchFormatEnv: "searxng", searchAPIURLEnv: "https://s.example.com/search"},
			want: ChannelLocalKey,
		},
		{
			name: "searxng 但缺 URL（该契约无默认端点）",
			env:  map[string]string{searchFormatEnv: "searxng"},
			want: ChannelNone,
		},
		{
			name: "格式写错 ⇒ fail-closed",
			env:  map[string]string{searchFormatEnv: "bogus", searchAPIKeyEnv: "k-fixture"},
			want: ChannelNone,
		},
		{
			name: "google + 只有 URL 没 key",
			env:  map[string]string{searchFormatEnv: "google", searchAPIURLEnv: "https://g.example.com"},
			want: ChannelNone,
		},
		{
			name: "legacy：只给 URL（推断 searxng，无需 key）",
			env:  map[string]string{searchAPIURLEnv: "https://s.example.com/search"},
			want: ChannelLocalKey,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 四个键全部显式设置（缺的置空），否则开发机上已有的 SEARCH_* 会让结论漂移。
			for _, k := range []string{searchFormatEnv, searchAPIURLEnv, searchAPIKeyEnv, searchEngineIDEnv} {
				t.Setenv(k, c.env[k])
			}

			got := WebSearchChannel()
			if got != c.want {
				t.Fatalf("WebSearchChannel()=%q，期望 %q", got, c.want)
			}

			// 同源自证：判 None 的情形，handler 必须**真的跑不起来**（不得是"其实能跑"）。
			if got == ChannelNone {
				e := newWebSearchProdExecutor(t)
				_, err := e.webSearchHandler()(context.Background(), []byte(`{"query":"q"}`))
				if err == nil {
					t.Fatalf("判为无通道，但 handler 竟然成功 ⇒ 判定与执行不同源")
				}
				if !mentionsAnySearchConfig(err.Error()) {
					t.Errorf("无通道的失败应可归因到配置项，实际错误：%s", err.Error())
				}
			}
		})
	}
}

func mentionsAnySearchConfig(msg string) bool {
	for _, k := range []string{searchAPIKeyEnv, searchAPIURLEnv, searchFormatEnv} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 面①-④：无通道 ⇒ 三面皆无

// TestS3_NoChannel_HidesWebSearchOnAllFaces 判据 T9（裁定 C=②）。
//
// 三面 = 注册点（AllDirect）· 档位目录（toolLevelsSnapshot， 的过滤点）·
// 子 agent（getAllowedToolSet/getToolNameList/buildAgentTools）· 执行门（含别名）。
// 反例注入（应打红）：删掉 toollevel.go 的过滤循环，或把 executor.go 的闸门去掉。
func TestS3_NoChannel_HidesWebSearchOnAllFaces(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)
	SetToolAvailability("WebSearch", false)

	registry := NewRegistry()

	// 面①：主会话工具面（AllDirect 遍历 r.builtin ⇒ 不注册即不在）
	for _, s := range registry.AllDirect() {
		if s.Name == "WebSearch" {
			t.Fatalf("无通道时 WebSearch 不得出现在 AllDirect() 里")
		}
	}

	// 面②：档位目录（静态快照"只增不减"，必须显式过滤）
	if _, ok := toolLevelsSnapshot()["WebSearch"]; ok {
		t.Fatalf("无通道时 toolLevelsSnapshot() 仍含 WebSearch（档位目录未按同一谓词过滤）")
	}
	if allToolNames()["WebSearch"] {
		t.Fatalf("无通道时 allToolNames() 仍含 WebSearch")
	}
	if roleToolSet(AgentTypeCoder)["WebSearch"] {
		t.Fatalf("无通道时 coder 角色面仍含 WebSearch")
	}

	// 面③：子 agent 下发面
	e := newWebSearchProdExecutor(t)
	a := NewAgentContext(e, "probe", "", t.TempDir(), 1)
	a.AgentType = AgentTypeCoder
	if a.getAllowedToolSet()["WebSearch"] {
		t.Fatalf("无通道时 getAllowedToolSet() 仍含 WebSearch")
	}
	for _, n := range a.getToolNameList() {
		if n == "WebSearch" {
			t.Fatalf("无通道时 getToolNameList() 仍含 WebSearch")
		}
	}
	for _, d := range a.buildAgentTools() {
		if d.Name == "WebSearch" {
			t.Fatalf("无通道时 buildAgentTools() 仍下发给模型 WebSearch")
		}
	}

	// 公式不破（g6 §P5 的口径）：过滤后两侧同源，故仍恒等。
	want := intersectToolSet(roleToolSet(AgentTypeCoder), nil)
	if !sameStringSet(a.getAllowedToolSet(), want) {
		t.Errorf("过滤后公式被破坏：getAllowedToolSet() ≠ roleToolSet(coder) ∩ parent")
	}

	// 面④：执行门 —— 直名与**别名**必须落到同一个出口（B1 连带项）。
	//
	// 出口实测有两处，判据断言的是"两条路同一个答案"而不是某个字面串：
	// - 生产管线在 schema_validate 阶段按**注册表**拦（pipeline.go:452
	// `tool %q not registered (fail-closed)`）—— 它自动跟随注册点，无需额外接线；
	// - 非管线路径（Pipeline == nil ⇒ executeDirect）落到 `unknown tool`，
	// 由 executor.go 的 handler 闸门保证（否则它会退化成"缺少配置"的软提示）。
	// 关键是二者都必须是**不可用**语义，绝不能是"可用的配置提示"（那才是 S3 要消灭的假承诺）。
	// 反例注入（应打红）：把 executor.go 的闸门去掉 + 走 Pipeline==nil 路径。
	var msgs []string
	for _, name := range []string{"WebSearch", "search"} {
		r := runOne(t, e, name, `{"query":"go 1.25"}`)
		if !r.IsError {
			t.Errorf("[%s] 无通道时必须 fail-closed，实际 IsError=false output=%q", name, r.Output)
		}
		if !strings.Contains(r.Output, "not registered") && !strings.Contains(r.Output, "unknown tool") {
			t.Errorf("[%s] 出口既不是『未注册』也不是『未知工具』（是否退化成软提示？）：%q", name, r.Output)
		}
		msgs = append(msgs, r.Output)
	}
	if msgs[0] != msgs[1] {
		t.Errorf("直名与别名的错误不同源（B1 连带项被破坏）：\n  WebSearch=%q\n  search   =%q", msgs[0], msgs[1])
	}
	if !strings.Contains(msgs[0], "WebSearch") {
		t.Errorf("拒绝信息应点明解析后的工具名，实际：%q", msgs[0])
	}

	// 误伤护栏：S3 只动 WebSearch；WebFetch 仍必须可用（不得落到"不可用"出口）。
	r := runOne(t, e, "WebFetch", `{"url":"http://127.0.0.1:1/","timeout":5}`)
	if strings.Contains(r.Output, "not registered") || strings.Contains(r.Output, "unknown tool") {
		t.Errorf("WebFetch 被误伤成不可用：%q", r.Output)
	}
}

// TestS3_ChannelPresent_ExposesWebSearch 判据 T10（有通道 ⇒ 三面皆有）。
// 反例注入（应打红）：把 toolAvailable 写成恒 false。
func TestS3_ChannelPresent_ExposesWebSearch(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)

	registry := NewRegistry()
	found := false
	for _, s := range registry.AllDirect() {
		if s.Name == "WebSearch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("有通道时 WebSearch 必须出现在 AllDirect() 里")
	}
	if _, ok := toolLevelsSnapshot()["WebSearch"]; !ok {
		t.Fatalf("有通道时 toolLevelsSnapshot() 必须含 WebSearch")
	}

	a := NewAgentContext(NewExecutor(registry, t.TempDir()), "probe", "", t.TempDir(), 1)
	a.AgentType = AgentTypeCoder
	if !a.getAllowedToolSet()["WebSearch"] {
		t.Fatalf("有通道时 coder 角色面必须含 WebSearch")
	}
	// 执行门：不得是"未知工具"（具体失败原因是配置/网络，不在此判据范围内）。
	e := newWebSearchProdExecutor(t)
	r := runOne(t, e, "WebSearch", `{"query":"go 1.25"}`)
	if strings.Contains(r.Output, "unknown tool") {
		t.Errorf("有通道时不应落到未知工具出口：%q", r.Output)
	}
}

func runOne(t *testing.T, e *Executor, name, input string) Result {
	t.Helper()
	results := e.ExecuteBatch(context.Background(), []Call{{ID: "s3-" + name, Name: name, Input: input}})
	if len(results) != 1 {
		t.Fatalf("[%s] 期望 1 个结果，得到 %d", name, len(results))
	}
	return results[0]
}

// ---------------------------------------------------------------- 面：上限 SSOT（S4）

// TestS4_WebFetchCap_IsSingleSourceOfTruth 判据 T13（WebFetch 侧）+ "声明 = 执行"同源。
//
// 这一条同时是**新增的 G11 覆盖约定**的判据：WHALE_WEBFETCH_MAX_RESULT_CHARS 一旦生效，
// 截断点必须立刻跟随 —— 若截断点仍写字面量，本用例必红。
// 反例注入（应打红）：把 capWebResult 调用改回 `if len(content) > 30000 { content = content[:30000] }`。
func TestS4_WebFetchCap_IsSingleSourceOfTruth(t *testing.T) {
	// ① 生产默认值：与改造前 webFetchHandler 的硬编码 30000 **逐字一致**（行为不变）。
	if got := GetToolLimits("WebFetch").MaxResultChars; got != 30000 {
		t.Fatalf("WebFetch 生产默认上限应为 30000（对齐改造前的执行值），实际 %d", got)
	}

	body := strings.Repeat("A", 40000)

	serve := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	fetch := func(t *testing.T, url string) string {
		t.Helper()
		e := newWebSearchProdExecutor(t)
		e.AllowLocalhostForTest = true
		t.Cleanup(func() { e.AllowLocalhostForTest = false })
		r := runOne(t, e, "WebFetch", `{"url":"`+url+`","timeout":5}`)
		if r.IsError {
			t.Fatalf("WebFetch 不应失败：%s", r.Output)
		}
		return r.Output
	}

	// ② 不设 env：截断到 30000 且带标记。
	got := fetch(t, serve(t).URL)
	if want := 30000 + len(webTruncationMarker); len(got) != want {
		t.Fatalf("默认上限下输出长度=%d，期望 %d（30000 + 标记）", len(got), want)
	}
	if !strings.HasSuffix(got, webTruncationMarker) {
		t.Fatalf("截断必须带显式标记")
	}

	// ③ env 覆盖为 8000（G11 测试期约定）：**同一个截断点**必须立刻跟随。
	t.Setenv(EnvWebFetchMaxResultChars, "8000")
	if c := GetToolLimits("WebFetch").MaxResultChars; c != 8000 {
		t.Fatalf("env 覆盖后 GetToolLimits 应返回 8000，实际 %d", c)
	}
	got = fetch(t, serve(t).URL)
	if want := 8000 + len(webTruncationMarker); len(got) != want {
		t.Fatalf("env=8000 时输出长度=%d，期望 %d —— 截断点没有读 SSOT（仍有字面量？）", len(got), want)
	}
}

// TestS4_WebFetchCap_EnvSemantics 覆盖 env 的解析与 clamp 语义（与 H13-G4 同族）。
func TestS4_WebFetchCap_EnvSemantics(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 30000},           // 未设置 ⇒ 定稿值
		{"12000", 12000},      // 正常覆盖
		{" 8000 ", 8000},      // 前后空白容忍
		{"0", 30000},          // 非正值 ⇒ 默认值兜底
		{"-5", 30000},         // 负值 ⇒ 默认值兜底
		{"abc", 30000},        // 解析失败 ⇒ 默认值兜底
		{"999999999", 200000}, // 超上限 ⇒ clamp
	}
	for _, c := range cases {
		t.Setenv(EnvWebFetchMaxResultChars, c.raw)
		if got := GetToolLimits("WebFetch").MaxResultChars; got != c.want {
			t.Errorf("env=%q ⇒ %d，期望 %d", c.raw, got, c.want)
		}
	}
}

// TestS4_WebSearchCap_Enforced 判据 T7 / T13（WebSearch 侧）。
//
// ★ B5 如实标注：这是**构造输入**（单条 snippet 20000 字符），不是生产常态
// （5 条摘要通常 <1.5K）—— 8000 是防御性上限。用构造输入是为了让"上限真的生效"可判别，
// 而不是为了凑绿：去掉截断，本用例立刻红。
func TestS4_WebSearchCap_Enforced(t *testing.T) {
	huge := strings.Repeat("S", 20000)
	var items []string
	for i := 0; i < maxSearchResults; i++ {
		items = append(items, fmt.Sprintf(
			`{"title":"t%d","url":"https://e.example.com/%d","content":"%s"}`, i, i, huge))
	}
	payload := `{"results":[` + strings.Join(items, ",") + `]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	t.Setenv(searchFormatEnv, "searxng") // Auth=none ⇒ 无需 key
	t.Setenv(searchAPIURLEnv, srv.URL)
	t.Setenv(searchAPIKeyEnv, "")
	t.Setenv(searchEngineIDEnv, "")

	e := newWebSearchProdExecutor(t)
	e.AllowLocalhostForTest = true
	t.Cleanup(func() { e.AllowLocalhostForTest = false })

	r := runOne(t, e, "WebSearch", `{"query":"go 1.25"}`)
	if r.IsError {
		t.Fatalf("检索应成功：%s", r.Output)
	}

	cap := GetToolLimits("WebSearch").MaxResultChars
	if len(r.Output) != cap+len(webTruncationMarker) {
		t.Fatalf("输出长度=%d，期望 %d（上限 %d + 标记）—— 上限未生效", len(r.Output), cap+len(webTruncationMarker), cap)
	}
	if !strings.HasSuffix(r.Output, webTruncationMarker) {
		t.Fatalf("截断必须带显式标记（模型要知道内容被砍过）")
	}
	if n := strings.Count(r.Output, "https://e.example.com/"); n > maxSearchResults {
		t.Errorf("条目数 %d 超过上限 %d", n, maxSearchResults)
	}
}

// TestS4_DescriptionsRenderLimitFromSSOT 判据 T15（B2：同一事实同源）。
//
// 断言对象是**真正下发给模型的文本**（spec.Description = RenderLean/Render 的产物），
// 不只是 Desc 结构体；并证明 env 覆盖后描述自动跟随（v0.2 P0 的"只改一行"承诺）。
// 反例注入（应打红）：把 webToolConstraints 里的数字换成另一个常量。
func TestS4_DescriptionsRenderLimitFromSSOT(t *testing.T) {
	assertDesc := func(t *testing.T, registry *Registry, name string) {
		t.Helper()
		spec, ok := registry.Get(name)
		if !ok {
			t.Fatalf("注册表里没有 %s", name)
		}
		want := strconv.Itoa(GetToolLimits(name).MaxResultChars)
		if !strings.Contains(spec.Desc.Render(), want) {
			t.Errorf("%s: Desc.Render() 未含上限常量 %s：%q", name, want, spec.Desc.Render())
		}
		if !strings.Contains(spec.Description, want) {
			t.Errorf("%s: 下发给模型的 description 未含上限常量 %s：%q", name, want, spec.Description)
		}
	}

	reg := NewRegistry()
	assertDesc(t, reg, "WebFetch")
	assertDesc(t, reg, "WebSearch")

	// env 覆盖 ⇒ 描述自动跟随（无需改第二处文案）。
	t.Setenv(EnvWebFetchMaxResultChars, "8000")
	reg2 := NewRegistry()
	assertDesc(t, reg2, "WebFetch")
	if strings.Contains(mustSpec(t, reg2, "WebFetch").Description, "30000") {
		t.Errorf("env=8000 时描述里仍出现旧值 30000 —— 文案与上限不同源")
	}
}

func mustSpec(t *testing.T, r *Registry, name string) Spec {
	t.Helper()
	s, ok := r.Get(name)
	if !ok {
		t.Fatalf("注册表里没有 %s", name)
	}
	return s
}

// TestS4_WebFetchDescDoesNotPromiseMarkdown 判据 D5（描述与实现一致）。
// 实现是 stripHTMLTags ⇒ 纯文本；改造前描述写 "convert to Markdown"（承诺了没有的能力）。
func TestS4_WebFetchDescDoesNotPromiseMarkdown(t *testing.T) {
	s := mustSpec(t, NewRegistry(), "WebFetch")
	if strings.Contains(s.Description, "Markdown") && !strings.Contains(s.Description, "not Markdown") {
		t.Fatalf("WebFetch 描述仍把 Markdown 说成能力（实现是纯文本）：%q", s.Description)
	}
	if !strings.Contains(s.Description, "plain text") {
		t.Errorf("WebFetch 描述应写明返回纯文本：%q", s.Description)
	}
}
