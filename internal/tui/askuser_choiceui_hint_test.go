package tui

// askuser_choiceui_hint_test.go — 判别测试：选项框的引导提示必须在**唯一活线**渲染器上可见。
//
// # 背景
//
// 「[i] 补充自由输入」这条提示原先只写在 `renderPendingChoice` 里 —— 而该函数**生产零调用**
// （调用点只有测试），整个函数被 linker 丢弃。机械证据（2026-09-22 实测件）：
//
//	go tool nm whale | grep -c 'renderChoiceUI' → 1 （活线：真实渲染点）
//	go tool nm whale | grep -c 'renderPendingChoice' → 0 （死线：连函数带文案被丢弃）
//	python3: whale 字节面 count('补充自由输入') → 0
//
// ⇒ 用户界面上从未显示过这条提示；用户之所以知道要按 i，靠的是外部文档兜底。
// 现提示并入 `renderChoiceUI`，并把断言钉在它的**输出**上（不再钉死函数，否则又是假绿）。
//
// ★ 判据落点的教训：改文案后不能只看源码或单测，要看**真实渲染函数的输出**。

import (
	"os"
	"strings"
	"testing"
)

// TestN128_ChoiceUIShowsFreeTextHintForAskUser 含 Other 哨兵的提问（AskUser 日常对话）必须显示按 i 的提示。
//
// 修前：renderChoiceUI 底部只有 `↑↓ select · Enter apply · Esc cancel` ⇒ 红。
func TestN128_ChoiceUIShowsFreeTextHintForAskUser(t *testing.T) {
	m := newAskUserPendingModel(t)

	pending := m.toolService.PendingChoice()
	if pending == nil || !hasOtherOption(pending.Options) {
		t.Fatal("前置失败：AskUser 提问应带 Other 哨兵项")
	}

	out := m.renderChoiceUI()
	// 2026-09-23 · ：文案已从 `[i] Provide your own answer`（与 Other 选项描述逐字重复）
	// 改为 `[i] Add a note to your choice` —— 界面上把「补充说明」与「我自己作答」分成两个入口。
	if !strings.Contains(out, "[i] Add a note to your choice") {
		t.Fatalf("★ 含 Other 的提问必须显示「按 i 补充 note」提示，实际输出：\n%s", out)
	}
	// 形态契约：中文辅助层必须缩进另起一行（形态 SSOT = bilingual.go）。
	// ★ 只检查**我们自己的文案行** —— 提问文本与选项描述来自调用方数据（本夹具就是中文提问、
	// 中文选项描述），它们不属于「文案形态门」的管辖范围（形态门管的是我们自己写的 copy）。
	if !strings.Contains(out, copyIndent+"按 i 给所选方案补充一句说明") {
		t.Fatalf("提示的中文辅助层必须以 copyIndent 缩进另起一行，实际输出：\n%s", out)
	}
}

// TestN128_ChoiceUIHidesHintForLocalMenu 反向保：本地菜单（权限/会话/安全套餐）无 Other ⇒ 不得出现该提示。
// 否则会把「按 i 补充」的语义泄漏到不该有的菜单上。
func TestN128_ChoiceUIHidesHintForLocalMenu(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(0)
	m.handleCommand("/permissions")

	if m.toolService.PendingChoice() == nil {
		t.Fatal("前置失败：/permissions 未打开选择器")
	}

	out := m.renderChoiceUI()
	if strings.Contains(out, "[i]") {
		t.Fatalf("本地菜单不得显示「按 i 补充」提示，实际输出：\n%s", out)
	}
}

// TestN128_ChoiceUIShowsTypingHintInSubmode 子态下提示应切换成输入指引（可发现性）：先提示「先输入」，
// 输入后提示「Enter 提交 · Esc 返回」。
func TestN128_ChoiceUIShowsTypingHintInSubmode(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))

	empty := m.renderChoiceUI()
	if !strings.Contains(empty, "Type your answer") {
		t.Fatalf("子态空内容时应提示先输入内容，实际输出：\n%s", empty)
	}
	if !strings.Contains(empty, copyIndent+"请先输入内容") {
		t.Fatalf("空内容提示的中文辅助层必须缩进另起一行，实际输出：\n%s", empty)
	}

	m = typeAtHumanPace(t, m, "OAuth2")
	typed := m.renderChoiceUI()
	if !strings.Contains(typed, "Enter submit") {
		t.Fatalf("子态有内容时应提示 Enter 提交 · Esc 返回，实际输出：\n%s", typed)
	}
	if !strings.Contains(typed, copyIndent+"Enter 提交 · Esc 返回选择") {
		t.Fatalf("子态提示的中文辅助层必须缩进另起一行，实际输出：\n%s", typed)
	}
}

// TestN128_DeadChoiceRendererNotRevived 结构门： 的死线渲染器不得复活。
//
// 判别性：把 `func renderPendingChoice(...)` 加回 render_extras.go ⇒ 红。
// 为什么要有这条门：本缺陷的形态是「改了提示，但改在了没人调用的渲染器上」——
// 源码里看得见、单测也是绿的，只有产物面（nm / 字节计数）能戳穿。
// 留住一个生产零调用的同形渲染器，等于把这个陷阱重新埋回去。
func TestN128_DeadChoiceRendererNotRevived(t *testing.T) {
	src, err := os.ReadFile("render_extras.go")
	if err != nil {
		t.Fatalf("读取 render_extras.go 失败：%v", err)
	}
	if strings.Contains(string(src), "func renderPendingChoice(") {
		t.Fatal("检测到 renderPendingChoice 被重新引入 —— 该函数生产零调用（只被测试引用），" +
			"整个函数会被 linker 丢弃，写在这里的用户可见文案永远不会显示。\n" +
			"若要新增提示，请加到唯一活线 renderChoiceUI（真实渲染点：app.go 的 StateWaitingChoice 分支）。")
	}
}
