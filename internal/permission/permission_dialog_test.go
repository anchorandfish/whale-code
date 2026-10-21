// Package permission — M12 权限体验增强测试。
//
// 覆盖范围：
// - M12-02 PermissionDialog API + AvailableOptionsForRisk
// - M12-03 PermissionRequest Hook 订阅/广播
// - M12-04 ModeCycle 循环切换
// - M12-05 ClassifyActionWithMemory 集成
package permission

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// === M12-02: PermissionDialog + AssessRiskLevel ===

func TestDialogOption_String(t *testing.T) {
	cases := []struct {
		opt      DialogOption
		expected string
	}{
		{DialogOptionAllowOnce, "allow-once"},
		{DialogOptionAllowAlways, "allow-always"},
		{DialogOptionDenyOnce, "deny-once"},
		{DialogOptionDenyAlways, "deny-always"},
		{DialogOptionSwitchMode, "switch-mode"},
		{DialogOptionCancel, "cancel"},
	}
	for _, c := range cases {
		if got := c.opt.String(); got != c.expected {
			t.Errorf("DialogOption(%d).String() = %q, want %q", c.opt, got, c.expected)
		}
	}
}

func TestAssessRiskLevel(t *testing.T) {
	cases := []struct {
		toolName string
		input    string
		expected string
	}{
		// bash 安全命令
		{"bash", `{"command":"ls -la"}`, "low"},
		{"bash", `{"command":"echo hello"}`, "low"},
		// bash 危险命令
		{"bash", `{"command":"rm -rf /"}`, "high"},
		{"bash", `{"command":"git push --force"}`, "high"},
		// ★ P-002：原为幽灵名 `PowerShell`（从未注册）⇒ 改指真身 `Monitor`。
		{"Monitor", `{"command":"Remove-Item file.txt"}`, "high"},
		// read 操作
		{"read_file", `{"file_path":"main.go"}`, "low"},
		{"grep_search", `{"pattern":"foo"}`, "low"},
		// write 操作（普通）
		{"write_file", `{"file_path":"main.go"}`, "medium"},
		// write 受保护路径
		{"write_file", `{"file_path":".env"}`, "high"},
		{"write_file", `{"file_path":".git/config"}`, "high"},
		// 其他工具
		{"unknown_tool", "{}", "medium"},
	}
	for _, c := range cases {
		got := AssessRiskLevel(c.toolName, c.input)
		if got != c.expected {
			t.Errorf("AssessRiskLevel(%q, %q) = %q, want %q",
				c.toolName, c.input, got, c.expected)
		}
	}
}

func TestAvailableOptionsForRisk(t *testing.T) {
	cases := []struct {
		risk             string
		minLen           int
		shouldContain    DialogOption
		shouldNotContain DialogOption
	}{
		{"low", 3, DialogOptionAllowAlways, DialogOptionDenyAlways},
		{"medium", 5, DialogOptionAllowAlways, DialogOptionCancel},
		{"high", 4, DialogOptionSwitchMode, DialogOptionAllowAlways},
		{"critical", 3, DialogOptionDenyAlways, DialogOptionAllowOnce},
	}
	for _, c := range cases {
		opts := AvailableOptionsForRisk(c.risk)
		if len(opts) < c.minLen {
			t.Errorf("AvailableOptionsForRisk(%q) = %d options, want >= %d",
				c.risk, len(opts), c.minLen)
		}
		found := false
		for _, o := range opts {
			if o == c.shouldContain {
				found = true
			}
		}
		if !found {
			t.Errorf("AvailableOptionsForRisk(%q) missing %s", c.risk, c.shouldContain)
		}
		notFound := true
		for _, o := range opts {
			if o == c.shouldNotContain {
				notFound = false
			}
		}
		if !notFound {
			t.Errorf("AvailableOptionsForRisk(%q) should NOT contain %s", c.risk, c.shouldNotContain)
		}
	}
}

// mockDialog 是测试用的 PermissionDialog 实现。
type mockDialog struct {
	response DialogResponse
	calls    int32
}

func (m *mockDialog) Show(ctx context.Context, req DialogRequest) DialogResponse {
	atomic.AddInt32(&m.calls, 1)
	return m.response
}

func TestPolicy_SetPermissionDialog(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	dialog := &mockDialog{response: DialogResponse{Option: DialogOptionAllowOnce}}
	p.SetPermissionDialog(dialog)

	if p.GetPermissionDialog() != dialog {
		t.Errorf("GetPermissionDialog did not return the set dialog")
	}
}

func TestPolicy_GetPermissionDialog_Nil(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	if p.GetPermissionDialog() != nil {
		t.Errorf("GetPermissionDialog on new policy should be nil")
	}
}

// === M12-03: PermissionRequest Hook ===

func TestPolicy_SubscribePermissionRequest(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	var mu sync.Mutex
	var receivedEvent PermissionRequestEvent

	unsub := p.SubscribePermissionRequest(func(event PermissionRequestEvent) {
		mu.Lock()
		defer mu.Unlock()
		receivedEvent = event
	})

	// 广播事件
	testEvent := PermissionRequestEvent{
		Request:   DialogRequest{ToolName: "bash", RiskLevel: "high"},
		Source:    "test",
		Timestamp: time.Now(),
	}
	p.emitPermissionRequest(testEvent)

	unsub()

	mu.Lock()
	defer mu.Unlock()
	if receivedEvent.Request.ToolName != "bash" {
		t.Errorf("hook did not receive event: tool=%q", receivedEvent.Request.ToolName)
	}
	if receivedEvent.Source != "test" {
		t.Errorf("hook received wrong source: %q", receivedEvent.Source)
	}
}

func TestPolicy_UnsubscribePermissionRequest(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	var callCount int32

	unsub := p.SubscribePermissionRequest(func(event PermissionRequestEvent) {
		atomic.AddInt32(&callCount, 1)
	})

	// 取消订阅
	unsub()

	// 广播事件，不应被调用
	p.emitPermissionRequest(PermissionRequestEvent{
		Request: DialogRequest{ToolName: "bash"},
	})

	if atomic.LoadInt32(&callCount) != 0 {
		t.Errorf("hook called after unsubscribe: count=%d", callCount)
	}
}

func TestPolicy_MultipleHooks(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	var count1, count2 int32

	p.SubscribePermissionRequest(func(event PermissionRequestEvent) {
		atomic.AddInt32(&count1, 1)
	})
	p.SubscribePermissionRequest(func(event PermissionRequestEvent) {
		atomic.AddInt32(&count2, 1)
	})

	p.emitPermissionRequest(PermissionRequestEvent{
		Request: DialogRequest{ToolName: "bash"},
	})

	if atomic.LoadInt32(&count1) != 1 {
		t.Errorf("hook 1 not called once: count=%d", count1)
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Errorf("hook 2 not called once: count=%d", count2)
	}
}

// === M12-04: ModeCycle ===

func TestNewModeCycle(t *testing.T) {
	mc := NewModeCycle(LevelWorkspaceWrite)
	if mc.Current() != LevelWorkspaceWrite {
		t.Errorf("Current = %s, want workspace-write", mc.Current())
	}
}

func TestNewModeCycle_InvalidInitial(t *testing.T) {
	// 不在循环中的模式（如 LevelDangerFullAccess）应回退到 workspace-write
	mc := NewModeCycle(LevelDangerFullAccess)
	if mc.Current() != LevelWorkspaceWrite {
		t.Errorf("Current with invalid initial = %s, want workspace-write", mc.Current())
	}
}

func TestModeCycle_Next(t *testing.T) {
	mc := NewModeCycle(LevelReadOnly)
	expected := []Level{
		LevelWorkspaceWrite, // after 1 Next
		LevelReadOnly,       // after 2 Next (cycle back)
	}
	for i, want := range expected {
		got := mc.Next()
		if got != want {
			t.Errorf("Next() #%d = %s, want %s", i+1, got, want)
		}
	}
}

func TestModeCycle_Previous(t *testing.T) {
	mc := NewModeCycle(LevelReadOnly)
	// Previous from ReadOnly should wrap to WorkspaceWrite（2 档循环）
	got := mc.Previous()
	if got != LevelWorkspaceWrite {
		t.Errorf("Previous() from ReadOnly = %s, want workspace-write", got)
	}
	// Previous from WorkspaceWrite should be ReadOnly
	got = mc.Previous()
	if got != LevelReadOnly {
		t.Errorf("Previous() from WorkspaceWrite = %s, want read-only", got)
	}
}

func TestModeCycle_Set(t *testing.T) {
	mc := NewModeCycle(LevelReadOnly)
	mc.Set(LevelWorkspaceWrite)
	if mc.Current() != LevelWorkspaceWrite {
		t.Errorf("After Set(workspace-write), Current = %s, want workspace-write", mc.Current())
	}
}

func TestModeCycle_CycleLevels(t *testing.T) {
	mc := NewModeCycle(LevelReadOnly)
	levels := mc.CycleLevels()
	if len(levels) != 2 {
		t.Errorf("CycleLevels = %d, want 2（danger 不参与循环）", len(levels))
	}
	// 验证顺序
	expected := []Level{
		LevelReadOnly, LevelWorkspaceWrite,
	}
	for i, want := range expected {
		if levels[i] != want {
			t.Errorf("CycleLevels[%d] = %s, want %s", i, levels[i], want)
		}
	}
}

func TestPolicy_SetModeCycle(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	mc := NewModeCycle(LevelReadOnly)
	p.SetModeCycle(mc)

	if p.GetModeCycle() != mc {
		t.Errorf("GetModeCycle did not return set cycle")
	}
}

func TestPolicy_SwitchModeNext(t *testing.T) {
	p := NewPolicy(LevelReadOnly)
	mc := NewModeCycle(LevelReadOnly)
	p.SetModeCycle(mc)

	newMode := p.SwitchModeNext()
	if newMode != LevelWorkspaceWrite {
		t.Errorf("SwitchModeNext from ReadOnly = %s, want workspace-write", newMode)
	}
	// 验证 Policy.mode 也被更新
	if p.mode != LevelWorkspaceWrite {
		t.Errorf("Policy.mode after SwitchModeNext = %s, want workspace-write", p.mode)
	}
}

func TestPolicy_SwitchModeNext_NoCycle(t *testing.T) {
	p := NewPolicy(LevelReadOnly)
	// 未注入 ModeCycle
	newMode := p.SwitchModeNext()
	if newMode != LevelReadOnly {
		t.Errorf("SwitchModeNext without cycle = %s, want read-only (unchanged)", newMode)
	}
}

// === M12-05: ClassifyActionWithMemory ===

func TestClassifyActionWithMemory_NoClassifier(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	memory := p.memory
	result := ClassifyActionWithMemory(context.Background(), nil, memory, nil, ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"ls"}`,
	})
	if result.Decision != DecisionUncertain {
		t.Errorf("Decision = %d, want %d (Uncertain)", result.Decision, DecisionUncertain)
	}
	if result.Source != "no-classifier" {
		t.Errorf("Source = %q, want 'no-classifier'", result.Source)
	}
}

func TestClassifyActionWithMemory_MemoryHit(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	memory := p.memory
	// 预先记录到 memory（使用 Memory.Set）
	memory.Set("bash", `{"command":"echo hello"}`, true)
	result := ClassifyActionWithMemory(context.Background(), nil, memory, nil, ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"echo hello"}`,
	})
	if result.Decision != DecisionAllow {
		t.Errorf("Decision = %d, want %d (Allow from memory)", result.Decision, DecisionAllow)
	}
	if result.Source != "memory" {
		t.Errorf("Source = %q, want 'memory'", result.Source)
	}
}

func TestClassifyActionWithMemory_Classifier(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	memory := p.memory
	classifier := NewClassifier(ClassifierConfig{Enabled: true, FailClosed: false})
	// bash 命令 ls 是 read-only，应在 allowlist 中 → DecisionAllow
	result := ClassifyActionWithMemory(context.Background(), classifier, memory, nil, ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"ls -la"}`,
	})
	// classifier 应返回 Allow（allowlist 命中）
	if result.Decision != DecisionAllow {
		t.Errorf("Decision = %d, want %d (Allow from classifier)", result.Decision, DecisionAllow)
	}
}

func TestClassifyActionWithMemory_DenialFallback(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	memory := p.memory
	tracker := NewDenialTracker()
	// 触发连续失败（默认 ShouldFallback 阈值在 denial.go 中定义）
	for i := 0; i < 100; i++ {
		tracker.RecordDenial()
	}

	result := ClassifyActionWithMemory(context.Background(), nil, memory, tracker, ClassifierAction{
		ToolName:  "bash",
		ToolInput: `{"command":"rm -rf /"}`,
	})
	// ShouldFallback 触发 → Uncertain
	if result.Decision != DecisionUncertain {
		t.Errorf("Decision with denial fallback = %d, want %d (Uncertain)",
			result.Decision, DecisionUncertain)
	}
	if result.Source != "denial-fallback" {
		t.Errorf("Source = %q, want 'denial-fallback'", result.Source)
	}
}
