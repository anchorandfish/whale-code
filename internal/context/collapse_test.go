// Package context — 上下文折叠压缩unit test
package context

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestContextCollapser_Collapse_Basic 基础折叠
func TestContextCollapser_Collapse_Basic(t *testing.T) {
	collapser := NewContextCollapser()

	// 创建 20 条消息
	messages := make([]session.Message, 20)
	for i := 0; i < 20; i++ {
		role := session.RoleUser
		if i%2 == 1 {
			role = session.RoleAssistant
		}
		messages[i] = session.Message{
			Role:      role,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "message content"}},
			Timestamp: int64(i * 1000),
		}
	}

	result := collapser.Collapse(messages, 5)

	if result.RemovedCount != 15 {
		t.Errorf("Expected 15 removed, got %d", result.RemovedCount)
	}

	// 应该有 1 条摘要 + 5 条保留 = 6 条
	if len(result.PreservedMessages) != 6 {
		t.Errorf("Expected 6 preserved messages (1 summary + 5 recent), got %d", len(result.PreservedMessages))
	}

	// 第一条应该是摘要消息
	if result.PreservedMessages[0].Role != session.RoleSystem {
		t.Errorf("First message should be system summary, got %v", result.PreservedMessages[0].Role)
	}
}

// TestContextCollapser_Collapse_PreserveCountGreaterThanMessages 保留数大于消息数
func TestContextCollapser_Collapse_PreserveCountGreaterThanMessages(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "bye"}}},
	}

	result := collapser.Collapse(messages, 10)

	if result.RemovedCount != 0 {
		t.Errorf("Expected 0 removed, got %d", result.RemovedCount)
	}

	if len(result.PreservedMessages) != 3 {
		t.Errorf("Expected 3 preserved (all), got %d", len(result.PreservedMessages))
	}
}

// TestContextCollapser_Collapse_ExtractFileRefs 提取文件引用
func TestContextCollapser_Collapse_ExtractFileRefs(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "修改了 main.go 和 internal/app.go"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已修改 main.go"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "继续"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "好的"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "完成"}}},
	}

	result := collapser.Collapse(messages, 2)

	// 应该提取到文件引用
	foundMain := false
	foundApp := false
	for _, f := range result.PreservedFiles {
		if strings.Contains(f, "main.go") {
			foundMain = true
		}
		if strings.Contains(f, "app.go") {
			foundApp = true
		}
	}

	if !foundMain {
		t.Error("Expected main.go in PreservedFiles")
	}
	if !foundApp {
		t.Error("Expected internal/app.go in PreservedFiles")
	}
}

// TestContextCollapser_Collapse_ExtractDecisions 提取关键决策
func TestContextCollapser_Collapse_ExtractDecisions(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "用什么缓存approach？"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "decided使用 Redis 作为缓存approach"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "好的"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "开始implementation"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "完成"}}},
	}

	result := collapser.Collapse(messages, 2)

	if len(result.PreservedDecisions) == 0 {
		t.Error("Expected at least one decision in PreservedDecisions")
	}

	foundRedis := false
	for _, d := range result.PreservedDecisions {
		if strings.Contains(d, "Redis") {
			foundRedis = true
		}
	}
	if !foundRedis {
		t.Error("Expected Redis decision in PreservedDecisions")
	}
}

// TestContextCollapser_Collapse_ExtractToolCalls 提取工具调用
func TestContextCollapser_Collapse_ExtractToolCalls(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
			{Type: "tool_use", Name: "read_file", Input: `{"path": "main.go"}`},
			{Type: "text", Text: "已读取文件"},
		}},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{
			{Type: "tool_result", Output: "file content here"},
		}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "继续"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "好的"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "完成"}}},
	}

	result := collapser.Collapse(messages, 2)

	// 摘要中should contain工具调用信息
	if !strings.Contains(result.CollapsedSummary, "read_file") {
		t.Errorf("Expected tool call 'read_file' in summary, got: %s", result.CollapsedSummary)
	}
}

// TestContextCollapser_Collapse_FileDedup 文件引用去重
func TestContextCollapser_Collapse_FileDedup(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "修改 main.go"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已修改 main.go"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "再改 main.go"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "已修改 main.go"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "完成"}}},
	}

	result := collapser.Collapse(messages, 1)

	// main.go 应该只出现一次
	mainCount := 0
	for _, f := range result.PreservedFiles {
		if strings.Contains(f, "main.go") {
			mainCount++
		}
	}

	if mainCount > 1 {
		t.Errorf("Expected main.go to appear once in PreservedFiles, got %d times", mainCount)
	}
}

// TestContextCollapser_Collapse_CacheBoundary 缓存边界感知
func TestContextCollapser_Collapse_CacheBoundary(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "system prompt"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "bye"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "bye"}}},
	}

	result := collapser.CollapseWithCacheBoundary(messages, 2)

	// system 消息应保留在前面
	if len(result.PreservedMessages) == 0 {
		t.Fatal("Expected preserved messages")
	}
	if result.PreservedMessages[0].Role != session.RoleSystem {
		t.Errorf("First message should be system, got %v", result.PreservedMessages[0].Role)
	}
}

// TestContextCollapser_Collapse_ErrorExtraction 提取错误信息
func TestContextCollapser_Collapse_ErrorExtraction(t *testing.T) {
	collapser := NewContextCollapser()

	messages := []session.Message{
		{Role: session.RoleTool, Blocks: []session.ContentBlock{
			{Type: "tool_result", Output: "Error: file not found", IsError: true},
		}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
			{Type: "text", Text: "fixed了 file not found 错误"},
		}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "继续"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "好的"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "完成"}}},
	}

	result := collapser.Collapse(messages, 2)

	if len(result.PreservedErrors) == 0 {
		t.Error("Expected at least one error in PreservedErrors")
	}
}

// TestCompactionPipeline_Execute_SnipOnly 仅 Snip 阶段足够
func TestCompactionPipeline_Execute_SnipOnly(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 创建少量消息，token 不超预算
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{}}, // 僵尸消息
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	budget := DefaultBudget(100000) // 大预算
	result := pipeline.Execute(messages, budget, nil)

	// 应该只执行 Snip（移除僵尸消息）
	if result.TotalRemoved != 1 {
		t.Errorf("Expected 1 removed (zombie), got %d", result.TotalRemoved)
	}

	// 应该只有 snip 阶段
	foundSnip := false
	for _, s := range result.StagesExecuted {
		if s == "snip" {
			foundSnip = true
		}
	}
	if !foundSnip {
		t.Errorf("Expected snip stage, got %v", result.StagesExecuted)
	}
}

// TestCompactionPipeline_Execute_UpToCollapse 需要 Collapse
func TestCompactionPipeline_Execute_UpToCollapse(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 创建大量消息，触发 Collapse
	messages := make([]session.Message, 30)
	for i := 0; i < 30; i++ {
		role := session.RoleUser
		if i%2 == 1 {
			role = session.RoleAssistant
		}
		messages[i] = session.Message{
			Role:      role,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "some content here to add tokens"}},
			Timestamp: int64(i * 1000),
		}
	}

	// 小预算，强制压缩
	budget := DefaultBudget(200)
	result := pipeline.Execute(messages, budget, nil)

	// 应该执行了 Collapse 阶段
	foundCollapse := false
	for _, s := range result.StagesExecuted {
		if s == "collapse" {
			foundCollapse = true
		}
	}
	if !foundCollapse {
		t.Errorf("Expected collapse stage, got %v", result.StagesExecuted)
	}

	// 消息数应该减少
	if len(result.Messages) >= len(messages) {
		t.Errorf("Expected fewer messages after compaction, got %d vs %d", len(result.Messages), len(messages))
	}
}

// TestCompactionPipeline_Execute_WithCacheInfo 带缓存信息的管线
func TestCompactionPipeline_Execute_WithCacheInfo(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 创建大量消息，确保 token 超预算
	messages := make([]session.Message, 30)
	for i := 0; i < 30; i++ {
		role := session.RoleUser
		if i%2 == 1 {
			role = session.RoleAssistant
		}
		// 使用较长的文本确保 token 超预算
		messages[i] = session.Message{
			Role:      role,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "this is a longer content to ensure we exceed the token budget significantly"}},
			Timestamp: int64(i * 1000),
		}
	}

	// 高缓存删除率
	cacheInfo := &ai.CacheInfo{
		CacheDeletedTokens: 800,
		TotalCachedTokens:  1000,
		CacheReadTokens:    200,
	}

	// 小预算，强制压缩
	budget := DefaultBudget(100)
	result := pipeline.Execute(messages, budget, cacheInfo)

	// 管线应该执行了至少一个阶段
	if len(result.StagesExecuted) == 0 {
		t.Errorf("Expected some stages to be executed, got none. Messages: %d -> %d", len(messages), len(result.Messages))
	}
}

// TestCompactionPipeline_PipelineResult 管线结果
func TestCompactionPipeline_PipelineResult(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
	}

	budget := DefaultBudget(100000)
	result := pipeline.Execute(messages, budget, nil)

	// 不需要压缩
	if result.TotalRemoved != 0 {
		t.Errorf("Expected 0 removed, got %d", result.TotalRemoved)
	}
	if len(result.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(result.Messages))
	}
}

// TestCacheAwareCompactor_CompactWithCacheInfo 带缓存信息的压缩
func TestCacheAwareCompactor_CompactWithCacheInfo(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCacheAwareCompactor(estimator)

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "hi there"}}},
	}

	cfg := DefaultCompactionConfig()

	// 低缓存删除率，不应压缩
	lowDeletion := &ai.CacheInfo{CacheDeletedTokens: 100, TotalCachedTokens: 1000}
	result, didCompact := compactor.CompactWithCacheInfo(messages, cfg, lowDeletion, 0.5)
	if didCompact {
		t.Error("Should not compact with low deletion ratio")
	}
	if len(result.CompactedMessages) != len(messages) {
		t.Error("Messages should be unchanged")
	}

	// 高缓存删除率，应压缩
	highDeletion := &ai.CacheInfo{CacheDeletedTokens: 800, TotalCachedTokens: 1000}
	result, didCompact = compactor.CompactWithCacheInfo(messages, cfg, highDeletion, 0.5)
	if !didCompact {
		t.Error("Should compact with high deletion ratio")
	}
}

// TestCacheAwareCompactor_EstimateCacheSavings 估算缓存节省
func TestCacheAwareCompactor_EstimateCacheSavings(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCacheAwareCompactor(estimator)

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello world this is a test"}}},
	}

	// 无缓存信息
	savings := compactor.EstimateCacheSavings(messages, nil)
	if savings != 0 {
		t.Errorf("Expected 0 savings with nil cache info, got %d", savings)
	}

	// 有缓存信息
	cacheInfo := &ai.CacheInfo{CacheReadTokens: 500, CacheCreationTokens: 500}
	savings = compactor.EstimateCacheSavings(messages, cacheInfo)
	if savings <= 0 {
		t.Error("Expected positive savings with cache info")
	}
}
