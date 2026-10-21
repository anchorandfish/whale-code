package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/whalehome"
	"github.com/charmbracelet/x/ansi"
)

// ⑧（2026-10-16 用户拍板「做吧」）：点击输入框里的 [Image #N] 打开原图。
//
// 背景：终端是文字世界，输入框里画不出缩略图 ⇒ 用户贴完图后**看不到**自己贴了什么。
// 同类 CLI 的解法是把占位符渲染成 **OSC 8 超链接**，指向落盘的图片文件，
// 点击即用系统看图程序打开（`ClickableImageRef.tsx:31-34`、`UserImageMessage.tsx:29-31`，
// 走 `Link.tsx` → `supportsHyperlinks()`，落盘路径来自 `getStoredImagePath`）。
// 两款同类 CLI 都**没有**这一手（它们的 OSC 8 只用在状态栏 PR 号上，见
// `bottom_pane/chat_composer.rs:4445-4447`）——所以 ⑧ 是 whale 的差异化形态。
//
// whale 侧的既有条件（研究结论，非假设）：
// - `RenderFileLink` / `escapeFilePath`（render.go:389-412）**已经是** OSC 8 发射器 ⇒ 复用，不写第二份；
// - `ansi.StringWidth` 按解析器状态机只对可打印字符计宽 ⇒ OSC 8（零宽）不破坏宽度计算；
// - bubbletea v1.2.4 渲染器逐行**原样写出**（`standard_renderer.go:205/254`），
// 只在行超宽时 `ansi.Truncate` ⇒ 超链接能活着到达终端。
//
// 本批**唯一**新增的承载体 = 「图片落盘后的路径」（`PendingImage.Path`）：
// 剪贴板里的图此前只以 base64 存在内存里（临时文件 `defer os.Remove`），**磁盘上没有可点开的东西**。

// osc8Prefix 是 OSC 8 超链接的开头。判据只认这个字面，不调用被测侧函数拼它
// （否则"实现错、判据跟着错"会一起漂移）。
const osc8Prefix = "\x1b]8;;"

// w104PasteImageWithPath 走生产入口贴一张**已落盘**的图（带 Path）。
func w104PasteImageWithPath(t *testing.T, m *Model, mime, path string) *Model {
	t.Helper()
	return w104Apply(t, m, clipboardPasteMsg{seq: 0, image: &PendingImage{MimeType: mime, Path: path}})
}

// w104FakeImageFile 造一个真实存在的小文件，返回其路径。
func w104FakeImageFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("PNG-BYTES"), 0o600); err != nil {
		t.Fatalf("前置：造图片文件失败: %v", err)
	}
	return path
}

// 主用例：占位符必须以 OSC 8 超链接指向**那张图落盘的文件**，而显示文本不变。
func TestW104_PlaceholderIsHyperlinkToStoredFile(t *testing.T) {
	forceANSIColors(t)
	imgPath := w104FakeImageFile(t, "shot.png")

	m := newComposerSelectionModel(t)
	m = w104PasteImageWithPath(t, m, "image/png", imgPath)

	view := m.composerDecoratedView()

	// 独立拼出期望 URI（字面量，不经被测实现）。t.TempDir() 路径无特殊字符，转义后与原文一致。
	wantURI := "file://" + imgPath
	if !strings.Contains(view, osc8Prefix+wantURI+"\x1b\\") {
		t.Fatalf("⑧ 占位符应带指向落盘文件的 OSC 8 超链接\n want 片段 %q\n got %q",
			osc8Prefix+wantURI+"\x1b\\", view)
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "[Image #1]") {
		t.Fatalf("⑧ 加超链接不得改变显示文本，strip 后应仍含 [Image #1]，got %q", plain)
	}
	// 成块样式（⑨）与超链接（⑧）必须**共存**：链接应恰好包在成块片段外面。
	// 只断言"含 OSC 8 + 含占位符"会漏掉"链接包错了对象"（例如包到了别处），
	// 所以这里钉住嵌套关系本身。
	if want := osc8Prefix + wantURI + "\x1b\\" + imageTokenStyle.Render("[Image #1]"); !strings.Contains(view, want) {
		t.Fatalf("⑧ 超链接应包住成块占位符（⑨ 的成块不得被链接挤掉）\n want 片段 %q\n got %q", want, view)
	}
}

// 编号 → 队列第 N 项：`[Image #2]` 必须指向**第 2 张**图，不是第 1 张。
func TestW104_PlaceholderNumberMapsToCorrespondingImage(t *testing.T) {
	forceANSIColors(t)
	p1 := w104FakeImageFile(t, "a.png")
	p2 := w104FakeImageFile(t, "b.png")

	m := newComposerSelectionModel(t)
	m = w104PasteImageWithPath(t, m, "image/png", p1)
	m = w104PasteImageWithPath(t, m, "image/jpeg", p2)

	view := m.composerDecoratedView()
	// 必须钉住**配对关系**（哪个 URI 包着哪个占位符），不能只断言"两个 URI 都在" ——
	// 后者在"编号被调换"（#1 指到第 2 张）时照样绿，属不可判别判据。
	for i, p := range []string{p1, p2} {
		tok := imageTokenStyle.Render("[Image #" + strconv.Itoa(i+1) + "]")
		want := osc8Prefix + "file://" + p + "\x1b\\" + tok
		if !strings.Contains(view, want) {
			t.Fatalf("⑧ [Image #%d] 必须链接到第 %d 张图（配对关系），缺 %q\ngot %q",
				i+1, i+1, want, view)
		}
	}
	// 反向半边：第 1 张图不得被链接两次（一刀切把两张都指到同一路径）。
	if n := strings.Count(view, osc8Prefix+"file://"+p1+"\x1b\\"); n != 1 {
		t.Fatalf("⑧ 第 1 张图应恰好被链接 1 次，got %d\nview=%q", n, view)
	}
}

// 反向半边（不得越界）：文本引用的编号越界（队列里没这张图）⇒ 不链接，也不得崩。
// 与 reconcilePendingImages 的「非法/越界编号不认领」同口径。
func TestW104_OutOfRangePlaceholderIsNotLinked(t *testing.T) {
	forceANSIColors(t)
	imgPath := w104FakeImageFile(t, "only.png")

	m := newComposerSelectionModel(t)
	m = w104PasteImageWithPath(t, m, "image/png", imgPath)
	m.textarea.SetValue("[Image #3] 手写的")

	view := m.composerDecoratedView()
	if strings.Contains(view, osc8Prefix) {
		t.Fatalf("⑧ 越界编号不得被链接到任何文件，got %q", view)
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "[Image #3]") {
		t.Fatalf("⑧ 越界占位符应原样保留为普通文本，got %q", plain)
	}
}

// 反向半边（降级）：图片未落盘（Path 为空，例如 HOME 只读/写盘失败）⇒ 退化为普通成块占位符。
// 与「终端不支持超链接」是同一条降级路径（对齐同类 CLI 的 fallback 语义）。
func TestW104_PlaceholderWithoutStoredFileFallsBackToPlainToken(t *testing.T) {
	forceANSIColors(t)

	m := newComposerSelectionModel(t)
	// 走生产入口，但 Path 为空 —— 模拟落盘失败。
	m = w104PasteImageWithPath(t, m, "image/png", "")

	view := m.composerDecoratedView()
	if strings.Contains(view, osc8Prefix) {
		t.Fatalf("⑧ 无落盘文件时不得发超链接，got %q", view)
	}
	if !strings.Contains(view, imageTokenStyle.Render("[Image #1]")) {
		t.Fatalf("⑧ 降级后仍应保留成块样式，got %q", view)
	}
}

// 宽度不变量：超链接**不得改变行的显示宽度**（否则输入框边框会在贴图后抖动）。
//
// ★ 本条是**护栏**而非本功能的判别器：把超链接整个去掉它照样绿（两态皆绿）。
//
//	它能红的唯一形态是「有人换成了会占列宽的转义序列」——那正是它要防的那件事，
//	所以留着；但别把它当"⑧ 生效了"的证据读（判别器见上面三条）。
func TestW104_ImageHyperlinkPreservesDisplayWidth(t *testing.T) {
	forceANSIColors(t)
	imgPath := w104FakeImageFile(t, "w.png")

	m := newComposerSelectionModel(t)
	m = w104PasteImageWithPath(t, m, "image/png", imgPath)

	view := m.composerDecoratedView()
	for _, line := range strings.Split(view, "\n") {
		got, want := ansi.StringWidth(line), ansi.StringWidth(ansi.Strip(line))
		if got != want {
			t.Fatalf("⑧ 超链接改变了行宽（会致输入框抖动）：got %d want %d，line=%q", got, want, line)
		}
	}
}

// 落盘：内容寻址（同内容 ⇒ 同路径、幂等），未知 MIME 不落盘。
func TestW104_StorePastedImageIsContentAddressed(t *testing.T) {
	dir, err := os.MkdirTemp("", "w104img")
	if err != nil {
		t.Fatalf("前置：建临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // 忽略竞态错误（勿用 t.TempDir：可能与后台协程抢清理）
	t.Setenv(whalehome.EnvVar, dir)

	data := []byte("\x89PNG-W104-FAKE")
	p1 := storePastedImage(data, "image/png")
	if p1 == "" {
		t.Fatal("⑧ 落盘应成功")
	}
	if want := filepath.Join(dir, "images"); !strings.HasPrefix(p1, want+string(filepath.Separator)) {
		t.Fatalf("⑧ 应落在 whale 状态目录的 images/ 下，got %q want 前缀 %q", p1, want)
	}
	if !strings.HasSuffix(p1, ".png") {
		t.Fatalf("⑧ 扩展名应随 MIME 走，got %q", p1)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf("⑧ 落盘文件应真实存在: %v", err)
	}
	if got := storePastedImage(data, "image/png"); got != p1 {
		t.Fatalf("⑧ 同内容应幂等同路径，first=%q second=%q", p1, got)
	}
	if got := storePastedImage([]byte("different"), "image/png"); got == p1 || got == "" {
		t.Fatalf("⑧ 不同内容应得到不同路径，got %q（p1=%q）", got, p1)
	}
	if got := storePastedImage(data, "image/svg+xml"); got != "" {
		t.Fatalf("⑧ 未知 MIME 不应落盘（无可打开的图片文件），got %q", got)
	}
}

// 既有缺陷修正（在 ⑧ 过程中被上面两条判据当场抓出）：绝对路径不得产出 `file:////…`（四个斜杠）。
//
// 为什么与 ⑧ 同批修：⑧ 的**全部**价值就是"点一下能打开"；URI 多一个斜杠时
// 解析出的 path 是 `//var/…`（实现相关），能不能打开全看系统容忍度 ⇒ 等于把 ⑧ 赌在运气上。
func TestW104_FileLinkURIUsesCanonicalLocalForm(t *testing.T) {
	cases := []struct{ path, want string }{
		// 规范形态：RFC 8089 本地文件 URL 的 host 为空 ⇒ file:///path（恰 3 个斜杠）
		{"/var/folders/a/b.png", "file:///var/folders/a/b.png"},
		// 相对路径保持原样（不引入 host 语义）
		{"internal/tui/render.go", "file:///internal/tui/render.go"},
	}
	for _, c := range cases {
		if got := FileLinkURI(FileLink{Path: c.path}); got != c.want {
			t.Fatalf("file:// URI 应为 RFC 8089 规范形态：path=%q\n got  %q\n want %q", c.path, got, c.want)
		}
	}
	if got := FileLinkURI(FileLink{Path: "/a/b.go", Line: 42}); got != "file:///a/b.go#L42" {
		t.Fatalf("行锚点不得因斜杠收敛而丢失：got %q", got)
	}
}

// 同源点护栏：输入框的**全部**渲染出口必须走同一个形态（composerDecoratedView）。
//
// ★ 本仓实测过同类分裂（CLI 子命令"双实现"、命令文案两侧各写各的）——
//
//	输入框有两个出口（render_extras.go 的正常态、以及选择面板的补充输入子态），
//	任一出口绕开装饰层，占位符就会在那个出口退化成纯文本（⑧⑨ 各半修）。
//
// ★ 本条也是**护栏**（读盘型结构门）：它不告诉你 ⑧ 生效了（那是上面行为判据的事），
//
//	只防"新增/回退出口时漏掉装饰层"。判别性靠正向注入反例证明（见回执）。
func TestW104_AllComposerOutletsUseSingleDecoratedForm(t *testing.T) {
	src, err := os.ReadFile("render_extras.go")
	if err != nil {
		t.Fatalf("前置：读不到 render_extras.go: %v", err)
	}
	body := string(src)

	if n := strings.Count(body, "inputBoxStyle.Render(m.uiService.Textarea().View())"); n != 0 {
		t.Fatalf("⑧ 仍有 %d 处出口绕开 composerDecoratedView："+
			"占位符会在该出口退化成纯文本（⑧⑨ 半修）", n)
	}
	if n := strings.Count(body, "inputBoxStyle.Render(m.composerDecoratedView())"); n < 1 {
		t.Fatalf("⑧ 未发现任何走统一渲染形态的输入框出口（应 ≥1 处）")
	}
}
