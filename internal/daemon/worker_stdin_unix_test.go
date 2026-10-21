//go:build !windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerSendsPromptThroughStdinNotArgv(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")
	scriptPath := filepath.Join(dir, "fake-whale")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WHALE_TEST_ARGS\"\ncat > \"$WHALE_TEST_STDIN\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHALE_TEST_ARGS", argsPath)
	t.Setenv("WHALE_TEST_STDIN", stdinPath)

	secretPrompt := "private-prompt-should-not-be-in-argv"
	queue := NewTaskQueue(filepath.Join(dir, "tasks.json"))
	worker := NewWorker(1, scriptPath, 15*time.Second)
	worker.queue = queue
	if err := worker.Start(context.Background(), queue.Add(secretPrompt, dir, TaskDream, PriorityNormal)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for worker.IsBusy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if worker.IsBusy() {
		worker.Stop()
		cleanupDeadline := time.Now().Add(2 * time.Second)
		for worker.IsBusy() && time.Now().Before(cleanupDeadline) {
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("worker did not complete stdin handoff within 10s")
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), secretPrompt) || strings.TrimSpace(string(args)) != "--ci-prompt-stdin" {
		t.Fatalf("unexpected worker argv: %q", args)
	}
	if string(stdin) != secretPrompt {
		t.Fatalf("worker stdin = %q, want prompt", stdin)
	}
}
