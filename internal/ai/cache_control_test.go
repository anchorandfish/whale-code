package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeCC() map[string]interface{} {
	return map[string]interface{}{"type": "ephemeral"}
}

// testCC returns a default CacheControl for tests (P0-6 统一格式).
func testCC() CacheControl {
	return CacheControl{Type: "ephemeral", TTL: "5m", Scope: ""}
}

func checkCacheControl(t *testing.T, obj map[string]interface{}, expectPresent bool) {
	t.Helper()
	cc, ok := obj["cache_control"]
	if expectPresent && !ok {
		t.Fatal("expected cache_control to be present")
	}
	if !expectPresent && ok {
		t.Fatal("expected cache_control to be absent")
	}
	if ok {
		ccMap, ok := cc.(map[string]interface{})
		if !ok {
			t.Fatal("cache_control should be a map")
		}
		if ccMap["type"] != "ephemeral" {
			t.Errorf("cache_control type should be 'ephemeral', got %v", ccMap["type"])
		}
	}
}

func bodyToJSON(t *testing.T, body map[string]interface{}) string {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	return string(data)
}

func assertNoCacheControlInBody(t *testing.T, bodyJSON string) {
	t.Helper()
	if strings.Contains(bodyJSON, "cache_control") {
		t.Error("body should not contain cache_control")
	}
}

// ---- 组 A：核心注入函数 ----

func TestInjectToolsCacheControl_Multiple(t *testing.T) {
	tools := []map[string]interface{}{
		{"name": "read_file"},
		{"name": "write_file"},
		{"name": "search"},
	}
	injectToolsCacheControl(tools, testCC())
	checkCacheControl(t, tools[0], false)
	checkCacheControl(t, tools[1], false)
	checkCacheControl(t, tools[2], true)
}

func TestInjectToolsCacheControl_Single(t *testing.T) {
	tools := []map[string]interface{}{{"name": "read_file"}}
	injectToolsCacheControl(tools, testCC())
	checkCacheControl(t, tools[0], true)
}

func TestInjectToolsCacheControl_Empty(t *testing.T) {
	tools := []map[string]interface{}{}
	injectToolsCacheControl(tools, testCC())
}

func TestInjectMessagesCacheControl_LastIsUser(t *testing.T) {
	messages := []MessageEntry{
		{Role: "user", Content: "Q1"},
		{Role: "assistant", Content: "A1"},
		{Role: "user", Content: "Q2"},
	}
	result := injectMessagesCacheControl(messages, testCC())

	blocks, ok := result[2].Content.([]map[string]interface{})
	if !ok {
		t.Fatal("content should be converted to blocks array")
	}
	checkCacheControl(t, blocks[len(blocks)-1], true)
}

func TestInjectMessagesCacheControl_LastIsAssistant(t *testing.T) {
	messages := []MessageEntry{
		{Role: "user", Content: "Q1"},
		{Role: "assistant", Content: "A1"},
	}
	result := injectMessagesCacheControl(messages, testCC())

	blocks, ok := result[0].Content.([]map[string]interface{})
	if !ok {
		t.Fatal("user content should be converted to blocks")
	}
	checkCacheControl(t, blocks[len(blocks)-1], true)

	if _, ok := result[1].Content.(string); !ok {
		t.Error("assistant content should remain as string")
	}
}

func TestInjectMessagesCacheControl_NoUser(t *testing.T) {
	messages := []MessageEntry{
		{Role: "system", Content: "SYSTEM"},
		{Role: "assistant", Content: "A1"},
	}
	result := injectMessagesCacheControl(messages, testCC())

	for _, msg := range result {
		if c, ok := msg.Content.(string); !ok {
			t.Error("non-user messages should keep original content type")
		} else {
			_ = c
		}
	}
}

func TestInjectMessagesCacheControl_ContentBlocks(t *testing.T) {
	messages := []MessageEntry{
		{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "text", "text": "part1"},
			map[string]interface{}{"type": "text", "text": "part2"},
		}},
	}
	result := injectMessagesCacheControl(messages, testCC())

	blocks := result[0].Content.([]map[string]interface{})
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	checkCacheControl(t, blocks[0], false)
	checkCacheControl(t, blocks[1], true)
}

func TestInjectMessagesCacheControl_StringToBlocks(t *testing.T) {
	messages := []MessageEntry{
		{Role: "user", Content: "plain text"},
	}
	result := injectMessagesCacheControl(messages, testCC())

	blocks, ok := result[0].Content.([]map[string]interface{})
	if !ok {
		t.Fatal("string content should be converted to blocks")
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "text" {
		t.Errorf("block type should be 'text', got %v", blocks[0]["type"])
	}
	if blocks[0]["text"] != "plain text" {
		t.Errorf("text mismatch: got %v", blocks[0]["text"])
	}
	checkCacheControl(t, blocks[0], true)
}

func TestInjectMessagesCacheControl_AlreadyHasCacheControl(t *testing.T) {
	messages := []MessageEntry{
		{Role: "user", Content: []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          "txt",
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		}},
	}
	result := injectMessagesCacheControl(messages, testCC())

	blocks := result[0].Content.([]map[string]interface{})
	if len(blocks) != 1 {
		t.Errorf("should not add extra blocks when cache_control already present, got %d", len(blocks))
	}
}

func TestInjectMessagesCacheControl_Empty(t *testing.T) {
	messages := []MessageEntry{}
	result := injectMessagesCacheControl(messages, testCC())
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d messages", len(result))
	}
}

// ---- 组 B：boundary condition ----

func TestBuildAnthropicBody_EmptySystem(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "key", "", "anthropic")
	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "Hello"}},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if _, ok := body["system"]; ok {
		t.Error("empty system should not be present in body")
	}
}

func TestBuildAnthropicBody_NoTools(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "key", "", "anthropic")
	req := Request{
		SystemPrompt: []string{"You are helpful."},
		Messages:     []MessageEntry{{Role: "user", Content: "Hello"}},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	if _, ok := body["tools"]; ok {
		t.Error("no tools should mean no tools key in body")
	}
}

func TestBuildAnthropicBody_EmptyMessages(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "key", "", "anthropic")
	req := Request{
		SystemPrompt: []string{"You are helpful."},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	jsonStr := bodyToJSON(t, body)
	if strings.Contains(jsonStr, `"messages"`) {
		t.Error("empty messages should not be included")
	}
}

func TestBuildAnthropicBody_QwenNoCacheControl(t *testing.T) {
	client := NewSimpleClient("qwen-max", "key", "", "openai")
	req := Request{
		SystemPrompt: []string{"You are helpful."},
		Messages:     []MessageEntry{{Role: "user", Content: "Hello"}},
		Tools:        []ToolDefinition{{Name: "read", Description: "Read", InputSchema: map[string]interface{}{}}},
	}
	body := client.buildAnthropicBody(req, client.snapshot())

	// qwen 不支持 cache_control，system 应该是字符串
	sys, ok := body["system"].(string)
	if !ok {
		t.Error("qwen model: system should remain as string")
	}
	if sys != "You are helpful." {
		t.Errorf("system text mismatch: got %s", sys)
	}

	jsonStr := bodyToJSON(t, body)
	assertNoCacheControlInBody(t, jsonStr)
}

// ---- 组 C：JSON 序列化 ----

func TestBuildAnthropicBody_JSONOutput(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "key", "", "anthropic")
	req := Request{
		SystemPrompt: []string{"You are helpful."},
		Messages:     []MessageEntry{{Role: "user", Content: "Hello"}},
		Tools:        []ToolDefinition{{Name: "read_file", Description: "Read", InputSchema: map[string]interface{}{}}},
	}
	body := client.buildAnthropicBody(req, client.snapshot())
	jsonStr := bodyToJSON(t, body)

	if !strings.Contains(jsonStr, `"cache_control"`) {
		t.Error("JSON should contain cache_control")
	}
	if !strings.Contains(jsonStr, `"type":"ephemeral"`) {
		t.Error("JSON should contain ephemeral cache_control type")
	}
	if !strings.Contains(jsonStr, `"system":[{`) {
		t.Error("system should be array format with cache_control")
	}

	requiredFields := []string{`"model"`, `"max_tokens"`, `"stream"`, `"messages"`}
	for _, field := range requiredFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestBuildAnthropicBody_JSONWithoutCache(t *testing.T) {
	client := NewSimpleClient("qwen-max", "key", "", "openai")
	req := Request{
		SystemPrompt: []string{"You are helpful."},
		Messages:     []MessageEntry{{Role: "user", Content: "Hello"}},
	}
	body := client.buildAnthropicBody(req, client.snapshot())
	jsonStr := bodyToJSON(t, body)

	assertNoCacheControlInBody(t, jsonStr)
}

// ---- 组 D：能力探测 ----

func TestSupportsCacheControl_ClaudeModels(t *testing.T) {
	models := []string{
		"claude-sonnet-4-20250514",
		"claude-opus-4",
		"claude-3-5-sonnet-20241022",
		"claude-unknown-future",
	}
	for _, m := range models {
		client := NewSimpleClient(m, "key", "", "anthropic")
		if !client.SupportsCacheControl() {
			t.Errorf("%s should support cache_control", m)
		}
	}
}

func TestSupportsCacheControl_QwenModels(t *testing.T) {
	models := []string{
		"qwen-max",
		"qwen-plus",
		"qwen3-235b-a22b",
		"qwen3-unknown",
	}
	for _, m := range models {
		client := NewSimpleClient(m, "key", "", "openai")
		if client.SupportsCacheControl() {
			t.Errorf("%s should NOT support cache_control", m)
		}
	}
}

func TestSupportsCacheControl_GenericModel(t *testing.T) {
	client := NewSimpleClient("gpt-4", "key", "", "openai")
	if client.SupportsCacheControl() {
		t.Error("gpt-4 should NOT support cache_control")
	}
}

func TestSetModel_UpdatesCacheSupport(t *testing.T) {
	client := NewSimpleClient("qwen-max", "key", "", "openai")
	if client.SupportsCacheControl() {
		t.Error("qwen-max should not support cache_control initially")
	}

	client.SetModel("claude-sonnet-4")
	if !client.SupportsCacheControl() {
		t.Error("claude-sonnet-4 should support cache_control after SetModel")
	}

	client.SetModel("gpt-4")
	if client.SupportsCacheControl() {
		t.Error("gpt-4 should not support cache_control")
	}
}

// ---- integration test ----

func TestIntegration_ClaudeModelInjectsCacheControl(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\"}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4", "test-key", server.URL, "anthropic")
	req := Request{
		SystemPrompt: []string{"You are a helpful assistant."},
		Messages: []MessageEntry{
			{Role: "user", Content: "Say hello"},
		},
		Tools: []ToolDefinition{
			{Name: "read_file", Description: "Read a file", InputSchema: map[string]interface{}{}},
		},
	}

	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	for range events {
	}

	if len(capturedBody) == 0 {
		t.Fatal("no request body captured")
	}

	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("JSON parse failed: %v", err)
	}

	// 验证 system
	sys, ok := body["system"].([]interface{})
	if !ok {
		t.Fatal("system should be array")
	}
	sysBlock := sys[0].(map[string]interface{})
	if _, ok := sysBlock["cache_control"]; !ok {
		t.Error("system should have cache_control")
	}

	// 验证 tools
	tools, _ := body["tools"].([]interface{})
	lastTool := tools[len(tools)-1].(map[string]interface{})
	if _, ok := lastTool["cache_control"]; !ok {
		t.Error("last tool should have cache_control")
	}

	// 验证 messages
	msgs, _ := body["messages"].([]interface{})
	lastMsg := msgs[len(msgs)-1].(map[string]interface{})
	content, _ := lastMsg["content"].([]interface{})
	lastBlock := content[len(content)-1].(map[string]interface{})
	if _, ok := lastBlock["cache_control"]; !ok {
		t.Error("last message content block should have cache_control")
	}
}

func TestIntegration_QwenModelNoCacheControl(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewSimpleClient("qwen-max", "test-key", server.URL, "openai")
	req := Request{
		SystemPrompt: []string{"You are a helpful assistant."},
		Messages: []MessageEntry{
			{Role: "user", Content: "Say hello"},
		},
	}

	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	for range events {
	}

	assertNoCacheControlInBody(t, string(capturedBody))
}

func TestIntegration_MultipleMessageWithCacheControl(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4", "test-key", server.URL, "anthropic")
	req := Request{
		SystemPrompt: []string{"You are helpful."},
		Messages: []MessageEntry{
			{Role: "user", Content: "Q1"},
			{Role: "assistant", Content: "A1"},
			{Role: "user", Content: "Q2"},
		},
	}

	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	for range events {
	}

	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("JSON parse failed: %v", err)
	}

	msgs, _ := body["messages"].([]interface{})
	// 最后一条消息 Q2 应该有 cache_control
	lastMsg := msgs[len(msgs)-1].(map[string]interface{})
	content, _ := lastMsg["content"].([]interface{})
	lastBlock := content[len(content)-1].(map[string]interface{})
	if _, ok := lastBlock["cache_control"]; !ok {
		t.Error("last user message should have cache_control")
	}
}
