// Package tui - HTML to Markdown converter ( M10-04)
//
// 富文本/HTML 粘贴支持：从剪贴板读取 HTML 内容，转换为 Markdown 注入 textarea。
// 对标 VSCode Clipboard.readText 富文本粘贴 @官方文档 2026-07-21。
//
// 转换策略：白名单标签 + 未知标签剥离（保留文本），防止 XSS 注入。
// 用 golang.org/x/net/html 完整 DOM 解析（纯 Go，CGO_ENABLED=0）。
package tui

import (
	"fmt"
	htmlstdlib "html"
	"strings"

	xhtml "golang.org/x/net/html"
)

// htmlToMarkdown 将 HTML 字符串转换为 Markdown。
//
// 白名单标签：pre/code/table/thead/tbody/tr/td/th/a/ul/ol/li/b/i/em/strong/br/p/h1-h6。
// 未知标签（如 script/iframe）剥离标签保留文本内容。
// 对标 turndown.js HTML→Markdown 算法（白名单 + 标签剥离）。
//
// 输入超 100KB 时降级返回纯文本（防 OOM）。
func htmlToMarkdown(htmlStr string) string {
	if len(htmlStr) > 100*1024 {
		return stripAllTags(htmlStr)
	}
	doc, err := xhtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		// 解析失败降级为纯文本
		return stripAllTags(htmlStr)
	}
	var sb strings.Builder
	renderNode(&sb, doc, &convCtx{inPre: false, listStack: []rune{}, olCounter: []int{}})
	return strings.TrimSpace(sb.String())
}

// convCtx 转换上下文，跟踪嵌套状态。
type convCtx struct {
	inPre     bool   // 是否在 <pre> 内（保留原始换行）
	listStack []rune // 列表类型栈（'u'=ul, 'o'=ol）
	olCounter []int  // ol 计数器栈（每层一个计数器）
	indent    int    // 当前列表缩进层级
}

// renderNode 递归渲染 DOM 节点到 Markdown。
//
// 白名单标签按规则转换；未知标签递归子节点（剥离标签保留文本）。
// 文本节点在 <pre> 内保留原样，否则折叠空白。
func renderNode(sb *strings.Builder, n *xhtml.Node, ctx *convCtx) {
	if n == nil {
		return
	}
	switch n.Type {
	case xhtml.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(sb, c, ctx)
		}
	case xhtml.TextNode:
		text := htmlstdlib.UnescapeString(n.Data)
		if ctx.inPre {
			sb.WriteString(text)
		} else {
			// 折叠空白（保留单个空格）
			text = strings.ReplaceAll(text, "\n", " ")
			text = strings.ReplaceAll(text, "\r", "")
			text = strings.ReplaceAll(text, "\t", " ")
			// 多个空格折叠为 1
			for strings.Contains(text, "  ") {
				text = strings.ReplaceAll(text, "  ", " ")
			}
			sb.WriteString(text)
		}
	case xhtml.ElementNode:
		renderElement(sb, n, ctx)
		// CommentNode / DoctypeNode 等忽略
	}
}

// renderElement 按白名单标签规则渲染元素节点。
//
// 白名单外标签（script/iframe 等）递归子节点（剥离标签保留文本）。
func renderElement(sb *strings.Builder, n *xhtml.Node, ctx *convCtx) {
	switch n.Data {
	case "pre":
		sb.WriteString("\n```\n")
		oldInPre := ctx.inPre
		ctx.inPre = true
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(sb, c, ctx)
		}
		ctx.inPre = oldInPre
		sb.WriteString("\n```\n")
	case "code":
		// <code> 在 <pre> 内时不重复围栏（pre 已处理）
		if !ctx.inPre {
			sb.WriteString("`")
			renderChildren(sb, n, ctx)
			sb.WriteString("`")
		} else {
			renderChildren(sb, n, ctx)
		}
	case "b", "strong":
		sb.WriteString("**")
		renderChildren(sb, n, ctx)
		sb.WriteString("**")
	case "i", "em":
		sb.WriteString("*")
		renderChildren(sb, n, ctx)
		sb.WriteString("*")
	case "br":
		sb.WriteString("\n")
	case "p":
		sb.WriteString("\n\n")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "h1":
		sb.WriteString("\n# ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "h2":
		sb.WriteString("\n## ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "h3":
		sb.WriteString("\n### ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "h4":
		sb.WriteString("\n#### ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "h5":
		sb.WriteString("\n##### ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "h6":
		sb.WriteString("\n###### ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "ul":
		ctx.listStack = append(ctx.listStack, 'u')
		ctx.olCounter = append(ctx.olCounter, 0)
		ctx.indent++
		renderChildren(sb, n, ctx)
		ctx.listStack = ctx.listStack[:len(ctx.listStack)-1]
		ctx.olCounter = ctx.olCounter[:len(ctx.olCounter)-1]
		ctx.indent--
		sb.WriteString("\n")
	case "ol":
		ctx.listStack = append(ctx.listStack, 'o')
		ctx.olCounter = append(ctx.olCounter, 0)
		ctx.indent++
		renderChildren(sb, n, ctx)
		ctx.listStack = ctx.listStack[:len(ctx.listStack)-1]
		ctx.olCounter = ctx.olCounter[:len(ctx.olCounter)-1]
		ctx.indent--
		sb.WriteString("\n")
	case "li":
		indent := strings.Repeat("  ", ctx.indent-1)
		if len(ctx.listStack) > 0 && ctx.listStack[len(ctx.listStack)-1] == 'o' {
			ctx.olCounter[len(ctx.olCounter)-1]++
			sb.WriteString(fmt.Sprintf("\n%s%d. ", indent, ctx.olCounter[len(ctx.olCounter)-1]))
		} else {
			sb.WriteString("\n" + indent + "- ")
		}
		renderChildren(sb, n, ctx)
	case "a":
		href := getAttr(n, "href")
		text := getNodeText(n)
		if href != "" && text != "" {
			sb.WriteString(fmt.Sprintf("[%s](%s)", text, href))
		} else if text != "" {
			sb.WriteString(text)
		}
	case "table":
		renderTable(sb, n, ctx)
	case "thead", "tbody", "tfoot":
		renderChildren(sb, n, ctx)
	case "tr":
		// tr 由 renderTable 统一处理，单独出现时降级
		renderChildren(sb, n, ctx)
	case "td", "th":
		// td/th 由 renderTable 统一处理，单独出现时降级
		renderChildren(sb, n, ctx)
	case "blockquote":
		sb.WriteString("\n> ")
		renderChildren(sb, n, ctx)
		sb.WriteString("\n")
	case "hr":
		sb.WriteString("\n---\n")
	case "img":
		alt := getAttr(n, "alt")
		src := getAttr(n, "src")
		if src != "" {
			sb.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
		}
	default:
		// 未知标签：剥离标签，递归子节点保留文本
		renderChildren(sb, n, ctx)
	}
}

// renderChildren 渲染所有子节点。
func renderChildren(sb *strings.Builder, n *xhtml.Node, ctx *convCtx) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(sb, c, ctx)
	}
}

// renderTable 渲染 HTML 表格为 Markdown 表格。
//
// 提取所有 tr 行，每行 td/th 作为单元格；首行作为表头，自动生成分隔行。
func renderTable(sb *strings.Builder, n *xhtml.Node, ctx *convCtx) {
	var rows [][]string
	collectRows(n, &rows)
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return
	}
	sb.WriteString("\n")
	// 表头行
	sb.WriteString("| ")
	for i := 0; i < cols; i++ {
		if i > 0 {
			sb.WriteString(" | ")
		}
		if i < len(rows[0]) {
			sb.WriteString(strings.TrimSpace(rows[0][i]))
		}
	}
	sb.WriteString(" |\n")
	// 分隔行
	sb.WriteString("|")
	for i := 0; i < cols; i++ {
		sb.WriteString(" --- |")
	}
	sb.WriteString("\n")
	// 数据行
	for r := 1; r < len(rows); r++ {
		sb.WriteString("| ")
		for i := 0; i < cols; i++ {
			if i > 0 {
				sb.WriteString(" | ")
			}
			if i < len(rows[r]) {
				sb.WriteString(strings.TrimSpace(rows[r][i]))
			}
		}
		sb.WriteString(" |\n")
	}
	sb.WriteString("\n")
}

// collectRows 递归收集表格中的所有行（tr）。
//
// 每个 tr 内的 td/th 文本作为单元格；thead/tbody 嵌套也兼容。
func collectRows(n *xhtml.Node, rows *[][]string) {
	if n == nil {
		return
	}
	if n.Type == xhtml.ElementNode && n.Data == "tr" {
		var row []string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.ElementNode && (c.Data == "td" || c.Data == "th") {
				row = append(row, getNodeText(c))
			}
		}
		if len(row) > 0 {
			*rows = append(*rows, row)
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectRows(c, rows)
	}
}

// getAttr 获取节点的指定属性值。
func getAttr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// getNodeText 获取节点及其子节点的纯文本（递归）。
func getNodeText(n *xhtml.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == xhtml.TextNode {
		return htmlstdlib.UnescapeString(n.Data)
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(getNodeText(c))
	}
	return sb.String()
}

// stripAllTags 剥离所有 HTML 标签，仅保留文本（超长 HTML 降级路径）。
//
// 用 html.Tokenizer 遍历，仅收集 TextToken，UnescapeString 后拼接。
func stripAllTags(htmlStr string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(htmlStr))
	var sb strings.Builder
	for {
		tt := tokenizer.Next()
		switch tt {
		case xhtml.ErrorToken:
			return sb.String()
		case xhtml.TextToken:
			sb.WriteString(htmlstdlib.UnescapeString(string(tokenizer.Text())))
		}
	}
}
