package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ⑨（2026-10-16 用户报「[Image #N] 不是一个整体，所以导致删除的时候删得很麻烦」）：
//
// 占位符此前是**纯文本**：退格只能逐字符啃（删 10 下），而且删剩 "[Image #"
// 就已经被判为「放弃这张图」——残渣还留在输入框里。本批让它在**删除语义**上
// 成为一个原子单位：贴着/落在占位符上的 Backspace / Delete / 删词键一次删净整个
// [Image #N]（连同尾随分隔空格），并在渲染上成块显示（imageTokenStyle）。
//
// 对标（本地快照可核实）：
// - 同类 CLI `同类实现 buffer 内的**元素**，
// `delete_backward` 走 `prev_atomic_boundary`（一次退格删整个元素），
// 并有测试 `delete_forward_deletes_element_at_left_edge` / `delete_forward_word_handles_atomic_elements`。
// - 同类 CLI `PromptInput.tsx`：占位符是**原子 chip**，backspace 整体删除。
//
// 本批不复刻它们的结构化 composer，而是复用 whale 既有的输入框选区层
// （composer_selection.go：composerCursorPos 能读光标、deleteComposerRange 能删区间），
// 在既有 Backspace/Delete 拦截点上加「原子边界」判定。

// w104Apply 走生产入口 Update，并按 R1b-2 做指针复位
// （与 composerUpdateKey 同法：Update 返回的是 Model 拷贝，需 reassignServices
// 让 m.textarea 与 m.uiService.Textarea() 指向同一内存）。
func w104Apply(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return modelFromTea(updated)
}

// w104PasteImages 在「输入框焦点就绪」的夹具上走生产入口连贴 n 张图。
// 返回的 Model 文本形如 "[Image #1] [Image #2] "，光标在末尾，队列 n 张。
func w104PasteImages(t *testing.T, n int) *Model {
	t.Helper()
	m := newComposerSelectionModel(t)
	mimes := []string{"image/png", "image/jpeg", "image/webp"}
	for i := 0; i < n; i++ {
		m = w104Apply(t, m, clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: mimes[i%len(mimes)]}})
	}
	if got := len(m.uiService.PendingImages()); got != n {
		t.Fatalf("前置：应贴入 %d 张图，actual %d", n, got)
	}
	return m
}

// 主用例：光标停在占位符尾部空格之后，一次退格应整块删掉 [Image #2]（含尾随空格）。
func TestW104_AtomicBackspaceAtPlaceholderTailDeletesWholeToken(t *testing.T) {
	m := w104PasteImages(t, 2)

	row, col := composerCursorPos(m.uiService.Textarea())
	t.Logf("前置：row=%d col=%d value=%q", row, col, m.textarea.Value())
	if want := len([]rune(m.textarea.Value())); col != want {
		t.Fatalf("前置：贴图后光标应落在末尾，col=%d want %d", col, want)
	}

	m = w104Apply(t, m, tea.KeyMsg{Type: tea.KeyBackspace})

	if got, want := m.textarea.Value(), "[Image #1] "; got != want {
		t.Fatalf("⑨ 一次退格应整块删掉 [Image #2]（不留残渣），actual %q want %q", got, want)
	}
	imgs := m.uiService.PendingImages()
	if len(imgs) != 1 || imgs[0].MimeType != "image/png" {
		t.Fatalf("⑨ 删掉 [Image #2] ⇒ 队列应只剩第 1 张(png)，actual %d 张 %+v", len(imgs), imgs)
	}
}

// 光标落在占位符**内部**（"[Ima|ge #1]"）时，一次退格也应整块删净，而不是啃成 "[Ima"。
func TestW104_AtomicBackspaceInsidePlaceholderDeletesWholeToken(t *testing.T) {
	m := w104PasteImages(t, 1)

	m.textarea.SetValue("[Image #1] 看")
	m.textarea.SetCursor(4) // 光标落在 "[Ima|ge #1]" 内部

	m = w104Apply(t, m, tea.KeyMsg{Type: tea.KeyBackspace})

	if got, want := m.textarea.Value(), "看"; got != want {
		t.Fatalf("⑨ 光标在占位符内部时退格应整块删净，actual %q want %q", got, want)
	}
	if n := len(m.uiService.PendingImages()); n != 0 {
		t.Fatalf("⑨ 占位符删净 ⇒ 该图应出队，actual %d 张", n)
	}
}

// Delete（向右删）：光标贴在占位符左端时同样整块删净。
// ★ 口径统一：原子单元 = 本体 + 尾随分隔空格 ⇒ 向右删也把
// 分隔空格一起吞掉（原期望 " 看" 会残留孤立空格）。有意翻转，与导航/向左删同源。
func TestW104_AtomicDeleteBeforePlaceholderDeletesWholeToken(t *testing.T) {
	m := w104PasteImages(t, 1)

	m.textarea.SetValue("[Image #1] 看")
	m.textarea.SetCursor(0)

	m = w104Apply(t, m, tea.KeyMsg{Type: tea.KeyDelete})

	if got, want := m.textarea.Value(), "看"; got != want {
		t.Fatalf("⑨ 向左端的 Delete 应整块删净占位符（含分隔空格，），actual %q want %q", got, want)
	}
	if n := len(m.uiService.PendingImages()); n != 0 {
		t.Fatalf("⑨ 占位符删净 ⇒ 该图应出队，actual %d 张", n)
	}
}

// 删词键（ctrl+w / alt+backspace）：光标贴着占位符右端时，若按词边界删会只剩 "[Image #]"，
// 因此同样按原子边界整块删净。
func TestW104_AtomicWordDeleteOnPlaceholderDeletesWholeToken(t *testing.T) {
	m := w104PasteImages(t, 1)

	m.textarea.SetValue("[Image #1] abc")
	m.textarea.SetCursor(10) // 贴着 "]" 右侧

	m = w104Apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlW})

	if got, want := m.textarea.Value(), "abc"; got != want {
		t.Fatalf("⑨ ctrl+w 贴着占位符时应整块删净（不得按词啃成残渣），actual %q want %q", got, want)
	}
	if n := len(m.uiService.PendingImages()); n != 0 {
		t.Fatalf("⑨ 占位符删净 ⇒ 该图应出队，actual %d 张", n)
	}
}

// 反向半边（不得越界）：没有占位符的普通文本，退格仍是删一个字符。
func TestW104_AtomicBackspaceOnPlainTextStillDeletesOneChar(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("abc")

	m = w104Apply(t, m, tea.KeyMsg{Type: tea.KeyBackspace})

	if got, want := m.textarea.Value(), "ab"; got != want {
		t.Fatalf("⑨ 不得改变普通文本的单字符删除语义，actual %q want %q", got, want)
	}
}

// 反向半边（不得越界）：光标远离占位符时，退格仍只删一个字符，占位符与队列原样。
func TestW104_AtomicBackspaceAwayFromPlaceholderStillDeletesOneChar(t *testing.T) {
	m := w104PasteImages(t, 2)

	m.textarea.SetValue("[Image #1] [Image #2] abc") // 光标落在末尾
	m = w104Apply(t, m, tea.KeyMsg{Type: tea.KeyBackspace})

	if got, want := m.textarea.Value(), "[Image #1] [Image #2] ab"; got != want {
		t.Fatalf("⑨ 远离占位符的退格不得吞掉整块，actual %q want %q", got, want)
	}
	if n := len(m.uiService.PendingImages()); n != 2 {
		t.Fatalf("⑨ 引用完整时不得丢图，actual %d 张", n)
	}
}

// 渲染：占位符在输入框里以成块样式呈现（"一眼看出是一个整体"）。
//
// ★ 必须先 forceANSIColors：无 TTY 时 lipgloss 走 Ascii profile，样式渲成裸文本
// ——那样 `strings.Contains(view, imageTokenStyle.Render(tok))` 会退化成"含这串字"，
// 恒真（把成块渲染整个关掉也照样绿）。本判据的判别性靠这一行撑着。
func TestW104_PlaceholderRenderedAsAtomicToken(t *testing.T) {
	forceANSIColors(t)
	m := w104PasteImages(t, 2)

	view := m.renderComposerDecorations(m.uiService.Textarea().View())
	for _, tok := range []string{"[Image #1]", "[Image #2]"} {
		if !strings.Contains(view, imageTokenStyle.Render(tok)) {
			t.Fatalf("占位符 %s 应以成块样式渲染，got %q", tok, view)
		}
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "[Image #1] [Image #2]") {
		t.Fatalf("成块渲染不得破坏纯文本内容，got %q", plain)
	}
}
