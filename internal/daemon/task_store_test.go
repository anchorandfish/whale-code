package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskStoreMigratesLegacyJSONWithBackup(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "tasks.json")
	legacyTask := newTask("legacy prompt", dir, TaskDream, PriorityHigh)
	data, err := json.Marshal([]Task{legacyTask})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	queue := NewTaskQueue(legacyPath)
	t.Cleanup(func() { _ = queue.Close() })
	if err := queue.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if task := queue.Get(legacyTask.ID); task == nil || task.Prompt != legacyTask.Prompt {
		t.Fatalf("legacy task was not migrated: %#v", task)
	}
	for _, path := range []string{filepath.Join(dir, "tasks.db"), legacyPath + ".pre-sqlite.bak"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migration artifact %s: %v", path, err)
		}
	}
	var marker map[string]interface{}
	markerData, err := os.ReadFile(legacyPath)
	if err != nil || json.Unmarshal(markerData, &marker) != nil || marker["migrated_to"] != "tasks.db" {
		t.Fatalf("invalid migration marker: %s (err=%v)", markerData, err)
	}
}

func TestTaskStoreCorruptDatabaseFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.db"), []byte("not-a-sqlite-database"), 0600); err != nil {
		t.Fatal(err)
	}
	queue := NewTaskQueue(filepath.Join(dir, "tasks.json"))
	t.Cleanup(func() { _ = queue.Close() })
	if err := queue.Load(); err == nil {
		t.Fatal("corrupt SQLite database was silently accepted")
	}
	if tasks := queue.List(); len(tasks) != 0 {
		t.Fatalf("corrupt database populated %d tasks", len(tasks))
	}
}

func TestTaskStoreCursorPaginationHasNoDuplicates(t *testing.T) {
	queue := NewTaskQueue(filepath.Join(t.TempDir(), "tasks.json"))
	t.Cleanup(func() { _ = queue.Close() })
	if err := queue.Load(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := queue.AddPersistedWithNotify(fmt.Sprintf("task-%d", i), "", TaskDream, PriorityNormal); err != nil {
			t.Fatal(err)
		}
	}
	first, err := queue.ListPage("", 2)
	if err != nil || len(first.Tasks) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %#v, err=%v", first, err)
	}
	second, err := queue.ListPage(first.NextCursor, 2)
	if err != nil || len(second.Tasks) != 2 {
		t.Fatalf("second page = %#v, err=%v", second, err)
	}
	seen := map[string]bool{}
	for _, task := range append(first.Tasks, second.Tasks...) {
		if seen[task.ID] {
			t.Fatalf("task %s appeared on multiple pages", task.ID)
		}
		seen[task.ID] = true
	}
}

func TestTaskStoreRetentionNeverDeletesActiveTasks(t *testing.T) {
	store := newTaskStore(filepath.Join(t.TempDir(), "tasks.json"))
	t.Cleanup(func() { _ = store.close() })
	now := time.Now().UTC()
	tasks := make([]Task, 0, maxRetainedTerminalTasks+3)
	for i := 0; i < maxRetainedTerminalTasks+2; i++ {
		completed := now.Add(-time.Duration(i) * time.Second)
		tasks = append(tasks, Task{
			ID: fmt.Sprintf("terminal-%05d", i), Prompt: "done", Status: TaskCompleted,
			CreatedAt: completed, CompletedAt: &completed, Type: TaskDream,
		})
	}
	oldCompleted := now.Add(-60 * 24 * time.Hour)
	tasks = append(tasks, Task{ID: "expired-terminal", Prompt: "old", Status: TaskFailed, CreatedAt: oldCompleted, CompletedAt: &oldCompleted, Type: TaskDream})
	tasks = append(tasks, Task{ID: "old-active", Prompt: "keep", Status: TaskPending, CreatedAt: oldCompleted, Type: TaskDream})
	if err := store.replaceAll(t.Context(), tasks); err != nil {
		t.Fatal(err)
	}
	if err := store.applyRetention(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	retained, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != maxRetainedTerminalTasks+1 {
		t.Fatalf("retained %d tasks, want %d", len(retained), maxRetainedTerminalTasks+1)
	}
	foundActive := false
	for _, task := range retained {
		if task.ID == "expired-terminal" {
			t.Fatal("expired terminal task was retained")
		}
		if task.ID == "old-active" {
			foundActive = true
		}
	}
	if !foundActive {
		t.Fatal("retention deleted an active task")
	}
}

func TestFinishRunningDoesNotAcknowledgeStorageFailure(t *testing.T) {
	queue := NewTaskQueue(t.TempDir())
	task := queue.Add("must remain running", "", TaskDream, PriorityNormal)
	if claimed := queue.ClaimNext(); claimed == nil {
		t.Fatal("task was not claimed")
	}
	updated, err := queue.FinishRunning(task.ID, TaskCompleted, "done", 0)
	if err == nil || updated {
		t.Fatalf("FinishRunning = (%v, %v), want (false, error)", updated, err)
	}
	if current := queue.Get(task.ID); current == nil || current.Status != TaskRunning {
		t.Fatalf("failed terminal commit changed in-memory state: %#v", current)
	}
	if health, _ := queue.StorageHealth(); health != "degraded" {
		t.Fatalf("storage health = %q, want degraded", health)
	}
}
