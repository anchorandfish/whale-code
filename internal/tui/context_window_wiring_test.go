package tui

import (
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
)

// context_window_wiring_test.go — C4「解析真的接在预算构造点上」判据。
//
// ★ 为什么必须从**生产入口**进，而不是直接断 `config.EffectiveContextWindow()`：
// 本轮病的是"**接线**没接"（解析器存在但零生产调用点， 同族）。只断 helper 的返回值，
// 把构造点改回 `cfg.ContextWindow` 也照样绿 ⇒ 那是假判据。所以这里走 plain/bridge 真正用的
// `NewRunnerCore`（`runner_core.go:384`）与 TUI 用的 `NewModel`（`lifecycle.go:318`）。
//
// 判别性（反向 mutant，两个方向都能红）：
// - 构造点改回 `cfg.ContextWindow` ⇒ Budget.ContextWindow 变 0 ⇒ 红；
// - 构造点改用 `config.DefaultContextWindow` ⇒ 变 131072（≠ 模型表值） ⇒ 红。

// c4ModelWindow 返回 `model` 的模型表窗口，并前置断言它与默认窗口不同 ——
// 否则"解析成功"与"没解析"读数一样，判据不可判别。
func c4ModelWindow(t *testing.T, model string) int {
	t.Helper()
	want := ai.ContextWindowForModel(model)
	if want <= 0 || want == config.DefaultContextWindow {
		t.Fatalf("前置失败：%s 的表值 %d 与 DefaultContextWindow(%d) 相同 ⇒ 判据不可判别",
			model, want, config.DefaultContextWindow)
	}
	return want
}

// TestC4_RunnerCoreBudgetWiresModelWindow 判据一：plain/bridge 入口（NewRunnerCore）。
func TestC4_RunnerCoreBudgetWiresModelWindow(t *testing.T) {
	const model = "qwen3.5-flash"
	want := c4ModelWindow(t, model)

	cfg := config.DefaultConfig()
	cfg.Model = model
	cfg.ContextWindow = 0 // 「用户没设」——声明语义 = 由模型决定

	home := guIsolatedHome(t)
	core := NewRunnerCore(&cfg, newTestClient(model), newTestRegistry(),
		t.TempDir(), home, filepath.Join(home, "sessions"), "Chinese")
	if core == nil {
		t.Fatal("前置失败：NewRunnerCore 返回 nil")
	}

	if got := core.Budget.ContextWindow; got != want {
		t.Errorf("未设 context_window 时应由模型决定：Budget.ContextWindow = %d, want %d"+
			"（0 ⇒ 解析器没接线；131072 ⇒ 走了默认值而非模型表）", got, want)
	}
}

// TestC4_TuiModelBudgetWiresModelWindow 判据二：TUI 入口（NewModel）。
//
// ★ 两个入口都要钉：本仓反复栽在"只接了一侧"（命令双实现）。
func TestC4_TuiModelBudgetWiresModelWindow(t *testing.T) {
	const model = "qwen3.5-flash"
	want := c4ModelWindow(t, model)

	cfg := config.DefaultConfig()
	cfg.Model = model
	cfg.ContextWindow = 0

	guIsolatedHome(t)
	m := NewModel(&cfg, newTestClient(model), newTestRegistry())
	guTrackModelTeardown(t, &m)

	if got := m.aiService.Budget().ContextWindow; got != want {
		t.Errorf("未设 context_window 时应由模型决定：Budget.ContextWindow = %d, want %d"+
			"（0 ⇒ 解析器没接线；131072 ⇒ 走了默认值而非模型表）", got, want)
	}
}

// TestC4_ExplicitWindowStillWinsOnProductionPath 判据三（反向保护）：
// 用户显式设了窗口时，生产路径必须**原样采用**，不得被模型表顶掉。
// 这条挡住"为了让判据一绿而把所有窗口都改成查模型表"的过度修复。
func TestC4_ExplicitWindowStillWinsOnProductionPath(t *testing.T) {
	const model = "qwen3.5-flash"
	c4ModelWindow(t, model) // 前置：该模型表值 ≠ 下面这个显式值

	const explicit = 50000
	cfg := config.DefaultConfig()
	cfg.Model = model
	cfg.ContextWindow = explicit

	home := guIsolatedHome(t)
	core := NewRunnerCore(&cfg, newTestClient(model), newTestRegistry(),
		t.TempDir(), home, filepath.Join(home, "sessions"), "Chinese")
	if core == nil {
		t.Fatal("前置失败：NewRunnerCore 返回 nil")
	}

	if got := core.Budget.ContextWindow; got != explicit {
		t.Errorf("显式 context_window 必须原样进入 Budget；got %d, want %d", got, explicit)
	}
}
