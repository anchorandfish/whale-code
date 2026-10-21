package tui

// · /plan「开关没接灯」判据（2026-10-19 拍板）。
//
// 现状（修前）：planMode 只在 TUI 内存存取（service_session.go:382-389），
// 全仓没有任何执行路径消费它 —— /plan on 文案宣称「AI 只分析不执行」，
// 实际该执行照样执行 = UI 在说谎（「文案承诺 ≠ 实际机制」同族）。
//
// 拍板：/plan on ⇒ buildSystemPrompt 真注入「只分析」提示层约束（PLAN MODE 块）；
// /plan off ⇒ 不得残留。硬门（PermissionStage 拦写）发布后立项，本判据只钉提示层。
//
// 判别性：注入反例（摘掉 plan_mode 组件 / 文案退回旧版）必红。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
)

// TestW106_PlanModeInjectsAnalyzeOnlyPrompt 验证 /plan 的灯接进系统提示。
// 修前必红（planMode 零消费）。
func TestW106_PlanModeInjectsAnalyzeOnlyPrompt(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	// off：不得出现 PLAN MODE 块
	m.sessionService.SetPlanMode(false)
	offPrompts := strings.Join(layerBlocksToStrings(m.buildSystemPrompt()), "\n")
	if strings.Contains(offPrompts, planModePromptMarker) {
		t.Fatalf("planMode=off 时系统提示不应包含 %q，实际包含了 ⇒ 关不掉的开关", planModePromptMarker)
	}

	// on：必须出现 PLAN MODE 块
	m.sessionService.SetPlanMode(true)
	onPrompts := strings.Join(layerBlocksToStrings(m.buildSystemPrompt()), "\n")
	if !strings.Contains(onPrompts, planModePromptMarker) {
		t.Fatalf("planMode=on 时系统提示缺少 %q ⇒ /plan 仍是「开关没接灯」", planModePromptMarker)
	}
}

// TestW106_PlanOnMessageIsHonest 验证 /plan on 的文案不再虚假承诺硬性拦截。
// 修前文案为「AI will only analyze, not execute」（硬承诺、无强制机制）——红；
// 修后必须明示这是 prompt-level 引导（诚实标记 planLevelHonestyMarker）。
func TestW106_PlanOnMessageIsHonest(t *testing.T) {
	m := createMinimalModel()
	out := m.handlePlan("on")
	if !strings.Contains(out, planLevelHonestyMarker) {
		t.Fatalf("/plan on 文案缺少诚实标记 %q（旧文案硬承诺「不执行」但无强制机制 = UI 说谎）", planLevelHonestyMarker)
	}
}
