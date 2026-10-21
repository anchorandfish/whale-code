// Package failtier 是族A「fail-closed 的度」三档兜底的**单一实现**（术语 + 落点）。
//
// 依据：集中拍板_决策表与并行修复分组_2026-09-14.md §1 族A
//
//	任何"能不能做"的判定链，**失效时不得静默放行（L0 禁止）**，必须落到三档之一：
//	  T1 · 降级到下一强制点 + 显式告警   ← 缺的是**能力**（可选能力未启用 / 外部输入不可得）
//	  T2 · 拒绝该次操作                   ← 缺的是**判据**（结构化事实不可得）
//	  T3 · 拒绝启动 / 中断回合             ← 缺的是**保护器**（未初始化 / 完整性受损）
//
// 判定口诀："缺的是能力 → T1；缺的是判据 → T2；缺的是保护器 → T3。"
//
// 为什么独立成包（G0 出口条件之一）：
//   - 三档语义此前在 G0 的两个包（internal/sandbox / internal/security）里**各自口头描述**，
//     没有类型化载体 ⇒ 无法断言、无法上抛、无法被审计消费（"结构化事实必须有出口"）。
//   - 本包是**零依赖叶子包**：internal/sandbox 与 internal/security 都可导入，
//     且不引入 internal/security ⇄ internal/sandbox 的循环依赖。
//
// 使用约束：
//   - 生产码只能通过 Deficiency 上报降级事实，**不得**只写日志/散文关键词（见 R4 收口）。
//   - T1 必须给出 FallbackTo（降级后的下一强制点），否则降级无从验证。
package failtier

import "fmt"

// Tier 是族A 的三档兜底（顺序 = 严重度递增，便于取 max）。
type Tier int

const (
	// TierNone 无降级：判定链完整生效。
	TierNone Tier = iota
	// TierDegrade 是 T1：能力缺失 ⇒ 降级到下一强制点，且必须显式告警。
	TierDegrade
	// TierDeny 是 T2：判据不可得 ⇒ 拒绝该次操作（ask/deny）。
	TierDeny
	// TierRefuse 是 T3：保护器未初始化 / 安全制品受损 ⇒ 拒绝启动或中断回合。
	TierRefuse
)

// 三档的可读别名（族语义用 T1/T2/T3 表述，别名让调用点与决策表逐字对应）。
const (
	T1 = TierDegrade
	T2 = TierDeny
	T3 = TierRefuse
)

func (t Tier) String() string {
	switch t {
	case TierNone:
		return "none"
	case TierDegrade:
		return "T1-degrade"
	case TierDeny:
		return "T2-deny"
	case TierRefuse:
		return "T3-refuse"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// Deficiency 是一条**结构化**降级事实。
//
// 它是族A 的"出口"：判定链一旦没能完整生效，就必须产出一条 Deficiency，
// 由调用方（而非日志读者）决定后续动作。
type Deficiency struct {
	// Tier 降级档位（T1/T2/T3）。
	Tier Tier
	// Layer 判定链中失效的那一环（如 "sandbox.L2"、"sandbox.os-enter"、"security.checker"）。
	Layer string
	// Reason 人类可读原因（供告警与审计；**不得**作为机器判据 —— 机器判据是 Tier/Layer）。
	Reason string
	// FallbackTo 降级后的下一强制点（T1 必填；T2/T3 可为空）。
	FallbackTo string
}

// String 是 Deficiency 的单行表示（告警/审计用）。
func (d Deficiency) String() string {
	if d.FallbackTo != "" {
		return fmt.Sprintf("%s %s: %s (fallback -> %s)", d.Tier, d.Layer, d.Reason, d.FallbackTo)
	}
	return fmt.Sprintf("%s %s: %s", d.Tier, d.Layer, d.Reason)
}

// MaxTier 返回一组 Deficiency 中的最高档（无则 TierNone）。
// 用途：调用方只需一个"本次判定有多不可信"的标量即可决定姿态。
func MaxTier(ds []Deficiency) Tier {
	max := TierNone
	for _, d := range ds {
		if d.Tier > max {
			max = d.Tier
		}
	}
	return max
}
