package diffutil

import (
	"strings"
	"testing"
)

// TestDiff_BasicChange 验证基本文件修改的 diff 输出格式
func TestDiff_BasicChange(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nmodified\nline3"
	diff := Diff("test.txt", old, new)

	if !strings.Contains(diff, "--- a/test.txt") {
		t.Error("missing old file header")
	}
	if !strings.Contains(diff, "+++ b/test.txt") {
		t.Error("missing new file header")
	}
	if !strings.Contains(diff, "-line2") {
		t.Error("missing removed line")
	}
	if !strings.Contains(diff, "+modified") {
		t.Error("missing added line")
	}
}

// TestDiff_NoChange 验证内容无变化时 diff 输出只有文件头
func TestDiff_NoChange(t *testing.T) {
	content := "line1\nline2\nline3"
	diff := Diff("test.txt", content, content)

	if !strings.Contains(diff, "--- a/test.txt") {
		t.Error("missing old file header")
	}
	if strings.Contains(diff, "@@") {
		t.Error("should not contain hunks when no changes")
	}
}

// TestDiff_AddLines 验证纯新增行的 diff
func TestDiff_AddLines(t *testing.T) {
	old := "line1\nline3"
	new := "line1\nline2\nline3"
	diff := Diff("test.txt", old, new)

	if !strings.Contains(diff, "+line2") {
		t.Error("missing added line")
	}
}

// TestDiff_RemoveLines 验证纯删除行的 diff
func TestDiff_RemoveLines(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nline3"
	diff := Diff("test.txt", old, new)

	if !strings.Contains(diff, "-line2") {
		t.Error("missing removed line")
	}
}

// TestColorDiff_Output 验证 ColorDiff 输出包含 ANSI 颜色码
func TestColorDiff_Output(t *testing.T) {
	old := "line1\nline2"
	new := "line1\nmodified"
	result := ColorDiff("test.txt", old, new)

	if !strings.Contains(result, "\x1b[") {
		t.Error("missing ANSI color codes")
	}
	if !strings.Contains(result, "\x1b[32m") {
		t.Error("missing green color for added lines")
	}
	if !strings.Contains(result, "\x1b[31m") {
		t.Error("missing red color for removed lines")
	}
}

// TestDiffStats_Counts 验证 diff 统计的行数计算
func TestDiffStats_Counts(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nmodified\nline3"
	added, removed := DiffStats(old, new)

	if added != 1 {
		t.Errorf("expected added=1, got %d", added)
	}
	if removed != 1 {
		t.Errorf("expected removed=1, got %d", removed)
	}
}

// TestDiffStats_NoChange 验证无变化时统计为零
func TestDiffStats_NoChange(t *testing.T) {
	content := "line1\nline2\nline3"
	added, removed := DiffStats(content, content)

	if added != 0 || removed != 0 {
		t.Errorf("expected 0 changes, got added=%d, removed=%d", added, removed)
	}
}

// TestDiff_EmptyContent 验证空内容的 diff
func TestDiff_EmptyContent(t *testing.T) {
	diff := Diff("test.txt", "", "new content")
	if !strings.Contains(diff, "+new content") {
		t.Error("missing added content for empty old")
	}
}
