package ai

import (
	"context"
	"testing"
)

// 本文件为【同类隐患排查探针】：kimi-k2.7-code 的"同帧多内容"bug 修复后，
// 验证其余厂商适配器是否仍假设"一帧只含一种内容"。
// 探针只观测并记录当前行为（t.Logf），不断言、不修改生产代码。

// ---------------------------------------------------------------
// 探针 1：OpenAI 兼容路径 —— 同帧 content 与 reasoning_content 均非空
// ---------------------------------------------------------------

// ProbeOpenAI_TextAndReasoningSameChunk 复现"思考→正文切换帧"。
// 部分兼容层（含阿里云百炼兼容端点）在切换边界会把最后一段 thinking 与
// 第一段正文合并在同一帧下发，而不是拆成两帧。
func ProbeOpenAI_TextAndReasoningSameChunk(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"content":"正文开头","reasoning_content":"思考尾巴"}}]}`

	pendingArgs := make(map[int]string)
	pendingNames := make(map[int]string)
	pendingIDs := make(map[int]string)

	evts, _, fr := parseOpenAIDeltaWithAccum(chunk, pendingArgs, pendingNames, pendingIDs)
	t.Logf("[OpenAI同帧 content+reasoning] 返回 %d 个事件，finishReason=%q", len(evts), fr)
	for i, e := range evts {
		t.Logf("    事件[%d] = %#v", i, e)
	}
}

// ---------------------------------------------------------------
// 探针 2：DashScope 同帧 thinking + text（验证是否为独立 if 而非提前 return）
// ---------------------------------------------------------------

// ProbeDashScope_ThinkingAndTextSameFrame 验证 DashScope 同帧 thinking + text。
func ProbeDashScope_ThinkingAndTextSameFrame(t *testing.T) {
	a := &DashScopeNativeAdapter{}
	ctx := context.Background()

	frame := `{"output":{"text":"正文","thinking":"思考"}}`
	events := make(chan Event, 64)
	a.parseDashScopeGenerate(frame, events, ctx, make(map[int]string), make(map[int]string), make(map[int]string))
	close(events)

	var got []Event
	for e := range events {
		got = append(got, e)
	}
	t.Logf("[DashScope同帧 thinking+text] 产出 %d 个事件（期望 2）:", len(got))
	for _, e := range got {
		t.Logf("    %#v", e)
	}
}

// ---------------------------------------------------------------
// 探针 3：Gemini —— 同一 part 内 text 与 functionCall 并存
// ---------------------------------------------------------------

// ProbeGemini_TextAndFunctionCallSamePart 验证 Gemini 3 的"边说边调用"模式：
// 同一 part 内同时携带 text 与 functionCall 时，text 是否被丢弃。
func ProbeGemini_TextAndFunctionCallSamePart(t *testing.T) {
	a := &GeminiNativeAdapter{}
	ctx := context.Background()

	obj := `{"candidates":[{"content":{"parts":[{"text":"我来执行这个命令","functionCall":{"name":"bash","args":{"command":"ls"}}}]}}]}`
	events := make(chan Event, 64)
	err := a.parseGeminiObject(obj, events, ctx, "gemini-2.5-pro", 0)
	close(events)

	var got []Event
	for e := range events {
		got = append(got, e)
	}
	t.Logf("[Gemini同part text+functionCall] err=%v 产出 %d 个事件:", err, len(got))
	for _, e := range got {
		t.Logf("    %#v", e)
	}
}

// ---------------------------------------------------------------
// 探针 4：Anthropic —— thinking block 占位时 tool_use 的 index 语义
// ---------------------------------------------------------------

// ProbeAnthropic_ToolUseBlockIndex 验证 content_block_delta 的 index 是
// 全局 block 序号（含 thinking block），而非第 N 个 tool_use。
// 子代理路径 tools/agent.go 用 toolCallIndexToID[e.Index] 查找，
// 若 index 语义不匹配则参数累积落空。
func ProbeAnthropic_ToolUseBlockIndex(t *testing.T) {
	// 典型开启 thinking 的响应：block 0 = thinking，block 1 = tool_use
	frames := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"让我查一下"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"bash"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
	}

	for _, f := range frames {
		evt, err := ParseAnthropicEvent(f)
		t.Logf("[Anthropic] %-70s → %#v (err=%v)", truncate(f, 70), evt, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestRunMultiframeProbes 是探针总入口（各 Probe 函数不以 Test 开头，避免与正式测试混淆）。
func TestRunMultiframeProbes(t *testing.T) {
	t.Run("openai_content_plus_reasoning", ProbeOpenAI_TextAndReasoningSameChunk)
	t.Run("dashscope_thinking_plus_text", ProbeDashScope_ThinkingAndTextSameFrame)
	t.Run("gemini_text_plus_functioncall", ProbeGemini_TextAndFunctionCallSamePart)
	t.Run("anthropic_tooluse_block_index", ProbeAnthropic_ToolUseBlockIndex)
}
