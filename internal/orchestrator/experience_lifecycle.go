// experience_lifecycle.go — H10 Phase 4 模块 4 InjectContext + 四级生命周期状态机（Phase 4 §3.2.4/§3.3）。
// REF-外部-同类CLI@2026-03-31快照-memory types.ts stale 老化警告 2 天；借鉴逻辑：按时间衰减；落地：90 天标 deprecated
// ；落地：unverified→provisionally_verified→verified→deprecated
// 升级条件加严 L1Score>=60；落地：防同质化偏差（同源）
// BASE 变更 pattern_id 关联；落地：TaskCompletionSignal.PatternID 写入 quality.jsonl
// SNAPSHOT: pkg=internal/orchestrator; states=[unverified,provisionally_verified,verified,deprecated]; upgrade_conditions={unverified→provisionally_verified:L0Pass+L2=adopted+L1Score>=60, provisionally_verified→verified:3次跨会话L2=adopted+L1Score>=60}; degrade_conditions={verified→deprecated:l2_rate<0.3+90天, unverified→deprecated:L0Fail+L2=discarded}; cross_session_check=isCrossSession(不同orchestration_id); inject_policy={unverified:不注入,provisionally_verified:注入不降级,verified:注入+router降级,deprecated:不注入}

package orchestrator

import (
	"fmt"
	"time"
)

// L1ScoreUpgradeThreshold 升级条件加严阈值（v2 审查改进 4：L1Score >= 60 才升级，防同质化偏差）。
const L1ScoreUpgradeThreshold = 60

// CrossSessionUpgradeRequired 跨会话升级所需 L2=adopted 次数（§3.3.2）。
const CrossSessionUpgradeRequired = 3

// AdoptionRateWindow 采纳率滚动窗口（30 天，§3.3.3）。
const AdoptionRateWindow = 30 * 24 * time.Hour

// TaskCompletionSignal 子任务完成信号，驱动 pattern 生命周期更新（Phase 4 §3.1.3b）。
type TaskCompletionSignal struct {
	L0Pass          bool   `json:"l0_pass"`
	L1Score         int    `json:"l1_score"`   // v2 增加 L1Score >= 60 约束（审查改进 4）
	L2Verdict       string `json:"l2_verdict"` // adopted/modified/discarded
	CompletedAt     string `json:"completed_at"`
	OrchestrationID string `json:"orchestration_id"` // 跨会话判定用
	PatternID       string `json:"pattern_id"`       // v2 新增，写入 quality.jsonl
}

// ExperienceHit 经验库命中信息，注入 TaskContext 供子任务感知（Phase 4 §3.1.3b）。
type ExperienceHit struct {
	PatternID     string  `json:"pattern_id"`
	Confidence    float64 `json:"confidence"`
	VerifiedState string  `json:"verified_state"`
}

// FailureTrace 失败追踪信息，从失败子任务沉淀 pitfall（Phase 4 §3.1.3b）。
type FailureTrace struct {
	ErrorType    string   `json:"error_type"`
	ErrorMessage string   `json:"error_message"`
	FailedStep   string   `json:"failed_step"`
	FilesTouched []string `json:"files_touched"`
}

// InjectContext 命中后将经验注入 TaskContext（对齐总纲 §7.2 line 892-898）。
// 模型降级由 router 第四因子决策（§3.6），不在此强制覆盖。
// 注入策略（§3.3.2）：unverified / deprecated 不注入；provisionally_verified / verified 注入。
func (s *ExperienceStore) InjectContext(task *TaskContext, hit *Pattern) error {
	if task == nil {
		return fmt.Errorf("experience_lifecycle: task must not be nil")
	}
	if hit == nil {
		return fmt.Errorf("experience_lifecycle: hit must not be nil")
	}

	// 校验 VerifiedState：unverified / deprecated 不注入（致命反例 1/5 关联）
	if hit.VerifiedState == VerifiedStateUnverified {
		return fmt.Errorf("experience_lifecycle: pattern %s is unverified, not injected (§3.3.2)", hit.PatternID)
	}
	if hit.VerifiedState == VerifiedStateDeprecated {
		return fmt.Errorf("experience_lifecycle: pattern %s is deprecated, not injected (致命反例 5)", hit.PatternID)
	}

	// 注入 PriorSolution / PriorFiles / ExperienceHit
	task.PriorSolution = hit.Steps
	if task.PriorSolution == nil {
		task.PriorSolution = []Step{}
	}
	task.PriorFiles = hit.FilesTouched
	if task.PriorFiles == nil {
		task.PriorFiles = []string{}
	}
	task.ExperienceHit = &ExperienceHit{
		PatternID:     hit.PatternID,
		Confidence:    0.7, // 默认置信度（verified/provisionally_verified 注入即视为可复用）
		VerifiedState: hit.VerifiedState,
	}

	// 模型降级由 router 第四因子决策（§3.6），不在此强制覆盖（v2 修正，避免越权覆盖 router）
	// 不修改 task.BudgetMode / task.BudgetHint

	// 更新 LastUsedAt（命中即视为已使用）
	s.mu.Lock()
	hit.LastUsedAt = s.nowFunc().UTC().Format(time.RFC3339)
	s.mu.Unlock()

	// 持久化 LastUsedAt 变更（不报错，best-effort）
	_ = s.Save(hit)

	return nil
}

// UpdateLifecycle 根据子任务完成信号更新 pattern 生命周期（Phase 4 §3.3.3）。
// v2 升级条件加严：provisionally_verified 升级需 L1Score >= 60（审查改进 4，防同质化偏差）。
//
// H1-1 修复（全模块审查）：消除持锁跨 Store 调用导致的潜在死锁。
// 原实现持 s.mu 锁调用 qualityStore.ScanAll()（跨 Store 加锁），存在锁顺序倒置风险。
// 修复：在加锁前预取 quality 记录，加锁后仅做纯内存计算，不再持锁跨 Store 调用。
func (s *ExperienceStore) UpdateLifecycle(patternID string, signal TaskCompletionSignal) error {
	if patternID == "" {
		return fmt.Errorf("experience_lifecycle: patternID must not be empty")
	}

	p, err := s.Get(patternID)
	if err != nil {
		return fmt.Errorf("experience_lifecycle: get pattern %s: %w", patternID, err)
	}

	// H1-1 修复：在加锁前预取 quality 记录，避免持 s.mu 锁跨 Store 调用 ScanAll。
	// 仅在 VerifiedStateVerified 降级判定时需要 quality 记录（计算采纳率）。
	// 非 verified 状态预取为 nil（computeAdoptionRateFromRecords 会回退到 p.L2AdoptionRate）。
	var preFetchedRecords []QualityRecord
	if p.VerifiedState == VerifiedStateVerified && s.qualityStore != nil {
		records, qErr := s.qualityStore.ScanAll()
		if qErr == nil {
			preFetchedRecords = records
		}
	}

	// 持锁计算变更（不调用 Save，避免重入死锁；不调用 qualityStore.ScanAll，避免持锁过界）
	s.mu.Lock()
	pCopy := *p
	changed := false

	switch pCopy.VerifiedState {
	case VerifiedStateUnverified:
		// 升级路径：L0Pass && L2=adopted && L1Score>=60 → provisionally_verified
		if signal.L0Pass && signal.L2Verdict == L2Adopted && signal.L1Score >= L1ScoreUpgradeThreshold {
			pCopy.VerifiedState = VerifiedStateProvisionallyVerified
			pCopy.VerifiedAt = s.nowFunc().UTC().Format(time.RFC3339)
			// 初始化 provisionalAdoptions（内存字段不落 jsonl）
			s.provisionalAdoptions[patternID] = []string{signal.OrchestrationID}
			changed = true
		}
		// 降级路径：!L0Pass && L2=discarded → deprecated（直接淘汰，避免污染）
		if !signal.L0Pass && signal.L2Verdict == L2Discarded {
			pCopy.VerifiedState = VerifiedStateDeprecated
			changed = true
		}

	case VerifiedStateProvisionallyVerified:
		// 升级路径：L2=adopted && L1Score>=60 → provisionalAdoptions++（内存字段）
		// 累计 3 次跨会话 L2=adopted → verified
		if signal.L2Verdict == L2Adopted && signal.L1Score >= L1ScoreUpgradeThreshold {
			adoptions := s.provisionalAdoptions[patternID]
			adoptions = append(adoptions, signal.OrchestrationID)
			s.provisionalAdoptions[patternID] = adoptions

			// 检查是否达到 3 次跨会话
			if len(adoptions) >= CrossSessionUpgradeRequired && s.isCrossSessionLocked(patternID) {
				pCopy.VerifiedState = VerifiedStateVerified
				pCopy.VerifiedAt = s.nowFunc().UTC().Format(time.RFC3339)
				// 清零内存字段
				delete(s.provisionalAdoptions, patternID)
				changed = true
			}
		}
		// 降级路径：L2=discarded → 退回 unverified + 清零 provisionalAdoptions
		if signal.L2Verdict == L2Discarded {
			pCopy.VerifiedState = VerifiedStateUnverified
			delete(s.provisionalAdoptions, patternID)
			changed = true
		}

	case VerifiedStateVerified:
		// 降级路径：滚动 30 天采纳率 < 0.3 + 90 天未用 → deprecated
		// H1-1 修复：使用预取的 records，不再持锁调用 ScanAll
		rate := s.computeAdoptionRateFromRecords(&pCopy, preFetchedRecords, AdoptionRateWindow)
		pCopy.L2AdoptionRate = rate
		if rate < TTLLowAdoptionRate {
			if lastUsedAt, err := time.Parse(time.RFC3339, pCopy.LastUsedAt); err == nil {
				if s.nowFunc().Sub(lastUsedAt) > time.Duration(TTLVerifiedDays)*24*time.Hour {
					pCopy.VerifiedState = VerifiedStateDeprecated
					changed = true
				}
			}
		}

	case VerifiedStateDeprecated:
		// deprecated 状态不再变更（由 TTL 物理删除处理）
	}
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// 解锁后持久化（避免重入死锁）
	if err := s.Save(&pCopy); err != nil {
		return fmt.Errorf("experience_lifecycle: save pattern %s: %w", patternID, err)
	}
	return nil
}

// RecordPitfall 从失败子任务沉淀 pitfall（解总纲 §11.5 风险"失败成本不浪费"，Phase 4 §3.1.3b）。
func (s *ExperienceStore) RecordPitfall(task *TaskContext, failure FailureTrace) error {
	if task == nil {
		return fmt.Errorf("experience_lifecycle: task must not be nil")
	}

	// 构造 pitfall
	signature := task.Goal // 用 Goal 作为 task_signature（简化）
	if signature == "" {
		return fmt.Errorf("experience_lifecycle: task.Goal must not be empty")
	}

	scope := ScopeProject // 默认 project scope
	// 查找已有 pitfall（同 task_signature）
	existing, _ := s.LookupPitfallBySignature(scope, signature)
	if existing != nil {
		// 累加 Occurrences + 更新 LastSeenAt
		existing.Occurrences++
		existing.LastSeenAt = s.nowFunc().UTC().Format(time.RFC3339)
		if failure.ErrorMessage != "" {
			existing.Symptom = failure.ErrorMessage
		}
		if failure.ErrorType != "" {
			existing.RootCause = failure.ErrorType
		}
		if len(failure.FilesTouched) > 0 {
			existing.Mitigation = fmt.Sprintf("检查文件: %v", failure.FilesTouched)
		}
		return s.SavePitfall(existing)
	}

	// 创建新 pitfall
	pitfall := &Pitfall{
		PitfallID:     fmt.Sprintf("pitfall-%s-%d", safeID(signature), s.nowFunc().UnixNano()),
		TaskSignature: signature,
		Symptom:       failure.ErrorMessage,
		RootCause:     failure.ErrorType,
		Mitigation:    fmt.Sprintf("检查文件: %v", failure.FilesTouched),
		Occurrences:   1,
		LastSeenAt:    s.nowFunc().UTC().Format(time.RFC3339),
		Scope:         scope,
	}
	return s.SavePitfall(pitfall)
}

// computeAdoptionRate 计算 pattern 滚动 30 天的 L2 采纳率（反查 quality.jsonl，Phase 4 §3.3.3）。
// 无 quality.jsonl 数据时返回当前 pattern 的 L2AdoptionRate（向后兼容）。
func (s *ExperienceStore) computeAdoptionRate(p *Pattern, window time.Duration) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.computeAdoptionRateLocked(p, window)
}

// computeAdoptionRateLocked computeAdoptionRate 的锁内版本（调用方持锁）。
//
// H1-1 修复：此方法仍调用 ScanAll 供 computeAdoptionRate（公开方法）使用，
// 但 UpdateLifecycle（持锁路径）已改用 computeAdoptionRateFromRecords 避免持锁跨 Store。
// 本方法保留供非持锁场景调用，不构成死锁风险（computeAdoptionRate 仅持 s.mu 后短暂调用）。
func (s *ExperienceStore) computeAdoptionRateLocked(p *Pattern, window time.Duration) float64 {
	if p == nil {
		return 0
	}
	if s.qualityStore == nil {
		return p.L2AdoptionRate
	}
	records, err := s.qualityStore.ScanAll()
	if err != nil || len(records) == 0 {
		return p.L2AdoptionRate
	}
	return s.computeAdoptionRateFromRecords(p, records, window)
}

// computeAdoptionRateFromRecords 根据预取的 quality 记录计算滚动窗口采纳率（H1-1 修复）。
//
// 与 computeAdoptionRateLocked 的区别：不调用 qualityStore.ScanAll，避免持 s.mu 锁跨 Store 调用。
// 调用方在加锁前预取 records，加锁后调用本方法做纯内存计算，消除持锁过界风险。
// records 为 nil 或空时回退到 p.L2AdoptionRate（向后兼容）。
func (s *ExperienceStore) computeAdoptionRateFromRecords(p *Pattern, records []QualityRecord, window time.Duration) float64 {
	if p == nil {
		return 0
	}
	if len(records) == 0 {
		return p.L2AdoptionRate
	}

	now := s.nowFunc()
	var inWindow int
	var adopted int
	for _, r := range records {
		// 仅统计同 pattern_id 的记录
		if r.PatternID != p.PatternID {
			continue
		}
		// 仅统计窗口内的记录
		if r.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
				if now.Sub(ts) > window {
					continue
				}
			}
		}
		inWindow++
		if r.L2Verdict == L2Adopted {
			adopted++
		}
	}
	if inWindow == 0 {
		return p.L2AdoptionRate
	}
	return float64(adopted) / float64(inWindow)
}

// isCrossSession 判定 3 次 L2=adopted 是否来自不同 orchestration_id（防同编排会话误判，§3.3.3）。
func (s *ExperienceStore) isCrossSession(p *Pattern) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isCrossSessionLocked(p.PatternID)
}

// isCrossSessionLocked isCrossSession 的锁内版本（调用方持锁）。
// 判定逻辑：provisionalAdoptions 中的 orchestration_id 至少有 2 个不同值（防同会话内多次子任务被误判）。
func (s *ExperienceStore) isCrossSessionLocked(patternID string) bool {
	adoptions := s.provisionalAdoptions[patternID]
	if len(adoptions) < CrossSessionUpgradeRequired {
		return false
	}
	unique := make(map[string]struct{}, len(adoptions))
	for _, id := range adoptions {
		unique[id] = struct{}{}
	}
	// 至少有 2 个不同 orchestration_id（容忍 1 次同会话）
	return len(unique) >= 2
}

// safeID 生成安全的 pitfall ID 片段（去非字母数字）。
func safeID(s string) string {
	var sb []byte
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb = append(sb, byte(r))
		} else if r == ' ' || r == '-' || r == '_' {
			sb = append(sb, '-')
		}
	}
	if len(sb) == 0 {
		return "unknown"
	}
	// 截断到 16 字符
	if len(sb) > 16 {
		sb = sb[:16]
	}
	return string(sb)
}
