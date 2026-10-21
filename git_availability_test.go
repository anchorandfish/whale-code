package main

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// w4GitHubToolNames 是票面事实（`检索联网_TEST_GUIDE.md` §T-5 列的 12 个名字）。
// 刻意硬编码：本文件要能在"改动前"的源码上编译并跑出红（见下方说明）。
var w4GitHubToolNames = []string{
	"github_create_pr", "github_review_pr", "github_list_prs", "github_get_pr_diff",
	"github_merge_pr", "github_list_issues", "github_create_issue", "github_get_issue",
	"github_list_workflows", "github_list_workflow_runs", "github_trigger_workflow",
	"github_rerun_workflow",
}

// modelFacingGitHubTools 按生产顺序装配一次，返回"模型可见工具面"里出现的 github_* 个数。
//
// 顺序与 tui/lifecycle.go:899-907 一致：启动咽喉点装配 → NewRegistry →
// NewGitHubTools(...).RegisterSpecs(reg) → reg.AllDirect()（buildToolDefs 的输入）。
func modelFacingGitHubTools(t *testing.T) map[string]bool {
	t.Helper()

	applyEnvDerivedToolAvailability() // ← 生产唯一装配点（main.go / loadEnvFiles 咽喉点）

	reg := tools.NewRegistry()
	gt := github.NewGitHubTools(nil, t.TempDir())
	if err := gt.RegisterSpecs(reg); err != nil {
		t.Fatalf("RegisterSpecs: %v", err)
	}

	visible := map[string]bool{}
	for _, s := range reg.AllDirect() {
		visible[s.Name] = true
	}
	return visible
}

// TestW4_EndToEnd_NoToken_NoGitHubOnModelFace 端到端判别器（，**本条具判别性**）。
//
// 走的是**生产的启动咽喉点**，不是手工置谓词：`applyEnvDerivedToolAvailability()`
// 是 main.go 里唯一的装配点，三条入口（启动 / `whale doctor` / `--check-api`）共用。
//
// 为什么另起一个文件、不并进 main_test.go：本条要与「改动前」的源码**同源可比** ——
// before 用 `-overlay` 只把生产文件换回旧版、本文件照跑，故**只引用旧版就存在的符号**
// （applyEnvDerivedToolAvailability / tools.NewRegistry / github.NewGitHubTools / AllDirect）。
// 若这里引用了本次新增的符号，before 只会是 `[build failed]`，那是实验无效而非判别性证据。
//
// 反例注入（应打红，已实测）：把 main.go 里 `github.ApplyToolAvailability()` 那一行删掉。
// 含义：**那一行是承重的** —— 少了它，谓词永远不会写进 GitHub 的名字，
// 注册点读到的就是默认值「全部可用」， 原样复现。
func TestW4_EndToEnd_NoToken_NoGitHubOnModelFace(t *testing.T) {
	tools.ResetToolAvailability()
	t.Cleanup(tools.ResetToolAvailability)

	t.Setenv("GITHUB_TOKEN", "")

	visible := modelFacingGitHubTools(t)
	for _, name := range w4GitHubToolNames {
		if visible[name] {
			t.Errorf("无 GITHUB_TOKEN 时 %s 仍出现在模型工具面 —— 模型会看到一个「调了只会报缺 token」的工具（ 未修）", name)
		}
	}
}

// TestW4_EndToEnd_WithToken_GitHubVisible 反例守卫（**本条刻意不具判别性**：before/after 皆绿）。
//
// 它防的是"过度隐藏"这种更糟的修法（把 12 个工具一律摘掉，判据 ① 照样绿，
// 但有 token 的用户也被剥夺了能力）。**不得**把它当判别器读。
func TestW4_EndToEnd_WithToken_GitHubVisible(t *testing.T) {
	tools.ResetToolAvailability()
	t.Cleanup(tools.ResetToolAvailability)

	t.Setenv("GITHUB_TOKEN", "ghp_"+strings.Repeat("a", 36))

	visible := modelFacingGitHubTools(t)
	for _, name := range w4GitHubToolNames {
		if !visible[name] {
			t.Errorf("有 GITHUB_TOKEN 时 %s 必须可用（否则修复越界成「一律隐藏」）", name)
		}
	}
}
