package main

import (
	"strings"
	"testing"
)

// parseSessionFlag 的判别器：四种写法都要能取到值，缺值必须报错而不是静默忽略。
//
// 静默忽略的后果是 `whale --session`（用户漏了 ID）退化成「打开新会话」，
// 用户以为接上了旧对话 —— 所以「缺值 ⇒ 必须报错且 matched=true」是本用例的核心断言。
func TestParseSessionFlag(t *testing.T) {
	cases := []struct {
		name        string
		arg         string
		next        string
		wantValue   string
		wantMatched bool
		wantErr     bool
	}{
		{name: "长参数空格形式", arg: "--session", next: "abc-123", wantValue: "abc-123", wantMatched: true},
		{name: "长参数等号形式", arg: "--session=abc-123", wantValue: "abc-123", wantMatched: true},
		{name: "短参数空格形式", arg: "-s", next: "abc-123", wantValue: "abc-123", wantMatched: true},
		{name: "短参数等号形式", arg: "-s=abc-123", wantValue: "abc-123", wantMatched: true},

		{name: "缺值-无后续参数", arg: "--session", next: "", wantErr: true, wantMatched: true},
		{name: "缺值-下一个是别的开关", arg: "--session", next: "--debug", wantErr: true, wantMatched: true},
		{name: "缺值-等号后为空", arg: "--session=", wantErr: true, wantMatched: true},
		{name: "缺值-只有空白", arg: "-s", next: "   ", wantErr: true, wantMatched: true},

		{name: "不是本参数-无关开关", arg: "--debug", wantMatched: false},
		{name: "不是本参数-相近前缀", arg: "--sessions", wantMatched: false},
		{name: "不是本参数-fork", arg: "--fork-session", next: "abc", wantMatched: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, matched, err := parseSessionFlag(tc.arg, tc.next)
			if matched != tc.wantMatched {
				t.Fatalf("matched = %v, want %v", matched, tc.wantMatched)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应报错，实际 value=%q err=nil", value)
				}
				// 报错文案必须包含用法提示，否则用户不知道该怎么补。
				if !strings.Contains(err.Error(), "--session") {
					t.Errorf("错误信息应含用法提示，实际 %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if value != tc.wantValue {
				t.Errorf("value = %q, want %q", value, tc.wantValue)
			}
		})
	}
}
