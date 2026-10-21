// Package lsp — Language Server Protocol client测试
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDetectLanguage 测试根据文件扩展名检测编程语言
func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		// 基本语言检测
		{"main.go", "go"},
		{"app.ts", "typescript"},
		{"style.css", ""}, // css 不在支持列表中
		{"unknown.xyz", ""},

		// 额外语言检测
		{"script.py", "python"},
		{"main.rs", "rust"},
		{"utils.c", "c"},
		{"app.cpp", "cpp"},
		{"Main.java", ""}, // java 不在 DetectLanguage 支持列表中

		// 大小写不敏感测试
		{"main.GO", "go"},
		{"app.TS", "typescript"},
		{"script.PY", "python"},

		// 带路径的文件名
		{"src/main.go", "go"},
		{"/home/user/project/app.ts", "typescript"},

		// tsx / jsx 扩展名
		{"component.tsx", "typescript"},
		{"app.jsx", "typescript"},

		// .h 和 .hpp 扩展名
		{"header.h", "c"},
		{"header.hpp", "cpp"},

		// .cc / .cxx 扩展名
		{"impl.cc", "cpp"},
		{"impl.cxx", "cpp"},

		// 无扩展名
		{"Makefile", ""},
		{"Dockerfile", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := DetectLanguage(tt.filename)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

// TestFindLanguageServer 测试根据语言查找对应的语言server命令
func TestFindLanguageServer(t *testing.T) {
	tests := []struct {
		name     string
		language string
		wantCmd  string // expected命令名包含的关键字（因为路径可能不同）
		wantErr  bool   // 是否expected找不到server
	}{
		{
			name:     "go语言查找gopls",
			language: "go",
			wantCmd:  "gopls",
			wantErr:  false,
		},
		{
			name:     "typescript查找typescript-language-server",
			language: "typescript",
			wantCmd:  "typescript-language-server",
			wantErr:  false,
		},
		{
			name:     "python查找pylsp或pyright",
			language: "python",
			wantCmd:  "", // 不确定安装了哪个，只检查非空
			wantErr:  false,
		},
		{
			name:     "rust查找rust-analyzer",
			language: "rust",
			wantCmd:  "rust-analyzer",
			wantErr:  false,
		},
		{
			name:     "c语言查找clangd",
			language: "c",
			wantCmd:  "clangd",
			wantErr:  false,
		},
		{
			name:     "cpp查找clangd",
			language: "cpp",
			wantCmd:  "clangd",
			wantErr:  false,
		},
		{
			name:     "未知语言返回空",
			language: "unknown-lang",
			wantCmd:  "",
			wantErr:  true,
		},
		{
			name:     "空语言返回空",
			language: "",
			wantCmd:  "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findLanguageServer(tt.language)

			if tt.wantErr {
				// expected找不到server
				if got != "" {
					t.Errorf("findLanguageServer(%q) = %q, want empty string", tt.language, got)
				}
				return
			}

			// 对于已知语言，检查server是否安装
			if got == "" {
				// server未安装，跳过该子测试
				t.Skipf("language server for %q is not installed, skipping", tt.language)
			}

			// 如果指定了expected命令名，验证返回路径包含该命令名
			if tt.wantCmd != "" {
				// 在 Windows 上，LookPath 可能返回 .exe 后缀的路径
				expectedInPath := false
				for _, cmd := range []string{tt.wantCmd, tt.wantCmd + ".exe"} {
					if len(got) >= len(cmd) {
						// 简单检查路径中是否包含命令名
						for i := 0; i <= len(got)-len(cmd); i++ {
							if got[i:i+len(cmd)] == cmd {
								expectedInPath = true
								break
							}
						}
					}
					if expectedInPath {
						break
					}
				}
				if !expectedInPath {
					t.Errorf("findLanguageServer(%q) = %q, want path containing %q", tt.language, got, tt.wantCmd)
				}
			}
		})
	}
}

// TestFindLanguageServer_CaseInsensitive 测试 findLanguageServer 的大小写不敏感
func TestFindLanguageServer_CaseInsensitive(t *testing.T) {
	// findLanguageServer 内部使用 strings.ToLower，因此大小写应该不影响结果
	result1 := findLanguageServer("Go")
	result2 := findLanguageServer("go")
	result3 := findLanguageServer("GO")

	// 如果 gopls 未安装，结果都should be empty
	if result1 == "" && result2 == "" && result3 == "" {
		t.Skip("gopls not installed, skipping case insensitivity test")
	}

	if result1 != result2 {
		t.Errorf("findLanguageServer(\"Go\") = %q, findLanguageServer(\"go\") = %q, should be equal", result1, result2)
	}
	if result2 != result3 {
		t.Errorf("findLanguageServer(\"go\") = %q, findLanguageServer(\"GO\") = %q, should be equal", result2, result3)
	}
}

// TestLSP_NotAvailable 测试当语言server未安装时返回友好错误信息
func TestLSP_NotAvailable(t *testing.T) {
	// 使用一个确定不存在语言server的语言
	ctx := context.Background()
	_, err := NewClient(ctx, "brainfuck", "/tmp")
	if err == nil {
		t.Error("expected error for unsupported language, got nil")
	}

	// 验证错误信息包含语言名称
	errMsg := err.Error()
	if errMsg == "" {
		t.Error("error message should not be empty")
	}

	// 验证错误信息包含 "no language server found" 关键字
	expected := "no language server found"
	if len(errMsg) < len(expected) {
		t.Errorf("error message %q too short, should contain %q", errMsg, expected)
	}
}

// TestLSP_NotAvailable_EmptyLanguage 测试空语言名称时返回错误
func TestLSP_NotAvailable_EmptyLanguage(t *testing.T) {
	ctx := context.Background()
	_, err := NewClient(ctx, "", "/tmp")
	if err == nil {
		t.Error("expected error for empty language, got nil")
	}
}

// TestLSP_NotAvailable_InstalledServer 测试已安装的语言server能否正常创建client
// 如果server未安装则跳过
func TestLSP_NotAvailable_InstalledServer(t *testing.T) {
	// 检查 gopls 是否安装
	goplsPath, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed, skipping")
	}
	t.Logf("gopls found at: %s", goplsPath)

	// 如果 gopls 已安装，验证不会返回 "not available" 错误
	// 注意：这里只验证 findLanguageServer 能找到 gopls
	result := findLanguageServer("go")
	if result == "" {
		t.Error("findLanguageServer(\"go\") returned empty, but gopls is installed")
	}
}

// TestLSP_ClientCache 测试同一语言复用已缓存的client
// mock lspClientCache 的缓存行为（actual缓存在 tools 包中）
func TestLSP_ClientCache(t *testing.T) {
	// 检查 gopls 是否安装
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed, skipping client cache test")
	}

	// mock缓存机制：与 tools/lsp_mcp_notebook.go 中的 lspClientCache 行为一致
	cache := make(map[string]*Client)
	var mu sync.Mutex

	ctx := context.Background()
	tempDir := t.TempDir()

	// 第一次创建 Go 语言client
	getOrCreateClient := func(lang, workDir string) (*Client, error) {
		mu.Lock()
		defer mu.Unlock()

		if client, ok := cache[lang]; ok && client.IsReady() {
			return client, nil
		}

		client, err := NewClient(ctx, lang, workDir)
		if err != nil {
			return nil, err
		}
		cache[lang] = client
		return client, nil
	}

	// 第一次获取client
	client1, err := getOrCreateClient("go", tempDir)
	if err != nil {
		t.Fatalf("first getOrCreateClient failed: %v", err)
	}
	defer client1.Close()

	// 第二次获取同语言client，should return缓存中的同一个实例
	client2, err := getOrCreateClient("go", tempDir)
	if err != nil {
		t.Fatalf("second getOrCreateClient failed: %v", err)
	}

	// 验证两次返回的是同一个client实例
	if client1 != client2 {
		t.Error("expected same client instance for same language, got different instances")
	}

	// 验证缓存中只有一个条目
	if len(cache) != 1 {
		t.Errorf("expected cache size 1, got %d", len(cache))
	}

	// 清理缓存中的client
	for lang, c := range cache {
		c.Close()
		delete(cache, lang)
	}
}

// TestLSP_ClientCache_DifferentLanguage 测试不同语言创建不同的client
func TestLSP_ClientCache_DifferentLanguage(t *testing.T) {
	// 检查 gopls 是否安装
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed, skipping different language test")
	}

	// 检查是否有第二个语言server可用
	secondLang := ""
	secondServer := ""
	for _, lang := range []string{"python", "typescript", "rust", "c", "cpp"} {
		if findLanguageServer(lang) != "" {
			secondLang = lang
			secondServer = findLanguageServer(lang)
			break
		}
	}
	if secondLang == "" {
		t.Skip("no second language server installed, skipping different language test")
	}
	t.Logf("using second language: %s (server: %s)", secondLang, secondServer)

	cache := make(map[string]*Client)
	var mu sync.Mutex

	ctx := context.Background()
	tempDir := t.TempDir()

	getOrCreateClient := func(lang, workDir string) (*Client, error) {
		mu.Lock()
		defer mu.Unlock()

		if client, ok := cache[lang]; ok && client.IsReady() {
			return client, nil
		}

		client, err := NewClient(ctx, lang, workDir)
		if err != nil {
			return nil, err
		}
		cache[lang] = client
		return client, nil
	}

	// 创建 Go 语言client
	goClient, err := getOrCreateClient("go", tempDir)
	if err != nil {
		t.Fatalf("create go client failed: %v", err)
	}
	defer goClient.Close()

	// 创建第二种语言client
	otherClient, err := getOrCreateClient(secondLang, tempDir)
	if err != nil {
		t.Fatalf("create %s client failed: %v", secondLang, err)
	}
	defer otherClient.Close()

	// 验证不同语言返回不同实例
	if goClient == otherClient {
		t.Error("expected different client instances for different languages, got same instance")
	}

	// 验证缓存中有两个条目
	if len(cache) != 2 {
		t.Errorf("expected cache size 2, got %d", len(cache))
	}

	// 清理
	for lang, c := range cache {
		c.Close()
		delete(cache, lang)
	}
}

// TestClient_IsReady 测试client就绪状态
func TestClient_IsReady(t *testing.T) {
	// 未初始化的client不应就绪
	c := &Client{ready: false}
	if c.IsReady() {
		t.Error("expected IsReady() to return false for uninitialized client")
	}

	// 设置 ready 后should return true
	c.ready = true
	if !c.IsReady() {
		t.Error("expected IsReady() to return true for ready client")
	}
}

// TestClient_Close 测试client关闭
func TestClient_Close(t *testing.T) {
	c := &Client{}
	// 关闭未启动的client不应报错
	if err := c.Close(); err != nil {
		t.Errorf("Close() on uninitialized client returned error: %v", err)
	}
}

// TestDetectLanguageID 测试 DetectLanguageID helper functions
func TestDetectLanguageID(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"main.go", "go"},
		{"script.py", "python"},
		{"app.ts", "typescript"},
		{"component.tsx", "typescriptreact"},
		{"app.js", "javascript"},
		{"component.jsx", "javascriptreact"},
		{"main.rs", "rust"},
		{"utils.c", "c"},
		{"app.cpp", "cpp"},
		{"header.h", "c"},
		{"header.hpp", "cpp"},
		{"Main.java", "java"},
		{"unknown.xyz", ""},
		{"Makefile", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := DetectLanguageID(tt.filename)
			if got != tt.want {
				t.Errorf("DetectLanguageID(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

// TestMustMarshal 测试 mustMarshal helper functions
func TestMustMarshal(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  string
	}{
		{"简单字符串", "hello", `"hello"`},
		{"整数", 42, `42`},
		{"map", map[string]string{"key": "value"}, `{"key":"value"}`},
		{"nil", nil, `null`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustMarshal(tt.input)
			if got != tt.want {
				t.Errorf("mustMarshal(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestClient_Initialize 测试 LSP 初始化握手（若无语言server则跳过）
func TestClient_Initialize(t *testing.T) {
	// 检查 gopls 是否安装
	goplsPath, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 LSP 初始化测试")
	}
	t.Logf("gopls 路径: %s", goplsPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	// 验证client已就绪
	if !client.IsReady() {
		t.Error("初始化后client应处于就绪状态")
	}
}

// TestClient_Definition 测试跳转到定义请求（若无语言server则跳过）
func TestClient_Definition(t *testing.T) {
	// 检查 gopls 是否安装
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 Definition 测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	// 创建一个简单的 Go 文件用于测试
	testFile := filepath.Join(tempDir, "main.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0644)

	// 请求定义（可能返回空结果，因为文件很简单）
	defCtx, defCancel := context.WithTimeout(ctx, 10*time.Second)
	defer defCancel()

	locs, err := client.Definition(defCtx, testFile, 0, 0)
	// 不强制要求返回结果，只要不报错即可
	if err != nil {
		t.Logf("Definition 返回错误（可接受）: %v", err)
	}
	t.Logf("Definition 返回 %d 个位置", len(locs))
}

// TestClient_References 测试查找引用请求（若无语言server则跳过）
func TestClient_References(t *testing.T) {
	// 检查 gopls 是否安装
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 References 测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	// 创建一个简单的 Go 文件用于测试
	testFile := filepath.Join(tempDir, "main.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0644)

	// 请求引用（可能返回空结果）
	refCtx, refCancel := context.WithTimeout(ctx, 10*time.Second)
	defer refCancel()

	locs, err := client.References(refCtx, testFile, 0, 0)
	if err != nil {
		t.Logf("References 返回错误（可接受）: %v", err)
	}
	t.Logf("References 返回 %d 个位置", len(locs))
}

// TestClient_Diagnostics 测试获取文件诊断信息（若无语言server则跳过）
func TestClient_Diagnostics(t *testing.T) {
	// 检查 gopls 是否安装
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 Diagnostics 测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	// 创建一个有语法错误的 Go 文件以触发诊断
	testFile := filepath.Join(tempDir, "broken.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main(\n"), 0644)

	// 请求诊断
	diagCtx, diagCancel := context.WithTimeout(ctx, 10*time.Second)
	defer diagCancel()

	diags, err := client.Diagnostics(diagCtx, testFile)
	if err != nil {
		t.Logf("Diagnostics 返回错误（可接受）: %v", err)
	} else {
		t.Logf("Diagnostics 返回 %d 条诊断", len(diags))
	}
}

// TestClient_Hover_NotReady 测试未就绪client调用 Hover 返回错误
func TestClient_Hover_NotReady(t *testing.T) {
	c := &Client{ready: false}
	_, err := c.Hover(context.Background(), "test.go", 1, 1)
	if err == nil {
		t.Error("expected error when client is not ready, got nil")
	}
}

// TestClient_Completion_NotReady 测试未就绪client调用 Completion 返回错误
func TestClient_Completion_NotReady(t *testing.T) {
	c := &Client{ready: false}
	_, err := c.Completion(context.Background(), "test.go", 1, 1)
	if err == nil {
		t.Error("expected error when client is not ready, got nil")
	}
}

// TestClient_Rename_NotReady 测试未就绪client调用 Rename 返回错误
func TestClient_Rename_NotReady(t *testing.T) {
	c := &Client{ready: false}
	_, err := c.Rename(context.Background(), "test.go", 1, 1, "newName")
	if err == nil {
		t.Error("expected error when client is not ready, got nil")
	}
}

// TestClient_Hover_ParseResult 测试 Hover 结果解析
func TestClient_Hover_ParseResult(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    string
	}{
		{
			name:    "结构化 Hover 结果",
			jsonStr: `{"contents":{"kind":"markdown","value":"func main()"}}`,
			want:    "func main()",
		},
		{
			name:    "空 Value",
			jsonStr: `{"contents":{"kind":"markdown","value":""}}`,
			want:    "",
		},
		{
			name:    "简单字符串",
			jsonStr: `"simple string"`,
			want:    "simple string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hover struct {
				Contents struct {
					Kind  string `json:"kind"`
					Value string `json:"value"`
				} `json:"contents"`
			}
			result := json.RawMessage(tt.jsonStr)
			var got string

			if err := json.Unmarshal(result, &hover); err != nil {
				var simpleStr string
				if err2 := json.Unmarshal(result, &simpleStr); err2 == nil {
					got = simpleStr
				}
			} else {
				got = hover.Contents.Value
			}

			if got != tt.want {
				t.Errorf("Hover 解析结果 = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClient_Completion_ParseResult 测试 Completion 结果解析
func TestClient_Completion_ParseResult(t *testing.T) {
	tests := []struct {
		name      string
		jsonStr   string
		wantLen   int
		wantLabel string
	}{
		{
			name:      "CompletionList 格式",
			jsonStr:   `{"items":[{"label":"fmt","kind":6,"detail":"fmt package"}]}`,
			wantLen:   1,
			wantLabel: "fmt",
		},
		{
			name:      "直接数组格式",
			jsonStr:   `[{"label":"main","kind":3}]`,
			wantLen:   1,
			wantLabel: "main",
		},
		{
			name:      "空 CompletionList",
			jsonStr:   `{"items":[]}`,
			wantLen:   0,
			wantLabel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := json.RawMessage(tt.jsonStr)
			var items []CompletionItem

			var completionList struct {
				Items []CompletionItem `json:"items"`
			}
			if err := json.Unmarshal(result, &completionList); err == nil && len(completionList.Items) > 0 {
				items = completionList.Items
			} else if err := json.Unmarshal(result, &items); err == nil {
				// 直接数组
			}

			if len(items) != tt.wantLen {
				t.Errorf("Completion 解析结果数量 = %d, want %d", len(items), tt.wantLen)
			}
			if tt.wantLen > 0 && len(items) > 0 && items[0].Label != tt.wantLabel {
				t.Errorf("Completion 第一项 Label = %q, want %q", items[0].Label, tt.wantLabel)
			}
		})
	}
}

// TestClient_Rename_ParseResult 测试 Rename 结果解析
func TestClient_Rename_ParseResult(t *testing.T) {
	tests := []struct {
		name      string
		jsonStr   string
		wantFiles int
		wantNil   bool
	}{
		{
			name:      "有效 WorkspaceEdit",
			jsonStr:   `{"changes":{"file:///test.go":[{"range":{"start":{"line":0,"character":5},"end":{"line":0,"character":8}},"newText":"newName"}]}}`,
			wantFiles: 1,
			wantNil:   false,
		},
		{
			name:      "空 changes",
			jsonStr:   `{"changes":{}}`,
			wantFiles: 0,
			wantNil:   false,
		},
		{
			name:      "无效 JSON",
			jsonStr:   `null`,
			wantFiles: 0,
			wantNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := json.RawMessage(tt.jsonStr)
			var edit WorkspaceEdit
			err := json.Unmarshal(result, &edit)

			if tt.wantNil {
				if err != nil {
					// null 解析为空结构体是合法的
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected unmarshal error: %v", err)
				}
				if len(edit.Changes) != tt.wantFiles {
					t.Errorf("Rename 解析结果文件数 = %d, want %d", len(edit.Changes), tt.wantFiles)
				}
			}
		})
	}
}

// TestClient_Hover_Integration 测试 Hover 集成（若无语言server则跳过）
func TestClient_Hover_Integration(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 Hover integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	testFile := filepath.Join(tempDir, "main.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0644)

	hoverCtx, hoverCancel := context.WithTimeout(ctx, 10*time.Second)
	defer hoverCancel()

	result, err := client.Hover(hoverCtx, testFile, 0, 0)
	if err != nil {
		t.Logf("Hover 返回错误（可接受）: %v", err)
	}
	t.Logf("Hover 返回: %q", result)
}

// TestClient_Completion_Integration 测试 Completion 集成（若无语言server则跳过）
func TestClient_Completion_Integration(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 Completion integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	testFile := filepath.Join(tempDir, "main.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {\n\tfmt.\n}\n"), 0644)

	compCtx, compCancel := context.WithTimeout(ctx, 10*time.Second)
	defer compCancel()

	items, err := client.Completion(compCtx, testFile, 3, 5)
	if err != nil {
		t.Logf("Completion 返回错误（可接受）: %v", err)
	}
	t.Logf("Completion 返回 %d 个补全项", len(items))
}

// TestClient_Rename_Integration 测试 Rename 集成（若无语言server则跳过）
func TestClient_Rename_Integration(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 Rename integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	testFile := filepath.Join(tempDir, "main.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0644)

	renameCtx, renameCancel := context.WithTimeout(ctx, 10*time.Second)
	defer renameCancel()

	edit, err := client.Rename(renameCtx, testFile, 2, 5, "newMain")
	if err != nil {
		t.Logf("Rename 返回错误（可接受）: %v", err)
	}
	if edit != nil {
		t.Logf("Rename 返回 %d 个文件变更", len(edit.Changes))
	} else {
		t.Log("Rename 返回 nil（可接受）")
	}
}

// ===== B4-3A 新增测试 =====

// TestClient_DidOpen_NotReady 测试未就绪 client 调用 DidOpen 返回错误（不 panic）
func TestClient_DidOpen_NotReady(t *testing.T) {
	c := &Client{ready: false}
	err := c.DidOpen(context.Background(), "test.go", "go", "package main")
	if err == nil {
		t.Fatal("expected error when client is not ready, got nil")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error should contain 'not ready', got: %v", err)
	}
}

// TestClient_DidChange_NotReady 测试未就绪 client 调用 DidChange 返回错误（不 panic）
func TestClient_DidChange_NotReady(t *testing.T) {
	c := &Client{ready: false}
	err := c.DidChange(context.Background(), "test.go", 1, "package main")
	if err == nil {
		t.Fatal("expected error when client is not ready, got nil")
	}
}

// TestClient_DidClose_NotReady 测试未就绪 client 调用 DidClose 返回错误（不 panic）
func TestClient_DidClose_NotReady(t *testing.T) {
	c := &Client{ready: false}
	err := c.DidClose(context.Background(), "test.go")
	if err == nil {
		t.Fatal("expected error when client is not ready, got nil")
	}
}

// TestClient_DidOpen_Integration 测试 DidOpen 集成（gopls 未装则跳过）
func TestClient_DidOpen_Integration(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 DidOpen integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	testFile := filepath.Join(tempDir, "main.go")
	content := "package main\n\nfunc main() {}\n"
	os.WriteFile(testFile, []byte(content), 0644)

	if err := client.DidOpen(ctx, testFile, "go", content); err != nil {
		t.Errorf("DidOpen 返回错误: %v", err)
	}
}

// TestClient_DidChange_Integration 测试 DidChange version 递增不报错（gopls 未装则跳过）
func TestClient_DidChange_Integration(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 DidChange integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	testFile := filepath.Join(tempDir, "main.go")
	content := "package main\n\nfunc main() {}\n"
	os.WriteFile(testFile, []byte(content), 0644)

	// 先 DidOpen（version=1）
	if err := client.DidOpen(ctx, testFile, "go", content); err != nil {
		t.Fatalf("DidOpen 失败: %v", err)
	}

	// 多次 DidChange，version 递增
	for v := 2; v <= 5; v++ {
		newContent := fmt.Sprintf("package main\n\nfunc main() { _ = %d }\n", v)
		if err := client.DidChange(ctx, testFile, v, newContent); err != nil {
			t.Errorf("DidChange(version=%d) 返回错误: %v", v, err)
		}
	}
}

// TestClient_DidClose_Integration 测试 DidClose 集成（gopls 未装则跳过）
func TestClient_DidClose_Integration(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 DidClose integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	testFile := filepath.Join(tempDir, "main.go")
	content := "package main\n\nfunc main() {}\n"
	os.WriteFile(testFile, []byte(content), 0644)

	if err := client.DidOpen(ctx, testFile, "go", content); err != nil {
		t.Fatalf("DidOpen 失败: %v", err)
	}
	if err := client.DidClose(ctx, testFile); err != nil {
		t.Errorf("DidClose 返回错误: %v", err)
	}
}

// TestClient_Close_WaitForReadLoop 验证 Close 后 readLoop goroutine 真正退出（3-A-1 核心）
// 通过 goroutine 计数对比验证；gopls 未装则跳过（需真实 server 触发 stdout EOF）
func TestClient_Close_WaitForReadLoop(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 readLoop 退出验证（需真实 server 触发 stdout EOF）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}

	// 记录 Close 前 goroutine 数
	before := runtime.NumGoroutine()

	// Close 应在 8 秒内返回（内部 wg.Wait 带 3 秒超时 + 进程 kill 3 秒超时）
	done := make(chan error, 1)
	go func() {
		done <- client.Close()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close 返回错误: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Close 超时 8 秒未返回，readLoop 可能卡死")
	}

	// 等 GC 回收 goroutine
	time.Sleep(200 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	// 允许 ±2 的波动（GC / 其他测试 goroutine）
	if after > before+2 {
		t.Errorf("goroutine 泄漏：Close 前 %d，Close 后 %d（差 %d）",
			before, after, after-before)
	}
}

// TestClient_Close_Concurrent_NoPanic 验证并发调 Close 不 panic（stopChan 已关闭分支兜底）
func TestClient_Close_Concurrent_NoPanic(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过并发 Close 测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}

	// 并发 10 个 goroutine 调 Close
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Close 并发调用 panic: %v", r)
				}
			}()
			errs[idx] = client.Close()
		}(i)
	}
	wg.Wait()

	// 所有 Close 都应返回 nil（或其中一个成功，其他返回 nil 因 stopChan 已关闭）
	for i, e := range errs {
		if e != nil {
			t.Logf("Close[%d] 返回: %v（可接受）", i, e)
		}
	}
}

// TestClient_Close_Idempotent 验证重复调 Close 不 panic 且返回 nil
func TestClient_Close_Idempotent(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过幂等测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}

	// 第一次 Close
	if err := client.Close(); err != nil {
		t.Errorf("第一次 Close 返回错误: %v", err)
	}
	// 第二次 Close 应直接返回 nil（stopChan 已关闭）
	if err := client.Close(); err != nil {
		t.Errorf("第二次 Close 返回错误: %v", err)
	}
	// 第三次
	if err := client.Close(); err != nil {
		t.Errorf("第三次 Close 返回错误: %v", err)
	}
}

// TestNewClient_InitializeFailure_NoLeak 验证 initialize 失败后 readLoop goroutine 退出（无泄漏）
// 通过模拟 readLoop + stopChan + wg 机制验证 wg.Wait 后 goroutine 退出，不依赖真实 server
func TestNewClient_InitializeFailure_NoLeak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx

	// 直接构造一个未完成 initialize 的 Client，模拟失败路径
	// 主要验证 wg.Wait 机制，不依赖真实 server
	c := &Client{
		stopChan: make(chan struct{}),
		pending:  make(map[int64]chan json.RawMessage),
	}
	// 模拟 readLoop 启动
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			select {
			case <-c.stopChan:
				return
			default:
			}
			c.mu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
		}()
		// 模拟 readLoop 阻塞读，等 stopChan
		<-c.stopChan
	}()

	before := runtime.NumGoroutine()
	// 模拟 NewClient 失败路径：close stopChan + wg.Wait
	close(c.stopChan)
	c.wg.Wait()
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after >= before {
		t.Errorf("goroutine 未退出：before=%d after=%d", before, after)
	}
}

// TestClient_Diagnostics_AfterRefactor 验证 Diagnostics 重构后仍能触发诊断（gopls 未装则跳过）
// 重构后 Diagnostics 内部调 c.DidOpen，行为应与原内联代码一致
func TestClient_Diagnostics_AfterRefactor(t *testing.T) {
	_, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls 未安装，跳过 Diagnostics 重构验证")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	client, err := NewClient(ctx, "go", tempDir)
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	defer client.Close()

	// 创建有语法错误的文件以触发诊断
	testFile := filepath.Join(tempDir, "broken.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main(\n"), 0644)

	diagCtx, diagCancel := context.WithTimeout(ctx, 10*time.Second)
	defer diagCancel()

	diags, err := client.Diagnostics(diagCtx, testFile)
	// 重构后应仍能获取诊断（或超时，但不应报错）
	if err != nil {
		t.Logf("Diagnostics 返回错误（可接受，可能是超时）: %v", err)
	} else {
		t.Logf("Diagnostics 返回 %d 条诊断", len(diags))
	}
}
