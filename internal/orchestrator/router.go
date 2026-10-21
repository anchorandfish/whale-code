// router.go — 模块 3 三因子路由器 Router + PickModel（H10 Phase 1 总纲 §5.3 /）。
// ；落地：PickModel 三因子 + 冷启动降级
// + 冷启动降级；落地：sampleSize<10 不触发升级（仅用因子1+因子3），sampleSize>=10 且 score<0.7 才升级
// （地基层 §3.5）；落地：Router 复用 Phase 0 ProfileStore.Get 返回 ModelProfile
// 是 H10 主设计；落地：internal/orchestrator/router.go 唯一主设计方
// SNAPSHOT: pkg=internal/orchestrator; type=Router; method=PickModel(SubTaskDef)string; factors={base_matrix,success_rate,budget_mode}; cold_start=sampleSize<10降级(仅因子1+3); upgrade_threshold=score<0.7&&sampleSize>=10; budget_mode∈{economy,balanced,premium}

package orchestrator

import (
	"fmt"
	"strings"
	"sync"
)

// TaskModelMatrix 任务能力矩阵（总纲 §5.3 表）。
// key = task_type, value = {complexity → model}。
type TaskModelMatrix map[string]map[string]string

// 模型枚举常量（总纲 §5.3 升级阶梯）。
const (
	ModelHaiku  = "haiku"
	ModelSonnet = "sonnet"
	ModelOpus   = "opus"
	ModelGPT5   = "gpt-5"
)

// ColdStartThreshold 冷启动阈值（：sampleSize < 此值时不触发升级）。
const ColdStartThreshold = 10

// UpgradeScoreThreshold 升级阈值（：score < 此值且 sampleSize>=10 时触发升级）。
const UpgradeScoreThreshold = 0.7

// defaultMatrix 默认任务能力矩阵（总纲 §5.3 表）。
var defaultMatrix = TaskModelMatrix{
	TaskTypeDocGen: {
		ComplexityT1: ModelHaiku,
		ComplexityT2: ModelHaiku,
		ComplexityT3: ModelSonnet,
	},
	"unit-test": {
		ComplexityT1: ModelHaiku,
		ComplexityT2: ModelSonnet,
		ComplexityT3: ModelSonnet,
	},
	TaskTypeBugFix: {
		ComplexityT1: ModelSonnet,
		ComplexityT2: ModelSonnet,
		ComplexityT3: ModelOpus,
	},
	"design": {
		ComplexityT1: ModelSonnet,
		ComplexityT2: ModelOpus,
		ComplexityT3: ModelOpus,
	},
	TaskTypeReview: {
		ComplexityT1: ModelHaiku,
		ComplexityT2: ModelSonnet,
		ComplexityT3: ModelSonnet,
	},
	"refactor": {
		ComplexityT1: ModelSonnet,
		ComplexityT2: ModelSonnet,
		ComplexityT3: ModelOpus,
	},
}

// Router 三因子路由器（总纲 §5.3，）。
// 三因子：① 基线矩阵 ② 历史成功率（quality.jsonl 聚合，冷启动 sampleSize<10 降级） ③ 用户预算模式。
type Router struct {
	mu         sync.RWMutex
	matrix     TaskModelMatrix
	profile    *ProfileStore
	budgetMode string
}

// NewRouterFromPhase0 从 Phase 0 ProfileStore + 静态矩阵构造路由器（总纲 §5.3）。
func NewRouterFromPhase0(profile *ProfileStore, budgetMode string) *Router {
	return &Router{
		matrix:     defaultMatrix,
		profile:    profile,
		budgetMode: budgetMode,
	}
}

// NewRouter 构造路由器（测试用，可指定矩阵）。
func NewRouter(matrix TaskModelMatrix, profile *ProfileStore, budgetMode string) *Router {
	if matrix == nil {
		matrix = defaultMatrix
	}
	return &Router{
		matrix:     matrix,
		profile:    profile,
		budgetMode: budgetMode,
	}
}

// PickModel 三因子路由选模型（，总纲 §5.3）。
// 因子1：base = matrix[task.Type][task.Complexity]
// 因子2：score, sampleSize = profile.SuccessRate(task.Type, base); sampleSize>=10 && score<0.7 → upgrade
// 因子3：budgetMode=="economy" → downgrade; "premium" → 不额外升级（因子2 已处理）
func (r *Router) PickModel(task SubTaskDef) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 因子1：基线矩阵
	base := r.lookupMatrix(task.Type, task.Complexity)

	// 因子2：历史成功率（冷启动降级，）
	score, sampleSize := r.SuccessRate(task.Type, base)
	if sampleSize >= ColdStartThreshold && score < UpgradeScoreThreshold {
		base = UpgradeModel(base)
	}

	// 因子3：用户预算模式
	switch r.budgetMode {
	case BudgetModeEconomy:
		base = DowngradeModel(base)
	case BudgetModePremium:
		// 不再额外升级（因子2 已处理）
	}

	return base
}

// SuccessRate 查询 (task_type, model) 的历史成功率（从 Phase 0 ProfileStore）。
// 冷启动：sampleSize < 10 → 返回 (0, 0)，路由器降级为仅用因子1+因子3。
func (r *Router) SuccessRate(taskType, model string) (score float64, sampleSize int) {
	if r.profile == nil {
		return 0, 0
	}
	prof := r.profile.Get(taskType, model)
	if prof == nil {
		// 冷启动：sampleSize < 10 → 返回 (0, 0)
		return 0, 0
	}
	return prof.SuccessRate, prof.SampleCount
}

// lookupMatrix 查询基线矩阵（持锁状态，内部调用）。
func (r *Router) lookupMatrix(taskType, complexity string) string {
	complexityMap, ok := r.matrix[taskType]
	if !ok {
		return ModelSonnet // 兜底默认
	}
	model, ok := complexityMap[complexity]
	if !ok || model == "" {
		return ModelSonnet // 兜底默认
	}
	return model
}

// UpgradeModel 模型升级一档（haiku→sonnet→opus→gpt-5，总纲 §5.3）。
func UpgradeModel(model string) string {
	switch model {
	case ModelHaiku:
		return ModelSonnet
	case ModelSonnet:
		return ModelOpus
	case ModelOpus:
		return ModelGPT5
	case ModelGPT5:
		return ModelGPT5 // 已是最高
	default:
		return ModelSonnet // 未知模型兜底
	}
}

// DowngradeModel 模型降级一档（gpt-5→opus→sonnet→haiku，总纲 §5.3）。
func DowngradeModel(model string) string {
	switch model {
	case ModelGPT5:
		return ModelOpus
	case ModelOpus:
		return ModelSonnet
	case ModelSonnet:
		return ModelHaiku
	case ModelHaiku:
		return ModelHaiku // 已是最低
	default:
		return ModelHaiku // 未知模型兜底
	}
}

// BudgetMode 返回当前预算模式（仅用于诊断）。
func (r *Router) BudgetMode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.budgetMode
}

// === Phase 4 §3.6 四因子路由扩展（BASE 变更 三段式已论证，不改 PickModel 公开签名） ===
// ；落地：SelectModel + experienceFactor + 决策合成优先级
// ；落地：experienceFactor 要求 VerifiedState=="verified" 才降级
// SNAPSHOT: factors={baseline,success_rate,budget,experience(新增)}; priority=budget>experience>successRate>baseline; cold_start=verified才降级(跨会话3次L2=adopted); experience_threshold={confidence>=0.8+verified→haiku, 0.5-0.8不降级, <0.5不注入}

// SelectModel 四因子路由决策（Phase 4 §3.6.2，扩展 Phase 1 三因子，不改 PickModel 公开签名）。
// 因子 1：基线矩阵（静态能力， 已有）
// 因子 2：历史成功率（quality.jsonl 聚合， 已有）
// 因子 3：用户预算（已有）
// 因子 4：经验库命中（Phase 4 新增，BASE 变更）
// 决策合成优先级：用户预算 > 经验库降级 > 历史成功率 > 基线矩阵
// 注：PickModel 仍保留为 Phase 1 三因子入口（SubTaskDef 输入），SelectModel 是 Phase 4 四因子入口（TaskContext 输入）。
func (r *Router) SelectModel(task *TaskContext) (string, error) {
	if task == nil {
		return "", fmt.Errorf("router: task must not be nil")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// 因子 1：基线矩阵（从 TaskContext.Goal 提取 task_type，从 BudgetMode 推断 complexity）
	taskType := inferTaskTypeFromGoal(task.Goal)
	complexity := inferComplexityFromGoal(task.Goal)
	baseline := r.lookupMatrix(taskType, complexity)
	if baseline == "" {
		baseline = ModelSonnet // 兜底默认
	}

	// 因子 2：历史成功率（冷启动降级，）
	score, sampleSize := r.SuccessRate(taskType, baseline)
	shouldUpgrade := sampleSize >= ColdStartThreshold && score < UpgradeScoreThreshold

	// 因子 3：用户预算（从 BudgetMode 字段读，而非 Router.budgetMode，因 TaskContext 可携带独立 budgetMode）
	budgetMode := task.BudgetMode
	if budgetMode == "" {
		budgetMode = r.budgetMode
	}
	budgetModel := ""
	switch budgetMode {
	case BudgetModeEconomy:
		budgetModel = DowngradeModel(baseline)
	case BudgetModePremium:
		// 不再额外升级（因子 2 已处理）
	}

	// 因子 4：经验库命中（Phase 4 新增，冷启动保护：仅 verified 才降级）
	var expModel string
	if task.ExperienceHit != nil {
		expModel = r.experienceFactor(task.ExperienceHit)
	}

	// 决策合成（优先级：用户预算 > 经验库降级 > 历史成功率 > 基线矩阵）
	switch {
	case budgetModel != "" && budgetMode == BudgetModeEconomy:
		// 用户硬预算优先（economy 模式强制降级）
		return budgetModel, nil
	case expModel != "":
		// 经验库命中降级
		return expModel, nil
	case shouldUpgrade:
		// 历史高失败率升级
		return UpgradeModel(baseline), nil
	default:
		// 基线矩阵兜底
		return baseline, nil
	}
}

// experienceFactor 第四因子决策（冷启动保护，Phase 4 §3.6.2）。
// 冷启动保护：VerifiedState == "verified" 才降级（需跨会话 3 次 L2=adopted，§3.3.2）。
// confidence >= 0.8 + verified → 返回 haiku（降级）
// confidence 0.5-0.8 → 返回空字符串（不降级，保留原模型兜底）
// confidence < 0.5 → 已在 §3.2.3 不注入（理论上不会进入此分支）
func (r *Router) experienceFactor(hit *ExperienceHit) string {
	if hit == nil {
		return ""
	}
	if hit.Confidence >= ConfidenceThresholdHigh && hit.VerifiedState == VerifiedStateVerified {
		return ModelHaiku // 有先验 + 高置信 + 已验证 → 降级
	}
	// confidence 0.5-0.8 不降级（保留原模型兜底）
	// confidence < 0.5 已在 §3.2.3 不注入
	return ""
}

// ExperienceFactor 公开包装（测试用，便于直接测试第四因子逻辑）。
func (r *Router) ExperienceFactor(hit *ExperienceHit) string {
	return r.experienceFactor(hit)
}

// inferTaskTypeFromGoal 从 TaskContext.Goal 提取 task_type（简化：关键字匹配）。
func inferTaskTypeFromGoal(goal string) string {
	g := strings.ToLower(goal)
	if strings.Contains(g, "bug") || strings.Contains(g, "fix") || strings.Contains(g, "修复") {
		return TaskTypeBugFix
	}
	if strings.Contains(g, "doc") || strings.Contains(g, "文档") {
		return TaskTypeDocGen
	}
	if strings.Contains(g, "plan") || strings.Contains(g, "计划") {
		return TaskTypePlan
	}
	if strings.Contains(g, "review") || strings.Contains(g, "审查") {
		return TaskTypeReview
	}
	if strings.Contains(g, "implement") || strings.Contains(g, "实现") {
		return TaskTypeImplement
	}
	return TaskTypeImplement // 兜底默认
}

// inferComplexityFromGoal 从 TaskContext.Goal 提取 complexity（简化：关键字匹配）。
func inferComplexityFromGoal(goal string) string {
	g := strings.ToLower(goal)
	if strings.Contains(g, "t3") || strings.Contains(g, "复杂") || strings.Contains(g, "重构") {
		return ComplexityT3
	}
	if strings.Contains(g, "t2") || strings.Contains(g, "中等") || strings.Contains(g, "多文件") {
		return ComplexityT2
	}
	return ComplexityT1 // 兜底默认 T1
}
