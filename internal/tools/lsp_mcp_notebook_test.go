package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/lsp"
	"github.com/anchorandfish/whale-code/internal/mcp"
)

// --- Mock implementation ---

// mockLSPClient mock LSP client，用于测试
type mockLSPClient struct {
	ready           bool
	definitions     []lsp.Location
	defErr          error
	references      []lsp.Location
	refErr          error
	diagnostics     []lsp.Diagnostic
	diagErr         error
	hoverResult     string
	hoverErr        error
	completionItems []lsp.CompletionItem
	completionErr   error
	renameEdit      *lsp.WorkspaceEdit
	renameErr       error
	implementations []lsp.Location
	implErr         error
	typeDefinitions []lsp.Location
	typeDefErr      error
	codeActions     []lsp.CodeAction
	codeActionErr   error
	documentSymbols []lsp.DocumentSymbol
	docSymbolErr    error
	// B4-3A 新增：DidOpen/DidChange/DidClose 调用记录，用于断言
	didOpenCalls   []didOpenCall
	didChangeCalls []didChangeCall
	didCloseCalls  []didCloseCall
	didOpenErr     error
	didChangeErr   error
	didCloseErr    error
}

// didOpenCall 记录 DidOpen 调用参数，供 file.go 接线测试断言
type didOpenCall struct {
	FilePath   string
	LanguageID string
	Content    string
}

// didChangeCall 记录 DidChange 调用参数，供 file.go 接线测试断言
type didChangeCall struct {
	FilePath   string
	NewContent string
	Version    int
}

// didCloseCall 记录 DidClose 调用参数，供后续接线测试断言
type didCloseCall struct {
	FilePath string
}

// IsReady 返回client是否就绪
func (m *mockLSPClient) IsReady() bool {
	return m.ready
}

// IsCrashed 返回 client 是否已崩溃（mock 默认 false，测试可覆盖）
// T1-P1-A2新增：满足 LSPClient 接口扩展
func (m *mockLSPClient) IsCrashed() bool {
	return false
}

// Restart 触发子进程重建（mock 默认 no-op，测试可覆盖）
// T1-P1-A2新增：满足 LSPClient 接口扩展
func (m *mockLSPClient) Restart(ctx context.Context) error {
	return nil
}

// Definition mock Definition 调用
func (m *mockLSPClient) Definition(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error) {
	return m.definitions, m.defErr
}

// References mock References 调用
func (m *mockLSPClient) References(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error) {
	return m.references, m.refErr
}

// Diagnostics mock Diagnostics 调用
func (m *mockLSPClient) Diagnostics(ctx context.Context, filePath string) ([]lsp.Diagnostic, error) {
	return m.diagnostics, m.diagErr
}

// Hover mock Hover 调用
func (m *mockLSPClient) Hover(ctx context.Context, filePath string, line, character int) (string, error) {
	return m.hoverResult, m.hoverErr
}

// Completion mock Completion 调用
func (m *mockLSPClient) Completion(ctx context.Context, filePath string, line, character int) ([]lsp.CompletionItem, error) {
	return m.completionItems, m.completionErr
}

// Rename mock Rename 调用
func (m *mockLSPClient) Rename(ctx context.Context, filePath string, line, character int, newName string) (*lsp.WorkspaceEdit, error) {
	return m.renameEdit, m.renameErr
}

// Implementation mock Implementation 调用
func (m *mockLSPClient) Implementation(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error) {
	return m.implementations, m.implErr
}

// TypeDefinition mock TypeDefinition 调用
func (m *mockLSPClient) TypeDefinition(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error) {
	return m.typeDefinitions, m.typeDefErr
}

// CodeActions mock CodeActions 调用
func (m *mockLSPClient) CodeActions(ctx context.Context, filePath string, line, character int, actionKind string) ([]lsp.CodeAction, error) {
	return m.codeActions, m.codeActionErr
}

// DocumentSymbols mock DocumentSymbols 调用
func (m *mockLSPClient) DocumentSymbols(ctx context.Context, filePath string) ([]lsp.DocumentSymbol, error) {
	return m.documentSymbols, m.docSymbolErr
}

// DidOpen mock DidOpen 调用，记录参数供测试断言
func (m *mockLSPClient) DidOpen(ctx context.Context, filePath, languageID, content string) error {
	m.didOpenCalls = append(m.didOpenCalls, didOpenCall{filePath, languageID, content})
	return m.didOpenErr
}

// DidChange mock DidChange 调用，记录参数供测试断言
func (m *mockLSPClient) DidChange(ctx context.Context, filePath string, version int, newContent string) error {
	m.didChangeCalls = append(m.didChangeCalls, didChangeCall{filePath, newContent, version})
	return m.didChangeErr
}

// DidClose mock DidClose 调用，记录参数供测试断言
func (m *mockLSPClient) DidClose(ctx context.Context, filePath string) error {
	m.didCloseCalls = append(m.didCloseCalls, didCloseCall{filePath})
	return m.didCloseErr
}

// mockMCPClient mock MCP client，用于测试
type mockMCPClient struct {
	tools        []mcp.ToolInfo
	toolsErr     error
	callResult   *mcp.ToolResult
	callErr      error
	resources    []mcp.Resource
	resErr       error
	readResult   string
	readErr      error
	prompts      []mcp.PromptInfo
	promptsErr   error
	messages     []mcp.PromptMessage
	getPromptErr error
	hasTools     bool
}

// ListTools mock ListTools 调用
func (m *mockMCPClient) ListTools(ctx context.Context) ([]mcp.ToolInfo, error) {
	return m.tools, m.toolsErr
}

// CallTool mock CallTool 调用
func (m *mockMCPClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	return m.callResult, m.callErr
}

// ListResources mock ListResources 调用
func (m *mockMCPClient) ListResources(ctx context.Context) ([]mcp.Resource, error) {
	return m.resources, m.resErr
}

// ReadResource mock ReadResource 调用
func (m *mockMCPClient) ReadResource(ctx context.Context, uri string) (string, error) {
	return m.readResult, m.readErr
}

// ListPrompts mock ListPrompts 调用
func (m *mockMCPClient) ListPrompts(ctx context.Context) ([]mcp.PromptInfo, error) {
	return m.prompts, m.promptsErr
}

// GetPrompt mock GetPrompt 调用
func (m *mockMCPClient) GetPrompt(ctx context.Context, name string, arguments map[string]string) ([]mcp.PromptMessage, error) {
	return m.messages, m.getPromptErr
}

// HasTools mock HasTools 调用
func (m *mockMCPClient) HasTools() bool {
	return m.hasTools
}

// Close mock Close 调用
func (m *mockMCPClient) Close() error {
	return nil
}

// --- LSP Tests ---

// TestLSP_DetectLanguage 测试从文件扩展名检测语言
func TestLSP_DetectLanguage(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"main.go", "go"},
		{"script.py", "python"},
		{"app.ts", "typescript"},
		{"component.tsx", "typescript"},
		{"index.js", "typescript"},
		{"main.rs", "rust"},
		{"utils.c", "c"},
		{"header.h", "c"},
		{"impl.cpp", "cpp"},
		{"header.hpp", "cpp"},
		{"impl.cc", "cpp"},
		{"impl.cxx", "cpp"},
		{"unknown.xyz", ""},
		{"Makefile", ""},
		{"README", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := lsp.DetectLanguage(tt.filename)
			if result != tt.expected {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tt.filename, result, tt.expected)
			}
		})
	}
}

// TestLSP_NotAvailable 测试 LSP 在语言server未安装时返回友好错误
func TestLSP_NotAvailable(t *testing.T) {
	// 创建一个使用不存在语言的 handler
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	// 注册 LSP handler
	e.handlers["LspDefinition"] = e.lspDefinitionHandler()
	e.handlers["LspReferences"] = e.lspReferencesHandler()
	e.handlers["LspDiagnostics"] = e.lspDiagnosticsHandler()

	// 测试无法检测语言的情况
	t.Run("unknown_language", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file":      "test.xyz",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspDefinition"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "LSP not available") {
			t.Fatalf("expected 'LSP not available' message, got: %s", output)
		}
	})

	// 测试空文件路径
	t.Run("empty_file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file":      "",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspDefinition"]
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for empty file")
		}
		if !strings.Contains(err.Error(), "file is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// 测试已知语言但server未安装的情况
	t.Run("known_language_no_server", func(t *testing.T) {
		// 使用 .go 文件，但 gopls 可能未安装
		input, _ := json.Marshal(map[string]interface{}{
			"file":      "main.go",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspDefinition"]
		output, err := handler(context.Background(), input)
		// 不管 gopls 是否安装，都不应该返回 hard error
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 如果 gopls 未安装，should return友好提示
		if strings.Contains(output, "LSP not available") {
			t.Log("gopls not installed, got friendly error message as expected")
		}
	})
}

// TestLSP_ClientCache 测试 LSP client按语言缓存
func TestLSP_ClientCache(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}

	// 清空缓存
	e.lspClientMu.Lock()
	for k := range e.lspClientCache {
		delete(e.lspClientCache, k)
	}
	e.lspClientMu.Unlock()

	// test cache逻辑：对于无法创建client的语言，不应缓存
	t.Run("no_cache_for_failed_client", func(t *testing.T) {
		ctx := context.Background()
		_, err := e.getLSPClient(ctx, "test.unknown_ext")
		if err == nil {
			t.Fatal("expected error for unknown language")
		}

		// 缓存应该为空
		e.lspClientMu.Lock()
		cacheLen := len(e.lspClientCache)
		e.lspClientMu.Unlock()

		if cacheLen != 0 {
			t.Fatalf("expected empty cache for failed client creation, got %d entries", cacheLen)
		}
	})

	// 测试空语言检测
	t.Run("empty_language_detection", func(t *testing.T) {
		ctx := context.Background()
		_, err := e.getLSPClient(ctx, "file.xyz")
		if err == nil {
			t.Fatal("expected error for unknown file extension")
		}
		if !strings.Contains(err.Error(), "cannot detect language") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// --- MCP Tests ---

// TestMCP_ListTools 测试列出 MCP 工具
func TestMCP_ListTools(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["McpListTools"] = e.mcpListToolsHandler()

	// 测试空server名
	t.Run("empty_server", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server": "",
		})
		handler := e.handlers["McpListTools"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "MCP not available") {
			t.Fatalf("expected 'MCP not available' message, got: %s", output)
		}
	})

	// 测试不存在的server
	t.Run("nonexistent_server", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server": "nonexistent_server",
		})
		handler := e.handlers["McpListTools"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "MCP not available") {
			t.Fatalf("expected 'MCP not available' message, got: %s", output)
		}
	})

	// 测试无效 JSON 输入
	t.Run("invalid_input", func(t *testing.T) {
		handler := e.handlers["McpListTools"]
		_, err := handler(context.Background(), json.RawMessage(`{invalid`))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

// TestMCP_CallTool 测试调用 MCP 工具
func TestMCP_CallTool(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["McpCallTool"] = e.mcpCallToolHandler()

	// 测试缺少必要参数
	t.Run("missing_required_params", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server":    "",
			"tool_name": "",
		})
		handler := e.handlers["McpCallTool"]
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for missing params")
		}
		if !strings.Contains(err.Error(), "server and tool_name are required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// 测试不存在的server
	t.Run("nonexistent_server", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server":    "nonexistent",
			"tool_name": "some_tool",
		})
		handler := e.handlers["McpCallTool"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "MCP not available") {
			t.Fatalf("expected 'MCP not available' message, got: %s", output)
		}
	})

	// 测试 nil arguments 被初始化为空 map
	t.Run("nil_arguments_initialized", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server":    "test",
			"tool_name": "tool",
		})
		handler := e.handlers["McpCallTool"]
		output, _ := handler(context.Background(), input)
		// server不存在，但不应 panic
		if !strings.Contains(output, "MCP not available") {
			t.Fatalf("expected 'MCP not available' message, got: %s", output)
		}
	})
}

// TestMCP_ClientCache 测试 MCP client缓存
func TestMCP_ClientCache(t *testing.T) {
	// 清空缓存
	mcpClientMu.Lock()
	for k := range mcpClientCache {
		delete(mcpClientCache, k)
	}
	mcpClientMu.Unlock()

	e := &Executor{
		handlers: make(map[string]Handler),
	}

	// 测试空server名
	t.Run("empty_server_name", func(t *testing.T) {
		ctx := context.Background()
		_, err := e.getMCPClient(ctx, "")
		if err == nil {
			t.Fatal("expected error for empty server name")
		}
		if !strings.Contains(err.Error(), "server name is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// 测试不存在的server配置
	t.Run("nonexistent_server_config", func(t *testing.T) {
		ctx := context.Background()
		_, err := e.getMCPClient(ctx, "nonexistent_mcp_server")
		if err == nil {
			t.Fatal("expected error for nonexistent server")
		}
		if !strings.Contains(err.Error(), "not found in config") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadMCPConfigConcurrentWorkDirsStayIsolated(t *testing.T) {
	writeConfig := func(workDir, server string) {
		t.Helper()
		configDir := filepath.Join(workDir, ".whale")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		data := []byte(fmt.Sprintf(`{"servers":{"%s":{"command":"test"}}}`, server))
		if err := os.WriteFile(filepath.Join(configDir, "mcp.json"), data, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	dirA := t.TempDir()
	dirB := t.TempDir()
	writeConfig(dirA, "server_a")
	writeConfig(dirB, "server_b")

	errCh := make(chan error, 2)
	check := func(workDir, want, unwanted string) {
		for range 100 {
			cfg := loadMCPConfig(workDir)
			if _, ok := cfg.Servers[want]; !ok {
				errCh <- fmt.Errorf("%s config missing %s", workDir, want)
				return
			}
			if _, ok := cfg.Servers[unwanted]; ok {
				errCh <- fmt.Errorf("%s config leaked %s", workDir, unwanted)
				return
			}
		}
		errCh <- nil
	}

	go check(dirA, "server_a", "server_b")
	go check(dirB, "server_b", "server_a")
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

// TestMCP_ListResources 测试 MCP 列出资源
func TestMCP_ListResources(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["McpListResources"] = e.mcpListResourcesHandler()

	t.Run("empty_server", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server": "",
		})
		handler := e.handlers["McpListResources"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "MCP not available") {
			t.Fatalf("expected 'MCP not available' message, got: %s", output)
		}
	})
}

// TestMCP_ReadResource 测试 MCP 读取资源
func TestMCP_ReadResource(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["McpReadResource"] = e.mcpReadResourceHandler()

	// 测试缺少必要参数
	t.Run("missing_params", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server": "",
			"uri":    "",
		})
		handler := e.handlers["McpReadResource"]
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for missing params")
		}
		if !strings.Contains(err.Error(), "server and uri are required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// --- Notebook Tests ---

// createTestNotebook 创建一个测试用的 notebook 文件
func createTestNotebook(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	nbPath := filepath.Join(tmpDir, "test.ipynb")

	nb := &NotebookFile{
		Cells: []NotebookCell{
			{
				CellType: "code",
				Source:   []string{"print('hello')\n"},
				Metadata: map[string]interface{}{},
				Outputs:  []interface{}{},
			},
			{
				CellType: "markdown",
				Source:   []string{"# Title\n"},
				Metadata: map[string]interface{}{},
			},
		},
		Metadata:      map[string]interface{}{},
		Nbformat:      4,
		NbformatMinor: 5,
	}

	data, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal notebook: %v", err)
	}
	if err := os.WriteFile(nbPath, data, 0644); err != nil {
		t.Fatalf("failed to write notebook: %v", err)
	}

	return nbPath
}

// TestNotebookEdit_Replace 测试替换 notebook 中的 cell
func TestNotebookEdit_Replace(t *testing.T) {
	nbPath := createTestNotebook(t)

	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()

	// 测试正常替换
	t.Run("replace_cell", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 0,
			"new_source": "print('replaced')",
			"edit_kind":  "replace",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}

		// 验证内容已更新
		nb, loadErr := loadNotebook(nbPath)
		if loadErr != nil {
			t.Fatalf("failed to load notebook: %v", loadErr)
		}
		if len(nb.Cells) != 2 {
			t.Fatalf("expected 2 cells, got %d", len(nb.Cells))
		}
		replacedSource := strings.Join(nb.Cells[0].Source, "")
		if !strings.Contains(replacedSource, "replaced") {
			t.Fatalf("expected replaced content, got: %s", replacedSource)
		}
	})

	// 测试替换时修改 cell 类型
	t.Run("replace_with_type_change", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 0,
			"new_source": "# Markdown cell",
			"cell_type":  "markdown",
			"edit_kind":  "replace",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}

		nb, _ := loadNotebook(nbPath)
		if nb.Cells[0].CellType != "markdown" {
			t.Fatalf("expected cell_type=markdown, got: %s", nb.Cells[0].CellType)
		}
	})

	// 测试越界索引
	t.Run("out_of_range_index", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 99,
			"new_source": "content",
			"edit_kind":  "replace",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "out of range") {
			t.Fatalf("expected out of range message, got: %s", output)
		}
	})

	// 测试负索引
	t.Run("negative_index", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": -1,
			"new_source": "content",
			"edit_kind":  "replace",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "out of range") {
			t.Fatalf("expected out of range message, got: %s", output)
		}
	})
}

// TestNotebookEdit_Insert 测试插入 cell
func TestNotebookEdit_Insert(t *testing.T) {
	nbPath := createTestNotebook(t)

	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()

	// 测试在开头插入
	t.Run("insert_at_beginning", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 0,
			"new_source": "import os",
			"cell_type":  "code",
			"edit_kind":  "insert",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}

		nb, _ := loadNotebook(nbPath)
		if len(nb.Cells) != 3 {
			t.Fatalf("expected 3 cells, got %d", len(nb.Cells))
		}
		firstSource := strings.Join(nb.Cells[0].Source, "")
		if !strings.Contains(firstSource, "import os") {
			t.Fatalf("expected 'import os' in first cell, got: %s", firstSource)
		}
	})

	// 测试在末尾插入（负索引）
	t.Run("insert_at_end_negative_index", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": -1,
			"new_source": "# End cell",
			"cell_type":  "markdown",
			"edit_kind":  "insert",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}
	})

	// 测试默认 cell_type 为 code
	t.Run("insert_default_code_type", func(t *testing.T) {
		// 重新创建 notebook
		nbPath2 := createTestNotebook(t)
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath2,
			"cell_index": 0,
			"new_source": "x = 1",
			"edit_kind":  "insert",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}

		nb, _ := loadNotebook(nbPath2)
		if nb.Cells[0].CellType != "code" {
			t.Fatalf("expected default cell_type=code, got: %s", nb.Cells[0].CellType)
		}
	})

	// 测试超出scope的索引（追加到末尾）
	t.Run("insert_beyond_end", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 100,
			"new_source": "# Appended",
			"cell_type":  "markdown",
			"edit_kind":  "insert",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}
	})
}

// TestNotebookEdit_Delete 测试删除 cell
func TestNotebookEdit_Delete(t *testing.T) {
	nbPath := createTestNotebook(t)

	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()

	// 测试正常删除
	t.Run("delete_cell", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 0,
			"edit_kind":  "delete",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Notebook updated") {
			t.Fatalf("expected update message, got: %s", output)
		}

		nb, _ := loadNotebook(nbPath)
		if len(nb.Cells) != 1 {
			t.Fatalf("expected 1 cell after deletion, got %d", len(nb.Cells))
		}
		// 剩余的应该是 markdown cell
		if nb.Cells[0].CellType != "markdown" {
			t.Fatalf("expected markdown cell, got: %s", nb.Cells[0].CellType)
		}
	})

	// 测试删除越界索引
	t.Run("delete_out_of_range", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": 99,
			"edit_kind":  "delete",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "out of range") {
			t.Fatalf("expected out of range message, got: %s", output)
		}
	})

	// 测试删除负索引
	t.Run("delete_negative_index", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"path":       nbPath,
			"cell_index": -1,
			"edit_kind":  "delete",
		})
		handler := e.handlers["NotebookEdit"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "out of range") {
			t.Fatalf("expected out of range message, got: %s", output)
		}
	})
}

// TestNotebook_LoadSave 测试加载和保存 notebook 文件
func TestNotebook_LoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	nbPath := filepath.Join(tmpDir, "loadsave.ipynb")

	// 测试保存 notebook
	t.Run("save_notebook", func(t *testing.T) {
		nb := &NotebookFile{
			Cells: []NotebookCell{
				{
					CellType: "code",
					Source:   []string{"x = 1\n", "print(x)\n"},
					Metadata: map[string]interface{}{},
					Outputs:  []interface{}{},
				},
			},
			Metadata:      map[string]interface{}{"kernelspec": "python3"},
			Nbformat:      4,
			NbformatMinor: 5,
		}

		if err := saveNotebook(nbPath, nb); err != nil {
			t.Fatalf("failed to save notebook: %v", err)
		}

		// 验证文件存在
		if _, err := os.Stat(nbPath); os.IsNotExist(err) {
			t.Fatal("notebook file was not created")
		}
	})

	// 测试加载 notebook
	t.Run("load_notebook", func(t *testing.T) {
		nb, err := loadNotebook(nbPath)
		if err != nil {
			t.Fatalf("failed to load notebook: %v", err)
		}

		if len(nb.Cells) != 1 {
			t.Fatalf("expected 1 cell, got %d", len(nb.Cells))
		}
		if nb.Cells[0].CellType != "code" {
			t.Fatalf("expected code cell, got: %s", nb.Cells[0].CellType)
		}
		source := strings.Join(nb.Cells[0].Source, "")
		if !strings.Contains(source, "x = 1") {
			t.Fatalf("unexpected cell content: %s", source)
		}
		if nb.Nbformat != 4 {
			t.Fatalf("expected nbformat=4, got: %d", nb.Nbformat)
		}
	})

	// 测试加载不存在的文件
	t.Run("load_nonexistent", func(t *testing.T) {
		_, err := loadNotebook(filepath.Join(tmpDir, "nonexistent.ipynb"))
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
	})

	// 测试加载无效 JSON
	t.Run("load_invalid_json", func(t *testing.T) {
		invalidPath := filepath.Join(tmpDir, "invalid.ipynb")
		if err := os.WriteFile(invalidPath, []byte("not json"), 0644); err != nil {
			t.Fatalf("failed to write invalid file: %v", err)
		}
		_, err := loadNotebook(invalidPath)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
		if !strings.Contains(err.Error(), "invalid notebook format") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// 测试加载缺少 nbformat 的 notebook（应设置default value）
	t.Run("load_missing_nbformat", func(t *testing.T) {
		minimalPath := filepath.Join(tmpDir, "minimal.ipynb")
		minimalContent := `{"cells":[]}`
		if err := os.WriteFile(minimalPath, []byte(minimalContent), 0644); err != nil {
			t.Fatalf("failed to write minimal file: %v", err)
		}
		nb, err := loadNotebook(minimalPath)
		if err != nil {
			t.Fatalf("failed to load minimal notebook: %v", err)
		}
		if nb.Nbformat != 4 {
			t.Fatalf("expected default nbformat=4, got: %d", nb.Nbformat)
		}
		if nb.NbformatMinor != 5 {
			t.Fatalf("expected default nbformat_minor=5, got: %d", nb.NbformatMinor)
		}
		if nb.Metadata == nil {
			t.Fatal("expected metadata to be initialized")
		}
	})
}

// TestNotebookEdit_InvalidEditKind 测试无效的编辑类型
func TestNotebookEdit_InvalidEditKind(t *testing.T) {
	nbPath := createTestNotebook(t)

	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"path":       nbPath,
		"cell_index": 0,
		"edit_kind":  "invalid",
	})
	handler := e.handlers["NotebookEdit"]
	output, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "Unknown edit_kind") {
		t.Fatalf("expected unknown edit_kind message, got: %s", output)
	}
}

// TestNotebookEdit_MissingPath 测试缺少路径参数
func TestNotebookEdit_MissingPath(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()

	input, _ := json.Marshal(map[string]interface{}{
		"cell_index": 0,
		"edit_kind":  "replace",
	})
	handler := e.handlers["NotebookEdit"]
	_, err := handler(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSplitLines 测试 splitLines helper functions
func TestSplitLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty_string",
			input:    "",
			expected: []string{},
		},
		{
			name:     "single_line",
			input:    "hello",
			expected: []string{"hello"},
		},
		{
			name:     "multiple_lines",
			input:    "line1\nline2\nline3",
			expected: []string{"line1\n", "line2\n", "line3"},
		},
		{
			name:     "trailing_newline",
			input:    "line1\nline2\n",
			expected: []string{"line1\n", "line2\n", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitLines(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("splitLines(%q) returned %d lines, expected %d", tt.input, len(result), len(tt.expected))
			}
			for i, line := range result {
				if line != tt.expected[i] {
					t.Errorf("splitLines(%q)[%d] = %q, want %q", tt.input, i, line, tt.expected[i])
				}
			}
		})
	}
}

// TestNotebookEdit_InvalidInput 测试无效的 JSON 输入
func TestNotebookEdit_InvalidInput(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["NotebookEdit"] = e.notebookEditHandler()

	handler := e.handlers["NotebookEdit"]
	_, err := handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
}

// TestLSP_Definition 测试 LSP Definition 工具处理器
func TestLSP_Definition(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["LspDefinition"] = e.lspDefinitionHandler()

	// 当 LSP client可用且返回位置时，格式化为 "file:line:character"
	t.Run("available_with_locations", func(t *testing.T) {
		// 清空缓存并注入 mock client
		e.lspClientMu.Lock()
		e.lspClientCache = make(map[string]LSPClient)
		e.lspClientCache["go"] = &mockLSPClient{
			ready: true,
			definitions: []lsp.Location{
				{
					URI: "file:///path/to/definition.go",
					Range: lsp.Range{
						Start: lsp.Position{Line: 9, Character: 4},
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
			"file":      "main.go",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspDefinition"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 验证输出格式为 "file:line:character"（line 和 character 从 0 开始加 1）
		// URIToFilePath 在 Windows 上会将 / 转为 \（FromSlash），期望值需平台感知
		expected := filepath.FromSlash("/path/to/definition.go") + ":10:5"
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got: %s", expected, output)
		}
	})

	// 当 Definition 返回空列表时，输出 "No definition found"
	t.Run("empty_locations", func(t *testing.T) {
		e.lspClientMu.Lock()
		e.lspClientCache = make(map[string]LSPClient)
		e.lspClientCache["go"] = &mockLSPClient{
			ready:       true,
			definitions: []lsp.Location{},
		}
		e.lspClientMu.Unlock()
		defer func() {
			e.lspClientMu.Lock()
			delete(e.lspClientCache, "go")
			e.lspClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"file":      "main.go",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspDefinition"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "No definition found" {
			t.Fatalf("expected 'No definition found', got: %s", output)
		}
	})

	// 当 Definition 调用失败时，输出包含错误信息
	t.Run("definition_error", func(t *testing.T) {
		e.lspClientMu.Lock()
		e.lspClientCache = make(map[string]LSPClient)
		e.lspClientCache["go"] = &mockLSPClient{
			ready:  true,
			defErr: fmt.Errorf("connection lost"),
		}
		e.lspClientMu.Unlock()
		defer func() {
			e.lspClientMu.Lock()
			delete(e.lspClientCache, "go")
			e.lspClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"file":      "main.go",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspDefinition"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Definition lookup failed") {
			t.Fatalf("expected 'Definition lookup failed' message, got: %s", output)
		}
		if !strings.Contains(output, "connection lost") {
			t.Fatalf("expected error detail 'connection lost', got: %s", output)
		}
	})
}

// TestLSP_References 测试 LSP References 工具处理器
func TestLSP_References(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["LspReferences"] = e.lspReferencesHandler()

	// 空文件路径返回 "file is required" 错误
	t.Run("empty_file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file":      "",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspReferences"]
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for empty file")
		}
		if !strings.Contains(err.Error(), "file is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// LSP 不可用时返回友好提示
	t.Run("lsp_not_available", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file":      "test.xyz",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspReferences"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "LSP not available") {
			t.Fatalf("expected 'LSP not available' message, got: %s", output)
		}
	})

	// 当 LSP client可用且返回引用时，格式化为 "file:line:character"
	t.Run("available_with_references", func(t *testing.T) {
		e.lspClientMu.Lock()
		e.lspClientCache = make(map[string]LSPClient)
		e.lspClientCache["go"] = &mockLSPClient{
			ready: true,
			references: []lsp.Location{
				{
					URI: "file:///path/to/ref1.go",
					Range: lsp.Range{
						Start: lsp.Position{Line: 19, Character: 9},
					},
				},
				{
					URI: "file:///path/to/ref2.go",
					Range: lsp.Range{
						Start: lsp.Position{Line: 29, Character: 14},
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
			"file":      "main.go",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspReferences"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// URIToFilePath 在 Windows 上会将 / 转为 \（FromSlash），期望值需平台感知
		ref1 := filepath.FromSlash("/path/to/ref1.go") + ":20:10"
		ref2 := filepath.FromSlash("/path/to/ref2.go") + ":30:15"
		if !strings.Contains(output, ref1) {
			t.Fatalf("expected first reference %q, got: %s", ref1, output)
		}
		if !strings.Contains(output, ref2) {
			t.Fatalf("expected second reference %q, got: %s", ref2, output)
		}
	})

	// 当 References 返回空列表时，输出 "No references found"
	t.Run("empty_references", func(t *testing.T) {
		e.lspClientMu.Lock()
		e.lspClientCache = make(map[string]LSPClient)
		e.lspClientCache["go"] = &mockLSPClient{
			ready:      true,
			references: []lsp.Location{},
		}
		e.lspClientMu.Unlock()
		defer func() {
			e.lspClientMu.Lock()
			delete(e.lspClientCache, "go")
			e.lspClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"file":      "main.go",
			"line":      1,
			"character": 1,
		})
		handler := e.handlers["LspReferences"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "No references found" {
			t.Fatalf("expected 'No references found', got: %s", output)
		}
	})
}

// TestLSP_Diagnostics 测试 LSP Diagnostics 工具处理器
func TestLSP_Diagnostics(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["LspDiagnostics"] = e.lspDiagnosticsHandler()

	// 空文件路径返回 "file is required" 错误
	t.Run("empty_file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file": "",
		})
		handler := e.handlers["LspDiagnostics"]
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for empty file")
		}
		if !strings.Contains(err.Error(), "file is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// LSP 不可用时返回友好提示
	t.Run("lsp_not_available", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file": "test.xyz",
		})
		handler := e.handlers["LspDiagnostics"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "LSP not available") {
			t.Fatalf("expected 'LSP not available' message, got: %s", output)
		}
	})

	// 当 LSP client可用时，返回成功消息
	t.Run("available_success", func(t *testing.T) {
		e.lspClientMu.Lock()
		e.lspClientCache = make(map[string]LSPClient)
		e.lspClientCache["go"] = &mockLSPClient{
			ready:       true,
			diagnostics: []lsp.Diagnostic{},
		}
		e.lspClientMu.Unlock()
		defer func() {
			e.lspClientMu.Lock()
			delete(e.lspClientCache, "go")
			e.lspClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"file": "main.go",
		})
		handler := e.handlers["LspDiagnostics"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "Diagnostics requested") {
			t.Fatalf("expected 'Diagnostics requested' message, got: %s", output)
		}
	})
}

// TestMCP_ListPrompts 测试 MCP ListPrompts 工具处理器
func TestMCP_ListPrompts(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["McpListPrompts"] = e.mcpListPromptsHandler()

	// 空server名返回 "MCP not available" 消息
	t.Run("empty_server", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server": "",
		})
		handler := e.handlers["McpListPrompts"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "MCP not available") {
			t.Fatalf("expected 'MCP not available' message, got: %s", output)
		}
	})

	// 当 MCP client可用时，返回格式化的 prompt 列表
	t.Run("available_with_prompts", func(t *testing.T) {
		mcpClientMu.Lock()
		mcpClientCache = make(map[string]MCPClient)
		mcpClientCache["test_server"] = &mockMCPClient{
			prompts: []mcp.PromptInfo{
				{
					Name:        "greet",
					Description: "Generate a greeting",
					Arguments: []mcp.PromptArgument{
						{Name: "name", Description: "Person name", Required: true},
					},
				},
				{
					Name:        "summarize",
					Description: "Summarize text",
				},
			},
		}
		// 注入 mock 时需同步设置创建时间，否则 getMCPClient 会因 TTL 元数据缺失而淘汰该缓存项
		mcpClientCreatedAt["test_server"] = time.Now()
		mcpClientMu.Unlock()
		defer func() {
			mcpClientMu.Lock()
			delete(mcpClientCache, "test_server")
			delete(mcpClientCreatedAt, "test_server")
			mcpClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"server": "test_server",
		})
		handler := e.handlers["McpListPrompts"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 验证包含 prompt 名称和描述
		if !strings.Contains(output, "greet") {
			t.Fatalf("expected output to contain 'greet', got: %s", output)
		}
		if !strings.Contains(output, "Generate a greeting") {
			t.Fatalf("expected output to contain description, got: %s", output)
		}
		if !strings.Contains(output, "name") {
			t.Fatalf("expected output to contain argument 'name', got: %s", output)
		}
		if !strings.Contains(output, "summarize") {
			t.Fatalf("expected output to contain 'summarize', got: %s", output)
		}
	})

	// 当没有可用的 prompts 时，返回 "No prompts available"
	t.Run("no_prompts_available", func(t *testing.T) {
		mcpClientMu.Lock()
		mcpClientCache = make(map[string]MCPClient)
		mcpClientCache["empty_server"] = &mockMCPClient{
			prompts: []mcp.PromptInfo{},
		}
		// 注入 mock 时需同步设置创建时间，否则 getMCPClient 会因 TTL 元数据缺失而淘汰该缓存项
		mcpClientCreatedAt["empty_server"] = time.Now()
		mcpClientMu.Unlock()
		defer func() {
			mcpClientMu.Lock()
			delete(mcpClientCache, "empty_server")
			delete(mcpClientCreatedAt, "empty_server")
			mcpClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"server": "empty_server",
		})
		handler := e.handlers["McpListPrompts"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "No prompts available" {
			t.Fatalf("expected 'No prompts available', got: %s", output)
		}
	})
}

// TestMCP_GetPrompt 测试 MCP GetPrompt 工具处理器
func TestMCP_GetPrompt(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
	}
	e.handlers["McpGetPrompt"] = e.mcpGetPromptHandler()

	// 缺少 server 或 prompt_name 返回错误
	t.Run("missing_params", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"server":      "",
			"prompt_name": "",
		})
		handler := e.handlers["McpGetPrompt"]
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for missing params")
		}
		if !strings.Contains(err.Error(), "server and prompt_name are required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// 当 MCP client可用时，返回格式化的消息
	t.Run("available_with_messages", func(t *testing.T) {
		mcpClientMu.Lock()
		mcpClientCache = make(map[string]MCPClient)
		mcpClientCache["test_server"] = &mockMCPClient{
			messages: []mcp.PromptMessage{
				{
					Role: "user",
					Content: mcp.PromptMessageContent{
						Type: "text",
						Text: "Please greet Alice warmly",
					},
				},
				{
					Role: "assistant",
					Content: mcp.PromptMessageContent{
						Type: "text",
						Text: "Hello, Alice!",
					},
				},
			},
		}
		// 注入 mock 时需同步设置创建时间，否则 getMCPClient 会因 TTL 元数据缺失而淘汰该缓存项
		mcpClientCreatedAt["test_server"] = time.Now()
		mcpClientMu.Unlock()
		defer func() {
			mcpClientMu.Lock()
			delete(mcpClientCache, "test_server")
			delete(mcpClientCreatedAt, "test_server")
			mcpClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"server":      "test_server",
			"prompt_name": "greet",
			"arguments": map[string]string{
				"name": "Alice",
			},
		})
		handler := e.handlers["McpGetPrompt"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "[user]") {
			t.Fatalf("expected output to contain '[user]', got: %s", output)
		}
		if !strings.Contains(output, "Please greet Alice warmly") {
			t.Fatalf("expected output to contain message text, got: %s", output)
		}
		if !strings.Contains(output, "[assistant]") {
			t.Fatalf("expected output to contain '[assistant]', got: %s", output)
		}
		if !strings.Contains(output, "Hello, Alice!") {
			t.Fatalf("expected output to contain assistant message, got: %s", output)
		}
	})

	// 当没有消息时，返回 "No messages in prompt"
	t.Run("no_messages", func(t *testing.T) {
		mcpClientMu.Lock()
		mcpClientCache = make(map[string]MCPClient)
		mcpClientCache["test_server"] = &mockMCPClient{
			messages: []mcp.PromptMessage{},
		}
		// 注入 mock 时需同步设置创建时间，否则 getMCPClient 会因 TTL 元数据缺失而淘汰该缓存项
		mcpClientCreatedAt["test_server"] = time.Now()
		mcpClientMu.Unlock()
		defer func() {
			mcpClientMu.Lock()
			delete(mcpClientCache, "test_server")
			delete(mcpClientCreatedAt, "test_server")
			mcpClientMu.Unlock()
		}()

		input, _ := json.Marshal(map[string]interface{}{
			"server":      "test_server",
			"prompt_name": "empty_prompt",
		})
		handler := e.handlers["McpGetPrompt"]
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "No messages in prompt" {
			t.Fatalf("expected 'No messages in prompt', got: %s", output)
		}
	})
}

// TestNotebook_Format4 测试 nbformat 4 compatibility
func TestNotebook_Format4(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建一个 nbformat 4 的 notebook 文件
	nbPath := filepath.Join(tmpDir, "nbformat4.ipynb")
	execCount := 3
	nb := &NotebookFile{
		Cells: []NotebookCell{
			{
				CellType:       "code",
				Source:         []string{"print('hello')\n"},
				Metadata:       map[string]interface{}{},
				Outputs:        []interface{}{map[string]interface{}{"output_type": "stream", "text": "hello\n"}},
				ExecutionCount: &execCount,
			},
			{
				CellType: "markdown",
				Source:   []string{"# Title\n"},
				Metadata: map[string]interface{}{},
			},
		},
		Metadata:      map[string]interface{}{"kernelspec": map[string]interface{}{"name": "python3", "display_name": "Python 3"}},
		Nbformat:      4,
		NbformatMinor: 5,
	}

	data, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal notebook: %v", err)
	}
	if err := os.WriteFile(nbPath, data, 0644); err != nil {
		t.Fatalf("failed to write notebook: %v", err)
	}

	// 加载 nbformat 4 notebook 文件并验证 Nbformat=4
	t.Run("load_nbformat4", func(t *testing.T) {
		loaded, err := loadNotebook(nbPath)
		if err != nil {
			t.Fatalf("failed to load notebook: %v", err)
		}
		if loaded.Nbformat != 4 {
			t.Fatalf("expected nbformat=4, got: %d", loaded.Nbformat)
		}
		if loaded.NbformatMinor != 5 {
			t.Fatalf("expected nbformat_minor=5, got: %d", loaded.NbformatMinor)
		}
	})

	// 保存并重新加载，验证 nbformat 字段保留
	t.Run("save_reload_preserves_nbformat", func(t *testing.T) {
		loaded, err := loadNotebook(nbPath)
		if err != nil {
			t.Fatalf("failed to load notebook: %v", err)
		}

		// 保存到新路径
		reloadPath := filepath.Join(tmpDir, "reloaded.ipynb")
		if err := saveNotebook(reloadPath, loaded); err != nil {
			t.Fatalf("failed to save notebook: %v", err)
		}

		// 重新加载
		reloaded, err := loadNotebook(reloadPath)
		if err != nil {
			t.Fatalf("failed to reload notebook: %v", err)
		}
		if reloaded.Nbformat != 4 {
			t.Fatalf("expected nbformat=4 after reload, got: %d", reloaded.Nbformat)
		}
		if reloaded.NbformatMinor != 5 {
			t.Fatalf("expected nbformat_minor=5 after reload, got: %d", reloaded.NbformatMinor)
		}
	})

	// 验证 cell execution_count 和 outputs 保留
	t.Run("preserves_execution_count_and_outputs", func(t *testing.T) {
		loaded, err := loadNotebook(nbPath)
		if err != nil {
			t.Fatalf("failed to load notebook: %v", err)
		}

		// 验证第一个 cell（code cell）的 execution_count
		if len(loaded.Cells) < 1 {
			t.Fatal("expected at least 1 cell")
		}
		codeCell := loaded.Cells[0]
		if codeCell.ExecutionCount == nil {
			t.Fatal("expected execution_count to be preserved, got nil")
		}
		if *codeCell.ExecutionCount != 3 {
			t.Fatalf("expected execution_count=3, got: %d", *codeCell.ExecutionCount)
		}

		// 验证 outputs 保留
		if len(codeCell.Outputs) != 1 {
			t.Fatalf("expected 1 output, got: %d", len(codeCell.Outputs))
		}
		outputMap, ok := codeCell.Outputs[0].(map[string]interface{})
		if !ok {
			t.Fatalf("expected output to be a map, got: %T", codeCell.Outputs[0])
		}
		if outputMap["output_type"] != "stream" {
			t.Fatalf("expected output_type=stream, got: %v", outputMap["output_type"])
		}

		// 验证 markdown cell 没有 execution_count 和 outputs
		markdownCell := loaded.Cells[1]
		if markdownCell.ExecutionCount != nil {
			t.Fatalf("expected markdown cell execution_count to be nil, got: %d", *markdownCell.ExecutionCount)
		}
	})
}

// ===== B4-3A 新增 mock 测试 =====

// TestMockLSPClient_ImplementsInterface 编译期断言：mockLSPClient 实现 LSPClient interface
// 新增 DidOpen/DidChange/DidClose 后，若 mock 未补齐方法，编译会失败
func TestMockLSPClient_ImplementsInterface(t *testing.T) {
	var _ LSPClient = (*mockLSPClient)(nil)
	// 若编译通过，说明 mockLSPClient 已实现所有 LSPClient 方法（含 3 个新方法）
}

// TestMockLSPClient_RecordsDidOpenChangeClose 验证 mock 记录调用参数，供 file.go 测试断言
func TestMockLSPClient_RecordsDidOpenChangeClose(t *testing.T) {
	m := &mockLSPClient{ready: true}

	ctx := context.Background()
	_ = m.DidOpen(ctx, "/tmp/a.go", "go", "package main")
	_ = m.DidChange(ctx, "/tmp/a.go", 2, "package main\n")
	_ = m.DidClose(ctx, "/tmp/a.go")

	if len(m.didOpenCalls) != 1 {
		t.Errorf("DidOpen 调用次数 = %d, want 1", len(m.didOpenCalls))
	} else {
		if m.didOpenCalls[0].FilePath != "/tmp/a.go" {
			t.Errorf("DidOpen filePath = %q, want /tmp/a.go", m.didOpenCalls[0].FilePath)
		}
		if m.didOpenCalls[0].LanguageID != "go" {
			t.Errorf("DidOpen languageID = %q, want go", m.didOpenCalls[0].LanguageID)
		}
	}

	if len(m.didChangeCalls) != 1 {
		t.Errorf("DidChange 调用次数 = %d, want 1", len(m.didChangeCalls))
	} else {
		if m.didChangeCalls[0].Version != 2 {
			t.Errorf("DidChange version = %d, want 2", m.didChangeCalls[0].Version)
		}
	}

	if len(m.didCloseCalls) != 1 {
		t.Errorf("DidClose 调用次数 = %d, want 1", len(m.didCloseCalls))
	}
}

// TestMockLSPClient_DidChangeError 验证 mock 可配置返回错误，供 file.go 错误路径测试
func TestMockLSPClient_DidChangeError(t *testing.T) {
	m := &mockLSPClient{
		ready:        true,
		didChangeErr: fmt.Errorf("simulated didChange failure"),
	}

	err := m.DidChange(context.Background(), "/tmp/a.go", 2, "content")
	if err == nil {
		t.Fatal("expected didChangeErr, got nil")
	}
	if !strings.Contains(err.Error(), "simulated") {
		t.Errorf("error should contain 'simulated', got: %v", err)
	}
}
