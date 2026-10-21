package tools

// apply_diff.go — G5：`apply_unified_diff` 工具。
//
// 为什么需要它：whale-code 此前只有 diff 的**渲染**侧（internal/diff：
// algorithm/render/types/cache/ide），没有任何把 unified diff 写回文件的能力 ——
// 「给模型看 diff」与「让模型提交 diff」之间缺了后半条链路。本文件补上它。
//
// 分层（与既有写类工具一致，不新造机制）：
// - 语法 / 匹配 / 冲突判定 → internal/diff（ParseUnifiedDiff / ApplyFilePatch）
// - 沙箱与工作区边界 → 管线 CustomValidateStage（前置、零副作用）+ 本文件的
// `validatePathSandbox` 预检（handler 底线，Pipeline==nil 回退时也成立）
// - 落盘 → util.AtomicWriteFile（与 write_file / edit_file 同款）
//
// ★ 事务决策：**全有或全无**。
// 函数先把所有文件的新内容在内存里算完（含冲突检测），**全部成功后才落盘**；
// 任一处冲突或越界 ⇒ 一个字节都不写。这样"回滚"这个动作根本不需要存在 ——
// 仓库无 git，靠事后回滚必然不可靠（记忆铁律：无 git 仓库，回滚与 A/B 靠文件级基线）。
// 唯一残留的中间态是"落盘过程中磁盘写失败"，此时如实回报已写/未写的文件清单，
// 不假装成功。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/diff"
	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/util"
)

// ApplyUnifiedDiffInput 是 apply_unified_diff 的入参。
type ApplyUnifiedDiffInput struct {
	Patch     string `json:"patch,omitempty" desc:"Unified diff text to apply (git-style a/ b/ prefixes optional)"`
	PatchPath string `json:"patch_path,omitempty" desc:"Path to a .patch/.diff file to read the patch from (alternative to patch)"`
	DryRun    bool   `json:"dry_run,omitempty" desc:"If true, report what would change and write nothing"`
	MaxFuzz   *int   `json:"max_fuzz,omitempty" desc:"Max leading/trailing context lines to ignore when locating hunks (default 2)"`
}

// applyUnifiedDiffToolName 是工具名（注册、isWriteTool、回报文案共用一处）。
const applyUnifiedDiffToolName = "apply_unified_diff"

// patchTargetAction 是单个目标文件将要发生的动作。
type patchTargetAction struct {
	Path      string
	AbsPath   string
	IsNew     bool
	IsDeleted bool
	Hunks     int
	Before    string
	After     string
	OldMode   os.FileMode
	Exist     bool
}

// applyUnifiedDiffHandler 应用一段 unified diff。
func (e *Executor) applyUnifiedDiffHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input ApplyUnifiedDiffInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}

		text, err := resolvePatchText(input)
		if err != nil {
			return "", err
		}

		patches, err := diff.ParseUnifiedDiff(text)
		if err != nil {
			return "", fmt.Errorf("invalid patch: %w", err)
		}

		opts := diff.DefaultApplyOptions()
		if input.MaxFuzz != nil {
			if *input.MaxFuzz < 0 || *input.MaxFuzz > 10 {
				return "", fmt.Errorf("max_fuzz must be between 0 and 10, got %d", *input.MaxFuzz)
			}
			opts.MaxFuzz = *input.MaxFuzz
		}

		// ---- 预检一：路径解析（越出工作区 → SandboxDenied，零副作用）----
		planned, err := e.planPatch(ctx, workDir, patches, opts)
		if err != nil {
			return softSandboxOutput(err)
		}

		// ---- 预检二：全部文件的新内容在内存里算完（冲突 → PatchConflict，零副作用）----
		for i := range planned {
			if planned[i].IsDeleted {
				continue
			}
			if leaked, hit := detectSecretLeakForWrite(planned[i].After); hit {
				// ★ ：双语 SSOT 形态（util.BiSame）—— 修前中文为主；
				// 英文主行保留 "SECRET_LEAK_BLOCKED"。
				return "", fmt.Errorf("%s", util.BiSame(
					"⚠️ [SECRET_LEAK_BLOCKED] Secret pattern detected; write blocked:\n%s\nCheck whether the patch content contains sensitive data (API key/Token/Private Key)",
					"⚠️ [SECRET_LEAK_BLOCKED] 检测到密钥模式，已阻断写入：\n%s\n请检查补丁内容是否含敏感数据（API key/Token/Private Key）",
					leaked))
			}
		}

		if input.DryRun {
			return renderPatchReport(planned, nil, true, opts.MaxFuzz), nil
		}

		// ---- 落盘（此刻已不可能因冲突而失败）----
		written, err := e.commitPatch(ctx, planned)
		if err != nil {
			// 已写与未写的文件必须如实列出 —— 不假装回滚。
			return renderPatchReport(planned, written, false, opts.MaxFuzz) +
				fmt.Sprintf("\n\nERROR: %v\n", err) +
				util.Bi(
					fmt.Sprintf("Files written: %d/%d. No rollback performed — this repo has no git; the transaction boundary only guarantees zero writes on conflict, not multi-file atomicity if a disk write fails.", len(written), len(planned)),
					fmt.Sprintf("（已落盘 %d/%d 个文件；本次未做回滚 —— 仓库无 git，事务边界只保证冲突时零写入，不保证磁盘写失败后的多文件原子性）", len(written), len(planned)),
				), err
		}
		return renderPatchReport(planned, written, false, opts.MaxFuzz), nil
	}
}

// resolvePatchText 取出补丁文本（内联 patch 优先，其次 patch_path）。
func resolvePatchText(input ApplyUnifiedDiffInput) (string, error) {
	if strings.TrimSpace(input.Patch) != "" {
		return input.Patch, nil
	}
	if strings.TrimSpace(input.PatchPath) == "" {
		return "", fmt.Errorf("patch is required: pass `patch` (unified diff text) or `patch_path`")
	}
	if err := validateFilePath(input.PatchPath); err != nil {
		return "", err
	}
	data, err := os.ReadFile(input.PatchPath)
	if err != nil {
		return "", fmt.Errorf("read patch file %q: %w", input.PatchPath, err)
	}
	return string(data), nil
}

// planPatch 做**全部**预检并把每个目标文件的新内容算到内存里。
// 任一步失败即返回错误 —— 调用方据此保证「零副作用」。
func (e *Executor) planPatch(ctx context.Context, workDir string, patches []diff.FilePatch, opts diff.ApplyOptions) ([]patchTargetAction, error) {
	planned := make([]patchTargetAction, 0, len(patches))

	for _, fp := range patches {
		target := fp.TargetPath()
		if strings.TrimSpace(target) == "" {
			return nil, fmt.Errorf("patch entry has no target path (both --- and +++ are /dev/null)")
		}

		absPath := resolvePatchTargetPath(workDir, target)

		// T8：路径遍历 + UNC 拦截（与 write_file / edit_file 同一把尺子）。
		if err := validateFilePath(absPath); err != nil {
			return nil, sandboxDenied(err.Error())
		}
		// 工作区边界：与管线 CustomValidateStage **同一个判据**（checkWorkspaceBoundary）。
		//
		// 为什么 handler 也要查（而不是只靠管线）：本工具的目标路径是**从补丁文本里
		// 扫出来的**，管线的可见性取决于 patchTextFields 登记被维护住；而写类工具
		// 必须留在工作区这条不变量不应该受"某个登记表有没有跟上"影响。
		// 实测证据：把 patchTextFields 里这一项摘掉后，越界补丁在
		// validatePathSandbox 为 no-op（未配置沙箱）时**真的写成功了** ——
		// 也就是说没有沙箱时 handler 底线形同虚设，边界必须由这里独立把住。
		// 这里是**拒绝闸**：只有「已证明越界」才拒（起判据返回三值 verdict，
		// 「未知」不再伪装成「在工作区内」，也不被当作「越界」——见 BoundaryVerdict）。
		if v, _ := checkWorkspaceBoundary(absPath, workDir); v.ProvablyOutside() {
			return nil, sandboxDenied(fmt.Sprintf(
				"apply_unified_diff: patch target %q is outside the workspace %q", absPath, workDir))
		}
		// C14：沙箱路径校验（Manager 优先，回退 legacy）。越界即拒，且此时尚未写任何文件。
		if err := e.validatePathSandbox(ctx, absPath); err != nil {
			return nil, sandboxDenied(err.Error())
		}

		action := patchTargetAction{
			Path:      target,
			AbsPath:   absPath,
			IsNew:     fp.IsNew,
			IsDeleted: fp.IsDeleted,
			Hunks:     len(fp.Hunks),
		}

		if info, statErr := os.Stat(absPath); statErr == nil {
			action.Exist = true
			action.OldMode = info.Mode().Perm()
		} else if !fp.IsNew {
			return nil, fmt.Errorf("cannot apply patch to %q: %w", target, statErr)
		}
		if action.OldMode == 0 {
			action.OldMode = 0o644
		}

		var before string
		if action.Exist {
			data, readErr := os.ReadFile(absPath)
			if readErr != nil {
				return nil, fmt.Errorf("read %q: %w", target, readErr)
			}
			before = string(data)
		}
		action.Before = before

		if fp.IsDeleted {
			if !action.Exist {
				return nil, fmt.Errorf("cannot delete %q: file does not exist", target)
			}
			planned = append(planned, action)
			continue
		}

		after, _, applyErr := diff.ApplyFilePatch(before, fp, opts)
		if applyErr != nil {
			// 冲突 → 结构化 PatchConflict（对齐 R1 契约：不把种类编进字符串）。
			return nil, &ToolExecError{
				Tool: applyUnifiedDiffToolName,
				Kind: FailureKindPatchConflict,
				Msg:  fmt.Sprintf("apply_unified_diff: %v", applyErr),
			}
		}
		action.After = after
		planned = append(planned, action)
	}

	if len(planned) == 0 {
		return nil, fmt.Errorf("patch contains no file to apply")
	}
	return planned, nil
}

// commitPatch 把计划逐文件落盘，返回**已成功写入**的目标路径列表。
func (e *Executor) commitPatch(ctx context.Context, planned []patchTargetAction) ([]string, error) {
	var written []string
	for _, p := range planned {
		if err := e.trackBeforePath(ctx, p.AbsPath); err != nil {
			return written, err
		}
		if p.IsDeleted {
			if err := os.Remove(p.AbsPath); err != nil {
				return written, fmt.Errorf("delete %s: %w", p.Path, err)
			}
		} else if err := util.AtomicWriteFile(p.AbsPath, []byte(p.After), p.OldMode); err != nil {
			return written, fmt.Errorf("write %s: %w", p.Path, err)
		}
		// 本工具 plan 时即以盘上现值为基准（apply_diff.go:193/209，内容锚定），
		// 写前无需比对；但写成功后必须更新读基准为 after —— 否则紧随其后的
		// write_file 会拿 plan 前的旧基准误判 stale ⇒ 误伤 apply→write 连招。
		e.recordWriteBase(p.AbsPath, []byte(p.After))
		if e.FileCache != nil {
			e.FileCache.Invalidate(p.AbsPath)
		}
		e.notifyLSPDidChange(ctx, p.AbsPath, p.After)
		e.pushFileChange(applyUnifiedDiffToolName, p.AbsPath, nil, nil, ToolCallIDFromCtx(ctx))
		written = append(written, p.Path)
	}
	return written, nil
}

// trackBeforePath 记录变更前状态（与 pushFileChange 内的 TrackAfter 配对）。
func (e *Executor) trackBeforePath(ctx context.Context, absPath string) error {
	if e.FileHistory == nil {
		return nil
	}
	if err := e.FileHistory.Track(e.CurrentMessageId(), ToolCallIDFromCtx(ctx),
		applyUnifiedDiffToolName, []string{absPath}); err != nil {
		return fmt.Errorf("filehistory track %s: %w", absPath, err)
	}
	return nil
}

// sandboxDeniedError 是本工具沙箱/越界拒绝的载体。
//
// 两个通道各取所需，缺一不可：
// - Error() → 给模型/用户看的中性文案（与 write_file 的 "Sandbox denied: …" 同形）；
// - Unwrap() → errSandboxDeniedSoft，使 executeDirect 走 **S11 G1-B 软拒绝**映射：
// IsError=false + FailureKind=sandbox_denied（与 write_file / edit_file 完全一致）。
//
// 为什么不用 ToolExecError：ToolExecError 走的是默认分支（IsError=true）。
// 写类工具的"handler 兜底拒绝"在仓库里已由 S11 G1-B 定型为软拒绝形态，
// 本工具与之同类，必须同形 —— 否则同一件事（越界写）会因为用哪个工具而
// 产生两种模型契约，TUI 的终局拒绝回路也会表现不一。
type sandboxDeniedError struct{ msg string }

func (e *sandboxDeniedError) Error() string { return e.msg }

func (e *sandboxDeniedError) Unwrap() error { return errSandboxDeniedSoft }

// sandboxDenied 构造软沙箱拒绝。
func sandboxDenied(cause string) error {
	return &sandboxDeniedError{msg: "Sandbox denied: " + cause}
}

// softSandboxOutput 把 handler 错误转成 executeDirect 需要的 (output, error) 返回对：
// 沙箱拒绝 → (文案, 软哨兵)；其余错误 → ("", err)。
func softSandboxOutput(err error) (string, error) {
	var sd *sandboxDeniedError
	if errors.As(err, &sd) {
		return sd.Error(), sd
	}
	return "", err
}

// resolvePatchTargetPath 把补丁里的路径解析为绝对路径。
// 相对路径以工作区为基准（补丁路径惯例是相对仓库根），绝对路径原样返回
// （是否允许由沙箱/工作区边界判定，本函数不做任何放行决策）。
// 锚定判定收口 platform.AnchorsToWorkDir —— Windows 上无盘符的 `/x`
// 锚到盘根，不得当相对路径拼进工作区。
func resolvePatchTargetPath(workDir, target string) string {
	if workDir == "" || !platform.AnchorsToWorkDir(target) {
		return target
	}
	return filepath.Join(workDir, target)
}

// renderPatchReport 生成人类可读的回报（英文为主，必要时附中文提示）。
func renderPatchReport(planned []patchTargetAction, written []string, dryRun bool, maxFuzz int) string {
	var totalHunks, totalAdd, totalDel int
	for _, p := range planned {
		totalHunks += p.Hunks
		change := diff.FileChange{Path: p.Path, OldContent: p.Before, NewContent: p.After, IsNew: p.IsNew, IsDeleted: p.IsDeleted}
		added, removed := diff.CountChanges(change)
		totalAdd += added
		totalDel += removed
	}

	verb := "Applied unified diff"
	if dryRun {
		verb = "Dry run: would apply unified diff"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: %d file(s), %d hunk(s), +%d/-%d lines (max fuzz %d)\n",
		verb, len(planned), totalHunks, totalAdd, totalDel, maxFuzz)

	for i, p := range planned {
		mark := "M"
		switch {
		case p.IsNew:
			mark = "A"
		case p.IsDeleted:
			mark = "D"
		}
		change := diff.FileChange{Path: p.Path, OldContent: p.Before, NewContent: p.After, IsNew: p.IsNew, IsDeleted: p.IsDeleted}
		added, removed := diff.CountChanges(change)
		status := ""
		if !dryRun {
			if i < len(written) {
				status = " [ok]"
			} else {
				status = " [NOT WRITTEN]"
			}
		}
		fmt.Fprintf(&sb, "  %s  %-40s +%-4d -%-4d (%d hunk)%s\n",
			mark, p.Path, added, removed, p.Hunks, status)
	}

	if dryRun {
		sb.WriteString("\n(dry run: nothing was written)\n")
		return sb.String()
	}

	var previews []string
	for _, p := range planned {
		if p.IsDeleted {
			previews = append(previews, fmt.Sprintf("--- %s (deleted)\n", p.Path))
			continue
		}
		change := diff.FileChange{Path: p.Path, OldContent: p.Before, NewContent: p.After, IsNew: p.IsNew}
		// PlainConfig：这段预览是工具结果（数据层），会被塞进剪贴板、模型上下文与会话存档，
		// 三者都是纯文本消费者。ANSI 在这里只是噪音（乱码 / 浪费 token），
		// 且下面按字节截断（maxPreview）还会切碎转义序列与 UTF-8。
		if rendered := diff.Render(change, diff.PlainConfig()); rendered != "" {
			previews = append(previews, rendered)
		}
	}
	if len(previews) > 0 {
		body := strings.Join(previews, "\n")
		const maxPreview = 4000
		if len(body) > maxPreview {
			body = body[:maxPreview] + "\n... (diff truncated)"
		}
		sb.WriteString("\n")
		sb.WriteString(body)
	}
	return sb.String()
}
