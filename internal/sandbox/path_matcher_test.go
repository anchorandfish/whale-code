// Package sandbox — PathMatcher 单元测试（C11 落地）。
package sandbox

import (
	"testing"
)

// TestGlobMatcher_Basic 验证基本 glob 匹配语义。
func TestGlobMatcher_Basic(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		// 单段字面量
		{"literal_exact", "foo", "foo", true},
		{"literal_no_match", "foo", "bar", false},
		{"literal_basename", "foo", "a/b/foo", true}, // 单段 Pattern 匹配末段
		{"literal_not_in_path", "foo", "a/foobar/b", false},

		// * 通配
		{"star_match", "*.go", "main.go", true},
		{"star_no_slash", "*.go", "a/main.go", true}, // 单段 Pattern 匹配 basename
		{"star_not_match_extension", "*.go", "main.txt", false},

		// ? 单字符
		{"question_match", "foo?.go", "foo1.go", true},
		{"question_no_match", "foo?.go", "foo12.go", false},

		// 字符类
		{"class_match", "foo[abc].go", "fooa.go", true},
		{"class_no_match", "foo[abc].go", "food.go", false},
		{"class_negate", "foo[!abc].go", "food.go", true},
		{"class_negate_no_match", "foo[!abc].go", "fooa.go", false},

		// 多段路径
		{"multi_segment", "src/*.go", "src/main.go", true},
		{"multi_segment_no_match", "src/*.go", "lib/main.go", false},
		{"multi_segment_star_no_cross_slash", "src/*.go", "src/sub/main.go", false},

		// ** 递归
		{"double_star_recursive", "src/**/*.go", "src/a/b/c/main.go", true},
		{"double_star_root", "src/**/*.go", "src/main.go", true},
		{"double_star_not_match_other_root", "src/**/*.go", "lib/main.go", false},
		{"double_star_only", "**/*.go", "a/b/c/main.go", true},
		{"double_star_only_root", "**/*.go", "main.go", true},

		// 行首 /
		{"leading_slash", "/foo", "foo", true},
		{"leading_slash_multi", "/src/*.go", "src/main.go", true},

		// 末尾 / 目录
		{"trailing_slash_dir", "foo/", "foo/bar", true},
		{"trailing_slash_dir_no_match", "foo/", "bar/foo", false}, // 单段 + dirOnly 不匹配 basename
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewGlobMatcher()
			if err := m.AddPattern(tc.pattern); err != nil {
				t.Fatalf("AddPattern(%q) error: %v", tc.pattern, err)
			}
			got := m.Match(tc.path)
			if got != tc.want {
				t.Errorf("Match(%q) = %v, want %v (pattern=%q)", tc.path, got, tc.want, tc.pattern)
			}
		})
	}
}

// TestGlobMatcher_Negation 验证 ! 取反语义（.gitignore last-match-wins）。
func TestGlobMatcher_Negation(t *testing.T) {
	m := NewGlobMatcher()
	if err := m.AddPattern("*.go"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddPattern("!main.go"); err != nil {
		t.Fatal(err)
	}
	if m.Match("main.go") {
		t.Error("main.go should not match (negated)")
	}
	if !m.Match("foo.go") {
		t.Error("foo.go should match")
	}
}

// TestGlobMatcher_EmptyPattern 验证空 Pattern 拒绝。
func TestGlobMatcher_EmptyPattern(t *testing.T) {
	m := NewGlobMatcher()
	if err := m.AddPattern(""); err != ErrEmptyPattern {
		t.Errorf("AddPattern(\"\") = %v, want ErrEmptyPattern", err)
	}
	if err := m.AddPattern("   "); err != ErrEmptyPattern {
		t.Errorf("AddPattern(\"   \") = %v, want ErrEmptyPattern", err)
	}
}

// TestGlobMatcher_UnclosedClass 验证未闭合字符类返回错误。
func TestGlobMatcher_UnclosedClass(t *testing.T) {
	m := NewGlobMatcher()
	err := m.AddPattern("foo[abc")
	if err == nil {
		t.Error("AddPattern(\"foo[abc\") should return error")
	}
}

// TestPrefixMatcher_Basic 验证前缀匹配语义。
func TestPrefixMatcher_Basic(t *testing.T) {
	m := NewPrefixMatcher()
	if err := m.AddPattern("/tmp/foo"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/tmp/foo", true},
		{"/tmp/foo/bar", true},
		{"/tmp/foo/baz/qux", true},
		{"/tmp/foobar", false}, // 边界：前缀+/ 必须严格
		{"/tmp", false},
		{"/etc/foo", false},
	}
	for _, tc := range cases {
		got := m.Match(tc.path)
		if got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestPrefixMatcher_CrossPlatform 验证 Windows 路径自动 ToSlash。
func TestPrefixMatcher_CrossPlatform(t *testing.T) {
	m := NewPrefixMatcher()
	if err := m.AddPattern("C:/Users/foo"); err != nil {
		t.Fatal(err)
	}
	// 即使调用方传入反斜杠路径，也应命中
	if !m.Match("C:\\Users\\foo\\bar") {
		t.Error("Windows backslash path should match")
	}
}

// TestCompositeMatcher_Basic 验证复合 Matcher OR 语义。
func TestCompositeMatcher_Basic(t *testing.T) {
	glob := NewGlobMatcher()
	if err := glob.AddPattern("*.env"); err != nil {
		t.Fatal(err)
	}
	prefix := NewPrefixMatcher()
	if err := prefix.AddPattern("/secrets"); err != nil {
		t.Fatal(err)
	}
	composite := NewCompositeMatcher(glob, prefix)

	cases := []struct {
		path string
		want bool
	}{
		{"foo.env", true},          // glob 命中
		{"/secrets/api.key", true}, // prefix 命中
		{"bar.txt", false},         // 都不命中
		{"/etc/passwd", false},
	}
	for _, tc := range cases {
		got := composite.Match(tc.path)
		if got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestPathMatcher_Interface 验证 GlobMatcher/PrefixMatcher/CompositeMatcher 都满足 PathMatcher 接口。
func TestPathMatcher_Interface(t *testing.T) {
	var _ PathMatcher = NewGlobMatcher()
	var _ PathMatcher = NewPrefixMatcher()
	var _ PathMatcher = NewCompositeMatcher()
	var _ PathMatcher = NewPathMatcher()
}

// TestMatchAny_Convenience 验证包级便捷函数。
func TestMatchAny_Convenience(t *testing.T) {
	// patterns 对标 secret.DefaultSensitivePatterns（whaleignore.go）
	// .env / .env.* 覆盖 .env / .env.local / .env.production 等
	patterns := []string{".env", ".env.*", ".aws/*", "*.pem"}
	cases := []struct {
		path string
		want bool
	}{
		{".env", true},            // .env 精确命中
		{".env.local", true},      // .env.* 命中
		{".env.production", true}, // .env.* 命中
		{".aws/credentials", true},
		{"id_rsa.pem", true},
		{"README.md", false},
		{"src/main.go", false},
	}
	for _, tc := range cases {
		got := MatchAny(tc.path, patterns)
		if got != tc.want {
			t.Errorf("MatchAny(%q, patterns) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestGlobMatcher_Concurrent 验证并发安全（-race 检测）。
func TestGlobMatcher_Concurrent(t *testing.T) {
	m := NewGlobMatcher()
	for _, p := range []string{"*.go", "src/**", "!*_test.go"} {
		_ = m.AddPattern(p)
	}
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_ = m.Match("src/foo.go")
				_ = m.Patterns()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestGlobMatcher_GitignoreSemantics 验证 .gitignore 风格语义。
//
// 对标 https://git-scm.com/docs/gitignore：
// - 空行忽略
// - # 注释（本实现保留 # 作为字面量，调用方预处理注释）
// - ! 取反
// - ** 递归
func TestGlobMatcher_GitignoreSemantics(t *testing.T) {
	m := NewGlobMatcher()
	patterns := []string{
		"*.log",
		"build/",
		"node_modules/",
		"!important.log",
		"src/**/*.tmp",
	}
	for _, p := range patterns {
		if err := m.AddPattern(p); err != nil {
			t.Fatalf("AddPattern(%q) error: %v", p, err)
		}
	}
	cases := []struct {
		path string
		want bool
	}{
		{"app.log", true},          // *.log 命中
		{"important.log", false},   // !important.log 取反
		{"build/main", true},       // build/ 命中（dirOnly 前缀匹配）
		{"node_modules/pkg", true}, // node_modules/ 命中
		{"src/a/b/c.tmp", true},    // src/**/*.tmp 命中（** 匹配 a/b）
		{"src/a.tmp", true},        // src/**/*.tmp 命中（** 匹配 0 段，*.tmp 匹配 a.tmp）
		{"src/just-dir", false},    // 不匹配 *.tmp 模式
		{"README.md", false},       // 不命中
	}
	for _, tc := range cases {
		got := m.Match(tc.path)
		if got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
