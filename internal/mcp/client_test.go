package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockMCPServer implements a simple MCP server for testing
type mockMCPServer struct {
	stdin     io.Reader
	stdout    io.Writer
	tools     []ToolInfo
	resources []Resource
	prompts   []PromptInfo
	mu        sync.Mutex
}

func newMockMCPServer() *mockMCPServer {
	return &mockMCPServer{
		tools: []ToolInfo{
			{
				Name:        "echo",
				Description: "Echo back the input",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{"type": "string"},
					},
					"required": []string{"message"},
				},
			},
			{
				Name:        "add",
				Description: "Add two numbers",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"a": map[string]interface{}{"type": "number"},
						"b": map[string]interface{}{"type": "number"},
					},
					"required": []string{"a", "b"},
				},
			},
		},
		resources: []Resource{
			{URI: "file:///test/hello.txt", Name: "hello.txt", MimeType: "text/plain"},
			{URI: "file:///test/data.json", Name: "data.json", MimeType: "application/json"},
		},
		prompts: []PromptInfo{
			{
				Name:        "greet",
				Description: "Generate a greeting",
				Arguments: []PromptArgument{
					{Name: "name", Description: "Person name", Required: true},
				},
			},
		},
	}
}

func (s *mockMCPServer) run(stdin io.Reader, stdout io.Writer) {
	s.stdin = stdin
	s.stdout = stdout
	reader := bufio.NewReader(stdin)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// newline-delimited：整行即一帧 JSON（MCP spec 2024-11-05 §Transports/stdio）
		buf := []byte(line)

		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(buf, &req); err != nil {
			continue
		}

		if req.ID > 0 {
			result := s.handleRequest(req.Method, req.Params)
			s.sendResponse(req.ID, result, nil)
		}
	}
}

func (s *mockMCPServer) handleRequest(method string, params json.RawMessage) interface{} {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools":     map[string]interface{}{},
				"resources": map[string]interface{}{},
				"prompts":   map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "mock-mcp-server",
				"version": "1.0.0",
			},
		}
	case "ping":
		return map[string]interface{}{}
	case "tools/list":
		return map[string]interface{}{
			"tools": s.tools,
		}
	case "tools/call":
		var input struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		json.Unmarshal(params, &input)
		return s.handleToolCall(input.Name, input.Arguments)
	case "resources/list":
		return map[string]interface{}{
			"resources": s.resources,
		}
	case "resources/read":
		var input struct {
			URI string `json:"uri"`
		}
		json.Unmarshal(params, &input)
		return map[string]interface{}{
			"contents": []map[string]interface{}{
				{"uri": input.URI, "mimeType": "text/plain", "text": fmt.Sprintf("Content of %s", input.URI)},
			},
		}
	case "prompts/list":
		return map[string]interface{}{
			"prompts": s.prompts,
		}
	case "prompts/get":
		var input struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		json.Unmarshal(params, &input)
		return s.handleGetPrompt(input.Name, input.Arguments)
	default:
		return map[string]interface{}{}
	}
}

func (s *mockMCPServer) handleToolCall(name string, arguments map[string]interface{}) interface{} {
	switch name {
	case "echo":
		msg, _ := arguments["message"].(string)
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": msg},
			},
		}
	case "add":
		a, _ := arguments["a"].(float64)
		b, _ := arguments["b"].(float64)
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("%.0f", a+b)},
			},
		}
	default:
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("Unknown tool: %s", name)},
			},
			"isError": true,
		}
	}
}

func (s *mockMCPServer) handleGetPrompt(name string, arguments map[string]string) interface{} {
	switch name {
	case "greet":
		personName := "World"
		if n, ok := arguments["name"]; ok {
			personName = n
		}
		return map[string]interface{}{
			"description": "A greeting prompt",
			"messages": []map[string]interface{}{
				{
					"role": "user",
					"content": map[string]interface{}{
						"type": "text",
						"text": fmt.Sprintf("Please greet %s warmly", personName),
					},
				},
			},
		}
	default:
		return map[string]interface{}{
			"messages": []map[string]interface{}{},
		}
	}
}

func (s *mockMCPServer) sendResponse(id int64, result interface{}, rpcErr *RPCError) {
	resultJSON, _ := json.Marshal(result)
	resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, string(resultJSON))
	if rpcErr != nil {
		errJSON, _ := json.Marshal(rpcErr)
		resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":%s}`, id, string(errJSON))
	}
	s.stdout.Write(append([]byte(resp), '\n'))
}

// mockServerCmd returns a command that runs the mock MCP server
// This uses a test helper binary approach
func startMockServer(t *testing.T) (*Client, func()) {
	t.Helper()

	// Create a Go source file for the mock server
	tmpDir := t.TempDir()
	serverSrc := filepath.Join(tmpDir, "mock_mcp_server.go")

	src := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	runServer(os.Stdin, os.Stdout)
}

func runServer(stdin io.Reader, stdout io.Writer) {
	reader := bufio.NewReader(stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// newline-delimited：整行即一帧 JSON（MCP spec 2024-11-05 §Transports/stdio）
		buf := []byte(line)
		var req struct {
			ID     int64            ` + "`json:\"id\"`" + `
			Method string           ` + "`json:\"method\"`" + `
			Params json.RawMessage  ` + "`json:\"params\"`" + `
		}
		if err := json.Unmarshal(buf, &req); err != nil {
			continue
		}
		if req.ID > 0 {
			// 工具名含 "hang"：故意**不回包**，用于构造确定性超时。
			// 有了这条路径，超时用例不必再赌"回包和超时谁先就绪"。
			if req.Method == "tools/call" && strings.Contains(string(req.Params), "hang") {
				continue
			}
			result := handleRequest(req.Method, req.Params)
			resultJSON, _ := json.Marshal(result)
			resp := fmt.Sprintf(` + "`" + `{"jsonrpc":"2.0","id":%d,"result":%s}` + "`" + `, req.ID, string(resultJSON))
			stdout.Write(append([]byte(resp), '\n'))
		}
	}
}

func handleRequest(method string, params json.RawMessage) interface{} {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools":     map[string]interface{}{},
				"resources": map[string]interface{}{},
				"prompts":   map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "mock-mcp-server",
				"version": "1.0.0",
			},
		}
	case "ping":
		return map[string]interface{}{}
	case "tools/list":
		return map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "echo",
					"description": "Echo back the input",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"message": map[string]interface{}{"type": "string"},
						},
						"required": []string{"message"},
					},
				},
				{
					"name":        "add",
					"description": "Add two numbers",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"a": map[string]interface{}{"type": "number"},
							"b": map[string]interface{}{"type": "number"},
						},
						"required": []string{"a", "b"},
					},
				},
			},
		}
	case "tools/call":
		var input struct {
			Name      string                 ` + "`json:\"name\"`" + `
			Arguments map[string]interface{} ` + "`json:\"arguments\"`" + `
		}
		json.Unmarshal(params, &input)
		switch input.Name {
		case "echo":
			msg, _ := input.Arguments["message"].(string)
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": msg},
				},
			}
		case "add":
			a, _ := input.Arguments["a"].(float64)
			b, _ := input.Arguments["b"].(float64)
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("%.0f", a+b)},
				},
			}
		default:
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": "Unknown tool: " + input.Name},
				},
				"isError": true,
			}
		}
	case "resources/list":
		return map[string]interface{}{
			"resources": []map[string]interface{}{
				{"uri": "file:///test/hello.txt", "name": "hello.txt", "mimeType": "text/plain"},
				{"uri": "file:///test/data.json", "name": "data.json", "mimeType": "application/json"},
			},
		}
	case "resources/read":
		var input struct {
			URI string ` + "`json:\"uri\"`" + `
		}
		json.Unmarshal(params, &input)
		return map[string]interface{}{
			"contents": []map[string]interface{}{
				{"uri": input.URI, "mimeType": "text/plain", "text": "Content of " + input.URI},
			},
		}
	case "prompts/list":
		return map[string]interface{}{
			"prompts": []map[string]interface{}{
				{
					"name":        "greet",
					"description": "Generate a greeting",
					"arguments": []map[string]interface{}{
						{"name": "name", "description": "Person name", "required": true},
					},
				},
			},
		}
	case "prompts/get":
		var input struct {
			Name      string            ` + "`json:\"name\"`" + `
			Arguments map[string]string ` + "`json:\"arguments\"`" + `
		}
		json.Unmarshal(params, &input)
		personName := "World"
		if n, ok := input.Arguments["name"]; ok {
			personName = n
		}
		return map[string]interface{}{
			"description": "A greeting prompt",
			"messages": []map[string]interface{}{
				{
					"role": "user",
					"content": map[string]interface{}{
						"type": "text",
						"text": "Please greet " + personName + " warmly",
					},
				},
			},
		}
	default:
		return map[string]interface{}{}
	}
}`

	if err := os.WriteFile(serverSrc, []byte(src), 0644); err != nil {
		t.Fatalf("Failed to write mock server source: %v", err)
	}

	// Build the mock server
	serverExe := filepath.Join(tmpDir, "mock_mcp_server.exe")
	buildCmd := exec.Command("go", "build", "-o", serverExe, serverSrc)
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build mock server: %v\nOutput: %s", err, string(output))
	}

	// Start the mock server as MCP server
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	client, err := NewClient(ctx, "mock", ServerConfig{
		Command: serverExe,
	})
	if err != nil {
		cancel()
		t.Fatalf("Failed to create MCP client: %v", err)
	}

	cleanup := func() {
		client.Close()
		cancel()
	}

	return client, cleanup
}

// TestMCPClientInitialize tests MCP client initialization
func TestMCPClientInitialize(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	if !client.IsReady() {
		t.Error("Client should be ready after initialization")
	}

	if client.Name() != "mock" {
		t.Errorf("Name = %s, want mock", client.Name())
	}

	caps := client.Capabilities()
	if caps.Tools == nil {
		t.Error("Server should support tools capability")
	}
	if caps.Resources == nil {
		t.Error("Server should support resources capability")
	}
	if caps.Prompts == nil {
		t.Error("Server should support prompts capability")
	}
}

// TestMCPClientPing tests MCP ping
func TestMCPClientPing(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

// TestMCPClientListTools tests listing MCP tools
func TestMCPClientListTools(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(tools) != 2 {
		t.Errorf("ListTools returned %d tools, want 2", len(tools))
	}

	if tools[0].Name != "echo" {
		t.Errorf("Tool[0].Name = %s, want echo", tools[0].Name)
	}
	if tools[1].Name != "add" {
		t.Errorf("Tool[1].Name = %s, want add", tools[1].Name)
	}

	if tools[0].InputSchema == nil {
		t.Error("Tool[0] should have InputSchema")
	}

	if !client.HasTools() {
		t.Error("HasTools should be true")
	}

	cachedTools := client.Tools()
	if len(cachedTools) != 2 {
		t.Errorf("Cached tools = %d, want 2", len(cachedTools))
	}
}

// TestMCPClientCallTool tests calling MCP tools
func TestMCPClientCallTool(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("echo tool", func(t *testing.T) {
		result, err := client.CallTool(ctx, "echo", map[string]interface{}{
			"message": "hello world",
		})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result.Text() != "hello world" {
			t.Errorf("CallTool result = %q, want %q", result.Text(), "hello world")
		}
	})

	t.Run("add tool", func(t *testing.T) {
		result, err := client.CallTool(ctx, "add", map[string]interface{}{
			"a": float64(3),
			"b": float64(5),
		})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
		}
		if result.Text() != "8" {
			t.Errorf("CallTool result = %q, want %q", result.Text(), "8")
		}
	})

	t.Run("unknown tool", func(t *testing.T) {
		_, err := client.CallTool(ctx, "nonexistent", map[string]interface{}{})
		if err == nil {
			t.Error("CallTool with unknown tool should return error")
		}
	})
}

// TestMCPClientListResources tests listing MCP resources
func TestMCPClientListResources(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resources, err := client.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}

	if len(resources) != 2 {
		t.Errorf("ListResources returned %d resources, want 2", len(resources))
	}

	if resources[0].Name != "hello.txt" {
		t.Errorf("Resource[0].Name = %s, want hello.txt", resources[0].Name)
	}

	if !client.HasResources() {
		t.Error("HasResources should be true")
	}
}

// TestMCPClientReadResource tests reading MCP resources
func TestMCPClientReadResource(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	content, err := client.ReadResource(ctx, "file:///test/hello.txt")
	if err != nil {
		t.Fatalf("ReadResource failed: %v", err)
	}

	if !strings.Contains(content, "file:///test/hello.txt") {
		t.Errorf("ReadResource content = %q, should contain URI", content)
	}
}

// TestMCPClientListPrompts tests listing MCP prompts
func TestMCPClientListPrompts(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prompts, err := client.ListPrompts(ctx)
	if err != nil {
		t.Fatalf("ListPrompts failed: %v", err)
	}

	if len(prompts) != 1 {
		t.Errorf("ListPrompts returned %d prompts, want 1", len(prompts))
	}

	if prompts[0].Name != "greet" {
		t.Errorf("Prompt[0].Name = %s, want greet", prompts[0].Name)
	}

	if len(prompts[0].Arguments) != 1 {
		t.Errorf("Prompt[0] should have 1 argument, got %d", len(prompts[0].Arguments))
	}

	if !client.HasPrompts() {
		t.Error("HasPrompts should be true")
	}
}

// TestMCPClientGetPrompt tests getting MCP prompts
func TestMCPClientGetPrompt(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("with arguments", func(t *testing.T) {
		messages, err := client.GetPrompt(ctx, "greet", map[string]string{"name": "Alice"})
		if err != nil {
			t.Fatalf("GetPrompt failed: %v", err)
		}

		if len(messages) != 1 {
			t.Errorf("GetPrompt returned %d messages, want 1", len(messages))
		}

		if messages[0].Role != "user" {
			t.Errorf("Message role = %s, want user", messages[0].Role)
		}

		if !strings.Contains(messages[0].Content.Text, "Alice") {
			t.Errorf("Message text should contain 'Alice', got: %s", messages[0].Content.Text)
		}
	})

	t.Run("without arguments", func(t *testing.T) {
		messages, err := client.GetPrompt(ctx, "greet", nil)
		if err != nil {
			t.Fatalf("GetPrompt failed: %v", err)
		}

		if len(messages) != 1 {
			t.Errorf("GetPrompt returned %d messages, want 1", len(messages))
		}

		if !strings.Contains(messages[0].Content.Text, "World") {
			t.Errorf("Message text should contain 'World' (default), got: %s", messages[0].Content.Text)
		}
	})
}

// TestMCPClientCapabilities tests capability detection
func TestMCPClientCapabilities(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	if !client.HasTools() {
		t.Error("Server should have tools capability")
	}
	if !client.HasResources() {
		t.Error("Server should have resources capability")
	}
	if !client.HasPrompts() {
		t.Error("Server should have prompts capability")
	}
}

// TestMCPClientClose tests graceful shutdown
func TestMCPClientClose(t *testing.T) {
	client, cleanup := startMockServer(t)

	if err := client.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	cleanup()
}

// TestMCPClientContextCancellation tests context cancellation
func TestMCPClientContextCancellation(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.ListTools(ctx)
	if err == nil {
		t.Error("Should return error on cancelled context")
	}
}

// TestLoadConfig tests loading MCP configuration
func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	config := map[string]interface{}{
		"servers": map[string]interface{}{
			"filesystem": map[string]interface{}{
				"command": "npx",
				"args":    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(configPath, data, 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(cfg.Servers) != 1 {
		t.Errorf("Servers count = %d, want 1", len(cfg.Servers))
	}

	fs, ok := cfg.Servers["filesystem"]
	if !ok {
		t.Error("filesystem server not found")
	}

	if fs.Command != "npx" {
		t.Errorf("Command = %s, want npx", fs.Command)
	}

	if len(fs.Args) != 3 {
		t.Errorf("Args count = %d, want 3", len(fs.Args))
	}
}

// TestLoadConfigNotFound tests loading non-existent config
func TestLoadConfigNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/mcp.json")
	if err == nil {
		t.Error("Should return error for non-existent file")
	}
}

// TestLoadConfigEmptyServers tests loading config with empty servers
func TestLoadConfigEmptyServers(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	data := []byte(`{"servers": {}}`)
	os.WriteFile(configPath, data, 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(cfg.Servers) != 0 {
		t.Errorf("Servers count = %d, want 0", len(cfg.Servers))
	}
}

// TestLoadConfigFromDirs tests loading config from multiple directories
func TestLoadConfigFromDirs(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	config := map[string]interface{}{
		"servers": map[string]interface{}{
			"test": map[string]interface{}{
				"command": "echo",
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(configPath, data, 0644)

	cfg := LoadConfigFromDirs(tmpDir, "/nonexistent")
	if len(cfg.Servers) != 1 {
		t.Errorf("Servers count = %d, want 1", len(cfg.Servers))
	}
}

// TestLoadConfigFromDirsEmpty tests loading config when no config exists
func TestLoadConfigFromDirsEmpty(t *testing.T) {
	cfg := LoadConfigFromDirs("/nonexistent1", "/nonexistent2")
	if len(cfg.Servers) != 0 {
		t.Errorf("Servers count = %d, want 0", len(cfg.Servers))
	}
}

// TestRPCError tests RPC error type
func TestRPCError(t *testing.T) {
	err := &RPCError{Code: -32600, Message: "Invalid Request"}
	if err.Error() != "MCP error [-32600]: Invalid Request" {
		t.Errorf("RPCError.Error() = %s, want MCP error [-32600]: Invalid Request", err.Error())
	}
}

// TestMCPClientNewClientFailure tests client creation with invalid command
func TestMCPClientNewClientFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := NewClient(ctx, "bad", ServerConfig{
		Command: "nonexistent_command_that_does_not_exist",
	})
	if err == nil {
		t.Error("Should return error for non-existent command")
	}
}

// TestClient_Timeout 测试 MCP 调用在 context 超时时返回错误
//
// 判据设计：旧版给 1ns 超时、同时让 mock server 正常回包。产品里的等待是
// `select { case resp := <-ch: … case <-ctx.Done(): … }`（transport_stdio.go:218），
// **两个分支同时就绪时 Go 随机挑一个** ⇒ 只要回包先落到 ch，这次就返回成功，
// 用例红。CI 上确实这样红过（"expected超时返回错误，但得到 nil"）。
// 现在改为调用 "hang"（mock server 收到请求但**永不回包**），ctx 成为唯一出路 ⇒ 确定性。
func TestClient_Timeout(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := client.CallTool(ctx, "hang", map[string]interface{}{
		"message": "server never replies",
	})
	if err == nil {
		t.Fatal("context 超时后必须返回错误，实际得到 nil（server 没回包却当成功 = 调用方会挂死）")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应满足 errors.Is(err, context.DeadlineExceeded)，实际: %v", err)
	}
}

// TestClient_Notification 测试向 MCP server发送通知
func TestClient_Notification(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	// sendNotification 不expected响应，只要不报错即可
	err := client.sendNotification("notifications/initialized", map[string]interface{}{})
	if err != nil {
		t.Errorf("sendNotification 返回错误: %v", err)
	}

	// 发送自定义通知
	err = client.sendNotification("custom/notification", map[string]interface{}{
		"key": "value",
	})
	if err != nil {
		t.Errorf("sendNotification 自定义通知返回错误: %v", err)
	}
}

// TestMCP_DiscoverAndRegister 测试从 MCP server发现工具并注册到工具注册表
func TestMCP_DiscoverAndRegister(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 验证server支持工具能力
	if !client.HasTools() {
		t.Fatal("MCP server应支持工具能力")
	}

	// 发现工具
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("应至少发现一个工具")
	}

	// mock注册到工具注册表（与 tools 包中的 DiscoverAndRegisterMCPTools 逻辑一致：
	// 均使用 mcp.BuildMcpToolName → mcp__<server>__<tool>，见 ④ 决策 2026-09-10）
	registered := make(map[string]bool)
	for _, tool := range tools {
		mcpToolName := BuildMcpToolName("mock", tool.Name)
		registered[mcpToolName] = true
	}

	// 验证注册结果
	if !registered["mcp__mock__echo"] {
		t.Error("应注册 mcp__mock__echo 工具")
	}
	if !registered["mcp__mock__add"] {
		t.Error("应注册 mcp__mock__add 工具")
	}
	if len(registered) != 2 {
		t.Errorf("注册工具数 = %d, expected 2", len(registered))
	}

	// 验证已发现的工具被缓存
	cached := client.Tools()
	if len(cached) != len(tools) {
		t.Errorf("缓存工具数 = %d, expected %d", len(cached), len(tools))
	}
}

// TestMCP_ClientCache test tool列表缓存，第二次调用使用缓存
func TestMCP_ClientCache(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 第一次调用 ListTools，结果应被缓存到 client.tools
	tools1, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("第一次 ListTools 失败: %v", err)
	}

	// 通过 Tools() 方法获取缓存
	cached := client.Tools()
	if len(cached) != len(tools1) {
		t.Errorf("缓存工具数 = %d, expected %d", len(cached), len(tools1))
	}

	// 验证缓存内容与第一次调用一致
	for i, tool := range tools1 {
		if cached[i].Name != tool.Name {
			t.Errorf("缓存工具[%d].Name = %s, expected %s", i, cached[i].Name, tool.Name)
		}
	}

	// 第二次调用 ListTools，仍然成功（服务端响应，同时更新缓存）
	tools2, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("第二次 ListTools 失败: %v", err)
	}

	if len(tools2) != len(tools1) {
		t.Errorf("第二次调用工具数 = %d, expected %d", len(tools2), len(tools1))
	}
}

// TestMCPClientFullWorkflow tests a complete MCP workflow
func TestMCPClientFullWorkflow(t *testing.T) {
	client, cleanup := startMockServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Step 1: Ping
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// Step 2: List tools
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) < 2 {
		t.Fatalf("Expected at least 2 tools, got %d", len(tools))
	}

	// Step 3: Call a tool
	result, err := client.CallTool(ctx, "echo", map[string]interface{}{
		"message": "test workflow",
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.Text() != "test workflow" {
		t.Errorf("CallTool result = %q, want %q", result.Text(), "test workflow")
	}

	// Step 4: List resources
	resources, err := client.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(resources) < 1 {
		t.Fatalf("Expected at least 1 resource, got %d", len(resources))
	}

	// Step 5: Read a resource
	content, err := client.ReadResource(ctx, resources[0].URI)
	if err != nil {
		t.Fatalf("ReadResource failed: %v", err)
	}
	if content == "" {
		t.Error("ReadResource should return non-empty content")
	}

	// Step 6: List prompts
	prompts, err := client.ListPrompts(ctx)
	if err != nil {
		t.Fatalf("ListPrompts failed: %v", err)
	}
	if len(prompts) < 1 {
		t.Fatalf("Expected at least 1 prompt, got %d", len(prompts))
	}

	// Step 7: Get a prompt
	messages, err := client.GetPrompt(ctx, prompts[0].Name, map[string]string{"name": "Test"})
	if err != nil {
		t.Fatalf("GetPrompt failed: %v", err)
	}
	if len(messages) < 1 {
		t.Fatalf("Expected at least 1 message, got %d", len(messages))
	}

	// Step 8: Close
	if err := client.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

// ============================================================================
// Section 18: 性能benchmark
// ============================================================================

// BenchmarkMCPClient benchmark MCP client工具列表获取性能
// 使用 mock server，跳过serverless环境
func BenchmarkMCPClient(b *testing.B) {
	// 创建 mock server用于benchmark
	tmpDir := b.TempDir()
	serverSrc := filepath.Join(tmpDir, "mock_mcp_server.go")

	src := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	runServer(os.Stdin, os.Stdout)
}

func runServer(stdin io.Reader, stdout io.Writer) {
	reader := bufio.NewReader(stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// newline-delimited：整行即一帧 JSON（MCP spec 2024-11-05 §Transports/stdio）
		buf := []byte(line)
		var req struct {
			ID     int64            ` + "`json:\"id\"`" + `
			Method string           ` + "`json:\"method\"`" + `
			Params json.RawMessage  ` + "`json:\"params\"`" + `
		}
		if err := json.Unmarshal(buf, &req); err != nil {
			continue
		}
		if req.ID > 0 {
			result := handleRequest(req.Method)
			resultJSON, _ := json.Marshal(result)
			resp := fmt.Sprintf(` + "`" + `{"jsonrpc":"2.0","id":%d,"result":%s}` + "`" + `, req.ID, string(resultJSON))
			stdout.Write(append([]byte(resp), '\n'))
		}
	}
}

func handleRequest(method string) interface{} {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]string{"name": "bench-server", "version": "1.0.0"},
		}
	case "ping":
		return map[string]interface{}{}
	case "tools/list":
		return map[string]interface{}{
			"tools": []map[string]interface{}{
				{"name": "echo", "description": "Echo", "inputSchema": map[string]interface{}{"type": "object"}},
				{"name": "add", "description": "Add", "inputSchema": map[string]interface{}{"type": "object"}},
			},
		}
	default:
		return map[string]interface{}{}
	}
}`

	if err := os.WriteFile(serverSrc, []byte(src), 0644); err != nil {
		b.Fatalf("写入 mock server源码失败: %v", err)
	}

	serverExe := filepath.Join(tmpDir, "mock_mcp_server.exe")
	buildCmd := exec.Command("go", "build", "-o", serverExe, serverSrc)
	if output, err := buildCmd.CombinedOutput(); err != nil {
		b.Fatalf("构建 mock server失败: %v\nOutput: %s", err, string(output))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := NewClient(ctx, "bench", ServerConfig{
		Command: serverExe,
	})
	if err != nil {
		b.Fatalf("创建 MCP client失败: %v", err)
	}
	defer client.Close()

	benchCtx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.ListTools(benchCtx)
		if err != nil {
			b.Fatalf("ListTools 失败: %v", err)
		}
	}
}

// ============================================================================
// Section: SSE 传输协议测试
// ============================================================================

// newMockSSEServer 创建一个mock SSE MCP server用于测试
func newMockSSEServer() *httptest.Server {
	mux := http.NewServeMux()

	// 处理 JSON-RPC POST 请求
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		result := handleMockSSERequest(req.Method, req.Params)
		resultJSON, _ := json.Marshal(result)

		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, string(resultJSON))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	})

	// 处理 SSE 事件流 GET 请求
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// 保持连接但不发送数据（测试用）
		<-r.Context().Done()
	})

	return httptest.NewServer(mux)
}

// handleMockSSERequest 处理mock SSE server的 JSON-RPC 请求
func handleMockSSERequest(method string, params json.RawMessage) interface{} {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools":     map[string]interface{}{},
				"resources": map[string]interface{}{},
				"prompts":   map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "mock-sse-server",
				"version": "1.0.0",
			},
		}
	case "ping":
		return map[string]interface{}{}
	case "tools/list":
		return map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "echo",
					"description": "Echo back the input",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"message": map[string]interface{}{"type": "string"},
						},
						"required": []string{"message"},
					},
				},
			},
		}
	case "tools/call":
		var input struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		json.Unmarshal(params, &input)
		if input.Name == "echo" {
			msg, _ := input.Arguments["message"].(string)
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": msg},
				},
			}
		}
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "unknown tool"},
			},
			"isError": true,
		}
	case "resources/list":
		return map[string]interface{}{
			"resources": []map[string]interface{}{
				{"uri": "file:///test/hello.txt", "name": "hello.txt", "mimeType": "text/plain"},
			},
		}
	case "resources/read":
		var input struct {
			URI string `json:"uri"`
		}
		json.Unmarshal(params, &input)
		return map[string]interface{}{
			"contents": []map[string]interface{}{
				{"uri": input.URI, "mimeType": "text/plain", "text": "Content of " + input.URI},
			},
		}
	case "prompts/list":
		return map[string]interface{}{
			"prompts": []map[string]interface{}{
				{
					"name":        "greet",
					"description": "Generate a greeting",
					"arguments": []map[string]interface{}{
						{"name": "name", "description": "Person name", "required": true},
					},
				},
			},
		}
	case "prompts/get":
		var input struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		json.Unmarshal(params, &input)
		personName := "World"
		if n, ok := input.Arguments["name"]; ok {
			personName = n
		}
		return map[string]interface{}{
			"description": "A greeting prompt",
			"messages": []map[string]interface{}{
				{
					"role": "user",
					"content": map[string]interface{}{
						"type": "text",
						"text": "Please greet " + personName + " warmly",
					},
				},
			},
		}
	default:
		return map[string]interface{}{}
	}
}

// TestNewSSEClient_URLValidation 验证 SSE client URL 格式校验
func TestNewSSEClient_URLValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "空 URL should return error",
			url:     "",
			wantErr: true,
		},
		{
			name:    "无效 URL should return error",
			url:     "not-a-valid-url",
			wantErr: true,
		},
		{
			name:    "缺少协议的 URL should return error",
			url:     "example.com/mcp",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSSEClient(ctx, "test", tt.url, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSSEClient() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestClient_TransportType 验证传输类型设置正确
func TestClient_TransportType(t *testing.T) {
	t.Run("stdio 传输类型", func(t *testing.T) {
		// 使用 mock server创建 stdio client
		client, cleanup := startMockServer(t)
		defer cleanup()

		if client.Transport() != "" {
			// stdio client transport 字段为空字符串（default value）
			t.Logf("stdio client transport = %q", client.Transport())
		}
	})

	t.Run("SSE 传输类型", func(t *testing.T) {
		server := newMockSSEServer()
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		client, err := NewSSEClient(ctx, "test-sse", server.URL, nil)
		if err != nil {
			t.Fatalf("NewSSEClient 失败: %v", err)
		}
		defer client.Close()

		if client.Transport() != "sse" {
			t.Errorf("Transport() = %q, expected %q", client.Transport(), "sse")
		}

		if client.ServerURL() != server.URL {
			t.Errorf("ServerURL() = %q, expected %q", client.ServerURL(), server.URL)
		}
	})
}

// TestClient_SendHTTP_ErrorHandling 验证 HTTP 请求错误处理
func TestClient_SendHTTP_ErrorHandling(t *testing.T) {
	t.Run("连接不存在的servershould return error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// 使用一个不存在的端口
		_, err := NewSSEClient(ctx, "test", "http://127.0.0.1:1/mcp", nil)
		if err == nil {
			t.Error("连接不存在的servershould return error")
		}
	})

	t.Run("server返回非 200 状态码", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal server error"))
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := NewSSEClient(ctx, "test", server.URL, nil)
		if err == nil {
			t.Error("server返回 500 should return error")
		}
		if !strings.Contains(err.Error(), "HTTP 500") {
			t.Errorf("错误should contain HTTP 500, actual: %v", err)
		}
	})

	t.Run("server返回 JSON-RPC 错误", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":1,"error":{"code":-32600,"message":"Invalid Request"}}`))
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := NewSSEClient(ctx, "test", server.URL, nil)
		if err == nil {
			t.Error("JSON-RPC 错误should return error")
		}
		if !strings.Contains(err.Error(), "JSON-RPC error") {
			t.Errorf("错误should contain JSON-RPC error, actual: %v", err)
		}
	})

	t.Run("上下文取消should return error", func(t *testing.T) {
		server := newMockSSEServer()
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // 立即取消

		_, err := NewSSEClient(ctx, "test", server.URL, nil)
		if err == nil {
			t.Error("取消的上下文should return error")
		}
	})
}

// TestSSEClient_FullWorkflow 测试 SSE client完整工作流
func TestSSEClient_FullWorkflow(t *testing.T) {
	server := newMockSSEServer()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := NewSSEClient(ctx, "test-sse", server.URL, nil)
	if err != nil {
		t.Fatalf("NewSSEClient 失败: %v", err)
	}
	defer client.Close()

	// 验证client就绪
	if !client.IsReady() {
		t.Error("SSE client应处于就绪状态")
	}

	// 验证名称
	if client.Name() != "test-sse" {
		t.Errorf("Name() = %q, expected %q", client.Name(), "test-sse")
	}

	// 验证能力
	if !client.HasTools() {
		t.Error("SSE server应支持工具能力")
	}
	if !client.HasResources() {
		t.Error("SSE server应支持资源能力")
	}
	if !client.HasPrompts() {
		t.Error("SSE server应支持提示能力")
	}

	// 列出工具
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("ListTools 返回 %d 个工具, expected 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("Tool[0].Name = %s, expected echo", tools[0].Name)
	}

	// 调用工具
	result, err := client.CallTool(ctx, "echo", map[string]interface{}{
		"message": "hello sse",
	})
	if err != nil {
		t.Fatalf("CallTool 失败: %v", err)
	}
	if result.Text() != "hello sse" {
		t.Errorf("CallTool result = %q, expected %q", result.Text(), "hello sse")
	}

	// 列出资源
	resources, err := client.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources 失败: %v", err)
	}
	if len(resources) != 1 {
		t.Errorf("ListResources 返回 %d 个资源, expected 1", len(resources))
	}

	// 读取资源
	content, err := client.ReadResource(ctx, resources[0].URI)
	if err != nil {
		t.Fatalf("ReadResource 失败: %v", err)
	}
	if content == "" {
		t.Error("ReadResource should return non-empty内容")
	}

	// 列出提示
	prompts, err := client.ListPrompts(ctx)
	if err != nil {
		t.Fatalf("ListPrompts 失败: %v", err)
	}
	if len(prompts) != 1 {
		t.Errorf("ListPrompts 返回 %d 个提示, expected 1", len(prompts))
	}

	// 获取提示
	messages, err := client.GetPrompt(ctx, "greet", map[string]string{"name": "SSE"})
	if err != nil {
		t.Fatalf("GetPrompt 失败: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("GetPrompt 返回 %d 条消息, expected 1", len(messages))
	}

	// Ping
	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping 失败: %v", err)
	}

	// 关闭
	if err := client.Close(); err != nil {
		t.Errorf("Close 失败: %v", err)
	}
}

// TestSSEClient_Close 测试 SSE client关闭
func TestSSEClient_Close(t *testing.T) {
	server := newMockSSEServer()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := NewSSEClient(ctx, "test-sse", server.URL, nil)
	if err != nil {
		t.Fatalf("NewSSEClient 失败: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close 失败: %v", err)
	}

	if client.IsReady() {
		t.Error("关闭后client不应处于就绪状态")
	}
}
