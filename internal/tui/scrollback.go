package tui

import (
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// scrollback.go —— 把「已完成的历史行」写进**终端原生 scrollback**。
//
// 对标同类 CLI 的 insert_history（REF-外部: 同类 CLI）：历史不占渲染区，
// 终端原生鼠标选择/复制天然可用，性能好。
//
// ⚠️ 接线状态（，2026-09-23 实查）：**本文件的导出符号在非测试代码里引用数 = 0**
// （`ScrollbackWriter` / `NewScrollbackWriter` / `WriteLines` / `DetectScrollbackSupport`
// 仅在本文件自身出现）。看板 `IT1-014` 记为"进行中"，剩余只写"跨平台收尾"——
// **"完成"的定义漏了最关键的一步：接线**（"件在、测试绿、看板绿，效果为零"）。
// ⇒ 本文件的目标状态是「前置清干净、可被 STEP 3 一次接上」，而**不是**"已集成"。
//
// ★ 与 `internal/tui` 里另一个 "scrollback" 的区别（**勿混谈**）：
// - 展示层的 `[]string` 视口缓冲（C1 轮在讨论、已判"保留"的那份）= 产品选择；
// - **本文件的终端原生 scrollback** = 从未接线。
//
// 审查条件2：ANSI 过滤改白名单，仅放行 SGR（\x1b[...m），
// 移除危险 CSI（如 \x1b[2J 清屏 / \x1b[?1049h 切 alt-screen），防 escape sequence 注入。

// ansiAnySeqPattern 统一匹配所有 ANSI 转义序列类型：
// - OSC: \x1b]...BEL 或 \x1b]...ST
// - CSI: \x1b[...<final byte>
// - 字符集指定: \x1b( B / \x1b) 0
// - ESC+单字符: \x1bM(RI)/\x1bD(IND)/\x1bE(NEL)/\x1bH(HTS)/\x1bc(RIS)/\x1b7(DECSC)/\x1b8(DECRC)/\x1b=/\x1b>
//
// 审查条件2（白名单）：仅放行 SGR（\x1b[...m），其余一律移除。
var ansiAnySeqPattern = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b\[[0-9;?]*[A-Za-z]|\x1b[()][A-Za-z0-9]|\x1b[A-Za-z0-9=>]`)

// sanitizeForScrollback 对历史文本做 ANSI 白名单过滤。
//
// 仅保留 SGR 序列（\x1b[<params>m，样式控制，安全无副作用），
// 移除所有其他 ANSI 转义序列（CSI 非 m / OSC / ESC+字母 / 字符集指定），
// 防止历史文本中的转义序列被终端执行（escape sequence 注入防护）。
//
// 例："\x1b[31m红色\x1b[2J清屏\x1bM滚动" → "\x1b[31m红色清屏滚动"（保留颜色，移除清屏和滚动）。
func sanitizeForScrollback(s string) string {
	return ansiAnySeqPattern.ReplaceAllStringFunc(s, func(seq string) string {
		// 仅保留 SGR：以 \x1b[ 开头且以 m 结尾
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			return seq // SGR 样式，保留
		}
		return "" // 其他所有 ANSI 序列（清屏/光标/alt-screen/RI/IND/RIS 等），移除
	})
}

// ScrollbackWriter 把已完成的历史行写入终端原生 scrollback。
//
// ⚠️ 职责边界（收窄，勿扩散）：本编写器**只做三件事** ——
// ① ANSI 白名单过滤 ② 按**显示宽度**换行 ③ 以 "\r\n" 逐行写出。
//
// 它**不**管理 DECSTBM scroll region，也**不**管理光标位置。原实现声称"用 DECSTBM + RI
// 定位 scrollback 区"，但那个前提在 bubbletea 接管终端后**无法成立**：光标位置只有渲染器
// 知道，本文件若自己写 \x1b[<top>;<bottom>r 与 \x1bM，就是与渲染器**争夺光标**——
// 原注释已自承"需终端光标位置，集成 inline 模式时完善"，属未验证的设计承诺。
//
// 框架已提供等价且正确的机制：bubbletea inline 模式（不启用 alt-screen）下，
// `tea.Println` 会把行插到渲染视图**上方**并自然滚入终端原生 scrollback，光标记账由
// 渲染器负责。⇒ 接线时报 **不得** 在此手写区域序列（那会是"同语义第二份实现"）。
//
// ★ 对标事实（2026-09-23 本地 同类 CLI 快照实查；**勿误读为"手写 region 就是错的"**）：
// 同类 CLI 的 `tui/src/insert_history.rs`（1066 行）**恰恰手写** scroll region ＋ RI ——
// `SetScrollRegion(top..height)`（:201）→ 循环 `Print("\x1bM")`（:203-205）→ `ResetScrollRegion`（:206）。
// 它能这么做，是因为它拿的是 ratatui 的 **Terminal 本身**（`terminal.backend_mut()` ·
// `terminal.last_known_cursor_pos` · `terminal.clear_after_position`），站在**渲染器内部**、持有光标账本。
// ⇒ 真正的分界不是"手写 vs 不手写"，而是**在渲染器内还是外**。本文件拿的是裸 `io.Writer`，
// 在渲染器**外面** ⇒ 只能把这件事交给持有账本的那一层（Go 里就是下面这个 `tea.Println`）。
//
// ⚠️ **接线陷阱（必读）**：`tea.Println` **只在 inline 模式生效** ——
// `vendor/github.com/charmbracelet/bubbletea/standard_renderer.go:655-662` 明确 `if !r.altScreenActive`，
// 其文档亦写明 "If the altscreen is active no output will be printed"。
// ⇒ 在 alt-screen 下调用它是**静默 no-op**（又一个"改了没用"）。故接线必须与
// `--no-alt-screen`（`main.go` 的 `resolveAltScreen`）**绑定，二者同生共死**。
//
// 注意（审查条件1）：需在 bubbletea 渲染帧之间的空窗期调用，避免与 renderer 冲突。
type ScrollbackWriter struct {
	w         io.Writer
	width     int
	supported bool // DECSTBM 探测结果（false 时调用方应降级 viewport）
}

// NewScrollbackWriter 创建历史写入器。
// supported 由 DetectScrollbackSupport 探测结果传入。
func NewScrollbackWriter(w io.Writer, width int, supported bool) *ScrollbackWriter {
	return &ScrollbackWriter{w: w, width: width, supported: supported}
}

// Supported 返回终端是否支持写入原生 scrollback。
// false ⇒ **调用方必须降级**（回落自绘 viewport），而不是硬写（见 DetectScrollbackSupport）。
func (sw *ScrollbackWriter) Supported() bool {
	return sw.supported
}

// WriteLines 将历史行写入终端原生 scrollback。
//
// 步骤：① 逐行 sanitizeForScrollback ② 按显示宽度换行 ③ "\r\n" 逐行写出
// （终端在最后一行之后自动滚动，历史行随之进入原生 scrollback）。
//
// 不支持时**静默跳过并返回 nil**，但调用方应先看 Supported() 决定是否降级渲染 ——
// 这里返回 nil 表示"没有可写的错误"，不表示"内容已经可见"。
func (sw *ScrollbackWriter) WriteLines(lines []string) error {
	if !sw.supported {
		return nil // 不支持则跳过，调用方降级 viewport
	}
	var b strings.Builder
	for _, line := range lines {
		// 过滤危险 ANSI，保留 SGR 样式
		safe := sanitizeForScrollback(line)
		// 按显示宽度换行（宽字符友好）
		for _, wl := range sw.wrapLine(safe) {
			b.WriteString(wl)
			b.WriteString("\r\n")
		}
	}
	_, err := io.WriteString(sw.w, b.String())
	return err
}

// wrapLine 按**显示宽度**换行（CJK 宽字符、emoji、ANSI SGR 均按真实占位计算）。
//
// 旧实现按 **rune 个数**切片（`for i := 0; i < len(runes); i += sw.width`）——
// CJK 一个 rune 占 **2** 个 cell，于是"每 width 个 rune 一行"实际会画出 2×width 宽，
// 在真实终端里必然折行错位（且 ANSI SGR 序列的字节也被当成可显示字符计数）。
// 现改用框架自带的 `ansi.Hardwrap`（ANSI 感知 + 宽字符感知），不再自造宽度算法 ——
// 宽度口径与 TUI 其余部分（`TextMeasurer` / runewidth）保持同源。
func (sw *ScrollbackWriter) wrapLine(s string) []string {
	if sw.width <= 0 {
		return []string{s}
	}
	wrapped := ansi.Hardwrap(s, sw.width, true)
	if wrapped == "" {
		return []string{""}
	}
	return strings.Split(wrapped, "\n")
}

// scrollbackOverrideEnv 显式覆盖开关：WHALE_SCROLLBACK=1 强制开、=0 强制关。
// 它是**回归测试与用户兜底**的唯一手段（探测是声明式的，不可能覆盖所有终端）。
const scrollbackOverrideEnv = "WHALE_SCROLLBACK"

// scrollbackSupportFor 是**纯判定**：相同输入恒给相同答案。
//
// 把「判定」与「读环境/读 TTY」分开，是为了让两个方向都能被单测钉死
// （不必改真实环境、也不依赖测试机是不是 TTY）。旧实现 `DetectScrollbackSupport()`
// **恒返回 true**，注释自承 TODO ⇒ 属"活缝补"：一旦到了不支持 DECSTBM/RI 的终端
// （Windows legacy conhost 等），显示立刻错乱，而**降级路径永不触发**。
//
// 判定顺序（先覆盖、再能力）：
// 1. override（"1"/"0"）最优先 —— 用户与回归测试都能钉死行为；
// 2. 输出端必须真的是终端：管道/重定向/CI 一律不支持
// （把控制序列写进日志或产物文件毫无意义，且会污染产物）；
// 3. TERM 为空或 `dumb` ⇒ 不支持（最保守的哑终端）；
// 4. Windows：Windows Terminal（有 WT_SESSION）支持；legacy conhost 不支持；
// 5. Linux 虚拟控制台（TERM 前缀 `linux`）⇒ 保守关闭（RI 历史滚入行为与 xterm 不同）。
//
// ⚠️ 边界（如实标注）：这是**声明式能力表**，不是运行期交互探测。真正的 DECSTBM 探测
// 需要"写序列 + 读终端应答"，而这在 bubbletea 接管输入后**无法安全进行**。
// 因此漏判的唯一兜底是**降级路径**（fallback 自绘 viewport），而不是"猜对了"。
func scrollbackSupportFor(override string, stdoutIsTTY bool, term, wtSession, goos string) bool {
	switch strings.TrimSpace(override) {
	case "1":
		return true
	case "0":
		return false
	}
	if !stdoutIsTTY {
		return false
	}
	if term == "" || term == "dumb" {
		return false
	}
	if goos == "windows" && wtSession == "" {
		// 无 WT_SESSION ⇒ 大概率是 legacy conhost
		return false
	}
	if goos != "windows" && strings.HasPrefix(term, "linux") {
		return false
	}
	return true
}

// 两个可注入的输入源（R6b）：`os.Stdout` 的 TTY 形态与进程环境在单测里
// **天然取值不同**（`go test` 的 stdout 常是管道、环境也不含 WT_SESSION）⇒
// 若直接读它们，测试就永远测不到生产取值。抽成函数变量后，测试可注入生产取值。
var (
	scrollbackStdoutIsTTY = func() bool {
		fi, err := os.Stdout.Stat()
		if err != nil {
			return false
		}
		return fi.Mode()&os.ModeCharDevice != 0
	}
	scrollbackGetenv = os.Getenv
)

// DetectScrollbackSupport 取真实输入并交给纯判定 scrollbackSupportFor。
//
// 审查条件3：不支持 DECSTBM/RI 的终端需降级，故调用方须先问本函数，
// false 时**回落自绘 viewport**（而不是硬写）。
func DetectScrollbackSupport() bool {
	return scrollbackSupportFor(
		scrollbackGetenv(scrollbackOverrideEnv),
		scrollbackStdoutIsTTY(),
		scrollbackGetenv("TERM"),
		scrollbackGetenv("WT_SESSION"),
		runtime.GOOS,
	)
}
