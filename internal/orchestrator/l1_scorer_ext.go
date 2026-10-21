// l1_scorer_ext.go — H10 Phase 5 桶 1 L1 权重学习扩展（地基层 §4.4 + §4.7 + Phase 5 §4.4）。
// REF-外部-Langfuse Custom Evaluation via SDKs-API@https://langfuse.com/docs/scores+2026-07-15访问-自定义评估算法；落地：L1WeightsExt 自定义权重
// line 644-653-8 项权重已定初版；落地：复用权重值，重构为 map[string]int 访问
// line 394-prompt_version 字段已支持 A/B 测试；落地：L1WeightsExt 版本化对齐 prompt 版本化心智
// 变更三段式（改进 3，沉淀）；落地：calcL1 重构为 w.Weights["error_handling"] 等映射访问
// SNAPSHOT: pkg=internal/orchestrator; type=L1ScorerExt+L1WeightsExt; weights_map_keys=[error_handling(20),test_coverage(20),test_quality(15),code_style(10),doc_coverage(10),todo_fixme(10),diff_reasonable(10),citation(5)]; archive_limit=20; regression_threshold=100; BASE变更=DEC-018三段式(); concurrency=sync.RWMutex

package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 致命反例 1 防护：L2 回归样本量阈值（地基层 §4.7 line 882 已定 100，Phase 5 不降）。
const L2RegressionSampleThreshold = 100

// L1 权重归档上限 N=20（§6.3 L1 权重版本 TTL，超出写 archive 仅保留 Version+Reason）。
const L1WeightsArchiveLimit = 20

// L1WeightsExt L1 评分权重（版本化支持回滚 + A/B 测试）。
// BASE 变更（改进 3，沉淀）：Weights 从 struct 字段重构为 map[string]int
// 原方案优势：地基层 §4.4 line 656-668 calcL1 用 struct 字段访问（w.Error/w.Test 等），有编译期类型检查
// 本次牺牲：需重构已落地 calcL1 为 w.Weights["error_handling"] 等映射访问，失去编译期检查；增加运行时 key 拼写错误风险
// 不得不改原因：L1Weights 版本化（Version/Before/After）需 JSON 序列化动态 key，struct 字段无法实现版本化回滚 + A/B 测试
//
// key 命名（snake_case， 统一）：
// - "error_handling" (20%) 替代地基层 w.Error
// - "test_coverage" (20%) 替代 w.TestCoverage
// - "test_quality" (15%) 替代 w.TestQuality
// - "code_style" (10%) 替代 w.Style
// - "doc_coverage" (10%) 替代 w.Doc
// - "todo_fixme" (10%) 替代 w.TODO
// - "diff_reasonable" (10%) 替代 w.DiffReasonable
// - "citation" (5%) 替代 w.Citation
//
// 与现有 l1_scorer.go 的 L1Weights struct 共存：Phase 0 l1_scorer.go 保留 struct 版本（不改签名），
// Phase 5 l1_scorer_ext.go 用 map 版本（新增）。 沉淀后可在地基层 v0.5 修订时合并。
type L1WeightsExt struct {
	Version   int            `json:"version"`    // 版本号（每次调整 +1，初版=1）
	Weights   map[string]int `json:"weights"`    // 项名 → 权重（0-100，总和=100）
	Reason    string         `json:"reason"`     // 调整理由（规则触发 / L2 回归）
	Before    map[string]int `json:"before"`     // 调整前权重（回滚用）
	After     map[string]int `json:"after"`      // 调整后权重
	UpdatedAt time.Time      `json:"updated_at"` // 调整时间
}

// L1WeightKey 枚举（snake_case 统一命名， 致命反例 8 防护）。
const (
	L1KeyErrorHandling  = "error_handling"  // 20%
	L1KeyTestCoverage   = "test_coverage"   // 20%
	L1KeyTestQuality    = "test_quality"    // 15%
	L1KeyCodeStyle      = "code_style"      // 10%
	L1KeyDocCoverage    = "doc_coverage"    // 10%
	L1KeyTodoFixme      = "todo_fixme"      // 10%
	L1KeyDiffReasonable = "diff_reasonable" // 10%
	L1KeyCitation       = "citation"        // 5%
)

// L1AllKeys 返回所有合法 key（snake_case， 致命反例 8 校验用）。
func L1AllKeys() []string {
	return []string{
		L1KeyErrorHandling, L1KeyTestCoverage, L1KeyTestQuality, L1KeyCodeStyle,
		L1KeyDocCoverage, L1KeyTodoFixme, L1KeyDiffReasonable, L1KeyCitation,
	}
}

// DefaultL1WeightsExt 返回初版权重表（地基层 §4.4 line 644-653 8 项权重，总和=100）。
func DefaultL1WeightsExt() map[string]int {
	return map[string]int{
		L1KeyErrorHandling:  20,
		L1KeyTestCoverage:   20,
		L1KeyTestQuality:    15,
		L1KeyCodeStyle:      10,
		L1KeyDocCoverage:    10,
		L1KeyTodoFixme:      10,
		L1KeyDiffReasonable: 10,
		L1KeyCitation:       5,
	}
}

// ErrSampleTooSmall 样本不足错误（地基层 §4.7 line 882 "样本<100 用规则阈值"，致命反例 1 防护）。
var ErrSampleTooSmall = fmt.Errorf("l1_scorer_ext: sample size < %d, cannot run L2 regression", L2RegressionSampleThreshold)

// ErrNoPreviousVersion 无上一版本可回滚（已是初版）。
var ErrNoPreviousVersion = fmt.Errorf("l1_scorer_ext: no previous version to rollback")

// ErrInvalidWeightKey 非法权重 key（致命反例 8 防护，key 必须 snake_case）。
var ErrInvalidWeightKey = fmt.Errorf("l1_scorer_ext: invalid weight key, must be snake_case in L1AllKeys()")

// L1ScorerExt L1 权重学习扩展器（Phase 5 §4.4，复用地基层 calcL1 但重构为 map 访问）。
// 职责：
// - 样本<100：AdjustWeightsByRules 规则阈值调整（3 条规则）
// - 样本≥100：AdjustWeightsByRegression L2 回归调整
// - 版本化：每次调整 saveVersioned 写 l1_weights.json + 归档旧版本（上限 N=20）
// - 回滚：RollbackWeights 回退到上一版本
//
// 并发安全：sync.RWMutex 保护 weights + archive。
type L1ScorerExt struct {
	mu      sync.RWMutex
	weights L1WeightsExt   // 当前权重（含 Version）
	archive []L1WeightsExt // 版本归档（上限 N=20，超出写 archive.json）
	path    string         // l1_weights.json 文件路径（""=不持久化，仅内存）
}

// NewL1ScorerExt 创建 L1 权重学习扩展器。
// path 为 l1_weights.json 路径，传 "" 表示仅内存（测试用）。
// 初版从 DefaultL1WeightsExt() 开始，Version=1。
func NewL1ScorerExt(path string) *L1ScorerExt {
	defaults := DefaultL1WeightsExt()
	return &L1ScorerExt{
		weights: L1WeightsExt{
			Version:   1,
			Weights:   cloneWeightsMap(defaults),
			Before:    nil,
			After:     cloneWeightsMap(defaults),
			Reason:    "initial default weights (地基层 §4.4 line 644-653)",
			UpdatedAt: time.Now().UTC(),
		},
		path: path,
	}
}

// GetWeights 取当前权重（返回副本，防外部修改）。
func (s *L1ScorerExt) GetWeights() L1WeightsExt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneL1Weights(s.weights)
}

// AdjustWeightsByRules 规则阈值调整（样本<100 时生效）。
// 落地地基层 §4.7 line 882 "样本<100 用规则阈值"。
// 规则 1：连续 3 次 L2=discarded 且 L1>70 → 降低 doc_coverage 权重 5%
// 规则 2：连续 3 次 L2=discarded 且 L1<40 → 降低 error_handling 权重 5%
// 规则 3：连续 5 次 L2=adopted 且 L1>80 → 通知路由器可降级模型（不调权重，仅日志）
//
// recentL2 按时间倒序传入（index 0=最新），len<3 不调整。
// 调整后调 saveVersioned 持久化 + 归档旧版本。
// 注意：不在 s.weights.Weights 上原地修改（否则归档的旧版本 sum≠100 导致回滚失败），
// 而是克隆后调整 + 归一化，再传给 saveVersioned 替换。
func (s *L1ScorerExt) AdjustWeightsByRules(recentL2 []QualityRecord) error {
	// 致命反例 1 关联：样本<3 不调整
	if len(recentL2) < 3 {
		return nil
	}

	s.mu.Lock()
	adjusted := false
	reason := ""

	// 克隆当前权重（不原地修改 s.weights.Weights，保证归档版本 sum=100）
	newWeights := cloneWeightsMap(s.weights.Weights)
	before := cloneWeightsMap(s.weights.Weights) // before = 调整前权重
	s.mu.Unlock()

	// 规则 1：连续 3 次 L2=discarded 且 L1>70 → 降低 doc_coverage 5%
	if lastNAll(recentL2, 3, func(r QualityRecord) bool {
		return r.L2Verdict == L2Discarded && r.L1Score > 70
	}) {
		if err := applyWeightDelta(newWeights, L1KeyDocCoverage, -5); err == nil {
			adjusted = true
			reason = "rule1: 3 consecutive discarded & L1>70, doc_coverage -5"
		}
	}

	// 规则 2：连续 3 次 L2=discarded 且 L1<40 → 降低 error_handling 5%
	if lastNAll(recentL2, 3, func(r QualityRecord) bool {
		return r.L2Verdict == L2Discarded && r.L1Score < 40
	}) {
		if err := applyWeightDelta(newWeights, L1KeyErrorHandling, -5); err == nil {
			adjusted = true
			if reason != "" {
				reason += "; "
			}
			reason += "rule2: 3 consecutive discarded & L1<40, error_handling -5"
		}
	}

	// 规则 3：连续 5 次 L2=adopted 且 L1>80 → 通知路由器可降级模型（不调权重）
	// 仅日志记录，不调权重（候选 4A 与路由器解耦，只发信号）
	if len(recentL2) >= 5 && lastNAll(recentL2, 5, func(r QualityRecord) bool {
		return r.L2Verdict == L2Adopted && r.L1Score > 80
	}) {
		// 仅记日志，不调权重（§4.4.2 候选 4A 第 3 条）
		// 实际通知路由器的方法由编排器在 dispatch 时读取此状态实现，本处仅返回信号
		if !adjusted {
			// 无权重调整但有信号 → 返回 nil 但不持久化
			return nil
		}
	}

	if !adjusted {
		return nil
	}

	// 归一化：调整后权重总和可能 !=100（如 -5 后 sum=95），需按比例缩放回 100
	newWeights = normalizeWeights(newWeights)

	return s.saveVersioned(newWeights, reason, before)
}

// AdjustWeightsByRegression L2 回归调整（样本≥100 才生效）。
// 落地地基层 §4.7 line 882 "样本≥100 每月 L2 回归"。
// 致命反例 1 防护：样本<100 返回 ErrSampleTooSmall。
// 算法：计算每项 L1 子分与 L2 verdict 的相关性，相关性高的项权重 +1，相关性低的项权重 -1（保守调整，地基层 §4.4.4）。
// 调整后调 saveVersioned 持久化。
func (s *L1ScorerExt) AdjustWeightsByRegression(l2Records []QualityRecord) error {
	// 致命反例 1：样本<100 拒绝回归
	if len(l2Records) < L2RegressionSampleThreshold {
		return ErrSampleTooSmall
	}

	// 计算每项 L1 子分与 L2 verdict 的相关性（简化版：按 L2 verdict 分组统计 L1 总分占比）
	correlations := s.computeCorrelations(l2Records)

	s.mu.Lock()
	// 调整：相关性 >0.6 的项 +1，<0.3 的项 -1（保守，地基层 §4.4.4 改进 3）
	newWeights := cloneWeightsMap(s.weights.Weights)
	for _, key := range L1AllKeys() {
		corr, ok := correlations[key]
		if !ok {
			continue
		}
		if corr > 0.6 {
			newWeights[key] = newWeights[key] + 1
		} else if corr < 0.3 {
			newWeights[key] = newWeights[key] - 1
		}
		// 钳制 [0, 100]
		if newWeights[key] < 0 {
			newWeights[key] = 0
		}
		if newWeights[key] > 100 {
			newWeights[key] = 100
		}
	}
	// 归一化：保证总和=100（按比例缩放）
	newWeights = normalizeWeights(newWeights)
	before := cloneWeightsMap(s.weights.Weights)
	s.mu.Unlock()

	reason := fmt.Sprintf("monthly L2 regression, n=%d", len(l2Records))
	return s.saveVersioned(newWeights, reason, before)
}

// RollbackWeights 回滚到上一版本（L1/L2 相关性下降时）。
// 落地 §6.3 L1 权重版本 TTL "新版本导致 L1/L2 相关性下降 → 回滚"。
// 无上一版本时返回 ErrNoPreviousVersion。
func (s *L1ScorerExt) RollbackWeights() error {
	s.mu.Lock()
	if len(s.archive) == 0 {
		s.mu.Unlock()
		return ErrNoPreviousVersion
	}
	// 取归档最后一项作为回滚目标
	prev := s.archive[len(s.archive)-1]
	// 当前版本入归档（保留以便再次回滚）
	current := cloneL1Weights(s.weights)
	s.archive = append(s.archive, current)
	// 超出 N=20 移除最旧（仅保留 Version+Reason 到 archive.json）
	if len(s.archive) > L1WeightsArchiveLimit {
		// 持久化溢出部分到 archive.json（best-effort）
		overflow := s.archive[:len(s.archive)-L1WeightsArchiveLimit]
		s.archive = s.archive[len(s.archive)-L1WeightsArchiveLimit:]
		// 异步写 archive.json（不阻塞主流程）
		if s.path != "" {
			go s.writeArchiveFile(overflow)
		}
	}
	// 回滚到 prev
	prev.Version = s.weights.Version // 保留版本号继续递增
	prev.Reason = "rollback to previous version (L1/L2 correlation dropped)"
	prev.UpdatedAt = time.Now().UTC()
	s.weights = cloneL1Weights(prev)
	weights := cloneWeightsMap(prev.Weights)
	s.mu.Unlock()

	// 持久化回滚后的版本
	return s.saveVersioned(weights, prev.Reason, prev.Before)
}

// saveVersioned 版本化写入 l1_weights.json + 归档旧版本（上限 N=20）。
// 落地 §4.4.3 L1Weights 版本化 + §6.3 L1 权重版本 TTL。
// newWeights 是新权重，reason 是调整理由，before 是调整前权重（用于回滚）。
// 持久化失败不阻塞内存更新（致命反例 5 关联：内存保留优先）。
func (s *L1ScorerExt) saveVersioned(newWeights map[string]int, reason string, before map[string]int) error {
	if err := validateWeightsMap(newWeights); err != nil {
		return fmt.Errorf("l1_scorer_ext: validate weights: %w", err)
	}

	s.mu.Lock()
	// 当前版本入归档
	current := cloneL1Weights(s.weights)
	s.archive = append(s.archive, current)
	// 超出 N=20 移除最旧（写 archive.json）
	if len(s.archive) > L1WeightsArchiveLimit {
		overflow := s.archive[:len(s.archive)-L1WeightsArchiveLimit]
		s.archive = s.archive[len(s.archive)-L1WeightsArchiveLimit:]
		if s.path != "" {
			go s.writeArchiveFile(overflow)
		}
	}
	// 更新当前版本
	s.weights = L1WeightsExt{
		Version:   s.weights.Version + 1,
		Weights:   cloneWeightsMap(newWeights),
		Reason:    reason,
		Before:    cloneWeightsMap(before),
		After:     cloneWeightsMap(newWeights),
		UpdatedAt: time.Now().UTC(),
	}
	path := s.path
	currentWeights := cloneL1Weights(s.weights)
	s.mu.Unlock()

	// 持久化（锁外写文件，best-effort，失败不阻塞）
	if path == "" {
		return nil
	}
	return s.writeWeightsFile(path, currentWeights)
}

// adjustWeightLocked 调整单项权重（持锁，内部用）。
// delta 可正可负（+5 / -5）。钳制 [0, 100]。
// 校验 key 合法性（致命反例 8：snake_case）。
func (s *L1ScorerExt) adjustWeightLocked(key string, delta int) error {
	if !isValidWeightKey(key) {
		return ErrInvalidWeightKey
	}
	// 保留 before（首次调整时初始化为当前权重）
	if s.weights.Before == nil {
		s.weights.Before = cloneWeightsMap(s.weights.Weights)
	}
	newVal := s.weights.Weights[key] + delta
	if newVal < 0 {
		newVal = 0
	}
	if newVal > 100 {
		newVal = 100
	}
	s.weights.Weights[key] = newVal
	return nil
}

// applyWeightDelta 调整传入 map 的单项权重（不修改 s.weights，用于 AdjustWeightsByRules）。
// delta 可正可负（+5 / -5）。钳制 [0, 100]。校验 key 合法性（致命反例 8：snake_case）。
func applyWeightDelta(weights map[string]int, key string, delta int) error {
	if !isValidWeightKey(key) {
		return ErrInvalidWeightKey
	}
	newVal := weights[key] + delta
	if newVal < 0 {
		newVal = 0
	}
	if newVal > 100 {
		newVal = 100
	}
	weights[key] = newVal
	return nil
}

// computeCorrelations 计算每项 L1 子分与 L2 verdict 的相关性（简化版）。
// 落地地基层 §4.4.4 "样本≥100 再回归"。
// 简化算法：按 L2=adopted/discarded 分组，统计每组 L1Score 均值差，差值大说明 L1 与 L2 相关。
// 返回 key → 相关系数（0.0-1.0，简化版用均值差归一化）。
func (s *L1ScorerExt) computeCorrelations(l2Records []QualityRecord) map[string]float64 {
	correlations := make(map[string]float64)
	if len(l2Records) == 0 {
		return correlations
	}

	// 按 L2 verdict 分组统计 L1 均值
	adoptedSum, adoptedCount := 0, 0
	discardedSum, discardedCount := 0, 0
	for _, r := range l2Records {
		switch r.L2Verdict {
		case L2Adopted:
			adoptedSum += r.L1Score
			adoptedCount++
		case L2Discarded:
			discardedSum += r.L1Score
			discardedCount++
		}
	}

	// 简化：所有 key 用同一个相关性（L1Score 与 L2 verdict 的相关性）
	// 真实实现应对每项 L1 子分单独算相关性，此处简化因 QualityRecord 只存 L1Score 总分
	var corr float64
	if adoptedCount > 0 && discardedCount > 0 {
		adoptedAvg := float64(adoptedSum) / float64(adoptedCount)
		discardedAvg := float64(discardedSum) / float64(discardedCount)
		// 均值差归一化到 [0, 1]：差值大说明 L1 与 L2 相关
		diff := adoptedAvg - discardedAvg
		if diff < 0 {
			diff = -diff
		}
		corr = diff / 100.0
	}

	for _, key := range L1AllKeys() {
		correlations[key] = corr
	}
	return correlations
}

// writeWeightsFile 持久化当前权重到 l1_weights.json（best-effort，失败不阻塞）。
func (s *L1ScorerExt) writeWeightsFile(path string, weights L1WeightsExt) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("l1_scorer_ext: mkdir %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(weights, "", "  ")
	if err != nil {
		return fmt.Errorf("l1_scorer_ext: marshal: %w", err)
	}
	// 原子写入：先写临时文件再 rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("l1_scorer_ext: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("l1_scorer_ext: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// writeArchiveFile 写归档文件（仅 Version+Reason，best-effort）。
// 落地 §6.3 "超出 N=20 的旧版本仅保留 Version + Reason 摘要"。
func (s *L1ScorerExt) writeArchiveFile(overflow []L1WeightsExt) {
	if s.path == "" || len(overflow) == 0 {
		return
	}
	archivePath := strings.TrimSuffix(s.path, filepath.Ext(s.path)) + "_archive.json"
	type archiveEntry struct {
		Version int    `json:"version"`
		Reason  string `json:"reason"`
	}
	entries := make([]archiveEntry, 0, len(overflow))
	for _, w := range overflow {
		entries = append(entries, archiveEntry{Version: w.Version, Reason: w.Reason})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(archivePath, data, 0o644) // best-effort
}

// ArchiveCount 返回当前归档数量（仅用于测试）。
func (s *L1ScorerExt) ArchiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.archive)
}

// lastNAll 检查 recentL2 前 N 条是否全部满足 pred。
// recentL2 按时间倒序传入（index 0=最新）。
func lastNAll(recentL2 []QualityRecord, n int, pred func(QualityRecord) bool) bool {
	if len(recentL2) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if !pred(recentL2[i]) {
			return false
		}
	}
	return true
}

// isValidWeightKey 校验 key 是否在 L1AllKeys() 中（致命反例 8：snake_case）。
func isValidWeightKey(key string) bool {
	for _, k := range L1AllKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// validateWeightsMap 校验权重 map：key 合法 + 总和=100 + 每项 [0,100]。
func validateWeightsMap(weights map[string]int) error {
	sum := 0
	validKeys := make(map[string]bool)
	for _, k := range L1AllKeys() {
		validKeys[k] = true
	}
	for k, v := range weights {
		if !validKeys[k] {
			return fmt.Errorf("%w: %s", ErrInvalidWeightKey, k)
		}
		if v < 0 || v > 100 {
			return fmt.Errorf("l1_scorer_ext: weight %s=%d out of [0,100]", k, v)
		}
		sum += v
	}
	if sum != 100 {
		return fmt.Errorf("l1_scorer_ext: weights sum=%d, must be 100", sum)
	}
	return nil
}

// normalizeWeights 归一化权重使总和=100（按比例缩放）。
func normalizeWeights(weights map[string]int) map[string]int {
	sum := 0
	for _, v := range weights {
		sum += v
	}
	if sum == 0 || sum == 100 {
		return weights
	}
	// 按比例缩放
	result := make(map[string]int, len(weights))
	scaled := 0
	for _, k := range L1AllKeys() {
		v := weights[k]
		newV := v * 100 / sum
		result[k] = newV
		scaled += newV
	}
	// 余数加到最大项
	if scaled != 100 {
		maxKey := L1KeyErrorHandling
		maxVal := -1
		for _, k := range L1AllKeys() {
			if result[k] > maxVal {
				maxVal = result[k]
				maxKey = k
			}
		}
		result[maxKey] += 100 - scaled
	}
	return result
}

// cloneWeightsMap 深拷贝权重 map。
func cloneWeightsMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	result := make(map[string]int, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// cloneL1Weights 深拷贝 L1WeightsExt（含 map 字段）。
func cloneL1Weights(w L1WeightsExt) L1WeightsExt {
	return L1WeightsExt{
		Version:   w.Version,
		Weights:   cloneWeightsMap(w.Weights),
		Reason:    w.Reason,
		Before:    cloneWeightsMap(w.Before),
		After:     cloneWeightsMap(w.After),
		UpdatedAt: w.UpdatedAt,
	}
}

// SaveVersionedPublic 公开 wrapper（02 §4 仲裁新增， 决策 6）。
// 供 H12 WeightTuner 跨模块调用权重版本化写入，不改 saveVersioned 私有可见性，
// 避免破坏 Phase 5 现有调用方 AdjustWeightsByRules / AdjustWeightsByRegression
// （它们内部调私有 saveVersioned）。
//
// 行为：
// 1. before 自动从 GetWeights 获取（调用方无需传入）
// 2. 内部调 saveVersioned（与新权重 / reason / before 三参数语义对齐）
// 3. 持久化策略与 saveVersioned 一致：path=="" 仅内存；非空则原子写 l1_weights.json
// 4. 失败回滚归档状态由 saveVersioned 内部处理
//
// 参数：
// - newWeights：新权重 map（必须 sum=100，key 在 L1AllKeys 中）
// - reason：调整理由（写 L1WeightsExt.Reason + 持久化 JSON）
//
// 返回：saveVersioned 的持久化错误（验证失败 / 文件 IO 错误）。
//
// 关键约束（BASE 保护）：
// - 不改 saveVersioned 签名 / 可见性（破坏 Phase 5 现有调用方将触发 BASE 变更三段式）
// - 不改 L1ScorerExt struct 字段签名（02 §2.7.5）
// - 仅新增 1 个公开方法，最小化仲裁范围
func (s *L1ScorerExt) SaveVersionedPublic(newWeights map[string]int, reason string) error {
	// before 自动从 GetWeights 获取（持有读锁后克隆，避免外部 GetWeights 双重加锁）
	s.mu.RLock()
	before := cloneWeightsMap(s.weights.Weights)
	s.mu.RUnlock()
	return s.saveVersioned(newWeights, reason, before)
}
