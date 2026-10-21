package context

import (
	"fmt"
	"testing"
)

// 本文件固化「上下文窗口 × 压缩时机」的三条不变量（2026-10-04 立）。
//
// 背景（实测）：auto 线用绝对减法 `window − AutoCompactBufferTokens(13000)`，
// 硬停线用比例 `window × BlockPercent(0.97)` ⇒ 两者在 window ≈ 433,333 处交叉。
// 窗口更大时 auto 线跑到硬停线之上，而 CheckState 先判 StateBlocked、
// autoCompactDue（tui/blocked_handoff.go）开头又判一次 ⇒ **自动压缩永不触发，直接硬停**。
//
// ★ 判别性自证：本文件落地时（公式未改）TestBudgetAutoLinePrecedesBlockLine
// 在 524288 / 1048576 / 2097152 三格必须红。

// invariantWindows 覆盖：当前默认 / 临界两侧 / 大窗口 / 超大窗口。
var invariantWindows = []int{131072, 200000, 262144, 524288, 1048576, 2097152}

// TestBudgetAutoLinePrecedesBlockLine 断言 auto 线严格早于硬停线。
//
// 这是「自动压缩还能不能触发」的唯一充分条件：一旦 auto >= block，
// CheckState 会先返回 StateBlocked，checkAutoCompact 只打交接模板并 return nil，
// autoCompactDue 也因 Blocked 短路返回 false ⇒ 自动压缩成为死代码。
func TestBudgetAutoLinePrecedesBlockLine(t *testing.T) {
	for _, w := range invariantWindows {
		t.Run(fmt.Sprintf("window_%d", w), func(t *testing.T) {
			b := NewBudgetFromConfig(w, nil)
			auto := b.CompactThreshold()
			block := int(float64(w) * b.BlockPercent)
			t.Logf("window=%d hint=%d auto=%d block=%d postCompact=%d",
				w, b.HintThreshold(), auto, block, b.PostCompactMaxTokens())

			if auto <= 0 {
				t.Fatalf("auto 线 = %d，应为正数", auto)
			}
			if auto >= block {
				t.Errorf("★ auto 线 %d 未早于硬停线 %d（窗口 %d，超出 %d tokens）\n"+
					"  后果：CheckState 先判 StateBlocked ⇒ checkAutoCompact 只打硬停模板 return nil，"+
					"autoCompactDue 短路 ⇒ 自动压缩永不触发",
					auto, block, w, auto-block)
			}
		})
	}
}

// TestBudgetCompactionLeavesHeadroom 断言压缩后目标确实低于 auto 线且留出可观余量。
//
// 若 postCompact >= auto：压缩一结束就再次越线 ⇒ 压缩退化成「每轮挤牙膏」。
func TestBudgetCompactionLeavesHeadroom(t *testing.T) {
	for _, w := range invariantWindows {
		t.Run(fmt.Sprintf("window_%d", w), func(t *testing.T) {
			b := NewBudgetFromConfig(w, nil)
			auto := b.CompactThreshold()
			post := b.PostCompactMaxTokens()
			gap := auto - post
			t.Logf("window=%d auto=%d postCompact=%d headroom=%d (%.1f%% of window)",
				w, auto, post, gap, float64(gap)/float64(w)*100)

			if post <= 0 {
				t.Fatalf("压缩后目标 = %d，应为正数", post)
			}
			if post >= auto {
				t.Errorf("★ 压缩后目标 %d 不低于 auto 线 %d（窗口 %d）⇒ 压缩后立即再次越线",
					post, auto, w)
			}
			// 余量至少要能装下「窗口到硬停线的距离」，否则下一轮稍长就再次触发。
			minGap := int(float64(w) * (1 - b.BlockPercent))
			if gap < minGap {
				t.Errorf("压缩余量 %d < 窗口到硬停线距离 %d（窗口 %d）⇒ 余量不足以吸收一轮增长",
					gap, minGap, w)
			}
		})
	}
}

// TestBudgetThresholdsUnchangedForCurrentWindows 固化「窗口 ≤ 200K 行为逐值不变」。
//
// 本批修的是大窗口越界；现有用户的 128K/200K 行为**一格都不许动**。
// oldCompactThreshold / oldPostCompactMaxTokens 复刻改动前的实现，仅作对照用。
func TestBudgetThresholdsUnchangedForCurrentWindows(t *testing.T) {
	for _, w := range []int{8192, 32768, 131072, 200000} {
		t.Run(fmt.Sprintf("window_%d", w), func(t *testing.T) {
			b := NewBudgetFromConfig(w, nil)

			wantAuto := oldCompactThreshold(w, b.CompactPercent)
			if got := b.CompactThreshold(); got != wantAuto {
				t.Errorf("CompactThreshold = %d，改动前为 %d（窗口 %d 本应完全不变）", got, wantAuto, w)
			}

			wantPost := oldPostCompactMaxTokens(w, b.RecoveryBudget)
			if got := b.PostCompactMaxTokens(); got != wantPost {
				t.Errorf("PostCompactMaxTokens = %d，改动前为 %d（窗口 %d 本应完全不变）", got, wantPost, w)
			}
		})
	}
}

// oldCompactThreshold 复刻 2026-10-04 之前的 CompactThreshold 实现（只作 before 对照）。
func oldCompactThreshold(window int, compactPercent float64) int {
	if window <= 0 {
		return 0
	}
	threshold := window - DefaultAutoCompactBufferTokens
	if threshold > 0 {
		return threshold
	}
	percent := compactPercent
	if percent <= 0 || percent >= 1 {
		percent = DefaultCompactPercent
	}
	threshold = int(float64(window) * percent)
	if threshold < 1 {
		return 1
	}
	return threshold
}

// oldPostCompactMaxTokens 复刻 2026-10-04 之前的 PostCompactMaxTokens 实现（只作 before 对照）。
func oldPostCompactMaxTokens(window, recoveryBudget int) int {
	if window <= 0 {
		return 0
	}
	maxTokens := window - recoveryBudget
	if maxTokens > 0 {
		return maxTokens
	}
	maxTokens = window * 3 / 4
	if maxTokens < 1 {
		return 1
	}
	return maxTokens
}
