package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// WorkerType workerstype
type WorkerType string

const (
	// WorkerTypeCode code generationworkers
	WorkerTypeCode WorkerType = "code"
	// WorkerTypeSearch searchworkers
	WorkerTypeSearch WorkerType = "search"
	// WorkerTypeAnalysis analysis worker
	WorkerTypeAnalysis WorkerType = "analysis"
	// WorkerTypeShell Shell command worker
	WorkerTypeShell WorkerType = "shell"
)

// WorkerCapabilities worker capability description
type WorkerCapabilities struct {
	Type        WorkerType `json:"type"`
	CanExecute  bool       `json:"can_execute"`  // Can execute commands
	CanSearch   bool       `json:"can_search"`   // Can search code
	CanAnalyze  bool       `json:"can_analyze"`  // Can analyze code
	CanGenerate bool       `json:"can_generate"` // Can generate code
	MaxTokens   int        `json:"max_tokens"`   // Max output token count
}

// RegisteredWorker registered worker
type RegisteredWorker struct {
	ID            string             `json:"id"`
	Type          WorkerType         `json:"type"`
	Capabilities  WorkerCapabilities `json:"capabilities"`
	LastHeartbeat time.Time          `json:"last_heartbeat"`
	Status        string             `json:"status"` // "active" | "idle" | "stale"
	CurrentTask   string             `json:"current_task,omitempty"`
}

// WorkerRegistry worker registry
type WorkerRegistry struct {
	mu                  sync.RWMutex
	workers             map[string]*RegisteredWorker
	heartbeatTimeout    time.Duration
	failCount           map[string]int
	healthCheckInterval time.Duration
	maxFailCount        int
	hcCtx               context.Context
	hcCancel            context.CancelFunc
	hcWG                sync.WaitGroup
	completedTasks      map[string]time.Time // task ID -> completion time
	completedTaskTTL    time.Duration        // how long to keep completed tasks
}

// NewWorkerRegistry creates worker registry
func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers:             make(map[string]*RegisteredWorker),
		failCount:           make(map[string]int),
		heartbeatTimeout:    60 * time.Second,
		healthCheckInterval: 30 * time.Second,
		maxFailCount:        3,
		completedTasks:      make(map[string]time.Time),
		completedTaskTTL:    24 * time.Hour,
	}
}

// Register registers a worker
func (r *WorkerRegistry) Register(id string, workerType WorkerType, caps WorkerCapabilities) *RegisteredWorker {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker := &RegisteredWorker{
		ID:            id,
		Type:          workerType,
		Capabilities:  caps,
		LastHeartbeat: time.Now(),
		Status:        "idle",
	}
	r.workers[id] = worker
	result := *worker
	return &result
}

// Unregister unregisters a worker
func (r *WorkerRegistry) Unregister(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.workers[id]; !ok {
		return fmt.Errorf("worker %s does not exist", id)
	}
	delete(r.workers, id)
	return nil
}

// Get gets a worker
func (r *WorkerRegistry) Get(id string) (*RegisteredWorker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[id]
	if !ok {
		return nil, false
	}
	result := *w
	return &result, true
}

// List lists all workers
func (r *WorkerRegistry) List() []*RegisteredWorker {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*RegisteredWorker, 0, len(r.workers))
	for _, w := range r.workers {
		copy := *w
		result = append(result, &copy)
	}
	return result
}

// ListByType lists workers by type
func (r *WorkerRegistry) ListByType(workerType WorkerType) []*RegisteredWorker {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*RegisteredWorker
	for _, w := range r.workers {
		if w.Type == workerType {
			copy := *w
			result = append(result, &copy)
		}
	}
	return result
}

// Heartbeat updates worker heartbeat
func (r *WorkerRegistry) Heartbeat(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return fmt.Errorf("worker %s does not exist", id)
	}
	w.LastHeartbeat = time.Now()
	if w.Status == "stale" {
		w.Status = "idle"
	}
	return nil
}

// SetStatus sets worker status
func (r *WorkerRegistry) SetStatus(id, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return fmt.Errorf("worker %s does not exist", id)
	}
	w.Status = status
	return nil
}

// SetCurrentTask sets worker current task
func (r *WorkerRegistry) SetCurrentTask(id, taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return fmt.Errorf("worker %s does not exist", id)
	}
	w.CurrentTask = taskID
	return nil
}

// DetectStale detects stale workers (heartbeat timeout)
func (r *WorkerRegistry) DetectStale() []*RegisteredWorker {
	r.mu.Lock()
	defer r.mu.Unlock()

	var stale []*RegisteredWorker
	now := time.Now()
	for _, w := range r.workers {
		if now.Sub(w.LastHeartbeat) > r.heartbeatTimeout {
			w.Status = "stale"
			copy := *w
			stale = append(stale, &copy)
		}
	}
	return stale
}

// FindIdleByType finds idle workers by type
func (r *WorkerRegistry) FindIdleByType(workerType WorkerType) *RegisteredWorker {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, w := range r.workers {
		if w.Type == workerType && w.Status == "idle" {
			result := *w
			return &result
		}
	}
	return nil
}

// CountByType counts workers by type
func (r *WorkerRegistry) CountByType(workerType WorkerType) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, w := range r.workers {
		if w.Type == workerType {
			count++
		}
	}
	return count
}

// ActiveCount returns active worker count
func (r *WorkerRegistry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, w := range r.workers {
		if w.Status != "stale" {
			count++
		}
	}
	return count
}

// StartHealthCheck 启动健康检查 goroutine
// 注意：goroutine 内通过局部变量 hcCtx 访问 context，
// 避免 StopHealthCheck 将 r.hcCtx 置 nil 后产生竞态 panic
func (r *WorkerRegistry) StartHealthCheck(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.hcCancel != nil {
		return
	}

	r.hcCtx, r.hcCancel = context.WithCancel(ctx)
	hcCtx := r.hcCtx // 捕获到局部变量，防止并发置 nil 后空指针

	r.hcWG.Add(1)
	go func() {
		defer r.hcWG.Done()
		ticker := time.NewTicker(r.healthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-hcCtx.Done():
				return
			case <-ticker.C:
				r.performHealthCheck()
			}
		}
	}()
}

// StopHealthCheck stops the health check goroutine
func (r *WorkerRegistry) StopHealthCheck() {
	r.mu.Lock()
	cancel := r.hcCancel
	r.hcCtx = nil
	r.hcCancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		r.hcWG.Wait()
	}
}

// performHealthCheck checks worker health and removes unhealthy workers.
// Also cleans up expired completed task entries.
func (r *WorkerRegistry) performHealthCheck() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var toRemove []string

	for id, w := range r.workers {
		if now.Sub(w.LastHeartbeat) > r.heartbeatTimeout {
			r.failCount[id]++
			if r.failCount[id] >= r.maxFailCount {
				toRemove = append(toRemove, id)
			} else {
				w.Status = "stale"
			}
		} else {
			r.failCount[id] = 0
			if w.Status == "stale" {
				w.Status = "idle"
			}
		}
	}

	for _, id := range toRemove {
		delete(r.workers, id)
		delete(r.failCount, id)
		fmt.Fprintf(os.Stderr, "[HealthCheck] Worker %s removed after %d consecutive failures\n", id, r.maxFailCount)
	}

	// Clean up expired completed task entries
	r.cleanupExpiredTasks()
}

// cleanupExpiredTasks removes completed task entries older than completedTaskTTL.
func (r *WorkerRegistry) cleanupExpiredTasks() {
	now := time.Now()
	for taskID, completedAt := range r.completedTasks {
		if now.Sub(completedAt) > r.completedTaskTTL {
			delete(r.completedTasks, taskID)
		}
	}
}

// RecordCompletedTask records a completed task for expiration tracking.
func (r *WorkerRegistry) RecordCompletedTask(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completedTasks[taskID] = time.Now()
}
