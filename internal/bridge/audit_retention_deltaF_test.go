// audit_retention_deltaF_test.go — S13 I9「不可变追加」的判别性验收（delta F 侧）。
//
// 本文件刻意只用**既有**符号（NewFileAuditLogger / Log / Cleanup / AuditEntry），
// 因此同一份测试能在"before"（-overlay 回退到原始 audit.go）与"after"两颗二进制上
// 同时编译，并给出**可判别**的红/绿对照：
//
//	before：Cleanup 是 read→filter→rename **整文件重写** ⇒ 历史字节被改写 ⇒ 红
//	after ：Cleanup 只做**整文件**删除/保留，永不改写历史字节       ⇒ 绿
package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deltaFLineOffsets 返回每一行在文件中的起始字节偏移。
func deltaFLineOffsets(data []byte) []int {
	var out []int
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			out = append(out, start)
			start = i + 1
		}
	}
	return out
}

func deltaFSameOffsets(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDeltaF_T6_CleanupNeverRewritesHistory —— T6：Cleanup 后历史字节**逐字节不变**。
//
// 断言"字节内容与行内偏移不变"，而不是"行数变少" —— 后者在 read→filter→rename
// 下同样成立，正是本用例要否定的那个行为（S13-HG Q1=A 的派生要求）。
func TestDeltaF_T6_CleanupNeverRewritesHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l := NewFileAuditLogger(path)

	old := time.Now().Add(-(auditRetentionDays + 1) * 24 * time.Hour).UnixMilli()
	fresh := time.Now().UnixMilli()
	if err := l.Log(&AuditEntry{Timestamp: old, Operation: AuditOpVerify, TokenID: "old", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(&AuditEntry{Timestamp: fresh, Operation: AuditOpVerify, TokenID: "new", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(splitAuditLines(string(before))) != 2 {
		t.Fatalf("前置：应有 2 行，实际 %q", before)
	}
	offsetsBefore := deltaFLineOffsets(before)

	if err := l.Cleanup(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Cleanup 不得删除尚未整体过期的当前文件: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("T6 失败：Cleanup 改写了历史字节（append-only 要求保留策略只做整文件删除，不得 read→filter→rename）\n"+
			" before=%q\n after =%q", before, after)
	}
	if offsetsAfter := deltaFLineOffsets(after); !deltaFSameOffsets(offsetsBefore, offsetsAfter) {
		t.Fatalf("T6 失败：行内偏移发生变化 before=%v after=%v", offsetsBefore, offsetsAfter)
	}
}

// TestDeltaF_T6b_CleanupDeletesExpiredArchiveAsWholeFile —— 过期归档**整个文件**删除，
// 未过期归档**字节不动**。
func TestDeltaF_T6b_CleanupDeletesExpiredArchiveAsWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l := NewFileAuditLogger(path)

	expiredName := "audit." + time.Now().AddDate(0, 0, -(auditRetentionDays+5)).Format("20060102") + ".jsonl"
	freshName := "audit." + time.Now().Format("20060102") + ".jsonl"
	expired := filepath.Join(dir, expiredName)
	fresh := filepath.Join(dir, freshName)
	payload := []byte(`{"timestamp":1,"operation":"verify","token_id":"archived","result":"success"}` + "\n")
	for _, p := range []string{expired, fresh} {
		if err := os.WriteFile(p, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := l.Cleanup(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("T6b 失败：过期归档应被**整个删除**，实际仍在（stat err=%v）", err)
	}
	got, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("T6b 失败：未过期的归档不得被删除: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("T6b 失败：未过期归档的字节被改写: %q", got)
	}
}

// TestDeltaF_T6c_LogRotatesWholeFileInsteadOfRewriting —— 跨天写入把旧文件**整文件改名**
// 归档（而非清空/重写），归档内容与轮转前逐字节一致。
func TestDeltaF_T6c_LogRotatesWholeFileInsteadOfRewriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l := NewFileAuditLogger(path)

	if err := l.Log(&AuditEntry{Operation: AuditOpVerify, TokenID: "yesterday", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	// 把当前文件的 mtime 拨回昨天 ⇒ 下一次 Log 必须触发轮转
	yesterday := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Log(&AuditEntry{Operation: AuditOpVerify, TokenID: "today", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(dir, "audit."+yesterday.Format("20060102")+".jsonl")
	got, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("T6c 失败：跨天写入未把旧文件整文件归档（append-only 要求轮转而不是重写）: %v", err)
	}
	if !bytes.Equal(before, got) {
		t.Fatalf("T6c 失败：归档内容被改写\n before=%q\n archive=%q", before, got)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := splitAuditLines(string(cur)); len(lines) != 1 {
		t.Fatalf("T6c 失败：轮转后当前文件应只含今天那 1 行，实际 %d 行: %q", len(lines), cur)
	}
}
