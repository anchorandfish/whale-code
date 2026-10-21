package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// helperComputeHash 测试helper functions，计算 SHA256 哈希
func helperComputeHash(data string) string {
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// TestRecordPromptState 测试 RecordPromptState 正确记录提示词状态
func TestRecordPromptState(t *testing.T) {
	detector := NewCacheBreakDetector()

	systemPrompt := "You are a helpful assistant."
	toolSchemas := []string{"tool1_schema", "tool2_schema"}
	model := "claude-sonnet-4"
	cacheControlHash := "v1"

	detector.RecordPromptState(systemPrompt, toolSchemas, model, cacheControlHash)

	if detector.prevPromptState == nil {
		t.Fatal("RecordPromptState should set prevPromptState")
	}

	expectedSystemHash := helperComputeHash(systemPrompt)
	if detector.prevPromptState.SystemPromptHash != expectedSystemHash {
		t.Errorf("SystemPromptHash mismatch: got %s, want %s", detector.prevPromptState.SystemPromptHash, expectedSystemHash)
	}

	expectedToolHash := helperComputeHash(strings.Join(toolSchemas, "|"))
	if detector.prevPromptState.ToolSchemasHash != expectedToolHash {
		t.Errorf("ToolSchemasHash mismatch: got %s, want %s", detector.prevPromptState.ToolSchemasHash, expectedToolHash)
	}

	expectedModelHash := helperComputeHash(model)
	if detector.prevPromptState.ModelHash != expectedModelHash {
		t.Errorf("ModelHash mismatch: got %s, want %s", detector.prevPromptState.ModelHash, expectedModelHash)
	}

	expectedCacheControlHash := helperComputeHash(cacheControlHash)
	if detector.prevPromptState.CacheControlHash != expectedCacheControlHash {
		t.Errorf("CacheControlHash mismatch: got %s, want %s", detector.prevPromptState.CacheControlHash, expectedCacheControlHash)
	}
}

// TestCheckForCacheBreak_DropDetected 测试当缓存读取 token 下降超过 5% 时检测到断裂
func TestCheckForCacheBreak_DropDetected(t *testing.T) {
	detector := NewCacheBreakDetector()

	// 第一次调用：记录状态并建立基线
	detector.RecordPromptState("system prompt", []string{"tool1"}, "claude-sonnet-4", "v1")
	sameState := PromptState{
		SystemPromptHash: helperComputeHash("system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}

	// 第一次检查：建立基线（prevCacheReadTokens 从 0 开始，无断裂）
	result := detector.CheckForCacheBreak(10000, sameState)
	if result != nil {
		t.Fatalf("First check should return nil, got %+v", result)
	}

	// 第二次检查：缓存读取从 10000 下降到 8000（下降 20%），应检测到断裂
	result = detector.CheckForCacheBreak(8000, sameState)
	if result == nil {
		t.Fatal("Should detect cache break with >5% drop")
	}
	if !result.IsBreak {
		t.Error("IsBreak should be true")
	}
	if result.CacheReadDrop <= 0.05 {
		t.Errorf("CacheReadDrop should be > 0.05, got %f", result.CacheReadDrop)
	}
}

// TestCheckForCacheBreak_NoFalsePositives 测试当缓存读取 token 下降不超过 5% 时不误报
func TestCheckForCacheBreak_NoFalsePositives(t *testing.T) {
	detector := NewCacheBreakDetector()

	detector.RecordPromptState("system prompt", []string{"tool1"}, "claude-sonnet-4", "v1")
	sameState := PromptState{
		SystemPromptHash: helperComputeHash("system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}

	// 建立基线
	detector.CheckForCacheBreak(10000, sameState)

	// 下降 3%（10000 -> 9700），不应检测到断裂
	result := detector.CheckForCacheBreak(9700, sameState)
	if result != nil {
		t.Fatalf("Should not detect break with <5%% drop, got %+v", result)
	}

	// 下降 0%（10000 -> 10000），不应检测到断裂
	result = detector.CheckForCacheBreak(10000, sameState)
	if result != nil {
		t.Fatalf("Should not detect break with 0%% drop, got %+v", result)
	}

	// 上升（9700 -> 10500），不应检测到断裂
	result = detector.CheckForCacheBreak(10500, sameState)
	if result != nil {
		t.Fatalf("Should not detect break when cache read increases, got %+v", result)
	}
}

// TestNotifyCacheDeletion_PreventsFalseBreak 测试通知缓存删除后不误报断裂
func TestNotifyCacheDeletion_PreventsFalseBreak(t *testing.T) {
	detector := NewCacheBreakDetector()

	detector.RecordPromptState("system prompt", []string{"tool1"}, "claude-sonnet-4", "v1")
	sameState := PromptState{
		SystemPromptHash: helperComputeHash("system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}

	// 建立基线
	detector.CheckForCacheBreak(10000, sameState)

	// 通知缓存删除（预期行为）
	detector.NotifyCacheDeletion()

	// 缓存读取大幅下降（10000 -> 5000），但因为已通知删除，不应报断裂
	result := detector.CheckForCacheBreak(5000, sameState)
	if result != nil {
		t.Fatalf("Should not report break after NotifyCacheDeletion, got %+v", result)
	}
}

// TestNotifyCompaction_ResetsBaseline 测试通知压缩后重置基线
func TestNotifyCompaction_ResetsBaseline(t *testing.T) {
	detector := NewCacheBreakDetector()

	detector.RecordPromptState("system prompt", []string{"tool1"}, "claude-sonnet-4", "v1")
	sameState := PromptState{
		SystemPromptHash: helperComputeHash("system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}

	// 建立基线
	detector.CheckForCacheBreak(10000, sameState)

	// 通知压缩
	detector.NotifyCompaction()

	// 压缩后缓存读取大幅下降，但因为是预期行为，不应报断裂
	result := detector.CheckForCacheBreak(3000, sameState)
	if result != nil {
		t.Fatalf("Should not report break after NotifyCompaction, got %+v", result)
	}

	// 压缩后基线已重置为 3000，后续正常检测应基于新基线
	result = detector.CheckForCacheBreak(2900, sameState)
	if result != nil {
		t.Fatalf("Small drop from new baseline should not trigger break, got %+v", result)
	}
}

// TestBreakReason_SystemPromptChange 测试系统提示词变化时的断裂原因
func TestBreakReason_SystemPromptChange(t *testing.T) {
	detector := NewCacheBreakDetector()

	// 记录初始状态
	detector.RecordPromptState("original system prompt", []string{"tool1"}, "claude-sonnet-4", "v1")

	// 建立基线
	oldState := PromptState{
		SystemPromptHash: helperComputeHash("original system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}
	detector.CheckForCacheBreak(10000, oldState)

	// 系统提示词变化后的状态
	newState := PromptState{
		SystemPromptHash: helperComputeHash("changed system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}

	// 缓存读取下降超过 5%
	result := detector.CheckForCacheBreak(8000, newState)
	if result == nil {
		t.Fatal("Should detect cache break")
	}
	if !result.IsBreak {
		t.Error("IsBreak should be true")
	}
	if result.BreakReason != "system_prompt_changed" {
		t.Errorf("BreakReason should be 'system_prompt_changed', got '%s'", result.BreakReason)
	}
	if _, ok := result.Details["system_prompt"]; !ok {
		t.Error("Details should contain 'system_prompt' key")
	}
}

// TestBreakReason_ModelChange 测试模型变化时的断裂原因
func TestBreakReason_ModelChange(t *testing.T) {
	detector := NewCacheBreakDetector()

	// 记录初始状态
	detector.RecordPromptState("system prompt", []string{"tool1"}, "claude-sonnet-4", "v1")

	// 建立基线
	oldState := PromptState{
		SystemPromptHash: helperComputeHash("system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-sonnet-4"),
		CacheControlHash: helperComputeHash("v1"),
	}
	detector.CheckForCacheBreak(10000, oldState)

	// 模型变化后的状态
	newState := PromptState{
		SystemPromptHash: helperComputeHash("system prompt"),
		ToolSchemasHash:  helperComputeHash("tool1"),
		ModelHash:        helperComputeHash("claude-opus-4"),
		CacheControlHash: helperComputeHash("v1"),
	}

	// 缓存读取下降超过 5%
	result := detector.CheckForCacheBreak(5000, newState)
	if result == nil {
		t.Fatal("Should detect cache break")
	}
	if !result.IsBreak {
		t.Error("IsBreak should be true")
	}
	if result.BreakReason != "model_changed" {
		t.Errorf("BreakReason should be 'model_changed', got '%s'", result.BreakReason)
	}
	if _, ok := result.Details["model"]; !ok {
		t.Error("Details should contain 'model' key")
	}
}
