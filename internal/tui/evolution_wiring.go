// evolution_wiring.go — H12 自我进化闭环 tui 层接线。
// 51 NewEvolutionLoop；落地：5 组件构造 + EvolutionLoop + SetEvolutionLoop
// 237 SetEvolutionLoop；落地：注入到 ExperienceOrchestrator.evolutionLoop 字段
// 139 StartBackgroundScanner；落地：enabled=true 时启动 24h TTL 扫描 goroutine
// 55 EvolutionConfig；落地：cfg.Evolution.Enabled / AutoRun / TTLScanIntervalHours
// 56 NewConvergenceGuard(persistencePath)；落地：persist 到 ~/.whale/orchestrator/convergence.json
// 35 NewPatternExtractor(experience)；落地：依赖 ExperienceStore
// 43 NewPatternTTLScanner(experience)；落地：依赖 ExperienceStore
// 50 NewPatternValidator(experience, quality)；落地：依赖 ExperienceStore + QualityStore
// 45 NewWeightTuner(scorer, quality, guard)；落地：依赖 L1ScorerExt + QualityStore + ConvergenceGuard
// SNAPSHOT: wireH12Evolution={5 组件构造 + EvolutionLoop + SetEvolutionLoop + StartBackgroundScanner}; enabled=false 仍构造 EvolutionLoop（Run 零成本降级），StartBackgroundScanner 内部判 enabled 跳过 goroutine

package tui

import (
	"context"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/orchestrator"
)

// wireH12Evolution 在 tui.NewModel 内调用，完成 H12 EvolutionLoop 构造 + 注入 + 后台扫描启动。
// 时序约束：必须在 T2-A ExperienceOrchestrator 实例化后调用（依赖 orch 非 nil）。
// 降级策略：
// - orch==nil：直接返回，日志 warn（当前 T2-A 未创建 ExperienceOrchestrator 时走此路径）
// - enabled=false：EvolutionLoop 仍构造（Run 内部零成本降级），StartBackgroundScanner 内部跳过 goroutine
// - 5 组件任一构造失败：log warn 不阻塞，SetEvolutionLoop 仍注入（部分组件为 nil 时 Run 走 nil 防御）
//
// 参数：
// - cfg：配置（含 Evolution.Enabled / AutoRun / TTLScanIntervalHours）
// - orch：ExperienceOrchestrator 实例（T2-A 创建，nil 时直接返回）
// - expStore：ExperienceStore（T2-A 创建，可为 nil）
// - qualityStore：QualityStore（T2-A 创建，可为 nil）
// - scorer：L1ScorerExt（T2-A 创建，可为 nil）
// - persistencePath：ConvergenceGuard 持久化路径（~/.whale/orchestrator/convergence.json）
// - ctx：顶层 ctx，用于 StartBackgroundScanner goroutine 联动取消
func wireH12Evolution(cfg *config.Config, orch *orchestrator.ExperienceOrchestrator, expStore *orchestrator.ExperienceStore, qualityStore *orchestrator.QualityStore, scorer *orchestrator.L1ScorerExt, persistencePath string, ctx context.Context) {
	if orch == nil {
		log.Warn("wireH12Evolution: ExperienceOrchestrator is nil, skip H12 wiring (T2-A not completed?)")
		return
	}

	// 1. 构造 ConvergenceGuard（无依赖）
	guard := orchestrator.NewConvergenceGuard(persistencePath)

	// 2. 构造 PatternExtractor（依赖 ExperienceStore）
	var extractor *orchestrator.PatternExtractor
	if expStore != nil {
		extractor = orchestrator.NewPatternExtractor(expStore)
	} else {
		log.Warn("wireH12Evolution: expStore is nil, PatternExtractor skipped")
	}

	// 3. 构造 PatternTTLScanner（依赖 ExperienceStore）
	var ttlScanner *orchestrator.PatternTTLScanner
	if expStore != nil {
		ttlScanner = orchestrator.NewPatternTTLScanner(expStore)
	} else {
		log.Warn("wireH12Evolution: expStore is nil, PatternTTLScanner skipped")
	}

	// 4. 构造 PatternValidator（依赖 ExperienceStore + QualityStore）
	var validator *orchestrator.PatternValidator
	if expStore != nil && qualityStore != nil {
		validator = orchestrator.NewPatternValidator(expStore, qualityStore)
	} else {
		log.Warn("wireH12Evolution: expStore or qualityStore is nil, PatternValidator skipped")
	}

	// 5. 构造 WeightTuner（依赖 L1ScorerExt + QualityStore + ConvergenceGuard）
	var weightTuner *orchestrator.WeightTuner
	if scorer != nil && qualityStore != nil && guard != nil {
		weightTuner = orchestrator.NewWeightTuner(scorer, qualityStore, guard)
	} else {
		log.Warn("wireH12Evolution: scorer/qualityStore/guard is nil, WeightTuner skipped")
	}

	// 6. 构造 EvolutionLoop（enabled=false 时仍构造，Run 内部零成本降级）
	// 注：autoRun=false 时 OnTaskDone 不自动触发 Run（orchestrator.go:271 IsAutoRun 检查）
	loop := orchestrator.NewEvolutionLoop(
		extractor,
		weightTuner,
		validator,
		ttlScanner,
		guard,
		cfg.Evolution.Enabled,
		cfg.Evolution.AutoRun,
	)

	// 7. 设置 TTL 扫描间隔（cfg.Evolution.TTLScanIntervalHours，默认 24）
	if cfg.Evolution.TTLScanIntervalHours > 0 {
		loop.SetTTLScanInterval(time.Duration(cfg.Evolution.TTLScanIntervalHours) * time.Hour)
	}

	// 8. 注入到 ExperienceOrchestrator
	orch.SetEvolutionLoop(loop)

	// 9. 启动后台 TTL 扫描 goroutine（仅 enabled=true 时启动，evolution_loop.go:140 内部判 enabled）
	if cfg.Evolution.Enabled {
		stop, err := loop.StartBackgroundScanner(ctx)
		if err != nil {
			log.Warn("wireH12Evolution: StartBackgroundScanner failed", "error", err)
		} else {
			// P1-15 修复：监听 ctx.Done() 后调用 stop() 确保 scanWG.Wait() 被执行，
			// 避免 ttlScanner.Scan 阻塞在 I/O 时 goroutine 短暂存活（CancelWiring 触发 wiringCancel 后联动）
			go func() {
				<-ctx.Done()
				stop()
			}()
		}
	} else {
		log.Info("wireH12Evolution: evolution disabled, StartBackgroundScanner skipped")
	}

	log.Info("wireH12Evolution: H12 wiring completed",
		"enabled", cfg.Evolution.Enabled,
		"autoRun", cfg.Evolution.AutoRun,
		"ttlScanIntervalHours", cfg.Evolution.TTLScanIntervalHours)
}
