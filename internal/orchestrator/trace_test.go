// trace_test.go — 模块 7 可观测性反例测试（TDD 先行，编排层 §3.5）。
// 覆盖致命反例：并发安全 / 读写一致 / Depth 字段存在 / 路径穿越拒绝。
// ≤3；落地：trace.jsonl 记录 Depth
// SNAPSHOT: tests={Append_ConcurrentSafe, ReadTrace_RoundTrip, DepthField_Present, PathTraversal_Rejected}

package orchestrator

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestTraceWriter_Append_ConcurrentSafe 两个 goroutine 同时 Append，sync.Mutex 防数据损坏（编排层 §3.5）。
func TestTraceWriter_Append_ConcurrentSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := NewTraceWriter(path)
	if err != nil {
		t.Fatalf("NewTraceWriter: %v", err)
	}
	defer w.Close()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(2)

	// 两个 goroutine 同时 Append
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = w.Append(TraceEntry{
				SubTaskID: "S-concurrent-1",
				Depth:     1,
				Event:     TraceEventDispatch,
				AgentID:   "agent-A",
				Stage:     StageImplement,
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = w.Append(TraceEntry{
				SubTaskID: "S-concurrent-2",
				Depth:     1,
				Event:     TraceEventDispatch,
				AgentID:   "agent-B",
				Stage:     StageImplement,
			})
		}
	}()

	wg.Wait()

	// 读取验证
	entries, err := ReadTrace(path)
	if err != nil {
		t.Fatalf("ReadTrace: %v", err)
	}
	if len(entries) != 2*N {
		t.Fatalf("expected %d entries, got %d (concurrent append corrupted file)", 2*N, len(entries))
	}

	// 验证每条 entry 的 AgentID 是 A 或 B（无交错损坏）
	for _, e := range entries {
		if e.AgentID != "agent-A" && e.AgentID != "agent-B" {
			t.Errorf("corrupted entry: unexpected AgentID %s", e.AgentID)
		}
	}
}

// TestTraceWriter_ReadTrace_RoundTrip Append 3 条后 ReadTrace，验证一致（编排层 §3.5）。
func TestTraceWriter_ReadTrace_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := NewTraceWriter(path)
	if err != nil {
		t.Fatalf("NewTraceWriter: %v", err)
	}

	entries := []TraceEntry{
		{SubTaskID: "S-001", Depth: 1, Event: TraceEventDispatch, AgentID: "agent-1", Stage: StageImplement, Model: ModelSonnet},
		{SubTaskID: "S-001", Depth: 1, Event: TraceEventStart, AgentID: "agent-1", Stage: StageImplement, Model: ModelSonnet},
		{SubTaskID: "S-001", Depth: 1, Event: TraceEventEnd, AgentID: "agent-1", Stage: StageImplement, Model: ModelSonnet},
	}

	for _, e := range entries {
		if err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	read, err := ReadTrace(path)
	if err != nil {
		t.Fatalf("ReadTrace: %v", err)
	}
	if len(read) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(read))
	}

	for i, e := range read {
		if e.SubTaskID != entries[i].SubTaskID {
			t.Errorf("entry %d SubTaskID mismatch: got %s, want %s", i, e.SubTaskID, entries[i].SubTaskID)
		}
		if e.Depth != entries[i].Depth {
			t.Errorf("entry %d Depth mismatch: got %d, want %d", i, e.Depth, entries[i].Depth)
		}
		if e.Event != entries[i].Event {
			t.Errorf("entry %d Event mismatch: got %s, want %s", i, e.Event, entries[i].Event)
		}
		if e.Model != entries[i].Model {
			t.Errorf("entry %d Model mismatch: got %s, want %s", i, e.Model, entries[i].Model)
		}
	}
}

// TestTraceEntry_DepthField_Present TraceEntry 必须含 Depth 字段（可观测）。
func TestTraceEntry_DepthField_Present(t *testing.T) {
	e := TraceEntry{
		SubTaskID: "S-depth-test",
		Depth:     2,
		Event:     TraceEventDispatch,
	}

	if e.Depth != 2 {
		t.Errorf("Depth field not set correctly: got %d, want 2", e.Depth)
	}

	// 验证 JSON 序列化包含 depth 字段
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"depth":2`) {
		t.Errorf("JSON 序列化必须包含 depth 字段，got: %s", string(data))
	}
}

// TestTraceWriter_PathTraversal_Rejected 致命反例：path 含 ".." → 返回 error（路径穿越防护）。
func TestTraceWriter_PathTraversal_Rejected(t *testing.T) {
	// 含 ".." 的 path（用字符串拼接而非 filepath.Join，因为 filepath.Join 会清理 .. 导致检测失效）
	rawPath := t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape" + string(filepath.Separator) + "trace.jsonl"
	_, err := NewTraceWriter(rawPath)
	if err == nil {
		t.Fatal("致命反例：path 含 '..' 时 NewTraceWriter 必须返回 error（路径穿越防护）")
	}

	// 空 path
	_, err = NewTraceWriter("")
	if err == nil {
		t.Fatal("空 path 时 NewTraceWriter 必须返回 error")
	}

	// ReadTrace 同样校验
	rawReadPath := t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape" + string(filepath.Separator) + "trace.jsonl"
	_, err = ReadTrace(rawReadPath)
	if err == nil {
		t.Fatal("ReadTrace path 含 '..' 时必须返回 error")
	}
}
