package context

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// mockAIClient 用于测试的 AI client mock
type mockAIClient struct {
	ai.Client
	streamResult     <-chan ai.Event
	streamErr        error
	lastRequest      *ai.Request
	callCount        int
	model            string
	modelAfterStream string
}

type perCallUsageClient struct {
	model string
	calls atomic.Int64
}

func (c *perCallUsageClient) Stream(_ context.Context, _ ai.Request) (<-chan ai.Event, error) {
	input := int(c.calls.Add(1)) * 100
	return makeMockStreamWithUsage("summary", ai.UsageEvent{InputTokens: input, OutputTokens: 1}), nil
}

func (c *perCallUsageClient) Model() string     { return c.model }
func (c *perCallUsageClient) SetModel(m string) { c.model = m }
func (c *perCallUsageClient) Close() error      { return nil }

func (m *mockAIClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	m.callCount++
	m.lastRequest = &req
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	if m.modelAfterStream != "" {
		m.model = m.modelAfterStream
	}
	return m.streamResult, nil
}

func (m *mockAIClient) Model() string {
	if m.model != "" {
		return m.model
	}
	return "test-model"
}
func (m *mockAIClient) SetModel(string) {}
func (m *mockAIClient) Close() error    { return nil }

// makeMockStream 创建 mock 事件流
func makeMockStream(text string) <-chan ai.Event {
	ch := make(chan ai.Event, 3)
	ch <- ai.TextDeltaEvent{Content: text}
	ch <- ai.MessageStopEvent{}
	close(ch)
	return ch
}

// makeMockStreamWithUsage 创建包含 UsageEvent 的 mock 事件流（N11 审查 D5 测试专用）。
// 发送顺序：TextDeltaEvent → UsageEvent → MessageStopEvent。
// 用于端到端验证 generateAISummary 的 case ai.UsageEvent 分支是否订阅并桥接到 costRecorder。
func makeMockStreamWithUsage(text string, usage ai.UsageEvent) <-chan ai.Event {
	ch := make(chan ai.Event, 3)
	ch <- ai.TextDeltaEvent{Content: text}
	ch <- usage
	ch <- ai.MessageStopEvent{}
	close(ch)
	return ch
}

// makeMessages 创建测试消息列表
func makeMessages(count int) []session.Message {
	msgs := make([]session.Message, count)
	for i := 0; i < count; i++ {
		role := session.RoleUser
		if i%2 == 1 {
			role = session.RoleAssistant
		}
		msgs[i] = session.Message{
			Role: role,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: strings.Repeat("test message content ", 10)},
			},
		}
	}
	return msgs
}

// TestAICompactorBasicCompression 测试 AI 压缩基本功能
func TestAICompactorBasicCompression(t *testing.T) {
	mockClient := &mockAIClient{
		streamResult: makeMockStream("AI 生成的摘要内容"),
	}

	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	if result.RemovedCount != 15 {
		t.Errorf("Expected RemovedCount=15, got %d", result.RemovedCount)
	}
	if result.Summary == "" {
		t.Error("Expected non-empty summary")
	}
	if len(result.CompactedMessages) != 6 {
		t.Errorf("Expected 6 compacted messages (1 summary + 5 recent), got %d",
			len(result.CompactedMessages))
	}
	if mockClient.callCount != 1 {
		t.Errorf("Expected 1 AI call, got %d", mockClient.callCount)
	}
}

// TestAICompactorNotEnoughMessages 测试消息不足不压缩
func TestAICompactorNotEnoughMessages(t *testing.T) {
	mockClient := &mockAIClient{}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(3)
	config := CompactionConfig{
		PreserveRecentMessages: 10,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	if result.RemovedCount != 0 {
		t.Errorf("Expected RemovedCount=0, got %d", result.RemovedCount)
	}
	if mockClient.callCount != 0 {
		t.Errorf("Expected 0 AI calls, got %d", mockClient.callCount)
	}
}

// TestAICompactorCircuitBreaker test circuit breaking保护
func TestAICompactorCircuitBreaker(t *testing.T) {
	mockClient := &mockAIClient{
		streamErr: context.DeadlineExceeded,
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	// 触发 3 次失败打开熔断器
	for i := 0; i < 3; i++ {
		compactor.Compact(msgs, config)
	}

	// 熔断器打开后应不调用 AI
	result := compactor.Compact(msgs, config)
	if result.RemovedCount != 0 {
		t.Errorf("Expected no compression when circuit breaker open, got RemovedCount=%d",
			result.RemovedCount)
	}
}

// TestAICompactorFallbackOnAIError 测试 AI 失败降级到 fallback 路径。
//
// 候选 B 行为变更：AI 失败时不再直接降级到 L3 规则提取，
// 而是先尝试 L2 关键上下文保留（对标同类 CLI collect_user_messages）。
// makeMessages 产生含 user 消息的列表 → 走 L2 关键上下文（含 [Key Context Summary]）。
// L3 规则提取路径由 TestAICompactor_FallbackChain 的 "L3_规则提取兜底" 子测试覆盖。
func TestAICompactorFallbackOnAIError(t *testing.T) {
	mockClient := &mockAIClient{
		streamErr: context.DeadlineExceeded,
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	if result.Summary == "" {
		t.Error("Expected fallback summary to be non-empty")
	}
	// AI 失败 + 含 user 消息 → L2 关键上下文保留
	if !strings.Contains(result.Summary, "[Key Context Summary") {
		t.Errorf("Expected L2 fallback summary to contain [Key Context Summary], got: %s", result.Summary)
	}
	// 验证 fallback 级别为 L2
	if got := compactor.LastFallbackLevel(); got != FallbackLevelKeyContext {
		t.Errorf("Expected LastFallbackLevel=FallbackLevelKeyContext, got %s", got)
	}
	if len(result.CompactedMessages) != 6 {
		t.Errorf("Expected 6 compacted messages, got %d", len(result.CompactedMessages))
	}
}

// TestAICompactorSummaryQuality 测试 AI 摘要质量
func TestAICompactorSummaryQuality(t *testing.T) {
	summaryText := `<analysis>
User intent: implementationUser登录功能
关键决策: 使用 JWT 认证approach
涉及文件: auth.go, handler.go, middleware.go
技术细节: 使用 HMAC-SHA256 签名
当前状态: 登录 API 已完成，待添加 token 刷新
</analysis>
<summary>
1. User intent: implementationUser登录功能，包括 JWT 认证和 token 刷新
2. 关键决策: 选择 JWT 而非 session approach
3. 涉及文件: auth.go, handler.go, middleware.go
4. 技术细节: HMAC-SHA256 签名，15 分钟过期
5. 当前状态: 登录 API 完成，token 刷新待implementation
</summary>`

	mockClient := &mockAIClient{
		streamResult: makeMockStream(summaryText),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	msgs[0].Blocks[0].Text = "请帮我implementationUser登录功能"
	msgs[1].Blocks[0].Text = "好的，我选择使用 JWT 认证approach"

	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	if !strings.Contains(result.Summary, "User intent") {
		t.Errorf("Expected summary to contain 'User intent', got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "关键决策") {
		t.Errorf("Expected summary to contain '关键决策', got: %s", result.Summary)
	}
}

// TestBuildCompactPrompt 测试压缩提示词构建
func TestBuildCompactPrompt(t *testing.T) {
	msgs := makeMessages(5)
	msgs[0].Blocks[0].Text = "请帮我写一个 HTTP server"
	msgs[1].Blocks[0].Text = "好的，我会使用 Go 标准库implementation"

	prompt := BuildCompactPrompt(msgs)

	if !strings.Contains(prompt, "<analysis>") {
		t.Error("Expected prompt to contain <analysis> tag")
	}
	if !strings.Contains(prompt, "<summary>") {
		t.Error("Expected prompt to contain <summary> tag")
	}
	// N11 G7 修复：5 段式 → 9 段式，验证新的 9 段标题
	if !strings.Contains(prompt, "Primary Request and Intent") {
		t.Error("Expected prompt to contain 'Primary Request and Intent' (N11 G7 9-section)")
	}
	if !strings.Contains(prompt, "Files and Code Sections") {
		t.Error("Expected prompt to contain 'Files and Code Sections' (N11 G7 9-section)")
	}
	if !strings.Contains(prompt, "All User Messages") {
		t.Error("Expected prompt to contain 'All User Messages' (N11 G7 9-section, external-implementation inspired)")
	}
}

// TestAICompactorPreservesRecentMessages 测试保留最近消息
func TestAICompactorPreservesRecentMessages(t *testing.T) {
	mockClient := &mockAIClient{
		streamResult: makeMockStream("摘要内容"),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	// 标记最后 5 条消息
	for i := 15; i < 20; i++ {
		msgs[i].Blocks[0].Text = "RECENT_MESSAGE_" + strings.Repeat("x", 50)
	}

	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	for i := 1; i < len(result.CompactedMessages); i++ {
		text := result.CompactedMessages[i].Blocks[0].Text
		if !strings.Contains(text, "RECENT_MESSAGE_") {
			t.Errorf("Expected recent message at position %d to be preserved, got: %s", i, text)
		}
	}
}

// TestAICompactorMultipleCompactions 测试多次压缩
func TestAICompactorMultipleCompactions(t *testing.T) {
	mockClient := &mockAIClient{
		streamResult: makeMockStream("第一次压缩摘要"),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	// 第一次压缩
	result1 := compactor.Compact(msgs, config)
	if result1.RemovedCount != 15 {
		t.Errorf("First compaction: Expected RemovedCount=15, got %d", result1.RemovedCount)
	}

	// 第二次压缩（6 条消息，1 条摘要 + 5 条最近，保留 5 条，压缩 1 条）
	result2 := compactor.Compact(result1.CompactedMessages, config)
	if result2.RemovedCount != 1 {
		t.Errorf("Second compaction: Expected RemovedCount=1, got %d", result2.RemovedCount)
	}
}

// TestAICompactor_Success 测试 AI 生成结构化摘要，RemovedCount > 0
// 验证 AI 压缩器在 AI 调用成功时生成摘要并正确移除旧消息
func TestAICompactor_Success(t *testing.T) {
	structuredSummary := `<analysis>
User正在implementationUser认证功能
</analysis>
<summary>
1. User intent: implementationUser登录功能
2. 关键决策: 选择 JWT 认证approach
3. 涉及文件: auth.go, handler.go
4. 技术细节: 使用 HMAC-SHA256 签名
5. 当前状态: 登录 API 已完成
</summary>`

	mockClient := &mockAIClient{
		streamResult: makeMockStream(structuredSummary),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	// 验证移除了旧消息
	if result.RemovedCount <= 0 {
		t.Errorf("RemovedCount should be greater than 0，actual %d", result.RemovedCount)
	}

	// 验证摘要包含结构化内容
	if !strings.Contains(result.Summary, "User intent") {
		t.Errorf("摘要should contain 'User intent'，actual: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "关键决策") {
		t.Errorf("摘要should contain '关键决策'，actual: %s", result.Summary)
	}

	// 验证 AI 被调用
	if mockClient.callCount != 1 {
		t.Errorf("AI 应被调用 1 次，actual %d 次", mockClient.callCount)
	}
}

// TestAICompactor_Fallback 测试 AI 失败时降级到 fallback 路径。
//
// 候选 B 行为变更：AI 失败时优先走 L2 关键上下文保留（含 user 消息时），
// 不再直接降级到 L3 规则提取。makeMessages 产生含 user 消息的列表 → 走 L2。
// L3 规则提取路径由 TestAICompactor_FallbackChain 的 "L3_规则提取兜底" 子测试覆盖。
func TestAICompactor_Fallback(t *testing.T) {
	mockClient := &mockAIClient{
		streamErr: context.DeadlineExceeded,
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)

	// 验证仍有摘要（：降级为 L2 关键上下文）
	if result.Summary == "" {
		t.Error("AI 失败后应降级为 L2 关键上下文摘要，摘要should not be empty")
	}

	// AI 失败 + 含 user 消息 → L2 关键上下文保留
	if !strings.Contains(result.Summary, "[Key Context Summary") {
		t.Errorf("降级摘要should contain '[Key Context Summary]'，actual: %s", result.Summary)
	}

	// 验证 fallback 级别为 L2
	if got := compactor.LastFallbackLevel(); got != FallbackLevelKeyContext {
		t.Errorf("Expected LastFallbackLevel=FallbackLevelKeyContext, got %s", got)
	}

	// 验证仍有消息被移除
	if result.RemovedCount != 15 {
		t.Errorf("RemovedCount = %d, expected 15", result.RemovedCount)
	}
}

// TestAICompactor_CircuitBreaker 测试 3 次 AI 失败触发熔断器
// 验证连续 3 次 AI 调用失败后，熔断器打开，后续压缩不再调用 AI
func TestAICompactor_CircuitBreaker(t *testing.T) {
	mockClient := &mockAIClient{
		streamErr: context.DeadlineExceeded,
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	// 触发 3 次 AI 失败
	for i := 0; i < 3; i++ {
		result := compactor.Compact(msgs, config)
		// 前 3 次应降级为启发式摘要
		if result.Summary == "" {
			t.Errorf("第 %d 次压缩应降级为启发式摘要", i+1)
		}
	}

	// 第 4 次压缩，熔断器应已打开
	result := compactor.Compact(msgs, config)
	if result.RemovedCount != 0 {
		t.Errorf("熔断器打开后不应压缩，RemovedCount = %d, expected 0", result.RemovedCount)
	}
}

// === N11 修复测试 ===

// TestBuildCompactPrompt_NineSections 验证 9 段式 prompt 格式（N11 修复 G7，P1-7）。
// 借鉴同类 CLI 的 8 段 + 同类 CLI "All user messages" 全保留启发。
func TestBuildCompactPrompt_NineSections(t *testing.T) {
	msgs := makeMessages(5)
	prompt := BuildCompactPrompt(msgs)

	requiredSections := []string{
		"1. Primary Request and Intent",
		"2. Key Technical Concepts",
		"3. Files and Code Sections",
		"4. Errors and Fixes",
		"5. Problem Solving",
		"6. All User Messages",
		"7. Pending Tasks",
		"8. Current Work",
		"9. Next Steps",
	}
	for _, section := range requiredSections {
		if !strings.Contains(prompt, section) {
			t.Errorf("9 段式 prompt 应包含 %q，actual prompt 缺失该段", section)
		}
	}

	// 验证 <analysis> 和 <summary> 标签
	if !strings.Contains(prompt, "<analysis>") {
		t.Error("prompt 应包含 <analysis> 标签")
	}
	if !strings.Contains(prompt, "<summary>") {
		t.Error("prompt 应包含 <summary> 标签")
	}
}

// TestBuildCompactPromptPreventsHistoryAsPending 压缩摘要防"历史被当成待办"。
//
// 背景（同类工具 #68097 实证）：压缩后的摘要若不区分"已完成"与"未完成"，
// 下一轮会把历史指令当 pending action 重新执行（如历史里的定时任务被再次调度）。
// 契约：prompt 必须显式要求区分 completed / pending，且禁止把已执行的历史指令
// 写成待办或建议执行项。
func TestBuildCompactPromptPreventsHistoryAsPending(t *testing.T) {
	msgs := makeMessages(3)
	prompt := BuildCompactPrompt(msgs)

	for _, want := range []string{
		"completed", // 必须显式区分已完成事项
		"do NOT",    // 必须有显式禁止指令
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("防误读 prompt 应包含 %q（区分已完成/未完成），actual:\n%s", want, prompt)
		}
	}
	// 第 6 段（用户消息）须标注历史消息不是活跃指令
	if !strings.Contains(prompt, "historical") && !strings.Contains(prompt, "past") {
		t.Errorf("All User Messages 段应标注用户消息为历史记录（historical/past），非活跃指令:\n%s", prompt)
	}
}

// TestClassifyCompactError 验证 classifyCompactError 的 ErrorID 分类（N11 修复 G2）。
// 契约：02 §2.3.2 要求 compact 失败走 ErrAIRetryable/ErrAIFatal，不新增 ErrorID。
func TestClassifyCompactError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantID     ai.ErrorID
	}{
		{"401 认证失败", 401, ai.ErrAIFatal},
		{"403 禁止访问", 403, ai.ErrAIFatal},
		{"413 prompt 过长", 413, ai.ErrAIFatal},
		{"500 服务端错误", 500, ai.ErrAIRetryable},
		{"502 网关错误", 502, ai.ErrAIRetryable},
		{"504 网关超时", 504, ai.ErrAIRetryable},
		{"429 限流", 429, ai.ErrAIRetryable},
		{"529 过载", 529, ai.ErrAIRetryable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &ai.APIError{StatusCode: tt.statusCode}
			got := classifyCompactError(err)
			if got != tt.wantID {
				t.Errorf("classifyCompactError(statusCode=%d) = %q, want %q",
					tt.statusCode, got, tt.wantID)
			}
		})
	}

	// nil error 应返回空字符串
	if got := classifyCompactError(nil); got != "" {
		t.Errorf("classifyCompactError(nil) = %q, want %q", got, "")
	}

	// 非 APIError（如普通 error）应归为 ErrAIRetryable
	if got := classifyCompactError(context.DeadlineExceeded); got != ai.ErrAIRetryable {
		t.Errorf("classifyCompactError(DeadlineExceeded) = %q, want %q",
			got, ai.ErrAIRetryable)
	}
}

// TestInjectCompactAgentID 验证 ctx 注入 AgentID="compact"（N11 修复 G11）。
// 契约：02 §2.1.2 要求通过 ctx 注入 AgentID 给 StreamWithRetry 路径识别。
// D4 修复（N1 合入后）：迁移到 tools.WithAgentID/ExtractAgentID 公开接口。
func TestInjectCompactAgentID(t *testing.T) {
	// nil ctx → 返回空
	if got := tools.ExtractAgentID(nil); got != "" {
		t.Errorf("tools.ExtractAgentID(nil) = %q, want %q", got, "")
	}

	// 注入后提取
	ctx := tools.WithAgentID(context.Background(), compactAgentID)
	got := tools.ExtractAgentID(ctx)
	if got != compactAgentID {
		t.Errorf("tools.ExtractAgentID(WithAgentID(ctx)) = %q, want %q",
			got, compactAgentID)
	}

	// 未注入的 ctx → 返回空
	if got := tools.ExtractAgentID(context.Background()); got != "" {
		t.Errorf("tools.ExtractAgentID(plain ctx) = %q, want %q", got, "")
	}
}

// TestGenerateAISummary_ErrorIDWrapping 验证 generateAISummary 失败时返回的错误
// 携带 N6 ErrorID（N11 修复 G2）。
func TestGenerateAISummary_ErrorIDWrapping(t *testing.T) {
	// nil aiClient → ErrAIFatal
	compactor := &AICompactor{
		aiClient:       nil,
		estimator:      ai.NewTokenEstimator(),
		circuitBreaker: NewCircuitBreaker(3),
		fallback:       NewCompactor(ai.NewTokenEstimator()),
		retryConfig:    ai.DefaultRetryConfig(),
		maxPTLRetries:  MaxPTLRetries,
	}
	msgs := makeMessages(5)
	_, err := compactor.generateAISummary(msgs)
	if err == nil {
		t.Fatal("generateAISummary with nil aiClient should return error")
	}
	id := ai.GetErrorID(err)
	if id != ai.ErrAIFatal {
		t.Errorf("GetErrorID(nil client err) = %q, want %q", id, ai.ErrAIFatal)
	}
}

// TestCompact_G14BreakGuard 验证 maxPreserve 收敛到 1 时的 break guard（N11 修复 G14）。
// 修复前：maxPreserve=1 且消息数 <=2 时死循环；修复后：break 避免死循环。
func TestCompact_G14BreakGuard(t *testing.T) {
	// 构造场景：MaxEstimatedTokens 极小，强制 maxPreserve 收敛到 1
	mockClient := &mockAIClient{
		streamResult: makeMockStream("short summary"),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	// 3 条消息，preserve=2，MaxEstimatedTokens=1（极小，强制循环）
	msgs := makeMessages(3)
	config := CompactionConfig{
		PreserveRecentMessages: 2,
		MaxEstimatedTokens:     1, // 极小，强制 maxPreserve 收敛
	}

	// 不应死循环或 panic
	result := compactor.Compact(msgs, config)
	if result == nil {
		t.Fatal("Compact should return non-nil result")
	}
	// 验证至少有 summary + preserved
	if len(result.CompactedMessages) < 1 {
		t.Errorf("Compact should return at least 1 message, got %d",
			len(result.CompactedMessages))
	}
}

// === N11 审查 B1 + D5 回归测试 ===

// TestGenerateAISummary_SubscribesUsageEvent 端到端验证 generateAISummary 订阅
// ai.UsageEvent 并桥接到 costRecorder → N2 metrics + N1 UsageTracker（N11 修复 G1，
// 审查 D5 补测）。
//
// 此测试在 AICompactor 直连路径上验证 operation-scoped recorder 接线正确，
// 配合 TestCompactionPipelineUsesOperationScopedCostRecorder 覆盖 pipeline
// 路径，两者共同构成 usage 桥接的端到端回归防护网。
func TestGenerateAISummary_SubscribesUsageEvent(t *testing.T) {
	usage := ai.UsageEvent{
		InputTokens:   1234,
		OutputTokens:  567,
		CacheCreation: 200,
		CacheRead:     100,
	}
	mockClient := &mockAIClient{
		streamResult: makeMockStreamWithUsage("AI 摘要内容", usage),
		model:        "claude-sonnet-4-20250514",
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	// 构造本次调用私有的 costRecorder（metrics + usageTracker 双写）
	m := &metrics.Metrics{}
	ut := ai.NewUsageTracker("") // storePath="" 避免磁盘 IO
	cr := NewCompactCostRecorder(m, ut, "claude-sonnet-4-20250514")

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}
	result := compactor.CompactWithCostRecorder(msgs, config, cr)

	if result.RemovedCount != 15 {
		t.Fatalf("Expected RemovedCount=15, got %d", result.RemovedCount)
	}

	// 验证 UsageEvent 已被订阅并桥接到 N2 metrics。
	// 若 case ai.UsageEvent 分支未执行，以下断言全部失败（B1 场景）。
	if got := m.InputTokens.Load(); got != int64(usage.InputTokens) {
		t.Errorf("metrics.InputTokens = %d, want %d (UsageEvent 未被订阅或未桥接到 costRecorder)",
			got, usage.InputTokens)
	}
	if got := m.OutputTokens.Load(); got != int64(usage.OutputTokens) {
		t.Errorf("metrics.OutputTokens = %d, want %d", got, usage.OutputTokens)
	}
	if got := m.CacheCreationTokens.Load(); got != int64(usage.CacheCreation) {
		t.Errorf("metrics.CacheCreationTokens = %d, want %d", got, usage.CacheCreation)
	}
	if got := m.CacheReadTokens.Load(); got != int64(usage.CacheRead) {
		t.Errorf("metrics.CacheReadTokens = %d, want %d", got, usage.CacheRead)
	}

	// 验证 N1 UsageTracker by-agent 分桶（双写）
	agent, ok := ut.AgentUsage[compactAgentID]
	if !ok {
		t.Fatalf("AgentUsage should contain %q after UsageEvent, got keys: %v",
			compactAgentID, ut.AgentUsage)
	}
	if agent.InputTokens != usage.InputTokens {
		t.Errorf("AgentUsage.InputTokens = %d, want %d", agent.InputTokens, usage.InputTokens)
	}
	if agent.OutputTokens != usage.OutputTokens {
		t.Errorf("AgentUsage.OutputTokens = %d, want %d", agent.OutputTokens, usage.OutputTokens)
	}
}

// TestGenerateAISummary_NoCostRecorder_NoPanic 验证 costRecorder=nil 时
// case ai.UsageEvent 分支安全跳过（降级路径，不阻塞 compact，不 panic）。
// 这是 B1 修复后仍需保证的向后兼容行为。
func TestGenerateAISummary_NoCostRecorder_NoPanic(t *testing.T) {
	usage := ai.UsageEvent{
		InputTokens:  100,
		OutputTokens: 50,
	}
	mockClient := &mockAIClient{
		streamResult: makeMockStreamWithUsage("摘要内容", usage),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)
	// 不提供 costRecorder

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	// 不应 panic
	result := compactor.Compact(msgs, config)
	if result.RemovedCount != 15 {
		t.Errorf("Expected RemovedCount=15, got %d", result.RemovedCount)
	}
	if result.Summary == "" {
		t.Error("Expected non-empty summary even without costRecorder")
	}
}

func TestGenerateAISummaryAttributesUsageToStreamRequestModel(t *testing.T) {
	mockClient := &mockAIClient{
		streamResult:     makeMockStreamWithUsage("summary", ai.UsageEvent{InputTokens: 120, OutputTokens: 8}),
		model:            "initial-model",
		modelAfterStream: "mutated-model",
	}
	tracker := ai.NewUsageTracker("")
	recorder := NewCompactCostRecorder(nil, tracker, "initial-model")
	compactor := NewAICompactor(mockClient, ai.NewTokenEstimator())
	if _, err := compactor.generateAISummaryWithCostRecorder(makeMessages(2), recorder); err != nil {
		t.Fatal(err)
	}
	breakdown := tracker.GetAgentBreakdown()
	compactUsage := breakdown[compactAgentID]
	if compactUsage == nil || compactUsage.ModelUsage["initial-model"] == nil {
		t.Fatalf("usage was not attributed to the model sent to Stream: %#v", compactUsage)
	}
	if compactUsage.ModelUsage["mutated-model"] != nil {
		t.Fatalf("usage was attributed to mutable post-stream model: %#v", compactUsage.ModelUsage)
	}
}

func TestConcurrentCompactionsKeepOperationScopedRecorders(t *testing.T) {
	client := &perCallUsageClient{model: "test-model"}
	compactor := NewAICompactor(client, ai.NewTokenEstimator())
	recorderA := NewCompactCostRecorder(nil, nil, "test-model")
	recorderB := NewCompactCostRecorder(nil, nil, "test-model")
	cfg := DefaultCompactionConfig()
	cfg.Trigger = CompactionTriggerManual

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		compactor.CompactWithCostRecorder(makeMessages(12), cfg, recorderA)
	}()
	go func() {
		defer wg.Done()
		compactor.CompactWithCostRecorder(makeMessages(12), cfg, recorderB)
	}()
	wg.Wait()

	usageA, okA := recorderA.TotalUsage()
	usageB, okB := recorderB.TotalUsage()
	if !okA || !okB || usageA.InputTokens == 0 || usageB.InputTokens == 0 {
		t.Fatalf("operation usage crossed or disappeared: A=%+v/%v B=%+v/%v", usageA, okA, usageB, okB)
	}
	if usageA.InputTokens+usageB.InputTokens != 300 {
		t.Fatalf("unexpected per-operation usage total: A=%d B=%d", usageA.InputTokens, usageB.InputTokens)
	}
}

func TestCompactionPipelineUsesOperationScopedCostRecorder(t *testing.T) {
	mockClient := &mockAIClient{
		streamResult: makeMockStreamWithUsage("summary", ai.UsageEvent{InputTokens: 42, OutputTokens: 3}),
	}
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipeline(estimator, mockClient)
	cr := NewCompactCostRecorder(nil, nil, "test-model")
	cfg := DefaultCompactionConfig()
	cfg.UseAISummary = true
	_ = pipeline.ExecuteWithOptions(makeMessages(20), DefaultBudget(200000), nil, ExecuteOptions{
		Trigger: CompactionTriggerManual, Policy: CompactionPolicyForce,
		Config: cfg, CostRecorder: cr,
	})
	usage, ok := cr.TotalUsage()
	if !ok || usage.InputTokens != 42 || usage.OutputTokens != 3 {
		t.Fatalf("pipeline did not use invocation recorder: %+v/%v", usage, ok)
	}
}

func TestCompactionPipelineOperationRecorderNoAIPipeline(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)
	cr := NewCompactCostRecorder(nil, nil, "test-model")
	_ = pipeline.ExecuteWithOptions(makeMessages(20), DefaultBudget(200000), nil, ExecuteOptions{
		Trigger: CompactionTriggerManual, Policy: CompactionPolicyForce,
		Config: DefaultCompactionConfig(), CostRecorder: cr,
	})
}

// === N11 D2 递归守卫测试 ===

// TestMaybeTriggerAutoCompact_RecursiveGuard 验证 compact ctx 下递归守卫生效：
// 当 ctx 携带 AgentID="compact" 时，maybeTriggerAutoCompact 返回 false（跳过），
// 避免 compact→AI→413→compact 递归循环（N11 §3.14 / D2 修复）。
func TestMaybeTriggerAutoCompact_RecursiveGuard(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	msgs := makeMessages(20)
	// 使用 DefaultBudget 确保百分比字段正确初始化（WarnPercent/BlockPercent 非 0）
	budget := DefaultBudget(1000) // 极小 ContextWindow，强制 StateShouldCompact

	// 正例：普通 ctx（无 AgentID）+ token 超阈值 → 应触发
	plainCtx := context.Background()
	if !pipeline.maybeTriggerAutoCompact(plainCtx, msgs, budget) {
		t.Error("maybeTriggerAutoCompact(plain ctx, over-threshold) should return true")
	}

	// 反例：compact ctx（AgentID="compact"）→ 跳过（递归守卫）
	compactCtx := tools.WithAgentID(context.Background(), compactAgentID)
	if pipeline.maybeTriggerAutoCompact(compactCtx, msgs, budget) {
		t.Error("maybeTriggerAutoCompact(compact ctx) should return false (recursive guard)")
	}
}

// TestMaybeTriggerAutoCompact_BelowThreshold 验证 token 未超阈值时不触发。
func TestMaybeTriggerAutoCompact_BelowThreshold(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	msgs := makeMessages(2)          // 少量消息，token 低
	budget := DefaultBudget(1000000) // 极大 ContextWindow，不会超阈值

	if pipeline.maybeTriggerAutoCompact(context.Background(), msgs, budget) {
		t.Error("maybeTriggerAutoCompact(below-threshold) should return false")
	}
}

// === N11 D3 G5 恒降级修复测试 ===

// TestCacheAwareMicroCompact_UsesLastCacheTime 验证 pipeline.cacheAwareMicroCompact
// 使用 cacheInfo.LastCacheTime（而非 time.Time{}），使 isCacheActive 返回 true 时
// 仅 compact cache boundary 之后的消息（N11 修复 D3 / G5 恒降级）。
func TestCacheAwareMicroCompact_UsesLastCacheTime(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// 构造带 cache_control metadata 标记的消息（使 findCacheBoundary 能定位 boundary）
	messages := []session.Message{
		{Role: session.RoleSystem, Blocks: []session.ContentBlock{
			{Type: "text", Text: "cached prefix"},
		}, Metadata: map[string]interface{}{"cache_control": true}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
			{Type: "text", Text: "response"},
		}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "text", Text: strings.Repeat("recent tool output ", 50)},
		}},
	}

	// 场景 1：LastCacheTime 在 TTL 内 → cache active → 保护 cache prefix
	cacheInfoActive := &ai.CacheInfo{
		CacheCreationTokens: 100,
		LastCacheTime:       time.Now().Add(-1 * time.Minute), // 1 分钟前，在 TTL 内
	}
	resultActive := pipeline.cacheAwareMicroCompact(messages, cacheInfoActive)
	// cache active 时，cache prefix（第 0 条）应被保护，不被 compact
	if len(resultActive) == 0 {
		t.Error("cacheActive: result should not be empty")
	}

	// 场景 2：LastCacheTime 为零值 → isCacheActive false → 降级为普通 MicroCompact
	cacheInfoZero := &ai.CacheInfo{
		CacheCreationTokens: 100,
		LastCacheTime:       time.Time{}, // 零值 → 降级
	}
	resultZero := pipeline.cacheAwareMicroCompact(messages, cacheInfoZero)
	if len(resultZero) == 0 {
		t.Error("cacheZero: result should not be empty")
	}

	// 场景 3：cacheInfo=nil → 降级为普通 MicroCompact
	resultNil := pipeline.cacheAwareMicroCompact(messages, nil)
	if len(resultNil) == 0 {
		t.Error("cacheNil: result should not be empty")
	}
}

// === 候选 B 测试：3 级 fallback 链 + 关键上下文提取 ===

// TestAICompactor_FallbackChain 验证 3 级 fallback 链（候选 B）。
// 测试场景：
// 1. L1 AI 成功 → lastFallbackLevel == FallbackLevelAISummary
// 2. L1 AI 失败 → L2 关键上下文 → lastFallbackLevel == FallbackLevelKeyContext
// 3. L1 失败 + L2 提取为空 → L3 规则提取 → lastFallbackLevel == FallbackLevelRuleExtract
// 4. Metadata["fallback_level"] 正确标记
func TestAICompactor_FallbackChain(t *testing.T) {
	t.Run("L1_AI_成功", func(t *testing.T) {
		mockClient := &mockAIClient{
			streamResult: makeMockStream("AI 生成的摘要"),
		}
		estimator := ai.NewTokenEstimator()
		compactor := NewAICompactor(mockClient, estimator)

		msgs := makeMessages(20)
		config := CompactionConfig{
			PreserveRecentMessages: 5,
			MaxEstimatedTokens:     10000,
		}

		result := compactor.Compact(msgs, config)

		// L1 成功：fallback_level == ai_summary
		if got := compactor.LastFallbackLevel(); got != FallbackLevelAISummary {
			t.Errorf("L1 成功后 LastFallbackLevel = %s, want %s", got, FallbackLevelAISummary)
		}
		// Metadata 标记
		if len(result.CompactedMessages) == 0 {
			t.Fatal("CompactedMessages should not be empty")
		}
		meta := result.CompactedMessages[0].Metadata
		if meta == nil {
			t.Fatal("summary msg Metadata should not be nil")
		}
		if got, _ := meta["fallback_level"].(string); got != "ai_summary" {
			t.Errorf("Metadata[fallback_level] = %q, want %q", got, "ai_summary")
		}
	})

	t.Run("L2_关键上下文降级", func(t *testing.T) {
		mockClient := &mockAIClient{
			streamErr: context.DeadlineExceeded, // L1 失败
		}
		estimator := ai.NewTokenEstimator()
		compactor := NewAICompactor(mockClient, estimator)

		// 构造含关键上下文的消息（user 消息、文件路径、决策关键词）
		msgs := makeMessages(20)
		msgs[0].Blocks[0].Text = "请帮我实现用户登录功能，涉及 auth.go 和 handler.go"
		msgs[1].Blocks[0].Text = "好的，我选择使用 JWT 认证方案，decided to use JWT"

		config := CompactionConfig{
			PreserveRecentMessages: 5,
			MaxEstimatedTokens:     10000,
		}

		result := compactor.Compact(msgs, config)

		// L1 失败 + L2 提取到关键上下文 → fallback_level == key_context
		if got := compactor.LastFallbackLevel(); got != FallbackLevelKeyContext {
			t.Errorf("L2 降级后 LastFallbackLevel = %s, want %s", got, FallbackLevelKeyContext)
		}
		// 摘要应包含关键上下文标记
		if !strings.Contains(result.Summary, "[Key Context Summary") {
			t.Errorf("L2 摘要应包含 [Key Context Summary]，actual: %s", result.Summary)
		}
		// Metadata 标记
		meta := result.CompactedMessages[0].Metadata
		if got, _ := meta["fallback_level"].(string); got != "key_context" {
			t.Errorf("Metadata[fallback_level] = %q, want %q", got, "key_context")
		}
	})

	t.Run("L3_规则提取兜底", func(t *testing.T) {
		mockClient := &mockAIClient{
			streamErr: context.DeadlineExceeded, // L1 失败
		}
		estimator := ai.NewTokenEstimator()
		compactor := NewAICompactor(mockClient, estimator)

		// 构造不含任何关键上下文的消息（无 user 消息/无决策/无路径/无任务关键词）
		// → ExtractKeyContext 返回空字符串 → 降级到 L3
		msgs := make([]session.Message, 20)
		for i := 0; i < 20; i++ {
			msgs[i] = session.Message{
				Role: session.RoleAssistant,
				Blocks: []session.ContentBlock{
					{Type: "text", Text: strings.Repeat("no key content ", 5)},
				},
			}
		}

		config := CompactionConfig{
			PreserveRecentMessages: 5,
			MaxEstimatedTokens:     10000,
		}

		result := compactor.Compact(msgs, config)

		// L1 失败 + L2 提取为空 → L3 规则提取
		if got := compactor.LastFallbackLevel(); got != FallbackLevelRuleExtract {
			t.Errorf("L3 兜底后 LastFallbackLevel = %s, want %s", got, FallbackLevelRuleExtract)
		}
		// L3 摘要应包含规则提取标记
		if !strings.Contains(result.Summary, "[Compaction Summary]") {
			t.Errorf("L3 摘要应包含 [Compaction Summary]，actual: %s", result.Summary)
		}
		// Metadata 标记
		meta := result.CompactedMessages[0].Metadata
		if got, _ := meta["fallback_level"].(string); got != "rule_extract" {
			t.Errorf("Metadata[fallback_level] = %q, want %q", got, "rule_extract")
		}
	})

	t.Run("消息不足时不触发Compact", func(t *testing.T) {
		mockClient := &mockAIClient{}
		estimator := ai.NewTokenEstimator()
		compactor := NewAICompactor(mockClient, estimator)

		msgs := makeMessages(3) // 不足 PreserveRecentMessages
		config := CompactionConfig{
			PreserveRecentMessages: 10,
			MaxEstimatedTokens:     10000,
		}

		compactor.Compact(msgs, config)

		// 未触发 Compact → fallback_level == none
		if got := compactor.LastFallbackLevel(); got != FallbackLevelNone {
			t.Errorf("消息不足时 LastFallbackLevel = %s, want %s", got, FallbackLevelNone)
		}
	})
}

// TestExtractKeyContext 验证关键上下文提取（候选 B）。
// 测试场景：
// 1. 5 类关键信息全部提取
// 2. 部分类别禁用（PreserveUserMessages=false）
// 3. MaxPreservedMessages 限制
// 4. 无关键信息返回空字符串（触发 L3 降级）
// 5. 零值配置回填默认（向后兼容）
func TestExtractKeyContext(t *testing.T) {
	t.Run("全开提取5类关键信息", func(t *testing.T) {
		msgs := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{
				{Type: "text", Text: "请帮我实现登录功能"},
			}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "我 decided 使用 JWT approach 作为认证 solution"},
			}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "修复了 token 验证 error，fix the bug"},
			}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "涉及文件 src/auth.go 和 src/handler.go"},
			}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "next step：添加 token 刷新，todo: 测试用例"},
			}},
		}

		cfg := DefaultKeyContextConfig()
		result := ExtractKeyContext(msgs, cfg)

		// 应包含 5 类关键信息
		if !strings.Contains(result, "[Key Context Summary") {
			t.Errorf("应包含 [Key Context Summary] 标记")
		}
		if !strings.Contains(result, "User Messages:") {
			t.Errorf("应包含 User Messages 段")
		}
		if !strings.Contains(result, "Key Decisions:") {
			t.Errorf("应包含 Key Decisions 段")
		}
		if !strings.Contains(result, "Error Fixes:") {
			t.Errorf("应包含 Error Fixes 段")
		}
		if !strings.Contains(result, "File Paths:") {
			t.Errorf("应包含 File Paths 段")
		}
		if !strings.Contains(result, "Pending Tasks:") {
			t.Errorf("应包含 Pending Tasks 段")
		}
	})

	t.Run("部分类别禁用", func(t *testing.T) {
		msgs := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{
				{Type: "text", Text: "用户消息"},
			}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "decided to use JWT"},
			}},
		}

		// 禁用 PreserveUserMessages
		cfg := KeyContextConfig{
			PreserveUserMessages: false,
			PreserveDecisions:    true,
			MaxPreservedMessages: 20,
		}
		result := ExtractKeyContext(msgs, cfg)

		// 不应包含 User Messages（被禁用）
		if strings.Contains(result, "User Messages:") {
			t.Errorf("PreserveUserMessages=false 时不应提取 user 消息")
		}
		// 应包含 Key Decisions（启用）
		if !strings.Contains(result, "Key Decisions:") {
			t.Errorf("PreserveDecisions=true 时应提取决策")
		}
	})

	t.Run("MaxPreservedMessages限制", func(t *testing.T) {
		// 构造 5 条 user 消息，限制 MaxPreservedMessages=2
		msgs := make([]session.Message, 5)
		for i := 0; i < 5; i++ {
			msgs[i] = session.Message{
				Role: session.RoleUser,
				Blocks: []session.ContentBlock{
					{Type: "text", Text: "user msg " + string(rune('A'+i))},
				},
			}
		}

		cfg := KeyContextConfig{
			PreserveUserMessages: true,
			MaxPreservedMessages: 2,
		}
		result := ExtractKeyContext(msgs, cfg)

		// 应只保留前 2 条 user 消息
		if strings.Contains(result, "user msg C") || strings.Contains(result, "user msg D") {
			t.Errorf("MaxPreservedMessages=2 时不应保留超过 2 条 user 消息，actual: %s", result)
		}
		// 应包含前 2 条
		if !strings.Contains(result, "user msg A") {
			t.Errorf("应保留第 1 条 user 消息")
		}
	})

	t.Run("无关键信息返回空字符串触发L3降级", func(t *testing.T) {
		// 构造不含任何关键信息的消息（无 user 消息/无决策/无路径/无任务）
		msgs := []session.Message{
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "plain assistant content"},
			}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
				{Type: "text", Text: "more plain content"},
			}},
		}

		cfg := DefaultKeyContextConfig()
		result := ExtractKeyContext(msgs, cfg)

		// 应返回空字符串（触发 L3 降级）
		if result != "" {
			t.Errorf("无关键信息时应返回空字符串，actual: %s", result)
		}
	})

	t.Run("零值配置回填默认", func(t *testing.T) {
		msgs := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{
				{Type: "text", Text: "user message content"},
			}},
		}

		// 零值配置（MaxPreservedMessages=0）
		zeroCfg := KeyContextConfig{}
		result := ExtractKeyContext(msgs, zeroCfg)

		// 零值回填默认后应能提取（默认全开）
		if result == "" {
			t.Errorf("零值配置回填默认后应提取关键信息，actual: 空字符串")
		}
		if !strings.Contains(result, "User Messages:") {
			t.Errorf("零值回填默认后应包含 User Messages 段")
		}
	})
}

// TestAICompactor_SetKeyContextConfig 验证 SetKeyContextConfig 注入配置（候选 B）。
func TestAICompactor_SetKeyContextConfig(t *testing.T) {
	mockClient := &mockAIClient{}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	// 初始应为零值（MaxPreservedMessages=0）
	compactor.mu.Lock()
	initialCfg := compactor.keyContextCfg
	compactor.mu.Unlock()
	if initialCfg.MaxPreservedMessages != 0 {
		t.Errorf("initial MaxPreservedMessages should be 0, got %d", initialCfg.MaxPreservedMessages)
	}

	// 注入配置
	cfg := KeyContextConfig{
		PreserveUserMessages: false,
		PreserveDecisions:    true,
		MaxPreservedMessages: 50,
	}
	compactor.SetKeyContextConfig(cfg)

	compactor.mu.Lock()
	got := compactor.keyContextCfg
	compactor.mu.Unlock()
	if got.MaxPreservedMessages != 50 {
		t.Errorf("after SetKeyContextConfig, MaxPreservedMessages = %d, want 50", got.MaxPreservedMessages)
	}
	if got.PreserveUserMessages != false {
		t.Errorf("after SetKeyContextConfig, PreserveUserMessages = %v, want false", got.PreserveUserMessages)
	}
}

// TestFallbackLevel_String 验证 FallbackLevel.String()（候选 B）。
func TestFallbackLevel_String(t *testing.T) {
	tests := []struct {
		level FallbackLevel
		want  string
	}{
		{FallbackLevelNone, "none"},
		{FallbackLevelAISummary, "ai_summary"},
		{FallbackLevelKeyContext, "key_context"},
		{FallbackLevelRuleExtract, "rule_extract"},
		{FallbackLevel(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("FallbackLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}
