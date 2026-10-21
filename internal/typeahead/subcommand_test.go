package typeahead

import (
	"reflect"
	"testing"
)

// SuggestCommands 对「exact 命令名 + 尾随空格」的分流语义。
//
// 旧行为：无条件 return nil（弹窗关闭）⇒ 用户敲完 /checkpoint 想接 show，提示消失。
// 新行为：
// - 叶子命令（候选集里没有 "name ..." 子命令）+ 空格 ⇒ 仍返回 nil（既有语义，等参数输入）；
// - 父命令（候选集里存在 "name ..." 子命令）+ 空格 ⇒ 返回子命令候选（不含父命令本身）。
//
// 判别性：before（旧行为）下 TestW107ParentSpaceReturnsChildren 必红。

func w107Fixture() []CommandItem {
	return []CommandItem{
		{Name: "checkpoint", Description: "parent"},
		{Name: "checkpoint save", Description: "create"},
		{Name: "checkpoint list", Description: "list"},
		{Name: "checkpoint restore", Description: "restore"},
		{Name: "checkpoint delete", Description: "delete"},
		{Name: "checkpoint show", Description: "show"},
		{Name: "permissions", Description: "leaf"},
	}
}

// TestW107ParentSpaceReturnsChildren：父命令 + 空格 ⇒ 子命令候选（新语义，before 必红）。
func TestW107ParentSpaceReturnsChildren(t *testing.T) {
	got := completionNames(SuggestCommands("checkpoint ", w107Fixture()))
	want := []string{
		"checkpoint delete",
		"checkpoint list",
		"checkpoint restore",
		"checkpoint save",
		"checkpoint show",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(": SuggestCommands(\"checkpoint \") = %v, want %v", got, want)
	}
}

// TestW107LeafSpaceStillCloses：叶子命令 + 空格 ⇒ 仍关闭（回归护栏，before/after 均绿）。
func TestW107LeafSpaceStillCloses(t *testing.T) {
	if got := SuggestCommands("permissions ", w107Fixture()); len(got) != 0 {
		t.Fatalf(": 叶子命令 + 空格应返回空，got %v", completionNames(got))
	}
}

// TestW107PartialSubcommandPrefix：敲到一半的子命令走常规前缀匹配（既有语义不受影响）。
func TestW107PartialSubcommandPrefix(t *testing.T) {
	got := completionNames(SuggestCommands("checkpoint s", w107Fixture()))
	want := []string{"checkpoint save", "checkpoint show"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(": 部分子命令前缀 = %v, want %v", got, want)
	}
}

// TestW107HasSubcommandsPredicate：HasSubcommands 谓词（TUI 回车语义复用同一实现）。
func TestW107HasSubcommandsPredicate(t *testing.T) {
	if !HasSubcommands("checkpoint", w107Fixture()) {
		t.Fatal(": checkpoint 应判定为有子命令")
	}
	if HasSubcommands("permissions", w107Fixture()) {
		t.Fatal(": permissions 应判定为叶子命令")
	}
	// 前缀同名的独立命令不算子命令：夹具只含 "checkpoint" 与 "checkpointx"（无真子命令），
	// "checkpointx" 不以 "checkpoint " 开头 ⇒ 不得误判（"checkpoint" + 非空白字符 ≠ 子命令）。
	fixture := []CommandItem{{Name: "checkpoint"}, {Name: "checkpointx"}}
	if HasSubcommands("checkpoint", fixture) {
		t.Fatal(": checkpointx 不得被当作 checkpoint 的子命令")
	}
}
