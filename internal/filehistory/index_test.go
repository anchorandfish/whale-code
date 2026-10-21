package filehistory

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestAppendEntry 验证追加索引记录（TC-301）
func TestAppendEntry(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	entry := IndexEntry{
		Type:       "track",
		MsgId:      "msg-001",
		ToolCallId: "tc-001",
		ToolName:   "write_file",
		Timestamp:  time.Now(),
		Files: []FileChange{
			{Path: "/test/file.go", Mode: 0644},
		},
	}

	if err := appendEntry(indexFile, entry); err != nil {
		t.Fatalf("appendEntry: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(indexFile); err != nil {
		t.Fatalf("index file not created: %v", err)
	}

	// 验证内容可被解析
	entries, err := scanEntries(indexFile)
	if err != nil {
		t.Fatalf("scanEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].MsgId != "msg-001" {
		t.Errorf("expected msgId=msg-001, got %s", entries[0].MsgId)
	}
}

// TestScanEntries 验证扫描多条索引记录（TC-302）
func TestScanEntries(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	// 追加 3 条记录
	for i := 0; i < 3; i++ {
		entry := IndexEntry{
			Type:      "track",
			MsgId:     "msg-" + string(rune('A'+i)),
			Timestamp: time.Now(),
			Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
		}
		if err := appendEntry(indexFile, entry); err != nil {
			t.Fatalf("appendEntry %d: %v", i, err)
		}
	}

	entries, err := scanEntries(indexFile)
	if err != nil {
		t.Fatalf("scanEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

// TestScanEntries_FailsOnCorrupt 验证损坏行触发 fail-closed（S10 / HG1=a，TC-303 翻转）。
// 原 TestScanEntries_SkipCorrupt 钉的是"跳过损坏行不报错"的 fail-open 行为；
// 决策 HG1=a 后行为刻意翻转为：损坏行 -> 返回错误、不返回部分记录。
func TestScanEntries_FailsOnCorrupt(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	// 手动写入混合内容：1 条正常 + 1 条损坏 + 1 条正常
	validEntry := `{"type":"track","msgId":"msg-valid","timestamp":"2026-01-01T00:00:00Z","files":[{"path":"/test.go","mode":420}]}`
	corruptLine := `{this is not valid json!!!`
	validEntry2 := `{"type":"snapshot","msgId":"snap-valid","timestamp":"2026-01-01T00:00:00Z","files":[]}`

	content := validEntry + "\n" + corruptLine + "\n" + validEntry2 + "\n"
	if err := os.WriteFile(indexFile, []byte(content), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	entries, err := scanEntries(indexFile)
	if err == nil {
		t.Fatalf("expected scanEntries to FAIL on corrupt line (fail-closed), got nil error, entries=%d", len(entries))
	}
	if entries != nil {
		t.Errorf("expected nil entries on corrupt line (no partial load), got %d", len(entries))
	}
}

// TestAppendEntry_ConcurrentSafety 验证并发追加安全（TC-304）
func TestAppendEntry_ConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	var wg sync.WaitGroup
	numGoroutines := 10
	entriesPerGoroutine := 5

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < entriesPerGoroutine; j++ {
				entry := IndexEntry{
					Type:      "track",
					MsgId:     "msg-concurrent",
					Timestamp: time.Now(),
					Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
				}
				if err := appendEntry(indexFile, entry); err != nil {
					t.Errorf("concurrent appendEntry: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	entries, err := scanEntries(indexFile)
	if err != nil {
		t.Fatalf("scanEntries: %v", err)
	}

	expected := numGoroutines * entriesPerGoroutine
	if len(entries) != expected {
		t.Errorf("expected %d entries, got %d (concurrent writes may have been lost)", expected, len(entries))
	}
}

// TestTruncateAfter 验证截断索引记录（TC-305）
func TestTruncateAfter(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	// 写入 3 条记录
	msgIds := []string{"msg-A", "msg-B", "msg-C"}
	for _, msgId := range msgIds {
		entry := IndexEntry{
			Type:      "track",
			MsgId:     msgId,
			Timestamp: time.Now(),
			Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
		}
		if err := appendEntry(indexFile, entry); err != nil {
			t.Fatalf("appendEntry %s: %v", msgId, err)
		}
	}

	// 截断到 msg-B（保留 msg-A 和 msg-B）
	if err := truncateAfter(indexFile, "msg-B"); err != nil {
		t.Fatalf("truncateAfter: %v", err)
	}

	entries, err := scanEntries(indexFile)
	if err != nil {
		t.Fatalf("scanEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries after truncate, got %d", len(entries))
	}
	if entries[1].MsgId != "msg-B" {
		t.Errorf("last entry should be msg-B, got %s", entries[1].MsgId)
	}
}

// TestFindEntry 验证查找指定记录（TC-306）
func TestFindEntry(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	// 写入 track 和 snapshot 记录
	trackEntry := IndexEntry{
		Type:       "track",
		MsgId:      "msg-001",
		ToolCallId: "tc-001",
		Timestamp:  time.Now(),
		Files:      []FileChange{{Path: "/test.go", Mode: 0644}},
	}
	snapEntry := IndexEntry{
		Type:      "snapshot",
		MsgId:     "msg-001",
		Label:     "test-snapshot",
		Timestamp: time.Now(),
		Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
	}

	if err := appendEntry(indexFile, trackEntry); err != nil {
		t.Fatalf("appendEntry track: %v", err)
	}
	if err := appendEntry(indexFile, snapEntry); err != nil {
		t.Fatalf("appendEntry snapshot: %v", err)
	}

	// 查找 snapshot 类型
	found, err := findEntry(indexFile, "msg-001", "snapshot")
	if err != nil {
		t.Fatalf("findEntry: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find snapshot entry")
	}
	if found.Label != "test-snapshot" {
		t.Errorf("expected label=test-snapshot, got %s", found.Label)
	}

	// 查找不存在的记录
	notFound, _ := findEntry(indexFile, "msg-nonexistent", "snapshot")
	if notFound != nil {
		t.Error("expected nil for nonexistent entry")
	}
}

// TestScanEntriesAfter 验证扫描指定 msgId 之后的记录
func TestScanEntriesAfter(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	for _, msgId := range []string{"msg-A", "msg-B", "msg-C"} {
		entry := IndexEntry{
			Type:      "track",
			MsgId:     msgId,
			Timestamp: time.Now(),
			Files:     []FileChange{{Path: "/test.go", Mode: 0644}},
		}
		if err := appendEntry(indexFile, entry); err != nil {
			t.Fatalf("appendEntry %s: %v", msgId, err)
		}
	}

	// 获取 msg-B 及之后的记录
	after, err := scanEntriesAfter(indexFile, "msg-B")
	if err != nil {
		t.Fatalf("scanEntriesAfter: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("expected 2 entries after msg-B, got %d", len(after))
	}
}

// TestScanEntries_EmptyFile 验证空索引文件返回空列表
func TestScanEntries_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	indexFile := filepath.Join(dir, "index.jsonl")

	entries, err := scanEntries(indexFile)
	if err != nil {
		t.Fatalf("scanEntries on nonexistent file: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nonexistent file, got %d", len(entries))
	}
}
