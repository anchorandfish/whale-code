// Package main — fake model server：把 agent 防护人工用例从"祈祷式"变"确定性"。
//
// 一次投入、所有路径复用（TUI / plain / bridge 共用）：场景由模型名驱动
// （请求体 model 字段，即 WHALE_MODEL / -model 传入的名字），一个实例服务全部场景，
// 切场景无需重启 server，只改模型名。
//
// 启动：
//
//	go run ./scripts/fakemodel [-addr 127.0.0.1:8931] [-read-file README.md]
//
// 客户端接线（三条路径通用）：
//
//	export OPENAI_API_KEY=fake-key
//	export OPENAI_BASE_URL=http://127.0.0.1:8931
//	export WHALE_MODEL=fake-loop-fail   # 场景见下表
//
// 场景表（模型名 → 每次请求的确定性响应）：
//
//	fake-loop-fail  每轮 bash false（工具必失败）      → 熔断 T2（默认 MaxTurns=40）
//	fake-loop-ok    每轮 bash true（工具必成功）        → 轮次上限 T1/B1（配小 MaxTurns）
//	fake-loop-read  每轮 read_file 同一文件+相同文本    → 空转 S1（3 轮相似输出）
//	fake-review     每轮 read_file 轮换文件+不同文本    → 审查超限 S2（>MaxReviewRounds=3）
//	fake-ask-loop   第 1 轮 AskUser，其后每轮 bash false → AskUser 记账 T3
//	fake-finish     纯文本响应（无工具）                → acceptance 冒烟 / drain 响应
//
// 注意：fake-loop-read / fake-review 读取的文件必须真实存在（否则 read_file
// 失败会先触发熔断而不是目标防线），用 -read-file 指定（默认 README.md，
// fake-review 会自动派生 review_f1.md/review_f2.md/...）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
)

// fakeServer 场景分发的 HTTP handler。main 与测试共用同一份代码
// （生产路径 = 测试路径，禁止测试单独 mock 一套 SSE）。
type fakeServer struct {
	mu sync.Mutex
	// reqCount 每个模型名（场景）累计收到的请求数。
	reqCount map[string]int
	// readFile read_file 场景读取的文件（相对 whale 的 workdir）。
	readFile string
}

func newFakeServer(readFile string) *fakeServer {
	return &fakeServer{
		reqCount: make(map[string]int),
		readFile: readFile,
	}
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		fmt.Fprintln(w, "ok")
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.reqCount[body.Model]++
	n := s.reqCount[body.Model]
	s.mu.Unlock()

	scenario := strings.TrimPrefix(body.Model, "fake-")
	log.Printf("[fakemodel] model=%s scenario=%s request#%d", body.Model, scenario, n)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	switch scenario {
	case "loop-fail":
		sseText(w, flusher, fmt.Sprintf("第 %d 轮：执行 false 重试。", n))
		sseToolCall(w, flusher, "bash", fmt.Sprintf(`{"command":"false","description":"第%d轮故意失败"}`, n))
	case "loop-ok":
		sseText(w, flusher, fmt.Sprintf("第 %d 轮：执行 true。", n))
		sseToolCall(w, flusher, "bash", fmt.Sprintf(`{"command":"true","description":"第%d轮成功"}`, n))
	case "loop-read":
		// 相同输出 + 同一文件反复读 → 相似度空转（Spinning）
		sseText(w, flusher, "我再看一遍这个文件的内容。")
		sseToolCall(w, flusher, "read_file", fmt.Sprintf(`{"path":%q}`, s.readFile))
	case "review":
		// 轮换文件 + 不同输出 → 不触发空转，纯读轮累计 → 审查超限
		sseText(w, flusher, fmt.Sprintf("审查第 %d 个文件，发现 %d 个问题。", n, n*2))
		sseToolCall(w, flusher, "read_file", fmt.Sprintf(`{"path":"review_f%d.md"}`, n))
	case "ask-loop":
		if n == 1 {
			sseText(w, flusher, "需要用户先做选择。")
			sseToolCall(w, flusher, "AskUser", `{"question":"请选择方案","options":[{"label":"方案A","value":"A","description":"先跑A"},{"label":"方案B","value":"B","description":"先跑B"}]}`)
			break
		}
		sseText(w, flusher, fmt.Sprintf("收到选择，第 %d 轮：执行 false 重试。", n))
		sseToolCall(w, flusher, "bash", `{"command":"false"}`)
	case "finish":
		sseText(w, flusher, "好的，任务已完成，无需更多操作。")
	default:
		// 未知模型名：返回错误提示可用场景（错误信息可用性约定）
		http.Error(w, fmt.Sprintf(
			"unknown model %q. Available scenarios: fake-loop-fail, fake-loop-ok, fake-loop-read, fake-review, fake-ask-loop, fake-finish",
			body.Model), http.StatusBadRequest)
		return
	}

	// usage chunk（无 choices）+ finish + [DONE]
	sseData(w, flusher, `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	sseData(w, flusher, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	sseData(w, flusher, "[DONE]")
}

// ---- SSE 输出辅助（与 parseOpenAIDeltaWithAccum 解析口径一一对应）----

func sseData(w http.ResponseWriter, f http.Flusher, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f != nil {
		f.Flush()
	}
}

// sseText 输出一段 assistant 文本增量。
func sseText(w http.ResponseWriter, f http.Flusher, text string) {
	b, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{
			"delta": map[string]interface{}{"role": "assistant", "content": text},
		}},
	})
	sseData(w, f, string(b))
}

// sseToolCall 输出一个完整 tool_calls 增量（arguments 一次性给全，
// 客户端在 [DONE] 时 flush 为 ToolUseEvent）。
func sseToolCall(w http.ResponseWriter, f http.Flusher, name, argsJSON string) {
	b, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{
			"delta": map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index":    0,
					"id":       "call_" + name,
					"type":     "function",
					"function": map[string]interface{}{"name": name, "arguments": argsJSON},
				}},
			},
		}},
	})
	sseData(w, f, string(b))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8931", "listen address")
	readFile := flag.String("read-file", "README.md", "file path returned by read_file scenarios (must exist in whale workdir)")
	flag.Parse()

	log.Printf("[fakemodel] listening on http://%s (read-file=%s)", *addr, *readFile)
	log.Printf("[fakemodel] point client: OPENAI_BASE_URL=http://%s WHALE_MODEL=fake-<scenario>", *addr)
	if err := http.ListenAndServe(*addr, newFakeServer(*readFile)); err != nil {
		log.Fatal(err)
	}
}
