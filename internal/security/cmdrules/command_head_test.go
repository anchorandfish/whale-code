package cmdrules

import "testing"

// 判别器（先红）：`CommandHead` / `IsSimpleCommand` 是「允许该命令前缀」
// 的**结构面判据**。
//
// 为什么这个判据必须存在（第一性原理）：
// 「允许前缀」天然是一条**放宽**规则 —— 一旦成立，后续调用不再询问。放宽规则
// 的边界必须由**结构**而不是由"再扫一遍危不危险"来划定：复合命令
// （`sleep 5 && rm -rf x`）的头部虽然也是 `sleep`，但它**不是**"以 sleep 开头的
// 简单调用"，而是一串调用。⇒ 判据 = 「恰好一段、无替换、无重定向」的单一简单命令。
// 复合/含替换/含重定向的命令**根本进不了前缀范围**，于是 `sleep 5 && rm -rf x`
// 不被 `sleep` 前缀豁免这件事是**结构性**保证，而不是"扫描后碰巧没命中"。
//
// 这四条用例在实现前必须红（`CommandHead` 未定义 ⇒ 编译失败）。

func TestW022_IsSimpleCommand_TrueForSingleSimpleSegments(t *testing.T) {
	cases := []string{
		"sleep 70",
		"sleep 60",
		"ls -la",
		"seq 1 3",
		"  seq   1   3  ", // 归一化后才判
		"/usr/bin/seq 1 9",
		"make build",
	}
	for _, cmd := range cases {
		if !IsSimpleCommand(cmd) {
			t.Errorf("IsSimpleCommand(%q) = false, want true（单一简单命令应在前缀范围内）", cmd)
		}
	}
}

func TestW022_IsSimpleCommand_FalseForCompoundOrIndirect(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"and-chain", "sleep 5 && rm -rf /tmp/x"},
		{"or-chain", "sleep 5 || echo fail"},
		{"semicolon", "sleep 5 ; echo done"},
		{"pipeline", "sleep 5 | cat"},
		{"background", "sleep 5 &"},
		{"trailing-sep", "sleep 5 &&"},
		{"cmd-substitution", "echo $(date)"},
		{"proc-substitution", "diff <(a) <(b)"},
		{"backtick", "echo `date`"},
		{"write-redirect", "sleep 1 > /tmp/x"},
		{"append-redirect", "sleep 1 >> /tmp/x"},
		{"read-redirect", "sleep 1 < /tmp/x"},
		{"imbalanced-subst", "echo $(date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsSimpleCommand(tc.cmd) {
				t.Errorf("IsSimpleCommand(%q) = true, want false（复合/含替换/含重定向不得进入前缀范围）", tc.cmd)
			}
		})
	}
}

func TestW022_CommandHead_SingleSimpleCommands(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"sleep 70", "sleep"},
		{"Sleep 70", "sleep"},       // 大小写不敏感
		{"  seq   1   3  ", "seq"},  // 归一化
		{"/usr/bin/seq 1 9", "seq"}, // 去路径 basename
		{"/bin/SLEEP 5", "sleep"},   // 路径 + 大小写
		{"FOO=1 seq 1 9", "foo=1"},  // 不剥环境赋值：前缀是"字面开头"，保守不交叉匹配
		{"sudo sleep 5", "sudo"},    // 不剥 wrapper：`sudo ...` 与 `sleep ...` 不得互相豁免
		{"sleep", "sleep"},          // 仅头词
	}
	for _, tc := range cases {
		if got := CommandHead(tc.cmd); got != tc.want {
			t.Errorf("CommandHead(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

func TestW022_CommandHead_EmptyForNonSimpleCommands(t *testing.T) {
	cases := []string{
		"sleep 5 && rm -rf /tmp/x",
		"echo $(date)",
		"sleep 1 > /tmp/x",
		"",
		"   ",
	}
	for _, cmd := range cases {
		if got := CommandHead(cmd); got != "" {
			t.Errorf("CommandHead(%q) = %q, want \"\"（非单一简单命令不得构成前缀）", cmd, got)
		}
	}
}

// 反向不变量：`sudo sleep 5` 与 `sleep 5` 必须是**两个不同的前缀**。
// 若两者同源，则用户批准 `sleep` 就等于顺带批准了 `sudo`（提权）—— 方向是放宽，不可接受。
func TestW022_CommandHead_WrapperDoesNotCollapse(t *testing.T) {
	if CommandHead("sudo sleep 5") == CommandHead("sleep 5") {
		t.Fatal("`sudo sleep 5` 与 `sleep 5` 的前缀必须不同（不剥 wrapper，防提权顺带豁免）")
	}
}
