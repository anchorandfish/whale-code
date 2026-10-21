package diff

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// sampleChange 覆盖 header / gutter 行号 / 上下文行 / - 行 / + 行 / 词级高亮的样本。
func sampleChange() FileChange {
	return FileChange{
		Path:       "handler.go",
		OldContent: "package main\n\nfunc handle() int {\n\treturn 1\n}\n",
		NewContent: "package main\n\nfunc handle() int {\n\treturn 2\n}\n",
	}
}

// TestCacheKeyDistinguishesStyleConfig 钉住：缓存键必须区分「只影响样式、不影响内容」的配置。
//
// 为什么单独钉这条：cacheKey 是顺序写入的 fnv，漏写任何影响输出的字段 ⇒
// 两种渲染结果互相命中对方的缓存条目。这种 bug **只在缓存命中时才复现** ——
// 第一次渲染是对的，第二次是错的，最难查的一类。
func TestCacheKeyDistinguishesStyleConfig(t *testing.T) {
	c := FileChange{Path: "a.go", OldContent: "a\n", NewContent: "b\n"}
	base := DefaultConfig()

	plain := base
	plain.Plain = true

	colorBlind := base
	colorBlind.ColorBlind = true

	if cacheKey(c, base) == cacheKey(c, plain) {
		t.Error("cacheKey 未区分 Plain：纯文本渲染会被彩色缓存命中")
	}
	if cacheKey(c, base) == cacheKey(c, colorBlind) {
		t.Error("cacheKey 未区分 ColorBlind：色盲主题会被普通主题缓存命中")
	}
}

// TestPlainStyleSetIsIdentity 钉住纯文本保证的**结构基础**：
// 空样式 Render 后逐字等于输入。
//
// 这条与终端是否支持颜色无关 —— 无论 lipgloss 的 ColorProfile 是什么，
// 无属性的样式都不改写文本。上游 lipgloss 一旦改变该行为，本测试先红，
// 而不是等用户在剪贴板里发现乱码。
func TestPlainStyleSetIsIdentity(t *testing.T) {
	st := stylesFor(PlainConfig())
	all := map[string]lipgloss.Style{
		"header":  st.header,
		"added":   st.added,
		"removed": st.removed,
		"context": st.context,
		"wordAdd": st.wordAdd,
		"wordDel": st.wordDel,
		"statAdd": st.statAdd,
		"statDel": st.statDel,
	}
	// 含换行、含中文、含空串：空样式最容易在"多行补位"上偷偷加料。
	samples := []string{
		"--- a/handler.go\n",
		"+++ b/handler.go\n",
		"1 1 │ package main",
		"+新增行",
		"-删除行",
		"",
	}
	for _, s := range samples {
		for name, style := range all {
			if got := style.Render(s); got != s {
				t.Errorf("纯文本样式 %s 不是恒等的：Render(%q) = %q", name, s, got)
			}
		}
	}
}

// 判别力前提（如实登记，不假装覆盖）：
//
//	go test 是非 TTY，lipgloss 会**自动去色** ⇒ 彩色路径也不含 ANSI ⇒
//	本文件的断言会退化成恒绿的假门。
//	要拿到完整判别力，用 CLICOLOR_FORCE=1 TERM=xterm-256color go test 跑。
//
// 即便在去色环境下，TestPlainStyleSetIsIdentity 仍提供**结构化**保证：
// 纯文本用的是全空样式，无论 profile 是什么都不改写文本。
//
// TestRenderPlainEmitsNoANSI 钉住：纯文本渲染不产出任何 ANSI 转义序列。
func TestRenderPlainEmitsNoANSI(t *testing.T) {
	out := Render(sampleChange(), PlainConfig())
	if out == "" {
		t.Fatal("纯文本渲染返回空串，样本未触发渲染 —— 判别器无效")
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("纯文本渲染仍含 ANSI：%q", out)
	}
}

// TestRenderPlainMatchesStrippedColor 钉住不变式：
//
//	纯文本输出 == 彩色输出剥离 ANSI 之后的结果
//
// 防的是「同一份格式两处实现」的老坑：一旦有人在彩色路径改了 gutter 几何 /
// 行号推进 / 词级配对而没同步（或某个硬编码样式复活），两条路径就会漂移，本测试红。
func TestRenderPlainMatchesStrippedColor(t *testing.T) {
	plain := Render(sampleChange(), PlainConfig())
	colorOut := Render(sampleChange(), DefaultConfig())

	// 判别力自检：go test 是非 TTY，lipgloss 可能自动去色 ⇒ 两条路径都没 ANSI，
	// 断言就变成恒绿的假门。这里显式确认彩色路径真的带了色，否则如实记录。
	if strings.Contains(colorOut, "\x1b") {
		if ansi.Strip(colorOut) == colorOut {
			t.Fatal("判别器失效：ansi.Strip 没能剥掉任何东西")
		}
	} else {
		t.Logf("注意：当前环境 lipgloss 未上色（非 TTY 自动去色），本用例的对比判别力受限；纯文本断言仍成立")
	}

	if stripped := ansi.Strip(colorOut); plain != stripped {
		t.Errorf("纯文本与彩色剥离后不一致 —— 两条路径漂移了：\nplain = %q\ncolor = %q", plain, stripped)
	}
}
