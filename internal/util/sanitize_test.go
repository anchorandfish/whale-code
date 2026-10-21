package util

import "testing"

// TestSanitizeExternalText 验证终端注入控制字符被剥离。
func TestSanitizeExternalText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"清屏 \x1b[2J", "hello\x1b[2Jworld", "hello[2Jworld"},
		{"回车伪造 \r", "evil-server\rTrusted", "evil-serverTrusted"},
		{"隐藏 \x1b[8m", "a\x1b[8mb", "a[8mb"},
		{"改标题 \x1b]0;", "x\x1b]0;evil", "x]0;evil"},
		{"NUL", "a\x00b", "ab"},
		{"DEL", "a\x7fb", "ab"},
		{"保留换行", "a\nb", "a\nb"},
		{"保留制表", "a\tb", "a\tb"},
		{"干净文本原样", "normal text 中文 🎉", "normal text 中文 🎉"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeExternalText(c.in); got != c.want {
				t.Errorf("SanitizeExternalText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
