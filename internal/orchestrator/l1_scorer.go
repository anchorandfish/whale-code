// l1_scorer.go — 桶 2 L1 代码质量评分器（linter 自动打分 0-100，地基层 §4.4）。
// REF-外部-Google Agent Quality 白皮书@https://www.kaggle.com/whitepaper-agent-quality+2026-07-15访问-L1 代码质量评估
// 8 项权重；落地：Phase 0 跑 7 项（DiffReasonable=0），Phase 1 编排器落地后补入
// SNAPSHOT: L1=int(0-100) 7项Phase0(DiffReasonable=0); weights={Error:22,TestCoverage:22,TestQuality:17,Style:11,Doc:11,TODO:11,Citation:6,DiffReasonable:0}; learning_phase1=<100样本不回归; learning_phase2=>=100样本线性回归

package orchestrator

import (
	"fmt"
	"strings"
)

// L1Scorer 代码质量评分器，linter 自动打分 0-100（地基层 §4.4）。
// Phase 0 跑 7 项（diff 合理性项权重归零，Phase 1 编排器落地后补入）。
type L1Scorer struct {
	weights L1Weights
	// 各项打分函数可注入（便于测试 mock），nil 时用默认实现
	scoreFns L1ScoreFns
}

// L1Weights L1 评分 8 项权重（地基层 §4.4 表）。
// Phase 0：DiffReasonable=0，其余 7 项按原比例放大（22/22/17/11/11/11/6）。
// 阶段 1（< 100 样本）：用初版权重表（不回归，防过拟合）。
// 阶段 2（≥ 100 样本）：每月线性回归调整权重，写入 .whale/orchestrator/l1_weights.json。
type L1Weights struct {
	Error          float64 // 22%（Phase 0）— errcheck+errorlint+nilerr 通过率
	TestCoverage   float64 // 22%（Phase 0）— 双指标：① 有无 error path ② go test -cover
	TestQuality    float64 // 17%（Phase 0）— whale-lint analyzer（table-driven 占比 + race 测试）
	Style          float64 // 11%（Phase 0）— revive warning 数量
	Doc            float64 // 11%（Phase 0）— whale-lint analyzer（公开符号 godoc 覆盖率）
	TODO           float64 // 11%（Phase 0）— whale-lint analyzer（交付代码 TODO 数，0=满分）
	Citation       float64 // 6%（Phase 0）— whale-lint analyzer（注释中 file:line 比例）
	DiffReasonable float64 // 0%（Phase 0 归零，Phase 1 补入恢复 10%）
}

// L1ScoreFns 各项打分函数集合（每项返回 0.0-1.0 的得分比例，便于测试 mock）。
type L1ScoreFns struct {
	Error          func(task SubTask) float64
	TestCoverage   func(task SubTask) float64
	TestQuality    func(task SubTask) float64
	Style          func(task SubTask) float64
	Doc            func(task SubTask) float64
	TODO           func(task SubTask) float64
	Citation       func(task SubTask) float64
	DiffReasonable func(task SubTask) float64
}

// DefaultL1Weights Phase 0 默认权重表（地基层 §4.4，DiffReasonable 归零，其余按原比例放大）。
func DefaultL1Weights() L1Weights {
	return L1Weights{
		Error:          0.22,
		TestCoverage:   0.22,
		TestQuality:    0.17,
		Style:          0.11,
		Doc:            0.11,
		TODO:           0.11,
		Citation:       0.06,
		DiffReasonable: 0.0, // Phase 0 归零
	}
}

// NewL1Scorer 创建 L1 评分器，使用 Phase 0 默认权重表 + 默认打分函数。
func NewL1Scorer() *L1Scorer {
	return &L1Scorer{
		weights:  DefaultL1Weights(),
		scoreFns: defaultL1ScoreFns(),
	}
}

// Calc 计算子任务 L1 分数（0-100，地基层 §4.4）。
// 致命反例防护：结果越界（<0 或 >100）必须返回 error，禁止返回越界值。
func (s *L1Scorer) Calc(task SubTask) (int, error) {
	w := s.weights
	fns := s.scoreFns

	// 各项得分比例（0.0-1.0）× 权重 → 加权总分（0.0-1.0）
	var total float64
	if fns.Error != nil {
		total += fns.Error(task) * w.Error
	}
	if fns.TestCoverage != nil {
		total += fns.TestCoverage(task) * w.TestCoverage
	}
	if fns.TestQuality != nil {
		total += fns.TestQuality(task) * w.TestQuality
	}
	if fns.Style != nil {
		total += fns.Style(task) * w.Style
	}
	if fns.Doc != nil {
		total += fns.Doc(task) * w.Doc
	}
	if fns.TODO != nil {
		total += fns.TODO(task) * w.TODO
	}
	if fns.Citation != nil {
		total += fns.Citation(task) * w.Citation
	}
	if fns.DiffReasonable != nil {
		total += fns.DiffReasonable(task) * w.DiffReasonable
	}

	// 转为 0-100 整数
	score := int(total * 100)

	// 致命反例防护：结果必须 ∈ [0,100]
	if score < 0 || score > 100 {
		return 0, fmt.Errorf("L1 score out of bounds [0,100]: got %d (total=%f)", score, total)
	}
	return score, nil
}

// testCoverageScore 双指标测试覆盖（地基层 §4.4 修正 v0.1 逻辑漏洞）。
// ① 是否有 error path（assert.Error/require.Error，0/1）② go test -cover 覆盖率。
// 无 error path 时 coverRate * 0.5（打折，防偷懒只写 happy path）。
func (s *L1Scorer) testCoverageScore(task SubTask) float64 {
	return defaultTestCoverageScore(task)
}

// defaultTestCoverageScore 默认测试覆盖打分函数（testCoverageScore 的默认实现）。
// 双指标：① 有 error path 得 1.0，否则 0.0；② go test -cover 覆盖率（0.0-1.0）。
// 合成：coverRate * (hasErrorPath ? 1.0 : 0.5) — 无 error path 打 5 折。
func defaultTestCoverageScore(task SubTask) float64 {
	coverRate := getCoverRate(task)      // 0.0-1.0
	hasErrorPath := checkErrorPath(task) // bool
	if hasErrorPath {
		return coverRate
	}
	return coverRate * 0.5 // 无 error path 打 5 折（地基层 §4.4 防偷懒）
}

// defaultL1ScoreFns 默认 8 项打分函数（Phase 0 简化版，全部返回固定中位值，Phase 1 接入真实 linter）。
// 注意：Phase 0 这些是降级实现，仅用于打通评分流程；Phase 1 必须接入 whale-lint analyzer。
func defaultL1ScoreFns() L1ScoreFns {
	return L1ScoreFns{
		Error:          func(task SubTask) float64 { return 0.8 }, // 默认 80% 通过率
		TestCoverage:   defaultTestCoverageScore,
		TestQuality:    func(task SubTask) float64 { return 0.7 },
		Style:          func(task SubTask) float64 { return 0.9 },
		Doc:            func(task SubTask) float64 { return 0.6 },
		TODO:           func(task SubTask) float64 { return 1.0 }, // 0 TODO = 满分
		Citation:       func(task SubTask) float64 { return 0.5 },
		DiffReasonable: func(task SubTask) float64 { return 0.0 }, // Phase 0 归零
	}
}

// getCoverRate 获取子任务的 go test -cover 覆盖率（0.0-1.0，Phase 0 占位返回 0.5）。
// Phase 1 接入真实 go test -cover 解析。
func getCoverRate(task SubTask) float64 {
	if len(task.TestFiles) == 0 {
		return 0.0 // 无测试文件 → 覆盖率 0
	}
	return 0.5 // Phase 0 占位
}

// checkErrorPath 检查子任务测试文件中是否有 error path（assert.Error/require.Error）。
// 返回 true=有 error path，false=无（仅 happy path，会被 testCoverageScore 打 5 折）。
func checkErrorPath(task SubTask) bool {
	if len(task.TestFiles) == 0 {
		return false
	}
	// Phase 0 简化：扫描 TestFiles 路径列表中是否含 "error" 关键字（避免读文件 I/O）。
	// Phase 1 接入 whale-lint analyzer 做完整 AST 扫描。
	for _, f := range task.TestFiles {
		if strings.Contains(strings.ToLower(f), "error") {
			return true
		}
	}
	return false
}
