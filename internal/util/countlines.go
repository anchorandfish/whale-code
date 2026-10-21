package util

import "strings"

// CountLines 按编辑器语义统计文本行数 —— 本仓「行数」的唯一语义源（收口，2026-10-08）。
//
// 语义拍板：行数 = 编辑器行数 —— 末尾换行不产生新行，空文本 0 行。
// 该语义与 read_file 的 bufio.Scanner 逐行读完全一致（internal/tools/file.go 读路径），
// 也与 TUI 渲染面既有实现一致（internal/tui/render.go countLines —— 该树有活跃写者，
// 刻意不收口，仅语义对齐；两处语义本就相同，不存在行为分叉）。
//
// 缺陷背景（W-046a）：write_file 曾用 strings.Count(s,"\n")+1（wc -l 语义），
// 末尾带换行时比同轮 read_file/diff 多报 1 行 —— 真机转录里同一输出块出现
// "File written: … (2 lines)" 与 "+1 lines" 与回读 1 行三个数字打架。
func CountLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
