// types.go — H10 共享类型定义（SubTask / DiffStats / ContractSpec 等）。
// 此文件集中放置跨桶复用的小型类型，避免循环依赖。

package orchestrator

// SubTask 子任务载体（地基层 §3.4，Phase 0 简化版，Phase 1 编排器落地后扩展）。
// Phase 0 仅含评分所需字段；Phase 1 的 state_machine.go 会扩展状态/优先级/依赖等字段。
type SubTask struct {
	TaskID          string        // 子任务 ID（与 QualityRecord.TaskID 对齐）
	OrchestrationID string        // 所属编排 ID
	TaskType        string        // bug-fix/doc-gen/plan/review/implement
	Complexity      string        // T1/T2/T3
	Model           string        // 实际执行模型
	WorkDir         string        // 子任务工作目录（L0 评分需要）
	HasContract     bool          // 是否有契约约束（L0 评分项 3）
	ContractSpec    *ContractSpec // 契约规范（HasContract=true 时非空）
	TestFiles       []string      // 测试文件路径（L1 testCoverageScore 用）
	SourceFiles     []string      // 源码文件路径（L1 各项评分用）
	PromptVersion   string        // 提示词版本（写入 QualityRecord）
	ToolCallsCount  int           // 工具调用次数
	DurationSec     int           // 子任务耗时（秒）
	RetryCount      int           // 重试次数
}

// ContractSpec 契约规范（L0 评分项 3：契约一致性检查，地基层 §4.3）。
// Phase 0 仅检查函数签名是否一致；Phase 1 扩展为完整契约（输入/输出/错误码）。
type ContractSpec struct {
	ExpectedFuncs []FuncSignature // 期望的函数签名列表
}

// FuncSignature 函数签名（L0 契约检查用）。
type FuncSignature struct {
	Name    string   // 函数名
	Pkg     string   // 所属包
	Params  []string // 参数类型列表
	Results []string // 返回值类型列表
}

// DiffStats git diff 统计（L2 隐式反馈弱信号用，，地基层 §4.6）。
// 复用 filehistory.DiffStats 命名以保持一致，但独立定义避免 import 循环。
type DiffStats struct {
	Insertions   int // 新增行数
	Deletions    int // 删除行数
	FilesChanged int // 变更文件数
}

// L2Result L2 评分结果（显式反馈记录后返回，地基层 §4.5）。
type L2Result struct {
	Verdict         string // adopted/modified/discarded
	ModificationPct int    // 修改百分比
	Reason          string // 弃用原因 a/b/c/d/e（discarded/大改 modified 必填）
}
