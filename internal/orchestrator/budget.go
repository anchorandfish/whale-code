// budget.go — 模块 8 预算追踪器 BudgetTracker（H10 Phase 1 编排层 §3.4 / 总纲 §8.3 O2）。
// 硬预算（O2 修订）；落地：双层预算 + 80%/100% 三档介入
// 是三因子之一；落地：CheckSubtaskBudget 校验 TaskContext.BudgetHint
// SNAPSHOT: pkg=internal/orchestrator; type=BudgetTracker; layers={total,subtask}; total_cap=token; subtask_cap=TaskContext.BudgetHint.TokensCap; thresholds={80%通知,100%暂停}; budget_exceeded=独立字段不覆盖l0_pass; usage_source=provider 真报 token
//
// ★ 2026-10-09：本追踪器的**预算轴 = provider 真报的 token**。
// 原先它按"我们自己编的单价 × token = 美分"做决策 —— 那是个**代理，不是真值**，
// 且混币种（见 内部工作回执 §6）。
// 「不算价钱」铁律（2026-10-05 拍板）⇒ 删掉「钱」这一维，节流阀改架在 token 这一维上。
// 功能（80%/100% 三档介入）保留，语义变真。

package orchestrator

import (
	"fmt"
	"sync"
)

// BudgetThreshold80 总预算 80% 通知阈值（总纲 §8.3 三档介入）。
const BudgetThreshold80 = 80

// BudgetThreshold100 总预算 100% 暂停阈值（总纲 §8.3 三档介入）。
const BudgetThreshold100 = 100

// DefaultTotalTokensCap 编排任务默认总 token 上限（单一声明，main.go 与 orchestrate_command.go 同源）。
//
// ★ 2026-10-09：替换原先两处硬编码的 `NewBudgetTracker(100000)`（"默认 1000 美元 = 100000 美分"）。
// 原来那个 100000 是**美分**、背后是"我们自己编的价"；现在它是**token** ——
// 属于 provider 真报的同一根轴，不再需要币种换算，也不再随模型价位漂移。
// 取 2,000,000（≈ balanced 档 200000/subtask × 10 个子任务的量级），是一个**真实、可解释**的上限。
const DefaultTotalTokensCap int64 = 2_000_000

// BudgetTracker 预算追踪器（总纲 §8.3 O2，双层预算）。
// 双层：① 编排任务总预算 ② 子任务预算（TaskContext.BudgetHint）。
// 用量来源：**provider 上报的 token**（usage.InputTokens + OutputTokens + cache 系），
// 不引入任何"自定价 × token"的派生字段（PIT-002 防护）。
type BudgetTracker struct {
	totalTokensCap int64 // 编排任务总 token 上限
	usedTokens     int64 // 已用 token（由 RecordUsage 从 provider 真报用量累加）
	mu             sync.Mutex
	exceeded       bool // 是否已超总预算（100%）
}

// NewBudgetTracker 构造预算追踪器（总预算单位：token，总纲 §8.3）。
// totalTokensCap 必须 > 0，否则返回最小可用 tracker 且恒 exceeded（防零预算导致除零）。
func NewBudgetTracker(totalTokensCap int64) *BudgetTracker {
	if totalTokensCap <= 0 {
		// 零预算时返回最小可用 tracker（token 校验永远 exceeded）
		return &BudgetTracker{
			totalTokensCap: 1,
			exceeded:       true,
		}
	}
	return &BudgetTracker{
		totalTokensCap: totalTokensCap,
	}
}

// RecordUsage 记录子任务用量（**provider 真报的 token**）。
// tokens 必须来自 provider 上报的 usage（不改 N2 签名，总纲 §8.3）。
// 负值拒绝（防倒扣），零值允许（缓存命中 / 空回复）。
func (b *BudgetTracker) RecordUsage(tokens int64) error {
	if tokens < 0 {
		return fmt.Errorf("budget: token usage must not be negative (got %d)", tokens)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.usedTokens += tokens

	// 检查是否超 100%
	if b.totalTokensCap > 0 {
		usedPct := b.usedTokens * 100 / b.totalTokensCap
		if usedPct >= BudgetThreshold100 {
			b.exceeded = true
		}
	}
	return nil
}

// CheckBudget 检查预算状态（总纲 §8.3 三档介入）。
// 返回：（已用百分比, 是否超 80%, 是否超 100%）。
// over80=true → 通知用户「预算已用 80%，是否继续？」
// over100=true → 暂停所有未派发子任务，问用户「加预算 / 终止 / 降级模型重跑」。
func (b *BudgetTracker) CheckBudget() (usedPct int, over80 bool, over100 bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.totalTokensCap <= 0 {
		return 0, false, false
	}

	usedPct = int(b.usedTokens * 100 / b.totalTokensCap)
	over80 = usedPct >= BudgetThreshold80
	over100 = usedPct >= BudgetThreshold100 || b.exceeded
	return
}

// CheckSubtaskBudget 检查子任务预算（TaskContext.BudgetHint.TokensCap，总纲 §8.3）。
// 子任务超 tokens_cap → 返回 exceeded=true（独立字段，写入 quality.jsonl，不覆盖 l0_pass）。
// 审查 v1 修订：budget_exceeded 不覆盖 l0_pass——L0 反映任务完成度，budget_exceeded 反映成本合规性，两者解耦。
// actualTokens 为 0 或负值时不视为超限（空回复 / 未计费）。
// ★ 2026-10-09：判据从 BudgetHint.CostCapCents 改为 BudgetHint.TokensCap（删掉「钱」这一维）。
func (b *BudgetTracker) CheckSubtaskBudget(tc TaskContext, actualTokens int64) (exceeded bool) {
	if actualTokens <= 0 {
		return false
	}
	// TokensCap 为 0 表示无硬上限（不限用量）
	if tc.BudgetHint.TokensCap <= 0 {
		return false
	}
	return actualTokens > int64(tc.BudgetHint.TokensCap)
}

// IsExceeded 是否已超总预算（100%，总纲 §8.3）。
func (b *BudgetTracker) IsExceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

// UsedTokens 返回已用 token（仅用于诊断）。
func (b *BudgetTracker) UsedTokens() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usedTokens
}

// TotalTokensCap 返回总 token 上限（仅用于诊断）。
func (b *BudgetTracker) TotalTokensCap() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalTokensCap
}
