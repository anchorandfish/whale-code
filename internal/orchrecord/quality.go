package orchrecord

// quality.go — 桶 1 QualityRecord + MetricsCorrelation struct 定义（地基层 §3.4 / 02 §2.7.3 v1.1）。
//
// 本文件是 quality.jsonl 落盘格式契约的权威承载者（见包注释）。
// json tag 即落盘格式权威，任何字段/语义修改只允许在本文件发生。

// L2Verdict 枚举常量（地基层 §4.5，02 §2.7.3 v1.1）。
const (
	L2Adopted   = "adopted"   // 直接采纳（改 0%）
	L2Modified  = "modified"  // 小改/大改（改 10%/35%）
	L2Discarded = "discarded" // 弃用重写（改 80%）
)

// L2Reason 枚举：弃用原因标签 a-e（地基层 §4.5）。
const (
	L2ReasonRequirement = "a" // 需求理解错
	L2ReasonStyle       = "b" // 代码风格差
	L2ReasonPerformance = "c" // 性能问题
	L2ReasonSecurity    = "d" // 安全问题
	L2ReasonOther       = "e" // 其他
)

// TaskType 枚举：子任务类型（地基层 §3.4，02 §2.7.3）。
const (
	TaskTypeBugFix    = "bug-fix"
	TaskTypeDocGen    = "doc-gen"
	TaskTypePlan      = "plan"
	TaskTypeReview    = "review"
	TaskTypeImplement = "implement"
)

// Complexity 枚举：任务复杂度 T1/T2/T3（地基层 §3.4）。
const (
	ComplexityT1 = "T1" // 简单（单文件小改）
	ComplexityT2 = "T2" // 中等（多文件协同）
	ComplexityT3 = "T3" // 复杂（跨包重构）
)

// ErrorType 枚举：子任务失败错误类型（地基层 §3.4）。
const (
	ErrorTypeCompileError      = "compile_error"
	ErrorTypeTestFailure       = "test_failure"
	ErrorTypeContractViolation = "contract_violation"
	ErrorTypeTimeout           = "timeout"
	ErrorTypeNone              = "none"
)

// QualityRecord 质量记录，与 N2 CostRecord 分离（，PIT-002 防护）。
// 对齐 02 §2.7.3 v1.1：嵌套 metrics_correlation + int l1_score + adopted/modified/discarded 枚举。
// 严格禁止包含任何 cost/token 字段（InputTokens/OutputTokens/CostCents 等），PIT-002 零容忍。
type QualityRecord struct {
	TaskID                string             `json:"task_id"`             // 主键（H10 自维护）
	MetricsCorrelation    MetricsCorrelation `json:"metrics_correlation"` // JOIN CostRecord 的关联键
	OrchestrationID       string             `json:"orchestration_id"`
	ParentTaskID          string             `json:"parent_task_id,omitempty"`
	ParentOrchestrationID string             `json:"parent_orchestration_id,omitempty"`
	TaskType              string             `json:"task_type"`  // bug-fix/doc-gen/plan/review/implement
	Complexity            string             `json:"complexity"` // T1/T2/T3
	Model                 string             `json:"model"`      // 冗余（便于聚合查询）
	L0Pass                bool               `json:"l0_pass"`    // 编译/测试通过
	L1Score               int                `json:"l1_score"`   // 0-100
	L2Verdict             string             `json:"l2_verdict"` // adopted/modified/discarded
	L2ModificationPct     int                `json:"l2_modification_pct"`
	L2Reason              string             `json:"l2_reason,omitempty"`           // 弃用原因标签 a/b/c/d/e
	L2VerdictImplicit     string             `json:"l2_verdict_implicit,omitempty"` // 隐式反馈弱信号
	L2VerdictInferred     string             `json:"l2_verdict_inferred,omitempty"` // Phase 3 LLM-as-Judge
	ToolCallsCount        int                `json:"tool_calls_count"`
	PromptVersion         string             `json:"prompt_version"`
	DurationSec           int                `json:"duration_sec"`
	RetryCount            int                `json:"retry_count"`
	ErrorType             string             `json:"error_type,omitempty"` // compile_error/test_failure/contract_violation/timeout/none
	Timestamp             string             `json:"timestamp"`            // ISO 8601

	// PatternID 关联经验库 pattern_id，用于 computeAdoptionRate 反查与生命周期更新。
	// omitempty 向后兼容：旧记录无此字段，反序列化为空字符串。
	PatternID string `json:"pattern_id,omitempty"`

	// BudgetExceeded 子任务是否超 tokens_cap 或 cost_cap_cents（独立字段，不覆盖 l0_pass）。
	BudgetExceeded bool   `json:"budget_exceeded,omitempty"`
	ExceedReason   string `json:"exceed_reason,omitempty"` // 超限原因（tokens_exceeded / cost_exceeded / both）
}

// MetricsCorrelation JOIN CostRecord 的关联键（CostRecord 无 task_id 字段，02 §2.7.3）。
// 通过 (AgentID, Timestamp) 二元组反查 N2 CostRecord，实现 cost 反查而不污染 N2 数据流。
type MetricsCorrelation struct {
	AgentID   string `json:"agent_id"`  // 与 CostRecord.AgentID 对齐
	Timestamp string `json:"timestamp"` // ISO 8601，与 CostRecord.Timestamp 对齐
}
