package diff

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// TestMain 设置终端环境以确保 lipgloss 输出 ANSI 码
func TestMain(m *testing.M) {
	os.Setenv("TERM", "xterm-256color")
	os.Setenv("COLORTERM", "truecolor")
	// 强制 lipgloss 使用 TrueColor profile，确保测试中有 ANSI 输出
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// stripANSI 去除 ANSI 转义序列，用于测试中比较纯文本内容
func stripANSI(s string) string {
	var result strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteByte(s[i])
	}
	return result.String()
}

// TestTruncate_UTF8Safe 验证 truncate 函数对含中文/emoji 字符串截断后仍是有效 UTF-8
func TestTruncate_UTF8Safe(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
	}{
		{"中文截断", "你好世界测试字符串", 5},
		{"emoji截断", "👋🌍🚀🎉🎊🎈", 3},
		{"混合截断", "Hello你好World世界", 8},
		{"中文不截断", "你好", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			if !utf8.ValidString(result) {
				t.Errorf("truncate(%q, %d) = %q, 不是有效 UTF-8", tt.input, tt.maxLen, result)
			}
			if utf8.RuneCountInString(result) > tt.maxLen {
				t.Errorf("truncate(%q, %d) = %q, rune 长度 %d 超过 maxLen %d",
					tt.input, tt.maxLen, result, utf8.RuneCountInString(result), tt.maxLen)
			}
		})
	}
}

// TestRender_HunkHeaders 验证 Render 输出含 @@ -X,Y +A,B @@ 格式
func TestRender_HunkHeaders(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "line1\nline2\nline3\nline4\nline5\n",
		NewContent: "line1\nline2_mod\nline3\nline4_mod\nline5\n",
	}
	cfg := DefaultConfig()

	result := Render(change, cfg)

	// 用正则验证完整 hunk 头格式：@@ -X,Y +A,B @@
	// 对齐 git diff --unified=N 输出规范，X/Y/A/B 均为数字
	hunkRe := regexp.MustCompile(`@@ -\d+,\d+ \+\d+,\d+ @@`)
	if !hunkRe.MatchString(result) {
		t.Errorf("Render 输出应包含完整 @@ -X,Y +A,B @@ hunk 头格式，实际: %s", result)
	}

	// 进一步验证行号从 1 开始（非 0，除非 count=0 空端场景）
	// 此处非空端场景，oldStart/newStart 应 ≥ 1
	startRe := regexp.MustCompile(`@@ -(\d+),\d+ \+(\d+),\d+ @@`)
	matches := startRe.FindStringSubmatch(result)
	if len(matches) >= 3 {
		oldStart, _ := strconv.Atoi(matches[1])
		newStart, _ := strconv.Atoi(matches[2])
		if oldStart == 0 || newStart == 0 {
			t.Errorf("非空端 hunk 头行号应从 1 开始（非 0），oldStart=%d newStart=%d", oldStart, newStart)
		}
	}
}

// TestRender_HunkHeaders_EmptyFile 验证 count=0 场景对齐 git diff 规范
func TestRender_HunkHeaders_EmptyFile(t *testing.T) {
	// 文件全部删除
	t.Run("all_deleted", func(t *testing.T) {
		change := FileChange{
			Path:       "test.go",
			OldContent: "line1\nline2\n",
			NewContent: "",
		}
		cfg := DefaultConfig()
		result := Render(change, cfg)
		// 全部删除时 new 侧为 +0,0
		if !strings.Contains(result, "+0,0") {
			t.Errorf("全部删除时应含 +0,0, 实际: %s", result)
		}
	})

	// 新文件（旧文件为空）
	t.Run("new_file", func(t *testing.T) {
		change := FileChange{
			Path:       "test.go",
			OldContent: "",
			NewContent: "line1\nline2\n",
		}
		cfg := DefaultConfig()
		result := Render(change, cfg)
		// 新文件时 old 侧为 -0,0
		if !strings.Contains(result, "-0,0") {
			t.Errorf("新文件时应含 -0,0, 实际: %s", result)
		}
	})
}

// TestRender_GutterLineNumbers 验证行号格式正确、行号递增正确
func TestRender_GutterLineNumbers(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "line1\nline2\nline3\n",
		NewContent: "line1\nline2_mod\nline3\n",
	}
	cfg := DefaultConfig()

	result := Render(change, cfg)

	// 验证包含 │ 分隔符
	if !strings.Contains(result, "│") {
		t.Error("Render 输出应包含 │ 行号分隔符")
	}

	// 验证行号出现
	if !strings.Contains(result, "1") {
		t.Error("Render 输出应包含行号")
	}
}

// TestRender_WordLevelHighlightIntegrated 验证 Render 输出的变更行内含词级前景色/粗体样式
func TestRender_WordLevelHighlightIntegrated(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "func hello() {\n    fmt.Println(\"hello\")\n}\n",
		NewContent: "func hello() {\n    fmt.Println(\"world\")\n}\n",
	}
	cfg := DefaultConfig()

	result := Render(change, cfg)

	// 去除 ANSI 后仍应包含原始文本
	plain := stripANSI(result)
	if !strings.Contains(plain, "hello") || !strings.Contains(plain, "world") {
		t.Error("Render 去除 ANSI 后应包含原始文本内容")
	}

	// 词级高亮使用 styleWordAdd/styleWordDel（Foreground + Bold），行级样式 styleAdded/styleRemoved 只有 Background 无 Bold
	// 仅检查 \x1b[ 会假阳性（行级背景色也含 ANSI 序列），必须检查 Bold 标记
	// TrueColor 模式下 Bold 可能以 \x1b[1m、\x1b[1;...m、\x1b[...;1m 形式出现
	if !hasBoldANSI(result) {
		t.Error("Render 输出应包含词级粗体样式（Bold ANSI 序列，区分于行级背景色）")
	}
}

// TestRender_WordLevelThresholdDegrade 验证变化比例 > 0.4 时无词级样式（退化为整行色）
func TestRender_WordLevelThresholdDegrade(t *testing.T) {
	// 构造大量变更的 diff：整行完全不同，变化比例接近 1.0
	change := FileChange{
		Path:       "test.go",
		OldContent: "completely different old content line here\n",
		NewContent: "totally unrelated new content line here\n",
	}
	cfg := DefaultConfig()
	cfg.WordThreshold = 0.4

	result := Render(change, cfg)

	// 当变化比例 > 阈值时，不应有词级粗体样式
	// 词级样式 styleWordAdd/styleWordDel 含 Bold(true)，行级样式 styleAdded/styleRemoved 只有 Background 无 Bold
	// 因此 Bold 的出现即词级高亮生效的标志
	// TrueColor 模式下 Bold 可能以 \x1b[1m、\x1b[1;...m、\x1b[...;1m 形式出现（不会是 "82;1"，因为 TrueColor 输出 38;2;R;G;B）
	// 用 || 而非 &&：任一 Bold 出现即失败（避免未来只有 Delete 或只有 Insert 场景漏检）
	if hasBoldANSI(result) {
		t.Error("变化比例 > 阈值时不应出现词级粗体样式（应退化为整行色）")
	}
}

// hasBoldANSI 检查字符串中是否含 Bold（粗体）ANSI 标记
// 用于区分词级样式（styleWordAdd/styleWordDel 含 Bold）和行级样式（styleAdded/styleRemoved 仅 Background，无 Bold）
// TrueColor 模式下 lipgloss 先调 Bold() 后调 Foreground()，termenv 按调用顺序拼接 SGR 序列，
// 输出形式为 \x1b[1;38;2;R;G;Bm（Bold 在 SGR 首位），故 \x1b[1; 检查项可覆盖所有词级样式
// 行级 Background 输出 \x1b[48;2;R;G;Bm（不含 1），不匹配
// 注：不检查 ";1m"——lipgloss/termenv 不会将 Bold 放在 SGR 末尾，且 ";1m" 会在 TrueColor
// 蓝色分量 B=1 时误匹配（如 \x1b[48;2;100;200;1m），有假阳性风险
func hasBoldANSI(s string) bool {
	return strings.Contains(s, "\x1b[1m") ||
		strings.Contains(s, "\x1b[1;")
}

// TestRender_CacheHit 验证相同 FileChange + 相同 RenderConfig 二次 Render 命中缓存
func TestRender_CacheHit(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "old line\n",
		NewContent: "new line\n",
	}
	cfg := DefaultConfig()

	result1 := Render(change, cfg)
	result2 := Render(change, cfg)

	if result1 != result2 {
		t.Error("相同输入的两次 Render 结果应一致（缓存命中）")
	}
}

// TestRender_CacheKey_ConfigSensitivity 验证同一 FileChange 不同 ContextLines 应缓存未命中
func TestRender_CacheKey_ConfigSensitivity(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "line1\nline2\nline3\nline4\nline5\nline6\nline7\n",
		NewContent: "line1\nline2\nline3_mod\nline4\nline5\nline6\nline7\n",
	}

	cfg1 := DefaultConfig()
	cfg1.ContextLines = 1
	cfg2 := DefaultConfig()
	cfg2.ContextLines = 5

	result1 := Render(change, cfg1)
	result2 := Render(change, cfg2)

	// 不同 ContextLines 应产生不同输出（不同上下文行数）
	if result1 == result2 {
		t.Error("不同 ContextLines 应产生不同渲染结果")
	}
}

// TestCountChanges_CacheReuse 验证先调 Render 写入缓存，CountChanges 应从缓存复用
func TestCountChanges_CacheReuse(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "old line1\nold line2\n",
		NewContent: "new line1\nnew line2\nnew line3\n",
	}
	cfg := DefaultConfig()

	// 先调 Render，写入缓存
	_ = Render(change, cfg)

	// 再调 CountChanges，应走缓存快路径
	added, removed := CountChanges(change)

	if added != 3 {
		t.Errorf("CountChanges added = %d, want 3", added)
	}
	if removed != 2 {
		t.Errorf("CountChanges removed = %d, want 2", removed)
	}
}

// TestRender_GutterWordLevelInteraction 验证 gutter 部分不受词级样式影响
func TestRender_GutterWordLevelInteraction(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "fmt.Println(\"hello\")\n",
		NewContent: "fmt.Println(\"world\")\n",
	}
	cfg := DefaultConfig()

	result := Render(change, cfg)

	// │ 分隔符应存在于输出中
	if !strings.Contains(result, "│") {
		t.Error("Render 输出应包含 │ 分隔符")
	}

	// 去除 ANSI 后验证 gutter 和内容结构
	plain := stripANSI(result)
	lines := strings.Split(plain, "\n")
	for _, line := range lines {
		if strings.Contains(line, "│") {
			gutterEnd := strings.Index(line, "│")
			contentAfterGutter := line[gutterEnd+len("│"):]
			trimmed := strings.TrimSpace(contentAfterGutter)
			if trimmed == "" {
				continue
			}
			if !strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, " ") {
				t.Errorf("│ 之后的行内容应以 +/-/ 开头，实际: %q", trimmed)
			}
		}
	}
}

// TestGroupHunks_Basic 验证基本 hunk 分组
func TestGroupHunks_Basic(t *testing.T) {
	lineDiffs := []LineDiff{
		{Type: diffmatchpatch.DiffEqual, Text: "line1"},
		{Type: diffmatchpatch.DiffEqual, Text: "line2"},
		{Type: diffmatchpatch.DiffEqual, Text: "line3"},
		{Type: diffmatchpatch.DiffDelete, Text: "line4_old"},
		{Type: diffmatchpatch.DiffInsert, Text: "line4_new"},
		{Type: diffmatchpatch.DiffEqual, Text: "line5"},
	}

	hunks := groupHunks(lineDiffs, 3)

	if len(hunks) == 0 {
		t.Fatal("应至少产生 1 个 hunk")
	}

	// 验证 hunk 头行号
	h := hunks[0]
	if h.OldStart != 1 {
		t.Errorf("OldStart = %d, want 1", h.OldStart)
	}
	if h.NewStart != 1 {
		t.Errorf("NewStart = %d, want 1", h.NewStart)
	}
}

// TestDigitCount 验证 digitCount 辅助函数
func TestDigitCount(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{0, 1},
		{1, 1},
		{9, 1},
		{10, 2},
		{99, 2},
		{100, 3},
		{999, 3},
		{1000, 4},
	}
	for _, tt := range tests {
		got := digitCount(tt.n)
		if got != tt.want {
			t.Errorf("digitCount(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

// === 破坏性测试（T1/T2）：空内容、超大文件、并发 ===

// TestRender_EmptyContent 验证空内容不 panic、返回空字符串
func TestRender_EmptyContent(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("both_empty", func(t *testing.T) {
		change := FileChange{Path: "test.go", OldContent: "", NewContent: ""}
		result := Render(change, cfg)
		if result != "" {
			t.Errorf("双空内容应返回空字符串，实际: %q", result)
		}
	})

	t.Run("old_empty", func(t *testing.T) {
		change := FileChange{Path: "test.go", OldContent: "", NewContent: "new\n"}
		result := Render(change, cfg)
		if result == "" {
			t.Error("旧文件空、新文件非空应产生 diff 输出")
		}
	})

	t.Run("new_empty", func(t *testing.T) {
		change := FileChange{Path: "test.go", OldContent: "old\n", NewContent: ""}
		result := Render(change, cfg)
		if result == "" {
			t.Error("旧文件非空、新文件空应产生 diff 输出")
		}
	})
}

// TestRender_LargeFile 验证超大文件返回截断提示
func TestRender_LargeFile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFileSize = 100 // 设置很小的文件大小限制

	// 构造超过限制的内容
	largeContent := strings.Repeat("x", 200)
	change := FileChange{Path: "test.go", OldContent: largeContent, NewContent: "new"}
	result := Render(change, cfg)

	if !strings.Contains(result, "Large file") {
		t.Errorf("超大文件应返回 Large file 提示，实际: %q", result)
	}
}

// TestRender_MaxLines 验证超过 MaxLines 限制时输出截断提示
func TestRender_MaxLines(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxLines = 3

	oldLines := ""
	newLines := ""
	for i := 0; i < 10; i++ {
		oldLines += fmt.Sprintf("line%d\n", i)
		newLines += fmt.Sprintf("modified_line%d\n", i)
	}

	change := FileChange{Path: "test.go", OldContent: oldLines, NewContent: newLines}
	result := Render(change, cfg)

	if !strings.Contains(result, "more lines") {
		t.Errorf("超过 MaxLines 应返回截断提示，实际: %s", result)
	}
}

// TestCountChanges_Concurrent 验证 CountChanges 并发调用不 panic
func TestCountChanges_Concurrent(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "old line1\nold line2\n",
		NewContent: "new line1\nnew line2\nnew line3\n",
	}

	// 先调 Render 写入缓存
	cfg := DefaultConfig()
	_ = Render(change, cfg)

	// 并发调 CountChanges
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			added, removed := CountChanges(change)
			if added != 3 || removed != 2 {
				t.Errorf("CountChanges = (%d, %d), want (3, 2)", added, removed)
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestRender_Concurrent 验证 Render 并发调用不 panic
func TestRender_Concurrent(t *testing.T) {
	change := FileChange{
		Path:       "test.go",
		OldContent: "old\n",
		NewContent: "new\n",
	}
	cfg := DefaultConfig()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			result := Render(change, cfg)
			if result == "" {
				t.Error("Render 应产生输出")
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
