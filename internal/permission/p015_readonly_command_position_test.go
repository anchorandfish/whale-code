package permission

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
)

// P-015（2026-09-22 · `补丁台账_2026-09-22.md` §2.7 / `P-014_Monitor可用面_定义与方案_2026-09-22.md` §5）——
// 「只读档」判定此前是**裸子串**匹配（`permission.AnalyzeBashCommand` 里
// `strings.Contains(lower, c)` 遍历 `cmdrules.readOnlyPhrases`），而那张表含 `"ls"`（无尾空格）。
//
// 实测（`-overlay` 纯函数探针，`internal/security` 包）BEFORE 事实：
//
//	`go test ./internal/tools/` ⇒ **Allow**（路径含 `tools` ⇒ 裸子串 `ls` 命中）
//	`go test ./...` ⇒ Ask （不含 `ls` ⇒ 本来就 destructive）
//	`make -C tools` ⇒ Allow
//	`docker build -t tools .` ⇒ Allow
//	`false` / `lsblk` ⇒ Allow
//
// ⇒ **分界只取决于路径里有没有 `ls` 两个字母**，而 `go test` 会执行仓内任意代码。
// 只读档的消费面 = 「免确认」＋「沙箱在强制档下命中免问出口 (c)」＋
// 「审批记忆按可执行名合并的前置条件」⇒ 这是**危险的误报方向**（放宽，不是收紧）。
//
// 修法（结构性）：只读判定改走 `cmdrules.MatchReadOnly` —— 单 token 命令名一律按
// **命令位**匹配（与 `dangerousCommands` 同一纪律），只有多词形态保留「段首前缀」匹配。

// TestFix_P015_NonReadOnlyCommandsMustNotBeClassifiedReadOnly 是 P-015 的主判别器。
//
// BEFORE 必然红：上表 **5 条判别项全部**被判 `read-only`（`go test ./...` 是对照组，修前修后都 destructive）。
func TestFix_P015_NonReadOnlyCommandsMustNotBeClassifiedReadOnly(t *testing.T) {
	cases := []struct {
		cmd string
		why string
	}{
		{"go test ./internal/tools/", "路径含 `tools` ⇒ 裸子串 `ls` 命中；而 go test 执行仓内任意代码"},
		{"make -C tools", "目标目录含 ls；make 执行任意 Makefile"},
		{"docker build -t tools .", "镜像名含 ls；构建执行 Dockerfile"},
		{"false", "含 `ls`（f-a-l-s-e）；它本身也不是本表成员"},
		{"lsblk", "以 ls 开头；列块设备，不是本表成员"},
		{"go test ./...", "对照组：不含 ls ⇒ 修前修后都 destructive"},
	}

	for _, c := range cases {
		if got := AnalyzeBashCommand(c.cmd); got == BashRiskReadOnly {
			t.Errorf("BEFORE 期望红：AnalyzeBashCommand(%q) = %s，但它**不是只读**（%s）\n"+
				"  ⇒ 只读档误报 = 免确认 + 沙箱强制档下免问 ⇒ 方向是放宽（危险）",
				c.cmd, got, c.why)
		}
	}

	// 对照组必须是 destructive（证明上表的红不是"整张表都坏了"这种笼统现象）：
	if got := AnalyzeBashCommand("go test ./..."); got != BashRiskDestructive {
		t.Errorf("对照组 %q = %s，期望 %s（用于证明分界原本只取决于 `ls` 两个字母）",
			"go test ./...", got, BashRiskDestructive)
	}
}

// TestGuard_P015_TrulyReadOnlyCommandsStayReadOnly 防"改过头"：
// 真只读命令必须**保持**只读，否则用户会为 `ls -la` / `git status` 反复点确认。
//
// ⚠️ 本条**不具判别性**（before/after 两态均绿），只承担回归职责。
func TestGuard_P015_TrulyReadOnlyCommandsStayReadOnly(t *testing.T) {
	readOnly := []string{
		"ls -la", "cat file.txt", "grep pattern file", "which go",
		"pwd", "tail -f log.txt", "echo hello", "sleep 60",
		"git status", "git log", "git diff", "go version",
		"Get-ChildItem", "Get-Content file.txt",
		// 组合命令里只读段仍应被认出（破坏性/危险档先行，故这里只放只读组合）
		"ls -la && pwd",
	}
	for _, cmd := range readOnly {
		if got := AnalyzeBashCommand(cmd); got != BashRiskReadOnly {
			t.Errorf("真只读命令被误降级：AnalyzeBashCommand(%q) = %s，期望 %s", cmd, got, BashRiskReadOnly)
		}
	}
}

// TestGuard_P015_ReadOnlyEntriesNeverMatchAsBareSubstring 是**结构性**守卫（防复发）。
//
// P-015 的复发形态不是"这一条命令又坏了"，而是"有人往只读表里**再加一个短 token**"
// （`sh`、`cd`、`cp`……）—— 只要判定方式回到裸子串，同一个 bug 立刻回来。
// 故本条不枚举命令，而是**遍历只读表的每一个成员**，断言它在"被别的字母包住"时都不命中。
//
// ⚠️ 本守卫读的是**生产函数** `cmdrules.MatchReadOnly`（不是源码文本），
// 因此 `-overlay` 对它有效（R4-8 的读盘型陷阱不适用）。
func TestGuard_P015_ReadOnlyEntriesNeverMatchAsBareSubstring(t *testing.T) {
	entries := append(cmdrules.ReadOnlyCommands(), cmdrules.ReadOnlyPhrases()...)
	if len(entries) == 0 {
		t.Fatal("precondition：只读表不得为空（否则本守卫静默失效）")
	}
	for _, e := range entries {
		for _, probe := range []string{"x" + e, e + "zzz", "cmd-" + e + "-x", "./" + e + "/dir"} {
			if cmdrules.MatchReadOnly(probe) {
				t.Errorf("只读条目 %q 在探针 %q 上被**裸子串**命中 ⇒ 同一形态会立刻复发 P-015\n"+
					"  （只读判定必须落在命令位/段首，不能是 `strings.Contains`）", e, probe)
			}
		}
	}
	// 自检：真命令位仍必须命中（否则上一条的"不命中"可能只是函数恒 false）
	if !cmdrules.MatchReadOnly("ls -la") {
		t.Fatal("precondition：`ls -la` 必须命中只读（否则守卫恒绿、无判别力）")
	}
}
