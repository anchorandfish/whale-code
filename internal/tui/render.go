package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// FileLink represents a file reference extracted from text
type FileLink struct {
	Path      string
	Line      int
	Col       int
	LineStart int
	LineEnd   int
	Text      string
}

// MarkdownRenderer wraps glamour renderer for terminal Markdown rendering.
//
// 修正：glamour.TermRenderer 内部 BlockStack 在 Render 时被读写，
// 并非不可变；故 MarkdownRenderer 内置 sync.Mutex 串行化 Render 调用，
// 防止 PrefetchBuffer 异步预渲染路径与主题切换竞态。
type MarkdownRenderer struct {
	renderer *glamour.TermRenderer
	theme    string
	// width 是该渲染器构造时的 glamour WordWrap 宽度（0 = 不折行，旧口径）。
	// glamour 不支持热改宽度（宽度写进内部 BlockStack 状态）⇒ 宽度变化必须重建渲染器。
	width int
	mu    sync.Mutex // 修正：串行化 Render，glamour 内部状态非不可变
}

// NewMarkdownRenderer creates a Markdown renderer with the given theme.
//
// WordWrap = 0（旧口径：折行交给 TUI 的 Hardwrap）。需要把真实终端宽度喂给 glamour
// （表格/段落按宽度排版）时用 NewMarkdownRendererWidth。
func NewMarkdownRenderer(theme string) (*MarkdownRenderer, error) {
	return NewMarkdownRendererWidth(theme, 0)
}

// NewMarkdownRendererWidth 构造带真实折行宽度的 Markdown 渲染器（修法 A）。
//
// 为什么必须带宽度：WordWrap=0 时 glamour 把表格按**内容自然宽度**铺开
// （`ansi/table.go` 的 `table.New().Width(0).Wrap(true)`，而 `MaxWidth(0)` 在
// lipgloss 里是 no-op），超宽处只能由 TUI 唯一折行点 `ansi.Hardwrap` **拦腰硬切**
// ⇒ 三列表格的列分隔符错位（用户报障：终端拉宽才恢复正常）。
// 喂真实宽度后，表格在渲染期内收缩列宽、单元格内折行 —— 窄终端变高但**不错位**。
//
// ★ 不动 `WrapText`（已拍板：全仓唯一折行点不改）。
func NewMarkdownRendererWidth(theme string, width int) (*MarkdownRenderer, error) {
	if theme == "" {
		theme = "dark"
	}
	if width < 0 {
		width = 0
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(theme),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, fmt.Errorf("create glamour renderer: %w", err)
	}

	return &MarkdownRenderer{renderer: r, theme: theme, width: width}, nil
}

// widthRendererCache 按宽度缓存渲染器（主题恒为 defaultRenderer 的 "dark"）。
//
// 缓存而非每次新建：正文渲染在消息收尾与流式帧的**热路径**上，每帧新建 glamour
// renderer 代价过高。键只需宽度 —— 宽度是唯一会让 glamour 内部状态不同的输入
// （主题在本仓消息渲染路径上恒为 dark：renderTextContent 走 defaultRenderer，
// /theme 只更新 m.mdRenderer 快照，不参与正文渲染，此为既有行为）。
var (
	widthRendererMu    sync.Mutex
	widthRendererCache = map[int]*MarkdownRenderer{}
)

// rendererForWidth 返回给定内容折行宽度的 glamour 渲染器。
//
// width <= 0 ⇒ 返回包级 defaultRenderer（旧行为兜底，语义与改造前完全一致）。
// 构造失败同样回退 defaultRenderer（fail-soft：渲染不因宽度改造而中断）。
func rendererForWidth(width int) *MarkdownRenderer {
	if width <= 0 {
		return defaultRenderer
	}
	widthRendererMu.Lock()
	defer widthRendererMu.Unlock()
	if r, ok := widthRendererCache[width]; ok {
		return r
	}
	r, err := NewMarkdownRendererWidth("dark", width)
	if err != nil {
		return defaultRenderer
	}
	widthRendererCache[width] = r
	return r
}

// Render renders Markdown text to ANSI-styled terminal output.
//
// 修正：持 mu 锁串行化 glamour.TermRenderer.Render 调用，
// 防止 BlockStack 并发读写导致 panic（index out of range）。
func (r *MarkdownRenderer) Render(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.renderer.Render(text)
	if err != nil {
		return text, fmt.Errorf("glamour render: %w", err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// defaultRenderer is the package-level default Markdown renderer
var defaultRenderer *MarkdownRenderer

func init() {
	r, err := NewMarkdownRenderer("dark")
	if err == nil {
		defaultRenderer = r
	}
}

// RenderMarkdown renders Markdown text using the default dark theme renderer
func RenderMarkdown(text string) string {
	if defaultRenderer == nil {
		return text
	}
	out, err := defaultRenderer.Render(text)
	if err != nil {
		return text
	}
	return out
}

// ---------------------------------------------------------------------------
// （M10-05）：主题热替换 ThemeManager
// 对标 VSCode workbench.colorTheme 即时切换 @官方文档 2026-07-21
// ---------------------------------------------------------------------------

// themeManager 主题管理器，持有当前主题 + 读写锁保护的 MarkdownRenderer。
//
// 关键设计（修正后）：
// - RWMutex 保护 renderer 指针 swap（SetTheme 写 / Renderer 读）。
// - MarkdownRenderer 自身内置 Mutex 串行化 Render 调用（glamour 内部
// BlockStack 在 Render 时被读写，非不可变）。
// - 二层锁分离：themeManager.RWMutex 管指针 swap，MarkdownRenderer.mu
// 管 Render 调用串行化；二者正交，不会嵌套死锁。
// - 对标 VSCode workbench.colorTheme 即时切换（无需重启）。
type themeManager struct {
	mu       sync.RWMutex
	current  string            // 当前主题名（"dark"/"light"/"notty"/"pink"）
	renderer *MarkdownRenderer // 当前 renderer 快照
}

// newThemeManager 创建主题管理器，用 initialTheme 初始化 renderer。
//
// initialTheme 非法时降级 "dark"（与既有 init() 行为一致）。
func newThemeManager(initialTheme string) *themeManager {
	if initialTheme == "" {
		initialTheme = "dark"
	}
	// "mono" 映射到 "notty"（无样式）
	if initialTheme == "mono" {
		initialTheme = "notty"
	}
	r, err := NewMarkdownRenderer(initialTheme)
	if err != nil {
		// 降级 dark
		r, err = NewMarkdownRenderer("dark")
		if err != nil {
			r = nil // 极端情况：dark 也失败，renderer 为 nil
		}
		initialTheme = "dark"
	}
	return &themeManager{
		current:  initialTheme,
		renderer: r,
	}
}

// SetTheme 切换主题并重建 renderer（持写锁）。
//
// 主题合法性：glamour.WithStandardStyle 仅接受 "dark"/"light"/"notty"/"pink"；
// "mono" 自动映射到 "notty"（无样式）。
// 重建失败时保留旧 renderer（不替换），返回 error。
func (t *themeManager) SetTheme(theme string) error {
	if theme == "" {
		return fmt.Errorf("empty theme")
	}
	// "mono" 映射到 "notty"
	normalized := theme
	if normalized == "mono" {
		normalized = "notty"
	}
	r, err := NewMarkdownRenderer(normalized)
	if err != nil {
		return fmt.Errorf("create renderer for theme %q: %w", theme, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current = normalized
	t.renderer = r
	return nil
}

// Renderer 返回当前 renderer 快照（持读锁）。
//
// 调用方拿到 *MarkdownRenderer 后调用其 Render 方法时，由 MarkdownRenderer.mu
// 串行化（修正）。themeManager.RWMutex 仅保护指针 swap。
func (t *themeManager) Renderer() *MarkdownRenderer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.renderer
}

// Current 返回当前主题名（持读锁）。
func (t *themeManager) Current() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current
}

// ---------------------------------------------------------------------------
// File link extraction
// ---------------------------------------------------------------------------

var (
	// markdownLinkRe matches [text](file:///path#L10) or [text](file:///path#L10-L20)
	markdownLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(file:///([^)#]+)(?:#L(\d+)(?:-L(\d+))?)?\)`)

	// fileURIRe matches file:///path#L10 or file:///path#L10-L20
	fileURIRe = regexp.MustCompile(`file:///([^#\s]+)(?:#L(\d+)(?:-L(\d+))?)?`)

	// pathLineColRe matches path:line:col (compiler error format)
	pathLineColRe = regexp.MustCompile(`(?m)(?:^|[\s(])([\w./\\-]+\.go):(\d+):(\d+)`)

	// pathLineRe matches path:line (common error format)
	pathLineRe = regexp.MustCompile(`(?m)(?:^|[\s(])([\w./\\-]+\.go):(\d+)`)

	// windowsPathLineRe matches C:\path\file.go:line
	windowsPathLineRe = regexp.MustCompile(`(?m)([A-Za-z]:[\\][\w\\.-]+\.go):(\d+)`)

	// pathLineSimpleRe matches path:line or path:line:col (T-10: promoted to package-level)
	pathLineSimpleRe = regexp.MustCompile(`([\w./\\-]+\.go):(\d+)(?::(\d+))?`)
)

// ExtractFileLinks extracts all file references from text
func ExtractFileLinks(text string) []FileLink {
	var links []FileLink
	seen := make(map[string]bool)
	seenPathLine := make(map[string]bool)

	addLink := func(l FileLink) {
		key := fmt.Sprintf("%s:%d:%d", l.Path, l.Line, l.Col)
		if !seen[key] {
			seen[key] = true
			links = append(links, l)
		}
		if l.Line > 0 {
			pathLineKey := fmt.Sprintf("%s:%d", l.Path, l.Line)
			seenPathLine[pathLineKey] = true
		}
	}

	for _, m := range markdownLinkRe.FindAllStringSubmatch(text, -1) {
		decoded, err := url.PathUnescape(m[2])
		if err != nil {
			decoded = m[2]
		}
		link := FileLink{
			Text: m[1],
			Path: decoded,
		}
		if m[3] != "" {
			if _, err := fmt.Sscanf(m[3], "%d", &link.LineStart); err != nil {
				link.LineStart = 0
			}
			link.Line = link.LineStart
		}
		if m[4] != "" {
			if _, err := fmt.Sscanf(m[4], "%d", &link.LineEnd); err != nil {
				link.LineEnd = 0
			}
		}
		if link.Text == "" {
			link.Text = filepath.Base(link.Path)
		}
		addLink(link)
	}

	for _, m := range fileURIRe.FindAllStringSubmatch(text, -1) {
		if strings.Contains(m[0], "[") {
			continue
		}
		decoded, err := url.PathUnescape(m[1])
		if err != nil {
			decoded = m[1]
		}
		link := FileLink{Path: decoded}
		if m[2] != "" {
			if _, err := fmt.Sscanf(m[2], "%d", &link.LineStart); err != nil {
				link.LineStart = 0
			}
			link.Line = link.LineStart
		}
		if m[3] != "" {
			if _, err := fmt.Sscanf(m[3], "%d", &link.LineEnd); err != nil {
				link.LineEnd = 0
			}
		}
		link.Text = formatFileLinkText(link)
		addLink(link)
	}

	for _, m := range pathLineColRe.FindAllStringSubmatch(text, -1) {
		line := 0
		col := 0
		if _, err := fmt.Sscanf(m[2], "%d", &line); err != nil {
			line = 0
		}
		if _, err := fmt.Sscanf(m[3], "%d", &col); err != nil {
			col = 0
		}
		link := FileLink{Path: m[1], Line: line, Col: col}
		link.Text = formatFileLinkText(link)
		addLink(link)
	}

	for _, m := range pathLineRe.FindAllStringSubmatch(text, -1) {
		line := 0
		if _, err := fmt.Sscanf(m[2], "%d", &line); err != nil {
			line = 0
		}
		pathLineKey := fmt.Sprintf("%s:%d", m[1], line)
		if seenPathLine[pathLineKey] {
			continue
		}
		link := FileLink{Path: m[1], Line: line}
		link.Text = formatFileLinkText(link)
		addLink(link)
	}

	for _, m := range windowsPathLineRe.FindAllStringSubmatch(text, -1) {
		line := 0
		if _, err := fmt.Sscanf(m[2], "%d", &line); err != nil {
			line = 0
		}
		link := FileLink{Path: m[1], Line: line}
		link.Text = formatFileLinkText(link)
		addLink(link)
	}

	return links
}

// formatFileLinkText generates display text for a FileLink
func formatFileLinkText(l FileLink) string {
	base := filepath.Base(l.Path)
	if l.LineEnd > 0 && l.LineStart > 0 {
		return fmt.Sprintf("%s:%d-%d", base, l.LineStart, l.LineEnd)
	}
	if l.Line > 0 {
		return fmt.Sprintf("%s:%d", base, l.Line)
	}
	return base
}

// ---------------------------------------------------------------------------
// OSC 8 hyperlink rendering
// ---------------------------------------------------------------------------

// escapeFilePath URL-encodes each path segment while preserving separators
func escapeFilePath(path string) string {
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// osc8Link 是 OSC 8 超链接的**唯一发射点**（⑧ 收口）。
//
// 任何"把某段文本变成可点击链接"的地方都必须走这里，否则就是第二份实现
// （同源点分裂：某处的转义写法漂移后，只有那条路径点击失效）。
// OSC 8 是零宽序列，ansi.StringWidth 会忽略它 ⇒ 不参与任何宽度计算。
func osc8Link(uri, display string) string {
	return "\x1b]8;;" + uri + "\x1b\\" + display + "\x1b]8;;\x1b\\"
}

// FileLinkURI 返回 FileLink 的 OSC 8 目标 URI（file:// + 逐段转义 + 可选行锚点）。
//
// ★ 2026-10-16 修正（⑧ 过程中发现）：绝对路径此前产出 `file:////var/x`
// （**四个**斜杠）—— `"file:///" + "/var/x"` 把路径自带的前导斜杠又叠了一次。
// RFC 8089 里本地文件 URL 的 host 必须为空 ⇒ 规范形态恰为 `file:///var/x`；
// `file:////var/x` 解析出的 path 是 `//var/x`（实现相关的双斜杠语义），
// 交给系统打开器是"能不能开看运气"。修正方式对**相对路径与 Windows 盘符路径保持原样**
// （它们本就没有前导斜杠），只收敛绝对 POSIX 路径这一种。
func FileLinkURI(link FileLink) string {
	p := escapeFilePath(link.Path)
	if strings.HasPrefix(p, "/") {
		p = p[1:] // 去掉重复的前导斜杠，避免 file:////…
	}
	uri := "file:///" + p
	if link.Line > 0 {
		uri += fmt.Sprintf("#L%d", link.Line)
	} else if link.LineStart > 0 {
		uri += fmt.Sprintf("#L%d", link.LineStart)
		if link.LineEnd > 0 {
			uri += fmt.Sprintf("-L%d", link.LineEnd)
		}
	}
	return uri
}

// RenderFileLink renders a FileLink as an OSC 8 terminal hyperlink
func RenderFileLink(link FileLink) string {
	return osc8Link(FileLinkURI(link), FormatFileLinkChoice(link))
}

// replaceEntry records a pending string replacement by position
type replaceEntry struct {
	start int
	end   int
	text  string
}

// RenderTextWithLinks replaces file references in text with OSC 8 hyperlinks
func RenderTextWithLinks(text string) string {
	links := ExtractFileLinks(text)
	if len(links) == 0 {
		return text
	}

	var replacements []replaceEntry

	for _, idx := range markdownLinkRe.FindAllStringSubmatchIndex(text, -1) {
		fullMatch := text[idx[0]:idx[1]]
		groups := markdownLinkRe.FindStringSubmatch(fullMatch)
		if len(groups) < 3 {
			continue
		}
		rendered := RenderFileLink(FileLink{
			Path: groups[2],
			Text: groups[1],
		})
		replacements = append(replacements, replaceEntry{start: idx[0], end: idx[1], text: rendered})
	}

	for _, idx := range pathLineSimpleRe.FindAllStringSubmatchIndex(text, -1) {
		groups := pathLineSimpleRe.FindStringSubmatch(text[idx[0]:idx[1]])
		if len(groups) < 3 {
			continue
		}
		line := 0
		if _, err := fmt.Sscanf(groups[2], "%d", &line); err != nil {
			line = 0
		}
		link := FileLink{Path: groups[1], Line: line}
		if len(groups) > 3 && groups[3] != "" {
			col := 0
			if _, err := fmt.Sscanf(groups[3], "%d", &col); err != nil {
				col = 0
			}
			link.Col = col
		}
		rendered := RenderFileLink(link)
		replacements = append(replacements, replaceEntry{start: idx[0], end: idx[1], text: rendered})
	}

	if len(replacements) == 0 {
		return text
	}

	// Sort replacements by start position descending to apply from end to start
	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start > replacements[j].start
	})

	result := text
	for _, r := range replacements {
		result = result[:r.start] + r.text + result[r.end:]
	}

	return result
}

// ---------------------------------------------------------------------------
// Keyboard-driven document navigation
// ---------------------------------------------------------------------------

// ExtractFileLinksFromMessages extracts file links from a list of rendered messages
func ExtractFileLinksFromMessages(messages []string) []FileLink {
	var allLinks []FileLink
	for _, msg := range messages {
		links := ExtractFileLinks(msg)
		allLinks = append(allLinks, links...)
	}
	return allLinks
}

// FormatFileLinkChoice formats a FileLink as a choice item for the navigation UI
func FormatFileLinkChoice(link FileLink) string {
	if link.LineEnd > 0 && link.LineStart > 0 {
		return fmt.Sprintf("%s:%d-%d", link.Path, link.LineStart, link.LineEnd)
	}
	if link.Line > 0 {
		return fmt.Sprintf("%s:%d", link.Path, link.Line)
	}
	return link.Path
}

// allowedEditors defines the set of editor executables that are safe to invoke
var allowedEditors = map[string]bool{
	"code": true, "vim": true, "nvim": true, "vi": true,
	"nano": true, "emacs": true, "subl": true, "atom": true,
	"gedit": true, "kate": true, "notepad": true, "notepad++": true,
	"cursor": true, "windsurf": true,
}

// sanitizeEditor validates the editor name against an allowlist
// returns the default editor if the provided name is not in the allowlist
func sanitizeEditor(editor string) string {
	if editor == "" {
		if runtime.GOOS == "windows" {
			return "code"
		}
		return "vim"
	}
	base := filepath.Base(editor)
	if allowedEditors[base] {
		return editor
	}
	if runtime.GOOS == "windows" {
		return "code"
	}
	return "vim"
}

// BuildEditorCommand constructs an exec.Cmd for opening a file in the editor
func BuildEditorCommand(editor string, path string, line int) *exec.Cmd {
	editor = sanitizeEditor(editor)

	var args []string
	if runtime.GOOS == "windows" || editor == "code" {
		args = []string{fmt.Sprintf("%s:%d", path, line)}
	} else {
		args = []string{fmt.Sprintf("+%d", line), path}
	}

	return exec.Command(editor, args...)
}

// OpenInEditor opens a file in the system editor at the given line number
func OpenInEditor(path string, line int) error {
	editor := os.Getenv("EDITOR")
	cmd := BuildEditorCommand(editor, path, line)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ---------------------------------------------------------------------------
// N13 L0 引擎辅助函数（RenderCache / VirtualScroll 调用）
// ---------------------------------------------------------------------------
//
// 以下三个函数为 N13 L0 引擎提供渲染 / 内容寻址 / 行数统计能力。
// 关键约束：
// - 不破坏 render.go 现有功能（Markdown 渲染与文件链接处理）。
// - SHA-256 与 N4 一致（碰撞概率 2^128 可忽略）。

// renderMessageWithGlamour 用 glamour 渲染单条消息（供 RenderCache 调用）。
//
// 复用包级 defaultRenderer（dark 主题）；渲染失败时返回原始消息（不 panic）。
func renderMessageWithGlamour(msg string) string {
	if msg == "" {
		return ""
	}
	return RenderMarkdown(msg)
}

// hashMessage 计算消息内容的 SHA-256 hash（用作 RenderCache key）。
//
// 与 N4 SHA-256 一致，碰撞概率 2^128 可忽略。
func hashMessage(msg string) string {
	sum := sha256.Sum256([]byte(msg))
	return hex.EncodeToString(sum[:])
}

// countLines 统计字符串行数（按 \n 分割，空串返回 0）。
//
// 用于 VirtualScroll.MessageHeight 计算消息渲染后的行数。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	// strings.Count 计算分隔符出现次数；行数 = 分隔符数 + 1（除非末尾刚好以 \n 结尾）。
	// 为兼容末尾换行场景，按 strings.Split 后过滤空尾元素的方式计算。
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
