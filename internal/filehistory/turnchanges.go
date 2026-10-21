package filehistory

import (
	"fmt"
	"sort"
)

// TurnChange 是本轮某个文件的**真实提交**变更（净变更）。
//
// 为什么是「内容」而不是 hash：本轮变更的消费者是**给人看的东西**（轮末卡片的
// `+N -M`、`/diff` 的逐文件正文）。hash 对它们不可读，且调用方拿不到 CAS 的钥匙
// （contentDir 属于本包）⇒ 在这里一次性读回内容。
//
// 为什么 IsNew/IsDeleted 必须显式：空文件与"文件不存在"是两件事，
// 不能用 `len(content) == 0` 代替。
type TurnChange struct {
	Path      string // 绝对路径（与索引里存的同一形态）
	Before    string // 变更前内容；不存在（新建）时为空串，由 IsNew 表达
	After     string // 变更后内容；不存在（删除）时为空串，由 IsDeleted 表达
	IsNew     bool   // 本轮首次出现（Track 时读不到文件 ⇒ BeforeHash 为 nil）
	IsDeleted bool   // 本轮被删除（TrackAfter 读到 NotExist ⇒ FileChange.Deleted）
}

// TurnChanges 汇总某一轮（msgId = 该轮**用户消息** UUID）内真实提交的文件净变更。
//
// ★ 事实来源（第一性原理 · 与 同类 CLI turn_diff_tracker.rs:48-50 同一条思路）：
// 只吃**已经提交的变更**——即写类工具在真正落盘前后写进索引的 track 条目
// （`tools/file.go` 的 trackBefore/pushFileChange、`tools/apply_diff.go` 的
// trackBeforePath/commitPatch）。因此它：
// - **不重读工作区**：内容全部从内容寻址存储（CAS）读回，用户在会话外的手工改动
// 不会被冒充成"AI 这轮改的"；
// - **不解析 AI 的话**：模型说自己改了什么都不影响这里的事实。
//
// ★ 净变更口径（为什么不是"每条 track 各报一次"）：一轮里同一文件常被改多次
// （read→edit→再 edit），逐条报会把中间态也当成结果。这里对每个 path 取
// **该轮第一条 track 的 BeforeHash** 作基线、**最后一条已提交 track 的 AfterHash** 作终态
// ⇒ 得到的是"这轮开始 → 这轮结束"的净变化，也正是用户问"你到底改了什么"的答案。
//
// ★ msgId 为什么就是"轮"：`SetCurrentMessageId` 只在 sendInput / plain / bridge 的
// **发送用户消息**处各调一次（stream.go:300 · plain.go:297 · bridge.go:520），
// 取值 = 刚入列的用户消息 UUID ⇒ 整轮（含多次工具往返、多条 assistant 消息）恒定。
// 轮末快照 `Snapshot` 用的也是同一个值 ⇒ 卡片、`/diff`、快照三者同锚点。
//
// ★ 已知边界（如实标注，勿当成 bug）：
// - **未提交的 track 一律剔除**（工具失败、TrackAfter 从未调用）：
// 宁可少报，也不把"没发生的事"说成变更；
// - **内容没变的"写入"剔除**（迭代③，2026-10-09）：`cat a > b` 且 b 本来等于 a
// 不是变更，不列（防 `+0 -0` 伪变更行）；
// - **bash 只入账 `>`/`>>` 等字面重定向**（迭代③）：由 bash handler 经
// `validator.BashWriteTargets`（AST 提取）静态提目标后走 Track/TrackAfter；
// 变量路径/命令替换**不猜**；tee/mv/cp/脚本内部写/后台 bash（TaskMgr 链）/Monitor
// 仍不入账 —— 漏报方向（2026-10-16 用户拍板：卡片提示行取消 —— 每轮必现太吵，
// 未列出的 bash 写入不再有卡片级提示，如实记录为已知边界）。
// - msgId 为空 ⇒ 返回空集（Track/Snapshot 对空 msgId 本就是 no-op）。
func (h *FileHistory) TurnChanges(msgId string) ([]TurnChange, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if msgId == "" {
		return nil, nil
	}

	type acc struct {
		before    *string
		beforeSet bool
		after     *string
		committed bool
		deleted   bool
	}
	seen := make(map[string]*acc)

	for i := range h.indexCache {
		entry := &h.indexCache[i]
		if entry.Type != "track" || entry.MsgId != msgId {
			continue
		}
		for _, fc := range entry.Files {
			a, ok := seen[fc.Path]
			if !ok {
				a = &acc{}
				seen[fc.Path] = a
			}
			// 基线只取**本轮第一次**看到的 BeforeHash（后续条目是中间态）。
			if !a.beforeSet {
				a.before = fc.BeforeHash
				a.beforeSet = true
			}
			// 终态取**最后一条已提交**的形态；AfterHash==nil 且未标 Deleted ⇒ 未提交，忽略。
			switch {
			case fc.AfterHash != nil:
				a.after = fc.AfterHash
				a.committed = true
				a.deleted = false
			case fc.Deleted:
				a.after = nil
				a.committed = true
				a.deleted = true
			}
		}
	}

	out := make([]TurnChange, 0, len(seen))
	for path, a := range seen {
		if !a.committed {
			continue
		}
		// no-op 剔除（迭代③，2026-10-09）：before 与 after 内容哈希一致 ⇒ 内容没变。
		// 典型来源是 bash 重定向 `cat a > b`（b 本来就等于 a）—— 内容没变的"写入"不是变更，
		// 列出来就是 `+0 -0` 的伪变更行。哈希相等即内容相等（CAS 的 key 就是内容哈希）。
		if a.before != nil && a.after != nil && !a.deleted && *a.before == *a.after {
			continue
		}
		// 净零剔除：轮首不存在（before=nil）且轮末也不存在（after=nil，Deleted 由
		// TrackAfter 的 NotExist 分支标出）⇒ 净变化为零。典型来源是重定向目标
		// 创建失败（`> 坏路径` 时 shell 起了进程但文件没写成）—— 不是"新建+删除"。
		if a.before == nil && a.after == nil {
			continue
		}
		tc := TurnChange{Path: path, IsNew: a.before == nil, IsDeleted: a.deleted}
		if a.before != nil {
			content, err := readContent(h.contentDir, *a.before)
			if err != nil {
				return nil, fmt.Errorf("read before content for %s: %w", path, err)
			}
			tc.Before = string(content)
		}
		if a.after != nil {
			content, err := readContent(h.contentDir, *a.after)
			if err != nil {
				return nil, fmt.Errorf("read after content for %s: %w", path, err)
			}
			tc.After = string(content)
		}
		out = append(out, tc)
	}

	// 按路径排序：输出确定（判据可断言），也避免 map 遍历顺序让卡片行序抖动。
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
