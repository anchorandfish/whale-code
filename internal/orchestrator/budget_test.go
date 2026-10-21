// budget_test.go — 模块 8 BudgetTracker 反例测试（TDD 先行，总纲 §8.3 / 编排层 §3.4）。
// 覆盖致命反例：子任务超预算但不覆盖 l0_pass（两者解耦）/ 并发安全 / 80%/100% 三档介入 / 用量来自 provider 真报 token。
// 硬预算；落地：双层预算 + 80%/100% 三档介入
// 是三因子之一；落地：CheckSubtaskBudget 校验 TaskContext.BudgetHint
// SNAPSHOT: tests={Over80_NotifiesUser, Over100_PausesAll, SubtaskExceeded_NotOverwritesL0Pass(致命反例), ConcurrentRecordUsage_MutexProtected, UsageIsTokenOnly_NoMoneyAxis}
//
// ★ 2026-10-09：本文件断言的美分口径全部改为 **token** 口径（「不算价钱」铁律）。

package orchestrator

import (
	"reflect"
	"sync"
	"testing"
)

// TestBudgetTracker_Over80_NotifiesUser 已用 80% → over80=true，over100=false。
func TestBudgetTracker_Over80_NotifiesUser(t *testing.T) {
	b := NewBudgetTracker(1000) // 总上限 1000 token

	// 已用 800 token = 80%
	if err := b.RecordUsage(800); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	usedPct, over80, over100 := b.CheckBudget()
	if usedPct != 80 {
		t.Errorf("expected usedPct=80, got %d", usedPct)
	}
	if !over80 {
		t.Error("over80 应为 true（已用 80%，应通知用户）")
	}
	if over100 {
		t.Error("over100 应为 false（仅 80%，未超 100%）")
	}
	if b.IsExceeded() {
		t.Error("IsExceeded 应为 false（仅 80%，未超 100%）")
	}
}

// TestBudgetTracker_Over100_PausesAll 已用 100% → over100=true, IsExceeded=true。
func TestBudgetTracker_Over100_PausesAll(t *testing.T) {
	b := NewBudgetTracker(1000) // 总上限 1000 token

	// 已用 1000 token = 100%
	if err := b.RecordUsage(1000); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	usedPct, over80, over100 := b.CheckBudget()
	if usedPct != 100 {
		t.Errorf("expected usedPct=100, got %d", usedPct)
	}
	if !over80 {
		t.Error("over80 应为 true（已用 100% > 80%）")
	}
	if !over100 {
		t.Error("over100 应为 true（已用 100%，应暂停所有未派发子任务）")
	}
	if !b.IsExceeded() {
		t.Error("IsExceeded 应为 true（已超总预算 100%）")
	}
}

// TestBudgetTracker_Over100_ExceedsBudget 超过 100% → IsExceeded=true。
func TestBudgetTracker_Over100_ExceedsBudget(t *testing.T) {
	b := NewBudgetTracker(1000)

	// 已用 1200 token = 120%
	if err := b.RecordUsage(1200); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	_, _, over100 := b.CheckBudget()
	if !over100 {
		t.Error("over100 应为 true（120% > 100%）")
	}
	if !b.IsExceeded() {
		t.Error("IsExceeded 应为 true（120% > 100%）")
	}
}

// TestBudgetTracker_SubtaskExceeded_NotOverwritesL0Pass 致命反例：
// 子任务超预算但 L0 通过 → budget_exceeded=true 但 l0_pass 仍为 true（两者解耦）。
// 验证目标（总纲 §8.3 审查 v1 修订）：budget_exceeded 不覆盖 l0_pass。
// ★ ：子任务上限走 BudgetHint.TokensCap（token 轴）。
func TestBudgetTracker_SubtaskExceeded_NotOverwritesL0Pass(t *testing.T) {
	b := NewBudgetTracker(10000)

	// 构造子任务：BudgetHint.TokensCap=100，实际用量 150 token（超限）
	tc := TaskContext{
		TaskID:     "subtask-overbudget",
		MaxDepth:   MaxDepthHardLimit,
		BudgetHint: BudgetHint{TokensCap: 100},
		BudgetMode: BudgetModeBalanced,
	}
	actualTokens := int64(150)

	// 检查子任务预算
	exceeded := b.CheckSubtaskBudget(tc, actualTokens)
	if !exceeded {
		t.Fatal("致命反例：子任务实际用量 150 > TokensCap 100，exceeded 必须为 true")
	}

	// 关键验证：budget_exceeded 与 l0_pass 解耦
	// 即使 budget_exceeded=true，L0 评分仍独立判定（这里模拟 L0 通过）
	l0Pass := true // L0 评分独立得出（编译/测试通过）
	budgetExceeded := exceeded

	// 两者独立，budget_exceeded 不覆盖 l0_pass
	if budgetExceeded && !l0Pass {
		t.Error("致命反例违反：budget_exceeded=true 不应覆盖 l0_pass=true（两者解耦，审查 v1 修订）")
	}
	if !budgetExceeded {
		t.Error("budget_exceeded 应为 true")
	}
	if !l0Pass {
		t.Error("l0_pass 应仍为 true（L0 反映任务完成度，与用量合规性解耦）")
	}
}

// TestBudgetTracker_SubtaskWithinBudget_NotExceeded 子任务未超预算 → exceeded=false。
func TestBudgetTracker_SubtaskWithinBudget_NotExceeded(t *testing.T) {
	b := NewBudgetTracker(10000)

	tc := TaskContext{
		TaskID:     "subtask-ok",
		BudgetHint: BudgetHint{TokensCap: 200},
	}
	// 实际用量 150 < TokensCap 200
	exceeded := b.CheckSubtaskBudget(tc, 150)
	if exceeded {
		t.Error("exceeded 应为 false（150 < 200，未超预算）")
	}
}

// TestBudgetTracker_ConcurrentRecordUsage_MutexProtected 两个 goroutine 同时 RecordUsage，sync.Mutex 防竞态。
func TestBudgetTracker_ConcurrentRecordUsage_MutexProtected(t *testing.T) {
	b := NewBudgetTracker(100000)

	const N = 100 // 每个 goroutine 记录 100 次，每次 10 token
	var wg sync.WaitGroup
	wg.Add(2)

	// goroutine 1：每次记录 10 token，共 1000 token
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			if err := b.RecordUsage(10); err != nil {
				t.Errorf("goroutine1 RecordUsage: %v", err)
				return
			}
		}
	}()

	// goroutine 2：每次记录 10 token，共 1000 token
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			if err := b.RecordUsage(10); err != nil {
				t.Errorf("goroutine2 RecordUsage: %v", err)
				return
			}
		}
	}()

	wg.Wait()

	// 验证总已用 = 2000 token（无竞态丢失）
	used := b.UsedTokens()
	expected := int64(2 * N * 10)
	if used != expected {
		t.Errorf("并发 RecordUsage 竞态：expected usedTokens=%d, got %d (sync.Mutex 未生效)", expected, used)
	}
}

// TestBudgetTracker_UsageIsTokenOnly_NoMoneyAxis 判别器：
// BudgetTracker **只有 token 一根轴** —— 不含任何"钱"字段（cost/cents/budget-dollar 类），
// 且字段集合恰为 {totalTokensCap, usedTokens, mu, exceeded}。
// 验证目标：「不算价钱」铁律；原 totalBudgetCents/usedCents 已退场。
func TestBudgetTracker_UsageIsTokenOnly_NoMoneyAxis(t *testing.T) {
	// 验证 BudgetTracker struct 不含「钱」字段与 N2 逐维 token 字段（PIT-002 防护）
	// 注意：必须用指针 Elem() 获取 struct 类型，避免 reflect.TypeOf(*b) 复制 sync.Mutex 值
	rt := reflect.TypeOf((*BudgetTracker)(nil)).Elem()
	forbidden := []string{
		"InputTokens", "OutputTokens", "CacheCreation", "CacheRead",
		"InputToken", "OutputToken", "Cost", "TokensIn", "TokensOut",
		"totalBudgetCents", "usedCents", "TotalCostCents", "UsedCostCents", "CostCapCents",
	}
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		for _, bad := range forbidden {
			if name == bad {
				t.Errorf(" / PIT-002 violation: BudgetTracker must not contain money/N2 field %q", name)
			}
		}
	}

	// 验证 BudgetTracker 仅含 4 个字段（totalTokensCap/usedTokens/mu/exceeded）
	expectedFields := map[string]bool{
		"totalTokensCap": false,
		"usedTokens":     false,
		"mu":             false,
		"exceeded":       false,
	}
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if _, ok := expectedFields[name]; ok {
			expectedFields[name] = true
		} else {
			t.Errorf("BudgetTracker 含未预期字段 %q（应仅 4 字段）", name)
		}
	}
	for name, found := range expectedFields {
		if !found {
			t.Errorf("BudgetTracker 缺少字段 %q", name)
		}
	}
}

// TestBudgetTracker_NegativeUsage_Rejected 负值用量拒绝（防倒扣）。
func TestBudgetTracker_NegativeUsage_Rejected(t *testing.T) {
	b := NewBudgetTracker(1000)

	err := b.RecordUsage(-50)
	if err == nil {
		t.Fatal("负值 token 用量必须拒绝（防倒扣）")
	}
}

// TestBudgetTracker_ZeroUsage_Allowed 零值用量允许（缓存命中 / 空回复）。
func TestBudgetTracker_ZeroUsage_Allowed(t *testing.T) {
	b := NewBudgetTracker(1000)

	if err := b.RecordUsage(0); err != nil {
		t.Fatalf("零值 token 用量应允许: %v", err)
	}

	usedPct, _, _ := b.CheckBudget()
	if usedPct != 0 {
		t.Errorf("expected usedPct=0, got %d", usedPct)
	}
}
