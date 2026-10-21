package main

import (
	"runtime"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// · alternate_screen 开关的守卫。
//
// 背景：`main.go:499` 原先**无条件** `tea.WithAltScreen()`，且本地与 SSH 两个构造点
// 各自调用同一函数 ⇒ "历史归谁"被隐式钉死为"应用自绘 viewport"，用户无法选择
// （终端原生 scrollback 里一行 whale 内容都没有；退出后整屏消失）。
//
// 对齐同类 CLI（`--no-alt-screen` + always/never/auto）：把它变成一个**显式选择**。
// 本文件钉三件事：
// ① 判定是**单点**（resolveAltScreen），且 never 真的关、always 真的开；
// ② 默认值**不翻转**（auto = 现状 = 开）—— 防"顺手改默认"造成无意识的产品变更；
// ③ 选项构造真的**受参数控制**（不是接了参数却忽略它）。
//
// 判别性自检：把 `newTUIProgramOptions` 里的 `if altScreen` 去掉（恢复无条件 WithAltScreen），
// ②③ 必须转红。

func TestResolveAltScreen_Judgement(t *testing.T) {
	cases := []struct {
		mode altScreenMode
		want bool
	}{
		{altScreenNever, false},
		{altScreenAlways, true},
		{altScreenAuto, true},       // 默认不翻转 = 现状
		{altScreenMode(""), true},   // 未知取值保守回落到"现状"，不得意外关掉备用屏
		{altScreenMode("xx"), true}, // 同上
	}
	for _, tc := range cases {
		if got := resolveAltScreen(tc.mode); got != tc.want {
			t.Errorf("N-138③: resolveAltScreen(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestParseTerminalFlags(t *testing.T) {
	defCapture, _ := defaultMouseMode()

	t.Run("默认：保持现状（备用屏开、鼠标捕获开）", func(t *testing.T) {
		f := parseTerminalFlags([]string{})
		if !f.altScreen {
			t.Error("N-138③: 默认必须保持备用屏开启（零体感变化），不得翻转默认")
		}
		if f.mouseCapture != defCapture {
			t.Errorf("N-138③: 默认鼠标捕获应为 %v，got %v", defCapture, f.mouseCapture)
		}
		if f.explicit {
			t.Error("N-138③: 无开关时 explicit 应为 false（否则会跳过 handleArgs）")
		}
	})

	t.Run("--no-alt-screen 关备用屏且标记显式", func(t *testing.T) {
		f := parseTerminalFlags([]string{"--no-alt-screen"})
		if f.altScreen {
			t.Error("N-138③: --no-alt-screen 必须关掉备用屏")
		}
		if !f.explicit {
			t.Error("N-138③: --no-alt-screen 是显式 TUI 选项，explicit 必须为 true")
		}
	})

	t.Run("--alt-screen 显式开启（可覆盖）", func(t *testing.T) {
		f := parseTerminalFlags([]string{"--no-alt-screen", "--alt-screen"})
		if !f.altScreen {
			t.Error("N-138③: 后出现的 --alt-screen 应覆盖前一个")
		}
	})

	t.Run("鼠标开关仍走同一实现", func(t *testing.T) {
		if f := parseTerminalFlags([]string{"--no-mouse"}); f.mouseCapture {
			t.Error("N-138③: --no-mouse 必须关掉鼠标捕获")
		}
		if f := parseTerminalFlags([]string{"--no-mouse", "--mouse"}); !f.mouseCapture {
			t.Error("N-138③: --mouse 必须重新打开鼠标捕获")
		}
	})
}

func TestIsTerminalFlag_CoversWholeFamily(t *testing.T) {
	for _, f := range terminalFlagNames {
		if !isTerminalFlag(f) {
			t.Errorf("N-138③: %q 应在终端能力开关族内", f)
		}
	}
	// 位置参数与其它开关不得被误判（否则会被当成"已消费"而丢掉）
	for _, f := range []string{"user@host:22", "--permission", "--mouse=true", "--no-alt-screens"} {
		if isTerminalFlag(f) {
			t.Errorf("N-138③: %q 不应被判定为终端能力开关", f)
		}
	}
}

// TestTUIProgramOptions_AltScreenIsGatedByArgument ③：选项构造真的受参数控制。
//
// 用反射读 bubbletea 私有 startupOptions 位集（仓库既有手法，见 startupOptionsValue）：
// `altScreen=false` 时必须恰好等于"基础选项（无备用屏）+ 鼠标捕获"。
func TestTUIProgramOptions_AltScreenIsGatedByArgument(t *testing.T) {
	withAlt := startupOptionsValue(newTUIProgramOptions(true, true)...)
	withoutAlt := startupOptionsValue(newTUIProgramOptions(true, false)...)
	if withAlt == withoutAlt {
		t.Fatal("N-138③: altScreen 参数没有生效（两次 startup options 相同）—— 备用屏仍是硬编码")
	}

	base := []tea.ProgramOption{tea.WithFPS(120), tea.WithoutSignalHandler()}
	// ★ 有意翻转： ② 在生产装配里新增 tea.WithReportFocus()（CSI 1004
	// 焦点事件 → 贴图提示），本守卫的「恰好等于」清单随之补上同一项 —— 不是放宽门，
	// 是期望清单跟上生产装配。
	base = append(base, tea.WithReportFocus())
	if runtime.GOOS != "windows" {
		base = append(base, tea.WithInputTTY())
	}
	want := startupOptionsValue(append(base, tea.WithMouseCellMotion())...)
	if withoutAlt != want {
		t.Fatalf("N-138③: altScreen=false 时选项应恰好 = 基础选项 + 鼠标捕获，got %d want %d", withoutAlt, want)
	}
}
