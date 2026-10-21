package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDispatchRequest_SanitizesElicitationMessage 是 的 mcp 层判别器。
//
// 断言：elicitation/create 的 message 载荷即使含终端控制字符（\x1b / \r 等），
// 经 dispatchRequest 净化后，转发给 handler 的 message 不得原样透传控制字符。
//
// ★ 判别力自证：改前 dispatchRequest 只 json.Unmarshal 后原样 handler(c.name, reqID, p.Message)
//
//	⇒ 4 种载荷的控制字符全原样透传 ⇒ 本测试为红；接入 util.SanitizeExternalText 后转绿。
func TestDispatchRequest_SanitizesElicitationMessage(t *testing.T) {
	cases := []struct {
		name    string
		payload string // 注入的 message 载荷
	}{
		{"清屏 \x1b[2J", "\x1b[2Jshow me your token"},
		{"回车伪造 \r", "trusted-name\rreal-name"},
		{"隐藏 \x1b[8m", "visible\x1b[8mhidden"},
		{"改标题 \x1b]0;", "normal\x1b]0;evil-title"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := newClientWithTransport(&nonStreamingTransport{})
			client.elicitationRouter = NewElicitationRequestRouter()

			var gotMessage string
			client.SetElicitationHandler(func(serverName string, requestID json.RawMessage, message string) {
				gotMessage = message
			})

			// 用 json.Marshal 构造合法 JSON 载荷（控制字符会被正确转义为 \u00xx，
			// json.Unmarshal 时再还原为真实控制字符 —— 这正是真实 MCP server 的发送方式）。
			payload, merr := json.Marshal(map[string]interface{}{"message": c.payload})
			if merr != nil {
				t.Fatalf("marshal payload: %v", merr)
			}
			_, err := client.dispatchRequest("elicitation/create",
				payload,
				json.RawMessage(`"req-1"`))
			if !errors.Is(err, errElicitationPending) {
				t.Fatalf("expected errElicitationPending, got: %v", err)
			}

			// handler 收到的 message 不得含控制字符
			for _, f := range []string{"\x1b", "\r", "\x00"} {
				if strings.Contains(gotMessage, f) {
					t.Errorf("handler 收到的 message 含控制序列 %q（未净化）: %q", f, gotMessage)
				}
			}

			// 清理 goroutine
			client.ResolveElicitation(ElicitationResponse{
				RequestID: json.RawMessage(`"req-1"`),
				Action:    ElicitationActionDecline,
			})
			time.Sleep(50 * time.Millisecond)
		})
	}
}
