package tools

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// PendingTool is a tool waiting for execution
type PendingTool struct {
	ID       string
	Name     string
	input    strings.Builder
	Ready    bool
	Done     bool
	Result   *Result
	Priority int // higher value = higher priority
	cancelFn context.CancelFunc
}

// ToolScheduler is a streaming tool scheduler, supports immediate execution after tool_use block is received
type ToolScheduler struct {
	pending   map[string]*PendingTool
	fileLocks map[string]*sync.Mutex
	maxConcur int
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	executor  *Executor
	resultCh  chan Result
	results   []Result
	resultsMu sync.Mutex
	wg        sync.WaitGroup
	concurSem chan struct{}
	bashMu    sync.Mutex
	unsafeMu  sync.Mutex
	fileMu    sync.Mutex
}

// NewToolScheduler creates a streaming tool scheduler
func NewToolScheduler(ctx context.Context, executor *Executor) *ToolScheduler {
	childCtx, cancel := context.WithCancel(ctx)
	return &ToolScheduler{
		pending:   make(map[string]*PendingTool),
		fileLocks: make(map[string]*sync.Mutex),
		maxConcur: 10,
		ctx:       childCtx,
		cancel:    cancel,
		executor:  executor,
		resultCh:  make(chan Result, 64),
		concurSem: make(chan struct{}, 10),
	}
}

// AddPending adds a pending tool for execution (called when tool_use block starts)
func (s *ToolScheduler) AddPending(id, name string, input string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pt := &PendingTool{
		ID:   id,
		Name: NormalizeToolName(name),
	}
	pt.input.WriteString(input)
	s.pending[id] = pt
	log.Printf("[scheduler] AddPending: id=%s tool=%s", id, pt.Name)
}

// MarkReady marks tool input as fully received, triggers execution
func (s *ToolScheduler) MarkReady(id string) {
	s.mu.Lock()
	pt, ok := s.pending[id]
	if !ok || pt.Ready || pt.Done {
		s.mu.Unlock()
		return
	}
	pt.Ready = true
	s.mu.Unlock()

	name := NormalizeToolName(pt.Name)
	log.Printf("[scheduler] MarkReady: id=%s tool=%s", id, name)

	s.wg.Add(1)
	// ⚠️ P-002（2026-09-22 反缝补扫描）：本 switch 里的工具名判定**刻意保持手抄**，不是漏改。
	//
	// 这里问的**不是**「是不是命令载体 / 要不要拦」（那用 `permission.IsCommandBearingTool`），
	// 而是「这一次调用用哪条**并发策略**执行」—— 两处语义不同（同 capability.go 里
	// CapExec 与 sandboxBoundedTools 的分工）。具体地：把 `Monitor` 归到
	// `executeWithBashLimit`（并发上限 1、无全局串行锁）会把它从当前的
	// `executeWithUnsafeLock`（**全局互斥，更严**）降级 ⇒ 那是**放松**，与本轮
	// "只增加确认、不放松任何判定"的方向相反。
	//
	// 若要改，属独立的并发策略批次，须配 A/B 实测：Monitor 是长跑进程工具
	// （`npm run dev` / `tail -f`），长期占住全局锁反向也可能是个真实缺陷 ——
	// 但那需要单独论证，不在本轮顺手改。
	switch {
	case s.executor.IsConcurrencySafe(name):
		go s.executeSafe(pt)
	case name == "bash":
		go s.executeWithBashLimit(pt)
	case name == "write_file" || name == "edit_file":
		go s.executeWithFileLock(pt)
	default:
		go s.executeWithUnsafeLock(pt)
	}
}

// SetInputDelta appends tool input JSON fragment
func (s *ToolScheduler) SetInputDelta(id string, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pt, ok := s.pending[id]
	if !ok || pt.Ready || pt.Done {
		return
	}
	pt.input.WriteString(delta)
}

// CancelAll cancels all pending tools, returns list of cancelled IDs
func (s *ToolScheduler) CancelAll() []string {
	s.cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	var cancelled []string
	for id, pt := range s.pending {
		if !pt.Done {
			if pt.cancelFn != nil {
				pt.cancelFn()
			}
			pt.Done = true
			cancelled = append(cancelled, id)
		}
	}
	return cancelled
}

// Results returns the result channel for streaming consumption of execution results
func (s *ToolScheduler) Results() <-chan Result {
	return s.resultCh
}

// WaitAll waits for all tool executions to complete and returns result list
func (s *ToolScheduler) WaitAll() []Result {
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range s.resultCh {
		}
	}()

	s.wg.Wait()
	close(s.resultCh)
	<-drainDone

	s.resultsMu.Lock()
	defer s.resultsMu.Unlock()
	return s.results
}

// RunningCount returns the count of currently executing tools
func (s *ToolScheduler) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, pt := range s.pending {
		if pt.Ready && !pt.Done {
			count++
		}
	}
	return count
}

// PendingCount returns the count of pending tools waiting for execution
func (s *ToolScheduler) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, pt := range s.pending {
		if !pt.Ready && !pt.Done {
			count++
		}
	}
	return count
}

// executeSafe executes concurrent-safe tools, immediately executing in goroutine
func (s *ToolScheduler) executeSafe(pt *PendingTool) {
	defer s.wg.Done()

	select {
	case s.concurSem <- struct{}{}:
		defer func() { <-s.concurSem }()
	case <-s.ctx.Done():
		s.finishTool(pt, Result{
			ID:      pt.ID,
			Name:    pt.Name,
			Output:  "toolexecutecancelled",
			IsError: true,
		})
		return
	}

	s.runTool(pt)
}

// executeWithBashLimit executes bash tool with concurrency limit of 1
func (s *ToolScheduler) executeWithBashLimit(pt *PendingTool) {
	defer s.wg.Done()

	select {
	case s.concurSem <- struct{}{}:
		defer func() { <-s.concurSem }()
	case <-s.ctx.Done():
		s.finishTool(pt, Result{
			ID:      pt.ID,
			Name:    pt.Name,
			Output:  "toolexecutecancelled",
			IsError: true,
		})
		return
	}

	s.bashMu.Lock()
	defer s.bashMu.Unlock()

	s.runTool(pt)
}

// executeWithFileLock executes tools requiring file locks (write_file/edit_file)
func (s *ToolScheduler) executeWithFileLock(pt *PendingTool) {
	defer s.wg.Done()

	select {
	case s.concurSem <- struct{}{}:
		defer func() { <-s.concurSem }()
	case <-s.ctx.Done():
		s.finishTool(pt, Result{
			ID:      pt.ID,
			Name:    pt.Name,
			Output:  "toolexecutecancelled",
			IsError: true,
		})
		return
	}

	filePath := extractFilePath(pt.Name, pt.input.String())
	fileLock := s.getFileLock(filePath)
	fileLock.Lock()
	defer fileLock.Unlock()

	s.runTool(pt)
}

// executeWithUnsafeLock executes non-concurrent-safe tools using global mutex for ordered execution
func (s *ToolScheduler) executeWithUnsafeLock(pt *PendingTool) {
	defer s.wg.Done()

	select {
	case s.concurSem <- struct{}{}:
		defer func() { <-s.concurSem }()
	case <-s.ctx.Done():
		s.finishTool(pt, Result{
			ID:      pt.ID,
			Name:    pt.Name,
			Output:  "toolexecutecancelled",
			IsError: true,
		})
		return
	}

	s.unsafeMu.Lock()
	defer s.unsafeMu.Unlock()

	s.runTool(pt)
}

// runTool executes a single tool and processes the result
func (s *ToolScheduler) runTool(pt *PendingTool) {
	toolCtx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	pt.cancelFn = cancel
	s.mu.Unlock()

	call := Call{
		ID:    pt.ID,
		Name:  pt.Name,
		Input: pt.input.String(),
	}

	pipelineResult := s.executor.ExecuteWithPipeline(toolCtx, call)
	var result Result
	if pipelineResult.Result != nil {
		result = *pipelineResult.Result
	} else if !pipelineResult.Proceed {
		result = Result{
			ID:      pt.ID,
			Name:    pt.Name,
			Output:  pipelineResult.Rejection,
			IsError: true,
		}
	}

	cancel()

	s.finishTool(pt, result)
}

// finishTool completes tool execution, updates state and sends result
func (s *ToolScheduler) finishTool(pt *PendingTool, result Result) {
	s.mu.Lock()
	pt.Done = true
	pt.Result = &result
	s.mu.Unlock()

	log.Printf("[scheduler] finishTool: id=%s tool=%s isError=%v", pt.ID, pt.Name, result.IsError)

	s.resultsMu.Lock()
	s.results = append(s.results, result)
	s.resultsMu.Unlock()

	s.resultCh <- result
	s.cleanupFileLocks()
}

func (s *ToolScheduler) cleanupFileLocks() {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()

	activePaths := make(map[string]bool)
	for _, pt := range s.pending {
		if !pt.Done && (pt.Name == "write_file" || pt.Name == "edit_file") {
			path := extractFilePath(pt.Name, pt.input.String())
			if path != "" {
				activePaths[path] = true
			}
		}
	}
	for path := range s.fileLocks {
		if !activePaths[path] {
			delete(s.fileLocks, path)
		}
	}
}

// getFileLock gets the mutex for a file path, falls back to global mutex when path is empty
func (s *ToolScheduler) getFileLock(path string) *sync.Mutex {
	if path == "" {
		return &s.unsafeMu
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if _, ok := s.fileLocks[path]; !ok {
		s.fileLocks[path] = &sync.Mutex{}
	}
	return s.fileLocks[path]
}

// extractFilePath extracts file path from tool input
func extractFilePath(toolName, input string) string {
	switch toolName {
	case "write_file", "edit_file", "read_file":
		var parsed struct {
			Path string `json:"path"`
		}
		json.Unmarshal([]byte(input), &parsed)
		return parsed.Path
	default:
		return ""
	}
}
