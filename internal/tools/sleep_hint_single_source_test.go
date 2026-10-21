package tools

// 同源护栏 —— 单独成文件，因为它**引用了新增符号**
// `monitor.MaxAllowedSleepSeconds`。
//
// ★ 编号沿革：本条总账里**曾登记为 **（与「面板批准下传」撞号），批 B 施工单 §1
// 建议顺延为 ****；总账改号是单独一步。
//
// 与 `sleep_hint_matches_gate_test.go` 的分工（R11：让"真语义红"与"编译失败"分开）：
// - matches_gate 文件**不引用任何新增符号** ⇒ 在**修前**即可编译 ⇒ 它的红是真语义红；
// - 本文件引用新符号 ⇒ 修前**编译不过**（这正是"API 变了"的证据，不是判据变红）。
//
// 共享 helper（w056NewEnv / w056RunSleep / w056OutputOf / w056HintLimitRe）
// 单一声明在 matches_gate 文件里（同包可见），本文件只声明这一条 Test。

import (
	"strconv"
	"testing"

	"github.com/anchorandfish/whale-code/internal/monitor"
)

// TestW056_SleepHintIntegerComesFromSingleDeclaration 考的是**同源**：
// 提示里的整数上界必须等于唯一声明 `monitor.MaxAllowedSleepSeconds`。
//
// 为什么主判据不够：主判据只断言「照提示做（sleep N）能被放行」，即 hint ≤ 实际上界。
// 它**测不出** hint < 实际上界（用户被吓住、明明能过却不敢用）那侧 —— 本判据两侧都钉。
//
// 判别性（★ 注意：本判据**不能**用 `-overlay` 判红 —— 提示是**运行期**产物，
// overlay 只改编译期输入；要判红须真实写回 + 无条件还原，见回执）：
// 若把两处提示退回"各写各的字面量"、或把 `MaxAllowedSleepSeconds` 改成 3 而提示仍写死，
// 本判据红。
func TestW056_SleepHintIntegerComesFromSingleDeclaration(t *testing.T) {
	want := monitor.MaxAllowedSleepSeconds
	for _, tool := range []string{"bash", "Monitor"} {
		t.Run(tool, func(t *testing.T) {
			e := w056NewEnv(t)
			hint := w056OutputOf(w056RunSleep(t, e, tool, "sleep 2"))
			m := w056HintLimitRe.FindStringSubmatch(hint)
			if m == nil {
				t.Fatalf("提示文本里读不出整数上界:\n%q", hint)
			}
			got, _ := strconv.Atoi(m[1])
			if got != want {
				t.Fatalf("提示里的上界 %d != 唯一声明 monitor.MaxAllowedSleepSeconds=%d "+
					"（文案可能被写死，）:\n%s", got, want, hint)
			}
		})
	}
}
