// evolution_wiring_test.go — H12 自我进化闭环接线单元测试。
// 覆盖：wireH12Evolution 5 组件构造 + EvolutionLoop 注入 + 降级路径。
// wireH12Evolution；落地：5 组件构造 + SetEvolutionLoop + StartBackgroundScanner
// 244 GetEvolutionLoop；落地：测试断言 loop 已注入
// 83 IsEnabled；落地：测试断言 enabled 状态
// SNAPSHOT: tests={wireH12Evolution_HappyPath, wireH12Evolution_OrchNil, wireH12Evolution_Disabled, wireH12Evolution_ExpStoreNil}

package tui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/orchestrator"
)

// newTestEvolutionOrchestrator 构造测试用 ExperienceOrchestrator（含全部依赖）。
// 复用 orchestrator 包测试模式：NewDepGraph + NewWorktreePool + NewBudgetTracker + NewTraceStore +
// NewStateMachine + NewParallelScheduler + NewRouter(nil, NewProfileStore, balanced) + NewExperienceStore + NewQualityStore。
func newTestEvolutionOrchestrator(t *testing.T) (*orchestrator.ExperienceOrchestrator, *orchestrator.ExperienceStore, *orchestrator.QualityStore, *orchestrator.L1ScorerExt) {
	t.Helper()
	tempDir := t.TempDir()

	graph := orchestrator.NewDepGraph()
	pool := orchestrator.NewWorktreePool(tempDir, 3)
	budget := orchestrator.NewBudgetTracker(100000)
	tracer, err := orchestrator.NewTraceStore(filepath.Join(tempDir, "trace.jsonl"))
	if err != nil {
		t.Fatalf("NewTraceStore: %v", err)
	}
	state := orchestrator.NewStateMachine("test-task")
	scheduler := orchestrator.NewParallelScheduler(graph, pool, budget, tracer, state, 3)

	router := orchestrator.NewRouter(nil, orchestrator.NewProfileStore(), orchestrator.BudgetModeBalanced)

	globalPath := filepath.Join(tempDir, "global")
	projectPath := filepath.Join(tempDir, "project")
	expStore, err := orchestrator.NewExperienceStore(globalPath, projectPath)
	if err != nil {
		t.Fatalf("NewExperienceStore: %v", err)
	}

	qualityStore, err := orchestrator.NewQualityStore(filepath.Join(tempDir, "quality.jsonl"))
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	scorer := orchestrator.NewL1ScorerExt(filepath.Join(tempDir, "l1_weights.json"))

	orch := orchestrator.NewExperienceOrchestrator(scheduler, state, router, expStore, qualityStore)
	return orch, expStore, qualityStore, scorer
}

// TestWireH12Evolution_HappyPath 验证 5 组件构造 + SetEvolutionLoop 注入成功。
// 准备：cfg(Evolution.Enabled=true, AutoRun=false) + orch + expStore + qualityStore + scorer + persistencePath + ctx
// 执行：wireH12Evolution
// 断言：orch.GetEvolutionLoop() != nil + loop.IsEnabled() == true + loop.IsAutoRun() == false
func TestWireH12Evolution_HappyPath(t *testing.T) {
	orch, expStore, qualityStore, scorer := newTestEvolutionOrchestrator(t)
	cfg := config.DefaultConfig()
	cfg.Evolution.Enabled = true
	cfg.Evolution.AutoRun = false
	cfg.Evolution.TTLScanIntervalHours = 1

	persistencePath := filepath.Join(t.TempDir(), "convergence.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 执行接线
	wireH12Evolution(&cfg, orch, expStore, qualityStore, scorer, persistencePath, ctx)

	// 断言 EvolutionLoop 已注入
	loop := orch.GetEvolutionLoop()
	if loop == nil {
		t.Fatal("期望 GetEvolutionLoop() 非 nil（SetEvolutionLoop 已注入），got nil")
	}
	if !loop.IsEnabled() {
		t.Error("期望 IsEnabled() == true（cfg.Evolution.Enabled=true），got false")
	}
	if loop.IsAutoRun() {
		t.Error("期望 IsAutoRun() == false（cfg.Evolution.AutoRun=false），got true")
	}

	// 停止后台扫描 goroutine（避免泄漏）
	cancel()
}

// TestWireH12Evolution_OrchNil 验证 orch==nil 时直接返回不 panic。
// 断言：不 panic + 无副作用（无法直接断言日志，但 panic 会导致测试失败）。
func TestWireH12Evolution_OrchNil(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Evolution.Enabled = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// orch=nil，不应 panic
	wireH12Evolution(&cfg, nil, nil, nil, nil, "", ctx)
}

// TestWireH12Evolution_Disabled 验证 enabled=false 时 EvolutionLoop 仍构造但 StartBackgroundScanner 跳过。
// 断言：orch.GetEvolutionLoop() != nil + loop.IsEnabled() == false
func TestWireH12Evolution_Disabled(t *testing.T) {
	orch, expStore, qualityStore, scorer := newTestEvolutionOrchestrator(t)
	cfg := config.DefaultConfig()
	cfg.Evolution.Enabled = false
	cfg.Evolution.AutoRun = false

	persistencePath := filepath.Join(t.TempDir(), "convergence.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 执行接线（enabled=false）
	wireH12Evolution(&cfg, orch, expStore, qualityStore, scorer, persistencePath, ctx)

	// 断言 EvolutionLoop 仍构造（enabled=false 时 NewEvolutionLoop 仍调用）
	loop := orch.GetEvolutionLoop()
	if loop == nil {
		t.Fatal("期望 GetEvolutionLoop() 非 nil（enabled=false 时仍构造），got nil")
	}
	if loop.IsEnabled() {
		t.Error("期望 IsEnabled() == false（cfg.Evolution.Enabled=false），got true")
	}
}

// TestWireH12Evolution_ExpStoreNil 验证 expStore==nil 时 extractor/ttlScanner/validator 跳过降级。
// 断言：orch.GetEvolutionLoop() != nil（EvolutionLoop 仍构造，部分组件 nil）
func TestWireH12Evolution_ExpStoreNil(t *testing.T) {
	orch, _, qualityStore, scorer := newTestEvolutionOrchestrator(t)
	cfg := config.DefaultConfig()
	cfg.Evolution.Enabled = false // 避免 StartBackgroundScanner 启动（expStore=nil 时 scanner 会 nil 防御）

	persistencePath := filepath.Join(t.TempDir(), "convergence.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// expStore=nil，不应 panic
	wireH12Evolution(&cfg, orch, nil, qualityStore, scorer, persistencePath, ctx)

	// EvolutionLoop 仍构造（部分组件 nil，Run 内部走 nil 防御）
	loop := orch.GetEvolutionLoop()
	if loop == nil {
		t.Fatal("期望 GetEvolutionLoop() 非 nil（expStore=nil 时仍构造），got nil")
	}
}
