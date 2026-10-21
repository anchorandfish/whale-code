// Package lsp — Language Server Protocol client
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// Client LSP client
type Client struct {
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdout          *bufio.Reader
	serverID        atomic.Int64
	pending         map[int64]chan json.RawMessage
	mu              sync.RWMutex
	rootURI         string
	ready           bool
	diagnosticsChan chan []Diagnostic
	cancelPending   context.CancelFunc
	// stopChan 主动退出信号，Close 时 close，让 readLoop defer 跳过重复清理 pending
	stopChan chan struct{}
	// wg 跟踪 readLoop goroutine，Close 通过 wg.Wait 等其退出
	wg sync.WaitGroup

	// T1-P1-A2新增字段：LSP 配置 + 崩溃恢复
	// SNAPSHOT--T1-P1-A2
	cfg           *LSPConfig  // 注入的配置（不可变，用于 restart 时查找 server）
	language      string      // 当前语言（用于 restart 时查找 server）
	workspaceRoot string      // 工作区根目录（用于 restart 时设置 cmd.Dir）
	restartCount  int         // 当前已重启次数
	lastRestart   time.Time   // 上次重启时间（用于退避窗口）
	restartMu     sync.Mutex  // 保护 restart 串行化（吸纳优化 1：与 Close 并发安全）
	crashed       atomic.Bool // 是否已崩溃（readLoop EOF 设置 true，restart 重置；超 maxRestarts 后永久 true）
	// / M18-03 SNAPSHOT：多根工作区支持
	workspaceFolders []WorkspaceFolder // 多根工作区（空=单根模式，向后兼容）
}

// Position LSP position
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range LSP range
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location LSP location
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Diagnostic LSP diagnostic
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// CompletionItem is a completion item
type CompletionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind"`
	Detail        string `json:"detail,omitempty"`
	Documentation string `json:"documentation,omitempty"`
}

// WorkspaceEdit is a workspace edit
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// TextEdit is a text edit
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// CodeAction is a code action
type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
	Command     *Command       `json:"command,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
}

// Command LSP command
type Command struct {
	Title     string        `json:"title"`
	Command   string        `json:"command"`
	Arguments []interface{} `json:"arguments,omitempty"`
}

// DocumentSymbol is a document symbol
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// WorkspaceFolder 表示 LSP 多根工作区中的一个文件夹（/ M18-03）。
// 对标 LSP 3.17 spec §"WorkspaceFolder"（[microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) @2023-08）。
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// InlayHint 表示 LSP 3.17 inlay hint（/ M18-04）。
// 对标 LSP 3.17 spec §"InlayHint"（@2023-08）。
// Kind 枚举：1=Type / 2=Parameter。
type InlayHint struct {
	Position     Position `json:"position"`
	Label        string   `json:"label"`
	Kind         int      `json:"kind,omitempty"`
	PaddingLeft  bool     `json:"paddingLeft,omitempty"`
	PaddingRight bool     `json:"paddingRight,omitempty"`
}

// SemanticTokens 表示 LSP 3.17 semantic tokens 响应（/ M18-04）。
// 对标 LSP 3.17 spec §"SemanticTokens"（@2023-08）。
// Data 是 delta 编码的 token 数组（每 5 个整数一组：line/char/length/tokenType/modifier）。
type SemanticTokens struct {
	Data []int `json:"data"`
}

// CodeLens 表示 LSP 3.17 code lens（/ M18-04）。
// 对标 LSP 3.17 spec §"CodeLens"（@2023-08）。
type CodeLens struct {
	Range   Range    `json:"range"`
	Command *Command `json:"command,omitempty"`
}

// NewClient create LSP client（向后兼容：使用 DefaultLSPConfig）
// 等价于 NewClientWithConfig(ctx, lang, workspaceRoot, nil)
// 保留原签名以兼容现有调用方（internal/tools/lsp_mcp_notebook.go 等）
func NewClient(ctx context.Context, language, workspaceRoot string) (*Client, error) {
	return NewClientWithConfig(ctx, language, workspaceRoot, nil)
}

// NewClientWithConfig 创建 LSP 客户端（注入 LSPConfig）
// cfg 为 nil 时降级到 DefaultLSPConfig（与原 NewClient 行为一致）
// T1-P1-A2新增：支持用户从 ~/.whale/lsp.json 加载自定义配置
// §3.1.1
func NewClientWithConfig(ctx context.Context, language, workspaceRoot string, cfg *LSPConfig) (*Client, error) {
	if cfg == nil {
		cfg = DefaultLSPConfig()
	}
	ctx, cancel := context.WithCancel(ctx)
	// cleanup 兜底 cancel：失败路径 return 时由 defer 触发；
	// 成功路径上把 cancel 责任转移给 Client.cancelPending 后置空 cleanup，
	// 避免 defer 在 NewClient 返回时立即取消 ctx 导致 cmd 被 kill
	cleanup := func() { cancel() }
	defer func() { cleanup() }()

	rootURI := platform.FilePathToURI(workspaceRoot)

	c := &Client{
		pending:         make(map[int64]chan json.RawMessage),
		rootURI:         rootURI,
		diagnosticsChan: make(chan []Diagnostic, 10),
		stopChan:        make(chan struct{}),
		cfg:             cfg,
		language:        language,
		workspaceRoot:   workspaceRoot,
	}

	// 调用 spawnAndInitialize 完成子进程启动 + initialize 握手
	// 失败路径：spawnAndInitialize 内部已清理资源（stdin/stdout/cmd/readLoop）
	if err := c.spawnAndInitialize(ctx); err != nil {
		// spawnAndInitialize 失败时已清理 cmd/stdin/stdout/readLoop
		// 此处兜底：cancel ctx + 等 readLoop 退出（若已启动）
		cleanup = func() {} // cancel 责任由 spawnAndInitialize 内部已处理或不需要
		close(c.stopChan)
		if c.stdin != nil {
			c.stdin.Close()
		}
		c.wg.Wait()
		cancel() // 显式 cancel：兜底
		return nil, err
	}

	c.ready = true
	cleanup = func() {} // cancel 责任已转移给 Client.cancelPending，defer 不再 cancel 避免 NewClient 返回时立即取消 ctx

	// 启动健康检查（仅 HealthCheckMs > 0 时启用，默认禁用，吸纳优化 2）
	if cfg.Recovery.HealthCheckMs > 0 {
		c.startHealthChecker(time.Duration(cfg.Recovery.HealthCheckMs) * time.Millisecond)
	}

	return c, nil
}

// findLanguageServer find language server command for given language
func findLanguageServer(language string) string {
	servers := map[string][]string{
		"go":         {"gopls"},
		"python":     {"pylsp", "pyright-langserver"},
		"typescript": {"typescript-language-server", "ts-node"},
		"rust":       {"rust-analyzer"},
		"c":          {"clangd"},
		"cpp":        {"clangd"},
	}

	lang := strings.ToLower(language)
	if cmds, ok := servers[lang]; ok {
		for _, cmd := range cmds {
			if p, err := exec.LookPath(cmd); err == nil {
				return p
			}
		}
	}
	return ""
}

// serverArgs 根据已解析的语言 server 可执行文件路径返回 stdio 启动参数
// 不同 LSP server 进入 stdio 模式的方式不同：
// - gopls 需要 `serve` 子命令
// - pyright-langserver / typescript-language-server 需要 `--stdio` 参数
// - pylsp / rust-analyzer / clangd / ts-node 默认以 stdio 模式运行，无需额外参数
//
// serverCmd 为 findLanguageServer 返回的完整路径，本函数取其基名判断类型
func serverArgs(serverCmd string) []string {
	base := strings.ToLower(filepath.Base(serverCmd))
	// Windows 下 LookPath 可能返回 .exe 后缀，统一去除后再判断
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "gopls":
		return []string{"serve"}
	case "pyright-langserver", "typescript-language-server":
		return []string{"--stdio"}
	default:
		// pylsp, rust-analyzer, clangd, ts-node 等默认 stdio，无需参数
		return nil
	}
}

// DetectLanguage detect language from file extension
func DetectLanguage(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "typescript"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	default:
		return ""
	}
}

// readLoop read responses from language server
// T1-P1-A2修改：EOF 时设置 crashed=true（仅当不是主动 Close 触发的退出）
func (c *Client) readLoop() {
	defer c.wg.Done()
	defer func() {
		// 检查 stopChan：若已关闭说明 Close 已接管清理 pending，readLoop 不再重复 close（避免 double-close）
		select {
		case <-c.stopChan:
			// 主动 Close 触发的退出，不标记 crashed
			return
		default:
		}
		// EOF 触发的退出（非主动 Close），标记 crashed=true 让调用方触发 Restart
		// T1-P1-A2新增：服务器崩溃自动恢复
		c.crashed.Store(true)
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}()

	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return
		}

		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Content-Length:") {
			continue
		}

		lengthStr := strings.TrimPrefix(line, "Content-Length:")
		length, err := strconv.Atoi(strings.TrimSpace(lengthStr))
		if err != nil {
			continue
		}

		c.stdout.ReadString('\n')

		buf := make([]byte, length)
		if _, err := io.ReadFull(c.stdout, buf); err != nil {
			return
		}

		var msg struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(buf, &msg); err != nil {
			continue
		}

		// process diagnostics notification
		if msg.Method == "textDocument/publishDiagnostics" {
			var params struct {
				URI         string       `json:"uri"`
				Diagnostics []Diagnostic `json:"diagnostics"`
			}
			if err := json.Unmarshal(msg.Params, &params); err == nil {
				select {
				case c.diagnosticsChan <- params.Diagnostics:
				default:
					// channel full, drop out
				}
			}
			continue
		}

		if msg.ID > 0 {
			c.mu.RLock()
			ch, ok := c.pending[msg.ID]
			c.mu.RUnlock()
			if ok {
				ch <- msg.Result
				c.mu.Lock()
				delete(c.pending, msg.ID)
				c.mu.Unlock()
			}
		}
	}
}

// sendRequest send JSON-RPC request
func (c *Client) sendRequest(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := c.serverID.Add(1)

	reqObj := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	reqBytes, err := json.Marshal(reqObj)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	content := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(reqBytes), string(reqBytes))
	if _, err := c.stdin.Write([]byte(content)); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}

	select {
	case result, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("connection closed")
		}
		return result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// initialize send initialize request
func (c *Client) initialize(ctx context.Context, rootURI string) error {
	params := map[string]interface{}{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"definition": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"references": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"publishDiagnostics": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"hover": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"completion": map[string]interface{}{
					"dynamicRegistration": false,
					"completionItem": map[string]interface{}{
						"snippetSupport": false,
					},
				},
				"rename": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"implementation": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"typeDefinition": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"codeAction": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"documentSymbol": map[string]interface{}{
					"dynamicRegistration": false,
				},
				// / M18-04 SNAPSHOT：LSP 3.17 新特性客户端能力声明
				"inlayHint": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"semanticTokens": map[string]interface{}{
					"dynamicRegistration": false,
					"requests": map[string]interface{}{
						"full": true,
					},
				},
				"codeLens": map[string]interface{}{
					"dynamicRegistration": false,
				},
			},
			// / M18-03 SNAPSHOT：多根工作区客户端能力声明
			"workspace": map[string]interface{}{
				"workspaceFolders": true,
			},
		},
	}

	// / M18-03：多根工作区支持（workspaceFolders 非空时发送）
	if len(c.workspaceFolders) > 0 {
		params["workspaceFolders"] = c.workspaceFolders
	}

	_, err := c.sendRequest(ctx, "initialize", params)
	if err != nil {
		return err
	}

	notifObj := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]interface{}{},
	}
	notifBytes, _ := json.Marshal(notifObj)
	content := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(notifBytes), string(notifBytes))
	c.stdin.Write([]byte(content))

	return nil
}

// Definition go to definition
func (c *Client) Definition(ctx context.Context, filePath string, line, character int) ([]Location, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
	}

	result, err := c.sendRequest(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, err
	}

	var locations []Location
	if err := json.Unmarshal(result, &locations); err != nil {
		var singleLoc Location
		if err2 := json.Unmarshal(result, &singleLoc); err2 != nil {
			return nil, nil
		}
		return []Location{singleLoc}, nil
	}

	return locations, nil
}

// References find references
func (c *Client) References(ctx context.Context, filePath string, line, character int) ([]Location, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
		"context":      map[string]interface{}{"includeDeclaration": true},
	}

	result, err := c.sendRequest(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}

	var locations []Location
	if err := json.Unmarshal(result, &locations); err != nil {
		return nil, nil
	}

	return locations, nil
}

// Hover fetches hover info
func (c *Client) Hover(ctx context.Context, filePath string, line, character int) (string, error) {
	if !c.ready {
		return "", fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
	}

	result, err := c.sendRequest(ctx, "textDocument/hover", params)
	if err != nil {
		return "", err
	}

	return parseHoverContents(result), nil
}

// parseHoverContents parses Hover.contents which can be MarkupContent, MarkedString, or MarkedString[]
func parseHoverContents(result json.RawMessage) string {
	var raw struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(result, &raw); err != nil {
		return ""
	}
	if len(raw.Contents) == 0 {
		return ""
	}

	// try MarkupContent format: {"kind": "...", "value": "..."}
	var markup struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw.Contents, &markup); err == nil && markup.Value != "" {
		return markup.Value
	}

	// try MarkedString[] format: [string | {language, value}, ...]
	var arr []json.RawMessage
	if err := json.Unmarshal(raw.Contents, &arr); err == nil {
		var parts []string
		for _, item := range arr {
			parts = append(parts, parseMarkedString(item))
		}
		return strings.Join(parts, "\n\n")
	}

	// try single MarkedString: string or {language, value}
	return parseMarkedString(raw.Contents)
}

// parseMarkedString parses a single MarkedString (plain string or {language, value})
func parseMarkedString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var ms struct {
		Language string `json:"language"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms.Language != "" {
			return fmt.Sprintf("```%s\n%s\n```", ms.Language, ms.Value)
		}
		return ms.Value
	}
	return ""
}

// Completion fetches code completions
func (c *Client) Completion(ctx context.Context, filePath string, line, character int) ([]CompletionItem, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
	}

	result, err := c.sendRequest(ctx, "textDocument/completion", params)
	if err != nil {
		return nil, err
	}

	// try resolving as array or object (LSP may return CompletionList or CompletionItem[])
	var items []CompletionItem

	// first try CompletionList format
	var completionList struct {
		Items []CompletionItem `json:"items"`
	}
	if err := json.Unmarshal(result, &completionList); err == nil && len(completionList.Items) > 0 {
		return completionList.Items, nil
	}

	// then try direct array
	if err := json.Unmarshal(result, &items); err == nil {
		return items, nil
	}

	return nil, nil
}

// Rename renames a symbol
func (c *Client) Rename(ctx context.Context, filePath string, line, character int, newName string) (*WorkspaceEdit, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
		"newName":      newName,
	}

	result, err := c.sendRequest(ctx, "textDocument/rename", params)
	if err != nil {
		return nil, err
	}

	var edit WorkspaceEdit
	if err := json.Unmarshal(result, &edit); err != nil {
		return nil, nil
	}

	return &edit, nil
}

// Diagnostics get diagnostics for a file
// Diagnostics fetches file diagnostics info (sends didOpen and waits for push result)
func (c *Client) Diagnostics(ctx context.Context, filePath string) ([]Diagnostic, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	// read file content
	var fileText string
	if data, err := os.ReadFile(filePath); err == nil {
		fileText = string(data)
	}

	// 重构：复用 DidOpen 方法，消除内联 didOpen 代码重复（version 硬编码 1）
	if err := c.DidOpen(ctx, filePath, DetectLanguageID(filePath), fileText); err != nil {
		return nil, err
	}

	// wait for diagnostics push (5 second timeout)
	timeout := time.After(5 * time.Second)
	select {
	case diags := <-c.diagnosticsChan:
		return diags, nil
	case <-timeout:
		return nil, fmt.Errorf("LSP diagnostics timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CollectDiagnostics collects workspace diagnostics info (alias for Diagnostics)
func (c *Client) CollectDiagnostics(ctx context.Context, filePath string) ([]Diagnostic, error) {
	return c.Diagnostics(ctx, filePath)
}

// DetectLanguageID detects language ID from file extension
func DetectLanguageID(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	langMap := map[string]string{
		".go":   "go",
		".py":   "python",
		".ts":   "typescript",
		".tsx":  "typescriptreact",
		".js":   "javascript",
		".jsx":  "javascriptreact",
		".rs":   "rust",
		".c":    "c",
		".cpp":  "cpp",
		".h":    "c",
		".hpp":  "cpp",
		".java": "java",
	}
	if lang, ok := langMap[ext]; ok {
		return lang
	}
	return ""
}

// Implementation finds interface implementation locations
func (c *Client) Implementation(ctx context.Context, filePath string, line, character int) ([]Location, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
	}

	result, err := c.sendRequest(ctx, "textDocument/implementation", params)
	if err != nil {
		return nil, err
	}

	var locations []Location
	if err := json.Unmarshal(result, &locations); err != nil {
		var singleLoc Location
		if err2 := json.Unmarshal(result, &singleLoc); err2 != nil {
			return nil, nil
		}
		return []Location{singleLoc}, nil
	}

	return locations, nil
}

// TypeDefinition finds symbol type definition locations
func (c *Client) TypeDefinition(ctx context.Context, filePath string, line, character int) ([]Location, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     Position{Line: line, Character: character},
	}

	result, err := c.sendRequest(ctx, "textDocument/typeDefinition", params)
	if err != nil {
		return nil, err
	}

	var locations []Location
	if err := json.Unmarshal(result, &locations); err != nil {
		var singleLoc Location
		if err2 := json.Unmarshal(result, &singleLoc); err2 != nil {
			return nil, nil
		}
		return []Location{singleLoc}, nil
	}

	return locations, nil
}

// CodeActions fetches code action recommendations for specified position
func (c *Client) CodeActions(ctx context.Context, filePath string, line, character int, actionKind string) ([]CodeAction, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"range": Range{
			Start: Position{Line: line, Character: character},
			End:   Position{Line: line, Character: character},
		},
	}
	if actionKind != "" {
		params["context"] = map[string]interface{}{
			"diagnostics": []interface{}{},
			"only":        []string{actionKind},
		}
	}

	result, err := c.sendRequest(ctx, "textDocument/codeAction", params)
	if err != nil {
		return nil, err
	}

	var actions []CodeAction
	if err := json.Unmarshal(result, &actions); err != nil {
		return nil, nil
	}

	return actions, nil
}

// DocumentSymbols fetches document symbol outline
func (c *Client) DocumentSymbols(ctx context.Context, filePath string) ([]DocumentSymbol, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}

	uri := platform.FilePathToURI(filePath)

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}

	result, err := c.sendRequest(ctx, "textDocument/documentSymbol", params)
	if err != nil {
		return nil, err
	}

	// try DocumentSymbol[] format (hierarchical)
	var symbols []DocumentSymbol
	if err := json.Unmarshal(result, &symbols); err == nil && len(symbols) > 0 {
		return symbols, nil
	}

	// try SymbolInformation[] format (flat)
	var symInfos []SymbolInformation
	if err := json.Unmarshal(result, &symInfos); err == nil && len(symInfos) > 0 {
		return convertSymbolInformationToDocumentSymbols(symInfos), nil
	}

	return nil, nil
}

// SymbolInformation represents a flat symbol information (LSP 3.x)
type SymbolInformation struct {
	Name     string   `json:"name"`
	Kind     int      `json:"kind"`
	Location Location `json:"location"`
}

// convertSymbolInformationToDocumentSymbols converts flat SymbolInformation[] to hierarchical DocumentSymbol[]
func convertSymbolInformationToDocumentSymbols(infos []SymbolInformation) []DocumentSymbol {
	result := make([]DocumentSymbol, 0, len(infos))
	for _, info := range infos {
		result = append(result, DocumentSymbol{
			Name:           info.Name,
			Kind:           info.Kind,
			Range:          info.Location.Range,
			SelectionRange: info.Location.Range,
		})
	}
	return result
}

// Close 关闭 LSP client：发出退出信号后等 readLoop 真正退出再清理 pending（消除 double-close channel 竞争）
// 幂等：重复调用直接返回 nil；未初始化的 client（stopChan 为 nil）也直接返回
func (c *Client) Close() error {
	// 未初始化的 client（如 &Client{}），直接返回（向后兼容现有 TestClient_Close）
	if c.stopChan == nil {
		return nil
	}

	// 1. 发出退出信号（close stopChan 让 readLoop defer 跳过重复清理 pending）
	select {
	case <-c.stopChan:
		// 已关闭（重复 Close 调用），直接返回保证幂等
		return nil
	default:
		close(c.stopChan)
	}

	// 2. 优雅关闭：cancel ctx + shutdown request（LSP 协议要求）+ exit notification
	if c.cancelPending != nil {
		c.cancelPending()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	c.sendRequest(shutdownCtx, "shutdown", nil)
	shutdownCancel()
	c.sendNotification("exit", nil)

	// 3. 关闭 stdin 触发 server 退出，server 退出后 stdout EOF，readLoop 退出
	if c.stdin != nil {
		c.stdin.Close()
	}

	// 4. 等 readLoop 退出（带 3 秒超时，防止卡死导致 Close 永久阻塞）
	waitDone := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		// readLoop 卡死，后续强制 kill 进程后 stdout pipe 关闭，readLoop 退出
	}

	// 5. readLoop 已退出（或超时），此时清理 pending 无 double-close 竞争
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()

	// 6. 等进程退出，超时则 kill
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			c.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			c.cmd.Process.Kill()
			<-done
		}
	}
	return nil
}

// sendNotification sends a JSON-RPC notification to the language server
func (c *Client) sendNotification(method string, params interface{}) {
	if c.stdin == nil {
		return
	}
	notifObj := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		notifObj["params"] = params
	}
	notifBytes, err := json.Marshal(notifObj)
	if err != nil {
		return
	}
	content := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(notifBytes), string(notifBytes))
	c.stdin.Write([]byte(content))
}

// DidOpen 通知语言服务器打开文档，同步文件内容用于诊断
// uri 由 platform.FilePathToURI 转换而来，languageID 通过 DetectLanguageID 推断
// version 固定为 1（LSP 协议：首次打开），未就绪 client 返回错误不 panic
func (c *Client) DidOpen(ctx context.Context, filePath, languageID, content string) error {
	if !c.ready {
		return fmt.Errorf("LSP client not ready")
	}
	uri := platform.FilePathToURI(filePath)
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       content,
		},
	}
	c.sendNotification("textDocument/didOpen", params)
	return nil
}

// DidChange 通知语言服务器文档内容变更，version 须单调递增（LSP 协议要求）
// 采用全量替换（contentChanges:[{text:newContent}]），不使用 range 增量
// 调用方负责 version 自增（见 Executor.nextLSPVersion），未就绪 client 返回错误不 panic
func (c *Client) DidChange(ctx context.Context, filePath string, version int, newContent string) error {
	if !c.ready {
		return fmt.Errorf("LSP client not ready")
	}
	uri := platform.FilePathToURI(filePath)
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":     uri,
			"version": version,
		},
		"contentChanges": []map[string]interface{}{
			{"text": newContent},
		},
	}
	c.sendNotification("textDocument/didChange", params)
	return nil
}

// DidClose 通知语言服务器关闭文档，释放服务端资源
// 本批次仅提供方法，不接线调用点（接线属 4-B checkpoint 范畴），未就绪 client 返回错误不 panic
func (c *Client) DidClose(ctx context.Context, filePath string) error {
	if !c.ready {
		return fmt.Errorf("LSP client not ready")
	}
	uri := platform.FilePathToURI(filePath)
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	c.sendNotification("textDocument/didClose", params)
	return nil
}

// IsReady check if client is ready
func (c *Client) IsReady() bool {
	return c.ready
}

// SetWorkspaceFolders 设置多根工作区（/ M18-03）。
//
// 必须在 NewClient/NewClientWithConfig 之前调用（workspaceFolders 在 initialize 时发送）。
// 实际用法：构造 Client 后、initialize 前设置；或通过 NewClientWithWorkspaceFolders 构造。
// 空 folders 降级到单根 workspaceRoot 模式（向后兼容）。
//
// 对标 LSP 3.17 spec §"WorkspaceFolder"（[microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) @2023-08）。
func (c *Client) SetWorkspaceFolders(folders []WorkspaceFolder) {
	c.workspaceFolders = folders
}

// InlayHints 获取文件的内联提示（/ M18-04）。
//
// 发送 textDocument/inlayHint 请求，返回 LSP 3.17 InlayHint 列表。
// server 不支持时返回空切片 + nil（降级，不报错）。
//
// 对标 LSP 3.17 spec §"InlayHint Request"（@2023-08）。
func (c *Client) InlayHints(ctx context.Context, filePath string) ([]InlayHint, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}
	uri := platform.FilePathToURI(filePath)
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	result, err := c.sendRequest(ctx, "textDocument/inlayHint", params)
	if err != nil {
		// context 超时/取消 → 返回 error（不降级，尊重调用方超时语义）
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// server 不支持 → 降级返回空切片
		return []InlayHint{}, nil
	}
	var hints []InlayHint
	if err := json.Unmarshal(result, &hints); err != nil {
		return []InlayHint{}, nil
	}
	return hints, nil
}

// SemanticTokensFull 获取文件的完整语义 token（/ M18-04）。
//
// 发送 textDocument/semanticTokens/full 请求，返回 LSP 3.17 SemanticTokens。
// server 不支持时返回 nil + nil（降级，不报错）。
//
// 对标 LSP 3.17 spec §"Semantic Tokens Full Request"（@2023-08）。
func (c *Client) SemanticTokensFull(ctx context.Context, filePath string) (*SemanticTokens, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}
	uri := platform.FilePathToURI(filePath)
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	result, err := c.sendRequest(ctx, "textDocument/semanticTokens/full", params)
	if err != nil {
		// server 不支持 → 降级返回 nil
		return nil, nil
	}
	var tokens SemanticTokens
	if err := json.Unmarshal(result, &tokens); err != nil {
		return nil, nil
	}
	return &tokens, nil
}

// CodeLenses 获取文件的 code lens 列表（/ M18-04）。
//
// 发送 textDocument/codeLens 请求，返回 LSP 3.17 CodeLens 列表。
// server 不支持时返回空切片 + nil（降级，不报错）。
//
// 对标 LSP 3.17 spec §"Code Lens Request"（@2023-08）。
func (c *Client) CodeLenses(ctx context.Context, filePath string) ([]CodeLens, error) {
	if !c.ready {
		return nil, fmt.Errorf("LSP client not ready")
	}
	uri := platform.FilePathToURI(filePath)
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	result, err := c.sendRequest(ctx, "textDocument/codeLens", params)
	if err != nil {
		// server 不支持 → 降级返回空切片
		return []CodeLens{}, nil
	}
	var lenses []CodeLens
	if err := json.Unmarshal(result, &lenses); err != nil {
		return []CodeLens{}, nil
	}
	return lenses, nil
}

func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
