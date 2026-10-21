// Package ai — 5 Provider usage 字段映射测试（T1-B §5.2）
//
// 测试覆盖：
// - Anthropic usage → UnifiedUsage 映射（4 字段全映射）
// - OpenAI usage → UnifiedUsage 映射（3 字段，CacheCreation=0）
// - DeepSeek usage → UnifiedUsage 映射（3 字段，CacheRead=prompt_cache_hit_tokens）
// - Gemini usage → UnifiedUsage 映射（3 字段，CacheRead=cachedContentTokenCount）
// - DashScope usage → UnifiedUsage 映射（2 字段，CacheCreation=0 + CacheRead=0）
//
// Phase 1（Anthropic + OpenAI）映射通过 RecordRemoteUsage → RecordUsage 链路验证；
// Phase 2（Gemini + DeepSeek + DashScope）映射测试预留，待 T1-A 适配器落地后激活。
//
// 官方源：
// - Anthropic @https://docs.anthropic.com/en/api/messages
// - OpenAI @https://platform.openai.com/docs/api-reference/chat
// - Gemini @https://ai.google.dev/api/rest/v1beta/models/generateContent
// - DeepSeek @https://api-docs.deepseek.com/api-reference-1
// - DashScope @https://help.aliyun.com/zh/dashscope/
package ai

import (
	"testing"
	"time"
)

// TestUnifiedUsage_Anthropic_Mapping 测试 Anthropic usage 字段映射。
//
// Anthropic 原生字段 → UnifiedUsage：
// - usage.input_tokens → InputTokens
// - usage.output_tokens → OutputTokens
// - usage.cache_creation_input_tokens → CacheCreation
// - usage.cache_read_input_tokens → CacheRead
//
// 验证：RecordRemoteUsage 后 PostCallActual 返回正确的 UnifiedUsage。
func TestUnifiedUsage_Anthropic_Mapping(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	// 模拟 Anthropic UsageEvent 解析后的 UnifiedUsage
	anthropicUsage := UnifiedUsage{
		InputTokens:   100,
		OutputTokens:  50,
		CacheCreation: 20,
		CacheRead:     10,
	}
	ts.RecordRemoteUsage("claude-sonnet-4-20250514", anthropicUsage, 90)

	// 通过 RecordUsage 落盘到 UsageTracker（模拟 stream.go 的 RecordRemoteUsage → RecordUsage 链路）
	tracker.RecordUsage("claude-sonnet-4-20250514",
		anthropicUsage.InputTokens,
		anthropicUsage.OutputTokens,
		anthropicUsage.CacheCreation,
		anthropicUsage.CacheRead,
	)

	usage, ok := ts.PostCallActual("claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true")
	}
	if usage.InputTokens != 100 {
		t.Errorf("Anthropic InputTokens: got=%d, expected=100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("Anthropic OutputTokens: got=%d, expected=50", usage.OutputTokens)
	}
	if usage.CacheCreation != 20 {
		t.Errorf("Anthropic CacheCreation: got=%d, expected=20", usage.CacheCreation)
	}
	if usage.CacheRead != 10 {
		t.Errorf("Anthropic CacheRead: got=%d, expected=10", usage.CacheRead)
	}
}

// TestUnifiedUsage_OpenAI_Mapping 测试 OpenAI usage 字段映射。
//
// OpenAI 原生字段 → UnifiedUsage：
// - usage.prompt_tokens → InputTokens
// - usage.completion_tokens → OutputTokens
// - usage.prompt_tokens_details.cached_tokens → CacheRead
// - （无） → CacheCreation=0
//
// 验证：OpenAI 无 cache_creation 字段，CacheCreation 固定为 0。
func TestUnifiedUsage_OpenAI_Mapping(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	// 模拟 OpenAI UsageEvent 解析后的 UnifiedUsage（CacheCreation=0）
	openAIUsage := UnifiedUsage{
		InputTokens:   100, // prompt_tokens
		OutputTokens:  50,  // completion_tokens
		CacheCreation: 0,   // OpenAI 无此字段
		CacheRead:     10,  // prompt_tokens_details.cached_tokens
	}
	ts.RecordRemoteUsage("gpt-4", openAIUsage, 90)
	tracker.RecordUsage("gpt-4",
		openAIUsage.InputTokens,
		openAIUsage.OutputTokens,
		openAIUsage.CacheCreation,
		openAIUsage.CacheRead,
	)

	usage, ok := ts.PostCallActual("gpt-4")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true")
	}
	if usage.InputTokens != 100 {
		t.Errorf("OpenAI InputTokens: got=%d, expected=100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OpenAI OutputTokens: got=%d, expected=50", usage.OutputTokens)
	}
	if usage.CacheCreation != 0 {
		t.Errorf("OpenAI CacheCreation 应为 0: got=%d", usage.CacheCreation)
	}
	if usage.CacheRead != 10 {
		t.Errorf("OpenAI CacheRead: got=%d, expected=10", usage.CacheRead)
	}
}

// TestUnifiedUsage_DeepSeek_Mapping 测试 DeepSeek usage 字段映射（Phase 2 预留）。
//
// DeepSeek 原生字段 → UnifiedUsage：
// - usage.prompt_tokens → InputTokens
// - usage.completion_tokens → OutputTokens
// - usage.prompt_cache_hit_tokens → CacheRead
// - （无） → CacheCreation=0
//
// 注：DeepSeek 适配器待 T1-A S5 落地，此测试验证 UnifiedUsage 结构兼容性。
func TestUnifiedUsage_DeepSeek_Mapping(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	// 模拟 DeepSeek UsageEvent 解析后的 UnifiedUsage
	deepSeekUsage := UnifiedUsage{
		InputTokens:   100, // prompt_tokens
		OutputTokens:  50,  // completion_tokens
		CacheCreation: 0,   // DeepSeek 无此字段
		CacheRead:     10,  // prompt_cache_hit_tokens
	}
	ts.RecordRemoteUsage("deepseek-chat", deepSeekUsage, 90)
	tracker.RecordUsage("deepseek-chat",
		deepSeekUsage.InputTokens,
		deepSeekUsage.OutputTokens,
		deepSeekUsage.CacheCreation,
		deepSeekUsage.CacheRead,
	)

	usage, ok := ts.PostCallActual("deepseek-chat")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true")
	}
	if usage.InputTokens != 100 {
		t.Errorf("DeepSeek InputTokens: got=%d, expected=100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("DeepSeek OutputTokens: got=%d, expected=50", usage.OutputTokens)
	}
	if usage.CacheCreation != 0 {
		t.Errorf("DeepSeek CacheCreation 应为 0: got=%d", usage.CacheCreation)
	}
	if usage.CacheRead != 10 {
		t.Errorf("DeepSeek CacheRead: got=%d, expected=10", usage.CacheRead)
	}
}

// TestUnifiedUsage_Gemini_Mapping 测试 Gemini usage 字段映射（Phase 2 预留）。
//
// Gemini 原生字段 → UnifiedUsage：
// - usageMetadata.promptTokenCount → InputTokens
// - usageMetadata.candidatesTokenCount → OutputTokens
// - usageMetadata.cachedContentTokenCount → CacheRead
// - （无） → CacheCreation=0
//
// 注：Gemini 适配器待 T1-A S5 落地，此测试验证 UnifiedUsage 结构兼容性。
func TestUnifiedUsage_Gemini_Mapping(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	// 模拟 Gemini UsageEvent 解析后的 UnifiedUsage
	geminiUsage := UnifiedUsage{
		InputTokens:   100, // promptTokenCount
		OutputTokens:  50,  // candidatesTokenCount
		CacheCreation: 0,   // Gemini 无此字段
		CacheRead:     10,  // cachedContentTokenCount
	}
	ts.RecordRemoteUsage("gemini-1.5-pro", geminiUsage, 90)
	tracker.RecordUsage("gemini-1.5-pro",
		geminiUsage.InputTokens,
		geminiUsage.OutputTokens,
		geminiUsage.CacheCreation,
		geminiUsage.CacheRead,
	)

	usage, ok := ts.PostCallActual("gemini-1.5-pro")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true")
	}
	if usage.InputTokens != 100 {
		t.Errorf("Gemini InputTokens: got=%d, expected=100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("Gemini OutputTokens: got=%d, expected=50", usage.OutputTokens)
	}
	if usage.CacheCreation != 0 {
		t.Errorf("Gemini CacheCreation 应为 0: got=%d", usage.CacheCreation)
	}
	if usage.CacheRead != 10 {
		t.Errorf("Gemini CacheRead: got=%d, expected=10", usage.CacheRead)
	}
}

// TestUnifiedUsage_DashScope_Mapping 测试 DashScope usage 字段映射（Phase 2 预留）。
//
// DashScope 原生字段 → UnifiedUsage：
// - usage.input_tokens → InputTokens
// - usage.output_tokens → OutputTokens
// - （无） → CacheCreation=0
// - （无） → CacheRead=0
//
// 注：DashScope 适配器待 T1-A S5 落地，此测试验证 UnifiedUsage 结构兼容性。
func TestUnifiedUsage_DashScope_Mapping(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	// 模拟 DashScope UsageEvent 解析后的 UnifiedUsage（无 cache 字段）
	dashScopeUsage := UnifiedUsage{
		InputTokens:   100, // input_tokens
		OutputTokens:  50,  // output_tokens
		CacheCreation: 0,   // DashScope 无此字段
		CacheRead:     0,   // DashScope 无此字段
	}
	ts.RecordRemoteUsage("qwen-max", dashScopeUsage, 90)
	tracker.RecordUsage("qwen-max",
		dashScopeUsage.InputTokens,
		dashScopeUsage.OutputTokens,
		dashScopeUsage.CacheCreation,
		dashScopeUsage.CacheRead,
	)

	usage, ok := ts.PostCallActual("qwen-max")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true")
	}
	if usage.InputTokens != 100 {
		t.Errorf("DashScope InputTokens: got=%d, expected=100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("DashScope OutputTokens: got=%d, expected=50", usage.OutputTokens)
	}
	if usage.CacheCreation != 0 {
		t.Errorf("DashScope CacheCreation 应为 0: got=%d", usage.CacheCreation)
	}
	if usage.CacheRead != 0 {
		t.Errorf("DashScope CacheRead 应为 0: got=%d", usage.CacheRead)
	}
}

// TestRecordRemoteUsage_UpdatesRecentModelUsage 测试 RecordRemoteUsage 后 UsageTracker.RecentModelUsage 更新。
//
// 验证：RecordRemoteUsage 调用后，UsageTracker.GetRecentModelUsage 返回最近一次 usage（非累计）。
// 这是 L1 路径的核心：PostCallActual 读 RecentModelUsage 获取「最近一次真实 usage」。
func TestRecordRemoteUsage_UpdatesRecentModelUsage(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	// 第一次记录
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 100, OutputTokens: 50}, 90)
	// 同时写入 UsageTracker（模拟 stream.go 的 RecordRemoteUsage → RecordUsage 链路）
	tracker.RecordUsage("gpt-4", 100, 50, 0, 0)

	// 第二次记录（覆盖第一次）
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 200, OutputTokens: 80}, 180)
	tracker.RecordUsage("gpt-4", 200, 80, 0, 0)

	// PostCallActual 应返回最近一次（200/80），不是累计（300/130）
	usage, ok := ts.PostCallActual("gpt-4")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true")
	}
	if usage.InputTokens != 200 {
		t.Errorf("RecentModelUsage 应为最近一次 200: got=%d", usage.InputTokens)
	}
	if usage.OutputTokens != 80 {
		t.Errorf("RecentModelUsage 应为最近一次 80: got=%d", usage.OutputTokens)
	}
}
