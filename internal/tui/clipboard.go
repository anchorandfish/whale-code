// Package tui - clipboard image paste support
//
// modeled after a comparable CLI image paste: user presses Ctrl+V to paste clipboard image，
// auto-encodes as base64 and injects as image content block。
package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	imgproc "github.com/anchorandfish/whale-code/internal/image"
	"github.com/anchorandfish/whale-code/internal/util"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// imagePlaceholderRe 匹配贴图占位符 [Image #N]（⑤）。
// 连同其后的一个可选空格一起吞掉，避免剥离后残留双空格。
var imagePlaceholderRe = regexp.MustCompile(`\[Image #\d+\] ?`)

// pasteHintStyle 是「剪贴板有图」提示行的样式（②），与既有 hintStyle 同灰阶。
var pasteHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

// clipboardImageProbe 探测「系统剪贴板当前是否有图片」（②）。
// 可注入变量（R6b：测试替换的是输入源，不替换任何判定分支）；
// 默认实现复用既有平台读取器（macOS osascript / Windows PowerShell / Linux wl-paste·xclip）。
var clipboardImageProbe = func() bool {
	data, err := GetClipboardImage()
	return err == nil && len(data) > 0
}

// clipboardImageProbeMsg 是探针的异步结果。
type clipboardImageProbeMsg struct{ hasImage bool }

// clipboardImageProbeCmd 在 cmd goroutine 里探测剪贴板（进程外调用，禁入 Update 主循环）。
func clipboardImageProbeCmd() tea.Cmd {
	return func() tea.Msg { return clipboardImageProbeMsg{hasImage: clipboardImageProbe()} }
}

// stripImagePlaceholders 从待发送文本中移除贴图占位符。
// 占位符只承载显示（⑤），图片本体经 PendingImages 在发送时附发（stream.go 合并点）。
func stripImagePlaceholders(s string) string {
	return strings.TrimSpace(imagePlaceholderRe.ReplaceAllString(s, ""))
}

// imagePlaceholderNumRe 匹配带编号的占位符**本体**（不含尾随空格），用于引用解析与编号重排（⑥⑦）。
var imagePlaceholderNumRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// imagePlaceholderSpan 是某行内一个占位符本体的 rune 区间 [start, end)（不含尾随空格）。
// num 是占位符里的编号 N（1-based），用于把「文本里的引用」映射回「队列里的第 N 张图」（⑧）。
type imagePlaceholderSpan struct {
	start, end int
	num        int
}

// imagePlaceholderSpans 返回单行文本中全部占位符本体的 rune 区间（按出现顺序）。
//
// regexp 给的是**字节**偏移，而输入框光标列是 **rune** 下标 —— 行内可能含 CJK/宽字符，
// 必须换算，否则「占位符之后的文字越长，原子删除判定越偏」。
func imagePlaceholderSpans(line string) []imagePlaceholderSpan {
	idx := imagePlaceholderNumRe.FindAllStringSubmatchIndex(line, -1)
	if len(idx) == 0 {
		return nil
	}
	spans := make([]imagePlaceholderSpan, 0, len(idx))
	for _, m := range idx {
		num, _ := strconv.Atoi(line[m[2]:m[3]])
		spans = append(spans, imagePlaceholderSpan{
			start: len([]rune(line[:m[0]])),
			end:   len([]rune(line[:m[1]])),
			num:   num,
		})
	}
	return spans
}

// imagePlaceholderURI 把占位符编号映射为对应图片的 OSC 8 目标 URI（⑧）。
//
// 编号是「队列第 N 项」的引用（与插入侧 applyClipboardPaste、⑥ 的 prune 同源约定，
// 见 reconcilePendingImages 的编号说明）⇒ 编号 → 图片 → 落盘路径 → file:// URI。
//
// 返回 "" 表示**不链接**（渲染降级为普通成块文本），三种情形：
// - 编号越界：文本里的引用与队列不一致（用户手写的占位符、或队列已被改动）；
// - 图片未落盘：落盘失败/HOME 不可写（Path 为空）；
// - 编号非法。
func imagePlaceholderURI(num int, images []PendingImage) string {
	if num < 1 || num > len(images) {
		return ""
	}
	path := images[num-1].Path
	if path == "" {
		return ""
	}
	return FileLinkURI(FileLink{Path: path})
}

// imagePlaceholderDeleteRange 判定「光标 (row,col) 处的一次删除是否应整块吞掉占位符」（⑨）。
//
// backspace=true 表示向左删（Backspace / ctrl+w / alt+backspace），false 表示向右删（Delete / alt+d）。
// 命中时返回要删除的 rune 区间 [start,end)（含被一并吞掉的尾随分隔空格）与 true；
// 未命中返回 false —— 调用方须原样放行给 textarea，保持普通文本的逐字符/逐词删除语义。
//
// 判定规则（贴着或落在占位符上即视为「要删这个整体」； 起单元口径 =
// **本体 + 尾随分隔空格**，导航/左删/右删三处同源）：
// - 向左：光标在占位符内或紧贴右端（start < col ≤ end）⇒ 整块删，并吞掉尾随分隔空格；
// 光标停在尾随空格之后（col == end+1 且该字符是空格）⇒ 连空格一起删。
// - 向右：光标在占位符内或紧贴左端（start ≤ col < end）⇒ 整块删，
// 并吞掉尾随分隔空格（：不留孤立空格，与向左口径对称）。
func imagePlaceholderDeleteRange(lines []string, row, col int, backspace bool) (int, int, bool) {
	if row < 0 || row >= len(lines) {
		return 0, 0, false
	}
	runes := []rune(lines[row])
	for _, s := range imagePlaceholderSpans(lines[row]) {
		if !backspace {
			if s.start <= col && col < s.end {
				end := s.end
				if end < len(runes) && runes[end] == ' ' {
					end++ // 占位符与其后的分隔空格一起走，免得留个孤立空格
				}
				return s.start, end, true
			}
			continue
		}
		switch {
		case s.start < col && col <= s.end:
			end := s.end
			if end < len(runes) && runes[end] == ' ' {
				end++ // 占位符与其后的分隔空格一起走，免得留个孤立空格
			}
			return s.start, end, true
		case col == s.end+1 && s.end < len(runes) && runes[s.end] == ' ':
			return s.start, col, true
		}
	}
	return 0, 0, false
}

// imagePlaceholderNavCol 判定「光标 (row 行内 col 列) 的一次左右移动是否应整块
// 跳过占位符」（， ⑨ 的导航半边）。
//
// 原子单元 = 占位符本体 [Image #N] + 其后的一个分隔空格（与 imagePlaceholderDeleteRange
// 的删除范围同源）。left=true 向左：光标在单元内或贴右端（start < col ≤ 单元尾）⇒
// 目标 = 本体前；left=false 向右：光标在单元内或贴左端（start ≤ col < 单元尾）⇒
// 目标 = 单元尾。命中返回目标列与 true；未涉及占位符返回 false —— 调用方原样
// 放行给 textarea，保持普通文本的逐字符导航。
func imagePlaceholderNavCol(line string, col int, left bool) (int, bool) {
	runes := []rune(line)
	for _, s := range imagePlaceholderSpans(line) {
		unitEnd := s.end
		if unitEnd < len(runes) && runes[unitEnd] == ' ' {
			unitEnd++
		}
		if left {
			if col > s.start && col <= unitEnd {
				return s.start, true
			}
		} else if col >= s.start && col < unitEnd {
			return unitEnd, true
		}
	}
	return 0, false
}

// imagePlaceholderUnitEnd 返回占位符原子单元（本体 + 尾随分隔空格）的排他终点。
// 与 imagePlaceholderDeleteRange / imagePlaceholderNavCol 的单元口径同源。
func imagePlaceholderUnitEnd(runes []rune, s imagePlaceholderSpan) int {
	unitEnd := s.end
	if unitEnd < len(runes) && runes[unitEnd] == ' ' {
		unitEnd++
	}
	return unitEnd
}

// imagePlaceholderSnapCol 判定「落点 col 是否骑在占位符单元内部，是则吸附到
// 最近边界」（：上下跨行 / 鼠标点击的落点吸附）。平局取本体前 ——
// 与 导航「向左优先」的口径一致：宁可停在整块之前，不停在残缺中间。
// 未骑在任何单元上返回 false（落点合法，原样保留）。
func imagePlaceholderSnapCol(line string, col int) (int, bool) {
	runes := []rune(line)
	best, bestDist := 0, 1<<31
	for _, s := range imagePlaceholderSpans(line) {
		unitEnd := imagePlaceholderUnitEnd(runes, s)
		if !(s.start < col && col < unitEnd) {
			continue
		}
		cand := s.start
		if unitEnd-col < col-s.start {
			cand = unitEnd
		}
		d := col - cand
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			bestDist, best = d, cand
		}
	}
	if bestDist == 1<<31 {
		return 0, false
	}
	return best, true
}

// imagePlaceholderWordMoveCol 判定「原生按词移动后落在 col，是否需要吸附出
// 占位符单元」（：alt+←→ / alt+b/f 的词边界不认识占位符，会停在
// "Image|#"、"#"、"1" 这类内部词边界上）。吸附口径：
// - 向左移动落在单元**内部**（严格 < unitEnd）⇒ 回吸到本体前；
// 恰落在单元尾（= 下一词词首）是合法落点，不吸 —— 否则会跳过中间的普通词。
// - 向右移动落在单元内（含贴本体前）⇒ 推到单元尾。
func imagePlaceholderWordMoveCol(line string, col int, left bool) (int, bool) {
	runes := []rune(line)
	for _, s := range imagePlaceholderSpans(line) {
		unitEnd := imagePlaceholderUnitEnd(runes, s)
		if left {
			if s.start < col && col < unitEnd {
				return s.start, true
			}
		} else if s.start <= col && col < unitEnd {
			return unitEnd, true
		}
	}
	return 0, false
}

// imagePlaceholderTransposeHits 判定「ctrl+t 的交换窗 {col-1, col} 是否会碰到
// 占位符本体」（：交换会把 "] " 变成 " ]"，占位符文本改坏 ⇒ 静默丢图）。
func imagePlaceholderTransposeHits(line string, col int) bool {
	if col <= 0 {
		return false // 没有交换窗
	}
	for _, s := range imagePlaceholderSpans(line) {
		for _, idx := range [2]int{col - 1, col} {
			if idx >= s.start && idx < s.end {
				return true
			}
		}
	}
	return false
}

// imagePlaceholderUnitAtCol 判定「光标是否骑在某个占位符单元上」（：
// alt+u/l/c 词大小写键会把本体当普通词变换，"​[Image" 被改大小写后识别不了）。
// 命中返回该单元终点（把整个单元当一个词整体跳过的落点）。
func imagePlaceholderUnitAtCol(line string, col int) (int, bool) {
	runes := []rune(line)
	for _, s := range imagePlaceholderSpans(line) {
		unitEnd := imagePlaceholderUnitEnd(runes, s)
		if s.start <= col && col < unitEnd {
			return unitEnd, true
		}
	}
	return 0, false
}

// reconcilePendingImages 让「输入框里的 [Image #N] 占位符」与「待发图片队列」保持一致（⑥⑦）：
//
//	⑥ 文本里不再被引用的图从队列剔除 —— 用户删掉占位符 = 不想发这张图
//
// （对齐 同类 CLI `PromptInput.tsx:1185` 与 同类 CLI `chat_composer.rs:64` 的 prune 语义）。
//
//	⑦ 存活者按文本出现顺序重新编号 1..K 并同步改写文本
//
// （对齐 同类 CLI `chat_composer.rs:101-105` 的编号连续化）。
//
// 返回 true 表示队列发生过改动。
//
// 编号约定（与插入侧 `applyClipboardPaste` 同源）：`[Image #N]` 的 N == 队列第 N 项（1-based），
// 因此「队列顺序」即「编号顺序」，不需要额外字段承载编号。
//
// 已知代价：textarea 只有 SetCursor（行内列号）、**没有 cursor getter**，
// 重排改写走 SetValue ⇒ 光标落到文本末尾。仅在「多图 + 编号确实需要重排」时发生（单图删除不需要改写文本）。
func (m *Model) reconcilePendingImages() bool {
	images := m.uiService.PendingImages()
	if len(images) == 0 {
		return false // 热路径快速返回：绝大多数输入没有任何待发图。
	}
	// 面板/确认态下输入框不是当前可编辑面，不得动它（与 报障里的「面板期间静默丢键」同族边界）。
	if !m.canAcceptComposerText() {
		return false
	}
	ta := m.uiService.Textarea()
	text := ta.Value()
	matches := imagePlaceholderNumRe.FindAllStringSubmatchIndex(text, -1)

	// 收集「文本仍引用、且编号落在队列范围内」的编号（按出现顺序去重）。
	refs := make([]int, 0, len(matches))
	seen := make(map[int]bool, len(matches))
	for _, mt := range matches {
		n, err := strconv.Atoi(text[mt[2]:mt[3]])
		if err != nil || n < 1 || n > len(images) || seen[n] {
			// 非法/越界编号：既不认领图片，也不改写这段文字（它不是我们插的占位符）。
			continue
		}
		seen[n] = true
		refs = append(refs, n)
	}

	// 快速路径：引用恰好是 1..len(images) 且顺序一致 ⇒ 本来就一致，不碰输入框（避免无谓改写与光标跳动）。
	if len(refs) == len(images) {
		contiguous := true
		for i, n := range refs {
			if n != i+1 {
				contiguous = false
				break
			}
		}
		if contiguous {
			return false
		}
	}

	// 旧编号 → 新编号；顺带重建文本（只替换占位符本体，其余字符逐字保留）。
	renum := make(map[int]int, len(refs))
	for i, n := range refs {
		renum[n] = i + 1
	}
	var sb strings.Builder
	sb.Grow(len(text))
	last := 0
	for _, mt := range matches {
		n, err := strconv.Atoi(text[mt[2]:mt[3]])
		if err != nil {
			continue
		}
		newN, ok := renum[n]
		if !ok {
			continue // 未被认领的引用：原样留在文本里。
		}
		sb.WriteString(text[last:mt[0]])
		fmt.Fprintf(&sb, "[Image #%d]", newN)
		last = mt[1]
	}
	sb.WriteString(text[last:])

	// prune 队列：只保留被引用的图，并按文本出现顺序重排（编号 ↔ 队列项一一对应）。
	kept := make([]PendingImage, 0, len(refs))
	for _, n := range refs {
		kept = append(kept, images[n-1])
	}
	m.uiService.SetPendingImages(kept)
	if newText := sb.String(); newText != text {
		ta.SetValue(newText)
	}
	return true
}

// PendingImage pending pasted images
// stores image info pasted from clipboard, waiting to be sent with message
type PendingImage struct {
	MimeType  string `json:"mimeType"`
	Base64    string `json:"base64"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	EstTokens int    `json:"estTokens,omitempty"`
	// Path 是这张图落盘后的绝对路径（⑧），供输入框把 [Image #N] 渲染成可点击的
	// OSC 8 超链接（点击用系统看图程序打开原图）。落盘失败/HOME 不可写时为空 ⇒ 不链接。
	Path string `json:"path,omitempty"`
}

// clipboardPasteMsg 是异步系统剪贴板读取完成后的主循环消息。
// 文本和图片最多返回一种。
type clipboardPasteMsg struct {
	seq   uint64
	text  string
	image *PendingImage
	err   error
}

// getClipboardRichText 是富文本剪贴板读取的可注入输入源（④，R6b）。
// 默认实现即平台读取器；测试替换它来模拟「剪贴板里的文本」。
var getClipboardRichText = GetClipboardRichText

// imageFileExtensions 是「贴入文本可能是图片文件」的扩展名白名单（④）。
// 只做**初筛**：真正是否图片由 imgproc.FromPath 的魔数识别定（不信任扩展名）。
var imageFileExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

// pastedImageFilePath 从贴入文本里识别出图片文件路径；不是路径形态则返回空。
// 形态约束：单行、长度合理、扩展名在白名单内；支持成对引号包裹与 ~/ 展开。
func pastedImageFilePath(text string) string {
	p := strings.TrimSpace(text)
	if p == "" || strings.ContainsAny(p, "\n\r") || len(p) > 1024 {
		return ""
	}
	p = strings.Trim(p, `"'`)
	if p == "" || strings.ContainsAny(p, "\n\r") {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		p = filepath.Join(home, p[2:])
	}
	if !imageFileExtensions[strings.ToLower(filepath.Ext(p))] {
		return ""
	}
	return p
}

// tryPasteImagePath 尝试把贴入文本当作图片文件附图（W-④，对齐 同类 CLI handle_paste_image_path）。
// 复用 imgproc.FromPath SSOT（os.Stat + 10MB 守卫 + 魔数识别 + 压缩）；任何失败都降级为「按文本贴入」。
// 成功时 path 返回**原文件**路径 —— PendingImage.Path 让 ⑧ 的占位符链接直接指向用户复制的那张图。
func tryPasteImagePath(text string) (imgproc.ImageRef, string, bool) {
	p := pastedImageFilePath(text)
	if p == "" {
		return imgproc.ImageRef{}, "", false
	}
	ref, err := imgproc.FromPath(p)
	if err != nil {
		return imgproc.ImageRef{}, "", false
	}
	return ref, p, true
}

// pasteFailureNotice 把剪贴板粘贴错误转成用户可见文案（③ 教学化）。
//
// 背景（2026-10-15 真机误报定谳）：用户在 Terminal.app 按 ⌘Cmd+V 贴图 → 按键被终端
// 在最外层截走（剪贴板只有图时终端无文字可贴、无任何回显）→ 用户以为 whale 坏了。
// 空剪贴板族的报错是 whale 唯一能开口的位置，必须把正确按键与 ⌘V 失效原因一起说清。
// 其它错误族保持原样（各有各的成因，不硬塞教学）。
func pasteFailureNotice(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "no image in clipboard") ||
		strings.Contains(msg, "clipboard is empty") ||
		strings.Contains(msg, "image data is empty") {
		return bi(
			"Clipboard is empty. To paste an image: copy one first (screenshot: ⌘⇧⌃4), then press Ctrl+V — ⌘V is captured by the terminal and never reaches whale",
			"剪贴板是空的。贴图：先复制图片（截图 ⌘⇧⌃4），再按 Ctrl+V（⌘V 会被终端截走，到不了 whale）",
		)
	}
	return fmt.Sprintf("Clipboard paste failed: %v", err)
}

// readClipboardPasteCmd 在 Bubble Tea 命令 goroutine 中读取系统剪贴板。
// 富文本转换和图片压缩都可能调用外部进程或执行重计算，禁止在 Update 主循环同步执行。
func readClipboardPasteCmd(includeImage bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		htmlContent, plainText, textErr := getClipboardRichText()
		if htmlContent != "" {
			if text := htmlToMarkdown(htmlContent); text != "" {
				return clipboardPasteMsg{seq: seq, text: text}
			}
		}
		if plainText != "" {
			// ④：贴入文本若是真实图片文件路径 ⇒ 自动附图（对齐 同类 CLI
			// handle_paste_image_path）。Finder 复制图片文件后 ⌘V，终端贴进来的
			// 是路径文本——这是用户既有习惯的通路。失败降级为按文本贴入。
			if ref, path, ok := tryPasteImagePath(plainText); ok {
				return clipboardPasteMsg{
					seq: seq,
					image: &PendingImage{
						MimeType:  ref.MediaType,
						Base64:    ref.Base64,
						Width:     ref.Width,
						Height:    ref.Height,
						EstTokens: ref.EstTokens,
						// ⑧ 联动：Path 指向**原文件**（非临时落盘），点击占位符打开原图。
						Path: path,
					},
				}
			}
			return clipboardPasteMsg{seq: seq, text: plainText}
		}

		var imageErr error
		if includeImage {
			imageData, err := GetClipboardImage()
			if err == nil && len(imageData) > 0 {
				ref, pasteErr := imgproc.Paste(imageData)
				if pasteErr != nil {
					return clipboardPasteMsg{seq: seq, err: fmt.Errorf("image paste failed: %w", pasteErr)}
				}
				return clipboardPasteMsg{
					seq: seq,
					image: &PendingImage{
						MimeType:  ref.MediaType,
						Base64:    ref.Base64,
						Width:     ref.Width,
						Height:    ref.Height,
						EstTokens: ref.EstTokens,
						// ⑧：把压缩后的图片落盘，占位符才能变成"点开原图"的链接。
						// 落盘在 cmd goroutine 里做（与压缩同处），不阻塞 Update 主循环。
						Path: storePastedImage(ref.Data, ref.MediaType),
					},
				}
			}
			imageErr = err
		}

		if textErr != nil && imageErr != nil {
			return clipboardPasteMsg{seq: seq, err: fmt.Errorf("clipboard read failed (text: %v; image: %v)", textErr, imageErr)}
		}
		if textErr != nil {
			return clipboardPasteMsg{seq: seq, err: textErr}
		}
		if imageErr != nil {
			return clipboardPasteMsg{seq: seq, err: imageErr}
		}
		return clipboardPasteMsg{seq: seq, err: fmt.Errorf("clipboard is empty")}
	}
}

// applyClipboardPaste 将单个剪贴板结果应用到当前编辑器或图片队列。
func (m *Model) applyClipboardPaste(msg clipboardPasteMsg) tea.Cmd {
	if !m.canAcceptComposerText() {
		return nil
	}
	if msg.err != nil {
		// ③：空剪贴板族错误换成教学文案；顺带复核 ② 的过期提示
		// （刚才那次读取已明确回答「剪贴板没有图」）。
		m.clipboardHasImage = false
		m.addMessage(errorStyle.Render(pasteFailureNotice(msg.err)))
		return nil
	}
	if msg.image != nil {
		images := append(m.uiService.PendingImages(), *msg.image)
		m.uiService.SetPendingImages(images)
		// ②：图片已消费，提示立即撤下。
		m.clipboardHasImage = false
		// ⑤（2026-10-15 用户拍板）：贴图回执不再写进对话流（滚走后易"以为没贴上"），
		// 改为在输入框内插入 [Image #N] 占位符——所见即所得，对齐同类 CLI。
		// 发送时占位符从文本剥离（stripImagePlaceholders，stream.go），图片本体经 PendingImages 附发。
		if truncated := m.insertComposerText(fmt.Sprintf("[Image #%d] ", len(images))); truncated {
			m.showInputLimitWarning()
		}
		return nil
	}
	if truncated := m.insertComposerText(msg.text); truncated {
		m.showInputLimitWarning()
	}
	if m.shouldDebounceTypeahead(msg.text) {
		return m.debounceTypeaheadCmd()
	}
	return nil
}

// handleClipboardPasteResult 缓存乱序完成的 Ctrl+V 结果，并按触发顺序连续应用。
func (m *Model) handleClipboardPasteResult(msg clipboardPasteMsg) tea.Cmd {
	// seq=0 兼容内部直接消息和既有测试，不参与异步 Ctrl+V 排序。
	if msg.seq == 0 {
		return m.applyClipboardPaste(msg)
	}
	if msg.seq <= m.clipboardApplySeq {
		return nil
	}
	if m.clipboardPending == nil {
		m.clipboardPending = make(map[uint64]clipboardPasteMsg)
	}
	m.clipboardPending[msg.seq] = msg

	var cmds []tea.Cmd
	for {
		nextSeq := m.clipboardApplySeq + 1
		next, ok := m.clipboardPending[nextSeq]
		if !ok {
			break
		}
		delete(m.clipboardPending, nextSeq)
		m.clipboardApplySeq = nextSeq
		if cmd := m.applyClipboardPaste(next); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// isImagePasteKey 报告按键是否触发剪贴板贴图（①）。
// 主键 ctrl+v；备用键 ctrl+alt+v —— 覆盖「终端把 Ctrl+V 截走」的环境
// （Windows Terminal / VS Code 内置终端默认绑定 Ctrl+V=粘贴文本），
// 对齐同类 CLI（同类 CLI-rs tui keymap.rs fixed.paste_image = ctrl+v + ctrl+alt+v）。
// 注意 bubbletea 对 ESC+0x16 的字符串化是 "alt+ctrl+v"（alt 前缀在前）。
func isImagePasteKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "ctrl+v", "alt+ctrl+v":
		return true
	default:
		return false
	}
}

// ImagePasteHandler clipboard image paste handler
// responsible for fetching image data from system clipboard, verifying size, encoding as base64 and converting to content block format
type ImagePasteHandler struct {
	// MaxSizeBytes is the max image size in bytes, default 10MB
	MaxSizeBytes int64
}

// NewImagePasteHandler createclipboard image paste handler
// default max image size is 10MB
func NewImagePasteHandler() *ImagePasteHandler {
	return &ImagePasteHandler{
		MaxSizeBytes: 10 * 1024 * 1024, // 10MB
	}
}

// HandlePaste handles pasted image data, returns full base64-encoded image reference
// Deprecated: 使用 image.Paste 代替，支持压缩+格式转换+token估算。
// 实际粘贴流程已改走 app.go 的 imgproc.Paste，本函数仅保留向后兼容。
func (h *ImagePasteHandler) HandlePaste(base64Data, mimeType string) string {
	return fmt.Sprintf("[image:%s;base64,%s]", mimeType, base64Data)
}

// DetectMimeType 根据图片原始字节数据检测 MIME 类型
// 通过文件头魔数识别图片格式，支持 png、jpg、gif、webp、bmp
// 无法识别时返回 "application/octet-stream"（与 image.DetectFormat 未知返回值一致，G12 修正）
func DetectMimeType(data []byte) string {
	if len(data) < 8 {
		return "application/octet-stream"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return "image/gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 {
		if data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46 &&
			data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50 {
			return "image/webp"
		}
	}
	// BMP: 42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return "image/bmp"
	}
	return "application/octet-stream" // G12 修正：与 image.DetectFormat 一致
}

// DetectMimeTypeByExt 根据文件扩展名检测 MIME 类型
// 支持常见图片格式: png、jpg/jpeg、gif、webp、bmp、svg
// 未知扩展名返回 "application/octet-stream"（G12 修正：与 image.DetectFormat 未知返回值一致）
func (h *ImagePasteHandler) DetectMimeTypeByExt(ext string) string {
	mimeTypes := map[string]string{
		"png":  "image/png",
		"jpg":  "image/jpeg",
		"jpeg": "image/jpeg",
		"gif":  "image/gif",
		"webp": "image/webp",
		"bmp":  "image/bmp",
		"svg":  "image/svg+xml",
	}
	if mt, ok := mimeTypes[ext]; ok {
		return mt
	}
	return "application/octet-stream" // G12 修正：与 image.DetectFormat 一致
}

// ValidateSize verifies if image size is within limit
// 代理 image.ValidateSize，保留旧签名向后兼容
// base64Data is the base64-encoded string, method estimates original data size
// returns estimated original data size and possible error
// ValidateSize 校验 base64 图片数据大小，返回解码后字节数与可能的错误。
// 双层校验：先调 imgproc.ValidateSize（API 全局上限 APIImageMaxBase64Size，
// 防止超大数据 OOM），再用 h.MaxSizeBytes 做调用方自定义上限检查
// （测试用例可注入小值触发拒绝路径，例如 TestValidateSize_ExceedsLimit）。
func (h *ImagePasteHandler) ValidateSize(base64Data string) (int64, error) {
	if err := imgproc.ValidateSize(base64Data); err != nil {
		// Base64-encoded data size is approximately 4/3 of original data
		decodedSize := int64(len(base64Data)) * 3 / 4
		return decodedSize, err
	}
	decodedSize := int64(len(base64Data)) * 3 / 4
	// 调用方自定义上限（默认 10MB，测试可注入更小值）
	if h.MaxSizeBytes > 0 && decodedSize > h.MaxSizeBytes {
		return decodedSize, fmt.Errorf("image size %d bytes exceeds MaxSizeBytes limit %d", decodedSize, h.MaxSizeBytes)
	}
	return decodedSize, nil
}

// escapePSString escapes a string for safe embedding in PowerShell single-quoted strings
func escapePSString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// GetClipboardImageWindows fetches Windows clipboard image data
// uses PowerShell to save clipboard image to temp file, then reads file content and returns
func GetClipboardImageWindows() ([]byte, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("whale_clipboard_%d.png", time.Now().UnixNano()))
	defer os.Remove(tmpFile)

	psScript := fmt.Sprintf(
		`Add-Type -AssemblyName System.Windows.Forms; $img = [System.Windows.Forms.Clipboard]::GetImage(); if ($img -ne $null) { $img.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png); Write-Output 'OK' } else { Write-Output 'NO_IMAGE' }`,
		escapePSString(tmpFile),
	)

	output, err := runClipboardBytesCommand("powershell", "-NoProfile", "-Command", psScript)
	if err != nil {
		return nil, fmt.Errorf("PowerShell execute failed: %w", err)
	}

	if strings.TrimSpace(string(output)) == "NO_IMAGE" {
		return nil, fmt.Errorf("no image in clipboard")
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read temp image file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("image data is empty")
	}

	return data, nil
}

// GetClipboardImageDarwin fetches macOS clipboard image data
// uses osascript to fetch PNG image from clipboard and save to temp file
func GetClipboardImageDarwin() ([]byte, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("whale_clipboard_%d.png", time.Now().UnixNano()))
	defer os.Remove(tmpFile)

	script := fmt.Sprintf(
		`set theType to (clipboard info) as text
if theType contains "«class PNGf»" then
    set theImage to the clipboard as «class PNGf»
    set theFile to open for access POSIX file "%s" with write permission
    write theImage to theFile
    close access theFile
    return "OK"
else
    return "NO_IMAGE"
end if`,
		tmpFile,
	)

	output, err := runClipboardBytesCommand("osascript", "-e", script)
	if err != nil {
		return nil, fmt.Errorf("osascript execute failed: %w", err)
	}

	if strings.TrimSpace(string(output)) == "NO_IMAGE" {
		return nil, fmt.Errorf("no image in clipboard")
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read temp image file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("image data is empty")
	}

	return data, nil
}

// GetClipboardImage fetches clipboard image data (cross-platform entry point)
// selects the appropriate clipboard image fetch method based on current OS
func GetClipboardImage() ([]byte, error) {
	switch runtime.GOOS {
	case "windows":
		return GetClipboardImageWindows()
	case "darwin":
		return GetClipboardImageDarwin()
	case "linux":
		return GetClipboardImageLinux()
	default:
		return nil, fmt.Errorf("clipboard image paste not yet supported on %s system", runtime.GOOS)
	}
}

// GetClipboardImageLinux fetches clipboard image data on Linux
// tries wl-paste (Wayland) first, then xclip (X11) as fallback
func GetClipboardImageLinux() ([]byte, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("whale_clipboard_%d.png", time.Now().UnixNano()))
	defer os.Remove(tmpFile)

	// Try wl-paste first (Wayland)
	if wlPath, err := exec.LookPath("wl-paste"); err == nil {
		output, err := runClipboardBytesCommand(wlPath, "-t", "image/png")
		if err == nil && len(output) > 0 {
			if err := util.AtomicWriteFile(tmpFile, output, 0600); err == nil {
				data, err := os.ReadFile(tmpFile)
				if err == nil && len(data) > 0 {
					return data, nil
				}
			}
		}
	}

	// Fallback to xclip (X11)
	if xclipPath, err := exec.LookPath("xclip"); err == nil {
		output, err := runClipboardBytesCommand(xclipPath, "-selection", "clipboard", "-t", "image/png", "-o")
		if err == nil && len(output) > 0 {
			if err := util.AtomicWriteFile(tmpFile, output, 0600); err == nil {
				data, err := os.ReadFile(tmpFile)
				if err == nil && len(data) > 0 {
					return data, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no image in clipboard (requires wl-paste or xclip)")
}

// EncodeImage encodes raw image data as base64 string
// Deprecated: 使用 image.Paste 代替，支持压缩+格式转换+token估算
func (h *ImagePasteHandler) EncodeImage(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// ---------------------------------------------------------------------------
// （M10-04）：富文本/HTML 剪贴板支持
// 对标 VSCode Clipboard.readText 富文本粘贴 @官方文档 2026-07-21
// ---------------------------------------------------------------------------

// maxRichTextSize 限制单次富文本粘贴大小（100KB），防止超大 HTML 撑爆 textarea
const maxRichTextSize = 100 * 1024

// clipboardReadTimeout 限制系统剪贴板子进程的最长执行时间，避免后台命令永久悬挂。
const clipboardReadTimeout = 5 * time.Second

// GetClipboardRichText 跨平台获取剪贴板富文本内容。
//
// 返回值：
// - htmlContent：HTML 格式内容（可能为空，表示剪贴板无 HTML）
// - plainText：纯文本内容（HTML 为空时使用）
// - err：获取失败错误
//
// 优先级：HTML > 纯文本；HTML 非空时调用方应用 htmlToMarkdown 转换后注入。
// 对标 VSCode Clipboard.readText（https://code.visualstudio.com/api/references/vscode-api#Clipboard @2026-07-21）。
func GetClipboardRichText() (htmlContent, plainText string, err error) {
	switch runtime.GOOS {
	case "windows":
		return getClipboardRichTextWindows()
	case "darwin":
		return getClipboardRichTextDarwin()
	case "linux":
		return getClipboardRichTextLinux()
	default:
		return "", "", fmt.Errorf("clipboard rich text not supported on %s", runtime.GOOS)
	}
}

// getClipboardRichTextWindows 用 PowerShell 获取 Windows 剪贴板富文本。
//
// 优先 Get-Clipboard -Format HTML 获取 HTML；失败降级 Get-Clipboard 获取纯文本。
// 对标 VSCode Windows 剪贴板实现。
//
// 修复：原命令用 `-and` 运算符，PowerShell 的 -and 始终返回布尔值
// （$true/$false），导致 stdout 永远输出 "True\r\n"/"False\r\n"，而非 HTML 内容。
// "False\r\n" 经 htmlToMarkdown 解析为 "False" 粘贴进输入框（用户反馈 Ctrl+V 显示 False）。
// 改用 if 语句：有 HTML 时输出内容，无 HTML 时输出空。
func getClipboardRichTextWindows() (string, string, error) {
	// 尝试 HTML 格式（用 if 语句确保输出实际内容而非布尔值）
	htmlContent, htmlErr := runClipboardCommand("powershell", "-NoProfile", "-Command",
		"Add-Type -AssemblyName System.Windows.Forms; if ([System.Windows.Forms.Clipboard]::ContainsText([System.Windows.Forms.TextDataFormat]::Html)) { Get-Clipboard -Format HTML }")
	trimmedHTML := strings.TrimSpace(htmlContent)
	// 过滤空值及 PowerShell 布尔兜底（防 -and 旧 bug 回归）
	if htmlErr == nil && trimmedHTML != "" && trimmedHTML != "False" && trimmedHTML != "True" {
		return truncateRichText(trimmedHTML), "", nil
	}
	// 降级纯文本
	plain, plainErr := runClipboardCommand("powershell", "-NoProfile", "-Command", "Get-Clipboard")
	if plainErr != nil {
		return "", "", fmt.Errorf("windows clipboard read failed (html:%v, plain:%v)", htmlErr, plainErr)
	}
	return "", truncateRichText(plain), nil
}

// getClipboardRichTextDarwin 用 pbpaste 获取 macOS 剪贴板富文本。
//
// 优先 pbpaste -P public.html 获取 HTML；失败降级 pbpaste 获取纯文本。
// 对标 VSCode macOS 剪贴板实现。
func getClipboardRichTextDarwin() (string, string, error) {
	// 尝试 HTML 格式
	htmlContent, htmlErr := runClipboardCommand("pbpaste", "-P", "public.html")
	if htmlErr == nil && strings.TrimSpace(htmlContent) != "" {
		return truncateRichText(htmlContent), "", nil
	}
	// 降级纯文本
	plain, plainErr := runClipboardCommand("pbpaste")
	if plainErr != nil {
		return "", "", fmt.Errorf("darwin clipboard read failed (html:%v, plain:%v)", htmlErr, plainErr)
	}
	return "", truncateRichText(plain), nil
}

// getClipboardRichTextLinux 用 wl-paste/xclip 获取 Linux 剪贴板富文本。
//
// 优先 wl-paste -t text/html（Wayland），次选 xclip -t text/html（X11）；
// 失败降级 xclip -o 纯文本。
// 对标 VSCode Linux 剪贴板实现。
func getClipboardRichTextLinux() (string, string, error) {
	// 尝试 wl-paste HTML
	if wlPath, err := exec.LookPath("wl-paste"); err == nil {
		htmlContent, htmlErr := runClipboardCommand(wlPath, "-t", "text/html")
		if htmlErr == nil && strings.TrimSpace(htmlContent) != "" {
			return truncateRichText(htmlContent), "", nil
		}
	}
	// 尝试 xclip HTML
	if xclipPath, err := exec.LookPath("xclip"); err == nil {
		htmlContent, htmlErr := runClipboardCommand(xclipPath, "-selection", "clipboard", "-t", "text/html", "-o")
		if htmlErr == nil && strings.TrimSpace(htmlContent) != "" {
			return truncateRichText(htmlContent), "", nil
		}
		// 降级纯文本
		plain, plainErr := runClipboardCommand(xclipPath, "-selection", "clipboard", "-o")
		if plainErr != nil {
			return "", "", fmt.Errorf("linux clipboard read failed: %v", plainErr)
		}
		return "", truncateRichText(plain), nil
	}
	// wl-paste 纯文本降级
	if wlPath, err := exec.LookPath("wl-paste"); err == nil {
		plain, plainErr := runClipboardCommand(wlPath)
		if plainErr != nil {
			return "", "", fmt.Errorf("linux clipboard read failed: %v", plainErr)
		}
		return "", truncateRichText(plain), nil
	}
	return "", "", fmt.Errorf("no clipboard tool available (requires wl-paste or xclip)")
}

// runClipboardCommand 执行剪贴板命令并返回标准输出字符串。
//
// 复用 escapePSString 防注入（参数化命令调用，无 shell 拼接）。
func runClipboardCommand(name string, args ...string) (string, error) {
	out, err := runClipboardBytesCommand(name, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// runClipboardBytesCommand 使用统一超时执行剪贴板子进程并返回原始字节。
func runClipboardBytesCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("clipboard command timed out after %s", clipboardReadTimeout)
	}
	return out, err
}

// truncateRichText 截断富文本到 maxRichTextSize，防止超长内容撑爆 textarea。
//
// 截断在 UTF-8 rune 边界进行，不产生半个字符。
func truncateRichText(s string) string {
	if len(s) <= maxRichTextSize {
		return s
	}
	// 按 rune 截断
	runes := []rune(s)
	maxRunes := maxRichTextSize
	if len(runes) < maxRunes {
		maxRunes = len(runes)
	}
	return string(runes[:maxRunes]) + "\n[... truncated]"
}
