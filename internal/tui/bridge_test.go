package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// ---- helper functions ----

// newTestBridgeRunner creates a test BridgeRunner 实例
// uses a mock AI client（指向 localhost:0）and in-memory output buffer, no external service dependencies
func newTestBridgeRunner() (*BridgeRunner, *bytes.Buffer) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite

	client := newTestClient("test-model")
	reg := newTestRegistry()
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	// create in-memory output buffer to replace stdout
	var buf bytes.Buffer

	homeDir, _ := os.UserHomeDir()
	sessionDir := homeDir + "/.whale/sessions"
	sessionStore, _ := session.NewStore(sessionDir)

	runner := &BridgeRunner{
		runnerCore: &runnerCore{
			Session:         session.NewSession(),
			Config:          &cfg,
			AIClient:        client,
			Registry:        toolRegistry,
			Executor:        tools.NewExecutor(toolRegistry, os.TempDir()),
			CmdRegistry:     newTestCmdRegistry(),
			Policy:          permission.NewPolicy(permission.LevelWorkspaceWrite),
			Budget:          ctxpkg.DefaultBudget(cfg.ContextWindow),
			Compactor:       ctxpkg.NewCompactor(estimator),
			AICompactor:     ctxpkg.NewAICompactor(client, estimator),
			Estimator:       estimator,
			Store:           sessionStore,
			Fallback:        ai.NewFallbackTracker("test-model", ""),
			MagicDocs:       config.NewMagicDocs(""), //nolint:staticcheck // H4 保留 V1 作为 fallback
			MemoryStore:     nil,
			MemoryIndex:     nil,
			MemSelector:     config.NewMemorySelector(nil, nil, client),
			MemExtractor:    config.NewAutoMemoryExtractor(nil),
			UsageTracker:    ai.NewUsageTracker(homeDir + "/.whale/usage.json"),
			ModelName:       "test-model",
			ModelRegistry:   reg,
			AvailableModels: reg.Names(),
			WorkDir:         os.TempDir(),
		},
		encoder:         json.NewEncoder(&buf),
		continueSession: false,
	}

	// initialize tool execution pipeline
	runner.Executor.InitPipelineWithHooks(runner.Policy, os.TempDir())

	// N15: 初始化 PromptBuilder（与 NewRunnerCore 对称，避免 buildSystemPrompt nil panic）
	runner.PromptBuilder = prompt.NewBuilder(runner.WorkDir, runner.ModelName, runner.MagicDocs, runner.MemSelector, runner.Executor, 100*1024)

	return runner, &buf
}

// newTestCmdRegistry creates a test command registry
func newTestCmdRegistry() *command.Registry {
	return command.NewRegistry()
}

// parseJSONRPCMessages parses all JSON-RPC messages from buffer
func parseJSONRPCMessages(buf *bytes.Buffer) []map[string]interface{} {
	var messages []map[string]interface{}
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err == nil {
			messages = append(messages, msg)
		}
	}
	return messages
}

// findNotifications finds notifications with specified method from message list
func findNotifications(msgs []map[string]interface{}, method string) []map[string]interface{} {
	var result []map[string]interface{}
	for _, msg := range msgs {
		if m, ok := msg["method"].(string); ok && m == method {
			result = append(result, msg)
		}
	}
	return result
}

// findResponses finds responses with specified ID from message list
func findResponses(msgs []map[string]interface{}, id float64) []map[string]interface{} {
	var result []map[string]interface{}
	for _, msg := range msgs {
		if msgID, ok := msg["id"].(float64); ok && msgID == id {
			result = append(result, msg)
		}
	}
	return result
}

// ---- JSON-RPC protocol structure tests ----

// TestBridgeJSONRPCRequest tests JSON-RPC request serialization/deserialization
// verifies request message conforms to JSON-RPC 2.0 spec
func TestBridgeJSONRPCRequest(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "chat",
		Params:  map[string]interface{}{"message": "hello"},
		ID:      intPtr(1),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("jsonRPCRequest serialization failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("jsonRPCRequest deserialization failed: %v", err)
	}
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc field expected '2.0'，actual %v", parsed["jsonrpc"])
	}
	if parsed["method"] != "chat" {
		t.Errorf("method field expected 'chat'，actual %v", parsed["method"])
	}
	if parsed["id"] != float64(1) {
		t.Errorf("id field expected 1，actual %v", parsed["id"])
	}
}

// TestBridgeJSONRPCResponse tests JSON-RPC response serialization/deserialization
// verifies both success and error responses conform to JSON-RPC 2.0 spec
func TestBridgeJSONRPCResponse(t *testing.T) {
	t.Run("success response", func(t *testing.T) {
		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			Result:  "ok",
			ID:      1,
		}
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("jsonRPCResponse serialization failed: %v", err)
		}

		var parsed map[string]interface{}
		json.Unmarshal(data, &parsed)
		if parsed["jsonrpc"] != "2.0" {
			t.Errorf("jsonrpc expected '2.0'，actual %v", parsed["jsonrpc"])
		}
		if parsed["result"] != "ok" {
			t.Errorf("result expected 'ok'，actual %v", parsed["result"])
		}
	})

	t.Run("error response", func(t *testing.T) {
		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32600, Message: "Invalid Request"},
			ID:      2,
		}
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("jsonRPCResponse error format serialization failed: %v", err)
		}

		var parsed map[string]interface{}
		json.Unmarshal(data, &parsed)
		errObj, ok := parsed["error"].(map[string]interface{})
		if !ok {
			t.Fatal("error field should be an object")
		}
		if errObj["code"] != float64(-32600) {
			t.Errorf("error.code expected -32600，actual %v", errObj["code"])
		}
		if errObj["message"] != "Invalid Request" {
			t.Errorf("error.message expected 'Invalid Request'，actual %v", errObj["message"])
		}
	})
}

// TestBridgeJSONRPCNotification tests JSON-RPC notification serialization
// verifies notification has no id field（JSON-RPC 2.0 specification）
func TestBridgeJSONRPCNotification(t *testing.T) {
	notif := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  "stream",
		Params:  streamParams{Type: "text", Content: "hello"},
	}

	data, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("jsonRPCNotification serialization failed: %v", err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc expected '2.0'，actual %v", parsed["jsonrpc"])
	}
	if parsed["method"] != "stream" {
		t.Errorf("method expected 'stream'，actual %v", parsed["method"])
	}
	if _, hasID := parsed["id"]; hasID {
		t.Error("notification message should not contain id field")
	}
}

// TestBridgeStreamParams tests parameter structure for 7 stream types
// verifies completeness and correctness of parameter fields for each stream notification type
func TestBridgeStreamParams(t *testing.T) {
	tests := []struct {
		name   string
		params streamParams
	}{
		{"text 类型", streamParams{Type: "text", Content: "Hello"}},
		{"thinking 类型", streamParams{Type: "thinking", Content: "thinking..."}},
		{"tool_use 类型", streamParams{Type: "tool_use", Name: "read_file"}},
		{"tool_result 类型", streamParams{Type: "tool_result", Name: "read_file", Output: "file content"}},
		{"cost 类型", streamParams{Type: "cost", InputTokens: 100, OutputTokens: 50}},
		{"error 类型", streamParams{Type: "error", Content: "连接超时"}},
		{"done 类型", streamParams{Type: "done"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.params)
			if err != nil {
				t.Fatalf("streamParams serialization failed: %v", err)
			}

			var parsed map[string]interface{}
			json.Unmarshal(data, &parsed)

			if parsed["type"] != tt.params.Type {
				t.Errorf("type expected %q，actual %v", tt.params.Type, parsed["type"])
			}
		})
	}
}

// ---- Bridge 方法测试 ----

// TestBridgeHandleNewSession 测试新建会话
// 验证 newSession 方法重置会话状态并返回success response
func TestBridgeHandleNewSession(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// 先添加一些消息到当前会话
	runner.Session.SetMessages(append(runner.Session.GetMessages(), session.UserText("test")))
	runner.InputTokens = 100
	runner.OutputTokens = 50
	runner.SessionName = "old-session"

	// 执行 newSession
	id := 1
	runner.handleNewSession(&id)

	// 验证会话被重置
	if len(runner.Session.GetMessages()) != 0 {
		t.Errorf("newSession 后消息列表should be empty，actual %d 条", len(runner.Session.GetMessages()))
	}
	if runner.InputTokens != 0 {
		t.Errorf("newSession 后 inputTokens 应为 0，actual %d", runner.InputTokens)
	}
	if runner.OutputTokens != 0 {
		t.Errorf("newSession 后 outputTokens 应为 0，actual %d", runner.OutputTokens)
	}
	if runner.SessionName != "" {
		t.Errorf("newSession 后 sessionName should be empty，actual %q", runner.SessionName)
	}

	// 验证返回了success response
	msgs := parseJSONRPCMessages(buf)
	responses := findResponses(msgs, 1)
	if len(responses) == 0 {
		t.Error("should return id=1 的响应")
	} else if responses[0]["result"] != "ok" {
		t.Errorf("响应 result expected 'ok'，actual %v", responses[0]["result"])
	}
}

// TestBridgeHandleAbort 测试中止生成
// 验证 abort 方法取消当前操作并发送 done 通知
func TestBridgeHandleAbort(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// mock正在生成的状态
	runner.cancelFunc = func() {}
	runner.State = stateStreaming

	// 执行 abort
	runner.handleAbort()

	// 验证状态回到 idle
	if runner.State != stateIdle {
		t.Errorf("abort 后状态应为 stateIdle，actual %d", runner.State)
	}
	// 验证 cancelFunc 被清空
	if runner.cancelFunc != nil {
		t.Error("abort 后 cancelFunc should be nil")
	}

	// 验证发送了 done 通知
	msgs := parseJSONRPCMessages(buf)
	streamNotifs := findNotifications(msgs, "stream")
	foundDone := false
	for _, n := range streamNotifs {
		if params, ok := n["params"].(map[string]interface{}); ok {
			if params["type"] == "done" {
				foundDone = true
			}
		}
	}
	if !foundDone {
		t.Error("abort 后应发送 done 通知")
	}
}

// TestBridgeHandleChatEmptyMessage 测试空消息处理
// 验证通过 handleRequest 路由时，空消息请求返回error response
// 注意：空消息检查在 handleRequest 中，不在 handleChat 中
func TestBridgeHandleChatEmptyMessage(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// 通过 handleRequest 路由，空消息会被拦截
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "chat",
		Params:  map[string]interface{}{"message": ""},
		ID:      intPtr(1),
	}
	runner.handleRequest(req)

	msgs := parseJSONRPCMessages(buf)
	responses := findResponses(msgs, 1)
	if len(responses) == 0 {
		t.Fatal("空消息should return error响应")
	}

	errObj, ok := responses[0]["error"].(map[string]interface{})
	if !ok {
		t.Fatal("响应should contain error 对象")
	}
	if errObj["code"] != float64(-32602) {
		t.Errorf("错误码expected -32602（无效参数），actual %v", errObj["code"])
	}
}

// ---- 错误处理测试 ----

// TestBridgeHandleStreamError 测试流式错误处理
// 验证不同类型的错误触发不同的处理逻辑
func TestBridgeHandleStreamError(t *testing.T) {
	t.Run("过载错误累积529计数", func(t *testing.T) {
		runner, _ := newTestBridgeRunner()

		// 单次 529 不会降级（需要 3 次且有 fallbackModel）
		runner.handleStreamError(fmt.Errorf("Error 529: Overloaded"))

		// fallbackModel 为空，不会降级，但 529 计数会增加
		if runner.Fallback.IsDegraded() {
			t.Error("无 fallbackModel 时单次 529 不应降级")
		}
	})

	t.Run("通用错误发送 error 和 done 通知", func(t *testing.T) {
		runner, buf := newTestBridgeRunner()

		runner.handleStreamError(fmt.Errorf("unknown error"))

		msgs := parseJSONRPCMessages(buf)
		streamNotifs := findNotifications(msgs, "stream")

		foundError := false
		foundDone := false
		for _, n := range streamNotifs {
			if params, ok := n["params"].(map[string]interface{}); ok {
				if params["type"] == "error" {
					foundError = true
				}
				if params["type"] == "done" {
					foundDone = true
				}
			}
		}
		if !foundError {
			t.Error("通用错误应发送 error 通知")
		}
		if !foundDone {
			t.Error("通用错误后应发送 done 通知")
		}
	})
}

// TestBridgeFriendlyError test error信息的友好化转换
// 验证 BridgeRunner 的 friendlyError 方法将原始错误转换为User友好提示
func TestBridgeFriendlyError(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	tests := []struct {
		name     string
		errMsg   string
		contains string
	}{
		{"DNS 解析失败", "lookup example.com: no such host", "DNS resolution failed"},
		{"连接rejected", "dial tcp 127.0.0.1:8080: connection refused", "Connection refused"},
		{"I/O 超时", "read tcp: i/o timeout", "timed out"},
		{"通用错误", "something went wrong", "Error:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runner.friendlyError(fmt.Errorf("%s", tt.errMsg))
			if !strings.Contains(result, tt.contains) {
				t.Errorf("friendlyError(%q) should contain %q，actual: %q", tt.errMsg, tt.contains, result)
			}
		})
	}
}

// ---- JSON-RPC 通信方法测试 ----

// TestBridgeSendResult 测试发送success response
// 验证 sendResult 生成符合 JSON-RPC 2.0 的响应
func TestBridgeSendResult(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	id := 42
	runner.sendResult(&id, "success")

	msgs := parseJSONRPCMessages(buf)
	if len(msgs) != 1 {
		t.Fatalf("应发送 1 条消息，actual %d 条", len(msgs))
	}

	msg := msgs[0]
	if msg["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc expected '2.0'，actual %v", msg["jsonrpc"])
	}
	if msg["result"] != "success" {
		t.Errorf("result expected 'success'，actual %v", msg["result"])
	}
	if msg["id"] != float64(42) {
		t.Errorf("id expected 42，actual %v", msg["id"])
	}
}

// TestBridgeSendResultNilID 测试 id 为 nil 时不发送响应
// 验证通知类请求（无 id）不会产生响应消息
func TestBridgeSendResultNilID(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	runner.sendResult(nil, "should not send")

	msgs := parseJSONRPCMessages(buf)
	if len(msgs) != 0 {
		t.Errorf("id 为 nil 时不应发送响应，actual发送了 %d 条", len(msgs))
	}
}

// TestBridgeSendError 测试发送error response
// 验证 sendError 生成包含错误码和消息的 JSON-RPC 响应
func TestBridgeSendError(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	id := 1
	runner.sendError(&id, -32600, "Invalid Request")

	msgs := parseJSONRPCMessages(buf)
	if len(msgs) != 1 {
		t.Fatalf("应发送 1 条消息，actual %d 条", len(msgs))
	}

	msg := msgs[0]
	errObj, ok := msg["error"].(map[string]interface{})
	if !ok {
		t.Fatal("响应should contain error 对象")
	}
	if errObj["code"] != float64(-32600) {
		t.Errorf("error.code expected -32600，actual %v", errObj["code"])
	}
	if errObj["message"] != "Invalid Request" {
		t.Errorf("error.message expected 'Invalid Request'，actual %v", errObj["message"])
	}
}

// TestBridgeSendNotification 测试发送通知
// 验证通知消息没有 id 字段
func TestBridgeSendNotification(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	runner.sendNotification("ready", map[string]interface{}{
		"modelName": "test-model",
		"workDir":   "/tmp",
	})

	msgs := parseJSONRPCMessages(buf)
	if len(msgs) != 1 {
		t.Fatalf("应发送 1 条消息，actual %d 条", len(msgs))
	}

	msg := msgs[0]
	if msg["method"] != "ready" {
		t.Errorf("method expected 'ready'，actual %v", msg["method"])
	}
	if _, hasID := msg["id"]; hasID {
		t.Error("notification message should not contain id field")
	}
}

// TestBridgeSendStream 测试发送流式通知
// 验证 sendStream 方法将参数包装为 stream 通知
func TestBridgeSendStream(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	runner.sendStream("text", "Hello", "", nil, "", 0, 0, 0)

	msgs := parseJSONRPCMessages(buf)
	if len(msgs) != 1 {
		t.Fatalf("应发送 1 条消息，actual %d 条", len(msgs))
	}

	msg := msgs[0]
	if msg["method"] != "stream" {
		t.Errorf("method expected 'stream'，actual %v", msg["method"])
	}
	params, ok := msg["params"].(map[string]interface{})
	if !ok {
		t.Fatal("stream 通知应有 params 对象")
	}
	if params["type"] != "text" {
		t.Errorf("params.type expected 'text'，actual %v", params["type"])
	}
	if params["content"] != "Hello" {
		t.Errorf("params.content expected 'Hello'，actual %v", params["content"])
	}
}

// ---- 请求路由测试 ----

// TestBridgeHandleRequest 测试请求路由
// 验证 handleRequest 正确分发到对应的处理方法
func TestBridgeHandleRequest(t *testing.T) {
	t.Run("newSession 方法路由", func(t *testing.T) {
		runner, buf := newTestBridgeRunner()

		req := jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  "newSession",
			ID:      intPtr(2),
		}
		runner.handleRequest(req)

		msgs := parseJSONRPCMessages(buf)
		responses := findResponses(msgs, 2)
		if len(responses) == 0 {
			t.Error("newSession 请求should return响应")
		}
	})

	t.Run("abort 方法路由", func(t *testing.T) {
		runner, buf := newTestBridgeRunner()

		runner.State = stateStreaming
		runner.cancelFunc = func() {}

		req := jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  "abort",
			ID:      intPtr(3),
		}
		runner.handleRequest(req)

		msgs := parseJSONRPCMessages(buf)
		responses := findResponses(msgs, 3)
		if len(responses) == 0 {
			t.Error("abort 请求should return响应")
		}
	})

	t.Run("未知方法返回错误", func(t *testing.T) {
		runner, buf := newTestBridgeRunner()

		req := jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  "unknownMethod",
			ID:      intPtr(4),
		}
		runner.handleRequest(req)

		msgs := parseJSONRPCMessages(buf)
		responses := findResponses(msgs, 4)
		if len(responses) == 0 {
			t.Fatal("未知方法should return error响应")
		}
		errObj, ok := responses[0]["error"].(map[string]interface{})
		if !ok {
			t.Fatal("响应should contain error 对象")
		}
		if errObj["code"] != float64(-32601) {
			t.Errorf("未知方法错误码expected -32601，actual %v", errObj["code"])
		}
	})

	t.Run("chat 缺少 message 参数", func(t *testing.T) {
		runner, buf := newTestBridgeRunner()

		req := jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  "chat",
			Params:  map[string]interface{}{}, // 缺少 message
			ID:      intPtr(5),
		}
		runner.handleRequest(req)

		msgs := parseJSONRPCMessages(buf)
		responses := findResponses(msgs, 5)
		if len(responses) == 0 {
			t.Fatal("缺少参数should return error响应")
		}
		errObj, ok := responses[0]["error"].(map[string]interface{})
		if !ok {
			t.Fatal("响应should contain error 对象")
		}
		if errObj["code"] != float64(-32602) {
			t.Errorf("缺少参数错误码expected -32602，actual %v", errObj["code"])
		}
	})
}

// ---- 会话管理测试 ----

// TestBridgeAutoNameSession 测试会话自动命名
// 验证 autoNameSession 用 UUID 作主键、首条输入的前 10 个字符作可读别名
//
// 改造前后差异（判别点）：改造前会话「名」就是「输入_时间戳」，既当主键又当展示名；
// 现在主键是 UUID，别名在 Session.Metadata.Name 上。
func TestBridgeAutoNameSession(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	runner.FirstUserInput = "这是一个很长的User输入用于测试自动命名功能"

	runner.autoNameSession()

	if runner.SessionName == "" {
		t.Fatal("autoNameSession 应生成会话身份")
	}

	// 主键必须是 UUID（不是从输入派生的名字）
	if !session.IsSessionID(runner.SessionName) {
		t.Errorf("会话主键应为 UUID，实际 %q", runner.SessionName)
	}

	// 可读别名在 Metadata.Name 上，包含输入的前 10 个字符
	runes := []rune("这是一个很长的User输入用于测试自动命名功能")
	expectedPrefix := string(runes[:10])
	if !strings.Contains(runner.Session.Metadata.Name, expectedPrefix) {
		t.Errorf("会话别名 should contain %q，actual %q", expectedPrefix, runner.Session.Metadata.Name)
	}
}

// TestBridgeSaveSession 测试会话增量保存
// 验证 saveSession 仅保存新增的消息
func TestBridgeSaveSession(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// 添加消息
	runner.Session.SetMessages(append(runner.Session.GetMessages(), session.UserText("hello")))
	runner.FirstUserInput = "hello"

	runner.saveSession()

	if runner.SavedMsgCount != 1 {
		t.Errorf("savedMsgCount expected 1，actual %d", runner.SavedMsgCount)
	}

	// 添加更多消息
	runner.Session.SetMessages(append(runner.Session.GetMessages(),
		session.AssistantWithTools([]session.ContentBlock{
			{Type: "text", Text: "Hi there"},
		}, "test-model", nil)))

	runner.saveSession()

	if runner.SavedMsgCount != 2 {
		t.Errorf("第二次保存后 savedMsgCount expected 2，actual %d", runner.SavedMsgCount)
	}
}

// ---- 上下文压缩测试 ----

// TestBridgeHandleCompact test context压缩
// 验证 handleCompact 方法能正确压缩会话消息
//
// 手动 /compact 是显式强制请求，不应依赖自动压缩的 token 阈值。
func TestBridgeHandleCompact(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// 12 条消息超过 balanced 策略保留的最近 10 条。
	for i := 0; i < 6; i++ {
		runner.Session.SetMessages(append(runner.Session.GetMessages(),
			session.UserText(fmt.Sprintf("CONTEXT-%c", 'A'+rune(i)))))
		runner.Session.SetMessages(append(runner.Session.GetMessages(),
			session.AssistantWithTools([]session.ContentBlock{
				{Type: "text", Text: fmt.Sprintf("CONTEXT-%c", 'A'+rune(i))},
			}, "test-model", nil)))
	}

	beforeCount := len(runner.Session.GetMessages())
	result := runner.handleCompact("")

	if len(runner.Session.GetMessages()) >= beforeCount {
		t.Errorf("压缩后消息数应减少，压缩前 %d，压缩后 %d", beforeCount, len(runner.Session.GetMessages()))
	}

	if result == "" {
		t.Error("handleCompact should return压缩结果信息")
	}
	// 批 3：英文主行 + 缩进中文辅行（意图不变、覆盖不缩水）。
	if !strings.Contains(result, "Historical messages summarized: 2 -> 1 summary") || !strings.Contains(result, "历史消息已摘要：2 -> 1 条摘要") || !strings.Contains(result, "Messages: 12 -> 11") || !strings.Contains(result, "消息数：12 -> 11") {
		t.Fatalf("result=%q, want explicit summary and message counts", result)
	}
	if runner.Session.GetMessages()[0].Metadata["compact_trigger"] != "manual" {
		t.Fatalf("compact boundary trigger=%v, want manual", runner.Session.GetMessages()[0].Metadata["compact_trigger"])
	}
}

func TestBridgeContextStatusUsesSharedLiveFormatter(t *testing.T) {
	runner, _ := newTestBridgeRunner()
	got := runner.handleContextInfo()
	if !strings.Contains(got, "上下文信息") || strings.Contains(got, "Messages: -") {
		t.Fatalf("bridge context returned placeholder instead of live status: %q", got)
	}
}

func TestBridgeHandleCompactShortConversationExplainsNoOp(t *testing.T) {
	runner, _ := newTestBridgeRunner()
	for i := 0; i < 4; i++ {
		runner.Session.SetMessages(append(runner.Session.GetMessages(),
			session.UserText(fmt.Sprintf("CONTEXT-%c", 'A'+rune(i))),
			session.AssistantText(fmt.Sprintf("CONTEXT-%c", 'A'+rune(i)), "test-model", nil),
		))
	}
	before := runner.Session.GetMessages()
	result := runner.handleCompact("")

	if !strings.Contains(result, "8 eligible messages") || !strings.Contains(result, "need at least 11") {
		t.Fatalf("result=%q, want structured insufficient-messages reason", result)
	}
	if len(runner.Session.GetMessages()) != len(before) {
		t.Fatalf("no-op compact changed message count: %d -> %d", len(before), len(runner.Session.GetMessages()))
	}
}

func newCompactTestModel(t *testing.T) *Model {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	estimator := ai.NewTokenEstimator()
	client := newTestClient("test-model")
	toolRegistry := tools.NewRegistry()
	m := &Model{
		SessionModel: SessionModel{
			session:   session.NewSession(),
			config:    &cfg,
			modelName: "test-model",
		},
		AIModel: AIModel{
			aiClient:    client,
			compactor:   ctxpkg.NewCompactor(estimator),
			aiCompactor: ctxpkg.NewAICompactor(client, estimator),
			estimator:   estimator,
			budget:      ctxpkg.DefaultBudget(cfg.ContextWindow),
		},
		ToolModel: ToolModel{
			registry: toolRegistry,
			executor: tools.NewExecutor(toolRegistry, t.TempDir()),
		},
	}
	m.reassignServices()
	return m
}

func TestModelHandleCompactForcesStateOkConversation(t *testing.T) {
	m := newCompactTestModel(t)
	m.session.SetMessages(append(m.session.GetMessages(), markerMessagesForTUI(6)...))

	result := m.handleCompact("")
	// 批 3：英文主行 + 缩进中文辅行（意图不变、覆盖不缩水）。
	if !strings.Contains(result, "Historical messages summarized: 2 -> 1 summary") || !strings.Contains(result, "历史消息已摘要：2 -> 1 条摘要") || !strings.Contains(result, "Messages: 12 -> 11") || !strings.Contains(result, "消息数：12 -> 11") {
		t.Fatalf("result=%q, want explicit summary and message counts", result)
	}
	if m.compactCount != 1 {
		t.Fatalf("compactCount=%d, want 1", m.compactCount)
	}
	if got := m.session.GetMessages()[0].Metadata["compact_trigger"]; got != "manual" {
		t.Fatalf("compact_trigger=%v, want manual", got)
	}
}

func TestModelHandleCompactNoOpDoesNotIncrementCount(t *testing.T) {
	m := newCompactTestModel(t)
	m.session.SetMessages(append(m.session.GetMessages(), markerMessagesForTUI(4)...))
	before := m.session.GetMessages()

	result := m.handleCompact("")
	if !strings.Contains(result, "8 eligible messages") {
		t.Fatalf("result=%q, want structured no-op reason", result)
	}
	if m.compactCount != 0 {
		t.Fatalf("compactCount=%d, want 0", m.compactCount)
	}
	if len(m.session.GetMessages()) != len(before) {
		t.Fatalf("no-op changed message count: %d -> %d", len(before), len(m.session.GetMessages()))
	}
}

func markerMessagesForTUI(turns int) []session.Message {
	messages := make([]session.Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		marker := fmt.Sprintf("CONTEXT-%c", 'A'+rune(i))
		messages = append(messages,
			session.UserText(marker),
			session.AssistantText(marker, "test-model", nil),
		)
	}
	return messages
}

func TestParseManualCompactConfigForceAlias(t *testing.T) {
	cfg, usage := parseManualCompactConfig("force", config.CompactBalanced)
	if usage != "" {
		t.Fatalf("force alias returned usage: %s", usage)
	}
	if cfg.Trigger != ctxpkg.CompactionTriggerManual {
		t.Fatalf("trigger=%q, want manual", cfg.Trigger)
	}
	if cfg.PreserveRecentMessages != 10 {
		t.Fatalf("preserve=%d, want 10", cfg.PreserveRecentMessages)
	}
}

// ---- 辅助方法测试 ----

// TestBridgeEstimateCurrentTokens 测试 token 估算
// 验证 estimateCurrentTokens 返回合理的估算值
func TestBridgeEstimateCurrentTokens(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	// 空会话 token 数应为 0
	if tokens := runner.estimateCurrentTokens(); tokens != 0 {
		t.Errorf("空会话 token 数expected 0，actual %d", tokens)
	}

	// 添加消息后 token 数应增加
	runner.Session.SetMessages(append(runner.Session.GetMessages(), session.UserText("hello world")))
	if tokens := runner.estimateCurrentTokens(); tokens <= 0 {
		t.Errorf("有消息时 token 数should be greater than 0，actual %d", tokens)
	}
}

// TestBridgeWriteJSON test thread safety的 JSON 写入
// 验证并发写入不会产生交错或损坏的 JSON
func TestBridgeWriteJSON(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			runner.writeJSON(map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "test",
				"n":       n,
			})
		}(i)
	}
	wg.Wait()

	// 验证每行都是合法 JSON
	lines := strings.Split(buf.String(), "\n")
	validCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Errorf("并发写入产生了无效 JSON: %q, 错误: %v", line, err)
		} else {
			validCount++
		}
	}
	if validCount != 10 {
		t.Errorf("并发写入应产生 10 条有效 JSON，actual %d 条", validCount)
	}
}

// ---- 状态管理测试 ----

// TestBridgeStateTransitions 测试 Bridge state transition
// 验证 BridgeRunner 在各种操作下的状态变化
func TestBridgeStateTransitions(t *testing.T) {
	t.Run("初始状态为 idle", func(t *testing.T) {
		runner, _ := newTestBridgeRunner()
		if runner.State != stateIdle {
			t.Errorf("初始状态应为 stateIdle(%d)，actual %d", stateIdle, runner.State)
		}
	})

	t.Run("abort 后状态回到 idle", func(t *testing.T) {
		runner, _ := newTestBridgeRunner()

		runner.State = stateStreaming
		runner.cancelFunc = func() {}
		runner.handleAbort()

		if runner.State != stateIdle {
			t.Errorf("abort 后状态应为 stateIdle，actual %d", runner.State)
		}
	})
}

// ---- Ready 通知测试 ----

// TestBridgeReadyNotification 测试 Bridge 启动时的 ready 通知
// 验证 Run 方法启动时发送 ready 通知
func TestBridgeReadyNotification(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// mock Run 方法中的 ready 通知
	runner.sendNotification("ready", map[string]interface{}{
		"modelName": runner.ModelName,
		"workDir":   runner.WorkDir,
	})

	msgs := parseJSONRPCMessages(buf)
	readyNotifs := findNotifications(msgs, "ready")
	if len(readyNotifs) != 1 {
		t.Fatalf("应发送 1 条 ready 通知，actual %d 条", len(readyNotifs))
	}

	params, ok := readyNotifs[0]["params"].(map[string]interface{})
	if !ok {
		t.Fatal("ready 通知应有 params 对象")
	}
	if params["modelName"] != "test-model" {
		t.Errorf("ready modelName expected 'test-model'，actual %v", params["modelName"])
	}
}

// ---- 协议往返测试 ----

// TestBridgeProtocolRoundTrip test protocol消息的完整序列化/反序列化流程
// 验证 Go 侧和 TS 侧共享的协议定义consistency
func TestBridgeProtocolRoundTrip(t *testing.T) {
	t.Run("请求往返", func(t *testing.T) {
		original := jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  "chat",
			Params:  map[string]interface{}{"message": "测试消息"},
			ID:      intPtr(1),
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("serialization failed: %v", err)
		}

		var parsed jsonRPCRequest
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("deserialization failed: %v", err)
		}

		if parsed.JSONRPC != "2.0" {
			t.Errorf("jsonrpc expected '2.0'，actual %q", parsed.JSONRPC)
		}
		if parsed.Method != "chat" {
			t.Errorf("method expected 'chat'，actual %q", parsed.Method)
		}
		if parsed.ID == nil || *parsed.ID != 1 {
			t.Errorf("id expected 1，actual %v", parsed.ID)
		}
	})

	t.Run("流式通知往返", func(t *testing.T) {
		original := jsonRPCNotification{
			JSONRPC: "2.0",
			Method:  "stream",
			Params: streamParams{
				Type: "tool_use",
				Name: "write_file",
			},
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("serialization failed: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("deserialization failed: %v", err)
		}

		if parsed["method"] != "stream" {
			t.Errorf("method expected 'stream'，actual %v", parsed["method"])
		}
		params := parsed["params"].(map[string]interface{})
		if params["type"] != "tool_use" {
			t.Errorf("params.type expected 'tool_use'，actual %v", params["type"])
		}
		if params["name"] != "write_file" {
			t.Errorf("params.name expected 'write_file'，actual %v", params["name"])
		}
	})

	t.Run("error response往返", func(t *testing.T) {
		original := jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32601, Message: "Method not found"},
			ID:      99,
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("serialization failed: %v", err)
		}

		var parsed jsonRPCResponse
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("deserialization failed: %v", err)
		}

		if parsed.JSONRPC != "2.0" {
			t.Errorf("jsonrpc expected '2.0'，actual %q", parsed.JSONRPC)
		}
		if parsed.Error == nil {
			t.Fatal("error 不should be nil")
		}
		if parsed.Error.Code != -32601 {
			t.Errorf("error.code expected -32601，actual %d", parsed.Error.Code)
		}
		if parsed.Error.Message != "Method not found" {
			t.Errorf("error.message expected 'Method not found'，actual %q", parsed.Error.Message)
		}
		if parsed.ID != 99 {
			t.Errorf("id expected 99，actual %d", parsed.ID)
		}
	})
}

// TestBridgeBuildSystemPrompt 测试系统提示构建
// 验证 buildSystemPrompt 返回包含关键信息的提示列表
func TestBridgeBuildSystemPrompt(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	prompts := runner.buildSystemPrompt()
	combined := strings.Join(layerBlocksToStrings(prompts), " ")

	if !strings.Contains(combined, "Whale Code") {
		t.Error("系统提示should contain 'Whale Code'")
	}
	if !strings.Contains(combined, "Working directory") {
		t.Error("系统提示should contain 'Working directory'")
	}
	if !strings.Contains(combined, "test-model") {
		t.Error("系统提示should contain模型名 'test-model'")
	}
}

// TestBridgeBuildMessages 测试消息构建
// 验证 buildMessages 正确转换会话消息为 AI 请求格式
func TestBridgeBuildMessages(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	runner.Session.SetMessages(append(runner.Session.GetMessages(), session.UserText("hello")))

	entries := runner.buildMessages()
	if len(entries) != 1 {
		t.Fatalf("expected 1 条消息，actual %d 条", len(entries))
	}
	if entries[0].Role != "user" {
		t.Errorf("角色expected 'user'，actual %q", entries[0].Role)
	}
}

// TestBridgeBuildToolDefs test tool定义构建
// 验证 buildToolDefs 返回注册的工具定义列表
func TestBridgeBuildToolDefs(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	defs := runner.buildToolDefs()
	if len(defs) == 0 {
		t.Error("应至少有一个工具定义")
	}
}

// TestBridgeHandleUndoRedo 测试撤销/重做
// 验证 handleUndo 和 handleRedo 在无操作时返回提示信息
func TestBridgeHandleUndoRedo(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	t.Run("无操作可撤销", func(t *testing.T) {
		result := runner.handleUndo()
		if !strings.Contains(result, "没有可撤销") {
			t.Errorf("无操作可撤销时应提示，actual: %q", result)
		}
	})

	t.Run("无操作可重做", func(t *testing.T) {
		result := runner.handleRedo()
		if !strings.Contains(result, "没有可重做") {
			t.Errorf("无操作可重做时应提示，actual: %q", result)
		}
	})
}

// TestBridgeResumeLastSession test recovery上次会话
// 验证 resumeLastSession 在无会话可恢复时不崩溃
func TestBridgeResumeLastSession(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// 使用临时目录作为会话存储
	tmpDir, _ := os.MkdirTemp("", "whale-test-sessions-*")
	defer os.RemoveAll(tmpDir)
	store, _ := session.NewStore(tmpDir)
	runner.Store = store

	// 无会话可恢复，不应崩溃
	runner.resumeLastSession()

	// 不应发送任何消息（无会话可恢复）
	msgs := parseJSONRPCMessages(buf)
	if len(msgs) != 0 {
		t.Errorf("无会话可恢复时不应发送消息，actual发送 %d 条", len(msgs))
	}
}

// TestBridgeJSONRPCErrorCodes 测试 JSON-RPC 标准错误码
// 验证 Bridge 使用正确的 JSON-RPC 2.0 错误码
func TestBridgeJSONRPCErrorCodes(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		meaning string
	}{
		{"Parse error", -32700, "解析错误"},
		{"Invalid Request", -32600, "无效请求"},
		{"Method not found", -32601, "方法未找到"},
		{"Invalid params", -32602, "无效参数"},
		{"Internal error", -32603, "内部错误"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, buf := newTestBridgeRunner()

			id := 1
			runner.sendError(&id, tt.code, tt.meaning)

			msgs := parseJSONRPCMessages(buf)
			if len(msgs) != 1 {
				t.Fatalf("应发送 1 条消息，actual %d 条", len(msgs))
			}

			errObj, ok := msgs[0]["error"].(map[string]interface{})
			if !ok {
				t.Fatal("响应should contain error 对象")
			}
			if errObj["code"] != float64(tt.code) {
				t.Errorf("错误码expected %d，actual %v", tt.code, errObj["code"])
			}
		})
	}
}

// ---- helper functions ----

// intPtr 返回 int 的指针，用于 JSON-RPC 请求的 ID 字段
func intPtr(n int) *int {
	return &n
}

// ---- 斜杠命令调度测试 (CMD-P1-001) ----

// TestBridgeSlashCommand_Help 测试 /help 命令在 Bridge 模式下的调度
// 验证 IDE 收到包含帮助文本的 text stream 通知和 done 通知
func TestBridgeSlashCommand_Help(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	id := 1
	runner.handleChat("/help", &id, nil)

	msgs := parseJSONRPCMessages(buf)
	streamNotifs := findNotifications(msgs, "stream")

	foundText := false
	foundDone := false
	for _, n := range streamNotifs {
		if params, ok := n["params"].(map[string]interface{}); ok {
			if params["type"] == "text" && params["content"] != "" {
				foundText = true
			}
			if params["type"] == "done" {
				foundDone = true
			}
		}
	}
	if !foundText {
		t.Error("/help 应发送 text stream 通知")
	}
	if !foundDone {
		t.Error("/help 应发送 done stream 通知")
	}

	// 验证返回了 result 响应
	responses := findResponses(msgs, 1)
	if len(responses) == 0 {
		t.Error("/help 应返回 id=1 的响应")
	}
}

// TestBridgeSlashCommand_Clear 测试 /clear 命令在 Bridge 模式下的调度
// 验证会话被清空且 IDE 收到通知
func TestBridgeSlashCommand_Clear(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// 预填充会话数据
	runner.Session.SetMessages(append(runner.Session.GetMessages(), session.UserText("test")))
	runner.InputTokens = 100
	runner.OutputTokens = 50

	id := 1
	runner.handleChat("/clear", &id, nil)

	// 验证会话被清空
	if len(runner.Session.GetMessages()) != 0 {
		t.Errorf("/clear 后消息列表应为空，实际 %d 条", len(runner.Session.GetMessages()))
	}
	if runner.InputTokens != 0 {
		t.Errorf("/clear 后 InputTokens 应为 0，实际 %d", runner.InputTokens)
	}
	if runner.OutputTokens != 0 {
		t.Errorf("/clear 后 OutputTokens 应为 0，实际 %d", runner.OutputTokens)
	}

	// 验证发送了 text 和 done 通知
	msgs := parseJSONRPCMessages(buf)
	streamNotifs := findNotifications(msgs, "stream")
	foundText := false
	foundDone := false
	for _, n := range streamNotifs {
		if params, ok := n["params"].(map[string]interface{}); ok {
			if params["type"] == "text" {
				foundText = true
			}
			if params["type"] == "done" {
				foundDone = true
			}
		}
	}
	if !foundText {
		t.Error("/clear 应发送 text stream 通知")
	}
	if !foundDone {
		t.Error("/clear 应发送 done stream 通知")
	}
}

// TestBridgeSlashCommand_Model 测试 /model 命令在 Bridge 模式下的调度
// 验证 IDE 收到模型信息通知
func TestBridgeSlashCommand_Model(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	id := 1
	runner.handleChat("/model", &id, nil)

	msgs := parseJSONRPCMessages(buf)
	streamNotifs := findNotifications(msgs, "stream")

	foundText := false
	foundDone := false
	for _, n := range streamNotifs {
		if params, ok := n["params"].(map[string]interface{}); ok {
			if params["type"] == "text" && params["content"] != "" {
				foundText = true
			}
			if params["type"] == "done" {
				foundDone = true
			}
		}
	}
	if !foundText {
		t.Error("/model 应发送 text stream 通知")
	}
	if !foundDone {
		t.Error("/model 应发送 done stream 通知")
	}
}

// TestBridgeSlashCommand_ModelSwitch 测试 /model 带参数切换模型
// 验证 ModelName 被更新
func TestBridgeSlashCommand_ModelSwitch(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	originalModel := runner.ModelName
	id := 1
	runner.handleChat("/model gpt-4o-test", &id, nil)

	if runner.ModelName != "gpt-4o-test" {
		t.Errorf("/model gpt-4o-test 后 ModelName 应为 gpt-4o-test，实际 %q (原 %q)", runner.ModelName, originalModel)
	}
}

// TestBridgeSlashCommand_Unknown 测试未知命令返回拼写建议
func TestBridgeSlashCommand_Unknown(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	id := 1
	runner.handleChat("/hlep", &id, nil)

	msgs := parseJSONRPCMessages(buf)
	streamNotifs := findNotifications(msgs, "stream")

	foundText := false
	foundDone := false
	var textContent string
	for _, n := range streamNotifs {
		if params, ok := n["params"].(map[string]interface{}); ok {
			if params["type"] == "text" {
				foundText = true
				textContent, _ = params["content"].(string)
			}
			if params["type"] == "done" {
				foundDone = true
			}
		}
	}
	if !foundText {
		t.Error("未知命令应发送 text stream 通知")
	}
	if !foundDone {
		t.Error("未知命令应发送 done stream 通知")
	}
	if !strings.Contains(textContent, "Unknown") {
		t.Errorf("未知命令输出应包含 'Unknown'，实际 %q", textContent)
	}
}

// TestBridgeSlashCommand_NotSentToAI 测试斜杠命令不会被加入会话消息
// 验证命令消息不作为 user message 被 append 到 Session
func TestBridgeSlashCommand_NotSentToAI(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	msgCountBefore := len(runner.Session.GetMessages())

	id := 1
	runner.handleChat("/help", &id, nil)

	msgCountAfter := len(runner.Session.GetMessages())
	if msgCountAfter != msgCountBefore {
		t.Errorf("斜杠命令不应加入会话消息，before=%d after=%d", msgCountBefore, msgCountAfter)
	}
}
