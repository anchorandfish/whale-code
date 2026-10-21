package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/lsp"
)

// --- ToolSearch unit test ---

// TestToolSearch_NormalSearch 测试正常关键词搜索
func TestToolSearch_NormalSearch(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.toolSearchHandler()

	input, _ := json.Marshal(map[string]string{
		"query": "file",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("ToolSearch failed: %v", err)
	}
	if result == "" {
		t.Error("ToolSearch should return results for 'file'")
	}
}

// TestToolSearch_EmptyQuery 测试空查询
func TestToolSearch_EmptyQuery(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.toolSearchHandler()

	input, _ := json.Marshal(map[string]string{
		"query": "",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("ToolSearch should fail with empty query")
	}
}

// TestToolSearch_CategoryFilter 测试类别过滤
func TestToolSearch_CategoryFilter(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.toolSearchHandler()

	input, _ := json.Marshal(map[string]string{
		"query":    "bash",
		"category": "builtin",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("ToolSearch failed: %v", err)
	}
	if result == "" {
		t.Error("ToolSearch should find bash in builtin category")
	}
}

// TestToolSearch_NoMatch 测试no match结果
func TestToolSearch_NoMatch(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.toolSearchHandler()

	input, _ := json.Marshal(map[string]string{
		"query": "xyznonexistent",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("ToolSearch failed: %v", err)
	}
	if result == "" {
		t.Error("ToolSearch should return message for no match")
	}
}

// --- LspImplementation unit test ---

// TestLspImplementation_Found 测试找到implementation
func TestLspImplementation_Found(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspImplementationHandler()

	// 注入 mock LSP client
	e.lspClientMu.Lock()
	e.lspClientCache["go"] = &mockLSPClient{
		ready: true,
		implementations: []lsp.Location{
			{
				URI:   "file:///path/to/impl.go",
				Range: lsp.Range{Start: lsp.Position{Line: 14, Character: 5}},
			},
		},
	}
	e.lspClientMu.Unlock()
	defer func() {
		e.lspClientMu.Lock()
		delete(e.lspClientCache, "go")
		e.lspClientMu.Unlock()
	}()

	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "main.go",
		"line":      10,
		"character": 5,
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("LspImplementation failed: %v", err)
	}
	if result == "No implementations found" {
		t.Error("Should find implementations")
	}
}

// TestLspImplementation_NoFile 测试缺少文件路径
func TestLspImplementation_NoFile(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspImplementationHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"line":      10,
		"character": 5,
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("LspImplementation should fail without file_path")
	}
}

// --- LspTypeDefinition unit test ---

// TestLspTypeDefinition_Found 测试找到类型定义
func TestLspTypeDefinition_Found(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspTypeDefinitionHandler()

	e.lspClientMu.Lock()
	e.lspClientCache["go"] = &mockLSPClient{
		ready: true,
		typeDefinitions: []lsp.Location{
			{
				URI:   "file:///path/to/types.go",
				Range: lsp.Range{Start: lsp.Position{Line: 19, Character: 5}},
			},
		},
	}
	e.lspClientMu.Unlock()
	defer func() {
		e.lspClientMu.Lock()
		delete(e.lspClientCache, "go")
		e.lspClientMu.Unlock()
	}()

	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "main.go",
		"line":      10,
		"character": 5,
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("LspTypeDefinition failed: %v", err)
	}
	if result == "No type definition found" {
		t.Error("Should find type definition")
	}
}

// --- LspCodeActions unit test ---

// TestLspCodeActions_Found 测试找到代码操作
func TestLspCodeActions_Found(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspCodeActionsHandler()

	e.lspClientMu.Lock()
	e.lspClientCache["go"] = &mockLSPClient{
		ready: true,
		codeActions: []lsp.CodeAction{
			{Title: "Organize Imports", Kind: "source.organizeImports"},
			{Title: "Add error check", Kind: "quickfix"},
		},
	}
	e.lspClientMu.Unlock()
	defer func() {
		e.lspClientMu.Lock()
		delete(e.lspClientCache, "go")
		e.lspClientMu.Unlock()
	}()

	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "main.go",
		"line":        10,
		"character":   5,
		"action_kind": "quickfix",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("LspCodeActions failed: %v", err)
	}
	if result == "No code actions available" {
		t.Error("Should find code actions")
	}
}

// TestLspCodeActions_NoFile 测试缺少文件路径
func TestLspCodeActions_NoFile(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspCodeActionsHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"line":      10,
		"character": 5,
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("LspCodeActions should fail without file_path")
	}
}

// --- LspDocumentSymbols unit test ---

// TestLspDocumentSymbols_Found 测试找到文档符号
func TestLspDocumentSymbols_Found(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspDocumentSymbolsHandler()

	e.lspClientMu.Lock()
	e.lspClientCache["go"] = &mockLSPClient{
		ready: true,
		documentSymbols: []lsp.DocumentSymbol{
			{
				Name:  "main",
				Kind:  12, // Function
				Range: lsp.Range{Start: lsp.Position{Line: 9, Character: 0}},
			},
			{
				Name:  "Config",
				Kind:  23, // Struct
				Range: lsp.Range{Start: lsp.Position{Line: 19, Character: 5}},
				Children: []lsp.DocumentSymbol{
					{
						Name:  "Name",
						Kind:  8, // Field
						Range: lsp.Range{Start: lsp.Position{Line: 20, Character: 1}},
					},
				},
			},
		},
	}
	e.lspClientMu.Unlock()
	defer func() {
		e.lspClientMu.Lock()
		delete(e.lspClientCache, "go")
		e.lspClientMu.Unlock()
	}()

	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "main.go",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("LspDocumentSymbols failed: %v", err)
	}
	if result == "No document symbols found" {
		t.Error("Should find document symbols")
	}
}

// TestLspDocumentSymbols_NoFile 测试缺少文件路径
func TestLspDocumentSymbols_NoFile(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.lspDocumentSymbolsHandler()

	input, _ := json.Marshal(map[string]interface{}{})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("LspDocumentSymbols should fail without file_path")
	}
}

// --- SendMessage unit test ---

// TestSendMessage_Normal 测试正常发送消息
func TestSendMessage_Normal(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sendMessageHandler()

	input, _ := json.Marshal(map[string]string{
		"agent_id": "agent_1",
		"message":  "Hello from main",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if result == "" {
		t.Error("SendMessage should return confirmation")
	}

	// 验证消息已存入总线
	e.agentBusMu.Lock()
	msgs, ok := e.agentBus["agent_1"]
	e.agentBusMu.Unlock()
	if !ok || len(msgs) == 0 {
		t.Error("Message should be stored in agent bus")
	}
	if msgs[0].Content != "Hello from main" {
		t.Errorf("Message content mismatch: got %q", msgs[0].Content)
	}
}

// TestSendMessage_EmptyAgentID 测试空proxy ID
func TestSendMessage_EmptyAgentID(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sendMessageHandler()

	input, _ := json.Marshal(map[string]string{
		"agent_id": "",
		"message":  "Hello",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("SendMessage should fail with empty agent_id")
	}
}

// TestSendMessage_EmptyMessage 测试空消息
func TestSendMessage_EmptyMessage(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sendMessageHandler()

	input, _ := json.Marshal(map[string]string{
		"agent_id": "agent_1",
		"message":  "",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("SendMessage should fail with empty message")
	}
}

// --- TaskCreate unit test ---

// TestTaskCreate_Normal 测试正常创建任务
func TestTaskCreate_Normal(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.taskCreateHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"command":     "echo hello",
		"description": "test task",
	})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("TaskCreate failed: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("TaskCreate result not valid JSON: %v", err)
	}
	if parsed["task_id"] == "" {
		t.Error("TaskCreate should return task_id")
	}
	if parsed["status"] != "running" {
		t.Errorf("Expected status 'running', got %q", parsed["status"])
	}
}

// TestTaskCreate_EmptyCommand 测试空命令
func TestTaskCreate_EmptyCommand(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.taskCreateHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"command": "",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("TaskCreate should fail with empty command")
	}
}

// --- TaskList unit test ---

// TestTaskList_Empty 测试空任务列表
func TestTaskList_Empty(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.taskListHandler()

	input, _ := json.Marshal(map[string]string{})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("TaskList failed: %v", err)
	}
	// 文案已改为「英文主行 + 缩进中文辅行」，全等比较会恒假（测试照样绿但不再拦）⇒ 改 Contains，意图不变。
	if !strings.Contains(result, "没有后台任务") {
		t.Errorf("Expected 含 '没有后台任务', got %q", result)
	}
}

// TestTaskList_AfterCreate 测试创建后列出任务
func TestTaskList_AfterCreate(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// 创建任务
	createHandler := e.taskCreateHandler()
	input, _ := json.Marshal(map[string]interface{}{
		"command":     "echo test",
		"description": "list test",
	})
	createHandler(context.Background(), input)

	// 等待任务完成
	time.Sleep(2 * time.Second)

	// 列出任务
	listHandler := e.taskListHandler()
	listInput, _ := json.Marshal(map[string]string{"status": "all"})
	result, err := listHandler(context.Background(), listInput)
	if err != nil {
		t.Fatalf("TaskList failed: %v", err)
	}
	// 改 Contains（意图不变：创建任务后不得报「没有后台任务」）。
	if strings.Contains(result, "没有后台任务") {
		t.Error("TaskList should show created task")
	}
}

// --- TaskStop unit test ---

// TestTaskStop_Nonexistent 测试停止不存在的任务
func TestTaskStop_Nonexistent(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.taskStopHandler()

	input, _ := json.Marshal(map[string]string{
		"task_id": "nonexistent",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("TaskStop should fail for nonexistent task")
	}
}

// TestTaskStop_EmptyID 测试空任务 ID
func TestTaskStop_EmptyID(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.taskStopHandler()

	input, _ := json.Marshal(map[string]string{
		"task_id": "",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("TaskStop should fail with empty task_id")
	}
}

// --- CronList unit test ---

// TestCronList_Empty 测试空定时任务列表
func TestCronList_Empty(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.cronListHandler()

	input, _ := json.Marshal(map[string]string{})
	result, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("CronList failed: %v", err)
	}
	// 改 Contains（意图不变：空列表须报「没有定时任务」）。
	if !strings.Contains(result, "没有定时任务") {
		t.Errorf("Expected 含 '没有定时任务', got %q", result)
	}
}

// --- CronDelete unit test ---

// TestCronDelete_Nonexistent 测试删除不存在的定时任务
func TestCronDelete_Nonexistent(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.cronDeleteHandler()

	input, _ := json.Marshal(map[string]string{
		"job_id": "nonexistent",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("CronDelete should fail for nonexistent job")
	}
}

// TestCronDelete_EmptyID 测试空任务 ID
func TestCronDelete_EmptyID(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.cronDeleteHandler()

	input, _ := json.Marshal(map[string]string{
		"job_id": "",
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("CronDelete should fail with empty job_id")
	}
}

// --- Sleep unit test ---

// TestSleep_Normal 测试正常等待
func TestSleep_Normal(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sleepHandler()

	start := time.Now()
	input, _ := json.Marshal(map[string]interface{}{
		"seconds": 0.2,
	})
	result, err := handler(context.Background(), input)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Sleep failed: %v", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("Sleep should wait at least 150ms, waited %v", elapsed)
	}
	if result == "" {
		t.Error("Sleep should return confirmation")
	}
}

// TestSleep_TooSmall 测试等待时间过小
func TestSleep_TooSmall(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sleepHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"seconds": 0.01,
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("Sleep should fail with seconds < 0.1")
	}
}

// TestSleep_TooLarge 测试等待时间过大
func TestSleep_TooLarge(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sleepHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"seconds": 120,
	})
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Error("Sleep should fail with seconds > 60")
	}
}

// TestSleep_Cancelled test context取消
func TestSleep_Cancelled(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	handler := e.sleepHandler()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	input, _ := json.Marshal(map[string]interface{}{
		"seconds": 10,
	})
	_, err := handler(ctx, input)
	if err == nil {
		t.Error("Sleep should fail when context is cancelled")
	}
}

// --- symbolKindName unit test ---

// TestSymbolKindName 测试符号类型名称映射
func TestSymbolKindName(t *testing.T) {
	tests := []struct {
		kind     int
		expected string
	}{
		{5, "Class"},
		{6, "Method"},
		{12, "Function"},
		{13, "Variable"},
		{23, "Struct"},
		{11, "Interface"},
		{999, "Kind999"},
	}
	for _, tt := range tests {
		result := symbolKindName(tt.kind)
		if result != tt.expected {
			t.Errorf("symbolKindName(%d) = %q, want %q", tt.kind, result, tt.expected)
		}
	}
}

// --- TaskManager unit test ---

// TestTaskManager_CreateAndList 测试创建和列出任务
func TestTaskManager_CreateAndList(t *testing.T) {
	tm := NewTaskManager()
	task := tm.Create("echo test", "test task", 0)

	if task.ID == "" {
		t.Error("Task ID should not be empty")
	}
	if task.Status != "running" {
		t.Errorf("Expected status 'running', got %q", task.Status)
	}

	tasks := tm.List("all")
	if len(tasks) != 1 {
		t.Errorf("Expected 1 task, got %d", len(tasks))
	}
}

// TestTaskManager_Stop 测试停止任务
func TestTaskManager_Stop(t *testing.T) {
	tm := NewTaskManager()
	task := tm.Create("sleep 60", "long task", 0)

	err := tm.Stop(task.ID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	updated, ok := tm.Get(task.ID)
	if !ok {
		t.Error("Task should still exist after stop")
	}
	if updated.Status != "failed" {
		t.Errorf("Expected status 'failed', got %q", updated.Status)
	}
}

// TestTaskManager_StopNonexistent 测试停止不存在的任务
func TestTaskManager_StopNonexistent(t *testing.T) {
	tm := NewTaskManager()
	err := tm.Stop("nonexistent")
	if err == nil {
		t.Error("Should fail to stop nonexistent task")
	}
}

// --- CronManager unit test ---

// TestCronManager_CreateAndList 测试创建和列出定时任务
func TestCronManager_CreateAndList(t *testing.T) {
	cm := NewCronManager()
	job, err := cm.Create("0 * * * *", "echo hourly", "hourly task")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if job.ID == "" {
		t.Error("Job ID should not be empty")
	}
	if job.Status != "active" {
		t.Errorf("Expected status 'active', got %q", job.Status)
	}

	jobs := cm.List()
	if len(jobs) != 1 {
		t.Errorf("Expected 1 job, got %d", len(jobs))
	}
}

// TestCronManager_Delete 测试删除定时任务
func TestCronManager_Delete(t *testing.T) {
	cm := NewCronManager()
	job, err := cm.Create("0 * * * *", "echo hourly", "hourly task")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = cm.Delete(job.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	jobs := cm.List()
	if len(jobs) != 0 {
		t.Errorf("Expected 0 active jobs after delete, got %d", len(jobs))
	}
}

// TestCronManager_DeleteNonexistent 测试删除不存在的定时任务
func TestCronManager_DeleteNonexistent(t *testing.T) {
	cm := NewCronManager()
	err := cm.Delete("nonexistent")
	if err == nil {
		t.Error("Should fail to delete nonexistent job")
	}
}

// TestCronManager_DoubleDelete 测试重复删除
func TestCronManager_DoubleDelete(t *testing.T) {
	cm := NewCronManager()
	job, err := cm.Create("0 * * * *", "echo hourly", "hourly task")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	cm.Delete(job.ID)
	err = cm.Delete(job.ID)
	if err == nil {
		t.Error("Should fail to delete already-deleted job")
	}
}

// --- Registry integration test ---

// TestRegistry_Has52BuiltinTools 测试注册表包含 52 个内置工具
func TestRegistry_Has52BuiltinTools(t *testing.T) {
	r := NewRegistry()
	names := r.List()
	if len(names) < 52 {
		t.Errorf("Expected at least 52 builtin tools, got %d", len(names))
	}
}

// TestRegistry_NewToolsRegistered 测试新工具已注册
func TestRegistry_NewToolsRegistered(t *testing.T) {
	r := NewRegistry()
	newTools := []string{
		"ToolSearch", "LspImplementation", "LspTypeDefinition",
		"LspCodeActions", "LspDocumentSymbols", "SendMessage",
		"TaskCreate", "TaskList", "TaskStop", "TaskOutput", "ForkStatus", "MonitorTail",
		"CronList", "CronDelete", "Sleep",
	}
	for _, name := range newTools {
		spec, ok := r.Get(name)
		if !ok {
			t.Errorf("Tool %q should be registered", name)
			continue
		}
		if spec.Name != name {
			t.Errorf("Expected name %q, got %q", name, spec.Name)
		}
	}
}

// TestRegistry_NewToolsSpecs 测试新工具规格正确
func TestRegistry_NewToolsSpecs(t *testing.T) {
	r := NewRegistry()
	tests := []struct {
		name      string
		permLevel Level
		concSafe  bool
	}{
		{"ToolSearch", LevelReadOnly, true},
		{"LspImplementation", LevelReadOnly, true},
		{"LspTypeDefinition", LevelReadOnly, true},
		{"LspCodeActions", LevelWorkspaceWrite, false},
		{"LspDocumentSymbols", LevelReadOnly, true},
		{"SendMessage", LevelReadOnly, false},
		{"TaskCreate", LevelWorkspaceWrite, false},
		{"TaskList", LevelReadOnly, true},
		{"TaskStop", LevelWorkspaceWrite, false},
		// ★ /059/060：三条读回工具统一 LevelReadOnly + 并发安全。
		{"TaskOutput", LevelReadOnly, true},
		{"MonitorTail", LevelReadOnly, true},
		{"ForkStatus", LevelReadOnly, true},
		{"CronList", LevelReadOnly, true},
		{"CronDelete", LevelWorkspaceWrite, false},
		{"Sleep", LevelReadOnly, true},
	}
	for _, tt := range tests {
		spec, ok := r.Get(tt.name)
		if !ok {
			t.Errorf("Tool %q not found", tt.name)
			continue
		}
		if spec.RequiredPermission != tt.permLevel {
			t.Errorf("Tool %q: expected permission %d, got %d", tt.name, tt.permLevel, spec.RequiredPermission)
		}
		if spec.ConcurrencySafe != tt.concSafe {
			t.Errorf("Tool %q: expected ConcurrencySafe=%v, got %v", tt.name, tt.concSafe, spec.ConcurrencySafe)
		}
	}
}

// --- Executor integration test ---

// TestExecutor_NewHandlersRegistered 测试新处理器已注册
func TestExecutor_NewHandlersRegistered(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	newTools := []string{
		"ToolSearch", "LspImplementation", "LspTypeDefinition",
		"LspCodeActions", "LspDocumentSymbols", "SendMessage",
		"TaskCreate", "TaskList", "TaskStop", "TaskOutput", "ForkStatus", "MonitorTail",
		"CronList", "CronDelete", "Sleep",
	}
	for _, name := range newTools {
		e.mu.RLock()
		_, ok := e.handlers[name]
		e.mu.RUnlock()
		if !ok {
			t.Errorf("Handler for %q should be registered", name)
		}
	}
}

// TestExecutor_ExecuteToolSearch test passed Execute 调用 ToolSearch
func TestExecutor_ExecuteToolSearch(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_1",
		Name:  "ToolSearch",
		Input: `{"query":"bash"}`,
	})
	if result.IsError {
		t.Errorf("ToolSearch should not error: %s", result.Output)
	}
}

// TestExecutor_ExecuteSleep test passed Execute 调用 Sleep
func TestExecutor_ExecuteSleep(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_2",
		Name:  "Sleep",
		Input: `{"seconds":0.1}`,
	})
	if result.IsError {
		t.Errorf("Sleep should not error: %s", result.Output)
	}
}

// TestExecutor_ExecuteCronList test passed Execute 调用 CronList
func TestExecutor_ExecuteCronList(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_3",
		Name:  "CronList",
		Input: `{}`,
	})
	if result.IsError {
		t.Errorf("CronList should not error: %s", result.Output)
	}
}

// TestExecutor_ExecuteTaskList test passed Execute 调用 TaskList
func TestExecutor_ExecuteTaskList(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_4",
		Name:  "TaskList",
		Input: `{}`,
	})
	if result.IsError {
		t.Errorf("TaskList should not error: %s", result.Output)
	}
}

// TestExecutor_ExecuteSendMessage test passed Execute 调用 SendMessage
func TestExecutor_ExecuteSendMessage(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_5",
		Name:  "SendMessage",
		Input: `{"agent_id":"agent_1","message":"hello"}`,
	})
	if result.IsError {
		t.Errorf("SendMessage should not error: %s", result.Output)
	}
}

// TestExecutor_ExecuteCronDelete_Invalid test passed Execute 调用 CronDelete（无效 ID）
func TestExecutor_ExecuteCronDelete_Invalid(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_6",
		Name:  "CronDelete",
		Input: `{"job_id":"nonexistent"}`,
	})
	if !result.IsError {
		t.Error("CronDelete with nonexistent job_id should error")
	}
}

// TestExecutor_ExecuteTaskStop_Invalid test passed Execute 调用 TaskStop（无效 ID）
func TestExecutor_ExecuteTaskStop_Invalid(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "test_7",
		Name:  "TaskStop",
		Input: `{"task_id":"nonexistent"}`,
	})
	if !result.IsError {
		t.Error("TaskStop with nonexistent task_id should error")
	}
}

// --- IsConcurrencySafe 测试 ---

// TestIsConcurrencySafe_NewTools 测试新工具的并发security
func TestIsConcurrencySafe_NewTools(t *testing.T) {
	r := NewRegistry()
	safeTools := []string{
		"ToolSearch", "LspImplementation", "LspTypeDefinition",
		"LspDocumentSymbols", "TaskList", "TaskOutput", "MonitorTail", "ForkStatus", "CronList", "Sleep",
	}
	unsafeTools := []string{
		"LspCodeActions", "SendMessage", "TaskCreate",
		"TaskStop", "CronDelete",
	}
	for _, name := range safeTools {
		if !r.IsConcurrencySafe(name) {
			t.Errorf("Tool %q should be concurrency safe", name)
		}
	}
	for _, name := range unsafeTools {
		if r.IsConcurrencySafe(name) {
			t.Errorf("Tool %q should NOT be concurrency safe", name)
		}
	}
}

// --- formatSymbols 测试 ---

// TestFormatSymbols 测试符号格式化
func TestFormatSymbols(t *testing.T) {
	symbols := []lsp.DocumentSymbol{
		{
			Name:  "main",
			Kind:  12,
			Range: lsp.Range{Start: lsp.Position{Line: 9}},
		},
		{
			Name:  "Config",
			Kind:  23,
			Range: lsp.Range{Start: lsp.Position{Line: 19}},
			Children: []lsp.DocumentSymbol{
				{
					Name:  "Name",
					Kind:  8,
					Range: lsp.Range{Start: lsp.Position{Line: 20}},
				},
			},
		},
	}
	var results []string
	formatSymbols(symbols, 0, &results)

	if len(results) != 3 {
		t.Errorf("Expected 3 formatted lines, got %d", len(results))
	}
	if results[0] != "main (Function) :10" {
		t.Errorf("Unexpected first line: %q", results[0])
	}
	if results[1] != "Config (Struct) :20" {
		t.Errorf("Unexpected second line: %q", results[1])
	}
	if results[2] != "  Name (Field) :21" {
		t.Errorf("Unexpected third line: %q", results[2])
	}
}

// --- CronCreateAndListWorkflow integration test ---

// TestCronCreateAndListWorkflow 测试创建定时任务后列出的完整流程
func TestCronCreateAndListWorkflow(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// 先通过 schedule_cron 创建（旧工具）
	scheduleResult := e.Execute(context.Background(), Call{
		ID:    "cron_create",
		Name:  "schedule_cron",
		Input: `{"schedule":"0 * * * *","command":"echo hourly","description":"hourly echo"}`,
	})
	if scheduleResult.IsError {
		t.Fatalf("schedule_cron failed: %s", scheduleResult.Output)
	}
	// create cron job via CronManager directly
	_, _ = e.CronMgr.Create("0 * * * *", "echo hourly", "hourly echo")

	// 列出定时任务
	listResult := e.Execute(context.Background(), Call{
		ID:    "cron_list",
		Name:  "CronList",
		Input: `{}`,
	})
	if listResult.IsError {
		t.Fatalf("CronList failed: %s", listResult.Output)
	}
	fmt.Printf("CronList output: %s\n", listResult.Output)
}

// --- TaskCreateAndListWorkflow integration test ---

// TestTaskCreateAndListWorkflow 测试创建后台任务后列出的完整流程
func TestTaskCreateAndListWorkflow(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// 创建后台任务
	createResult := e.Execute(context.Background(), Call{
		ID:    "task_create",
		Name:  "TaskCreate",
		Input: `{"command":"echo hello","description":"test task"}`,
	})
	if createResult.IsError {
		t.Fatalf("TaskCreate failed: %s", createResult.Output)
	}

	// 等待任务完成
	time.Sleep(2 * time.Second)

	// 列出任务
	listResult := e.Execute(context.Background(), Call{
		ID:    "task_list",
		Name:  "TaskList",
		Input: `{"status":"all"}`,
	})
	if listResult.IsError {
		t.Fatalf("TaskList failed: %s", listResult.Output)
	}
	fmt.Printf("TaskList output: %s\n", listResult.Output)
}

// --- 延迟工具加载 / Token 优化度量测试 ---

// estimateTokens 通过 JSON 序列化字节大小估算 token 数（英文约 4 字符/token）
func estimateTokens(specs []Spec) int {
	total := 0
	for _, s := range specs {
		b, _ := json.Marshal(map[string]interface{}{
			"name":         s.Name,
			"description":  s.Description,
			"input_schema": s.InputSchema,
		})
		total += len(b)
	}
	return total / 4
}

// TestAllDirect_OnlyCoreTools 验证 AllDirect 只返回核心工具（ExposureDirect）
func TestAllDirect_OnlyCoreTools(t *testing.T) {
	r := NewRegistry()
	direct := r.AllDirect()

	// 核心工具不应为空
	if len(direct) == 0 {
		t.Fatal("AllDirect should return at least core tools")
	}

	// 验证所有返回的工具都是 ExposureDirect
	for _, s := range direct {
		if s.Exposure != ExposureDirect {
			t.Errorf("Tool %q in AllDirect has Exposure=%v, expected ExposureDirect", s.Name, s.Exposure)
		}
	}

	// 验证 bash 等核心工具在 direct 列表中
	coreExpected := []string{"bash", "read_file", "write_file", "edit_file", "glob_search", "grep_search", "TodoWrite"}
	directNames := make(map[string]bool)
	for _, s := range direct {
		directNames[s.Name] = true
	}
	for _, name := range coreExpected {
		if !directNames[name] {
			t.Errorf("Core tool %q should be in AllDirect", name)
		}
	}
}

// TestDeferredToolDirectory_NonEmpty 验证延迟工具目录非空且包含预期工具
func TestDeferredToolDirectory_NonEmpty(t *testing.T) {
	r := NewRegistry()
	dir := r.DeferredToolDirectory()
	if dir == "" {
		t.Fatal("DeferredToolDirectory should not be empty")
	}

	// 验证包含已知延迟工具
	expectedDeferred := []string{"McpListTools", "LspDefinition", "schedule_cron", "TaskCreate"}
	for _, name := range expectedDeferred {
		if !strings.Contains(dir, name) {
			t.Errorf("DeferredToolDirectory should contain %q", name)
		}
	}
}

// TestLoadDeferredTool_StateManagement 验证延迟工具加载状态管理
func TestLoadDeferredTool_StateManagement(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// 初始状态：无已加载延迟工具
	loaded := e.LoadedDeferredToolNames()
	if len(loaded) != 0 {
		t.Errorf("Expected 0 loaded deferred tools initially, got %d", len(loaded))
	}

	// 加载一个延迟工具
	e.LoadDeferredTool("LspDefinition")
	loaded = e.LoadedDeferredToolNames()
	if len(loaded) != 1 {
		t.Fatalf("Expected 1 loaded deferred tool, got %d", len(loaded))
	}
	if loaded[0] != "LspDefinition" {
		t.Errorf("Expected 'LspDefinition', got %q", loaded[0])
	}

	// 重复加载（幂等）
	e.LoadDeferredTool("LspDefinition")
	loaded = e.LoadedDeferredToolNames()
	if len(loaded) != 1 {
		t.Errorf("Repeated LoadDeferredTool should be idempotent, got %d tools", len(loaded))
	}
}

// TestIsDeferredTool 验证延迟工具判断
func TestIsDeferredTool(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// 核心工具不是延迟工具
	if e.IsDeferredTool("bash") {
		t.Error("bash should not be a deferred tool")
	}

	// LSP 工具是延迟工具
	if !e.IsDeferredTool("LspDefinition") {
		t.Error("LspDefinition should be a deferred tool")
	}

	// 不存在的工具不是延迟工具
	if e.IsDeferredTool("nonexistent_tool") {
		t.Error("nonexistent tool should not be deferred")
	}
}

// TestTokenOptimization_Metrics 度量优化前后 token 降幅
func TestTokenOptimization_Metrics(t *testing.T) {
	r := NewRegistry()

	// 优化前：全量加载
	allSpecs := r.All()
	fullTokens := estimateTokens(allSpecs)

	// 优化后：只加载核心工具
	directSpecs := r.AllDirect()
	directTokens := estimateTokens(directSpecs)

	// 延迟工具目录文本的 token 估算
	dirText := r.DeferredToolDirectory()
	dirTokens := len(dirText) / 4

	optimizedTokens := directTokens + dirTokens

	t.Logf("=== Token 优化度量 ===")
	t.Logf("全量工具数: %d, 优化后核心工具数: %d", len(allSpecs), len(directSpecs))
	t.Logf("全量 schema token 估算: ~%d", fullTokens)
	t.Logf("核心 schema token 估算: ~%d", directTokens)
	t.Logf("延迟工具目录 token 估算: ~%d", dirTokens)
	t.Logf("优化后总 token 估算: ~%d", optimizedTokens)
	if fullTokens > 0 {
		reduction := (1 - float64(optimizedTokens)/float64(fullTokens)) * 100
		t.Logf("降幅: %.1f%%", reduction)
	}

	// 断言：优化后 token 必须显著低于全量
	if optimizedTokens >= fullTokens {
		t.Errorf("优化后 token (%d) 应低于全量 (%d)", optimizedTokens, fullTokens)
	}

	// 断言：核心工具数应远少于全量（至少减少 50%）
	if len(directSpecs) > len(allSpecs)/2 {
		t.Errorf("核心工具数 (%d) 应少于全量 (%d) 的一半", len(directSpecs), len(allSpecs))
	}
}
