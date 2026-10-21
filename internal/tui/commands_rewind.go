package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/search"
	"github.com/anchorandfish/whale-code/internal/session"
)

// rewindReloadAfterRestore 在 `fh.Rewind` 报告会话已恢复后，把**磁盘上的会话**重新读进
// 内存（`SetCurrentSession` 的唯一调用点）。
//
// A8（2026-09-15，O-U 轮）：此前三处写法是
//
//	if loaded, loadErr := ...Load(sessionId); loadErr == nil { SetCurrentSession(loaded) }
//
// —— 加载失败被**静默吞掉**，用户仍看到 "Session restored: true"，而内存里还是回退前那条
// 会话。这与 A8 的「拒绝加载必须**显著告警**」直接冲突（族A：拒绝不得静默）。
//
// 现在：失败必须①写日志 ②通过返回值把**可读告警**交回调用方渲染；损坏类错误用
// `errors.As` 取**结构化**隔离路径（R4：可判定的结构化事实不得由散文关键词断言）。
func rewindReloadAfterRestore(store *session.Store, sessionId string, apply func(*session.Session)) (warn string) {
	if store == nil || sessionId == "" {
		return ""
	}
	loaded, err := store.Load(sessionId)
	if err == nil {
		if apply != nil {
			apply(loaded)
		}
		return ""
	}

	var sce *session.SessionCorruptionError
	if errors.As(err, &sce) && sce.QuarantinePath != "" {
		log.Printf("[tui] rewind: session %q refused after rewind (corrupt, quarantined to %s): %v",
			sessionId, sce.QuarantinePath, err)
		return fmt.Sprintf("Session reload after rewind refused (corrupt file quarantined to %s); "+
			"in-memory session was NOT refreshed", sce.QuarantinePath)
	}
	log.Printf("[tui] rewind: session %q reload after rewind failed: %v", sessionId, err)
	return fmt.Sprintf("Session reload after rewind failed; in-memory session was NOT refreshed: %v", err)
}

// handleRewind 处理 /rewind 命令（阶段 4：会话级回退，同时恢复文件+会话状态）
// 替代旧的基于 turn 计数的会话回退，改用 FileHistory.Rewind
// 支持 --preview 标志：仅显示 DiffStats 预览，不实际执行回退
func (m *Model) handleRewind(args string) string {
	// 无参数 = 打开选择器（对标同类 CLI 的 /rewind：从列表里挑，不要求记编号）。
	// 要文本清单用 /checkpoint list；要免交互，直接用 /rewind #N。
	if strings.TrimSpace(args) == "" {
		return m.openRewindPicker()
	}
	return m.doRewind(args)
}

// openRewindPicker 打开交互式快照选择器（↑↓ 选择 · Enter 回退 · Esc 取消）。
//
// 复用 PendingChoice + StateWaitingChoice，不新增按键分支（与 handleSessionsPicker
// 同法：键盘/鼠标/渲染/高度计算都已就位且被测试覆盖，复用等于免费获得，也避免
// 第三份「列表选择」实现）。选中后的回退**走 doRewind** —— 选择器只是"输入锚点的
// 另一种方式"，不重复实现回退逻辑。
func (m *Model) openRewindPicker() string {
	sessionID, alias, currentMsgID := m.rewindViewContext()
	rows, err := rewindRowsFromHistory(m.toolService.FileHistory(), sessionID, alias, currentMsgID)
	if err != nil {
		return fmt.Sprintf("Failed to list snapshots: %v", err)
	}
	if len(rows) == 0 {
		return formatRewindList(rows)
	}
	opts, def := rewindPickerOptions(rows)
	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt:     "Rewind to which snapshot?",
		Options:    opts,
		ToolID:     rewindPickerChoiceToolID,
		DefaultIdx: def,
		Category:   "rewind",
	})
	m.toolService.SetChoiceIdx(def)
	m.sessionService.SetState(StateWaitingChoice)
	m.recalcViewportHeight()

	hint := biSame("Select a snapshot (↑↓ select · Enter rewind · Esc cancel)",
		"选择要回退到的快照（↑↓ 选择 · Enter 回退 · Esc 取消）")
	if len(opts) < len(rows) {
		hint += "\n" + bi(fmt.Sprintf("Showing the %d newest of %d snapshots", len(opts), len(rows)),
			fmt.Sprintf("展示最近 %d 个，共 %d 个快照", len(opts), len(rows)))
	}
	// 不可逆必须在下手前说清：Rewind 会截断本次对话的消息、删掉快照之后新建的文件，
	// 并清空 undo/redo 栈（internal/filehistory/filehistory.go 的 Rewind 尾部）
	// ⇒ 不能让人带着"大不了再退回来"的错觉选。
	hint += "\n" + bi("⚠️ Rewind restores files AND truncates this conversation; it cannot be undone",
		"⚠️ 回退会还原文件并截断本次对话，且不可撤销")
	return hint
}

// vimDeleteCurrentLine deletes the line under cursor in textarea
func (m *Model) vimDeleteCurrentLine() {
	value := m.uiService.Textarea().Value()
	if value == "" {
		return
	}

	lines := strings.Split(value, "\n")
	if len(lines) == 0 {
		return
	}

	// Get current line number via textarea.Line()
	cursorLine := m.uiService.Textarea().Line()
	if cursorLine < 0 {
		cursorLine = 0
	}
	if cursorLine >= len(lines) {
		cursorLine = len(lines) - 1
	}

	// Delete that line
	if len(lines) == 1 {
		m.uiService.Textarea().SetValue("")
	} else {
		newLines := make([]string, 0, len(lines)-1)
		newLines = append(newLines, lines[:cursorLine]...)
		newLines = append(newLines, lines[cursorLine+1:]...)
		m.uiService.Textarea().SetValue(strings.Join(newLines, "\n"))
	}
}

// handleCheckpoint 处理 checkpoint 命令（阶段 4 切换：save/list/restore/show 改为调 FileHistory）
func (m *Model) handleCheckpoint(args string) string {
	fh := m.toolService.FileHistory()
	if fh == nil {
		return "File history not initialized"
	}

	parts := strings.Fields(args)
	if len(parts) == 0 {
		return formatCheckpointUsage()
	}

	action := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = strings.Join(parts[1:], " ")
	}

	switch action {
	case "save":
		// 阶段 4：save 分支调 fh.Snapshot
		label := rest
		exec := m.toolService.Executor()
		if exec == nil {
			return "Executor not initialized"
		}
		msgId := exec.CurrentMessageId()
		if msgId == "" {
			return "No active message; cannot create snapshot"
		}
		msgCount := len(m.sessionService.CurrentSession().GetMessages())
		if label == "" {
			// 落盘标签改存**会话人话名**（别名），不再存会话主键 UUID ——
			// 见 snapshotLabelForStorage 注释（：label 位放 UUID 正是"看不懂"的来源）。
			alias := ""
			if sess := m.sessionService.CurrentSession(); sess != nil {
				alias = sess.Metadata.Name
			}
			label = snapshotLabelForStorage(alias, m.sessionService.SessionName())
		}
		snap, err := fh.Snapshot(msgId, label, msgCount)
		if err != nil {
			return fmt.Sprintf("Failed to create snapshot: %v", err)
		}
		return fmt.Sprintf("✅ Snapshot created\n MsgId: %s\n Label: %s\n Files: %d, Messages: %d",
			snap.MsgId, snap.Label, len(snap.Files), snap.SessionMsgCount)

	case "list":
		// 渲染收口到 formatRewindList（与 /rewind 无参的非交互路径同一实现）。
		sessionID, alias, currentMsgID := m.rewindViewContext()
		rows, err := rewindRowsFromHistory(fh, sessionID, alias, currentMsgID)
		if err != nil {
			return fmt.Sprintf("Failed to list snapshots: %v", err)
		}
		return formatRewindList(rows)

	case "restore":
		// 阶段 4：restore 分支调 fh.Rewind（等价于 /rewind，同时恢复文件+会话）
		if rest == "" {
			return "Usage: /checkpoint restore <msgId>"
		}
		return m.doRewind(rest)

	case "delete":
		// 阶段 5 将由 GC 处理；当前不支持单条快照删除
		return "Snapshot deletion is handled by GC (run on next start). Use /rewind to truncate history."

	case "show":
		// 阶段 4：show 分支调 fh.GetSnapshot
		if rest == "" {
			return "Usage: /checkpoint show #<n>"
		}
		// show 与 restore 用同一套解析：`#N` / 短码 / 完整锚点都能进。
		viewSessionID, viewAlias, viewMsgID := m.rewindViewContext()
		showRows, listErr := rewindRowsFromHistory(fh, viewSessionID, viewAlias, viewMsgID)
		if listErr != nil {
			return fmt.Sprintf("Failed to list snapshots: %v", listErr)
		}
		target, resolveErr := resolveRewindArg(rest, showRows)
		if resolveErr != "" {
			return resolveErr
		}
		snap, err := fh.GetSnapshot(target)
		if err != nil {
			return fmt.Sprintf("Failed to get snapshot: %v", err)
		}
		if snap == nil {
			return fmt.Sprintf("Snapshot not found: %s", rest)
		}
		// 旧索引里的 label 可能是会话主键（之前的写入）⇒ 展示前换回人话别名/短码。
		labelStr := displaySnapshotLabel(snap.Label, viewSessionID, viewAlias)
		return fmt.Sprintf("📌 Snapshot details\n MsgId: %s\n Label: %s\n Files: %d\n Messages: %d\n Created: %s",
			snap.MsgId, labelStr, len(snap.Files), snap.SessionMsgCount,
			snap.Timestamp.Format("2006-01-02 15:04:05"))

	default:
		return fmt.Sprintf("Unknown subcommand: %s\nAvailable: save, list, restore, delete, show", action)
	}
}

// doRewind 执行 Rewind 逻辑：调用 FileHistory.Rewind 恢复文件 + 会话
// 传入 msgId（支持 turn-N 别名解析），支持 --preview 标志仅显示变更预览
// 候选 A：新增 --from-session <id> 标志支持跨会话快照恢复
// 返回用户可读的结果字符串
func (m *Model) doRewind(target string) string {
	fh := m.toolService.FileHistory()
	if fh == nil {
		return "File history not initialized"
	}
	// 解析 --preview 标志
	preview := false
	args := strings.TrimSpace(target)
	if strings.HasSuffix(args, " --preview") {
		preview = true
		args = strings.TrimSuffix(args, " --preview")
	}
	// 候选 A：解析 --from-session <id> 标志（跨会话恢复）
	fromSession := ""
	if strings.HasPrefix(args, "--from-session ") {
		parts := strings.SplitN(args, " ", 3)
		if len(parts) >= 3 {
			fromSession = strings.TrimSpace(parts[1])
			args = strings.TrimSpace(parts[2])
		} else {
			return "Usage: /rewind --from-session <session-id> <msgId>"
		}
	}
	// 候选 A：跨会话恢复路径。★ 必须在解析**之前**分叉：跨会话目标住在
	// 另一个会话的索引里，本地清单解析不了它 ⇒ 原样透传（旧行为不变）。
	if fromSession != "" {
		target := strings.TrimSpace(args)
		if target == "" {
			return "Rewind target must not be empty"
		}
		return m.doCrossSessionRewind(fh, fromSession, target)
	}
	// 解析目标：`#N` / 裸 N（清单序号，1 = 最新）· 短码 · 完整锚点。
	// （`turn-N` 从未实现过 ⇒ resolveRewindArg 给一条能当场纠正的说明，见其注释。）
	viewSessionID, viewAlias, viewMsgID := m.rewindViewContext()
	rows, listErr := rewindRowsFromHistory(fh, viewSessionID, viewAlias, viewMsgID)
	if listErr != nil {
		return fmt.Sprintf("Failed to list snapshots: %v", listErr)
	}
	msgId, resolveErr := resolveRewindArg(args, rows)
	if resolveErr != "" {
		return resolveErr
	}
	// preview 模式：仅显示 DiffStats，不执行实际回退
	if preview {
		exec := m.toolService.Executor()
		currentMsgId := ""
		if exec != nil {
			currentMsgId = exec.CurrentMessageId()
		}
		if currentMsgId == "" {
			return "Cannot preview: no active message"
		}
		stats, err := fh.DiffStats(msgId, currentMsgId)
		if err != nil {
			return fmt.Sprintf("Preview failed: %v", err)
		}
		return fmt.Sprintf("⏮️ Rewind preview (to %s)\n Insertions: %d\n Deletions: %d\n Files changed: %d\n\nRun /rewind %s to confirm and execute",
			msgId, stats.Insertions, stats.Deletions, stats.FilesChanged, msgId)
	}
	// 构造 SessionRestorer 适配器（复用 checkpoint 的 backup-then-rename 策略）
	var restorer filehistory.SessionRestorer
	if store := m.sessionService.Store(); store != nil {
		restorer = &sessionRestorerAdapter{store: store}
	}
	sessionId := m.sessionService.SessionName()
	result, err := fh.Rewind(msgId, restorer, sessionId)
	if err != nil {
		return fmt.Sprintf("Rewind failed: %v", err)
	}
	// 恢复后重载会话（与旧 checkpoint restore 行为一致）
	// A8：失败不得静默 —— 见 rewindReloadAfterRestore 头注释。
	reloadWarn := ""
	if result.SessionRestored && sessionId != "" {
		reloadWarn = rewindReloadAfterRestore(m.sessionService.Store(), sessionId, m.sessionService.SetCurrentSession)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ Rewound to %s\n Files restored: %d, deleted: %d\n Session restored: %v",
		result.TargetMsgId, result.FilesRestored, result.FilesDeleted, result.SessionRestored))
	if reloadWarn != "" {
		sb.WriteString(fmt.Sprintf("\n ⚠️ %s", reloadWarn))
	}
	if result.BackupMsgId != "" {
		sb.WriteString(fmt.Sprintf("\n Backup snapshot: %s", result.BackupMsgId))
	}
	for _, w := range result.Warnings {
		sb.WriteString(fmt.Sprintf("\n ⚠️ %s", w))
	}
	return sb.String()
}

// doCrossSessionRewind 执行跨会话 Rewind（候选 A）。
// 从源会话读取快照，应用到当前工作区 + 当前会话。
func (m *Model) doCrossSessionRewind(fh *filehistory.FileHistory, fromSession, msgId string) string {
	// 从源会话读取快照
	snap, err := fh.GetSnapshotFromSession(fromSession, msgId)
	if err != nil {
		return fmt.Sprintf("Cross-session restore failed (GetSnapshotFromSession): %v", err)
	}
	// 构造 SessionRestorer 适配器
	var restorer filehistory.SessionRestorer
	if store := m.sessionService.Store(); store != nil {
		restorer = &sessionRestorerAdapter{store: store}
	}
	sessionId := m.sessionService.SessionName()
	// 应用快照到当前工作区 + 恢复会话消息
	result, err := fh.RestoreFromSnapshot(snap, restorer, sessionId)
	if err != nil {
		return fmt.Sprintf("Cross-session restore failed (RestoreFromSnapshot): %v", err)
	}
	// 恢复后重载会话
	// A8：失败不得静默 —— 见 rewindReloadAfterRestore 头注释。
	reloadWarn := ""
	if result.SessionRestored && sessionId != "" {
		reloadWarn = rewindReloadAfterRestore(m.sessionService.Store(), sessionId, m.sessionService.SetCurrentSession)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ Cross-session rewound from %s to %s\n Files restored: %d, deleted: %d\n Session restored: %v",
		fromSession, result.TargetMsgId, result.FilesRestored, result.FilesDeleted, result.SessionRestored))
	if reloadWarn != "" {
		sb.WriteString(fmt.Sprintf("\n ⚠️ %s", reloadWarn))
	}
	if result.BackupMsgId != "" {
		sb.WriteString(fmt.Sprintf("\n Backup snapshot: %s", result.BackupMsgId))
	}
	for _, w := range result.Warnings {
		sb.WriteString(fmt.Sprintf("\n ⚠️ %s", w))
	}
	return sb.String()
}

// sessionRestorerAdapter 将 session.Store 适配为 filehistory.SessionRestorer 接口
// 采用与旧 checkpoint.Restore 一致的 backup-then-rename 策略，保证会话替换的原子性
type sessionRestorerAdapter struct {
	store *session.Store
}

// LoadMessages 加载指定会话的所有消息并序列化为 JSON 字节列表
func (a *sessionRestorerAdapter) LoadMessages(sessionId string) ([][]byte, error) {
	sess, err := a.store.Load(sessionId)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", sessionId, err)
	}
	var msgs [][]byte
	for _, m := range sess.GetMessages() {
		data, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("marshal message: %w", err)
		}
		msgs = append(msgs, data)
	}
	return msgs, nil
}

// ReplaceMessages 用消息列表替换当前会话内容（backup-then-rename 原子策略）
func (a *sessionRestorerAdapter) ReplaceMessages(sessionId string, messages [][]byte) error {
	tempID := sessionId + ".restoring"
	backupID := sessionId + ".backup"
	// 反序列化为 session.Message 列表
	var sessionMsgs []session.Message
	for _, data := range messages {
		var msg session.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("unmarshal message: %w", err)
		}
		sessionMsgs = append(sessionMsgs, msg)
	}
	// 写入临时会话
	if len(sessionMsgs) > 0 {
		if err := a.store.AppendBatch(tempID, sessionMsgs); err != nil {
			return fmt.Errorf("write temp session: %w", err)
		}
	} else {
		if err := a.store.Save(tempID, session.NewSession()); err != nil {
			return fmt.Errorf("create empty temp session: %w", err)
		}
	}
	// 备份原会话
	if a.store.Exists(sessionId) {
		if err := a.store.Rename(sessionId, backupID); err != nil {
			return fmt.Errorf("backup existing session: %w", err)
		}
	}
	// 原子替换：temp → sessionId
	if err := a.store.Rename(tempID, sessionId); err != nil {
		if a.store.Exists(backupID) {
			_ = a.store.Rename(backupID, sessionId)
		}
		return fmt.Errorf("rename temp session: %w", err)
	}
	// 压缩归档保护（对抗式审查 1.1 P1 修复）：上面的 Rename(sessionId, backupID)
	// 会把 <sessionId>.archive.jsonl 一并跟随到 backupID 名下，随后
	// Delete(backupID) 的归档连带清理将把整份压缩归档销毁——rewind 一次
	// 即摧毁全部压缩谱系。压缩归档是 append-only 审计轨，必须在清理备份前
	// 还原到原会话名下。还原失败不阻断 rewind（可观测 canary），
	// 此时归档残留在 <backupID>.archive.jsonl，可手工找回。
	if err := os.Rename(a.store.ArchivePath(backupID), a.store.ArchivePath(sessionId)); err != nil && !os.IsNotExist(err) {
		log.Printf("[rewind] restore archive sidecar %s -> %s failed: %v", backupID, sessionId, err)
	}
	// 注：rewind 截断掉的消息本身不入归档——与 /clear 一致，属用户显式
	// 时间旅行语义（契约边界见 archive.go 头注释）。
	// 清理备份
	_ = a.store.Delete(backupID)
	return nil
}

// SaveSession 保存空会话（创建占位会话）
func (a *sessionRestorerAdapter) SaveSession(sessionId string) error {
	return a.store.Save(sessionId, session.NewSession())
}

// handleSearch handles search commands
func (m *Model) handleSearch(args string) string {
	if m.toolService.Searcher() == nil {
		return "Search feature not initialized"
	}

	query := search.SearchQuery{
		MaxResults: 10,
	}

	// Parse arguments
	remaining := parseSearchArgs(args, &query)
	if len(remaining) == 0 && !query.RegexMode {
		return "Usage: /search [--regex|--case-sensitive|--session <id>] <keyword>"
	}

	query.Keywords = remaining

	var resp *search.SearchResponse
	var err error
	if len(query.SessionIDs) == 1 {
		result, searchErr := m.toolService.Searcher().SearchInSession(query.SessionIDs[0], query)
		if searchErr != nil {
			return fmt.Sprintf("Search failed: %v", searchErr)
		}
		resp = &search.SearchResponse{
			Query:         query,
			TotalSearched: 1,
			SearchedAt:    time.Now().UnixMilli(),
		}
		if result != nil && result.MatchCount > 0 {
			resp.TotalMatches = 1
			resp.Results = []search.SearchResult{*result}
		}
	} else {
		resp, err = m.toolService.Searcher().Search(query)
	}

	if resp == nil && err != nil {
		return fmt.Sprintf("Search failed: %v", err)
	}

	return search.FormatResults(resp)
}

// parseSearchArgs parses search command arguments
func parseSearchArgs(input string, query *search.SearchQuery) []string {
	parts := strings.Fields(input)
	var keywords []string

	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--case-sensitive":
			query.MatchCase = true
		case "--regex":
			query.RegexMode = true
		case "--session":
			if i+1 < len(parts) {
				query.SessionIDs = append(query.SessionIDs, parts[i+1])
				i++
			}
		default:
			keywords = append(keywords, parts[i])
		}
	}

	return keywords
}
