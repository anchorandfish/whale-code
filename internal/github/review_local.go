package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// LocalReviewMode 本地 /review 命令的审查模式。
// 所有模式均只读，绝不修改工作树或索引（对标同类 CLI 的只读约束）。
type LocalReviewMode string

const (
	// ModeBranch 分支模式：审查当前分支相对于基线分支的累计 diff。
	// 默认基线为 origin/HEAD，可通过 Arg 指定其它基线。
	ModeBranch LocalReviewMode = "branch"
	// ModeUncommitted 未提交模式：审查工作区所有未提交变更（暂存 + 未暂存）。
	ModeUncommitted LocalReviewMode = "uncommitted"
	// ModeCommit 提交模式：审查指定 commit 的 diff。
	ModeCommit LocalReviewMode = "commit"
	// ModeCustom 自定义模式：用户直接提供 git diff 的参数。
	ModeCustom LocalReviewMode = "custom"
)

// LocalReviewRequest 本地审查请求，描述 diff 来源与参数。
type LocalReviewRequest struct {
	// Mode 审查模式（branch/uncommitted/commit/custom）。
	Mode LocalReviewMode
	// Arg 模式参数：branch 时为基线分支（空则用 origin/HEAD）；commit 时为 commit hash；custom 时为 diff 参数。
	Arg string
}

// ParseLocalReviewArgs 解析 /review 命令参数为本地审查请求。
// 规则：
//   - 无参数 → uncommitted 模式（审查所有未提交变更）
//   - "uncommitted" → uncommitted 模式
//   - "branch" → branch 模式（基线 origin/HEAD）
//   - "branch <base>" → branch 模式（基线 base）
//   - "commit <hash>" → commit 模式
//   - "custom <args...>" → custom 模式
//
// 非法模式返回错误（含合法模式列表）。
func ParseLocalReviewArgs(args string) (LocalReviewRequest, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return LocalReviewRequest{Mode: ModeUncommitted}, nil
	}

	// 按首个空格切分模式与剩余参数
	mode, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)

	switch LocalReviewMode(mode) {
	case ModeUncommitted:
		if rest != "" {
			return LocalReviewRequest{}, fmt.Errorf("uncommitted 模式不接受额外参数，收到 %q", rest)
		}
		return LocalReviewRequest{Mode: ModeUncommitted}, nil
	case ModeBranch:
		return LocalReviewRequest{Mode: ModeBranch, Arg: rest}, nil // Arg 为空时使用默认基线
	case ModeCommit:
		if rest == "" {
			return LocalReviewRequest{}, fmt.Errorf("commit 模式需要指定 commit hash，例如 /review commit <hash>")
		}
		return LocalReviewRequest{Mode: ModeCommit, Arg: rest}, nil
	case ModeCustom:
		if rest == "" {
			return LocalReviewRequest{}, fmt.Errorf("custom 模式需要指定 diff 参数，例如 /review custom main..feature")
		}
		return LocalReviewRequest{Mode: ModeCustom, Arg: rest}, nil
	}
	return LocalReviewRequest{}, fmt.Errorf("未知审查模式 %q，合法模式：branch / uncommitted / commit / custom", mode)
}

// diffArgs 根据 LocalReviewRequest 构造只读 git 子命令参数。
// 所有返回的命令均为只读（diff / show），绝不修改工作树或索引。
// 抽出为独立纯函数便于测试只读性与参数构造。
func diffArgs(req LocalReviewRequest) ([]string, error) {
	switch req.Mode {
	case ModeUncommitted:
		// git diff HEAD：同时显示暂存区与工作区相对 HEAD 的变更，只读
		return []string{"diff", "HEAD"}, nil
	case ModeBranch:
		base := req.Arg
		if base == "" {
			base = "origin/HEAD"
		}
		// 三点 diff：当前分支自基线分叉以来的累计变更，只读
		return []string{"diff", base + "...HEAD"}, nil
	case ModeCommit:
		// git show <hash>：显示指定 commit 的完整 diff，只读
		return []string{"show", req.Arg}, nil
	case ModeCustom:
		// 用户自定义 diff 参数，git diff 本身只读
		fields := strings.Fields(req.Arg)
		if len(fields) == 0 {
			return nil, fmt.Errorf("custom 模式参数为空")
		}
		return append([]string{"diff"}, fields...), nil
	}
	return nil, fmt.Errorf("未知审查模式 %q", req.Mode)
}

// getLocalDiff 通过 GitHelper 执行只读 git 命令获取本地 diff。
// 所有模式均不修改工作树（diff/show 为只读操作）。
func (g *GitHubTools) getLocalDiff(ctx context.Context, req LocalReviewRequest) (string, error) {
	helper := tools.NewGitHelper(g.workDir)
	if !helper.IsGitRepo(ctx) {
		return "", fmt.Errorf("当前工作目录 %q 不是 Git 仓库", g.workDir)
	}

	args, err := diffArgs(req)
	if err != nil {
		return "", err
	}

	diff, err := helper.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("执行 git %s 失败: %w", strings.Join(args, " "), err)
	}
	return diff, nil
}

// ReviewLocal 对本地 diff 执行多 Agent 并行审查。
// 流程：获取只读 diff → 调用 MultiAgentReviewer.ReviewDiff（并行 Agent + 交叉验证 + 去重排序）。
// 全程只读，不发布任何结果到 GitHub，不修改工作树。
func (g *GitHubTools) ReviewLocal(ctx context.Context, req LocalReviewRequest) (*ReviewResult, error) {
	if g.multiReviewer == nil {
		return nil, fmt.Errorf("多 Agent 审查器未初始化")
	}

	diff, err := g.getLocalDiff(ctx, req)
	if err != nil {
		return nil, err
	}

	result, err := g.multiReviewer.ReviewDiff(ctx, diff)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// FormatLocalReviewResult 将本地审查结果格式化为 Markdown 字符串，供 TUI 会话记录展示。
// 输出包含模式说明、统计概览与 result.Summary（已含 Markdown 表格），
// 并显式标注"只读审查 · 未发布到 GitHub"，强化非破坏性语义。
func FormatLocalReviewResult(req LocalReviewRequest, result *ReviewResult, elapsed time.Duration) string {
	if result == nil {
		return "本地审查未返回结果"
	}

	modeDesc := describeMode(req)
	header := fmt.Sprintf("## 🤖 本地多 Agent 代码审查\n\n> 模式：%s · 耗时 %s · 🔴%d 🟡%d 🟢%d · **只读审查，未发布到 GitHub，未修改工作树**\n",
		modeDesc, elapsed.Round(time.Millisecond),
		result.Metrics.Blockers, result.Metrics.Warnings, result.Metrics.Suggestions)

	if result.Metrics.TotalFindings == 0 {
		return header + "\n✅ 未发现明显问题，多 Agent 审查通过。\n"
	}
	return header + "\n" + result.Summary
}

// describeMode 返回模式的可读描述，用于输出头部。
func describeMode(req LocalReviewRequest) string {
	switch req.Mode {
	case ModeUncommitted:
		return "未提交变更（git diff HEAD）"
	case ModeBranch:
		base := req.Arg
		if base == "" {
			base = "origin/HEAD"
		}
		return fmt.Sprintf("分支差异（%s...HEAD）", base)
	case ModeCommit:
		return fmt.Sprintf("指定提交（git show %s）", req.Arg)
	case ModeCustom:
		return fmt.Sprintf("自定义（git diff %s）", req.Arg)
	}
	return string(req.Mode)
}

// HandleLocalReview 是 /review 斜杠命令的入口，供 TUI 回调调用。
// 解析参数 → 执行只读本地审查 → 格式化 Markdown 输出。
// 任何错误均返回可读字符串（不抛 error，便于回调直接展示）。
func (g *GitHubTools) HandleLocalReview(args string) string {
	req, err := ParseLocalReviewArgs(args)
	if err != nil {
		return fmt.Sprintf("❌ 参数解析失败: %v\n\n用法: /review [branch [base] | uncommitted | commit <hash> | custom <diff-args>]", err)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := g.ReviewLocal(ctx, req)
	if err != nil {
		return fmt.Sprintf("❌ 本地审查失败: %v\n\n提示: 确认当前目录是 Git 仓库且存在可审查的 diff。", err)
	}

	return FormatLocalReviewResult(req, result, time.Since(start))
}
