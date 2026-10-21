package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// 父命令补全语义。
//
// 现象（用户直报）：敲 `/checkpoint` 回车后想接着打 `/checkpoint show`，但命令提示弹窗消失，
// 得重新敲一遍。根因两层：
// 1. `typeahead.SuggestCommands` 对「exact 命令名 + 尾随空格」无条件关闭弹窗
// （`internal/typeahead/typeahead.go` 旧 :129-136），不管该命令有没有已注册子命令；
// 2. 裸父命令回车走 `typeaheadSelectionAlreadyTyped` ⇒ 关弹窗 ⇒ 执行裸命令打印静态 usage 死文本。
//
// 新语义（对标 同类 CLI 弹窗过滤器只取首 token / Claude argumentHint 常驻）：
// - 叶子命令（registry 无 "name ..." 子命令）+ 空格 ⇒ 仍关闭弹窗（既有语义，/permissions 判例不变）；
// - 父命令（registry 注册了 "name ..." 子命令）+ 空格 ⇒ 弹出子命令候选；
// - 裸父命令回车 ⇒ 不执行死 usage，展开为 "/name " 并弹出子命令候选。
//
// 子命令候选的 SSOT = 命令注册表本身（checkpointCommands 已注册全名子命令），不新增第二份清单。

// TestW107ParentCommandSpaceShowsSubcommands：`/checkpoint `（父命令+空格）必须弹出子命令候选。
func TestW107ParentCommandSpaceShowsSubcommands(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("/checkpoint ")
	m.updateTypeaheadSuggestions()

	if !m.suggestionState.Visible {
		t.Fatal(": 父命令 /checkpoint + 空格应弹出子命令候选，而不是关闭弹窗" +
			"（用户现象：敲完 /checkpoint 想接 show，提示却消失了）")
	}
	got := completionDisplayNames(m.suggestionState.Completions)
	want := []string{
		"checkpoint delete",
		"checkpoint list",
		"checkpoint restore",
		"checkpoint save",
		"checkpoint show",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(": 子命令候选 = %v, want %v（且不得再含父命令本身）", got, want)
	}
}

// TestW107ParentCommandSpaceShowsSubcommands: 叶子命令回归护栏 ——
// `/permissions `（无子命令注册）必须保持既有语义：关闭弹窗。
// （本条在 TestSlashCompletionFiltersMultiwordCommandsAndClosesForArguments 已有，
// 这里按「 只改父命令分支、不碰叶子」的口径再钉一次，防实现把分支写反。）
func TestW107LeafCommandSpaceStillCloses(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("/permissions ")
	m.updateTypeaheadSuggestions()
	if m.suggestionState.Visible {
		t.Fatal(": 叶子命令 /permissions + 空格仍应关闭弹窗（既有语义不得被连带改变）")
	}
}

// TestW107BareParentEnterExpandsInsteadOfExecuting：裸 `/checkpoint` 回车
// 不得执行并打印静态 usage 死文本（交互链断），而应展开为 "/checkpoint " 并弹子命令候选。
func TestW107BareParentEnterExpandsInsteadOfExecuting(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("/checkpoint")
	m.updateTypeaheadSuggestions()
	if !m.suggestionState.Visible {
		t.Fatal("前置失败：/checkpoint 应先弹出候选，测试夹具异常")
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	if got := updated.textarea.Value(); got != "/checkpoint " {
		t.Fatalf(": 裸父命令回车后输入框 = %q, want %q（展开而非执行死 usage）",
			got, "/checkpoint ")
	}
	if !updated.suggestionState.Visible {
		t.Fatal(": 裸父命令回车后应弹出子命令候选（提示常驻，用户可直接接着敲 show）")
	}
	if updated.sessionService.State() != StateIdle {
		t.Fatal(": 裸父命令回车是展开语义，不得真的执行 /checkpoint")
	}
}
