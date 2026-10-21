// pattern_validator.go — H12 自我进化系统 §2.3 模式验证器（v0.2 修正）。
// 设计说明 §2.3 + §3.3；落地：基于 L2 历史评分流转 Pattern.VerifiedState
// v0.2；落地：四级生命周期 + 冷启动保护 + 双验证升级
// SNAPSHOT: pkg=internal/orchestrator; type=PatternValidator; flow={ScanAll反查L2→按流转规则表判断→Save持久化}; states={unverified→provisionally_verified(L2连续2次adopted+L1≥60), provisionally_verified→verified(再2次累计4次), verified→provisionally_verified(L2连续3次discarded), provisionally_verified→unverified(L2连续3次discarded), 任意→deprecated(L2连续5次discarded)}; cold_start_protection=前10任务不验证; concurrency=sync.Mutex
package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// PatternValidatorColdStartThreshold 冷启动保护阈值：前 10 任务不验证（§2.3）。
const PatternValidatorColdStartThreshold = 10

// PatternValidatorL1ScoreThreshold L1 评分阈值：L1Score>=60 才升级（§2.3 流转规则表）。
const PatternValidatorL1ScoreThreshold = 60

// PatternValidatorUpgradeConsecutive 升级所需 L2 连续 adopted 次数（§2.3）。
const PatternValidatorUpgradeConsecutive = 2

// PatternValidatorDegradeConsecutive 降级所需 L2 连续 discarded 次数（§2.3）。
const PatternValidatorDegradeConsecutive = 3

// PatternValidatorDeprecateConsecutive 淘汰所需 L2 连续 discarded 次数（§2.3）。
const PatternValidatorDeprecateConsecutive = 5

// PatternValidatorWindow L2 评分反查窗口（30 天）。
const PatternValidatorWindow = 30 * 24 * time.Hour

// PatternValidator 模式验证器（v0.2 修正：四级生命周期 + 冷启动保护 + 双验证升级）。
// 基于 L2 历史评分流转 Pattern 的 VerifiedState（unverified → provisionally_verified → verified → deprecated）。
//
// 关键约束：
// - 不修改 Pattern struct 字段签名（BASE 保护）
// - 严格用 experience.go:22-27 四级枚举字符串常量
// - 冷启动期（前 10 任务）跳过验证，避免样本不足误判
// - L2 评分缺失 → 跳过验证，返回 nil
type PatternValidator struct {
	experience  *ExperienceStore
	quality     *QualityStore
	taskCounter int // 任务计数器（冷启动保护，前 10 任务不验证）
	mu          sync.Mutex
}

// NewPatternValidator 构造模式验证器。
// experience / quality 必填，空时 Validate 直接返回 nil（降级）。
func NewPatternValidator(experience *ExperienceStore, quality *QualityStore) *PatternValidator {
	return &PatternValidator{
		experience: experience,
		quality:    quality,
	}
}

// IncrementTaskCount 任务计数器+1（编排器每次任务完成调，§2.3 冷启动保护）。
func (p *PatternValidator) IncrementTaskCount() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.taskCounter++
}

// GetTaskCount 返回当前任务计数（测试用）。
func (p *PatternValidator) GetTaskCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.taskCounter
}

// Validate 验证 Pattern 是否可升级/降级 VerifiedState（§2.3 + §3.3）。
// 流程：
// 1. ctx 校验 + experience/quality 非空校验
// 2. 冷启动保护：taskCounter < 10 直接返回 nil
// 3. 反查 QualityStore.ScanAll 取该 pattern_id 的近期 L2 评分（按 timestamp 排序，30 天窗口）
// 4. 按流转规则表判断是否升级/降级
// 5. 调 ExperienceStore.Save 持久化新 VerifiedState
//
// 流转规则表（§2.3）：
// - unverified → provisionally_verified: L2 连续 2 次 adopted 且 L1Score>=60
// - provisionally_verified → verified: 再 L2 连续 2 次 adopted 且 L1Score>=60（累计 4 次）
// - verified → provisionally_verified: L2 连续 3 次 discarded
// - provisionally_verified → unverified: L2 连续 3 次 discarded
// - 任意 → deprecated: L2 连续 5 次 discarded（终态）
//
// 降级：L2 评分缺失/冷启动期 → 跳过验证，返回 nil
func (p *PatternValidator) Validate(ctx context.Context, patternID string) error {
	// ① ctx 校验
	if err := ctx.Err(); err != nil {
		return err
	}
	// ② experience / quality / patternID 校验
	if p == nil || p.experience == nil || p.quality == nil {
		return nil // 降级
	}
	if patternID == "" {
		return fmt.Errorf("pattern_validator: patternID must not be empty")
	}
	// ③ 冷启动保护
	p.mu.Lock()
	counter := p.taskCounter
	p.mu.Unlock()
	if counter < PatternValidatorColdStartThreshold {
		return nil // 冷启动期跳过验证
	}

	// ④ 获取当前 Pattern
	pattern, err := p.experience.Get(patternID)
	if err != nil {
		return nil // Pattern 不存在视为已淘汰，跳过验证
	}
	// deprecated 终态不再流转
	if pattern.VerifiedState == VerifiedStateDeprecated {
		return nil
	}
	// 值拷贝，避免并发写共享指针（参考 pattern_extractor.go:93-94）
	// 注：ExperienceStore.Get 返回的是缓存内的 *Pattern，直接修改会污染缓存且引发 data race
	// （另一个 goroutine 可能同时读 pattern.VerifiedState，如 ExperienceStore.Match 构造 PatternSummary）。
	// Save 失败时缓存已被改但 jsonl 未更新将无法回滚，故必须先拷贝再改。
	cloned := *pattern

	// ⑤ 反查 L2 评分历史
	records, err := p.quality.ScanAll()
	if err != nil {
		return nil // ScanAll 失败降级
	}
	// 筛选该 pattern_id 的记录 + 30 天窗口
	now := time.Now().UTC()
	var l2Records []QualityRecord
	for _, r := range records {
		if r.PatternID != patternID {
			continue
		}
		if r.Timestamp == "" {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, r.Timestamp)
		if perr != nil {
			continue
		}
		if now.Sub(ts) > PatternValidatorWindow {
			continue
		}
		l2Records = append(l2Records, r)
	}
	if len(l2Records) == 0 {
		return nil // 无 L2 评分降级
	}
	// 按 timestamp 升序排序（旧→新）
	sort.Slice(l2Records, func(i, j int) bool {
		return l2Records[i].Timestamp < l2Records[j].Timestamp
	})

	// ⑥ 按流转规则表判断
	newState, ok := p.evaluateTransition(cloned.VerifiedState, l2Records)
	if !ok {
		return nil // 不触发流转
	}

	// ⑦ 持久化新 VerifiedState（在拷贝上修改，原始缓存对象不受影响）
	cloned.VerifiedState = newState
	cloned.VerifiedAt = now.Format(time.RFC3339)
	if saveErr := p.experience.Save(&cloned); saveErr != nil {
		// 降级：Save 失败 warn 不阻断
		return nil
	}
	return nil
}

// evaluateTransition 按流转规则表评估新状态（核心算法，§2.3 流转规则表）。
// l2Records 按时间升序（旧→新），取末尾若干条作为"最近连续 N 次"。
// 返回 (newState, true) 表示流转；(原状态, false) 表示不流转。
func (p *PatternValidator) evaluateTransition(currentState string, l2Records []QualityRecord) (string, bool) {
	n := len(l2Records)

	// 优先级 1：任意 → deprecated（L2 连续 5 次 discarded，终态）
	if n >= PatternValidatorDeprecateConsecutive {
		last5 := l2Records[n-PatternValidatorDeprecateConsecutive:]
		if allDiscarded(last5) {
			return VerifiedStateDeprecated, true
		}
	}

	// 优先级 2：按当前状态分支
	switch currentState {
	case VerifiedStateUnverified:
		// 升级：L2 连续 2 次 adopted + L1Score>=60
		if n >= PatternValidatorUpgradeConsecutive {
			last2 := l2Records[n-PatternValidatorUpgradeConsecutive:]
			if allAdoptedAndHighL1(last2, PatternValidatorL1ScoreThreshold) {
				return VerifiedStateProvisionallyVerified, true
			}
		}

	case VerifiedStateProvisionallyVerified:
		// 升级：再 L2 连续 2 次 adopted + L1Score>=60（累计 4 次）
		if n >= PatternValidatorUpgradeConsecutive*2 {
			last4 := l2Records[n-PatternValidatorUpgradeConsecutive*2:]
			if allAdoptedAndHighL1(last4, PatternValidatorL1ScoreThreshold) {
				return VerifiedStateVerified, true
			}
		}
		// 降级：L2 连续 3 次 discarded → unverified
		if n >= PatternValidatorDegradeConsecutive {
			last3 := l2Records[n-PatternValidatorDegradeConsecutive:]
			if allDiscarded(last3) {
				return VerifiedStateUnverified, true
			}
		}

	case VerifiedStateVerified:
		// 降级：L2 连续 3 次 discarded → provisionally_verified
		if n >= PatternValidatorDegradeConsecutive {
			last3 := l2Records[n-PatternValidatorDegradeConsecutive:]
			if allDiscarded(last3) {
				return VerifiedStateProvisionallyVerified, true
			}
		}
	}

	// 默认不流转
	return currentState, false
}

// allDiscarded 检查所有记录的 L2Verdict 是否为 discarded。
func allDiscarded(records []QualityRecord) bool {
	if len(records) == 0 {
		return false
	}
	for _, r := range records {
		if r.L2Verdict != L2Discarded {
			return false
		}
	}
	return true
}

// allAdoptedAndHighL1 检查所有记录 L2=adopted 且 L1Score>=threshold。
func allAdoptedAndHighL1(records []QualityRecord, threshold int) bool {
	if len(records) == 0 {
		return false
	}
	for _, r := range records {
		if r.L2Verdict != L2Adopted {
			return false
		}
		if r.L1Score < threshold {
			return false
		}
	}
	return true
}
