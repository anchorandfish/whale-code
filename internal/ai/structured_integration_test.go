package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStreamStructured_OpenAIMock 使用 Mock OpenAI server测试结构化输出
func TestStreamStructured_OpenAIMock(t *testing.T) {
	// 构造 OpenAI SSE 格式响应（纯 JSON 文本输出）
	// OpenAI 格式: data: {"choices":[{"delta":{"content":"..."}}]}
	sseResponse := "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"name\\\":\\\"Alice\\\",\\\"age\\\":30}\"}}]}\n\n" +
		"data: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, sseResponse)
		flusher.Flush()
	}))
	defer server.Close()

	client := NewSimpleClient("gpt-4o", "test-key", server.URL, ProviderOpenAI)
	defer client.Close()

	schema := &ResponseSchema{
		Name:        "person",
		Description: "A person object",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string"},
				"age":  map[string]interface{}{"type": "integer"},
			},
			"required": []interface{}{"name", "age"},
		},
		Strict: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.StreamStructured(ctx, Request{
		ResponseSchema: schema,
		Messages:       []MessageEntry{{Role: "user", Content: "Tell me about Alice"}},
	})
	if err != nil {
		t.Fatalf("StreamStructured 返回错误: %v", err)
	}

	if result.ProviderUsed != MethodOpenAINative {
		t.Errorf("ProviderUsed = %q, want %q", result.ProviderUsed, MethodOpenAINative)
	}
	if !result.Validated {
		t.Errorf("Validated = false, want true, errors: %v", result.ValidationErrors)
	}
	if result.Data["name"] != "Alice" {
		t.Errorf("Data['name'] = %v, want 'Alice'", result.Data["name"])
	}
}

// TestStreamStructured_AnthropicMock 使用 Mock Anthropic server测试结构化输出
func TestStreamStructured_AnthropicMock(t *testing.T) {
	// 构造 Anthropic SSE 格式响应（tool_use 方式返回结构化数据）
	sseResponse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"structured_output\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Beijing\\\",\\\"country\\\":\\\"China\\\"}\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, sseResponse)
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4", "test-key", server.URL, ProviderAnthropic)
	defer client.Close()

	schema := &ResponseSchema{
		Name:        "location",
		Description: "A location object",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"city":    map[string]interface{}{"type": "string"},
				"country": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"city", "country"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.StreamStructured(ctx, Request{
		ResponseSchema: schema,
		Messages:       []MessageEntry{{Role: "user", Content: "Where is Beijing?"}},
	})
	if err != nil {
		t.Fatalf("StreamStructured 返回错误: %v", err)
	}

	if result.ProviderUsed != MethodAnthropicToolUse {
		t.Errorf("ProviderUsed = %q, want %q", result.ProviderUsed, MethodAnthropicToolUse)
	}
	if !result.Validated {
		t.Errorf("Validated = false, want true, errors: %v", result.ValidationErrors)
	}
	if result.Data["city"] != "Beijing" {
		t.Errorf("Data['city'] = %v, want 'Beijing'", result.Data["city"])
	}
}

// TestStreamStructured_FallbackMock 使用 Mock server测试 Prompt 降级模式
func TestStreamStructured_FallbackMock(t *testing.T) {
	// 构造 OpenAI 兼容 SSE 格式响应（纯文本 JSON 输出，mock降级模式）
	// 非 OpenAI/Anthropic 的 provider 也走 OpenAI 兼容格式
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)

		// 分块发送 SSE 数据，确保解析器能正确处理
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"```json\\n{\\\"status\\\":\\\"ok\\\",\\\"code\\\":200}\\n```\"}}]}\n\n")
		flusher.Flush()

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	// 使用 OpenAI provider（custom 也走 OpenAI 格式，但用 OpenAI 更稳定）
	client := NewSimpleClient("custom-model", "test-key", server.URL, ProviderOpenAI)
	defer client.Close()

	schema := &ResponseSchema{
		Name:   "response",
		Schema: map[string]interface{}{"type": "object"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.StreamStructured(ctx, Request{
		ResponseSchema: schema,
		Messages:       []MessageEntry{{Role: "user", Content: "Check status"}},
	})
	if err != nil {
		t.Fatalf("StreamStructured 返回错误: %v", err)
	}

	// OpenAI provider 使用 MethodOpenAINative，但 mock 返回的是纯文本 JSON
	// 所以会走 extractJSON 路径
	if result.Data["status"] != "ok" {
		t.Errorf("Data['status'] = %v, want 'ok'", result.Data["status"])
	}
}

// TestStreamStructured_NilSchema 测试 ResponseSchema 为空时返回错误
func TestStreamStructured_NilSchema(t *testing.T) {
	client := NewSimpleClient("gpt-4o", "test-key", "", ProviderOpenAI)
	defer client.Close()

	ctx := context.Background()
	_, err := client.StreamStructured(ctx, Request{})
	if err == nil {
		t.Error("ResponseSchema 为空时should return error")
	}
}

// TestBuildOpenAIBody_WithResponseSchema 测试 OpenAI 请求体包含 response_format
func TestBuildOpenAIBody_WithResponseSchema(t *testing.T) {
	client := NewSimpleClient("gpt-4o", "test-key", "", ProviderOpenAI)
	defer client.Close()

	schema := &ResponseSchema{
		Name:        "test_schema",
		Description: "Test schema",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string"},
			},
		},
		Strict: true,
	}

	req := Request{
		Messages:       []MessageEntry{{Role: "user", Content: "Hello"}},
		ResponseSchema: schema,
		Tools: []ToolDefinition{
			{Name: "some_tool", Description: "Should be skipped", InputSchema: map[string]interface{}{"type": "object"}},
		},
	}

	body := client.buildOpenAIBody(req, client.snapshot())

	// 验证 response_format 存在
	rf, ok := body["response_format"].(map[string]interface{})
	if !ok {
		t.Fatal("body 中缺少 response_format 字段或类型不正确")
	}
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", rf["type"])
	}
	js, _ := rf["json_schema"].(map[string]interface{})
	if js == nil {
		t.Fatal("response_format 中缺少 json_schema 字段")
	}
	if js["name"] != "test_schema" {
		t.Errorf("json_schema.name = %v, want test_schema", js["name"])
	}
	if js["strict"] != true {
		t.Errorf("json_schema.strict = %v, want true", js["strict"])
	}

	// 验证 tools 被跳过（ResponseSchema 设置时不添加 tools）
	if _, ok := body["tools"]; ok {
		t.Error("ResponseSchema 设置时不应添加 tools 字段")
	}
}

// TestBuildAnthropicBody_WithResponseSchema 测试 Anthropic 请求体包含 structured_output 工具
func TestBuildAnthropicBody_WithResponseSchema(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	schema := &ResponseSchema{
		Name:        "test_schema",
		Description: "Test schema",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string"},
			},
		},
	}

	req := Request{
		Messages:       []MessageEntry{{Role: "user", Content: "Hello"}},
		ResponseSchema: schema,
		Tools: []ToolDefinition{
			{Name: "some_tool", Description: "Should be skipped", InputSchema: map[string]interface{}{"type": "object"}},
		},
	}

	body := client.buildAnthropicBody(req, client.snapshot())

	// 验证 tools 包含 structured_output
	tools, ok := body["tools"].([]map[string]interface{})
	if !ok {
		t.Fatal("body 中缺少 tools 字段或类型不正确")
	}
	if len(tools) != 1 {
		t.Fatalf("tools 长度 = %d, want 1", len(tools))
	}
	if tools[0]["name"] != "structured_output" {
		t.Errorf("tools[0].name = %v, want structured_output", tools[0]["name"])
	}

	// 验证 tool_choice
	tc, ok := body["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatal("body 中缺少 tool_choice 字段或类型不正确")
	}
	if tc["type"] != "tool" {
		t.Errorf("tool_choice.type = %v, want tool", tc["type"])
	}
	if tc["name"] != "structured_output" {
		t.Errorf("tool_choice.name = %v, want structured_output", tc["name"])
	}
}

// TestGetStructuredOutputMethod 测试根据 provider 选择结构化输出方式
func TestGetStructuredOutputMethod(t *testing.T) {
	tests := []struct {
		provider string
		want     StructuredOutputMethod
	}{
		{ProviderOpenAI, MethodOpenAINative},
		{ProviderAnthropic, MethodAnthropicToolUse},
		{"custom", MethodFallbackPrompt},
		{"ollama", MethodFallbackPrompt},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			client := NewSimpleClient("test-model", "test-key", "", tt.provider)
			defer client.Close()
			got := client.getStructuredOutputMethod(&ResponseSchema{Name: "test"})
			if got != tt.want {
				t.Errorf("getStructuredOutputMethod() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestExtractStructuredOutputFromToolUse 测试从工具调用中提取结构化输出
func TestExtractStructuredOutputFromToolUse(t *testing.T) {
	t.Run("工具名为 structured_output 且有输入", func(t *testing.T) {
		result, err := extractStructuredOutputFromToolUse("structured_output", `{"key":"value"}`, "")
		if err != nil {
			t.Fatalf("返回意外错误: %v", err)
		}
		if result != `{"key":"value"}` {
			t.Errorf("result = %q, want %q", result, `{"key":"value"}`)
		}
	})

	t.Run("工具名不是 structured_output，从文本提取", func(t *testing.T) {
		result, err := extractStructuredOutputFromToolUse("other_tool", "", `{"key":"value"}`)
		if err != nil {
			t.Fatalf("返回意外错误: %v", err)
		}
		if result != `{"key":"value"}` {
			t.Errorf("result = %q, want %q", result, `{"key":"value"}`)
		}
	})

	t.Run("工具输入为空，从文本提取", func(t *testing.T) {
		result, err := extractStructuredOutputFromToolUse("structured_output", "", `{"fallback":true}`)
		if err != nil {
			t.Fatalf("返回意外错误: %v", err)
		}
		if result != `{"fallback":true}` {
			t.Errorf("result = %q, want %q", result, `{"fallback":true}`)
		}
	})
}
