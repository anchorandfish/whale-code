package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// mouse_selection_screen_test.go — C-2判据：整屏拖选。
//
// 用户视角症状：按住左键拖一段、松手自动复制，这套**只对中间那段对话生效**；
// 确认框 / 选择面板 / 提示 / 状态栏的文字都选不中。要的是**整屏都能拖选**
// （可分区选，也可跨区块连选）。
//
// 根因：屏幕是一整块，坐标系却有四套 —— 拖选按「区」做（viewport 坐标），
// 而弹窗/提示/状态栏只是 buildView 里另外几个区块的拼接，根本不进 viewport。
//
// 修法（批 C 换前提）：选区升格为**屏幕坐标**，文本来源 = buildView() 那一帧的
// 整屏文档（五区共用一套坐标，见 mouse_selection.go screenDocGeom）。
//
// 判据形态（结构面，不依赖散文关键词）：把 View() 交给**与 bubbletea 同一条裁剪规则**
// 得到「用户在屏幕上真正看到的行」，再断言「屏幕第 r 行的文本」== 「拖选该行复制到的文本」，
// **逐区块**验（chat / status / input-面板 / toast / monitor），并要求跨区块按屏幕顺序拼接。
//
// 生产入口：NewModel → WindowSizeMsg（置 ready，账本才有效）→ Model.Update(MouseMsg)
// → mouseSelectionText()（finishMouseSelection 交给剪贴板的就是它的返回值）。

// newScreenSelectionFixture 造一个「账本有效」的整屏夹具：WindowSizeMsg 让
// uiService 进入 Ready（layout.go buildView 才会产出几何账本），再喂对话内容。
func newScreenSelectionFixture(t *testing.T, width, height, chatLines int) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	mm := res.(Model)
	mm.reassignServices()
	lines := make([]string, 0, chatLines)
	for i := 0; i < chatLines; i++ {
		lines = append(lines, fmt.Sprintf("CHAT-%02d-marker", i))
	}
	mm.setViewportContent(strings.Join(lines, "\n"))
	mm.uiService.Viewport().GotoTop()
	mm.recalcViewportHeight()
	return &mm
}

// findRegion 从账本里取指定名称的区块；缺失即夹具不健全（本判据不可判别）。
func findRegion(t *testing.T, l screenLayout, name string) screenRegion {
	t.Helper()
	for _, r := range l.regions {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("夹具不健全：账本里没有 %q 区块", name)
	return screenRegion{}
}

// dragWholeScreenRow 走生产入口拖选屏幕上第 row 行（x=0 → x=200），
// 返回（复制文本, 是否进入了拖选态）。未进入拖选态 ⇒ 该行不可选。
func dragWholeScreenRow(t *testing.T, m *Model, row int) (string, bool) {
	t.Helper()
	m.clearMouseSelection()
	res, _ := m.Update(tea.MouseMsg{
		X: 0, Y: row, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	mm := res.(Model)
	if !mm.mouseSelection.dragging {
		return "", false
	}
	res2, _ := mm.Update(tea.MouseMsg{
		X: 200, Y: row, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion,
	})
	mm2 := res2.(Model)
	return mm2.mouseSelectionText(), true
}

// assertRowCopyable 断言「屏幕上第 row 行的渲染文本」可以被拖选复制出来，且逐字一致。
//
// 这是 C-2 的核心不变量：屏幕是唯一的真值面，复制必须与之一一对应（不分区块）。
func assertRowCopyable(t *testing.T, name string, m *Model, row int) {
	t.Helper()
	screen := screenLines(t, m)
	if row < 0 || row >= len(screen) {
		t.Fatalf("[%s] 屏幕行 %d 越界（共 %d 行）", name, row, len(screen))
	}
	want := strings.TrimRight(ansi.Strip(screen[row]), " ")
	got, dragging := dragWholeScreenRow(t, m, row)
	if !dragging {
		// 失败时必须能自证「输入完整」：把账本/区块归属一并打出，
		// 否则「按不中」与「账本失效回退到估算」两种成因无法区分（假红/假绿同源）。
		l := m.measureScreenLayout()
		region := "NOT-FOUND"
		if r, ok := l.regionAtScreenRow(row); ok {
			region = r.name
		}
		t.Fatalf("[%s] 屏幕第 %d 行（%q）不在拖选区：按该行无法开始选择"+
			"（账本 valid=%v overflow=%d total=%d h=%d 区块归属=%s 可选=%v）",
			name, row, want, l.valid, l.overflow, l.total, m.uiService.Height(),
			region, m.regionSelectable(region))
	}
	if got != want {
		t.Errorf("[%s] 屏幕第 %d 行复制不一致：\n  屏幕上看到的 = %q\n  实际复制到的 = %q",
			name, row, want, got)
	}
}

// TestScreenSelectionCoversStatusBar —— 判据 1：状态栏文字可拖选。
//
// 根因面：状态栏是 buildView 的**最后一个区块**，不在 viewport 里 ⇒ 修前按不到
// （press 落到 input 分支，enterSelection 不启动）。修后按区块表命中 status。
func TestScreenSelectionCoversStatusBar(t *testing.T) {
	m := newScreenSelectionFixture(t, 120, 30, 12)
	l := m.measureScreenLayout()
	if !l.valid {
		t.Fatal("夹具不健全：几何账本不可用")
	}
	status := findRegion(t, l, "status")
	for i := 0; i < status.rows; i++ {
		row := status.viewTop - l.overflow + i
		if row < 0 {
			continue
		}
		screen := screenLines(t, m)
		if row >= len(screen) {
			break
		}
		if strings.TrimSpace(ansi.Strip(screen[row])) == "" {
			continue // 空行不作断言（无文本可复制）
		}
		assertRowCopyable(t, "status", m, row)
	}
}

// TestScreenSelectionSpansChatIntoStatusBar —— 判据 2：跨区块一次连选，
// 复制按**屏幕顺序**拼接（不是只取中间那段对话）。
func TestScreenSelectionSpansChatIntoStatusBar(t *testing.T) {
	m := newScreenSelectionFixture(t, 120, 40, 40)
	l := m.measureScreenLayout()
	if !l.valid {
		t.Fatal("夹具不健全：几何账本不可用")
	}
	chat := findRegion(t, l, "chat")
	status := findRegion(t, l, "status")
	chatRow := chat.viewTop - l.overflow // 聊天区首行（GotoTop ⇒ 内容是 CHAT-00-marker）
	// 状态栏是带边框的盒子，首行是上边框；取真正带文本的那一行（含 Permission:）。
	screen := screenLines(t, m)
	statusRow := -1
	for i := status.viewTop - l.overflow; i < status.viewTop-l.overflow+status.rows; i++ {
		if i < 0 || i >= len(screen) {
			continue
		}
		if strings.Contains(ansi.Strip(screen[i]), "Permission:") {
			statusRow = i
			break
		}
	}
	if statusRow <= chatRow {
		t.Fatalf("夹具不健全：状态栏文本行（%d）应在聊天区（%d）之下", statusRow, chatRow)
	}

	m.clearMouseSelection()
	res, _ := m.Update(tea.MouseMsg{
		X: 0, Y: chatRow, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	mm := res.(Model)
	if !mm.mouseSelection.dragging {
		t.Fatal("press 在聊天区应进入拖选态")
	}
	res2, _ := mm.Update(tea.MouseMsg{
		X: 200, Y: statusRow, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion,
	})
	got := res2.(Model).mouseSelectionText()

	chatAt := strings.Index(got, "CHAT-00-marker")
	permAt := strings.Index(got, "Permission:")
	if chatAt < 0 {
		t.Errorf("跨区块选区应包含聊天区文本，实际：\n%s", got)
	}
	if permAt < 0 {
		t.Errorf("跨区块选区应包含状态栏文本（Permission:），实际：\n%s", got)
	}
	if chatAt >= 0 && permAt >= 0 && chatAt > permAt {
		t.Errorf("跨区块选区应按屏幕顺序拼接（对话在上、状态栏在下）：\n%s", got)
	}
}

// TestScreenSelectionCopiesConfirmPanel —— 判据 1（弹窗分支）：面板态下
// 确认框的选项文字可拖选。
//
// 面板渲染在 input 区块（renderInputAreaForState → renderConfirmUI）。普通输入态
// 的 input 不可选（保留 textarea 拖选 / 按钮点击），但**面板态**整块是文本 ⇒ 可选。
func TestScreenSelectionCopiesConfirmPanel(t *testing.T) {
	m := newScreenSelectionFixture(t, 120, 30, 6)
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:  "bash",
		ToolInput: `{"command":"ls"}`,
		RiskLevel: "test",
		Kind:      ApprovalKindExec,
		Options:   buildApprovalOptions(ApprovalKindExec),
	})
	m.sessionService.SetState(StateWaitingConfirm)

	l := m.measureScreenLayout()
	if !l.valid {
		t.Fatal("夹具不健全：几何账本不可用")
	}
	input := findRegion(t, l, "input")
	screen := screenLines(t, m)

	hit := false
	for i := 0; i < input.rows; i++ {
		row := input.viewTop - l.overflow + i
		if row < 0 || row >= len(screen) {
			continue
		}
		plain := ansi.Strip(screen[row])
		if !strings.Contains(plain, "Allow once") {
			continue
		}
		hit = true
		assertRowCopyable(t, "confirm-panel", m, row)
	}
	if !hit {
		t.Fatal("夹具不健全：确认面板未渲染 'Allow once'，本判据不可判别")
	}
}

// TestScreenSelectionActiveContentRowNotHijacked —— 回归（判据 3）：
// 普通输入态的 textarea 行**不可**拖选（保留光标定位/输入框内拖选），
// 以免 C-2 把「点击输入框定位光标」改坏。
func TestScreenSelectionActiveContentRowNotHijacked(t *testing.T) {
	m := newScreenSelectionFixture(t, 120, 30, 12)
	l := m.measureScreenLayout()
	if !l.valid {
		t.Fatal("夹具不健全：几何账本不可用")
	}
	input := findRegion(t, l, "input")
	// 输入框文字行：input 顶部 + preview 高度 + 上边框（与 composer_mouse.go 同口径）。
	textareaRow := input.viewTop - l.overflow + m.lastPreviewHeight + 1
	if _, dragging := dragWholeScreenRow(t, m, textareaRow); dragging {
		t.Errorf("普通输入态的 textarea 行（屏幕第 %d 行）不应进入整屏拖选", textareaRow)
	}
}

// TestScreenSelectionRowMatchPairedOverflowAndShort —— 判据 4：判别面必须覆盖
// 「渲染行数 > 终端高度」这一格（bubbletea standard_renderer.go 会从顶部砍行）。
//
// 配对反证：① 长内容（超屏）能选对 ② 短内容同样能选对 —— 只跑其中一条 = 假绿。
func TestScreenSelectionRowMatchPairedOverflowAndShort(t *testing.T) {
	t.Run("short-no-overflow", func(t *testing.T) {
		m := newScreenSelectionFixture(t, 120, 30, 8)
		l := m.measureScreenLayout()
		if !l.valid {
			t.Fatal("夹具不健全：几何账本不可用")
		}
		if l.overflow != 0 {
			t.Fatalf("夹具前提不符：短内容不应超屏，overflow=%d", l.overflow)
		}
		assertRowCopyable(t, "short-chat", m, findRegion(t, l, "chat").viewTop-l.overflow)
		assertRowCopyable(t, "short-status", m, findRegion(t, l, "status").viewTop-l.overflow)
	})

	t.Run("long-overflow-crop", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		res, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
		mm := res.(Model)
		mm.reassignServices()
		lines := make([]string, 0, 60)
		for i := 0; i < 60; i++ {
			lines = append(lines, fmt.Sprintf("LONG-%02d-marker", i))
		}
		mm.uiService.Viewport().Width = 120
		// 故意把视口撑高到超出终端预算 ⇒ View() 渲染行数 > 终端高度 ⇒ bubbletea 顶部砍行。
		// （真机上"超屏"是预算漏算区块时的缺陷态；本格就是把修复面钉在这一格上。）
		mm.uiService.Viewport().Height = 30
		mm.setViewportContent(strings.Join(lines, "\n"))
		mm.uiService.Viewport().GotoTop()

		l := mm.measureScreenLayout()
		if !l.valid {
			t.Fatal("夹具不健全：几何账本不可用")
		}
		if l.overflow <= 0 {
			t.Fatalf("夹具前提不符：应渲染超屏，overflow=%d", l.overflow)
		}
		screen := screenLines(t, &mm)
		if len(screen) != mm.uiService.Height() {
			t.Fatalf("夹具前提不符：屏幕行数 %d ≠ 终端高度 %d", len(screen), mm.uiService.Height())
		}
		// 屏幕首行（被砍行后的第一行）、聊天区可见末行、状态栏行都必须可选且复制一致。
		probe := []int{0, findRegion(t, l, "status").viewTop - l.overflow, len(screen) - 1}
		for _, row := range probe {
			if row < 0 || row >= len(screen) {
				continue
			}
			assertRowCopyable(t, "overflow", &mm, row)
		}
	})
}
