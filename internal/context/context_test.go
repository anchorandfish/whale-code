package context

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
)

func TestBudgetCheckState(t *testing.T) {
	budget := DefaultBudget(200000)

	if budget.CheckState(50000, 0) != StateOk {
		t.Error("25% 应为 ok")
	}
	if budget.CheckState(150000, 0) != StateWarning {
		t.Error("75% 应为 warning")
	}
	// P0-13: 主力线 200000 - 13000 = 187000；提示线 0.80*200000 = 160000
	// 170000 >= 160000 提示线 → should_compact（决策窗口内，主力线未到）
	if budget.CheckState(170000, 0) != StateShouldCompact {
		t.Error("170000 >= 160000 (0.80 提示线) 应为 should_compact")
	}
	if budget.CheckState(188000, 0) != StateShouldCompact {
		t.Error("188000 >= 187000 应为 should_compact")
	}
	if budget.CheckState(194000, 0) != StateBlocked {
		t.Error("97% 应为 blocked")
	}
}

func TestBudgetPostCompactMaxTokens(t *testing.T) {
	budget := DefaultBudget(200000)
	max := budget.PostCompactMaxTokens()
	expected := 200000 - RecoveryBudgetTokens
	if max != expected {
		t.Errorf("PostCompactMaxTokens = %d, want %d", max, expected)
	}
}

func TestBudgetSmallContextUsesPositiveDynamicFallbacks(t *testing.T) {
	budget := DefaultBudget(8000)
	// 小窗口 window-13000 <= 0 → CompactThreshold 回退百分比线 0.80*8000 = 6400
	if got := budget.CompactThreshold(); got != 6400 {
		t.Fatalf("CompactThreshold=%d, want 6400", got)
	}
	if got := budget.PostCompactMaxTokens(); got != 6000 {
		t.Fatalf("PostCompactMaxTokens=%d, want 6000", got)
	}
	if state := budget.CheckState(0, 0); state != StateOk {
		t.Fatalf("empty 8k context state=%v, want ok", state)
	}
	if state := budget.CheckState(6400, 0); state != StateShouldCompact {
		t.Fatalf("8k threshold state=%v, want should_compact", state)
	}
}

func TestCompactionConfigUsesModelRecoveryBudgetNotPreserveUserCap(t *testing.T) {
	cfg := DefaultCompactionConfig().WithBudget(DefaultBudget(200000))
	if cfg.MaxEstimatedTokens != 150000 {
		t.Fatalf("candidate max=%d, want 150000", cfg.MaxEstimatedTokens)
	}
	if cfg.PreservedUserTokenBudget() != 10000 {
		t.Fatalf("preserve-user cap=%d, want 10000", cfg.PreservedUserTokenBudget())
	}
}

func TestCompactionConfigReservesStaticRequestOverhead(t *testing.T) {
	cfg := DefaultCompactionConfig().WithBudgetAndOverhead(DefaultBudget(200000), 40000)
	if cfg.MaxEstimatedTokens != 110000 {
		t.Fatalf("candidate max=%d, want 110000 after reserving system/tools", cfg.MaxEstimatedTokens)
	}
	small := DefaultCompactionConfig().WithBudgetAndOverhead(DefaultBudget(8000), 7000)
	if small.MaxEstimatedTokens != 1 {
		t.Fatalf("exhausted small-window candidate max=%d, want positive fail-closed limit", small.MaxEstimatedTokens)
	}
}

func TestBudgetMeetsRecoveryBudget(t *testing.T) {
	budget := DefaultBudget(200000)
	if !budget.MeetsRecoveryBudget(100000) {
		t.Error("100000 tokens 应满足恢复预算")
	}
	if budget.MeetsRecoveryBudget(200000) {
		t.Error("200000 tokens 不应满足恢复预算")
	}
}

func TestBudgetUsagePercent(t *testing.T) {
	budget := DefaultBudget(200000)
	pct := budget.UsagePercent(100000)
	if pct != 50.0 {
		t.Errorf("UsagePercent = %f, want 50.0", pct)
	}
}

func TestCompactorCompact(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	messages := make([]session.Message, 10)
	for i := range messages {
		messages[i] = session.UserText("test message")
	}

	config := DefaultCompactionConfig()
	config.PreserveRecentMessages = 3

	result := compactor.Compact(messages, config)
	if result.RemovedCount != 7 {
		t.Errorf("RemovedCount = %d, want 7", result.RemovedCount)
	}
	if len(result.CompactedMessages) != 4 {
		t.Errorf("CompactedMessages count = %d, want 4 (1 summary + 3 preserved)", len(result.CompactedMessages))
	}
}

func TestCompactorCompactTooFew(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	messages := []session.Message{
		session.UserText("msg1"),
		session.UserText("msg2"),
	}

	config := DefaultCompactionConfig()
	config.PreserveRecentMessages = 4

	result := compactor.Compact(messages, config)
	if result.RemovedCount != 0 {
		t.Error("消息数少于保留数时不应压缩")
	}
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(3)

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.IsOpen() {
		t.Error("2 次失败不应打开熔断器")
	}

	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Error("3 次失败应打开熔断器")
	}

	cb.RecordSuccess()
	if cb.IsOpen() {
		t.Error("成功后应关闭熔断器")
	}
}

func TestOverflowRecovery(t *testing.T) {
	recovery := NewContextOverflowRecovery(2)
	if !recovery.ShouldRetry() {
		t.Error("初始应有重试机会")
	}

	recovery.Consume()
	recovery.Consume()
	if recovery.ShouldRetry() {
		t.Error("2 次消耗后不应有重试机会")
	}
}

func TestTruncationRecovery(t *testing.T) {
	recovery := NewTruncationRecovery()
	if recovery.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", recovery.MaxAttempts)
	}

	recovery.Consume()
	recovery.Consume()
	recovery.Consume()
	if recovery.ShouldRetry() {
		t.Error("3 次消耗后不应有重试机会")
	}

	recovery.Reset()
	if !recovery.ShouldRetry() {
		t.Error("重置后应有重试机会")
	}
}

func TestHasUnclosedCodeBlock(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"```go\nfmt.Println()\n```", false},
		{"```go\nfmt.Println()", true},
		{"no code blocks", false},
		{"```\ncode\n```\n```\nmore code", true},
	}

	for _, tt := range tests {
		if got := HasUnclosedCodeBlock(tt.text); got != tt.want {
			t.Errorf("HasUnclosedCodeBlock(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestDefaultCompactionConfig(t *testing.T) {
	cfg := DefaultCompactionConfig()
	if cfg.PreserveRecentMessages != 10 {
		t.Errorf("PreserveRecentMessages = %d, want 10", cfg.PreserveRecentMessages)
	}
	if cfg.Strategy != config.CompactBalanced {
		t.Errorf("Strategy = %s, want balanced", cfg.Strategy)
	}
}

// TestDefaultBudget 测试默认预算参数
// 压缩体系重构：CompactPercent 0.85 → 0.80（提示线）
func TestDefaultBudget(t *testing.T) {
	budget := DefaultBudget(200000)

	if budget.WarnPercent != 0.75 {
		t.Errorf("WarnPercent = %f, expected 0.75", budget.WarnPercent)
	}
	if budget.CompactPercent != DefaultCompactPercent {
		t.Errorf("CompactPercent = %f, expected %f", budget.CompactPercent, DefaultCompactPercent)
	}
	if budget.BlockPercent != 0.97 {
		t.Errorf("BlockPercent = %f, expected 0.97", budget.BlockPercent)
	}
	if budget.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, expected 200000", budget.ContextWindow)
	}
	if budget.RecoveryBudget != RecoveryBudgetTokens {
		t.Errorf("RecoveryBudget = %d, expected %d", budget.RecoveryBudget, RecoveryBudgetTokens)
	}
}

// TestCheckState_Levels 测试各状态级别的阈值判断
// 压缩体系重构：ShouldCompact 为 0.80 提示线（0.80*100000=80000）；
// 主力自动压缩线 87000（100000-13000）由 ShouldAutoCompact 独立判定，
// 96999~97000 之间从提示线状态直接进入 Blocked（97% 硬停）。
func TestCheckState_Levels(t *testing.T) {
	budget := DefaultBudget(100000)

	tests := []struct {
		tokens int
		want   State
	}{
		{0, StateOk},                // 0%
		{50000, StateOk},            // 50%
		{74999, StateOk},            // < 75%
		{75000, StateWarning},       // 75%
		{79999, StateWarning},       // < 80%
		{80000, StateShouldCompact}, // 80% 提示线（用户决策窗口）
		{86999, StateShouldCompact}, // < 87000 主力线，仍在决策窗口内
		{87000, StateShouldCompact}, // 87000 = 100000 - 13000 主力线（自动压缩另行触发）
		{90000, StateShouldCompact}, // 90%
		{96999, StateShouldCompact}, // < 97%
		{97000, StateBlocked},       // 97%
		{100000, StateBlocked},      // 100%
	}

	for _, tt := range tests {
		// H13-G1: CheckState 签名扩展为 (estimatedTokens, cachedInputTokens)，
		// 本测试不涉及缓存命中，cachedInputTokens 传 0 保持原行为
		got := budget.CheckState(tt.tokens, 0)
		if got != tt.want {
			t.Errorf("CheckState(%d) = %v, expected %v", tt.tokens, got, tt.want)
		}
	}
}

// TestCompact_Balanced 测试 CompactBalanced 策略保留近期消息，摘要旧消息
// 验证 balanced 策略的压缩行为：保留最近消息，生成摘要
func TestCompact_Balanced(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	// 创建 15 条消息
	messages := make([]session.Message, 15)
	for i := range messages {
		messages[i] = session.UserText(fmt.Sprintf("message %d", i))
	}

	cfg := DefaultCompactionConfig()
	cfg.Strategy = config.CompactBalanced
	cfg.PreserveRecentMessages = 5

	result := compactor.Compact(messages, cfg)

	// 应移除 10 条旧消息
	if result.RemovedCount != 10 {
		t.Errorf("RemovedCount = %d, expected 10", result.RemovedCount)
	}

	// 应有 6 条消息（1 条摘要 + 5 条保留）
	if len(result.CompactedMessages) != 6 {
		t.Errorf("CompactedMessages count = %d, expected 6", len(result.CompactedMessages))
	}

	// 第一条应为摘要
	if result.CompactedMessages[0].Role != session.RoleSystem {
		t.Error("第一条消息应为系统摘要")
	}

	// 保留的最近消息应保持原始内容
	lastMsg := result.CompactedMessages[len(result.CompactedMessages)-1]
	if !strings.Contains(lastMsg.Blocks[0].Text, "message 14") {
		t.Errorf("最后一条消息should contain 'message 14'，actual: %s", lastMsg.Blocks[0].Text)
	}
}

// TestCompact_PreserveUser 测试 CompactPreserveUser 策略优先保留User消息
// 验证 preserve_user 策略下User消息得到优先保留
func TestCompact_PreserveUser(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	// 创建交替的User和Assistant消息
	messages := make([]session.Message, 12)
	for i := range messages {
		if i%2 == 0 {
			messages[i] = session.UserText(fmt.Sprintf("user message %d", i))
		} else {
			messages[i] = session.AssistantText(fmt.Sprintf("assistant message %d", i), "model", nil)
		}
	}

	cfg := DefaultCompactionConfig()
	cfg.Strategy = config.CompactPreserveUser
	cfg.PreserveRecentMessages = 4

	result := compactor.Compact(messages, cfg)

	// 应移除 8 条旧消息
	if result.RemovedCount != 8 {
		t.Errorf("RemovedCount = %d, expected 8", result.RemovedCount)
	}

	// 保留的最近消息中应有User消息
	hasUserMsg := false
	for _, msg := range result.CompactedMessages {
		if msg.Role == session.RoleUser {
			hasUserMsg = true
			break
		}
	}
	if !hasUserMsg {
		t.Error("保留的消息中should containUser消息")
	}
}

func TestCompact_PreserveUserAppliesDeterministicTokenCap(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)
	messages := []session.Message{
		session.UserText("FIRST-MARKER " + strings.Repeat("very-long-user-input ", 20)),
		session.AssistantText("old answer", "model", nil),
		session.UserText("recent user"),
		session.AssistantText("recent answer", "model", nil),
	}
	cfg := DefaultCompactionConfig()
	cfg.Strategy = config.CompactPreserveUser
	cfg.PreserveRecentMessages = 2

	// Give the fixed summary + recent suffix eight additional tokens for the
	// old user turn. This verifies that the preservation budget is applied to
	// the complete candidate rather than being consumed before fixed overhead.
	baselineCfg := cfg
	baselineCfg.Strategy = config.CompactBalanced
	baselineCfg.MaxEstimatedTokens = 0
	baseline := compactor.Compact(messages, baselineCfg)
	cfg.MaxEstimatedTokens = baseline.PostTokens + 8

	result := compactor.Compact(messages, cfg)
	if !result.Changed {
		t.Fatal("expected compaction")
	}
	var preservedOldUser string
	for _, msg := range result.CompactedMessages {
		if msg.Role == session.RoleUser && strings.Contains(msg.TextContent(), "FIRST-MARKER") {
			preservedOldUser = msg.TextContent()
			break
		}
	}
	if preservedOldUser == "" {
		t.Fatal("preserve-user should keep the earliest marker within the cap")
	}
	if preservedOldUser == messages[0].TextContent() {
		t.Fatal("over-budget user message should be truncated deterministically")
	}
}

// TestCompact_Aggressive 测试 CompactAggressive 策略保留最少消息
// 验证 aggressive 策略下只保留最少量的消息
func TestCompact_Aggressive(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	// 创建大量消息
	messages := make([]session.Message, 20)
	for i := range messages {
		messages[i] = session.UserText(fmt.Sprintf("message %d", i))
	}

	cfg := DefaultCompactionConfig()
	cfg.Strategy = config.CompactAggressive
	cfg.PreserveRecentMessages = 3

	result := compactor.Compact(messages, cfg)

	// 应移除 17 条旧消息
	if result.RemovedCount != 17 {
		t.Errorf("RemovedCount = %d, expected 17", result.RemovedCount)
	}

	// 应有 4 条消息（1 条摘要 + 3 条保留）
	if len(result.CompactedMessages) != 4 {
		t.Errorf("CompactedMessages count = %d, expected 4", len(result.CompactedMessages))
	}
}

// TestGenerateSummary 测试摘要生成包含关键信息
// 验证摘要包含User intent、文件路径和决策信息
func TestGenerateSummary(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	// 构造包含关键信息的消息（文件路径需包含 / 或 \ 以及扩展名）
	messages := []session.Message{
		session.UserText("请帮我implementationUser登录功能"),
		session.AssistantText("好的，我选择使用 JWT 认证approach，修改 auth/handler.go 和 auth/middleware.go", "model", nil),
		session.UserText("请检查 src/main.go 中的错误"),
	}

	summary := compactor.generateSummary(messages)

	// 验证摘要包含User intent
	if !strings.Contains(summary, "User intent") {
		t.Error("摘要should contain 'User intent'")
	}
	if !strings.Contains(summary, "implementationUser登录功能") {
		t.Error("摘要should containUser intent内容")
	}

	// 验证摘要包含文件路径
	if !strings.Contains(summary, "Files involved") {
		t.Error("摘要should contain 'Files involved'")
	}

	if !strings.Contains(summary, "Key decisions") {
		t.Error("摘要should contain 'Key decisions'")
	}
}

// TestContextCollapse test context折叠压缩
// 验证 ContextCollapser 创建折叠视图，保留近期消息，生成折叠摘要
func TestContextCollapse(t *testing.T) {
	// 构造包含工具调用和文件引用的消息序列
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "task 1: edit src/main.go"}}, Timestamp: 1000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "response 1"}, {Type: "tool_use", Name: "read_file", ID: "t1"}}, Timestamp: 2000},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Text: "file content"}}, Timestamp: 3000},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "task 2"}}, Timestamp: 4000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "response 2"}}, Timestamp: 5000},
	}

	collapser := NewContextCollapser()
	result := collapser.Collapse(messages, 2) // 保留最近 2 条消息

	// 应保留近期消息（1 条摘要 + 2 条保留 = 3 条）
	if len(result.PreservedMessages) != 3 {
		t.Errorf("Expected 3 preserved messages (1 summary + 2 recent), got %d", len(result.PreservedMessages))
	}

	// 应有折叠摘要
	if result.CollapsedSummary == "" {
		t.Error("Should have collapsed summary")
	}

	// 应保留折叠部分中的文件引用
	if len(result.PreservedFiles) == 0 {
		t.Error("Should preserve file references from collapsed messages")
	}

	// 第一条消息应为系统摘要
	if result.PreservedMessages[0].Role != session.RoleSystem {
		t.Error("First preserved message should be system summary")
	}
}

// TestContextCollapsePreservesMarkers 测试折叠视图保留上下文标记
// 验证折叠压缩能提取文件路径等关键上下文标记
func TestContextCollapsePreservesMarkers(t *testing.T) {
	// 折叠视图应保留上下文标记（文件路径、决策）
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "edit main.go"}}, Timestamp: 1000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "tool_use", Name: "edit_file", ID: "t1"}}, Timestamp: 2000},
		{Role: session.RoleTool, Blocks: []session.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Text: "edited main.go successfully"}}, Timestamp: 3000},
	}

	collapser := NewContextCollapser()
	result := collapser.Collapse(messages, 1)

	// 应提取文件引用
	found := false
	for _, f := range result.PreservedFiles {
		if strings.Contains(f, "main.go") {
			found = true
		}
	}
	if !found {
		t.Error("Should preserve main.go as a file reference")
	}
}

// TestCacheAwareCompactor test cache感知压缩器
// 验证 CacheAwareCompactor 能正常执行压缩并返回结果
func TestCacheAwareCompactor(t *testing.T) {
	compactor := NewCacheAwareCompactor(ai.NewTokenEstimator())

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}, Timestamp: 1000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "world"}}, Timestamp: 2000},
	}

	cfg := DefaultCompactionConfig()
	result := compactor.Compact(messages, cfg)

	if result == nil {
		t.Error("CacheAwareCompactor should return a result")
	}
}

// TestCacheAwareCompactor_ShouldCompactBasedOnCache test cache状态判断
// 验证根据缓存删除比例判断是否需要压缩
func TestCacheAwareCompactor_ShouldCompactBasedOnCache(t *testing.T) {
	compactor := NewCacheAwareCompactor(ai.NewTokenEstimator())

	// 缓存删除比例低于 50%，不应压缩
	lowDeletion := &ai.CacheInfo{CacheDeletedTokens: 30, TotalCachedTokens: 100, CacheReadTokens: 70}
	if compactor.ShouldCompactBasedOnCache(lowDeletion, 0.5) {
		t.Error("Should not compact when deletion ratio < 50%")
	}

	// 缓存删除比例高于 50%，应压缩
	highDeletion := &ai.CacheInfo{CacheDeletedTokens: 60, TotalCachedTokens: 100, CacheReadTokens: 40}
	if !compactor.ShouldCompactBasedOnCache(highDeletion, 0.5) {
		t.Error("Should compact when deletion ratio > 50%")
	}

	// 无缓存 token，不应压缩
	noCache := &ai.CacheInfo{}
	if compactor.ShouldCompactBasedOnCache(noCache, 0.5) {
		t.Error("Should not compact when no cached tokens")
	}

	// nil 缓存信息，不应压缩
	if compactor.ShouldCompactBasedOnCache(nil, 0.5) {
		t.Error("Should not compact when cache info is nil")
	}
}

// TestSnipCompactor 测试裁剪压缩器
// 验证 SnipCompactor 移除空内容的僵尸消息
func TestSnipCompactor(t *testing.T) {
	snipper := NewSnipCompactor(ai.NewTokenEstimator())

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}, Timestamp: 1000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{}, Timestamp: 2000},                    // 空消息
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: ""}}, Timestamp: 3000}, // 空文本
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "response"}}, Timestamp: 4000},
	}

	result := snipper.Snip(messages)

	// 应移除 2 条空消息
	if result.RemovedCount != 2 {
		t.Errorf("Expected 2 removed messages, got %d", result.RemovedCount)
	}

	// 应保留 2 条有效消息
	if len(result.Messages) != 2 {
		t.Errorf("Expected 2 remaining messages, got %d", len(result.Messages))
	}
}

// TestFourLayerPipeline 测试四层压缩管线可composition性
// 验证所有 4 层压缩器可以独立使用
func TestFourLayerPipeline(t *testing.T) {
	estimator := ai.NewTokenEstimator()

	// Layer 1: Snip（移除僵尸消息）
	snipper := NewSnipCompactor(estimator)

	// Layer 2: Micro（渐进式截断）
	microConfig := MicroCompactConfig{
		PreserveRecent:      3,
		TimeThreshold:       5 * time.Minute,
		TruncationThreshold: 500,
	}

	// Layer 3: Collapse（折叠视图）
	collapser := NewContextCollapser()

	// Layer 4: Auto（启发式摘要）
	auto := NewCompactor(estimator)

	// 构造测试消息
	messages := []session.Message{
		session.UserText("test message 1"),
		session.UserText("test message 2"),
		session.UserText("test message 3"),
	}

	// Layer 1: Snip
	snipResult := snipper.Snip(messages)
	if snipResult == nil {
		t.Error("Snip should return a result")
	}

	// Layer 2: Micro
	microResult := MicroCompact(snipResult.Messages, microConfig)
	if microResult == nil {
		t.Error("MicroCompact should return a result")
	}

	// Layer 3: Collapse
	collapseResult := collapser.Collapse(microResult, 1)
	if collapseResult == nil {
		t.Error("Collapse should return a result")
	}

	// Layer 4: Auto
	cfg := DefaultCompactionConfig()
	autoResult := auto.Compact(microResult, cfg)
	if autoResult == nil {
		t.Error("Auto Compact should return a result")
	}

	// 验证所有层都能正常工作
	_ = snipResult
	_ = collapseResult
	_ = autoResult
}

type countingCompactionCache struct {
	deletions   int
	compactions int
}

func (c *countingCompactionCache) NotifyCacheDeletion() { c.deletions++ }
func (c *countingCompactionCache) NotifyCompaction()    { c.compactions++ }
func (c *countingCompactionCache) GetCapability() ai.CacheCapability {
	return ai.CacheCapability{}
}

func TestAutomaticCompactionRejectsGrowingCandidateBeforeCacheNotifications(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)
	cache := &countingCompactionCache{}
	pipeline.cachingIntegration = cache
	var messages []session.Message
	for i := 0; i < 6; i++ {
		messages = append(messages, session.UserText(fmt.Sprintf("CONTEXT-%c", 'A'+rune(i))))
		messages = append(messages, session.AssistantText(fmt.Sprintf("CONTEXT-%c", 'A'+rune(i)), "m", nil))
	}

	result := pipeline.Execute(messages, DefaultBudget(30), nil)
	if result.Changed || result.NoOpReason != CompactionNoOpNotBeneficial {
		t.Fatalf("result changed=%v reason=%s, want rejected growing candidate", result.Changed, result.NoOpReason)
	}
	if len(result.Messages) != len(messages) {
		t.Fatalf("automatic rollback changed messages: %d -> %d", len(messages), len(result.Messages))
	}
	if cache.deletions != 0 || cache.compactions != 0 {
		t.Fatalf("cache notifications fired before candidate acceptance: %+v", cache)
	}
}

func TestManualPipelineHonorsStrategyProfiles(t *testing.T) {
	var messages []session.Message
	for i := 0; i < 6; i++ {
		marker := fmt.Sprintf("CONTEXT-%c", 'A'+rune(i))
		messages = append(messages, session.UserText(marker), session.AssistantText(marker, "m", nil))
	}
	tests := []struct {
		name      string
		strategy  config.CompactionStrategy
		wantCount int
	}{
		{name: "balanced keeps ten plus summary", strategy: config.CompactBalanced, wantCount: 11},
		{name: "aggressive keeps three plus summary", strategy: config.CompactAggressive, wantCount: 4},
		{name: "preserve-user keeps old user plus recent ten and summary", strategy: config.CompactPreserveUser, wantCount: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
			cfg := DefaultCompactionConfig()
			cfg.Strategy = tt.strategy
			result := pipeline.ExecuteWithOptions(messages, DefaultBudget(200000), nil, ExecuteOptions{
				Trigger: CompactionTriggerManual, Policy: CompactionPolicyForce, Config: cfg,
			})
			if !result.Changed || len(result.Messages) != tt.wantCount {
				t.Fatalf("changed=%v messages=%d, want true/%d", result.Changed, len(result.Messages), tt.wantCount)
			}
			if tt.strategy == config.CompactPreserveUser {
				found := false
				for _, msg := range result.Messages {
					found = found || (msg.Role == session.RoleUser && msg.TextContent() == "CONTEXT-A")
				}
				if !found {
					t.Fatal("preserve-user pipeline lost the earliest user marker")
				}
			}
		})
	}
}

// ============================================================================
// Section 18: 性能benchmark
// ============================================================================

// BenchmarkContextCompaction benchmark 100 条消息上下文的压缩性能
func BenchmarkContextCompaction(b *testing.B) {
	estimator := ai.NewTokenEstimator()
	compactor := NewCompactor(estimator)

	// 创建 100 条消息
	messages := make([]session.Message, 100)
	for i := range messages {
		if i%2 == 0 {
			messages[i] = session.UserText(fmt.Sprintf("user message %d: please implement feature %d with details about the implementation approach", i, i))
		} else {
			messages[i] = session.AssistantText(fmt.Sprintf("assistant message %d: I will implement feature %d using the following approach with src/handler.go and src/service.go", i, i), "model", nil)
		}
	}

	cfg := DefaultCompactionConfig()
	cfg.PreserveRecentMessages = 10

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compactor.Compact(messages, cfg)
	}
}

// === N11 修复测试 ===

// TestIsPipelineEnabled_DefaultTrue 验证 5 层管线默认启用（N11 修复 G9）。
// 修改前：默认 DISABLED（安全灰度）；修改后：默认 ENABLED（生产启用）。
func TestIsPipelineEnabled_DefaultTrue(t *testing.T) {
	// 确保 env 未设置
	os.Unsetenv(EnvPipeline)

	if !IsPipelineEnabled() {
		t.Error("G9: IsPipelineEnabled() 默认应为 true（生产启用 5 层管线）")
	}

	// 显式设置为 "0" 应关闭
	os.Setenv(EnvPipeline, "0")
	defer os.Unsetenv(EnvPipeline)
	if IsPipelineEnabled() {
		t.Error("G9: WHALE_CTX_PIPELINE=0 时 IsPipelineEnabled() 应为 false")
	}

	// 显式设置为 "false" 应关闭
	os.Setenv(EnvPipeline, "false")
	if IsPipelineEnabled() {
		t.Error("G9: WHALE_CTX_PIPELINE=false 时 IsPipelineEnabled() 应为 false")
	}

	// 显式设置为 "1" 应启用
	os.Setenv(EnvPipeline, "1")
	if !IsPipelineEnabled() {
		t.Error("G9: WHALE_CTX_PIPELINE=1 时 IsPipelineEnabled() 应为 true")
	}
}

// TestPipeline_SetCachingIntegration 验证 CachingIntegration 注入（N11 修复 G3）。
func TestPipeline_SetCachingIntegration(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	// 初始应为 nil
	if pipeline.cachingIntegration != nil {
		t.Error("初始 cachingIntegration 应为 nil")
	}

	// 注入
	ci := ai.NewCachingIntegration("claude-sonnet-4-20250514", 0.05)
	pipeline.SetCachingIntegration(ci)

	if pipeline.cachingIntegration == nil {
		t.Error("SetCachingIntegration 后 cachingIntegration 不应为 nil")
	}
}

// TestNotifyCacheRebuild_InjectsCacheControlMarkers 验证 notifyCacheRebuild
// 实际调用 InjectCacheControlMarkers（审查报告 v2 修订项 1 修复）。
// 修复前：仅占位注释 _ = messages，未实际注入 cache_control 标记。
// 修复后：从 cachingIntegration.GetCapability() 获取缓存能力 →
// ai.DefaultCacheControl 生成 cc → findCacheBoundary 定位 →
// InjectCacheControlMarkers 注入到 boundary 消息的 Metadata。
func TestNotifyCacheRebuild_InjectsCacheControlMarkers(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	// 注入 CachingIntegration（claude-sonnet-4 支持 Anthropic 风格 cache_control）
	ci := ai.NewCachingIntegration("claude-sonnet-4-20250514", 0.05)
	pipeline.SetCachingIntegration(ci)

	// 构造消息：1 条 user 消息（first user message 是自然 cache breakpoint）
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	// 调用 notifyCacheRebuild
	result := pipeline.notifyCacheRebuild(messages)

	// 验证 cache_control 标记已注入到 boundary 消息
	if len(result) == 0 {
		t.Fatal("notifyCacheRebuild 应返回非空消息列表")
	}
	boundaryMsg := result[0]
	if boundaryMsg.Metadata == nil {
		t.Fatal("boundary 消息应有 Metadata，got nil")
	}
	cc, ok := boundaryMsg.Metadata["cache_control"]
	if !ok {
		t.Fatal("boundary 消息 Metadata 应包含 cache_control 键（审查报告 v2 修订项 1）")
	}
	// 验证 cache_control 值类型为 ai.CacheControl
	ccVal, ok := cc.(ai.CacheControl)
	if !ok {
		t.Fatalf("cache_control 值应为 ai.CacheControl 类型，got %T", cc)
	}
	if ccVal.Type != "ephemeral" {
		t.Errorf("cache_control.Type = %q, want %q", ccVal.Type, "ephemeral")
	}
}

// TestNotifyCacheRebuild_SkipsWhenCachingIntegrationNil 验证
// cachingIntegration=nil 时跳过注入，返回原消息。
func TestNotifyCacheRebuild_SkipsWhenCachingIntegrationNil(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	// cachingIntegration 为 nil，应跳过注入
	result := pipeline.notifyCacheRebuild(messages)
	if len(result) != 1 {
		t.Errorf("应返回 1 条消息，got %d", len(result))
	}
	// 验证未注入 cache_control（因为 cachingIntegration=nil）
	if result[0].Metadata != nil {
		if _, ok := result[0].Metadata["cache_control"]; ok {
			t.Error("cachingIntegration=nil 时不应注入 cache_control")
		}
	}
}

// TestNotifyCacheRebuild_SkipsForNonAnthropicModel 验证模型不支持
// Anthropic 风格 cache_control 时跳过注入（DefaultCacheControl 返回空值）。
func TestNotifyCacheRebuild_SkipsForNonAnthropicModel(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	// 使用 DeepSeek 模型（不支持 Anthropic 风格 cache_control）
	ci := ai.NewCachingIntegration("deepseek-chat", 0.05)
	pipeline.SetCachingIntegration(ci)

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	result := pipeline.notifyCacheRebuild(messages)
	// DeepSeek 不支持 cache_control，应跳过注入
	if result[0].Metadata != nil {
		if _, ok := result[0].Metadata["cache_control"]; ok {
			t.Error("DeepSeek 模型不应注入 cache_control（DefaultCacheControl 返回空值）")
		}
	}
}

// TestPipeline_CircuitBreakerWarning 验证断路器告警消息（N11 修复 G10）。
func TestPipeline_CircuitBreakerWarning(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	// NewCompactionPipelineNoAI 不创建 aiCompactor，应返回空字符串
	warning := pipeline.CircuitBreakerWarning()
	if warning != "" {
		t.Errorf("无 aiCompactor 时 CircuitBreakerWarning() 应返回空，got %q", warning)
	}

	// 使用 NewCompactionPipeline（带 AI client）创建，但 nil client
	pipeline2 := NewCompactionPipeline(est, nil)
	// 断路器未打开时应返回空
	warning2 := pipeline2.CircuitBreakerWarning()
	if warning2 != "" {
		t.Errorf("断路器未打开时 CircuitBreakerWarning() 应返回空，got %q", warning2)
	}

	// 手动触发 3 次失败打开断路器
	if pipeline2.aiCompactor != nil && pipeline2.aiCompactor.circuitBreaker != nil {
		for i := 0; i < 3; i++ {
			pipeline2.aiCompactor.circuitBreaker.RecordFailure()
		}
		warning3 := pipeline2.CircuitBreakerWarning()
		if warning3 == "" {
			t.Error("断路器打开后 CircuitBreakerWarning() 应返回非空告警")
		}
		if !strings.Contains(warning3, "compact 断路器已打开") {
			t.Errorf("告警消息应包含 'compact 断路器已打开'，got %q", warning3)
		}
	}
}

// TestPipeline_EstimateMessagesTokens_ImageAndThinking 验证 image 块 + thinking 块
// 的 token 估算（N11 修复 G8，P1-10）。
// 借鉴同类 CLI estimateMessageTokens：
// - image 块固定 2000 token
// - thinking 块按字符估算
// - 总 token × 4/3 padding
func TestPipeline_EstimateMessagesTokens_ImageAndThinking(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	// 纯文本消息
	textMsgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "text", Text: "hello world"},
		}},
	}
	textTokens := pipeline.estimateMessagesTokens(textMsgs)
	if textTokens <= 0 {
		t.Errorf("文本消息 token 估算应 > 0，got %d", textTokens)
	}

	// 含 image 块的消息（应额外 +2000）
	imageMsgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "text", Text: "hello"},
			{Type: "image", Base64Data: "base64data"},
		}},
	}
	imageTokens := pipeline.estimateMessagesTokens(imageMsgs)
	// image 块固定 2000，text 块按字符估算，总和 × 4/3
	// 至少应比纯 text 多（因为 image +2000）
	textOnlyTokens := pipeline.estimateMessagesTokens([]session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "text", Text: "hello"},
		}},
	})
	if imageTokens <= textOnlyTokens {
		t.Errorf("G8: 含 image 块的 token 估算 (%d) 应 > 纯 text (%d)，image 应 +2000",
			imageTokens, textOnlyTokens)
	}
}

// TestPipeline_EstimateMessagesTokensUsesSharedEstimator verifies that budget
// checks and user-visible estimates use the same token contract.
func TestPipeline_EstimateMessagesTokensUsesSharedEstimator(t *testing.T) {
	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(est)

	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "text", Text: strings.Repeat("a", 300)}, // 约 75 token（4 char/token）
		}},
	}

	got := pipeline.estimateMessagesTokens(msgs)
	want := est.EstimateSessionMessagesTokens(msgs)
	if got != want {
		t.Errorf("pipeline token estimate = %d, want shared estimator value %d", got, want)
	}
}

// TestSnip_TombstonedRemoved 验证 Snip 移除 Tombstoned 标记的消息（N11 修复 G15）。
func TestSnip_TombstonedRemoved(t *testing.T) {
	snipper := NewSnipCompactor(ai.NewTokenEstimator())

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "keep"}}, Tombstoned: false},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "tombstoned"}}, Tombstoned: true},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "keep2"}}, Tombstoned: false},
	}

	result := snipper.Snip(messages)

	if result.RemovedCount != 1 {
		t.Errorf("G15: 应移除 1 条 Tombstoned 消息，got %d", result.RemovedCount)
	}
	if len(result.Messages) != 2 {
		t.Errorf("G15: 应保留 2 条消息，got %d", len(result.Messages))
	}
	// 验证保留的消息不含 tombstoned 的
	for _, msg := range result.Messages {
		if msg.Tombstoned {
			t.Error("G15: 保留的消息不应包含 Tombstoned=true 的")
		}
	}
}

// TestSnipWithBudget_TombstonedRemoved 验证 SnipWithBudget 也移除 Tombstoned。
func TestSnipWithBudget_TombstonedRemoved(t *testing.T) {
	snipper := NewSnipCompactor(ai.NewTokenEstimator())

	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "keep"}}, Tombstoned: false},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "tombstoned"}}, Tombstoned: true},
	}

	result := snipper.SnipWithBudget(messages, 10000)

	if result.RemovedCount != 1 {
		t.Errorf("G15: SnipWithBudget 应移除 1 条 Tombstoned 消息，got %d", result.RemovedCount)
	}
}

// TestCircuitBreaker_WarningMessage 验证断路器告警消息格式（N11 修复 G10）。
func TestCircuitBreaker_WarningMessage(t *testing.T) {
	cb := NewCircuitBreaker(3)

	// 未打开时应返回空
	if msg := cb.WarningMessage(); msg != "" {
		t.Errorf("断路器未打开时 WarningMessage() 应返回空，got %q", msg)
	}

	// 触发 3 次失败打开断路器
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// 打开后应返回非空告警
	msg := cb.WarningMessage()
	if msg == "" {
		t.Error("断路器打开后 WarningMessage() 应返回非空告警")
	}
	if !strings.Contains(msg, "compact 断路器已打开") {
		t.Errorf("告警消息应包含 'compact 断路器已打开'，got %q", msg)
	}
	if !strings.Contains(msg, "连续失败") {
		t.Errorf("告警消息应包含 '连续失败'，got %q", msg)
	}

	// RecordSuccess 后应重置
	cb.RecordSuccess()
	if msg := cb.WarningMessage(); msg != "" {
		t.Errorf("RecordSuccess 后 WarningMessage() 应返回空，got %q", msg)
	}
}
