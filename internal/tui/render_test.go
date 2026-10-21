package tui

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 功能 1：MarkdownRenderer
// ---------------------------------------------------------------------------

func TestNewMarkdownRenderer(t *testing.T) {
	tests := []struct {
		name    string
		theme   string
		wantErr bool
	}{
		{"dark theme", "dark", false},
		{"light theme", "light", false},
		{"empty theme falls back to dark", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewMarkdownRenderer(tt.theme)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewMarkdownRenderer(%q) error = %v, wantErr %v", tt.theme, err, tt.wantErr)
			}
			if r == nil {
				t.Fatal("expected non-nil renderer")
			}
		})
	}
}

func TestMarkdownRenderer_Render(t *testing.T) {
	r, err := NewMarkdownRenderer("dark")
	if err != nil {
		t.Fatalf("failed to create renderer: %v", err)
	}

	stripANSI := func(s string) string {
		re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
		return re.ReplaceAllString(s, "")
	}

	tests := []struct {
		name        string
		input       string
		contains    []string
		notContains []string
	}{
		{
			name:     "empty string",
			input:    "",
			contains: []string{},
		},
		{
			name:     "plain text",
			input:    "hello world",
			contains: []string{"hello world"},
		},
		{
			name:        "h2 heading",
			input:       "## Title",
			contains:    []string{"Title"},
			notContains: []string{},
		},
		{
			name:     "inline code",
			input:    "`code`",
			contains: []string{"code"},
		},
		{
			name:     "code block",
			input:    "```go\nfmt.Println()\n```",
			contains: []string{"fmt.Println()"},
		},
		{
			name:     "unordered list",
			input:    "- item1\n- item2",
			contains: []string{"item1", "item2"},
		},
		{
			name:     "bold text",
			input:    "**bold**",
			contains: []string{"bold"},
		},
		{
			name:     "link",
			input:    "[text](https://example.com)",
			contains: []string{"text"},
		},
		{
			name:     "mixed content",
			input:    "## Title\n\nparagraph\n\n- item",
			contains: []string{"Title", "paragraph", "item"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Render(tt.input)
			if err != nil {
				t.Fatalf("Render(%q) error = %v", tt.input, err)
			}

			plain := stripANSI(got)
			for _, s := range tt.contains {
				if !strings.Contains(plain, s) {
					t.Errorf("Render(%q) output missing %q\nANSI-stripped: %q", tt.input, s, truncateStr(plain, 200))
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(plain, s) {
					t.Errorf("Render(%q) output should not contain %q\nANSI-stripped: %q", tt.input, s, truncateStr(plain, 200))
				}
			}
		})
	}
}

func TestMarkdownRenderer_RenderLongText(t *testing.T) {
	r, _ := NewMarkdownRenderer("dark")
	longText := strings.Repeat("a", 5000)
	got, err := r.Render(longText)
	if err != nil {
		t.Fatalf("Render(long text) error = %v", err)
	}
	if len(got) == 0 {
		t.Error("Render(long text) returned empty output")
	}
}

func TestRenderMarkdown(t *testing.T) {
	input := "## Hello"
	got := RenderMarkdown(input)
	if !strings.Contains(got, "Hello") {
		t.Errorf("RenderMarkdown(%q) missing 'Hello', got: %q", input, truncateStr(got, 200))
	}
}

// ---------------------------------------------------------------------------
// 功能 2：文件链接提取与 OSC 8 超链接
// ---------------------------------------------------------------------------

func TestExtractFileLinks(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		check   func([]FileLink) bool
	}{
		{
			name:    "markdown link format",
			input:   "[app.go](file:///d:/src/app.go#L42)",
			wantLen: 1,
			check: func(links []FileLink) bool {
				return links[0].Path == "d:/src/app.go" && links[0].Line == 42 && links[0].Text == "app.go"
			},
		},
		{
			name:    "file URI with line range",
			input:   "file:///d:/src/app.go#L123-L145",
			wantLen: 1,
			check: func(links []FileLink) bool {
				return links[0].Path == "d:/src/app.go" && links[0].LineStart == 123 && links[0].LineEnd == 145
			},
		},
		{
			name:    "path:line format",
			input:   "internal/tools/Executor.go:141",
			wantLen: 1,
			check: func(links []FileLink) bool {
				return links[0].Path == "internal/tools/Executor.go" && links[0].Line == 141
			},
		},
		{
			name:    "path:line:col format",
			input:   "main.go:10:5",
			wantLen: 1,
			check: func(links []FileLink) bool {
				return links[0].Path == "main.go" && links[0].Line == 10 && links[0].Col == 5
			},
		},
		{
			name:    "multiple links",
			input:   "see app.go:10 and web.go:20",
			wantLen: 2,
		},
		{
			name:    "no links in plain text",
			input:   "hello world",
			wantLen: 0,
		},
		{
			name:    "ignore http URLs",
			input:   "https://example.com",
			wantLen: 0,
		},
		{
			name:    "relative path",
			input:   "./internal/tui/app.go:100",
			wantLen: 1,
			check: func(links []FileLink) bool {
				return links[0].Path == "./internal/tui/app.go" && links[0].Line == 100
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := ExtractFileLinks(tt.input)
			if len(links) != tt.wantLen {
				t.Fatalf("ExtractFileLinks(%q) = %d links, want %d", tt.input, len(links), tt.wantLen)
			}
			if tt.check != nil && len(links) > 0 {
				if !tt.check(links) {
					t.Errorf("ExtractFileLinks(%q) check failed, got: %+v", tt.input, links)
				}
			}
		})
	}
}

func TestExtractFileLinks_WindowsPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	links := ExtractFileLinks(`C:\Users\test\main.go:5`)
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0].Line != 5 {
		t.Errorf("expected Line=5, got %d", links[0].Line)
	}
}

func TestRenderFileLink(t *testing.T) {
	tests := []struct {
		name     string
		link     FileLink
		contains string
	}{
		{
			name:     "basic link with line",
			link:     FileLink{Path: "app.go", Line: 42},
			contains: "app.go:42",
		},
		{
			name:     "link with line range",
			link:     FileLink{Path: "app.go", LineStart: 10, LineEnd: 20},
			contains: "app.go:10-20",
		},
		{
			name:     "link without line",
			link:     FileLink{Path: "app.go"},
			contains: "app.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderFileLink(tt.link)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("RenderFileLink(%+v) missing %q, got: %q", tt.link, tt.contains, got)
			}
			if !strings.Contains(got, "\x1b]8;;") {
				t.Errorf("RenderFileLink(%+v) missing OSC 8 sequence", tt.link)
			}
		})
	}
}

func TestRenderTextWithLinks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "replace path:line with link",
			input:    "see app.go:10",
			contains: "\x1b]8;;",
		},
		{
			name:     "no links text unchanged",
			input:    "hello world",
			contains: "hello world",
		},
		{
			name:     "multiple links",
			input:    "fix in app.go:10 and web.go:20",
			contains: "app.go:10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderTextWithLinks(tt.input)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("RenderTextWithLinks(%q) missing %q, got: %q", tt.input, tt.contains, truncateStr(got, 200))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 功能 3：键盘驱动文档导航
// ---------------------------------------------------------------------------

func TestExtractFileLinksFromMessages(t *testing.T) {
	messages := []string{
		"see app.go:10 for details",
		"also check web.go:20",
		"no links here",
	}
	links := ExtractFileLinksFromMessages(messages)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}

func TestExtractFileLinksFromMessages_Empty(t *testing.T) {
	links := ExtractFileLinksFromMessages(nil)
	if len(links) != 0 {
		t.Errorf("expected 0 links for nil input, got %d", len(links))
	}
}

func TestFormatFileLinkChoice(t *testing.T) {
	tests := []struct {
		name   string
		link   FileLink
		expect string
	}{
		{
			name:   "with line number",
			link:   FileLink{Path: "app.go", Line: 42},
			expect: "app.go:42",
		},
		{
			name:   "with line range",
			link:   FileLink{Path: "app.go", LineStart: 10, LineEnd: 20},
			expect: "app.go:10-20",
		},
		{
			name:   "without line number",
			link:   FileLink{Path: "app.go"},
			expect: "app.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatFileLinkChoice(tt.link)
			if got != tt.expect {
				t.Errorf("FormatFileLinkChoice(%+v) = %q, want %q", tt.link, got, tt.expect)
			}
		})
	}
}

func TestBuildEditorCommand(t *testing.T) {
	tests := []struct {
		name     string
		editor   string
		path     string
		line     int
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "uses EDITOR env var on windows",
			editor:   "code",
			path:     "app.go",
			line:     10,
			wantCmd:  "code",
			wantArgs: []string{"app.go:10"},
		},
		{
			name:     "vim style args",
			editor:   "vim",
			path:     "app.go",
			line:     5,
			wantCmd:  "vim",
			wantArgs: []string{"+5", "app.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := BuildEditorCommand(tt.editor, tt.path, tt.line)
			if cmd.Path != "" && cmd.Args != nil {
				if cmd.Args[0] != tt.wantCmd {
					t.Errorf("BuildEditorCommand cmd = %v, want %v", cmd.Args[0], tt.wantCmd)
				}
			}
		})
	}
}

func TestBuildEditorCommand_DefaultEditor(t *testing.T) {
	cmd := BuildEditorCommand("", "app.go", 10)
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
}

// TestMarkdownRenderer 测试 Markdown 渲染器对代码块、标题和链接的处理
func TestMarkdownRenderer(t *testing.T) {
	r, err := NewMarkdownRenderer("dark")
	if err != nil {
		t.Fatalf("创建渲染器失败: %v", err)
	}

	stripANSI := func(s string) string {
		re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
		return re.ReplaceAllString(s, "")
	}

	t.Run("代码块渲染", func(t *testing.T) {
		input := "```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"
		got, err := r.Render(input)
		if err != nil {
			t.Fatalf("Render 代码块失败: %v", err)
		}
		plain := stripANSI(got)
		if !strings.Contains(plain, "func main()") {
			t.Errorf("代码块渲染should contain 'func main()'，actual: %q", truncateStr(plain, 200))
		}
		if !strings.Contains(plain, "fmt.Println") {
			t.Errorf("代码块渲染should contain 'fmt.Println'，actual: %q", truncateStr(plain, 200))
		}
	})

	t.Run("标题渲染", func(t *testing.T) {
		input := "# H1 Title\n## H2 Title\n### H3 Title"
		got, err := r.Render(input)
		if err != nil {
			t.Fatalf("Render 标题失败: %v", err)
		}
		plain := stripANSI(got)
		if !strings.Contains(plain, "H1 Title") {
			t.Errorf("标题渲染should contain 'H1 Title'，actual: %q", truncateStr(plain, 200))
		}
		if !strings.Contains(plain, "H2 Title") {
			t.Errorf("标题渲染should contain 'H2 Title'，actual: %q", truncateStr(plain, 200))
		}
		if !strings.Contains(plain, "H3 Title") {
			t.Errorf("标题渲染should contain 'H3 Title'，actual: %q", truncateStr(plain, 200))
		}
	})

	t.Run("链接渲染", func(t *testing.T) {
		input := "[Click here](https://example.com)"
		got, err := r.Render(input)
		if err != nil {
			t.Fatalf("Render 链接失败: %v", err)
		}
		plain := stripANSI(got)
		if !strings.Contains(plain, "Click here") {
			t.Errorf("链接渲染should contain链接文本 'Click here'，actual: %q", truncateStr(plain, 200))
		}
	})
}
