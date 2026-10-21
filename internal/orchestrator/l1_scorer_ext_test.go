// l1_scorer_ext_test.go — H10 Phase 5 桶 1 L1 权重学习扩展器反例测试（TDD 先行）。
// 覆盖致命反例：
// - 致命反例 1：L1 权重学习小样本误判（样本<100 用规则，≥100 才回归）
// - 致命反例 8：L1Weights map key snake_case 一致性
// line 882-样本<100 用规则阈值，≥100 才回归；落地：AdjustWeightsByRegression 致命反例 1
// 格式从 struct → map[string]int；落地：TestL1WeightsExt_MapKeySnakeCase_Consistent
// SNAPSHOT: tests={AdjustWeightsByRules_SampleLessThan3, ThreeDiscardedHighL1_LowerDocWeight, AdjustWeightsByRegression_SampleLessThan100_Reject(致命1), Sample100_Adjust, RollbackWeights_RestoresPreviousVersion, MapKeySnakeCase_Consistent(致命8), SaveVersioned_ArchiveLimit20, ConcurrentAdjust_NoRace}

package orchestrator

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeL2Record 构造测试用 QualityRecord（L2 评分完成）。
func makeL2Record(taskID, l2Verdict string, l1Score int, ts time.Time) QualityRecord {
	return QualityRecord{
		TaskID:      taskID,
		TaskType:    TaskTypeImplement,
		Model:       "claude-sonnet",
		L0Pass:      true,
		L1Score:     l1Score,
		L2Verdict:   l2Verdict,
		DurationSec: 100,
		RetryCount:  0,
		Timestamp:   ts.UTC().Format(time.RFC3339),
	}
}

// TestAdjustWeightsByRules_SampleLessThan3_NoAdjust 样本<3 不调整（候选 4A 第 1 条）。
func TestAdjustWeightsByRules_SampleLessThan3_NoAdjust(t *testing.T) {
	s := NewL1ScorerExt("")
	before := s.GetWeights()

	// 2 条记录，< 3 不调整
	recentL2 := []QualityRecord{
		makeL2Record("t1", L2Discarded, 75, time.Now()),
		makeL2Record("t2", L2Discarded, 75, time.Now()),
	}
	if err := s.AdjustWeightsByRules(recentL2); err != nil {
		t.Fatalf("AdjustWeightsByRules: %v", err)
	}

	after := s.GetWeights()
	if after.Version != before.Version {
		t.Errorf("样本<3 不应调整版本，before=%d after=%d", before.Version, after.Version)
	}
}

// TestAdjustWeightsByRules_ThreeDiscardedHighL1_LowerDocWeight 3 次 L2=discarded + L1>70 → doc_coverage -5%。
func TestAdjustWeightsByRules_ThreeDiscardedHighL1_LowerDocWeight(t *testing.T) {
	s := NewL1ScorerExt("")
	before := s.GetWeights()
	beforeDoc := before.Weights[L1KeyDocCoverage]

	// 3 次 L2=discarded + L1=75 → 规则 1 触发
	recentL2 := []QualityRecord{
		makeL2Record("t1", L2Discarded, 75, time.Now()),
		makeL2Record("t2", L2Discarded, 75, time.Now()),
		makeL2Record("t3", L2Discarded, 75, time.Now()),
	}
	if err := s.AdjustWeightsByRules(recentL2); err != nil {
		t.Fatalf("AdjustWeightsByRules: %v", err)
	}

	after := s.GetWeights()
	afterDoc := after.Weights[L1KeyDocCoverage]
	if afterDoc != beforeDoc-5 {
		t.Errorf("规则 1：doc_coverage 应 -5，before=%d after=%d", beforeDoc, afterDoc)
	}
	if after.Version != before.Version+1 {
		t.Errorf("调整后 Version 应 +1，before=%d after=%d", before.Version, after.Version)
	}
	if after.Reason == "" {
		t.Error("调整后 Reason 非空")
	}
	if !strings.Contains(after.Reason, "rule1") {
		t.Errorf("Reason 应含 rule1，got=%s", after.Reason)
	}
}

// TestAdjustWeightsByRules_ThreeDiscardedLowL1_LowerErrorWeight 3 次 L2=discarded + L1<40 → error_handling -5%。
func TestAdjustWeightsByRules_ThreeDiscardedLowL1_LowerErrorWeight(t *testing.T) {
	s := NewL1ScorerExt("")
	before := s.GetWeights()
	beforeErr := before.Weights[L1KeyErrorHandling]

	recentL2 := []QualityRecord{
		makeL2Record("t1", L2Discarded, 30, time.Now()),
		makeL2Record("t2", L2Discarded, 30, time.Now()),
		makeL2Record("t3", L2Discarded, 30, time.Now()),
	}
	if err := s.AdjustWeightsByRules(recentL2); err != nil {
		t.Fatalf("AdjustWeightsByRules: %v", err)
	}

	after := s.GetWeights()
	afterErr := after.Weights[L1KeyErrorHandling]
	if afterErr != beforeErr-5 {
		t.Errorf("规则 2：error_handling 应 -5，before=%d after=%d", beforeErr, afterErr)
	}
}

// TestAdjustWeightsByRegression_SampleLessThan100_Reject 致命反例 1：99 条样本 → ErrSampleTooSmall。
// 零容忍：样本<100 拒绝回归（地基层 §4.7 line 882）。
func TestAdjustWeightsByRegression_SampleLessThan100_Reject(t *testing.T) {
	s := NewL1ScorerExt("")

	// 99 条样本 < 100 → 必须拒绝（致命反例 1）
	records := make([]QualityRecord, 99)
	for i := range records {
		records[i] = makeL2Record("t", L2Adopted, 80, time.Now())
	}

	err := s.AdjustWeightsByRegression(records)
	if err == nil {
		t.Fatal("致命反例 1：样本<100 必须返回 ErrSampleTooSmall，零容忍")
	}
	if err != ErrSampleTooSmall {
		t.Errorf("应返回 ErrSampleTooSmall，got=%v", err)
	}

	// 验证权重未被修改
	after := s.GetWeights()
	defaults := DefaultL1WeightsExt()
	if !reflect.DeepEqual(after.Weights, defaults) {
		t.Error("致命反例 1：样本<100 时权重不应被修改")
	}
}

// TestAdjustWeightsByRegression_Sample100_Adjust 100 条样本 → 调整 + 版本 +1。
func TestAdjustWeightsByRegression_Sample100_Adjust(t *testing.T) {
	s := NewL1ScorerExt("")
	before := s.GetWeights()

	// 100 条样本，构造 L1 与 L2 相关的数据：adopted L1 高，discarded L1 低
	records := make([]QualityRecord, 100)
	for i := range records {
		if i < 60 {
			records[i] = makeL2Record("t", L2Adopted, 85, time.Now())
		} else {
			records[i] = makeL2Record("t", L2Discarded, 30, time.Now())
		}
	}

	if err := s.AdjustWeightsByRegression(records); err != nil {
		t.Fatalf("AdjustWeightsByRegression 100 样本: %v", err)
	}

	after := s.GetWeights()
	if after.Version != before.Version+1 {
		t.Errorf("100 样本回归后 Version 应 +1，before=%d after=%d", before.Version, after.Version)
	}
	if after.Reason == "" {
		t.Error("回归后 Reason 非空")
	}
	if !strings.Contains(after.Reason, "regression") {
		t.Errorf("Reason 应含 regression，got=%s", after.Reason)
	}
}

// TestRollbackWeights_RestoresPreviousVersion 回滚后 Version -1（恢复上一版本权重）。
func TestRollbackWeights_RestoresPreviousVersion(t *testing.T) {
	s := NewL1ScorerExt("")
	original := s.GetWeights()
	originalWeights := cloneWeightsMap(original.Weights)

	// 先做一次规则调整
	recentL2 := []QualityRecord{
		makeL2Record("t1", L2Discarded, 75, time.Now()),
		makeL2Record("t2", L2Discarded, 75, time.Now()),
		makeL2Record("t3", L2Discarded, 75, time.Now()),
	}
	if err := s.AdjustWeightsByRules(recentL2); err != nil {
		t.Fatalf("AdjustWeightsByRules: %v", err)
	}

	adjusted := s.GetWeights()
	if adjusted.Version != original.Version+1 {
		t.Fatalf("调整后 Version 应 +1，got=%d", adjusted.Version)
	}

	// 回滚
	if err := s.RollbackWeights(); err != nil {
		t.Fatalf("RollbackWeights: %v", err)
	}

	rolled := s.GetWeights()
	// 回滚后 doc_coverage 应恢复原值
	if rolled.Weights[L1KeyDocCoverage] != originalWeights[L1KeyDocCoverage] {
		t.Errorf("回滚后 doc_coverage 应恢复，original=%d rolled=%d",
			originalWeights[L1KeyDocCoverage], rolled.Weights[L1KeyDocCoverage])
	}
}

// TestRollbackWeights_NoPreviousVersion_Error 无上一版本时返回 ErrNoPreviousVersion。
func TestRollbackWeights_NoPreviousVersion_Error(t *testing.T) {
	s := NewL1ScorerExt("")

	err := s.RollbackWeights()
	if err == nil {
		t.Fatal("无上一版本应返回 ErrNoPreviousVersion")
	}
	if err != ErrNoPreviousVersion {
		t.Errorf("应返回 ErrNoPreviousVersion，got=%v", err)
	}
}

// TestL1WeightsExt_MapKeySnakeCase_Consistent 致命反例 8：L1Weights map key 全 snake_case。
// 零容忍：key 命名必须统一 snake_case，无驼峰/连字符/空格。
func TestL1WeightsExt_MapKeySnakeCase_Consistent(t *testing.T) {
	// 1. 校验 L1AllKeys() 全部 snake_case
	for _, key := range L1AllKeys() {
		if !isValidSnakeCase(key) {
			t.Errorf("致命反例 8：key %q 不是 snake_case", key)
		}
	}

	// 2. 校验 DefaultL1WeightsExt() 的 key 全部在 L1AllKeys() 中
	defaults := DefaultL1WeightsExt()
	for key := range defaults {
		if !isValidWeightKey(key) {
			t.Errorf("致命反例 8：default key %q 不在 L1AllKeys()", key)
		}
	}

	// 3. 校验 L1ScorerExt.GetWeights() 的 key 全部 snake_case
	s := NewL1ScorerExt("")
	weights := s.GetWeights()
	for key := range weights.Weights {
		if !isValidSnakeCase(key) {
			t.Errorf("致命反例 8：weights key %q 不是 snake_case", key)
		}
	}

	// 4. 校验 key 总数为 8（地基层 §4.4 8 项权重）
	if len(L1AllKeys()) != 8 {
		t.Errorf("L1AllKeys 应有 8 项，got=%d", len(L1AllKeys()))
	}

	// 5. 校验默认权重总和=100
	sum := 0
	for _, v := range defaults {
		sum += v
	}
	if sum != 100 {
		t.Errorf("默认权重总和应=100，got=%d", sum)
	}
}

// isValidSnakeCase 校验字符串是否 snake_case（全小写 + 下划线分隔，无空格/连字符/大写）。
func isValidSnakeCase(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return false // 不允许大写
		}
		if r == ' ' || r == '-' {
			return false // 不允许空格/连字符
		}
		// 允许小写字母、数字、下划线
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' {
			return false
		}
	}
	return true
}

// TestSaveVersioned_ArchiveLimit20 21 版本时最旧版本移入 archive（§6.3）。
func TestSaveVersioned_ArchiveLimit20(t *testing.T) {
	s := NewL1ScorerExt("")

	// 做 21 次调整（每次 doc_coverage -5，但钳制 [0,100]，所以会触发归档）
	for i := 0; i < 21; i++ {
		recentL2 := []QualityRecord{
			makeL2Record("t", L2Discarded, 75, time.Now()),
			makeL2Record("t", L2Discarded, 75, time.Now()),
			makeL2Record("t", L2Discarded, 75, time.Now()),
		}
		if err := s.AdjustWeightsByRules(recentL2); err != nil {
			t.Fatalf("第 %d 次调整: %v", i+1, err)
		}
	}

	// 归档数应 ≤ L1WeightsArchiveLimit=20
	if s.ArchiveCount() > L1WeightsArchiveLimit {
		t.Errorf("归档数应 ≤ %d，got=%d", L1WeightsArchiveLimit, s.ArchiveCount())
	}
	if s.ArchiveCount() == 0 {
		t.Error("21 次调整后归档数应 > 0")
	}
}

// TestL1ScorerExt_PersistToFile 持久化到文件 + 加载验证。
func TestL1ScorerExt_PersistToFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "l1_weights.json")

	s := NewL1ScorerExt(path)
	original := s.GetWeights()

	// 做一次调整触发持久化
	recentL2 := []QualityRecord{
		makeL2Record("t", L2Discarded, 75, time.Now()),
		makeL2Record("t", L2Discarded, 75, time.Now()),
		makeL2Record("t", L2Discarded, 75, time.Now()),
	}
	if err := s.AdjustWeightsByRules(recentL2); err != nil {
		t.Fatalf("AdjustWeightsByRules: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("l1_weights.json 文件应存在")
	}

	// 验证文件内容可解析
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "\"version\"") {
		t.Error("文件应含 version 字段")
	}
	if !strings.Contains(string(data), L1KeyDocCoverage) {
		t.Error("文件应含 doc_coverage key")
	}

	// 验证 original.Version < 调整后 Version
	adjusted := s.GetWeights()
	if adjusted.Version <= original.Version {
		t.Errorf("调整后 Version 应 > original，original=%d adjusted=%d", original.Version, adjusted.Version)
	}
}

// TestL1ScorerExt_ConcurrentAdjust_NoRace 并发调整无竞态（-race 验证）。
func TestL1ScorerExt_ConcurrentAdjust_NoRace(t *testing.T) {
	s := NewL1ScorerExt("")

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)

	// N 个 goroutine 同时 AdjustWeightsByRules
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			recentL2 := []QualityRecord{
				makeL2Record("t", L2Discarded, 75, time.Now()),
				makeL2Record("t", L2Discarded, 75, time.Now()),
				makeL2Record("t", L2Discarded, 75, time.Now()),
			}
			_ = s.AdjustWeightsByRules(recentL2)
		}()
	}
	wg.Wait()

	// 验证最终权重总和仍=100
	w := s.GetWeights()
	sum := 0
	for _, v := range w.Weights {
		sum += v
	}
	if sum != 100 {
		t.Errorf("并发后权重总和应=100，got=%d", sum)
	}
}

// TestL1ScorerExt_GetWeights_ReturnsCopy GetWeights 返回副本，外部修改不影响内部。
func TestL1ScorerExt_GetWeights_ReturnsCopy(t *testing.T) {
	s := NewL1ScorerExt("")
	w1 := s.GetWeights()
	w1.Weights[L1KeyErrorHandling] = 999 // 修改副本

	w2 := s.GetWeights()
	if w2.Weights[L1KeyErrorHandling] == 999 {
		t.Error("GetWeights 应返回副本，外部修改不应影响内部")
	}
}

// TestL1ScorerExt_ValidateWeightsMap_InvalidKey 校验非法 key 被拒绝。
func TestL1ScorerExt_ValidateWeightsMap_InvalidKey(t *testing.T) {
	// 含非法 key（驼峰）
	weights := map[string]int{
		L1KeyErrorHandling: 20,
		"ErrorHandling":    20, // 非法驼峰
	}
	if err := validateWeightsMap(weights); err == nil {
		t.Error("validateWeightsMap 应拒绝非法 key（驼峰）")
	}
}

// TestL1ScorerExt_ValidateWeightsMap_Sum100 校验总和≠100 被拒绝。
func TestL1ScorerExt_ValidateWeightsMap_Sum100(t *testing.T) {
	weights := map[string]int{
		L1KeyErrorHandling:  20,
		L1KeyTestCoverage:   20,
		L1KeyTestQuality:    15,
		L1KeyCodeStyle:      10,
		L1KeyDocCoverage:    10,
		L1KeyTodoFixme:      10,
		L1KeyDiffReasonable: 10,
		L1KeyCitation:       10, // 总和=105 ≠ 100
	}
	if err := validateWeightsMap(weights); err == nil {
		t.Error("validateWeightsMap 应拒绝总和≠100 的权重")
	}
}
