package onboarding

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/anchorandfish/whale-code/internal/util"
	versionpkg "github.com/anchorandfish/whale-code/internal/version"
)

// welcomeBoxWidth 差异化首屏 box 内容区显示宽度（列）。
// 总行宽 = 1(║) + welcomeBoxWidth + 1(║) = 66 列。
const welcomeBoxWidth = 64

// renderHeader 渲染 Onboarding 欢迎头部（版本 + 平台信息）。
// 版本号来自 internal/version.Version，与 --version / doctor 同源。
func renderHeader() string {
	return fmt.Sprintf("\nwhale-code Onboarding v%s (%s, %s/%s)\n\n",
		versionpkg.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// renderStepProgress 渲染步骤进度提示（步骤 x/N：name）。
// 批 5：双语形态（英文主行 + 缩进中文辅行），形态 SSOT = util.Bi。
// 调用方以 Println 收尾（util.Bi 会 TrimRight 尾部 \n）。
// 对标同类 CLI Onboarding.tsx 的 currentStepIndex 进度展示。
func renderStepProgress(stepNum int, nameEN, nameZH string) string {
	return util.Bi(
		fmt.Sprintf("━━━ Step %d/%d: %s ━━━", stepNum, stepCount, nameEN),
		fmt.Sprintf("━━━ 步骤 %d/%d：%s ━━━", stepNum, stepCount, nameZH),
	)
}

// displayWidth 估算字符串显示宽度（列）。
// 简化规则（覆盖首屏用到的字符集）：
// - ASCII（< 0x80）：1 列
// - Box Drawing 块（U+2500–U+257F，含 ╔═╗║╠╣╚╝）：1 列
// - 中点 ·（U+00B7，East Asian Width=Ambiguous）：1 列
// - 其余非 ASCII（CJK、emoji、全角标点）：2 列
//
// 仅用于首屏 box 对齐填充，不引入外部依赖（cellbuf 在 tui 包使用，onboarding 包保持独立）。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r < 0x80:
			w++ // ASCII
		case r >= 0x2500 && r <= 0x257F:
			w++ // Box Drawing block（╔ ═ ╗ ║ ╠ ╣ ╚ ╝ 等）
		case r == 0x00B7:
			w++ // 中点 ·（Ambiguous，按 1 列计）
		default:
			w += 2 // CJK + emoji + 全角标点
		}
	}
	return w
}

// padToWidth 将文本用空格右填充至指定显示宽度（左对齐）。
// 文本宽度 >= 目标宽度时原样返回（避免截断破坏语义）。
// 用于首屏 box 内容对齐，保证每行 ║ 右对齐。
func padToWidth(text string, width int) string {
	w := displayWidth(text)
	if w >= width {
		return text
	}
	return text + strings.Repeat(" ", width-w)
}

// buildWelcomeLine 构造首屏 box 一行：║ + 内容（填充至 welcomeBoxWidth）+ ║。
// 内容为空时填充纯空格（用作分隔行）。
func buildWelcomeLine(content string) string {
	return "║" + padToWidth(content, welcomeBoxWidth) + "║"
}

// appendBiBoxLine 把一行双语 copy（util.Bi 形态：英文主行 + 缩进中文辅行）
// 按行拆开，逐行包进 box 行后追加到 lines。
// box 的每行是独立的单行容器（║…║ 64 列对齐），双语必须在 box 行结构内展开。
func appendBiBoxLine(lines []string, en, zh string) []string {
	for _, ln := range strings.Split(util.Bi(en, zh), "\n") {
		lines = append(lines, buildWelcomeLine(ln))
	}
	return lines
}

// renderDifferentiatedWelcome 渲染差异化首屏文案（D1-1 最小版）。
// 突出 whale-code 三大卖点：本地优先 / H12 自我进化 / Windows 原生沙箱。
// 返回 UTF-8 字符串（box drawing 字符，内容区显示宽度 64 列）。
//
// 实现约束：
// - 纯字符串拼接，无 I/O 副作用
// - panic 安全：defer recover 包裹，panic 时返回空串（fail-open，调用方跳过首屏）
// - 不依赖 Onboarding 字段（可在零值/nil receiver 上安全调用）
//
// 对标同类 CLI Onboarding.tsx 的 hero 标题与卖点陈列，
// whale-code 用纯文本 box drawing（非 Ink 组件），突出个人 CLI 的差异化定位。
func (o *Onboarding) renderDifferentiatedWelcome() (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = "" // panic 时返回空串，调用方跳过首屏直接进步骤 1（fail-open）
		}
	}()

	// 顶部边框
	top := "╔" + strings.Repeat("═", welcomeBoxWidth) + "╗"
	// 分隔线（section 之间）
	sep := "╠" + strings.Repeat("═", welcomeBoxWidth) + "╣"
	// 底部边框
	bottom := "╚" + strings.Repeat("═", welcomeBoxWidth) + "╝"

	lines := []string{top}
	// 标题区（批 5：双语，中文副题降为缩进辅行）
	lines = append(lines, buildWelcomeLine("  🐋 Welcome to Whale Code"))
	lines = appendBiBoxLine(lines,
		"  A learning, trustworthy, Windows-native coding companion",
		"  会学习、可信任、Windows 原生的编码伙伴")
	lines = append(lines, sep)
	// 卖点 1：本地优先
	lines = appendBiBoxLine(lines,
		"  🔒 Local-first: code and chats never leave your machine",
		"  🔒 本地优先：你的代码与对话永不离开本机")
	lines = appendBiBoxLine(lines,
		"     · sessions/memory/config stay 100% local",
		"     · 会话/memory/config 100% 本地存储")
	lines = appendBiBoxLine(lines,
		"     · API key never uploaded, no middle server",
		"     · API key 不上传，无中间服务器")
	lines = append(lines, buildWelcomeLine(""))
	// 卖点 2：H12 自我进化
	lines = appendBiBoxLine(lines,
		"  🧠 H12 self-evolution: whale remembers your coding habits",
		"  🧠 H12 自我进化：whale 会记住你的编码习惯")
	lines = appendBiBoxLine(lines,
		"     · learns patterns after every task (Voyager-style loop)",
		"     · 每次任务后自动学习 Pattern（行业独有，Voyager 式闭环）")
	lines = appendBiBoxLine(lines,
		"     · the longer you use it, the better it fits your project",
		"     · 用得越久，whale 越懂你的项目")
	lines = append(lines, buildWelcomeLine(""))
	// 卖点 3：Windows 原生沙箱
	lines = appendBiBoxLine(lines,
		"  🪟 Windows-native: OS-level sandbox (Job Object)",
		"  🪟 Windows 原生：CREATE_SUSPENDED+Job Object OS 级沙箱")
	lines = appendBiBoxLine(lines,
		"     · deny-wins rule + plugin verification fail-closed",
		"     · deny-wins 铁律 + 插件验签 fail-closed")
	lines = append(lines, buildWelcomeLine(""))
	// 引导提示（中英同构；中文措辞保持原样）
	lines = appendBiBoxLine(lines,
		"  Next: 4-step guided setup (key / provider / env / config)",
		"  接下来：4 步引导配置（API key / Provider / 环境 / 配置）")
	lines = append(lines, bottom)

	out = strings.Join(lines, "\n")
	return out
}
