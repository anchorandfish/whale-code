// Package session — 归档式压缩（archive-on-compact）测试。
//
// 设计契约（见 2026-09-07 方案讨论）：
//  1. Rewrite/Compact 物理删除消息前，被删消息必须先落入 sidecar 归档
//     （<sessionID>.archive.jsonl），写入顺序 = 归档先于主文件重写。
//  2. 归档按"压缩记录"组织：每条记录以 type=compact_archive 头行开始，
//     后跟被删消息的原始 JSONL 行（保留 UUID/ParentUUID 链，供重建）。
//  3. 归档按 UUID 去重（幂等）：崩溃在"归档后、重写前"时重试不产生重复。
//  4. compact_archive=false（settings.json）时完全不产生归档文件。
//  5. 归档文件不是会话：List()/SearchByTag() 不得把 *.archive.jsonl
//     误列为会话；Delete/Rename/Fork 生命周期必须覆盖归档文件。
//
// 断言风格：全部断言结果（文件内容、消息数、UUID 集合），禁止只断言
// "执行了归档"（如仅检查无 error）。
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildChainedSession 构造 n 条链式消息的会话（AddMessage 自动生成 UUID 链）。
func buildChainedSession(t *testing.T, texts ...string) *Session {
	t.Helper()
	sess := NewSession()
	for i, txt := range texts {
		sess.AddMessage(Message{
			Role:      RoleUser,
			Blocks:    []ContentBlock{{Type: "text", Text: txt}},
			Timestamp: int64(1000 + i),
		})
	}
	return sess
}

// archiveTexts 读取归档文件，返回其中的全部 text 块内容（按出现顺序）。
// 兼容 compact_archive 头行（type 字段非空且非消息 role 的行被跳过）。
func archiveTexts(t *testing.T, store *Store, sessionID string) []string {
	t.Helper()
	data, err := os.ReadFile(store.archivePath(sessionID))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var texts []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		msg, _, err := decodeArchiveLine([]byte(line))
		if err != nil || msg == nil {
			continue // 头行或坏行
		}
		for _, b := range msg.Blocks {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
	}
	return texts
}

// archiveUUIDSet 返回归档中已出现的消息 UUID 集合（供去重断言）。
func archiveUUIDSet(t *testing.T, store *Store, sessionID string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(store.archivePath(sessionID))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	set := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		msg, _, err := decodeArchiveLine([]byte(line))
		if err != nil || msg == nil || msg.UUID == "" {
			continue
		}
		set[msg.UUID]++
	}
	return set
}

// doRewrite 对 live 会话执行一次压缩重写（模拟 commitCompactResult 的生产调用形态）。
func doRewrite(t *testing.T, store *Store, id string, survivors []Message, compactionCount int) {
	t.Helper()
	live, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	version, err := store.TranscriptVersion(id)
	if err != nil {
		t.Fatalf("TranscriptVersion: %v", err)
	}
	_, _, revision, _ := live.Snapshot()
	candidate := NewSession()
	candidate.Metadata.CompactionCount = compactionCount
	candidate.SetMessages(survivors)
	if err := store.Rewrite(id, version, live, revision, candidate); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
}

// TestRewriteArchivesDroppedMessages 结果断言：Rewrite 后主文件只剩存活链，
// 被删消息全部进入归档（内容、顺序、UUID 链完整），且存活消息不进归档。
func TestRewriteArchivesDroppedMessages(t *testing.T) {
	store := createTestStore(t)
	id := "arch-basic"
	sess := buildChainedSession(t, "m1", "m2", "m3", "m4", "m5")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	msgs := all.GetMessages()

	doRewrite(t, store, id, msgs[3:5], 1) // 保留 m4,m5

	// 主文件：只剩 m4,m5
	reloaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	got := reloaded.GetMessages()
	if len(got) != 2 {
		t.Fatalf("主文件应只剩 2 条消息，实际 %d", len(got))
	}
	if got[0].Blocks[0].Text != "m4" || got[1].Blocks[0].Text != "m5" {
		t.Fatalf("主文件存活链错误：%v %v", got[0].Blocks[0].Text, got[1].Blocks[0].Text)
	}

	// 归档：m1..m3 按原顺序，内容完整
	texts := archiveTexts(t, store, id)
	if len(texts) != 3 {
		t.Fatalf("归档应含 3 条被删消息，实际 %d：%v", len(texts), texts)
	}
	for i, want := range []string{"m1", "m2", "m3"} {
		if texts[i] != want {
			t.Fatalf("归档顺序/内容错误：第 %d 条应为 %q，实际 %q", i, want, texts[i])
		}
	}

	// 归档统计：1 条压缩记录、3 条消息、UUID 无重复
	stats, err := store.ArchiveStats(id)
	if err != nil {
		t.Fatalf("ArchiveStats: %v", err)
	}
	if stats.Records != 1 || stats.Messages != 3 {
		t.Fatalf("ArchiveStats 应为 Records=1 Messages=3，实际 Records=%d Messages=%d",
			stats.Records, stats.Messages)
	}
	if stats.Bytes <= 0 {
		t.Fatalf("ArchiveStats.Bytes 应 > 0，实际 %d", stats.Bytes)
	}

	// UUID 链：归档消息保留原始 UUID/ParentUUID（重建能力的前提）
	archUUIDs := archiveUUIDSet(t, store, id)
	for _, m := range msgs[:3] {
		if archUUIDs[m.UUID] != 1 {
			t.Fatalf("被删消息 %q 的 UUID 应在归档中出现恰好 1 次，实际 %d", m.Blocks[0].Text, archUUIDs[m.UUID])
		}
	}
	for _, m := range msgs[3:] {
		if _, ok := archUUIDs[m.UUID]; ok {
			t.Fatalf("存活消息 %q 不应出现在归档中", m.Blocks[0].Text)
		}
	}
	// 头行：含 compact_archive 标记与 compaction_count
	data, _ := os.ReadFile(store.archivePath(id))
	if !strings.Contains(string(data), `"type":"compact_archive"`) {
		t.Fatalf("归档应含 compact_archive 头行，实际：%.200s", string(data))
	}
	if !strings.Contains(string(data), `"compaction_count":1`) {
		t.Fatalf("归档头行应记录 compaction_count=1，实际：%.200s", string(data))
	}
}

// TestRewriteArchiveDisabled 结果断言：SetArchiveEnabled(false) 后 Rewrite
// 正常完成，但磁盘上不产生归档文件。
func TestRewriteArchiveDisabled(t *testing.T) {
	store := createTestStore(t)
	store.SetArchiveEnabled(false)
	if store.ArchiveEnabled() {
		t.Fatal("SetArchiveEnabled(false) 后 ArchiveEnabled() 应为 false")
	}
	id := "arch-off"
	sess := buildChainedSession(t, "m1", "m2", "m3")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _ := store.Load(id)
	msgs := all.GetMessages()
	doRewrite(t, store, id, msgs[2:], 1)

	if _, err := os.Stat(store.archivePath(id)); !os.IsNotExist(err) {
		t.Fatalf("禁用归档时不应产生归档文件，stat err=%v", err)
	}
	reloaded, _ := store.Load(id)
	if len(reloaded.GetMessages()) != 1 {
		t.Fatalf("主文件应只剩 1 条消息，实际 %d", len(reloaded.GetMessages()))
	}
}

// TestRewriteArchiveFailureAbortsRewrite 结果断言：归档写失败时 Rewrite
// 整体失败（主文件原样保留，5 条消息一条不少）——写入顺序 = 归档先于重写。
func TestRewriteArchiveFailureAbortsRewrite(t *testing.T) {
	store := createTestStore(t)
	id := "arch-fail"
	sess := buildChainedSession(t, "m1", "m2", "m3", "m4", "m5")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := store.TranscriptVersion(id)
	if err != nil {
		t.Fatalf("TranscriptVersion: %v", err)
	}

	// 用同名目录堵死归档文件路径 → append 打开失败
	archPath := store.archivePath(id)
	if err := os.MkdirAll(archPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	live, _ := store.Load(id)
	version, _ := store.TranscriptVersion(id)
	_, _, revision, _ := live.Snapshot()
	candidate := NewSession()
	candidate.SetMessages(live.GetMessages()[4:])
	err = store.Rewrite(id, version, live, revision, candidate)
	if err == nil {
		t.Fatal("归档写失败时 Rewrite 应返回错误（先归档后重写的安全顺序被破坏）")
	}

	// 主文件原样保留：版本未变、消息数不变
	after, err := store.TranscriptVersion(id)
	if err != nil {
		t.Fatalf("re-TranscriptVersion: %v", err)
	}
	if after != before {
		t.Fatal("归档失败后主文件被改动（应为归档先于重写，失败即中止）")
	}
	reloaded, _ := store.Load(id)
	if len(reloaded.GetMessages()) != 5 {
		t.Fatalf("归档失败后主文件应保留全部 5 条消息，实际 %d", len(reloaded.GetMessages()))
	}
}

// TestArchiveIdempotentAfterCrash 结果断言：模拟"归档成功、主文件未重写"
// 的崩溃窗口——再次归档同一批被删消息不产生重复（UUID 幂等）。
func TestArchiveIdempotentAfterCrash(t *testing.T) {
	store := createTestStore(t)
	id := "arch-idem"
	sess := buildChainedSession(t, "m1", "m2", "m3", "m4")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _ := store.Load(id)
	msgs := all.GetMessages()
	survivors := msgs[2:]

	// 第一次归档（主文件尚未重写 —— 崩溃窗口）
	if err := store.archiveDroppedLocked(id, survivors, 1); err != nil {
		t.Fatalf("第一次 archiveDroppedLocked: %v", err)
	}
	// 崩溃后重试：同样的 survivors、主文件还是旧的
	if err := store.archiveDroppedLocked(id, survivors, 1); err != nil {
		t.Fatalf("第二次 archiveDroppedLocked: %v", err)
	}

	// m1,m2 恰好各出现一次；压缩记录也只应有 1 条（fresh 为空不追加头行）
	uuids := archiveUUIDSet(t, store, id)
	for _, m := range msgs[:2] {
		if uuids[m.UUID] != 1 {
			t.Fatalf("被删消息 %q 应在归档中恰好 1 次，实际 %d（崩溃重试产生重复）",
				m.Blocks[0].Text, uuids[m.UUID])
		}
	}
	stats, _ := store.ArchiveStats(id)
	if stats.Records != 1 {
		t.Fatalf("幂等重试不应追加压缩记录，实际 Records=%d", stats.Records)
	}
}

// TestMultiCompactionArchiveIncludesPriorBoundary 结果断言：第二次压缩时，
// 第一份摘要（compact_boundary）降级为被删消息进入归档——"每条被持久化
// 过的消息恰好存在于一个地方"（主文件 ∪ 归档 = 完整历史）。
func TestMultiCompactionArchiveIncludesPriorBoundary(t *testing.T) {
	store := createTestStore(t)
	id := "arch-multi"
	sess := buildChainedSession(t, "m1", "m2", "m3", "m4", "m5")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _ := store.Load(id)
	msgs := all.GetMessages()

	// 第一次压缩：[S1, m4, m5]，S1 的 parent 指向压缩前链尾 m3（生产语义）
	s1 := Message{
		Role:       RoleSystem,
		Subtype:    "compact_boundary",
		Blocks:     []ContentBlock{{Type: "text", Text: "summary-1"}},
		Timestamp:  9000,
		UUID:       generateUUID(),
		ParentUUID: msgs[2].UUID,
	}
	doRewrite(t, store, id, append([]Message{s1}, msgs[3:]...), 1)

	// 第二次压缩：[S2, m5]，S2 的 parent 指向 m4
	m4, m5 := msgs[3], msgs[4]
	s2 := Message{
		Role:       RoleSystem,
		Subtype:    "compact_boundary",
		Blocks:     []ContentBlock{{Type: "text", Text: "summary-2"}},
		Timestamp:  9500,
		UUID:       generateUUID(),
		ParentUUID: m4.UUID,
	}
	doRewrite(t, store, id, []Message{s2, m5}, 2)

	// 主文件：[S2, m5]
	reloaded, _ := store.Load(id)
	got := reloaded.GetMessages()
	if len(got) != 2 || got[0].Blocks[0].Text != "summary-2" || got[1].Blocks[0].Text != "m5" {
		t.Fatalf("二次压缩后主文件应为 [S2, m5]，实际 %d 条", len(got))
	}

	// 归档：m1,m2,m3,S1,m4 全部在内且各一次；S2/m5 不在
	texts := archiveTexts(t, store, id)
	wantSet := map[string]int{"m1": 1, "m2": 1, "m3": 1, "summary-1": 1, "m4": 1}
	for _, txt := range texts {
		wantSet[txt]--
	}
	for txt, n := range wantSet {
		if n != 0 {
			t.Fatalf("归档中 %q 出现次数错误（应 1 次，差 %d）；归档全集=%v", txt, n, texts)
		}
	}
	stats, _ := store.ArchiveStats(id)
	if stats.Records != 2 || stats.Messages != 5 {
		t.Fatalf("二次压缩后归档应为 Records=2 Messages=5，实际 Records=%d Messages=%d",
			stats.Records, stats.Messages)
	}

	// S1 进归档时保留 ParentUUID=m3.UUID（跨记录链重建能力）
	uuids := archiveUUIDSet(t, store, id)
	if _, ok := uuids[s1.UUID]; !ok {
		t.Fatal("第一份摘要 S1 应在归档中（摘要被新摘要取代时降级为被删消息）")
	}
	data, _ := os.ReadFile(store.archivePath(id))
	if !strings.Contains(string(data), `"parent_uuid":"`+msgs[2].UUID+`"`) {
		t.Fatal("归档中的 S1 应保留指向 m3 的 ParentUUID（重建依赖跨记录链）")
	}
}

// TestCompactArchivesTombstoned 结果断言：Store.Compact（tombstone 清理入口）
// 同样先归档后重写——被 tombstone 的消息进归档。
func TestCompactArchivesTombstoned(t *testing.T) {
	store := createTestStore(t)
	id := "arch-compact"
	sess := buildChainedSession(t, "m1", "m2", "m3", "m4")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _ := store.Load(id)
	msgs := all.GetMessages()
	msgs[1].Tombstoned = true // tombstone m2
	all.SetMessages(msgs)
	if err := store.Save(id, all); err != nil {
		t.Fatalf("Save tombstones: %v", err)
	}

	removed, err := store.Compact(id)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if removed != 1 {
		t.Fatalf("Compact 应移除 1 条（tombstoned m2），实际 %d", removed)
	}
	texts := archiveTexts(t, store, id)
	if len(texts) != 1 || texts[0] != "m2" {
		t.Fatalf("Compact 后归档应只含被 tombstone 的 m2，实际 %v", texts)
	}
	reloaded, _ := store.Load(id)
	if len(reloaded.GetMessages()) != 3 {
		t.Fatalf("Compact 后主文件应剩 3 条，实际 %d", len(reloaded.GetMessages()))
	}
}

// TestListExcludesArchiveFiles 结果断言：归档文件不被 List() 误列为会话。
func TestListExcludesArchiveFiles(t *testing.T) {
	store := createTestStore(t)
	id := "arch-list"
	sess := buildChainedSession(t, "m1", "m2", "m3")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _ := store.Load(id)
	doRewrite(t, store, id, all.GetMessages()[2:], 1)

	if _, err := os.Stat(store.archivePath(id)); err != nil {
		t.Fatalf("前置条件：归档应存在，err=%v", err)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("List 应只返回 1 个会话，实际 %d：%+v", len(summaries), summaries)
	}
	if summaries[0].ID != id {
		t.Fatalf("唯一会话应为 %q，实际 %q", id, summaries[0].ID)
	}
}

// TestArchiveLifecycleRenameDeleteFork 结果断言：Rename 带走归档、Delete
// 连带清理归档、Fork 继承归档（fork = checkpoint 语义）、Purge 幂等。
func TestArchiveLifecycleRenameDeleteFork(t *testing.T) {
	store := createTestStore(t)
	id := "arch-life"
	sess := buildChainedSession(t, "m1", "m2", "m3", "m4")
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _ := store.Load(id)
	msgs := all.GetMessages()
	doRewrite(t, store, id, msgs[2:], 1)
	origStats, _ := store.ArchiveStats(id)

	// Rename：归档跟随
	id2 := "arch-life-renamed"
	if err := store.Rename(id, id2); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(store.archivePath(id2)); err != nil {
		t.Fatalf("Rename 后归档应跟随到新 ID，err=%v", err)
	}
	if _, err := os.Stat(store.archivePath(id)); !os.IsNotExist(err) {
		t.Fatalf("Rename 后旧归档应不存在，err=%v", err)
	}

	// Fork：归档继承（副本独立演化，历史谱系完整）
	forkedID, err := store.ForkSession(id2)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkStats, err := store.ArchiveStats(forkedID)
	if err != nil {
		t.Fatalf("fork ArchiveStats: %v", err)
	}
	if forkStats.Messages != origStats.Messages {
		t.Fatalf("fork 应继承归档（%d 条），实际 %d 条", origStats.Messages, forkStats.Messages)
	}

	// Purge：删除归档，幂等
	removed, freed, err := store.PurgeArchive(id2)
	if err != nil || !removed || freed <= 0 {
		t.Fatalf("PurgeArchive 应成功（removed=%v freed=%d err=%v）", removed, freed, err)
	}
	if _, err := os.Stat(store.archivePath(id2)); !os.IsNotExist(err) {
		t.Fatal("Purge 后归档文件应不存在")
	}
	removed2, _, err := store.PurgeArchive(id2)
	if err != nil || removed2 {
		t.Fatalf("二次 Purge 应幂等（removed=%v err=%v）", removed2, err)
	}

	// Delete：连带清理（用 fork 的归档验证）
	if err := store.Delete(forkedID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(store.archivePath(forkedID)); !os.IsNotExist(err) {
		t.Fatal("Delete 会话后其归档应被连带清理（孤儿文件）")
	}
}

// TestArchivePathLayout 单元断言：归档路径布局（sidecar，同目录）。
func TestArchivePathLayout(t *testing.T) {
	store := createTestStore(t)
	want := filepath.Join(store.Dir(), "s1.archive.jsonl")
	if got := store.archivePath("s1"); got != want {
		t.Fatalf("archivePath = %q, want %q", got, want)
	}
}

// ---- ReadArchive（只读查看/导出配套，2026-09-07）----

// writeTwoRecordArchive 写入两段归档记录的测试归档文件（ReadArchive 用例前置）。
func writeTwoRecordArchive(t *testing.T, dir, name string) {
	t.Helper()
	content := `{"type":"compact_archive","ts":1757240000,"compaction_count":1,"dropped":2}` + "\n" +
		`{"role":"user","uuid":"ra-u1","parent_uuid":"","blocks":[{"type":"text","text":"first-archived-question"}]}` + "\n" +
		`{"role":"assistant","uuid":"ra-a1","parent_uuid":"ra-u1","blocks":[{"type":"text","text":"first-archived-answer"}]}` + "\n" +
		`{"type":"compact_archive","ts":1757243600,"compaction_count":2,"dropped":1}` + "\n" +
		`{"role":"user","uuid":"rb-u1","parent_uuid":"","blocks":[{"type":"text","text":"second-record-message"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".archive.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
}

// TestReadArchiveParsesRecordsAndOrder 结果断言：ReadArchive 按头行切分记录，
// 头行字段正确、消息保序、UUID/ParentUUID 链保留——还原查看的数据基础。
func TestReadArchiveParsesRecordsAndOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeTwoRecordArchive(t, dir, "read-arch")

	recs, err := store.ReadArchive("read-arch")
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("记录数应 2，实际 %d", len(recs))
	}
	if recs[0].Header.CompactionCount != 1 || recs[0].Header.Dropped != 2 || recs[0].Header.Timestamp != 1757240000 {
		t.Fatalf("第 1 段头行字段错: %+v", recs[0].Header)
	}
	if recs[1].Header.CompactionCount != 2 || recs[1].Header.Dropped != 1 {
		t.Fatalf("第 2 段头行字段错: %+v", recs[1].Header)
	}
	if len(recs[0].Messages) != 2 || len(recs[1].Messages) != 1 {
		t.Fatalf("消息数应 2/1，实际 %d/%d", len(recs[0].Messages), len(recs[1].Messages))
	}
	if recs[0].Messages[0].TextContent() != "first-archived-question" || recs[0].Messages[0].UUID != "ra-u1" {
		t.Fatalf("第 1 段消息[0] 内容/UUID 错: %q/%q", recs[0].Messages[0].TextContent(), recs[0].Messages[0].UUID)
	}
	if recs[0].Messages[1].ParentUUID != "ra-u1" {
		t.Fatalf("ParentUUID 链应保留（ra-a1 的 parent=ra-u1），实际 %q", recs[0].Messages[1].ParentUUID)
	}
	if recs[1].Messages[0].TextContent() != "second-record-message" {
		t.Fatalf("第 2 段消息内容错: %q", recs[1].Messages[0].TextContent())
	}
}

// TestReadArchiveMissingFileReturnsEmpty 结果断言：未压缩过的会话（无归档）
// 返回空切片不报错——/archive 首次使用不应看到错误。
func TestReadArchiveMissingFileReturnsEmpty(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recs, err := store.ReadArchive("never-compacted")
	if err != nil {
		t.Fatalf("无归档不应报错: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("无归档应返回空切片，实际 %d 条", len(recs))
	}
}

// TestReadArchiveBadLineSkipsTolerantly 结果断言：归档尾部截断坏行被跳过，
// 前面完整记录仍可读（半行崩溃只可能发生在归档尾部）。
func TestReadArchiveBadLineSkipsTolerantly(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := `{"type":"compact_archive","ts":1,"compaction_count":1,"dropped":1}` + "\n" +
		`{"role":"user","uuid":"ok-u1","parent_uuid":"","blocks":[{"type":"text","text":"ok-message"}]}` + "\n" +
		`{"truncated-bad-line`
	if err := os.WriteFile(filepath.Join(dir, "bad-tail.archive.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := store.ReadArchive("bad-tail")
	if err != nil {
		t.Fatalf("坏行应容错跳过而非报错: %v", err)
	}
	if len(recs) != 1 || len(recs[0].Messages) != 1 || recs[0].Messages[0].UUID != "ok-u1" {
		t.Fatalf("完整记录应可读（1 记录 1 消息），实际 %+v", recs)
	}
}
