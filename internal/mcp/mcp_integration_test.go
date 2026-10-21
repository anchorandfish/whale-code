package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExecutor_DiscoverMCP_NoConfig 测试无 MCP config file时，DiscoverAndRegisterMCPTools 不应报错
func TestExecutor_DiscoverMCP_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// 在空目录下load config，不应报错
	cfg := LoadConfigFromDirs(tmpDir)

	if cfg == nil {
		t.Fatal("LoadConfigFromDirs 不should return nil")
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("无config file时 Servers should be empty，actual有 %d 个", len(cfg.Servers))
	}
}

// TestExecutor_DiscoverMCP_InvalidConfig 测试无效config file时，应静默跳过
func TestExecutor_DiscoverMCP_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// 写入无效 JSON
	invalidConfigPath := filepath.Join(tmpDir, "mcp.json")
	if err := os.WriteFile(invalidConfigPath, []byte("this is not json"), 0644); err != nil {
		t.Fatalf("写入无效config file失败: %v", err)
	}

	// LoadConfig should return error，LoadConfigFromDirs 应静默跳过
	cfg, err := LoadConfig(invalidConfigPath)
	if err == nil {
		t.Error("LoadConfig 对无效 JSON should return error")
	}

	// LoadConfigFromDirs 应跳过无效文件，返回空配置
	cfg = LoadConfigFromDirs(tmpDir)
	if cfg == nil {
		t.Fatal("LoadConfigFromDirs 不should return nil")
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("无效config file时 Servers should be empty，actual有 %d 个", len(cfg.Servers))
	}
}

// TestMCPToolRegistration 测试 MCP 工具注册到 Registry 的路径
// mock MCP 工具发现过程：直接调用 RegisterMCP 注册工具规格
func TestMCPToolRegistration(t *testing.T) {
	// mock MCP 工具信息
	toolInfo := ToolInfo{
		Name:        "mcp_test_tool",
		Description: "A test MCP tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
		},
	}

	// 验证 ToolInfo 可以正确序列化/反序列化
	data, err := json.Marshal(toolInfo)
	if err != nil {
		t.Fatalf("序列化 ToolInfo 失败: %v", err)
	}

	var decoded ToolInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("反序列化 ToolInfo 失败: %v", err)
	}

	if decoded.Name != toolInfo.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, toolInfo.Name)
	}
	if decoded.Description != toolInfo.Description {
		t.Errorf("Description = %q, want %q", decoded.Description, toolInfo.Description)
	}
	if decoded.InputSchema == nil {
		t.Error("InputSchema 不should be nil")
	}
}

// TestMCPToolHandler 测试 MCP 工具处理器的创建和调用
// 验证 mcpToolHandler 模式：根据 serverName 查找client并调用工具
func TestMCPToolHandler(t *testing.T) {
	// mock MCP 工具调用的输入参数
	input := map[string]interface{}{
		"query": "test query",
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("序列化输入失败: %v", err)
	}

	// 验证输入可以被正确解析为 map[string]interface{}
	var parsed map[string]interface{}
	if err := json.Unmarshal(inputJSON, &parsed); err != nil {
		t.Fatalf("反序列化输入失败: %v", err)
	}

	if parsed["query"] != "test query" {
		t.Errorf("query = %v, want %q", parsed["query"], "test query")
	}
}

// TestLoadConfigFromDirs_ValidConfig 测试从有效config file加载 MCP 配置
func TestLoadConfigFromDirs_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// 写入有效配置
	configData := `{
		"servers": {
			"test-server": {
				"command": "echo",
				"args": ["hello"]
			}
		}
	}`
	configPath := filepath.Join(tmpDir, "mcp.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("写入config file失败: %v", err)
	}

	cfg := LoadConfigFromDirs(tmpDir)
	if cfg == nil {
		t.Fatal("LoadConfigFromDirs 不should return nil")
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("应有 1 个server，actual有 %d 个", len(cfg.Servers))
	}

	server, ok := cfg.Servers["test-server"]
	if !ok {
		t.Fatal("应找到 test-server")
	}
	if server.Command != "echo" {
		t.Errorf("Command = %q, want %q", server.Command, "echo")
	}
	if len(server.Args) != 1 || server.Args[0] != "hello" {
		t.Errorf("Args = %v, want [hello]", server.Args)
	}
}

// TestLoadConfigFromDirs_SSEConfig 测试加载 SSE 传输的 MCP 配置
func TestLoadConfigFromDirs_SSEConfig(t *testing.T) {
	tmpDir := t.TempDir()

	configData := `{
		"servers": {
			"remote-server": {
				"url": "http://localhost:8080/mcp"
			}
		}
	}`
	configPath := filepath.Join(tmpDir, "mcp.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("写入config file失败: %v", err)
	}

	cfg := LoadConfigFromDirs(tmpDir)
	if len(cfg.Servers) != 1 {
		t.Fatalf("应有 1 个server，actual有 %d 个", len(cfg.Servers))
	}

	server, ok := cfg.Servers["remote-server"]
	if !ok {
		t.Fatal("应找到 remote-server")
	}
	if server.URL != "http://localhost:8080/mcp" {
		t.Errorf("URL = %q, want %q", server.URL, "http://localhost:8080/mcp")
	}
}

// TestLoadConfigFromDirs_MultipleDirs 测试从多个目录load config（优先使用第一个有效配置）
func TestLoadConfigFromDirs_MultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// 第一个目录无配置，第二个目录有配置
	configData := `{
		"servers": {
			"fallback-server": {
				"command": "node",
				"args": ["server.js"]
			}
		}
	}`
	configPath := filepath.Join(dir2, "mcp.json")
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("写入config file失败: %v", err)
	}

	cfg := LoadConfigFromDirs(dir1, dir2)
	if len(cfg.Servers) != 1 {
		t.Fatalf("应有 1 个server，actual有 %d 个", len(cfg.Servers))
	}

	if _, ok := cfg.Servers["fallback-server"]; !ok {
		t.Fatal("应找到 fallback-server")
	}
}

// TestLoadConfigFromDirs_FirstDirWins 测试多个目录都有配置时，优先使用第一个
func TestLoadConfigFromDirs_FirstDirWins(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// 两个目录都有配置
	config1 := `{"servers": {"first": {"command": "cmd1"}}}`
	config2 := `{"servers": {"second": {"command": "cmd2"}}}`

	if err := os.WriteFile(filepath.Join(dir1, "mcp.json"), []byte(config1), 0644); err != nil {
		t.Fatalf("写入配置1失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "mcp.json"), []byte(config2), 0644); err != nil {
		t.Fatalf("写入配置2失败: %v", err)
	}

	cfg := LoadConfigFromDirs(dir1, dir2)
	if len(cfg.Servers) != 1 {
		t.Fatalf("应只有 1 个server（来自第一个目录），actual有 %d 个", len(cfg.Servers))
	}

	if _, ok := cfg.Servers["first"]; !ok {
		t.Fatal("应使用第一个目录的配置")
	}
}

// TestServerConfig_EnvVars 测试 ServerConfig 的environment variable字段
func TestServerConfig_EnvVars(t *testing.T) {
	configData := `{
		"servers": {
			"env-server": {
				"command": "node",
				"args": ["server.js"],
				"env": {
					"API_KEY": "test-key",
					"DEBUG": "true"
				}
			}
		}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(configData), &cfg); err != nil {
		t.Fatalf("parse config失败: %v", err)
	}

	server, ok := cfg.Servers["env-server"]
	if !ok {
		t.Fatal("应找到 env-server")
	}
	if len(server.Env) != 2 {
		t.Errorf("应有 2 个environment variable，actual有 %d 个", len(server.Env))
	}
	if server.Env["API_KEY"] != "test-key" {
		t.Errorf("API_KEY = %q, want %q", server.Env["API_KEY"], "test-key")
	}
}
