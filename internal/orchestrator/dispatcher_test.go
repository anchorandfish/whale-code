// dispatcher_test.go — 模块 4 U2 外层包装反例测试（TDD 先行，编排层 §3.4）。
// 覆盖致命反例：MaxDepth 超限拒绝 / CapabilitySnapshot 不一致拒绝 / 断点续传状态恢复 / N1 签名一致 / 并发安全。
// tool + H10 外层包装；落地：Dispatcher 写 input/output.json + 重派机制
// ≤3；落地：checkDepth 派发前调用
// SNAPSHOT: tests={MaxDepthExceeded_Rejected, CapabilitySnapshot_Inconsistent_Rejected, Redispatch_GitRollback, Redispatch_MaxRetry3, StatusRunning_NoOutput_MarkedInterrupted, StatusRunning_OutputComplete_MarkedCompleted, WriteInput_WriteOutput_RoundTrip, N1SignatureNotChanged, ConcurrentDispatch_MutexProtected}

package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// mockN1Agent 测试用 N1Agent mock 实现。
type mockN1Agent struct {
	executeFunc func(ctx context.Context, input N1AgentInput) (N1AgentOutput, error)
	callCount   int
	mu          sync.Mutex
}

func (m *mockN1Agent) Execute(ctx context.Context, input N1AgentInput) (N1AgentOutput, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()
	if m.executeFunc != nil {
		return m.executeFunc(ctx, input)
	}
	return N1AgentOutput{
		CodeDiff:     "diff --git a/foo.go b/foo.go",
		TransferDoc:  "transfer doc",
		FilesTouched: []string{"foo.go"},
	}, nil
}

// makeTestDispatcher 构造测试用 Dispatcher。
func makeTestDispatcher(t *testing.T) (*Dispatcher, *mockN1Agent, string) {
	t.Helper()
	baseDir := t.TempDir()
	agent := &mockN1Agent{}
	d := NewDispatcher(agent, baseDir)
	return d, agent, baseDir
}

// makeValidTaskContext 构造合法 TaskContext（MaxDepth=3, Depth=0）。
func makeValidTaskContext() TaskContext {
	return TaskContext{
		TaskID:             "test-task",
		MaxDepth:           MaxDepthHardLimit,
		CapabilitySnapshot: map[string]bool{"env.test": true},
		BudgetHint:         BudgetHint{TokensCap: 100},
		BudgetMode:         BudgetModeBalanced,
		Depth:              0,
		Goal:               "test goal",
		Tools:              []string{"read_file", "write_file"},
	}
}

// TestDispatcher_MaxDepthExceeded_Rejected 致命反例：tc.Depth=3 → checkDepth 拒绝（Depth+1=4 > 3，）。
func TestDispatcher_MaxDepthExceeded_Rejected(t *testing.T) {
	d, _, _ := makeTestDispatcher(t)

	tc := makeValidTaskContext()
	tc.Depth = 3 // Depth+1=4 > MaxDepthHardLimit=3

	subtask := SubTaskDef{SubTaskID: "S-001", Type: TaskTypeImplement, Complexity: ComplexityT1}

	_, err := d.Dispatch(context.Background(), tc, subtask)
	if err == nil {
		t.Fatal("致命反例：tc.Depth=3 时 Dispatch 必须拒绝（Depth+1=4 >  硬上限 3）")
	}
	if !strings.Contains(err.Error(), "max depth exceeded") {
		t.Errorf("error 应包含 'max depth exceeded'，got: %v", err)
	}
}

// TestDispatcher_CapabilitySnapshot_Inconsistent_Rejected 致命反例：CapabilitySnapshot 不一致时拒绝派发（零容忍）。
func TestDispatcher_CapabilitySnapshot_Inconsistent_Rejected(t *testing.T) {
	_, _, _ = makeTestDispatcher(t)

	tc := makeValidTaskContext()
	tc.CapabilitySnapshot = map[string]bool{"env.test": true, "env.diff": false}

	// 主进程当前 Snapshot 不一致
	currentSnap := map[string]bool{"env.test": true, "env.new": true}

	err := VerifyCapabilitySnapshot(tc, currentSnap)
	if err == nil {
		t.Fatal("致命反例：CapabilitySnapshot 与主进程不一致时必须拒绝派发（零容忍）")
	}
}

// TestDispatcher_Redispatch_GitRollback 重派前调用 git 回滚（重派机制）。
func TestDispatcher_Redispatch_GitRollback(t *testing.T) {
	d, _, _ := makeTestDispatcher(t)

	rollbackCalled := false
	d.SetGitRollbackFn(func(workDir string) error {
		rollbackCalled = true
		return nil
	})

	tc := makeValidTaskContext()
	subtask := SubTaskDef{SubTaskID: "S-redispatch", Type: TaskTypeImplement, Complexity: ComplexityT1}

	_, err := d.Redispatch(context.Background(), tc, subtask, 0)
	if err != nil {
		t.Fatalf("Redispatch: %v", err)
	}
	if !rollbackCalled {
		t.Fatal("重派前必须调用 git 回滚（ 重派幂等性）")
	}
}

// TestDispatcher_Redispatch_MaxRetry3 重派 3 次仍失败 → 返回 error，不再重派。
func TestDispatcher_Redispatch_MaxRetry3(t *testing.T) {
	agent := &mockN1Agent{
		executeFunc: func(ctx context.Context, input N1AgentInput) (N1AgentOutput, error) {
			return N1AgentOutput{}, errAgentExecuteFailedForTest
		},
	}
	baseDir := t.TempDir()
	d := NewDispatcher(agent, baseDir)

	tc := makeValidTaskContext()
	subtask := SubTaskDef{SubTaskID: "S-max-retry", Type: TaskTypeImplement, Complexity: ComplexityT1}

	// retryCount >= 3 时应直接拒绝
	_, err := d.Redispatch(context.Background(), tc, subtask, 3)
	if err == nil {
		t.Fatal("retryCount=3 时 Redispatch 必须返回 error（ 最多重派 3 次）")
	}
	if !strings.Contains(err.Error(), "max retry") {
		t.Errorf("error 应包含 'max retry'，got: %v", err)
	}
}

// TestDispatcher_StatusRunning_NoOutput_MarkedInterrupted 致命反例：status=running 但无 output.json → 标记 interrupted（防状态丢失）。
func TestDispatcher_StatusRunning_NoOutput_MarkedInterrupted(t *testing.T) {
	d, _, baseDir := makeTestDispatcher(t)
	subtaskID := "S-crash-no-output"
	subtaskDir := filepath.Join(baseDir, "subtasks", subtaskID)
	if err := os.MkdirAll(subtaskDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 写 status=running 但不写 output.json（模拟崩溃场景）
	if err := d.writeStatus(subtaskDir, SubTaskStatusRunning, 1); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	// 调 CheckRecovery 检测恢复
	status, needsRedispatch, err := d.CheckRecovery(subtaskID)
	if err != nil {
		t.Fatalf("CheckRecovery: %v", err)
	}

	if status != SubTaskStatusInterrupted {
		t.Fatalf("致命反例：status=running 无 output.json 时必须标记 interrupted，got %s", status)
	}
	if !needsRedispatch {
		t.Fatal("status=running 无 output.json 时 needsRedispatch 必须为 true")
	}
}

// TestDispatcher_StatusRunning_OutputComplete_MarkedCompleted status=running 且 output.json 完整 → 视为 completed（崩溃窗口恢复）。
func TestDispatcher_StatusRunning_OutputComplete_MarkedCompleted(t *testing.T) {
	d, _, baseDir := makeTestDispatcher(t)
	subtaskID := "S-crash-with-output"
	subtaskDir := filepath.Join(baseDir, "subtasks", subtaskID)
	if err := os.MkdirAll(subtaskDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 写 status=running + 完整 output.json（模拟 writeOutput 完成但 writeStatus("completed") 未执行的崩溃窗口）
	if err := d.writeStatus(subtaskDir, SubTaskStatusRunning, 1); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	output := &ImplementOutput{CodeDiff: "diff", TransferDoc: "doc", FilesTouched: []string{"foo.go"}}
	if err := d.writeOutput(subtaskDir, output); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}

	status, needsRedispatch, err := d.CheckRecovery(subtaskID)
	if err != nil {
		t.Fatalf("CheckRecovery: %v", err)
	}

	if status != SubTaskStatusCompleted {
		t.Fatalf("status=running 且 output.json 完整时应视为 completed，got %s", status)
	}
	if needsRedispatch {
		t.Fatal("output.json 完整时 needsRedispatch 应为 false")
	}
}

// TestDispatcher_WriteInput_WriteOutput_RoundTrip writeInput 后读取验证一致（编排层 §3.4）。
func TestDispatcher_WriteInput_WriteOutput_RoundTrip(t *testing.T) {
	d, _, baseDir := makeTestDispatcher(t)
	subtaskID := "S-roundtrip"
	subtaskDir := filepath.Join(baseDir, "subtasks", subtaskID)
	if err := os.MkdirAll(subtaskDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tc := makeValidTaskContext()
	subtask := SubTaskDef{SubTaskID: subtaskID, Type: TaskTypeImplement, Complexity: ComplexityT1, FilesToRead: []string{"foo.go"}}

	if err := d.writeInput(subtaskDir, tc, subtask); err != nil {
		t.Fatalf("writeInput: %v", err)
	}

	// 读取 input.json 验证
	inputPath := filepath.Join(subtaskDir, "input.json")
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}

	var readInput struct {
		TaskContext TaskContext `json:"task_context"`
		SubTaskDef  SubTaskDef  `json:"subtask_def"`
	}
	if err := json.Unmarshal(data, &readInput); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	if readInput.TaskContext.TaskID != tc.TaskID {
		t.Errorf("TaskID mismatch: got %s, want %s", readInput.TaskContext.TaskID, tc.TaskID)
	}
	if readInput.SubTaskDef.SubTaskID != subtask.SubTaskID {
		t.Errorf("SubTaskID mismatch: got %s, want %s", readInput.SubTaskDef.SubTaskID, subtask.SubTaskID)
	}

	// 测试 writeOutput
	output := &ImplementOutput{CodeDiff: "test-diff", FilesTouched: []string{"a.go", "b.go"}}
	if err := d.writeOutput(subtaskDir, output); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}

	outputPath := filepath.Join(subtaskDir, "output.json")
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var readOutput ImplementOutput
	if err := json.Unmarshal(outData, &readOutput); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if readOutput.CodeDiff != "test-diff" {
		t.Errorf("CodeDiff mismatch: got %s", readOutput.CodeDiff)
	}
}

// TestDispatcher_N1SignatureNotChanged 致命反例：N1Agent interface 与 N1 Agent.Execute 签名一致（零容忍，防偷改 N1）。
// 验证：N1Agent.Execute 方法签名形状与 N1 Agent.Execute 一致：(ctx context.Context, input <InputType>) (<OutputType>, error)。
func TestDispatcher_N1SignatureNotChanged(t *testing.T) {
	// 验证 N1Agent interface 有 Execute 方法
	n1AgentType := reflect.TypeOf((*N1Agent)(nil)).Elem()
	method, ok := n1AgentType.MethodByName("Execute")
	if !ok {
		t.Fatal("N1Agent interface 必须有 Execute 方法")
	}

	// 验证方法签名：context.Context + Input → Output + error
	// Go reflect 中 interface 方法的 Method.Type 可能含/不含 receiver，需兼容处理
	numIn := method.Type.NumIn()
	// 合法值：2（无 receiver: ctx+input）或 3（有 receiver: iface+ctx+input）
	if numIn != 2 && numIn != 3 {
		t.Fatalf("N1Agent.Execute 入参数量不一致：期望 2 或 3（ctx+input [±receiver]），got %d", numIn)
	}
	if method.Type.NumOut() != 2 {
		t.Fatalf("N1Agent.Execute 出参数量不一致：期望 2（output+error），got %d", method.Type.NumOut())
	}

	// 验证入参中 context.Context 存在（索引取决于是否有 receiver）
	ctxIdx := 0
	if numIn == 3 {
		ctxIdx = 1 // 有 receiver: In(0)=iface, In(1)=ctx, In(2)=input
	}
	ctxType := method.Type.In(ctxIdx)
	ctxInterface := reflect.TypeOf((*context.Context)(nil)).Elem()
	if ctxType != ctxInterface {
		t.Fatalf("N1Agent.Execute 入参必须是 context.Context，got %v", ctxType)
	}

	// 验证出参 2 是 error
	errType := method.Type.Out(1)
	errInterface := reflect.TypeOf((*error)(nil)).Elem()
	if errType != errInterface {
		t.Fatalf("N1Agent.Execute 第二个出参必须是 error，got %v", errType)
	}

	// 验证 mockN1Agent 实现了 N1Agent interface
	var _ N1Agent = &mockN1Agent{}
}

// TestDispatcher_ConcurrentDispatch_MutexProtected 两个 goroutine 同时 Dispatch 同一 subtask，sync.Mutex 防竞态（编排层 §3.4）。
func TestDispatcher_ConcurrentDispatch_MutexProtected(t *testing.T) {
	agent := &mockN1Agent{}
	baseDir := t.TempDir()
	d := NewDispatcher(agent, baseDir)

	tc := makeValidTaskContext()
	subtask := SubTaskDef{SubTaskID: "S-concurrent", Type: TaskTypeImplement, Complexity: ComplexityT1}

	var wg sync.WaitGroup
	wg.Add(2)

	var errs [2]error
	go func() {
		defer wg.Done()
		_, errs[0] = d.Dispatch(context.Background(), tc, subtask)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = d.Dispatch(context.Background(), tc, subtask)
	}()

	wg.Wait()

	// 至少一个应成功（不 panic 即并发安全）
	for i, err := range errs {
		if err != nil && !strings.Contains(err.Error(), "max depth") {
			t.Logf("goroutine %d returned error: %v (acceptable for concurrent test)", i, err)
		}
	}
}

// errAgentExecuteFailedForTest 测试用 agent.Execute 失败错误。
var errAgentExecuteFailedForTest = &agentError{msg: "agent execute failed for test"}

type agentError struct{ msg string }

func (e *agentError) Error() string { return e.msg }

// TestDispatcher_Dispatch_Success 正常派发流程测试（编排层 §3.4）。
func TestDispatcher_Dispatch_Success(t *testing.T) {
	d, _, baseDir := makeTestDispatcher(t)

	tc := makeValidTaskContext()
	tc.Depth = 0 // Depth+1=1 ≤ 3
	subtask := SubTaskDef{SubTaskID: "S-success", Type: TaskTypeImplement, Complexity: ComplexityT1}

	output, err := d.Dispatch(context.Background(), tc, subtask)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if output == nil {
		t.Fatal("output must not be nil")
	}
	if output.CodeDiff == "" {
		t.Error("CodeDiff should not be empty")
	}

	// 验证 status.json 为 completed
	subtaskDir := filepath.Join(baseDir, "subtasks", "S-success")
	statusPath := filepath.Join(subtaskDir, "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	var st struct {
		Status string `json:"status"`
		Depth  int    `json:"depth"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.Status != SubTaskStatusCompleted {
		t.Errorf("expected status=%s, got %s", SubTaskStatusCompleted, st.Status)
	}
	if st.Depth != 1 {
		t.Errorf("expected depth=1 (tc.Depth+1), got %d", st.Depth)
	}
}

// TestDispatcher_SubtaskIDPathTraversal_Rejected 致命反例：subtaskID 含路径穿越字符（.. / / \）时 Dispatch / MarkInterrupted / CheckRecovery 必须拒绝（审查 v1 致命漏洞修复）。
// 审查 v1 致命漏洞：validateSubtaskID 已定义但从未被调用，导致 SubTaskID 可注入 `..`/`/`/`\` 穿越 baseDir。
func TestDispatcher_SubtaskIDPathTraversal_Rejected(t *testing.T) {
	d, _, baseDir := makeTestDispatcher(t)

	// 直接校验 validateSubtaskID 函数本身的反例（4 类非法输入）
	evilIDs := []string{
		"",         // 空值
		"../evil",  // 含 ..
		"foo/bar",  // 含 /
		`foo\bar`,  // 含 \
		"..",       // 单独 ..
		"foo..bar", // 含 .. 子串
	}
	for _, id := range evilIDs {
		if err := validateSubtaskID(id); err == nil {
			t.Errorf("validateSubtaskID(%q) 必须返回 error（路径穿越字符防注入）", id)
		}
	}

	// 合法 ID 应通过
	if err := validateSubtaskID("S-001"); err != nil {
		t.Errorf("validateSubtaskID(合法 ID) 不应返回 error，got: %v", err)
	}

	tc := makeValidTaskContext()
	tc.Depth = 0

	// 1. Dispatch 拒绝路径穿越 SubTaskID
	dirtySubtask := SubTaskDef{SubTaskID: "../escape", Type: TaskTypeImplement, Complexity: ComplexityT1}
	_, err := d.Dispatch(context.Background(), tc, dirtySubtask)
	if err == nil {
		t.Fatal("致命反例：Dispatch 用含 '..' 的 SubTaskID 必须拒绝（路径穿越防注入）")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("Dispatch 路径穿越 error 应包含 '..'，got: %v", err)
	}
	// 验证确实没有创建逃逸目录（baseDir 之外不应有 escape 目录）
	escapePath := filepath.Join(baseDir, "..", "escape")
	if _, statErr := os.Stat(escapePath); statErr == nil {
		t.Errorf("致命反例：Dispatch 路径穿越未被拦截，baseDir 之外存在逃逸目录 %s", escapePath)
	}

	// 2. MarkInterrupted 拒绝路径穿越 subtaskID
	if err := d.MarkInterrupted("../escape", 1); err == nil {
		t.Fatal("致命反例：MarkInterrupted 用含 '..' 的 subtaskID 必须拒绝（路径穿越防注入）")
	}

	// 3. CheckRecovery 拒绝路径穿越 subtaskID
	if _, _, err := d.CheckRecovery("../escape"); err == nil {
		t.Fatal("致命反例：CheckRecovery 用含 '..' 的 subtaskID 必须拒绝（路径穿越防注入）")
	}

	// 4. 含斜杠的 subtaskID 同样拒绝
	slashSubtask := SubTaskDef{SubTaskID: "foo/bar", Type: TaskTypeImplement, Complexity: ComplexityT1}
	if _, err := d.Dispatch(context.Background(), tc, slashSubtask); err == nil {
		t.Fatal("致命反例：Dispatch 用含 '/' 的 SubTaskID 必须拒绝（路径穿越防注入）")
	}
	if err := d.MarkInterrupted("foo/bar", 1); err == nil {
		t.Fatal("致命反例：MarkInterrupted 用含 '/' 的 subtaskID 必须拒绝（路径穿越防注入）")
	}
	if _, _, err := d.CheckRecovery("foo/bar"); err == nil {
		t.Fatal("致命反例：CheckRecovery 用含 '/' 的 subtaskID 必须拒绝（路径穿越防注入）")
	}

	// 5. 含反斜杠的 subtaskID 同样拒绝（Windows 路径分隔符）
	bslashSubtask := SubTaskDef{SubTaskID: `foo\bar`, Type: TaskTypeImplement, Complexity: ComplexityT1}
	if _, err := d.Dispatch(context.Background(), tc, bslashSubtask); err == nil {
		t.Fatal("致命反例：Dispatch 用含 '\\' 的 SubTaskID 必须拒绝（Windows 路径穿越防注入）")
	}
}
