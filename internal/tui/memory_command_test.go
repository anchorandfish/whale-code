// Package tui — memory_command_test.go N12 /memory 命令 5 子命令分发测试（审查 prompt §3.7 测试 4）
//
// 本文件实现 N12-memory-command-审查prompt.md §3.7 测试 4 TestHandleMemory_Subcommands。
// 测试 HandleMemoryCommand 的 5 子命令分发（list/edit/show/add/delete）+ 无参数等同 list
// + 无效子命令返回用法提示 + nil store 防御。edit 子命令需 $EDITOR 集成，跨平台差异推 CI。
// 本机 Esafenet DLP 限制可用 go test -c 验证编译，运行推 CI。
package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
)

// TestHandleMemory_Subcommands 验证 /memory 命令 5 子命令分发（P0-4）。
// 反例：/memory 仅支持 set/get/delete（V1 现状）→ 无 list/edit/show/add。
// 覆盖 list/show/add/delete/无参数/无效子命令/nil store 七个场景。
// edit 子命令需 $EDITOR，本机跨平台编辑器差异推 CI，此处跳过。
func TestHandleMemory_Subcommands(t *testing.T) {
	store, err := config.NewMemoryStore(t.TempDir(), "/test/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore 失败: %v", err)
	}

	// 预置一条记忆供 show/list 测试
	if err := store.Write(config.MemoryEntryV2{
		Type:        config.MemoryTypeUser,
		Name:        "preferences",
		Content:     "喜欢用 Vim",
		Description: "edit pref",
	}); err != nil {
		t.Fatalf("Write 预置记忆失败: %v", err)
	}

	// /memory list → 应包含文件名
	out, err := HandleMemoryCommand(store, "list")
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	if !strings.Contains(out, "preferences") {
		t.Errorf("list 应包含文件名 'preferences'，实际: %s", out)
	}

	// /memory show preferences → 应含 type 和 content
	out, err = HandleMemoryCommand(store, "show preferences")
	if err != nil {
		t.Fatalf("show 失败: %v", err)
	}
	if !strings.Contains(out, "user") {
		t.Errorf("show 应含 type 'user'，实际: %s", out)
	}
	if !strings.Contains(out, "喜欢用 Vim") {
		t.Errorf("show 应含 content，实际: %s", out)
	}

	// /memory add newmem hello world → 应含保存确认
	out, err = HandleMemoryCommand(store, "add newmem hello world")
	if err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if !strings.Contains(out, "记忆已保存") {
		t.Errorf("add 应含保存确认，实际: %s", out)
	}

	// 验证 add 确实写入
	list, err := store.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	found := false
	for _, e := range list {
		if e.Name == "newmem" {
			found = true
		}
	}
	if !found {
		t.Errorf("add 后应在 list 中找到 newmem")
	}

	// /memory delete newmem → 应含删除确认
	out, err = HandleMemoryCommand(store, "delete newmem")
	if err != nil {
		t.Fatalf("delete 失败: %v", err)
	}
	if !strings.Contains(out, "记忆已删除") {
		t.Errorf("delete 应含删除确认，实际: %s", out)
	}

	// /memory 无参数 → 等同 list
	out, err = HandleMemoryCommand(store, "")
	if err != nil {
		t.Fatalf("空参数失败: %v", err)
	}
	if !strings.Contains(out, "preferences") {
		t.Errorf("空参数应等同 list 包含 'preferences'，实际: %s", out)
	}

	// /memory invalid → 返回错误含用法提示
	out, err = HandleMemoryCommand(store, "invalid")
	if err == nil {
		t.Fatal("invalid 子命令应返回错误，实际 nil")
	}
	// 批 5：error 面英文单侧（usage 提示随 error 返回）
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("invalid 应含用法提示，实际: %v", err)
	}

	// nil store → 返回错误（nil 防御）
	_, err = HandleMemoryCommand(nil, "list")
	if err == nil {
		t.Fatal("nil store 应返回错误，实际 nil")
	}
	// 批 5：error 面英文单侧
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("nil store 错误应含 'not initialized'，实际: %v", err)
	}
}
