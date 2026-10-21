// forgetting.go — H11/N27 Phase A A5 Ebbinghaus 衰减 + 后台扫描 + 启动补扫描。
// 衰减；落地：CalculateRetention 公式 exp(-Δt/τ)×(1+log(1+access_count))，τ=7天
// ；落地：min(1.0, retention)，防高频访问+短间隔 retention>1.0
// REF-外部-MemoryBank@arxiv:2305.10250+2026-07-15访问-Ebbinghaus 遗忘曲线；落地：CalculateRetention 借鉴改编
// +补扫描；落地：StartForgettingScanner 24h ticker + shouldCompensateScan(.last_scan_at>24h)
// SNAPSHOT: Ebbinghaus_v0.4.1={retention=min(1.0, exp(-Δt/7天)×(1+log(1+access_count))); thresholds={>=0.5正常, 0.1-0.5转unverified(内部通道), <0.1转contradicted+ValidUntil=now}; scan=后台24h+启动补扫描(.last_scan_at>24h触发); ApplyForgetting仅ephemeral}
// S18/G1 修复：Δt 的时间锚改为 forgettingAnchor（LastAccessedAt→UpdatedAt→ValidFrom/CreatedAt），
// 且 AccessCount/LastAccessedAt 改为持久化 frontmatter 字段；无时间锚时跳过衰减（不下 contradicted）。
// S18/G4 修复：.last_scan_at 目录 0700 / 文件 0600（原 0755/0644 偏离全包约定）。

package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"
)

// EbbinghausTau Ebbinghaus 衰减时间常数（τ=7 天，可配置）。
// 对齐 v0.4 §7.1：τ=7 天表示 1 周后 retention 衰减到 exp(-1)≈0.368。
const EbbinghausTau = 7 * 24 * time.Hour

// RetentionThresholdNormal retention >= 此值正常保留（不流转）。
const RetentionThresholdNormal = 0.5

// RetentionThresholdDecay retention >= 此值但 < Normal，弱化但保留（转 unverified 内部通道）。
const RetentionThresholdDecay = 0.1

// lastScanAtFileName 启动补扫描判断用的标记文件名（位于 scopeDir 下）。
const lastScanAtFileName = ".last_scan_at"

// CalculateRetention 计算 Ebbinghaus 衰减 retention 值。
// 对齐 v0.4.1 §7.1：归一化 min(1.0, ...)，防止高频访问+短间隔时 retention > 1.0。
// 参考 MemoryBank @arxiv:2305.10250。
//
// 公式：retention = exp(-Δt/τ) × (1 + log(1 + access_count))
// - Δt = time.Since(lastAccessedAt)
// - τ = 7 天（EbbinghausTau，可配置）
// - access_count = 访问次数（高频访问增强 retention）
//
// 归一化（审查报告优化 1 修复）：retention > 1.0 时强制为 1.0
// （高频访问 + 短间隔时 (1+log(1+100)) ≈ 5.6 会导致 retention > 1.0）。
func CalculateRetention(lastAccessedAt time.Time, accessCount int) float64 {
	deltaTime := time.Since(lastAccessedAt)
	tau := float64(EbbinghausTau)

	// exp(-Δt/τ) 衰减因子
	decay := math.Exp(-float64(deltaTime) / tau)

	// (1 + log(1 + access_count)) 访问频率增强因子
	accessFactor := 1 + math.Log(1+float64(accessCount))

	retention := decay * accessFactor

	// 归一化上限（审查报告优化 1 修复）
	if retention > 1.0 {
		retention = 1.0
	}

	// 下限保护：retention 不应为负（exp/log 都不应返回负，但防 float 异常）
	if retention < 0 {
		retention = 0
	}

	return retention
}

// forgettingAnchor 计算用于 Ebbinghaus 衰减的有效「上次活跃时间」锚点。
//
// S18/G1 修复背景：AccessCount/LastAccessedAt 历史上是 yaml:"-" 不持久化，
// 冷加载（新进程首次从磁盘读记忆）后 LastAccessedAt 为零值 → time.Since(zero)
// 极大 → decay≈0 → retention≈0 → 有效 ephemeral 记忆在首次后台扫描即被直接判
// contradicted（静默失效，数据/可用性风险）。
//
// 修复策略（fail-safe：宁可保留，不误杀）：
// 1. 优先用已持久化的 LastAccessedAt（真实访问数据）
// 2. 为零则回退到持久化的 UpdatedAt（最近一次写入视作最近一次活跃）
// 3. 再为零则回退到 ValidFrom / CreatedAt（写入时必然设置）
// 4. 全部缺失（无任何时间锚）时返回 ok=false，调用方跳过衰减，
// 绝不在「无证据」的情况下得出 contradicted 结论。
func forgettingAnchor(entry *MemoryEntry) (time.Time, bool) {
	if entry == nil {
		return time.Time{}, false
	}
	if !entry.LastAccessedAt.IsZero() {
		return entry.LastAccessedAt, true
	}
	if !entry.UpdatedAt.IsZero() {
		return entry.UpdatedAt, true
	}
	if t := entry.ValidFromOrCreatedAt(); !t.IsZero() {
		return t, true
	}
	return time.Time{}, false
}

// ApplyForgetting 应用遗忘策略，更新 ephemeral 记忆的 verification 状态。
// 仅 ephemeral 走 Ebbinghaus（stable/capability/procedural 不衰减）。
//
// 阈值：
// - retention >= 0.5：正常保留，不流转
// - 0.1 <= retention < 0.5：弱化但保留，verification→unverified（内部通道，绕过 isValidTransition）
// - retention < 0.1：待清理，verification→contradicted + ValidUntil=now
//
// 注：verified→unverified 仅本函数内部触发，公共 API TransitionVerification 拒绝此流转。
// 注：已 contradicted 的 ephemeral 不被降级为 unverified（防止 contradicted 状态被覆盖）。
func ApplyForgetting(entry *MemoryEntry) error {
	if entry == nil {
		return errors.New("ApplyForgetting: entry is nil")
	}

	// 仅 ephemeral 走 Ebbinghaus（stable/capability/procedural 不衰减）
	if entry.TrustLevel != TrustEphemeral {
		return nil
	}

	// S18/G1：先取有效时间锚（可能回退到持久化时间字段），再算 retention。
	anchor, ok := forgettingAnchor(entry)
	if !ok {
		// 无任何可用时间锚（极旧文件且无 CreatedAt/UpdatedAt）：
		// 无证据证明其失效，fail-safe 跳过衰减，绝不在证据缺失时下 contradicted 结论。
		return nil
	}

	retention := CalculateRetention(anchor, entry.AccessCount)

	switch {
	case retention >= RetentionThresholdNormal:
		// 正常保留，不流转。
		// S18/G1：仍持久化一次，把内存中刷新过的访问热度落盘（扫描按天调度，写放大有界），
		// 使 LastAccessedAt/AccessCount 真正成为「持久化访问数据」而非进程内瞬态。
		return saveEntry(entry)
	case retention >= RetentionThresholdDecay:
		// 弱化但保留：转 unverified（内部通道，绕过 isValidTransition）
		// 防护：已 contradicted 的不被降级为 unverified（防止终态被覆盖）
		if entry.DefaultVerification() != VerificationContradicted {
			entry.Verification = VerificationUnverified
		}
		return saveEntry(entry)
	default:
		// 待清理：转 contradicted + ValidUntil=now。
		//
		// E3 幂等性修复（归档 GC 的前提）：**首次**失效才记录时间戳。
		// 修复前每次扫描都会把 ValidUntil 重写为 now，而归档判据以 ValidUntil
		// 为失效锚（见 archive.go shouldArchiveEntry）⇒ ValidUntil 永远停在
		// 「刚刚」，宽限期永远不会届满，磁盘只增不减（E3 描述的现象）。
		if entry.DefaultVerification() == VerificationContradicted && entry.ValidUntil != nil {
			// 已处于终态且有失效时间戳：不重写（无状态变化，也避免每轮扫描写放大）。
			return nil
		}
		entry.Verification = VerificationContradicted
		if entry.ValidUntil == nil {
			now := time.Now()
			entry.ValidUntil = &now
		}
		return saveEntry(entry)
	}
}

// StartForgettingScanner 启动后台扫描 goroutine + 启动时补扫描。
// 对齐 v0.4.1 §7.1：每天 1 次扫描 + 启动时检查 .last_scan_at 补扫描（审查报告优化 5 修复）。
//
// 补扫描条件（审查报告优化 5）：.last_scan_at 不存在或距上次扫描 > 24h（用户不足 24h 退出场景）。
// 阻塞行为：本函数启动后台 goroutine 后立即返回，不阻塞调用方。
//
// 参数：
// - ctx: 用于取消后台 goroutine（ctx.Done() 时退出）
// - scopeDir: 记忆目录路径（扫描 .md 文件 + 写 .last_scan_at）
func StartForgettingScanner(ctx context.Context, scopeDir string) {
	// 启动时补扫描（防用户不足 24h 退出导致漏扫描）
	if shouldCompensateScan(scopeDir) {
		if err := scanOnce(scopeDir); err != nil {
			log.Printf("WARN: forgetting scanner compensate scan failed: %v", err)
		}
		if err := updateLastScanAt(scopeDir); err != nil {
			log.Printf("WARN: forgetting scanner update last_scan_at failed: %v", err)
		}
	}

	// 后台周期扫描
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := scanOnce(scopeDir); err != nil {
				log.Printf("WARN: forgetting scanner periodic scan failed: %v", err)
				// 扫描失败不更新 .last_scan_at，下一轮重试
				continue
			}
			if err := updateLastScanAt(scopeDir); err != nil {
				log.Printf("WARN: forgetting scanner update last_scan_at failed: %v", err)
			}
		}
	}
}

// shouldCompensateScan 检查是否需要补扫描。
// 条件：.last_scan_at 不存在或距上次扫描 > 24h。
// 返回 true 时启动时应执行补扫描。
func shouldCompensateScan(scopeDir string) bool {
	lastScanAt, err := readLastScanAt(scopeDir)
	if err != nil {
		// 文件不存在或读取失败：需要补扫描
		return true
	}
	// 距上次扫描 > 24h：需要补扫描
	return time.Since(lastScanAt) > 24*time.Hour
}

// scanOnce 执行一次扫描：先对 ephemeral 走 Ebbinghaus 衰减，再执行归档式 GC。
//
// E3（归档式 GC，不硬删）：扫描是**唯一**的周期性清理入口（StartForgettingScanner
// 24h ticker + 启动补扫描），故归档步骤挂在这里，使「磁盘只增不减」有确定出口。
// 归档判据是结构化事实（contradicted ∧ 失效时间超过宽限期），见 archive.go。
//
// 单个 entry 失败不阻塞其余（防一个坏文件导致全扫描失败）。
func scanOnce(scopeDir string) error {
	if scopeDir == "" {
		return errors.New("scanOnce: scopeDir is empty")
	}

	// 路径穿越防护
	cleaned := filepath.Clean(scopeDir)
	entries, err := os.ReadDir(cleaned)
	if err != nil {
		return fmt.Errorf("scanOnce: read dir %s: %w", cleaned, err)
	}

	var lastError error
	processedCount := 0

	for _, fi := range entries {
		if fi.IsDir() {
			continue
		}
		name := fi.Name()
		if filepath.Ext(name) != ".md" {
			continue
		}

		// 通过 capabilityStore 查找已加载的 entry（Phase A stub：内存 map）
		// Phase B scope.go 落地后改为从文件加载
		entry, loadErr := loadEntryByName(stripExt(name))
		if loadErr != nil {
			// entry 不在内存 map 中：跳过（Phase B 会从文件加载）
			continue
		}

		// 仅 ephemeral 走 Ebbinghaus 衰减；其余信任级别不衰减，
		// 但仍需参与下面的归档判定（例如被 /memory invalidate 判 contradicted 的 stable 条目）。
		if entry.TrustLevel == TrustEphemeral {
			if err := ApplyForgetting(entry); err != nil {
				log.Printf("WARN: ApplyForgetting failed for %s: %v", name, err)
				lastError = err
				continue
			}
			processedCount++
		}

		// E3 归档式 GC：失效且过了宽限期的记忆从活动目录移入 _archive/（不硬删）。
		if shouldArchiveEntry(entry, time.Now()) {
			archived, archErr := archiveFile(cleaned, filepath.Join(cleaned, name))
			if archErr != nil {
				log.Printf("WARN: archive expired memory %s failed: %v", name, archErr)
				lastError = archErr
				continue
			}
			log.Printf("[memory] archived expired memory %s -> %s", name, archived)
		}
	}

	_ = processedCount
	return lastError
}

// updateLastScanAt 更新 .last_scan_at 时间戳。
// 写入当前时间（RFC3339 格式）到 scopeDir/.last_scan_at 文件。
func updateLastScanAt(scopeDir string) error {
	if scopeDir == "" {
		return errors.New("updateLastScanAt: scopeDir is empty")
	}

	cleaned := filepath.Clean(scopeDir)
	path := filepath.Join(cleaned, lastScanAtFileName)

	// 确保目录存在。
	// S18/G4 修复：目录 0700、文件 0600，与全包约定（scope/probe/verification 均 0600/0700）对齐。
	// 修复前为 0755/0644（世界可读），标记文件虽非敏感正文，但权限卫生应统一，
	// 避免 _user/ 与 project scope 目录被同机其他用户枚举。
	if err := os.MkdirAll(cleaned, 0o700); err != nil {
		return fmt.Errorf("updateLastScanAt: mkdir %s: %w", cleaned, err)
	}

	content := time.Now().Format(time.RFC3339)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("updateLastScanAt: write %s: %w", path, err)
	}
	return nil
}

// readLastScanAt 读取 .last_scan_at 时间戳。
// 文件不存在返回 error（调用方据此判断是否需要补扫描）。
func readLastScanAt(scopeDir string) (time.Time, error) {
	cleaned := filepath.Clean(scopeDir)
	path := filepath.Join(cleaned, lastScanAtFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("readLastScanAt: read %s: %w", path, err)
	}

	t, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		return time.Time{}, fmt.Errorf("readLastScanAt: parse %q: %w", string(data), err)
	}
	return t, nil
}

// stripExt 移除文件扩展名（仅 .md）。
func stripExt(name string) string {
	ext := filepath.Ext(name)
	if ext == ".md" {
		return name[:len(name)-len(ext)]
	}
	return name
}
