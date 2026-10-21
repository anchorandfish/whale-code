// Package session — 桶 X6 O-3 Session 文件损坏恢复反例测试。
//
// ⚠️ 已翻转（G-B / A8，2026-09-14）：本文件此前钉住「Load 跳过损坏行、返回可用
// session + LoadError」的 **fail-open** 行为。A8 改判为 **fail-closed T2** 后，
// 以下 3 个用例的期望被反向（拒绝加载 + 隔离），空行/正常文件两个守卫用例不变。
// 判别性证据：改判前这些用例在旧断言下为 GREEN、在新断言下为 RED；改判后反之。
//
// 覆盖场景（改判后）：
//   - Load 遇损坏行 ⇒ 拒绝加载（session == nil）+ *SessionCorruptionError + 隔离
//   - 损坏行前后的有效行**不会**被部分加载（不得返回半成品会话）
//   - LoadWithReport 把损坏行号 + 隔离路径透出到 LoadReport，并上抛错误
//   - 全部行损坏时同样拒绝加载
//   - 空行不被计为损坏行（跳过而非报错）
package session

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// TestX6_Load_RefusesCorruptSession 验证 Load 拒绝加载损坏会话（A8 fail-closed T2）。
// 反例（旧行为）：若 Load 跳过损坏行继续返回可用 session，用户会在不知情下
// 使用一份被篡改/截断的对话历史。
func TestX6_Load_RefusesCorruptSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "corrupt-test"

	// 第1行：有效，第2行：损坏，第3行：有效，第4行：损坏
	path := store.sessionPath(sessionID)
	valid1, _ := json.Marshal(UserText("valid line 1"))
	valid2, _ := json.Marshal(UserText("valid line 2"))
	content := string(valid1) + "\n" +
		"THIS_IS_CORRUPT_JSON{invalid}\n" +
		string(valid2) + "\n" +
		"{another corrupt line\n"
	os.WriteFile(path, []byte(content), 0o600)

	session, loadErr := store.Load(sessionID)

	if loadErr == nil {
		t.Fatal("A8：损坏行必须导致拒绝加载，实际无错误")
	}
	if session != nil {
		t.Fatalf("A8：拒绝加载时不得返回 session，实际 %d 条消息", len(session.GetMessages()))
	}
	var sce *SessionCorruptionError
	if !errors.As(loadErr, &sce) {
		t.Fatalf("expected *SessionCorruptionError, got %T: %v", loadErr, loadErr)
	}

	// 应记录 2 个损坏行（第 2 行和第 4 行）
	if len(sce.CorruptLines) != 2 {
		t.Errorf("expected 2 corrupt lines, got %d: %v", len(sce.CorruptLines), sce.CorruptLines)
	}
	expectedLines := map[int]bool{2: true, 4: true}
	for _, line := range sce.CorruptLines {
		if !expectedLines[line] {
			t.Errorf("unexpected corrupt line number: %d", line)
		}
	}
	if sce.QuarantinePath == "" {
		t.Error("A8：错误必须携带隔离路径")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("A8：损坏文件必须已从可加载路径移除")
	}
}

// TestX6_Load_NoPartialLoadAroundCorrupt 验证损坏行前后的有效行不会被部分加载。
// 反例（旧行为）：若 Load 边扫边返回，损坏行之后的有效行会以「半成品会话」形式
// 被上层当成完整历史使用。
func TestX6_Load_NoPartialLoadAroundCorrupt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "around-corrupt"

	path := store.sessionPath(sessionID)
	msg1, _ := json.Marshal(UserText("before corrupt"))
	msg2, _ := json.Marshal(UserText("after corrupt"))
	content := string(msg1) + "\n" +
		"CORRUPT_LINE\n" +
		string(msg2) + "\n"
	os.WriteFile(path, []byte(content), 0o600)

	session, loadErr := store.Load(sessionID)
	if loadErr == nil {
		t.Fatal("A8：损坏行必须导致拒绝加载")
	}
	if session != nil {
		t.Fatal("A8：不得返回部分加载的会话（即使绝大多数行都是有效的）")
	}
}

// TestX6_LoadWithReport_SurfacesCorruptLinesAndQuarantine 验证 LoadWithReport
// 把损坏行号与隔离路径透出到 LoadReport，同时上抛错误（不再静默降级）。
func TestX6_LoadWithReport_SurfacesCorruptLinesAndQuarantine(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "report-corrupt"

	path := store.sessionPath(sessionID)
	valid, _ := json.Marshal(UserText("good message"))
	content := string(valid) + "\n" +
		"BROKEN_JSON{{{\n" +
		string(valid) + "\n"
	os.WriteFile(path, []byte(content), 0o600)

	session, report, loadErr := store.LoadWithReport(sessionID)
	if loadErr == nil {
		t.Fatal("A8：LoadWithReport 不得吞掉完整性错误")
	}
	if session != nil {
		t.Fatal("A8：LoadWithReport 不得返回 session")
	}
	if report == nil {
		t.Fatal("A8：LoadWithReport 必须返回 LoadReport")
	}
	if len(report.CorruptLines) != 1 || report.CorruptLines[0] != 2 {
		t.Errorf("expected corrupt line [2] in report, got %v", report.CorruptLines)
	}
	if report.QuarantinePath == "" {
		t.Error("A8：LoadReport 必须携带隔离路径")
	}
}

// TestX6_Load_AllLinesCorrupt 验证全部行损坏时同样拒绝加载。
// 反例（旧行为）：返回空 session + error 会让调用方拿到一个「零消息但可用」的
// 会话，误以为是新会话并把原历史覆盖掉。
func TestX6_Load_AllLinesCorrupt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "all-corrupt"

	path := store.sessionPath(sessionID)
	content := "CORRUPT_1\nCORRUPT_2\nCORRUPT_3\n"
	os.WriteFile(path, []byte(content), 0o600)

	session, loadErr := store.Load(sessionID)
	if loadErr == nil {
		t.Fatal("expected error when all lines are corrupt")
	}
	var sce *SessionCorruptionError
	if !errors.As(loadErr, &sce) {
		t.Fatalf("expected *SessionCorruptionError, got %T", loadErr)
	}
	if len(sce.CorruptLines) != 3 {
		t.Errorf("expected 3 corrupt lines, got %d", len(sce.CorruptLines))
	}
	if session != nil {
		t.Fatal("A8：拒绝加载时必须返回 nil session")
	}
}

// TestX6_Load_EmptyLinesSkipped 验证空行被跳过而非计为损坏行。
// 反例：若空行被计为损坏行，会误判整份会话损坏并触发隔离。
func TestX6_Load_EmptyLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "empty-lines"

	path := store.sessionPath(sessionID)
	valid, _ := json.Marshal(UserText("message"))
	content := string(valid) + "\n\n\n" + string(valid) + "\n\n"
	os.WriteFile(path, []byte(content), 0o600)

	session, err := store.Load(sessionID)
	// 不应有 error（空行被跳过，不计为损坏）
	if err != nil {
		t.Errorf("expected no error for empty lines, got: %v", err)
	}
	msgs := session.GetMessages()
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages (empty lines skipped), got %d", len(msgs))
	}
}

// TestX6_Load_NoCorruptLines 验证正常文件不返回错误、不被隔离。
func TestX6_Load_NoCorruptLines(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "clean-file"

	store.Append(sessionID, UserText("clean message 1"))
	store.Append(sessionID, UserText("clean message 2"))

	session, err := store.Load(sessionID)
	if err != nil {
		t.Errorf("expected no error for clean file, got: %v", err)
	}
	msgs := session.GetMessages()
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}
}
