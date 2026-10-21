package util

import "testing"

// TestCountLines —— W-046a 唯一语义源的单测（编辑器语义：末尾换行不分新行）。
// 表值与 read_file 的 Scanner 行为逐格对齐（"a\n" Scanner 读 1 行、"a\n\n" 读 2 行）。
func TestCountLines(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"no trailing newline", "abc", 1},
		{"single line with trailing newline", "abc\n", 1},
		{"two lines", "a\nb", 2},
		{"two lines with trailing newline", "a\nb\n", 2},
		{"blank second line", "a\n\n", 2},
		{"only one newline (single empty line)", "\n", 1},
		{"only two newlines (two empty lines)", "\n\n", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountLines(tc.s); got != tc.want {
				t.Errorf("CountLines(%q) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
}
