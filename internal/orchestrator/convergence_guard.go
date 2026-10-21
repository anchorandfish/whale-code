// convergence_guard.go — H12 自我进化系统 §2.5 收敛保护器（v0.2 新增，致命漏洞 5 修复）。
// 设计说明 §2.5 + §3.6；落地：三件套（<1% 停止 + 24h 冷却 + 3 次反转回滚）
// v0.2；落地：本文件为 H12 自我进化系统收敛保护实现
// SNAPSHOT: pkg=internal/orchestrator; type=ConvergenceGuard; three_pieces={ShouldStop(<1%连续2次), InCooldown(24h), RecordDelta(3次反转回滚)}; persistence=last_tune.json; concurrency=sync.Mutex
//
// 三件套对标 RL 训练收敛机制：
// - 权重变化 <1% 停止（连续 2 次）← Early stopping
// - 24h 调优冷却期 ← 学习率衰减
// - 3 次方向反转触发回滚 ← 梯度裁剪
package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConvergenceGuardCooldownDuration 调优冷却期 24h（§2.5.2）。
const ConvergenceGuardCooldownDuration = 24 * time.Hour

// ConvergenceGuardSmallChangeThreshold 权重变化 <1% 视为小变化（§2.5.1）。
const ConvergenceGuardSmallChangeThreshold = 0.01

// ConvergenceGuardSmallChangeLimit 连续 2 次小变化触发停止（§2.5.1）。
const ConvergenceGuardSmallChangeLimit = 2

// ConvergenceGuardDeltaHistoryLimit delta 历史最多保留 5 个（§2.5.3）。
const ConvergenceGuardDeltaHistoryLimit = 5

// ConvergenceGuardReversalWindow 检查最近 3 个 delta 是否交替（+-+ 或 -+-，§2.5.3）。
const ConvergenceGuardReversalWindow = 3

// ConvergenceGuard cooldown 起点零值（time.Time 的零值），表示从未调优过。
// 与 lastTuneAt.IsZero() 配合判定"首次调优无冷却期"。

// ConvergenceGuard 收敛保护器（v0.2 新增，致命漏洞 5 修复）。
// 防止 WeightTuner 权重振荡 + 防止闭环死循环，对标 RL 训练的
// early stopping + 学习率衰减 + 梯度裁剪三件套。
//
// 并发安全：sync.Mutex 保护 lastTuneAt + deltaHistory + smallChangeCount。
// 持久化：可选写 last_tune.json（persistencePath 注入，空=仅内存）。
type ConvergenceGuard struct {
	mu               sync.Mutex
	lastTuneAt       time.Time        // 最近一次调优时间（零值=从未调优）
	deltaHistory     []int            // 权重 delta 主符号历史（+1/-1/0），最多 5 个
	smallChangeCount int              // 连续小变化计数（达 2 触发停止）
	nowFunc          func() time.Time // 时间注入（测试用）
	persistencePath  string           // last_tune.json 路径（空=仅内存）
}

// NewConvergenceGuard 构造收敛保护器。
// persistencePath 空表示仅内存（测试用），非空时持久化到 last_tune.json。
// 首次构造会尝试从 persistencePath 加载已有状态（崩溃恢复）。
func NewConvergenceGuard(persistencePath string) *ConvergenceGuard {
	g := &ConvergenceGuard{
		deltaHistory:    make([]int, 0, ConvergenceGuardDeltaHistoryLimit),
		nowFunc:         time.Now,
		persistencePath: persistencePath,
	}
	// 崩溃恢复：从持久化文件加载状态（best-effort，失败不阻塞）
	if persistencePath != "" {
		_ = g.loadFromPath()
	}
	return g
}

// SetNowFunc 注入时间函数（测试用，便于固定时间）。
func (g *ConvergenceGuard) SetNowFunc(fn func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nowFunc = fn
}

// ShouldStop 判断是否应停止调优（权重变化 <1% 连续 2 次，§2.5.1）。
// 算法：L1 范数变化率 ||after - before||_1 / ||before||_1 < 0.01
// 触发条件：连续 2 次小变化（smallChangeCount 达 2）。
// before/after 为 GetWeights 返回的 Weights map。
// 返回 true 表示应停止调优（调用方应停止后续 Tune）。
func (g *ConvergenceGuard) ShouldStop(before, after map[string]int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 计算变化率：||after - before||_1 / ||before||_1
	beforeNorm := 0
	for _, v := range before {
		if v < 0 {
			v = -v
		}
		beforeNorm += v
	}
	if beforeNorm == 0 {
		// before 全零：视为已停止（避免除零）
		g.smallChangeCount = 0
		return false
	}

	diffNorm := 0
	// 合并 key 集合
	keys := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	for k := range keys {
		d := after[k] - before[k]
		if d < 0 {
			d = -d
		}
		diffNorm += d
	}

	rate := float64(diffNorm) / float64(beforeNorm)
	if rate < ConvergenceGuardSmallChangeThreshold {
		g.smallChangeCount++
	} else {
		// 非小变化重置计数（连续语义）
		g.smallChangeCount = 0
	}
	return g.smallChangeCount >= ConvergenceGuardSmallChangeLimit
}

// InCooldown 判断是否在冷却期（24h 内不调优，§2.5.2）。
// lastTuneAt 为零值（从未调优）→ 不在冷却期，允许调优。
// time.Since(lastTuneAt) < 24h → 在冷却期。
func (g *ConvergenceGuard) InCooldown() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastTuneAt.IsZero() {
		return false
	}
	return g.nowFunc().Sub(g.lastTuneAt) < ConvergenceGuardCooldownDuration
}

// RecordDelta 记录权重 delta，检测是否触发 3 次方向反转（§2.5.3）。
// delta 是 before/after 差值（after[k] - before[k]）。
// 算法：
// 1. 计算每个 key 的 delta 符号（+1/-1/0）
// 2. 取主符号（多数票，平票视为 0 无方向）
// 3. 加入 deltaHistory（保留最近 5 个）
// 4. 检查最近 3 个是否交替（+-+ 或 -+-）
//
// 返回 true 表示触发回滚（调用方应调 L1ScorerExt.RollbackWeights）。
func (g *ConvergenceGuard) RecordDelta(delta map[string]int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 计算主符号：多数票
	pos, neg, zero := 0, 0, 0
	for _, v := range delta {
		switch {
		case v > 0:
			pos++
		case v < 0:
			neg++
		default:
			zero++
		}
	}
	var sign int
	switch {
	case pos > neg && pos > zero:
		sign = 1
	case neg > pos && neg > zero:
		sign = -1
	default:
		sign = 0
	}

	// 追加 deltaHistory（保留最近 5 个）
	g.deltaHistory = append(g.deltaHistory, sign)
	if len(g.deltaHistory) > ConvergenceGuardDeltaHistoryLimit {
		g.deltaHistory = g.deltaHistory[len(g.deltaHistory)-ConvergenceGuardDeltaHistoryLimit:]
	}

	// 检查最近 3 个是否交替（+-+ 或 -+-）
	if len(g.deltaHistory) < ConvergenceGuardReversalWindow {
		return false
	}
	last := g.deltaHistory[len(g.deltaHistory)-ConvergenceGuardReversalWindow:]
	// +-+ 模式
	if last[0] == 1 && last[1] == -1 && last[2] == 1 {
		return true
	}
	// -+- 模式
	if last[0] == -1 && last[1] == 1 && last[2] == -1 {
		return true
	}
	return false
}

// UpdateLastTune 更新 lastTuneAt 为当前时间并持久化（§2.5.2）。
// 调用方在调优成功后调用。返回持久化错误（持久化失败不阻塞内存状态更新）。
func (g *ConvergenceGuard) UpdateLastTune() error {
	g.mu.Lock()
	g.lastTuneAt = g.nowFunc()
	path := g.persistencePath
	// 持久化所需状态快照（锁内拷贝避免锁外读取）
	snap := persistenceSnapshot{
		LastTuneAt:       g.lastTuneAt,
		DeltaHistory:     append([]int(nil), g.deltaHistory...),
		SmallChangeCount: g.smallChangeCount,
	}
	g.mu.Unlock()

	if path == "" {
		return nil
	}
	return g.saveToPath(snap)
}

// ResetSmallChangeCount 重置 smallChangeCount（§2.5.1）。
// 连续 2 次小变化触发停止后，由外部决策重置（如冷启动期 30 天后）调用。
func (g *ConvergenceGuard) ResetSmallChangeCount() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.smallChangeCount = 0
}

// GetSmallChangeCount 返回连续小变化计数（测试用）。
func (g *ConvergenceGuard) GetSmallChangeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.smallChangeCount
}

// GetDeltaHistory 返回 delta 历史副本（测试用）。
func (g *ConvergenceGuard) GetDeltaHistory() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]int, len(g.deltaHistory))
	copy(out, g.deltaHistory)
	return out
}

// GetLastTuneAt 返回最近一次调优时间（测试用）。
func (g *ConvergenceGuard) GetLastTuneAt() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastTuneAt
}

// persistenceSnapshot 持久化快照（锁外序列化用，避免锁重入）。
type persistenceSnapshot struct {
	LastTuneAt       time.Time `json:"last_tune_at"`
	DeltaHistory     []int     `json:"delta_history"`
	SmallChangeCount int       `json:"small_change_count"`
}

// saveToPath 原子写入 last_tune.json（best-effort，失败不阻塞）。
func (g *ConvergenceGuard) saveToPath(snap persistenceSnapshot) error {
	if g.persistencePath == "" {
		return nil
	}
	if dir := filepath.Dir(g.persistencePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := g.persistencePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.persistencePath)
}

// loadFromPath 从 last_tune.json 加载状态（best-effort，失败不阻塞）。
func (g *ConvergenceGuard) loadFromPath() error {
	data, err := os.ReadFile(g.persistencePath)
	if err != nil {
		return nil // 文件不存在视为首次启动
	}
	var snap persistenceSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil // 损坏视为首次启动
	}
	g.lastTuneAt = snap.LastTuneAt
	g.deltaHistory = snap.DeltaHistory
	if g.deltaHistory == nil {
		g.deltaHistory = make([]int, 0, ConvergenceGuardDeltaHistoryLimit)
	}
	g.smallChangeCount = snap.SmallChangeCount
	return nil
}
