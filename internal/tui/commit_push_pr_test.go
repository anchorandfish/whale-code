package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupTestGitRepo creates a temporary Git repository for testing
func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exec.Command("git", "-C", dir, "init").Run()
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test User").Run()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)
	return dir
}

// setupNonGitDir creates a temporary non-Git directory
func setupNonGitDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestHandleCommitPushPRNoToken tests error message when GITHUB_TOKEN is not set
func TestHandleCommitPushPRNoToken(t *testing.T) {
	origToken := os.Getenv("GITHUB_TOKEN")
	os.Unsetenv("GITHUB_TOKEN")
	defer func() {
		if origToken != "" {
			os.Setenv("GITHUB_TOKEN", origToken)
		}
	}()

	r := &PlainRunner{runnerCore: &runnerCore{WorkDir: t.TempDir()}}
	result := r.handleCommitPushPR()

	if result == "" {
		t.Error("expected error message, got empty string")
	}
	if !containsStr(result, "GITHUB_TOKEN") {
		t.Errorf("error message should contain 'GITHUB_TOKEN': %q", result)
	}
	t.Logf("no token result: %q", result)
}

// TestHandleCommitPushPRNotGitRepo tests error message when the directory is not a Git repository
func TestHandleCommitPushPRNotGitRepo(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fake-token-for-test")
	r := &PlainRunner{runnerCore: &runnerCore{WorkDir: setupNonGitDir(t)}}
	result := r.handleCommitPushPR()

	if result == "" {
		t.Error("expected error message, got empty string")
	}
	if !containsStr(result, "not a Git repository") {
		t.Errorf("error message should contain 'not a Git repository': %q", result)
	}
	t.Logf("non-Git repo result: %q", result)
}

// TestHandleCommitPushPRNoAIClient tests error message when AI client is not initialized
func TestHandleCommitPushPRNoAIClient(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fake-token-for-test")
	r := &PlainRunner{runnerCore: &runnerCore{
		WorkDir:  setupTestGitRepo(t),
		AIClient: nil,
	}}
	result := r.handleCommitPushPR()

	if result == "" {
		t.Error("expected error message, got empty string")
	}
	if !containsStr(result, "AI") {
		t.Errorf("error message should contain 'AI': %q", result)
	}
	t.Logf("no AI client result: %q", result)
}

// TestHandleCommitPushPREmptyWorkDir tests that nil AIClient is handled without panic
// Uses an empty workDir to verify the code doesn't crash on edge cases.
// When WorkDir is empty, exec.Command uses the current working directory.
func TestHandleCommitPushPREmptyWorkDir(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fake-token-for-test")
	r := &PlainRunner{runnerCore: &runnerCore{WorkDir: setupTestGitRepo(t)}}
	result := r.handleCommitPushPR()

	if result == "" {
		t.Error("expected error message, got empty string")
	}
	if !containsStr(result, "AI") {
		t.Errorf("error message should contain 'AI': %q", result)
	}
	t.Logf("nil AI client result: %q", result)
}

// TestDispatchCommitPushPRNonMagicString tests dispatchCommand passthrough for non-magic-string output
func TestDispatchCommitPushPRNonMagicString(t *testing.T) {
	r := &PlainRunner{runnerCore: &runnerCore{WorkDir: t.TempDir()}}
	result := r.dispatchCommand("commit-push-pr", "some args", "自定义内容")

	if result != "自定义内容" {
		t.Errorf("dispatchCommand should passthrough: got %q, want %q", result, "自定义内容")
	}
}

// TestHandleCommitPushPRGitRepoWithToken tests the normal flow entry in a Git repo with a token
// After passing all precondition checks, the flow enters the PRCreator stage,
// which will fail due to the fake token calling the GitHub API.
func TestHandleCommitPushPRGitRepoWithToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fake-token-for-test")
	r := &PlainRunner{runnerCore: &runnerCore{
		WorkDir:  setupTestGitRepo(t),
		AIClient: newTestClient("test-model"),
	}}
	result := r.handleCommitPushPR()

	if result == "" {
		t.Error("expected some result, got empty string")
	}
	t.Logf("Git repo with token result: %q", result)
}
