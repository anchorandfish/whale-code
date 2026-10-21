package context

// 压缩体系重构（移交文档 2026-08-31 §三）的失败测试：
//   - 提示线 ShouldCompact 拉前到 ~0.80（用户决策窗口）
//   - 主力自动压缩线保持 contextWindow - AutoCompactBufferTokens（128K 下 ≈89.8%）
//   - 97% Blocked 只做硬停，不再承担压缩职责（由调用方策略层实现，本文件测阈值本身）
//
// 断言的是结果（状态/布尔判定收敛到界内），非"执行了"。

import "testing"

func TestBudgetShouldCompactHintLineAt80Percent(t *testing.T) {
	budget := DefaultBudget(128000)

	// 79% 仍为 warning（提示线之前）
	if s := budget.CheckState(101000, 0); s != StateWarning {
		t.Errorf("101000/128000 (≈79%%) state=%v, want warning", s)
	}
	// 0.80*128000 = 102400：提示线（用户决策窗口）
	if s := budget.CheckState(102400, 0); s != StateShouldCompact {
		t.Errorf("102400/128000 (=80%%) state=%v, want should_compact（提示线拉前到 0.80）", s)
	}
	// 0.80~0.97 之间仍为 should_compact（含主力线 89.8% 所在区间）
	if s := budget.CheckState(115000, 0); s != StateShouldCompact {
		t.Errorf("115000/128000 (≈89.8%%) state=%v, want should_compact", s)
	}
	// 97% 仍为 blocked
	if s := budget.CheckState(124200, 0); s != StateBlocked {
		t.Errorf("124200/128000 (≥97%%) state=%v, want blocked", s)
	}
}

func TestBudgetShouldAutoCompactSeparateFromHintLine(t *testing.T) {
	budget := DefaultBudget(128000)
	t.Setenv("WHALE_BODY_AFTER_PREFIX", "")

	// 80% 提示线不触发自动压缩
	if budget.ShouldAutoCompact(102400, 0) {
		t.Error("102400 (80%% 提示线) 不应触发自动压缩（主力线未到）")
	}
	// 主力线 contextWindow-13000 = 115000：触发
	if !budget.ShouldAutoCompact(115000, 0) {
		t.Error("115000 (=window-13000) 应触发自动压缩")
	}
	if budget.ShouldAutoCompact(114999, 0) {
		t.Error("114999 (< window-13000) 不应触发自动压缩")
	}
}

func TestBudgetShouldAutoCompactBodyAfterPrefixScope(t *testing.T) {
	budget := DefaultBudget(128000)

	// BodyAfterPrefix 默认启用：扣除缓存命中前缀后未达主力线 → 不触发
	t.Setenv("WHALE_BODY_AFTER_PREFIX", "")
	if budget.ShouldAutoCompact(120000, 20000) {
		t.Error("120000-20000 cached=100000 effective < 115000，不应触发自动压缩")
	}
	// 关闭 BodyAfterPrefix：按全量计 → 触发
	t.Setenv("WHALE_BODY_AFTER_PREFIX", "0")
	if !budget.ShouldAutoCompact(120000, 20000) {
		t.Error("BodyAfterPrefix 关闭时 120000 >= 115000 应触发自动压缩")
	}
}

func TestBudgetCompactPercentDefaultIs80(t *testing.T) {
	if got := DefaultBudget(128000).CompactPercent; got != 0.80 {
		t.Errorf("DefaultBudget CompactPercent=%v, want 0.80", got)
	}
	if got := NewBudgetFromConfig(128000, nil).CompactPercent; got != 0.80 {
		t.Errorf("NewBudgetFromConfig(nil) CompactPercent=%v, want 0.80", got)
	}
}

func TestBudgetSmallWindowCompactThresholdFallsBackTo80Percent(t *testing.T) {
	// 8000 窗口：window-13000 <= 0 → 回退百分比线 0.80*8000 = 6400
	budget := DefaultBudget(8000)
	if got := budget.CompactThreshold(); got != 6400 {
		t.Errorf("CompactThreshold=%d, want 6400 (0.80*8000)", got)
	}
}
