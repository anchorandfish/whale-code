// router_test.go — 模块 3 三因子路由反例测试（TDD 先行，总纲 §5.3 /）。
// 覆盖致命反例：冷启动 sampleSize<10 不触发升级 / 暖启动升级 / 预算模式降级 / 矩阵兜底。
// + 冷启动降级；落地：sampleSize<10 不触发升级
// SNAPSHOT: tests={ColdStart_NoUpgrade, WarmStart_UpgradeOnLowScore, WarmStart_NoUpgradeOnHighScore, EconomyMode_Downgrade, PremiumMode_NoExtraUpgrade, ConcurrentPickModel_MutexProtected, UpgradeModel_Ladder, MatrixEmpty_DefaultSonnet}

package orchestrator

import (
	"sync"
	"testing"
)

// makeTestRouter 构造测试用 Router（指定 budgetMode）。
func makeTestRouter(budgetMode string) *Router {
	return NewRouter(nil, NewProfileStore(), budgetMode)
}

// makeTestRouterWithProfile 构造带预设 ProfileStore 的 Router（用于暖启动测试）。
func makeTestRouterWithProfile(budgetMode string, taskType, model string, successRate float64, sampleCount int) *Router {
	ps := NewProfileStore()
	// 直接写入预设画像（绕过 quality.jsonl 加载）
	ps.mu.Lock()
	ps.profiles[profileKey{TaskType: taskType, Model: model}] = ModelProfile{
		TaskType:    taskType,
		Model:       model,
		SuccessRate: successRate,
		SampleCount: sampleCount,
	}
	ps.mu.Unlock()
	return NewRouter(nil, ps, budgetMode)
}

// TestRouter_ColdStart_NoUpgrade 致命反例：sampleSize=9（<10）+ score=0.3 → 不触发升级，返回 base（冷启动降级）。
func TestRouter_ColdStart_NoUpgrade(t *testing.T) {
	// 构造 sampleSize=9 的画像（< 10 冷启动阈值）
	r := makeTestRouterWithProfile(BudgetModeBalanced, TaskTypeBugFix, ModelSonnet, 0.3, 9)

	task := SubTaskDef{
		SubTaskID:  "S-cold-start",
		Type:       TaskTypeBugFix,
		Complexity: ComplexityT1, // 基线矩阵：bug-fix/T1 → sonnet
	}

	got := r.PickModel(task)
	if got != ModelSonnet {
		t.Fatalf("致命反例：冷启动 sampleSize=9 + score=0.3 时不应触发升级，期望 %s，got %s（ 冷启动降级）",
			ModelSonnet, got)
	}
}

// TestRouter_WarmStart_UpgradeOnLowScore 暖启动：sampleSize=15（>=10）+ score=0.5（<0.7）→ 触发升级。
func TestRouter_WarmStart_UpgradeOnLowScore(t *testing.T) {
	r := makeTestRouterWithProfile(BudgetModeBalanced, TaskTypeBugFix, ModelSonnet, 0.5, 15)

	task := SubTaskDef{
		SubTaskID:  "S-warm-upgrade",
		Type:       TaskTypeBugFix,
		Complexity: ComplexityT1, // 基线矩阵：bug-fix/T1 → sonnet
	}

	got := r.PickModel(task)
	if got != ModelOpus {
		t.Fatalf("暖启动 sampleSize=15 + score=0.5 时应触发升级 sonnet→opus，期望 %s，got %s", ModelOpus, got)
	}
}

// TestRouter_WarmStart_NoUpgradeOnHighScore 暖启动：sampleSize=15 + score=0.85（>=0.7）→ 不升级。
func TestRouter_WarmStart_NoUpgradeOnHighScore(t *testing.T) {
	r := makeTestRouterWithProfile(BudgetModeBalanced, TaskTypeBugFix, ModelSonnet, 0.85, 15)

	task := SubTaskDef{
		SubTaskID:  "S-warm-no-upgrade",
		Type:       TaskTypeBugFix,
		Complexity: ComplexityT1,
	}

	got := r.PickModel(task)
	if got != ModelSonnet {
		t.Fatalf("暖启动 sampleSize=15 + score=0.85 时不应升级，期望 %s，got %s", ModelSonnet, got)
	}
}

// TestRouter_EconomyMode_Downgrade budgetMode=economy → 降级一档。
func TestRouter_EconomyMode_Downgrade(t *testing.T) {
	r := makeTestRouter(BudgetModeEconomy)

	task := SubTaskDef{
		SubTaskID:  "S-economy",
		Type:       TaskTypeBugFix,
		Complexity: ComplexityT1, // 基线矩阵：bug-fix/T1 → sonnet
	}

	got := r.PickModel(task)
	// economy → downgrade sonnet → haiku
	if got != ModelHaiku {
		t.Fatalf("economy 模式应降级 sonnet→haiku，期望 %s，got %s", ModelHaiku, got)
	}
}

// TestRouter_PremiumMode_NoExtraUpgrade budgetMode=premium → 不再额外升级（因子2 已处理）。
func TestRouter_PremiumMode_NoExtraUpgrade(t *testing.T) {
	// 冷启动（sampleSize=0），premium 模式
	r := makeTestRouter(BudgetModePremium)

	task := SubTaskDef{
		SubTaskID:  "S-premium",
		Type:       TaskTypeBugFix,
		Complexity: ComplexityT1, // 基线矩阵：bug-fix/T1 → sonnet
	}

	got := r.PickModel(task)
	// premium 冷启动：因子2 不触发升级，因子3 premium 也不额外升级 → 返回 base=sonnet
	if got != ModelSonnet {
		t.Fatalf("premium 模式冷启动不应额外升级，期望 %s，got %s", ModelSonnet, got)
	}
}

// TestRouter_ConcurrentPickModel_MutexProtected 两个 goroutine 同时 PickModel，sync.RWMutex 防竞态（总纲 §5.3）。
func TestRouter_ConcurrentPickModel_MutexProtected(t *testing.T) {
	r := makeTestRouter(BudgetModeBalanced)

	task := SubTaskDef{
		SubTaskID:  "S-concurrent",
		Type:       TaskTypeBugFix,
		Complexity: ComplexityT1,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	results := make([]string, 2)
	go func() {
		defer wg.Done()
		results[0] = r.PickModel(task)
	}()
	go func() {
		defer wg.Done()
		results[1] = r.PickModel(task)
	}()

	wg.Wait()

	// 两个 goroutine 应得到相同结果（并发安全）
	if results[0] != results[1] {
		t.Errorf("并发 PickModel 结果不一致（竞态）：goroutine1=%s, goroutine2=%s", results[0], results[1])
	}
}

// TestUpgradeModel_Ladder haiku→sonnet→opus→gpt-5 阶梯正确（总纲 §5.3）。
func TestUpgradeModel_Ladder(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{ModelHaiku, ModelSonnet},
		{ModelSonnet, ModelOpus},
		{ModelOpus, ModelGPT5},
		{ModelGPT5, ModelGPT5}, // 已是最高
	}
	for _, tt := range tests {
		got := UpgradeModel(tt.input)
		if got != tt.want {
			t.Errorf("UpgradeModel(%s) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

// TestDowngradeModel_Ladder gpt-5→opus→sonnet→haiku 阶梯正确（总纲 §5.3）。
func TestDowngradeModel_Ladder(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{ModelGPT5, ModelOpus},
		{ModelOpus, ModelSonnet},
		{ModelSonnet, ModelHaiku},
		{ModelHaiku, ModelHaiku}, // 已是最低
	}
	for _, tt := range tests {
		got := DowngradeModel(tt.input)
		if got != tt.want {
			t.Errorf("DowngradeModel(%s) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

// TestRouter_MatrixEmpty_DefaultSonnet 矩阵未覆盖的 (task_type, complexity) → 兜底返回 "sonnet"（总纲 §5.3）。
func TestRouter_MatrixEmpty_DefaultSonnet(t *testing.T) {
	r := makeTestRouter(BudgetModeBalanced)

	task := SubTaskDef{
		SubTaskID:  "S-unknown",
		Type:       "unknown-type",
		Complexity: "T99", // 矩阵未覆盖
	}

	got := r.PickModel(task)
	if got != ModelSonnet {
		t.Fatalf("矩阵未覆盖的 (task_type, complexity) 应兜底返回 sonnet，got %s", got)
	}
}

// TestRouter_SuccessRate_ColdStart ProfileStore 为 nil 或无画像时返回 (0, 0)（冷启动降级）。
func TestRouter_SuccessRate_ColdStart(t *testing.T) {
	r := makeTestRouter(BudgetModeBalanced)

	score, sampleSize := r.SuccessRate("any-type", "any-model")
	if score != 0 || sampleSize != 0 {
		t.Errorf("冷启动（无画像）应返回 (0, 0)，got (%f, %d)", score, sampleSize)
	}
}

// === Phase 4 §3.6 四因子路由扩展测试（追加，不改 Phase 1 已有测试） ===
// ；落地：SelectModel + experienceFactor
// ；落地：experienceFactor 仅 verified 才降级（致命反例 3）
// SNAPSHOT: tests={SelectModel_FourFactor_BudgetFirst, SelectModel_FourFactor_ExperienceSecond, SelectModel_FourFactor_SuccessRateThird, SelectModel_FourFactor_BaselineFallback, ExperienceFactor_ColdStart_NoVerified_NoDegrade(致命反例3), ExperienceFactor_LowConfidence_NoDegrade, ExperienceFactor_HighConfidence_Verified_Degrade, SelectModel_PublicSignature_NotChanged}

// makeTestTaskContextForRouter 构造 SelectModel 测试用 TaskContext（可指定 budgetMode + ExperienceHit）。
func makeTestTaskContextForRouter(taskID, goal, budgetMode string, hit *ExperienceHit) *TaskContext {
	tc := makeTestTaskContext(taskID, goal)
	if budgetMode != "" {
		tc.BudgetMode = budgetMode
	}
	tc.ExperienceHit = hit
	return tc
}

// TestRouter_SelectModel_FourFactor_BudgetFirst 优先级 1：用户预算 > 经验库 > 成功率 > 基线。
// economy 模式 + 经验库命中（verified, confidence=0.9）+ 高失败率 → 返回 budgetModel（haiku）。
func TestRouter_SelectModel_FourFactor_BudgetFirst(t *testing.T) {
	t.Parallel()
	// 预设画像：bug-fix/sonnet 成功率 0.5（< 0.7）sampleSize=20（>=10）→ 触发升级
	r := makeTestRouterWithProfile(BudgetModeEconomy, TaskTypeBugFix, ModelSonnet, 0.5, 20)

	hit := &ExperienceHit{
		PatternID:     "p-budget-first",
		Confidence:    0.9,
		VerifiedState: VerifiedStateVerified,
	}
	tc := makeTestTaskContextForRouter("S-budget-first", "fix bug in auth", BudgetModeEconomy, hit)

	got, err := r.SelectModel(tc)
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	// economy 优先 → DowngradeModel(sonnet)=haiku
	if got != ModelHaiku {
		t.Fatalf("预算优先级最高：economy 模式应返回 haiku（DowngradeModel(sonnet)），got %s", got)
	}
}

// TestRouter_SelectModel_FourFactor_ExperienceSecond 优先级 2：经验库降级（无 budget 约束时）。
// balanced + 经验库命中（verified, confidence=0.9）+ 冷启动 → 返回 haiku（经验库降级）。
func TestRouter_SelectModel_FourFactor_ExperienceSecond(t *testing.T) {
	t.Parallel()
	// 冷启动：无画像
	r := makeTestRouter(BudgetModeBalanced)

	hit := &ExperienceHit{
		PatternID:     "p-exp-second",
		Confidence:    0.9,
		VerifiedState: VerifiedStateVerified,
	}
	tc := makeTestTaskContextForRouter("S-exp-second", "fix bug in parser", BudgetModeBalanced, hit)

	got, err := r.SelectModel(tc)
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	// 经验库命中降级 → haiku
	if got != ModelHaiku {
		t.Fatalf("balanced + 经验库命中（verified, 0.9）应降级返回 haiku，got %s", got)
	}
}

// TestRouter_SelectModel_FourFactor_SuccessRateThird 优先级 3：历史成功率升级（无 budget/经验库时）。
// balanced + 无经验库 + sampleSize=20/rate=0.5 → 升级 sonnet→opus。
func TestRouter_SelectModel_FourFactor_SuccessRateThird(t *testing.T) {
	t.Parallel()
	// 预设画像：bug-fix/sonnet 成功率 0.5（< 0.7）sampleSize=20（>=10）→ 触发升级
	r := makeTestRouterWithProfile(BudgetModeBalanced, TaskTypeBugFix, ModelSonnet, 0.5, 20)

	tc := makeTestTaskContextForRouter("S-success-third", "fix bug in router", BudgetModeBalanced, nil)

	got, err := r.SelectModel(tc)
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	// 历史高失败率升级 → UpgradeModel(sonnet)=opus
	if got != ModelOpus {
		t.Fatalf("balanced + 无经验 + 高失败率（rate=0.5, sample=20）应升级返回 opus，got %s", got)
	}
}

// TestRouter_SelectModel_FourFactor_BaselineFallback 优先级 4：基线兜底。
// balanced + 无经验库 + 冷启动 → 返回 baseline（sonnet）。
func TestRouter_SelectModel_FourFactor_BaselineFallback(t *testing.T) {
	t.Parallel()
	// 冷启动：无画像
	r := makeTestRouter(BudgetModeBalanced)

	tc := makeTestTaskContextForRouter("S-baseline", "fix bug in utils", BudgetModeBalanced, nil)

	got, err := r.SelectModel(tc)
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	// 基线兜底 → sonnet（bug-fix/T1）
	if got != ModelSonnet {
		t.Fatalf("balanced + 冷启动 + 无经验应返回基线 sonnet，got %s", got)
	}
}

// TestRouter_ExperienceFactor_ColdStart_NoVerified_NoDegrade 致命反例 3（零容忍）：
// VerifiedState=provisionally_verified（非 verified）→ experienceFactor 不降级，返回空字符串。
// 冷启动保护：仅 verified 才降级（需跨会话 3 次 L2=adopted，§3.3.2）。
func TestRouter_ExperienceFactor_ColdStart_NoVerified_NoDegrade(t *testing.T) {
	t.Parallel()
	r := makeTestRouter(BudgetModeBalanced)

	// provisionally_verified 而非 verified → 不应降级（即使 confidence 很高）
	hit := &ExperienceHit{
		PatternID:     "p-cold-start-fatal",
		Confidence:    0.9, // 高置信
		VerifiedState: VerifiedStateProvisionallyVerified,
	}

	got := r.ExperienceFactor(hit)
	if got != "" {
		t.Fatalf("致命反例 3：VerifiedState=provisionally_verified 不应降级，期望空字符串，got %q（冷启动保护：仅 verified 才降级）", got)
	}
}

// TestRouter_ExperienceFactor_LowConfidence_NoDegrade 低置信度（< 0.8）+ verified → 不降级。
func TestRouter_ExperienceFactor_LowConfidence_NoDegrade(t *testing.T) {
	t.Parallel()
	r := makeTestRouter(BudgetModeBalanced)

	hit := &ExperienceHit{
		PatternID:     "p-low-conf",
		Confidence:    0.6, // < 0.8
		VerifiedState: VerifiedStateVerified,
	}

	got := r.ExperienceFactor(hit)
	if got != "" {
		t.Fatalf("低置信度（0.6 < 0.8）不应降级，期望空字符串，got %q", got)
	}
}

// TestRouter_ExperienceFactor_HighConfidence_Verified_Degrade 高置信度（>= 0.8）+ verified → 降级返回 haiku。
func TestRouter_ExperienceFactor_HighConfidence_Verified_Degrade(t *testing.T) {
	t.Parallel()
	r := makeTestRouter(BudgetModeBalanced)

	hit := &ExperienceHit{
		PatternID:     "p-high-conf",
		Confidence:    0.9, // >= 0.8
		VerifiedState: VerifiedStateVerified,
	}

	got := r.ExperienceFactor(hit)
	if got != ModelHaiku {
		t.Fatalf("高置信度（0.9 >= 0.8）+ verified 应降级返回 haiku，got %q", got)
	}
}

// TestRouter_SelectModel_PublicSignature_NotChanged 验证 SelectModel 公开签名与开发 prompt 一致：(task *TaskContext) (string, error)。
func TestRouter_SelectModel_PublicSignature_NotChanged(t *testing.T) {
	t.Parallel()
	r := makeTestRouter(BudgetModeBalanced)
	tc := makeTestTaskContextForRouter("S-sig", "fix bug", BudgetModeBalanced, nil)

	// 验证签名：接收 *TaskContext，返回 (string, error)
	got, err := r.SelectModel(tc)
	if err != nil {
		t.Fatalf("SelectModel 不应返回 error（合法 TaskContext）：%v", err)
	}
	if got == "" {
		t.Fatal("SelectModel 不应返回空字符串（合法 TaskContext）")
	}

	// 验证 nil task 返回 error（签名契约）
	_, err = r.SelectModel(nil)
	if err == nil {
		t.Fatal("SelectModel(nil) 应返回 error（签名契约：task must not be nil）")
	}
}
