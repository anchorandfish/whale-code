package ai

import (
	"strings"
	"testing"
)

// TestEstimateMessagesTokens_CountsAllBlockTypes 钉一个**既有缺陷**（2026-10-14 由第 0 批
// 「仪表」暴露）：
//
// EstimateMessageTokens 的 switch 只认 "text" / "input" / "image" / "image_url"，
// 但真实请求里最常见的三种块它一个都不认 ——
//
//	sessionMessagesToAPI（tui）对 RoleTool 产出 {"type":"tool_result","content":…}，
//	对助手产出 {"type":"tool_use",…} 与 {"type":"thinking",…}。
//
// 后果不是"数字不好看"：estimateCurrentInputContextTokens() 的返回值
// 直接喂 ShouldAutoCompact（tui/bridge.go、blocked_handoff.go）⇒
// **工具结果一多，压缩会在真实占用远超阈值时才触发**（可致上下文溢出）。
//
// ★ 判别性：修复前本用例必须红（三种块全部计 0）；修复后绿。
func TestEstimateMessagesTokens_CountsAllBlockTypes(t *testing.T) {
	e := NewTokenEstimator()
	payload := strings.Repeat("x", 8000) // ASCII ⇒ ~2000 tok
	think := strings.Repeat("y", 4000)   // ~1000 tok

	cases := []struct {
		name  string
		entry MessageEntry
		want  int
	}{
		{
			name: "tool_result（RoleTool 消息，最大的那一块）",
			entry: MessageEntry{Role: "tool", Content: []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "t1", "content": payload},
			}},
			want: 2000,
		},
		{
			name: "thinking（推理块）",
			entry: MessageEntry{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": think},
			}},
			want: 1000,
		},
		{
			name: "tool_use（助手工具调用，input 是已反序列化的 JSON 值）",
			entry: MessageEntry{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "t1", "name": "read_file",
					"input": map[string]interface{}{"path": strings.Repeat("z", 8000)}},
			}},
			want: 2000,
		},
	}
	for _, tc := range cases {
		got := e.EstimateMessagesTokens([]MessageEntry{tc.entry})
		if got < tc.want {
			t.Errorf("%s：got=%d, want >=%d —— 该块内容未被计入", tc.name, got, tc.want)
		}
	}
}
