// pattern_ttl_scanner.go — H12 自我进化系统 §2.4 Pattern TTL 扫描器（v0.2 新增）。
// 设计说明 §2.4 + §3.5；落地：扫描并淘汰过期 Pattern
// v0.2；落地：替代 v0.1 假设的 ApplyForgetting 做 Pattern TTL
// SNAPSHOT: pkg=internal/orchestrator; type=PatternTTLScanner; flow={遍历global+project→按LastUsedAt+VerifiedState判断TTL→Save持久化}; ttl={unverified+30天→deprecated, provisionally_verified+60天→unverified, verified+90天→provisionally_verified, deprecated:终态不流转}; pitfall=独立struct永久保留不参与扫描; degraded={ListByScope/Save失败→warn不阻断}
package orchestrator

import (
	"context"
	"time"
)

// PatternTTLScannerUnverifiedDays unverified 30 天未用 → deprecated（§2.4 TTL 规则表）。
const PatternTTLScannerUnverifiedDays = 30

// PatternTTLScannerProvisionallyVerifiedDays provisionally_verified 60 天未用 → unverified。
const PatternTTLScannerProvisionallyVerifiedDays = 60

// PatternTTLScannerVerifiedDays verified 90 天未用 → provisionally_verified。
const PatternTTLScannerVerifiedDays = 90

// PatternTTLScanner Pattern TTL 扫描器（v0.2 新增，替代 v0.1 假设的 ApplyForgetting）。
// 基于 LastUsedAt + VerifiedState 对 Pattern 做 TTL 淘汰。
//
// TTL 规则（§2.4）：
// - unverified + 30 天未复用 → deprecated（未验证 Pattern 长期未用，无价值）
// - provisionally_verified + 60 天未复用 → unverified（临时验证 Pattern 长期未用，需重新验证）
// - verified + 90 天未复用 → provisionally_verified（长期未用可能已过时）
// - deprecated: 终态，不流转
// - Pitfall: 独立 struct，永久保留，不参与 TTL（§2.4）
//
// 不复用声明（v0.2 修正致命漏洞 4）：
// - v0.1 曾假设直接复用 memory 包 ApplyForgetting 做 Pattern TTL
// - v0.2 删除该假设：ApplyForgetting 仅对 MemoryEntry.TrustLevel==TrustEphemeral 生效，
// Pattern 无 TrustLevel 字段（experience.go:76-92），跨包语义不匹配
// - 本文件独立实现 Pattern TTL，全部在 orchestrator 包内
type PatternTTLScanner struct {
	experience *ExperienceStore
	nowFunc    func() time.Time
}

// NewPatternTTLScanner 构造 TTL 扫描器。
// experience 必填，空时 Scan 直接返回 nil（降级）。
func NewPatternTTLScanner(experience *ExperienceStore) *PatternTTLScanner {
	return &PatternTTLScanner{
		experience: experience,
		nowFunc:    time.Now,
	}
}

// SetNowFunc 注入时间函数（测试用，便于固定时间）。
func (s *PatternTTLScanner) SetNowFunc(fn func() time.Time) {
	s.nowFunc = fn
}

// Scan 扫描并淘汰过期 Pattern（后台 24h 周期调用，§2.4 + §3.5）。
// 流程：
//
//	① ctx 校验 + experience 非空校验
//	② 遍历 ScopeGlobal + ScopeProject，调 ListByScope
//	③ 按 LastUsedAt + VerifiedState 判断 TTL
//	④ Pitfall 独立 struct 永久保留，不参与扫描
//	⑤ 调 ExperienceStore.Save 持久化状态变更
//
// 降级：ListByScope/Save 失败 → warn 不阻断，下次后台扫描重试
func (s *PatternTTLScanner) Scan(ctx context.Context) error {
	// ① ctx 校验
	if err := ctx.Err(); err != nil {
		return err
	}
	// ② experience 校验
	if s == nil || s.experience == nil {
		return nil // 降级
	}

	now := s.nowFunc()

	// ③ 遍历 global + project 两个 scope
	for _, scope := range []string{ScopeGlobal, ScopeProject} {
		patterns, err := s.experience.ListByScope(scope)
		if err != nil {
			// 降级：ListByScope 失败 warn 不阻断，尝试下一个 scope
			continue
		}
		for _, p := range patterns {
			// ④ 应用 TTL 规则
			newState, ok := s.applyTTL(p, now)
			if !ok {
				continue
			}
			// ⑤ 持久化状态变更
			p.VerifiedState = newState
			p.VerifiedAt = now.UTC().Format(time.RFC3339)
			// 降级：Save 失败 warn 不阻断，下次后台扫描重试
			_ = s.experience.Save(p)
		}
	}
	// Pitfall 永久保留，不参与扫描（§2.4）
	return nil
}

// applyTTL 对单个 Pattern 应用 TTL 规则，返回新状态（§2.4 TTL 规则表）。
// 返回 (newState, true) 表示流转；(原状态, false) 表示不流转。
// 判定依据：LastUsedAt 字段（ISO8601），未解析成功则不流转。
func (s *PatternTTLScanner) applyTTL(p *Pattern, now time.Time) (string, bool) {
	if p == nil {
		return "", false
	}
	// LastUsedAt 空视为从未使用，不触发 TTL（避免误淘汰新 Pattern）
	if p.LastUsedAt == "" {
		return "", false
	}
	lastUsedAt, err := time.Parse(time.RFC3339, p.LastUsedAt)
	if err != nil {
		return "", false
	}
	sinceLastUse := now.Sub(lastUsedAt)

	switch p.VerifiedState {
	case VerifiedStateUnverified:
		// 30 天未复用 → deprecated
		if sinceLastUse > time.Duration(PatternTTLScannerUnverifiedDays)*24*time.Hour {
			return VerifiedStateDeprecated, true
		}

	case VerifiedStateProvisionallyVerified:
		// 60 天未复用 → unverified（重新验证）
		if sinceLastUse > time.Duration(PatternTTLScannerProvisionallyVerifiedDays)*24*time.Hour {
			return VerifiedStateUnverified, true
		}

	case VerifiedStateVerified:
		// 90 天未复用 → provisionally_verified
		if sinceLastUse > time.Duration(PatternTTLScannerVerifiedDays)*24*time.Hour {
			return VerifiedStateProvisionallyVerified, true
		}

	case VerifiedStateDeprecated:
		// deprecated 是终态，不流转（§2.4）
		return "", false
	}

	return "", false
}
