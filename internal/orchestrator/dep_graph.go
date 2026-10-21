// dep_graph.go — H10 Phase 3 模块 2 受限 DAG 依赖图（H10-Phase3-方案细化-v3.md §6.2）。
// REF-外部-LangGraph@https://langchain-ai.github.io/langgraph/concepts/persistence/+2026-07-15访问-DAG 节点边模型；借鉴逻辑：节点状态 + 边依赖 + 拓扑排序；落地：DepGraph + TaskNode
// 数据结构；落地：nodes/edges/buckets 三 map + RWMutex
// ；落地：buckets 字段对齐 A-H 桶，桶内串行+桶间并行
// 复用-DepGraph 不改 Phase 1 StateMachine 签名；落地：DepGraph 独立于 StateMachine，调度器组合使用
// SNAPSHOT: pkg=internal/orchestrator; type=DepGraph; fields={nodes(map[subtask_id]*TaskNode),edges(map[subtask_id][]string),buckets(map[subtask_id]bucket)}; cycle_detection=DFS; failure_propagation=1层反向边(非全图传递闭包); buckets=A-H(02§3); concurrency=sync.RWMutex

package orchestrator

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// BucketA-H 02 §3 分桶标识常量（桶间并行 + 桶内串行）。
const (
	BucketA = "A"
	BucketB = "B"
	BucketC = "C"
	BucketD = "D"
	BucketE = "E"
	BucketF = "F"
	BucketG = "G"
	BucketH = "H"
)

// bucketOrder 02 §3 桶顺序（A 优先级最高 > H 最低）。
var bucketOrder = map[string]int{
	BucketA: 0, BucketB: 1, BucketC: 2, BucketD: 3,
	BucketE: 4, BucketF: 5, BucketG: 6, BucketH: 7,
}

// DFS 三色标记法颜色常量（环检测用）。
const (
	colorWhite = 0 // 未访问
	colorGray  = 1 // 访问中（在递归栈中）
	colorBlack = 2 // 已完成
)

// TaskNode 依赖图节点（Phase 3 §6.2.1）。
type TaskNode struct {
	ID          string     `json:"id"`
	Bucket      string     `json:"bucket"` // A-H（02 §3 分桶）
	Status      NodeStatus `json:"status"`
	Priority    int        `json:"priority"`     // 0-100，默认按 bucket 顺序
	DynamicDeps []string   `json:"dynamic_deps"` // 运行时新增的依赖
	DetectedAt  time.Time  `json:"detected_at"`
}

// NodeStatus 节点状态枚举（Phase 3 §6.2.1）。
type NodeStatus int

const (
	StatusPending NodeStatus = iota
	StatusRunning
	StatusDone
	StatusFailed
	StatusBlocked // 等待动态依赖
)

// String 返回 NodeStatus 的字符串表示，便于日志与 trace。
func (s NodeStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusDone:
		return "done"
	case StatusFailed:
		return "failed"
	case StatusBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// DepGraph 受限 DAG 依赖图，从线性 Stage 扩展为桶间并行+桶内串行（Phase 3 §6.2.1）。
// 三 map 设计：nodes 节点元数据 / edges 依赖边（key 依赖 val 列表）/ buckets 节点所属桶。
// 并发保护：sync.RWMutex，AddEdge/TopologicalSort 等加锁。
type DepGraph struct {
	mu      sync.RWMutex
	nodes   map[string]*TaskNode // key = subtask_id
	edges   map[string][]string  // key = subtask_id, val = 依赖的 subtask_id 列表
	buckets map[string]string    // key = subtask_id, val = bucket (A-H)
}

// NewDepGraph 创建空依赖图。
func NewDepGraph() *DepGraph {
	return &DepGraph{
		nodes:   make(map[string]*TaskNode),
		edges:   make(map[string][]string),
		buckets: make(map[string]string),
	}
}

// AddNode 添加节点到依赖图（Phase 3 §6.2.1）。
// 校验：节点 ID 非空 + 不重复 + Bucket 合法（A-H）。
func (g *DepGraph) AddNode(node *TaskNode) error {
	if node == nil {
		return fmt.Errorf("dep_graph: node must not be nil")
	}
	if node.ID == "" {
		return fmt.Errorf("dep_graph: node ID must not be empty")
	}
	if _, ok := bucketOrder[node.Bucket]; !ok {
		return fmt.Errorf("dep_graph: invalid bucket %q (must be A-H)", node.Bucket)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[node.ID]; exists {
		return fmt.Errorf("dep_graph: node %s already exists", node.ID)
	}

	// 复制节点避免外部修改
	cloned := *node
	if cloned.DynamicDeps == nil {
		cloned.DynamicDeps = []string{}
	}
	if cloned.DetectedAt.IsZero() {
		cloned.DetectedAt = time.Now()
	}
	if cloned.Priority == 0 {
		cloned.Priority = 100 - bucketOrder[cloned.Bucket]*10 // A=100, B=90, ...
	}

	g.nodes[node.ID] = &cloned
	g.buckets[node.ID] = node.Bucket
	if _, ok := g.edges[node.ID]; !ok {
		g.edges[node.ID] = []string{}
	}
	return nil
}

// AddEdge 添加依赖边（from 依赖 to），添加前做循环检测（致命反例 2）。
// 语义：from 必须等 to 完成才能执行（to 是 from 的前置依赖）。
// 致命反例 2：A→A 自环 / A→B→A 间接环 / A→B→C→A 三节点环 必须拒绝。
func (g *DepGraph) AddEdge(from, to string) error {
	if from == "" || to == "" {
		return fmt.Errorf("dep_graph: edge endpoints must not be empty (from=%q, to=%q)", from, to)
	}
	if from == to {
		return fmt.Errorf("dep_graph: self-loop detected: %s → %s (致命反例 2)", from, to)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.nodes[from]; !ok {
		return fmt.Errorf("dep_graph: node %s not found (from)", from)
	}
	if _, ok := g.nodes[to]; !ok {
		return fmt.Errorf("dep_graph: node %s not found (to)", to)
	}

	// 检查是否已存在相同边
	for _, existing := range g.edges[from] {
		if existing == to {
			return nil // 幂等：已存在不重复添加
		}
	}

	// 先添加边
	g.edges[from] = append(g.edges[from], to)

	// 环检测：添加后若产生环，回滚
	if g.hasCycleLocked() {
		// 回滚：移除刚添加的边
		g.edges[from] = removeString(g.edges[from], to)
		return fmt.Errorf("dep_graph: cycle detected when adding edge %s → %s (致命反例 2)", from, to)
	}

	return nil
}

// AddDynamicDep 运行时新增动态依赖（Phase 3 §6.2.3，trace 事件触发）。
// reason 记录动态依赖产生原因（写入 trace）。
func (g *DepGraph) AddDynamicDep(from, to, reason string) error {
	if reason == "" {
		reason = "dynamic dependency detected"
	}

	if err := g.AddEdge(from, to); err != nil {
		return fmt.Errorf("dep_graph: add dynamic dep: %w (reason=%s)", err, reason)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	node, ok := g.nodes[from]
	if !ok {
		return nil // 边已添加，节点状态更新失败不回滚
	}
	// 记录动态依赖标记
	node.DynamicDeps = append(node.DynamicDeps, to)
	return nil
}

// TopologicalSort 拓扑排序，返回可派发顺序；存在环返回 error（Phase 3 §6.2.2）。
// Kahn 算法：入度 0 入队 → 出队 → 减下游入度 → 递归。
// 边语义：edges[from]=[to,...] 表示 from 依赖 to，to 应先执行 → from 的入度 += len(edges[from])。
// 排序稳定性：同入度 0 节点按 bucket 顺序 + priority 排序。
func (g *DepGraph) TopologicalSort() ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.hasCycleLocked() {
		return nil, fmt.Errorf("dep_graph: cycle detected, cannot topological sort")
	}

	// 计算入度：edges[from] = [to, ...] 表示 from 依赖 to → from 被阻塞 → from 入度 += len
	inDegree := make(map[string]int, len(g.nodes))
	for id := range g.nodes {
		inDegree[id] = 0
	}
	for from, deps := range g.edges {
		inDegree[from] += len(deps)
	}

	// Kahn 算法
	var sorted []string
	queue := make([]string, 0)
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	// 排序：按 bucket 顺序 + priority（高优先级先出）
	sort.SliceStable(queue, func(i, j int) bool {
		return g.nodePriorityLocked(queue[i]) > g.nodePriorityLocked(queue[j])
	})

	for len(queue) > 0 {
		// 出队
		current := queue[0]
		queue = queue[1:]
		sorted = append(sorted, current)

		// 找所有依赖 current 的节点（即 edges 中 to=current 的 from）
		// 减少它们的入度，入度归 0 入队
		var newReady []string
		for from, deps := range g.edges {
			for _, dep := range deps {
				if dep == current {
					inDegree[from]--
					if inDegree[from] == 0 {
						newReady = append(newReady, from)
					}
					break
				}
			}
		}
		// 按优先级排序后合并到队列（保持整体优先级排序）
		sort.SliceStable(newReady, func(i, j int) bool {
			return g.nodePriorityLocked(newReady[i]) > g.nodePriorityLocked(newReady[j])
		})
		queue = mergeByPriority(queue, newReady, g.nodePriorityLocked)
	}

	if len(sorted) != len(g.nodes) {
		return nil, fmt.Errorf("dep_graph: topological sort incomplete (sorted=%d, nodes=%d), cycle may exist", len(sorted), len(g.nodes))
	}
	return sorted, nil
}

// nodePriorityLocked 返回节点的优先级（持读锁，桶顺序 + priority）。
func (g *DepGraph) nodePriorityLocked(id string) int {
	node, ok := g.nodes[id]
	if !ok {
		return 0
	}
	// priority 主排序，bucket 顺序作为加成
	return node.Priority*10 + (100 - bucketOrder[node.Bucket])
}

// mergeByPriority 按优先级合并两个已排序的队列。
func mergeByPriority(a, b []string, priority func(string) int) []string {
	result := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if priority(a[i]) >= priority(b[j]) {
			result = append(result, a[i])
			i++
		} else {
			result = append(result, b[j])
			j++
		}
	}
	result = append(result, a[i:]...)
	result = append(result, b[j:]...)
	return result
}

// ReadyToRun 返回入度为 0 且 status=pending 的节点列表（Phase 3 §6.2.2）。
// 排序：按 bucket 顺序（A > B > ... > H）+ priority 降序。
func (g *DepGraph) ReadyToRun() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// 计算入度
	inDegree := make(map[string]int, len(g.nodes))
	for id := range g.nodes {
		inDegree[id] = 0
	}
	for from, deps := range g.edges {
		inDegree[from] += len(deps)
	}
	// 减去已完成依赖
	for from, deps := range g.edges {
		remaining := 0
		for _, dep := range deps {
			if node, ok := g.nodes[dep]; ok && node.Status != StatusDone {
				remaining++
			}
		}
		inDegree[from] = remaining
	}

	var ready []string
	for id, node := range g.nodes {
		if node.Status == StatusPending && inDegree[id] == 0 {
			ready = append(ready, id)
		}
	}

	// 按 bucket 顺序 + priority 排序
	sort.SliceStable(ready, func(i, j int) bool {
		return g.nodePriorityLocked(ready[i]) > g.nodePriorityLocked(ready[j])
	})
	return ready
}

// MarkDone 标记节点完成（Phase 3 §6.2.2）。
func (g *DepGraph) MarkDone(subtaskID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	node, ok := g.nodes[subtaskID]
	if !ok {
		return fmt.Errorf("dep_graph: node %s not found", subtaskID)
	}
	node.Status = StatusDone
	return nil
}

// MarkFailed 标记节点失败，仅遍历 1 层反向边标记直接下游为 blocked（Phase 3 §6.2.2）。
// 致命反例防护：非全图传递闭包，仅 1 层反向边传播。
// 例：A→B→C，A 失败 → B 标 blocked，C 不标 blocked（C 的直接依赖是 B 不是 A）。
func (g *DepGraph) MarkFailed(subtaskID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	node, ok := g.nodes[subtaskID]
	if !ok {
		return fmt.Errorf("dep_graph: node %s not found", subtaskID)
	}
	node.Status = StatusFailed

	// 仅 1 层反向边：找所有直接依赖 subtaskID 的节点（edges 中 to=subtaskID 的 from）
	for from, deps := range g.edges {
		for _, dep := range deps {
			if dep == subtaskID {
				if downstream, ok := g.nodes[from]; ok {
					// 仅当下游还 pending/running 时标 blocked（已 done/failed 不覆盖）
					if downstream.Status == StatusPending || downstream.Status == StatusRunning {
						downstream.Status = StatusBlocked
					}
				}
				break // 一个 from 只标一次
			}
		}
	}
	return nil
}

// HasCycle 检测图中是否存在环（DFS 三色环检测，致命反例 2）。
// 三色：白（未访问）/ 灰（访问中，在递归栈）/ 黑（已完成）。
func (g *DepGraph) HasCycle() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.hasCycleLocked()
}

// hasCycleLocked 持读锁的环检测（DFS 三色标记法）。
// 遇到灰色节点即环（说明存在回边）。
func (g *DepGraph) hasCycleLocked() bool {
	color := make(map[string]int, len(g.nodes))
	for id := range g.nodes {
		color[id] = colorWhite
	}

	// DFS 遍历所有节点
	for id := range g.nodes {
		if color[id] == colorWhite {
			if g.dfsCycleLocked(id, color) {
				return true
			}
		}
	}
	return false
}

// dfsCycleLocked DFS 三色环检测（持读锁）。
// 返回 true 表示发现环。
func (g *DepGraph) dfsCycleLocked(nodeID string, color map[string]int) bool {
	color[nodeID] = colorGray

	// 遍历 nodeID 依赖的所有节点（edges[nodeID] = [to, ...]）
	// nodeID 依赖 to，所以遍历 to
	for _, dep := range g.edges[nodeID] {
		if color[dep] == colorGray {
			// 遇到灰色节点 → 回边 → 环
			return true
		}
		if color[dep] == colorWhite {
			if g.dfsCycleLocked(dep, color) {
				return true
			}
		}
	}

	color[nodeID] = colorBlack
	return false
}

// GetNode 获取节点（只读，返回副本避免外部修改）。
func (g *DepGraph) GetNode(subtaskID string) (*TaskNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, ok := g.nodes[subtaskID]
	if !ok {
		return nil, false
	}
	cloned := *node
	return &cloned, true
}

// Count 返回节点数量。
func (g *DepGraph) Count() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// BucketOf 返回节点所属桶。
func (g *DepGraph) BucketOf(subtaskID string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	bucket, ok := g.buckets[subtaskID]
	return bucket, ok
}

// removeString 从切片中移除第一个匹配的字符串。
func removeString(slice []string, s string) []string {
	for i, v := range slice {
		if v == s {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}
