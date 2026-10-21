package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rewriteTestSession() *Session {
	sess := NewSession()
	sess.AddMessage(UserText("first"))
	sess.AddMessage(AssistantText("tail", "test-model", nil))
	return sess
}

func rewriteCandidate(messages []Message, count int) *Session {
	candidate := NewSession()
	candidate.Metadata.CompactionCount = count
	candidate.SetMessages(NormalizeMessageChain(messages))
	return candidate
}

func TestStoreRewriteRejectsTranscriptRaces(t *testing.T) {
	t.Run("append after snapshot", func(t *testing.T) {
		store := createTestStore(t)
		live := rewriteTestSession()
		if err := store.Save("append-race", live); err != nil {
			t.Fatal(err)
		}
		expected, _ := store.TranscriptVersion("append-race")
		_, _, revision, _ := live.Snapshot()
		if err := store.Append("append-race", UserText("concurrent append")); err != nil {
			t.Fatal(err)
		}

		err := store.Rewrite("append-race", expected, live, revision, rewriteCandidate([]Message{UserText("replacement")}, 1))
		if !errors.Is(err, ErrSessionRewriteConflict) {
			t.Fatalf("Rewrite error = %v, want conflict", err)
		}
		loaded, err := store.Load("append-race")
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.GetMessages(); len(got) != 3 || got[2].TextContent() != "concurrent append" {
			t.Fatalf("concurrent append was overwritten: %#v", got)
		}
	})

	t.Run("competing rewrite with unchanged tail", func(t *testing.T) {
		store := createTestStore(t)
		live := rewriteTestSession()
		if err := store.Save("rewrite-race", live); err != nil {
			t.Fatal(err)
		}
		expected, _ := store.TranscriptVersion("rewrite-race")
		original, _, revision, _ := live.Snapshot()
		first := CloneMessages(original)
		first[0].Blocks[0].Text = "changed prefix"
		if err := store.Rewrite("rewrite-race", expected, live, revision, rewriteCandidate(first, 1)); err != nil {
			t.Fatalf("first Rewrite failed: %v", err)
		}

		err := store.Rewrite("rewrite-race", expected, live, revision, rewriteCandidate([]Message{UserText("stale")}, 1))
		if !errors.Is(err, ErrSessionRewriteConflict) {
			t.Fatalf("second Rewrite error = %v, want conflict", err)
		}
		loaded, err := store.Load("rewrite-race")
		if err != nil {
			t.Fatal(err)
		}
		got := loaded.GetMessages()
		if len(got) != 2 || got[0].TextContent() != "changed prefix" || got[1].UUID != original[1].UUID {
			t.Fatalf("winning rewrite was overwritten: %#v", got)
		}
	})

	t.Run("file deleted after snapshot", func(t *testing.T) {
		store := createTestStore(t)
		live := rewriteTestSession()
		if err := store.Save("delete-race", live); err != nil {
			t.Fatal(err)
		}
		expected, _ := store.TranscriptVersion("delete-race")
		_, _, revision, _ := live.Snapshot()
		if err := os.Remove(store.sessionPath("delete-race")); err != nil {
			t.Fatal(err)
		}

		err := store.Rewrite("delete-race", expected, live, revision, rewriteCandidate([]Message{UserText("replacement")}, 1))
		if !errors.Is(err, ErrSessionRewriteConflict) {
			t.Fatalf("Rewrite error = %v, want conflict", err)
		}
		if _, err := os.Stat(store.sessionPath("delete-race")); !os.IsNotExist(err) {
			t.Fatalf("conflicting Rewrite recreated deleted transcript: %v", err)
		}
	})

	t.Run("file created after absent snapshot", func(t *testing.T) {
		store := createTestStore(t)
		live := NewSession()
		expected, err := store.TranscriptVersion("create-race")
		if err != nil || expected.Exists {
			t.Fatalf("initial version = %#v, %v", expected, err)
		}
		_, _, revision, _ := live.Snapshot()
		if err := store.Append("create-race", UserText("concurrent create")); err != nil {
			t.Fatal(err)
		}

		err = store.Rewrite("create-race", expected, live, revision, rewriteCandidate([]Message{UserText("replacement")}, 1))
		if !errors.Is(err, ErrSessionRewriteConflict) {
			t.Fatalf("Rewrite error = %v, want conflict", err)
		}
		loaded, err := store.Load("create-race")
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.GetMessages(); len(got) != 1 || got[0].TextContent() != "concurrent create" {
			t.Fatalf("concurrent create was overwritten: %#v", got)
		}
	})
}

func TestRewriteMetadataAndNormalizedChainSurviveResume(t *testing.T) {
	store := createTestStore(t)
	live := NewSession()
	for i := 0; i < 12; i++ {
		live.AddMessage(UserText("message"))
	}
	if err := store.Save("resume", live); err != nil {
		t.Fatal(err)
	}
	expected, _ := store.TranscriptVersion("resume")
	_, _, revision, _ := live.Snapshot()

	broken := live.GetMessages()[1:]
	broken[0].UUID = "duplicate"
	broken[1].UUID = "duplicate"
	broken[2].UUID = ""
	candidate := rewriteCandidate(broken, 1)
	if err := store.Rewrite("resume", expected, live, revision, candidate); err != nil {
		t.Fatalf("Rewrite failed: %v", err)
	}

	data, err := os.ReadFile(store.sessionPath("resume"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 12 {
		t.Fatalf("JSONL records = %d, want 1 metadata + 11 messages", lines)
	}
	loaded, err := store.Load("resume")
	if err != nil {
		t.Fatal(err)
	}
	msgs := loaded.GetMessages()
	if len(msgs) != 11 || loaded.Metadata.CompactionCount != 1 {
		t.Fatalf("resumed messages/count = %d/%d, want 11/1", len(msgs), loaded.Metadata.CompactionCount)
	}
	seen := map[string]bool{}
	parent := ""
	for i, msg := range msgs {
		if msg.UUID == "" || seen[msg.UUID] {
			t.Fatalf("message %d has missing/duplicate UUID %q", i, msg.UUID)
		}
		if msg.ParentUUID != parent {
			t.Fatalf("message %d parent = %q, want %q", i, msg.ParentUUID, parent)
		}
		seen[msg.UUID] = true
		parent = msg.UUID
	}
	removed, err := store.Compact("resume")
	if err != nil || removed != 0 {
		t.Fatalf("Store.Compact removed normalized messages: removed=%d err=%v", removed, err)
	}
}

func TestTranscriptVersionRejectsCorruptJSONL(t *testing.T) {
	store := createTestStore(t)
	live := rewriteTestSession()
	if err := store.Save("corrupt", live); err != nil {
		t.Fatal(err)
	}
	path := store.sessionPath("corrupt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("{not-json}\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TranscriptVersion("corrupt"); err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("TranscriptVersion error = %v, want corrupt line report", err)
	}
}

func TestStoreRewriteRequiresCrossProcessLockInBestEffortMode(t *testing.T) {
	store := createTestStore(t)
	live := rewriteTestSession()
	if err := store.Save("required-lock", live); err != nil {
		t.Fatal(err)
	}
	expected, err := store.TranscriptVersion("required-lock")
	if err != nil {
		t.Fatal(err)
	}
	_, _, revision, _ := live.Snapshot()
	// A directory at the lock-file path makes AcquireLock fail for a reason
	// that is not reported as an active holder. The old best-effort path would
	// silently continue and overwrite the transcript.
	lockPath := filepath.Join(store.dir, "required-lock.lock")
	// ★ L1 修复（flock 迁移）：Save 后锁 backing file 保留（flock 不删文件），
	// 先删掉它才能在同一路径放一个目录来构造「非锁原因失败」。
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "keep"), []byte("non-empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = store.Rewrite("required-lock", expected, live, revision, rewriteCandidate([]Message{UserText("replacement")}, 1))
	if err == nil || !strings.Contains(err.Error(), "required session rewrite lock") {
		t.Fatalf("Rewrite error = %v, want required-lock failure", err)
	}
	loaded, loadErr := store.Load("required-lock")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if got := loaded.GetMessages(); len(got) != 2 || got[0].TextContent() != "first" {
		t.Fatalf("Rewrite without lock changed transcript: %#v", got)
	}
}

func TestSessionDecoderRejectsEmptyAndUnknownRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "empty object", line: "{}\n"},
		{name: "unknown record", line: `{"type":"future_record"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := createTestStore(t)
			if err := os.WriteFile(store.sessionPath("invalid"), []byte(tc.line), 0o600); err != nil {
				t.Fatal(err)
			}
			// 顺序（G-B / A8 改判后必须）：先验 TranscriptVersion 的解码拒绝，
			// 再调 Load。原因：A8 下 Load 是**有权改状态**的（损坏即隔离并移走文件），
			// 隔离后文件不复存在，TranscriptVersion 返回 {Exists:false, nil} 是正确语义
			//（等价于一份全新会话），不能当作"accepted an invalid record"。
			// 两个不变量都被保留，只是各自在能真正被触发的状态下断言。
			if _, err := store.TranscriptVersion("invalid"); err == nil {
				t.Fatal("TranscriptVersion accepted an invalid JSONL record")
			}
			if _, err := store.Load("invalid"); err == nil {
				t.Fatal("Load accepted an invalid JSONL record")
			}
		})
	}
}
