package config

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// context_window_test.go — C4「默认值不再猜 / 解析链接线」判据组。
//
// 病根不是"默认值选错了数"，而是**声明了语义却没接线**：
// `Config.ContextWindow` 的注释写着「0 = 由模型决定」，但 7 处预算构造点直接吃它，
// 传 0 进去并不会"由模型决定"，而是让 `Budget.ContextWindow <= 0` 的分支全线降级。
// 解析器 `ai.ContextWindowForModel` 早就存在，只是全仓零生产调用点（同族）。
//
// 本文件只钉**规则本身**；"规则真的接在预算构造点上"由
// `internal/tui/context_window_wiring_test.go` 从生产入口钉。

// TestC4_EffectiveContextWindow_ExplicitWins 判据一：用户显式值优先，模型表不得覆盖用户。
func TestC4_EffectiveContextWindow_ExplicitWins(t *testing.T) {
	const model = "qwen3.5-flash" // 表窗口 1048576
	c := &Config{ContextWindow: 4242, Model: model}

	if got := c.EffectiveContextWindow(); got != 4242 {
		t.Errorf("显式 context_window 必须原样采用；got %d, want 4242（模型表 %d 不得覆盖用户）",
			got, ai.ContextWindowForModel(model))
	}
}

// TestC4_EffectiveContextWindow_ZeroMeansModelTable 判据二（本单核心）：
// `ContextWindow == 0` 的**声明语义**是"由模型决定" ⇒ 必须真的解析到模型表值。
//
// 判别性：改动前构造点直接吃 `cfg.ContextWindow` ⇒ 0 原样进 Budget ⇒ 全线降级。
func TestC4_EffectiveContextWindow_ZeroMeansModelTable(t *testing.T) {
	const model = "qwen3.5-flash"
	want := ai.ContextWindowForModel(model)
	// 前置：表值必须与默认窗口**不同**，否则"解析成功"与"没解析"读数一样 ⇒ 判据不可判别。
	if want <= 0 || want == DefaultContextWindow {
		t.Fatalf("前置失败：%s 的表值 %d 与 DefaultContextWindow(%d) 相同 ⇒ 判据不可判别",
			model, want, DefaultContextWindow)
	}

	c := &Config{ContextWindow: 0, Model: model}
	if got := c.EffectiveContextWindow(); got != want {
		t.Errorf("context_window=0 应解析为模型表值；got %d, want %d", got, want)
	}
}

// TestC4_EffectiveContextWindow_UnknownModelFallsBack 判据三：
// 未知模型的兜底值是**单源**（`ai.ContextWindowForModel` 的 200000），本层不复制常量。
func TestC4_EffectiveContextWindow_UnknownModelFallsBack(t *testing.T) {
	const model = "zzz-definitely-unknown-model-qqq"
	if table := ai.ContextWindowForModel(model); table != 200000 {
		t.Fatalf("前置失败：%q 竟命中了模型表（得 %d）—— 换一个不可能命中的名字", model, table)
	}

	c := &Config{ContextWindow: 0, Model: model}
	if got := c.EffectiveContextWindow(); got != 200000 {
		t.Errorf("未知模型应回退到 ai 侧兜底 200000；got %d", got)
	}
}

// TestC4_EffectiveContextWindow_NilConfig 判据四：nil 注册表/配置不得 panic，回退默认窗口。
func TestC4_EffectiveContextWindow_NilConfig(t *testing.T) {
	var c *Config
	if got := c.EffectiveContextWindow(); got != DefaultContextWindow {
		t.Errorf("nil Config 应回退 DefaultContextWindow(%d)；got %d", DefaultContextWindow, got)
	}
}

// TestC4_ResolveContextWindow_ModelSideCarrier 判据五：
// 「换模型」路径（/model set、/fast）以**目标模型**为承载体解析 —— 规则与用户配置侧同源。
func TestC4_ResolveContextWindow_ModelSideCarrier(t *testing.T) {
	const model = "qwen3.5-flash"
	want := ai.ContextWindowForModel(model)
	if want <= 0 || want == DefaultContextWindow {
		t.Fatalf("前置失败：%s 表值 %d 与默认窗口相同 ⇒ 判据不可判别", model, want)
	}

	if got := ResolveContextWindow(0, model); got != want {
		t.Errorf("模型侧 0 应解析到模型表值；got %d, want %d", got, want)
	}
	if got := ResolveContextWindow(65536, model); got != 65536 {
		t.Errorf("模型侧显式值优先；got %d, want 65536", got)
	}
}

// TestC4_DefaultContextWindow_KeptAt131072 判据六（拍板锁定，防"顺手放宽"）：
// 默认值**刻意保持** 131072 —— 不自动放宽到模型窗口。
// 理由：窗口变大 ⇒ 压缩更晚 ⇒ 长会话更容易飘进更高计价档（等于替用户改账单）。
// 用户想放宽必须显式设置（settings.json 的 context_window / WHALE_CONTEXT_WINDOW）。
func TestC4_DefaultContextWindow_KeptAt131072(t *testing.T) {
	if DefaultContextWindow != 131072 {
		t.Errorf("DefaultContextWindow 应为 131072（2026-10-05 拍板「默认保持 131072」）；got %d", DefaultContextWindow)
	}
	if got := DefaultConfig().ContextWindow; got != DefaultContextWindow {
		t.Errorf("DefaultConfig().ContextWindow(%d) 必须来自 DefaultContextWindow(%d)（同一常量，勿各写各的）",
			got, DefaultContextWindow)
	}
}
