// Package diffutil 提供 unified diff 生成与着色功能。
package diffutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Diff 计算新旧内容之间的 unified diff
// 返回标准 unified diff 格式字符串（--- / +++ / @@ hunk 头）
func Diff(oldPath, oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- a/%s\n", filepath.Base(oldPath)))
	sb.WriteString(fmt.Sprintf("+++ b/%s\n", filepath.Base(oldPath)))

	type diffEntry struct {
		op      byte
		content string
	}

	var entries []diffEntry
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	for i := 0; i < maxLen; i++ {
		if i >= len(oldLines) {
			entries = append(entries, diffEntry{'+', newLines[i]})
		} else if i >= len(newLines) {
			entries = append(entries, diffEntry{'-', oldLines[i]})
		} else if oldLines[i] != newLines[i] {
			entries = append(entries, diffEntry{'-', oldLines[i]})
			entries = append(entries, diffEntry{'+', newLines[i]})
		} else {
			entries = append(entries, diffEntry{' ', oldLines[i]})
		}
	}

	if len(entries) == 0 {
		return sb.String()
	}

	var changePos []int
	for i, e := range entries {
		if e.op != ' ' {
			changePos = append(changePos, i)
		}
	}

	if len(changePos) == 0 {
		return sb.String()
	}

	const contextSize = 3
	type hunkRange struct {
		start, end int
	}

	var hunks []hunkRange
	hunkStart := changePos[0]
	if hunkStart > contextSize {
		hunkStart -= contextSize
	} else {
		hunkStart = 0
	}
	lastChange := changePos[0]

	for _, pos := range changePos[1:] {
		if pos-lastChange > 2*contextSize+1 {
			hunkEnd := lastChange + contextSize
			if hunkEnd >= len(entries) {
				hunkEnd = len(entries) - 1
			}
			hunks = append(hunks, hunkRange{hunkStart, hunkEnd})
			hunkStart = pos - contextSize
			if hunkStart < 0 {
				hunkStart = 0
			}
		}
		lastChange = pos
	}
	hunkEnd := lastChange + contextSize
	if hunkEnd >= len(entries) {
		hunkEnd = len(entries) - 1
	}
	hunks = append(hunks, hunkRange{hunkStart, hunkEnd})

	for _, h := range hunks {
		oldCount := 0
		newCount := 0
		oldLinesBefore := 0
		newLinesBefore := 0

		for i := 0; i < h.start; i++ {
			switch entries[i].op {
			case ' ':
				oldLinesBefore++
				newLinesBefore++
			case '-':
				oldLinesBefore++
			case '+':
				newLinesBefore++
			}
		}

		for i := h.start; i <= h.end; i++ {
			switch entries[i].op {
			case ' ':
				oldCount++
				newCount++
			case '-':
				oldCount++
			case '+':
				newCount++
			}
		}

		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldLinesBefore+1, oldCount, newLinesBefore+1, newCount))

		for i := h.start; i <= h.end; i++ {
			sb.WriteString(fmt.Sprintf("%c%s\n", entries[i].op, entries[i].content))
		}
	}

	return sb.String()
}

// ColorDiff 用 ANSI 颜色渲染 diff 输出
// 绿色=新增行，红色=删除行，青色=文件头，灰色=上下文行
//
// Deprecated: 使用 internal/diff.Render 替代
func ColorDiff(oldPath, oldContent, newContent string) string {
	diff := Diff(oldPath, oldContent, newContent)
	lines := strings.Split(diff, "\n")

	var sb strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			sb.WriteString(fmt.Sprintf("\x1b[36m%s\x1b[0m\n", line)) // cyan
		} else if strings.HasPrefix(line, "+") {
			sb.WriteString(fmt.Sprintf("\x1b[32m%s\x1b[0m\n", line)) // green
		} else if strings.HasPrefix(line, "-") {
			sb.WriteString(fmt.Sprintf("\x1b[31m%s\x1b[0m\n", line)) // red
		} else {
			sb.WriteString(fmt.Sprintf("\x1b[90m%s\x1b[0m\n", line)) // gray
		}
	}
	return sb.String()
}

// DiffStats 计算 diff 统计信息
// 返回新增行数和删除行数
//
// Deprecated: 使用 internal/diff.CountChanges 替代
func DiffStats(oldContent, newContent string) (added, removed int) {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	oldSet := make(map[string]int)
	for _, l := range oldLines {
		oldSet[l]++
	}

	newSet := make(map[string]int)
	for _, l := range newLines {
		newSet[l]++
	}

	for l, count := range newSet {
		if oldCount, ok := oldSet[l]; ok {
			if count > oldCount {
				added += count - oldCount
			}
		} else {
			added += count
		}
	}

	for l, count := range oldSet {
		if newCount, ok := newSet[l]; ok {
			if count > newCount {
				removed += count - newCount
			}
		} else {
			removed += count
		}
	}

	return
}
