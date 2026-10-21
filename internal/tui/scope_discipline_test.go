package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/prompt"
)

// 方案 A（2026-10-19 拍板）：范围纪律必须进 TUI 系统提示（stream.go 内联 baseParts）。
// 文案 SSOT = prompt.ScopeDisciplinePrompt；Runner/PromptBuilder 侧另有对偶判据
// internal/prompt/scope_discipline_test.go，两侧都验、缺一即红。
func TestW106_TUISystemPromptIncludesScopeDiscipline(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	prompts := m.buildSystemPrompt()
	combined := strings.Join(layerBlocksToStrings(prompts), "\n")
	if !strings.Contains(combined, prompt.ScopeDisciplinePrompt) {
		t.Fatal("TUI 系统提示缺少  范围纪律（prompt.ScopeDisciplinePrompt）")
	}
}
