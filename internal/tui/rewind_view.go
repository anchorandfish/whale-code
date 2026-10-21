package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/session"
)

// 本文件是 `/rewind` / `/checkpoint` 的**视图与文案 SSOT**。
//
// 为什么单独一个文件（，2026-10-14 用户直报「为什么 /rewind 还要 id 号，
// checkpoint list 看也看不出来什么」）：
//
//	① 同一份列表/usage 文案原先在 `commands_rewind.go`（Model 路，app 入口）与
// `runner_core.go`（runnerCore 路，bridge/plain 入口）各写一份 —— 逐字重复，
// 属 ``「同语义两处实现」。只改一处 = 半个修复（用户仍会在另一个入口
// 看到旧形态）。现在实现与文案都收在这里，两侧只做薄转发。
//	② 用户看不懂的**根因不在"多余"**，而在展示层把**内部主键**当人话递给用户：
// 快照 label 里装的是会话 UUID（== 会话主键），它和 msgId 形态相同 ⇒ 一行里
// 两串 UUID、无字段名。修法分两半：**写侧**改存会话别名
// （`snapshotLabelForStorage`），**读侧**把历史遗留的 UUID label 换回别名/短码
// （`displaySnapshotLabel`）—— 只修写侧的话，用户**已有**的那条快照照样是哑谜。

// rewindPickerChoiceToolID 是交互式回退选择器的 PendingChoice.ToolID。
//
// 与 sessionPickerChoiceToolID / permissionModeChoiceToolID 同类：TUI 本地选择，
// 不进 AskUser 工具链路，由 app.go 的 choiceResultMsg 分支按 ToolID 分派。
const rewindPickerChoiceToolID = "__whale_rewind_picker__"

// rewindPickerMaxRows 是选择器一次展示的最大快照数（列表按「新 → 旧」）。
// 超出部分用 `/checkpoint list` 看全量。
const rewindPickerMaxRows = 12

// snapshotLabelForStorage 返回**落盘用**的快照标签：优先会话人话名（别名），
// 回落到会话主键。
//
// 历史（根因）:轮末自动建快照原先传 `SessionName()`，而它存的是**会话主键**
// （`SetSessionName(id)`，见 `lifecycle.go`；别名另存在 `Session.Metadata.Name`）
// ⇒ `/checkpoint list` 的 label 位是第二串 UUID。
func snapshotLabelForStorage(alias, sessionID string) string {
	if a := strings.TrimSpace(alias); a != "" {
		return a
	}
	return sessionID
}

// displaySnapshotLabel 把快照标签渲染成人能读的一串。
//
// 三类取值：
// - 空 ⇒ `(no label)`（沿用旧列表文案）；
// - 等于**当前会话主键** ⇒ 换会话别名；别名缺失时用短码（旧条目走这里，
// 这正是 那两串 UUID 的来源）；
// - 形如 UUID（别的会话主键）⇒ 短码；
// - 其余（人话别名）⇒ 原样。
//
// 为什么读侧也必须改：写侧只影响**新**快照，用户报障时索引里那条旧快照
// 仍是 UUID label ⇒ 不换的话"看不懂"当场复发。
func displaySnapshotLabel(raw, sessionID, alias string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "(no label)"
	case sessionID != "" && raw == sessionID:
		if a := strings.TrimSpace(alias); a != "" {
			return a
		}
		return session.ShortID(sessionID)
	case session.IsSessionID(raw):
		return session.ShortID(raw)
	default:
		return raw
	}
}

// rewindRow 是列表与选择器共用的一行快照视图。
//
// 序号 `Num` 从**最新**数起：`#1` = 最新（通常就是当前状态），`#2` = 退一步。
// 这样"直接退回来"只需敲 `/rewind #2`，不必认识 UUID。
type rewindRow struct {
	Num     int
	MsgID   string // 快照锚点（那一轮最后一条消息的 UUID）—— /rewind 的正式取值
	Handle  string // 可复制的短码（与 /session 同一套算法）
	Label   string // 人话标签
	Files   int
	Msgs    int
	At      time.Time
	Current bool // 该快照锚点 == 当前消息锚点；**结构性事实**，不靠推断
}

// rewindRows 把 `ListSnapshots()` 的（旧 → 新）序列翻成展示用行（新 → 旧）。
//
// `currentMsgID` 传 `Executor.CurrentMessageId()` —— 与 turn-diff / 快照写入
// 同一个锚点来源，因此"(current)"标记是结构比对的结果，不是猜的。
func rewindRows(summaries []filehistory.SnapshotSummary, sessionID, alias, currentMsgID string) []rewindRow {
	if len(summaries) == 0 {
		return nil
	}
	ids := make([]string, 0, len(summaries))
	for _, s := range summaries {
		ids = append(ids, s.MsgId)
	}
	// ★ 复用 /session 的同一套短码算法（`session.Handles` 整组算，避免固定 8 位
	// 在两行碰巧同尾段时分不清）。不另写一份 —— 短码规则一改必须两侧同时生效。
	handles := session.Handles(ids)

	rows := make([]rewindRow, 0, len(summaries))
	for i := len(summaries) - 1; i >= 0; i-- {
		s := summaries[i]
		rows = append(rows, rewindRow{
			Num:     len(rows) + 1,
			MsgID:   s.MsgId,
			Handle:  handles[s.MsgId],
			Label:   displaySnapshotLabel(s.Label, sessionID, alias),
			Files:   s.FileCount,
			Msgs:    s.SessionMsgCount,
			At:      s.Timestamp,
			Current: currentMsgID != "" && s.MsgId == currentMsgID,
		})
	}
	return rows
}

// formatRewindRowLine 渲染一行。字段名写在行内（不再是一串没有名字的值），
// 用户能说出"哪个是编号、哪个是名字"。
func formatRewindRowLine(r rewindRow) string {
	line := fmt.Sprintf(" #%d  %s  [%s]  %d file(s) · %d msgs · %s",
		r.Num, r.Label, r.Handle, r.Files, r.Msgs, r.At.Format("2006-01-02 15:04"))
	if r.Current {
		line += "  (current)"
	}
	return line
}

// formatRewindList 渲染完整快照清单（`/checkpoint list` 与 `/rewind` 无参的
// 非交互路径共用）。
//
// 行内不含中文（与 `/session list` 的既有形态一致：**行是数据**，语言只出现在
// 说明行上）；说明行走 `bi` ⇒ 英文主 + 缩进中文辅，满足双语形态门。
func formatRewindList(rows []rewindRow) string {
	if len(rows) == 0 {
		return bi("No snapshots yet (a snapshot is written at the end of each turn)",
			"还没有快照（每轮对话结束时自动写一条）")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 Snapshots (%d)\n", len(rows)))
	for _, r := range rows {
		sb.WriteString(formatRewindRowLine(r))
		sb.WriteString("\n")
	}
	sb.WriteString(bi(
		"Roll back with /rewind #<n> — #1 is the newest · /checkpoint show #<n> for details",
		"回退用 /rewind #<n>（#1 是最新）· 看详情用 /checkpoint show #<n>"))
	return sb.String()
}

// rewindPickerOptions 把行转成选择器选项，并给出初始高亮位。
//
// 初始高亮 = **第一条非 current 行**（通常就是 `#2` = 退一步），而不是 `#1`
// —— `#1` 就是当前状态，回退到它几乎无事发生，高亮它等于把最常见的动作
// 藏到下一次按键后面。只高亮、不写入 ⇒ 打开菜单没有副作用。
func rewindPickerOptions(rows []rewindRow) (opts []ChoiceOption, defaultIdx int) {
	n := len(rows)
	if n > rewindPickerMaxRows {
		n = rewindPickerMaxRows
	}
	opts = make([]ChoiceOption, 0, n)
	for i := 0; i < n; i++ {
		opts = append(opts, ChoiceOption{
			Label: formatRewindRowLine(rows[i]),
			Value: rows[i].MsgID,
		})
		if !rows[i].Current && defaultIdx == 0 && i > 0 {
			defaultIdx = i
		}
	}
	return opts, defaultIdx
}

// resolveRewindArg 把用户给的参数解析成快照锚点（msgId）。
//
// 接受四种写法：
// - `#N` / 裸 `N` —— 清单里的序号（1 = 最新）。这是"直接退回来"的正门。
// - 短码（如 `a3f2c1d9`）—— 从清单里复制的那一串。
// - 完整 msgId —— 与旧行为一致（脚本/既有笔记里的写法）。
//
// `turn-N` 不在此列：**它从来没实现过**（旧注释自认"当前未维护 turn 索引，
// 暂直接当作 msgId 透传"），照 usage 抄一定拿到 `snapshot not found`。这里不
// 假装支持它，而是给一条能当场纠正的话；`internal/command` 的 Usage 同批已删。
func resolveRewindArg(arg string, rows []rewindRow) (msgID, errOut string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", formatRewindUsage()
	}
	if strings.HasPrefix(strings.ToLower(arg), "turn-") {
		return "", bi(
			"Turn numbers are not indexed; /rewind turn-N never worked. Use /rewind #N (see /checkpoint list, #1 is the newest).",
			"「第 N 轮」没有被索引，/rewind turn-N 从未生效过。请用 /rewind #N（见 /checkpoint list，#1 是最新）。")
	}

	raw := arg
	explicitIndex := false
	if strings.HasPrefix(raw, "#") {
		raw = strings.TrimPrefix(raw, "#")
		explicitIndex = true
	}
	if n, err := strconv.Atoi(raw); err == nil && (explicitIndex || raw != "") {
		if n < 1 || n > len(rows) {
			return "", bi(
				fmt.Sprintf("Snapshot #%d does not exist (valid: #1–#%d, newest first).", n, len(rows)),
				fmt.Sprintf("没有第 #%d 个快照（有效范围 #1–#%d，按新→旧）。", n, len(rows)))
		}
		return rows[n-1].MsgID, ""
	}

	// 短码：清单里复制出来的那一串（大小写不敏感，与 /session 的用法一致）。
	for _, r := range rows {
		if r.Handle != "" && strings.EqualFold(r.Handle, arg) {
			return r.MsgID, ""
		}
	}
	// 其余按完整锚点透传 —— 交给 FileHistory 用它自己的错误信息说明找不到。
	return arg, ""
}

// formatRewindUsage 是 `/rewind` 的用法说明（两个入口共用同一份文案）。
func formatRewindUsage() string {
	return bi(
		"Usage: /rewind [#N | <snapshot-id>] [--preview]\n       /rewind --from-session <session-id> <snapshot-id>\n       /rewind            open the snapshot picker",
		"用法：/rewind [#N | <快照编号>] [--preview]\n      /rewind --from-session <会话-id> <快照编号>\n      /rewind            打开快照选择器")
}

// formatCheckpointUsage 是 `/checkpoint` 的用法说明（两个入口共用同一份文案）。
// 与 registry 子命令面（checkpointCommands 的 5 个全名子命令）对齐，
// 补上此前漏登记的 delete 行 —— 弹窗候选（SSOT=registry）与 usage 文案不得各说各话。
func formatCheckpointUsage() string {
	return bi(
		"Usage:\n /checkpoint save [label] — create a snapshot\n /checkpoint list — list snapshots (#1 is the newest)\n /checkpoint restore #<n> — roll back (alias of /rewind)\n /checkpoint show #<n> — view snapshot details\n /checkpoint delete #<n> — delete a snapshot",
		"用法：\n /checkpoint save [标签] — 建一条快照\n /checkpoint list — 列出快照（#1 是最新）\n /checkpoint restore #<n> — 回退（等价于 /rewind）\n /checkpoint show #<n> — 看快照详情\n /checkpoint delete #<n> — 删除一条快照")
}

// rewindRowsFromHistory 是"取列表行"的单一实现：两侧入口（Model / runnerCore）
// 都只调它，不各自 ListSnapshots + 拼装。
func rewindRowsFromHistory(fh *filehistory.FileHistory, sessionID, alias, currentMsgID string) ([]rewindRow, error) {
	if fh == nil {
		return nil, fmt.Errorf("file history not initialized")
	}
	summaries, err := fh.ListSnapshots()
	if err != nil {
		return nil, err
	}
	return rewindRows(summaries, sessionID, alias, currentMsgID), nil
}

// rewindViewContext 收集列表/选择器需要的三件事实。
//
// 两侧入口各自实现一份是**有意的**（`Model` 与 `runnerCore` 的依赖持有者不同：
// 一个走 sessionService/toolService 接口，一个持有裸字段），但**取值来源相同** ——
// 会话主键来自 SessionName()、别名来自 Session.Metadata.Name、当前锚点来自
// Executor.CurrentMessageId()。任一来源变了必须两侧同改。
func (m *Model) rewindViewContext() (sessionID, alias, currentMsgID string) {
	sessionID = m.sessionService.SessionName()
	if sess := m.sessionService.CurrentSession(); sess != nil {
		alias = sess.Metadata.Name
	}
	if exec := m.toolService.Executor(); exec != nil {
		currentMsgID = exec.CurrentMessageId()
	}
	return sessionID, alias, currentMsgID
}

func (c *runnerCore) rewindViewContext() (sessionID, alias, currentMsgID string) {
	sessionID = c.SessionName
	if c.Session != nil {
		alias = c.Session.Metadata.Name
	}
	if c.Executor != nil {
		currentMsgID = c.Executor.CurrentMessageId()
	}
	return sessionID, alias, currentMsgID
}
