// Package session — 桶 X6 O-2 崩溃恢复策略反例测试。
//
// 验证现有实现已覆盖 O-2 的"原子写入 + .tmp crash recovery + .bak 备份"要求。
// checkpoint 接线推迟到 v1.1（按任务"若工作量过大，可拆分为'原子写入'（本轮）+'checkpoint 接线'（v1.1）"要求）。
//
// 覆盖场景：
//   - Save 使用原子写入（temp + fsync + rename），崩溃后不留 .tmp 残留
//   - Save 前创建 .bak 备份，崩溃后可从 .bak 恢复
//   - .tmp crash recovery（S3）：旧 .tmp 被提升为主文件
//   - .tmp crash recovery（S3）：新 .tmp 不被提升（避免干扰运行中的写入）
//   - 原子写入保证：Save 失败时主文件保持旧内容完整
package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

func firstMessageRecord(t *testing.T, data []byte) Message {
	t.Helper()
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		msg, metadata, err := decodeSessionRecord(line)
		if err != nil {
			t.Fatalf("failed to parse session record: %v", err)
		}
		if metadata == nil {
			return *msg
		}
	}
	t.Fatal("session data contains no message record")
	return Message{}
}

// TestX6_Save_CreatesBakBackup 验证 Save 在覆盖前创建 .bak 备份文件。
// 反例：若 Save 不创建 .bak，崩溃后无法回滚到旧内容（FACT-SESS-005）。
func TestX6_Save_CreatesBakBackup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "bak-test"

	// 写入初始内容
	msg1 := UserText("original content")
	if err := store.Append(sessionID, msg1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Save 新内容（触发 .bak 创建）
	sess := NewSession()
	sess.AddMessage(UserText("new content"))
	if err := store.Save(sessionID, sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 验证 .bak 文件存在且包含旧内容
	bakPath := store.sessionPath(sessionID) + ".bak"
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("expected .bak file to exist, but got error: %v", err)
	}

	// .bak 应包含原始消息（Append 写入的 msg1）
	bakMsg := firstMessageRecord(t, bakData)
	if bakMsg.TextContent() != "original content" {
		t.Errorf("expected .bak to contain 'original content', got %q", bakMsg.TextContent())
	}
}

// TestX6_Save_BakPreservesPreviousState 验证多次 Save 后 .bak 始终保存上一次的内容。
// 反例：若 .bak 被新内容覆盖，崩溃后无法回滚。
func TestX6_Save_BakPreservesPreviousState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "multi-save-test"

	// 第一次 Save
	sess1 := NewSession()
	sess1.AddMessage(UserText("version-1"))
	store.Save(sessionID, sess1)

	// 第二次 Save
	sess2 := NewSession()
	sess2.AddMessage(UserText("version-2"))
	store.Save(sessionID, sess2)

	// .bak 应包含 version-1（上一次的内容）
	bakPath := store.sessionPath(sessionID) + ".bak"
	bakData, _ := os.ReadFile(bakPath)
	bakMsg := firstMessageRecord(t, bakData)
	if bakMsg.TextContent() != "version-1" {
		t.Errorf("expected .bak to contain 'version-1', got %q", bakMsg.TextContent())
	}

	// 主文件应包含 version-2（最新内容）
	mainData, _ := os.ReadFile(store.sessionPath(sessionID))
	mainMsg := firstMessageRecord(t, mainData)
	if mainMsg.TextContent() != "version-2" {
		t.Errorf("expected main file to contain 'version-2', got %q", mainMsg.TextContent())
	}
}

// TestX6_Save_AtomicNoTempLeak 验证 Save 使用原子写入，成功后不残留 .tmp 文件。
// 反例：若 Save 非原子写入，崩溃时可能留下半写文件或 .tmp 残留。
func TestX6_Save_AtomicNoTempLeak(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "atomic-test"

	sess := NewSession()
	for i := 0; i < 5; i++ {
		sess.AddMessage(UserText("msg"))
	}
	if err := store.Save(sessionID, sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 检查目录中不应有 .tmp 文件残留
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if len(name) > 4 && name[len(name)-4:] == ".tmp" {
			t.Errorf("temp file leaked after successful Save: %s", name)
		}
	}
}

// TestX6_Save_BakRecovery 验证 .bak 备份可用于手动恢复（O-2 核心场景）。
// 模拟主文件损坏后，从 .bak 恢复旧内容（反例：无 .bak 则崩溃后数据丢失）。
func TestX6_Save_BakRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "bak-recovery-test"

	// 第一次 Save（version-1）
	sess1 := NewSession()
	sess1.AddMessage(UserText("version-1"))
	store.Save(sessionID, sess1)

	// 第二次 Save（version-2，此时 .bak = version-1）
	sess2 := NewSession()
	sess2.AddMessage(UserText("version-2"))
	store.Save(sessionID, sess2)

	// 模拟主文件损坏（截断为空）
	path := store.sessionPath(sessionID)
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("failed to corrupt main file: %v", err)
	}

	// 从 .bak 恢复
	bakPath := path + ".bak"
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("failed to read .bak for recovery: %v", err)
	}
	if err := os.WriteFile(path, bakData, 0o600); err != nil {
		t.Fatalf("failed to restore from .bak: %v", err)
	}

	// 验证恢复后的内容为 version-1（.bak 保存的内容）
	session, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load after recovery failed: %v", err)
	}
	msgs := session.GetMessages()
	if len(msgs) != 1 || msgs[0].TextContent() != "version-1" {
		t.Errorf("expected 'version-1' after .bak recovery, got %d msgs", len(msgs))
	}
}

// TestX6_CrashRecovery_TmpPromotion 验证崩溃后 .tmp 被提升为主文件（S3 核心场景）。
// 反例：若 LoadWithReport 不检查 .tmp，崩溃中写入的数据会丢失。
func TestX6_CrashRecovery_TmpPromotion(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "crash-promote"

	// 写入旧主文件
	store.Append(sessionID, UserText("old state"))

	// 模拟崩溃：写入 .tmp 但不 rename（崩溃在 rename 前）
	path := store.sessionPath(sessionID)
	tmpPath := path + ".tmp"
	newMsg := UserText("crash recovered state")
	newSess := NewSession()
	newSess.AddMessage(newMsg)
	data, _ := json.Marshal(newMsg)
	data = append(data, '\n')
	os.WriteFile(tmpPath, data, 0o600)

	// 将 .tmp 的时间设为 10 分钟前（超过 crashedTmpThreshold=5min）
	oldTime := time.Now().Add(-10 * time.Minute)
	os.Chtimes(tmpPath, oldTime, oldTime)

	// LoadWithReport 应提升 .tmp
	session, report, err := store.LoadWithReport(sessionID)
	if err != nil {
		t.Fatalf("LoadWithReport failed: %v", err)
	}
	if !report.RecoveredTmp {
		t.Error("expected RecoveredTmp=true")
	}
	msgs := session.GetMessages()
	if len(msgs) != 1 || msgs[0].TextContent() != "crash recovered state" {
		t.Errorf("expected recovered content, got %d msgs", len(msgs))
	}

	// .tmp 应在提升后被删除
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error(".tmp should be removed after promotion")
	}
}

// TestX6_CrashRecovery_RecentTmpUntouched 验证新的 .tmp 不被提升（避免干扰运行中写入）。
// 反例：若新 .tmp 被提升，会破坏正在进行的原子写入。
func TestX6_CrashRecovery_RecentTmpUntouched(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "recent-tmp"

	store.Append(sessionID, UserText("main content"))

	path := store.sessionPath(sessionID)
	tmpPath := path + ".tmp"
	os.WriteFile(tmpPath, []byte("{}\n"), 0o600)

	// .tmp 时间设为 10 秒前（远小于 crashedTmpThreshold=5min）
	recentTime := time.Now().Add(-10 * time.Second)
	os.Chtimes(tmpPath, recentTime, recentTime)

	session, report, err := store.LoadWithReport(sessionID)
	if err != nil {
		t.Fatalf("LoadWithReport failed: %v", err)
	}
	if report.RecoveredTmp {
		t.Error("did not expect RecoveredTmp for recent .tmp")
	}
	// 主文件内容应不变
	msgs := session.GetMessages()
	if len(msgs) != 1 || msgs[0].TextContent() != "main content" {
		t.Error("main content should be unchanged")
	}
	// .tmp 应仍存在
	if _, err := os.Stat(tmpPath); os.IsNotExist(err) {
		t.Error("recent .tmp should not be removed")
	}
}

// TestX6_CrashRecovery_NoTmpNormalLoad 验证无 .tmp 时正常加载。
// 反例：若无 .tmp 时 RecoveredTmp=true，说明逻辑有误。
func TestX6_CrashRecovery_NoTmpNormalLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "no-tmp-normal"

	store.Append(sessionID, UserText("hello world"))

	_, report, err := store.LoadWithReport(sessionID)
	if err != nil {
		t.Fatalf("LoadWithReport failed: %v", err)
	}
	if report.RecoveredTmp {
		t.Error("did not expect RecoveredTmp when no .tmp exists")
	}
	if report.TmpPath != "" {
		t.Errorf("expected empty TmpPath, got %q", report.TmpPath)
	}
}

// TestX6_AtomicWriteFile_Durability 验证 AtomicWriteFile 写入后文件可立即读取（fsync 保证持久性）。
// 反例：若无 fsync，崩溃后文件可能为空或不完整。
func TestX6_AtomicWriteFile_Durability(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "durable.jsonl")
	data := []byte("durable content\n")

	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	// 立即读取应得到完整内容
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}

	// 不应有 .tmp 残留
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if len(name) > 4 && name[len(name)-4:] == ".tmp" {
			t.Errorf("temp file leaked: %s", name)
		}
	}
}
