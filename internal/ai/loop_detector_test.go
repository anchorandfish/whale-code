// Package ai — V3-6-2 AI 循环检测单元测试
package ai

import (
	"strings"
	"sync"
	"testing"
)

// TestLoopDetector_SuccessLoop 测试成功调用循环检测（默认阈值 3）。
func TestLoopDetector_SuccessLoop(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	input := `{"path":"app.go"}`

	// 第 1、2 次不触发
	if w := d.CheckLoop("read_file", input, true); w != "" {
		t.Errorf("第 1 次不应触发: %q", w)
	}
	if w := d.CheckLoop("read_file", input, true); w != "" {
		t.Errorf("第 2 次不应触发: %q", w)
	}
	// 第 3 次触发
	w := d.CheckLoop("read_file", input, true)
	if w == "" {
		t.Errorf("第 3 次应触发循环警告")
	}
	if !strings.Contains(w, "循环行为") {
		t.Errorf("警告应包含'循环行为': %q", w)
	}
	if !strings.Contains(w, "read_file") {
		t.Errorf("警告应包含工具名: %q", w)
	}
}

// TestLoopDetector_FailureRetry 测试失败重试检测（默认阈值 5）。
func TestLoopDetector_FailureRetry(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	input := `{"path":"app.go"}`

	// 前 4 次不触发
	for i := 0; i < 4; i++ {
		if w := d.CheckLoop("edit_file", input, false); w != "" {
			t.Errorf("第 %d 次不应触发: %q", i+1, w)
		}
	}
	// 第 5 次触发
	w := d.CheckLoop("edit_file", input, false)
	if w == "" {
		t.Errorf("第 5 次应触发重试警告")
	}
	if !strings.Contains(w, "重试失败") {
		t.Errorf("警告应包含'重试失败': %q", w)
	}
}

// TestLoopDetector_NoProgress 测试无进展检测（默认阈值 5）。
func TestLoopDetector_NoProgress(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)

	// 前 4 次不触发
	for i := 0; i < 4; i++ {
		if w := d.CheckNoProgress(false, false); w != "" {
			t.Errorf("第 %d 次不应触发: %q", i+1, w)
		}
	}
	// 第 5 次触发
	w := d.CheckNoProgress(false, false)
	if w == "" {
		t.Errorf("第 5 次应触发无进展警告")
	}
	if !strings.Contains(w, "无进展") {
		t.Errorf("警告应包含'无进展': %q", w)
	}
}

// TestLoopDetector_NoProgressReset 测试有进展时重置计数。
func TestLoopDetector_NoProgressReset(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	d.CheckNoProgress(false, false)
	d.CheckNoProgress(false, false)
	// 有新工具调用 → 重置
	if w := d.CheckNoProgress(true, false); w != "" {
		t.Errorf("有进展时不应触发: %q", w)
	}
	// 重置后需要再 5 次才触发
	for i := 0; i < 4; i++ {
		if w := d.CheckNoProgress(false, false); w != "" {
			t.Errorf("重置后第 %d 次不应触发: %q", i+1, w)
		}
	}
	if w := d.CheckNoProgress(false, false); w == "" {
		t.Errorf("重置后第 5 次应触发")
	}
}

// TestLoopDetector_NoProgressFileChange 测试文件变更也能重置无进展计数。
func TestLoopDetector_NoProgressFileChange(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	for i := 0; i < 4; i++ {
		d.CheckNoProgress(false, false)
	}
	// 文件变更 → 重置
	if w := d.CheckNoProgress(false, true); w != "" {
		t.Errorf("文件变更时不应触发: %q", w)
	}
	// 重置后需要再 5 次
	for i := 0; i < 4; i++ {
		if w := d.CheckNoProgress(false, false); w != "" {
			t.Errorf("重置后第 %d 次不应触发: %q", i+1, w)
		}
	}
	if w := d.CheckNoProgress(false, false); w == "" {
		t.Errorf("重置后第 5 次应触发")
	}
}

// TestLoopDetector_DifferentInputNoLoop 测试不同参数不触发循环。
func TestLoopDetector_DifferentInputNoLoop(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	inputs := []string{
		`{"path":"a.go"}`,
		`{"path":"b.go"}`,
		`{"path":"c.go"}`,
	}
	for i, input := range inputs {
		if w := d.CheckLoop("read_file", input, true); w != "" {
			t.Errorf("不同参数第 %d 次不应触发: %q", i+1, w)
		}
	}
}

// TestLoopDetector_CustomThreshold 测试自定义阈值。
func TestLoopDetector_CustomThreshold(t *testing.T) {
	d := NewLoopDetector(5, 7, 10)
	input := `{"path":"app.go"}`

	// 成功循环：5 次触发
	for i := 0; i < 4; i++ {
		if w := d.CheckLoop("read_file", input, true); w != "" {
			t.Errorf("阈值 5 时第 %d 次不应触发: %q", i+1, w)
		}
	}
	if w := d.CheckLoop("read_file", input, true); w == "" {
		t.Errorf("阈值 5 时第 5 次应触发")
	}

	d.Reset()

	// 失败重试：7 次触发
	for i := 0; i < 6; i++ {
		if w := d.CheckLoop("edit_file", input, false); w != "" {
			t.Errorf("阈值 7 时第 %d 次不应触发: %q", i+1, w)
		}
	}
	if w := d.CheckLoop("edit_file", input, false); w == "" {
		t.Errorf("阈值 7 时第 7 次应触发")
	}
}

// TestLoopDetector_DefaultThreshold 测试零值参数降级为默认阈值。
func TestLoopDetector_DefaultThreshold(t *testing.T) {
	d := NewLoopDetector(0, 0, 0)
	input := `{"path":"app.go"}`

	// 默认 threshold=3，前 2 次不触发
	for i := 0; i < 2; i++ {
		if w := d.CheckLoop("read_file", input, true); w != "" {
			t.Errorf("默认阈值时第 %d 次不应触发: %q", i+1, w)
		}
	}
	if w := d.CheckLoop("read_file", input, true); w == "" {
		t.Errorf("默认阈值 3 时第 3 次应触发")
	}
}

// TestLoopDetector_Reset 测试 Reset 清空状态。
func TestLoopDetector_Reset(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	input := `{"path":"app.go"}`
	d.CheckLoop("read_file", input, true)
	d.CheckLoop("read_file", input, true)
	d.Reset()
	// Reset 后前 2 次不触发，第 3 次触发
	if w := d.CheckLoop("read_file", input, true); w != "" {
		t.Errorf("Reset 后第 1 次不应触发: %q", w)
	}
	if w := d.CheckLoop("read_file", input, true); w != "" {
		t.Errorf("Reset 后第 2 次不应触发: %q", w)
	}
	if w := d.CheckLoop("read_file", input, true); w == "" {
		t.Errorf("Reset 后第 3 次应触发")
	}
}

// TestLoopDetector_ConcurrentSafe 测试并发安全。
// 不 panic 即通过；建议配合 go test -race 验证无数据竞争。
func TestLoopDetector_ConcurrentSafe(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = d.CheckLoop("read_file", `{"path":"app.go"}`, true)
		}()
	}
	wg.Wait()
}

// TestLoopDetector_SuccessFailureSeparate 测试成功/失败计数分离。
// 成功调用与失败调用的连续计数互不干扰。
func TestLoopDetector_SuccessFailureSeparate(t *testing.T) {
	d := NewLoopDetector(3, 3, 5)
	input := `{"path":"app.go"}`

	// 2 次成功 + 1 次失败 + 1 次成功 → 不应触发（计数被打断）
	d.CheckLoop("read_file", input, true)
	d.CheckLoop("read_file", input, true)
	d.CheckLoop("read_file", input, false)
	w := d.CheckLoop("read_file", input, true)
	if w != "" {
		t.Errorf("成功/失败交替时不应触发: %q", w)
	}
}

// TestLoopDetector_DifferentToolNoLoop 测试不同工具名不触发循环。
func TestLoopDetector_DifferentToolNoLoop(t *testing.T) {
	d := NewLoopDetector(3, 5, 5)
	input := `{"path":"app.go"}`
	// 不同工具名，相同输入
	d.CheckLoop("read_file", input, true)
	d.CheckLoop("edit_file", input, true)
	w := d.CheckLoop("write_file", input, true)
	if w != "" {
		t.Errorf("不同工具名不应触发循环: %q", w)
	}
}
