package tui

// unread_badge_hint_test.go — P3：「回到底部」的提示必须点出一个**真的能按出来、且真的有效**的键
//。
//
// # 报障与根因
//
// 状态栏角标 `⏬ Unread: N below (End)` 是全应用**唯一**教用户回到底部的提示（口径）。
// 但 `(End)` 是**第二份**手写的「回底用哪个键」—— 与 viewportNavKey 的 `case "end"` 各写各的
//（「同一语义两处实现」同一族）。后果已经真实发生：mac 笔记本**没有独立的 End 键**
//（End = Fn+→）⇒ 这条唯一的提示在用户手上那台机器上**按不出来**，用户反馈
//「批准后回不到最新」。
//
// # 修法与判据形态
//
// 修法 = 把「键名」与「本平台键盘写法」收进唯一声明 `jumpToBottomHintFor(goos)`（app.go，
// 与 viewportNavKey 相邻），渲染层只取 Label，不再出现键名字面量。
//
// 本条三个断言（缺任何一个都可被绕过）：
//
//	① 文案侧（纯函数）：darwin ⇒ Fn+→，linux/windows ⇒ End —— 可在一台机器上验证全部平台；
//	② 行为侧：文案点出的那个键（hint.Key），驱动**生产入口** `Update` 必须真的跳到底
// （面板在位 + 无面板各跑一遍 —— "回不到最新"正是发生在面板期间）；
//	③ 渲染侧：非底部时状态栏真的出现本平台的键写法。
//
// 只有 ①③ 是"文案对不对"，只有 ② 是"键活不活"。两条合起来才同时封住
// 「文案改了但键失效」与「键改了但文案没跟上」两个方向。

import (
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// viewportNavKeyCandidates 是生产按键链能收到的导航键事件全集。
//
// ★ 刻意**不**手写「键名 → tea 事件」映射表：改用 bubbletea 自己的 `KeyMsg.String()` 反查。
// 键名的唯一权威是 bubbletea，不是本测试 —— 若 hint.Key 被改成任何不存在/未登记的字符串，
// 反查失败即红，"提示对着一个按不出来的键说话"因此不可静默通过。
var viewportNavKeyCandidates = []tea.KeyMsg{
	{Type: tea.KeyPgUp},
	{Type: tea.KeyPgDown},
	{Type: tea.KeyCtrlUp},
	{Type: tea.KeyCtrlDown},
	{Type: tea.KeyHome},
	{Type: tea.KeyEnd},
}

func keyMsgForName(t *testing.T, name string) tea.KeyMsg {
	t.Helper()
	for _, k := range viewportNavKeyCandidates {
		if k.String() == name {
			return k
		}
	}
	t.Fatalf("提示声明的键名 %q 不是任何生产按键事件的名称（bubbletea KeyMsg.String()）"+
		"—— 提示正在对着一个按不出来的键说话", name)
	return tea.KeyMsg{}
}

// TestUnreadBadgeHint_LabelMatchesPlatform 断言**每个平台**的文案（纯函数，单机可验证跨平台）。
func TestUnreadBadgeHint_LabelMatchesPlatform(t *testing.T) {
	cases := []struct {
		goos      string
		wantLabel string
		reason    string
	}{
		{"darwin", "Fn+→", "mac 笔记本无独立 End 键（End = Fn+→）"},
		{"linux", "End", "Linux 终端键盘有独立 End 键"},
		{"windows", "End", "Windows 终端键盘有独立 End 键"},
	}
	for _, c := range cases {
		t.Run(c.goos, func(t *testing.T) {
			hint := jumpToBottomHintFor(c.goos)
			if hint.Label != c.wantLabel {
				t.Fatalf("%s 的回底提示应为 %q（%s），got %q", c.goos, c.wantLabel, c.reason, hint.Label)
			}
			if strings.ContainsAny(hint.Label, " \t") {
				t.Errorf("提示文案 %q 含空白，状态栏单行排布会被拆开", hint.Label)
			}
		})
	}
}

// TestUnreadBadgeHint_AdvertisedKeyActuallyWorks 是 P3 的**行为侧**判别器：
// 文案点出的键，驱动生产入口 `Update` 必须真的回到最新。
//
// 覆盖「四种面板在位 + 无面板」：面板在位的路径正是 P1 新接上的（修前该键被面板分支吞掉），
// 无面板是收口前的旧路径 —— 两条都必须活。若 P1 被回退而文案仍宣称能用，本条必须红。
func TestUnreadBadgeHint_AdvertisedKeyActuallyWorks(t *testing.T) {
	scenarios := []struct {
		name  string
		apply func(t *testing.T, m *Model)
	}{{"no-panel", nil}}
	scenarios = append(scenarios, panelKinds()...)

	for _, goos := range []string{"darwin", "linux"} {
		hint := jumpToBottomHintFor(goos)
		key := keyMsgForName(t, hint.Key)

		for _, s := range scenarios {
			t.Run(goos+"/"+s.name, func(t *testing.T) {
				m := panelScrollFixture(t, s.apply)
				m.uiService.Viewport().GotoTop()
				if m.uiService.Viewport().AtBottom() {
					t.Fatal("夹具不健全：GotoTop 后仍在底部，该键不可判别")
				}

				res, _ := m.Update(key)
				got := modelFromUpdate(t, res)

				if !got.uiService.Viewport().AtBottom() {
					t.Fatalf("提示教用户按 %q 回到底部，但在 %s 平台 / %s 场景下按它**回不到底部**"+
						"（offset=%d/%d）—— 这条提示在说谎",
						hint.Label, goos, s.name,
						got.uiService.Viewport().YOffset, maxYOffset(got))
				}
			})
		}
	}
}

// TestUnreadBadgeHint_StatusBarShowsPlatformLabel 是**渲染侧**断言：
// 非底部时状态栏必须出现本机平台真实存在的那个键写法。
func TestUnreadBadgeHint_StatusBarShowsPlatformLabel(t *testing.T) {
	m := panelScrollFixture(t, nil)
	m.unreadBelow = 3
	m.uiService.Viewport().GotoTop()

	view := ansi.Strip(m.renderStatusBar())
	want := jumpToBottomHint().Label

	if !strings.Contains(view, "Unread:") {
		t.Fatalf("前置不成立：非底部且 unreadBelow>0 时状态栏应出现角标，got:\n%s", view)
	}
	if !strings.Contains(view, want) {
		t.Fatalf("角标必须写本平台（%s）真实存在的键 %q，状态栏实际:\n%s", runtime.GOOS, want, view)
	}
}
