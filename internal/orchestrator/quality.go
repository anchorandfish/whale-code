// Package orchestrator 实现 H10 智能编排系统：质量旁路记账 / 三层评分 / 模型画像 / 能力降级 / 反偷懒 linter / 基线 checklist。
// 本包是 H10 Phase 0 地基层，后续 Phase 1-5 在此基础上构建状态机/路由/经验库/并行调度/成本治理。
package orchestrator

import "github.com/anchorandfish/whale-code/internal/orchrecord"

// quality.go — 桶 1 QualityRecord + MetricsCorrelation struct 定义（地基层 §3.4 / 02 §2.7.3 v1.1）。
//
// ★ 类型与枚举权威已下沉到 internal/orchrecord：本文件只剩类型别名
// 与常量转发，保留 orchestrator 内部既有消费点的源码兼容，不另起一份定义。

// L2Verdict 枚举常量（地基层 §4.5，02 §2.7.3 v1.1）。
const (
	L2Adopted   = orchrecord.L2Adopted   // 直接采纳（改 0%）
	L2Modified  = orchrecord.L2Modified  // 小改/大改（改 10%/35%）
	L2Discarded = orchrecord.L2Discarded // 弃用重写（改 80%）
)

// L2Reason 枚举：弃用原因标签 a-e（地基层 §4.5）。
const (
	L2ReasonRequirement = orchrecord.L2ReasonRequirement // 需求理解错
	L2ReasonStyle       = orchrecord.L2ReasonStyle       // 代码风格差
	L2ReasonPerformance = orchrecord.L2ReasonPerformance // 性能问题
	L2ReasonSecurity    = orchrecord.L2ReasonSecurity    // 安全问题
	L2ReasonOther       = orchrecord.L2ReasonOther       // 其他
)

// TaskType 枚举：子任务类型（地基层 §3.4，02 §2.7.3）。
const (
	TaskTypeBugFix    = orchrecord.TaskTypeBugFix
	TaskTypeDocGen    = orchrecord.TaskTypeDocGen
	TaskTypePlan      = orchrecord.TaskTypePlan
	TaskTypeReview    = orchrecord.TaskTypeReview
	TaskTypeImplement = orchrecord.TaskTypeImplement
)

// Complexity 枚举：任务复杂度 T1/T2/T3（地基层 §3.4）。
const (
	ComplexityT1 = orchrecord.ComplexityT1 // 简单（单文件小改）
	ComplexityT2 = orchrecord.ComplexityT2 // 中等（多文件协同）
	ComplexityT3 = orchrecord.ComplexityT3 // 复杂（跨包重构）
)

// ErrorType 枚举：子任务失败错误类型（地基层 §3.4）。
const (
	ErrorTypeCompileError      = orchrecord.ErrorTypeCompileError
	ErrorTypeTestFailure       = orchrecord.ErrorTypeTestFailure
	ErrorTypeContractViolation = orchrecord.ErrorTypeContractViolation
	ErrorTypeTimeout           = orchrecord.ErrorTypeTimeout
	ErrorTypeNone              = orchrecord.ErrorTypeNone
)

// QualityRecord 质量记录（格式契约权威在 internal/orchrecord，）。
type QualityRecord = orchrecord.QualityRecord

// MetricsCorrelation JOIN CostRecord 的关联键（格式契约权威在 internal/orchrecord，）。
type MetricsCorrelation = orchrecord.MetricsCorrelation
