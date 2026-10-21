package agentguard

import (
	"testing"
)

// P0-4 收敛信号：失败测试数单调下降
func TestConvergingFailingTestsMonotonicDecrease(t *testing.T) {
	tr := NewProgressTracker()
	for _, failing := range []int{8, 5, 2} {
		tr.Observe(TurnObservation{FailingTests: failing})
	}
	if !tr.Converging() {
		t.Fatal("失败测试数 8→5→2 单调下降应判定收敛")
	}
}

// 收敛信号反例：失败测试数上升不判定收敛
func TestNotConvergingWhenFailingTestsIncrease(t *testing.T) {
	tr := NewProgressTracker()
	for _, failing := range []int{2, 5, 8} {
		tr.Observe(TurnObservation{FailingTests: failing})
	}
	if tr.Converging() {
		t.Fatal("失败测试数上升不应判定收敛")
	}
}

// 收敛信号：改动文件数趋于稳定（最近 3 轮改动集合不再增长）
func TestConvergingFileChangesStabilize(t *testing.T) {
	tr := NewProgressTracker()
	tr.Observe(TurnObservation{FilesChanged: []string{"/a.go", "/b.go", "/c.go", "/d.go"}})
	tr.Observe(TurnObservation{FilesChanged: []string{"/a.go", "/b.go", "/c.go", "/d.go"}})
	tr.Observe(TurnObservation{FilesChanged: []string{"/e.go"}})
	if !tr.Converging() {
		t.Fatal("改动文件数 4→4→1 趋于稳定应判定收敛")
	}
}

// 空转信号：连续 N 轮输出高度相似
func TestSpinningSimilarOutputs(t *testing.T) {
	tr := NewProgressTracker()
	same := "我已经检查过 runner_core.go，实现看起来符合预期，继续审查其他部分"
	for i := 0; i < 3; i++ {
		tr.Observe(TurnObservation{Output: same})
	}
	if !tr.Spinning() {
		t.Fatal("连续 3 轮输出完全相同应判定空转")
	}
}

// 空转信号反例：输出持续变化不算空转
func TestNotSpinningWhenOutputsDiffer(t *testing.T) {
	tr := NewProgressTracker()
	outputs := []string{
		"先读取了配置文件，发现 A 处理器缺少错误分支",
		"为 A 处理器补上错误分支，测试从 8 个失败降到 5 个",
		"接着修复 B 模块的死锁问题，新增 2 个单元测试",
	}
	for _, o := range outputs {
		tr.Observe(TurnObservation{Output: o})
	}
	if tr.Spinning() {
		t.Fatal("输出持续变化不应判定空转")
	}
}

// 空转信号：同一文件重读超阈值（默认 5 次）
func TestSpinningRereadingSameFile(t *testing.T) {
	tr := NewProgressTracker()
	for i := 0; i < 6; i++ {
		tr.Observe(TurnObservation{FilesRead: []string{"/repo/runner_core.go"}, Output: "第" + string(rune('A'+i)) + "次分析"})
	}
	if !tr.Spinning() {
		t.Fatal("同一文件读取 6 次（>5 阈值）应判定空转")
	}
	detail := tr.SpinningDetail()
	if len(detail.Tried) == 0 || detail.Tried[0] != "/repo/runner_core.go" {
		t.Fatalf("空转详情应指出重读的文件, got %#v", detail.Tried)
	}
}

// 空转信号：diff 两状态间震荡（A,B,A,B）
func TestSpinningDiffOscillation(t *testing.T) {
	tr := NewProgressTracker()
	setA := []string{"/a.go"}
	setB := []string{"/b.go"}
	for i := 0; i < 6; i++ {
		if i%2 == 0 {
			tr.Observe(TurnObservation{FilesChanged: setA, Output: "改" + string(rune('A'+i))})
		} else {
			tr.Observe(TurnObservation{FilesChanged: setB, Output: "改" + string(rune('A'+i))})
		}
	}
	if !tr.Spinning() {
		t.Fatal("改动集合 A,B,A,B 震荡应判定空转")
	}
}

// 空转压倒收敛：即使失败测试数下降，空转信号仍判停
func TestSpinningOverridesConverging(t *testing.T) {
	tr := NewProgressTracker()
	same := "检查 runner_core.go 中……发现与上轮相同的问题"
	for _, failing := range []int{8, 5, 2} {
		tr.Observe(TurnObservation{FailingTests: failing, Output: same})
	}
	if tr.Converging() {
		t.Fatal("空转信号应压倒收敛判定（Converging 必须为 false）")
	}
	if !tr.Spinning() {
		t.Fatal("连续 3 轮相同输出应判定空转")
	}
}
