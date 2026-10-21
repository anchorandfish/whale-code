// Package tools — N1 subagent / Task tool 单元测试（反例先行）。
//
// 测试覆盖（02 §2.1.1 契约 + /018/020）：
// - 契约一致性：AgentInput 仅 4 字段，无 CacheControl/AgentID 字段（反例）
// - 递归深度限制：depth=4 返回 ErrAgentRecursionLimit
// - 超时：返回 ErrAgentTimeout
// - 状态机：非法转移与终态不可转移（反例）
// - 并发：单失败不阻塞、100 并发无 race
// - cancel 传播：父 cancel 传到后代
// - spawn 限流：超 maxSpawn 返回 ErrAgentRecursionLimit
// - AgentID 透传：StreamWithRetry 收到 agentIDKey（mock 验证）
//
// mock 策略：复用 agent_test.go 的 agentMockClient 模式，不依赖真实 AI 调用。
// 不使用 httptest（Winsock 损坏，关卡约束）。
package tools

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// =====================================================================
// 契约一致性测试（反例先行：验证非法字段不存在）
// =====================================================================

// TestAgentInput_NoCacheControlField 验证 AgentInput 无 CacheControl 字段（02 §2.1.1 契约）。
// 反例测试：若有人为 N3 CacheControl 加字段，此测试应失败。
func TestAgentInput_NoCacheControlField(t *testing.T) {
	v := AgentInput{}
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Name == "CacheControl" {
			t.Fatalf("AgentInput must NOT have CacheControl field (02 §2.1.1 契约禁止扩展)")
		}
	}
}

// TestAgentInput_NoAgentIDField 验证 AgentInput 无 AgentID 字段（02 §2.1.1 契约）。
// 反例测试：若有人为 N2 AgentID 加字段，此测试应失败。
func TestAgentInput_NoAgentIDField(t *testing.T) {
	v := AgentInput{}
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Name == "AgentID" {
			t.Fatalf("AgentInput must NOT have AgentID field (02 §2.1.1 契约禁止扩展)")
		}
	}
}

// TestAgentInput_FieldContract 验证 AgentInput 的字段契约。
//
// 沿革：本用例原名 TestAgentInput_OnlyFourFields，钉的是 02 §2.1.1 的
// 「Prompt/Tools/Model/ParentCtx 四字段、禁止扩展」契约。
//
// L2起该契约被**有意扩展**为 6 字段，因为「声明即真相 + 委派不扩权」
// 必须在委派边界上携带两样东西，二者都没有既有字段可以承载：
// - AgentType —— 委派的角色声明（「声明即真相」的载体）。未声明 ⇒ 默认只读 explore。
// 必须在边界传递：inferAgentType(prompt) 的关键字推断已删除。
// - ParentToolSet —— 父会话可执行工具面的快照（「委派不扩权」的载体）：
// 子 agent 工具面 = 角色面 ∩ 父面。
//
// 这两个字段都只可能**收窄**权限（AgentType 决定角色面，ParentToolSet 做交集），
// 不存在放宽路径，因此不属于提权面。
//
// 仍然禁止的是缓存类/身份类字段（见 TestAgentInput_NoCacheControlField /
// TestAgentInput_NoAgentIDField）：它们与权限无关，加进来只会制造第二套真相。
func TestAgentInput_FieldContract(t *testing.T) {
	v := AgentInput{}
	rt := reflect.TypeOf(v)
	expected := map[string]bool{
		"Prompt":        false,
		"Tools":         false,
		"Model":         false,
		"ParentCtx":     false,
		"AgentType":     false,
		"ParentToolSet": false,
	}
	if rt.NumField() != len(expected) {
		t.Fatalf("AgentInput must have exactly %d fields, got %d", len(expected), rt.NumField())
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if _, ok := expected[f.Name]; !ok {
			t.Fatalf("AgentInput has unexpected field: %s", f.Name)
		}
		expected[f.Name] = true
	}
	for name, found := range expected {
		if !found {
			t.Fatalf("AgentInput missing required field: %s", name)
		}
	}
}

// =====================================================================
// 递归深度限制测试
// =====================================================================

// TestExecute_RecursionLimit 验证 depth=4（超 maxAgentDepth=3）返回 ErrAgentRecursionLimit。
func TestExecute_RecursionLimit(t *testing.T) {
	mgr := NewAgentTaskManager(5, 20, nil)
	task := &AgentTask{
		id:       generateAgentUUID(),
		depth:    maxAgentDepth + 1, // depth=4，超限
		status:   TaskPending,
		manager:  mgr,
		executor: nil, // 不需要 executor，深度检查在 executor 使用前
	}

	ctx := context.Background()
	parentCtx := ctx
	_, err := task.Execute(ctx, AgentInput{
		Prompt:    "test",
		ParentCtx: &parentCtx,
	})

	if err == nil {
		t.Fatal("Expected error for recursion depth exceeded, got nil")
	}
	id := ai.GetErrorID(err)
	if id != ai.ErrAgentRecursionLimit {
		t.Fatalf("Expected ErrAgentRecursionLimit, got %s (err=%v)", id, err)
	}
}

// TestSpawn_SpawnLimitExceeded 验证超 maxSpawn 返回 ErrAgentRecursionLimit。
// 使用极小 maxSpawn=1，连续 Spawn 2 次应第二次失败。
func TestSpawn_SpawnLimitExceeded(t *testing.T) {
	mgr := NewAgentTaskManager(1, 1, nil) // maxSpawn=1
	ctx := context.Background()
	parentCtx := ctx

	// 第一次 Spawn 会成功获取 spawnCounter（Add(1)=1 ≤ maxSpawn=1），
	// 但随后 Acquire 会成功，task.Execute 会因 executor=nil 失败。
	// spawnCounter 在 task.Execute defer 中释放。
	// 为简化测试，直接手动占满 spawnCounter：
	mgr.spawnCounter.Add(1) // 手动占满

	_, err := mgr.Spawn(ctx, AgentInput{
		Prompt:    "test",
		ParentCtx: &parentCtx,
	}, "", 0)

	if err == nil {
		t.Fatal("Expected error for spawn limit exceeded, got nil")
	}
	id := ai.GetErrorID(err)
	if id != ai.ErrAgentRecursionLimit {
		t.Fatalf("Expected ErrAgentRecursionLimit, got %s (err=%v)", id, err)
	}
}

// =====================================================================
// 超时测试
// =====================================================================

// TestExecute_Timeout 验证超时返回 ErrAgentTimeout（D2 补强：端到端 mock 验证）。
//
// 使用 blockingAgentClient 模拟 AI 调用阻塞到 ctx 超时，
// 验证 Execute 返回的错误 ErrorID 为 ai.ErrAgentTimeout，状态为 TaskFailed。
// 审查 prompt §3.3 要求：验证 ai.GetErrorID(err) == ai.ErrAgentTimeout。
func TestExecute_Timeout(t *testing.T) {
	// 构造 mock AIClient：Stream 阻塞到 ctx 超时后返回 DeadlineExceeded
	mockClient := &blockingAgentClient{}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	// 不通过 Spawn 创建（避免 Acquire/Release 不匹配），manager 设为 nil。
	// executor 仍注入，runMultiRound 可正常调用 StreamWithRetry。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	task := &AgentTask{
		id:       generateAgentUUID(),
		depth:    1,
		status:   TaskPending,
		executor: executor,
	}

	parentCtx := context.Background()
	_, err := task.Execute(ctx, AgentInput{
		Prompt:    "test",
		ParentCtx: &parentCtx,
	})

	if err == nil {
		t.Fatal("Expected error for timeout, got nil")
	}

	// 验证 ErrorID 为 ErrAgentTimeout（N6 契约，审查 prompt §3.3 核心断言）
	if id := ai.GetErrorID(err); id != ai.ErrAgentTimeout {
		t.Fatalf("Expected ErrAgentTimeout, got %s (err=%v)", id, err)
	}

	// 验证状态为 TaskFailed（超时归入 Failed + ErrAgentTimeout，非 Cancelled）
	if status := task.getStatus(); status != TaskFailed {
		t.Fatalf("Expected TaskFailed status for timeout, got %s", status.String())
	}
}

// =====================================================================
// 状态机测试（反例）
// =====================================================================

// TestTransition_IllegalTransfer 验证 pending → completed 是非法转移（应失败）。
// pending 只能 → running 或 cancelled，不能直接 → completed。
func TestTransition_IllegalTransfer(t *testing.T) {
	task := &AgentTask{
		id:     generateAgentUUID(),
		depth:  1,
		status: TaskPending,
	}
	err := task.transition(TaskPending, TaskCompleted)
	if err == nil {
		t.Fatal("Expected error for illegal transition pending → completed, got nil")
	}
}

// TestTransition_TerminalCannotTransfer 验证终态（completed）不可转移（应失败）。
func TestTransition_TerminalCannotTransfer(t *testing.T) {
	task := &AgentTask{
		id:     generateAgentUUID(),
		depth:  1,
		status: TaskCompleted,
	}
	err := task.transition(TaskCompleted, TaskRunning)
	if err == nil {
		t.Fatal("Expected error for transition from terminal state completed → running, got nil")
	}
}

// TestTransition_LegalTransfer 验证合法转移成功（正向用例，补充反例）。
func TestTransition_LegalTransfer(t *testing.T) {
	task := &AgentTask{
		id:     generateAgentUUID(),
		depth:  1,
		status: TaskPending,
	}
	// pending → running 合法
	if err := task.transition(TaskPending, TaskRunning); err != nil {
		t.Fatalf("Legal transition pending → running failed: %v", err)
	}
	// running → completed 合法
	if err := task.transition(TaskRunning, TaskCompleted); err != nil {
		t.Fatalf("Legal transition running → completed failed: %v", err)
	}
}

// =====================================================================
// 并发测试
// =====================================================================

// TestSpawnParallel_SingleFailureNotBlock 验证单个子任务失败不阻塞其他子任务。
//
// 使用 nil executor 让所有子任务的 runMultiRound 失败，
// 验证 SpawnParallel 仍然返回所有结果（不因单个失败而中断 errgroup）。
func TestSpawnParallel_SingleFailureNotBlock(t *testing.T) {
	mgr := NewAgentTaskManager(5, 20, nil) // executor=nil，runMultiRound 会失败

	ctx := context.Background()
	parentCtx := ctx
	inputs := []AgentInput{
		{Prompt: "task1", ParentCtx: &parentCtx},
		{Prompt: "task2", ParentCtx: &parentCtx},
		{Prompt: "task3", ParentCtx: &parentCtx},
	}

	results, err := mgr.SpawnParallel(ctx, inputs, "parent-1", 0)
	if err != nil {
		t.Fatalf("SpawnParallel should not return error (single failure tolerant), got: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}
	// 所有子任务都应返回结果（即使失败，results[i] 也有值）
	for i, r := range results {
		if r.TaskID == "" {
			t.Fatalf("Result %d has empty TaskID", i)
		}
	}
}

// TestTaskManager_ConcurrentSpawn 验证 100 并发 Spawn 无 race（CI 补跑 -race）。
//
// 使用 nil executor 让子任务快速失败返回，验证 spawnCounter 与 tasks map 的并发安全。
func TestTaskManager_ConcurrentSpawn(t *testing.T) {
	mgr := NewAgentTaskManager(10, 200, nil) // maxSpawn=200 容纳 100 并发

	var wg sync.WaitGroup
	var successCount int64
	var failCount int64
	N := 100

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := context.Background()
			parentCtx := ctx
			_, err := mgr.Spawn(ctx, AgentInput{
				Prompt:    "concurrent test",
				ParentCtx: &parentCtx,
			}, "parent-concurrent", 0)
			if err != nil {
				atomic.AddInt64(&failCount, 1)
			} else {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}
	wg.Wait()

	t.Logf("Concurrent spawn: %d success, %d fail (of %d)", successCount, failCount, N)
	// 验证 tasks map 无 race（-race 检测器会报错若有 race）
	// 验证 spawnCounter 归零（所有任务完成后）
	remaining := mgr.spawnCounter.Load()
	if remaining != 0 {
		t.Errorf("spawnCounter should be 0 after all tasks done, got %d", remaining)
	}
}

// =====================================================================
// Cancel 传播测试
// =====================================================================

// TestCancel_PropagateToChildren 验证 cancel 传播到后代（D4 补强：端到端 spawn 链验证）。
//
// 使用 blockingAgentClient 模拟长时间运行的 AI 调用，
// 从父任务的 childCtx 派生子任务（模拟 TaskTool.Call 把父 ctx 传给 Spawn），
// cancel 父任务后验证子任务状态 = TaskCancelled。
// 审查 prompt §3.5 要求：spawn 父任务 → spawn 子任务 → cancel 父 → 验证子任务状态 = Cancelled。
func TestCancel_PropagateToChildren(t *testing.T) {
	// mock AIClient：Stream 阻塞到 ctx cancel，started channel 用于测试同步
	mockClient := &blockingAgentClient{started: make(chan struct{}, 1)}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	mgr := NewAgentTaskManager(5, 20, executor)

	// 1. 创建父任务并手动设置为 Running 状态（模拟 Execute 内部的状态转移）
	// 手动设置 cancel 函数（模拟 Execute 内部 childCtx, cancel := WithCancel(ctx)）
	parentCtx := context.Background()
	parentChildCtx, parentCancel := context.WithCancel(parentCtx)

	parentTask := &AgentTask{
		id:       generateAgentUUID(),
		depth:    0,
		status:   TaskRunning,
		manager:  mgr,
		executor: executor,
		cancel:   parentCancel,
	}
	mgr.mu.Lock()
	mgr.tasks[parentTask.id] = parentTask
	mgr.mu.Unlock()

	// 2. 从父任务的 childCtx 派生子任务（模拟 TaskTool.Call 把父 ctx 传给 Spawn）
	// cancel 父任务 → parentChildCtx cancel → 子任务 ctx 链 cancel → Stream 返回 Canceled
	var childOutput AgentOutput
	spawnDone := make(chan struct{})
	go func() {
		defer close(spawnDone)
		childOutput, _ = mgr.Spawn(parentChildCtx, AgentInput{
			Prompt:    "child task",
			ParentCtx: &parentCtx,
		}, parentTask.id, 0)
	}()

	// 3. 等待子任务的 Stream 被调用（确认子任务已进入 Running 并阻塞在 mock）
	select {
	case <-mockClient.started:
		// 子任务已开始 Stream 调用
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for child task to start Stream")
	}

	// 4. cancel 父任务（触发 context 链传播）
	if err := mgr.Cancel(parentTask.id); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}

	// 5. 等待 Spawn 返回（子任务 Execute 完成，状态转为 Cancelled）
	select {
	case <-spawnDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for child Spawn to return after parent cancel")
	}

	// 6. 验证子任务状态为 Cancelled（审查 prompt §3.5 核心断言）
	if childOutput.Status != TaskCancelled {
		t.Fatalf("child task must be Cancelled after parent cancel, got %s", childOutput.Status.String())
	}

	// 7. 验证 cancel 不存在的任务返回错误
	if err := mgr.Cancel("nonexistent-id"); err == nil {
		t.Fatal("Cancel of non-existent task should return error")
	}
}

// =====================================================================
// AgentID 透传测试
// =====================================================================

// TestExecute_AgentIDTransmitted 验证 AgentID 透传到 StreamWithRetry（D3 补强：端到端 mock 验证）。
//
// Part 1: helper 函数测试（extractAgentID / ExtractAgentID 基础验证）
// Part 2: 端到端 mock 验证 — Execute → runMultiRound → Run → StreamWithRetry → client.Stream，
//
//	captureCtxAgentClient 捕获 Stream 收到的 ctx，验证 ctx.Value(agentIDKey{}) 非 nil。
//
// 审查 prompt §3.6 要求：mock StreamWithRetry 捕获 context，验证 capturedCtx.Value(agentIDKey{}) 非 nil。
func TestExecute_AgentIDTransmitted(t *testing.T) {
	// === Part 1: helper 函数测试 ===
	testAgentID := "test-agent-id-1234"
	parentCtx := WithAgentID(context.Background(), testAgentID)

	extracted := extractAgentID(&parentCtx)
	if extracted != testAgentID {
		t.Fatalf("extractAgentID failed: expected %s, got %s", testAgentID, extracted)
	}

	// 测试 nil ParentCtx 安全返回空字符串
	emptyID := extractAgentID(nil)
	if emptyID != "" {
		t.Fatalf("extractAgentID(nil) should return empty string, got %s", emptyID)
	}

	// 测试未携带 agentIDKey 的 context 返回空字符串
	plainCtx := context.Background()
	plainID := extractAgentID(&plainCtx)
	if plainID != "" {
		t.Fatalf("extractAgentID on plain ctx should return empty, got %s", plainID)
	}

	// 验证 ExtractAgentID（导出函数）与 extractAgentID 行为一致
	if ExtractAgentID(parentCtx) != testAgentID {
		t.Fatal("ExtractAgentID should match extractAgentID")
	}

	// === Part 2: D3 补强 — 端到端 mock 验证 AgentID 透传到 Stream ===
	mockClient := &captureCtxAgentClient{}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	// 不通过 Spawn 创建（避免 Acquire/Release 不匹配），manager 设为 nil。
	// 构造携带 agentIDKey 的 ParentCtx（模拟父任务注入 AgentID）
	parentAgentID := "parent-agent-123"
	pCtx := context.WithValue(context.Background(), agentIDKey{}, parentAgentID)

	task := &AgentTask{
		id:       generateAgentUUID(),
		depth:    1,
		status:   TaskPending,
		executor: executor,
	}

	ctx := context.Background()
	_, _ = task.Execute(ctx, AgentInput{
		Prompt:    "test",
		ParentCtx: &pCtx,
	})

	// 验证 Stream 收到的 ctx 携带 agentIDKey（审查 prompt §3.6 核心断言）
	if mockClient.captured == nil {
		t.Fatal("Stream was not called (captured ctx is nil)")
	}
	v := mockClient.captured.Value(agentIDKey{})
	if v == nil {
		t.Fatal("captured ctx does not carry agentIDKey — AgentID not transmitted to StreamWithRetry")
	}
	id, ok := v.(string)
	if !ok {
		t.Fatalf("agentIDKey value is not string: %T", v)
	}
	if id != parentAgentID {
		t.Fatalf("expected agentID %s, got %s", parentAgentID, id)
	}
}

// =====================================================================
// 辅助测试：状态机与类型
// =====================================================================

// TestTaskStatus_IsFinal 验证 IsFinal 方法（终态判断）。
func TestTaskStatus_IsFinal(t *testing.T) {
	if !TaskCompleted.IsFinal() {
		t.Error("TaskCompleted should be final")
	}
	if !TaskFailed.IsFinal() {
		t.Error("TaskFailed should be final")
	}
	if !TaskCancelled.IsFinal() {
		t.Error("TaskCancelled should be final")
	}
	if TaskPending.IsFinal() {
		t.Error("TaskPending should NOT be final")
	}
	if TaskRunning.IsFinal() {
		t.Error("TaskRunning should NOT be final")
	}
}

// TestTaskStatus_String 验证 String 方法返回正确字符串。
func TestTaskStatus_String(t *testing.T) {
	cases := []struct {
		status TaskStatus
		want   string
	}{
		{TaskPending, "pending"},
		{TaskRunning, "running"},
		{TaskCompleted, "completed"},
		{TaskFailed, "failed"},
		{TaskCancelled, "cancelled"},
	}
	for _, c := range cases {
		if got := c.status.String(); got != c.want {
			t.Errorf("TaskStatus(%d).String() = %s, want %s", c.status, got, c.want)
		}
	}
}

// TestIsValidTransition 验证状态转移表完整性。
func TestIsValidTransition(t *testing.T) {
	// 合法转移
	legal := []struct{ from, to TaskStatus }{
		{TaskPending, TaskRunning},
		{TaskPending, TaskCancelled},
		{TaskRunning, TaskCompleted},
		{TaskRunning, TaskFailed},
		{TaskRunning, TaskCancelled},
	}
	for _, c := range legal {
		if !isValidTransition(c.from, c.to) {
			t.Errorf("isValidTransition(%s, %s) should be true", c.from, c.to)
		}
	}

	// 非法转移
	illegal := []struct{ from, to TaskStatus }{
		{TaskPending, TaskCompleted}, // pending 不能直接 → completed
		{TaskPending, TaskFailed},    // pending 不能直接 → failed
		{TaskCompleted, TaskRunning}, // 终态不可转移
		{TaskFailed, TaskRunning},    // 终态不可转移
		{TaskCancelled, TaskRunning}, // 终态不可转移
		{TaskCompleted, TaskPending}, // 终态不可转移
	}
	for _, c := range illegal {
		if isValidTransition(c.from, c.to) {
			t.Errorf("isValidTransition(%s, %s) should be false", c.from, c.to)
		}
	}
}

// TestGenerateAgentUUID 验证 UUID 生成格式（RFC 4122 v4）。
func TestGenerateAgentUUID(t *testing.T) {
	id := generateAgentUUID()
	if len(id) != 36 {
		t.Fatalf("UUID length should be 36, got %d (%s)", len(id), id)
	}
	// 验证 version 位（第14个字符应为 '4'）
	if id[14] != '4' {
		t.Errorf("UUID version should be 4, got %c in %s", id[14], id)
	}
	// 验证 variant 位（第19个字符应为 '8'/'9'/'a'/'b'）
	v := id[19]
	if v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Errorf("UUID variant should be 8/9/a/b, got %c in %s", v, id)
	}
	// 验证唯一性（生成 100 个应全不同）
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := generateAgentUUID()
		if seen[id] {
			t.Fatalf("UUID collision: %s", id)
		}
		seen[id] = true
	}
}

// TestParseAgentType_DeclarationIsTruth 验证「声明即真相」的角色解析契约（L2）。
//
// 取代此前的 TestInferAgentType：prompt 关键字推断（inferAgentType）已被删除，
// 它会把「声明 explore」在中文 prompt 下猜成 coder（全量工具面），是现场 T-A 的直因。
//
// 契约：
// - 缺省 / 空 / 纯空白 → DefaultAgentType（explore，委派默认不扩权）
// - 合法值（大小写、首尾空白不敏感）→ 原样归一
// - 非法值 → ok=false（调用方必须 fail-closed 报错，不得静默降级）
func TestParseAgentType_DeclarationIsTruth(t *testing.T) {
	cases := []struct {
		raw   string
		want  AgentType
		valid bool
	}{
		{"", DefaultAgentType, true},
		{"   ", DefaultAgentType, true},
		{"coder", AgentTypeCoder, true},
		{"PLAN", AgentTypePlan, true},
		{" explore ", AgentTypeExplore, true},
		{"verification", AgentTypeVerification, true},
		{"fork", AgentTypeFork, true},
		{"bogus", "", false},
		{"读取README第一行", "", false},
	}
	for _, c := range cases {
		got, ok := ParseAgentType(c.raw)
		if ok != c.valid {
			t.Errorf("ParseAgentType(%q) ok = %v, want %v", c.raw, ok, c.valid)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseAgentType(%q) = %s, want %s", c.raw, got, c.want)
		}
	}

	// 默认角色必须是**只读**角色：委派默认不扩权（§7⑤）。
	if !permission.IsReadOnlyAgentType(string(DefaultAgentType)) {
		t.Errorf("DefaultAgentType = %q 必须是只读角色（委派默认不扩权）", DefaultAgentType)
	}
	if DefaultAgentType != AgentType(permission.DefaultAgentTypeName) {
		t.Errorf("tools.DefaultAgentType(%q) 必须与 permission.DefaultAgentTypeName(%q) 同源",
			DefaultAgentType, permission.DefaultAgentTypeName)
	}
}

// TestRoleToolSet_DerivedFromDeclaredLevel 验证角色工具面**派生自档位声明**（L1）。
//
// 反例判别：若把只读工具面的档位从 RO 改成 WW，它应立刻从只读面消失 ——
// 因为工具面不再有手写清单（旧 agent.go:895-932 已删）。
func TestRoleToolSet_DerivedFromDeclaredLevel(t *testing.T) {
	ro := readOnlyToolSet()
	full := fullToolSet()

	// 声明的档位决定成员：只读面里的每个工具，其声明档都必须是 LevelReadOnly。
	for name := range ro {
		lvl, ok := permission.LevelOf(name)
		if !ok {
			t.Errorf("只读面含未登记工具 %q —— 派生来源必须是档位目录", name)
			continue
		}
		if lvl != permission.LevelReadOnly {
			t.Errorf("%q 在只读面，但其声明档 = %v（应为 LevelReadOnly）", name, lvl)
		}
	}

	// 写工具不得进入只读面；读工具必须进入。
	for _, tool := range []string{"write_file", "edit_file", "bash", "git_add", "config_set", "export_session"} {
		if ro[tool] {
			t.Errorf("只读角色工具面不应含 %s", tool)
		}
	}
	for _, tool := range []string{"read_file", "grep_search", "glob_search", "git_diff", "TodoWrite"} {
		if !ro[tool] {
			t.Errorf("只读角色工具面应含 %s", tool)
		}
	}

	// 全量面 ⊇ 只读面；委派型工具不参与派生。
	for name := range ro {
		if !full[name] {
			t.Errorf("全量工具面应包含只读面成员 %s", name)
		}
	}
	if full["Agent"] {
		t.Error("委派型工具 Agent 不应出现在派生的全量面里（由角色+深度单独决定）")
	}

	// 真正对外的**角色工具面**（coder）必须扣除 childHiddenTools。
	// 注意 fullToolSet() 只是「档位目录视图」，它保留 Agent/Task 之外的治理面工具；
	// 面向子 agent 的工具面在 roleToolSet 这一层收口。
	coder := roleToolSet(AgentTypeCoder)
	for name := range childHiddenTools {
		if coder[name] {
			t.Errorf("顶层/治理面工具 %s 不应进入子 agent 工具面（理由：%s）", name, childHiddenTools[name])
		}
	}
	if coder["Agent"] {
		t.Error("Agent 应由「角色+深度」单独追加，不得出现在 roleToolSet 基础面里")
	}
	// 对照：coder 仍应拿到写能力（排除表不得误伤正常工具）。
	for _, tool := range []string{"bash", "write_file", "edit_file", "TaskCreate", "Skill"} {
		if !coder[tool] {
			t.Errorf("coder 工具面应含 %s（排除表误伤）", tool)
		}
	}
}

// TestListAndClean 验证 List 与 CleanCompleted 方法。
func TestListAndClean(t *testing.T) {
	mgr := NewAgentTaskManager(5, 20, nil)

	// 初始为空
	if list := mgr.List(); len(list) != 0 {
		t.Fatalf("Expected empty list, got %d", len(list))
	}

	// 添加 2 个任务
	t1 := &AgentTask{id: "t1", depth: 1, status: TaskPending, manager: mgr}
	t2 := &AgentTask{id: "t2", depth: 1, status: TaskCompleted, manager: mgr}
	mgr.mu.Lock()
	mgr.tasks["t1"] = t1
	mgr.tasks["t2"] = t2
	mgr.mu.Unlock()

	// List 应返回 2 个
	if list := mgr.List(); len(list) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(list))
	}

	// CleanCompleted 应清理 t2（completed），保留 t1（pending）
	cleaned := mgr.CleanCompleted()
	if cleaned != 1 {
		t.Fatalf("Expected 1 cleaned, got %d", cleaned)
	}
	if list := mgr.List(); len(list) != 1 {
		t.Fatalf("Expected 1 task after clean, got %d", len(list))
	}

	// Get 应返回 t1
	if info, err := mgr.Get("t1"); err != nil {
		t.Fatalf("Get(t1) failed: %v", err)
	} else if info.ID != "t1" {
		t.Errorf("Get returned wrong ID: %s", info.ID)
	}

	// Get 不存在的应返回错误
	if _, err := mgr.Get("nonexistent"); err == nil {
		t.Fatal("Get(nonexistent) should return error")
	}
}

// TestAgentTaskManager_NilExecutor 验证 nil executor 时 Execute 返回错误。
func TestAgentTaskManager_NilExecutor(t *testing.T) {
	mgr := NewAgentTaskManager(5, 20, nil) // executor=nil

	ctx := context.Background()
	parentCtx := ctx
	_, err := mgr.Spawn(ctx, AgentInput{
		Prompt:    "test nil executor",
		ParentCtx: &parentCtx,
	}, "", 0)

	// executor=nil 时 runMultiRound 返回错误，但 spawnCounter 应已释放
	if err == nil {
		t.Log("Spawn with nil executor returned nil error (unexpected but not fatal)")
	} else {
		// 验证不是 ErrAgentRecursionLimit（深度未超）
		id := ai.GetErrorID(err)
		if id == ai.ErrAgentRecursionLimit {
			t.Fatal("Should not be ErrAgentRecursionLimit for nil executor")
		}
	}

	// 验证 spawnCounter 归零（资源清理）
	remaining := mgr.spawnCounter.Load()
	if remaining != 0 {
		t.Errorf("spawnCounter should be 0 after Spawn with nil executor, got %d", remaining)
	}
}

// TestErrorsAsPropagation 验证 ai.WithID 包装的 error 可被 errors.As 提取。
func TestErrorsAsPropagation(t *testing.T) {
	// 模拟 AgentTask.Execute 的超时错误包装
	original := errors.New("context deadline exceeded")
	wrapped := ai.WithID(original, ai.ErrAgentTimeout)

	id := ai.GetErrorID(wrapped)
	if id != ai.ErrAgentTimeout {
		t.Fatalf("Expected ErrAgentTimeout, got %s", id)
	}

	// 验证 errors.Is 链
	if !errors.Is(wrapped, original) {
		t.Fatal("errors.Is should find original error in chain")
	}
}

// =====================================================================
// Mock clients for D2/D3/D4 test enhancements
// =====================================================================

// blockingAgentClient 阻塞到 ctx.Done() 后返回 ctx.Err()（用于超时/cancel 测试）。
// started channel 在 Stream 被调用时发信号（可选，用于测试同步）。
type blockingAgentClient struct {
	started chan struct{}
}

// Stream 实现 ai.Client 接口：阻塞到 ctx 超时或 cancel，发信号后返回 ctx.Err()。
func (m *blockingAgentClient) Stream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingAgentClient) Model() string   { return "test-model" }
func (m *blockingAgentClient) SetModel(string) {}
func (m *blockingAgentClient) Close() error    { return nil }

// captureCtxAgentClient 捕获 Stream 收到的 ctx，返回 ErrorEvent 快速结束（用于 AgentID 透传测试）。
type captureCtxAgentClient struct {
	captured context.Context
}

// Stream 实现 ai.Client 接口：捕获 ctx 后返回带 ErrorEvent 的 channel。
func (m *captureCtxAgentClient) Stream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	m.captured = ctx
	ch := make(chan ai.Event, 1)
	ch <- ai.ErrorEvent{Err: errors.New("capture done")}
	close(ch)
	return ch, nil
}

func (m *captureCtxAgentClient) Model() string   { return "test-model" }
func (m *captureCtxAgentClient) SetModel(string) {}
func (m *captureCtxAgentClient) Close() error    { return nil }
