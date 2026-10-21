// Package tools — LSP, MCP, NotebookEdit tool handlers
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/lsp"
	"github.com/anchorandfish/whale-code/internal/mcp"
	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

const lspOperationTimeout = 30 * time.Second

func (e *Executor) withLSPOperationTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	_, hasDeadline := ctx.Deadline()
	if hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, lspOperationTimeout)
}

// lspDefinitionHandler LSP definition handler
func (e *Executor) lspDefinitionHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.File == "" {
			return "", fmt.Errorf("file is required")
		}

		client, err := e.getLSPClient(ctx, input.File)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		locations, err := client.Definition(lspCtx, input.File, input.Line, input.Character)
		if err != nil {
			return fmt.Sprintf("Definition lookup failed: %v", err), nil
		}

		if len(locations) == 0 {
			return "No definition found", nil
		}

		var results []string
		for _, loc := range locations {
			filePath, err := platform.URIToFilePath(loc.URI)
			if err != nil {
				filePath = loc.URI
			}
			results = append(results, fmt.Sprintf("%s:%d:%d", filePath, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspReferencesHandler LSP references handler
func (e *Executor) lspReferencesHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.File == "" {
			return "", fmt.Errorf("file is required")
		}

		client, err := e.getLSPClient(ctx, input.File)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		locations, err := client.References(lspCtx, input.File, input.Line, input.Character)
		if err != nil {
			return fmt.Sprintf("References lookup failed: %v", err), nil
		}

		if len(locations) == 0 {
			return "No references found", nil
		}

		var results []string
		for _, loc := range locations {
			filePath, err := platform.URIToFilePath(loc.URI)
			if err != nil {
				filePath = loc.URI
			}
			results = append(results, fmt.Sprintf("%s:%d:%d", filePath, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspDiagnosticsHandler LSP diagnostics handler
func (e *Executor) lspDiagnosticsHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			File string `json:"file"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.File == "" {
			return "", fmt.Errorf("file is required")
		}

		client, err := e.getLSPClient(ctx, input.File)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		_, err = client.Diagnostics(lspCtx, input.File)
		if err != nil {
			return fmt.Sprintf("Diagnostics failed: %v", err), nil
		}

		return "Diagnostics requested (results arrive asynchronously)", nil
	}
}

// lspHoverHandler LSP hover handler
func (e *Executor) lspHoverHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.File == "" {
			return "", fmt.Errorf("file is required")
		}

		client, err := e.getLSPClient(ctx, input.File)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		result, err := client.Hover(lspCtx, input.File, input.Line, input.Character)
		if err != nil {
			return fmt.Sprintf("Hover failed: %v", err), nil
		}

		if result == "" {
			return "No hover information available", nil
		}

		return result, nil
	}
}

// lspCompletionHandler LSP completion handler
func (e *Executor) lspCompletionHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.File == "" {
			return "", fmt.Errorf("file is required")
		}

		client, err := e.getLSPClient(ctx, input.File)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		items, err := client.Completion(lspCtx, input.File, input.Line, input.Character)
		if err != nil {
			return fmt.Sprintf("Completion failed: %v", err), nil
		}

		if len(items) == 0 {
			return "No completion items available", nil
		}

		var results []string
		for _, item := range items {
			if item.Detail != "" {
				results = append(results, fmt.Sprintf("%s — %s", item.Label, item.Detail))
			} else {
				results = append(results, item.Label)
			}
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspRenameHandler LSP rename handler
func (e *Executor) lspRenameHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
			NewName   string `json:"new_name"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.File == "" {
			return "", fmt.Errorf("file is required")
		}
		if input.NewName == "" {
			return "", fmt.Errorf("new_name is required")
		}

		client, err := e.getLSPClient(ctx, input.File)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		edit, err := client.Rename(lspCtx, input.File, input.Line, input.Character, input.NewName)
		if err != nil {
			return fmt.Sprintf("Rename failed: %v", err), nil
		}

		if edit == nil || len(edit.Changes) == 0 {
			return "No rename changes available", nil
		}

		var results []string
		for uri, edits := range edit.Changes {
			filePath, err := platform.URIToFilePath(uri)
			if err != nil {
				filePath = uri
			}
			for _, te := range edits {
				results = append(results, fmt.Sprintf("%s:%d:%d — replace with %q", filePath, te.Range.Start.Line+1, te.Range.Start.Character+1, te.NewText))
			}
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspImplementationHandler LSP implementation handler
func (e *Executor) lspImplementationHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			FilePath  string `json:"file_path"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.FilePath == "" {
			return "", fmt.Errorf("file_path is required")
		}

		client, err := e.getLSPClient(ctx, input.FilePath)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		locations, err := client.Implementation(lspCtx, input.FilePath, input.Line, input.Character)
		if err != nil {
			return fmt.Sprintf("Implementation lookup failed: %v", err), nil
		}

		if len(locations) == 0 {
			return "No implementations found", nil
		}

		var results []string
		for _, loc := range locations {
			filePath, err := platform.URIToFilePath(loc.URI)
			if err != nil {
				filePath = loc.URI
			}
			results = append(results, fmt.Sprintf("%s:%d:%d", filePath, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspTypeDefinitionHandler LSP type definition handler
func (e *Executor) lspTypeDefinitionHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			FilePath  string `json:"file_path"`
			Line      int    `json:"line"`
			Character int    `json:"character"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.FilePath == "" {
			return "", fmt.Errorf("file_path is required")
		}

		client, err := e.getLSPClient(ctx, input.FilePath)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		locations, err := client.TypeDefinition(lspCtx, input.FilePath, input.Line, input.Character)
		if err != nil {
			return fmt.Sprintf("Type definition lookup failed: %v", err), nil
		}

		if len(locations) == 0 {
			return "No type definition found", nil
		}

		var results []string
		for _, loc := range locations {
			filePath, err := platform.URIToFilePath(loc.URI)
			if err != nil {
				filePath = loc.URI
			}
			results = append(results, fmt.Sprintf("%s:%d:%d", filePath, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspCodeActionsHandler LSP code actions handler
func (e *Executor) lspCodeActionsHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			FilePath   string `json:"file_path"`
			Line       int    `json:"line"`
			Character  int    `json:"character"`
			ActionKind string `json:"action_kind"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.FilePath == "" {
			return "", fmt.Errorf("file_path is required")
		}

		client, err := e.getLSPClient(ctx, input.FilePath)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		actions, err := client.CodeActions(lspCtx, input.FilePath, input.Line, input.Character, input.ActionKind)
		if err != nil {
			return fmt.Sprintf("Code actions lookup failed: %v", err), nil
		}

		if len(actions) == 0 {
			return "No code actions available", nil
		}

		var results []string
		for _, action := range actions {
			detail := action.Title
			if action.Kind != "" {
				detail = fmt.Sprintf("[%s] %s", action.Kind, action.Title)
			}
			results = append(results, detail)
		}
		return strings.Join(results, "\n"), nil
	}
}

// lspDocumentSymbolsHandler LSP document symbols handler
func (e *Executor) lspDocumentSymbolsHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			FilePath string `json:"file_path"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.FilePath == "" {
			return "", fmt.Errorf("file_path is required")
		}

		client, err := e.getLSPClient(ctx, input.FilePath)
		if err != nil {
			return fmt.Sprintf("LSP not available: %v\nTip: Install a language server (e.g., 'go install gopls@latest' for Go)", err), nil
		}

		lspCtx, lspCancel := e.withLSPOperationTimeout(ctx)
		defer lspCancel()
		symbols, err := client.DocumentSymbols(lspCtx, input.FilePath)
		if err != nil {
			return fmt.Sprintf("Document symbols lookup failed: %v", err), nil
		}

		if len(symbols) == 0 {
			return "No document symbols found", nil
		}

		var results []string
		formatSymbols(symbols, 0, &results)
		return strings.Join(results, "\n"), nil
	}
}

// formatSymbols recursively formats the document symbol tree
func formatSymbols(symbols []lsp.DocumentSymbol, depth int, results *[]string) {
	indent := strings.Repeat("  ", depth)
	for _, sym := range symbols {
		kindName := symbolKindName(sym.Kind)
		line := sym.Range.Start.Line + 1
		*results = append(*results, fmt.Sprintf("%s%s (%s) :%d", indent, sym.Name, kindName, line))
		if len(sym.Children) > 0 {
			formatSymbols(sym.Children, depth+1, results)
		}
	}
}

// symbolKindName converts an LSP SymbolKind value to a name
func symbolKindName(kind int) string {
	names := map[int]string{
		1: "File", 2: "Module", 3: "Namespace", 4: "Package",
		5: "Class", 6: "Method", 7: "Property", 8: "Field",
		9: "Constructor", 10: "Enum", 11: "Interface", 12: "Function",
		13: "Variable", 14: "Constant", 15: "String", 16: "Number",
		17: "Boolean", 18: "Array", 19: "Object", 20: "Key",
		21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event",
		25: "Operator", 26: "TypeParameter",
	}
	if name, ok := names[kind]; ok {
		return name
	}
	return fmt.Sprintf("Kind%d", kind)
}

// LSPClient is the LSP client interface for decoupling and testing
type LSPClient interface {
	Definition(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error)
	References(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error)
	Diagnostics(ctx context.Context, filePath string) ([]lsp.Diagnostic, error)
	Hover(ctx context.Context, filePath string, line, character int) (string, error)
	Completion(ctx context.Context, filePath string, line, character int) ([]lsp.CompletionItem, error)
	Rename(ctx context.Context, filePath string, line, character int, newName string) (*lsp.WorkspaceEdit, error)
	Implementation(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error)
	TypeDefinition(ctx context.Context, filePath string, line, character int) ([]lsp.Location, error)
	CodeActions(ctx context.Context, filePath string, line, character int, actionKind string) ([]lsp.CodeAction, error)
	DocumentSymbols(ctx context.Context, filePath string) ([]lsp.DocumentSymbol, error)
	// DidOpen 通知服务器打开文档（version=1），同步内容用于诊断
	DidOpen(ctx context.Context, filePath, languageID, content string) error
	// DidChange 通知服务器文档内容变更，version 须单调递增（全量替换）
	DidChange(ctx context.Context, filePath string, version int, newContent string) error
	// DidClose 通知服务器关闭文档，释放服务端资源
	DidClose(ctx context.Context, filePath string) error
	IsReady() bool
	// IsCrashed 返回 client 是否已崩溃（无法恢复，超 maxRestarts）
	// 调用方检测到 IsCrashed()==true 时应从缓存移除并返回错误
	// T1-P1-A2新增
	IsCrashed() bool
	// Restart 触发子进程重建，按 maxRestarts + 指数退避
	// 调用方在 IsReady()==false 且 IsCrashed()==false 时调用
	// T1-P1-A2新增
	Restart(ctx context.Context) error
}

// MCPClient is the MCP client interface for decoupling and testing
type MCPClient interface {
	ListTools(ctx context.Context) ([]mcp.ToolInfo, error)
	CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*mcp.ToolResult, error)
	ListResources(ctx context.Context) ([]mcp.Resource, error)
	ReadResource(ctx context.Context, uri string) (string, error)
	ListPrompts(ctx context.Context) ([]mcp.PromptInfo, error)
	GetPrompt(ctx context.Context, name string, arguments map[string]string) ([]mcp.PromptMessage, error)
	HasTools() bool
	Close() error
}

// lspHandlerFactories 是 10 个 LSP 工具「名字 → handler 工厂」的**唯一**映射。
//
// 执行门（executor.go registerBuiltinHandlers）用它按 lspToolNames 逐名判 toolAvailable 后注册；
// 它与 lspToolNames 一一对应（名单漂移守卫在 mcp_lsp_availability_test.go：新增工具忘进本表
// 或忘进名单都会红）。放在本文件是因为 handler 定义都在这里。
func lspHandlerFactories(e *Executor) map[string]func() Handler {
	return map[string]func() Handler{
		"LspDefinition":      e.lspDefinitionHandler,
		"LspReferences":      e.lspReferencesHandler,
		"LspDiagnostics":     e.lspDiagnosticsHandler,
		"LspHover":           e.lspHoverHandler,
		"LspCompletion":      e.lspCompletionHandler,
		"LspRename":          e.lspRenameHandler,
		"LspImplementation":  e.lspImplementationHandler,
		"LspTypeDefinition":  e.lspTypeDefinitionHandler,
		"LspCodeActions":     e.lspCodeActionsHandler,
		"LspDocumentSymbols": e.lspDocumentSymbolsHandler,
	}
}

// mcpHandlerFactories 是 6 个 MCP 元工具「名字 → handler 工厂」的**唯一**映射（理由同上）。
func mcpHandlerFactories(e *Executor) map[string]func() Handler {
	return map[string]func() Handler{
		"McpListResources": e.mcpListResourcesHandler,
		"McpReadResource":  e.mcpReadResourceHandler,
		"McpListTools":     e.mcpListToolsHandler,
		"McpCallTool":      e.mcpCallToolHandler,
		"McpListPrompts":   e.mcpListPromptsHandler,
		"McpGetPrompt":     e.mcpGetPromptHandler,
	}
}

// getLSPClient get or create LSP client for a file
// T1-P1-A2修改：检测 crashed → 自动 Restart；超 maxRestarts → 重建 client
func (e *Executor) getLSPClient(ctx context.Context, filePath string) (LSPClient, error) {
	lang := lsp.DetectLanguage(filePath)
	if lang == "" {
		return nil, fmt.Errorf("cannot detect language for %s", filePath)
	}

	e.lspClientMu.Lock()
	defer e.lspClientMu.Unlock()

	if client, ok := e.lspClientCache[lang]; ok {
		// 检测是否已永久崩溃（超 maxRestarts）
		if client.IsCrashed() {
			// 永久崩溃，从缓存移除并重建（用新 client）
			delete(e.lspClientCache, lang)
		} else if !client.IsReady() {
			// 暂时崩溃（EOF 但未超 maxRestarts），尝试 Restart 自动恢复
			restartCtx, restartCancel := context.WithTimeout(ctx, 30*time.Second)
			restartErr := client.Restart(restartCtx)
			restartCancel()
			if restartErr != nil {
				// Restart 失败，从缓存移除并重建
				delete(e.lspClientCache, lang)
			} else {
				// Restart 成功，复用 client
				return client, nil
			}
		} else {
			// client 可用，直接返回
			return client, nil
		}
	}

	workDir := filepath.Dir(filePath)
	if absDir, err := filepath.Abs(workDir); err == nil {
		workDir = absDir
	}

	newCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 使用 NewClientWithConfig 注入 LSPConfig（T1-P1-A2）
	// e.lspConfig 为 nil 时降级到 DefaultLSPConfig（与原 NewClient 行为一致）
	client, err := lsp.NewClientWithConfig(newCtx, lang, workDir, e.lspConfig)
	if err != nil {
		return nil, err
	}

	e.lspClientCache[lang] = client
	return client, nil
}

// SetLSPConfig 更新 LSP 配置（SIGHUP 热重载 hook 调用）
// 已启动的 client 不重启，仅影响下次 NewClientWithConfig
// 调用方需持 lspClientMu 锁外调用，本方法内部加锁
// T1-P1-A2新增
func (e *Executor) SetLSPConfig(cfg *lsp.LSPConfig) {
	e.lspClientMu.Lock()
	defer e.lspClientMu.Unlock()
	e.lspConfig = cfg
}

// mcpListResourcesHandler MCP list resources handler
func (e *Executor) mcpListResourcesHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Server string `json:"server"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}

		client, err := e.getMCPClient(ctx, input.Server)
		if err != nil {
			return fmt.Sprintf("MCP not available: %v\nTip: Configure MCP servers in ~/.whale/mcp.json", err), nil
		}

		resources, err := client.ListResources(ctx)
		if err != nil {
			return fmt.Sprintf("List resources failed: %v", err), nil
		}

		if len(resources) == 0 {
			return "No resources available", nil
		}

		var results []string
		for _, r := range resources {
			results = append(results, fmt.Sprintf("%s — %s", r.Name, r.URI))
		}
		return strings.Join(results, "\n"), nil
	}
}

// mcpReadResourceHandler MCP read resource handler
func (e *Executor) mcpReadResourceHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Server string `json:"server"`
			URI    string `json:"uri"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Server == "" || input.URI == "" {
			return "", fmt.Errorf("server and uri are required")
		}

		client, err := e.getMCPClient(ctx, input.Server)
		if err != nil {
			return fmt.Sprintf("MCP not available: %v", err), nil
		}

		content, err := client.ReadResource(ctx, input.URI)
		if err != nil {
			return fmt.Sprintf("Read resource failed: %v", err), nil
		}

		return content, nil
	}
}

// mcpListToolsHandler MCP list tools handler
func (e *Executor) mcpListToolsHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Server string `json:"server"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}

		client, err := e.getMCPClient(ctx, input.Server)
		if err != nil {
			return fmt.Sprintf("MCP not available: %v\nTip: Configure MCP servers in ~/.whale/mcp.json", err), nil
		}

		tools, err := client.ListTools(ctx)
		if err != nil {
			return fmt.Sprintf("List tools failed: %v", err), nil
		}

		if len(tools) == 0 {
			return "No tools available", nil
		}

		var results []string
		for _, tool := range tools {
			desc := tool.Description
			if desc == "" {
				desc = "(no description)"
			}
			results = append(results, fmt.Sprintf("%s — %s", tool.Name, desc))
		}
		return strings.Join(results, "\n"), nil
	}
}

// mcpCallToolHandler MCP call tool handler
func (e *Executor) mcpCallToolHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Server    string                 `json:"server"`
			ToolName  string                 `json:"tool_name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Server == "" || input.ToolName == "" {
			return "", fmt.Errorf("server and tool_name are required")
		}

		client, err := e.getMCPClient(ctx, input.Server)
		if err != nil {
			return fmt.Sprintf("MCP not available: %v", err), nil
		}

		if input.Arguments == nil {
			input.Arguments = make(map[string]interface{})
		}

		result, err := client.CallTool(ctx, input.ToolName, input.Arguments)
		if err != nil {
			return fmt.Sprintf("Call tool failed: %v", err), nil
		}

		return result.Text(), nil
	}
}

// mcpListPromptsHandler MCP list prompts handler
func (e *Executor) mcpListPromptsHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Server string `json:"server"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}

		client, err := e.getMCPClient(ctx, input.Server)
		if err != nil {
			return fmt.Sprintf("MCP not available: %v\nTip: Configure MCP servers in ~/.whale/mcp.json", err), nil
		}

		prompts, err := client.ListPrompts(ctx)
		if err != nil {
			return fmt.Sprintf("List prompts failed: %v", err), nil
		}

		if len(prompts) == 0 {
			return "No prompts available", nil
		}

		var results []string
		for _, p := range prompts {
			desc := p.Description
			if desc == "" {
				desc = "(no description)"
			}
			argNames := make([]string, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				argNames = append(argNames, a.Name)
			}
			if len(argNames) > 0 {
				results = append(results, fmt.Sprintf("%s — %s (args: %s)", p.Name, desc, strings.Join(argNames, ", ")))
			} else {
				results = append(results, fmt.Sprintf("%s — %s", p.Name, desc))
			}
		}
		return strings.Join(results, "\n"), nil
	}
}

// mcpGetPromptHandler MCP get prompt handler
func (e *Executor) mcpGetPromptHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Server     string            `json:"server"`
			PromptName string            `json:"prompt_name"`
			Arguments  map[string]string `json:"arguments"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Server == "" || input.PromptName == "" {
			return "", fmt.Errorf("server and prompt_name are required")
		}

		client, err := e.getMCPClient(ctx, input.Server)
		if err != nil {
			return fmt.Sprintf("MCP not available: %v", err), nil
		}

		messages, err := client.GetPrompt(ctx, input.PromptName, input.Arguments)
		if err != nil {
			return fmt.Sprintf("Get prompt failed: %v", err), nil
		}

		if len(messages) == 0 {
			return "No messages in prompt", nil
		}

		var results []string
		for _, msg := range messages {
			results = append(results, fmt.Sprintf("[%s] %s", msg.Role, msg.Content.Text))
		}
		return strings.Join(results, "\n"), nil
	}
}

// mcpClientCache cache MCP clients by server name
var (
	mcpClientCache     = make(map[string]MCPClient)
	mcpClientCreatedAt = make(map[string]time.Time)
	mcpClientMu        sync.Mutex
	mcpClientMaxAge    = 30 * time.Minute

	// P0-7：elicitation handler 全局注册点
	// TUI 层在初始化时通过 SetMCPElicitationHandler 注入回调，
	// getMCPClient 创建新 client 时自动注册此 handler。
	// 对标同类 CLI ElicitationRequestManager
	mcpElicitationHandler   mcp.ElicitationHandler
	mcpElicitationHandlerMu sync.RWMutex
)

// SetMCPElicitationHandler 注册全局 MCP elicitation 请求回调（P0-7）。
// TUI 层在初始化时调用此函数注入回调，后续 getMCPClient 创建的新 client 会自动注册。
// handler=nil 清除回调（elicitation 请求将返回 decline，fail-safe）。
//
// 对标同类 CLI ElicitationRequestManager::new
func SetMCPElicitationHandler(handler mcp.ElicitationHandler) {
	mcpElicitationHandlerMu.Lock()
	mcpElicitationHandler = handler
	mcpElicitationHandlerMu.Unlock()
}

// GetCachedMCPClient 按服务器名查找已缓存的 MCP 客户端（P0-7）。
// 仅查找不创建：若缓存中不存在返回 (nil, false)。
// TUI 层在 ResolveElicitation 时通过此函数获取对应 server 的 client。
//
// 返回的 MCPClient 接口可能实现了 mcp.MCPClient（含 SetElicitationHandler/ResolveElicitation），
// 调用方可通过类型断言访问 elicitation 相关方法。
func GetCachedMCPClient(serverName string) (MCPClient, bool) {
	mcpClientMu.Lock()
	defer mcpClientMu.Unlock()
	client, ok := mcpClientCache[serverName]
	return client, ok
}

// getMCPClient get or create MCP client with unified config loading
func (e *Executor) getMCPClient(ctx context.Context, serverName string) (MCPClient, error) {
	return e.getMCPClientForWorkDir(ctx, serverName, e.workDir)
}

// mcpSandboxManager 返回启动 MCP server 子进程时**可用于**沙箱隔离的沙箱管理器。
//
// ★ （2026-10-10，对齐 同类 CLI）翻转过此处的语义 —— 历史沿革：
// - G1 决策：默认 `relaxed` ⇒ 外部 server 一律套 OS 沙箱启动。
// 实测副作用：联网型 server（检索/GitHub/远程库）开箱即死，且在面板点"允许"
// 也放行不了（审批轴与强制轴零连线）。见 内部问题台账 。
// - ：**默认改为去沙箱**（`parseSandboxLevel("") → Disabled`）。
// 本函数仍**照常返回管理器**，用途只剩"支撑显式 opt-in 逃生舱"：
// 用户在 `.whale/mcp.json` 显式写 `"sandbox_level":"relaxed"` 时回到沙箱路径。
//
// 返回值语义：**非 nil ≠ 一定套沙箱**；是否套沙箱由 server 配置的 sandbox_level 决定
// （transport 按 level 三态分流，见 transport_stdio.go）。故"永不返回 nil"不再是
// "无沙箱不启动"的保证，而只是"显式要沙箱时拿得到管理器"。
//
// 取值优先级：
// 1. Executor 注入的生产沙箱管理器（TUI lifecycle.go:442 / EnableProductionSandbox）；
// 2. 未注入时就地构造 sandbox.NewProductionSandboxManager（与 1 同一单一事实源）。
//
// ★ 密钥过滤不再依附本函数：无论走不走沙箱，子进程 env 都会剥 `*_API_KEY/*_TOKEN/…`
// （去沙箱路径用 sandbox.UnsandboxedEnvConfig，单一事实源仍是 sandbox.DefaultEnvConfig 的 DenyList）。
func (e *Executor) mcpSandboxManager(workDir string) sandbox.Manager {
	e.mu.RLock()
	mgr := e.SandboxMgr
	e.mu.RUnlock()
	if mgr != nil {
		return mgr
	}
	return sandbox.NewProductionSandboxManager(workDir)
}

func (e *Executor) getMCPClientForWorkDir(ctx context.Context, serverName, workDir string) (MCPClient, error) {
	if serverName == "" {
		return nil, fmt.Errorf("server name is required")
	}

	mcpClientMu.Lock()
	defer mcpClientMu.Unlock()

	if client, ok := mcpClientCache[serverName]; ok {
		if createdAt, ok := mcpClientCreatedAt[serverName]; ok && time.Since(createdAt) < mcpClientMaxAge {
			return client, nil
		}
		client.Close()
		delete(mcpClientCache, serverName)
		delete(mcpClientCreatedAt, serverName)
	}

	config := loadMCPConfig(workDir)

	serverConfig, ok := config.Servers[serverName]
	if !ok {
		return nil, fmt.Errorf("server %s not found in config", serverName)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var client *mcp.Client
	var err error

	// Support SSE transport
	if serverConfig.URL != "" {
		client, err = mcp.NewSSEClient(ctx, serverName, serverConfig.URL, nil)
	} else {
		// （2026-10-10，对齐 同类 CLI）：stdio MCP server **默认去沙箱**启动
		// （serverConfig.SandboxLevel 缺省 → parseSandboxLevel → Disabled ⇒ 不 Prepare、
		// 不 Restrict），但仍剥宿主密钥（transport 走 UnsandboxedEnvConfig）。
		// 管理器照传：仅用于支撑显式 `"sandbox_level":"relaxed"` 的逃生舱。
		// ★ 这是对 G1（2026-09-10「无沙箱不启动」）的**有意翻转**，见 内部问题台账 。
		client, err = mcp.NewClient(ctx, serverName, serverConfig, e.mcpSandboxManager(workDir))
	}

	if err != nil {
		return nil, err
	}

	// G3 决策：按 mcp.json 顶层 elicitations 开关（默认开启）配置交互。
	// 对齐业内既有实现 GranularApprovalConfig.mcp_elicitations。
	client.SetElicitationsEnabled(config.ElicitationsEnabled())

	// P0-7：新 client 创建后注册全局 elicitation handler（若已设置）
	// 对标同类 CLI ElicitationRequestManager 注入模式
	mcpElicitationHandlerMu.RLock()
	handler := mcpElicitationHandler
	mcpElicitationHandlerMu.RUnlock()
	if handler != nil {
		client.SetElicitationHandler(handler)
	}

	mcpClientCache[serverName] = client
	mcpClientCreatedAt[serverName] = time.Now()
	return client, nil
}

// loadMCPConfig load MCP config from multiple sources (unified)
func loadMCPConfig(workDir string) *mcp.Config {
	var dirs []string

	// Load from working directory first
	if workDir != "" {
		dirs = append(dirs, filepath.Join(workDir, ".whale"))
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		dirs = append(dirs, whalehome.Dir())
	}

	cfg := mcp.LoadConfigFromDirs(dirs...)

	return cfg
}

// DiscoverAndRegisterMCPTools discovers and registers tools provided by MCP servers
func (e *Executor) DiscoverAndRegisterMCPTools(workDir string) error {
	config := loadMCPConfig(workDir)
	if len(config.Servers) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var errs []string

	for serverName := range config.Servers {
		client, err := e.getMCPClientForWorkDir(ctx, serverName, workDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: connection failed: %v", serverName, err))
			continue
		}

		if !client.HasTools() {
			continue
		}

		tools, err := client.ListTools(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: list tools failed: %v", serverName, err))
			continue
		}

		for _, tool := range tools {
			// ④ 决策：注册名必须与权限 deny 契约一致。
			// 旧实现用单下划线 `mcp_<server>_<tool>`，而 deny 匹配契约（normalization.go:19,57
			// + permission/rules.go:167-172 的 `mcp__server` 前缀匹配）用双下划线
			// `mcp__<server>__<tool>` → 按文档写的 deny 规则静默命中不了。
			// 统一改用 mcp.BuildMcpToolName（归一化 server/tool 名，防注入字符破坏格式）。
			mcpToolName := mcp.BuildMcpToolName(serverName, tool.Name)
			spec := Spec{
				Name:               mcpToolName,
				Description:        fmt.Sprintf("[MCP/%s] %s", serverName, tool.Description),
				InputSchema:        tool.InputSchema,
				RequiredPermission: LevelWorkspaceWrite,
				ConcurrencySafe:    false,
			}

			if err := e.registry.RegisterMCP(spec); err != nil {
				continue
			}

			capturedServer := serverName
			capturedToolName := tool.Name
			e.mu.Lock()
			e.handlers[mcpToolName] = e.mcpDynamicToolHandler(capturedServer, capturedToolName)
			e.mu.Unlock()
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("MCP discovery errors: %s", strings.Join(errs, "; "))
	}

	return nil
}

// mcpDynamicToolHandler create a handler for a dynamically discovered MCP tool
func (e *Executor) mcpDynamicToolHandler(serverName, toolName string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var arguments map[string]interface{}
		if err := json.Unmarshal(raw, &arguments); err != nil {
			arguments = make(map[string]interface{})
		}

		client, err := e.getMCPClient(ctx, serverName)
		if err != nil {
			return fmt.Sprintf("MCP server %s not available: %v", serverName, err), nil
		}

		result, err := client.CallTool(ctx, toolName, arguments)
		if err != nil {
			return fmt.Sprintf("MCP tool %s/%s failed: %v", serverName, toolName, err), nil
		}

		return result.Text(), nil
	}
}

// CloseAllMCPClients close all cached MCP clients
func CloseAllMCPClients() {
	mcpClientMu.Lock()
	defer mcpClientMu.Unlock()

	for name, client := range mcpClientCache {
		client.Close()
		delete(mcpClientCache, name)
	}
}

// RemoveMCPClient disconnects and removes the specified MCP client cache
func RemoveMCPClient(name string) error {
	mcpClientMu.Lock()
	defer mcpClientMu.Unlock()

	client, ok := mcpClientCache[name]
	if !ok {
		return fmt.Errorf("MCP server %s not found in cache", name)
	}
	client.Close()
	delete(mcpClientCache, name)
	return nil
}

// RefreshMCPTools disconnects and reconnects the specified MCP server, re-registers its tools
func (e *Executor) RefreshMCPTools(workDir string, serverName string) (int, error) {
	// Disconnect old connection
	mcpClientMu.Lock()
	if client, ok := mcpClientCache[serverName]; ok {
		client.Close()
		delete(mcpClientCache, serverName)
	}
	mcpClientMu.Unlock()

	// Reconnect
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := e.getMCPClientForWorkDir(ctx, serverName, workDir)
	if err != nil {
		return 0, fmt.Errorf("reconnection failed: %w", err)
	}

	if !client.HasTools() {
		return 0, nil
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list tools: %w", err)
	}

	// Re-register tools
	for _, tool := range tools {
		// ④ 决策：与 DiscoverAndRegisterMCPTools 同源点，注册名统一为
		// mcp.BuildMcpToolName（mcp__<server>__<tool>），保证 deny 契约一致。
		mcpToolName := mcp.BuildMcpToolName(serverName, tool.Name)
		spec := Spec{
			Name:               mcpToolName,
			Description:        fmt.Sprintf("[MCP/%s] %s", serverName, tool.Description),
			InputSchema:        tool.InputSchema,
			RequiredPermission: LevelWorkspaceWrite,
			ConcurrencySafe:    false,
		}

		if err := e.registry.RegisterMCP(spec); err != nil {
			continue
		}

		capturedServer := serverName
		capturedToolName := tool.Name
		e.mu.Lock()
		e.handlers[mcpToolName] = e.mcpDynamicToolHandler(capturedServer, capturedToolName)
		e.mu.Unlock()
	}

	return len(tools), nil
}

// notebookEditHandler notebook edit handler
func (e *Executor) notebookEditHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Path      string `json:"path"`
			CellIndex int    `json:"cell_index"`
			NewSource string `json:"new_source"`
			Content   string `json:"content"` // legacy alias for new_source
			CellType  string `json:"cell_type"`
			EditKind  string `json:"edit_kind"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Path == "" {
			return "", fmt.Errorf("path is required")
		}
		if err := validateFilePath(input.Path); err != nil {
			return "", err
		}

		// Support both new_source and content (legacy) fields
		sourceText := input.NewSource
		if sourceText == "" {
			sourceText = input.Content
		}

		nb, err := loadNotebook(input.Path)
		if err != nil {
			return fmt.Sprintf("Load notebook failed: %v", err), nil
		}

		switch input.EditKind {
		case "replace":
			if input.CellIndex < 0 || input.CellIndex >= len(nb.Cells) {
				return fmt.Sprintf("Cell index %d out of range (0-%d)", input.CellIndex, len(nb.Cells)-1), nil
			}
			if sourceText == "" {
				return "", fmt.Errorf("new_source cannot be empty for replace operation")
			}
			nb.Cells[input.CellIndex].Source = splitLines(sourceText)
			if input.CellType != "" {
				nb.Cells[input.CellIndex].CellType = input.CellType
			}
		case "insert":
			if sourceText == "" {
				return "", fmt.Errorf("new_source cannot be empty for insert operation")
			}
			cell := NotebookCell{
				CellType:       input.CellType,
				Source:         splitLines(sourceText),
				Metadata:       map[string]interface{}{},
				Outputs:        []interface{}{},
				ExecutionCount: nil,
			}
			if cell.CellType == "" {
				cell.CellType = "code"
			}
			idx := input.CellIndex
			if idx < 0 {
				idx = len(nb.Cells)
			}
			if idx >= len(nb.Cells) {
				nb.Cells = append(nb.Cells, cell)
			} else {
				nb.Cells = append(nb.Cells[:idx], append([]NotebookCell{cell}, nb.Cells[idx:]...)...)
			}
		case "delete":
			if input.CellIndex < 0 || input.CellIndex >= len(nb.Cells) {
				return fmt.Sprintf("Cell index %d out of range", input.CellIndex), nil
			}
			nb.Cells = append(nb.Cells[:input.CellIndex], nb.Cells[input.CellIndex+1:]...)
		default:
			return fmt.Sprintf("Unknown edit_kind: %s (use replace/insert/delete)", input.EditKind), nil
		}

		if err := saveNotebook(input.Path, nb); err != nil {
			return fmt.Sprintf("Save notebook failed: %v", err), nil
		}

		return fmt.Sprintf("Notebook updated: %s (%d cells)", input.Path, len(nb.Cells)), nil
	}
}

// NotebookFile notebook file structure
type NotebookFile struct {
	Cells         []NotebookCell         `json:"cells"`
	Metadata      map[string]interface{} `json:"metadata"`
	Nbformat      int                    `json:"nbformat"`
	NbformatMinor int                    `json:"nbformat_minor"`
}

// NotebookCell notebook cell
type NotebookCell struct {
	CellType       string                 `json:"cell_type"`
	Source         []string               `json:"source"`
	Metadata       map[string]interface{} `json:"metadata"`
	Outputs        []interface{}          `json:"outputs,omitempty"`
	ExecutionCount *int                   `json:"execution_count,omitempty"`
}

// loadNotebook load notebook file
func loadNotebook(path string) (*NotebookFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var nb NotebookFile
	if err := json.Unmarshal(data, &nb); err != nil {
		return nil, fmt.Errorf("invalid notebook format: %w", err)
	}

	if nb.Nbformat == 0 {
		nb.Nbformat = 4
		nb.NbformatMinor = 5
	}
	if nb.Metadata == nil {
		nb.Metadata = make(map[string]interface{})
	}

	return &nb, nil
}

// saveNotebook save notebook file
func saveNotebook(path string, nb *NotebookFile) error {
	data, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		return err
	}
	tmpFile := path + ".whale-tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		os.Remove(tmpFile)
		return err
	}
	if err := os.Rename(tmpFile, path); err != nil {
		os.Remove(tmpFile)
		return err
	}
	return nil
}

// splitLines split text into lines for notebook source format
func splitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	lines := strings.Split(text, "\n")
	result := make([]string, len(lines))
	for i, line := range lines {
		if i < len(lines)-1 {
			result[i] = line + "\n"
		} else {
			result[i] = line
		}
	}
	return result
}
