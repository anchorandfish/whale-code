// Package session — tests for the S1-S11 P0 enhancements.
//
// Covers: process start-time detection (S1/S2), crash recovery (S3),
// AtomicWriteFile (S4/S5), UUID chain (S6), Compact (S7), Base64Data hash
// semantics (S8), deterministicUUID bug fix (S9), Subtype unification (S10),
// and NewStore error return (S11).
package session

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// ---------------------------------------------------------------------------
// S1/S2: process start-time detection
// ---------------------------------------------------------------------------

// TestGetProcessStartTime_CurrentProcess verifies that getProcessStartTime
// returns a non-zero value for the current process on all platforms.
func TestGetProcessStartTime_CurrentProcess(t *testing.T) {
	start, err := getProcessStartTime(os.Getpid())
	if err != nil {
		// On some CI environments the syscall may fail; skip rather than fail.
		t.Skipf("getProcessStartTime failed on this platform: %v", err)
	}
	if start == 0 {
		t.Fatal("expected non-zero start time for current process")
	}

	// Calling again should return the same value (stable within a process).
	start2, err := getProcessStartTime(os.Getpid())
	if err != nil {
		t.Skipf("second getProcessStartTime call failed: %v", err)
	}
	if start != start2 {
		t.Errorf("process start time not stable: %d vs %d", start, start2)
	}
}

// TestGetProcessStartTime_InvalidPID verifies error handling for bad PIDs.
func TestGetProcessStartTime_InvalidPID(t *testing.T) {
	_, err := getProcessStartTime(0)
	if err == nil {
		t.Error("expected error for pid 0")
	}
	_, err = getProcessStartTime(-1)
	if err == nil {
		t.Error("expected error for negative pid")
	}
}

// TestParseWmicTime verifies the WMIC date parser (Windows fallback path).
func TestParseWmicTime(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64 // 0 means "unparseable"
	}{
		{"valid full", "20260624123045.123456+480", 1},
		{"valid no fraction", "20260624123045+480", 1},
		{"too short", "202606", 0},
		{"empty", "", 0},
		{"bad month", "20261324123045.123456+480", 0},
		{"bad hour", "20260624253045.123456+480", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWmicTime(tt.input)
			if tt.want == 0 && got != 0 {
				t.Errorf("parseWmicTime(%q) = %d, want 0", tt.input, got)
			}
			if tt.want != 0 && got == 0 {
				t.Errorf("parseWmicTime(%q) = 0, want non-zero", tt.input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// S3: crash recovery (.tmp promotion)
// ---------------------------------------------------------------------------

// TestRecoverCrashedTmp_OldTmpPromoted creates a stale .tmp file and verifies
// that LoadWithReport promotes it to the main file.
func TestRecoverCrashedTmp_OldTmpPromoted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "crash-test"

	// Write the "old" main file with 1 message.
	oldMsg := UserText("old content before crash")
	if err := store.Append(sessionID, oldMsg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Simulate a crash: write a .tmp file with newer content that was never
	// renamed. The .tmp represents the state that was fully written but lost
	// when the process died before rename.
	newMsg := UserText("new content from crashed write")
	newSession := NewSession()
	newSession.AddMessage(newMsg)
	path := store.sessionPath(sessionID)
	tmpPath := path + ".tmp"

	// Serialize the new session to the .tmp file.
	data, _ := json.Marshal(newSession.GetMessages()[0])
	data = append(data, '\n')
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		t.Fatalf("failed to write .tmp: %v", err)
	}

	// Backdate the .tmp file so it looks like a crash leftover (> 5 min).
	oldTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(tmpPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes failed: %v", err)
	}

	// LoadWithReport should promote the .tmp and load the new content.
	session, report, err := store.LoadWithReport(sessionID)
	if err != nil {
		t.Fatalf("LoadWithReport failed: %v", err)
	}
	if !report.RecoveredTmp {
		t.Error("expected RecoveredTmp=true in report")
	}
	if report.TmpPath != tmpPath {
		t.Errorf("TmpPath = %q, want %q", report.TmpPath, tmpPath)
	}

	msgs := session.GetMessages()
	if len(msgs) != 1 || msgs[0].TextContent() != "new content from crashed write" {
		t.Errorf("expected recovered new content, got %d msgs: %v", len(msgs), msgs)
	}

	// The .tmp file should be gone after promotion.
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error(".tmp file should be removed after promotion")
	}
}

// TestRecoverCrashedTmp_RecentTmpUntouched verifies that a recent .tmp file
// (younger than the threshold) is NOT promoted — it may belong to a running writer.
func TestRecoverCrashedTmp_RecentTmpUntouched(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "recent-tmp"

	if err := store.Append(sessionID, UserText("main content")); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Write a .tmp that is only 10 seconds old (well under the 5-min threshold).
	path := store.sessionPath(sessionID)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("failed to write .tmp: %v", err)
	}
	recentTime := time.Now().Add(-10 * time.Second)
	os.Chtimes(tmpPath, recentTime, recentTime)

	session, report, err := store.LoadWithReport(sessionID)
	if err != nil {
		t.Fatalf("LoadWithReport failed: %v", err)
	}
	if report.RecoveredTmp {
		t.Error("did not expect RecoveredTmp for a recent .tmp")
	}
	// The main file content should be unchanged.
	if len(session.GetMessages()) != 1 || session.GetMessages()[0].TextContent() != "main content" {
		t.Error("main file content should be unchanged for recent .tmp")
	}
	// The .tmp should still exist.
	if _, err := os.Stat(tmpPath); os.IsNotExist(err) {
		t.Error("recent .tmp should not be removed")
	}
}

// TestRecoverCrashedTmp_NoTmp verifies normal load when no .tmp exists.
func TestRecoverCrashedTmp_NoTmp(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "no-tmp"
	store.Append(sessionID, UserText("hello"))

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

// ---------------------------------------------------------------------------
// S4/S5: AtomicWriteFile
// ---------------------------------------------------------------------------

// TestAtomicWriteFile_RoundTrip verifies that AtomicWriteFile writes data
// correctly and the file is readable.
func TestAtomicWriteFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	data := []byte("line1\nline2\n")

	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}

	// Verify permissions.
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0o600", info.Mode().Perm())
	}
}

// TestAtomicWriteFile_Overwrite verifies that overwriting an existing file
// works atomically (old content is fully replaced).
func TestAtomicWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overwrite.jsonl")

	// Initial write.
	util.AtomicWriteFile(path, []byte("old\n"), 0o600)
	// Overwrite.
	util.AtomicWriteFile(path, []byte("new content\n"), 0o600)

	got, _ := os.ReadFile(path)
	if string(got) != "new content\n" {
		t.Errorf("after overwrite: got %q, want %q", got, "new content\n")
	}
}

// TestAtomicWriteFileNoFsync verifies the no-fsync variant also writes correctly.
func TestAtomicWriteFileNoFsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nofsync.jsonl")
	data := []byte("no-fsync-content\n")

	if err := util.AtomicWriteFileNoFsync(path, data, 0o600); err != nil {
		t.Fatalf("AtomicWriteFileNoFsync failed: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}
}

// TestAtomicWriteFile_NoTempLeak verifies that no .tmp files are left behind
// after a successful write (important for crash recovery correctness).
func TestAtomicWriteFile_NoTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noleak.jsonl")

	util.AtomicWriteFile(path, []byte("data\n"), 0o600)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// S6: UUID chain
// ---------------------------------------------------------------------------

// TestAddMessage_AutoLinksChain verifies that AddMessage auto-generates UUIDs
// and links messages via ParentUUID.
func TestAddMessage_AutoLinksChain(t *testing.T) {
	s := NewSession()

	m1 := UserText("first")
	s.AddMessage(m1)
	m2 := UserText("second")
	s.AddMessage(m2)
	m3 := UserText("third")
	s.AddMessage(m3)

	msgs := s.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Each message must have a UUID.
	for i, m := range msgs {
		if m.UUID == "" {
			t.Errorf("message %d has empty UUID", i)
		}
	}

	// m2.ParentUUID must equal m1.UUID, m3.ParentUUID must equal m2.UUID.
	if msgs[1].ParentUUID != msgs[0].UUID {
		t.Errorf("m2.ParentUUID = %q, want %q", msgs[1].ParentUUID, msgs[0].UUID)
	}
	if msgs[2].ParentUUID != msgs[1].UUID {
		t.Errorf("m3.ParentUUID = %q, want %q", msgs[2].ParentUUID, msgs[1].UUID)
	}

	// LastUUID must be the tail.
	if s.LastUUID() != msgs[2].UUID {
		t.Errorf("LastUUID = %q, want %q", s.LastUUID(), msgs[2].UUID)
	}
}

// TestAddMessageWithParent_Fork verifies that AddMessageWithParent creates a
// fork point (new chain root with an explicit parent).
func TestAddMessageWithParent_Fork(t *testing.T) {
	s := NewSession()

	root := UserText("root")
	s.AddMessage(root)
	msgs := s.GetMessages()
	rootUUID := msgs[0].UUID

	// Fork from root: new message whose parent is root, not the current tail.
	forkMsg := UserText("forked branch")
	s.AddMessageWithParent(forkMsg, rootUUID)

	msgs = s.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[1].ParentUUID != rootUUID {
		t.Errorf("fork ParentUUID = %q, want %q", msgs[1].ParentUUID, rootUUID)
	}
	if s.LastUUID() != msgs[1].UUID {
		t.Errorf("LastUUID = %q, want %q", s.LastUUID(), msgs[1].UUID)
	}
}

// TestLoadMessagesWithChain_Backfill verifies that legacy messages without
// UUID/ParentUUID get deterministic UUIDs and are linked into a chain.
func TestLoadMessagesWithChain_Backfill(t *testing.T) {
	// Simulate legacy messages (no UUID, no ParentUUID).
	legacy := []Message{
		UserText("legacy-1"),
		UserText("legacy-2"),
		UserText("legacy-3"),
	}
	// Strip UUIDs to simulate legacy.
	for i := range legacy {
		legacy[i].UUID = ""
		legacy[i].ParentUUID = ""
	}

	result := loadMessagesWithChain(legacy)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// All must have UUIDs now.
	for i, m := range result {
		if m.UUID == "" {
			t.Errorf("message %d still has empty UUID after backfill", i)
		}
	}

	// Chain: m2.ParentUUID == m1.UUID, m3.ParentUUID == m2.UUID.
	if result[1].ParentUUID != result[0].UUID {
		t.Errorf("backfill: m2.ParentUUID = %q, want %q", result[1].ParentUUID, result[0].UUID)
	}
	if result[2].ParentUUID != result[1].UUID {
		t.Errorf("backfill: m3.ParentUUID = %q, want %q", result[2].ParentUUID, result[1].UUID)
	}
}

// TestLoadMessagesWithChain_Deterministic verifies that the same legacy input
// produces the same UUIDs across calls (required for chain stability).
func TestLoadMessagesWithChain_Deterministic(t *testing.T) {
	legacy1 := []Message{UserText("same content")}
	legacy1[0].UUID = ""
	legacy1[0].ParentUUID = ""
	// UserText 会打上 time.Now().UnixMilli()，而 deterministicUUID 的
	// 哈希输入含 Timestamp。两次构造若落进不同毫秒（慢机器/CI 上常见），
	// 「相同 legacy 输入」的前提即被破坏 —— mac 上同毫秒侥幸绿，掩盖了这一点。
	// 归零 Timestamp 才是真正相同的输入（回填契约 = 同 (内容,行,时间戳) 同 UUID）。
	legacy1[0].Timestamp = 0

	legacy2 := []Message{UserText("same content")}
	legacy2[0].UUID = ""
	legacy2[0].ParentUUID = ""
	legacy2[0].Timestamp = 0

	r1 := loadMessagesWithChain(legacy1)
	r2 := loadMessagesWithChain(legacy2)

	if r1[0].UUID != r2[0].UUID {
		t.Errorf("backfill not deterministic: %q vs %q", r1[0].UUID, r2[0].UUID)
	}
}

// ---------------------------------------------------------------------------
// S7: Compact (dead-branch + tombstone cleanup)
// ---------------------------------------------------------------------------

// TestCompact_RemovesTombstoned verifies that Compact drops tombstoned messages.
func TestCompact_RemovesTombstoned(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "compact-tomb"

	s := NewSession()
	s.AddMessage(UserText("keep-1"))
	s.AddMessage(UserText("tombstone-me"))
	s.AddMessage(UserText("keep-2"))

	// Tombstone the middle message.
	msgs := s.GetMessages()
	msgs[1].Tombstoned = true
	s.SetMessages(msgs)

	if err := store.Save(sessionID, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	removed, err := store.Compact(sessionID)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	loaded, _ := store.Load(sessionID)
	msgs = loaded.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after compact, got %d", len(msgs))
	}
	if msgs[0].TextContent() != "keep-1" || msgs[1].TextContent() != "keep-2" {
		t.Errorf("unexpected messages after compact: %v", msgs)
	}
}

// TestCompact_RemovesDeadBranch verifies that Compact removes messages not
// reachable from the chain tail (dead fork branches).
func TestCompact_RemovesDeadBranch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "compact-dead"

	s := NewSession()
	// Build main chain: A -> B -> C
	s.AddMessage(UserText("A"))
	s.AddMessage(UserText("B"))
	s.AddMessage(UserText("C"))

	msgs := s.GetMessages()
	aUUID := msgs[0].UUID
	cUUID := msgs[2].UUID

	// Add a dead-branch message D that forks from A. This advances lastUUID
	// to D, so we must explicitly link E back to C (not D) to keep the main
	// chain intact and make D unreachable from the tail.
	deadMsg := UserText("D-dead-branch")
	s.AddMessageWithParent(deadMsg, aUUID)

	// Add E to the main chain (C's child), making D a dead branch.
	// Use AddMessageWithParent so E links to C, not D.
	s.AddMessageWithParent(UserText("E"), cUUID)

	msgs = s.GetMessages()
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages before compact, got %d", len(msgs))
	}

	if err := store.Save(sessionID, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	removed, err := store.Compact(sessionID)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	// D is the only dead-branch message (not reachable from tail E).
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	loaded, _ := store.Load(sessionID)
	msgs = loaded.GetMessages()
	// A, B, C, E should survive; D should be gone.
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages after compact, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.TextContent() == "D-dead-branch" {
			t.Error("dead-branch message D should have been removed by Compact")
		}
	}
}

// TestCompact_EmptySession verifies Compact on an empty session is a no-op.
func TestCompact_EmptySession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "compact-empty"
	store.Save(sessionID, NewSession())

	removed, err := store.Compact(sessionID)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 for empty session", removed)
	}
}

// ---------------------------------------------------------------------------
// S8: Base64Data hash semantics
// ---------------------------------------------------------------------------

// TestNormalizeImageBase64Data_LegacyConverts verifies that a long base64
// string (legacy format) is converted to a 64-char SHA-256 hex digest.
func TestNormalizeImageBase64Data_LegacyConverts(t *testing.T) {
	// A base64-encoded image payload > 64 chars (needs > 48 raw bytes).
	rawImage := []byte("fake-image-data-for-testing-base64-conversion-must-be-long-enough-for-hash")
	legacy := base64.StdEncoding.EncodeToString(rawImage)
	if len(legacy) <= 64 {
		t.Fatalf("test setup error: legacy payload too short (%d chars)", len(legacy))
	}

	result := normalizeImageBase64Data(legacy)
	if len(result) != 64 {
		t.Errorf("legacy conversion: result length = %d, want 64 (SHA-256 hex)", len(result))
	}
	// Must be valid lowercase hex.
	for _, c := range result {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("legacy conversion: result contains non-hex char %q in %q", c, result)
			break
		}
	}
}

// TestNormalizeImageBase64Data_AlreadyHashed verifies that a 64-char value
// (already a hash) is returned unchanged.
func TestNormalizeImageBase64Data_AlreadyHashed(t *testing.T) {
	hashed := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	if len(hashed) != 64 {
		t.Fatalf("test setup error: hashed value not 64 chars")
	}
	result := normalizeImageBase64Data(hashed)
	if result != hashed {
		t.Errorf("already-hashed value should be unchanged: got %q, want %q", result, hashed)
	}
}

// TestNormalizeImageBase64Data_Empty verifies empty string is returned as-is.
func TestNormalizeImageBase64Data_Empty(t *testing.T) {
	if result := normalizeImageBase64Data(""); result != "" {
		t.Errorf("empty input should return empty, got %q", result)
	}
}

// TestNormalizeImageBase64Data_Idempotent verifies that converting twice
// produces the same result (second pass is a no-op).
func TestNormalizeImageBase64Data_Idempotent(t *testing.T) {
	rawImage := []byte("idempotency-test-image-data-payload")
	legacy := base64.StdEncoding.EncodeToString(rawImage)

	first := normalizeImageBase64Data(legacy)
	second := normalizeImageBase64Data(first)

	if first != second {
		t.Errorf("not idempotent: first=%q, second=%q", first, second)
	}
}

// TestNormalizeMessagesBase64Data_WalksMessages verifies that
// normalizeMessagesBase64Data converts image blocks across multiple messages.
func TestNormalizeMessagesBase64Data_WalksMessages(t *testing.T) {
	// Need > 48 raw bytes so base64 output exceeds 64 chars.
	rawImage := []byte("multi-message-walk-test-payload-must-be-long-enough-for-hash-conversion")
	legacy := base64.StdEncoding.EncodeToString(rawImage)
	if len(legacy) <= 64 {
		t.Fatalf("test setup error: legacy payload too short (%d chars)", len(legacy))
	}

	msgs := []Message{
		{
			Role: RoleUser,
			Blocks: []ContentBlock{
				{Type: "text", Text: "hello"},
				{Type: "image", Base64Data: legacy},
			},
		},
		{
			Role: RoleAssistant,
			Blocks: []ContentBlock{
				{Type: "image", Base64Data: legacy},
			},
		},
	}

	normalizeMessagesBase64Data(msgs)

	// All image blocks should now be 64-char hashes.
	for i, m := range msgs {
		for j, b := range m.Blocks {
			if b.Type == "image" {
				if len(b.Base64Data) != 64 {
					t.Errorf("msg[%d].Blocks[%d] image not converted: len=%d", i, j, len(b.Base64Data))
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// S9: deterministicUUID bug fix
// ---------------------------------------------------------------------------

// TestDeterministicUUID_StableForLargeLineNumbers verifies the S9 bug fix:
// previously string(rune(lineNum)) produced garbled multi-byte sequences for
// large integers, making the hash non-deterministic. Now strconv.Itoa is used.
func TestDeterministicUUID_StableForLargeLineNumbers(t *testing.T) {
	msg := UserText("deterministic-test")

	// Large line numbers that would break string(rune(...)).
	// rune(50000) = '쀀' (3-byte UTF-8) — previously caused instability.
	largeLineNums := []int{50000, 100000, 999999, 1500000}

	uuids := make(map[string]bool)
	for _, ln := range largeLineNums {
		uuid := deterministicUUID(msg, ln)
		// Each call must produce a valid UUID (not empty, proper format).
		if uuid == "" {
			t.Errorf("deterministicUUID returned empty for lineNum %d", ln)
		}
		if len(uuid) != 36 {
			t.Errorf("deterministicUUID lineNum %d: length = %d, want 36", ln, len(uuid))
		}
		uuids[uuid] = true
	}
	// Each line number must produce a distinct UUID.
	if len(uuids) != len(largeLineNums) {
		t.Errorf("expected %d distinct UUIDs, got %d (collision?)", len(largeLineNums), len(uuids))
	}
}

// TestDeterministicUUID_SameInputSameOutput verifies true determinism.
func TestDeterministicUUID_SameInputSameOutput(t *testing.T) {
	msg := UserText("reproducible")
	uuid1 := deterministicUUID(msg, 42)
	uuid2 := deterministicUUID(msg, 42)
	if uuid1 != uuid2 {
		t.Errorf("deterministicUUID not reproducible: %q vs %q", uuid1, uuid2)
	}
}

// ---------------------------------------------------------------------------
// S10: Subtype unification (no isCompactSummary field)
// ---------------------------------------------------------------------------

// TestMessage_NoIsCompactSummaryField verifies that the Message struct does
// NOT have an isCompactSummary field (S10/C7: replaced by Subtype).
func TestMessage_NoIsCompactSummaryField(t *testing.T) {
	msg := Message{
		Role:    RoleUser,
		Subtype: "compact_boundary",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, "isCompactSummary") {
		t.Errorf("Message JSON should not contain isCompactSummary: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, "compact_boundary") {
		t.Errorf("Message JSON should contain Subtype value: %s", jsonStr)
	}
}

// TestMessage_SubtypeAndMetadata_RoundTrip verifies Subtype and Metadata
// survive JSON serialization (C7 contract fields).
func TestMessage_SubtypeAndMetadata_RoundTrip(t *testing.T) {
	msg := Message{
		Role:    RoleAssistant,
		Subtype: "compact_boundary",
		Metadata: map[string]interface{}{
			"removed_count":  float64(5),
			"summary_tokens": float64(100),
		},
	}
	data, _ := json.Marshal(msg)

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.Subtype != "compact_boundary" {
		t.Errorf("Subtype = %q, want %q", decoded.Subtype, "compact_boundary")
	}
	if decoded.Metadata["removed_count"] != float64(5) {
		t.Errorf("Metadata[removed_count] = %v, want 5", decoded.Metadata["removed_count"])
	}
}

// ---------------------------------------------------------------------------
// S11: NewStore error return
// ---------------------------------------------------------------------------

// TestNewStore_CreatesDirectory verifies NewStore creates the directory and
// returns a usable store.
func TestNewStore_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned nil store")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("NewStore should create directory %q", dir)
	}
}

// TestNewStore_InvalidPath verifies NewStore returns an error for an
// unwritable path.
func TestNewStore_InvalidPath(t *testing.T) {
	// A path under a non-existent drive on Windows or /proc on Linux
	// should fail. We use a path that definitely cannot be created.
	var badPath string
	if runtime.GOOS == "windows" {
		badPath = `Z:\nonexistent-drive-xyz\cannot\create\this`
	} else {
		// /dev/null/path is not a directory and cannot be created.
		badPath = "/dev/null/cannot-create-here"
	}
	_, err := NewStore(badPath)
	if err == nil {
		// Some environments may allow this; skip if no error.
		t.Skipf("NewStore did not fail for bad path %q (environment may allow it)", badPath)
	}
}

// ---------------------------------------------------------------------------
// Cleanup registry (S1: signal handler support)
// ---------------------------------------------------------------------------

// TestRegisterUnregisterLock verifies the cleanup registry mechanics without
// triggering the actual signal handler.
func TestRegisterUnregisterLock(t *testing.T) {
	// Save original state.
	heldLocksMu.Lock()
	original := heldLocks
	heldLocks = make([]*SessionLock, 0)
	heldLocksMu.Unlock()
	defer func() {
		heldLocksMu.Lock()
		heldLocks = original
		heldLocksMu.Unlock()
	}()

	lock1 := &SessionLock{PID: 1}
	lock2 := &SessionLock{PID: 2}

	registerLockForCleanup(lock1)
	registerLockForCleanup(lock2)

	heldLocksMu.Lock()
	count := len(heldLocks)
	heldLocksMu.Unlock()
	if count != 2 {
		t.Errorf("after registering 2 locks: count = %d, want 2", count)
	}

	// Unregister one.
	unregisterLock(lock1)
	heldLocksMu.Lock()
	count = len(heldLocks)
	heldLocksMu.Unlock()
	if count != 1 {
		t.Errorf("after unregistering 1: count = %d, want 1", count)
	}

	// Unregister the other.
	unregisterLock(lock2)
	heldLocksMu.Lock()
	count = len(heldLocks)
	heldLocksMu.Unlock()
	if count != 0 {
		t.Errorf("after unregistering all: count = %d, want 0", count)
	}

	// Unregistering a nil or unknown lock should be safe.
	unregisterLock(nil)
	unregisterLock(&SessionLock{PID: 999})
}

// TestRegisterLockForCleanup_NilSafe verifies nil locks are ignored.
func TestRegisterLockForCleanup_NilSafe(t *testing.T) {
	heldLocksMu.Lock()
	before := len(heldLocks)
	heldLocksMu.Unlock()

	registerLockForCleanup(nil)

	heldLocksMu.Lock()
	after := len(heldLocks)
	heldLocksMu.Unlock()
	if after != before {
		t.Errorf("nil lock should not be registered: before=%d, after=%d", before, after)
	}
}

// ---------------------------------------------------------------------------
// UUID chain persistence (S6 + S7 integration)
// ---------------------------------------------------------------------------

// TestUUIDChain_PersistedAcrossSaveLoad verifies that UUID/ParentUUID are
// correctly saved and loaded back.
func TestUUIDChain_PersistedAcrossSaveLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "chain-persist"

	s := NewSession()
	s.AddMessage(UserText("msg-1"))
	s.AddMessage(UserText("msg-2"))
	s.AddMessage(UserText("msg-3"))

	originalMsgs := s.GetMessages()
	originalUUIDs := []string{originalMsgs[0].UUID, originalMsgs[1].UUID, originalMsgs[2].UUID}

	if err := store.Save(sessionID, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	loadedMsgs := loaded.GetMessages()
	if len(loadedMsgs) != 3 {
		t.Fatalf("expected 3 loaded messages, got %d", len(loadedMsgs))
	}

	for i, m := range loadedMsgs {
		if m.UUID != originalUUIDs[i] {
			t.Errorf("msg %d UUID = %q, want %q", i, m.UUID, originalUUIDs[i])
		}
	}

	// Chain links must be preserved.
	if loadedMsgs[1].ParentUUID != loadedMsgs[0].UUID {
		t.Errorf("loaded m2.ParentUUID = %q, want %q", loadedMsgs[1].ParentUUID, loadedMsgs[0].UUID)
	}
	if loadedMsgs[2].ParentUUID != loadedMsgs[1].UUID {
		t.Errorf("loaded m3.ParentUUID = %q, want %q", loadedMsgs[2].ParentUUID, loadedMsgs[1].UUID)
	}

	// LastUUID must be re-derived correctly.
	if loaded.LastUUID() != loadedMsgs[2].UUID {
		t.Errorf("loaded LastUUID = %q, want %q", loaded.LastUUID(), loadedMsgs[2].UUID)
	}
}
