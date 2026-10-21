// trace_store_test.go — H10 Phase 3 模块 4 TraceStore 反例测试（TDD 先行）。
// 覆盖：并发 append 无损坏 / append-only 不覆盖 / dependency_detected payload 解析 / 按子任务查询过滤。
// ；落地：TraceStore append-only JSONL
// SNAPSHOT: tests={ConcurrentAppend_NoCorruption, AppendOnly_NoOverwrite, DependencyDetected_PayloadParsed, QueryBySubtask_Filtered, QueryDependencyDetected, QueryReviewEvents, EmptyEventType_Rejected}

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// makeTestTraceStore 构造测试用 TraceStore，路径在 t.TempDir() 下。
func makeTestTraceStore(t *testing.T) *TraceStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review_trace.jsonl")
	store, err := NewTraceStore(path)
	if err != nil {
		t.Fatalf("NewTraceStore: %v", err)
	}
	return store
}

// TestTraceStore_ConcurrentAppend_NoCorruption 多 goroutine 同时 Append，-race 无竞态，文件不损坏。
func TestTraceStore_ConcurrentAppend_NoCorruption(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	const n = 50
	var wg sync.WaitGroup
	// 50 个 goroutine 各 append 10 条事件
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				err := store.Append(PhaseTraceEvent{
					EventType: Phase3EvtDispatch,
					SubtaskID: "subtask-concurrent",
					Payload: map[string]interface{}{
						"goroutine": g,
						"iter":      j,
					},
				})
				if err != nil {
					t.Errorf("Append 失败: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	// 验证文件未损坏：行数应为 500
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != n*10 {
		t.Errorf("文件应有 %d 行，got %d", n*10, len(lines))
	}

	// 验证每行都能正确 unmarshal
	valid := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		var event PhaseTraceEvent
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			valid++
		}
	}
	if valid != n*10 {
		t.Errorf("应全部可反序列化，valid=%d / expected=%d", valid, n*10)
	}
}

// TestTraceStore_AppendOnly_NoOverwrite 追加 3 条后文件含 3 行，不覆盖。
func TestTraceStore_AppendOnly_NoOverwrite(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	for i := 0; i < 3; i++ {
		err := store.Append(PhaseTraceEvent{
			EventType: Phase3EvtDispatch,
			SubtaskID: "subtask-A",
			Payload:   map[string]interface{}{"iter": i},
		})
		if err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	// 验证文件含 3 行
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("append-only 文件应含 3 行，got %d", len(lines))
	}

	// 再次 append，验证不覆盖
	if err := store.Append(PhaseTraceEvent{
		EventType: Phase3EvtReviewStarted,
		SubtaskID: "subtask-B",
	}); err != nil {
		t.Fatalf("Append 第四条: %v", err)
	}

	data, _ = os.ReadFile(store.Path())
	lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 4 {
		t.Errorf("append-only 文件应含 4 行（不覆盖），got %d", len(lines))
	}
}

// TestTraceStore_DependencyDetected_PayloadParsed dependency_detected 事件 payload 含 from/to/reason/evidence。
func TestTraceStore_DependencyDetected_PayloadParsed(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	err := store.Append(PhaseTraceEvent{
		EventType: Phase3EvtDependencyDetected,
		SubtaskID: "subtask-A",
		Payload: map[string]interface{}{
			"from":     "subtask-A",
			"to":       "subtask-B",
			"reason":   "contract change detected",
			"evidence": "https://example.com/evidence",
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := store.QueryDependencyDetected("subtask-A")
	if err != nil {
		t.Fatalf("QueryDependencyDetected: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应返回 1 条事件，got %d", len(events))
	}

	event := events[0]
	if event.EventType != Phase3EvtDependencyDetected {
		t.Errorf("EventType 应为 %s，got %s", Phase3EvtDependencyDetected, event.EventType)
	}

	// 验证 payload 字段
	from, ok := event.Payload["from"].(string)
	if !ok || from != "subtask-A" {
		t.Errorf("payload.from 应为 subtask-A，got %v", event.Payload["from"])
	}
	to, ok := event.Payload["to"].(string)
	if !ok || to != "subtask-B" {
		t.Errorf("payload.to 应为 subtask-B，got %v", event.Payload["to"])
	}
	reason, ok := event.Payload["reason"].(string)
	if !ok || reason != "contract change detected" {
		t.Errorf("payload.reason 应为 'contract change detected'，got %v", event.Payload["reason"])
	}
}

// TestTraceStore_QueryBySubtask_Filtered 写入 5 条（3 条 subtask-A + 2 条 subtask-B），QueryBySubtask("subtask-A") 返回 3 条。
func TestTraceStore_QueryBySubtask_Filtered(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	// 3 条 subtask-A
	for i := 0; i < 3; i++ {
		if err := store.Append(PhaseTraceEvent{
			EventType: Phase3EvtDispatch,
			SubtaskID: "subtask-A",
			Payload:   map[string]interface{}{"iter": i},
		}); err != nil {
			t.Fatalf("Append A[%d]: %v", i, err)
		}
	}
	// 2 条 subtask-B
	for i := 0; i < 2; i++ {
		if err := store.Append(PhaseTraceEvent{
			EventType: Phase3EvtDispatch,
			SubtaskID: "subtask-B",
		}); err != nil {
			t.Fatalf("Append B[%d]: %v", i, err)
		}
	}

	// QueryBySubtask("subtask-A") 应返回 3 条
	events, err := store.QueryBySubtask("subtask-A")
	if err != nil {
		t.Fatalf("QueryBySubtask: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("subtask-A 应返回 3 条事件，got %d", len(events))
	}

	// 验证全部是 subtask-A
	for _, e := range events {
		if e.SubtaskID != "subtask-A" {
			t.Errorf("事件 SubtaskID 应为 subtask-A，got %s", e.SubtaskID)
		}
	}

	// QueryBySubtask("subtask-B") 应返回 2 条
	eventsB, _ := store.QueryBySubtask("subtask-B")
	if len(eventsB) != 2 {
		t.Errorf("subtask-B 应返回 2 条事件，got %d", len(eventsB))
	}

	// QueryBySubtask 不存在的子任务应返回 0 条
	eventsX, _ := store.QueryBySubtask("subtask-X")
	if len(eventsX) != 0 {
		t.Errorf("不存在的 subtask-X 应返回 0 条，got %d", len(eventsX))
	}
}

// TestTraceStore_QueryReviewEvents 查询审查事件（review_started + review_completed）。
func TestTraceStore_QueryReviewEvents(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	// 写入混合事件
	for _, evt := range []PhaseTraceEvent{
		{EventType: Phase3EvtReviewStarted, SubtaskID: "subtask-R"},
		{EventType: Phase3EvtDispatch, SubtaskID: "subtask-R"},
		{EventType: Phase3EvtReviewCompleted, SubtaskID: "subtask-R"},
		{EventType: Phase3EvtBudgetCheck, SubtaskID: "subtask-R"},
		{EventType: Phase3EvtReviewStarted, SubtaskID: "subtask-R2"},
	} {
		if err := store.Append(evt); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// QueryReviewEvents("subtask-R") 应返回 2 条（started + completed）
	events, err := store.QueryReviewEvents("subtask-R")
	if err != nil {
		t.Fatalf("QueryReviewEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("subtask-R 审查事件应返回 2 条，got %d", len(events))
	}

	// 全部查询（subtaskID=""）应返回 3 条审查事件
	allEvents, _ := store.QueryReviewEvents("")
	if len(allEvents) != 3 {
		t.Errorf("全部审查事件应返回 3 条，got %d", len(allEvents))
	}
}

// TestTraceStore_EmptyEventType_Rejected 空 EventType 必须拒绝。
func TestTraceStore_EmptyEventType_Rejected(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	err := store.Append(PhaseTraceEvent{
		EventType: "",
		SubtaskID: "subtask-A",
	})
	if err == nil {
		t.Fatal("空 EventType 必须返回 error")
	}
	if !strings.Contains(err.Error(), "EventType must not be empty") {
		t.Errorf("error 应包含 'EventType must not be empty'，got: %v", err)
	}
}

// TestTraceStore_TimestampAutoFill Timestamp 为空时自动填充。
func TestTraceStore_TimestampAutoFill(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	err := store.Append(PhaseTraceEvent{
		EventType: Phase3EvtDispatch,
		SubtaskID: "subtask-A",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, _ := store.QueryBySubtask("subtask-A")
	if len(events) != 1 {
		t.Fatalf("应返回 1 条事件，got %d", len(events))
	}
	if events[0].Timestamp == "" {
		t.Error("Timestamp 为空时应自动填充，但仍为空")
	}
}

// TestTraceStore_PathTraversal_Rejected 路径含 ".." 时拒绝。
func TestTraceStore_PathTraversal_Rejected(t *testing.T) {
	t.Parallel()
	_, err := NewTraceStore("../escape.jsonl")
	if err == nil {
		t.Fatal("路径含 '..' 时必须拒绝")
	}
	if !strings.Contains(err.Error(), "contains '..'") {
		t.Errorf("error 应包含 'contains ..'，got: %v", err)
	}
}

// TestTraceStore_EmptyPath_Rejected 空路径拒绝。
func TestTraceStore_EmptyPath_Rejected(t *testing.T) {
	t.Parallel()
	_, err := NewTraceStore("")
	if err == nil {
		t.Fatal("空路径必须拒绝")
	}
}

// TestTraceStore_QueryEmptyFile 文件不存在时查询返回 nil 无 error。
func TestTraceStore_QueryEmptyFile(t *testing.T) {
	t.Parallel()
	store := makeTestTraceStore(t)

	// 未 append 任何事件，文件不存在
	events, err := store.QueryBySubtask("subtask-X")
	if err != nil {
		t.Fatalf("QueryBySubtask 空 store 应返回 nil, nil，got err: %v", err)
	}
	if events != nil {
		t.Errorf("空 store 查询应返回 nil，got %d 条", len(events))
	}
}
