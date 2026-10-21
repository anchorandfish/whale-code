// Package lsp — / M18-04 LSP 3.17 新特性测试
//
// 测试覆盖：
// - InlayHints：textDocument/inlayHint 请求 + 反序列化 + 降级
// - SemanticTokensFull：textDocument/semanticTokens/full 请求 + 反序列化 + 降级
// - CodeLenses：textDocument/codeLens 请求 + 反序列化 + 降级
// - SetWorkspaceFolders：多根工作区设置 + initialize 参数注入
// - 诊断渲染辅助（diagnostics.go）：SeverityToLabel / FormatDiagnostic / FormatDiagnostics / SanitizeString
//
// 测试架构：通过 io.Pipe 将 Client 与 MockServer 直连，绕过 exec.CommandContext
// 子进程启动，实现 L3 mock 测试层（02-审查报告 §2.5 不依赖真实 gopls）。
//
// 对标 LSP 3.17 spec
// [microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) @2023-08
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// === 测试辅助 ===

// newClientWithMock 创建一个手动接线的 Client，绕过 exec.CommandContext 子进程启动。
//
// 通过 io.Pipe 将 Client.stdin/stdout 与 MockServer 直连：
// - Client.stdin → pipeA → MockServer.reader
// - MockServer.writer → pipeB → Client.stdout
//
// 启动 readLoop goroutine 处理响应。返回 Client + MockServer + cleanup。
// cleanup 关闭所有 pipe + 等 readLoop 退出。
func newClientWithMock(t *testing.T) (*Client, *MockServer, func()) {
	t.Helper()
	server := NewMockServer()

	// Client → Server pipe（io.Pipe 返回 (*PipeReader, *PipeWriter)：
	// serverRead 取 *PipeReader 用于 MockServer.Serve 读取，clientStdin 取 *PipeWriter 用于 Client.stdin 写入）
	serverRead, clientStdin := io.Pipe()
	// Server → Client pipe（clientStdout 取 *PipeReader 供 Client.stdout bufio 包裹读取，
	// serverWrite 取 *PipeWriter 供 MockServer.Serve 写入）
	clientStdout, serverWrite := io.Pipe()

	c := &Client{
		stdin:           clientStdin,
		stdout:          bufio.NewReader(clientStdout),
		pending:         make(map[int64]chan json.RawMessage),
		rootURI:         "file:///test-root",
		ready:           true,
		diagnosticsChan: make(chan []Diagnostic, 10),
		stopChan:        make(chan struct{}),
	}
	// serverID 从 0 开始，Add(1) 后第一个请求 id=1
	c.serverID.Store(0)

	// 启动 readLoop（与生产代码一致）
	c.wg.Add(1)
	go c.readLoop()

	// 启动 MockServer.Serve
	go server.Serve(serverRead, serverWrite)

	cleanup := func() {
		// 1. 关闭 stopChan 让 readLoop defer 跳过重复清理 pending
		select {
		case <-c.stopChan:
		default:
			close(c.stopChan)
		}
		// 2. 关闭 clientStdin 触发 server EOF
		clientStdin.Close()
		// 3. 关闭 serverRead/serverWrite 让 MockServer.Serve 退出
		serverRead.Close()
		serverWrite.Close()
		// 4. 关闭 clientStdout 让 Client.stdout EOF 让 readLoop 退出
		clientStdout.Close()
		// 5. 等 readLoop 退出（带超时防止卡死）
		waitDone := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
		// 6. 关闭 MockServer
		server.Close()
		// 7. 清理 pending
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}
	return c, server, cleanup
}

// === InlayHints 测试 ===

// TestInlayHints_Success 测试正常获取 inlay hints。
func TestInlayHints_Success(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	// 注册 inlayHint handler 返回 2 个 hints
	server.RegisterHandler("textDocument/inlayHint", func(params json.RawMessage) (interface{}, error) {
		return []map[string]interface{}{
			{
				"position":     map[string]interface{}{"line": 0, "character": 5},
				"label":        ": int",
				"kind":         1, // Type
				"paddingRight": true,
			},
			{
				"position": map[string]interface{}{"line": 1, "character": 8},
				"label":    "x:",
				"kind":     2, // Parameter
			},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	hints, err := c.InlayHints(ctx, "/test/file.go")
	if err != nil {
		t.Fatalf("InlayHints failed: %v", err)
	}
	if len(hints) != 2 {
		t.Fatalf("hints count = %d, want 2", len(hints))
	}
	if hints[0].Label != ": int" {
		t.Errorf("hints[0].Label = %q, want ': int'", hints[0].Label)
	}
	if hints[0].Kind != 1 {
		t.Errorf("hints[0].Kind = %d, want 1 (Type)", hints[0].Kind)
	}
	if hints[0].PaddingRight != true {
		t.Errorf("hints[0].PaddingRight = %v, want true", hints[0].PaddingRight)
	}
	if hints[1].Position.Line != 1 || hints[1].Position.Character != 8 {
		t.Errorf("hints[1].Position = %+v, want {1, 8}", hints[1].Position)
	}
}

// TestInlayHints_EmptyResult 测试空结果（server 返回空数组）。
func TestInlayHints_EmptyResult(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	server.RegisterHandler("textDocument/inlayHint", func(params json.RawMessage) (interface{}, error) {
		return []interface{}{}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	hints, err := c.InlayHints(ctx, "/test/file.go")
	if err != nil {
		t.Fatalf("InlayHints failed: %v", err)
	}
	if len(hints) != 0 {
		t.Errorf("hints count = %d, want 0", len(hints))
	}
}

// TestInlayHints_ServerNotSupported 测试 server 不支持 inlayHint（method not found error）。
// 降级策略：返回空切片 + nil error（不报错）。
func TestInlayHints_ServerNotSupported(t *testing.T) {
	c, _, cleanup := newClientWithMock(t)
	defer cleanup()

	// 不注册 inlayHint handler（MockServer 返回 method not found）

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	hints, err := c.InlayHints(ctx, "/test/file.go")
	if err != nil {
		t.Errorf("InlayHints should degrade silently, got error: %v", err)
	}
	if hints != nil && len(hints) != 0 {
		t.Errorf("hints should be empty on method not found, got %d items", len(hints))
	}
}

// TestInlayHints_ClientNotReady 测试 Client 未就绪时返回错误。
func TestInlayHints_ClientNotReady(t *testing.T) {
	c := &Client{ready: false}
	_, err := c.InlayHints(context.Background(), "/test/file.go")
	if err == nil {
		t.Error("InlayHints on not-ready client should return error")
	}
}

// TestInlayHints_ContextTimeout 测试 context 超时返回错误。
func TestInlayHints_ContextTimeout(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	// 注册慢 handler（200ms）
	server.RegisterHandler("textDocument/inlayHint", func(params json.RawMessage) (interface{}, error) {
		time.Sleep(200 * time.Millisecond)
		return []interface{}{}, nil
	})

	// 50ms 超时
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.InlayHints(ctx, "/test/file.go")
	if err == nil {
		t.Error("InlayHints should return error on context timeout")
	}
}

// === SemanticTokensFull 测试 ===

// TestSemanticTokensFull_Success 测试正常获取 semantic tokens。
func TestSemanticTokensFull_Success(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	server.RegisterHandler("textDocument/semanticTokens/full", func(params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{
			"data": []int{0, 0, 5, 1, 0, 0, 6, 4, 2, 0},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tokens, err := c.SemanticTokensFull(ctx, "/test/file.go")
	if err != nil {
		t.Fatalf("SemanticTokensFull failed: %v", err)
	}
	if tokens == nil {
		t.Fatal("tokens is nil")
	}
	if len(tokens.Data) != 10 {
		t.Errorf("tokens.Data length = %d, want 10", len(tokens.Data))
	}
	if tokens.Data[0] != 0 || tokens.Data[2] != 5 {
		t.Errorf("tokens.Data[0:3] = %v, want [0, 0, 5]", tokens.Data[:3])
	}
}

// TestSemanticTokensFull_ServerNotSupported 测试 server 不支持 semanticTokens（降级返回 nil + nil）。
func TestSemanticTokensFull_ServerNotSupported(t *testing.T) {
	c, _, cleanup := newClientWithMock(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tokens, err := c.SemanticTokensFull(ctx, "/test/file.go")
	if err != nil {
		t.Errorf("SemanticTokensFull should degrade silently, got error: %v", err)
	}
	if tokens != nil {
		t.Errorf("tokens should be nil on method not found, got %+v", tokens)
	}
}

// TestSemanticTokensFull_EmptyResult 测试 server 返回空 data。
func TestSemanticTokensFull_EmptyResult(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	server.RegisterHandler("textDocument/semanticTokens/full", func(params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"data": []int{}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tokens, err := c.SemanticTokensFull(ctx, "/test/file.go")
	if err != nil {
		t.Fatalf("SemanticTokensFull failed: %v", err)
	}
	if tokens == nil {
		t.Fatal("tokens is nil")
	}
	if len(tokens.Data) != 0 {
		t.Errorf("tokens.Data length = %d, want 0", len(tokens.Data))
	}
}

// TestSemanticTokensFull_ClientNotReady 测试 Client 未就绪时返回错误。
func TestSemanticTokensFull_ClientNotReady(t *testing.T) {
	c := &Client{ready: false}
	_, err := c.SemanticTokensFull(context.Background(), "/test/file.go")
	if err == nil {
		t.Error("SemanticTokensFull on not-ready client should return error")
	}
}

// === CodeLenses 测试 ===

// TestCodeLenses_Success 测试正常获取 code lenses。
func TestCodeLenses_Success(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	server.RegisterHandler("textDocument/codeLens", func(params json.RawMessage) (interface{}, error) {
		return []map[string]interface{}{
			{
				"range": map[string]interface{}{
					"start": map[string]interface{}{"line": 0, "character": 0},
					"end":   map[string]interface{}{"line": 0, "character": 10},
				},
				"command": map[string]interface{}{
					"title":   "Run",
					"command": "go.test",
				},
			},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	lenses, err := c.CodeLenses(ctx, "/test/file.go")
	if err != nil {
		t.Fatalf("CodeLenses failed: %v", err)
	}
	if len(lenses) != 1 {
		t.Fatalf("lenses count = %d, want 1", len(lenses))
	}
	if lenses[0].Range.Start.Line != 0 || lenses[0].Range.End.Character != 10 {
		t.Errorf("lenses[0].Range = %+v, want start={0,0} end={0,10}", lenses[0].Range)
	}
	if lenses[0].Command == nil {
		t.Fatal("lenses[0].Command is nil")
	}
	if lenses[0].Command.Title != "Run" {
		t.Errorf("lenses[0].Command.Title = %q, want 'Run'", lenses[0].Command.Title)
	}
	if lenses[0].Command.Command != "go.test" {
		t.Errorf("lenses[0].Command.Command = %q, want 'go.test'", lenses[0].Command.Command)
	}
}

// TestCodeLenses_EmptyResult 测试空结果。
func TestCodeLenses_EmptyResult(t *testing.T) {
	c, server, cleanup := newClientWithMock(t)
	defer cleanup()

	server.RegisterHandler("textDocument/codeLens", func(params json.RawMessage) (interface{}, error) {
		return []interface{}{}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	lenses, err := c.CodeLenses(ctx, "/test/file.go")
	if err != nil {
		t.Fatalf("CodeLenses failed: %v", err)
	}
	if len(lenses) != 0 {
		t.Errorf("lenses count = %d, want 0", len(lenses))
	}
}

// TestCodeLenses_ServerNotSupported 测试 server 不支持 codeLens（降级返回空切片 + nil error）。
func TestCodeLenses_ServerNotSupported(t *testing.T) {
	c, _, cleanup := newClientWithMock(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	lenses, err := c.CodeLenses(ctx, "/test/file.go")
	if err != nil {
		t.Errorf("CodeLenses should degrade silently, got error: %v", err)
	}
	if lenses != nil && len(lenses) != 0 {
		t.Errorf("lenses should be empty on method not found, got %d items", len(lenses))
	}
}

// TestCodeLenses_ClientNotReady 测试 Client 未就绪时返回错误。
func TestCodeLenses_ClientNotReady(t *testing.T) {
	c := &Client{ready: false}
	_, err := c.CodeLenses(context.Background(), "/test/file.go")
	if err == nil {
		t.Error("CodeLenses on not-ready client should return error")
	}
}

// === SetWorkspaceFolders 测试 ===

// TestSetWorkspaceFolders_SetsField 测试 SetWorkspaceFolders 设置 Client.workspaceFolders 字段。
func TestSetWorkspaceFolders_SetsField(t *testing.T) {
	c, _, cleanup := newClientWithMock(t)
	defer cleanup()

	folders := []WorkspaceFolder{
		{URI: "file:///root1", Name: "root1"},
		{URI: "file:///root2", Name: "root2"},
	}
	c.SetWorkspaceFolders(folders)

	// 验证字段被设置（直接读字段，因为是同包测试）
	if len(c.workspaceFolders) != 2 {
		t.Errorf("workspaceFolders count = %d, want 2", len(c.workspaceFolders))
	}
	if c.workspaceFolders[0].URI != "file:///root1" {
		t.Errorf("workspaceFolders[0].URI = %q, want 'file:///root1'", c.workspaceFolders[0].URI)
	}
}

// TestSetWorkspaceFolders_Empty 测试 SetWorkspaceFolders 传空切片清空字段（降级到单根模式）。
func TestSetWorkspaceFolders_Empty(t *testing.T) {
	c, _, cleanup := newClientWithMock(t)
	defer cleanup()

	// 先设置
	c.SetWorkspaceFolders([]WorkspaceFolder{{URI: "file:///root1", Name: "root1"}})
	// 再清空
	c.SetWorkspaceFolders([]WorkspaceFolder{})

	if len(c.workspaceFolders) != 0 {
		t.Errorf("workspaceFolders count = %d, want 0 after empty set", len(c.workspaceFolders))
	}
}

// TestSetWorkspaceFolders_Nil 测试 SetWorkspaceFolders 传 nil 清空字段。
func TestSetWorkspaceFolders_Nil(t *testing.T) {
	c, _, cleanup := newClientWithMock(t)
	defer cleanup()

	c.SetWorkspaceFolders([]WorkspaceFolder{{URI: "file:///root1", Name: "root1"}})
	c.SetWorkspaceFolders(nil)

	if len(c.workspaceFolders) != 0 {
		t.Errorf("workspaceFolders count = %d, want 0 after nil set", len(c.workspaceFolders))
	}
}

// === 诊断渲染辅助测试（diagnostics.go）===

// TestSeverityToLabel 测试 Severity 数字转标签。
func TestSeverityToLabel(t *testing.T) {
	tests := []struct {
		severity int
		want     string
	}{
		{SeverityError, "Error"},
		{SeverityWarning, "Warning"},
		{SeverityInformation, "Information"},
		{SeverityHint, "Hint"},
		{0, "Unknown"},
		{5, "Unknown"},
		{-1, "Unknown"},
	}
	for _, tt := range tests {
		got := SeverityToLabel(tt.severity)
		if got != tt.want {
			t.Errorf("SeverityToLabel(%d) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

// TestSeverityToIcon 测试 Severity 数字转图标。
func TestSeverityToIcon(t *testing.T) {
	tests := []struct {
		severity int
		want     string
	}{
		{SeverityError, "E"},
		{SeverityWarning, "W"},
		{SeverityInformation, "I"},
		{SeverityHint, "H"},
		{0, "?"},
		{99, "?"},
	}
	for _, tt := range tests {
		got := SeverityToIcon(tt.severity)
		if got != tt.want {
			t.Errorf("SeverityToIcon(%d) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

// TestFormatDiagnostic 测试单条诊断格式化。
func TestFormatDiagnostic(t *testing.T) {
	tests := []struct {
		name     string
		d        Diagnostic
		filePath string
		want     string
	}{
		{
			name: "Error with source",
			d: Diagnostic{
				Range:    Range{Start: Position{Line: 0, Character: 5}, End: Position{Line: 0, Character: 10}},
				Severity: SeverityError,
				Message:  "syntax error",
				Source:   "gopls",
			},
			filePath: "/test/file.go",
			want:     "E [gopls] /test/file.go:1:6: syntax error",
		},
		{
			name: "Warning without source",
			d: Diagnostic{
				Range:    Range{Start: Position{Line: 9, Character: 0}, End: Position{Line: 9, Character: 5}},
				Severity: SeverityWarning,
				Message:  "unused variable",
			},
			filePath: "/test/file.go",
			want:     "W /test/file.go:10:1: unused variable",
		},
		{
			name: "Hint without filePath",
			d: Diagnostic{
				Range:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 1}},
				Severity: SeverityHint,
				Message:  "can be simplified",
			},
			filePath: "",
			want:     "H 1:1: can be simplified",
		},
		{
			name: "Unknown severity",
			d: Diagnostic{
				Range:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 1}},
				Severity: 99,
				Message:  "unknown",
			},
			filePath: "/test/file.go",
			want:     "? /test/file.go:1:1: unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatDiagnostic(tt.d, tt.filePath)
			if got != tt.want {
				t.Errorf("FormatDiagnostic() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatDiagnostic_SanitizesMessage 测试 FormatDiagnostic 消毒消息（过滤控制字符 + 截断超长）。
func TestFormatDiagnostic_SanitizesMessage(t *testing.T) {
	d := Diagnostic{
		Range:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 1}},
		Severity: SeverityError,
		Message:  "line1\nline2\ttab\x00null", // \x00 应被替换为空格
		Source:   "",
	}
	got := FormatDiagnostic(d, "/test/file.go")
	if !strings.Contains(got, "line1") {
		t.Errorf("expected message to contain 'line1', got %q", got)
	}
	if strings.Contains(got, "\x00") {
		t.Errorf("null byte should be sanitized, got %q", got)
	}
}

// TestFormatDiagnostics 测试批量诊断格式化。
func TestFormatDiagnostics(t *testing.T) {
	diagnostics := []Diagnostic{
		{
			Range:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}},
			Severity: SeverityError,
			Message:  "error1",
			Source:   "gopls",
		},
		{
			Range:    Range{Start: Position{Line: 1, Character: 2}, End: Position{Line: 1, Character: 8}},
			Severity: SeverityWarning,
			Message:  "warning1",
		},
	}
	got := FormatDiagnostics(diagnostics, "/test/file.go")
	if !strings.Contains(got, "error1") {
		t.Errorf("expected output to contain 'error1', got %q", got)
	}
	if !strings.Contains(got, "warning1") {
		t.Errorf("expected output to contain 'warning1', got %q", got)
	}
	// 验证两条诊断以 \n 分隔
	if !strings.Contains(got, "\n") {
		t.Errorf("expected output to contain newline separator, got %q", got)
	}
}

// TestFormatDiagnostics_Empty 测试空切片返回空字符串。
func TestFormatDiagnostics_Empty(t *testing.T) {
	got := FormatDiagnostics(nil, "/test/file.go")
	if got != "" {
		t.Errorf("FormatDiagnostics(nil) = %q, want empty", got)
	}
	got = FormatDiagnostics([]Diagnostic{}, "/test/file.go")
	if got != "" {
		t.Errorf("FormatDiagnostics([]) = %q, want empty", got)
	}
}

// TestSanitizeString 测试字符串消毒。
func TestSanitizeString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty", "", 10, ""},
		{"normal", "hello", 10, "hello"},
		{"control chars", "a\x00b\x01c", 10, "a b c"},
		{"preserve tab/newline", "a\tb\nc", 10, "a\tb\nc"},
		{"truncate", "0123456789ABCDEF", 10, "0123456789..."},
		{"no truncate when maxLen=0", "0123456789ABCDEF", 0, "0123456789ABCDEF"},
		{"negative maxLen treated as 0", "0123456789ABCDEF", -1, "0123456789ABCDEF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("SanitizeString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// === 集成测试：Initialize 时 workspaceFolders 注入 ===

// TestInitialize_SendsWorkspaceFolders 测试 initialize 请求包含 workspaceFolders 字段。
func TestInitialize_SendsWorkspaceFolders(t *testing.T) {
	// 使用 newClientWithMock 但手动调用 initialize 验证 params
	server := NewMockServer()
	serverRead, clientStdin := io.Pipe()
	clientStdout, serverWrite := io.Pipe()

	c := &Client{
		stdin:           clientStdin,
		stdout:          bufio.NewReader(clientStdout),
		pending:         make(map[int64]chan json.RawMessage),
		rootURI:         "file:///test-root",
		ready:           false, // initialize 前未就绪
		diagnosticsChan: make(chan []Diagnostic, 10),
		stopChan:        make(chan struct{}),
	}
	c.serverID.Store(0)
	c.wg.Add(1)
	go c.readLoop()
	go server.Serve(serverRead, serverWrite)

	defer func() {
		close(c.stopChan)
		clientStdin.Close()
		serverRead.Close()
		serverWrite.Close()
		clientStdout.Close()
		c.wg.Wait()
		server.Close()
	}()

	// 设置 workspaceFolders
	c.SetWorkspaceFolders([]WorkspaceFolder{
		{URI: "file:///root1", Name: "root1"},
		{URI: "file:///root2", Name: "root2"},
	})

	// 调用 initialize
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.initialize(ctx, c.rootURI); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	// 验证 MockServer 收到的 initialize params 包含 workspaceFolders
	cached := server.GetInitializeParams()
	if cached == nil {
		t.Fatal("GetInitializeParams returned nil")
	}
	var params map[string]interface{}
	if err := json.Unmarshal(cached, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	folders, ok := params["workspaceFolders"].([]interface{})
	if !ok {
		t.Fatal("workspaceFolders not in initialize params")
	}
	if len(folders) != 2 {
		t.Errorf("workspaceFolders count = %d, want 2", len(folders))
	}
}

// TestInitialize_NoWorkspaceFolders 测试未设置 workspaceFolders 时不发送该字段（向后兼容）。
func TestInitialize_NoWorkspaceFolders(t *testing.T) {
	server := NewMockServer()
	serverRead, clientStdin := io.Pipe()
	clientStdout, serverWrite := io.Pipe()

	c := &Client{
		stdin:           clientStdin,
		stdout:          bufio.NewReader(clientStdout),
		pending:         make(map[int64]chan json.RawMessage),
		rootURI:         "file:///test-root",
		ready:           false,
		diagnosticsChan: make(chan []Diagnostic, 10),
		stopChan:        make(chan struct{}),
	}
	c.serverID.Store(0)
	c.wg.Add(1)
	go c.readLoop()
	go server.Serve(serverRead, serverWrite)

	defer func() {
		close(c.stopChan)
		clientStdin.Close()
		serverRead.Close()
		serverWrite.Close()
		clientStdout.Close()
		c.wg.Wait()
		server.Close()
	}()

	// 不设置 workspaceFolders（默认 nil）
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.initialize(ctx, c.rootURI); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	// 验证 initialize params 不包含 workspaceFolders 字段
	cached := server.GetInitializeParams()
	if cached == nil {
		t.Fatal("GetInitializeParams returned nil")
	}
	var params map[string]interface{}
	if err := json.Unmarshal(cached, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if _, ok := params["workspaceFolders"]; ok {
		t.Error("workspaceFolders should not be sent when not set")
	}
}

// === 辅助 ===
// （使用标准库 strings.Contains 替代自定义实现，避免重复造轮子）
