package context

import (
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestCacheAwareMicroCompact_CacheActive_Skip test cache有效时跳过微压缩
func TestCacheAwareMicroCompact_CacheActive_Skip(t *testing.T) {
	config := DefaultCacheAwareMicroCompactConfig()
	now := time.Now()
	lastCacheTime := now.Add(-30 * time.Minute) // 30分钟前，缓存仍在TTL内

	// 创建包含旧工具结果的消息
	messages := []session.Message{
		session.ToolResult("id1", "read_file", strings.Repeat("x", 600), false),
		session.UserText("User消息"),
	}

	result := CacheAwareMicroCompact(messages, config, lastCacheTime, now, len(messages))

	// 缓存有效且所有消息在缓存前缀中，消息不应被修改
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages when cache active, got %d", len(messages), len(result))
	}
	// 工具结果内容应保持不变
	if result[0].Blocks[0].Output != messages[0].Blocks[0].Output {
		t.Fatal("expected tool result output to be unchanged when cache is active")
	}
}

// TestCacheAwareMicroCompact_CacheActive_CompactAfterBoundary test cache有效时对边界之后的消息执行微压缩
func TestCacheAwareMicroCompact_CacheActive_CompactAfterBoundary(t *testing.T) {
	config := DefaultCacheAwareMicroCompactConfig()
	now := time.Now()
	lastCacheTime := now.Add(-30 * time.Minute)

	oldTimestamp := now.Add(-10 * time.Minute).UnixMilli()
	messages := []session.Message{
		session.UserText("cached user message"),
		session.AssistantText("cached assistant message", "model", nil),
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id1", ToolName: "read_file", Output: strings.Repeat("x", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id2", ToolName: "read_file", Output: strings.Repeat("y", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id3", ToolName: "read_file", Output: strings.Repeat("z", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id4", ToolName: "read_file", Output: strings.Repeat("a", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id5", ToolName: "read_file", Output: strings.Repeat("b", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id6", ToolName: "read_file", Output: strings.Repeat("c", 600)}}, Timestamp: oldTimestamp},
		session.UserText("User消息"),
	}

	result := CacheAwareMicroCompact(messages, config, lastCacheTime, now, 2)

	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}
	if result[0].Blocks[0].Text != "cached user message" {
		t.Fatal("expected cached prefix message to be unchanged")
	}
	if result[1].Blocks[0].Text != "cached assistant message" {
		t.Fatal("expected cached prefix message to be unchanged")
	}
	if result[2].Blocks[0].Output == messages[2].Blocks[0].Output {
		t.Fatal("expected oldest tool result after boundary to be truncated")
	}
	if !strings.Contains(result[2].Blocks[0].Output, "[Old tool result content cleared") {
		t.Fatalf("expected truncation placeholder, got: %s", result[2].Blocks[0].Output)
	}
}

// TestCacheAwareMicroCompact_CacheExpired_Trigger test cache过期时触发微压缩
func TestCacheAwareMicroCompact_CacheExpired_Trigger(t *testing.T) {
	config := DefaultCacheAwareMicroCompactConfig()
	now := time.Now()
	lastCacheTime := now.Add(-2 * time.Hour) // 2小时前，缓存已过期

	// 创建包含多条旧工具结果的消息（时间戳设为很久以前）
	// PreserveRecent=5，需要超过5条工具结果才能触发截断
	oldTimestamp := now.Add(-10 * time.Minute).UnixMilli()
	messages := []session.Message{
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id1", ToolName: "read_file", Output: strings.Repeat("x", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id2", ToolName: "read_file", Output: strings.Repeat("y", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id3", ToolName: "read_file", Output: strings.Repeat("z", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id4", ToolName: "read_file", Output: strings.Repeat("a", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id5", ToolName: "read_file", Output: strings.Repeat("b", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id6", ToolName: "read_file", Output: strings.Repeat("c", 600)}}, Timestamp: oldTimestamp},
		session.UserText("User消息"),
	}

	result := CacheAwareMicroCompact(messages, config, lastCacheTime, now, 0)

	// 缓存过期，应执行微压缩
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}
	// 第1条（最旧的）工具结果应被截断（前5条被保留，第6条被截断）
	if result[0].Blocks[0].Output == messages[0].Blocks[0].Output {
		t.Fatal("expected oldest tool result output to be truncated when cache is expired")
	}
	if !strings.Contains(result[0].Blocks[0].Output, "[Old tool result content cleared") {
		t.Fatalf("expected truncation placeholder, got: %s", result[0].Blocks[0].Output)
	}
}

// TestCacheAwareMicroCompact_CacheEditingSupported 测试支持缓存编辑时的行为
func TestCacheAwareMicroCompact_CacheEditingSupported(t *testing.T) {
	config := DefaultCacheAwareMicroCompactConfig()
	config.CacheEditingSupported = true
	now := time.Now()
	lastCacheTime := now.Add(-2 * time.Hour) // 缓存已过期

	oldTimestamp := now.Add(-10 * time.Minute).UnixMilli()
	messages := []session.Message{
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id1", ToolName: "read_file", Output: strings.Repeat("x", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id2", ToolName: "read_file", Output: strings.Repeat("y", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id3", ToolName: "read_file", Output: strings.Repeat("z", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id4", ToolName: "read_file", Output: strings.Repeat("a", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id5", ToolName: "read_file", Output: strings.Repeat("b", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id6", ToolName: "read_file", Output: strings.Repeat("c", 600)}}, Timestamp: oldTimestamp},
		session.UserText("User消息"),
	}

	// 当支持缓存编辑时，仍应执行微压缩（生成编辑操作由 GenerateCacheEdits 处理）
	result := CacheAwareMicroCompact(messages, config, lastCacheTime, now, 0)

	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}

	// 同时验证 GenerateCacheEdits 能生成编辑操作
	edits := GenerateCacheEdits(messages, config)
	if len(edits) == 0 {
		t.Fatal("expected cache edit operations to be generated")
	}
	if edits[0].Type != "delete" {
		t.Fatalf("expected edit type 'delete', got %s", edits[0].Type)
	}
}

// TestCacheAwareMicroCompact_CacheEditingNotSupported_Fallback 测试不支持缓存编辑时回退到常规微压缩
func TestCacheAwareMicroCompact_CacheEditingNotSupported_Fallback(t *testing.T) {
	config := DefaultCacheAwareMicroCompactConfig()
	config.CacheEditingSupported = false
	now := time.Now()
	lastCacheTime := now.Add(-2 * time.Hour) // 缓存已过期

	oldTimestamp := now.Add(-10 * time.Minute).UnixMilli()
	messages := []session.Message{
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id1", ToolName: "read_file", Output: strings.Repeat("x", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id2", ToolName: "read_file", Output: strings.Repeat("y", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id3", ToolName: "read_file", Output: strings.Repeat("z", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id4", ToolName: "read_file", Output: strings.Repeat("a", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id5", ToolName: "read_file", Output: strings.Repeat("b", 600)}}, Timestamp: oldTimestamp},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id6", ToolName: "read_file", Output: strings.Repeat("c", 600)}}, Timestamp: oldTimestamp},
		session.UserText("User消息"),
	}

	result := CacheAwareMicroCompact(messages, config, lastCacheTime, now, 0)

	// 不支持缓存编辑时，应回退到常规微压缩
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}
	// 最旧的工具结果应被截断（常规微压缩行为）
	if result[0].Blocks[0].Output == messages[0].Blocks[0].Output {
		t.Fatal("expected oldest tool result output to be truncated in fallback mode")
	}
}

// TestGenerateCacheEdits test cache编辑操作生成
func TestGenerateCacheEdits(t *testing.T) {
	config := DefaultCacheAwareMicroCompactConfig()
	now := time.Now()

	t.Run("空消息列表不生成编辑操作", func(t *testing.T) {
		edits := GenerateCacheEdits(nil, config)
		if len(edits) != 0 {
			t.Fatalf("expected 0 edits for empty messages, got %d", len(edits))
		}
	})

	t.Run("近期工具结果不生成删除操作", func(t *testing.T) {
		messages := []session.Message{
			{
				Role: session.RoleTool,
				Blocks: []session.ContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: "id1",
						ToolName:  "read_file",
						Output:    strings.Repeat("x", 600),
					},
				},
				Timestamp: now.UnixMilli(), // 当前时间，属于近期
			},
		}
		edits := GenerateCacheEdits(messages, config)
		if len(edits) != 0 {
			t.Fatalf("expected 0 edits for recent tool result, got %d", len(edits))
		}
	})

	t.Run("旧的长工具结果生成删除操作", func(t *testing.T) {
		oldTimestamp := now.Add(-10 * time.Minute).UnixMilli()
		messages := []session.Message{
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id1", ToolName: "read_file", Output: strings.Repeat("x", 600)}}, Timestamp: oldTimestamp},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id2", ToolName: "read_file", Output: strings.Repeat("y", 600)}}, Timestamp: oldTimestamp},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id3", ToolName: "read_file", Output: strings.Repeat("z", 600)}}, Timestamp: oldTimestamp},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id4", ToolName: "read_file", Output: strings.Repeat("a", 600)}}, Timestamp: oldTimestamp},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id5", ToolName: "read_file", Output: strings.Repeat("b", 600)}}, Timestamp: oldTimestamp},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id6", ToolName: "read_file", Output: strings.Repeat("c", 600)}}, Timestamp: oldTimestamp},
		}
		edits := GenerateCacheEdits(messages, config)
		if len(edits) < 1 {
			t.Fatalf("expected at least 1 edit for old long tool result, got %d", len(edits))
		}
		if edits[0].Type != "delete" {
			t.Fatalf("expected edit type 'delete', got %s", edits[0].Type)
		}
	})

	t.Run("短的旧工具结果不生成删除操作", func(t *testing.T) {
		oldTimestamp := now.Add(-10 * time.Minute).UnixMilli()
		messages := []session.Message{
			{
				Role: session.RoleTool,
				Blocks: []session.ContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: "id1",
						ToolName:  "read_file",
						Output:    "short output",
					},
				},
				Timestamp: oldTimestamp,
			},
		}
		edits := GenerateCacheEdits(messages, config)
		if len(edits) != 0 {
			t.Fatalf("expected 0 edits for short tool result, got %d", len(edits))
		}
	})

	t.Run("非工具消息不生成编辑操作", func(t *testing.T) {
		messages := []session.Message{
			session.UserText("User消息"),
			session.AssistantText("Assistant回复", "model", nil),
		}
		edits := GenerateCacheEdits(messages, config)
		if len(edits) != 0 {
			t.Fatalf("expected 0 edits for non-tool messages, got %d", len(edits))
		}
	})

	t.Run("lastCacheTime为零值时缓存无效", func(t *testing.T) {
		config := DefaultCacheAwareMicroCompactConfig()
		now := time.Now()
		oldTs := now.Add(-10 * time.Minute).UnixMilli()
		messages := []session.Message{
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id1", ToolName: "read_file", Output: strings.Repeat("x", 600)}}, Timestamp: oldTs},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id2", ToolName: "read_file", Output: strings.Repeat("y", 600)}}, Timestamp: oldTs},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id3", ToolName: "read_file", Output: strings.Repeat("z", 600)}}, Timestamp: oldTs},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id4", ToolName: "read_file", Output: strings.Repeat("a", 600)}}, Timestamp: oldTs},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id5", ToolName: "read_file", Output: strings.Repeat("b", 600)}}, Timestamp: oldTs},
			{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "id6", ToolName: "read_file", Output: strings.Repeat("c", 600)}}, Timestamp: oldTs},
		}
		// lastCacheTime 为零值，缓存应无效，触发微压缩
		result := CacheAwareMicroCompact(messages, config, time.Time{}, now, 0)
		// 最旧的工具结果（第1条）应被截断
		if result[0].Blocks[0].Output == messages[0].Blocks[0].Output {
			t.Fatal("expected truncation when lastCacheTime is zero")
		}
	})
}
