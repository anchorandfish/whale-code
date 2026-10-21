package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// w049SameTailIDPair 见 session 包同名夹具说明：末 8 位 hex 相同的两个合法主键，
// 用来把「随机尾段也会撞车」这个低概率事件确定性地造出来。
func w049SameTailIDPair() (string, string) {
	return "01a13661-75ad-7223-8dca-1111deadbeef",
		"01a13661-75ad-7223-8dca-2222deadbeef"
}

// TestW049_SessionListDisambiguatesCollidingShortCodes 走生产入口 `/session list`：
// **即使两个会话的最短短码真的撞上**（随机尾段也有生日碰撞概率），列表里也必须给出
// 两个互不相同的句柄，且各自回填 Resolve 命中自己。
//
// 与 TestW049_SessionListShortCodeIdentifiesSession 的分工：那条钉的是「时间戳前缀
// 不再抹平短码」（概率问题已解决）；本条钉的是「撞了也不能分不清」（**保证**问题）——
// 靠的是按需加长，而不是把概率压得更小。
func TestW049_SessionListDisambiguatesCollidingShortCodes(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}

	a, b := w049SameTailIDPair()
	if session.ShortID(a) != session.ShortID(b) {
		t.Fatalf("前置条件：两个主键的最短短码应相同，实际 %q / %q（%s / %s）",
			session.ShortID(a), session.ShortID(b), a, b)
	}
	for _, id := range []string{a, b} {
		if err := store.AppendBatchNamed(id, "说你好",
			[]session.Message{session.UserText("说你好")}); err != nil {
			t.Fatalf("AppendBatchNamed(%s): %v", id, err)
		}
	}

	out := m.handleSessions()
	codes := w049ShortCodes(out, "说你好")
	if len(codes) != 2 {
		t.Fatalf("应渲染 2 行「说你好」会话，实际 %d 行：\n%s", len(codes), out)
	}
	if codes[0] == codes[1] {
		t.Fatalf("最短短码撞车时列表仍显示相同句柄 [%s] ⇒ 与  原症状相同\n列表：\n%s",
			codes[0], out)
	}
	for _, c := range codes {
		if len(c) <= session.ShortIDLen {
			t.Errorf("撞车时的句柄应被加长（> %d 位），实际 %q（%d 位）",
				session.ShortIDLen, c, len(c))
		}
	}

	// 用户「看到句柄 → /resume <句柄>」的动作逐字重放。
	got := make(map[string]string, 2)
	for _, c := range codes {
		resolved, err := store.Resolve(c)
		if err != nil {
			t.Fatalf("列表里的句柄 [%s] 点名失败（用户照抄列表却选不中）：%v", c, err)
		}
		got[c] = resolved
	}
	if got[codes[0]] == got[codes[1]] {
		t.Fatalf("两个句柄都解析到同一会话 %s", got[codes[0]])
	}
	want := map[string]bool{a: true, b: true}
	for c, resolved := range got {
		if !want[resolved] {
			t.Errorf("句柄 [%s] 解析到 %s，不在两个已建会话内", c, resolved)
		}
	}

	// 交互式选择器（裸 /session）用的是同一条句柄来源，标签也必须能分辨。
	if out := m.handleSessionsPicker(); out == "" {
		t.Fatal("handleSessionsPicker 应返回提示语")
	}
	pc := m.toolService.PendingChoice()
	if pc == nil {
		t.Fatal("PendingChoice 未设置")
	}
	if len(pc.Options) != 2 {
		t.Fatalf("选择器应有 2 个选项，实际 %d", len(pc.Options))
	}
	if pc.Options[0].Label == pc.Options[1].Label {
		t.Fatalf("选择器两个选项标签完全相同，无法分辨：%q", pc.Options[0].Label)
	}
	for _, o := range pc.Options {
		if !strings.Contains(o.Label, "[") {
			t.Errorf("选项标签应含方括号句柄：%q", o.Label)
		}
	}
}

// TestW049_LongestCommonTailStillShort 反向：**没有**撞车时不得无谓加长。
//
// 若为了「保险」把所有句柄都加长到 12 位，行宽会白白变长；这条钉住「按需」二字。
func TestW049_LongestCommonTailStillShort(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}
	// 两个主键的最短短码不同（末 8 位：deadbeef vs ef012345）。
	ids := []string{
		"01a13661-75ad-7223-8dca-1111deadbeef",
		"01a13661-75ad-7223-8dca-abcdef012345",
	}
	for _, id := range ids {
		if err := store.AppendBatchNamed(id, "说你好",
			[]session.Message{session.UserText("说你好")}); err != nil {
			t.Fatalf("AppendBatchNamed(%s): %v", id, err)
		}
	}

	out := m.handleSessions()
	for _, c := range w049ShortCodes(out, "说你好") {
		if len(c) != session.ShortIDLen {
			t.Errorf("无碰撞时句柄应保持最短 %d 位，实际 %q（%d 位）",
				session.ShortIDLen, c, len(c))
		}
	}
}
