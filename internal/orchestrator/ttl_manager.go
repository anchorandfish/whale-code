// ttl_manager.go — H10 Phase 5 桶 5 TTL 管理器（落地 §6 三类 TTL 规则）。
// REF-外部-LangGraph@https://langchain-ai.github.io/langgraph/concepts/persistence/+2026-07-15访问-Time travel + checkpoint 老化；落地：画像表 90d 老化
// line 826-830-隐式反馈 7 天时效；落地：BudgetState 7 天清理
// + checkpoint；落地：BudgetState.Snapshot 持久化 + 7d 清理
// 7d 清理；落地：ApplyBudgetStateTTL 扫描 stateDir
// 90d stale / 180d 清理；落地：ApplyProfileTTL + SweepStaleProfiles
// 权重 N=20 版本归档；落地：ApplyL1WeightsTTL 触发归档检查
// 变更三段式（BudgetState 7d 清理需 stateDir，不改 BudgetTrackerExt 签名）
// SNAPSHOT: pkg=internal/orchestrator; type=TTLManager+ProfileSweeper; ttl={budget_state:7d清理(stateDir扫描),profile:90d stale标记+180d内存移除,l1_weights:N=20版本归档}; interval=1h默认; concurrency=sync.Mutex(sweepMu)+sync.RWMutex(profileSweeper复用); persist_failure=内存保留+告警+完成后重写; BASE变更=无(不改已有签名)

package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TTLBudgetStateMaxAge BudgetState 快照最大保留时长（7 天，§6.1）。
// 编排任务完成后 7 天清理 state.json。
const TTLBudgetStateMaxAge = 7 * 24 * time.Hour

// TTLProfileStaleThreshold 画像表条目老化阈值（90 天，§6.2）。
// UpdatedAt 超过 90 天 → 标记 stale（保留内存但降级使用）。
const TTLProfileStaleThreshold = 90 * 24 * time.Hour

// TTLProfileRemoveThreshold 画像表条目移除阈值（180 天，§6.2）。
// EffectiveSampleCount==0 且 UpdatedAt 超过 180 天 → 从内存移除。
const TTLProfileRemoveThreshold = 180 * 24 * time.Hour

// TTLManagerDefaultInterval TTL 扫描默认间隔（1h，§6）。
const TTLManagerDefaultInterval = 1 * time.Hour

// ProfileSweeper 画像表老化清理接口（Phase 5 §6.2）。
// *profileUpdater 实现此接口（在 ttl_manager.go 中添加 SweepStaleProfiles 方法）。
// 落地 §6.2：EffectiveSampleCount<10 || UpdatedAt>90d → 标记 stale；==0 && >180d → 内存移除。
type ProfileSweeper interface {
	// SweepStaleProfiles 扫描画像表，标记 stale + 移除过期条目。
	// now 是当前时间（测试可注入）。
	// 返回 (staleCount, removedCount)。
	// staleCount：EffectiveSampleCount<10 或 UpdatedAt>90d 的条目数（标记但不移除）。
	// removedCount：EffectiveSampleCount==0 且 UpdatedAt>180d 的条目数（从内存移除）。
	SweepStaleProfiles(now time.Time) (staleCount, removedCount int)
}

// SweepStaleProfiles 扫描画像表，标记 stale + 移除过期条目（Phase 5 §6.2）。
// 落地 §6.2：
// - EffectiveSampleCount<10 || UpdatedAt>90d → 标记 stale（保留内存，调用方降级使用）
// - EffectiveSampleCount==0 && UpdatedAt>180d → 从内存移除
//
// 注意：ModelProfile 无 stale 标志字段，本方法仅统计 staleCount 不实际标记。
// 实际降级由调用方（路由器）通过 GetProfile 返回 ErrColdStart 实现。
// 此方法在 ttl_manager.go 中给 *profileUpdater 添加（Go 允许同 package 不同文件添加方法）。
func (u *profileUpdater) SweepStaleProfiles(now time.Time) (staleCount, removedCount int) {
	u.mu.Lock()
	defer u.mu.Unlock()

	var toRemove []string
	for key, profile := range u.profiles {
		// 解析 UpdatedAt
		var age time.Duration
		if profile.UpdatedAt != "" {
			if ts, err := time.Parse(time.RFC3339, profile.UpdatedAt); err == nil {
				age = now.Sub(ts)
			}
		}

		// 移除条件：EffectiveSampleCount==0 且 UpdatedAt>180d
		if profile.EffectiveSampleCount == 0 && age > TTLProfileRemoveThreshold {
			toRemove = append(toRemove, key)
			removedCount++
			continue
		}

		// stale 条件：EffectiveSampleCount<10 或 UpdatedAt>90d
		if profile.EffectiveSampleCount < EffectiveSampleCountThreshold || age > TTLProfileStaleThreshold {
			staleCount++
		}
	}

	// 执行移除
	for _, key := range toRemove {
		delete(u.profiles, key)
	}
	return staleCount, removedCount
}

// TTLManager Phase 5 三类 TTL 管理器（落地 §6）。
// 职责：
// - ApplyBudgetStateTTL：BudgetState 快照 7 天清理（扫描 stateDir）
// - ApplyProfileTTL：画像表条目 90d 老化 + 180d 移除
// - ApplyL1WeightsTTL：L1 权重版本 N=20 归档
//
// 并发安全：
// - sweepMu sync.Mutex 保护 SweepAll 并发扫描（防多 goroutine 同时扫描）
// - profileSweeper 复用 profileUpdater 的 sync.RWMutex
//
// 与 BudgetTrackerExt/ProfileUpdater/L1ScorerExt 共存：
// - budgetTracker 用 *BudgetTrackerExt（Phase 5 扩展版，有 statePath）
// - profileSweeper 用 ProfileSweeper interface（*profileUpdater 实现）
// - l1ScorerExt 用 *L1ScorerExt
type TTLManager struct {
	budgetTracker  *BudgetTrackerExt // Phase 5 扩展版预算追踪器（statePath 清理用）
	profileSweeper ProfileSweeper    // 画像表老化清理（*profileUpdater 实现）
	l1ScorerExt    *L1ScorerExt      // L1 权重版本归档

	stateDir string        // state.json 所在目录（BudgetState 7d 清理扫描用）
	interval time.Duration // 扫描间隔（默认 1h）
	stopCh   chan struct{} // 停止后台扫描
	stopped  bool          // 是否已停止（防重复 close stopCh）

	// sweepMu 保护 SweepAll 并发扫描（防多 goroutine 同时触发）
	sweepMu sync.Mutex

	// nowFunc 用于测试注入时间（默认 time.Now）
	nowFunc func() time.Time
}

// NewTTLManager 创建 TTL 管理器。
// budgetTracker 可为 nil（跳过 BudgetState TTL）；profileSweeper 可为 nil（跳过画像表 TTL）；
// l1ScorerExt 可为 nil（跳过 L1 权重 TTL）；stateDir 为 BudgetState 文件目录（""=不清理 BudgetState）。
func NewTTLManager(budgetTracker *BudgetTrackerExt, profileSweeper ProfileSweeper, l1ScorerExt *L1ScorerExt, stateDir string) *TTLManager {
	return &TTLManager{
		budgetTracker:  budgetTracker,
		profileSweeper: profileSweeper,
		l1ScorerExt:    l1ScorerExt,
		stateDir:       stateDir,
		interval:       TTLManagerDefaultInterval,
		stopCh:         make(chan struct{}),
		nowFunc:        time.Now,
	}
}

// SetNowFunc 注入时间函数（测试用，便于控制 TTL 阈值）。
func (m *TTLManager) SetNowFunc(f func() time.Time) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	m.nowFunc = f
}

// now 返回当前时间（统一入口，便于测试注入）。
// 注意：本方法不获取 sweepMu 锁（调用方通常已持有 sweepMu，避免重入死锁）。
// nowFunc 的读写由 SetNowFunc（持锁写）+ 本方法（无锁读）组成，
// 测试场景下 SetNowFunc 在 Apply* 之前调用，无并发竞争。
func (m *TTLManager) now() time.Time {
	f := m.nowFunc
	if f == nil {
		return time.Now()
	}
	return f()
}

// SetInterval 设置扫描间隔（测试用，缩短间隔便于快速验证）。
func (m *TTLManager) SetInterval(d time.Duration) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	m.interval = d
}

// ApplyBudgetStateTTL BudgetState 快照 TTL（落地 §6.1）。
// 保留：每次派发前 Snapshot 到 state.json（BudgetTrackerExt.persistState 已实现）。
// 清理：编排任务完成后 7 天清理 state.json（扫描 stateDir 下 *.json，mtime 超过 7 天删除）。
// 恢复：断点续传时从 state.json 加载（BudgetTrackerExt 持久化已实现）。
// 持久化失败降级（§6.1 v0.3 建议 5）：内存保留 + 告警 + 完成后强制重写（BudgetTrackerExt.ForceRewriteState 已实现）。
//
// 返回清理的文件数 + error（扫描失败时返回）。
func (m *TTLManager) ApplyBudgetStateTTL() (cleanedCount int, err error) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()

	if m.stateDir == "" {
		return 0, nil
	}

	now := m.now()
	entries, err := os.ReadDir(m.stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // 目录不存在视为无文件需清理
		}
		return 0, fmt.Errorf("ttl_manager: ApplyBudgetStateTTL ReadDir %s: %w", m.stateDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 仅清理 *.json 文件（state.json + 临时文件）
		if filepath.Ext(name) != ".json" {
			continue
		}
		// 跳过 .tmp 文件（原子写入临时文件，可能残留）
		if filepath.Ext(name) == ".tmp" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue // 获取 info 失败跳过
		}

		// mtime 超过 7 天 → 删除
		age := now.Sub(info.ModTime())
		if age > TTLBudgetStateMaxAge {
			path := filepath.Join(m.stateDir, name)
			if rmErr := os.Remove(path); rmErr != nil {
				// 删除失败不阻塞（best-effort），记日志继续
				continue
			}
			cleanedCount++
		}
	}

	// 若 budgetTracker 持久化失败，触发强制重写（§6.1 v0.3 建议 5）
	if m.budgetTracker != nil && m.budgetTracker.IsPersistFailed() {
		if rwErr := m.budgetTracker.ForceRewriteState(); rwErr != nil {
			// 重写失败不阻塞（best-effort），返回 error 供调用方记录
			return cleanedCount, fmt.Errorf("ttl_manager: ForceRewriteState failed: %w", rwErr)
		}
	}

	return cleanedCount, nil
}

// ApplyProfileTTL 画像表条目老化（落地 §6.2）。
// 老化阈值：EffectiveSampleCount<10 或 UpdatedAt>90 天 → 标记 stale（统计，不移除）。
// 清理：EffectiveSampleCount==0 且 UpdatedAt>180 天 → 从内存移除。
// 返回 (staleCount, removedCount) + error（profileSweeper 为 nil 时返回 0,0,nil）。
func (m *TTLManager) ApplyProfileTTL() (staleCount, removedCount int, err error) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()

	if m.profileSweeper == nil {
		return 0, 0, nil
	}

	now := m.now()
	stale, removed := m.profileSweeper.SweepStaleProfiles(now)
	return stale, removed, nil
}

// ApplyL1WeightsTTL L1 权重版本 TTL（落地 §6.3）。
// 版本保留：保留最近 N=20 版本（L1ScorerExt.saveVersioned 已自动归档）。
// 回滚：新版本导致 L1/L2 相关性下降 → 回滚（L1ScorerExt.RollbackWeights 已实现）。
// 归档：超出 N=20 的旧版本仅保留 Version+Reason（L1ScorerExt.writeArchiveFile 已实现）。
// 返回当前归档数 + 是否超出上限（true=需触发归档清理）。
func (m *TTLManager) ApplyL1WeightsTTL() (archiveCount int, overflow bool, err error) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()

	if m.l1ScorerExt == nil {
		return 0, false, nil
	}

	archiveCount = m.l1ScorerExt.ArchiveCount()
	// 达到上限即视为溢出风险（saveVersioned 已自动归档超出部分到 archive.json，
	// 但 archiveCount==L1WeightsArchiveLimit 表示已满，下次保存将触发文件归档）
	overflow = archiveCount >= L1WeightsArchiveLimit
	return archiveCount, overflow, nil
}

// SweepAll 执行一次全量 TTL 扫描（三类 TTL 都执行）。
// 返回 SweepResult + error（任一失败时返回）。
// 并发安全：sweepMu 保护（防多 goroutine 同时扫描）。
func (m *TTLManager) SweepAll() (SweepResult, error) {
	result := SweepResult{}

	cleaned, err := m.ApplyBudgetStateTTL()
	if err != nil {
		return result, fmt.Errorf("ttl_manager: SweepAll ApplyBudgetStateTTL: %w", err)
	}
	result.BudgetStateCleaned = cleaned

	stale, removed, err := m.ApplyProfileTTL()
	if err != nil {
		return result, fmt.Errorf("ttl_manager: SweepAll ApplyProfileTTL: %w", err)
	}
	result.ProfileStale = stale
	result.ProfileRemoved = removed

	archive, overflow, err := m.ApplyL1WeightsTTL()
	if err != nil {
		return result, fmt.Errorf("ttl_manager: SweepAll ApplyL1WeightsTTL: %w", err)
	}
	result.L1ArchiveCount = archive
	result.L1Overflow = overflow

	return result, nil
}

// SweepResult SweepAll 扫描结果。
type SweepResult struct {
	BudgetStateCleaned int  // 清理的 BudgetState 文件数
	ProfileStale       int  // 标记 stale 的画像表条目数
	ProfileRemoved     int  // 移除的画像表条目数
	L1ArchiveCount     int  // L1 权重当前归档数
	L1Overflow         bool // L1 权重归档是否超出 N=20
}

// Start 启动后台周期扫描（默认 1h 间隔）。
// 调用 Stop() 停止。
// 阻塞调用方，应在 goroutine 中调用。
func (m *TTLManager) Start() {
	interval := m.interval
	if interval <= 0 {
		interval = TTLManagerDefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			_, _ = m.SweepAll()
		}
	}
}

// Stop 停止后台扫描（防重复 close）。
func (m *TTLManager) Stop() {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	close(m.stopCh)
}
