// trace_store.go — H10 Phase 3 模块 4 审查与调度 trace 存储（H10-Phase3-方案细化-v3.md §6.2.3 / §6.3）。
// REF-外部-LangGraph@https://langchain-ai.github.io/langgraph/concepts/persistence/+2026-07-15访问-Checkpointer StateSnapshot；借鉴逻辑：每步保存 trace；落地：TraceStore append-only JSONL
// ；落地：dependency_detected 事件 {from,to,reason,evidence}
// 复用-TraceWriter append-only 模式；落地：TraceStore 同模式独立文件
// SNAPSHOT: pkg=internal/orchestrator; type=TraceStore; path=.whale/orchestrator/review_trace.jsonl; events={dependency_detected,review_started,review_completed,dispatch,budget_check}; concurrency=sync.Mutex; format=append-only JSONL

package orchestrator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TraceStore 事件类型枚举（Phase 3 §6.2.3 动态依赖 + §6.3 审查事件）。
// 注意：与 Phase 1 trace.go 的 TraceEvent* 区分（Phase 1 是 subtask 执行 trace，
// Phase 3 是审查/调度 trace），用 Phase3Evt 前缀避免重定义冲突。
const (
	// Phase3EvtDependencyDetected 动态依赖检测事件（Phase 3 §6.2.3）。
	// Payload: {from, to, reason, evidence}
	Phase3EvtDependencyDetected = "dependency_detected"

	// Phase3EvtReviewStarted 审查开始事件（Phase 3 §6.3）。
	// Payload: {tier, dimensions, content_length}
	Phase3EvtReviewStarted = "review_started"

	// Phase3EvtReviewCompleted 审查完成事件（Phase 3 §6.3）。
	// Payload: {verdict, fatal_flaws_count, round}
	Phase3EvtReviewCompleted = "review_completed"

	// Phase3EvtDispatch 子任务派发事件（与 Phase 1 TraceEventDispatch 同值但独立常量）。
	// Payload: {subtask_id, bucket, base_branch, model}
	Phase3EvtDispatch = "dispatch"

	// Phase3EvtBudgetCheck 预算检查事件（Phase 3 §6.2.4 派发前预估 + 事后检查）。
	// Payload: {check_type, estimated_cents, actual_cents, over_budget}
	Phase3EvtBudgetCheck = "budget_check"

	// Phase3EvtSubtaskOutput 子任务产出事件（B-P0-6 修复：不再丢弃 agentOutput）。
	// Payload: {status, result_length, task_id, usage}
	Phase3EvtSubtaskOutput = "subtask_output"
)

// TraceStore 审查与调度 trace 存储（Phase 3 §6.2.3 动态依赖检测 + §6.3 审查 trace）。
// 复用 Phase 1 TraceWriter 的 append-only JSONL 模式，独立文件不混入执行 trace。
// 并发保护：sync.Mutex（02 §2.7.3 并发控制）。
type TraceStore struct {
	path string     // .whale/orchestrator/review_trace.jsonl
	mu   sync.Mutex // 保护并发 append（02 §2.7.3 并发控制）
}

// PhaseTraceEvent Phase 3 trace 事件类型（Phase 3 §6.2.3 动态依赖 + §6.3 审查事件）。
// 注意：与 Phase 1 TraceEntry 区分，Phase 3 用 Payload map 携带结构化数据。
type PhaseTraceEvent struct {
	EventType string                 `json:"event"` // dependency_detected / review_started / review_completed / dispatch / budget_check
	SubtaskID string                 `json:"subtask_id"`
	Timestamp string                 `json:"timestamp"` // ISO 8601
	Payload   map[string]interface{} `json:"payload"`
}

// NewTraceStore 创建 trace 存储。
// path 为 trace.jsonl 文件路径，目录不存在时自动创建。
// 路径穿越防护：path 含 ".." 时拒绝。
func NewTraceStore(path string) (*TraceStore, error) {
	if path == "" {
		return nil, fmt.Errorf("trace_store: path must not be empty")
	}
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("trace_store: path contains '..': %s", path)
	}

	cleaned := filepath.Clean(path)
	// 确保目录存在
	if dir := filepath.Dir(cleaned); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("trace_store: mkdir %s: %w", dir, err)
		}
	}

	return &TraceStore{
		path: cleaned,
	}, nil
}

// Append 追加一条 trace 事件（append-only，加锁）。
// 事件被序列化为 JSON 单行 + \n，以 O_APPEND|O_CREATE|O_WRONLY 模式写入。
// Timestamp 为空时自动填充当前时间（ISO 8601）。
func (t *TraceStore) Append(event PhaseTraceEvent) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if event.EventType == "" {
		return fmt.Errorf("trace_store: EventType must not be empty")
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("trace_store: marshal event: %w", err)
	}
	data = append(data, '\n')

	// 每次打开-写入-关闭，确保 append-only 语义（不持长连接文件句柄）
	f, err := os.OpenFile(t.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("trace_store: open %s: %w", t.path, err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("trace_store: write %s: %w", t.path, err)
	}
	return nil
}

// QueryBySubtask 按子任务 ID 查询所有 trace 事件。
// 只读打开文件逐行扫描，过滤 SubtaskID == subtaskID 的事件。
func (t *TraceStore) QueryBySubtask(subtaskID string) ([]PhaseTraceEvent, error) {
	return t.query(func(e PhaseTraceEvent) bool {
		return e.SubtaskID == subtaskID
	})
}

// QueryDependencyDetected 查询动态依赖检测事件（Phase 3 §6.2.3）。
// 过滤 EventType == "dependency_detected" 的事件，可选过滤 subtaskID。
// subtaskID 为空时返回所有 dependency_detected 事件。
func (t *TraceStore) QueryDependencyDetected(subtaskID string) ([]PhaseTraceEvent, error) {
	return t.query(func(e PhaseTraceEvent) bool {
		if e.EventType != Phase3EvtDependencyDetected {
			return false
		}
		if subtaskID == "" {
			return true
		}
		return e.SubtaskID == subtaskID
	})
}

// QueryReviewEvents 查询审查事件（Phase 3 §6.3）。
// 过滤 EventType 为 review_started 或 review_completed 的事件，可选过滤 subtaskID。
func (t *TraceStore) QueryReviewEvents(subtaskID string) ([]PhaseTraceEvent, error) {
	return t.query(func(e PhaseTraceEvent) bool {
		if e.EventType != Phase3EvtReviewStarted && e.EventType != Phase3EvtReviewCompleted {
			return false
		}
		if subtaskID == "" {
			return true
		}
		return e.SubtaskID == subtaskID
	})
}

// query 通用的过滤查询（逐行扫描 trace.jsonl）。
func (t *TraceStore) query(filter func(PhaseTraceEvent) bool) ([]PhaseTraceEvent, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 文件不存在视为空
		}
		return nil, fmt.Errorf("trace_store: open %s: %w", t.path, err)
	}
	defer f.Close()

	var events []PhaseTraceEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event PhaseTraceEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// 跳过损坏行（不中断查询），但记录 error
			continue
		}
		if filter(event) {
			events = append(events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("trace_store: scan: %w", err)
	}
	return events, nil
}

// Path 返回 trace.jsonl 文件路径（仅用于诊断）。
func (t *TraceStore) Path() string {
	return t.path
}
