// scheduler.go — 模块 6 依赖图 + 调度器（H10 Phase 1 编排层 §3.1，单串行）。
// + 调度；落地：DependencyGraph + Scheduler，Phase 1 单串行
// 1 单串行；落地：Scheduler 一次只派一个子任务
// SNAPSHOT: pkg=internal/orchestrator; type=Scheduler; phase=1(单串行); concurrency=1(Phase3才扩展并行); graph=线性(非DAG); next_ready=按顺序取第一个pending

package orchestrator

import (
	"fmt"
	"sync"
)

// DependencyGraph 依赖图（总纲 §6.2，Phase 1 单串行，依赖图退化为线性序列）。
// Phase 3 才扩展为 DAG 拓扑排序。
type DependencyGraph struct {
	mu    sync.RWMutex
	nodes map[string]*DepNode
	order []string // 线性顺序（Phase 1 单串行）
}

// DepNode 依赖图节点。
type DepNode struct {
	SubTaskID string
	DependsOn []string
	Stage     string
	Status    string
	SubTask   SubTaskDef // 关联的子任务定义
}

// NewDependencyGraph 创建空依赖图。
func NewDependencyGraph() *DependencyGraph {
	return &DependencyGraph{
		nodes: make(map[string]*DepNode),
	}
}

// AddNode 添加节点到依赖图（Phase 1 按添加顺序为线性序列）。
func (g *DependencyGraph) AddNode(node DepNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes[node.SubTaskID] = &node
	g.order = append(g.order, node.SubTaskID)
}

// GetNode 获取节点。
func (g *DependencyGraph) GetNode(subtaskID string) (*DepNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	node, ok := g.nodes[subtaskID]
	return node, ok
}

// UpdateStatus 更新节点状态。
func (g *DependencyGraph) UpdateStatus(subtaskID, status string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	node, ok := g.nodes[subtaskID]
	if !ok {
		return fmt.Errorf("scheduler: node %s not found", subtaskID)
	}
	node.Status = status
	return nil
}

// Order 返回线性顺序的 SubTaskID 列表（Phase 1 单串行）。
func (g *DependencyGraph) Order() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]string, len(g.order))
	copy(result, g.order)
	return result
}

// Count 返回节点数量。
func (g *DependencyGraph) Count() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// Scheduler 调度器（Phase 1 单串行，编排层 §3.1）。
type Scheduler struct {
	mu         sync.Mutex
	graph      *DependencyGraph
	dispatcher *Dispatcher
	// runningCount 当前 running 的子任务数（Phase 1 单串行：≤1）
	runningCount int
}

// NewScheduler 创建调度器。
func NewScheduler(graph *DependencyGraph, dispatcher *Dispatcher) *Scheduler {
	return &Scheduler{
		graph:      graph,
		dispatcher: dispatcher,
	}
}

// NextReady 返回下一个可派发的子任务（Phase 1：按线性顺序取第一个 pending 且依赖已完成的）。
func (s *Scheduler) NextReady() *SubTaskDef {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Phase 1 单串行：同一时间只允许 1 个子任务 running
	if s.runningCount > 0 {
		return nil
	}

	// 按线性顺序找第一个 pending 且依赖全部 completed 的节点
	for _, id := range s.graph.Order() {
		node, ok := s.graph.GetNode(id)
		if !ok {
			continue
		}
		if node.Status != SubTaskStatusPending {
			continue
		}
		// 检查依赖是否全部 completed
		depsOk := true
		for _, dep := range node.DependsOn {
			depNode, ok := s.graph.GetNode(dep)
			if !ok || depNode.Status != SubTaskStatusCompleted {
				depsOk = false
				break
			}
		}
		if depsOk {
			subtask := node.SubTask
			return &subtask
		}
	}
	return nil
}

// MarkRunning 标记子任务为 running（Phase 1 单串行：runningCount++）。
func (s *Scheduler) MarkRunning(subtaskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.runningCount > 0 {
		return fmt.Errorf("scheduler: phase1 serial: another subtask already running (runningCount=%d)", s.runningCount)
	}

	if err := s.graph.UpdateStatus(subtaskID, SubTaskStatusRunning); err != nil {
		return err
	}
	s.runningCount++
	return nil
}

// MarkCompleted 标记子任务完成，更新依赖图（编排层 §3.1）。
func (s *Scheduler) MarkCompleted(subtaskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.graph.UpdateStatus(subtaskID, SubTaskStatusCompleted); err != nil {
		return err
	}
	if s.runningCount > 0 {
		s.runningCount--
	}
	return nil
}

// MarkFailed 标记子任务失败（按 OnFail 处理：retry_once/interrupt/abort，编排层 §2.3）。
func (s *Scheduler) MarkFailed(subtaskID string, onFail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch onFail {
	case OnFailRetryOnce:
		// 子任务状态回 pending（最多修一次）
		if err := s.graph.UpdateStatus(subtaskID, SubTaskStatusPending); err != nil {
			return err
		}
		if s.runningCount > 0 {
			s.runningCount--
		}
	case OnFailInterrupt:
		if err := s.graph.UpdateStatus(subtaskID, SubTaskStatusInterrupted); err != nil {
			return err
		}
		if s.runningCount > 0 {
			s.runningCount--
		}
	case OnFailAbort:
		if err := s.graph.UpdateStatus(subtaskID, SubTaskStatusInterrupted); err != nil {
			return err
		}
		if s.runningCount > 0 {
			s.runningCount--
		}
	default:
		return fmt.Errorf("scheduler: unknown on_fail %q", onFail)
	}
	return nil
}

// IsPhase1Serial Phase 1 单串行校验：同一时间只允许 1 个子任务 running。
func (s *Scheduler) IsPhase1Serial() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningCount <= 1
}

// AllCompleted 检查所有子任务是否全部 completed。
func (s *Scheduler) AllCompleted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range s.graph.Order() {
		node, ok := s.graph.GetNode(id)
		if !ok {
			continue
		}
		if node.Status != SubTaskStatusCompleted {
			return false
		}
	}
	return true
}

// RunningCount 返回当前 running 的子任务数（仅用于诊断）。
func (s *Scheduler) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningCount
}
