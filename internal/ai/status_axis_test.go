package ai

// / —— 状态码轴：结构化事实收口 + 散文判据让路。
//
// 本文件的两族判据（各自由 overlay 变体证明承重，见 handoff 回执）：
//
//	（行为面，生产入口 ai.StreamWithRetry / 适配器 Stream）：
// "文本里含裸 413 的错误"不得再抢在结构化分类之前被判成 context overflow；
// 同时**回落层必须仍然工作**（只有字符串的调用方依赖它）。
//	（口径面，五个判据函数）：
// 同一个状态码在全部分支上必须给出同一个答案（收口前 501 两侧相反）。
//
// 事实（*APIError）一律由**生产解析器** ParseAPIError 从真实 HTTP 响应构造；
// HTTP 层用 httptest 真发包，不手工拼装"让被测代码跑到"的前提。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// apiErrorFromJSON 用**生产解析器** ParseAPIError 从真实 HTTP 响应构造结构化事实。
func apiErrorFromJSON(t *testing.T, status int, body string) *APIError {
	t.Helper()
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	apiErr := ParseAPIError(resp)
	if apiErr == nil {
		t.Fatal("ParseAPIError 返回 nil ⇒ 判据前提不成立")
	}
	if apiErr.StatusCode != status {
		t.Fatalf("ParseAPIError 未保留状态码：got %d want %d", apiErr.StatusCode, status)
	}
	return apiErr
}

// ---------------------------------------------------------------------------
// · 行为面 ①：500（body 含 "4132"）不得再被散文判据抢判为 context overflow
// ---------------------------------------------------------------------------

// TestN115_500With413Substring_NoLongerPreemptedAsContextOverflow
//
// 生产形态：provider 返回 500，body 里带一个以 413 开头的数字（request-id / token 数均可）。
//
//	收口前：retry.go 在结构化 `switch apiErr.ErrorID` **之前**调
//
// IsContextOverflowError(err.Error())，而 APIError.Error() 自带状态码/文本
// ⇒ 裸子串 "413" 命中 ⇒ 进 context overflow 分支；
// EnableContextOverflowRetry=false 时**立即**返回 "context overflow: …"，一次都不重试。
// 用户可见现象：一次瞬时 5xx 被报成"上下文溢出·不可恢复"。
//
//	收口后：结构化优先（StatusCode=500 ≠ 413，消息里也没有溢出措辞）⇒ 落到
//
// ErrAIApiServer/ErrAIRetryable 分支 ⇒ 按 MaxRetries 正常重试。
func TestN115_500With413Substring_NoLongerPreemptedAsContextOverflow(t *testing.T) {
	apiErr := apiErrorFromJSON(t, http.StatusInternalServerError,
		`{"error":{"type":"internal_server_error","message":"upstream unavailable (request-id 4132)"}}`)

	// 判据前提自检：结构化事实必须真的在位（否则测到的是"结构化缺失⇒回落"，不是"让路"）。
	if apiErr.ErrorID != ErrAIRetryable {
		t.Fatalf("事实前置不成立：ErrorID=%q 期望 %q", apiErr.ErrorID, ErrAIRetryable)
	}
	if !strings.Contains(apiErr.Error(), "4132") {
		t.Fatalf("事实前置不成立：err.Error() 未携带裸数字 4132 ⇒ 收口前的抢判路径根本不会触发：%v", apiErr.Error())
	}

	client := &mockClient{err: apiErr}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, JitterRatio: 0}
	_, err := StreamWithRetry(context.Background(), client, Request{Model: "m"}, cfg, nil, nil)

	if err == nil {
		t.Fatal("全部尝试失败后应返回错误")
	}
	if strings.Contains(err.Error(), "context overflow") {
		t.Errorf("500（body 含 4132）被判成 context overflow —— 散文判据仍抢在结构化分类之前：%v", err)
	}
	if client.callCount != 3 {
		t.Errorf("callCount = %d, want 3（MaxRetries=2 ⇒ 3 次；收口前被误判为不可恢复 ⇒ 1 次）", client.callCount)
	}
}

// ---------------------------------------------------------------------------
// · 行为面 ②：529 的文本回落不再被**跨迭代陈旧**的 apiErr 挡掉
// ---------------------------------------------------------------------------

// TestN115_OverloadTextFallbackNotBlockedByStaleAPIError
//
// 事实形态取自 ：`errors.As` 不重置目标 ⇒ 第一轮的 *APIError 会存活到第二轮。
//
//	收口前：retry.go 的 529 回落门写成 `apiErr == nil && IsOverloadedError(errMsg)`
//
// ⇒ 第二轮 err 是**普通错误** "529 Overloaded"，但 apiErr 仍是上一轮的
// {500, ErrAIApiServer}（非 nil）⇒ 门挡住回落，继续按陈旧的 ErrorID 走
// "照常重试" ⇒ QuerySource=Background 的 529 **不 drop**。
//
//	收口后：isOverloadedErr(err) 自带一次 errors.As（读**本轮** err）⇒ 文本判据生效
//
// ⇒ Background 529 按策略 drop（ErrAIFatal）。
func TestN115_OverloadTextFallbackNotBlockedByStaleAPIError(t *testing.T) {
	client := &mockClient{errs: []error{
		&APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: "internal"},
		fmt.Errorf("529 Overloaded"),
	}}
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, JitterRatio: 0}

	_, err := StreamWithRetry(context.Background(), client,
		Request{Model: "m", QuerySource: QuerySourceBackground}, cfg, nil, nil)

	if got := GetErrorID(err); got != ErrAIFatal {
		t.Errorf("Background 的 529 应 drop 为 ErrAIFatal，got=%q（收口前被陈旧的 apiErr 挡住 ⇒ 回落失效）", got)
	}
	if client.callCount != 2 {
		t.Errorf("callCount = %d, want 2（第 2 轮即应按 529 处理）", client.callCount)
	}
}

// ---------------------------------------------------------------------------
// · 判据面：结构化层与回落层必须**各自**正确（防"收窄过头"与"两通道同值"）
// ---------------------------------------------------------------------------

// TestN115_StructuredFirstSplit_BareDigitsOnlyInTextFallback
//
// ① 结构化在位 + 状态码非 413 ⇒ 消息里的裸数字**不可信**（false）
// ② 结构化在位 + 状态码 413 ⇒ 溢出成立（结构化信号）
// ③ 结构化在位 + 400 且**措辞**命中 ⇒ 成立（回落短语必须留，否则 prompt-too-long 的恢复路径不可达）
// ④ 结构化缺失（纯文本）⇒ 裸 "413" 仍须被采信（tui/plain.go:526 等外部调用方只有文本）
// ⑤ 反向半边：①②与④必须给出**不同**答案 ⇒ 证明差异来自"让路"，而不是两个通道恒同值（假绿）
func TestN115_StructuredFirstSplit_BareDigitsOnlyInTextFallback(t *testing.T) {
	// ①
	if (&APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: "backend 4132 unavailable"}).IsContextOverflow() {
		t.Error("① 状态码已知(500) ⇒ 消息里的裸 4132 不得当作溢出信号")
	}
	// ②
	if !(&APIError{StatusCode: 413, ErrorID: ErrAIFatal, Message: "payload too large"}).IsContextOverflow() {
		t.Error("② 413 是溢出的结构化信号，必须成立（否则收口把恢复路径关掉）")
	}
	// ③ 结构化在位 + 400 且**措辞**命中 ⇒ 成立（回落短语必须留，否则 prompt-too-long 的恢复路径不可达）
	// ⚠️ 本处刻意使用词表内的措辞。另有一条**既有缺口**（非本次引入、非本判据范围）：
	// `contextOverflowPhrases` 不含 Anthropic 协议原文 "prompt is too long"，
	// 而 stream.go 的 contextOverflowRe2 正是为该原文写的 ⇒ 该解析路径在生产上不可达。
	// 已登记进 handoff 回执「发现但未修」，**不**在此处以断言固化（否则等于把缺口锁成绿色契约）。
	if !(&APIError{StatusCode: 400, ErrorID: ErrAIFatal, Message: "prompt too long: 300000 tokens"}).IsContextOverflow() {
		t.Error("③ provider 把 prompt too long 编码在 400 的 message 里 ⇒ 短语回落必须留")
	}
	// ④
	if !IsContextOverflowError("API error 413 [ai_fatal]: request too large") {
		t.Error("④ 字符串回落层必须保留裸 413：tui/plain.go:526 等调用方手上只有 err.Error()")
	}
	// ⑤
	const text = "backend 4132 unavailable"
	if !IsContextOverflowError(text) {
		t.Fatal("⑤ 回落层（纯文本）应命中裸 413 —— 否则 ① 与 ④ 的差异并非'让路'造成，判据不可判别")
	}
	if (&APIError{StatusCode: 500, ErrorID: ErrAIApiServer, Message: text}).IsContextOverflow() {
		t.Fatal("⑤ 结构化层与回落层同值 ⇒ 该判据退化为恒绿，必须换观测面")
	}
}

// ---------------------------------------------------------------------------
// · 端到端：所有"非 200 响应"入口都不得把可重试 5xx 升级为不可重试
// ---------------------------------------------------------------------------

// TestN115_EntryPaths_500With413SubstringStaysRetryable
//
// 覆盖 provider.go 的两处 doStream + 3 个适配器的 MapError（内部 5 个入口，
// 两条实现路径：SimpleClient 直连 与 adapter 分发）。
//
//	收口前：MapError/doStream 里的 `IsContextOverflowError(apiErr.Message)` 读 **Message 散文**，
//
// body 含 "4132" ⇒ 命中裸 413 ⇒ 把 ErrorID 覆盖成 ErrAIFatal
// ⇒ 一次可重试的 500 变成"不可恢复"（用户可见：整轮请求直接失败，不重试）。
//
//	收口后：`apiErr.IsContextOverflow()` 结构化优先 ⇒ ErrorID 保持 ErrAIRetryable。
func TestN115_EntryPaths_500With413SubstringStaysRetryable(t *testing.T) {
	const body = `{"error":{"type":"internal_server_error","message":"upstream unavailable (request-id 4132)"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	type streamer interface {
		Stream(context.Context, Request) (<-chan Event, error)
	}
	cases := []struct {
		name string
		c    streamer
	}{
		{"SimpleClient/Anthropic(doStream)", NewSimpleClient("claude-sonnet-4", "test-key", srv.URL, ProviderAnthropic)},
		{"Adapter/Anthropic(MapError)", NewAnthropicAdapter("claude-sonnet-4", "test-key", srv.URL, ProviderAnthropic)},
		{"Adapter/OpenAI(MapError)", NewOpenAIAdapter("gpt-4o", "test-key", srv.URL, ProviderOpenAI)},
		{"Adapter/DeepSeek(MapError)", NewDeepSeekAdapter("deepseek-chat", "test-key", srv.URL, ProviderDeepSeek)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.c.Stream(context.Background(), Request{
				Model:    "m",
				Messages: []MessageEntry{{Role: "user", Content: "hi"}},
			})
			if err == nil {
				t.Fatal("500 应返回错误")
			}
			if got := GetErrorID(err); got != ErrAIRetryable {
				t.Errorf("ErrorID = %q, want %q —— 收口前 body 里的 \"4132\" 命中裸 413 ⇒ 被覆盖成 %q（不可重试）",
					got, ErrAIRetryable, ErrAIFatal)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// · 口径面：同一状态码在全部分支上必须同源
// ---------------------------------------------------------------------------

// TestN114_SingleStatusAxis_AllJudgmentsAgree
//
// 收口前「同一状态码该不该重试」有四份手写口径（APIError.IsRetryable 的 StatusCode switch /
// IsRetryableError / ReconnectWrapper.ShouldReconnect 的 switch / retry.go 实际行为），
// 字面量集合互不一致：**501/505…599 在 IsRetryableError 为 true、在 APIError.IsRetryable 为 false**。
// 本判据把五个判定函数钉在同一张期望表上 —— 收口后它们全部由
// httpStatusErrorIDSpecs + errorCategorySpecs 推出。
//
// 注：429/529 的 reconnect=true 是本表**唯一故意保留的语义差异**
// （API 层 Fatal 不直接重试 ↔ 传输层重连一次安全），已在 reconnect.go 注释写明。
func TestN114_SingleStatusAxis_AllJudgmentsAgree(t *testing.T) {
	rows := []struct {
		code      int
		retryable bool
		rateLimit bool
		reconnect bool
	}{
		{0, false, false, false},
		{200, false, false, false},
		{302, false, false, false},
		{400, false, false, false},
		{401, false, false, false},
		{403, false, false, false},
		{404, false, false, false},
		{405, false, false, false},
		{408, true, false, true},
		{413, false, false, false},
		{425, true, false, true},
		{429, false, true, true}, // 传输层例外：可重连
		{500, true, false, true},
		{501, true, false, true}, // ★ 收口前两侧相反（函数 true / 方法 false）
		{502, true, false, true},
		{503, true, false, true},
		{504, true, false, true},
		{505, true, false, true},
		{529, false, false, true}, // 传输层例外：可重连
		{599, true, false, true},
	}

	rw := &ReconnectWrapper{}
	for _, r := range rows {
		t.Run(fmt.Sprintf("status_%d", r.code), func(t *testing.T) {
			if got := IsRetryableError(r.code); got != r.retryable {
				t.Errorf("IsRetryableError(%d) = %v, want %v", r.code, got, r.retryable)
			}
			if got := (&APIError{StatusCode: r.code}).IsRetryable(); got != r.retryable {
				t.Errorf("(*APIError{StatusCode:%d}).IsRetryable() = %v, want %v —— 必须与 IsRetryableError 同源",
					r.code, got, r.retryable)
			}
			if got := IsRateLimitError(r.code); got != r.rateLimit {
				t.Errorf("IsRateLimitError(%d) = %v, want %v", r.code, got, r.rateLimit)
			}
			if got := (&APIError{StatusCode: r.code}).IsRateLimit(); got != r.rateLimit {
				t.Errorf("(*APIError{StatusCode:%d}).IsRateLimit() = %v, want %v", r.code, got, r.rateLimit)
			}
			if got := rw.ShouldReconnect(&APIError{StatusCode: r.code}); got != r.reconnect {
				t.Errorf("ShouldReconnect(StatusCode=%d) = %v, want %v", r.code, got, r.reconnect)
			}
		})
	}
}

// TestN114_MapHTTPStatus_UnlistedFallbackIsUnconditionalRetryable
//
// ⚠️ **本条不具判别性**（按 production-path-tdd R4-7 的登记纪律注明）：
// 它钉的是"default 兜底 = 无条件 ErrAIRetryable"这一**既有事实**。收口时删掉的那句
// `if IsRetryableError(statusCode) { return ErrAIRetryable }` 与紧随其后的 return **同值**
// ⇒ 删它是**等价变换**（no-op），本条因此 before/after 两态都绿。
// 它的价值是"防止有人把该分支加回来时误以为它改变了行为"，以及钉住兜底取值本身。
func TestN114_MapHTTPStatus_UnlistedFallbackIsUnconditionalRetryable(t *testing.T) {
	for _, code := range []int{0, 302, 599, 100} {
		if got := MapHTTPStatusToErrorID(code, ""); got != ErrAIRetryable {
			t.Errorf("MapHTTPStatusToErrorID(%d, \"\") = %q, want %q（未登记 ⇒ 保守可重试兜底）", code, got, ErrAIRetryable)
		}
	}
	// provider body type 优先于兜底（同一状态码跨 provider 语义可不同）
	if got := MapHTTPStatusToErrorID(200, "overloaded_error"); got != ErrAIApiOverload {
		t.Errorf("bodyType=overloaded_error 应优先，got %q", got)
	}
	if got := MapHTTPStatusToErrorID(200, "rate_limit_error"); got != ErrAIApiRateLimit {
		t.Errorf("bodyType=rate_limit_error 应优先，got %q", got)
	}
}
