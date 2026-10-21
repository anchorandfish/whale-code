// llm_reranker_test.go — H10 Phase 4 模块 3 LLMReranker 反例测试（TDD 先行，Phase 4 §3.2.3）。
// 覆盖：Rerank 成功 / LLM 不可用降级为 top-1 + confidence=0.5 / 幻觉 pattern_id 拒绝 / confidence 越界拒绝 / 无效 JSON 拒绝 / prompt 不泄漏完整 Pattern。
// prompt 模板；落地：buildRerankPrompt + parseRerankResponse
// 不可用降级；落地：Rerank 失败时返回粗筛 top-1 + confidence=0.5
// ；落地：confidence < 0.5 不注入（§3.2.3 阈值）
// SNAPSHOT: tests={Rerank_Success, Rerank_LLMUnavailable_DegradeToTop1, ParseResponse_HallucinatedPatternID_Rejected, ParseResponse_ConfidenceOutOfRange_Rejected, ParseResponse_InvalidJSON_Rejected, Prompt_NoFullPatternLeak, Rerank_NilAIClient_DegradeToTop1, Rerank_NilContext_DegradeToTop1, Rerank_EmptyCandidates_EmptyResult, Rerank_SingleCandidate_ReturnDirectly, ParseResponse_MarkdownCodeBlock_Extracted, Model_DefaultHaiku}

package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// mockAIClient 测试用 AIClient mock 实现。
type mockAIClient struct {
	completeFn func(ctx context.Context, prompt string) (string, error)
	modelName  string
	called     bool
	lastPrompt string
}

// Complete 实现 AIClient 接口。
func (m *mockAIClient) Complete(ctx context.Context, prompt string) (string, error) {
	m.called = true
	m.lastPrompt = prompt
	if m.completeFn != nil {
		return m.completeFn(ctx, prompt)
	}
	return "", nil
}

// Model 实现 AIClient 接口。
func (m *mockAIClient) Model() string {
	if m.modelName == "" {
		return ModelHaiku
	}
	return m.modelName
}

// makeTestPatternSummaries 构造测试用 PatternSummary 列表。
func makeTestPatternSummaries(n int) []PatternSummary {
	summaries := make([]PatternSummary, 0, n)
	for i := 0; i < n; i++ {
		summaries = append(summaries, PatternSummary{
			PatternID:     "p-rerank-" + string(rune('a'+i)),
			TaskSignature: "fix bug in module " + string(rune('a'+i)),
			Tags:          []string{".go"},
			VerifiedState: VerifiedStateVerified,
			AdoptionRate:  0.8,
		})
	}
	return summaries
}

// TestLLMReranker_Rerank_Success mock aiClient 返回合法 JSON，验证 sorted + confidence 正确。
func TestLLMReranker_Rerank_Success(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(3)

	client := &mockAIClient{
		completeFn: func(ctx context.Context, prompt string) (string, error) {
			// 返回合法 JSON（sorted 顺序反转，confidence=0.9）
			return `{"sorted_pattern_ids":["p-rerank-c","p-rerank-b","p-rerank-a"],"best_confidence":0.9,"reason":"high match"}`, nil
		},
	}
	r := NewLLMReranker(client, ModelHaiku)

	sorted, confidence, err := r.Rerank(context.Background(), "fix bug in module", candidates)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("sorted 应有 3 个 pattern_id, got %d", len(sorted))
	}
	if sorted[0] != "p-rerank-c" {
		t.Errorf("sorted[0] 应为 p-rerank-c, got %s", sorted[0])
	}
	if confidence != 0.9 {
		t.Errorf("confidence 应为 0.9, got %v", confidence)
	}
}

// TestLLMReranker_Rerank_LLMUnavailable_DegradeToTop1 mock aiClient 返回 error → 降级为 top-1 + confidence=0.5（Phase 4 §5.1）。
func TestLLMReranker_Rerank_LLMUnavailable_DegradeToTop1(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(3)

	client := &mockAIClient{
		completeFn: func(ctx context.Context, prompt string) (string, error) {
			return "", context.DeadlineExceeded
		},
	}
	r := NewLLMReranker(client, ModelHaiku)

	sorted, confidence, err := r.Rerank(context.Background(), "fix bug in module", candidates)
	if err != nil {
		t.Fatalf("LLM 不可用时 Rerank 不应返回 error（应降级），got: %v", err)
	}
	// 降级：返回粗筛 top-1（保留原顺序）
	if len(sorted) == 0 {
		t.Fatal("降级后 sorted 不应为空")
	}
	// confidence 应为 0.5（FallbackConfidence）
	if confidence != FallbackConfidence {
		t.Errorf("LLM 不可用降级 confidence 应为 %v, got %v", FallbackConfidence, confidence)
	}
}

// TestLLMReranker_Rerank_NilAIClient_DegradeToTop1 aiClient=nil 时降级为 top-1 + confidence=0.5。
func TestLLMReranker_Rerank_NilAIClient_DegradeToTop1(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(3)
	r := NewLLMReranker(nil, ModelHaiku)

	sorted, confidence, err := r.Rerank(context.Background(), "fix bug in module", candidates)
	if err != nil {
		t.Fatalf("aiClient=nil 时 Rerank 不应返回 error（应降级），got: %v", err)
	}
	if len(sorted) == 0 {
		t.Fatal("降级后 sorted 不应为空")
	}
	if confidence != FallbackConfidence {
		t.Errorf("aiClient=nil 降级 confidence 应为 %v, got %v", FallbackConfidence, confidence)
	}
}

// TestLLMReranker_Rerank_NilContext_DegradeToTop1 ctx=nil 时降级为 top-1 + confidence=0.5。
func TestLLMReranker_Rerank_NilContext_DegradeToTop1(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(3)
	client := &mockAIClient{}
	r := NewLLMReranker(client, ModelHaiku)

	sorted, confidence, err := r.Rerank(nil, "fix bug in module", candidates)
	if err != nil {
		t.Fatalf("ctx=nil 时 Rerank 不应返回 error（应降级），got: %v", err)
	}
	if len(sorted) == 0 {
		t.Fatal("降级后 sorted 不应为空")
	}
	if confidence != FallbackConfidence {
		t.Errorf("ctx=nil 降级 confidence 应为 %v, got %v", FallbackConfidence, confidence)
	}
}

// TestLLMReranker_Rerank_EmptyCandidates_EmptyResult 无候选返回空（反例边界）。
func TestLLMReranker_Rerank_EmptyCandidates_EmptyResult(t *testing.T) {
	t.Parallel()
	client := &mockAIClient{}
	r := NewLLMReranker(client, ModelHaiku)

	sorted, confidence, err := r.Rerank(context.Background(), "fix bug", nil)
	if err != nil {
		t.Fatalf("无候选不应返回 error, got: %v", err)
	}
	if len(sorted) != 0 {
		t.Errorf("无候选 sorted 应为空, got %d", len(sorted))
	}
	if confidence != 0 {
		t.Errorf("无候选 confidence 应为 0, got %v", confidence)
	}
}

// TestLLMReranker_Rerank_SingleCandidate_ReturnDirectly 单候选直接返回（不调 LLM）。
func TestLLMReranker_Rerank_SingleCandidate_ReturnDirectly(t *testing.T) {
	t.Parallel()
	client := &mockAIClient{}
	r := NewLLMReranker(client, ModelHaiku)

	candidates := []PatternSummary{
		{PatternID: "p-single", TaskSignature: "fix single bug", VerifiedState: VerifiedStateVerified, AdoptionRate: 0.9},
	}
	sorted, confidence, err := r.Rerank(context.Background(), "fix single bug", candidates)
	if err != nil {
		t.Fatalf("单候选 Rerank: %v", err)
	}
	if len(sorted) != 1 || sorted[0] != "p-single" {
		t.Errorf("单候选应直接返回, got %v", sorted)
	}
	// 单候选 confidence 应为 FallbackConfidence（0.5）
	if confidence != FallbackConfidence {
		t.Errorf("单候选 confidence 应为 %v, got %v", FallbackConfidence, confidence)
	}
	// 不应调用 LLM
	if client.called {
		t.Error("单候选不应调用 LLM")
	}
}

// TestLLMReranker_ParseResponse_HallucinatedPatternID_Rejected LLM 返回不在 candidates 中的 pattern_id → 返回 error（防 LLM 幻觉）。
func TestLLMReranker_ParseResponse_HallucinatedPatternID_Rejected(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(3)
	r := NewLLMReranker(nil, ModelHaiku)

	// 返回含幻觉 pattern_id 的 JSON
	output := `{"sorted_pattern_ids":["p-rerank-a","hallucinated-id"],"best_confidence":0.9,"reason":"test"}`

	_, _, _, err := r.ParseRerankResponse(output, candidates)
	if err == nil {
		t.Fatal("LLM 返回不在 candidates 中的 pattern_id 应返回 error（防幻觉）")
	}
	if !strings.Contains(err.Error(), "hallucinated") {
		t.Errorf("error 应含 'hallucinated' 关键词, got: %v", err)
	}
}

// TestLLMReranker_ParseResponse_ConfidenceOutOfRange_Rejected confidence=1.5 → 返回 error。
func TestLLMReranker_ParseResponse_ConfidenceOutOfRange_Rejected(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(2)
	r := NewLLMReranker(nil, ModelHaiku)

	// confidence=1.5 越界
	output := `{"sorted_pattern_ids":["p-rerank-a","p-rerank-b"],"best_confidence":1.5,"reason":"test"}`

	_, _, _, err := r.ParseRerankResponse(output, candidates)
	if err == nil {
		t.Fatal("confidence=1.5 越界应返回 error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error 应含 'out of range', got: %v", err)
	}

	// confidence=-0.1 也应拒绝
	outputNeg := `{"sorted_pattern_ids":["p-rerank-a"],"best_confidence":-0.1,"reason":"test"}`
	_, _, _, err = r.ParseRerankResponse(outputNeg, candidates)
	if err == nil {
		t.Fatal("confidence=-0.1 越界应返回 error")
	}
}

// TestLLMReranker_ParseResponse_InvalidJSON_Rejected LLM 返回非 JSON → 返回 error。
func TestLLMReranker_ParseResponse_InvalidJSON_Rejected(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(2)
	r := NewLLMReranker(nil, ModelHaiku)

	// 非 JSON 输出
	_, _, _, err := r.ParseRerankResponse("this is not json at all", candidates)
	if err == nil {
		t.Fatal("非 JSON 输出应返回 error")
	}
}

// TestLLMReranker_ParseResponse_EmptySorted_Rejected sorted_pattern_ids 为空 → 返回 error。
func TestLLMReranker_ParseResponse_EmptySorted_Rejected(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(2)
	r := NewLLMReranker(nil, ModelHaiku)

	output := `{"sorted_pattern_ids":[],"best_confidence":0.9,"reason":"test"}`
	_, _, _, err := r.ParseRerankResponse(output, candidates)
	if err == nil {
		t.Fatal("sorted_pattern_ids 为空应返回 error")
	}
}

// TestLLMReranker_ParseResponse_MarkdownCodeBlock_Extracted LLM 返回 markdown 代码块包裹的 JSON 也能解析。
func TestLLMReranker_ParseResponse_MarkdownCodeBlock_Extracted(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(2)
	r := NewLLMReranker(nil, ModelHaiku)

	output := "```json\n" + `{"sorted_pattern_ids":["p-rerank-a","p-rerank-b"],"best_confidence":0.85,"reason":"test"}` + "\n```"
	sorted, confidence, _, err := r.ParseRerankResponse(output, candidates)
	if err != nil {
		t.Fatalf("markdown 代码块包裹的 JSON 应能解析, got: %v", err)
	}
	if len(sorted) != 2 {
		t.Errorf("sorted 应有 2 个, got %d", len(sorted))
	}
	if confidence != 0.85 {
		t.Errorf("confidence 应为 0.85, got %v", confidence)
	}
}

// TestLLMReranker_Prompt_NoFullPatternLeak rerank prompt 不含完整 Steps/FilesTouched（仅摘要）。
func TestLLMReranker_Prompt_NoFullPatternLeak(t *testing.T) {
	t.Parallel()
	client := &mockAIClient{}
	r := NewLLMReranker(client, ModelHaiku)

	// 构造含完整 Steps/FilesTouched 的候选摘要
	candidates := []PatternSummary{
		{
			PatternID:     "p-leak-test",
			TaskSignature: "fix leak test bug",
			Tags:          []string{".go"},
			VerifiedState: VerifiedStateVerified,
			AdoptionRate:  0.8,
		},
	}
	// 需 ≥2 候选才走 LLM 路径
	candidates = append(candidates, PatternSummary{
		PatternID:     "p-leak-test-2",
		TaskSignature: "fix another bug",
		Tags:          []string{".go"},
		VerifiedState: VerifiedStateVerified,
		AdoptionRate:  0.7,
	})

	prompt := r.BuildRerankPrompt("fix leak test bug", candidates)

	// prompt 不应含 Steps 字段（仅摘要，不含完整解法步骤）
	if strings.Contains(prompt, "steps") || strings.Contains(prompt, "Steps") {
		t.Errorf("rerank prompt 不应含完整 Steps 字段（仅摘要）:\n%s", prompt)
	}
	// prompt 不应含 FilesTouched 字段
	if strings.Contains(prompt, "files_touched") || strings.Contains(prompt, "FilesTouched") {
		t.Errorf("rerank prompt 不应含 FilesTouched 字段（仅摘要）:\n%s", prompt)
	}
	// prompt 应含 PatternID（摘要字段）
	if !strings.Contains(prompt, "p-leak-test") {
		t.Errorf("rerank prompt 应含 pattern_id 摘要字段:\n%s", prompt)
	}
	// prompt 应含 task_signature 摘要
	if !strings.Contains(prompt, "fix leak test bug") {
		t.Errorf("rerank prompt 应含 task_signature 摘要:\n%s", prompt)
	}
}

// TestLLMReranker_Prompt_ContainsSignatureAndCandidates rerank prompt 含任务签名 + 候选摘要 + 输出格式说明。
func TestLLMReranker_Prompt_ContainsSignatureAndCandidates(t *testing.T) {
	t.Parallel()
	client := &mockAIClient{}
	r := NewLLMReranker(client, ModelHaiku)
	candidates := makeTestPatternSummaries(3)

	prompt := r.BuildRerankPrompt("fix task signature", candidates)

	if !strings.Contains(prompt, "fix task signature") {
		t.Errorf("prompt 应含任务签名")
	}
	if !strings.Contains(prompt, "sorted_pattern_ids") {
		t.Errorf("prompt 应含输出格式说明 sorted_pattern_ids")
	}
	if !strings.Contains(prompt, "best_confidence") {
		t.Errorf("prompt 应含输出格式说明 best_confidence")
	}
}

// TestLLMReranker_Model_DefaultHaiku 未指定 model 时默认 haiku。
func TestLLMReranker_Model_DefaultHaiku(t *testing.T) {
	t.Parallel()
	r := NewLLMReranker(nil, "")
	if r.Model() != ModelHaiku {
		t.Errorf("未指定 model 时应默认 haiku, got %s", r.Model())
	}
}

// TestLLMReranker_Model_Explicit 显式指定 model 时返回该 model。
func TestLLMReranker_Model_Explicit(t *testing.T) {
	t.Parallel()
	r := NewLLMReranker(nil, ModelSonnet)
	if r.Model() != ModelSonnet {
		t.Errorf("显式指定 sonnet 应返回 sonnet, got %s", r.Model())
	}
}

// TestLLMReranker_Rerank_ParseFail_DegradeToTop1 LLM 返回无法解析的 JSON → 降级为 top-1 + confidence=0.5。
func TestLLMReranker_Rerank_ParseFail_DegradeToTop1(t *testing.T) {
	t.Parallel()
	candidates := makeTestPatternSummaries(3)
	client := &mockAIClient{
		completeFn: func(ctx context.Context, prompt string) (string, error) {
			return "invalid json response", nil
		},
	}
	r := NewLLMReranker(client, ModelHaiku)

	sorted, confidence, err := r.Rerank(context.Background(), "fix bug", candidates)
	if err != nil {
		t.Fatalf("解析失败应降级而非返回 error, got: %v", err)
	}
	if len(sorted) == 0 {
		t.Fatal("降级后 sorted 不应为空")
	}
	if confidence != FallbackConfidence {
		t.Errorf("解析失败降级 confidence 应为 %v, got %v", FallbackConfidence, confidence)
	}
}
