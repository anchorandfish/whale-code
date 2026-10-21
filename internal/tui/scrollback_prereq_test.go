package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// 守卫：`scrollback.go` 接线前的三处**前置缺口**（清障，非接线）。
//
// 背景（看板 `IT1-014` 的"假完工"）：本件被记为"进行中、仅剩跨平台收尾"，
// 但实查「非测试代码引用数 = 0」—— 主循环从没调用过它。也就是说这些缺口
// **此刻不可见**；一旦 STEP 3 接线，它们会立刻变成用户可见缺陷。
// 所以判据必须**先于接线**存在，否则接线即引入缺陷。
//
// 三条缺口 ⇄ 三条用例族：
// ① DetectScrollbackSupport 恒 true（不支持 DECSTBM/RI 的终端显示错乱，降级永不触发）
// ② wrapLine 按 rune 数切片（CJK 一行画 2×width 宽，必然折行错位）
// ③ WriteLines 的职责边界含未验证的 DECSTBM 承诺（与渲染器争夺光标）
// ③ 属**删除未验证承诺**，没有"行为判据"可写 ⇒ 见文件内注释与回执，不在本文件断言。
//
// 判别性自检（before 必须红）：把 `scrollbackSupportFor` 退回 `return true`、
// 把 `wrapLine` 退回 rune 计数（见回执 §A/B），本文件族①族②必须转红。

// ───────────────────────── 族① 探测判定 ─────────────────────────

func TestN138_ScrollbackSupport_JudgementTable(t *testing.T) {
	cases := []struct {
		name        string
		override    string
		stdoutIsTTY bool
		term        string
		wtSession   string
		goos        string
		want        bool
	}{
		{"显式开：即使哑终端也开", "1", false, "dumb", "", "windows", true},
		{"显式关：即使 iTerm 也关", "0", true, "xterm-256color", "", "darwin", false},
		{"非 TTY（管道/CI）→ 关", "", false, "xterm-256color", "", "darwin", false},
		{"TERM 为空 → 关", "", true, "", "", "darwin", false},
		{"TERM=dumb → 关", "", true, "dumb", "", "linux", false},
		{"macOS + xterm-256color → 开", "", true, "xterm-256color", "", "darwin", true},
		{"Linux + xterm-256color → 开", "", true, "xterm-256color", "", "linux", true},
		{"Linux 虚拟控制台 → 保守关", "", true, "linux", "", "linux", false},
		{"Windows legacy conhost（无 WT_SESSION）→ 关", "", true, "xterm-256color", "", "windows", false},
		{"Windows Terminal（有 WT_SESSION）→ 开", "", true, "xterm-256color", "abc-123", "windows", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrollbackSupportFor(tc.override, tc.stdoutIsTTY, tc.term, tc.wtSession, tc.goos)
			if got != tc.want {
				t.Errorf("N-138①: scrollbackSupportFor(%q,%v,%q,%q,%q) = %v, want %v",
					tc.override, tc.stdoutIsTTY, tc.term, tc.wtSession, tc.goos, got, tc.want)
			}
		})
	}
}

// TestN138_DetectScrollbackSupport_UsesInjectedInputs 证明探测**真的读了输入**
// （而不是像旧实现那样恒 true）：注入两组"世界"，结果必须相反。
//
// ★ 为什么要注入：`os.Stdout` 的 TTY 形态与进程环境在单测里天然与生产不同
// （go test 的 stdout 常是管道、环境无 WT_SESSION）⇒ 直接读它们会得到"测试恒关"，
// 那是**测试的假象**，不是生产取值（production-path-tdd R6b）。
func TestN138_DetectScrollbackSupport_UsesInjectedInputs(t *testing.T) {
	origTTY, origEnv := scrollbackStdoutIsTTY, scrollbackGetenv
	defer func() { scrollbackStdoutIsTTY, scrollbackGetenv = origTTY, origEnv }()

	// 世界A：真终端 + xterm-256color ⇒ 开
	scrollbackStdoutIsTTY = func() bool { return true }
	scrollbackGetenv = func(k string) string {
		switch k {
		case "TERM":
			return "xterm-256color"
		case scrollbackOverrideEnv:
			return ""
		}
		return ""
	}
	if !DetectScrollbackSupport() {
		t.Error("N-138①: 生产形态（TTY + xterm-256color）应判定为支持 —— 注入的世界没被读到？")
	}

	// 世界B：同一进程，只把输出端改成非 TTY ⇒ 必须翻转为关
	scrollbackStdoutIsTTY = func() bool { return false }
	if DetectScrollbackSupport() {
		t.Error("N-138①: 非 TTY（管道）必须判定为不支持，否则会把控制序列写进日志/产物")
	}
}

// ───────────────────────── 族② 显示宽度换行 ─────────────────────────

func TestN138_WrapLine_WrapsByDisplayWidthNotRuneCount(t *testing.T) {
	var w strings.Builder
	sw := NewScrollbackWriter(&w, 10, true)

	// 8 个 CJK 字符 = 16 个 cell > width 10 ⇒ 必须切成 2 行（每行 ≤ 10 cell）
	// 旧实现按 rune 数（8 ≤ 10）⇒ **1 行**，在终端里是 16 cell 宽，必然错位。
	lines := sw.wrapLine("你好世界你好世界")
	if len(lines) != 2 {
		t.Fatalf("N-138②: 16 cell 的 CJK 串在 width=10 下应切成 2 行，got %d 行: %q", len(lines), lines)
	}
	for i, ln := range lines {
		if got := ansi.StringWidth(ln); got > 10 {
			t.Errorf("N-138②: 第 %d 行显示宽度 %d > 10: %q", i, got, ln)
		}
	}
	if joined := strings.Join(lines, ""); joined != "你好世界你好世界" {
		t.Errorf("N-138②: 换行不得丢字符，got %q", joined)
	}
}

func TestN138_WrapLine_AnsiAware(t *testing.T) {
	var w strings.Builder
	sw := NewScrollbackWriter(&w, 10, true)

	// 带 SGR 的串：ANSI 序列**不占显示宽度**，不能被当成可显示字符计数。
	lines := sw.wrapLine("\x1b[31m你好世界\x1b[0m你好世界")
	for i, ln := range lines {
		if got := ansi.StringWidth(ln); got > 10 {
			t.Errorf("N-138②: 第 %d 行显示宽度 %d > 10（SGR 不得计入宽度）: %q", i, got, ln)
		}
	}
	if len(lines) < 2 {
		t.Errorf("N-138②: 16 cell 内容应至少切成 2 行，got %d", len(lines))
	}
}

func TestN138_WrapLine_ZeroWidthIsPassthrough(t *testing.T) {
	var w strings.Builder
	sw := NewScrollbackWriter(&w, 0, true)
	lines := sw.wrapLine("abc")
	if len(lines) != 1 || lines[0] != "abc" {
		t.Errorf("N-138②: width<=0 应原样返回，got %q", lines)
	}
}

// ───────────────── 族① 的降级语义 + 既有白名单过滤（防回归） ─────────────────

func TestN138_WriteLines_UnsupportedIsNoop(t *testing.T) {
	var w strings.Builder
	sw := NewScrollbackWriter(&w, 20, false)
	if err := sw.WriteLines([]string{"不应被写出"}); err != nil {
		t.Fatalf(": 不支持时不应报错（降级由调用方负责），got %v", err)
	}
	if w.Len() != 0 {
		t.Errorf(": 不支持时不得写出任何字节（否则会把控制序列/内容写进未知终端），got %q", w.String())
	}
	if sw.Supported() {
		t.Error(": Supported() 应与构造参数一致")
	}
}

func TestN138_WriteLines_SanitizeWhitelistAndEmit(t *testing.T) {
	var w strings.Builder
	sw := NewScrollbackWriter(&w, 200, true)
	in := "\x1b[31mred\x1b[2J\x1b[?1049h\x1bMtail"
	if err := sw.WriteLines([]string{in}); err != nil {
		t.Fatalf("WriteLines 报错: %v", err)
	}
	out := w.String()
	if !strings.Contains(out, "\x1b[31m") {
		t.Errorf(": SGR 样式应保留（white-list），got %q", out)
	}
	for _, danger := range []string{"\x1b[2J", "\x1b[?1049h", "\x1bM"} {
		if strings.Contains(out, danger) {
			t.Errorf(": 危险序列 %q 必须被剥离（escape sequence 注入防护），got %q", danger, out)
		}
	}
	if !strings.HasSuffix(out, "\r\n") {
		t.Errorf(": 每行应以 CRLF 收尾（终端自动滚入 scrollback），got %q", out)
	}
}
