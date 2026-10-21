// Package command — ShellSplit 单元测试
package command

import (
	"reflect"
	"strings"
	"testing"
)

// TestShellSplit_Coverage 验证 ShellSplit 对 POSIX shell 引用规则的完整覆盖
// 覆盖：简单参数、单引号、双引号、转义、嵌套、续行、未闭合引号、空字符串、中文
func TestShellSplit_Coverage(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{
			name:  "简单参数",
			input: "a b c",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "单引号包裹带空格参数",
			input: "a 'b c' d",
			want:  []string{"a", "b c", "d"},
		},
		{
			name:  "双引号包裹带空格参数",
			input: `a "b c" d`,
			want:  []string{"a", "b c", "d"},
		},
		{
			name:  "反斜杠转义空格",
			input: `a b\ c d`,
			want:  []string{"a", "b c", "d"},
		},
		{
			name:  "嵌套引号（单引号内双引号字面）",
			input: `'a "b"' 'c d'`,
			want:  []string{`a "b"`, "c d"},
		},
		{
			name:  "续行（反斜杠换行被消除）",
			input: "a b\\\nc d",
			want:  []string{"a", "bc", "d"},
		},
		{
			name:    "未闭合单引号报错",
			input:   "a 'b c",
			wantErr: true,
		},
		{
			name:    "未闭合双引号报错",
			input:   `a "b c`,
			wantErr: true,
		},
		{
			name:  "空字符串参数（单引号）",
			input: "a '' b",
			want:  []string{"a", "", "b"},
		},
		{
			name:  "空字符串参数（双引号）",
			input: `a "" b`,
			want:  []string{"a", "", "b"},
		},
		{
			name:  "中文参数",
			input: "/feedback 这是反馈",
			want:  []string{"/feedback", "这是反馈"},
		},
		{
			name:  "空输入返回空切片",
			input: "",
			want:  nil, // ShellSplit 对空输入返回 nil，与 strings.Fields 行为一致
		},
		{
			name:  "纯空白返回空切片",
			input: "   \t\n  ",
			want:  nil,
		},
		{
			name:  "单个参数",
			input: "hello",
			want:  []string{"hello"},
		},
		{
			name:  "多个连续空白",
			input: "a   b",
			want:  []string{"a", "b"},
		},
		{
			name:  "混合引号与转义",
			input: `arg1 'quoted "with" double' "single 'inside'" end\ space`,
			want:  []string{"arg1", `quoted "with" double`, "single 'inside'", "end space"},
		},
		{
			name:  "双引号内反斜杠转义美元符号",
			input: "\"\\$\"",
			want:  []string{"$"},
		},
		{
			name:  "双引号内反斜杠转义反引号",
			input: "\"\\`\"",
			want:  []string{"`"},
		},
		{
			name:  "双引号内反斜杠转义双引号",
			input: "\"\\\"\"",
			want:  []string{"\""},
		},
		{
			name:  "双引号内反斜杠转义反斜杠",
			input: "\"\\\\\"",
			want:  []string{"\\"},
		},
		{
			name:  "双引号内其他字符反斜杠保留字面",
			input: `"a\nb" "\d"`,
			want:  []string{`a\nb`, `\d`},
		},
		{
			name:  "单引号内反斜杠字面",
			input: `'a\nb' 'c\d'`,
			want:  []string{`a\nb`, `c\d`},
		},
		{
			name:  "单引号内双引号字面",
			input: `'has "double" quotes'`,
			want:  []string{`has "double" quotes`},
		},
		{
			name:  "双引号内单引号字面",
			input: `"has 'single' quotes"`,
			want:  []string{`has 'single' quotes`},
		},
		{
			name:  "反斜杠转义引号",
			input: `a \"b\" c`,
			want:  []string{"a", `"b"`, "c"},
		},
		{
			name:  "反斜杠转义反斜杠",
			input: `a b\\c d`,
			want:  []string{"a", `b\c`, "d"},
		},
		{
			name:  "续行后接续参数",
			input: "cmd arg1\\\narg2 arg3",
			want:  []string{"cmd", "arg1arg2", "arg3"},
		},
		{
			name:  "tab 分隔符",
			input: "a\tb\tc",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "CR 字符作为空白",
			input: "a\r\nb",
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ShellSplit(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ShellSplit(%q) 期望错误，实际 err=nil, got=%v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ShellSplit(%q) 不期望错误, 实际 err=%v", tt.input, err)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ShellSplit(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

// TestShellSplit_TaskSpecCases 验证任务规格中明确列出的全部测试用例
// 这是改造项的验收基准，任何变更都必须保持这些用例通过
func TestShellSplit_TaskSpecCases(t *testing.T) {
	cases := []struct {
		input   string
		want    []string
		wantErr bool
	}{
		{"a b c", []string{"a", "b", "c"}, false},
		{"a 'b c' d", []string{"a", "b c", "d"}, false},
		{`a "b c" d`, []string{"a", "b c", "d"}, false},
		{`a b\ c d`, []string{"a", "b c", "d"}, false},
		{`'a "b"' 'c d'`, []string{`a "b"`, "c d"}, false},
		{"a b\\\nc d", []string{"a", "bc", "d"}, false},
		{"a 'b c", nil, true},
		{"a '' b", []string{"a", "", "b"}, false},
		{"/feedback 这是反馈", []string{"/feedback", "这是反馈"}, false},
	}
	for i, tc := range cases {
		got, err := ShellSplit(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("case %d: ShellSplit(%q) 期望错误, got=%v", i, tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("case %d: ShellSplit(%q) 不期望错误, err=%v", i, tc.input, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("case %d: ShellSplit(%q) = %#v, want %#v", i, tc.input, got, tc.want)
		}
	}
}

// TestShellSplitOrFields_Fallback 验证错误输入降级到 strings.Fields
// 未闭合引号场景应回退到 strings.Fields 而非返回空切片
func TestShellSplitOrFields_Fallback(t *testing.T) {
	// 未闭合引号 → 降级
	got := ShellSplitOrFields("a 'b c")
	if !reflect.DeepEqual(got, []string{"a", "'b", "c"}) {
		t.Errorf("降级路径 ShellSplitOrFields(%q) = %#v, want [a 'b c]", "a 'b c", got)
	}

	// 正常输入 → ShellSplit 结果
	got = ShellSplitOrFields("a 'b c' d")
	if !reflect.DeepEqual(got, []string{"a", "b c", "d"}) {
		t.Errorf("正常路径 ShellSplitOrFields(%q) = %#v, want [a b c d]", "a 'b c' d", got)
	}

	// 空输入 → 空切片（与 strings.Fields 一致）
	got = ShellSplitOrFields("")
	if len(got) != 0 {
		t.Errorf("空输入 ShellSplitOrFields(\"\") = %#v, want []", got)
	}

	// 纯空白 → 空切片
	got = ShellSplitOrFields("   ")
	if len(got) != 0 {
		t.Errorf("纯空白 ShellSplitOrFields(\"   \") = %#v, want []", got)
	}
}

// TestShellSplit_TrailingBackslash 验证行尾单独的反斜杠报错
// POSIX 不允许行尾单独的 \（要么转义下一字符，要么是行续接）
func TestShellSplit_TrailingBackslash(t *testing.T) {
	_, err := ShellSplit(`a b\`)
	if err == nil {
		t.Error("ShellSplit(\"a b\\\\\") 应返回错误（行尾单独反斜杠），实际 err=nil")
	}
	if !strings.Contains(err.Error(), "trailing backslash") {
		t.Errorf("错误信息应包含 'trailing backslash', 实际: %v", err)
	}
}

// TestShellSplit_UnicodeSupport 验证多字节字符（中文/emoji/日文）按 rune 正确处理
// 避免被字节级解析截断
func TestShellSplit_UnicodeSupport(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "中文带空格参数",
			input: "/orchestrate start \"实现登录功能\"",
			want:  []string{"/orchestrate", "start", "实现登录功能"},
		},
		{
			name:  "日文参数",
			input: "/feedback これはテストです",
			want:  []string{"/feedback", "これはテストです"},
		},
		{
			name:  "emoji 参数",
			input: "/feedback 🐋 bug report",
			want:  []string{"/feedback", "🐋", "bug", "report"},
		},
		{
			name:  "中文双引号包裹",
			input: `/feedback "这是 反馈 内容"`,
			want:  []string{"/feedback", "这是 反馈 内容"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ShellSplit(tc.input)
			if err != nil {
				t.Fatalf("ShellSplit(%q) err=%v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ShellSplit(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}
