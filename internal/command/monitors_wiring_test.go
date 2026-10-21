package command

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// monitors_wiring_test.go —— /monitors 的「声明 ↔ 能力一致」与「未接线不得断言事实」判据。
//
// 背景（2026-09-30， 的复发形态）：
//
//	/monitors 是**双实现**：TUI 侧 `internal/tui/format.go handleMonitors` 支持
//	stop/tail/show；注册侧（本包）旧签名 `func() string` **在类型上收不到 args**，
//	却在 Usage 里承诺 `/monitors [stop <id>]` —— 做不到的承诺（声明与实际能力不一致）。
//	且旧 fallback 逐字返回 "No active monitors"：在 `OnMonitors == nil`（未接线）时
//	断言了一个运行期事实，正是 的反模式。
//
// ★ 本文件只钉 **command 侧**。真实分发入口是 `internal/tui/app.go handleCommand`，
// 而 TUI 由 `isTUICommandHandledLocally("monitors")` 短路 ⇒ 注册侧 Handler 在 TUI 下**不执行**。
// TUI 侧接线与端到端**不在本单范围**（见 内部工作回执 回执的「未端到端验证」声明）。

// TestMonitorsUsagePromiseIsCarriable —— C-3 结构面：Usage 的承诺必须能被类型承载。
//
// 这是本单的首选判据（真实分发入口在 tui 而 tui 不可改 ⇒ 用**结构面**而非端到端）：
//
//	不变量：声明（Usage 承诺了带参数的子命令）⟺ 能力（回调能收到 args）。
//
// before（HEAD）：`OnMonitors func() string` 的 NumIn()==0，而 Usage 含 "stop" ⇒ **红**。
// after：NumIn()==1 且形参为 string ⇒ 绿。
func TestMonitorsUsagePromiseIsCarriable(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("monitors")
	if !ok {
		t.Fatal("monitors 应在注册表中（否则 app.go 的 cmdRegistry.Get(name) 闸直接判 Unknown）")
	}

	field, ok := reflect.TypeOf(HandlerCallbacks{}).FieldByName("OnMonitors")
	if !ok {
		t.Fatal("HandlerCallbacks 应有 OnMonitors 字段")
	}
	ft := field.Type

	if strings.Contains(cmd.Usage, "stop") {
		if ft.NumIn() != 1 {
			t.Errorf("做不到的承诺：Usage=%q 承诺了带参数的子命令，但 OnMonitors 的形参个数为 %d —— 收不到 args",
				cmd.Usage, ft.NumIn())
			return
		}
		if k := ft.In(0).Kind(); k != reflect.String {
			t.Errorf("OnMonitors 的唯一形参应为 string，实际 %v", k)
		}
	}
	if ft.NumOut() != 1 || ft.Out(0).Kind() != reflect.String {
		t.Errorf("OnMonitors 应返回单个 string（与 Handler 的 (string, error) 装配一致），实际 %v", ft)
	}
}

// TestMonitorsHandlerForwardsArgs —— C-3 行为面：args 不得被丢弃。
//
// ★ 本判据在 before 下**不可编译**（`func(args string) string` 无法赋给 `func() string`）——
// 这正是"类型上不可表达"的证据；本单的 before 红以编译失败形式留档，
// 结构面那一条（TestMonitorsUsagePromiseIsCarriable）提供可编译的 before 红。
func TestMonitorsHandlerForwardsArgs(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("monitors")
	if !ok {
		t.Fatal("monitors 应在注册表中")
	}

	var gotArgs string
	called := false
	r.OnMonitors = func(args string) string {
		called = true
		gotArgs = args
		return "STUB-MONITORS-OUT"
	}

	out, err := cmd.Handler("stop m-1")
	if err != nil {
		t.Fatalf("/monitors stop m-1 执行失败: %v", err)
	}
	if !called {
		t.Fatal("已接线时 /monitors 必须调用 OnMonitors")
	}
	if gotArgs != "stop m-1" {
		t.Errorf("args 被丢弃：OnMonitors 收到 %q，期望 %q", gotArgs, "stop m-1")
	}
	if out != "STUB-MONITORS-OUT" {
		t.Errorf("回调返回值应原样透传，实际 %q", out)
	}
}

// TestMonitorsUnwiredDoesNotAssertState —— C-2：未接线时不得断言运行期事实。
//
// before（HEAD）：fallback 逐字返回 "No active monitors" ⇒ 命中 I-1 禁词 ⇒ 红。
//
// 不变量（三条都写 Errorf，不短路，便于 before 一次暴露全部）：
//
//	I-1 不断言：不得含任何运行期存在性/数量断言
//	I-2 诚实降级：必须明确说"未接线"（含子串 "not wired"）
//	I-3 形态：含汉字的行必须以 util.CopyIndent 开头（禁止行内并列 English / 中文）
func TestMonitorsUnwiredDoesNotAssertState(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("monitors")
	if !ok {
		t.Fatal("monitors 应在注册表中")
	}
	if r.OnMonitors != nil {
		t.Fatal("前置失败：本判据要求 OnMonitors 未接线（nil）才能测到 fallback 分支")
	}

	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("/monitors 执行失败: %v", err)
	}

	lower := strings.ToLower(got)
	for _, banned := range []string{"no active", "none", "no monitors", "no monitoring", "0 monitors", "暂无", "没有监控"} {
		if strings.Contains(lower, banned) {
			t.Errorf("I-1 被破坏：未接线时不得断言监控运行状态（命中禁词 %q）：%q", banned, got)
		}
	}
	if !strings.Contains(lower, "not wired") {
		t.Errorf("I-2 被破坏：未接线提示须含 'not wired'，实际 %q", got)
	}

	// 扫描循环由本测试自己写（**不**调用生产的 util.AssertChineseOnlyOnIndentedLines）：
	// 判据与实现同源会让实现写错时判据跟着错。字符集判定仍走同源的 util.HasHan。
	for _, line := range strings.Split(got, "\n") {
		if !util.HasHan(line) {
			continue
		}
		if !strings.HasPrefix(line, util.CopyIndent) {
			t.Errorf("I-3 被破坏：含汉字的行必须以 %q 开头（禁止行内并列）：%q", util.CopyIndent, line)
		}
	}
}

// TestMonitorsRegistrationAndBilingualDeclaration —— 注册闸 + C-4 双语声明形态。
//
// 形态 SSOT = 本仓既有的「DescriptionEN 英文主 / Description 中文辅」**字段分离**
// （见 internal/tui/completion_popup.go：primary = DescriptionEN、secondary = Description）。
//
// ★ 不把 Description 改成行内双语：它受 TestCommandDescriptions_AllChinese（须含中文）
// 与 _StartsWithVerb（首字须中文动词）双重约束，行内加英文主行会同时打红两条 —— 形态已由字段分离承担。
func TestMonitorsRegistrationAndBilingualDeclaration(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("monitors")
	if !ok {
		t.Fatal("注册项缺失：app.go handleCommand 的 cmdRegistry.Get(name) 闸会直接判 Unknown")
	}
	if cmd.DescriptionEN == "" {
		t.Error("DescriptionEN 为空：弹窗/建议面将没有英文主行")
	}
	if util.HasHan(cmd.DescriptionEN) {
		t.Errorf("DescriptionEN 含汉字（主行必须是可独立读懂的英文）：%q", cmd.DescriptionEN)
	}
	if !util.HasHan(cmd.Description) {
		t.Errorf("Description 应为中文辅行（受 TestCommandDescriptions_AllChinese 约束）：%q", cmd.Description)
	}
	if cmd.Usage == "" {
		t.Error("Usage 不应为空")
	}
	if cmd.Handler == nil {
		t.Error("Handler 不应为 nil（隐藏只影响可见性，手输仍需可执行）")
	}
}

// monitorsNoticeMainLine 是未接线提示的英文主行 —— 「文案是否被复制到第二处」的判别锚。
const monitorsNoticeMainLine = "Monitor control is not wired in this entry point"

// countNoticeSites 统计 dir 下**非测试** .go 文件中该文案出现的次数。
func countNoticeSites(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败 %s: %v", dir, err)
	}
	total := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		total += strings.Count(string(src), monitorsNoticeMainLine)
	}
	return total
}

// TestMonitorsUnwiredNoticeHasSingleSource —— 收敛守卫：未接线文案只能有唯一来源。
//
// 判别性用**反例输入自证**：临时目录里放两份重复文案 ⇒ 计数必须为 2。
// 若计数对重复视而不见，本守在真实目录上也会恒绿（假门）。
func TestMonitorsUnwiredNoticeHasSingleSource(t *testing.T) {
	// 前置：判据锚与实现必须仍在同一句话上 —— 否则计数会变成 0，
	// 报出来的会是"文案被复制"这种误导性结论。脱钩要显式失败。
	if !strings.Contains(monitorsNotWiredNotice(), monitorsNoticeMainLine) {
		t.Fatalf("前置失败：monitorsNotWiredNotice() 不含锚串 %q —— 判据与实现已脱钩，需同步本判据",
			monitorsNoticeMainLine)
	}

	// ① 判别力自证（反例输入）
	dir := t.TempDir()
	body := []byte("package command\n\n// " + monitorsNoticeMainLine + "\n")
	for _, n := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, n), body, 0o644); err != nil {
			t.Fatalf("写反例失败: %v", err)
		}
	}
	if got := countNoticeSites(t, dir); got != 2 {
		t.Fatalf("判别力自证失败：两处重复文案应计数为 2，实际 %d（该门对重复不敏感 ⇒ 假绿）", got)
	}

	// ② 真实面：命令包内该文案只许 1 处
	if got := countNoticeSites(t, "."); got != 1 {
		t.Errorf("未接线文案应只有唯一来源（SSOT），实际 %d 处 —— 别把同一句话复制到第二处", got)
	}
}
