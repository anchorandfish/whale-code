package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestResumeLastSession_UnorderedSessions tests that resumeLastSession
// correctly picks the latest session even when sessions are not sorted
func TestResumeLastSession_UnorderedSessions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "whale-resume-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	now := time.Now()

	// Create 3 session files with different modification times
	// The latest one is in the middle position alphabetically
	sessions := []struct {
		id       string
		modTime  time.Time
		messages []session.Message
	}{
		{
			id:      "session_old",
			modTime: now.Add(-2 * time.Hour),
			messages: []session.Message{
				session.UserText("old message"),
			},
		},
		{
			id:      "session_latest",
			modTime: now,
			messages: []session.Message{
				session.UserText("latest message"),
			},
		},
		{
			id:      "session_middle",
			modTime: now.Add(-1 * time.Hour),
			messages: []session.Message{
				session.UserText("middle message"),
			},
		},
	}

	for _, s := range sessions {
		path := filepath.Join(tmpDir, s.id+".jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("failed to create session file: %v", err)
		}
		for _, msg := range s.messages {
			data, _ := json.Marshal(msg)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		f.Close()
		// Set modification time
		if err := os.Chtimes(path, s.modTime, s.modTime); err != nil {
			t.Fatalf("failed to set mod time: %v", err)
		}
	}

	runner, _ := newTestBridgeRunner()
	store, err := session.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}
	runner.Store = store

	runner.resumeLastSession()

	// Verify the latest session was loaded
	if runner.SessionName != "session_latest" {
		t.Errorf("expected session_latest, got %q", runner.SessionName)
	}
}

// TestResumeLastSession_SingleSession tests that resumeLastSession
// correctly resumes when there is only one session
func TestResumeLastSession_SingleSession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "whale-resume-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	path := filepath.Join(tmpDir, "session_only.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create session file: %v", err)
	}
	msg := session.UserText("only message")
	data, _ := json.Marshal(msg)
	f.Write(data)
	f.Write([]byte("\n"))
	f.Close()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("failed to set mod time: %v", err)
	}

	runner, _ := newTestBridgeRunner()
	store, err := session.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}
	runner.Store = store

	runner.resumeLastSession()

	if runner.SessionName != "session_only" {
		t.Errorf("expected session_only, got %q", runner.SessionName)
	}
}

// TestResumeLastSession_SameUpdatedAt tests that resumeLastSession
// handles sessions with the same UpdatedAt without crashing
func TestResumeLastSession_SameUpdatedAt(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "whale-resume-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sameTime := time.Now()

	// Create 2 sessions with the same modification time
	for _, id := range []string{"session_a", "session_b"} {
		path := filepath.Join(tmpDir, id+".jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("failed to create session file: %v", err)
		}
		msg := session.UserText("message for " + id)
		data, _ := json.Marshal(msg)
		f.Write(data)
		f.Write([]byte("\n"))
		f.Close()
		if err := os.Chtimes(path, sameTime, sameTime); err != nil {
			t.Fatalf("failed to set mod time: %v", err)
		}
	}

	runner, _ := newTestBridgeRunner()
	store, err := session.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("session.NewStore failed: %v", err)
	}
	runner.Store = store

	// Should not panic and should resume one of them
	runner.resumeLastSession()

	if runner.SessionName != "session_a" && runner.SessionName != "session_b" {
		t.Errorf("expected session_a or session_b, got %q", runner.SessionName)
	}
}
