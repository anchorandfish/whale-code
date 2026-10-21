package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestNewClientFromConfig_Stdio 测试从配置创建 stdio client
func TestNewClientFromConfig_Stdio(t *testing.T) {
	// 使用已有的 startMockServer 逻辑创建可执行文件
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
			"serverInfo": map[string]string{"name": "mock-server", "version": "1.0.0"},
		}
	case "ping":
		return map[string]interface{}{}
	case "tools/list":
		return map[string]interface{}{
			"tools": []map[string]interface{}{
				{"name": "echo", "description": "Echo", "inputSchema": map[string]interface{}{"type": "object"}},
			},
		}
	default:
		return map[string]interface{}{}
	}
}`

	if err := os.WriteFile(serverSrc, []byte(src), 0644); err != nil {
		t.Fatalf("写入 mock server源码失败: %v", err)
	}

	serverExe := filepath.Join(tmpDir, "mock_mcp_server.exe")
	buildCmd := exec.Command("go", "build", "-o", serverExe, serverSrc)
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 mock server失败: %v\nOutput: %s", err, string(output))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("通过 Command 字段自动检测 stdio", func(t *testing.T) {
		config := ServerConfig{
			Command: serverExe,
		}

		client, err := NewClientFromConfig(ctx, "test-stdio", config)
		if err != nil {
			t.Fatalf("NewClientFromConfig 失败: %v", err)
		}
		defer client.Close()

		if !client.IsReady() {
			t.Error("client应处于就绪状态")
		}

		if client.Transport() != "stdio" {
			t.Errorf("Transport() = %q, expected %q", client.Transport(), "stdio")
		}
	})

	t.Run("显式指定 stdio 传输类型", func(t *testing.T) {
		config := ServerConfig{
			Command:   serverExe,
			Transport: "stdio",
		}

		client, err := NewClientFromConfig(ctx, "test-stdio-explicit", config)
		if err != nil {
			t.Fatalf("NewClientFromConfig 失败: %v", err)
		}
		defer client.Close()

		if client.Transport() != "stdio" {
			t.Errorf("Transport() = %q, expected %q", client.Transport(), "stdio")
		}
	})
}

// TestNewClientFromConfig_SSE 测试从配置创建 SSE client
func TestNewClientFromConfig_SSE(t *testing.T) {
	server := newMockSSEServer()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("通过 URL 字段自动检测 SSE", func(t *testing.T) {
		config := ServerConfig{
			URL: server.URL,
		}

		client, err := NewClientFromConfig(ctx, "test-sse", config)
		if err != nil {
			t.Fatalf("NewClientFromConfig 失败: %v", err)
		}
		defer client.Close()

		if client.Transport() != "sse" {
			t.Errorf("Transport() = %q, expected %q", client.Transport(), "sse")
		}
	})

	t.Run("显式指定 SSE 传输类型", func(t *testing.T) {
		config := ServerConfig{
			URL:       server.URL,
			Transport: "sse",
		}

		client, err := NewClientFromConfig(ctx, "test-sse-explicit", config)
		if err != nil {
			t.Fatalf("NewClientFromConfig 失败: %v", err)
		}
		defer client.Close()

		if client.Transport() != "sse" {
			t.Errorf("Transport() = %q, expected %q", client.Transport(), "sse")
		}
	})
}

// TestNewClientFromConfig_HTTP 测试从配置创建 HTTP client
func TestNewClientFromConfig_HTTP(t *testing.T) {
	server := newMockHTTPServer()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config := ServerConfig{
		URL:       server.URL,
		Transport: "http",
	}

	client, err := NewClientFromConfig(ctx, "test-http", config)
	if err != nil {
		t.Fatalf("NewClientFromConfig 失败: %v", err)
	}
	defer client.Close()

	if !client.IsReady() {
		t.Error("client应处于就绪状态")
	}

	if client.Transport() != "http" {
		t.Errorf("Transport() = %q, expected %q", client.Transport(), "http")
	}
}

// TestNewClientFromConfig_WebSocket 测试从配置创建 WebSocket client
func TestNewClientFromConfig_WebSocket(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config := ServerConfig{
		URL:       wsURL(server.URL),
		Transport: "ws",
	}

	client, err := NewClientFromConfig(ctx, "test-ws", config)
	if err != nil {
		t.Fatalf("NewClientFromConfig 失败: %v", err)
	}
	defer client.Close()

	if !client.IsReady() {
		t.Error("client应处于就绪状态")
	}

	if client.Transport() != "ws" {
		t.Errorf("Transport() = %q, expected %q", client.Transport(), "ws")
	}
}

// TestNewClientFromConfig_InvalidConfig 测试无效配置
func TestNewClientFromConfig_InvalidConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("既无 Command 也无 URL", func(t *testing.T) {
		config := ServerConfig{}
		_, err := NewClientFromConfig(ctx, "test", config)
		if err == nil {
			t.Error("无效配置should return error")
		}
	})

	t.Run("无效的传输类型", func(t *testing.T) {
		config := ServerConfig{
			URL:       "http://localhost:8080",
			Transport: "invalid",
		}
		_, err := NewClientFromConfig(ctx, "test", config)
		if err == nil {
			t.Error("无效传输类型should return error")
		}
	})
}

// TestNewClientFromConfig_BackwardCompatibility 测试向后compatibility
func TestNewClientFromConfig_BackwardCompatibility(t *testing.T) {
	t.Run("ServerConfig 新字段不影响旧配置解析", func(t *testing.T) {
		configData := `{
			"servers": {
				"filesystem": {
					"command": "npx",
					"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
				}
			}
		}`

		var cfg Config
		if err := json.Unmarshal([]byte(configData), &cfg); err != nil {
			t.Fatalf("parse config失败: %v", err)
		}

		fs, ok := cfg.Servers["filesystem"]
		if !ok {
			t.Fatal("应找到 filesystem server")
		}

		if fs.Command != "npx" {
			t.Errorf("Command = %s, expected npx", fs.Command)
		}
		if fs.Transport != "" {
			t.Errorf("Transport should be empty，actual为 %s", fs.Transport)
		}
	})

	t.Run("新字段可正确解析", func(t *testing.T) {
		configData := `{
			"servers": {
				"remote": {
					"url": "http://localhost:8080/mcp",
					"transport": "http",
					"headers": {
						"Authorization": "Bearer test-token"
					},
					"timeout": 30
				}
			}
		}`

		var cfg Config
		if err := json.Unmarshal([]byte(configData), &cfg); err != nil {
			t.Fatalf("parse config失败: %v", err)
		}

		remote, ok := cfg.Servers["remote"]
		if !ok {
			t.Fatal("应找到 remote server")
		}

		if remote.URL != "http://localhost:8080/mcp" {
			t.Errorf("URL = %s, expected http://localhost:8080/mcp", remote.URL)
		}
		if remote.Transport != "http" {
			t.Errorf("Transport = %s, expected http", remote.Transport)
		}
		if remote.Headers["Authorization"] != "Bearer test-token" {
			t.Errorf("Headers[Authorization] = %s, expected Bearer test-token", remote.Headers["Authorization"])
		}
		if remote.Timeout != 30 {
			t.Errorf("Timeout = %d, expected 30", remote.Timeout)
		}
	})

	t.Run("OAuth 配置可正确解析", func(t *testing.T) {
		configData := `{
			"servers": {
				"oauth-server": {
					"url": "http://localhost:8080/mcp",
					"transport": "http",
					"oauth": {
						"authorization_url": "https://auth.example.com/authorize",
						"token_url": "https://auth.example.com/token",
						"client_id": "test-client",
						"scopes": ["mcp", "profile"]
					}
				}
			}
		}`

		var cfg Config
		if err := json.Unmarshal([]byte(configData), &cfg); err != nil {
			t.Fatalf("parse config失败: %v", err)
		}

		oauth, ok := cfg.Servers["oauth-server"]
		if !ok {
			t.Fatal("应找到 oauth-server server")
		}

		if oauth.OAuth == nil {
			t.Fatal("OAuth 不should be nil")
		}
		if oauth.OAuth.AuthorizationURL != "https://auth.example.com/authorize" {
			t.Errorf("AuthorizationURL = %s, expected https://auth.example.com/authorize", oauth.OAuth.AuthorizationURL)
		}
		if oauth.OAuth.ClientID != "test-client" {
			t.Errorf("ClientID = %s, expected test-client", oauth.OAuth.ClientID)
		}
		if len(oauth.OAuth.Scopes) != 2 {
			t.Errorf("Scopes 数量 = %d, expected 2", len(oauth.OAuth.Scopes))
		}
	})
}

// TestNewClientFromConfig_WithHeaders 测试带自定义request header的配置
func TestNewClientFromConfig_WithHeaders(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config := ServerConfig{
		URL:       server.URL,
		Transport: "http",
		Headers: map[string]string{
			"Authorization": "Bearer my-token",
		},
	}

	client, err := NewClientFromConfig(ctx, "test-headers", config)
	if err != nil {
		t.Fatalf("NewClientFromConfig 失败: %v", err)
	}
	defer client.Close()

	if receivedAuth != "Bearer my-token" {
		t.Errorf("Authorization = %s, expected Bearer my-token", receivedAuth)
	}
}

// TestNewClientFromConfig_Timeout test timeout配置
func TestNewClientFromConfig_Timeout(t *testing.T) {
	config := ServerConfig{
		URL:       "http://localhost:8080/mcp",
		Transport: "http",
		Timeout:   30,
	}

	if config.Timeout != 30 {
		t.Errorf("Timeout = %d, expected 30", config.Timeout)
	}
}
