package tools

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newMockExecutor 创建一个只有 mock 处理器的执行器（不注册内置处理器）
func newMockExecutor() *Executor {
	return &Executor{
		handlers: make(map[string]Handler),
		registry: NewRegistry(),
	}
}

// setMockHandler 设置 mock 处理器（直接写入 handlers map，允许覆盖）
func setMockHandler(e *Executor, name string, handler Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[name] = handler
}

// TestScheduler_SafeTool_Concurrent test security工具可以并发执行
func TestScheduler_SafeTool_Concurrent(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	// 使用原子计数器跟踪并发数
	var concurrentCount int64
	var maxConcurrent int64

	// 设置mock的 read_file 处理器，用于检测并发
	setMockHandler(executor, "read_file", func(ctx context.Context, raw json.RawMessage) (string, error) {
		current := atomic.AddInt64(&concurrentCount, 1)
		defer atomic.AddInt64(&concurrentCount, -1)

		// 记录最大并发数
		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if current <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, current) {
				break
			}
		}

		// mockduration操作
		time.Sleep(50 * time.Millisecond)
		return "ok", nil
	})

	// 添加并标记多个安全工具为就绪
	const count = 5
	for i := 0; i < count; i++ {
		id := string(rune('a' + i))
		input, _ := json.Marshal(map[string]string{"path": "/tmp/test.txt"})
		scheduler.AddPending(id, "read_file", string(input))
		scheduler.MarkReady(id)
	}

	results := scheduler.WaitAll()
	if len(results) != count {
		t.Fatalf("expected %d 个结果，actual %d 个", count, len(results))
	}

	// 安全工具应该能并发执行，最大并发数should be greater than 1
	max := atomic.LoadInt64(&maxConcurrent)
	if max <= 1 {
		t.Errorf("安全工具应并发执行，但最大并发数仅为 %d", max)
	}
}

// TestScheduler_BashTool_Mutex 测试 bash 工具顺序执行（全局互斥锁）
func TestScheduler_BashTool_Mutex(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var concurrentCount int64
	var maxConcurrent int64

	// 设置mock的 bash 处理器
	setMockHandler(executor, "bash", func(ctx context.Context, raw json.RawMessage) (string, error) {
		current := atomic.AddInt64(&concurrentCount, 1)
		defer atomic.AddInt64(&concurrentCount, -1)

		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if current <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, current) {
				break
			}
		}

		time.Sleep(50 * time.Millisecond)
		return "ok", nil
	})

	const count = 3
	for i := 0; i < count; i++ {
		id := string(rune('a' + i))
		input, _ := json.Marshal(map[string]string{"command": "echo hello"})
		scheduler.AddPending(id, "bash", string(input))
		scheduler.MarkReady(id)
	}

	results := scheduler.WaitAll()
	if len(results) != count {
		t.Fatalf("expected %d 个结果，actual %d 个", count, len(results))
	}

	// bash 工具应顺序执行，最大并发数应为 1
	max := atomic.LoadInt64(&maxConcurrent)
	if max > 1 {
		t.Errorf("bash 工具应顺序执行，但最大并发数为 %d", max)
	}
}

// TestScheduler_FileTool_FileLock 测试文件工具按文件路径加锁
func TestScheduler_FileTool_FileLock(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var fileAConcurrent int64
	var fileBConcurrent int64
	var fileAMaxConcurrent int64
	var fileBMaxConcurrent int64

	// 设置mock的 write_file 处理器
	setMockHandler(executor, "write_file", func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Path string `json:"path"`
		}
		json.Unmarshal(raw, &input)

		time.Sleep(50 * time.Millisecond)

		if input.Path == "/tmp/a.txt" {
			current := atomic.AddInt64(&fileAConcurrent, 1)
			defer atomic.AddInt64(&fileAConcurrent, -1)
			for {
				old := atomic.LoadInt64(&fileAMaxConcurrent)
				if current <= old || atomic.CompareAndSwapInt64(&fileAMaxConcurrent, old, current) {
					break
				}
			}
		} else {
			current := atomic.AddInt64(&fileBConcurrent, 1)
			defer atomic.AddInt64(&fileBConcurrent, -1)
			for {
				old := atomic.LoadInt64(&fileBMaxConcurrent)
				if current <= old || atomic.CompareAndSwapInt64(&fileBMaxConcurrent, old, current) {
					break
				}
			}
		}

		return "ok", nil
	})

	// 同一文件的两个写操作
	inputA1, _ := json.Marshal(map[string]string{"path": "/tmp/a.txt", "content": "a1"})
	inputA2, _ := json.Marshal(map[string]string{"path": "/tmp/a.txt", "content": "a2"})
	// 不同文件的写操作
	inputB1, _ := json.Marshal(map[string]string{"path": "/tmp/b.txt", "content": "b1"})

	scheduler.AddPending("a1", "write_file", string(inputA1))
	scheduler.AddPending("a2", "write_file", string(inputA2))
	scheduler.AddPending("b1", "write_file", string(inputB1))

	scheduler.MarkReady("a1")
	scheduler.MarkReady("a2")
	scheduler.MarkReady("b1")

	results := scheduler.WaitAll()
	if len(results) != 3 {
		t.Fatalf("expected 3 个结果，actual %d 个", len(results))
	}

	// 同一文件的操作应顺序执行
	maxA := atomic.LoadInt64(&fileAMaxConcurrent)
	if maxA > 1 {
		t.Errorf("同一文件的写操作应顺序执行，但最大并发数为 %d", maxA)
	}
}

// TestScheduler_UnsafeTool_Mutex 测试非安全工具顺序执行
func TestScheduler_UnsafeTool_Mutex(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var concurrentCount int64
	var maxConcurrent int64

	// 设置mock的 Agent 处理器（Agent 是非安全工具）
	setMockHandler(executor, "Agent", func(ctx context.Context, raw json.RawMessage) (string, error) {
		current := atomic.AddInt64(&concurrentCount, 1)
		defer atomic.AddInt64(&concurrentCount, -1)

		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if current <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, current) {
				break
			}
		}

		time.Sleep(50 * time.Millisecond)
		return "ok", nil
	})

	const count = 3
	for i := 0; i < count; i++ {
		id := string(rune('a' + i))
		input, _ := json.Marshal(map[string]string{"prompt": "test"})
		scheduler.AddPending(id, "Agent", string(input))
		scheduler.MarkReady(id)
	}

	results := scheduler.WaitAll()
	if len(results) != count {
		t.Fatalf("expected %d 个结果，actual %d 个", count, len(results))
	}

	// 非安全工具应顺序执行
	max := atomic.LoadInt64(&maxConcurrent)
	if max > 1 {
		t.Errorf("非安全工具应顺序执行，但最大并发数为 %d", max)
	}
}

// TestScheduler_CancelAll test cancelled所有待执行工具
func TestScheduler_CancelAll(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	// 设置一个会阻塞的处理器
	setMockHandler(executor, "bash", func(ctx context.Context, raw json.RawMessage) (string, error) {
		select {
		case <-time.After(5 * time.Second):
			return "ok", nil
		case <-ctx.Done():
			return "cancelled", ctx.Err()
		}
	})

	// 添加一个 bash 工具
	input, _ := json.Marshal(map[string]string{"command": "sleep 5"})
	scheduler.AddPending("1", "bash", string(input))
	scheduler.MarkReady("1")

	// 等待工具开始执行
	time.Sleep(50 * time.Millisecond)

	// 取消所有
	cancelled := scheduler.CancelAll()
	if len(cancelled) == 0 {
		t.Error("应至少取消一个工具")
	}
}

// TestScheduler_WaitAll 测试等待所有工具执行完成
func TestScheduler_WaitAll(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	setMockHandler(executor, "read_file", func(ctx context.Context, raw json.RawMessage) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return "content", nil
	})

	// 添加多个工具
	for i := 0; i < 3; i++ {
		id := string(rune('a' + i))
		input, _ := json.Marshal(map[string]string{"path": "/tmp/test.txt"})
		scheduler.AddPending(id, "read_file", string(input))
		scheduler.MarkReady(id)
	}

	results := scheduler.WaitAll()
	if len(results) != 3 {
		t.Errorf("expected 3 个结果，actual %d 个", len(results))
	}

	// 验证所有结果都不是错误
	for _, r := range results {
		if r.IsError {
			t.Errorf("结果不should be error: %s", r.Output)
		}
	}
}

// TestScheduler_RunningCount 测试跟踪正在执行的工具数
func TestScheduler_RunningCount(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var started sync.WaitGroup
	block := make(chan struct{})

	setMockHandler(executor, "bash", func(ctx context.Context, raw json.RawMessage) (string, error) {
		started.Done()
		<-block
		return "ok", nil
	})

	// 添加一个会阻塞的 bash 工具
	input, _ := json.Marshal(map[string]string{"command": "echo test"})
	started.Add(1)
	scheduler.AddPending("1", "bash", string(input))
	scheduler.MarkReady("1")

	// 等待工具开始执行
	started.Wait()

	// 检查运行数
	running := scheduler.RunningCount()
	if running != 1 {
		t.Errorf("运行数expected 1，actual %d", running)
	}

	// 释放阻塞
	close(block)

	// 等待完成
	scheduler.WaitAll()

	running = scheduler.RunningCount()
	if running != 0 {
		t.Errorf("完成后运行数expected 0，actual %d", running)
	}
}

// TestScheduler_MaxConcurrency 测试最大并发限制（10）
func TestScheduler_MaxConcurrency(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var concurrentCount int64
	var maxConcurrent int64

	setMockHandler(executor, "read_file", func(ctx context.Context, raw json.RawMessage) (string, error) {
		current := atomic.AddInt64(&concurrentCount, 1)
		defer atomic.AddInt64(&concurrentCount, -1)

		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if current <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, current) {
				break
			}
		}

		time.Sleep(50 * time.Millisecond)
		return "ok", nil
	})

	// 添加超过最大并发数的安全工具
	const count = 15
	for i := 0; i < count; i++ {
		id := string(rune('a' + i))
		input, _ := json.Marshal(map[string]string{"path": "/tmp/test.txt"})
		scheduler.AddPending(id, "read_file", string(input))
		scheduler.MarkReady(id)
	}

	results := scheduler.WaitAll()
	if len(results) != count {
		t.Fatalf("expected %d 个结果，actual %d 个", count, len(results))
	}

	// 最大并发数不应超过 10
	max := atomic.LoadInt64(&maxConcurrent)
	if max > 10 {
		t.Errorf("最大并发数不应超过 10，actual为 %d", max)
	}
}

// TestScheduler_AddPendingAndMarkReady 测试 AddPending 后 MarkReady 触发执行
func TestScheduler_AddPendingAndMarkReady(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var executed int64

	setMockHandler(executor, "read_file", func(ctx context.Context, raw json.RawMessage) (string, error) {
		atomic.AddInt64(&executed, 1)
		return "executed", nil
	})

	input, _ := json.Marshal(map[string]string{"path": "/tmp/test.txt"})

	// 添加 pending 但不标记 ready
	scheduler.AddPending("1", "read_file", string(input))

	// 此时不应执行
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt64(&executed) != 0 {
		t.Error("未 MarkReady 时不应执行")
	}

	// 标记 ready 后应触发执行
	scheduler.MarkReady("1")

	results := scheduler.WaitAll()
	if len(results) != 1 {
		t.Fatalf("expected 1 个结果，actual %d 个", len(results))
	}
	if results[0].Output != "executed" {
		t.Errorf("Output = %q, want %q", results[0].Output, "executed")
	}
}

// TestScheduler_SetInputDelta 测试多次 SetInputDelta 追加输入
func TestScheduler_SetInputDelta(t *testing.T) {
	executor := newMockExecutor()
	ctx := context.Background()
	scheduler := NewToolScheduler(ctx, executor)

	var capturedInput string

	setMockHandler(executor, "read_file", func(ctx context.Context, raw json.RawMessage) (string, error) {
		capturedInput = string(raw)
		return "ok", nil
	})

	// 分段追加输入
	scheduler.AddPending("1", "read_file", `{"pa`)
	scheduler.SetInputDelta("1", `th":`)
	scheduler.SetInputDelta("1", `"/tmp/test.txt"}`)
	scheduler.MarkReady("1")

	results := scheduler.WaitAll()
	if len(results) != 1 {
		t.Fatalf("expected 1 个结果，actual %d 个", len(results))
	}

	// 验证输入被正确拼接
	expectedInput := `{"path":"/tmp/test.txt"}`
	if capturedInput != expectedInput {
		t.Errorf("拼接后的输入 = %q, want %q", capturedInput, expectedInput)
	}
}
