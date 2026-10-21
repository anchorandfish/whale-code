package ai

import "testing"

// TestParseOpenAIDelta_ToolCallInSameChunkAsReasoning 覆盖 kimi-k2.7-code 的真实流式行为：
// 厂商会在【同一帧 delta】里同时下发 reasoning_content 与 tool_calls，
// 而 tool_calls 的 id / name 恰好只出现在这一帧，后续帧只有 arguments 且 id/name 为空字符串。
//
// 修复前：parseOpenAIDeltaWithAccum 见到 reasoning_content 非空就提前 return，
// 导致同帧的 tool_calls 被整个丢弃 → pendingNames/pendingIDs 为空 →
// [DONE] flush 出 Name="" 的 ToolUseEvent → 执行器找不到工具 → 用户看到
// "AI returned an empty response."（finish_reason 仍是 tool_calls，极具误导性）。
func TestParseOpenAIDelta_ToolCallInSameChunkAsReasoning(t *testing.T) {
	// 第 1 帧：reasoning 与 tool_calls（携带 id + name）同帧
	chunk1 := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":" the bash tool.","tool_calls":[{"id":"call_2a992e1e9c394095bbfa9e3a","type":"function","index":0,"function":{"name":"bash","arguments":""}}],"content":""},"finish_reason":null}]}`
	// 第 2 帧：只有 arguments，id / name 为空字符串（该厂商不省略字段）
	chunk2 := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"id":"","type":"function","index":0,"function":{"name":"","arguments":"{\"command\": \"rm /tmp/whale-ab/victim.txt\"}"}}],"content":"","reasoning_content":""},"finish_reason":null}]}`

	pendingArgs := make(map[int]string)
	pendingNames := make(map[int]string)
	pendingIDs := make(map[int]string)

	// 第 1 帧：thinking 仍需产出（不能丢），同时必须累积 id/name
	evts1, _, _ := parseOpenAIDeltaWithAccum(chunk1, pendingArgs, pendingNames, pendingIDs)
	if len(evts1) != 1 {
		t.Fatalf("第 1 帧应产出 1 个事件，got %d 个：%#v", len(evts1), evts1)
	}
	if _, ok := evts1[0].(ThinkingDeltaEvent); !ok {
		t.Errorf("第 1 帧应返回 ThinkingDeltaEvent（thinking 不应丢失），got %#v", evts1[0])
	}
	if pendingNames[0] != "bash" {
		t.Errorf("同帧的 tool_calls 被丢弃：pendingNames[0] = %q, want %q", pendingNames[0], "bash")
	}
	if pendingIDs[0] != "call_2a992e1e9c394095bbfa9e3a" {
		t.Errorf("同帧的 tool_calls 被丢弃：pendingIDs[0] = %q, want %q", pendingIDs[0], "call_2a992e1e9c394095bbfa9e3a")
	}

	// 第 2 帧：累积 arguments
	parseOpenAIDeltaWithAccum(chunk2, pendingArgs, pendingNames, pendingIDs)

	const wantArgs = `{"command": "rm /tmp/whale-ab/victim.txt"}`
	if pendingArgs[0] != wantArgs {
		t.Errorf("pendingArgs[0] = %q, want %q", pendingArgs[0], wantArgs)
	}
	// 关键不变量：flush 时必须能拼出【有名有参】的工具调用
	if pendingNames[0] == "" {
		t.Fatalf("flush 将产出 Name=\"\" 的 ToolUseEvent，执行器必然丢弃该调用")
	}
}

// TestParseOpenAIDelta_PureToolChunkStillSilent 回归保护：
// 纯 tool_calls 帧（无 reasoning、无 content）修复前后都应返回 nil 且只做累积。
func TestParseOpenAIDelta_PureToolChunkStillSilent(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_1","index":0,"function":{"name":"bash","arguments":"{\"c\":1}"}}]}}]}`
	pendingArgs := make(map[int]string)
	pendingNames := make(map[int]string)
	pendingIDs := make(map[int]string)

	evts, _, _ := parseOpenAIDeltaWithAccum(chunk, pendingArgs, pendingNames, pendingIDs)
	if len(evts) != 0 {
		t.Errorf("纯 tool_calls 帧应不产出展示型事件，got %#v", evts)
	}
	if pendingNames[0] != "bash" || pendingArgs[0] != `{"c":1}` {
		t.Errorf("累积错误：name=%q args=%q", pendingNames[0], pendingArgs[0])
	}
}

// TestParseOpenAIDelta_TextChunkUnchanged 回归保护：
// 纯文本帧行为不变（返回 TextDeltaEvent，不产生工具累积）。
func TestParseOpenAIDelta_TextChunkUnchanged(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"content":"hello"}}]}`
	pendingArgs := make(map[int]string)
	pendingNames := make(map[int]string)
	pendingIDs := make(map[int]string)

	evts, _, _ := parseOpenAIDeltaWithAccum(chunk, pendingArgs, pendingNames, pendingIDs)
	if len(evts) != 1 {
		t.Fatalf("纯文本帧应产出 1 个事件，got %d 个：%#v", len(evts), evts)
	}
	if td, ok := evts[0].(TextDeltaEvent); !ok || td.Content != "hello" {
		t.Errorf("文本帧应返回 TextDeltaEvent{hello}，got %#v", evts[0])
	}
	if len(pendingNames) != 0 {
		t.Errorf("文本帧不应产生工具累积，got %v", pendingNames)
	}
}

// TestParseOpenAIDelta_TextAndReasoningSameChunk 覆盖同类隐患的第二处残留：
// "一帧只含一种内容"假设本身。
//
// 修复前 content 非空即提前 return，同帧的 reasoning_content 被静默丢弃。
// 阿里云百炼兼容端点等兼容层在"思考结束 → 正文开始"的切换边界会把两者合进同一帧，
// 且用空字符串而非省略字段表示"这一路没有内容"——因此不能靠字段是否存在来判断。
func TestParseOpenAIDelta_TextAndReasoningSameChunk(t *testing.T) {
	// 同帧同时下发 content 与 reasoning_content（切换边界帧）
	chunk := `{"choices":[{"index":0,"delta":{"content":"正文开头","reasoning_content":"思考尾巴"}}]}`

	pendingArgs := make(map[int]string)
	pendingNames := make(map[int]string)
	pendingIDs := make(map[int]string)

	evts, _, _ := parseOpenAIDeltaWithAccum(chunk, pendingArgs, pendingNames, pendingIDs)

	if len(evts) != 2 {
		t.Fatalf("同帧 content+reasoning 应产出 2 个事件，got %d 个：%#v", len(evts), evts)
	}

	var gotThinking, gotText string
	for _, e := range evts {
		switch evt := e.(type) {
		case ThinkingDeltaEvent:
			gotThinking = evt.Content
		case TextDeltaEvent:
			gotText = evt.Content
		}
	}
	if gotThinking != "思考尾巴" {
		t.Errorf("同帧的 reasoning_content 被丢弃：got %q, want %q", gotThinking, "思考尾巴")
	}
	if gotText != "正文开头" {
		t.Errorf("同帧的 content 被丢弃：got %q, want %q", gotText, "正文开头")
	}
	// thinking 应排在 text 之前，与模型生成顺序一致
	if _, ok := evts[0].(ThinkingDeltaEvent); !ok {
		t.Errorf("事件顺序应为 thinking 先于 text，got %#v 在首位", evts[0])
	}
}

// TestParseOpenAIDelta_EmptyStringMeansNoContent 回归保护：
// 兼容层用空字符串而非省略字段表示"这一路没有内容"，空字符串不得产出事件。
func TestParseOpenAIDelta_EmptyStringMeansNoContent(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"content":"","reasoning_content":""}}]}`

	pendingArgs := make(map[int]string)
	pendingNames := make(map[int]string)
	pendingIDs := make(map[int]string)

	evts, _, _ := parseOpenAIDeltaWithAccum(chunk, pendingArgs, pendingNames, pendingIDs)
	if len(evts) != 0 {
		t.Errorf("全空字符串帧不应产出事件，got %#v", evts)
	}
}
