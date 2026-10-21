package github

import (
	"strings"
	"testing"
	"time"
)

// TestParseLocalReviewArgs_Default 校验无参数默认为 uncommitted 模式。
func TestParseLocalReviewArgs_Default(t *testing.T) {
	req, err := ParseLocalReviewArgs("")
	if err != nil {
		t.Fatalf("无参数不应报错: %v", err)
	}
	if req.Mode != ModeUncommitted {
		t.Errorf("默认模式应为 uncommitted，实际 %s", req.Mode)
	}
	if req.Arg != "" {
		t.Errorf("默认模式 Arg 应为空，实际 %q", req.Arg)
	}
}

// TestParseLocalReviewArgs_Uncommitted 校验 uncommitted 模式拒绝额外参数。
func TestParseLocalReviewArgs_Uncommitted(t *testing.T) {
	req, err := ParseLocalReviewArgs("uncommitted")
	if err != nil {
		t.Fatalf("uncommitted 不应报错: %v", err)
	}
	if req.Mode != ModeUncommitted {
		t.Errorf("模式应为 uncommitted，实际 %s", req.Mode)
	}

	// uncommitted 不接受额外参数
	if _, err := ParseLocalReviewArgs("uncommitted extra"); err == nil {
		t.Error("uncommitted 模式带额外参数应报错")
	}
}

// TestParseLocalReviewArgs_Branch 校验 branch 模式含/不含基线分支。
func TestParseLocalReviewArgs_Branch(t *testing.T) {
	// 无基线 → Arg 为空（运行时用 origin/HEAD）
	req, err := ParseLocalReviewArgs("branch")
	if err != nil {
		t.Fatalf("branch 不应报错: %v", err)
	}
	if req.Mode != ModeBranch || req.Arg != "" {
		t.Errorf("branch 无基线：期望 Mode=branch Arg=空，实际 %+v", req)
	}

	// 指定基线
	req, err = ParseLocalReviewArgs("branch main")
	if err != nil {
		t.Fatalf("branch main 不应报错: %v", err)
	}
	if req.Mode != ModeBranch || req.Arg != "main" {
		t.Errorf("branch main：期望 Mode=branch Arg=main，实际 %+v", req)
	}

	// 带斜杠的远程基线
	req, err = ParseLocalReviewArgs("branch origin/dev")
	if err != nil {
		t.Fatalf("branch origin/dev 不应报错: %v", err)
	}
	if req.Arg != "origin/dev" {
		t.Errorf("Arg 应为 origin/dev，实际 %q", req.Arg)
	}
}

// TestParseLocalReviewArgs_Commit 校验 commit 模式要求 hash 参数。
func TestParseLocalReviewArgs_Commit(t *testing.T) {
	req, err := ParseLocalReviewArgs("commit abc123")
	if err != nil {
		t.Fatalf("commit abc123 不应报错: %v", err)
	}
	if req.Mode != ModeCommit || req.Arg != "abc123" {
		t.Errorf("期望 Mode=commit Arg=abc123，实际 %+v", req)
	}

	// 缺少 hash 应报错
	if _, err := ParseLocalReviewArgs("commit"); err == nil {
		t.Error("commit 无 hash 应报错")
	}
}

// TestParseLocalReviewArgs_Custom 校验 custom 模式要求 diff 参数。
func TestParseLocalReviewArgs_Custom(t *testing.T) {
	req, err := ParseLocalReviewArgs("custom main..feature")
	if err != nil {
		t.Fatalf("custom main..feature 不应报错: %v", err)
	}
	if req.Mode != ModeCustom || req.Arg != "main..feature" {
		t.Errorf("期望 Mode=custom Arg=main..feature，实际 %+v", req)
	}

	// 缺少参数应报错
	if _, err := ParseLocalReviewArgs("custom"); err == nil {
		t.Error("custom 无参数应报错")
	}
}

// TestParseLocalReviewArgs_Invalid 校验未知模式返回错误且含合法模式列表。
func TestParseLocalReviewArgs_Invalid(t *testing.T) {
	_, err := ParseLocalReviewArgs("unknown")
	if err == nil {
		t.Fatal("未知模式应报错")
	}
	for _, m := range []string{"branch", "uncommitted", "commit", "custom"} {
		if !strings.Contains(err.Error(), m) {
			t.Errorf("错误信息应包含合法模式 %s，实际: %v", m, err)
		}
	}
}

// TestDiffArgs_ReadOnly 校验所有模式生成的 git 参数只使用只读子命令（diff/show）。
// 这是 P0-4 只读约束的核心防线：绝不应出现 add/commit/checkout/reset 等写操作。
func TestDiffArgs_ReadOnly(t *testing.T) {
	cases := []struct {
		name string
		req  LocalReviewRequest
	}{
		{"uncommitted", LocalReviewRequest{Mode: ModeUncommitted}},
		{"branch 默认", LocalReviewRequest{Mode: ModeBranch}},
		{"branch 指定", LocalReviewRequest{Mode: ModeBranch, Arg: "main"}},
		{"commit", LocalReviewRequest{Mode: ModeCommit, Arg: "abc123"}},
		{"custom", LocalReviewRequest{Mode: ModeCustom, Arg: "main..feature"}},
	}
	writeOps := []string{"add", "commit", "checkout", "reset", "clean", "stash", "merge", "rebase", "pull", "push", "rm", "mv", "apply", "cherry-pick"}

	for _, c := range cases {
		args, err := diffArgs(c.req)
		if err != nil {
			t.Errorf("%s: diffArgs 报错: %v", c.name, err)
			continue
		}
		if len(args) == 0 {
			t.Errorf("%s: diffArgs 返回空参数", c.name)
			continue
		}
		// 首个参数必须是只读子命令
		sub := args[0]
		if sub != "diff" && sub != "show" {
			t.Errorf("%s: 子命令应为 diff/show，实际 %q（违反只读约束）", c.name, sub)
		}
		// 全部参数不应包含任何写操作
		for _, a := range args {
			for _, w := range writeOps {
				if a == w {
					t.Errorf("%s: 参数包含写操作 %q（违反只读约束），完整参数: %v", c.name, w, args)
				}
			}
		}
	}
}

// TestDiffArgs_Uncommitted 校验 uncommitted 用 git diff HEAD（含暂存+未暂存）。
func TestDiffArgs_Uncommitted(t *testing.T) {
	args, err := diffArgs(LocalReviewRequest{Mode: ModeUncommitted})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "diff" || args[1] != "HEAD" {
		t.Errorf("期望 [diff HEAD]，实际 %v", args)
	}
}

// TestDiffArgs_BranchDefault 校验 branch 默认基线为 origin/HEAD 且用三点 diff。
func TestDiffArgs_BranchDefault(t *testing.T) {
	args, err := diffArgs(LocalReviewRequest{Mode: ModeBranch})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "diff" || args[1] != "origin/HEAD...HEAD" {
		t.Errorf("期望 [diff origin/HEAD...HEAD]，实际 %v", args)
	}
}

// TestDiffArgs_BranchCustom 校验 branch 指定基线时三点 diff 拼接正确。
func TestDiffArgs_BranchCustom(t *testing.T) {
	args, err := diffArgs(LocalReviewRequest{Mode: ModeBranch, Arg: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[1] != "main...HEAD" {
		t.Errorf("期望 [diff main...HEAD]，实际 %v", args)
	}
}

// TestDiffArgs_Commit 校验 commit 模式用 git show。
func TestDiffArgs_Commit(t *testing.T) {
	args, err := diffArgs(LocalReviewRequest{Mode: ModeCommit, Arg: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "show" || args[1] != "abc123" {
		t.Errorf("期望 [show abc123]，实际 %v", args)
	}
}

// TestDiffArgs_Custom 校验 custom 模式按空白切分并前置 diff。
func TestDiffArgs_Custom(t *testing.T) {
	args, err := diffArgs(LocalReviewRequest{Mode: ModeCustom, Arg: "main..feature --stat"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"diff", "main..feature", "--stat"}
	if len(args) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d]: 期望 %q，实际 %q", i, want[i], args[i])
		}
	}
}

// TestDiffArgs_UnknownMode 校验未知模式返回错误。
func TestDiffArgs_UnknownMode(t *testing.T) {
	if _, err := diffArgs(LocalReviewRequest{Mode: "bogus"}); err == nil {
		t.Error("未知模式应返回错误")
	}
}

// TestDescribeMode 校验各模式描述可读且含关键信息。
func TestDescribeMode(t *testing.T) {
	cases := []struct {
		req  LocalReviewRequest
		want string
	}{
		{LocalReviewRequest{Mode: ModeUncommitted}, "未提交变更"},
		{LocalReviewRequest{Mode: ModeBranch}, "origin/HEAD"},
		{LocalReviewRequest{Mode: ModeBranch, Arg: "main"}, "main...HEAD"},
		{LocalReviewRequest{Mode: ModeCommit, Arg: "abc123"}, "abc123"},
		{LocalReviewRequest{Mode: ModeCustom, Arg: "main..feat"}, "main..feat"},
	}
	for _, c := range cases {
		got := describeMode(c.req)
		if !strings.Contains(got, c.want) {
			t.Errorf("describeMode(%+v) = %q，应包含 %q", c.req, got, c.want)
		}
	}
}

// TestFormatLocalReviewResult_NoFindings 校验零发现时输出通过提示且含只读标注。
func TestFormatLocalReviewResult_NoFindings(t *testing.T) {
	result := &ReviewResult{
		Summary:  "",
		Findings: nil,
		Metrics:  ReviewMetrics{TotalFindings: 0},
	}
	out := FormatLocalReviewResult(LocalReviewRequest{Mode: ModeUncommitted}, result, 100*time.Millisecond)
	if !strings.Contains(out, "未发现明显问题") {
		t.Errorf("零发现应输出通过提示，实际: %s", out)
	}
	if !strings.Contains(out, "只读审查") {
		t.Errorf("输出应含只读标注，实际: %s", out)
	}
}

// TestFormatLocalReviewResult_WithFindings 校验有发现时输出含统计与 Summary。
func TestFormatLocalReviewResult_WithFindings(t *testing.T) {
	result := &ReviewResult{
		Summary:  "## 发现表格\n| 🔴 | a.go | 10 | nil ptr |",
		Findings: []Finding{{Severity: SeverityBlocker, File: "a.go", Line: 10, Title: "nil ptr"}},
		Metrics:  ReviewMetrics{TotalFindings: 1, Blockers: 1},
	}
	out := FormatLocalReviewResult(LocalReviewRequest{Mode: ModeBranch, Arg: "main"}, result, 250*time.Millisecond)
	for _, want := range []string{"本地多 Agent 代码审查", "main...HEAD", "🔴1", "只读审查", "未发布到 GitHub", "未修改工作树", "发现表格"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应包含 %q，实际: %s", want, out)
		}
	}
}

// TestFormatLocalReviewResult_NilResult 校验 nil 结果不 panic。
func TestFormatLocalReviewResult_NilResult(t *testing.T) {
	out := FormatLocalReviewResult(LocalReviewRequest{Mode: ModeUncommitted}, nil, 0)
	if !strings.Contains(out, "未返回结果") {
		t.Errorf("nil 结果应输出提示，实际: %s", out)
	}
}

// TestHandleLocalReview_BadArgs 校验参数错误返回可读提示而非 panic。
func TestHandleLocalReview_BadArgs(t *testing.T) {
	g := &GitHubTools{workDir: "."}
	out := g.HandleLocalReview("bogus-mode")
	if !strings.Contains(out, "参数解析失败") {
		t.Errorf("参数错误应返回解析失败提示，实际: %s", out)
	}
	if !strings.Contains(out, "用法") {
		t.Errorf("应包含用法提示，实际: %s", out)
	}
}

// TestHandleLocalReview_NotGitRepo 校验非 Git 仓库返回可读错误。
func TestHandleLocalReview_NotGitRepo(t *testing.T) {
	// 使用一个几乎不可能为 Git 仓库的路径
	g := &GitHubTools{
		workDir:       "Z:\\nonexistent\\path\\for\\review\\test\\12345",
		multiReviewer: NewMultiAgentReviewer(nil, &MockAIClient{Response: "[]"}),
	}
	out := g.HandleLocalReview("uncommitted")
	if !strings.Contains(out, "本地审查失败") {
		t.Errorf("非 Git 仓库应返回审查失败提示，实际: %s", out)
	}
}
