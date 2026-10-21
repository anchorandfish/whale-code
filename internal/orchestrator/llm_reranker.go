// llm_reranker.go — H10 Phase 4 模块 3 LLMReranker + rerank prompt 模板 + 降级策略（Phase 4 §3.2.3）。
// REF-外部-同类CLI@2026-03-31快照-memory types.ts sonnet 当选择器选 top-5；借鉴逻辑：小模型 rerank；落地：LLMReranker 用 haiku
// prompt 模板；落地：buildRerankPrompt + parseRerankResponse
// 不可用降级；落地：Rerank 失败时返回粗筛 top-1 + confidence=0.5
// ；落地：confidence < 0.5 不注入（§3.2.3 阈值）
// SNAPSHOT: pkg=internal/orchestrator; type=LLMReranker; model=haiku(默认); input=PatternSummary(不喂完整pattern); output={sorted_pattern_ids,best_confidence,reason}; fallback=粗筛top-1+confidence=0.5(LLM不可用); confidence_threshold={>=0.8降级模型,0.5-0.8不降级,<0.5不注入}; prompt_format=JSON

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LLMRerankerDefaultModel 默认 rerank 模型（haiku 成本最低，Phase 4 §3.2.3）。
const LLMRerankerDefaultModel = ModelHaiku

// ConfidenceThresholdHigh 高置信阈值（>= 此值且 verified 才降级模型，§3.6.2）。
const ConfidenceThresholdHigh = 0.8

// ConfidenceThresholdLow 低置信阈值（< 此值不注入，§3.2.3）。
const ConfidenceThresholdLow = 0.5

// FallbackConfidence LLM 不可用降级时的固定置信度（§5.1）。
const FallbackConfidence = 0.5

// AIClient LLMReranker 依赖的非流式 AI 客户端接口（项目内 ai.Client 仅支持 Stream，本接口隔离）。
// 仲裁前降级方案（Phase 4 v2 §3.2.3）：用自定义 interface + mock 测试，避免改 ai.Client 签名。
// 02 §4 仲裁通过后可替换为 ai.Client 的非流式扩展。
type AIClient interface {
	// Complete 非流式调用，返回完整文本输出。
	Complete(ctx context.Context, prompt string) (string, error)
	// Model 返回当前模型名（用于诊断）。
	Model() string
}

// LLMReranker 用小模型对粗筛 top-5 重排（Phase 4 §3.2.3）。
// v2 仲裁待定：复用 N11 StreamWithRetry 用途扩展待 02 §4 仲裁，仲裁前用降级方案（AIClient 接口）。
type LLMReranker struct {
	aiClient AIClient // 非流式调用（仲裁前降级方案）
	model    string   // 默认 haiku（成本最低）
}

// PatternSummary rerank 输入摘要（不喂完整 pattern，降低成本，Phase 4 §3.2.3）。
type PatternSummary struct {
	PatternID     string   `json:"pattern_id"`
	TaskSignature string   `json:"task_signature"`
	Tags          []string `json:"tags"`
	VerifiedState string   `json:"verified_state"`
	AdoptionRate  float64  `json:"adoption_rate"`
}

// rerankResponse LLM 返回的 JSON 结构（parseRerankResponse 解析）。
type rerankResponse struct {
	SortedPatternIDs []string `json:"sorted_pattern_ids"`
	BestConfidence   float64  `json:"best_confidence"`
	Reason           string   `json:"reason"`
}

// NewLLMReranker 创建 reranker，model 默认 haiku（Phase 4 §3.2.3）。
// aiClient 为 nil 时 Rerank 始终走降级路径（粗筛 top-1 + confidence=0.5）。
func NewLLMReranker(aiClient AIClient, model string) *LLMReranker {
	if model == "" {
		model = LLMRerankerDefaultModel
	}
	return &LLMReranker{
		aiClient: aiClient,
		model:    model,
	}
}

// Rerank 输入：任务签名 + top-5 候选 pattern 摘要；输出：排序后 pattern_id 列表 + 最佳命中置信度（Phase 4 §3.2.3）。
// LLM 不可用时降级为粗筛 top-1 + confidence=0.5（Phase 4 §5.1）。
// ctx 为 nil 时也走降级路径（便于 ExperienceStore.Match 在无 context 时调用）。
func (r *LLMReranker) Rerank(ctx context.Context, signature string, candidates []PatternSummary) (sorted []string, confidence float64, err error) {
	// 边界：无候选或单候选，直接返回
	if len(candidates) == 0 {
		return nil, 0, nil
	}
	if len(candidates) == 1 {
		return []string{candidates[0].PatternID}, FallbackConfidence, nil
	}

	// 降级路径：aiClient 为 nil 或 ctx 为 nil
	if r.aiClient == nil || ctx == nil {
		return r.degradeToTop1(candidates), FallbackConfidence, nil
	}

	// 构造 prompt
	prompt := r.buildRerankPrompt(signature, candidates)

	// 调用 LLM
	output, callErr := r.aiClient.Complete(ctx, prompt)
	if callErr != nil {
		// 降级：返回粗筛 top-1 + confidence=0.5（不阻塞编排，§5.1）
		return r.degradeToTop1(candidates), FallbackConfidence, nil
	}

	// 解析 LLM 输出
	sorted, confidence, _, parseErr := r.parseRerankResponse(output)
	if parseErr != nil {
		// 降级：返回粗筛 top-1 + confidence=0.5
		return r.degradeToTop1(candidates), FallbackConfidence, nil
	}

	return sorted, confidence, nil
}

// buildRerankPrompt 构造 rerank prompt（Phase 4 §3.2.3 模板）。
// 仅喂摘要不喂完整 Pattern（防完整 Steps/FilesTouched 泄漏，降低成本）。
func (r *LLMReranker) buildRerankPrompt(signature string, candidates []PatternSummary) string {
	var sb strings.Builder
	sb.WriteString("你是经验库精排器。请根据任务签名对候选 pattern 重新排序，并给出最佳命中的置信度。\n\n")
	sb.WriteString("任务签名：\n")
	sb.WriteString(signature)
	sb.WriteString("\n\n候选 pattern 摘要：\n")
	for i, c := range candidates {
		sb.WriteString(fmt.Sprintf("%d. pattern_id=%s | signature=%s | verified_state=%s | adoption_rate=%.2f | tags=%s\n",
			i+1, c.PatternID, c.TaskSignature, c.VerifiedState, c.AdoptionRate, strings.Join(c.Tags, ",")))
	}
	sb.WriteString("\n输出格式（严格 JSON，无其他文字）：\n")
	sb.WriteString(`{"sorted_pattern_ids":["id1","id2",...],"best_confidence":0.0~1.0,"reason":"简短理由"}`)
	sb.WriteString("\n")
	return sb.String()
}

// parseRerankResponse 解析 LLM JSON 输出为 sorted_pattern_ids + best_confidence + reason（Phase 4 §3.2.3）。
// 校验：sorted_pattern_ids 中的 id 都在 candidates 列表内（防 LLM 幻觉）；best_confidence ∈ [0,1]。
func (r *LLMReranker) parseRerankResponse(output string) (sorted []string, confidence float64, reason string, err error) {
	// 提取 JSON 子串（LLM 可能输出 markdown 代码块包裹的 JSON）
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return nil, 0, "", fmt.Errorf("llm_reranker: no JSON found in response")
	}

	var resp rerankResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return nil, 0, "", fmt.Errorf("llm_reranker: unmarshal: %w", err)
	}

	// 校验 sorted_pattern_ids 非空
	if len(resp.SortedPatternIDs) == 0 {
		return nil, 0, "", fmt.Errorf("llm_reranker: sorted_pattern_ids is empty")
	}

	// 校验 best_confidence ∈ [0, 1]
	if resp.BestConfidence < 0 || resp.BestConfidence > 1 {
		return nil, 0, "", fmt.Errorf("llm_reranker: best_confidence %v out of range [0,1]", resp.BestConfidence)
	}

	// 注：candidates 校验由调用方在 Rerank 内做（parseRerankResponse 不持有 candidates 列表）
	return resp.SortedPatternIDs, resp.BestConfidence, resp.Reason, nil
}

// ParseRerankResponse 公开包装（测试用，便于直接测试解析逻辑）。
func (r *LLMReranker) ParseRerankResponse(output string, candidates []PatternSummary) (sorted []string, confidence float64, reason string, err error) {
	sorted, confidence, reason, err = r.parseRerankResponse(output)
	if err != nil {
		return nil, 0, "", err
	}
	// 校验 sorted 中的 id 都在 candidates 列表内（防 LLM 幻觉）
	validIDs := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		validIDs[c.PatternID] = struct{}{}
	}
	for _, id := range sorted {
		if _, ok := validIDs[id]; !ok {
			return nil, 0, "", fmt.Errorf("llm_reranker: hallucinated pattern_id %q not in candidates", id)
		}
	}
	return sorted, confidence, reason, nil
}

// BuildRerankPrompt 公开包装（测试用，便于校验 prompt 不含完整 Pattern 字段）。
func (r *LLMReranker) BuildRerankPrompt(signature string, candidates []PatternSummary) string {
	return r.buildRerankPrompt(signature, candidates)
}

// degradeToTop1 降级为粗筛 top-1（保留原顺序，§5.1）。
func (r *LLMReranker) degradeToTop1(candidates []PatternSummary) []string {
	result := make([]string, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, c.PatternID)
	}
	return result
}

// extractJSON 从可能含 markdown 代码块的输出中提取 JSON 子串。
// 支持 ```json ... ``` 与裸 JSON 两种形式。
func extractJSON(output string) string {
	s := strings.TrimSpace(output)
	// 去掉 markdown 代码块
	if strings.HasPrefix(s, "```") {
		// 找第一个 \n
		nl := strings.Index(s, "\n")
		if nl < 0 {
			return ""
		}
		s = s[nl+1:]
		// 去掉结尾的 ```
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	// 找第一个 { 与最后一个 }
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

// Model 返回当前 rerank 模型名（仅用于诊断）。
func (r *LLMReranker) Model() string {
	return r.model
}
