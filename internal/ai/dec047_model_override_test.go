package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDEC047_RequestModelReachesProvider 证明「请求级 model」真的进了 HTTP body。
//
// 判别性（的核心）：
// - 修复前 SimpleClient.Stream 只读 snap.model（= client 全局字段），req.Model 被完全忽略
// ⇒ 本用例 "override" 子用例在 before 态拿到 "global-model"，断言失败。
// - 修复后 req.Model 非空即覆盖快照 ⇒ 拿到 "override-model"。
//
// 第二个子用例是**反向守卫**（R9「一刀切」防线）：req.Model 为空时必须回退全局值，
// 否则「让 override 生效」就变成了「把缺省语义打掉」。
// 它不具判别性（before/after 都绿），作用是防止修复越界。
func TestDEC047_RequestModelReachesProvider(t *testing.T) {
	const globalModel = "global-model"

	cases := []struct {
		name     string
		reqModel string
		want     string
	}{
		{"请求级覆盖生效", "override-model", "override-model"},
		{"空值回退全局（反向守卫）", "", globalModel},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotModel := make(chan string, 1)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var payload map[string]interface{}
				_ = json.Unmarshal(body, &payload)
				m, _ := payload["model"].(string)
				select {
				case gotModel <- m:
				default:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()

			c := NewSimpleClient(globalModel, "test-key", srv.URL, ProviderOpenAI)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ch, err := c.Stream(ctx, Request{
				Model:     tc.reqModel,
				Messages:  []MessageEntry{{Role: "user", Content: "hi"}},
				MaxTokens: 16,
			})
			if err != nil {
				t.Fatalf("Stream 返回错误: %v", err)
			}
			go func() {
				for range ch {
				}
			}()

			select {
			case got := <-gotModel:
				if got != tc.want {
					t.Fatalf("HTTP body 的 model = %q，期望 %q（req.Model=%q，全局=%q）。\n"+
						"⇒ 请求级 model 没有生效：provider 仍在用全局快照。",
						got, tc.want, tc.reqModel, globalModel)
				}
				t.Logf("HTTP body model = %q（req.Model=%q）", got, tc.reqModel)
			case <-time.After(5 * time.Second):
				t.Fatal("5s 内未收到 HTTP 请求 —— Stream 未真正发出")
			}
		})
	}
}
