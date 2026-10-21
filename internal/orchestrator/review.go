// review.go — H10 Phase 2 模块 4 审查子对话接口骨架（总纲 §5.4，Phase 3 完善具体派发）。
// ；落地：ReviewSubconversation 接口骨架（Phase 2 noop 实现，Phase 3 替换）
// /020-审查 1 轮硬上限 + 不许凑数；落地：ReviewRequest.MaxRound=1（ValidateReviewRequest 校验）+ ReviewReport.Round=1
// ；落地：ReviewRequest.NoPlan 字段（true 时不喂方案）
// SNAPSHOT: pkg=internal/orchestrator; type=ReviewSubconversation(interface); methods={Review}; phase2=noop空实现; phase3=真实子对话派发; MaxRound=1(); NoPlan(); EvidenceURLs()

package orchestrator

import (
	"context"
	"fmt"
)

// ReviewSubconversation 审查子对话接口骨架（总纲 §5.4，Phase 3 完善）。
// Phase 2 只定义接口 + 空实现，Phase 3 实现独立审查子对话派发。
// 审查原则：1 轮硬上限（/020）+ 不喂方案+ 0 个就是 0 个不许凑数。
type ReviewSubconversation interface {
	// Review 派发独立审查子对话，1 轮硬上限（/020）。
	// noPlan=true 时不喂方案（审查代码，），noPlan=false 时喂方案（审查方案本身）。
	Review(ctx context.Context, req ReviewRequest) (*ReviewReport, error)
}

// ReviewRequest 审查请求（总纲 §5.4）。
// Phase 3 扩展（SNAPSHOT）：新增 AuthorFiles 字段，供 SanitizeAuthorContext 屏蔽作者文件路径。
type ReviewRequest struct {
	TaskID     string `json:"task_id"`            // 任务 ID
	Stage      string `json:"stage"`              // plan/review
	NoPlan     bool   `json:"no_plan"`            // 是否不喂方案（true=审代码 false=审方案）
	Dimensions int    `json:"dimensions"`         // 审查维度数（3/7）
	MaxRound   int    `json:"max_round"`          // 必须 = 1（/020 硬上限）
	Content    string `json:"content"`            // 审查对象（代码或方案）
	PlanDoc    string `json:"plan_doc,omitempty"` // noPlan=false 时喂的方案文档路径或内联
	// AuthorFiles 作者文件路径列表（Phase 3 新增， 屏蔽作者上下文，致命反例 3）。
	// SanitizeAuthorContext 会将这些路径从 Content 中替换为 <author_file_redacted>。
	// SNAPSHOT-Phase3: field=AuthorFiles; type=[]string; json=author_files,omitempty; reason=DEC-011屏蔽作者上下文(致命反例3); backward_compat=向后兼容(omitempty, Phase 2 调用方不传该字段不影响)
	AuthorFiles []string `json:"author_files,omitempty"`
}

// ReviewReport 审查报告（总纲 §5.4）。
type ReviewReport struct {
	Verdict      string   `json:"verdict"`       // pass/fail
	FatalFlaws   []string `json:"fatal_flaws"`   // 致命漏洞（阻断，0 个就是 0 个不许凑数）
	Suggestions  []string `json:"suggestions"`   // 建议优化（不阻断）
	Round        int      `json:"round"`         // 实际轮数（必须 = 1）
	EvidenceURLs []string `json:"evidence_urls"` // 官方源证据（，致命漏洞必备）
}

// ReviewVerdictPass / ReviewVerdictFail 审查结论枚举（总纲 §5.4）。
const (
	ReviewVerdictPass = "pass"
	ReviewVerdictFail = "fail"
)

// noopReviewSubconversation Phase 2 空实现（Phase 3 替换为真实子对话派发）。
// Review 返回 verdict=pass, round=1 不 panic，保证接口契约稳定。
type noopReviewSubconversation struct{}

// Review noop 实现：返回 verdict=pass, round=1（Phase 3 替换为真实派发）。
// 不会校验 MaxRound（noop 不实际派发），但调用方应通过 ValidateReviewRequest 预检。
func (n *noopReviewSubconversation) Review(ctx context.Context, req ReviewRequest) (*ReviewReport, error) {
	return &ReviewReport{
		Verdict: ReviewVerdictPass,
		Round:   1,
	}, nil
}

// NewReviewSubconversation Phase 2 返回空实现，Phase 3 返回真实派发。
// 调用方不感知实现切换（接口稳定， FALLBACK 声明）。
func NewReviewSubconversation() ReviewSubconversation {
	return &noopReviewSubconversation{}
}

// ValidateReviewRequest 校验审查请求合法性（/020 硬约束，零容忍）。
// 校验项：① MaxRound 必须 = 1（1 轮硬上限）② TaskID 非空 ③ Stage 非空 ④ Content 非空。
// noPlan=false 时 PlanDoc 必须非空（喂方案审查方案本身）。
func ValidateReviewRequest(req *ReviewRequest) error {
	if req == nil {
		return fmt.Errorf("review: request must not be nil")
	}
	// ① MaxRound 必须 = 1（/020 硬上限，零容忍）
	if req.MaxRound != 1 {
		return fmt.Errorf("review: MaxRound must be 1, got %d", req.MaxRound)
	}
	// ② TaskID 非空
	if req.TaskID == "" {
		return fmt.Errorf("review: TaskID must not be empty")
	}
	// ③ Stage 非空
	if req.Stage == "" {
		return fmt.Errorf("review: Stage must not be empty")
	}
	// ④ Content 非空（审查对象必须提供）
	if req.Content == "" {
		return fmt.Errorf("review: Content must not be empty")
	}
	// noPlan=false 时 PlanDoc 必须非空（喂方案审查）
	if !req.NoPlan && req.PlanDoc == "" {
		return fmt.Errorf("review: PlanDoc must not be empty when NoPlan=false")
	}
	return nil
}
