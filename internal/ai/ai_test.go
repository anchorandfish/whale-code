package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEstimateTextTokens(t *testing.T) {
	estimator := NewTokenEstimator()

	tests := []struct {
		name     string
		input    string
		minToken int
		maxToken int
	}{
		{"空字符串", "", 0, 0},
		{"ASCII", "hello world", 2, 5},
		{"中文", "你好世界", 2, 5},
		{"混合", "hello 你好", 2, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimator.EstimateTextTokens(tt.input)
			if tt.minToken > 0 && result < tt.minToken {
				t.Errorf("EstimateTextTokens(%q) = %d, want >= %d", tt.input, result, tt.minToken)
			}
			if tt.maxToken > 0 && result > tt.maxToken {
				t.Errorf("EstimateTextTokens(%q) = %d, want <= %d", tt.input, result, tt.maxToken)
			}
		})
	}
}

func TestIsCJK(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'中', true},
		{'あ', true},
		{'ア', true},
		{'한', true},
		{'a', false},
		{'1', false},
		{' ', false},
	}

	for _, tt := range tests {
		if got := isCJK(tt.r); got != tt.want {
			t.Errorf("isCJK(%q) = %v, want %v", tt.r, got, tt.want)
		}
	}
}

func TestTruncateToTokenLimit(t *testing.T) {
	estimator := NewTokenEstimator()

	short := "hello"
	result := estimator.TruncateToTokenLimit(short, 100)
	if result != short {
		t.Errorf("短文本不应被截断")
	}

	long := ""
	for i := 0; i < 10000; i++ {
		long += "hello world "
	}
	result = estimator.TruncateToTokenLimit(long, 100)
	if result == long {
		t.Errorf("长文本应被截断")
	}
	estimated := estimator.EstimateTextTokens(result)
	if estimated > 120 {
		t.Errorf("截断后 token 数 %d 超过限制 120", estimated)
	}
}

// TestSpecForModel 验证模糊匹配器仍工作（D 批 2026-10-05：定价字段已删，
// 该表退化为「模型规格表」——只回答上下文窗口 / 最大输出，不再回答价格。
// 本用例钉住匹配器本身：exact 命中、别名前缀命中、未知返回 nil。）
func TestSpecForModel(t *testing.T) {
	if s := SpecForModel("gpt-4o"); s == nil {
		t.Error("SpecForModel(gpt-4o) should not be nil")
	}

	if s := SpecForModel("claude-sonnet-4-20250514"); s == nil {
		t.Error("SpecForModel(claude-sonnet-4) should not be nil")
	}

	if s := SpecForModel("nonexistent"); s != nil {
		t.Error("SpecForModel(nonexistent) should be nil")
	}
}

func TestContextWindowForModel(t *testing.T) {
	window := ContextWindowForModel("claude-sonnet-4-20250514")
	if window != 200000 {
		t.Errorf("ContextWindowForModel(claude-sonnet-4) = %d, want 200000", window)
	}

	window = ContextWindowForModel("unknown")
	if window != 200000 {
		t.Errorf("未知模型should return默认 200000，got %d", window)
	}
}

func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"context overflow detected", true},
		{"prompt too long: 300000 tokens", true},
		{"413 Request Entity Too Large", true},
		{"normal error", false},
		{"rate limit exceeded", false},
	}

	for _, tt := range tests {
		if got := IsContextOverflowError(tt.msg); got != tt.want {
			t.Errorf("IsContextOverflowError(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestIsOverloadedError(t *testing.T) {
	if !IsOverloadedError("529 Overloaded") {
		t.Error("应检测到 529 过载错误")
	}
	if IsOverloadedError("normal error") {
		t.Error("不应检测普通错误为过载")
	}
}

func TestCalculateBackoff(t *testing.T) {
	cfg := DefaultRetryConfig()

	delay0 := CalculateBackoff(0, cfg, 0)
	if delay0 < 0 {
		t.Errorf("退避时间不应为负: %v", delay0)
	}

	delay5 := CalculateBackoff(5, cfg, 0)
	maxWithJitter := time.Duration(float64(cfg.MaxDelay) * 1.25)
	if delay5 > maxWithJitter {
		t.Errorf("退避时间不应超过最大延迟+jitter: %v > %v", delay5, maxWithJitter)
	}
}

func TestFallbackTracker(t *testing.T) {
	tracker := NewFallbackTracker("claude-sonnet-4", "claude-3-5-haiku")

	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Errorf("初始模型应为 claude-sonnet-4")
	}

	tracker.Record529()
	tracker.Record529()
	if tracker.IsDegraded() {
		t.Error("2 次 529 不应降级")
	}

	tracker.Record529()
	if !tracker.IsDegraded() {
		t.Error("3 次 529 应降级")
	}
	if tracker.ActiveModel() != "claude-3-5-haiku" {
		t.Errorf("降级后应使用备用模型")
	}

	tracker.Restore()
	if tracker.IsDegraded() {
		t.Error("恢复后不应降级")
	}
}

func TestNewSimpleClient(t *testing.T) {
	client := NewSimpleClient("test-model", "test-key", "", ProviderAnthropic)
	if client.Model() != "test-model" {
		t.Errorf("Model() = %s, want test-model", client.Model())
	}

	client.SetModel("new-model")
	if client.Model() != "new-model" {
		t.Errorf("SetModel 后 Model() = %s, want new-model", client.Model())
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close() 返回错误: %v", err)
	}
}

// TestRequestStructure 验证 Request 结构体字段及 JSON 序列化/反序列化
func TestRequestStructure(t *testing.T) {
	req := Request{
		SystemPrompt: []string{"You are a helpful assistant"},
		Messages: []MessageEntry{
			{Role: "user", Content: "Hello"},
		},
		MaxTokens: 4096,
		Tools:     []ToolDefinition{{Name: "test_tool", Description: "A test tool", InputSchema: map[string]interface{}{"type": "object"}}},
		Thinking:  &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 5000},
		Model:     "claude-sonnet-4-20250514",
	}

	// 序列化
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("序列化 Request 失败: %v", err)
	}

	// 反序列化验证
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("反序列化 JSON 失败: %v", err)
	}

	// 验证各字段存在
	if _, ok := decoded["system_prompt"]; !ok {
		t.Error("JSON 中缺少 system_prompt 字段")
	}
	if _, ok := decoded["messages"]; !ok {
		t.Error("JSON 中缺少 messages 字段")
	}
	if _, ok := decoded["max_tokens"]; !ok {
		t.Error("JSON 中缺少 max_tokens 字段")
	}
	if _, ok := decoded["tools"]; !ok {
		t.Error("JSON 中缺少 tools 字段")
	}
	if _, ok := decoded["thinking"]; !ok {
		t.Error("JSON 中缺少 thinking 字段")
	}
	if _, ok := decoded["model"]; !ok {
		t.Error("JSON 中缺少 model 字段")
	}
	// A4：effort_level 是死字段——四家厂商（OpenAI/Anthropic/Gemini/百炼）都不识别，
	// 全仓无任何 buildXxxBody 读取它。删除后此处改为「必须不存在」，防止有人重新加回一个没人读的字段。
	// 这是 A4 的第二道锁：它曾断言存在（假绿），正是这个假绿让死字段活了下来。
	if _, ok := decoded["effort_level"]; ok {
		t.Error("JSON 不应包含 effort_level 字段（A4：死字段已删除，见 internal/ai/client.go）")
	}

	// 验证 max_tokens 值
	if mt, _ := decoded["max_tokens"].(float64); int(mt) != 4096 {
		t.Errorf("max_tokens = %v, want 4096", mt)
	}

	// 验证 model 值
	if m, _ := decoded["model"].(string); m != "claude-sonnet-4-20250514" {
		t.Errorf("model = %v, want claude-sonnet-4-20250514", m)
	}

	// 反序列化回 Request 结构体
	var req2 Request
	if err := json.Unmarshal(data, &req2); err != nil {
		t.Fatalf("反序列化回 Request 失败: %v", err)
	}
	if req2.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", req2.MaxTokens)
	}
	if req2.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %s, want claude-sonnet-4-20250514", req2.Model)
	}
}

// TestRequestHasNoEffortLevelField 是 A4 的**结构面锁**，比「JSON 不含 effort_level」更强：
// 若有人把字段加回去却从未赋值，`omitempty` 会把它从 JSON 里藏起来 ⇒ JSON 断言测不到；
// 而字段存在性检查（reflect）无论如何都会红。防止死字段以「看起来接了线」的形式复活。
func TestRequestHasNoEffortLevelField(t *testing.T) {
	if f, ok := reflect.TypeOf(Request{}).FieldByName("EffortLevel"); ok {
		t.Errorf("ai.Request 不应再有 EffortLevel 字段（A4 已删），但发现它，json tag=%q", f.Tag.Get("json"))
	}
}

// TestMessageEntryFormats 测试 MessageEntry Content 字段支持字符串和 []interface{} 两种格式
func TestMessageEntryFormats(t *testing.T) {
	// 字符串内容
	msg1 := MessageEntry{Role: "user", Content: "Hello"}
	data1, err := json.Marshal(msg1)
	if err != nil {
		t.Fatalf("序列化字符串 Content 失败: %v", err)
	}
	var decoded1 map[string]interface{}
	if err := json.Unmarshal(data1, &decoded1); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	if content, _ := decoded1["content"].(string); content != "Hello" {
		t.Errorf("字符串 content = %v, want Hello", content)
	}

	// []interface{} 内容（content block 数组）
	msg2 := MessageEntry{
		Role: "assistant",
		Content: []interface{}{
			map[string]interface{}{"type": "text", "text": "Hi there"},
			map[string]interface{}{"type": "tool_use", "id": "tool_1", "name": "read_file"},
		},
	}
	data2, err := json.Marshal(msg2)
	if err != nil {
		t.Fatalf("序列化 []interface{} Content 失败: %v", err)
	}
	var decoded2 map[string]interface{}
	if err := json.Unmarshal(data2, &decoded2); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	contentArr, ok := decoded2["content"].([]interface{})
	if !ok {
		t.Fatal("content should be array")
	}
	if len(contentArr) != 2 {
		t.Errorf("content 数组长度 = %d, want 2", len(contentArr))
	}
}

// TestEventTypes 测试所有 7 种事件类型的 eventType() 返回值
func TestEventTypes(t *testing.T) {
	tests := []struct {
		name     string
		event    Event
		wantType string
	}{
		{"TextDeltaEvent", TextDeltaEvent{Content: "hello"}, "text_delta"},
		{"ThinkingDeltaEvent", ThinkingDeltaEvent{Content: "thinking..."}, "thinking_delta"},
		{"ToolUseEvent", ToolUseEvent{ID: "1", Name: "test"}, "tool_use"},
		{"ToolInputDeltaEvent", ToolInputDeltaEvent{PartialJSON: `{"key":`}, "tool_input_delta"},
		{"UsageEvent", UsageEvent{InputTokens: 100, OutputTokens: 50}, "usage"},
		{"MessageStopEvent", MessageStopEvent{}, "message_stop"},
		{"ErrorEvent", ErrorEvent{Err: fmt.Errorf("test error")}, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.eventType(); got != tt.wantType {
				t.Errorf("eventType() = %q, want %q", got, tt.wantType)
			}
		})
	}
}

// TestToolDefinition 验证 ToolDefinition 结构体字段及 JSON 标签
func TestToolDefinition(t *testing.T) {
	tool := ToolDefinition{
		Name:        "read_file",
		Description: "Read a file from disk",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
		},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("序列化 ToolDefinition 失败: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	// 验证 JSON 标签：name, description, input_schema
	if _, ok := decoded["name"]; !ok {
		t.Error("JSON 中缺少 name 字段")
	}
	if _, ok := decoded["description"]; !ok {
		t.Error("JSON 中缺少 description 字段")
	}
	if _, ok := decoded["input_schema"]; !ok {
		t.Error("JSON 中缺少 input_schema 字段")
	}

	if name, _ := decoded["name"].(string); name != "read_file" {
		t.Errorf("name = %v, want read_file", name)
	}
}

// TestThinkingConfig 验证 ThinkingConfig 的三态语义。
//
// 本次修复的核心：二值时代「显式关闭」在**类型上不可表达**（Enabled=false 同时
// 意味着"关"和"未指定"），于是 JSON 里根本出不来 `enabled:false`；
// 三态化后未指定 = nil（不下发）、显式关闭 = 指向 false（必须下发）。
func TestThinkingConfig(t *testing.T) {
	// 1) 未指定（nil）→ enabled 与 budget_tokens 都不序列化（不下发，交给服务端默认）
	unsetData, err := json.Marshal(ThinkingConfig{})
	if err != nil {
		t.Fatalf("序列化 ThinkingConfig 失败: %v", err)
	}
	var unsetDecoded map[string]interface{}
	if err := json.Unmarshal(unsetData, &unsetDecoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	if _, ok := unsetDecoded["enabled"]; ok {
		t.Error("未指定（Enabled=nil）时，enabled 不应出现在 JSON 中")
	}
	if _, ok := unsetDecoded["budget_tokens"]; ok {
		t.Error("未指定且 BudgetTokens=0 时，budget_tokens 不应出现在 JSON 中")
	}

	// 2) 显式关闭 → enabled:false 必须出现（这正是二值时代被丢掉的取值）
	offData, err := json.Marshal(ThinkingConfig{Enabled: ThinkingPtr(false)})
	if err != nil {
		t.Fatalf("序列化 ThinkingConfig 失败: %v", err)
	}
	var offDecoded map[string]interface{}
	if err := json.Unmarshal(offData, &offDecoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	offVal, ok := offDecoded["enabled"]
	if !ok {
		t.Fatal("显式关闭时 enabled 必须序列化为 false（否则「关」在类型上不可表达）")
	}
	if b, _ := offVal.(bool); b {
		t.Errorf("enabled = %v, want false", offVal)
	}
	if _, ok := offDecoded["budget_tokens"]; ok {
		t.Error("显式关闭且 BudgetTokens=0 时，budget_tokens 不应出现在 JSON 中")
	}

	// 3) 显式开启 + 预算 → 两者都出现
	onData, err := json.Marshal(ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 5000})
	if err != nil {
		t.Fatalf("序列化 ThinkingConfig 失败: %v", err)
	}
	var onDecoded map[string]interface{}
	if err := json.Unmarshal(onData, &onDecoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	if b, _ := onDecoded["enabled"].(bool); !b {
		t.Errorf("enabled = %v, want true", onDecoded["enabled"])
	}
	if _, ok := onDecoded["budget_tokens"]; !ok {
		t.Error("显式开启且 BudgetTokens=5000 时，budget_tokens 应出现在 JSON 中")
	}
}

// TestNewSimpleClient_Anthropic 创建 Anthropic client，验证 baseURL default value和 httpClient 初始化
func TestNewSimpleClient_Anthropic(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	if client.baseURL != "https://api.anthropic.com" {
		t.Errorf("Anthropic baseURL = %q, want https://api.anthropic.com", client.baseURL)
	}
	if client.httpClient == nil {
		t.Error("httpClient 不should be nil")
	}
	if client.provider != ProviderAnthropic {
		t.Errorf("provider = %q, want %q", client.provider, ProviderAnthropic)
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close() 返回错误: %v", err)
	}
}

// TestNewSimpleClient_OpenAI 创建 OpenAI client，验证 baseURL default value
func TestNewSimpleClient_OpenAI(t *testing.T) {
	client := NewSimpleClient("gpt-4o", "test-key", "", ProviderOpenAI)
	if client.baseURL != "https://api.openai.com" {
		t.Errorf("OpenAI baseURL = %q, want https://api.openai.com", client.baseURL)
	}
	if client.httpClient == nil {
		t.Error("httpClient 不should be nil")
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close() 返回错误: %v", err)
	}
}

// TestBuildAnthropicBody 测试 Anthropic 请求体构建
// 验证 system/tools/messages 三个位置的 cache_control 注入
func TestBuildAnthropicBody(t *testing.T) {
	client := NewSimpleClient("claude-sonnet-4", "test-key", "", ProviderAnthropic)
	defer client.Close()

	req := Request{
		SystemPrompt: []string{"You are helpful"},
		Messages: []MessageEntry{
			{Role: "user", Content: "Hello"},
		},
		MaxTokens: 8192,
		Tools: []ToolDefinition{
			{Name: "read_file", Description: "Read file", InputSchema: map[string]interface{}{"type": "object"}},
		},
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 5000},
	}

	body := client.buildAnthropicBody(req, client.snapshot())

	// 验证 system 字段：claude 模型应注入 cache_control，system 转为 content blocks 数组
	sysBlocks, ok := body["system"].([]map[string]interface{})
	if !ok || len(sysBlocks) != 1 {
		t.Errorf("system should be array of 1 content block with cache_control, got %v", body["system"])
	} else {
		if sysBlocks[0]["type"] != "text" {
			t.Errorf("system block type = %v, want 'text'", sysBlocks[0]["type"])
		}
		if sysBlocks[0]["text"] != "You are helpful" {
			t.Errorf("system block text = %v, want 'You are helpful'", sysBlocks[0]["text"])
		}
		if cc, ok := sysBlocks[0]["cache_control"].(map[string]interface{}); !ok {
			t.Error("system block should have cache_control")
		} else if cc["type"] != "ephemeral" {
			t.Errorf("cache_control type = %v, want 'ephemeral'", cc["type"])
		}
	}

	// 验证 messages 有 cache_control
	msgs, ok := body["messages"].([]MessageEntry)
	if !ok {
		t.Error("body 中缺少 messages 字段")
	} else {
		lastMsg := msgs[len(msgs)-1]
		if contentBlocks, ok := lastMsg.Content.([]map[string]interface{}); ok {
			if len(contentBlocks) > 0 {
				if _, hasCC := contentBlocks[len(contentBlocks)-1]["cache_control"]; !hasCC {
					t.Error("last message content block should have cache_control")
				}
			}
		} else {
			t.Error("user message content should be blocks array with cache_control")
		}
	}

	// 验证 tools（数组）：最后一个 tool 有 cache_control
	tools, ok := body["tools"].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Errorf("tools 类型或长度不正确, got %v", body["tools"])
	} else {
		if cc, ok := tools[len(tools)-1]["cache_control"].(map[string]interface{}); !ok {
			t.Error("last tool should have cache_control")
		} else if cc["type"] != "ephemeral" {
			t.Errorf("tool cache_control type = %v, want 'ephemeral'", cc["type"])
		}
	}

	// 验证 thinking 配置
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok {
		t.Error("body 中缺少 thinking 字段或类型不正确")
	} else {
		if thinking["type"] != "enabled" {
			t.Errorf("thinking.type = %v, want enabled", thinking["type"])
		}
		if bt, _ := thinking["budget_tokens"].(int); bt != 5000 {
			t.Errorf("thinking.budget_tokens = %v, want 5000", thinking["budget_tokens"])
		}
	}

	// 验证 max_tokens
	if mt, _ := body["max_tokens"].(int); mt != 8192 {
		t.Errorf("max_tokens = %v, want 8192", body["max_tokens"])
	}

	// 验证 model
	if m, _ := body["model"].(string); m != "claude-sonnet-4" {
		t.Errorf("model = %v, want claude-sonnet-4", body["model"])
	}
}

// TestBuildOpenAIBody 测试 OpenAI 格式转换
func TestBuildOpenAIBody(t *testing.T) {
	client := NewSimpleClient("gpt-4o", "test-key", "", ProviderOpenAI)
	defer client.Close()

	req := Request{
		SystemPrompt: []string{"You are helpful"},
		Messages: []MessageEntry{
			// assistant 消息带 tool_use content blocks → 应转为 tool_calls 格式
			{
				Role: "assistant",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "Let me check"},
					map[string]interface{}{"type": "tool_use", "id": "call_1", "name": "read_file", "input": map[string]interface{}{"path": "/tmp/test"}},
				},
			},
			// tool 消息（tool_result）→ 应转为 role=tool 消息
			{
				Role: "tool",
				Content: []interface{}{
					map[string]interface{}{"tool_use_id": "call_1", "output": "file content here"},
				},
			},
		},
		MaxTokens: 4096,
	}

	body := client.buildOpenAIBody(req, client.snapshot())
	messages, ok := body["messages"].([]map[string]interface{})
	if !ok {
		t.Fatal("messages 类型不正确")
	}

	// 第一条消息应为 system（从 SystemPrompt 转换）
	if messages[0]["role"] != "system" {
		t.Errorf("第一条消息 role = %v, want system", messages[0]["role"])
	}

	// 第二条消息应为 assistant，带 tool_calls
	if messages[1]["role"] != "assistant" {
		t.Errorf("assistant 消息 role = %v, want assistant", messages[1]["role"])
	}
	if _, ok := messages[1]["tool_calls"]; !ok {
		t.Error("assistant 消息should contain tool_calls 字段")
	}

	// 第三条消息应为 tool（从 tool_result 转换）
	if messages[2]["role"] != "tool" {
		t.Errorf("tool 消息 role = %v, want tool", messages[2]["role"])
	}
	if _, ok := messages[2]["tool_call_id"]; !ok {
		t.Error("tool 消息should contain tool_call_id 字段")
	}
}

// TestBaseURLHandling 测试 baseURL 带/不带 /v1 的处理
func TestBaseURLHandling(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		provider string
		wantAnth string
		wantOpen string
	}{
		{
			name:     "Anthropic 无 /v1",
			baseURL:  "https://api.anthropic.com",
			provider: ProviderAnthropic,
			wantAnth: "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "Anthropic 有 /v1",
			baseURL:  "https://api.anthropic.com/v1",
			provider: ProviderAnthropic,
			wantAnth: "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "OpenAI 无 /v1",
			baseURL:  "https://api.openai.com",
			provider: ProviderOpenAI,
			wantOpen: "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "OpenAI 有 /v1",
			baseURL:  "https://api.openai.com/v1",
			provider: ProviderOpenAI,
			wantOpen: "https://api.openai.com/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewSimpleClient("test-model", "test-key", tt.baseURL, tt.provider)
			defer client.Close()

			if tt.provider == ProviderAnthropic {
				got := buildAnthropicURL(client.snapshot())
				if got != tt.wantAnth {
					t.Errorf("buildAnthropicURL() = %q, want %q", got, tt.wantAnth)
				}
			} else {
				got := buildOpenAIURL(client.snapshot())
				if got != tt.wantOpen {
					t.Errorf("buildOpenAIURL() = %q, want %q", got, tt.wantOpen)
				}
			}
		})
	}
}

// TestSSEParser 测试 SSE 事件解析
func TestSSEParser(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\"}\n" +
		"\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\n" +
		"data:  \"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n" +
		"\n" +
		"id: evt-123\n" +
		"retry: 5000\n" +
		"data: single-data-event\n" +
		"\n"

	rc := io.NopCloser(strings.NewReader(input))
	events := make(chan SSEEvent, 16)

	// ParseStream 会关闭 body，并在完成后返回
	// 需要在 ParseStream 返回后关闭 channel，以便 range 能正常结束
	go func() {
		parser := NewSSEParser()
		if err := parser.ParseStream(rc, events); err != nil {
			t.Errorf("ParseStream 返回错误: %v", err)
		}
		close(events)
	}()

	// 读取所有事件
	var collected []SSEEvent
	for e := range events {
		collected = append(collected, e)
	}

	if len(collected) < 3 {
		t.Fatalf("解析到 %d 个事件, want >= 3", len(collected))
	}

	// 第一个事件
	if collected[0].Event != "message_start" {
		t.Errorf("事件0 Event = %q, want message_start", collected[0].Event)
	}

	// 第二个事件（多行 data）
	if collected[1].Event != "content_block_delta" {
		t.Errorf("事件1 Event = %q, want content_block_delta", collected[1].Event)
	}
	if !strings.Contains(collected[1].Data, "text_delta") {
		t.Errorf("事件1 Data = %q, should contain text_delta", collected[1].Data)
	}

	// 第三个事件（带 id 和 retry）
	if collected[2].ID != "evt-123" {
		t.Errorf("事件2 ID = %q, want evt-123", collected[2].ID)
	}
	if collected[2].Retry != "5000" {
		t.Errorf("事件2 Retry = %q, want 5000", collected[2].Retry)
	}
}

// TestParseAnthropicEvent 测试各种 Anthropic SSE 事件类型的解析
func TestParseAnthropicEvent(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string // eventType
	}{
		{
			name: "text_delta → TextDeltaEvent",
			data: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
			want: "text_delta",
		},
		{
			name: "thinking_delta → ThinkingDeltaEvent",
			data: `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm..."}}`,
			want: "thinking_delta",
		},
		{
			name: "input_json_delta → ToolInputDeltaEvent",
			data: `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"key\":"}}`,
			want: "tool_input_delta",
		},
		{
			name: "message_start with usage → UsageEvent",
			data: `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}}`,
			want: "usage",
		},
		{
			name: "message_stop → MessageStopEvent",
			data: `{"type":"message_stop"}`,
			want: "message_stop",
		},
		{
			name: "content_block_start with tool_use → ToolUseEvent",
			data: `{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool_1","name":"read_file"}}`,
			want: "tool_use",
		},
		{
			name: "error → ErrorEvent",
			data: `{"type":"error","error":{"message":"rate limit exceeded"}}`,
			want: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParseAnthropicEvent(tt.data)
			if tt.want == "error" {
				if err == nil {
					t.Fatal("ParseAnthropicEvent should return error for error event type")
				}
				if event == nil {
					t.Fatal("ParseAnthropicEvent 返回 nil")
				}
			} else {
				if err != nil {
					t.Fatalf("ParseAnthropicEvent 返回错误: %v", err)
				}
				if event == nil {
					t.Fatal("ParseAnthropicEvent 返回 nil")
				}
			}
			if got := event.eventType(); got != tt.want {
				t.Errorf("eventType() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIsRateLimitError 测试速率限制错误判断
// P0-2 修复：529 不再归为 RateLimit（529 = Overloaded），仅 429 为 RateLimit。
func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{429, true},
		{529, false}, // P0-2: 529 = Overloaded, not RateLimit
		{400, false},
		{401, false},
		{403, false},
		{500, false},
		{200, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.code), func(t *testing.T) {
			if got := IsRateLimitError(tt.code); got != tt.want {
				t.Errorf("IsRateLimitError(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// TestIsRetryableError 测试可重试错误判断
// 修复 P1：429/529 改为 false（统一与 APIError.IsRetryable() 行为一致，策略反转）
func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{429, false}, // 修复 P1：策略反转，429 不可直接重试（等待 RetryAfter 专用路径）
		{529, false}, // 修复 P1：策略反转，529 不可重试（直接降级）
		{500, true},
		{502, true},
		{503, true},
		{408, true}, // 修复 P1：新增，对齐 APIError.IsRetryable 的 StatusCode 降级路径
		{425, true}, // 修复 P1：新增，对齐 APIError.IsRetryable 的 StatusCode 降级路径
		{400, false},
		{401, false},
		{403, false},
		{200, false},
		{404, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.code), func(t *testing.T) {
			if got := IsRetryableError(tt.code); got != tt.want {
				t.Errorf("IsRetryableError(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// TestEstimateMessagesTokens 测试消息列表 token 估算，每条消息有 4 token 开销
func TestEstimateMessagesTokens(t *testing.T) {
	estimator := NewTokenEstimator()

	messages := []MessageEntry{
		{Role: "user", Content: "Hello"},         // 4 + ~1 = ~5
		{Role: "assistant", Content: "Hi there"}, // 4 + ~2 = ~6
		{Role: "user", Content: "How are you?"},  // 4 + ~3 = ~7
	}

	result := estimator.EstimateMessagesTokens(messages)

	// 每条消息至少 4 token 开销，3 条消息至少 12 token
	if result < 12 {
		t.Errorf("EstimateMessagesTokens = %d, want >= 12 (3 条消息 × 4 开销)", result)
	}
}

// TestFallbackTracker_529Degradation 测试连续 529 降级逻辑
func TestFallbackTracker_529Degradation(t *testing.T) {
	tracker := NewFallbackTracker("claude-sonnet-4", "claude-3-5-haiku")

	// 初始状态
	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Errorf("初始 ActiveModel = %q, want claude-sonnet-4", tracker.ActiveModel())
	}
	if tracker.IsDegraded() {
		t.Error("初始状态不应降级")
	}

	// 记录 2 次 529
	tracker.Record529()
	tracker.Record529()
	if tracker.IsDegraded() {
		t.Error("2 次 529 不应降级")
	}
	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Error("2 次 529 后仍应使用原始模型")
	}

	// 第 3 次 529 → 降级
	tracker.Record529()
	if !tracker.IsDegraded() {
		t.Error("3 次 529 应降级")
	}
	if tracker.ActiveModel() != "claude-3-5-haiku" {
		t.Errorf("降级后 ActiveModel = %q, want claude-3-5-haiku", tracker.ActiveModel())
	}
}

// TestFallbackTracker_Success 测试 RecordSuccess 重置 529 计数
func TestFallbackTracker_Success(t *testing.T) {
	tracker := NewFallbackTracker("claude-sonnet-4", "claude-3-5-haiku")

	// 触发降级
	tracker.Record529()
	tracker.Record529()
	tracker.Record529()
	if !tracker.IsDegraded() {
		t.Error("应已降级")
	}

	// RecordSuccess 重置计数（但不会auto recovery isDegraded 状态）
	tracker.RecordSuccess()

	// Restore 才能恢复
	tracker.Restore()
	if tracker.IsDegraded() {
		t.Error("Restore 后不应降级")
	}
}

// TestFallbackTracker_Restore 测试 Restore() 恢复到原始模型
func TestFallbackTracker_Restore(t *testing.T) {
	tracker := NewFallbackTracker("claude-sonnet-4", "claude-3-5-haiku")

	// 触发降级
	tracker.Record529()
	tracker.Record529()
	tracker.Record529()
	if !tracker.IsDegraded() {
		t.Error("应已降级")
	}
	if tracker.ActiveModel() != "claude-3-5-haiku" {
		t.Error("降级后应使用备用模型")
	}

	// Restore
	tracker.Restore()
	if tracker.IsDegraded() {
		t.Error("Restore 后不应降级")
	}
	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Errorf("Restore 后 ActiveModel = %q, want claude-sonnet-4", tracker.ActiveModel())
	}
}

// TestEstimateTextTokens_ASCII 测试 ASCII 文本 token 估算
func TestEstimateTextTokens_ASCII(t *testing.T) {
	estimator := NewTokenEstimator()
	// "Hello World" = 11 字符，11/4 ≈ 2.75 → 2 tokens
	result := estimator.EstimateTextTokens("Hello World")
	if result != 2 {
		t.Errorf("EstimateTextTokens('Hello World') = %d, want 2", result)
	}
}

// TestEstimateTextTokens_CJK 测试 CJK 文本 token 估算
func TestEstimateTextTokens_CJK(t *testing.T) {
	estimator := NewTokenEstimator()
	// "你好世界" = 4 字符，4/1.5 ≈ 2.67 → 2 tokens
	result := estimator.EstimateTextTokens("你好世界")
	if result != 2 {
		t.Errorf("EstimateTextTokens('你好世界') = %d, want 2", result)
	}
}

// TestEstimateTextTokens_Mixed 测试混合文本 token 估算
func TestEstimateTextTokens_Mixed(t *testing.T) {
	estimator := NewTokenEstimator()
	// "Hello你好" = ASCII 5/4=1.25 + CJK 2/1.5≈1.33 = 2.58 → 2 tokens
	result := estimator.EstimateTextTokens("Hello你好")
	if result != 2 {
		t.Errorf("EstimateTextTokens('Hello你好') = %d, want 2", result)
	}
}

// TestEstimateTextTokens_Japanese 测试日文文本 token 估算
// 日文字符属于 CJK scope，应按约 1.5 字符/token 估算，
// 结果should be positive且在合理scope内
func TestEstimateTextTokens_Japanese(t *testing.T) {
	estimator := NewTokenEstimator()
	// "こんにちは世界" = 7 个日文字符，7/1.5 ≈ 4.67 → 4 tokens
	japaneseText := "こんにちは世界"
	result := estimator.EstimateTextTokens(japaneseText)
	if result <= 0 {
		t.Errorf("EstimateTextTokens(%q) = %d, expected正数", japaneseText, result)
	}
	// 验证估算值在合理scope：7 字符 / 1.5 ≈ 4.67，取整为 4
	expected := int(float64(utf8.RuneCountInString(japaneseText)) / 1.5)
	if result != expected {
		t.Errorf("EstimateTextTokens(%q) = %d, expected约 %d (1.5 chars/token)", japaneseText, result, expected)
	}
}

// TestEstimateTextTokens_Korean 测试韩文文本 token 估算
// 韩文字符属于 CJK scope，应按约 1.5 字符/token 估算，
// 结果should be positive且在合理scope内
func TestEstimateTextTokens_Korean(t *testing.T) {
	estimator := NewTokenEstimator()
	// "안녕하세요세계" = 7 个韩文字符，7/1.5 ≈ 4.67 → 4 tokens
	koreanText := "안녕하세요세계"
	result := estimator.EstimateTextTokens(koreanText)
	if result <= 0 {
		t.Errorf("EstimateTextTokens(%q) = %d, expected正数", koreanText, result)
	}
	// 验证估算值在合理scope：7 字符 / 1.5 ≈ 4.67，取整为 4
	expected := int(float64(utf8.RuneCountInString(koreanText)) / 1.5)
	if result != expected {
		t.Errorf("EstimateTextTokens(%q) = %d, expected约 %d (1.5 chars/token)", koreanText, result, expected)
	}
}

// TestEstimateTextTokens_Empty 测试空文本 token 估算
func TestEstimateTextTokens_Empty(t *testing.T) {
	estimator := NewTokenEstimator()
	result := estimator.EstimateTextTokens("")
	if result != 0 {
		t.Errorf("EstimateTextTokens('') = %d, want 0", result)
	}
}

// TestModelRegistry_Concurrency 测试 ModelRegistry 并发 Add/Get/Remove，确保无数据竞争
func TestModelRegistry_Concurrency(t *testing.T) {
	registry := NewModelRegistry()
	var wg sync.WaitGroup

	// 100 个 goroutine 并发操作
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("model-%d", idx)
			cfg := &ModelConfig{Name: name, Provider: ProviderOpenAI}

			registry.Add(cfg)
			if got := registry.Get(name); got == nil {
				t.Errorf("Get(%q) 返回 nil", name)
			}
			if !registry.Remove(name) {
				t.Errorf("Remove(%q) 返回 false", name)
			}
		}(i)
	}

	wg.Wait()
}

// TestStreamWithRetry_ContextOverflow test context溢出错误不应重试
func TestStreamWithRetry_ContextOverflow(t *testing.T) {
	mockClient := &mockClient{
		err: fmt.Errorf("context overflow detected: prompt too long"),
	}

	cfg := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	_, err := StreamWithRetry(context.Background(), mockClient, Request{}, cfg, nil, nil)

	if err == nil {
		t.Fatal("should return error")
	}
	if !strings.Contains(err.Error(), "context overflow") {
		t.Errorf("error should contain 'context overflow', got: %v", err)
	}
	// 验证没有重试（mockClient.callCount 应为 1）
	if mockClient.callCount != 1 {
		t.Errorf("callCount = %d, want 1 (不应重试)", mockClient.callCount)
	}
}

// TestStreamWithRetry_RateLimit 测试速率限制错误应重试
func TestStreamWithRetry_RateLimit(t *testing.T) {
	// 第一次返回 429 错误，第二次成功
	mockClient := &mockClient{
		errs: []error{
			fmt.Errorf("API 返回 429: rate limit exceeded"),
			nil,
		},
		successCh: func() <-chan Event {
			ch := make(chan Event, 1)
			ch <- MessageStopEvent{}
			close(ch)
			return ch
		}(),
	}

	cfg := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	events, err := StreamWithRetry(context.Background(), mockClient, Request{}, cfg, nil, nil)

	if err != nil {
		t.Fatalf("不should return error: %v", err)
	}
	// 验证有重试（callCount 应 >= 2）
	if mockClient.callCount < 2 {
		t.Errorf("callCount = %d, want >= 2 (应重试)", mockClient.callCount)
	}
	// 验证能读取事件
	<-events
}

// TestStreamWithMockHTTPServer 使用 httptest.NewServer Mock HTTP server测试流式响应解析
func TestStreamWithMockHTTPServer(t *testing.T) {
	// 构造 Anthropic SSE 格式响应
	sseResponse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" World\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"read_file\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"/tmp/test\\\"}\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":50}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	// 启动 Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证request header
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header is missing")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, sseResponse)
	}))
	defer server.Close()

	// 创建client指向 Mock server
	client := NewSimpleClient("claude-sonnet-4", "test-key", server.URL, ProviderAnthropic)
	defer client.Close()

	// 发送流式请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Stream(ctx, Request{
		Messages: []MessageEntry{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}

	// 收集所有事件
	var collected []Event
	for e := range events {
		collected = append(collected, e)
	}

	// 验证事件序列
	if len(collected) < 3 {
		t.Fatalf("解析到 %d 个事件, want >= 3", len(collected))
	}

	// 验证包含 TextDeltaEvent
	hasTextDelta := false
	hasToolUse := false
	hasUsage := false
	hasMessageStop := false
	for _, e := range collected {
		switch e.eventType() {
		case "text_delta":
			hasTextDelta = true
		case "tool_use":
			hasToolUse = true
			tu := e.(ToolUseEvent)
			if tu.Name != "read_file" {
				t.Errorf("ToolUseEvent.Name = %q, want read_file", tu.Name)
			}
		case "usage":
			hasUsage = true
		case "message_stop":
			hasMessageStop = true
		}
	}

	if !hasTextDelta {
		t.Error("缺少 TextDeltaEvent")
	}
	if !hasToolUse {
		t.Error("缺少 ToolUseEvent")
	}
	if !hasUsage {
		t.Error("缺少 UsageEvent")
	}
	if !hasMessageStop {
		t.Error("缺少 MessageStopEvent")
	}
}

// TestStreamWithRetry_ServerError 测试 500 server错误的重试行为
// 使用 httptest.NewServer mock第一次返回 500、第二次返回 200 的scenario，
// 验证client在遇到 500 错误时能通过重试最终成功
func TestStreamWithRetry_ServerError(t *testing.T) {
	callCount := 0
	sseResponse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// 第一次请求返回 500
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "API 返回 500: internal server error")
			return
		}
		// 第二次请求返回 200
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseResponse)
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4", "test-key", server.URL, ProviderAnthropic)
	defer client.Close()

	cfg := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	events, err := StreamWithRetry(context.Background(), client, Request{
		Messages: []MessageEntry{{Role: "user", Content: "Hello"}},
	}, cfg, nil, nil)

	if err != nil {
		t.Fatalf("重试后应成功，但返回错误: %v", err)
	}
	// 验证确实发生了重试（至少调用 2 次）
	if callCount < 2 {
		t.Errorf("callCount = %d, want >= 2（应发生重试）", callCount)
	}
	// 验证能读取事件
	for e := range events {
		if _, ok := e.(MessageStopEvent); ok {
			break
		}
	}
}

// TestFallbackTracker_NoFallbackModel 测试未配置备用模型时的降级行为
// 创建 FallbackTracker 时 fallbackModel 为空字符串，
// 验证即使连续 529 错误达到阈值也不会降级，始终使用原始模型
func TestFallbackTracker_NoFallbackModel(t *testing.T) {
	tracker := NewFallbackTracker("claude-sonnet-4", "")

	// 初始状态：使用原始模型
	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Errorf("初始 ActiveModel = %q, want claude-sonnet-4", tracker.ActiveModel())
	}
	if tracker.IsDegraded() {
		t.Error("初始状态不应降级")
	}

	// 记录 3 次 529（达到降级阈值）
	tracker.Record529()
	tracker.Record529()
	result := tracker.Record529()

	// 未配置备用模型时，Record529 不应触发降级
	if result {
		t.Error("未配置备用模型时 Record529 不should return true")
	}
	if tracker.IsDegraded() {
		t.Error("未配置备用模型时不应降级")
	}
	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Errorf("未配置备用模型时 ActiveModel = %q, want claude-sonnet-4", tracker.ActiveModel())
	}

	// 继续记录更多 529，仍不应降级
	tracker.Record529()
	tracker.Record529()
	if tracker.IsDegraded() {
		t.Error("即使多次 529，未配置备用模型也不应降级")
	}
	if tracker.ActiveModel() != "claude-sonnet-4" {
		t.Errorf("ActiveModel = %q, want claude-sonnet-4", tracker.ActiveModel())
	}
}

// mockClient 用于测试的mockclient
type mockClient struct {
	err       error
	errs      []error
	successCh <-chan Event
	callCount int
}

func (m *mockClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	m.callCount++
	if len(m.errs) > 0 {
		idx := m.callCount - 1
		if idx < len(m.errs) {
			if m.errs[idx] != nil {
				return nil, m.errs[idx]
			}
			return m.successCh, nil
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.successCh, nil
}

func (m *mockClient) Model() string   { return "mock-model" }
func (m *mockClient) SetModel(string) {}
func (m *mockClient) Close() error    { return nil }

// ============================================================================
// Section 18: 性能benchmark
// ============================================================================

// BenchmarkTokenEstimation benchmark EstimateTextTokens 在不同文本大小下的性能
// 分别测试 1KB、10KB、100KB 文本的 token 估算速度
func BenchmarkTokenEstimation(b *testing.B) {
	estimator := NewTokenEstimator()

	// 生成不同大小的测试文本
	text1KB := strings.Repeat("Hello World! ", 70)     // ~1KB
	text10KB := strings.Repeat("Hello World! ", 700)   // ~10KB
	text100KB := strings.Repeat("Hello World! ", 7000) // ~100KB

	b.Run("1KB", func(b *testing.B) {
		b.SetBytes(int64(len(text1KB)))
		for i := 0; i < b.N; i++ {
			estimator.EstimateTextTokens(text1KB)
		}
	})

	b.Run("10KB", func(b *testing.B) {
		b.SetBytes(int64(len(text10KB)))
		for i := 0; i < b.N; i++ {
			estimator.EstimateTextTokens(text10KB)
		}
	})

	b.Run("100KB", func(b *testing.B) {
		b.SetBytes(int64(len(text100KB)))
		for i := 0; i < b.N; i++ {
			estimator.EstimateTextTokens(text100KB)
		}
	})
}
