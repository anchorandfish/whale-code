package tui

// rewind_view_test.go — （2026-10-14 用户直报「为什么 /rewind 还要 id 号，
// 这样 checkpoint list 看也看不出来什么」）的判据。
//
// 用户看到的三件事，各配一条**可判别**的判据（不靠散文关键词）：
//
//	① 列表「看不懂」 → `displaySnapshotLabel` 把写进 label 的**会话主键 UUID**
// 换回人话别名/短码（读侧）；`snapshotLabelForStorage` 让**新**快照
// 不再存 UUID（写侧）。两条都从生产入口（`handleCheckpoint("list")`
// / `handleCheckpoint("save")`）验，不直调纯函数了事。
//	② 「必须记 id」 → `resolveRewindArg` 接受 `#N`/裸 N/短码；`/rewind` 无参数必须
// 打开上下键选择器（走 `handleCommand("/rewind")` 生产分发）。
//	③ `turn-N` 假承诺 → 该写法**从未实现**，必须给一条能当场纠正的话，而不是
// 「snapshot not found」。
//
// 判据判别性（"before 必须红"）：
// - 写侧：把 `snapshotLabelForStorage` 换回旧的 `label := rest`（空 → 传 SessionName()
// = 会话 UUID）⇒ TestW101_CheckpointSaveStoresAliasNotSessionUUID 立刻变红；
// - 读侧：把 `displaySnapshotLabel` 还原成 `raw` 直出 ⇒ ..._CheckpointListShowsAlias... 变红；
// - 选择器：把 `handleRewind` 无参分支改回打印 usage ⇒ ..._RewindDispatchOpensPicker 变红；
// - 接线：把 app.go 的 `rewindPickerChoiceToolID` 分支摘掉 ⇒ ..._PickerSelection... 变红。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/session"
)

// ───────────────────────── 测试夹具 ─────────────────────────

// newRewindTestModel 隔离 HOME → 经**生产构造** NewModel 建 *Model → 注册退出收尾。
//
// 为什么必须隔离 HOME：filehistory 索引落 `~/.whale/filehistory/index.jsonl`、
// session store 落 `~/.whale/sessions`。不隔离就把测试快照写进真实用户目录
// （探针不得写真实用户文件）。
func newRewindTestModel(t *testing.T) *Model {
	t.Helper()
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	m := newTUIRegressionModel(t)
	guTrackModelTeardown(t, m)
	if m.toolService.FileHistory() == nil {
		t.Fatal("前置失败：Model 未装配 filehistory（降级态）⇒ 本判据不可执行")
	}
	return m
}

// seedRewindSnapshots 用**产品自身的 Snapshot API** 造快照（不伪造索引文件）。
// label 由调用方给： 报告时的存量索引里，它是**会话主键 UUID**。
func seedRewindSnapshots(t *testing.T, m *Model, label string, ids ...string) {
	t.Helper()
	fh := m.toolService.FileHistory()
	for i, id := range ids {
		if _, err := fh.Snapshot(id, label, i+1); err != nil {
			t.Fatalf("Snapshot(%s): %v", id, err)
		}
	}
}

// seedRewindSession 造一个带**人话别名**的会话并载入内存（别名 = Metadata.Name）。
func seedRewindSession(t *testing.T, m *Model, sid, alias string) {
	t.Helper()
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置失败：Model 未装配 session store")
	}
	if err := store.AppendBatchNamed(sid, alias, []session.Message{
		session.UserText(alias),
	}); err != nil {
		t.Fatalf("seed session %s: %v", sid, err)
	}
	loaded, err := store.Load(sid)
	if err != nil {
		t.Fatalf("load session %s: %v", sid, err)
	}
	m.sessionService.SetSessionName(sid)
	m.sessionService.SetCurrentSession(loaded)
}

// rewindTestIDs 返回三条**形状合法且尾段含字母**的 UUID。
//
// 为什么不用 session.GenerateID()：随机尾段有约 2% 概率全为数字 ⇒ 会被
// `resolveRewindArg` 的 Atoi 分支当成序号，短码判据随机会红。固定尾段 a/b/c 去掉随机性。
// 为什么必须**形状合法**：`session.IsSessionID` 要求 36 位 + 4 个连字符位。
var rewindTestIDs = []string{
	"0192a1b0-0000-7000-8000-aaaaaaaaaaaa",
	"0192a1b0-0000-7000-8000-bbbbbbbbbbbb",
	"0192a1b0-0000-7000-8000-cccccccccccc",
}

// ───────────────────────── 纯函数判据 ─────────────────────────

func TestW101_SnapshotLabelForStorage_PrefersAlias(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	cases := []struct {
		name  string
		alias string
		want  string
	}{
		{"有别名存别名", "登录重构", "登录重构"},
		{"别名带空白也认", "  登录重构  ", "登录重构"},
		{"别名缺失回落主键", "", sid},
		{"别名纯空白回落主键", "   ", sid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := snapshotLabelForStorage(c.alias, sid); got != c.want {
				t.Errorf("snapshotLabelForStorage(%q, %q) = %q, want %q", c.alias, sid, got, c.want)
			}
		})
	}
}

// TestW101_DisplaySnapshotLabel_DegradesStorageUUID 把 的**根因**（label 位放了
// 与 msgId 同形的会话 UUID）钉住：读侧必须把它换回人话。
func TestW101_DisplaySnapshotLabel_DegradesStorageUUID(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	const otherID = "0192a1b0-0000-7000-8000-cccccccccccc"

	cases := []struct {
		name    string
		raw     string
		alias   string
		want    string
		wantNot string
	}{
		{"空 label 沿用旧文案", "", "登录重构", "(no label)", ""},
		{"等于当前会话主键 → 换别名", sid, "登录重构", "登录重构", sid},
		{"等于当前会话主键但无别名 → 短码", sid, "", session.ShortID(sid), sid},
		{"别的会话主键 → 短码", otherID, "登录重构", session.ShortID(otherID), otherID},
		{"本来就是人话别名 → 原样", "部署排障", "登录重构", "部署排障", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := displaySnapshotLabel(c.raw, sid, c.alias)
			if got != c.want {
				t.Errorf("displaySnapshotLabel(%q, sid, %q) = %q, want %q", c.raw, c.alias, got, c.want)
			}
			if c.wantNot != "" && strings.Contains(got, c.wantNot) {
				t.Errorf("展示串不得再出现主键 UUID：got %q 含 %q", got, c.wantNot)
			}
		})
	}
}

func TestW101_RewindRows_NewestFirstAndCurrentFlagged(t *testing.T) {
	summaries := []filehistory.SnapshotSummary{
		{MsgId: rewindTestIDs[0], Label: "oldest", SessionMsgCount: 1, FileCount: 2},
		{MsgId: rewindTestIDs[1], Label: "middle", SessionMsgCount: 2, FileCount: 3},
		{MsgId: rewindTestIDs[2], Label: "newest", SessionMsgCount: 3, FileCount: 4},
	}
	rows := rewindRows(summaries, "", "", rewindTestIDs[2])

	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3", len(rows))
	}
	// ★ #1 = 最新（列表翻成新 → 旧），这是「直接退回来只需 #2」的前提。
	if rows[0].MsgID != rewindTestIDs[2] || rows[0].Num != 1 {
		t.Errorf("#1 应为最新快照 %s（Num=1），实际 MsgID=%s Num=%d", rewindTestIDs[2], rows[0].MsgID, rows[0].Num)
	}
	if rows[2].MsgID != rewindTestIDs[0] || rows[2].Num != 3 {
		t.Errorf("#3 应为最旧快照 %s（Num=3），实际 MsgID=%s Num=%d", rewindTestIDs[0], rows[2].MsgID, rows[2].Num)
	}
	// 字段搬运不得错位（Files/Msgs 与来源一一对应）。
	if rows[0].Files != 4 || rows[0].Msgs != 3 {
		t.Errorf("#1 应带 Files=4 Msgs=3，实际 Files=%d Msgs=%d", rows[0].Files, rows[0].Msgs)
	}
	// (current) 是**结构比对**结果（锚点相等），不是推断。
	if !rows[0].Current {
		t.Error("锚点 == 当前消息锚点的行必须标 Current")
	}
	for i := 1; i < 3; i++ {
		if rows[i].Current {
			t.Errorf("#%d 不应标 Current（锚点 %s ≠ 当前 %s）", rows[i].Num, rows[i].MsgID, rewindTestIDs[2])
		}
	}
	// 短码必须非空且组内互异（否则用户仍分不清两行）。
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Handle == "" {
			t.Errorf("行 %s 短码为空", r.MsgID)
		}
		if seen[r.Handle] {
			t.Errorf("短码在组内撞车：%q", r.Handle)
		}
		seen[r.Handle] = true
	}
}

// TestW101_FormatRewindList_HasFieldNamesAndLegend 行是**数据**：每一行都要能说出
// 「哪一段是编号、哪一段是名字」，并且说明行要教用户「#1 是最新」。
func TestW101_FormatRewindList_HasFieldNamesAndLegend(t *testing.T) {
	rows := rewindRows([]filehistory.SnapshotSummary{
		{MsgId: rewindTestIDs[0], Label: "登录重构", SessionMsgCount: 1, FileCount: 2},
	}, "", "", rewindTestIDs[0])

	out := formatRewindList(rows)

	for _, want := range []string{"#1", "登录重构", "file(s)", "msgs", "#1 is the newest"} {
		if !strings.Contains(out, want) {
			t.Errorf("清单输出缺少 %q：\n%s", want, out)
		}
	}
	// 空列表不得让用户面对一片空白 —— 必须解释"快照是怎么来的"。
	empty := formatRewindList(nil)
	if !strings.Contains(empty, "No snapshots yet") {
		t.Errorf("空清单应给出说明，实际 %q", empty)
	}
}

// TestW101_ResolveRewindArg_IndexHandleAndID 「直接退回来」的正门：序号 / 短码 / 完整锚点。
func TestW101_ResolveRewindArg_IndexHandleAndID(t *testing.T) {
	rows := rewindRows([]filehistory.SnapshotSummary{
		{MsgId: rewindTestIDs[0]},
		{MsgId: rewindTestIDs[1]},
		{MsgId: rewindTestIDs[2]},
	}, "", "", "")

	cases := []struct {
		arg  string
		want string
	}{
		{"#1", rewindTestIDs[2]}, // #1 = 最新
		{"1", rewindTestIDs[2]},  // 裸序号同样认
		{"#3", rewindTestIDs[0]}, // #3 = 最旧
		{rows[0].Handle, rewindTestIDs[2]},
		{strings.ToUpper(rows[1].Handle), rewindTestIDs[1]}, // 短码大小写不敏感
		{rewindTestIDs[1], rewindTestIDs[1]},                // 完整锚点透传（旧行为不变）
	}
	for _, c := range cases {
		got, errOut := resolveRewindArg(c.arg, rows)
		if errOut != "" {
			t.Errorf("resolveRewindArg(%q) 不应报错：%s", c.arg, errOut)
			continue
		}
		if got != c.want {
			t.Errorf("resolveRewindArg(%q) = %q, want %q", c.arg, got, c.want)
		}
	}
}

func TestW101_ResolveRewindArg_RejectsTurnNAndOutOfRange(t *testing.T) {
	rows := rewindRows([]filehistory.SnapshotSummary{
		{MsgId: rewindTestIDs[0]},
		{MsgId: rewindTestIDs[1]},
	}, "", "", "")

	// turn-N 从未实现 ⇒ 必须给能当场纠正的话，而不是让它掉进"snapshot not found"。
	got, errOut := resolveRewindArg("turn-2", rows)
	if got != "" {
		t.Errorf("turn-N 不得解析出锚点，实际 %q", got)
	}
	if !strings.Contains(errOut, "never worked") {
		t.Errorf("turn-N 的纠正文案应点明「从未生效」，实际 %q", errOut)
	}

	// 越界序号必须报错并给出有效范围。
	for _, arg := range []string{"0", "#0", "#9", "9"} {
		got, errOut := resolveRewindArg(arg, rows)
		if got != "" || errOut == "" {
			t.Errorf("resolveRewindArg(%q) 应报越界错，实际 msgID=%q err=%q", arg, got, errOut)
		}
		if !strings.Contains(errOut, "does not exist") {
			t.Errorf("越界文案应说明快照不存在，实际 %q", errOut)
		}
	}

	// 空参数 = 用法说明（不是崩、也不是静默）。
	got, errOut = resolveRewindArg("", rows)
	if got != "" || !strings.Contains(errOut, "Usage") {
		t.Errorf("空参数应回用法，实际 msgID=%q err=%q", got, errOut)
	}
}

// TestW101_RewindPickerOptions_DefaultsToStepBack 打开菜单**不得有副作用**，且初始高亮
// 应落在「退一步」（第一条非 current 行），而不是 #1（== 当前状态，退它几乎无事发生）。
func TestW101_RewindPickerOptions_DefaultsToStepBack(t *testing.T) {
	summaries := []filehistory.SnapshotSummary{
		{MsgId: rewindTestIDs[0]},
		{MsgId: rewindTestIDs[1]},
		{MsgId: rewindTestIDs[2]},
	}
	rows := rewindRows(summaries, "", "", rewindTestIDs[2]) // current = 最新

	opts, def := rewindPickerOptions(rows)
	if len(opts) != 3 {
		t.Fatalf("选项数 = %d, want 3", len(opts))
	}
	if opts[0].Value != rewindTestIDs[2] {
		t.Errorf("首选项 Value 应为最新锚点 %s，实际 %s", rewindTestIDs[2], opts[0].Value)
	}
	if def != 1 {
		t.Errorf("初始高亮应为退一步（下标 1），实际 %d", def)
	}

	// 超过上限时截断，且不得 panic。
	many := make([]filehistory.SnapshotSummary, 0, rewindPickerMaxRows+3)
	for i := 0; i < rewindPickerMaxRows+3; i++ {
		many = append(many, filehistory.SnapshotSummary{MsgId: rewindTestIDs[i%3]})
	}
	manyRows := rewindRows(many, "", "", "")
	optsMany, _ := rewindPickerOptions(manyRows)
	if len(optsMany) != rewindPickerMaxRows {
		t.Errorf("超过上限应截到 %d 项，实际 %d", rewindPickerMaxRows, len(optsMany))
	}
}

// ───────────────────────── 生产路径判据 ─────────────────────────

// TestW101_CheckpointSaveStoresAliasNotSessionUUID 写侧：`/checkpoint save`（无 label）
// 落盘的 label 必须是**会话人话别名**，不再是与 msgId 同形的会话主键 UUID。
//
// 判别性：把 handleCheckpoint 的 label 分支还原成"空 → SessionName()"即转红。
func TestW101_CheckpointSaveStoresAliasNotSessionUUID(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	const alias = "登录重构"

	m := newRewindTestModel(t)
	seedRewindSession(t, m, sid, alias)
	m.toolService.Executor().SetCurrentMessageId(rewindTestIDs[2])

	out := m.handleCheckpoint("save")
	t.Logf("save 输出=%q", out)

	if !strings.Contains(out, "Label: "+alias) {
		t.Errorf("save 应报落盘 label = 别名 %q，实际 %q", alias, out)
	}
	if strings.Contains(out, "Label: "+sid) {
		t.Errorf("save 不得把会话主键当 label 落盘（ 根因），实际 %q", out)
	}

	// 走 FileHistory 的读面复核（不只看输出文案）。
	summaries, err := m.toolService.FileHistory().ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("save 后索引里应有一条快照")
	}
	if got := summaries[len(summaries)-1].Label; got != alias {
		t.Errorf("落盘 label = %q, want 别名 %q", got, alias)
	}
}

// TestW101_CheckpointListShowsAliasNotRawUUID 读侧：存量索引里的 label 是会话 UUID，
// `/checkpoint list` 必须把它渲染成人话别名（否则用户"看不懂"当场复发）。
//
// 判别性：把 `displaySnapshotLabel` 换回 raw 直出即转红。
func TestW101_CheckpointListShowsAliasNotRawUUID(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	const alias = "登录重构"

	m := newRewindTestModel(t)
	seedRewindSession(t, m, sid, alias)
	// 复刻 存量索引：label 位放的是**会话主键**。
	seedRewindSnapshots(t, m, sid, rewindTestIDs[2])
	m.toolService.Executor().SetCurrentMessageId(rewindTestIDs[2])

	out := m.handleCheckpoint("list")
	t.Logf("list 输出=%q", out)

	if !strings.Contains(out, alias) {
		t.Errorf("清单应显示会话别名 %q，实际 %q", alias, out)
	}
	if strings.Contains(out, sid) {
		t.Errorf("清单不得再出现会话主键 UUID（ 原症状），实际 %q", out)
	}
	if !strings.Contains(out, "#1 is the newest") {
		t.Errorf("清单说明行应教用户「#1 是最新」，实际 %q", out)
	}
}

// TestW101_RewindDispatchOpensPicker 生产分发入口：敲 `/rewind`（无参）必须打开
// 上下键选择器，而不是要求用户背编号。
//
// 判别性：把 handleRewind 的无参分支改回「打印 usage」即转红。
func TestW101_RewindDispatchOpensPicker(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	const alias = "登录重构"

	m := newRewindTestModel(t)
	seedRewindSession(t, m, sid, alias)
	seedRewindSnapshots(t, m, alias, rewindTestIDs[0], rewindTestIDs[1], rewindTestIDs[2])
	m.toolService.Executor().SetCurrentMessageId(rewindTestIDs[2])

	out := m.handleCommand("/rewind")
	t.Logf("/rewind 无参输出=%q", out)

	if m.sessionService.State() != StateWaitingChoice {
		t.Fatalf("/rewind 无参应进入选择态，实际 %v（out=%q）", m.sessionService.State(), out)
	}
	pc := m.toolService.PendingChoice()
	if pc == nil {
		t.Fatal("/rewind 无参未设置 PendingChoice")
	}
	if pc.ToolID != rewindPickerChoiceToolID {
		t.Errorf("ToolID = %q, want %q", pc.ToolID, rewindPickerChoiceToolID)
	}
	if len(pc.Options) != 3 {
		t.Fatalf("选项数 = %d, want 3", len(pc.Options))
	}
	// 选项 Value 必须是**完整锚点**（能直接喂给 Rewind）；Label 必须带人话别名。
	if pc.Options[0].Value != rewindTestIDs[2] {
		t.Errorf("首选项 Value 应为最新锚点，实际 %q", pc.Options[0].Value)
	}
	if !strings.Contains(pc.Options[0].Label, alias) {
		t.Errorf("选项 Label 应含别名 %q，实际 %q", alias, pc.Options[0].Label)
	}
	// 不可逆必须在**下手前**说清。
	if !strings.Contains(out, "cannot be undone") {
		t.Errorf("打开选择器应提示不可撤销，实际 %q", out)
	}
}

// TestW101_RewindByIndexEndToEnd 生产路径：#N 序号必须真的落到对应快照上（不只是解析出字符串）。
//
// 用索引截断这一**结构面**做判据：回退到 #2（= 中间那条）后，其后（含最新那条与
// Rewind 自建的 backup）全部被 truncateIndexAfter 剪掉 ⇒ 只应剩 2 条。
func TestW101_RewindByIndexEndToEnd(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	const alias = "登录重构"

	m := newRewindTestModel(t)
	seedRewindSession(t, m, sid, alias)
	seedRewindSnapshots(t, m, alias, rewindTestIDs[0], rewindTestIDs[1], rewindTestIDs[2])
	m.toolService.Executor().SetCurrentMessageId(rewindTestIDs[2])

	fh := m.toolService.FileHistory()
	if before, _ := fh.ListSnapshots(); len(before) != 3 {
		t.Fatalf("前置：应有 3 条快照，实际 %d", len(before))
	}

	out := m.handleRewind("#2") // #2 = 中间（= rewindTestIDs[1]）
	t.Logf("/rewind #2 输出=%q", out)

	if !strings.Contains(out, "Rewound to "+rewindTestIDs[1]) {
		t.Fatalf("#2 应解析为 %s 并回退，实际输出 %q", rewindTestIDs[1], out)
	}
	after, err := fh.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("回退到 #2 后应只保留前 2 条（其后 + backup 被截断），实际 %d：%+v", len(after), after)
	}
	if after[len(after)-1].MsgId != rewindTestIDs[1] {
		t.Errorf("保留的最后一条应为目标锚点 %s，实际 %s", rewindTestIDs[1], after[len(after)-1].MsgId)
	}
}

// TestW101_PickerSelectionExecutesRewind 接线判据：用户在菜单里选了一条，
// `choiceResultMsg` 必须把它送回**同一条回退路径**（doRewind），而不是在菜单里
// 另实现一遍（双实现漂移）。选完状态必须回到 Idle。
//
// 判别性：摘掉 app.go 的 `rewindPickerChoiceToolID` 分支即转红（选择值不会被消费）。
func TestW101_PickerSelectionExecutesRewind(t *testing.T) {
	const sid = "0192a1b0-0000-7000-8000-aaaaaaaaaaaa"
	const alias = "登录重构"

	m := newRewindTestModel(t)
	seedRewindSession(t, m, sid, alias)
	seedRewindSnapshots(t, m, alias, rewindTestIDs[0], rewindTestIDs[1], rewindTestIDs[2])
	m.toolService.Executor().SetCurrentMessageId(rewindTestIDs[2])

	// 先真的打开菜单（Enter 之前的状态）。
	m.handleCommand("/rewind")
	if m.sessionService.State() != StateWaitingChoice {
		t.Fatalf("前置：/rewind 应打开选择器，实际状态 %v", m.sessionService.State())
	}

	// 模拟用户在菜单里选中状态最旧的那条（Value = 完整锚点）。
	updatedModel, _ := m.Update(choiceResultMsg{
		ToolID: rewindPickerChoiceToolID,
		Answer: rewindTestIDs[0],
	})
	updated := updatedModel.(Model)

	if updated.sessionService.State() != StateIdle {
		t.Errorf("选择后状态应回到 Idle，实际 %v", updated.sessionService.State())
	}

	fh := m.toolService.FileHistory()
	after, err := fh.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(after) != 1 || after[0].MsgId != rewindTestIDs[0] {
		t.Fatalf("选择最旧快照后应只剩它一条（其余被截断），实际 %+v", after)
	}

	// 取消（Esc）不得执行回退 —— 否则"打开看看"变成破坏性动作。
	m2 := newRewindTestModel(t)
	seedRewindSession(t, m2, sid, alias)
	seedRewindSnapshots(t, m2, alias, rewindTestIDs[0], rewindTestIDs[1], rewindTestIDs[2])
	m2.toolService.Executor().SetCurrentMessageId(rewindTestIDs[2])
	m2.handleCommand("/rewind")
	cancelledModel, _ := m2.Update(choiceResultMsg{ToolID: rewindPickerChoiceToolID, Cancelled: true})
	cancelled := cancelledModel.(Model)
	if cancelled.sessionService.State() != StateIdle {
		t.Errorf("取消后状态应回到 Idle，实际 %v", cancelled.sessionService.State())
	}
	if got, _ := m2.toolService.FileHistory().ListSnapshots(); len(got) != 3 {
		t.Errorf("取消不得执行回退（快照应仍为 3 条），实际 %d", len(got))
	}
}
