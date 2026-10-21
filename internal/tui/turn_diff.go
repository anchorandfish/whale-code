package tui

// turn_diff.go — 「本轮变更」的**唯一采集与渲染入口**。
//
// 用户拍板的展示形态：**轮末折叠卡片** —— 每轮结束在结束标志正上方
// 多一行摘要（`▸ N files changed +X -Y (/diff)`），想看细节用 `/diff` 展开。
//
// 为什么采集与渲染都收在本文件（第一性原理）：
// - **事实只有一个来源**：`filehistory` 从**真实提交的 track 记录**算出净变更
// （`FileHistory.TurnChanges`，不重读盘、不解析 AI 的话）。轮末卡片与 `/diff`
// 是同一事实的两个视图 ⇒ 都在这里取数，避免"两处各算一遍"漂移。
// - **组装只有一个出口**：`diff.NewTurnDiff` 统一统计口径；渲染复用 `diff.RenderTurn`
// （N9 批早已落地但**生产零调用点** —— 本轮才把它接上，而不是另写一个渲染器）。
// - **没有第二套折叠**：卡片**只放摘要、不放被折叠的正文** ⇒ 不存在"屏幕上有省略行、
// 复制拿不到中段"的老坑。展开体由 `/diff` 以**完整消息**打印，天然可复制。

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/diff"
	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// turnDiffFromHistory 把某一轮的净变更（filehistory）转成渲染层要的 TurnDiff。
//
// 返回 nil 表示**取不到事实**（filehistory 降级 / 读取失败 / 锚点为空）——
// 与"本轮确实没有变更"（返回非 nil 且 Files 为空）**语义不同**：
// 前者不许声称"无变更"，后者才是"无变更"。调用方据此区分。
func turnDiffFromHistory(fh *filehistory.FileHistory, msgID string, turnIndex int, promptPreview string) *diff.TurnDiff {
	if fh == nil || msgID == "" {
		return nil
	}
	changes, err := fh.TurnChanges(msgID)
	if err != nil {
		log.Printf("[tui] turn changes unavailable (non-fatal): %v", err)
		return nil
	}
	files := make([]diff.FileChange, 0, len(changes))
	for _, c := range changes {
		files = append(files, diff.FileChange{
			Path:       c.Path,
			OldContent: c.Before,
			NewContent: c.After,
			IsNew:      c.IsNew,
			IsDeleted:  c.IsDeleted,
		})
	}
	td := diff.NewTurnDiff(turnIndex, promptPreview, files)
	return &td
}

// currentTurnChanges 采集**刚刚结束的这一轮**的变更（轮末卡片的数据源）。
//
// 锚点 = `Executor.CurrentMessageId()` 的当前取值。它在生产中由 `sendInput` 设置
// （stream.go:300），取值 = 本轮那条用户消息的 UUID，整轮（含多次工具往返）恒定 ——
// 与写类工具写 track 记录用的锚点、轮末快照用的锚点**是同一个**。
func (m *Model) currentTurnChanges() *diff.TurnDiff {
	exec := m.toolService.Executor()
	if exec == nil {
		return nil
	}
	return turnDiffFromHistory(exec.FileHistoryGetter(), exec.CurrentMessageId(), m.turn.seq+1, m.lastUserPrompt())
}

// lastUserPrompt 取会话里**最近一条用户消息**的正文（单行化），供 `/diff` 的 Prompt 行。
func (m *Model) lastUserPrompt() string {
	return lastUserPromptOf(m.sessionService.CurrentSession().GetMessages())
}

// lastUserPromptOf 从消息列表尾部找最近一条用户正文。抽成自由函数便于两个入口（TUI / runnerCore）共用。
func lastUserPromptOf(msgs []session.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != "user" {
			continue
		}
		for _, b := range msg.Blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return collapseToSingleLine(b.Text)
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// `/diff` 的**唯一实现**（，2026-10-08）
// ---------------------------------------------------------------------------

// handleDiffFor 是 `/diff` 的**唯一实现**。两个命令入口（TUI `Model.handleDiff`
// 与 headless `runnerCore.handleDiff`）都只薄转发到这里 —— 收在一处是 的直接要求：
// 旧形态是同一段 ~90 行逻辑在 `commands_github.go` 与 `runner_core.go` 各写一遍，
// 连 `--stat` 的间距都不同（`" %s +%d -%d"` vs `" %s +%d -%d"`），典型「同语义两处实现」。
//
// 第一性修法（不只是合并重复）——旧实现的事实**取错了**：
//
//	旧：`beforeContent := ""` 后喂 `diff.Render(change)` ⇒ 渲染出来的其实是
//
// "空文件 → 当前文件"= **把文件整篇当成新增行**，用户看到的根本不是"改了什么"。
// 而真实的旧内容**就在 filehistory 的 track 记录里**（写盘前拍下的 before）——
// 不读它才是本命令从没显示过真实 diff 的根因。
//
//	新：与轮末折叠卡片**共用同一事实源与同一渲染器**（这正是卡片 `(/diff)` 提示承诺的"展开"）：
//
// ① 本轮净变更：锚点 = `exec.CurrentMessageId()`（与卡片同一个锚点）；
// ② 无本轮变更 → 最近一次快照所属那一轮：锚点 = 最新快照 MsgId（供回看历史轮）；
// ③ 两者皆无 → git diff 兜底（保持旧降级行为）。
//
// 三级共用 `turnDiffFromHistory`（→ `FileHistory.TurnChanges` → CAS 内容），
// **不重读工作区**：diff 的旧/新两侧都来自真实提交的字节（同类 CLI turn_diff_tracker 同思路）。
func handleDiffFor(exec *tools.Executor, workDir, args string) string {
	statOnly, fileFilter := parseDiffArgs(args)
	if exec == nil {
		return "File history not initialized"
	}
	fh := exec.FileHistoryGetter()
	if fh == nil {
		return "File history not initialized"
	}

	// ① 本轮。② 退化到最新快照那一轮。
	td := turnDiffFromHistory(fh, exec.CurrentMessageId(), 0, "")
	if td == nil || td.Stats.FilesChanged == 0 {
		if snaps, err := fh.ListSnapshots(); err == nil && len(snaps) > 0 {
			td = turnDiffFromHistory(fh, snaps[len(snaps)-1].MsgId, 0, "")
		}
	}
	// ③ 都没有 ⇒ git 兜底。
	if td == nil || len(td.Files) == 0 {
		return gitDiffFallback(workDir, statOnly)
	}

	files := filterTurnFiles(td.Files, fileFilter, workDir)
	if len(files) == 0 {
		if fileFilter != "" {
			return fmt.Sprintf("No changes for: %s", fileFilter)
		}
		return "No changes"
	}

	if statOnly {
		var sb strings.Builder
		sb.WriteString("Changes:\n")
		for _, f := range files {
			added, removed := diff.CountChanges(f)
			sb.WriteString(fmt.Sprintf("  %s  +%d -%d\n", f.Path, added, removed))
		}
		return sb.String()
	}

	// 会话恢复后 `/diff` 不知道轮次号（marks 不持久化）⇒ TurnIndex=0 ⇒ RenderTurn 不打轮次头。
	cfg := diff.DefaultConfig()
	cfg.MaxLines = 200
	return diff.RenderTurn(diff.NewTurnDiff(0, "", files), cfg)
}

// parseDiffArgs 解析 `/diff` 参数：`--stat`（只要统计）与文件过滤（`--file X` 或裸词）。
func parseDiffArgs(args string) (statOnly bool, fileFilter string) {
	if args == "" {
		return false, ""
	}
	parts := strings.Fields(args)
	for i, p := range parts {
		switch {
		case p == "--stat":
			statOnly = true
		case p == "--file" && i+1 < len(parts):
			fileFilter = parts[i+1]
		case !strings.HasPrefix(p, "--") && fileFilter == "":
			fileFilter = p
		}
	}
	return statOnly, fileFilter
}

// filterTurnFiles 按过滤词筛变更（词匹配 basename 或路径子串，保持旧语义），
// 并把展示路径**相对化**：filehistory 的 Path 是绝对路径（的约定），
// 但 `/diff` 是给人看的清单 ⇒ 工作区内显示相对路径（与旧 `/diff` 一致），
// 工作区外的保持绝对（不硬造 `../..`）。
func filterTurnFiles(files []diff.FileChange, fileFilter, workDir string) []diff.FileChange {
	out := make([]diff.FileChange, 0, len(files))
	for _, f := range files {
		if fileFilter != "" && filepath.Base(f.Path) != fileFilter && !strings.Contains(f.Path, fileFilter) {
			continue
		}
		f.Path = displayPath(f.Path, workDir)
		out = append(out, f)
	}
	return out
}

// displayPath 把绝对路径相对化到工作区（仅当确实落在工作区内）；否则原样返回。
// 判据是 `rel` 不以 ".." 开头 —— 用前缀判断而非 `strings.HasPrefix(path, workDir)`，
// 避免 `/work/foo` 与 `/work/foobar` 的兄弟目录误判（多一个字节就跨目录）。
func displayPath(path, workDir string) string {
	if workDir == "" {
		return path
	}
	rel, err := filepath.Rel(workDir, path)
	if err != nil || rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// gitDiffFallback 是"没有 filehistory 记录"时的降级：非 git 仓直接告知，git 仓给 `git diff --stat`。
// 与旧两处实现的唯一可见行为差异是这条路径**被收敛到一处**（原为两份逐字重复）。
func gitDiffFallback(workDir string, statOnly bool) string {
	checkCmd := exec.Command("git", "rev-parse", "--git-dir")
	checkCmd.Dir = workDir
	if _, err := checkCmd.Output(); err != nil {
		return "No changes (not a git repo and no file snapshots)"
	}
	gitArgs := []string{"diff"}
	if statOnly {
		gitArgs = append(gitArgs, "--stat")
	}
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v", err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return "No changes"
	}
	return fmt.Sprintf("Changes:\n%s", string(output))
}
