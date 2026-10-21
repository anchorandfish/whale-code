package tui

// 批 5 守卫：/memory 命令输出双语形态（HandleMemoryCommand 共享入口，
// TUI 与 plain 同源）。error 面英文单侧（批 3/4 先例），string 输出双语形态。
//
// 形态判定 SSOT = util.AssertChineseOnlyOnIndentedLines。

import (
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/util"
)

func b5MemoryStore(t *testing.T) *config.MemoryStore {
	t.Helper()
	s, err := config.NewMemoryStore(t.TempDir(), filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return s
}

// TestW083B5_MemoryCommandOutputs_BilingualForm 走真实 store 钉 /memory 输出形态。
func TestW083B5_MemoryCommandOutputs_BilingualForm(t *testing.T) {
	store := b5MemoryStore(t)

	// ① 空列表（双语提示）。
	out, err := HandleMemoryCommand(store, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Errorf("/memory list：中文出现在非缩进行 ⇒ %q\n输出：\n%s", v, out)
	}
	if !contains(out, "No memories yet") {
		t.Errorf("list 应含英文主行，实际：\n%s", out)
	}

	// ② add 成功（双语确认）→ list 有条目（数据行英文数据 + 英文框架）。
	out, err = HandleMemoryCommand(store, "add buy-milk remember the oat milk")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Errorf("/memory add：中文出现在非缩进行 ⇒ %q\n输出：\n%s", v, out)
	}

	// 记忆描述用英文（数据隔离）；freshness 标签已英文化（批 5）⇒ list 行应无汉字非缩进。
	// 用足够新的条目（freshness 为空）规避数据行内任何历史文案。
	out, err = HandleMemoryCommand(store, "list")
	if err != nil {
		t.Fatalf("list after add: %v", err)
	}
	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Errorf("/memory list(有条目)：中文出现在非缩进行 ⇒ %q\n输出：\n%s", v, out)
	}

	// ③ delete 成功（双语确认）。
	out, err = HandleMemoryCommand(store, "delete buy-milk")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Errorf("/memory delete：中文出现在非缩进行 ⇒ %q\n输出：\n%s", v, out)
	}

	// ④ 未知子命令：error 面英文单侧。
	_, err = HandleMemoryCommand(store, "bogus")
	if err == nil {
		t.Fatal("未知子命令应报错")
	}
	if util.HasHan(err.Error()) {
		t.Errorf("未知子命令 error 含汉字：%q（error 面英文单侧）", err.Error())
	}

	// ⑤ usage error 英文单侧（缺参）。
	for _, args := range []string{"edit", "show", "add", "delete"} {
		if _, err := HandleMemoryCommand(store, args); err == nil {
			t.Errorf("/memory %s 缺参应报错", args)
		} else if util.HasHan(err.Error()) {
			t.Errorf("/memory %s 缺参 error 含汉字：%q", args, err.Error())
		}
	}
}
