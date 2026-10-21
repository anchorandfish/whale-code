// scheduler_test.go — 模块 6 依赖图 + 调度器反例测试（TDD 先行，编排层 §3.1）。
// 覆盖致命反例：Phase 1 单串行 / 依赖未完成跳过 / 全部 completed 返回 nil / OnFail 处理。
// + 调度；落地：DependencyGraph + Scheduler，Phase 1 单串行
// SNAPSHOT: tests={Phase1Serial_OneRunningAtATime, NextReady_DependsOnNotCompleted_ReturnsNil, NextReady_AllCompleted_ReturnsNil, MarkFailed_RetryOnce, MarkFailed_Interrupt}

package orchestrator

import (
	"testing"
)

// makeTestScheduler 构造测试用 Scheduler（含 3 个 pending 子任务）。
func makeTestScheduler(t *testing.T) (*Scheduler, *DependencyGraph) {
	t.Helper()
	graph := NewDependencyGraph()

	// 添加 3 个 pending 子任务
	graph.AddNode(DepNode{
		SubTaskID: "S-001",
		Stage:     StageImplement,
		Status:    SubTaskStatusPending,
		SubTask:   SubTaskDef{SubTaskID: "S-001", Type: TaskTypeImplement, Complexity: ComplexityT1},
	})
	graph.AddNode(DepNode{
		SubTaskID: "S-002",
		Stage:     StageImplement,
		Status:    SubTaskStatusPending,
		SubTask:   SubTaskDef{SubTaskID: "S-002", Type: TaskTypeImplement, Complexity: ComplexityT2},
	})
	graph.AddNode(DepNode{
		SubTaskID: "S-003",
		Stage:     StageImplement,
		Status:    SubTaskStatusPending,
		SubTask:   SubTaskDef{SubTaskID: "S-003", Type: TaskTypeImplement, Complexity: ComplexityT3},
	})

	d, _, _ := makeTestDispatcher(t)
	s := NewScheduler(graph, d)
	return s, graph
}

// TestScheduler_Phase1Serial_OneRunningAtATime 致命反例：Phase 1 同一时间只允许 1 个子任务 running。
// 第二个 NextReady 在第一个未 MarkCompleted 前返回 nil。
func TestScheduler_Phase1Serial_OneRunningAtATime(t *testing.T) {
	s, _ := makeTestScheduler(t)

	// 第一次 NextReady → 返回 S-001
	first := s.NextReady()
	if first == nil {
		t.Fatal("第一次 NextReady 应返回 S-001")
	}
	if first.SubTaskID != "S-001" {
		t.Fatalf("expected S-001, got %s", first.SubTaskID)
	}

	// 标记 S-001 为 running
	if err := s.MarkRunning("S-001"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	// 第二次 NextReady → 应返回 nil（Phase 1 单串行，S-001 仍 running）
	second := s.NextReady()
	if second != nil {
		t.Fatalf("致命反例：Phase 1 单串行，S-001 仍 running 时 NextReady 必须返回 nil，got %s", second.SubTaskID)
	}

	// MarkCompleted 后再调 NextReady → 返回 S-002
	if err := s.MarkCompleted("S-001"); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	third := s.NextReady()
	if third == nil {
		t.Fatal("MarkCompleted 后 NextReady 应返回 S-002")
	}
	if third.SubTaskID != "S-002" {
		t.Fatalf("expected S-002, got %s", third.SubTaskID)
	}
}

// TestScheduler_NextReady_DependsOnNotCompleted_ReturnsNil 依赖未完成 → NextReady 跳过该节点。
func TestScheduler_NextReady_DependsOnNotCompleted_ReturnsNil(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddNode(DepNode{
		SubTaskID: "S-dep",
		Stage:     StageImplement,
		Status:    SubTaskStatusPending,
		DependsOn: []string{"S-prereq"}, // 依赖未完成
		SubTask:   SubTaskDef{SubTaskID: "S-dep", Type: TaskTypeImplement},
	})
	// S-prereq 不在图中（未完成）

	d, _, _ := makeTestDispatcher(t)
	s := NewScheduler(graph, d)

	// NextReady → S-dep 依赖未完成，返回 nil
	result := s.NextReady()
	if result != nil {
		t.Fatalf("依赖未完成时 NextReady 应返回 nil，got %s", result.SubTaskID)
	}

	// 添加 S-prereq 并标记完成
	graph.AddNode(DepNode{
		SubTaskID: "S-prereq",
		Status:    SubTaskStatusCompleted,
		SubTask:   SubTaskDef{SubTaskID: "S-prereq", Type: TaskTypeImplement},
	})

	// 现在 S-dep 依赖已完成，但 S-prereq 在 S-dep 之后添加
	// 按线性顺序，S-dep 在前所以应返回 S-dep（依赖已满足）
	result = s.NextReady()
	if result == nil {
		t.Fatal("依赖完成后 NextReady 应返回 S-dep")
	}
}

// TestScheduler_NextReady_AllCompleted_ReturnsNil 所有子任务 completed → NextReady 返回 nil。
func TestScheduler_NextReady_AllCompleted_ReturnsNil(t *testing.T) {
	s, graph := makeTestScheduler(t)

	// 全部标记完成
	for _, id := range graph.Order() {
		_ = graph.UpdateStatus(id, SubTaskStatusCompleted)
	}

	result := s.NextReady()
	if result != nil {
		t.Fatalf("所有子任务 completed 时 NextReady 应返回 nil，got %s", result.SubTaskID)
	}

	if !s.AllCompleted() {
		t.Error("AllCompleted 应返回 true")
	}
}

// TestScheduler_MarkFailed_RetryOnce OnFail=retry_once → 子任务状态回 pending（最多修一次）。
func TestScheduler_MarkFailed_RetryOnce(t *testing.T) {
	s, graph := makeTestScheduler(t)

	// S-001 → running → failed(retry_once) → pending
	_ = s.MarkRunning("S-001")
	if err := s.MarkFailed("S-001", OnFailRetryOnce); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	node, _ := graph.GetNode("S-001")
	if node.Status != SubTaskStatusPending {
		t.Errorf("retry_once 后状态应回 pending，got %s", node.Status)
	}
	if s.RunningCount() != 0 {
		t.Errorf("retry_once 后 runningCount 应为 0，got %d", s.RunningCount())
	}
}

// TestScheduler_MarkFailed_Interrupt OnFail=interrupt → 子任务状态置 interrupted。
func TestScheduler_MarkFailed_Interrupt(t *testing.T) {
	s, graph := makeTestScheduler(t)

	_ = s.MarkRunning("S-001")
	if err := s.MarkFailed("S-001", OnFailInterrupt); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	node, _ := graph.GetNode("S-001")
	if node.Status != SubTaskStatusInterrupted {
		t.Errorf("interrupt 后状态应为 interrupted，got %s", node.Status)
	}
	if s.RunningCount() != 0 {
		t.Errorf("interrupt 后 runningCount 应为 0，got %d", s.RunningCount())
	}
}

// TestScheduler_IsPhase1Serial Phase 1 单串行校验始终为 true。
func TestScheduler_IsPhase1Serial(t *testing.T) {
	s, _ := makeTestScheduler(t)
	if !s.IsPhase1Serial() {
		t.Error("Phase 1 单串行校验应始终为 true")
	}
}
