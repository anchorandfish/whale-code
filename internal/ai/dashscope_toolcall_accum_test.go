package ai

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// 本文件覆盖 DashScope 适配器的 tool_calls 增量累积修复——
// 与 kimi-k2.7-code（openai_delta_reasoning_tool_test.go）同源的"一帧只含一种内容"隐患。
//
// 修复前 parseDashScopeGenerate 对每一帧 tool_calls 直接 emit 一个完整 ToolUseEvent，
// 隐含假设"一帧就带齐 id + name + 完整 arguments"。但 incremental_output=true 时
// arguments 是逐帧分片的，且只有首帧带 id/name（后续帧显式下发空字符串而非省略字段）：
//   - 首帧 arguments 为空 → 被兜底成 "{}"，产出一个参数为空的 bash 调用
//   - 后续帧 name 为空 → 执行器找不到工具而丢弃
// 表现与 kimi 一致："AI returned an empty response."，或更糟——真的执行了空参数命令。

// dashScopeSSE 构造 DashScope SSE 流文本。
func dashScopeSSE(frames []string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("event:result.generate\n")
		b.WriteString("data:")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	b.WriteString("event:result.done\n")
	b.WriteString(`data:{"usage":{"input_tokens":10,"output_tokens":20}}`)
	b.WriteString("\n\n")
	return b.String()
}

func runDashScopeStream(t *testing.T, sse string) []Event {
	t.Helper()

	adapter := NewDashScopeNativeAdapter("qwen3-coder-plus", "sk-test", "https://dashscope.aliyuncs.com", ProviderDashScope)
	req, err := http.NewRequest("POST", "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(sse)),
		Request:    req,
	}

	events, err := adapter.parseDashScopeStream(resp, 0)
	if err != nil {
		t.Fatalf("parseDashScopeStream: %v", err)
	}

	var got []Event
	for e := range events {
		got = append(got, e)
	}
	return got
}

// TestDashScope_ToolCallArgumentsAccumulatedAcrossFrames 核心回归：
// arguments 分片必须跨帧拼合，且只产出一个有名有参的 ToolUseEvent。
func TestDashScope_ToolCallArgumentsAccumulatedAcrossFrames(t *testing.T) {
	frames := []string{
		// 帧 1：带 id + name，arguments 为空
		`{"output":{"text":"","thinking":"","tool_calls":[{"id":"call_a1","index":0,"function":{"name":"bash","arguments":""}}]}}`,
		// 帧 2：只有 arguments 分片，id / name 为空字符串（该兼容层不省略字段）
		`{"output":{"text":"","thinking":"","tool_calls":[{"id":"","index":0,"function":{"name":"","arguments":"{\"command\": "}}]}}`,
		// 帧 3：arguments 续片
		`{"output":{"text":"","thinking":"","tool_calls":[{"id":"","index":0,"function":{"name":"","arguments":"\"rm /tmp/x.txt\"}"}}]}}`,
	}

	got := runDashScopeStream(t, dashScopeSSE(frames))

	var toolUses []ToolUseEvent
	for _, e := range got {
		if tu, ok := e.(ToolUseEvent); ok {
			toolUses = append(toolUses, tu)
		}
	}

	// 修复前这里会得到 3 个残缺事件；修复后必须恰好 1 个完整事件。
	if len(toolUses) != 1 {
		for i, tu := range toolUses {
			t.Logf("toolUse[%d] = %#v", i, tu)
		}
		t.Fatalf("应产出 1 个 ToolUseEvent，实际 %d 个", len(toolUses))
	}

	tu := toolUses[0]
	if tu.Name != "bash" {
		t.Errorf("Name = %q, want %q（空名会导致执行器丢弃该调用）", tu.Name, "bash")
	}
	if tu.ID != "call_a1" {
		t.Errorf("ID = %q, want %q", tu.ID, "call_a1")
	}
	const wantArgs = `{"command": "rm /tmp/x.txt"}`
	if tu.Input != wantArgs {
		t.Errorf("Input = %q, want %q（空参数会被执行成一个无意义的 bash 调用）", tu.Input, wantArgs)
	}
}

// TestDashScope_ToolCallViaChoicesPath 兼容层路径覆盖：
// 部分端点把 tool_calls 放在 output.choices[0].message.tool_calls（原生结构），
// 修复前只查 output.tool_calls，该结构下整个工具调用被静默丢弃。
func TestDashScope_ToolCallViaChoicesPath(t *testing.T) {
	frames := []string{
		`{"output":{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_b1","index":0,"function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}}`,
		`{"output":{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"","index":0,"function":{"name":"","arguments":"\"main.go\"}"}}]}}]}}`,
	}

	got := runDashScopeStream(t, dashScopeSSE(frames))

	var toolUses []ToolUseEvent
	for _, e := range got {
		if tu, ok := e.(ToolUseEvent); ok {
			toolUses = append(toolUses, tu)
		}
	}
	if len(toolUses) != 1 {
		t.Fatalf("choices 路径应产出 1 个 ToolUseEvent，实际 %d 个", len(toolUses))
	}
	if toolUses[0].Name != "read_file" {
		t.Errorf("Name = %q, want %q", toolUses[0].Name, "read_file")
	}
	if toolUses[0].Input != `{"path":"main.go"}` {
		t.Errorf("Input = %q, want %q", toolUses[0].Input, `{"path":"main.go"}`)
	}
}

// TestDashScope_ThinkingAndTextSameFrameUnaffected 回归保护：
// DashScope 的 thinking / text 是两个独立 if（非提前 return），同帧两者都应产出。
func TestDashScope_ThinkingAndTextSameFrameUnaffected(t *testing.T) {
	frames := []string{
		`{"output":{"text":"正文","thinking":"思考"}}`,
	}
	got := runDashScopeStream(t, dashScopeSSE(frames))

	var thinking, text bool
	for _, e := range got {
		switch evt := e.(type) {
		case ThinkingDeltaEvent:
			if evt.Content == "思考" {
				thinking = true
			}
		case TextDeltaEvent:
			if evt.Content == "正文" {
				text = true
			}
		}
	}
	if !thinking || !text {
		t.Errorf("同帧 thinking+text 应两者都产出（thinking=%v text=%v）", thinking, text)
	}
}

// TestDashScope_NoToolCallsEmitsNone 回归保护：纯文本流不应产生任何 ToolUseEvent。
func TestDashScope_NoToolCallsEmitsNone(t *testing.T) {
	frames := []string{
		`{"output":{"text":"你好","thinking":""}}`,
	}
	got := runDashScopeStream(t, dashScopeSSE(frames))

	for _, e := range got {
		if tu, ok := e.(ToolUseEvent); ok {
			t.Errorf("纯文本流不应产出 ToolUseEvent，got %#v", tu)
		}
	}
}
