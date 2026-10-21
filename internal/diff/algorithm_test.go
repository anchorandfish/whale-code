package diff

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// TestComputeLineDiffs_LineReorder 验证行重排产出最优 diff（非爆炸）
func TestComputeLineDiffs_LineReorder(t *testing.T) {
	oldText := "a\nb\nc\n"
	newText := "c\nb\na\n"

	diffs := ComputeLineDiffs(oldText, newText)

	// 行重排应产出合理的 diff，不是每行都 delete+insert
	// 最优解：保留 b，a 和 c 各 delete+insert
	equalCount := 0
	for _, ld := range diffs {
		if ld.Type == diffmatchpatch.DiffEqual {
			equalCount++
		}
	}

	if equalCount == 0 {
		t.Error("行重排应保留相同的行（b），equalCount 不应为 0")
	}
}

// TestComputeLineDiffs_HunkConsistency 验证 lineDiffs 与 hunk 行数一致
func TestComputeLineDiffs_HunkConsistency(t *testing.T) {
	oldText := "line1\nline2\nline3\nline4\nline5\n"
	newText := "line1\nline2_mod\nline3\nline4_mod\nline5\n"

	diffs := ComputeLineDiffs(oldText, newText)
	hunks := groupHunks(diffs, 3)

	// hunk 内所有行数之和应等于 lineDiffs 中涉及的总行数
	totalHunkLines := 0
	for _, h := range hunks {
		totalHunkLines += len(h.Lines)
	}

	if totalHunkLines == 0 {
		t.Error("hunk 应包含行")
	}

	// hunk 行数不应超过 lineDiffs 总行数
	if totalHunkLines > len(diffs) {
		t.Errorf("hunk 总行数 %d 超过 lineDiffs 总行数 %d", totalHunkLines, len(diffs))
	}
}

// equalSlices 比较两个字符串切片是否深度相等
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// verifyDiffCorrectness 验证 diff 序列应用到 old/new 后能重建原始行
// 这是 diff 正确性的核心校验：Equal 行同时属于 old 和 new，
// Delete 行仅属于 old，Insert 行仅属于 new
func verifyDiffCorrectness(t *testing.T, oldText, newText string) {
	t.Helper()
	diffs := ComputeLineDiffs(oldText, newText)
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	var reconOld, reconNew []string
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			reconOld = append(reconOld, d.Text)
			reconNew = append(reconNew, d.Text)
		case diffmatchpatch.DiffDelete:
			reconOld = append(reconOld, d.Text)
		case diffmatchpatch.DiffInsert:
			reconNew = append(reconNew, d.Text)
		}
	}

	if !equalSlices(reconOld, oldLines) {
		t.Errorf("diff 重建 old 不匹配\n期望: %v\n实际: %v", oldLines, reconOld)
	}
	if !equalSlices(reconNew, newLines) {
		t.Errorf("diff 重建 new 不匹配\n期望: %v\n实际: %v", newLines, reconNew)
	}
}

// TestComputeLineDiffs_SmallFile 验证小文件 diff 正确性（多种变更模式）
func TestComputeLineDiffs_SmallFile(t *testing.T) {
	cases := []struct {
		name    string
		oldText string
		newText string
	}{
		{"单行修改", "hello\n", "world\n"},
		{"多行修改", "line1\nline2\nline3\n", "line1\nmodified\nline3\n"},
		{"行首插入", "b\nc\n", "a\nb\nc\n"},
		{"行尾插入", "a\nb\n", "a\nb\nc\n"},
		{"行首删除", "a\nb\nc\n", "b\nc\n"},
		{"行尾删除", "a\nb\nc\n", "a\nb\n"},
		{"中间插入", "a\nc\n", "a\nb\nc\n"},
		{"中间删除", "a\nb\nc\n", "a\nc\n"},
		{"完全替换", "old1\nold2\n", "new1\nnew2\nnew3\n"},
		{"行重排", "a\nb\nc\n", "c\nb\na\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifyDiffCorrectness(t, tc.oldText, tc.newText)
		})
	}
}

// TestComputeLineDiffs_IdenticalFiles 验证完全相同文件快速返回全 Equal
func TestComputeLineDiffs_IdenticalFiles(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"单行", "hello\n"},
		{"多行", "line1\nline2\nline3\nline4\n"},
		{"空行", ""},
		{"无尾换行", "line1\nline2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diffs := ComputeLineDiffs(tc.text, tc.text)
			for _, d := range diffs {
				if d.Type != diffmatchpatch.DiffEqual {
					t.Errorf("相同文件应全部为 Equal，发现 %v: %q", d.Type, d.Text)
				}
			}
		})
	}
}

// TestComputeLineDiffs_CompletelyDifferent 验证完全不同文件 diff 正确性
func TestComputeLineDiffs_CompletelyDifferent(t *testing.T) {
	cases := []struct {
		name    string
		oldText string
		newText string
	}{
		{"单行不同", "a\n", "b\n"},
		{"多行全不同", "a\nb\nc\n", "x\ny\nz\n"},
		{"长度不同", "a\nb\n", "x\ny\nz\nw\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifyDiffCorrectness(t, tc.oldText, tc.newText)
		})
	}
}

// TestComputeLineDiffs_Unicode 验证中文/Unicode 内容 diff 正确性
func TestComputeLineDiffs_Unicode(t *testing.T) {
	cases := []struct {
		name    string
		oldText string
		newText string
	}{
		{"中文修改", "你好\n世界\n", "你好\n地球\n"},
		{"中文插入", "你好\n", "你好\n世界\n"},
		{"emoji", "👋\n🌍\n", "👋\n🚀\n"},
		{"混合", "Hello 你好\nWorld 世界\n", "Hello 你好\nGo 语言\n"},
		{"日文", "こんにちは\n", "こんばんは\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifyDiffCorrectness(t, tc.oldText, tc.newText)
		})
	}
}

// TestComputeLineDiffs_EdgeCases 验证边界 case：空文件、单行文件
func TestComputeLineDiffs_EdgeCases(t *testing.T) {
	t.Run("双空", func(t *testing.T) {
		diffs := ComputeLineDiffs("", "")
		if len(diffs) != 0 {
			t.Errorf("双空应返回 nil，实际 %d 个 diff", len(diffs))
		}
	})

	t.Run("旧空新非空", func(t *testing.T) {
		verifyDiffCorrectness(t, "", "a\nb\n")
	})

	t.Run("旧非空新空", func(t *testing.T) {
		verifyDiffCorrectness(t, "a\nb\n", "")
	})

	t.Run("单行相同", func(t *testing.T) {
		diffs := ComputeLineDiffs("hello\n", "hello\n")
		if len(diffs) != 1 || diffs[0].Type != diffmatchpatch.DiffEqual {
			t.Errorf("单行相同应为 1 个 Equal，实际: %v", diffs)
		}
	})

	t.Run("单行不同", func(t *testing.T) {
		verifyDiffCorrectness(t, "hello\n", "world\n")
	})

	t.Run("无尾换行", func(t *testing.T) {
		verifyDiffCorrectness(t, "a\nb", "a\nc")
	})

	t.Run("仅换行符差异", func(t *testing.T) {
		verifyDiffCorrectness(t, "a\nb", "a\nb\n")
	})
}

// TestComputeLineDiffs_LargeFile_Performance 验证大文件 diff 性能（10K 行 < 100ms 无 race）
// 注：-race 模式下有 2-10x 开销，阈值放宽到 1s；基准性能见 BenchmarkComputeLineDiffs
func TestComputeLineDiffs_LargeFile_Performance(t *testing.T) {
	const lineCount = 10000

	// 构造 10K 行文件，中间修改 50 行（模拟真实编辑场景）
	var oldBuf, newBuf strings.Builder
	for i := 0; i < lineCount; i++ {
		if i >= lineCount/2-25 && i < lineCount/2+25 {
			oldBuf.WriteString(fmt.Sprintf("original_line_%d\n", i))
			newBuf.WriteString(fmt.Sprintf("modified_line_%d\n", i))
		} else {
			line := fmt.Sprintf("unchanged_line_%d\n", i)
			oldBuf.WriteString(line)
			newBuf.WriteString(line)
		}
	}

	oldText := oldBuf.String()
	newText := newBuf.String()

	start := time.Now()
	diffs := ComputeLineDiffs(oldText, newText)
	elapsed := time.Since(start)

	// 正确性验证
	verifyDiffCorrectness(t, oldText, newText)

	// 性能验证：-race 下放宽到 1s，非 -race 应 < 100ms
	if elapsed > time.Second {
		t.Errorf("大文件 diff 耗时 %v 超过 1s（-race 阈值）", elapsed)
	}
	t.Logf("10K 行文件 diff（50 行修改）耗时: %v, diff 数: %d", elapsed, len(diffs))
}

// TestComputeLineDiffs_LargeFile_Identical 验证大文件完全相同快速返回
func TestComputeLineDiffs_LargeFile_Identical(t *testing.T) {
	const lineCount = 10000
	var buf strings.Builder
	for i := 0; i < lineCount; i++ {
		buf.WriteString(fmt.Sprintf("line_%d\n", i))
	}
	text := buf.String()

	start := time.Now()
	diffs := ComputeLineDiffs(text, text)
	elapsed := time.Since(start)

	// 相同文件应通过前缀修剪快速返回
	if elapsed > 100*time.Millisecond {
		t.Errorf("相同大文件 diff 耗时 %v 超过 100ms", elapsed)
	}
	// 应全部为 Equal
	if len(diffs) != lineCount {
		t.Errorf("相同文件应返回 %d 个 Equal，实际 %d", lineCount, len(diffs))
	}
	t.Logf("10K 行相同文件 diff 耗时: %v", elapsed)
}

// TestComputeLineDiffs_LargeFile_CompletelyDifferent 验证大文件完全不同 diff 正确性与性能
func TestComputeLineDiffs_LargeFile_CompletelyDifferent(t *testing.T) {
	const lineCount = 10000
	var oldBuf, newBuf strings.Builder
	for i := 0; i < lineCount; i++ {
		oldBuf.WriteString(fmt.Sprintf("old_%d\n", i))
		newBuf.WriteString(fmt.Sprintf("new_%d\n", i))
	}
	oldText := oldBuf.String()
	newText := newBuf.String()

	start := time.Now()
	diffs := ComputeLineDiffs(oldText, newText)
	elapsed := time.Since(start)

	// 正确性验证
	verifyDiffCorrectness(t, oldText, newText)

	// 性能验证：完全不同文件应通过 Myers fallback（朴素 diff）快速完成
	// -race 下放宽到 2s（Myers forward pass 有 O(capD*(N+M)) 开销）
	if elapsed > 2*time.Second {
		t.Errorf("完全不同大文件 diff 耗时 %v 超过 2s", elapsed)
	}
	t.Logf("10K 行完全不同文件 diff 耗时: %v, diff 数: %d", elapsed, len(diffs))
}

// TestComputeLineDiffs_LargeFile_Append 验证大文件尾部追加场景
func TestComputeLineDiffs_LargeFile_Append(t *testing.T) {
	const lineCount = 10000
	var oldBuf, newBuf strings.Builder
	for i := 0; i < lineCount; i++ {
		oldBuf.WriteString(fmt.Sprintf("line_%d\n", i))
		newBuf.WriteString(fmt.Sprintf("line_%d\n", i))
	}
	// 尾部追加 100 行
	for i := 0; i < 100; i++ {
		newBuf.WriteString(fmt.Sprintf("appended_%d\n", i))
	}

	oldText := oldBuf.String()
	newText := newBuf.String()

	start := time.Now()
	diffs := ComputeLineDiffs(oldText, newText)
	elapsed := time.Since(start)

	verifyDiffCorrectness(t, oldText, newText)

	if elapsed > 200*time.Millisecond {
		t.Errorf("尾部追加大文件 diff 耗时 %v 超过 200ms", elapsed)
	}

	// 应有 100 个 Insert
	insertCount := 0
	for _, d := range diffs {
		if d.Type == diffmatchpatch.DiffInsert {
			insertCount++
		}
	}
	if insertCount != 100 {
		t.Errorf("追加 100 行应有 100 个 Insert，实际 %d", insertCount)
	}
	t.Logf("10K 行尾部追加 100 行 diff 耗时: %v", elapsed)
}

// TestMyersDiff_FallbackTrigger 验证 Myers D 超限时正确触发朴素 fallback
// 构造 D 远超 sqrt(budget/2) 的场景，确认 fallback 仍产出合法 diff
func TestMyersDiff_FallbackTrigger(t *testing.T) {
	// 构造大量交替行，使 D 接近 N+M（触发 Myers 内存保护降级）
	const lineCount = 5000
	var oldBuf, newBuf strings.Builder
	for i := 0; i < lineCount; i++ {
		if i%2 == 0 {
			oldBuf.WriteString(fmt.Sprintf("old_%d\n", i))
			newBuf.WriteString(fmt.Sprintf("new_%d\n", i))
		} else {
			// 间隔行也全不同，确保 D 极大
			oldBuf.WriteString(fmt.Sprintf("x_%d\n", i))
			newBuf.WriteString(fmt.Sprintf("y_%d\n", i))
		}
	}

	oldText := oldBuf.String()
	newText := newBuf.String()

	// 应正确触发 fallback 且产出合法 diff
	verifyDiffCorrectness(t, oldText, newText)
}

// BenchmarkComputeLineDiffs_SmallFile 小文件 diff 基准（无 -race 开销）
func BenchmarkComputeLineDiffs_SmallFile(b *testing.B) {
	oldText := "line1\nline2\nline3\nline4\nline5\n"
	newText := "line1\nmodified\nline3\nline4\nline5\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeLineDiffs(oldText, newText)
	}
}

// BenchmarkComputeLineDiffs_LargeFile_50Changes 大文件 50 行修改基准
func BenchmarkComputeLineDiffs_LargeFile_50Changes(b *testing.B) {
	const lineCount = 10000
	var oldBuf, newBuf strings.Builder
	for i := 0; i < lineCount; i++ {
		if i >= lineCount/2-25 && i < lineCount/2+25 {
			oldBuf.WriteString(fmt.Sprintf("original_line_%d\n", i))
			newBuf.WriteString(fmt.Sprintf("modified_line_%d\n", i))
		} else {
			line := fmt.Sprintf("unchanged_line_%d\n", i)
			oldBuf.WriteString(line)
			newBuf.WriteString(line)
		}
	}
	oldText := oldBuf.String()
	newText := newBuf.String()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeLineDiffs(oldText, newText)
	}
}

// BenchmarkComputeLineDiffs_LargeFile_Identical 大文件相同内容基准（前缀修剪快路径）
func BenchmarkComputeLineDiffs_LargeFile_Identical(b *testing.B) {
	const lineCount = 10000
	var buf strings.Builder
	for i := 0; i < lineCount; i++ {
		buf.WriteString(fmt.Sprintf("line_%d\n", i))
	}
	text := buf.String()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeLineDiffs(text, text)
	}
}
