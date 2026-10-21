package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
)

// m2Store 为 plain runner 测试创建独立 store（避免污染共享状态）。
func m2Store(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// TestNewModelWiresArchiveEnabled 接线断言（分层验证链第 2 层：被接受/已接线）：
// NewModel（TUI 生产入口）必须把 cfg.CompactArchive 传给 session.Store——
// 默认 true；显式 false 关闭。若接线缺失，settings.json 的开关将静默失效。
func TestNewModelWiresArchiveEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	m := NewModel(&cfg, newTestClient("test-model"), newTestRegistry())
	m.reassignServices()
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：NewModel 应创建 session store")
	}
	if !store.ArchiveEnabled() {
		t.Fatal("默认配置下 NewModel 的 store 应启用归档（compact_archive 默认 true）")
	}

	cfgOff := config.DefaultConfig()
	cfgOff.Model = "test-model"
	cfgOff.CompactArchive = false
	m2 := NewModel(&cfgOff, newTestClient("test-model"), newTestRegistry())
	m2.reassignServices()
	store2 := m2.sessionService.Store()
	if store2 == nil {
		t.Fatal("前置条件：NewModel 应创建 session store")
	}
	if store2.ArchiveEnabled() {
		t.Fatal("cfg.CompactArchive=false 时 NewModel 的 store 应关闭归档（接线缺失 = 开关静默失效）")
	}
}

// TestNewPlainRunnerWiresArchiveEnabled 接线断言（plain 路径）：
// NewPlainRunner 生产构造函数必须传播 compact_archive 设置。
func TestNewPlainRunnerWiresArchiveEnabled(t *testing.T) {
	cfgOff := config.DefaultConfig()
	cfgOff.Model = "test-model"
	cfgOff.CompactArchive = false
	r := NewPlainRunner(&cfgOff, newTestClient("test-model"), newTestRegistry())
	if r.Store == nil {
		t.Fatal("前置条件：NewPlainRunner 应创建 session store")
	}
	if r.Store.ArchiveEnabled() {
		t.Fatal("plain 路径：cfg.CompactArchive=false 应关闭归档")
	}
}

// TestHandleCompactPurgeRouting 结果断言：/compact purge 经真实命令入口
// （Model.handleCompact）路由到归档删除——文件真被删、输出含结果、幂等。
func TestHandleCompactPurgeRouting(t *testing.T) {
	m := newGuardTestModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：store 未初始化")
	}
	name := "purge-routing-test"
	m.sessionService.SetSessionName(name)

	// 手工放置归档文件（模拟已发生过压缩）
	archPath := filepath.Join(store.Dir(), name+".archive.jsonl")
	content := `{"type":"compact_archive","ts":1,"compaction_count":1,"dropped":1}` + "\n" +
		`{"role":"user","uuid":"u1","blocks":[{"type":"text","text":"secret-before-compact"}]}` + "\n"
	if err := os.WriteFile(archPath, []byte(content), 0o600); err != nil {
		t.Fatalf("放置归档文件: %v", err)
	}

	out := m.handleCompact("purge")
	// 对抗式审查 2.2 P2 修复：断言成功专属文案（失败文案也含"归档"，
	// 弱断言在失败时会假通过）。
	if !strings.Contains(out, "已删除") {
		t.Fatalf("/compact purge 输出应说明已删除及释放字节数，实际：%s", out)
	}
	if _, err := os.Stat(archPath); !os.IsNotExist(err) {
		t.Fatalf("purge 后归档文件应被删除，stat err=%v", err)
	}

	// 幂等：再次 purge 不报错
	out2 := m.handleCompact("purge")
	if strings.Contains(out2, "failed") || strings.Contains(out2, "错误") {
		t.Fatalf("二次 purge 应幂等不报错，实际：%s", out2)
	}
}

// TestPlainRunnerHandleCompactPurgeRouting 结果断言（plain 路径）：
// runnerCore.handleCompact("purge") 同样路由到归档删除。
func TestPlainRunnerHandleCompactPurgeRouting(t *testing.T) {
	r := newTestPlainRunner()
	store := m2Store(t)
	r.Store = store
	r.SessionName = "plain-purge-test"

	archPath := filepath.Join(store.Dir(), "plain-purge-test.archive.jsonl")
	if err := os.WriteFile(archPath, []byte(`{"type":"compact_archive","ts":1,"compaction_count":1,"dropped":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("放置归档文件: %v", err)
	}

	out := r.handleCompact("purge")
	if !strings.Contains(out, "已删除") {
		t.Fatalf("plain /compact purge 输出应说明已删除及释放字节数，实际：%s", out)
	}
	if _, err := os.Stat(archPath); !os.IsNotExist(err) {
		t.Fatalf("purge 后归档文件应被删除，stat err=%v", err)
	}
	// 幂等：再次 purge 应返回"无归档文件"而非报错
	out2 := r.handleCompact("purge")
	if !strings.Contains(out2, "无归档文件") {
		t.Fatalf("plain 二次 purge 应幂等返回无归档文件，实际：%s", out2)
	}
}

// archiveUUIDsOnDisk 解析归档文件：返回（头行 JSON、已归档消息 UUID 集合、
// 消息行数）。E2E 断言辅助：头行必须是第一条且 type=compact_archive。
func archiveUUIDsOnDisk(t *testing.T, path string) (map[string]bool, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("归档文件应存在且可读（%s）: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("归档应含头行+至少 1 条消息行，实际 %d 行", len(lines))
	}
	if !strings.Contains(lines[0], `"type":"compact_archive"`) {
		t.Fatalf("归档首行应为 compact_archive 头行，实际：%s", lines[0])
	}
	uuids := make(map[string]bool)
	for _, line := range lines[1:] {
		if strings.Contains(line, `"uuid":"`) {
			start := strings.Index(line, `"uuid":"`) + len(`"uuid":"`)
			end := strings.Index(line[start:], `"`)
			if end > 0 {
				uuids[line[start:start+end]] = true
			}
		}
	}
	return uuids, len(lines) - 1
}

// TestHandleCompactEndToEndArchivesDroppedToDisk E2E 结果断言（对抗式审查
// 2.3 P1 修复）：从 plain 生产入口 runnerCore.handleCompact 进，经
// commitCompactResult → Store.Rewrite → archiveDroppedLocked，断言
// 归档文件真实落盘且恰好包含被删消息（before − after 集合差），
// 头行 CompactionCount 与主文件元数据一致——非 mock 写入点。
func TestHandleCompactEndToEndArchivesDroppedToDisk(t *testing.T) {
	core := persistedCompactRunner(t)
	before := make(map[string]bool)
	for _, m := range core.Session.GetMessages() {
		before[m.UUID] = true
	}

	got := core.handleCompact("")
	if !strings.Contains(got, "12 -> 11") {
		t.Fatalf("前置条件：压缩应成功（12 -> 11），实际：%s", got)
	}

	archPath := core.Store.ArchivePath(core.SessionName)
	archivedUUIDs, msgLines := archiveUUIDsOnDisk(t, archPath)

	// 主文件存活集合（重载自磁盘，非内存）
	loaded, err := core.Store.Load(core.SessionName)
	if err != nil {
		t.Fatalf("重载会话: %v", err)
	}
	after := make(map[string]bool)
	for _, m := range loaded.GetMessages() {
		after[m.UUID] = true
	}

	// 集合差断言：归档 == before − after（既不缺失也不重复）
	wantDropped := 0
	for uuid := range before {
		if !after[uuid] {
			wantDropped++
			if !archivedUUIDs[uuid] {
				t.Fatalf("被删消息 %s 未落入归档（安全网失效）", uuid)
			}
		}
	}
	for uuid := range archivedUUIDs {
		if after[uuid] {
			t.Fatalf("存活消息 %s 不应出现在归档中（重复归档）", uuid)
		}
	}
	if wantDropped == 0 {
		t.Fatal("前置条件：本次压缩应至少删除 1 条消息")
	}
	if msgLines != wantDropped {
		t.Fatalf("归档消息行数 %d 应等于被删消息数 %d", msgLines, wantDropped)
	}
	if len(archivedUUIDs) != wantDropped {
		t.Fatalf("归档 UUID 数 %d 应等于被删消息数 %d（无重复）", len(archivedUUIDs), wantDropped)
	}
}

// TestTUIHandleCompactEndToEndArchivesDroppedToDisk E2E 结果断言（TUI 路径）：
// 真实 Model + 真实 store 目录，m.handleCompact("") 后归档文件含被删消息。
func TestTUIHandleCompactEndToEndArchivesDroppedToDisk(t *testing.T) {
	m := newCompactTestModel(t)
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	m.SessionModel.store = store
	name := "e2e-archive-tui"
	m.sessionService.SetSessionName(name)
	m.session.SetMessages(append(m.session.GetMessages(), markerMessagesForTUI(6)...))
	if err := store.Save(name, m.session); err != nil {
		t.Fatalf("保存初始会话: %v", err)
	}
	before := make(map[string]bool)
	for _, msg := range m.session.GetMessages() {
		before[msg.UUID] = true
	}

	result := m.handleCompact("")
	if !strings.Contains(result, "12 -> 11") {
		t.Fatalf("前置条件：TUI 压缩应成功（12 -> 11），实际：%s", result)
	}

	archivedUUIDs, msgLines := archiveUUIDsOnDisk(t, store.ArchivePath(name))
	loaded, err := store.Load(name)
	if err != nil {
		t.Fatalf("重载会话: %v", err)
	}
	after := make(map[string]bool)
	for _, msg := range loaded.GetMessages() {
		after[msg.UUID] = true
	}
	wantDropped := 0
	for uuid := range before {
		if !after[uuid] {
			wantDropped++
			if !archivedUUIDs[uuid] {
				t.Fatalf("被删消息 %s 未落入归档（TUI 路径安全网失效）", uuid)
			}
		}
	}
	if wantDropped == 0 {
		t.Fatal("前置条件：本次压缩应至少删除 1 条消息")
	}
	if msgLines != wantDropped || len(archivedUUIDs) != wantDropped {
		t.Fatalf("归档行/UUID 数 %d/%d 应等于被删消息数 %d", msgLines, len(archivedUUIDs), wantDropped)
	}
}

// TestRewindReplaceMessagesPreservesArchiveSidecar 结果断言（对抗式审查
// 1.1 P1 修复）：rewind 的 backup-then-rename 原子替换不得销毁压缩归档——
// ReplaceMessages 完成后 <sessionId>.archive.jsonl 仍存在且内容不变，
// backupID 名下无残留。
func TestRewindReplaceMessagesPreservesArchiveSidecar(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	adapter := &sessionRestorerAdapter{store: store}
	sid := "rewind-archive-keep"

	sess := session.NewSession()
	sess.SetMessages(markerMessagesForTUI(3))
	if err := store.Save(sid, sess); err != nil {
		t.Fatalf("保存会话: %v", err)
	}
	archiveContent := `{"type":"compact_archive","ts":1,"compaction_count":2,"dropped":4}` + "\n" +
		`{"role":"user","uuid":"archived-u1","blocks":[{"type":"text","text":"secret-archived"}]}` + "\n"
	if err := os.WriteFile(store.ArchivePath(sid), []byte(archiveContent), 0o600); err != nil {
		t.Fatalf("放置归档: %v", err)
	}

	// rewind 到只剩前 2 条消息
	var msgs [][]byte
	for _, m := range sess.GetMessages()[:2] {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		msgs = append(msgs, data)
	}
	if err := adapter.ReplaceMessages(sid, msgs); err != nil {
		t.Fatalf("ReplaceMessages: %v", err)
	}

	got, err := os.ReadFile(store.ArchivePath(sid))
	if err != nil {
		t.Fatalf("rewind 后归档应保留: %v", err)
	}
	if string(got) != archiveContent {
		t.Fatalf("rewind 后归档内容不应改变:\n got: %s\nwant: %s", got, archiveContent)
	}
	// backupID 名下无归档残留（若归档被卷进备份链又未被还原，这里能抓到）
	if _, err := os.Stat(store.ArchivePath(sid + ".backup")); !os.IsNotExist(err) {
		t.Fatalf("backupID 名下不应残留归档文件: %v", err)
	}
}

// writeArchiveFixture 写入两段归档记录的测试归档（/archive 查看导出用例前置）。
func writeArchiveFixture(t *testing.T, store *session.Store, name string) {
	t.Helper()
	content := `{"type":"compact_archive","ts":1757240000,"compaction_count":1,"dropped":2}` + "\n" +
		`{"role":"user","uuid":"ra-u1","parent_uuid":"","blocks":[{"type":"text","text":"first-archived-question"}]}` + "\n" +
		`{"role":"assistant","uuid":"ra-a1","parent_uuid":"ra-u1","blocks":[{"type":"text","text":"first-archived-answer"}]}` + "\n" +
		`{"type":"compact_archive","ts":1757243600,"compaction_count":2,"dropped":1}` + "\n" +
		`{"role":"user","uuid":"rb-u1","parent_uuid":"","blocks":[{"type":"text","text":"second-record-message"}]}` + "\n"
	if err := os.WriteFile(store.ArchivePath(name), []byte(content), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
}

// TestHandleArchiveViewsAndExports 结果断言（TUI 路径）：
// /archive 列记录、/archive view N 显示原文、/archive export 落盘 markdown。
func TestHandleArchiveViewsAndExports(t *testing.T) {
	m := newGuardTestModel(t)
	store := m2Store(t)
	m.SessionModel.store = store
	name := "arch-view-tui"
	m.sessionService.SetSessionName(name)
	writeArchiveFixture(t, store, name)

	// 列表：记录数 + 每段摘要
	list := m.handleArchive("")
	if !strings.Contains(list, "2") || !strings.Contains(list, "压缩") {
		t.Fatalf("/archive 应列出 2 段压缩记录，实际：\n%s", list)
	}

	// 查看：第 1 段含两消息原文
	view := m.handleArchive("view 1")
	for _, want := range []string{"first-archived-question", "first-archived-answer"} {
		if !strings.Contains(view, want) {
			t.Fatalf("/archive view 1 应含 %q，实际：\n%s", want, view)
		}
	}

	// 越界：明确报错而非 panic/空输出
	bad := m.handleArchive("view 3")
	if !strings.Contains(bad, "1-2") || !strings.Contains(bad, "无效") {
		t.Fatalf("/archive view 3 应提示记录号无效（范围 1-2），实际：\n%s", bad)
	}

	// 导出：markdown 文件真实落盘且含原文
	out := t.TempDir() + "/arch-export.md"
	exp := m.handleArchive("export " + out)
	if !strings.Contains(exp, "已导出") {
		t.Fatalf("/archive export 应说明导出结果，实际：\n%s", exp)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("导出文件应存在: %v", err)
	}
	for _, want := range []string{"first-archived-question", "second-record-message", "压缩记录"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("导出内容应含 %q（原文 + 记录分节）", want)
		}
	}
}

// TestHandleArchiveNoSessionAndEmpty 结果断言：
// 未保存会话 / 无归档文件时不报错，给出可理解提示。
func TestHandleArchiveNoSessionAndEmpty(t *testing.T) {
	m := newGuardTestModel(t)
	store := m2Store(t)
	m.SessionModel.store = store
	m.sessionService.SetSessionName("")

	if out := m.handleArchive(""); !strings.Contains(out, "尚未保存") {
		t.Fatalf("未保存会话应提示无归档，实际：%s", out)
	}

	m.sessionService.SetSessionName("no-archive-yet")
	if out := m.handleArchive("view 1"); !strings.Contains(out, "无归档") {
		t.Fatalf("无归档文件应提示未压缩，实际：%s", out)
	}
}

// TestPlainRunnerHandleArchiveViewExport 结果断言（plain 路径）：
// runnerCore.handleArchive 与 TUI 行为一致。
func TestPlainRunnerHandleArchiveViewExport(t *testing.T) {
	r := newTestPlainRunner()
	store := m2Store(t)
	r.Store = store
	r.SessionName = "arch-view-plain"
	writeArchiveFixture(t, store, "arch-view-plain")

	if view := r.handleArchive("view 2"); !strings.Contains(view, "second-record-message") {
		t.Fatalf("plain /archive view 2 应含原文，实际：\n%s", view)
	}
	out := t.TempDir() + "/plain-export.md"
	if exp := r.handleArchive("export " + out); !strings.Contains(exp, "已导出") {
		t.Fatalf("plain /archive export 应说明结果，实际：\n%s", exp)
	}
	if data, err := os.ReadFile(out); err != nil || !strings.Contains(string(data), "second-record-message") {
		t.Fatalf("plain 导出文件应含原文: err=%v", err)
	}
}

// ---- 对抗式审查 P1 测试缺口补齐（dispatch 路由×3 / 脱敏 / 截断 / 默认路径 / 无效参数）----

// TestHandleArchiveDispatchRoutingThreePaths 结果断言（审查缺口：dispatch 路由×3）。
// 旧测试直接调 handleArchive，绕过了三条生产调度层——命令注册表缺 "archive"
// 或 switch 缺 case 时旧测试仍绿（假验证）。本测试从各模式的真实调度入口进：
// - TUI: handleCommand（command.Parse → Registry.Get → switch case）
// - plain: dispatchCommand（initDispatchHandlers 表，与 NewPlainRunner 同源）
// - bridge: dispatchBridgeCommand（switch——compact 产生归档的路径必须有 /archive 出口）
func TestHandleArchiveDispatchRoutingThreePaths(t *testing.T) {
	store := m2Store(t)
	name := "arch-dispatch"
	writeArchiveFixture(t, store, name)

	// TUI 调度链
	m := newGuardTestModel(t)
	m.SessionModel.store = store
	m.sessionService.SetSessionName(name)
	if out := m.handleCommand("/archive view 1"); !strings.Contains(out, "first-archived-question") {
		t.Fatalf("TUI handleCommand(/archive view 1) 应路由到真实实现，实际：\n%s", out)
	}

	// plain 调度链（dispatchHandlers 用生产 initDispatchHandlers 构造，非测试专用表）
	r := newTestPlainRunner()
	r.Store = store
	r.SessionName = name
	r.dispatchHandlers = r.initDispatchHandlers()
	if out := r.dispatchCommand("archive", "view 1", ""); !strings.Contains(out, "first-archived-question") {
		t.Fatalf("plain dispatchCommand(archive) 应路由到真实实现，实际：\n%s", out)
	}

	// bridge 调度链
	br, _ := newTestBridgeRunner()
	br.Store = store
	br.SessionName = name
	if out := br.dispatchBridgeCommand("archive", "view 1", ""); !strings.Contains(out, "first-archived-question") {
		t.Fatalf("bridge dispatchBridgeCommand(archive) 应路由到真实实现，实际：\n%s", out)
	}
}

// TestHandleArchiveExportRedactsSecrets 结果断言（审查缺口：导出脱敏）：
// 归档原文可能含压缩前用户贴过的密钥；导出的 markdown 文件必须脱敏——
// [REDACTED] 落盘、原始密钥不落盘（view 是本地只读所以保留原文，
// export 产物可能被分享，是脱敏的强制边界）。
func TestHandleArchiveExportRedactsSecrets(t *testing.T) {
	store := m2Store(t)
	name := "arch-redact"
	const rawKey = "sk-abc123def456ghi789jkl012"
	content := `{"type":"compact_archive","ts":1757240000,"compaction_count":1,"dropped":1}` + "\n" +
		`{"role":"user","uuid":"rr-u1","parent_uuid":"","blocks":[{"type":"text","text":"my key is sk-abc123def456ghi789jkl012 please help"}]}` + "\n"
	if err := os.WriteFile(store.ArchivePath(name), []byte(content), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	m := newGuardTestModel(t)
	m.SessionModel.store = store
	m.sessionService.SetSessionName(name)

	out := t.TempDir() + "/redact-export.md"
	exp := m.handleArchive("export " + out)
	if !strings.Contains(exp, "已导出") || !strings.Contains(exp, "脱敏 1 处") {
		t.Fatalf("导出输出应报告脱敏 1 处，实际：%s", exp)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("导出文件应存在: %v", err)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatal("导出文件应含 [REDACTED]（脱敏生效）")
	}
	if strings.Contains(string(data), rawKey) {
		t.Fatal("导出文件不得含原始密钥（脱敏失效，密钥落盘）")
	}
}

// TestHandleArchiveViewTruncatesLongMessages 结果断言（审查缺口：截断）：
// 超过 archiveViewTruncate（500 rune）的消息在 view 中截断并标注提示；
// 尾部内容不进 view（防单条巨消息刷屏），全文走 export。
func TestHandleArchiveViewTruncatesLongMessages(t *testing.T) {
	store := m2Store(t)
	name := "arch-truncate"
	head := strings.Repeat("前", archiveViewTruncate) // 恰好 500 rune
	tail := "TAIL-MUST-NOT-APPEAR-IN-VIEW"
	content := `{"type":"compact_archive","ts":1757240000,"compaction_count":1,"dropped":1}` + "\n" +
		`{"role":"user","uuid":"tr-u1","parent_uuid":"","blocks":[{"type":"text","text":"` + head + tail + `"}]}` + "\n"
	if err := os.WriteFile(store.ArchivePath(name), []byte(content), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	m := newGuardTestModel(t)
	m.SessionModel.store = store
	m.sessionService.SetSessionName(name)

	view := m.handleArchive("view 1")
	if !strings.Contains(view, head) {
		t.Fatal("view 应保留前 500 rune 原文")
	}
	// ★ 批 3 随文案更新（意图不变）：view 仍必须标注截断提示。
	// 截断标记挂在「含用户数据原文的单行」行尾 ⇒ 容不下缩进中文辅行，
	// 与 priorityLabel/表头同理走英文单侧（判据：b3 守卫的同款设计）。
	if !strings.Contains(view, "(truncated") {
		t.Fatalf("view 应标注截断提示，实际：\n%s", view)
	}
	if strings.Contains(view, tail) {
		t.Fatal("view 不得显示第 500 rune 之后的内容（截断失效）")
	}

	// 对照：export 输出全文（尾部内容在导出文件中）
	out := t.TempDir() + "/trunc-export.md"
	if exp := m.handleArchive("export " + out); !strings.Contains(exp, "已导出") {
		t.Fatalf("export 应成功，实际：%s", exp)
	}
	data, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(data), tail) {
		t.Fatalf("导出文件应含未截断全文（尾部内容）: err=%v", err)
	}
}

// TestArchiveSafeFilename 单元结果断言（默认路径消毒的纯函数层）：
// 路径敌对字符全部替换、首尾点号剔除（防 .. 与隐藏文件）、
// 全敌对/空输入回退 "session"、超长截断到 80 rune。
func TestArchiveSafeFilename(t *testing.T) {
	hostile := `../ev/il\:name*?"<>|`
	got := archiveSafeFilename(hostile)
	if strings.ContainsAny(got, `/\:*?"<>|`) {
		t.Fatalf("消毒后不得残留路径敌对字符: %q", got)
	}
	if strings.HasPrefix(got, ".") || strings.HasSuffix(got, ".") {
		t.Fatalf("消毒后不得以点号开头/结尾（防 .. 与隐藏文件）: %q", got)
	}
	if got == "" {
		t.Fatal("消毒结果不得为空")
	}
	if got := archiveSafeFilename(".."); got != "session" {
		t.Fatalf("全点号输入应回退 session，实际 %q", got)
	}
	if got := archiveSafeFilename("  /\\:*  "); got == "" || strings.ContainsAny(got, `/\\:*?"<>|`) {
		t.Fatalf("全敌对字符输入应清洗为非空安全名，实际 %q", got)
	}
	long := strings.Repeat("长", 100)
	if got := archiveSafeFilename(long); len([]rune(got)) != 80 {
		t.Fatalf("超长会话名应截断到 80 rune，实际 %d", len([]rune(got)))
	}
	if !strings.Contains(archiveSafeFilename("中文会话名"), "中文会话名") {
		t.Fatal("CJK 会话名应原样保留")
	}
}

// TestHandleArchiveExportDefaultPathSanitized 结果断言（审查缺口：默认路径）：
// 不带路径的 /archive export 必须落 store 父目录下 exports/，且文件名经消毒——
// 敌对会话名（含 : * ? 等敌对字符，Windows 非法/Unix 竖线类）不得原样落盘，
// exports 目录外不产生任何文件。
func TestHandleArchiveExportDefaultPathSanitized(t *testing.T) {
	store := m2Store(t)
	hostile := "evil:name*?"
	writeArchiveFixture(t, store, hostile)
	m := newGuardTestModel(t)
	m.SessionModel.store = store
	m.sessionService.SetSessionName(hostile)

	exp := m.handleArchive("export")
	if !strings.Contains(exp, "已导出") {
		t.Fatalf("敌对会话名的默认路径导出应成功，实际：%s", exp)
	}
	exportsDir := filepath.Join(filepath.Dir(store.Dir()), "exports")
	entries, err := os.ReadDir(exportsDir)
	if err != nil {
		t.Fatalf("exports 目录应存在: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("exports 目录应恰有 1 个文件，实际 %d", len(entries))
	}
	fname := entries[0].Name()
	if fname != "evil_name__.archive.md" {
		t.Fatalf("导出文件名应为消毒后的名字，实际 %q", fname)
	}
	data, err := os.ReadFile(filepath.Join(exportsDir, fname))
	if err != nil || !strings.Contains(string(data), "first-archived-question") {
		t.Fatalf("消毒路径下的导出内容应完整: err=%v", err)
	}
	// 敌对名字不得按字面落盘（exports 目录无原名字残留）
	if _, err := os.Stat(filepath.Join(exportsDir, hostile+".archive.md")); !os.IsNotExist(err) {
		t.Fatalf("不得按原始敌对文件名落盘: %v", err)
	}
}

// TestHandleArchiveViewInvalidParams 结果断言（审查缺口：无效参数）：
// 非数字/0/负数/小数记录号返回带用法的错误提示，而非 panic/空输出/误读第 1 段。
func TestHandleArchiveViewInvalidParams(t *testing.T) {
	m := newGuardTestModel(t)
	store := m2Store(t)
	m.SessionModel.store = store
	name := "arch-invalid-params"
	writeArchiveFixture(t, store, name)
	m.sessionService.SetSessionName(name)

	for _, bad := range []string{"view abc", "view 0", "view -1", "view 1.5"} {
		out := m.handleArchive(bad)
		if !strings.Contains(out, "无效") || !strings.Contains(out, "/archive view N") {
			t.Fatalf("%q 应返回带用法的无效参数提示，实际：\n%s", bad, out)
		}
		// 关键结果断言：无效参数不得退化成读取第 1 段（把 "abc" 当 1）
		if strings.Contains(out, "first-archived-question") {
			t.Fatalf("%q 不得被误读为第 1 段记录", bad)
		}
	}
}
