// evolution_loop.go — H12 自我进化系统 §3.1 闭环触发器（v0.2 新增）。
// 设计说明 §3.1；落地：四步闭环（PatternExtractor → WeightTuner → PatternValidator → PatternTTLScanner）
// v0.2；落地：Feature Flag 默认 false（enabled/auto_run 均默认 false）
// SNAPSHOT: pkg=internal/orchestrator; type=EvolutionLoop; flow={校验→Extract→Tune→Validate→TTL独立goroutine}; feature_flag={enabled=false零成本降级, auto_run=false仅手动触发}; degraded={任一步失败warn不阻断后续}; concurrency=独立goroutine 24h周期TTL
package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EvolutionLoopTTLScanDefaultHours 后台 TTL 扫描默认间隔（24h，§3.1）。
const EvolutionLoopTTLScanDefaultHours = 24

// EvolutionLoop 自我进化闭环触发器（§3.1）。
// 任务完成后调用，依次执行：模式提取 → 权重调优 → 模式验证。
// PatternTTLScanner.Scan 由独立 goroutine 24h 周期触发，不在 Run 主路径。
//
// Feature Flag：
// - enabled：总开关，false 时 EvolutionLoop 不被实例化
// - autoRun：自动触发开关，false 时即使 enabled=true 也需手动调用 Run
//
// 关键约束（§3.1）：
// - taskID 为空返回 error（v0.2 优化 3）
// - ctx 超时返回 ctx.Err()（v0.2 优化 3）
// - 任一步骤失败 warn 不阻断后续步骤（原子性）
type EvolutionLoop struct {
	extractor   *PatternExtractor
	weightTuner *WeightTuner
	validator   *PatternValidator
	ttlScanner  *PatternTTLScanner
	guard       *ConvergenceGuard
	enabled     bool // Feature Flag: evolution.enabled
	autoRun     bool // Feature Flag: evolution.auto_run
	nowFunc     func() time.Time

	// 后台 TTL 扫描控制
	ttlScanInterval time.Duration  // TTL 扫描间隔
	scanStopCh      chan struct{}  // 停止信号
	scanWG          sync.WaitGroup // 等待 goroutine 退出
	scanMu          sync.Mutex     // 保护 scanStopCh
	scanRunning     bool
}

// NewEvolutionLoop 构造自我进化闭环触发器。
// enabled=false 时 Run 直接返回 nil（Feature Flag 关闭，零成本降级）。
// autoRun=false 时仅手动触发 Run，编排器不自动调用。
// ttlScanner 由 StartBackgroundScanner 独立 goroutine 调用。
func NewEvolutionLoop(
	extractor *PatternExtractor,
	weightTuner *WeightTuner,
	validator *PatternValidator,
	ttlScanner *PatternTTLScanner,
	guard *ConvergenceGuard,
	enabled, autoRun bool,
) *EvolutionLoop {
	return &EvolutionLoop{
		extractor:       extractor,
		weightTuner:     weightTuner,
		validator:       validator,
		ttlScanner:      ttlScanner,
		guard:           guard,
		enabled:         enabled,
		autoRun:         autoRun,
		nowFunc:         time.Now,
		ttlScanInterval: time.Duration(EvolutionLoopTTLScanDefaultHours) * time.Hour,
	}
}

// SetNowFunc 注入时间函数（测试用，便于固定时间）。
func (e *EvolutionLoop) SetNowFunc(fn func() time.Time) {
	e.nowFunc = fn
}

// SetTTLScanInterval 设置后台 TTL 扫描间隔（测试用，便于快速验证）。
func (e *EvolutionLoop) SetTTLScanInterval(d time.Duration) {
	e.ttlScanInterval = d
}

// IsEnabled 返回 enabled 状态（测试用）。
func (e *EvolutionLoop) IsEnabled() bool {
	return e != nil && e.enabled
}

// IsAutoRun 返回 autoRun 状态（测试用）。
func (e *EvolutionLoop) IsAutoRun() bool {
	return e != nil && e.autoRun
}

// Run 执行自我进化闭环（§3.1）。
// 流程：
// 1. 校验：taskID 为空返回 error；ctx 超时返回 ctx.Err()
// 2. enabled=false 直接返回 nil（Feature Flag 关闭）
// 3. extractor.Extract → 失败 warn 不阻断
// 4. weightTuner.Tune → 失败 warn 不阻断
// 5. validator.Validate（针对本次任务命中的 Pattern）→ 失败 warn 不阻断
// 6. ttlScanner.Scan 由独立 goroutine 24h 周期触发，不在 Run 主路径
//
// 任一步失败不阻断后续步骤（降级策略，）。
func (e *EvolutionLoop) Run(ctx context.Context, taskID string, trace []TraceEntry) error {
	// ① taskID 空校验
	if taskID == "" {
		return fmt.Errorf("evolution_loop: taskID must not be empty")
	}
	// ② ctx 超时校验
	if err := ctx.Err(); err != nil {
		return err
	}
	// ③ Feature Flag 检查
	if e == nil || !e.enabled {
		return nil
	}

	// ④ PatternExtractor.Extract（失败 warn 不阻断）
	if e.extractor != nil {
		_ = e.extractor.Extract(ctx, taskID, trace)
	}

	// ⑤ WeightTuner.Tune（失败 warn 不阻断）
	if e.weightTuner != nil {
		_ = e.weightTuner.Tune(ctx)
	}

	// ⑥ PatternValidator.Validate（失败 warn 不阻断）
	// 修复 B-P0-3：从 trace 提取命中 Pattern 的 ID，逐一调 validator.Validate。
	// trace 条目中 PatternID 非 nil 表示该子任务命中了经验库 Pattern，
	// Validate 基于近期 L2 评分流转 Pattern 的 VerifiedState（unverified→provisionally_verified→verified）。
	if e.validator != nil {
		patternIDs := extractHitPatternIDs(trace)
		for _, pid := range patternIDs {
			_ = e.validator.Validate(ctx, pid) // 失败 warn 不阻断（降级策略）
		}
	}

	// ⑦ ttlScanner.Scan 由独立 goroutine 24h 周期触发，不在 Run 主路径（§3.1）
	return nil
}

// extractHitPatternIDs 从 trace 提取命中的 Pattern ID（修复 B-P0-3 辅助函数）。
// 扫描 trace 条目，收集所有 PatternID 非空的唯一值（去重保序）。
// 返回空切片表示本次任务未命中任何经验库 Pattern（Validate 步骤无操作）。
func extractHitPatternIDs(trace []TraceEntry) []string {
	if len(trace) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, entry := range trace {
		if entry.PatternID == "" {
			continue
		}
		if _, ok := seen[entry.PatternID]; ok {
			continue // 去重
		}
		seen[entry.PatternID] = struct{}{}
		ids = append(ids, entry.PatternID)
	}
	return ids
}

// StartBackgroundScanner 启动后台 TTL 扫描 goroutine（24h 周期，§3.1）。
// 仅在 enabled=true 时启动。
// 返回 stop func 供优雅停止（调用 stop 会等待 goroutine 退出）。
// 若已启动，返回错误避免重复启动。
func (e *EvolutionLoop) StartBackgroundScanner(ctx context.Context) (stop func(), err error) {
	if e == nil || !e.enabled {
		return func() {}, nil // Feature Flag 关闭，不启动
	}
	if e.ttlScanner == nil {
		return func() {}, nil // 无 scanner 不启动
	}

	e.scanMu.Lock()
	if e.scanRunning {
		e.scanMu.Unlock()
		return func() {}, fmt.Errorf("evolution_loop: background scanner already running")
	}
	e.scanStopCh = make(chan struct{})
	e.scanRunning = true
	e.scanWG.Add(1)
	stopCh := e.scanStopCh
	e.scanMu.Unlock()

	go func() {
		defer e.scanWG.Done()
		ticker := time.NewTicker(e.ttlScanInterval)
		defer ticker.Stop()
		// 立即跑一次首扫
		_ = e.ttlScanner.Scan(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				_ = e.ttlScanner.Scan(ctx)
			}
		}
	}()

	// 返回 stop func（idempotent）
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			e.scanMu.Lock()
			if e.scanRunning {
				close(stopCh)
				e.scanRunning = false
			}
			e.scanMu.Unlock()
			e.scanWG.Wait()
		})
	}, nil
}

// ValidatePattern 显式触发单个 Pattern 的验证（§3.1 步骤 5 补充）。
// 由编排器在 ExperienceHit 命中后调用，传入命中的 patternID。
// enabled=false 或 validator=nil 时直接返回 nil（降级）。
func (e *EvolutionLoop) ValidatePattern(ctx context.Context, patternID string) error {
	if e == nil || !e.enabled {
		return nil
	}
	if e.validator == nil {
		return nil
	}
	return e.validator.Validate(ctx, patternID)
}

// IncrementTaskCount 任务计数器+1（编排器每次任务完成调，触发冷启动保护更新）。
// enabled=false 或 validator=nil 时直接返回（降级）。
func (e *EvolutionLoop) IncrementTaskCount() {
	if e == nil || !e.enabled || e.validator == nil {
		return
	}
	e.validator.IncrementTaskCount()
}
