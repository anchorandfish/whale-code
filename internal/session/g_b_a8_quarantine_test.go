// g_b_a8_quarantine_test.go — G-B（O-B）A8 判别性测试：损坏会话 = fail-closed T2。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md §2 A8 行（★已改判 19:45）
//
//	A8 | S18-G2 | session.Load 毁坏行静默跳过（fail-open）
//	   | ✅ fail-closed T2（拒绝加载该会话 + 隔离损坏文件 + 显著告警）
//	   | 推翻 S18 早先的「Option C 跳过但可见 + 审计」；
//	   | 「可见 + 审计」降为附加项（不是替代品）
//
// 这些用例在**改判前必须是 RED**（当前 Load 返回 (可用 session, *LoadError) 且不隔离），
// 改判后 GREEN。断言全部走生产入口 Store.Load / Store.LoadWithReport（不经内部辅助函数）。
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corruptSessionFixture 写一个「第 1/3 行有效、第 2/4 行损坏」的会话文件。
func corruptSessionFixture(t *testing.T, store *Store, id string) string {
	t.Helper()
	valid1, _ := json.Marshal(UserText("valid line 1"))
	valid2, _ := json.Marshal(UserText("valid line 2"))
	content := string(valid1) + "\n" +
		"THIS_IS_CORRUPT_JSON{invalid}\n" +
		string(valid2) + "\n" +
		"{another corrupt line\n"
	path := store.sessionPath(id)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestG_B_A8_LoadRefusesCorruptSessionAndQuarantines —— T2 主语义。
// 期望：session==nil（拒绝加载）+ 类型化错误 + 损坏文件被隔离（原路径消失、隔离路径存在且内容完整）。
func TestG_B_A8_LoadRefusesCorruptSessionAndQuarantines(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "a8-quarantine-1"
	origPath := corruptSessionFixture(t, store, id)

	sess, loadErr := store.Load(id)

	// 1) 拒绝加载：不得返回可用 session
	if sess != nil {
		t.Fatalf("A8 T2：损坏会话必须拒绝加载（session 必须为 nil），实际非 nil（%d 条消息）", len(sess.GetMessages()))
	}
	if loadErr == nil {
		t.Fatal("A8 T2：损坏会话必须返回 error，实际 nil")
	}

	// 2) 类型化错误必须携带损坏行号 + 隔离路径
	var sce *SessionCorruptionError
	if !errors.As(loadErr, &sce) {
		t.Fatalf("A8 T2：期望 *SessionCorruptionError，实际 %T (%v)", loadErr, loadErr)
	}
	if len(sce.CorruptLines) != 2 || sce.CorruptLines[0] != 2 || sce.CorruptLines[1] != 4 {
		t.Fatalf("A8：损坏行号错误：%v", sce.CorruptLines)
	}
	if sce.QuarantinePath == "" {
		t.Fatalf("A8 T2：错误必须携带隔离路径，实际为空")
	}

	// 3) 隔离已发生：原路径消失、隔离路径存在、字节完整（不丢数据）
	if _, err := os.Stat(origPath); !os.IsNotExist(err) {
		t.Fatalf("A8 T2：损坏文档未被隔离（原路径 %s 仍存在）", origPath)
	}
	qBytes, err := os.ReadFile(sce.QuarantinePath)
	if err != nil {
		t.Fatalf("A8 T2：隔离文件不可读（%s）：%v", sce.QuarantinePath, err)
	}
	if !strings.Contains(string(qBytes), "THIS_IS_CORRUPT_JSON{invalid}") {
		t.Fatal("A8 T2：隔离文件内容不完整（原始损坏字节必须原样保留以便人工修复）")
	}

	// 4) 「显著告警」必须可被上层看到：错误文案含隔离路径与拒绝语义
	msg := loadErr.Error()
	if !strings.Contains(msg, sce.QuarantinePath) {
		t.Fatalf("A8：错误文案未暴露隔离路径（上层无法提示用户恢复位置）：%q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "refus") {
		t.Fatalf("A8：错误文案未体现「拒绝加载」语义：%q", msg)
	}
}

// TestG_B_A8_LoadWithReportNoLongerSwallows —— 附加项：透出必须改为上抛，不得静默吞掉。
func TestG_B_A8_LoadWithReportNoLongerSwallows(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "a8-report-1"
	corruptSessionFixture(t, store, id)

	sess, report, loadErr := store.LoadWithReport(id)

	if loadErr == nil {
		t.Fatal("A8 T2：LoadWithReport 不得把 fail-closed 错误降级为 nil（静默吞错）")
	}
	if sess != nil {
		t.Fatalf("A8 T2：LoadWithReport 返回了 session（应拒绝加载）")
	}
	if report == nil {
		t.Fatal("A8：LoadWithReport 必须返回 LoadReport（供上层提示）")
	}
	if len(report.CorruptLines) != 2 {
		t.Fatalf("A8：LoadReport.CorruptLines 丢失：%v", report.CorruptLines)
	}
	if report.QuarantinePath == "" {
		t.Fatal("A8：LoadReport 必须携带隔离路径（可视性附加项）")
	}
}

// TestG_B_A8_QuarantineWritesAuditEvent —— 附加项「审计」保留（append-only）。
func TestG_B_A8_QuarantineWritesAuditEvent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "a8-audit-1"
	corruptSessionFixture(t, store, id)

	_, loadErr := store.Load(id)
	if loadErr == nil {
		t.Fatal("期望拒绝加载")
	}

	data, err := os.ReadFile(store.IntegrityAuditPath())
	if err != nil {
		t.Fatalf("A8：未写完整性审计事件：%v", err)
	}
	var ev IntegrityAuditEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &ev); err != nil {
		t.Fatalf("审计事件不是合法 JSON：%q (%v)", data, err)
	}
	if ev.Event != IntegrityAuditEventSessionCorrupt {
		t.Fatalf("审计事件类型错误：%q", ev.Event)
	}
	if ev.SessionID != id {
		t.Fatalf("审计事件 SessionID 错误：%q", ev.SessionID)
	}
	if len(ev.CorruptLines) != 2 || ev.CorruptLines[0] != 2 || ev.CorruptLines[1] != 4 {
		t.Fatalf("审计事件 CorruptLines 错误：%v", ev.CorruptLines)
	}
	if ev.QuarantinePath == "" {
		t.Fatal("A8：审计事件必须记录隔离路径（事后核查的不可否认痕迹）")
	}
	// 审计不得落在顶层目录（否则会被 List() 误列为会话）
	if filepath.Base(filepath.Dir(store.IntegrityAuditPath())) != "audit" {
		t.Fatalf("审计文件目录错误：%s", store.IntegrityAuditPath())
	}
}

// TestG_B_A8_QuarantineSecondLoadKeepsRefusingWithoutPanic —— 幂等/重复加载不得改变结论。
// 隔离后原路径已不存在：再次 Load 必须仍失败（不可回退为「空会话可用」），且不得 panic。
func TestG_B_A8_QuarantineSecondLoadKeepsRefusingWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "a8-idempotent-1"
	corruptSessionFixture(t, store, id)

	if _, err := store.Load(id); err == nil {
		t.Fatal("首次 Load 应拒绝")
	}
	sess, err2 := store.Load(id)
	if err2 == nil {
		t.Fatal("二次 Load 不得因为文件已隔离而放行")
	}
	if sess != nil {
		t.Fatal("二次 Load 不得返回非 nil session")
	}

	// 隔离目录下必须恰有一个隔离件（不得重复隔离或产生垃圾）
	qDir := filepath.Join(dir, quarantineDirName)
	entries, err := os.ReadDir(qDir)
	if err != nil {
		t.Fatalf("隔离目录不可读：%v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("隔离目录应有 1 个隔离件，实际 %d: %v", len(entries), entries)
	}
}

// TestG_B_A8_QuarantinedSessionNotListedAsLive —— 隔离后不得仍以「可恢复会话」身份出现在 List()。
func TestG_B_A8_QuarantinedSessionNotListedAsLive(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	healthy := "a8-healthy-1"
	if err := store.Append(healthy, UserText("ok")); err != nil {
		t.Fatal(err)
	}
	corrupt := "a8-broken-1"
	corruptSessionFixture(t, store, corrupt)

	if _, err := store.Load(corrupt); err == nil {
		t.Fatal("期望拒绝加载")
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == corrupt {
			t.Fatalf("A8：已隔离会话不得再作为可加载会话列出：%s", s.ID)
		}
	}
	// 隔离不影响健康会话
	var foundHealthy bool
	for _, s := range list {
		if s.ID == healthy {
			foundHealthy = true
		}
	}
	if !foundHealthy {
		t.Fatal("A8：隔离损坏会话不得影响健康会话的可见性")
	}
}

// TestG_B_A8_HealthySessionNotQuarantined —— 反例守卫（防误伤）：正常会话不得被隔离/拒绝。
func TestG_B_A8_HealthySessionNotQuarantined(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "a8-clean-1"
	if err := store.Append(id, UserText("clean 1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(id, UserText("clean 2")); err != nil {
		t.Fatal(err)
	}

	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("正常会话被误拒：%v", err)
	}
	if sess == nil || len(sess.GetMessages()) != 2 {
		t.Fatalf("正常会话加载结果错误：%v", sess)
	}
	if _, err := os.Stat(filepath.Join(dir, quarantineDirName)); !os.IsNotExist(err) {
		t.Fatal("A8：正常会话不得触发隔离目录创建（防误伤）")
	}
}
