// Package tui - html_to_md tests ( M10-04)
//
// 验证 HTML→Markdown 白名单转换器。
package tui

import (
	"strings"
	"testing"
)

// TestHTMLToMarkdown_CodeBlock 验证 <pre><code> 转围栏代码块。
func TestHTMLToMarkdown_CodeBlock(t *testing.T) {
	html := `<pre><code>func main() {
    println("hello")
}</code></pre>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "```") {
		t.Errorf("代码块应包含围栏 ```，实际: %q", md)
	}
	if !strings.Contains(md, "func main()") {
		t.Errorf("代码块应保留内容，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Table 验证 <table> 转 Markdown 表格。
func TestHTMLToMarkdown_Table(t *testing.T) {
	html := `<table><tr><th>Name</th><th>Age</th></tr><tr><td>Alice</td><td>30</td></tr></table>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "|") {
		t.Errorf("表格应包含 | 分隔符，实际: %q", md)
	}
	if !strings.Contains(md, "---") {
		t.Errorf("表格应包含分隔行 ---，实际: %q", md)
	}
	if !strings.Contains(md, "Name") || !strings.Contains(md, "Alice") {
		t.Errorf("表格应保留内容，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Link 验证 <a href> 转 [text](url)。
func TestHTMLToMarkdown_Link(t *testing.T) {
	html := `<a href="https://example.com">Example</a>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "[Example](https://example.com)") {
		t.Errorf("链接应转为 [text](url)，实际: %q", md)
	}
}

// TestHTMLToMarkdown_UnorderedList 验证 <ul><li> 转 - item。
func TestHTMLToMarkdown_UnorderedList(t *testing.T) {
	html := `<ul><li>apple</li><li>banana</li></ul>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "- apple") {
		t.Errorf("无序列表应转为 - item，实际: %q", md)
	}
	if !strings.Contains(md, "- banana") {
		t.Errorf("无序列表应转为 - item，实际: %q", md)
	}
}

// TestHTMLToMarkdown_OrderedList 验证 <ol><li> 转 1. item。
func TestHTMLToMarkdown_OrderedList(t *testing.T) {
	html := `<ol><li>first</li><li>second</li></ol>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "1. first") {
		t.Errorf("有序列表应转为 1. item，实际: %q", md)
	}
	if !strings.Contains(md, "2. second") {
		t.Errorf("有序列表应转为 2. item，实际: %q", md)
	}
}

// TestHTMLToMarkdown_BoldItalic 验证 <b>/<i> 转 **bold**/*italic*。
func TestHTMLToMarkdown_BoldItalic(t *testing.T) {
	html := `<b>bold</b> <i>italic</i>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "**bold**") {
		t.Errorf("<b> 应转为 **bold**，实际: %q", md)
	}
	if !strings.Contains(md, "*italic*") {
		t.Errorf("<i> 应转为 *italic*，实际: %q", md)
	}
}

// TestHTMLToMarkdown_StrongEm 验证 <strong>/<em> 转 **bold**/*italic*。
func TestHTMLToMarkdown_StrongEm(t *testing.T) {
	html := `<strong>strong</strong> <em>em</em>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "**strong**") {
		t.Errorf("<strong> 应转为 **strong**，实际: %q", md)
	}
	if !strings.Contains(md, "*em*") {
		t.Errorf("<em> 应转为 *em*，实际: %q", md)
	}
}

// TestHTMLToMarkdown_UnknownTagStripped 验证未知标签剥离保留文本。
//
// XSS 防护：<script> 标签剥离，仅保留文本内容。
func TestHTMLToMarkdown_UnknownTagStripped(t *testing.T) {
	html := `<script>alert(1)</script>`
	md := htmlToMarkdown(html)
	if strings.Contains(md, "<script>") {
		t.Errorf("<script> 应被剥离，实际: %q", md)
	}
	if !strings.Contains(md, "alert(1)") {
		t.Errorf("文本内容应保留，实际: %q", md)
	}
}

// TestHTMLToMarkdown_EntityUnescape 验证 HTML 实体转义。
func TestHTMLToMarkdown_EntityUnescape(t *testing.T) {
	html := `<p>text &amp; more &lt;tag&gt;</p>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "text & more") {
		t.Errorf("实体应转义，实际: %q", md)
	}
	if strings.Contains(md, "&amp;") {
		t.Errorf("&amp; 应转为 &，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Br 验证 <br> 转换行。
func TestHTMLToMarkdown_Br(t *testing.T) {
	html := `line1<br>line2`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "line1\nline2") {
		t.Errorf("<br> 应转为换行，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Heading 验证 <h1>-<h6> 转 Markdown 标题。
func TestHTMLToMarkdown_Heading(t *testing.T) {
	tests := []struct {
		html     string
		expected string
	}{
		{"<h1>Title 1</h1>", "# Title 1"},
		{"<h2>Title 2</h2>", "## Title 2"},
		{"<h3>Title 3</h3>", "### Title 3"},
	}
	for _, tt := range tests {
		md := htmlToMarkdown(tt.html)
		if !strings.Contains(md, tt.expected) {
			t.Errorf("标题应转为 %q，实际: %q", tt.expected, md)
		}
	}
}

// TestHTMLToMarkdown_NestedList 验证嵌套列表缩进。
func TestHTMLToMarkdown_NestedList(t *testing.T) {
	html := `<ul><li>top<ul><li>nested</li></ul></li></ul>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "- top") {
		t.Errorf("顶层列表应转为 - top，实际: %q", md)
	}
	if !strings.Contains(md, "- nested") {
		t.Errorf("嵌套列表应转为 - nested，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Blockquote 验证 <blockquote> 转 > 引用。
func TestHTMLToMarkdown_Blockquote(t *testing.T) {
	html := `<blockquote>quoted text</blockquote>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "> quoted text") {
		t.Errorf("<blockquote> 应转为 > 引用，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Hr 验证 <hr> 转 ---。
func TestHTMLToMarkdown_Hr(t *testing.T) {
	html := `<p>before</p><hr><p>after</p>`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "---") {
		t.Errorf("<hr> 应转为 ---，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Empty 验证空 HTML 返回空字符串。
func TestHTMLToMarkdown_Empty(t *testing.T) {
	md := htmlToMarkdown("")
	if md != "" {
		t.Errorf("空 HTML 应返回空字符串，实际: %q", md)
	}
}

// TestHTMLToMarkdown_Img 验证 <img> 转 ![alt](src)。
func TestHTMLToMarkdown_Img(t *testing.T) {
	html := `<img src="pic.png" alt="Picture">`
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "![Picture](pic.png)") {
		t.Errorf("<img> 应转为 ![alt](src)，实际: %q", md)
	}
}
