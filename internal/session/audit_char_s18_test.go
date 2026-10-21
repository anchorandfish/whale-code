package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runtime"
)

// audit_char_s18_test.go — S18 characterization tests (Snapshot phase).
//
// 目的：钉死 internal/session 的「当前行为」(含偏离项)，作为后续 Fix 的回归护栏。
// 这些测试在【未修改的生产源码】上必须保持 GREEN。它们不改变任何生产逻辑，
// 仅新增 _test.go 文件（属于本轮审计的度量装置，满足硬规则 1「先钉住再改」）。
//
// 覆盖范围：I6 原子写/fail-safe、I9 append-only、I11 资源/锁语义。
//
// ⚠️ Fix 决策翻转（G-B / A8，2026-09-14 19:45 改判）：
// 原先 G2 选「选项 C：损坏行跳过 + 强制可见 + 审计事件」（fail-open），
// 现**推翻**为 **fail-closed T2**：拒绝加载该会话 + 隔离损坏文件 + 显著告警；
// 「可见 + 审计」降为附加项。本文件两个 corrupt 用例已按改判翻转期望
// （原 `TestS18_Session_CorruptLineSkippedFailOpen` → `..._RefusedFailClosed`）。
// 其余 I6/I9/I11 用例（原子写/锁/.tmp 恢复）不受影响，保持原断言。

func TestS18_Session_AppendIsAppendOnlyNotTruncate(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "audit-append-1"
	if err := store.Append(id, UserText("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(id, UserText("second")); err != nil {
		t.Fatal(err)
	}
	// 再用 Append 追加一条（O_APPEND 模式，不应截断已有内容）
	if err := store.Append(id, UserText("third")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.sessionPath(id))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("期望 3 行，实际 %d: %q", len(lines), data)
	}
	for _, l := range lines {
		if !strings.Contains(l, "blocks") {
			t.Fatalf("某行缺少 blocks 字段（不是合法 JSONL 消息）: %q", l)
		}
	}
}

// TestS18_Session_CorruptLineRefusedFailClosed —— A8 改判后：损坏行 ⇒ 拒绝加载。
// （本用例原名 TestS18_Session_CorruptLineSkippedFailOpen，钉住旧的 fail-open 行为；
// A8 改为 fail-closed T2 后期望整体反向：session==nil + 类型化错误 + 已隔离。）
func TestS18_Session_CorruptLineRefusedFailClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "audit-corrupt-1"
	valid1, _ := json.Marshal(UserText("valid1"))
	valid2, _ := json.Marshal(UserText("valid2"))
	content := string(valid1) + "\n" +
		"this line is not valid json\n" +
		string(valid2) + "\n"
	if err := os.WriteFile(store.sessionPath(id), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, loadErr := store.Load(id)
	if sess != nil {
		t.Fatalf("A8：损坏场景下 Load 必须返回 nil session，实际 %d 条消息", len(sess.messages))
	}
	if loadErr == nil {
		t.Fatal("A8：损坏场景下 Load 必须返回错误")
	}
	var sce *SessionCorruptionError
	if !errors.As(loadErr, &sce) {
		t.Fatalf("期望 *SessionCorruptionError，实际 %T (%v)", loadErr, loadErr)
	}
	if len(sce.CorruptLines) != 1 || sce.CorruptLines[0] != 2 {
		t.Fatalf("期望 CorruptLines==[2]，实际 %v", sce.CorruptLines)
	}
	if sce.QuarantinePath == "" {
		t.Fatal("A8：必须已隔离损坏文件（隔离路径为空）")
	}
}

// TestS18_Session_CorruptLineWritesAuditEvent —— A8 附加项「审计」保留。
// 改判后：拒绝加载 + 隔离，同时仍写一条 append-only 完整性审计事件
// （含隔离路径），使拒绝动作同样留下不可否认的痕迹。
func TestS18_Session_CorruptLineWritesAuditEvent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "audit-corrupt-audit-1"
	valid1, _ := json.Marshal(UserText("valid1"))
	valid2, _ := json.Marshal(UserText("valid2"))
	content := string(valid1) + "\n" +
		"NOT VALID JSON\n" +
		string(valid2) + "\n"
	if err := os.WriteFile(store.sessionPath(id), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	sess, loadErr := store.Load(id)
	if sess != nil {
		t.Fatal("A8：损坏场景下 Load 必须返回 nil session")
	}
	var sce *SessionCorruptionError
	if !errors.As(loadErr, &sce) {
		t.Fatalf("期望 *SessionCorruptionError，实际 %T (%v)", loadErr, loadErr)
	}

	// 审计事件必须落盘
	auditPath := store.IntegrityAuditPath()
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("未写完整性审计事件：%v", err)
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
	if len(ev.CorruptLines) != 1 || ev.CorruptLines[0] != 2 {
		t.Fatalf("审计事件 CorruptLines 错误：%v", ev.CorruptLines)
	}
	if ev.SkippedCount != 1 {
		t.Fatalf("审计事件 SkippedCount 错误：%d", ev.SkippedCount)
	}
	if ev.QuarantinePath == "" {
		t.Fatal("A8：审计事件必须记录隔离路径")
	}
	if ev.QuarantinePath != sce.QuarantinePath {
		t.Fatalf("审计路径与错误路径不一致：%q vs %q", ev.QuarantinePath, sce.QuarantinePath)
	}
	// 审计必须放在 audit/ 子目录，避免被 List() 当作会话文件误列
	if filepath.Base(filepath.Dir(auditPath)) != "audit" {
		t.Fatalf("审计文件目录错误：%s", auditPath)
	}
}

func TestS18_Session_SaveAtomicNoTmpLeak(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "audit-save-1"
	sess := NewSession()
	sess.AddMessage(UserText("a"))
	sess.AddMessage(UserText("b"))
	if err := store.Save(id, sess); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("Save 泄漏了 .tmp 文件: %s", e.Name())
		}
	}
}

func TestS18_Session_LockUsesOEXCLAnd0600(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "audit-lock-1"
	lock1, err := store.AcquireLock(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lock1.Release()
	// 同一 id 二次加锁必须失败（O_EXCL 语义）
	if _, err := store.AcquireLock(id); err == nil {
		t.Fatal("期望第二次 AcquireLock 因 O_EXCL 失败，却成功了")
	}
	// 找到 .lock 文件并校验权限为 0600
	entries, _ := os.ReadDir(dir)
	var lockPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lock") {
			lockPath = filepath.Join(dir, e.Name())
			break
		}
	}
	if lockPath == "" {
		t.Fatal("未找到 .lock 文件")
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 无 POSIX 权限位（Go 建文件恒 0666），0600 断言仅适用 POSIX；
	// O_EXCL 二次加锁失败的断言（上方）两侧都保留。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("锁文件权限 = %o，期望 0600", info.Mode().Perm())
	}
}

func TestS18_Session_RecoverCrashedTmpPromotesOld(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "audit-tmp-1"
	// 先种一个有效主文件
	if err := store.Append(id, UserText("orig")); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃残留 .tmp（已完整写入但未 rename），且 mtime 超过 5 分钟阈值
	tmpPath := store.sessionPath(id) + ".tmp"
	recovered, _ := json.Marshal(UserText("recovered"))
	if err := os.WriteFile(tmpPath, append(recovered, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(tmpPath, old, old); err != nil {
		t.Fatal(err)
	}
	sess, report, err := store.LoadWithReport(id)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || !report.RecoveredTmp {
		t.Fatal("期望对旧 .tmp 触发 RecoveredTmp=true")
	}
	if len(sess.messages) != 1 {
		t.Fatalf("期望恢复出 1 条消息，实际 %d", len(sess.messages))
	}
	if got := sess.messages[0].Blocks; len(got) == 0 || got[0].Text != "recovered" {
		t.Fatalf("期望恢复出 recovered 消息，实际 %+v", sess.messages[0])
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("旧 .tmp 提升后应被删除")
	}
}
