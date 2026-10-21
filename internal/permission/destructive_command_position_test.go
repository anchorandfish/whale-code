package permission

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// 同源补漏——`permission.AnalyzeBashCommand` 的「破坏性命令名」判定
// 此前用本包私有的 `wordBoundaryMatch`（`\b` 整串词边界）遍历 `cmdrules.DestructiveCommandNames()`：
// 判的是「命令文本里有没有这个词」，不是「命令位是不是这个命令」。
//
// 与 sandbox 的 deny 判定（`matchDenyPattern`）是**同一判据的两处实现**，
// 也都与 S17/ 在幻觉检测侧删掉的 `bashFileOpRe=(?i)\b(rm|mv|cp)\b` 同源。
//
// 后果方向（比 sandbox 那处轻，但同样错）：破坏性档 = 「需用户确认」，
// 且分级顺序里破坏性档（第 3 步）**先于**只读档（第 5 步）⇒ 只读命令被拉进确认闸。
//
//	`grep -rn rm .` ⇒ Destructive（应 ReadOnly）
//	`grep -rn chmod Makefile` ⇒ Destructive（应 ReadOnly）
//	`ls -la | grep dd` ⇒ Destructive（应 ReadOnly）

// TestN121_DestructiveJudge_MustBeAtCommandPosition 是 permission 侧主判别器。
//
// BEFORE 必然红：下列命令的命令位是 grep / cat / echo（均在只读档表内），
// 参数位含 rm / chmod / dd 字样，但没有任何删除/改元数据行为。
func TestN121_DestructiveJudge_MustBeAtCommandPosition(t *testing.T) {
	cases := []struct {
		cmd string
		why string
	}{
		{"grep -rn rm .", "grep 只读（只读档表成员）；`rm` 是搜索词"},
		{"grep -rn chmod Makefile", "`chmod` 是搜索词"},
		{"ls -la | grep dd", "管道第二段命令位是 grep；`dd` 是搜索词"},
		{"cat rm.md", "读文件；文件名含 rm"},
		{"echo mv", "纯输出"},
	}
	for _, c := range cases {
		if got := AnalyzeBashCommand(c.cmd); got == BashRiskDestructive {
			t.Errorf("BEFORE 期望红：AnalyzeBashCommand(%q) = %s —— 但命令位是只读命令（%s）\n"+
				"  ⇒ 破坏性档 = 需确认，且第 3 步先于只读档第 5 步 ⇒ 只读命令被拉进确认闸\n"+
				"  ⇒ 判据必须落在**命令位**（cmdrules.MatchRiskDestructive），不是命令文本里的词",
				c.cmd, got, c.why)
		}
	}
}

// TestN121_DestructiveJudge_RealDestructiveCommandsStillClassified 防越界（before/after 均绿）。
//
// ⚠️ 本条**不具判别性**（两态皆绿），只承担回归职责：新判据不得把真该确认的放过去。
func TestN121_DestructiveJudge_RealDestructiveCommandsStillClassified(t *testing.T) {
	cases := []string{
		"rm x",
		"rm -rf build/",
		"ls | rm x",
		"mv a b",
		"sudo rm x",
		"env FOO=1 rm x",        // wrapper 后还有赋值（cmdrules.stripLeadingPrefixes 补漏项）
		"nice -n 10 rm x",       // wrapper 的带值选项（同上）
		"chmod +x build.sh",     // riskOnly 档：改元数据，仍须参与风险分级
		"sh -c \"rm -rf /tmp\"", // shell 下探（本轮 deny 侧新增，此处核对 permission 侧不受影响）
	}
	for _, cmd := range cases {
		// 判据 = 「不得被当成只读放过去」。危险档（Dangerous, Deny）比破坏性档
		// （Destructive, Ask）**更严**，命中危险档（如 `sh -c "rm -rf /tmp"` 命中短语档、
		// `mkfs arg` 命中命令位档）同样满足"没漏"。见 cmdrules 第 33-38 行：两档不可合并。
		if got := AnalyzeBashCommand(cmd); got == BashRiskReadOnly {
			t.Errorf("真破坏性命令被漏判为只读：AnalyzeBashCommand(%q) = %s，期望 ≥ %s", cmd, got, BashRiskDestructive)
		}
	}
}

// TestGuard_N121_DestructiveNamesNeverMatchOutsideCommandPosition 是**结构性**守卫（防复发）。
//
// 复发形态不是"某一条命令又坏了"，而是「有人把判定方式改回整串/子串匹配」。
// 故本条不枚举命令，而是**遍历破坏性命令名表的每一个成员**，断言把该名字
// 当**参数**用时一律不命中（命令位仍必须命中）。表被扩项时本守卫自动覆盖新项。
func TestGuard_N121_DestructiveNamesNeverMatchOutsideCommandPosition(t *testing.T) {
	names := cmdrules.DestructiveCommandNames()
	if len(names) == 0 {
		t.Fatal("precondition：破坏性命令名表不得为空（否则本守卫静默失效）")
	}
	for _, name := range names {
		// ① 命令位：必须命中（自检 —— 否则下面的"不命中"可能只是函数恒 false）。
		// 用「不得为只读」而非「必须为 Destructive」：`mkfs arg` 会命中危险档
		// （dangerousCommands 的命令位条目），那是**更严**的档，同样证明判据在位。
		if got := AnalyzeBashCommand(name + " arg"); got == BashRiskReadOnly {
			t.Errorf("precondition：命令位形态 %q 被判只读，got %s（守卫将失去判别力）", name+" arg", got)
		}
		// ② 参数位：一律不得命中
		for _, probe := range []string{
			"grep -rn " + name + " .",
			"echo " + name,
			"cat " + name + ".md",
			"ls -la | grep " + name,
		} {
			if got := AnalyzeBashCommand(probe); got == BashRiskDestructive {
				t.Errorf("破坏性命令名 %q 在参数位探针 %q 上被命中 ⇒ 判定回到了「命令文本里的词」\n"+
					"  （判据必须落在命令位：cmdrules.MatchRiskDestructive）", name, probe)
			}
		}
	}
}
