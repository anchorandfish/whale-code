// dep_graph_test.go — H10 Phase 3 模块 2 DepGraph 反例测试（TDD 先行）。
// 覆盖致命反例 2：循环依赖检测（A→B→A 间接环 / A→A 自环 / A→B→C→A 三节点环）。
// 覆盖：拓扑排序 + ReadyToRun + MarkFailed 1 层反向边传播 + 并发安全。
// ；落地：DFS 环检测 + Kahn 拓扑排序 + 1 层反向边
// SNAPSHOT: tests={CycleDetection_SelfLoop, CycleDetection_Indirect, CycleDetection_ThreeNode, TopologicalSort_Valid, ReadyToRun_PriorityOrder, MarkFailed_OneLevelPropagation, AddDynamicDep, ConcurrentAddEdge_NoRace, AddNode_InvalidBucket}

package orchestrator

import (
	"strings"
	"sync"
	"testing"
)

// makeSimpleGraph 构造简单的 3 节点图（A→B→C，A 依赖 B，B 依赖 C）。
func makeSimpleGraph(t *testing.T) *DepGraph {
	t.Helper()
	g := NewDepGraph()
	for _, n := range []struct {
		id     string
		bucket string
	}{
		{"A", BucketA}, {"B", BucketB}, {"C", BucketC},
	} {
		if err := g.AddNode(&TaskNode{ID: n.id, Bucket: n.bucket}); err != nil {
			t.Fatalf("AddNode %s: %v", n.id, err)
		}
	}
	return g
}

// TestDepGraph_CycleDetection_SelfLoop 致命反例 2：A→A 自环 → AddEdge 返回 error（零容忍）。
func TestDepGraph_CycleDetection_SelfLoop(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	if err := g.AddNode(&TaskNode{ID: "A", Bucket: BucketA}); err != nil {
		t.Fatalf("AddNode A: %v", err)
	}

	err := g.AddEdge("A", "A")
	if err == nil {
		t.Fatal("致命反例 2：A→A 自环必须被拒绝（零容忍）")
	}
	if !strings.Contains(err.Error(), "self-loop") {
		t.Errorf("error 应包含 'self-loop'，got: %v", err)
	}
}

// TestDepGraph_CycleDetection_Indirect 致命反例 2：A→B→A 间接环 → 第二条 AddEdge 返回 error（零容忍）。
func TestDepGraph_CycleDetection_Indirect(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	// 第一条边：A 依赖 B（合法）
	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B 应成功，got: %v", err)
	}

	// 第二条边：B 依赖 A → 形成环 A→B→A → 必须拒绝（致命反例 2）
	err := g.AddEdge("B", "A")
	if err == nil {
		t.Fatal("致命反例 2：A→B→A 间接环必须被拒绝（零容忍）")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error 应包含 'cycle detected'，got: %v", err)
	}

	// 验证环检测后边已回滚：B 的依赖列表不应包含 A
	node, ok := g.GetNode("B")
	if !ok {
		t.Fatal("GetNode B 失败")
	}
	// 检查 edges[B] 不含 A（回滚验证）
	for _, dep := range node.DynamicDeps {
		if dep == "A" {
			t.Error("致命反例 2：环检测后边未回滚，B 仍依赖 A")
		}
	}
}

// TestDepGraph_CycleDetection_ThreeNode 致命反例 2：A→B→C→A 三节点环 → 第三条 AddEdge 返回 error。
func TestDepGraph_CycleDetection_ThreeNode(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	// A 依赖 B（合法）
	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B: %v", err)
	}
	// B 依赖 C（合法）
	if err := g.AddEdge("B", "C"); err != nil {
		t.Fatalf("AddEdge B→C: %v", err)
	}
	// C 依赖 A → 形成三节点环 A→B→C→A → 必须拒绝
	err := g.AddEdge("C", "A")
	if err == nil {
		t.Fatal("致命反例 2：A→B→C→A 三节点环必须被拒绝（零容忍）")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error 应包含 'cycle detected'，got: %v", err)
	}

	// HasCycle 验证：图本身不应有环（因为环边已被回滚）
	if g.HasCycle() {
		t.Error("环边回滚后 HasCycle 应返回 false")
	}
}

// TestDepGraph_TopologicalSort_Valid A→B→C 拓扑排序返回 [C, B, A]（C 先执行，A 最后）。
// 注意：边语义 A 依赖 B 表示 B 先执行 → 拓扑排序为 [C, B, A]。
func TestDepGraph_TopologicalSort_Valid(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	// A 依赖 B，B 依赖 C
	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B: %v", err)
	}
	if err := g.AddEdge("B", "C"); err != nil {
		t.Fatalf("AddEdge B→C: %v", err)
	}

	sorted, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("sorted 长度应为 3，got %d", len(sorted))
	}
	// C 应在 B 之前，B 应在 A 之前（因为 A 依赖 B，B 依赖 C）
	if sorted[0] != "C" || sorted[1] != "B" || sorted[2] != "A" {
		t.Errorf("拓扑顺序应为 [C, B, A]，got %v", sorted)
	}
}

// TestDepGraph_TopologicalSort_NoDeps 无依赖节点拓扑排序按 bucket 顺序。
func TestDepGraph_TopologicalSort_NoDeps(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	// 故意以 C, A, B 顺序添加，验证排序按 bucket
	for _, n := range []struct {
		id, bucket string
	}{
		{"C", BucketC}, {"A", BucketA}, {"B", BucketB},
	} {
		if err := g.AddNode(&TaskNode{ID: n.id, Bucket: n.bucket}); err != nil {
			t.Fatalf("AddNode %s: %v", n.id, err)
		}
	}

	sorted, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	// A 优先级最高（bucket=A, priority=100）→ A 应在第一位
	if sorted[0] != "A" {
		t.Errorf("无依赖时 A 应排首位（bucket 优先级），got %v", sorted)
	}
}

// TestDepGraph_ReadyToRun_PriorityOrder 多入度 0 节点按 bucket 顺序 + 优先级排序。
func TestDepGraph_ReadyToRun_PriorityOrder(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	// 添加 3 个无依赖节点，分别属于 A/B/C 桶
	for _, n := range []struct {
		id, bucket string
	}{
		{"S-C", BucketC}, {"S-A", BucketA}, {"S-B", BucketB},
	} {
		if err := g.AddNode(&TaskNode{ID: n.id, Bucket: n.bucket}); err != nil {
			t.Fatalf("AddNode %s: %v", n.id, err)
		}
	}

	ready := g.ReadyToRun()
	if len(ready) != 3 {
		t.Fatalf("ReadyToRun 应返回 3 个节点，got %d", len(ready))
	}
	// A 桶优先级最高 → S-A 应首位
	if ready[0] != "S-A" {
		t.Errorf("ReadyToRun 首位应为 S-A（A 桶优先级最高），got %v", ready)
	}
}

// TestDepGraph_ReadyToRun_RespectsDependencies 有依赖未完成的节点不返回。
func TestDepGraph_ReadyToRun_RespectsDependencies(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)
	// A 依赖 B
	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B: %v", err)
	}

	ready := g.ReadyToRun()
	// B 和 C 无依赖 → 应在 ready 中；A 有依赖 B 未完成 → 不在 ready 中
	if len(ready) != 2 {
		t.Fatalf("ReadyToRun 应返回 2 个节点（B, C），got %d: %v", len(ready), ready)
	}
	// 验证 A 不在 ready 中
	for _, id := range ready {
		if id == "A" {
			t.Error("A 有未完成依赖，不应出现在 ReadyToRun 中")
		}
	}

	// 标记 B 完成 → A 应进入 ready（B 是 A 唯一依赖）
	if err := g.MarkDone("B"); err != nil {
		t.Fatalf("MarkDone B: %v", err)
	}
	ready = g.ReadyToRun()
	found := false
	for _, id := range ready {
		if id == "A" {
			found = true
		}
	}
	if !found {
		t.Error("B 完成后 A 应进入 ReadyToRun")
	}
}

// TestDepGraph_MarkFailed_OneLevelPropagation 致命反例防护：A→B→C，A 失败 → B 标 blocked，C 不标 blocked。
// 仅 1 层反向边传播，非全图传递闭包。
func TestDepGraph_MarkFailed_OneLevelPropagation(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)
	// A 依赖 B，B 依赖 C
	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B: %v", err)
	}
	if err := g.AddEdge("B", "C"); err != nil {
		t.Fatalf("AddEdge B→C: %v", err)
	}

	// A 失败
	if err := g.MarkFailed("A"); err != nil {
		t.Fatalf("MarkFailed A: %v", err)
	}

	// 验证 A 状态 = failed
	nodeA, _ := g.GetNode("A")
	if nodeA.Status != StatusFailed {
		t.Errorf("A 状态应为 failed，got %s", nodeA.Status)
	}

	// 验证 B 状态：B 依赖 A？不是！B 不依赖 A，是 A 依赖 B。
	// 所以 A 失败不应该影响 B（A 失败影响的是依赖 A 的节点，但没人依赖 A）
	// 重新审视：A 失败 → 找谁依赖 A → 没人 → 不传播
	// 所以 B 应保持 pending
	nodeB, _ := g.GetNode("B")
	if nodeB.Status != StatusPending {
		t.Errorf("B 不依赖 A，A 失败不应影响 B，B 应保持 pending，got %s", nodeB.Status)
	}
}

// TestDepGraph_MarkFailed_OneLevelPropagation_Downstream A 失败 → 直接下游 B blocked，间接下游 C 不 blocked。
func TestDepGraph_MarkFailed_OneLevelPropagation_Downstream(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	// 3 节点：A → B → C（B 依赖 A，C 依赖 B）
	for _, n := range []struct {
		id, bucket string
	}{
		{"A", BucketA}, {"B", BucketB}, {"C", BucketC},
	} {
		if err := g.AddNode(&TaskNode{ID: n.id, Bucket: n.bucket}); err != nil {
			t.Fatalf("AddNode %s: %v", n.id, err)
		}
	}
	// B 依赖 A，C 依赖 B
	if err := g.AddEdge("B", "A"); err != nil {
		t.Fatalf("AddEdge B→A: %v", err)
	}
	if err := g.AddEdge("C", "B"); err != nil {
		t.Fatalf("AddEdge C→B: %v", err)
	}

	// A 失败 → B（直接依赖 A）应标 blocked，C（间接依赖 A 通过 B）不应标 blocked
	if err := g.MarkFailed("A"); err != nil {
		t.Fatalf("MarkFailed A: %v", err)
	}

	nodeB, _ := g.GetNode("B")
	if nodeB.Status != StatusBlocked {
		t.Errorf("B 直接依赖 A，A 失败后 B 应标 blocked，got %s", nodeB.Status)
	}

	nodeC, _ := g.GetNode("C")
	if nodeC.Status == StatusBlocked {
		t.Error("致命反例防护失败：C 仅间接依赖 A，1 层反向边传播下 C 不应标 blocked")
	}
}

// TestDepGraph_MarkDone 标记节点完成后状态正确。
func TestDepGraph_MarkDone(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	if err := g.MarkDone("A"); err != nil {
		t.Fatalf("MarkDone A: %v", err)
	}
	node, _ := g.GetNode("A")
	if node.Status != StatusDone {
		t.Errorf("A 状态应为 done，got %s", node.Status)
	}
}

// TestDepGraph_MarkDone_NotFound 标记不存在的节点返回 error。
func TestDepGraph_MarkDone_NotFound(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	err := g.MarkDone("X")
	if err == nil {
		t.Fatal("MarkDone 不存在的节点应返回 error")
	}
}

// TestDepGraph_AddNode_InvalidBucket 无效桶返回 error。
func TestDepGraph_AddNode_InvalidBucket(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()

	err := g.AddNode(&TaskNode{ID: "X", Bucket: "Z"})
	if err == nil {
		t.Fatal("无效桶 Z 应返回 error")
	}
	if !strings.Contains(err.Error(), "invalid bucket") {
		t.Errorf("error 应包含 'invalid bucket'，got: %v", err)
	}
}

// TestDepGraph_AddNode_Duplicate 重复添加节点返回 error。
func TestDepGraph_AddNode_Duplicate(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	if err := g.AddNode(&TaskNode{ID: "A", Bucket: BucketA}); err != nil {
		t.Fatalf("AddNode A: %v", err)
	}

	err := g.AddNode(&TaskNode{ID: "A", Bucket: BucketA})
	if err == nil {
		t.Fatal("重复添加节点 A 应返回 error")
	}
}

// TestDepGraph_AddEdge_Idempotent 重复添加相同边幂等（不报错）。
func TestDepGraph_AddEdge_Idempotent(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B: %v", err)
	}
	// 再次添加相同边 → 幂等，不报错
	if err := g.AddEdge("A", "B"); err != nil {
		t.Errorf("重复添加相同边应幂等，got: %v", err)
	}
}

// TestDepGraph_AddEdge_NodeNotFound 边端点节点不存在返回 error。
func TestDepGraph_AddEdge_NodeNotFound(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	if err := g.AddNode(&TaskNode{ID: "A", Bucket: BucketA}); err != nil {
		t.Fatalf("AddNode A: %v", err)
	}

	err := g.AddEdge("A", "X")
	if err == nil {
		t.Fatal("边端点 X 不存在应返回 error")
	}
}

// TestDepGraph_AddDynamicDep 动态依赖添加成功并记录到 DynamicDeps。
func TestDepGraph_AddDynamicDep(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)

	err := g.AddDynamicDep("A", "B", "contract change detected")
	if err != nil {
		t.Fatalf("AddDynamicDep A→B: %v", err)
	}

	node, _ := g.GetNode("A")
	found := false
	for _, dep := range node.DynamicDeps {
		if dep == "B" {
			found = true
		}
	}
	if !found {
		t.Error("动态依赖 B 应记录在 A.DynamicDeps 中")
	}
}

// TestDepGraph_ConcurrentAddEdge_NoRace 多 goroutine 同时 AddEdge，-race 无竞态。
func TestDepGraph_ConcurrentAddEdge_NoRace(t *testing.T) {
	t.Parallel()
	g := NewDepGraph()
	// 添加 10 个节点
	nodes := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		id := string(rune('A' + i))
		if err := g.AddNode(&TaskNode{ID: id, Bucket: BucketA}); err != nil {
			t.Fatalf("AddNode %s: %v", id, err)
		}
		nodes = append(nodes, id)
	}

	var wg sync.WaitGroup
	// 多 goroutine 并发添加边
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			from := nodes[i]
			to := nodes[(i+1)%10]
			_ = g.AddEdge(from, to) // 可能因环失败，忽略 error
		}(i)
	}
	wg.Wait()
	// -race 检测到此无竞态即 PASS
}

// TestDepGraph_HasCycle_NoCycle 无环图返回 false。
func TestDepGraph_HasCycle_NoCycle(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)
	if err := g.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A→B: %v", err)
	}
	if g.HasCycle() {
		t.Error("无环图 HasCycle 应返回 false")
	}
}

// TestDepGraph_Count 节点计数正确。
func TestDepGraph_Count(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)
	if g.Count() != 3 {
		t.Errorf("Count 应为 3，got %d", g.Count())
	}
}

// TestDepGraph_BucketOf 返回节点所属桶。
func TestDepGraph_BucketOf(t *testing.T) {
	t.Parallel()
	g := makeSimpleGraph(t)
	bucket, ok := g.BucketOf("A")
	if !ok || bucket != BucketA {
		t.Errorf("BucketOf A 应返回 %s, true，got %s, %v", BucketA, bucket, ok)
	}
	_, ok = g.BucketOf("X")
	if ok {
		t.Error("BucketOf 不存在的节点应返回 false")
	}
}
