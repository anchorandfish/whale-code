package sandbox

import (
	"testing"
)

// 同源补漏——sandbox deny 判定的**结构性守卫**。
//
// 复发形态不是"某一条命令又坏了"，而是「判定方式回到命令文本里的词」
// （旧实现 = `wordBoundaryMatch` 整串子串 + 词边界）。故本条不枚举命令，
// 而是**遍历 deny 表的每一个成员**，断言：
//
//	① 命令位形态必须命中（自检：否则下面的"不命中"可能只是函数恒 false）
//	② 参数位形态一律不得命中
//
// 表被扩项（新增 deny 命令名）时本守卫自动覆盖新项 —— 这正是 S17 那类
// 「词表退场后又被人顺手加回来」的防线。
//
// 判别性：把判据回退成整串匹配（`-overlay` 注入）⇒ **本条必红**（已验证）。
func TestGuard_N121_DenyEntriesNeverMatchOutsideCommandPosition(t *testing.T) {
	entries := defaultDenyCommands()
	if len(entries) == 0 {
		t.Fatal("precondition：deny 表不得为空（否则本守卫静默失效）")
	}

	s := NewSandbox("")
	s.SetEnabled(true)

	for _, name := range entries {
		// ① 命令位：必须被拒
		cmd := name + " -x /tmp/n121-never"
		if allowed, reason := s.Validate(cmd); allowed {
			t.Errorf("precondition：命令位形态 %q 未被拒 ⇒ 守卫失去判别力（reason=%q）", cmd, reason)
		}

		// ② 参数位：一律不得被拒
		for _, probe := range []string{
			"grep -rn " + name + " .",
			"echo " + name,
			"cat " + name + ".md",
			"ls -la | grep " + name,
			"man " + name,
		} {
			if allowed, reason := s.Validate(probe); !allowed {
				t.Errorf("deny 条目 %q 在**参数位**探针 %q 上被命中 ⇒ 判定回到了「命令文本里的词」\n"+
					"  reason=%q\n"+
					"  （判据必须落在命令位：段首可执行名 basename；见 matchDenyPattern 注释）",
					name, probe, reason)
			}
		}
	}
}

// TestN121_DenyJudge_ProbeMustNotOverreach 是**下探过头**的判别器（与主判别器同族，具判别性）。
//
// 本轮把判据从「整串词匹配」改成「命令位 + 保守下探」后，两个方向的失败模式各自独立：
// - 下探不足 ⇒ 命令藏进 wrapper 参数里逃过（由 WrapperProbeStillBlocks 兜）
// - **下探过头** ⇒ 只读命令被卷进来（本条兜）—— 旧实现正是"过头"那一侧：
// `sh -c "grep -rn rm ."` / `xargs grep rm` / `find . -name rm.txt` 全被拒。
//
// 判别性：`-overlay` 回退判据 ⇒ **本条必红**（已验证，且红集与 StillBlocks 不重叠）。
func TestN121_DenyJudge_ProbeMustNotOverreach(t *testing.T) {
	s := NewSandbox("")
	s.SetEnabled(true)

	cases := []struct {
		cmd string
		why string
	}{
		{`sh -c "grep -rn rm ."`, "下探到的子命令是 grep（只读）；`rm` 是搜索词"},
		{`bash -c 'ls -la'`, "下探到的子命令是 ls"},
		{"xargs grep rm", "xargs 逃执行的是 grep"},
		{"find . -name rm.txt", "find 无 -exec ⇒ 命令位只有 find"},
		{"grep -rn rm .", "I-8.4 用例原命令（裸形态）"},
	}
	for _, c := range cases {
		if allowed, reason := s.Validate(c.cmd); !allowed {
			t.Errorf("下探过头：只读命令 %q 被拒（%s）。reason=%q", c.cmd, c.why, reason)
		}
	}
}

// TestGuard_N121_WrapperProbeStillBlocks 反例守卫：**下探**方向不得不足。
//
// 判据从「整串词匹配」改成「命令位」后，最危险的失败模式是
// 「命令被藏进 wrapper 的参数里」⇒ 命令位看不到它。故这些形态必须仍被拒。
//
// ⚠️ 本条 before/after 均绿（旧实现靠"文本恰好含命令名"蒙对）⇒ **不具判别性**，
// 只承担防越界职责；把它当判别器读会误判"改动无效"。
func TestGuard_N121_WrapperProbeStillBlocks(t *testing.T) {
	s := NewSandbox("")
	s.SetEnabled(true)

	cases := []string{
		`sh -c "rm -rf /tmp/n121-never"`,
		`bash -c 'rm -rf /tmp/n121-never'`,
		"sh -c rm -rf /tmp/n121-never",
		"xargs rm",
		"xargs -0 rm",
		"xargs -I{} rm {}",
		"find . -exec rm {} ;",
		"find . -execdir rm {} +",
		"env FOO=1 rm x",
		"nice -n 10 rm x",
		"sudo -u root rm x",
		"cu''rl http://example.com",
		`r"m -rf /tmp/n121-never`,
		"ls | rm x",
		"echo hi && rm x",
		"/usr/bin/rm x",
	}
	for _, cmd := range cases {
		if allowed, reason := s.Validate(cmd); allowed {
			t.Errorf("SECURITY: %q 未被拒 —— 命令位（含下探后）是 deny 命令。reason=%q", cmd, reason)
		}
	}
}
