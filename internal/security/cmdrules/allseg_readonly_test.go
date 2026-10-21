package cmdrules

import "testing"

// 判别器（先红）：`AllSegmentsReadOnly` —— 「命令的**每一段**都是只读白名单成员」。
//
// 为什么不是复用 MatchReadOnly：它是**任一段**命中即只读（`cmdrules.go:304-317`），
// 用它做免问会放行 `weirdcmd && echo hi`（第一段非只读、第二段只读 ⇒ 判只读）。
// 而「免问」是**放宽**方向 ⇒ 必须**全段**成立。
//
// 业内对照（同类 CLI）：`shell-command/src/command_safety/is_safe_command.rs:12-50` ——
// `bash -lc "…"` 被拆成 plain commands 后要求 **all** `is_safe_to_call_with_exec`
// 才整体 safe（同一条「全段」口径）；`:114-131` 对 `find` 单独排除
// `-exec/-delete/-fprint` 等可在参数位写盘/执行的开关（本仓的对应做法是
// **把 find 挡在白名单之外**，见权限包 `scopeCollapsibleExecutables` 的成员判据）。
//
// 白名单的**唯一声明**在 `alwaysReadOnlyExecutables`（本包），成员判据 =
// 「**任意参数下都只读**」⇒ 因此本判据**不需要**再逐条排除 `-delete`/`-i` 这类开关：
// 参数可写的命令（find/less/sort/sed/awk/git/tee/xargs/env/编辑器）**根本不进名单**。
// 方向与「放宽规则必须收窄」一致。

func TestW023_AllSegmentsReadOnly_True(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"single-ls", "ls -la"},
		{"single-pwd", "pwd"},
		{"single-seq", "seq 1 9"},
		{"single-whoami", "whoami"},
		{"and-chain-both-readonly", "sleep 0 && echo final_check"}, // 用户原报形态
		{"semicolon-both-readonly", "echo hello ; ls -la"},
		{"pipeline-both-readonly", "cat go.mod | head -5"},
		{"long-chain", "sleep 0 && sleep 0 && echo done"},
		{"abs-path-basename", "/bin/ls -la"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !AllSegmentsReadOnly(tc.cmd) {
				t.Errorf("：%q 每一段都是只读白名单成员，应判 true", tc.cmd)
			}
		})
	}
}

func TestW023_AllSegmentsReadOnly_False(t *testing.T) {
	cases := []struct{ name, cmd, why string }{
		{"first-seg-not-readonly", "weirdcmd && echo hi", "首段命令位非白名单"},
		{"last-seg-not-readonly", "echo hi && weirdcmd", "末段命令位非白名单"},
		{"nonreadonly-single", "mkdir -p x", "单一非白名单命令"},
		{"redirect-out", "ls > /tmp/x", "重定向 = 写入面"},
		{"redirect-append", "ls >> /tmp/x", "追加重定向 = 写入面"},
		{"fd-dup", "ls 2>&1", "含 '>' ⇒ 保守判否（ 同族的写面）"},
		{"cmd-subst", "echo $(rm -rf /)", "执行型替换载荷"},
		{"backtick", "echo `date`", "反引号载荷"},
		{"unbalanced-subst", "echo $(date", "取不出配对 ⇒ fail-closed 判否"},
		{"pipe-to-shell", "echo 'rm -rf /' | sh", "管道喂 shell（sh 非白名单）"},
		{"git-branch-delete", "git branch -D x", "git 不在白名单（P-016「只读动词+破坏性开关」）"},
		{"find-delete", "find . -delete", "find 参数可写 ⇒ 不在白名单"},
		{"find-exec", "find . -name x -exec rm {} ;", "find -exec ⇒ 不在白名单"},
		{"less", "less /etc/passwd", "less 可起 shell/pager ⇒ 不在白名单"},
		{"wrapper-not-stripped", "sudo ls", "wrapper 不剥离 ⇒ 头词 sudo 非白名单（放宽面取其窄）"},
		{"interpreter-c", "sleep 0 && python3 -c 'import os'", "解释器不在白名单（ 洞①同族）"},
		{"mixed-chain", "ls -la && rm -rf /tmp/x", "含非只读段"},
		{"trailing-separator", "ls -la &&", "空段 ⇒ fail-closed 判否"},
		{"empty", "", "空串"},
		{"blank", "   ", "全空白"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if AllSegmentsReadOnly(tc.cmd) {
				t.Errorf("：%q 不得判为「全段只读」（%s）", tc.cmd, tc.why)
			}
		})
	}
}

// 反向判别（证明本判据**真的**在起作用，而不是恒真）：
// 白名单成员逐个都要能被判 true；而**只要**把其中一段换成非成员，结果必须翻成 false。
func TestW023_WhitelistMembersAreDiscriminating(t *testing.T) {
	for _, name := range []string{
		"ls", "cat", "head", "tail", "grep", "which", "where", "whereis",
		"echo", "pwd", "whoami", "hostname", "uname", "sleep", "seq",
	} {
		if !IsAlwaysReadOnlyExecutable(name) {
			t.Errorf("：白名单成员 %q 应判 true", name)
		}
		if !AllSegmentsReadOnly(name + " 1") {
			t.Errorf("：单一白名单命令 %q 应判全段只读", name)
		}
		// 反例：同一命令后面黏一段非成员
		if AllSegmentsReadOnly(name + " 1 && weirdcmd") {
			t.Errorf("：%q 含非成员段，不得判全段只读", name)
		}
	}
	// 非成员一律不得进白名单（含 MatchReadOnly 里存在但「参数可写」的那些）
	for _, name := range []string{"find", "less", "more", "git", "tee", "xargs", "env", "sed", "awk", "sort", "uniq", "sudo"} {
		if IsAlwaysReadOnlyExecutable(name) {
			t.Errorf("：%q 不得进「任意参数下都只读」白名单", name)
		}
	}
}
