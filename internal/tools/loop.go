// Package tools — AI conversation-level loop task scheduler
// LoopScheduler manages periodically triggered AI prompt tasks. When the timer fires,
// it injects the prompt as a virtual user message into the AI conversation queue.
package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/util"
)

// Default max lifetime: 7 days
const defaultMaxAge = 7 * 24 * time.Hour

// Minimum trigger interval: 30 seconds, prevents API abuse
const minInterval = 30 * time.Second

// Default jitter range: ±60 seconds
const defaultJitterRange = 60 * time.Second

// Maximum loop iterations to prevent infinite loops
const maxLoopIterations = 1000

// LoopTask represents a periodically triggered AI prompt task
type LoopTask struct {
	ID          string        `json:"id"`
	CronExpr    string        `json:"cron_expr,omitempty"`     // reserved field, not used currently
	Interval    time.Duration `json:"interval"`                // parsed interval duration
	IntervalStr string        `json:"interval_str"`            // original interval string (e.g., "5m")
	Prompt      string        `json:"prompt"`                  // AI prompt, not a shell command
	Recurring   bool          `json:"recurring"`               // whether to loop, false means one-shot task
	CreatedAt   time.Time     `json:"created_at"`              // creation time
	LastFiredAt *time.Time    `json:"last_fired_at,omitempty"` // last fired time
	MaxAge      time.Duration `json:"max_age"`                 // max lifetime, default 7 days
	MaxFires    int           `json:"max_fires,omitempty"`     // max fire count, 0=unlimited
	FireCount   int           `json:"fire_count"`              // fire count
	Status      string        `json:"status"`                  // task status: "active" | "paused" | "cancelled" | "completed"
	JitterRange time.Duration `json:"jitter_range,omitempty"`  // jitter range, default ±60s
	jitterOff   time.Duration `json:"-"`                       // computed jitter offset (not serialized)
}

// loopStore is the persistent storage format
type loopStore struct {
	NextID int64      `json:"next_id"`
	Tasks  []LoopTask `json:"tasks"`
}

// LoopScheduler manages loop task scheduling
// Uses a 1-second ticker to check if tasks need firing, injects prompts into conversation via onFire callback
type LoopScheduler struct {
	mu       sync.RWMutex
	tasks    map[string]*LoopTask
	nextID   atomic.Int64
	ticker   *time.Ticker
	done     chan struct{}
	onFire   func(taskID string, prompt string)
	started  bool
	filePath string
	stopOnce sync.Once
}

// LoopCreateOptions are options for creating a loop task
type LoopCreateOptions struct {
	IntervalStr     string // interval string
	Prompt          string // AI prompt
	Recurring       bool   // whether to loop (true=recurring, false=one-shot)
	FireImmediately bool   // whether to fire immediately on first run
	MaxFires        int    // max fire count, 0=unlimited
}

// NewLoopScheduler creates a loop task scheduler
// onFire: callback function when task fires, called in ticker goroutine, must be thread-safe
// filePath: persistence file path, empty string means no persistence
func NewLoopScheduler(onFire func(taskID, prompt string), filePath string) *LoopScheduler {
	ls := &LoopScheduler{
		tasks:    make(map[string]*LoopTask),
		onFire:   onFire,
		done:     make(chan struct{}),
		filePath: filePath,
	}
	// Load tasks from file
	if filePath != "" {
		ls.loadFromFile()
	}
	return ls
}

// CreateWithOpts creates a loop task with options
func (ls *LoopScheduler) CreateWithOpts(opts LoopCreateOptions) (*LoopTask, error) {
	interval, err := parseInterval(opts.IntervalStr)
	if err != nil {
		return nil, err
	}

	if interval < minInterval {
		return nil, fmt.Errorf("interval cannot be less than %v, got: %v", minInterval, interval)
	}

	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt cannot be empty")
	}

	id := fmt.Sprintf("loop_%d", ls.nextID.Add(1))
	now := time.Now()

	// Compute jitter offset
	jitterOff := computeJitter(id, defaultJitterRange)

	task := &LoopTask{
		ID:          id,
		Interval:    interval,
		IntervalStr: opts.IntervalStr,
		Prompt:      opts.Prompt,
		Recurring:   opts.Recurring,
		CreatedAt:   now,
		LastFiredAt: nil,
		MaxAge:      defaultMaxAge,
		MaxFires:    opts.MaxFires,
		FireCount:   0,
		Status:      "active",
		JitterRange: defaultJitterRange,
		jitterOff:   jitterOff,
	}

	ls.mu.Lock()
	ls.tasks[id] = task
	ls.mu.Unlock()

	// Persist
	ls.saveToFile()

	// Fire immediately on first run
	if opts.FireImmediately && ls.onFire != nil {
		now := time.Now()
		task.LastFiredAt = &now
		task.FireCount++
		ls.saveToFile()
		ls.onFire(task.ID, task.Prompt)

		// One-shot task completes immediately after firing
		if !task.Recurring {
			task.Status = "completed"
			ls.saveToFile()
		}
	}

	return task, nil
}

// Create creates a new loop task (legacy interface, default recurring + fire immediately)
func (ls *LoopScheduler) Create(intervalStr, prompt string) (*LoopTask, error) {
	return ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     intervalStr,
		Prompt:          prompt,
		Recurring:       true,
		FireImmediately: true,
		MaxFires:        0,
	})
}

// List lists all active loop tasks
func (ls *LoopScheduler) List() []*LoopTask {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	var result []*LoopTask
	for _, task := range ls.tasks {
		if task.Status == "active" {
			result = append(result, task)
		}
	}
	return result
}

// ListAll lists all loop tasks (including cancelled and completed)
func (ls *LoopScheduler) ListAll() []*LoopTask {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	var result []*LoopTask
	for _, task := range ls.tasks {
		result = append(result, task)
	}
	return result
}

// Cancel cancels the specified loop task
func (ls *LoopScheduler) Cancel(id string) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	task, ok := ls.tasks[id]
	if !ok {
		return fmt.Errorf("loop task %s does not exist", id)
	}
	if task.Status == "cancelled" {
		return fmt.Errorf("loop task %s already cancelled", id)
	}
	if task.Status == "completed" {
		return fmt.Errorf("loop task %s already completed", id)
	}
	task.Status = "cancelled"
	ls.saveToFileLocked()
	return nil
}

// Pause pauses the specified loop task
func (ls *LoopScheduler) Pause(id string) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	task, ok := ls.tasks[id]
	if !ok {
		return fmt.Errorf("loop task %s does not exist", id)
	}
	if task.Status != "active" {
		return fmt.Errorf("loop task %s current status is %s, cannot pause", id, task.Status)
	}
	task.Status = "paused"
	ls.saveToFileLocked()
	return nil
}

// Resume resumes a paused loop task
func (ls *LoopScheduler) Resume(id string) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	task, ok := ls.tasks[id]
	if !ok {
		return fmt.Errorf("loop task %s does not exist", id)
	}
	if task.Status != "paused" {
		return fmt.Errorf("loop task %s current status is %s, cannot resume", id, task.Status)
	}
	task.Status = "active"
	ls.saveToFileLocked()
	return nil
}

// Get returns the loop task with the specified ID
func (ls *LoopScheduler) Get(id string) (*LoopTask, bool) {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	task, ok := ls.tasks[id]
	if !ok {
		return nil, false
	}
	return task, true
}

// Start starts the scheduler's 1-second ticker
func (ls *LoopScheduler) Start() {
	if ls.started {
		return
	}
	ls.started = true
	ls.ticker = time.NewTicker(1 * time.Second)
	go ls.run()
}

// Stop stops the scheduler
func (ls *LoopScheduler) Stop() {
	ls.stopOnce.Do(func() {
		if !ls.started {
			return
		}
		ls.ticker.Stop()
		close(ls.done)
		ls.started = false
	})
}

// run is the scheduler main loop, checks every second if tasks need firing
func (ls *LoopScheduler) run() {
	for {
		select {
		case <-ls.done:
			return
		case <-ls.ticker.C:
			ls.checkAndFire()
		}
	}
}

// checkAndFire checks all active tasks for firing and calls onFire callback
func (ls *LoopScheduler) checkAndFire() {
	now := time.Now()

	// Use a single Lock (not RLock+Lock) to eliminate the race window between
	// releasing RLock and acquiring Lock where task state could change.
	ls.mu.Lock()
	var toFire []*LoopTask
	for _, task := range ls.tasks {
		if task.Status != "active" {
			continue
		}
		// Check if max lifetime is exceeded
		if now.Sub(task.CreatedAt) > task.MaxAge {
			continue
		}
		// Compute next fire time with jitter
		nextFireTime := ls.nextFireTime(task, now)
		if now.After(nextFireTime) || now.Equal(nextFireTime) {
			toFire = append(toFire, task)
		}
	}

	// Update state for all tasks to fire while still holding the lock
	for _, task := range toFire {
		fireNow := time.Now()
		task.LastFiredAt = &fireNow
		task.FireCount++

		// Check if max fire count is reached
		if task.MaxFires > 0 && task.FireCount >= task.MaxFires {
			task.Status = "completed"
		}
		// Check hard limit for total iterations
		if task.FireCount >= maxLoopIterations {
			task.Status = "completed"
		}
		// One-shot task completes after firing
		if !task.Recurring {
			task.Status = "completed"
		}
	}

	// ★ 只在真有任务状态变化时才落盘（2026-10-15 修）。
	// 原实现每个 tick（1s）无条件调 saveToFileLocked()，任务列表为空也照写 ⇒
	// 单实例 86400 写/天、7 个实例约 600K 写/天，全是"写什么都没变的东西"。
	// 每次写都会让 util.AtomicWriteFile CreateTemp 出 <name>.<rand>.tmp；
	// 而本机外层沙箱对 ~/.whale 内的 rename/unlink 返回 EPERM
	// （whale.log 原话：`rename ... operation not permitted`），
	// 连带错误分支里的 os.Remove(tmpPath) 也失败 ⇒ 临时文件滞留原地。
	// 实测 ~/.whale 顶层因此攒到 29.9 万条目，把 doctor 的
	// "Recovery / Data Integrity" 5 秒扫描直接打超时。
	// 判别器：internal/tools/loop_noop_write_test.go（无到点任务 ⇒ 文件不得被替换）。
	if len(toFire) > 0 {
		ls.saveToFileLocked()
	}
	ls.mu.Unlock()

	// Call onFire callback outside lock to avoid deadlock
	for _, task := range toFire {
		if ls.onFire != nil {
			ls.onFire(task.ID, task.Prompt)
		}
	}
}

// nextFireTime computes the next fire time with jitter
func (ls *LoopScheduler) nextFireTime(task *LoopTask, now time.Time) time.Time {
	var baseTime time.Time
	if task.LastFiredAt != nil {
		baseTime = *task.LastFiredAt
	} else {
		baseTime = task.CreatedAt
	}
	return baseTime.Add(task.Interval + task.jitterOff)
}

// CleanExpired cleans up expired and cancelled tasks
func (ls *LoopScheduler) CleanExpired() int {
	now := time.Now()
	ls.mu.Lock()
	defer ls.mu.Unlock()

	count := 0
	for id, task := range ls.tasks {
		// Clean tasks exceeding max lifetime
		if task.Status == "active" && now.Sub(task.CreatedAt) > task.MaxAge {
			task.Status = "cancelled"
			count++
		}
		// Clean cancelled/completed tasks older than 1 day
		if (task.Status == "cancelled" || task.Status == "completed") && now.Sub(task.CreatedAt) > 24*time.Hour {
			delete(ls.tasks, id)
			count++
		}
	}
	if count > 0 {
		ls.saveToFileLocked()
	}
	return count
}

// FormatList 将任务列表格式化为可读字符串，空列表时返回中文提示
func (ls *LoopScheduler) FormatList() string {
	tasks := ls.List()
	if len(tasks) == 0 {
		return util.Bi("No active loop tasks", "没有活跃的循环任务")
	}

	var result string
	for i, task := range tasks {
		var lastFired string
		if task.LastFiredAt != nil {
			lastFired = task.LastFiredAt.Format("15:04:05")
		} else {
			lastFired = "not yet fired"
		}

		var nextFire string
		now := time.Now()
		nextFireTime := ls.nextFireTime(task, now)
		if task.LastFiredAt != nil {
			nextFire = nextFireTime.Format("15:04:05")
		} else {
			nextFire = nextFireTime.Format("15:04:05")
		}

		mode := "recurring"
		if !task.Recurring {
			mode = "one-shot"
		}

		extra := ""
		if task.MaxFires > 0 {
			extra = fmt.Sprintf(", limit: %d fires", task.MaxFires)
		}

		result += fmt.Sprintf("  %d. [%s] %s — %s, interval: %s, fired: %d times%s, last: %s, next: %s\n",
			i+1, task.ID, task.Prompt, mode, task.IntervalStr, task.FireCount, extra, lastFired, nextFire)
	}
	return fmt.Sprintf("Loop task list (%d):\n%s", len(tasks), result)
}

// --- Persistence ---

// saveToFile saves tasks to file (caller must hold read lock or no lock)
func (ls *LoopScheduler) saveToFile() {
	if ls.filePath == "" {
		return
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	ls.saveToFileLocked()
}

// saveToFileLocked saves tasks to file (caller must hold lock)
func (ls *LoopScheduler) saveToFileLocked() {
	if ls.filePath == "" {
		return
	}

	store := loopStore{
		NextID: ls.nextID.Load(),
		Tasks:  make([]LoopTask, 0, len(ls.tasks)),
	}
	for _, task := range ls.tasks {
		t := *task      // value copy
		t.jitterOff = 0 // not serialized
		store.Tasks = append(store.Tasks, t)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return
	}

	if err := util.AtomicWriteFile(ls.filePath, data, 0600); err != nil {
		// 原先此处静默 return —— 29.9 万个滞留临时文件正是在"完全没有日志"的情况下
		// 攒了 59 天（2026-08-17 起）才被发现。失败必须留痕（对齐 main.go:1617 的
		// `log.Warn("Failed to save models.json", "error", err)`）。
		log.Warn("Failed to save loop_tasks.json", "error", err)
		return
	}
}

// loadFromFile loads tasks from file
func (ls *LoopScheduler) loadFromFile() {
	if ls.filePath == "" {
		return
	}

	data, err := os.ReadFile(ls.filePath)
	if err != nil {
		return // file not found or corrupted, normal initialization
	}

	var store loopStore
	if err := json.Unmarshal(data, &store); err != nil {
		return // corrupted file, normal initialization
	}

	now := time.Now()
	ls.nextID.Store(store.NextID)

	for i := range store.Tasks {
		task := &store.Tasks[i]
		// Skip expired tasks
		if task.Status == "active" && now.Sub(task.CreatedAt) > task.MaxAge {
			continue
		}
		// Reset LastFiredAt to prevent retroactive firing
		// 加载时清空 LastFiredAt（置为 nil），避免持久化的旧时间戳触发补触发
		if task.LastFiredAt != nil {
			task.LastFiredAt = nil
		}
		// Recompute jitter offset
		task.jitterOff = computeJitter(task.ID, task.JitterRange)
		ls.tasks[task.ID] = task
	}
}

// --- Jitter Computation ---

// computeJitter computes a deterministic jitter offset based on taskID hash
func computeJitter(taskID string, jitterRange time.Duration) time.Duration {
	if jitterRange <= 0 {
		return 0
	}
	h := sha256.Sum256([]byte(taskID))
	// Take first 8 bytes and convert to uint64
	var hashVal uint64
	for i := 0; i < 8; i++ {
		hashVal = hashVal<<8 | uint64(h[i])
	}
	// Map to [-jitterRange, +jitterRange]
	rangeNs := jitterRange.Nanoseconds() * 2
	offset := int64(hashVal%uint64(rangeNs)) - jitterRange.Nanoseconds()
	return time.Duration(offset)
}

// --- Interval Parsing ---

// parseInterval parses an interval string into a time.Duration
// Supported formats: "30s", "5m", "2h", "1d"
func parseInterval(s string) (time.Duration, error) {
	re := regexp.MustCompile(`^(\d+)(s|m|h|d)$`)
	matches := re.FindStringSubmatch(s)
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid interval format: %q, supported formats: 30s, 5m, 2h, 1d", s)
	}

	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value: %q", matches[1])
	}

	if value <= 0 {
		return 0, fmt.Errorf("interval value must be greater than 0")
	}

	var duration time.Duration
	switch matches[2] {
	case "s":
		duration = time.Duration(value) * time.Second
	case "m":
		duration = time.Duration(value) * time.Minute
	case "h":
		duration = time.Duration(value) * time.Hour
	case "d":
		duration = time.Duration(value) * 24 * time.Hour
	}

	return duration, nil
}
