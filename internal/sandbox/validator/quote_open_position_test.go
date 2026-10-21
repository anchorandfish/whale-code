package validator

// 止血：LOCALE-Q / ANSIC-Q 引号位感知。
//
// 病灶（真机 T-3 误伤实锤，2026-10-14 01:20 会话）：
//	`git ls-files | grep -E "\.go$"` 被硬拒、一行不跑。
//	原实现 `\$"` / `\$'` 是**纯子串匹配**：正则行尾锚点 `$` 紧贴闭引号
//	恰好拼出字面 `$"`（单引号版同理 `$'`），与 bash 的 locale 引用
//	`$"text"` / ANSI-C 引用 `$'text'` 毫无关系。
//
// 修复语义：`$` 必须紧贴**开启**引号位（前一字符不是 word 字符）才判为
// 引用混淆 —— word 字符后跟 `$` 只可能是正则锚点/普通文本。
//
// 本文件两个方向都必须可判别（先红后绿）：
//	- 误伤组（修复后应放行）：改前必红；
//	- 真混淆组（修复后仍拦截）：改前改后都绿 —— 防止"修误伤"变成"放空"。

import "testing"

// TestW093_QuoteAnchor_FalsePositivesPass 误伤组：正则锚点/普通文本紧贴闭引号。
func TestW093_QuoteAnchor_FalsePositivesPass(t *testing.T) {
	cases := []string{
		`git ls-files | grep -E "\.go$"`,     // T-3 真机误伤原文（LOCALE-Q 原命中）
		`grep -E '\.go$' internal/util/*.go`, // 单引号版行尾锚点（ANSIC-Q 原命中）
		`grep "abc$" f.txt`,                  // 双引号内的行尾锚点
		`grep 'abc$' f.txt`,                  // 单引号内的行尾锚点
		`sed 's/foo$/bar/' f.txt`,            // sed 行尾锚点
		`awk '/\.go$/{print}' f.txt`,         // awk 行尾锚点
	}
	for _, cmd := range cases {
		for _, r := range NewValidatorWithoutAST().Run(cmd) {
			if r.CheckID == "LOCALE-Q" || r.CheckID == "ANSIC-Q" {
				t.Errorf(" 误伤: %q 不应命中 %s（$ 紧贴闭引号是正则锚点，不是 locale/ANSI-C 引用）\n  结果: %s",
					cmd, r.CheckID, r.Message)
			}
		}
	}
}

// TestW093_QuoteAnchor_TruePositivesStillBlocked 真混淆组：真正的引用语法仍拦。
func TestW093_QuoteAnchor_TruePositivesStillBlocked(t *testing.T) {
	cases := []struct {
		cmd string
		id  string
	}{
		{`echo $"hello"`, "LOCALE-Q"},
		{`echo $"hello world"`, "LOCALE-Q"},
		{`FOO=$"x"; echo $FOO`, "LOCALE-Q"},
		{`echo $'it'`, "ANSIC-Q"},
		{`echo $'\x41'`, "ANSIC-Q"},
	}
	for _, tc := range cases {
		found := false
		for _, r := range NewValidatorWithoutAST().Run(tc.cmd) {
			if r.CheckID == tc.id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf(" 回退: %q 是真正的引用混淆，应命中 %s（不得因修误伤而放空）", tc.cmd, tc.id)
		}
	}
}
