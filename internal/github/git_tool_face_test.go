package github

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// w4ToolNames 是票面事实（`检索联网_TEST_GUIDE.md` §T-5 列的 12 个名字）。
//
// ★ 刻意**硬编码**，且本文件**只引用改动前就存在的符号**（tools.ResetToolAvailability /
// tools.SetToolAvailability / NewGitHubTools / RegisterSpecs / RegisterHandlers / NewRegistry /
// NewExecutor / Execute）：判据要在"改动前"的源码上也能编译并**真的跑出红** ——
// 否则 before 只会是 `[build failed]`，那是实验无效，不是判别性证据
// （production-path-tdd R4-6/R4-7；参 S3 的 search_surface_test.go 同款手法）。
//
// 本文件的角色 = **消费面**（注册点/执行门读到谓词后行为对不对）；
// 「谓词由环境派生」那一半在 github/availability_from_test.go 与根包
// git_availability_test.go（走真装配点）里判 —— 两条链各判一段，合起来才是完整闭环。
var w4ToolNames = []string{
	"github_create_pr", "github_review_pr", "github_list_prs", "github_get_pr_diff",
	"github_merge_pr", "github_list_issues", "github_create_issue", "github_get_issue",
	"github_list_workflows", "github_list_workflow_runs", "github_trigger_workflow",
	"github_rerun_workflow",
}

// resetAvailability 把工具面谓词复位到"默认全部可用"。
//
// 必须配 `t.Cleanup`：availabilityOverride 是**包级单例**，不复位会污染同包后续用例
// （production-path-tdd「夹具泄漏进程级状态」）。
func resetAvailability(t *testing.T) {
	t.Helper()
	tools.ResetToolAvailability()
	t.Cleanup(tools.ResetToolAvailability)
}

// setGitHubAvailability 直接写唯一谓词（**不经过** github 的装配函数）。
//
// 这样写是为了让本文件在 before 态可编译：装配函数本身是本次新增的符号。
// 「装配函数真的把 env 派生的值写进这里」由根包的 TestW4_EndToEnd_* 判（走真装配点）。
func setGitHubAvailability(available bool) {
	for _, name := range w4ToolNames {
		tools.SetToolAvailability(name, available)
	}
}

// TestW4_NoCredential_NoGitHubOnToolFace 判据 ①：**谓词说不可用 ⇒ 12 个 github_* 不进工具面**。
//
// 生产入口：github.RegisterSpecs —— 与 tui/lifecycle.go:902 同一个调用；
// 观测面：tools.Registry.AllDirect() —— buildToolDefs 的输入，即**模型真正看到的那份**。
//
// 反例注入（应打红，已实测）：把 specs.go 里 `if !tools.ToolAvailable(spec.Name) { continue }` 删掉。
func TestW4_NoCredential_NoGitHubOnToolFace(t *testing.T) {
	resetAvailability(t)
	setGitHubAvailability(false)

	reg := tools.NewRegistry()
	gt := NewGitHubTools(nil, t.TempDir())
	if err := gt.RegisterSpecs(reg); err != nil {
		t.Fatalf("RegisterSpecs: %v", err)
	}

	for _, name := range w4ToolNames {
		if _, ok := reg.Get(name); ok {
			t.Errorf("无凭据时 %s 不得进注册表（否则它会顺 AllDirect 下发给模型）", name)
		}
	}
	visible := map[string]bool{}
	for _, s := range reg.AllDirect() {
		visible[s.Name] = true
	}
	for _, name := range w4ToolNames {
		if visible[name] {
			t.Errorf("无凭据时 %s 出现在 AllDirect()（模型可见工具面）", name)
		}
	}
}

// TestW4_WithCredential_GitHubStaysOnToolFace 判据 ②（**反例守卫，非本轮的判别器**）：
// 谓词说可用 ⇒ 12 个必须都在（before/after 皆绿，刻意不具判别性）。
//
// 没有它，"把 12 个一律摘掉"也能让判据 ① 变绿 —— 那是把一个 bug 换成另一个 bug。
func TestW4_WithCredential_GitHubStaysOnToolFace(t *testing.T) {
	resetAvailability(t)
	setGitHubAvailability(true)

	reg := tools.NewRegistry()
	gt := NewGitHubTools(nil, t.TempDir())
	if err := gt.RegisterSpecs(reg); err != nil {
		t.Fatalf("RegisterSpecs: %v", err)
	}

	visible := map[string]bool{}
	for _, s := range reg.AllDirect() {
		visible[s.Name] = true
	}
	for _, name := range w4ToolNames {
		if !visible[name] {
			t.Errorf("有凭据时 %s 必须回到工具面（否则修复越界成「一律隐藏」）", name)
		}
	}
}

// TestW4_NoCredential_ExecutionGateIsUnavailable 判据 ③：执行门也走同一谓词。
//
// 观测的是**语义差**，不是某个字面串：不可用时必须是"不可用"（unknown tool / 未注册），
// 绝不能是 "GITHUB_TOKEN environment variable not set" 那种**配置提示** ——
// 后者正是 的用户可见症状（T-4 那张截图里的那一行）。
//
// 为什么执行门要单独判：生产管线按**注册表**拦（自动跟随注册点），
// 但非管线路径（Pipeline == nil ⇒ executeDirect）只查 handler 表
// ⇒ 不补闸门就会退化成软提示（与 executor.go 里 WebSearch 的处理同因）。
//
// 反例注入（应打红，已实测）：把 tools.go RegisterHandlers 里的谓词短路删掉。
func TestW4_NoCredential_ExecutionGateIsUnavailable(t *testing.T) {
	resetAvailability(t)
	setGitHubAvailability(false)

	reg := tools.NewRegistry()
	e := tools.NewExecutor(reg, t.TempDir())
	gt := NewGitHubTools(nil, t.TempDir())
	if err := gt.RegisterHandlers(e); err != nil {
		t.Fatalf("RegisterHandlers: %v", err)
	}

	r := e.Execute(context.Background(), tools.Call{ID: "w4", Name: "github_list_prs", Input: `{"owner":"golang","repo":"go"}`})
	if !r.IsError {
		t.Errorf("无凭据时 github_list_prs 必须 fail-closed，实际 IsError=false output=%q", r.Output)
	}
	if strings.Contains(r.Output, "GITHUB_TOKEN") {
		t.Errorf("无凭据时不得给出「缺 token」的配置提示（那是把  的假承诺换个说法）；output=%q", r.Output)
	}
}
