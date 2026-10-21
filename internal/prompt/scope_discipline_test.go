package prompt

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
)

// 方案 A（2026-10-19 拍板）：范围纪律必须进系统提示（Runner/PromptBuilder 侧）。
// 文案 SSOT = ScopeDisciplinePrompt 常量；TUI 侧（stream.go 内联 baseParts）
// 另有对偶判据 internal/tui/scope_discipline_test.go，两侧都验、缺一即红。
func TestW106_BuilderIncludesScopeDiscipline(t *testing.T) {
	dir := t.TempDir()
	md := config.NewMagicDocs(dir) //nolint:staticcheck // H4 保留 V1 作为 fallback
	b := NewBuilder(dir, "test-model", md, nil, nil, 100*1024)
	blocks := b.Build("", "hello", "English")

	combined := ""
	for _, blk := range blocks {
		combined += blk.Text + "\n"
	}
	if !strings.Contains(combined, ScopeDisciplinePrompt) {
		t.Fatal("Runner/PromptBuilder 系统提示缺少  范围纪律（ScopeDisciplinePrompt）")
	}
}
