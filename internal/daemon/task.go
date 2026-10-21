package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStatus task status type
type TaskStatus string

const (
	// TaskPending pending
	TaskPending TaskStatus = "pending"
	// TaskRunning running
	TaskRunning TaskStatus = "running"
	// TaskCompleted completed
	TaskCompleted TaskStatus = "completed"
	// TaskFailed failed
	TaskFailed TaskStatus = "failed"
	// TaskCancelled cancelled
	TaskCancelled TaskStatus = "cancelled"
)

// TaskType task type
type TaskType string

const (
	// TaskDream AI generation task
	TaskDream TaskType = "dream"
	// TaskShell local command task
	TaskShell TaskType = "shell"
	// TaskAnalysis code analysis task
	TaskAnalysis TaskType = "analysis"
)

// TaskPriority task priority
type TaskPriority int

const (
	// PriorityLow low priority
	PriorityLow TaskPriority = iota
	// PriorityNormal normal priority
	PriorityNormal
	// PriorityHigh high priority
	PriorityHigh
	// PriorityCritical critical priority
	PriorityCritical
)

// Task background task structure
type Task struct {
	ID            string       `json:"id"`                       // Unique identifier, format "task-{timestamp}-{random}"
	Prompt        string       `json:"prompt"`                   // User prompt
	Status        TaskStatus   `json:"status"`                   // Task status
	WorkDir       string       `json:"work_dir"`                 // Working directory
	Result        string       `json:"result"`                   // Execution result
	ExitCode      int          `json:"exit_code"`                // Exit code
	CreatedAt     time.Time    `json:"created_at"`               // Creation time
	StartedAt     *time.Time   `json:"started_at"`               // Start time
	CompletedAt   *time.Time   `json:"completed_at"`             // Completion time
	Type          TaskType     `json:"type"`                     // Task type
	Priority      TaskPriority `json:"priority"`                 // Task priority
	Progress      int          `json:"progress"`                 // Progress percentage (0-100)
	PartialOutput string       `json:"partial_output,omitempty"` // Partial output
}

// TaskQueue manages background tasks
// - Priority queue (higher priority first, FIFO within same priority)
// - Persisted transactionally to SQLite
// - Thread-safe
type TaskQueue struct {
	mu                    sync.RWMutex
	tasks                 []Task
	storePath             string
	store                 *taskStore
	synced                bool
	storeErr              string
	terminalCount         int
	lastRetention         time.Time
	terminalStoreFailures map[string]struct{}
	notifyCh              chan struct{}
}

// NewTaskQueue creates task queue
// storePath: persistence storage file path
func NewTaskQueue(storePath string) *TaskQueue {
	return &TaskQueue{
		tasks:                 make([]Task, 0),
		storePath:             storePath,
		store:                 newTaskStore(storePath),
		terminalStoreFailures: make(map[string]struct{}),
		notifyCh:              make(chan struct{}, 1),
	}
}

// generateTaskID generates unique task ID, format "task-{timestamp}-{random}"
var taskIDFallback atomic.Uint64

func generateTaskID() string {
	timestamp := time.Now().Format("20060102150405")
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		fallback := uint64(time.Now().UnixNano()) ^ taskIDFallback.Add(1)
		return fmt.Sprintf("task-%s-%08x", timestamp, uint32(fallback))
	}
	random := hex.EncodeToString(b)
	return fmt.Sprintf("task-%s-%s", timestamp, random)
}

// Add adds task to queue
// prompt: user prompt
// workDir: working directory
// taskType: task type
// priority: task priority
func (q *TaskQueue) Add(prompt, workDir string, taskType TaskType, priority TaskPriority) *Task {
	q.mu.Lock()
	defer q.mu.Unlock()

	task := newTask(prompt, workDir, taskType, priority)
	q.tasks = append(q.tasks, task)
	q.synced = false
	result := task
	return &result
}

func newTask(prompt, workDir string, taskType TaskType, priority TaskPriority) Task {
	return Task{
		ID:        generateTaskID(),
		Prompt:    prompt,
		Status:    TaskPending,
		WorkDir:   workDir,
		CreatedAt: time.Now(),
		Type:      taskType,
		Priority:  priority,
	}
}

// Get gets task by ID
func (q *TaskQueue) Get(id string) *Task {
	q.mu.RLock()
	defer q.mu.RUnlock()

	for i := range q.tasks {
		if q.tasks[i].ID == id {
			result := q.tasks[i]
			return &result
		}
	}
	return nil
}

// List lists all tasks
func (q *TaskQueue) List() []Task {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]Task, len(q.tasks))
	copy(result, q.tasks)
	return result
}

// Next gets next pending task (highest priority first)
func (q *TaskQueue) Next() *Task {
	q.mu.Lock()
	defer q.mu.Unlock()

	bestIndex := -1
	for i := range q.tasks {
		if q.tasks[i].Status == TaskPending {
			if bestIndex == -1 || q.tasks[i].Priority > q.tasks[bestIndex].Priority {
				bestIndex = i
			}
		}
	}
	if bestIndex == -1 {
		return nil
	}
	result := q.tasks[bestIndex]
	return &result
}

// ClaimNext atomically selects the highest-priority pending task and marks it
// running. Returning a snapshot prevents callers from retaining pointers into
// the queue's backing slice, which may move when new tasks are appended.
func (q *TaskQueue) ClaimNext() *Task {
	q.mu.Lock()
	defer q.mu.Unlock()

	bestIndex := q.bestPendingIndexLocked()
	if bestIndex == -1 {
		return nil
	}

	now := time.Now()
	q.tasks[bestIndex].Status = TaskRunning
	q.tasks[bestIndex].StartedAt = &now
	q.tasks[bestIndex].CompletedAt = nil
	q.tasks[bestIndex].Result = ""
	q.synced = false
	result := q.tasks[bestIndex]
	return &result
}

// ClaimNextPersisted claims the next task only when the running transition is
// durably stored. This prevents a crash from replaying a task whose process was
// already started while durable storage still described it as pending.
func (q *TaskQueue) ClaimNextPersisted() (*Task, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	bestIndex := q.bestPendingIndexLocked()
	if bestIndex == -1 {
		return nil, nil
	}

	previous := q.tasks[bestIndex]
	now := time.Now()
	next := previous
	next.Status = TaskRunning
	next.StartedAt = &now
	next.CompletedAt = nil
	next.Result = ""
	if q.synced {
		if err := q.store.update(context.Background(), next, previous.Status); err != nil {
			q.recordStoreErrorLocked(err)
			return nil, fmt.Errorf("persist claimed task %s: %w", previous.ID, err)
		}
		q.tasks[bestIndex] = next
	} else {
		q.tasks[bestIndex] = next
		if err := q.saveLocked(); err != nil {
			q.tasks[bestIndex] = previous
			q.synced = false
			return nil, fmt.Errorf("persist claimed task %s: %w", previous.ID, err)
		}
	}
	q.clearStoreErrorLocked()

	result := q.tasks[bestIndex]
	return &result, nil
}

func (q *TaskQueue) bestPendingIndexLocked() int {
	bestIndex := -1
	for i := range q.tasks {
		if q.tasks[i].Status != TaskPending {
			continue
		}
		if bestIndex == -1 || q.tasks[i].Priority > q.tasks[bestIndex].Priority {
			bestIndex = i
		}
	}
	return bestIndex
}

// UpdateStatus updates task status
// id: task ID
// status: new status
// result: execution result
func (q *TaskQueue) UpdateStatus(id string, status TaskStatus, result string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.tasks {
		if q.tasks[i].ID == id {
			q.tasks[i].Status = status
			q.tasks[i].Result = result
			now := time.Now()

			if status == TaskRunning && q.tasks[i].StartedAt == nil {
				q.tasks[i].StartedAt = &now
			}
			if status == TaskCompleted || status == TaskFailed || status == TaskCancelled {
				q.tasks[i].CompletedAt = &now
			}
			q.synced = false
			return nil
		}
	}
	return fmt.Errorf("task does not exist: %s", id)
}

// Cancel cancels a task
func (q *TaskQueue) Cancel(id string) error {
	_, err := q.CancelWithPrevious(id)
	return err
}

// CancelWithPrevious cancels a pending or running task and returns its previous
// state. The caller uses the previous state to terminate an active worker.
func (q *TaskQueue) CancelWithPrevious(id string) (TaskStatus, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.tasks {
		if q.tasks[i].ID == id {
			// Can only cancel pending or running tasks
			if q.tasks[i].Status != TaskPending && q.tasks[i].Status != TaskRunning {
				return q.tasks[i].Status, fmt.Errorf("task %s status is %s, cannot cancel", id, q.tasks[i].Status)
			}
			previous := q.tasks[i].Status
			now := time.Now()
			q.tasks[i].Status = TaskCancelled
			q.tasks[i].Result = "Task cancelled"
			q.tasks[i].ExitCode = -1
			q.tasks[i].CompletedAt = &now
			q.synced = false
			return previous, nil
		}
	}
	return "", fmt.Errorf("task does not exist: %s", id)
}

// CancelPersisted commits cancellation before it is acknowledged or an active
// worker is terminated. On write failure the prior state is restored, so a
// pending task cannot reappear after restart despite having reported success.
func (q *TaskQueue) CancelPersisted(id string) (TaskStatus, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.tasks {
		if q.tasks[i].ID != id {
			continue
		}
		if q.tasks[i].Status != TaskPending && q.tasks[i].Status != TaskRunning {
			return q.tasks[i].Status, fmt.Errorf("task %s status is %s, cannot cancel", id, q.tasks[i].Status)
		}

		previous := q.tasks[i]
		now := time.Now()
		next := previous
		next.Status = TaskCancelled
		next.Result = "Task cancelled"
		next.ExitCode = -1
		next.CompletedAt = &now
		if q.synced {
			if err := q.store.update(context.Background(), next, previous.Status); err != nil {
				q.recordStoreErrorLocked(err)
				return previous.Status, fmt.Errorf("persist cancelled task %s: %w", id, err)
			}
			q.tasks[i] = next
		} else {
			q.tasks[i] = next
			if err := q.saveLocked(); err != nil {
				q.tasks[i] = previous
				q.synced = false
				return previous.Status, fmt.Errorf("persist cancelled task %s: %w", id, err)
			}
		}
		delete(q.terminalStoreFailures, id)
		q.clearStoreErrorLocked()
		q.maintainRetentionLocked()
		return previous.Status, nil
	}
	return "", fmt.Errorf("task does not exist: %s", id)
}

// FinishRunning moves a running task to a terminal state. If a concurrent
// cancellation already won, the cancelled state is preserved.
func (q *TaskQueue) FinishRunning(id string, status TaskStatus, result string, exitCode int) (bool, error) {
	if status != TaskCompleted && status != TaskFailed && status != TaskCancelled {
		return false, fmt.Errorf("invalid terminal task status: %s", status)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.tasks {
		if q.tasks[i].ID != id {
			continue
		}
		if q.tasks[i].Status != TaskRunning {
			return false, nil
		}
		previous := q.tasks[i]
		now := time.Now()
		next := previous
		next.Status = status
		next.Result = result
		next.ExitCode = exitCode
		next.CompletedAt = &now
		if q.synced {
			if err := q.store.update(context.Background(), next, previous.Status); err != nil {
				q.recordStoreErrorLocked(err)
				q.recordTerminalStoreFailureLocked(id)
				return false, fmt.Errorf("persist terminal task %s: %w", id, err)
			}
			q.tasks[i] = next
		} else {
			q.tasks[i] = next
			if err := q.saveLocked(); err != nil {
				q.tasks[i] = previous
				q.synced = false
				q.recordTerminalStoreFailureLocked(id)
				return false, fmt.Errorf("persist terminal task %s: %w", id, err)
			}
		}
		delete(q.terminalStoreFailures, id)
		q.maintainRetentionLocked()
		return true, nil
	}
	return false, fmt.Errorf("task does not exist: %s", id)
}

// RequeueRunning returns a claimed task to the pending queue after a worker
// could not be started. It does not resurrect a concurrently cancelled task.
func (q *TaskQueue) RequeueRunning(id string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.tasks {
		if q.tasks[i].ID != id {
			continue
		}
		if q.tasks[i].Status != TaskRunning {
			return false, nil
		}
		previous := q.tasks[i]
		next := previous
		next.Status = TaskPending
		next.StartedAt = nil
		next.CompletedAt = nil
		next.Result = ""
		next.ExitCode = 0
		if q.synced {
			if err := q.store.update(context.Background(), next, previous.Status); err != nil {
				q.recordStoreErrorLocked(err)
				return false, fmt.Errorf("persist requeued task %s: %w", id, err)
			}
			q.tasks[i] = next
		} else {
			q.tasks[i] = next
			if err := q.saveLocked(); err != nil {
				q.tasks[i] = previous
				q.synced = false
				return false, fmt.Errorf("persist requeued task %s: %w", id, err)
			}
		}
		delete(q.terminalStoreFailures, id)
		q.clearStoreErrorLocked()
		return true, nil
	}
	return false, fmt.Errorf("task does not exist: %s", id)
}

// PendingCount returns pending task count
func (q *TaskQueue) PendingCount() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	count := 0
	for _, t := range q.tasks {
		if t.Status == TaskPending {
			count++
		}
	}
	return count
}

// RunningCount returns running task count
func (q *TaskQueue) RunningCount() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	count := 0
	for _, t := range q.tasks {
		if t.Status == TaskRunning {
			count++
		}
	}
	return count
}

// Save persists the complete in-memory snapshot in one SQLite transaction.
func (q *TaskQueue) Save() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.saveLocked()
}

func (q *TaskQueue) saveLocked() error {
	if err := q.store.replaceAll(context.Background(), q.tasks); err != nil {
		q.recordStoreErrorLocked(err)
		return fmt.Errorf("write task database: %w", err)
	}
	q.synced = true
	q.clearStoreErrorLocked()
	q.terminalCount = countTerminalTasks(q.tasks)
	return nil
}

// Load opens/migrates the database, applies recovery and retention, then loads
// an in-memory scheduling snapshot.
func (q *TaskQueue) Load() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	ctx := context.Background()
	if err := q.store.open(ctx); err != nil {
		q.recordStoreErrorLocked(err)
		return fmt.Errorf("open task database: %w", err)
	}
	now := time.Now()
	if err := q.store.recoverInterrupted(ctx, now); err != nil {
		q.recordStoreErrorLocked(err)
		return fmt.Errorf("recover interrupted tasks: %w", err)
	}
	if err := q.store.applyRetention(ctx, now); err != nil {
		q.recordStoreErrorLocked(err)
		return fmt.Errorf("apply task retention: %w", err)
	}
	tasks, err := q.store.load(ctx)
	if err != nil {
		q.recordStoreErrorLocked(err)
		return fmt.Errorf("load tasks: %w", err)
	}
	q.tasks = tasks
	q.synced = true
	q.storeErr = ""
	q.terminalStoreFailures = make(map[string]struct{})
	q.terminalCount = countTerminalTasks(tasks)
	q.lastRetention = now
	return nil
}

// notifyCh task notification channel
var notifyCh = make(chan struct{}, 1)

// NotifyChannel returns task notification channel
func NotifyChannel() <-chan struct{} {
	return notifyCh
}

// AddWithNotify adds task and notifies scheduler
func (q *TaskQueue) AddWithNotify(prompt, workDir string, taskType TaskType, priority TaskPriority) *Task {
	task := q.Add(prompt, workDir, taskType, priority)
	// Non-blocking notification
	select {
	case notifyCh <- struct{}{}:
	default:
	}
	q.notifyScheduler()
	return task
}

// AddPersistedWithNotify acknowledges a task only after SQLite commits it. The
// scheduler is notified after the durable write, so it cannot start an
// unpersisted task.
func (q *TaskQueue) AddPersistedWithNotify(prompt, workDir string, taskType TaskType, priority TaskPriority) (*Task, error) {
	q.mu.Lock()
	task := newTask(prompt, workDir, taskType, priority)
	if q.synced {
		if err := q.store.insert(context.Background(), task); err != nil {
			q.recordStoreErrorLocked(err)
			q.mu.Unlock()
			return nil, fmt.Errorf("persist new task %s: %w", task.ID, err)
		}
		q.tasks = append(q.tasks, task)
	} else {
		q.tasks = append(q.tasks, task)
		if err := q.saveLocked(); err != nil {
			q.tasks = q.tasks[:len(q.tasks)-1]
			q.synced = false
			q.mu.Unlock()
			return nil, fmt.Errorf("persist new task %s: %w", task.ID, err)
		}
	}
	q.clearStoreErrorLocked()
	q.mu.Unlock()

	q.notifyScheduler()
	result := task
	return &result, nil
}

func (q *TaskQueue) notifyScheduler() {
	select {
	case q.notifyCh <- struct{}{}:
	default:
	}
}

// Notifications returns this queue's scheduler notification channel. A
// per-queue channel prevents one daemon instance from consuming another
// instance's wake-up signal in tests or embedded use.
func (q *TaskQueue) Notifications() <-chan struct{} {
	return q.notifyCh
}

// UpdateProgress updates task progress
func (q *TaskQueue) UpdateProgress(id string, progress int, partialOutput string) error {
	if progress < 0 || progress > 100 {
		return fmt.Errorf("task progress must be between 0 and 100, got %d", progress)
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.tasks {
		if q.tasks[i].ID == id {
			q.tasks[i].Progress = progress
			q.tasks[i].PartialOutput = partialOutput
			q.synced = false
			return nil
		}
	}
	return fmt.Errorf("task does not exist: %s", id)
}

// ListPage returns newest-first cursor pagination backed by SQLite.
func (q *TaskQueue) ListPage(cursor string, limit int) (TaskPage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.synced {
		if err := q.saveLocked(); err != nil {
			return TaskPage{}, err
		}
	}
	page, err := q.store.listPage(context.Background(), cursor, limit)
	if err != nil {
		q.recordStoreErrorLocked(err)
	} else {
		q.clearStoreErrorLocked()
	}
	return page, err
}

func (q *TaskQueue) StorageHealth() (string, string) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.storeErr != "" {
		return "degraded", q.storeErr
	}
	if !q.synced {
		return "dirty", "in-memory task changes are not yet persisted"
	}
	return "healthy", ""
}

func (q *TaskQueue) recordStoreErrorLocked(err error) {
	if err != nil {
		q.storeErr = err.Error()
	}
}

func (q *TaskQueue) recordTerminalStoreFailureLocked(id string) {
	if q.terminalStoreFailures == nil {
		q.terminalStoreFailures = make(map[string]struct{})
	}
	q.terminalStoreFailures[id] = struct{}{}
}

func (q *TaskQueue) clearStoreErrorLocked() {
	if len(q.terminalStoreFailures) == 0 {
		q.storeErr = ""
	}
}

func (q *TaskQueue) maintainRetentionLocked() {
	q.terminalCount++
	if q.terminalCount <= maxRetainedTerminalTasks && time.Since(q.lastRetention) < 24*time.Hour {
		return
	}
	ctx := context.Background()
	if err := q.store.applyRetention(ctx, time.Now()); err != nil {
		q.recordStoreErrorLocked(err)
		return
	}
	tasks, err := q.store.load(ctx)
	if err != nil {
		q.recordStoreErrorLocked(err)
		return
	}
	q.tasks = tasks
	q.synced = true
	q.clearStoreErrorLocked()
	q.terminalCount = countTerminalTasks(tasks)
	q.lastRetention = time.Now()
}

func countTerminalTasks(tasks []Task) int {
	count := 0
	for i := range tasks {
		if tasks[i].Status == TaskCompleted || tasks[i].Status == TaskFailed || tasks[i].Status == TaskCancelled {
			count++
		}
	}
	return count
}

func (q *TaskQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.store.close()
}
