package diff

// unified_test.go — G5：unified diff 解析 / 应用的单元表征。
//
// 关注点：
//  1. 解析的**保真**（a//b/ 前缀、/dev/null、计数、no-newline 标记）与**fail-closed**
//     （截断 hunk、非法前缀必须报错，不得静默吞掉）；
//  2. 应用的**逐字节**正确性 + fuzz 上界 + 冲突必须报 ConflictError；
//  3. **模型同源**：用渲染侧（ComputeLineDiffs + groupHunks）产出的 Hunk 回到应用侧
//     必须还原出目标内容 —— 证明两侧确实是同一个 hunk 模型，而不是凑巧形状相同。

import (
	"errors"
	"strings"
	"testing"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const sampleDiff = `diff --git a/hello.txt b/hello.txt
index 1234567..89abcde 100644
--- a/hello.txt
+++ b/hello.txt
@@ -1,4 +1,5 @@
 line one
-line two
+line two changed
+line two and a half
 line three
 line four
`

func TestParseUnifiedDiff_GitPrefixAndHunk(t *testing.T) {
	patches, err := ParseUnifiedDiff(sampleDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("期望 1 个文件补丁，实际 %d", len(patches))
	}
	fp := patches[0]
	if fp.OldPath != "hello.txt" || fp.NewPath != "hello.txt" {
		t.Errorf("a//b/ 前缀未剥离：old=%q new=%q", fp.OldPath, fp.NewPath)
	}
	if fp.IsNew || fp.IsDeleted {
		t.Errorf("普通修改不应标记为新建/删除：isNew=%v isDeleted=%v", fp.IsNew, fp.IsDeleted)
	}
	if len(fp.Hunks) != 1 {
		t.Fatalf("期望 1 个 hunk，实际 %d", len(fp.Hunks))
	}
	h := fp.Hunks[0]
	if h.OldStart != 1 || h.OldCount != 4 || h.NewStart != 1 || h.NewCount != 5 {
		t.Errorf("hunk 头解析错误：%s", HunkHeaderString(h))
	}
	if len(h.Lines) != 6 {
		t.Fatalf("hunk 行数应为 6，实际 %d", len(h.Lines))
	}
	want := []struct {
		typ  diffmatchpatch.Operation
		text string
	}{
		{diffmatchpatch.DiffEqual, "line one"},
		{diffmatchpatch.DiffDelete, "line two"},
		{diffmatchpatch.DiffInsert, "line two changed"},
		{diffmatchpatch.DiffInsert, "line two and a half"},
		{diffmatchpatch.DiffEqual, "line three"},
		{diffmatchpatch.DiffEqual, "line four"},
	}
	for i, w := range want {
		if h.Lines[i].Type != w.typ || h.Lines[i].Text != w.text {
			t.Errorf("hunk.Lines[%d] = (%v,%q)，期望 (%v,%q)", i, h.Lines[i].Type, h.Lines[i].Text, w.typ, w.text)
		}
	}
	// 计数不变量：old 侧 = 上下文 + 删除；new 侧 = 上下文 + 新增。
	var oldN, newN int
	for _, ld := range h.Lines {
		switch ld.Type {
		case diffmatchpatch.DiffEqual:
			oldN++
			newN++
		case diffmatchpatch.DiffDelete:
			oldN++
		case diffmatchpatch.DiffInsert:
			newN++
		}
	}
	if oldN != h.OldCount || newN != h.NewCount {
		t.Errorf("hunk 行数与声明不符：实测 old=%d new=%d，声明 old=%d new=%d", oldN, newN, h.OldCount, h.NewCount)
	}
}

func TestParseUnifiedDiff_NoPrefix(t *testing.T) {
	p := `--- hello.txt
+++ hello.txt
@@ -1,2 +1,2 @@
-a
+A
 b
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if patches[0].OldPath != "hello.txt" || patches[0].NewPath != "hello.txt" {
		t.Errorf("无前缀路径被误改：old=%q new=%q", patches[0].OldPath, patches[0].NewPath)
	}
}

func TestParseUnifiedDiff_NewFile(t *testing.T) {
	p := `--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+first
+second
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	fp := patches[0]
	if !fp.IsNew {
		t.Error("旧侧为 /dev/null 应标记 IsNew")
	}
	if fp.OldPath != "" {
		t.Errorf("新建文件的 OldPath 应为空，实际 %q", fp.OldPath)
	}
	if fp.TargetPath() != "new.txt" {
		t.Errorf("TargetPath = %q，期望 new.txt", fp.TargetPath())
	}
	h := fp.Hunks[0]
	if h.OldStart != 0 || h.OldCount != 0 || h.NewStart != 1 || h.NewCount != 2 {
		t.Errorf("新建文件 hunk 头错误：%s", HunkHeaderString(h))
	}
}

func TestParseUnifiedDiff_SkipsMetadataAndBlankSeparators(t *testing.T) {
	// 两个文件、中间夹空行与元信息行：hunk 必须按声明行数收口，不能把空行吞进 hunk。
	p := `--- a/one.txt
+++ b/one.txt
@@ -1,1 +1,1 @@
-old
+new
index 0000000..1111111 100644
--- a/two.txt
+++ b/two.txt
@@ -1,1 +1,1 @@
-x
+y
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if len(patches) != 2 {
		t.Fatalf("期望 2 个文件补丁，实际 %d", len(patches))
	}
	for i, want := range []string{"one.txt", "two.txt"} {
		if got := patches[i].TargetPath(); got != want {
			t.Errorf("patch[%d].TargetPath() = %q，期望 %q", i, got, want)
		}
		if len(patches[i].Hunks) != 1 || len(patches[i].Hunks[0].Lines) != 2 {
			t.Errorf("patch[%d] hunk 行数错误：%+v", i, patches[i].Hunks)
		}
	}
}

func TestParseUnifiedDiff_FailClosed(t *testing.T) {
	cases := []struct {
		name string
		p    string
	}{
		{"空补丁", "   \n"},
		{"截断 hunk", "--- a/x\n+++ b/x\n@@ -1,3 +1,3 @@\n a\n"},
		{"hunk 内空行", "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n a\n\n"},
		{"非法前缀", "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n?a\n"},
		{"无 hunk", "--- a/x\n+++ b/x\n"},
		{"combined diff", "@@@ -1,1 -1,1 +1,1 @@@\n"},
		{"binary patch", "GIT binary patch\nliteral 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseUnifiedDiff(tc.p); err == nil {
				t.Fatal("期望解析失败，实际成功（fail-open）")
			} else {
				var pe *ParseError
				if !errors.As(err, &pe) {
					t.Fatalf("期望 *ParseError，实际 %T: %v", err, err)
				}
			}
		})
	}
}

func TestApplyFilePatch_ByteExact(t *testing.T) {
	old := "line one\nline two\nline three\nline four\n"
	want := "line one\nline two changed\nline two and a half\nline three\nline four\n"

	patches, err := ParseUnifiedDiff(sampleDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	got, stats, err := ApplyFilePatch(old, patches[0], DefaultApplyOptions())
	if err != nil {
		t.Fatalf("ApplyFilePatch: %v", err)
	}
	if got != want {
		t.Errorf("应用结果非逐字节相等：\n got=%q\nwant=%q", got, want)
	}
	if stats.HunksApplied != 1 || stats.HunksTotal != 1 || stats.MaxFuzz != 0 {
		t.Errorf("统计错误：%+v（期望 1/1，fuzz=0）", stats)
	}
}

func TestApplyFilePatch_NewFile(t *testing.T) {
	p := `--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+first
+second
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	got, _, err := ApplyFilePatch("", patches[0], DefaultApplyOptions())
	if err != nil {
		t.Fatalf("ApplyFilePatch: %v", err)
	}
	if got != "first\nsecond\n" {
		t.Errorf("新建文件内容错误：%q", got)
	}
}

func TestApplyFilePatch_FuzzAllowsContextDrift(t *testing.T) {
	// 补丁声明改动在第 1 行，但真实文件前面多插入了 2 行导致位置偏移。
	old := "inserted A\ninserted B\nline one\nline two\nline three\n"
	p := `--- a/x
+++ b/x
@@ -1,3 +1,4 @@
 line one
-line two
+line two changed
+line two and a half
 line three
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	got, stats, err := ApplyFilePatch(old, patches[0], DefaultApplyOptions())
	if err != nil {
		t.Fatalf("ApplyFilePatch 应在漂移内匹配成功：%v", err)
	}
	want := "inserted A\ninserted B\nline one\nline two changed\nline two and a half\nline three\n"
	if got != want {
		t.Errorf("漂移匹配结果错误：\n got=%q\nwant=%q", got, want)
	}
	if stats.MaxFuzz != 0 {
		t.Errorf("上下文完整时不应动用 fuzz，实际 fuzz=%d", stats.MaxFuzz)
	}
}

func TestApplyFilePatch_FuzzTrimsContext(t *testing.T) {
	// 首行上下文与文件不符（只剩中间能对上）⇒ 必须靠 fuzz=1 才可应用。
	old := "DIFFERENT head\nline two\nline three\nline four\n"
	p := `--- a/x
+++ b/x
@@ -1,4 +1,4 @@
 line one
-line two
+line two changed
 line three
 line four
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	got, stats, err := ApplyFilePatch(old, patches[0], DefaultApplyOptions())
	if err != nil {
		t.Fatalf("fuzz=1 应可应用：%v", err)
	}
	want := "DIFFERENT head\nline two changed\nline three\nline four\n"
	if got != want {
		t.Errorf("fuzz 结果错误：\n got=%q\nwant=%q", got, want)
	}
	if stats.MaxFuzz != 1 {
		t.Errorf("应记录 fuzz=1，实际 %d", stats.MaxFuzz)
	}
}

func TestApplyFilePatch_FuzzBounded(t *testing.T) {
	// MaxFuzz=0 时，上面那个需要裁上下文才成立的补丁必须冲突（判别性：证明 fuzz 真的在起作用）。
	old := "DIFFERENT head\nline two\nline three\nline four\n"
	p := `--- a/x
+++ b/x
@@ -1,4 +1,4 @@
 line one
-line two
+line two changed
 line three
 line four
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if _, _, err := ApplyFilePatch(old, patches[0], ApplyOptions{MaxFuzz: 0, MaxLineDrift: 200}); err == nil {
		t.Fatal("MaxFuzz=0 时必须冲突，实际成功（fuzz 未生效）")
	}
}

func TestApplyFilePatch_FuzzOnlyTrimsContext(t *testing.T) {
	// hunk 首行是删除行（实质内容）⇒ 即使给 fuzz 也不得把它裁掉，结果只能是冲突。
	old := "zzz\nbbb\n"
	p := `--- a/x
+++ b/x
@@ -1,2 +1,2 @@
-aaa
+AAA
 bbb
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	_, _, err = ApplyFilePatch(old, patches[0], DefaultApplyOptions())
	if err == nil {
		t.Fatal("首行是删除行时不得靠 fuzz 蒙混过关，期望冲突")
	}
	if !errors.Is(err, ErrPatchConflict) {
		t.Errorf("期望 ErrPatchConflict，实际 %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("期望 *ConflictError，实际 %T", err)
	}
	if ce.HunkIndex != 1 || ce.Path != "x" {
		t.Errorf("冲突信息不完整：%+v", ce)
	}
}

func TestApplyFilePatch_ConflictIsAtomic(t *testing.T) {
	// 第二个 hunk 冲突 ⇒ 第一个 hunk 也不得生效（返回值不含部分应用内容）。
	old := "a\nb\nc\nzzz\n"
	p := `--- a/x
+++ b/x
@@ -1,2 +1,2 @@
-a
+A
 b
@@ -3,2 +3,2 @@
-c
+C
 d
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	got, stats, err := ApplyFilePatch(old, patches[0], DefaultApplyOptions())
	if err == nil {
		t.Fatal("第二个 hunk 应冲突")
	}
	if got != "" {
		t.Errorf("冲突时不得返回部分应用内容，实际 %q", got)
	}
	if stats.HunksApplied != 1 {
		t.Errorf("统计应如实反映「已成功 1 个 hunk 后失败」，实际 %d", stats.HunksApplied)
	}
}

func TestApplyFilePatch_NoTrailingNewlineMarker(t *testing.T) {
	// old 有行尾换行，new 无 —— 标记落在 `+` 行上。
	p := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n"
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	fp := patches[0]
	if fp.NewNoNewlineAtEOF {
		t.Error("标记落在 `+` 行上时，旧侧不应被判为无行尾换行")
	}
	// 注意：这里标记同时跟随旧的 `-b`，故 OldNoNewlineAtEOF=true、NewNoNewlineAtEOF=false，
	// 结论应是"新文件有行尾换行"。
	got, _, err := ApplyFilePatch("a\nb\n", fp, DefaultApplyOptions())
	if err != nil {
		t.Fatalf("ApplyFilePatch: %v", err)
	}
	if got != "a\nb\n" {
		t.Errorf("结果 %q，期望 %q", got, "a\nb\n")
	}
}

func TestApplyFilePatch_ResultKeepsNoTrailingNewline(t *testing.T) {
	// 新旧两侧都无行尾换行（git 在上下文行后打标记）。
	p := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-a\n+A\n b\n\\ No newline at end of file\n"
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	fp := patches[0]
	if !fp.NewNoNewlineAtEOF {
		t.Fatal("上下文行后的标记应同时判为两侧无行尾换行")
	}
	got, _, err := ApplyFilePatch("a\nb", fp, DefaultApplyOptions())
	if err != nil {
		t.Fatalf("ApplyFilePatch: %v", err)
	}
	if got != "A\nb" {
		t.Errorf("结果 %q，期望 %q（不得擅自补行尾换行）", got, "A\nb")
	}
}

func TestApplyFilePatch_MultipleHunks(t *testing.T) {
	old := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, "\n") + "\n"
	p := `--- a/x
+++ b/x
@@ -1,3 +1,3 @@
-1
+ONE
 2
 3
@@ -8,3 +8,3 @@
 8
-9
+NINE
 10
`
	patches, err := ParseUnifiedDiff(p)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	got, stats, err := ApplyFilePatch(old, patches[0], DefaultApplyOptions())
	if err != nil {
		t.Fatalf("多 hunk 应用失败：%v", err)
	}
	want := strings.Join([]string{"ONE", "2", "3", "4", "5", "6", "7", "8", "NINE", "10"}, "\n") + "\n"
	if got != want {
		t.Errorf("多 hunk 结果错误：\n got=%q\nwant=%q", got, want)
	}
	if stats.HunksApplied != 2 {
		t.Errorf("应应用 2 个 hunk，实际 %d", stats.HunksApplied)
	}
}

// TestApplyFilePatch_RenderModelRoundTrip 是**模型同源**的证据：用渲染侧的
// ComputeLineDiffs + groupHunks 产出的 Hunk 交给应用侧，必须还原出目标内容。
// 若哪天有人给解析侧另造一套 hunk 结构，这条会立刻红。
func TestApplyFilePatch_RenderModelRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"单行替换", "alpha\nbeta\ngamma\n", "alpha\nBETA\ngamma\n"},
		{"插入多行", "a\nb\nc\n", "a\nb1\nb2\nc\n"},
		{"删除行", "a\nb\nc\nd\n", "a\nd\n"},
		{"大文件多块", strings.Repeat("x\n", 50) + "target\n" + strings.Repeat("y\n", 50), strings.Repeat("x\n", 50) + "TARGET\n" + strings.Repeat("y\n", 50)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change := FileChange{Path: "x.txt", OldContent: tc.old, NewContent: tc.new}
			lineDiffs := ComputeLineDiffs(change.OldContent, change.NewContent)
			hunks := groupHunks(lineDiffs, 3)
			if len(hunks) == 0 {
				t.Fatal("渲染侧未产出 hunk，用例无意义")
			}
			fp := FilePatch{OldPath: "x.txt", NewPath: "x.txt", Hunks: hunks}
			got, _, err := ApplyFilePatch(tc.old, fp, DefaultApplyOptions())
			if err != nil {
				t.Fatalf("应用渲染侧模型的 hunk 失败：%v", err)
			}
			if got != tc.new {
				t.Errorf("往返不一致：\n got=%q\nwant=%q", got, tc.new)
			}
		})
	}
}

func TestScanPatchTargets(t *testing.T) {
	p := `diff --git a/one.txt b/one.txt
--- a/one.txt
+++ b/one.txt
@@ -1,1 +1,1 @@
-a
+A
--- /dev/null
+++ b/two.txt
@@ -0,0 +1,1 @@
+new
`
	got := ScanPatchTargets(p)
	want := []string{"one.txt", "two.txt"}
	if len(got) != len(want) {
		t.Fatalf("ScanPatchTargets = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ScanPatchTargets[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
	if empty := ScanPatchTargets("  "); empty != nil {
		t.Errorf("空输入应返回 nil，实际 %v", empty)
	}
}
