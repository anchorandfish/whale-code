package github

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// TestW4_AssemblyFromEnv_WritesPredicate 判据 ④：装配函数把「有没有凭据」写进唯一谓词。
//
// 这是 R1b 意义上的**接线断言**：不是断言"某字段非 nil"，而是**调用**生产装配函数
// （main.go 咽喉点调的就是它，只差一个 os.Getenv 绑定）后，观察它**真写进去的值**。
//
// 判据用注入 getenv 驱动、不读进程 env ⇒ 结论不随开发机的 GITHUB_TOKEN 而飘（环境相关假绿）。
//
// ★ before 态无对照实现（装配函数与 tools.ToolAvailable 都是本次新增符号），
// 故本条的判别力靠**注入变体**自证：把 availability.go 的判据改成不去空白的
// `getenv(GitTokenEnvVar) != ""` ⇒ 下面"只含空白"那一例必须红（已实测）。
func TestW4_AssemblyFromEnv_WritesPredicate(t *testing.T) {
	resetAvailability(t)

	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"未设置（空串）", "", false},
		{"只含空白", "   \t\n", false},
		{"正常 token", "ghp_" + strings.Repeat("a", 36), true},
	}
	for _, c := range cases {
		applyToolAvailabilityFrom(func(string) string { return c.value })
		for _, n := range w4ToolNames {
			if got := tools.ToolAvailable(n); got != c.want {
				t.Errorf("%s：%s 的可用性应为 %v，实际 %v", c.name, n, c.want, got)
			}
		}
	}
}

// TestW4_TicketNamesMatchSpecTable 漂移守卫：票面 12 个名字必须与声明表**完全一致**。
//
// 与行为判据分工不同，**勿合并**（production-path-tdd「守卫是分工的，不是互相兜底的」）：
//
//   - 行为判据管「能力面与工具面是否一致」（行为维）；
//   - 本条管「票面（检索联网_TEST_GUIDE.md §T-5）与代码是不是同一份名单」（名单维）。
//
// 响应差异（刻意）：新增一个工具但两张名单都没动 ⇒ 本条**红**（抓得到）；
// 名单里加了名字而声明表没有 ⇒ 本条**绿**（它比的是"票面 vs 声明表"，不是"实现 vs 期望"）。
// 另：`ApplyToolAvailability` 遍历的就是 `toolSpecs` ⇒ 新工具自动获得谓词，
// 不存在"忘了给新工具接能力面"这条漂移路径。
func TestW4_TicketNamesMatchSpecTable(t *testing.T) {
	got := map[string]bool{}
	for _, s := range toolSpecs {
		got[s.Name] = true
	}
	for _, name := range w4ToolNames {
		if !got[name] {
			t.Errorf("票面列了 %s，声明表 toolSpecs 里没有", name)
		}
		delete(got, name)
	}
	for name := range got {
		t.Errorf("声明表里有 %s，票面（TEST_GUIDE §T-5）没列 —— 请同步票面", name)
	}
}
