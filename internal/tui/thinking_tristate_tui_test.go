package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// 本文件的判据刻意做成**类型无关**：
// 只读「生产链路最终打到线上的请求体」，不引用 SessionModel 内部字段的类型。
// 因此 thinking 从二值 bool 升到三态 *bool 的过程中，**本文件一个字都不用改**
// —— 判据不会跟着实现漂移（这正是本仓反复强调的「判据先用 before 自检」）。

// wireTriPtr 构造三态判定用的 *bool。
func wireTriPtr(v bool) *bool { return &v }

// wireIntPtr 构造 *int。
func wireIntPtr(v int) *int { return &v }

// wireThinkingTri 从**线上请求体**读 enable_thinking 的三态：
// nil = 字段未下发（未指定）· &true = 显式开 · &false = 显式关。
func wireThinkingTri(body map[string]interface{}) *bool {
	raw, ok := body["enable_thinking"]
	if !ok {
		return nil
	}
	b, _ := raw.(bool)
	return &b
}

// wireBodyKeys 返回 body 键集合（排障用）。
func wireBodyKeys(body map[string]interface{}) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// captureThinkingWire 走**完整生产链路**捕获线上请求体：
//
//	settings(cfg) → NewModel(cfg,…) ← TUI 唯一构造入口（档3 的接线点在这里）
//
// → m.buildRequest() ← 真实请求构造
// → ai.SimpleClient.Stream(ctx, req) → 本地假端点（抓到真实 HTTP body）
//
// 为什么不用 mock：本仓纪律「禁止为了让被测代码跑到而手工构造前提」。
// 这里从头到尾都是生产构件，只有**终点**换成了本地端点。
func captureThinkingWire(t *testing.T, cfg *config.Config) map[string]interface{} {
	t.Helper()

	m := NewModel(cfg, newTestClient("test-model"), newTestRegistry())
	m.reassignServices()
	req := m.buildRequest()

	var mu sync.Mutex
	var captured map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		captured = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	// ★ A2：`srv.URL` 是**回环地址 = 非官方端点**，A2 之后 thinking
	// 会被端点降级为 Unknown ⇒ `enable_thinking` 不下发，本判据会从"测 settings 接线"
	// 变成"测端点降级"（假失败）。
	// 处理：用**用户声明**把该模型的能力显式化 —— 这正是 A2 给非官方端点用户的
	// 逃生口①（声明优先于端点门），顺带在本判据里把它验一遍。
	// 本判据的**被测对象仍是"settings 配置是否真的走到线上"**，端点维度另有
	// `internal/ai` 的 TestA2_* 专测。
	// ★ ：声明的模型名必须与**请求实际携带的模型名**一致。
	//
	// 之后 `ai.Request.Model` 会覆盖 client 的 model
	// （`provider.go`：`if req.Model != "" { snap.model = req.Model }`），
	// 而 thinking 能力判定用的正是覆盖后的 `snap.model`
	// （`provider.go`：`ThinkingSupportFor(snap.model, snap.baseURL)`）。
	//
	// 本用例的 `req.Model` 来自 TUI 的 `sessionService.ModelName()` = `test-model`
	// （见上面 `NewModel(cfg, newTestClient("test-model"), …)`）。若仍声明
	// `qwen3.7-flash`，覆盖后永远查不到声明 ⇒ `enable_thinking` 不下发（假失败）。
	// 真实生产里"TUI 配置的模型"与"发请求的模型"本就是同一个名字，此处对齐它。
	ai.RegisterThinkingDeclaration("test-model", ai.ThinkingCapSupported)
	t.Cleanup(ai.ClearThinkingDeclarations)

	client := ai.NewSimpleClient("test-model", "test-key", srv.URL, ai.ProviderOpenAI)
	defer client.Close()

	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}
	for range events {
		// 排空事件通道，确保请求已到达假端点。
	}

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("假端点未收到请求体：生产链路没有把请求发出去")
	}
	return captured
}

// TestSettingsThinkingReachesTheWire 判 **档3：settings.json 里的 thinking 配置真的生效**。
//
// 缺陷（本次修复前）：
// - `config.ThinkingEnabled` 是 `*bool`（三态本来就在），但**全仓零读取点**；
// - TUI 侧 `SessionModel.thinkingEnabled` 也**没有任何初始化点**
// （`lifecycle.go` 只硬编码了 `thinkingBudgetTokens = 10000`）；
// ⇒ 无论 settings 怎么配，请求里 `thinkingEnabled` 恒为 false，服务端按自己默认走。
//
// 三态语义（对齐业界：同类 CLI `ReasoningEffort::None` 是显式枚举变体，
// 同类 CLI 用判别联合 `{type:'disabled'}` 显式构造）：
// - 未配置 ⇒ 不下发 enable_thinking，交给服务端默认；
// - 显式 true ⇒ 下发 true（并带 budget）；
// - 显式 false ⇒ 下发 false（这才是 `/think off` 真正生效的前提）。
func TestSettingsThinkingReachesTheWire(t *testing.T) {
	cases := []struct {
		name       string
		set        func(cfg *config.Config)
		want       *bool
		wantBudget int
	}{
		{
			name: "未配置(nil) ⇒ 不下发，交给服务端默认",
			set:  func(cfg *config.Config) {},
			want: nil,
		},
		{
			name: "true ⇒ 显式开 + 预算生效",
			set: func(cfg *config.Config) {
				cfg.ThinkingEnabled = wireTriPtr(true)
				cfg.ThinkingBudgetTokens = wireIntPtr(2048)
			},
			want:       wireTriPtr(true),
			wantBudget: 2048,
		},
		{
			name: "false ⇒ 显式关（下发 enable_thinking=false）",
			set: func(cfg *config.Config) {
				cfg.ThinkingEnabled = wireTriPtr(false)
			},
			want: wireTriPtr(false),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfig()
			tc.set(cfg)

			body := captureThinkingWire(t, cfg)
			got := wireThinkingTri(body)

			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("期望不下发 enable_thinking，实际下发 %v（body 键=%v）", *got, wireBodyKeys(body))
			case tc.want != nil && got == nil:
				t.Fatalf("期望下发 enable_thinking=%v，实际字段缺失（body 键=%v）", *tc.want, wireBodyKeys(body))
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("enable_thinking = %v, want %v", *got, *tc.want)
			}

			if tc.wantBudget > 0 {
				b, _ := body["thinking_budget"].(float64)
				if int(b) != tc.wantBudget {
					t.Fatalf("thinking_budget = %v, want %d", body["thinking_budget"], tc.wantBudget)
				}
			}
		})
	}
}

// TestFinalizeKeepsThinkingBlock 判 **保留思考**：回答完成后，思考块必须在最终消息里留下。
//
// 缺陷（本次修复前）：finalize 路径把「是否渲染思考」的开关传成客户端开关
// （`m.sessionService.ThinkingEnabled()`，恒 false）⇒ 流式期间能看到思考、
// 回答一结束思考块就凭空消失。
//
// 走生产入口 `handleStreamDone`（真实回合收尾），断言**用户可见的最终转录**。
func TestFinalizeKeepsThinkingBlock(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.SetCurrentSession(session.NewSession())
	m.sessionService.SetState(StateStreaming)

	_ = m.handleStreamDone(streamDoneMsg{
		blocks:   []session.ContentBlock{{Type: "text", Text: "答案正文"}},
		thinking: "先看 A，再看 B。",
		model:    "test-model",
	})

	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))
	if !strings.Contains(transcript, "🧠 Thinking") {
		t.Fatalf("回答完成后思考块被丢弃（用户只看到答案、看不到推理过程）：%q", transcript)
	}
	if !strings.Contains(transcript, "先看 A，再看 B。") {
		t.Fatalf("思考内容未保留在最终消息里：%q", transcript)
	}
	if !strings.Contains(transcript, "答案正文") {
		t.Fatalf("正文不应因保留思考而被挤掉：%q", transcript)
	}
}
