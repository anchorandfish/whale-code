package context

import (
	"strings"
	"testing"
)

// TestContextOverflowRecoveryBasic test context溢出恢复基本流程
func TestContextOverflowRecoveryBasic(t *testing.T) {
	recovery := NewContextOverflowRecovery(3)

	if !recovery.ShouldRetry() {
		t.Error("Expected ShouldRetry=true initially")
	}

	recovery.Consume()
	if recovery.RemainingAttempts != 2 {
		t.Errorf("Expected 2 remaining, got %d", recovery.RemainingAttempts)
	}
}

// TestContextOverflowRecoveryExhausted test recovery次数耗尽
func TestContextOverflowRecoveryExhausted(t *testing.T) {
	recovery := NewContextOverflowRecovery(3)

	for i := 0; i < 3; i++ {
		if !recovery.ShouldRetry() {
			t.Errorf("Expected ShouldRetry=true at attempt %d", i)
		}
		recovery.Consume()
	}

	if recovery.ShouldRetry() {
		t.Error("Expected ShouldRetry=false after exhausting attempts")
	}
}

// TestContextOverflowRecovery_Consume 测试逐步消耗恢复机会
// 验证每次 Consume 后 RemainingAttempts 递减，CanRecover 状态正确变化
func TestContextOverflowRecovery_Consume(t *testing.T) {
	recovery := NewContextOverflowRecovery(3)

	// 初始状态：3 次机会
	if recovery.RemainingAttempts != 3 {
		t.Errorf("初始 RemainingAttempts = %d, expected 3", recovery.RemainingAttempts)
	}
	if !recovery.ShouldRetry() {
		t.Error("初始 ShouldRetry 应为 true")
	}

	// 第一次消耗：3 -> 2
	recovery.Consume()
	if recovery.RemainingAttempts != 2 {
		t.Errorf("第一次消耗后 RemainingAttempts = %d, expected 2", recovery.RemainingAttempts)
	}
	if !recovery.ShouldRetry() {
		t.Error("第一次消耗后 ShouldRetry 应为 true")
	}

	// 第二次消耗：2 -> 1
	recovery.Consume()
	if recovery.RemainingAttempts != 1 {
		t.Errorf("第二次消耗后 RemainingAttempts = %d, expected 1", recovery.RemainingAttempts)
	}
	if !recovery.ShouldRetry() {
		t.Error("第二次消耗后 ShouldRetry 应为 true")
	}

	// 第三次消耗：1 -> 0，CanRecover 变为 false
	recovery.Consume()
	if recovery.RemainingAttempts != 0 {
		t.Errorf("第三次消耗后 RemainingAttempts = %d, expected 0", recovery.RemainingAttempts)
	}
	if recovery.ShouldRetry() {
		t.Error("三次消耗后 ShouldRetry 应为 false")
	}
}

// TestContextOverflowRecoveryReset 测试重置
func TestContextOverflowRecoveryReset(t *testing.T) {
	recovery := NewContextOverflowRecovery(3)
	recovery.Consume()
	recovery.Consume()

	recovery.Reset()

	if recovery.RemainingAttempts != 3 {
		t.Errorf("Expected 3 remaining after reset, got %d", recovery.RemainingAttempts)
	}
}

// TestMaxOutputTokensRecovery test output截断恢复逐步升级
func TestMaxOutputTokensRecovery(t *testing.T) {
	recovery := NewMaxOutputTokensRecovery(65536)

	if recovery.CurrentMaxTokens != 16384 {
		t.Errorf("Expected initial max=16384, got %d", recovery.CurrentMaxTokens)
	}

	// 第一次升级：16384 -> 32768
	newMax := recovery.Escalate()
	if newMax != 32768 {
		t.Errorf("Expected escalated max=32768 after first escalation, got %d", newMax)
	}

	if !recovery.HasEscalated {
		t.Error("Expected HasEscalated=true after escalation")
	}
}

// TestMaxOutputTokensRecoveryAlreadyEscalated 测试逐步升级完整流程
func TestMaxOutputTokensRecoveryAlreadyEscalated(t *testing.T) {
	recovery := NewMaxOutputTokensRecovery(65536)

	// 第一次升级：16384 -> 32768
	newMax := recovery.Escalate()
	if newMax != 32768 {
		t.Errorf("Expected max=32768 after first escalation, got %d", newMax)
	}

	// 第二次升级：32768 -> 65536
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("Expected max=65536 after second escalation, got %d", newMax)
	}

	// 第三次升级：已到最大值，不再提升
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("Expected max=65536 after third escalation (no change), got %d", newMax)
	}
}

// TestTruncationRecoveryContinuation 测试截断续写
func TestTruncationRecoveryContinuation(t *testing.T) {
	recovery := NewTruncationRecovery()

	if !recovery.ShouldRetry() {
		t.Error("Expected ShouldRetry=true initially")
	}

	msg := TruncationRecoveryMessage
	if msg == "" {
		t.Error("Expected non-empty continuation message")
	}
}

// TestTruncationRecoveryDetectUnclosedBlock 测试未闭合代码块检测
func TestTruncationRecoveryDetectUnclosedBlock(t *testing.T) {
	tests := []struct {
		text     string
		expected bool
	}{
		{"```python\nprint('hello')", true},
		{"```python\nprint('hello')\n```", false},
		{"no code blocks here", false},
		{"```\ncode\n```\n```\nmore code", true},
		{"```\ncode\n```\n```\nmore code\n```", false},
	}

	for _, tt := range tests {
		result := HasUnclosedCodeBlock(tt.text)
		if result != tt.expected {
			t.Errorf("HasUnclosedCodeBlock(%q) = %v, want %v", tt.text, result, tt.expected)
		}
	}
}

// TestErrorRecoveryOrchestrator test error恢复编排器
func TestErrorRecoveryOrchestrator(t *testing.T) {
	orchestrator := NewErrorRecoveryOrchestrator()

	if orchestrator == nil {
		t.Fatal("Expected non-nil orchestrator")
	}

	// test context溢出恢复策略
	strategy := orchestrator.GetRecoveryStrategy("上下文溢出: prompt too long")
	if strategy.Type != RecoveryTypeCompact {
		t.Errorf("Expected RecoveryTypeCompact, got %s", strategy.Type)
	}

	// test output截断恢复策略
	strategy = orchestrator.GetRecoveryStrategy("max_tokens exceeded")
	if strategy.Type != RecoveryTypeEscalateTokens {
		t.Errorf("Expected RecoveryTypeEscalateTokens, got %s", strategy.Type)
	}

	// 测试过载恢复策略
	strategy = orchestrator.GetRecoveryStrategy("529 overloaded")
	if strategy.Type != RecoveryTypeRetryWithBackoff {
		t.Errorf("Expected RecoveryTypeRetryWithBackoff, got %s", strategy.Type)
	}

	// 测试不可恢复错误
	strategy = orchestrator.GetRecoveryStrategy("authentication failed")
	if strategy.Type != RecoveryTypeNone {
		t.Errorf("Expected RecoveryTypeNone, got %s", strategy.Type)
	}
}

// TestErrorRecoveryOrchestratorMaxAttempts 测试最大恢复次数
func TestErrorRecoveryOrchestratorMaxAttempts(t *testing.T) {
	orchestrator := NewErrorRecoveryOrchestrator()

	// 第一次溢出
	strategy := orchestrator.GetRecoveryStrategy("上下文溢出: prompt too long")
	if !strategy.CanRecover {
		t.Error("Expected CanRecover=true on first overflow")
	}
	orchestrator.RecordRecovery(RecoveryTypeCompact)

	// 第二次溢出
	strategy = orchestrator.GetRecoveryStrategy("上下文溢出: prompt too long")
	if !strategy.CanRecover {
		t.Error("Expected CanRecover=true on second overflow")
	}
	orchestrator.RecordRecovery(RecoveryTypeCompact)

	// 第三次溢出 — 超过 ContextOverflowRecovery 的 MaxAttempts=2 限制
	strategy = orchestrator.GetRecoveryStrategy("上下文溢出: prompt too long")
	if strategy.CanRecover {
		t.Error("Expected CanRecover=false after max recoveries")
	}
}

// TestCompactStrategy 测试压缩策略
func TestCompactStrategy(t *testing.T) {
	strategy := CompactStrategy{
		PreserveRecentMessages: 5,
		UseAISummary:           true,
	}

	if strategy.PreserveRecentMessages != 5 {
		t.Errorf("Expected 5, got %d", strategy.PreserveRecentMessages)
	}

	// 更激进的压缩
	aggressive := strategy.MoreAggressive()
	if aggressive.PreserveRecentMessages != 3 {
		t.Errorf("Expected 3 for aggressive, got %d", aggressive.PreserveRecentMessages)
	}
}

// TestFormatRecoveryAction test recovery动作格式化
func TestFormatRecoveryAction(t *testing.T) {
	strategy := RecoveryStrategy{
		Type:        RecoveryTypeCompact,
		CanRecover:  true,
		Description: "Auto-compacting context to free space",
	}

	msg := strategy.FormatAction()
	if msg == "" {
		t.Error("Expected non-empty action message")
	}
}

// TestContextOverflowRecoveryWithCompaction 测试带压缩的溢出恢复
func TestContextOverflowRecoveryWithCompaction(t *testing.T) {
	recovery := NewContextOverflowRecovery(3)
	strategy := CompactStrategy{
		PreserveRecentMessages: 10,
		UseAISummary:           true,
	}

	attempts := 0
	for recovery.ShouldRetry() {
		attempts++
		strategy = strategy.MoreAggressive()
		recovery.Consume()

		if attempts > 5 {
			break
		}
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}

	// 验证每次压缩更激进
	finalPreserve := strategy.PreserveRecentMessages
	if finalPreserve >= 10 {
		t.Errorf("Expected more aggressive preservation (< 10), got %d", finalPreserve)
	}
}

// TestBuildOverflowErrorMessage 测试溢出错误消息构建
func TestBuildOverflowErrorMessage(t *testing.T) {
	msg := BuildOverflowErrorMessage(3, 2)
	if msg == "" {
		t.Error("Expected non-empty error message")
	}
	if !strings.Contains(msg, "3") {
		t.Errorf("Expected message to contain '3', got: %s", msg)
	}
}

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

// TestGetRecoveryStrategy_ContextOverflow test context溢出返回 RecoveryTypeCompact
// 验证 GetRecoveryStrategy 对上下文溢出错误返回正确的恢复策略类型
func TestGetRecoveryStrategy_ContextOverflow(t *testing.T) {
	orchestrator := NewErrorRecoveryOrchestrator()

	strategy := orchestrator.GetRecoveryStrategy("context overflow: prompt too long")
	if strategy.Type != RecoveryTypeCompact {
		t.Errorf("Type = %s, expected %s", strategy.Type, RecoveryTypeCompact)
	}
	if !strategy.CanRecover {
		t.Error("CanRecover 应为 true")
	}
}

// TestGetRecoveryStrategy_Overloaded 测试过载错误返回 RecoveryTypeRetryWithBackoff
// 验证 GetRecoveryStrategy 对服务过载错误返回退避重试策略
func TestGetRecoveryStrategy_Overloaded(t *testing.T) {
	orchestrator := NewErrorRecoveryOrchestrator()

	strategy := orchestrator.GetRecoveryStrategy("529 overloaded")
	if strategy.Type != RecoveryTypeRetryWithBackoff {
		t.Errorf("Type = %s, expected %s", strategy.Type, RecoveryTypeRetryWithBackoff)
	}
	if !strategy.CanRecover {
		t.Error("CanRecover 应为 true")
	}
}

// TestGetRecoveryStrategy_MaxTokens 测试 max_tokens 错误返回适当恢复类型
// 验证 GetRecoveryStrategy 对输出截断错误返回升级 token 策略
func TestGetRecoveryStrategy_MaxTokens(t *testing.T) {
	orchestrator := NewErrorRecoveryOrchestrator()

	strategy := orchestrator.GetRecoveryStrategy("max_tokens exceeded")
	if strategy.Type != RecoveryTypeEscalateTokens {
		t.Errorf("Type = %s, expected %s", strategy.Type, RecoveryTypeEscalateTokens)
	}
	if !strategy.CanRecover {
		t.Error("CanRecover 应为 true")
	}
}

// TestContextOverflowRecovery_MoreAggressive 测试溢出恢复逐步激进
// 验证第一次恢复保留 10 条消息，第二次 5 条，第三次 3 条
func TestContextOverflowRecovery_MoreAggressive(t *testing.T) {
	recovery := NewContextOverflowRecovery(3)

	// 初始策略保留 10 条消息
	if recovery.CompactStrategy.PreserveRecentMessages != 10 {
		t.Errorf("初始 PreserveRecentMessages = %d, expected 10", recovery.CompactStrategy.PreserveRecentMessages)
	}

	// 第一次消耗后
	recovery.Consume()
	if recovery.CompactStrategy.PreserveRecentMessages != 5 {
		t.Errorf("第一次消耗后 PreserveRecentMessages = %d, expected 5", recovery.CompactStrategy.PreserveRecentMessages)
	}

	// 第二次消耗后
	recovery.Consume()
	if recovery.CompactStrategy.PreserveRecentMessages != 3 {
		t.Errorf("第二次消耗后 PreserveRecentMessages = %d, expected 3", recovery.CompactStrategy.PreserveRecentMessages)
	}

	// 第三次消耗后（已耗尽）
	recovery.Consume()
	if recovery.ShouldRetry() {
		t.Error("三次消耗后不应再重试")
	}
}

// TestMaxOutputTokensRecovery_Escalate 测试 max_tokens 逐步升级
// 验证 Escalate 方法能逐步提升 max_tokens：16384 -> 32768 -> 65536
func TestMaxOutputTokensRecovery_Escalate(t *testing.T) {
	recovery := NewMaxOutputTokensRecovery(65536)

	// 初始值
	if recovery.CurrentMaxTokens != 16384 {
		t.Errorf("初始 CurrentMaxTokens = %d, expected 16384", recovery.CurrentMaxTokens)
	}

	// 第一次升级：16384 -> 32768
	newMax := recovery.Escalate()
	if newMax != 32768 {
		t.Errorf("第一次升级后 CurrentMaxTokens = %d, expected 32768", newMax)
	}
	if !recovery.HasEscalated {
		t.Error("升级后 HasEscalated 应为 true")
	}

	// 第二次升级：32768 -> 65536
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("第二次升级后 CurrentMaxTokens = %d, expected 65536", newMax)
	}

	// 第三次升级：已到最大值，不再提升
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("第三次升级后 CurrentMaxTokens = %d, expected仍为 65536", newMax)
	}
}

// TestMaxOutputTokensRecovery_EscalateWithLowerAbsoluteMax 测试 AbsoluteMax 小于 65536 时的逐步升级
// 验证当 AbsoluteMax 较小时，第二次升级不超过 AbsoluteMax
func TestMaxOutputTokensRecovery_EscalateWithLowerAbsoluteMax(t *testing.T) {
	recovery := NewMaxOutputTokensRecovery(50000)

	// 初始值
	if recovery.CurrentMaxTokens != 16384 {
		t.Errorf("初始 CurrentMaxTokens = %d, expected 16384", recovery.CurrentMaxTokens)
	}

	// 第一次升级：16384 -> 32768
	newMax := recovery.Escalate()
	if newMax != 32768 {
		t.Errorf("第一次升级后 CurrentMaxTokens = %d, expected 32768", newMax)
	}

	// 第二次升级：32768 -> 50000（受 AbsoluteMax 限制）
	newMax = recovery.Escalate()
	if newMax != 50000 {
		t.Errorf("第二次升级后 CurrentMaxTokens = %d, expected 50000", newMax)
	}
}

// TestMaxOutputTokensRecovery_MaxAttempts 测试截断恢复的最大升级次数限制
// 多次调用 Escalate 直到达到绝对最大值后，验证后续升级不再超过 AbsoluteMax
func TestMaxOutputTokensRecovery_MaxAttempts(t *testing.T) {
	recovery := NewMaxOutputTokensRecovery(65536)

	// 初始值应为 16384
	if recovery.CurrentMaxTokens != 16384 {
		t.Errorf("初始 CurrentMaxTokens = %d, expected 16384", recovery.CurrentMaxTokens)
	}

	// 第一次升级：16384 -> 32768
	newMax := recovery.Escalate()
	if newMax != 32768 {
		t.Errorf("第一次升级后 CurrentMaxTokens = %d, expected 32768", newMax)
	}

	// 第二次升级：32768 -> 65536
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("第二次升级后 CurrentMaxTokens = %d, expected 65536", newMax)
	}

	// 第三次升级：已达到绝对最大值，应保持 65536 不变
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("第三次升级后 CurrentMaxTokens = %d, expected仍为 65536", newMax)
	}

	// 第四次升级：继续调用仍应保持 65536
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("第四次升级后 CurrentMaxTokens = %d, expected仍为 65536", newMax)
	}

	// 第五次升级：继续调用仍应保持 65536
	newMax = recovery.Escalate()
	if newMax != 65536 {
		t.Errorf("第五次升级后 CurrentMaxTokens = %d, expected仍为 65536", newMax)
	}
}

// TestMaxOutputTokensRecovery_MaxAttempts_LowerAbsoluteMax 测试 AbsoluteMax 较小时的最大升级限制
// 当 AbsoluteMax 小于第二次升级目标值时，验证升级不超过 AbsoluteMax
func TestMaxOutputTokensRecovery_MaxAttempts_LowerAbsoluteMax(t *testing.T) {
	recovery := NewMaxOutputTokensRecovery(40000)

	// 第一次升级：16384 -> 32768
	newMax := recovery.Escalate()
	if newMax != 32768 {
		t.Errorf("第一次升级后 CurrentMaxTokens = %d, expected 32768", newMax)
	}

	// 第二次升级：32768 -> 40000（受 AbsoluteMax 限制）
	newMax = recovery.Escalate()
	if newMax != 40000 {
		t.Errorf("第二次升级后 CurrentMaxTokens = %d, expected 40000", newMax)
	}

	// 后续升级不应超过 AbsoluteMax
	newMax = recovery.Escalate()
	if newMax != 40000 {
		t.Errorf("后续升级后 CurrentMaxTokens = %d, expected仍为 40000", newMax)
	}
}
