package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ⑤（2026-10-15 用户拍板「我希望它贴在输入框内，而不是在历史对话框中」）：
// 贴图成功后，占位符 [Image #N] 必须插入输入框，不再往对话流加 "Image pasted ..." 回执。
func TestW104_ImagePastePlaceholderGoesToComposer(t *testing.T) {
	m := newTUIRegressionModel(t)
	before := len(m.sessionService.Messages())

	updated, _ := m.Update(clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: "image/png"}})
	got := updated.(Model)
	got.reassignServices()

	if !strings.Contains(got.textarea.Value(), "[Image #1]") {
		t.Fatalf("输入框应出现 [Image #1] 占位符，actual %q", got.textarea.Value())
	}
	if after := len(got.sessionService.Messages()); after != before {
		t.Fatalf("贴图不应往对话流加回执：消息数 %d → %d", before, after)
	}
	if n := len(got.uiService.PendingImages()); n != 1 {
		t.Fatalf("贴图应进入发送队列 PendingImages，actual %d 张", n)
	}
}

// 多次贴图：占位符编号递增（[Image #1]、[Image #2]），队列同步累积。
func TestW104_SecondImagePasteIncrementsPlaceholder(t *testing.T) {
	m := newTUIRegressionModel(t)

	updated, _ := m.Update(clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: "image/png"}})
	got := updated.(Model)
	got.reassignServices()

	updated2, _ := got.Update(clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: "image/jpeg"}})
	got2 := updated2.(Model)
	got2.reassignServices()

	v := got2.textarea.Value()
	if !strings.Contains(v, "[Image #1]") || !strings.Contains(v, "[Image #2]") {
		t.Fatalf("两次贴图应出现 [Image #1] 与 [Image #2]，actual %q", v)
	}
	if n := len(got2.uiService.PendingImages()); n != 2 {
		t.Fatalf("两次贴图后队列应有 2 张，actual %d", n)
	}
}

// ①（2026-10-15 用户拍板「备用键也行」）：
// ctrl+alt+v 作为贴图备用键，覆盖「终端把 Ctrl+V 截走」的环境（Windows Terminal / VS Code）。
// bubbletea 把 ESC+0x16 字符串化为 "alt+ctrl+v" —— 该契约若变，本测试先红。
func TestW104_AltCtrlVMatchesImagePasteKey(t *testing.T) {
	if got := (tea.KeyMsg{Type: tea.KeyCtrlV}).String(); got != "ctrl+v" {
		t.Fatalf("bubbletea 主键字符串化变化：actual %q", got)
	}
	if got := (tea.KeyMsg{Type: tea.KeyCtrlV, Alt: true}).String(); got != "alt+ctrl+v" {
		t.Fatalf("bubbletea 备用键字符串化变化：actual %q —— isImagePasteKey 需同步", got)
	}
	if !isImagePasteKey(tea.KeyMsg{Type: tea.KeyCtrlV}) {
		t.Fatal("ctrl+v 应触发贴图")
	}
	if !isImagePasteKey(tea.KeyMsg{Type: tea.KeyCtrlV, Alt: true}) {
		t.Fatal("ctrl+alt+v 应触发贴图（备用键）")
	}
	if isImagePasteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")}) {
		t.Fatal("裸 v 不应触发贴图")
	}
}

// 发送链路：[Image #N] 占位符只承载显示，发送时从文本剥离（图片本体经 PendingImages 附发）。
func TestW104_StripImagePlaceholders(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[Image #1] 看看这个", "看看这个"},
		{"看看 [Image #1] 和 [Image #2]", "看看 和"},
		{"[Image #1]", ""},
		{"没有占位符", "没有占位符"},
		{"[Image #12] 多位数", "多位数"},
	}
	for _, c := range cases {
		if got := stripImagePlaceholders(c.in); got != c.want {
			t.Fatalf("stripImagePlaceholders(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// pasteTwoImages 走生产入口连贴两张图，返回文本形如 "[Image #1] [Image #2] "、队列 [png, jpeg] 的 Model。
func pasteTwoImages(t *testing.T) Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	u1, _ := m.Update(clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: "image/png"}})
	m1 := u1.(Model)
	m1.reassignServices()
	u2, _ := m1.Update(clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: "image/jpeg"}})
	m2 := u2.(Model)
	m2.reassignServices()
	if n := len(m2.uiService.PendingImages()); n != 2 {
		t.Fatalf("前置：应有两张待发图，actual %d", n)
	}
	return m2
}

// ⑥⑦（2026-10-16 用户拍板「那你先做6，7」）：
//
//	⑥ 用户删掉 [Image #N] 占位符文字 ⇒ 该图从待发队列剔除
//
// —— 对齐 同类 CLI（PromptInput.tsx:1185 prune 未被引用的图）
// 与 同类 CLI（chat_composer.rs:64 "only placeholders that survive … are sent"）。
//
//	⑦ 存活者按文本出现顺序重新编号 1..K，文本同步改写
//
// —— 对齐 同类 CLI（chat_composer.rs:101-105 编号连续化）。
func TestW104_DeletePlaceholderPrunesImageAndRenumbers(t *testing.T) {
	m := pasteTwoImages(t)

	// 模拟用户在输入框里删掉第一个占位符（字段直访，理由见下条测试的同款注释）。
	m.textarea.SetValue("[Image #2] ")

	// 生产入口触发同步：任意一次按键编辑（routeKeyToComposerTextarea）。
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("看")})
	got := u.(Model)
	got.reassignServices()

	imgs := got.uiService.PendingImages()
	if len(imgs) != 1 {
		t.Fatalf("⑥ 删掉 [Image #1] 后队列应只剩 1 张，actual %d", len(imgs))
	}
	if imgs[0].MimeType != "image/jpeg" {
		t.Fatalf("⑥ 保留的应是原第 2 张图（jpeg），actual %q", imgs[0].MimeType)
	}
	v := got.uiService.Textarea().Value()
	if strings.Contains(v, "[Image #2]") {
		t.Fatalf("⑦ 存活占位符应重排为 [Image #1]，actual %q", v)
	}
	if !strings.Contains(v, "[Image #1]") {
		t.Fatalf("⑦ 应出现重排后的 [Image #1]，actual %q", v)
	}
}

// ⑥ 边界：占位符全部删掉 ⇒ 队列清空（图不再被任何引用）。
func TestW104_DeleteAllPlaceholdersClearsQueue(t *testing.T) {
	m := pasteTwoImages(t)

	// ★ 用字段直访 m.textarea，不用 m.uiService.Textarea()：pasteTwoImages 的返回值是拷贝，
	// 其 uiService 仍指向函数内的旧副本（R1b-2 指针别名），经它写的文本到不了本次 Update。
	m.textarea.SetValue("重新来过")

	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	got := u.(Model)
	got.reassignServices()

	if n := len(got.uiService.PendingImages()); n != 0 {
		t.Fatalf("⑥ 占位符全删后队列应清空，actual %d 张", n)
	}
}

// ⑦ 反向半边：编号连续、引用完整时**不得**丢图，也不得把编号改乱。
func TestW104_ContiguousPlaceholdersKeepImagesAndNumbering(t *testing.T) {
	m := pasteTwoImages(t)

	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	got := u.(Model)
	got.reassignServices()

	v := got.uiService.Textarea().Value()
	if !strings.Contains(v, "[Image #1]") || !strings.Contains(v, "[Image #2]") {
		t.Fatalf("引用完整时编号应原样保留，actual %q", v)
	}
	if n := len(got.uiService.PendingImages()); n != 2 {
		t.Fatalf("引用完整时不应丢图，actual %d 张", n)
	}
}
