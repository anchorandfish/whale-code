package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// ========== L3 集成测试：模拟下游模块的真实调用模式 ==========

// TestIntegration_LSPClient_FilePathToURI 模拟 lsp/client.go 的 FilePathToURI 调用模式
// lsp/client.go 在 Initialize、DidOpen、DidChange 等 13 处调用 FilePathToURI
func TestIntegration_LSPClient_FilePathToURI(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	// 模拟 LSP 客户端初始化场景（lsp/client.go:124）
	workspaceRoot := "/home/user/project"
	rootURI := FilePathToURI(workspaceRoot)
	if rootURI != "file:///home/user/project" {
		t.Errorf("LSP workspace root URI 不正确: %s", rootURI)
	}

	// 模拟 didOpen 通知（lsp/client.go:438）
	filePath := filepath.Join(workspaceRoot, "src", "main.go")
	fileURI := FilePathToURI(filePath)
	if fileURI != "file:///home/user/project/src/main.go" {
		t.Errorf("LSP file URI 不正确: %s", fileURI)
	}

	// 模拟中文路径（LSP 中文文件名场景）
	chinesePath := filepath.Join(workspaceRoot, "中文目录", "测试.go")
	chineseURI := FilePathToURI(chinesePath)
	back, err := URIToFilePath(chineseURI)
	if err != nil {
		t.Fatalf("中文路径 URI 往返失败: %v", err)
	}
	if back != chinesePath {
		t.Errorf("LSP 中文路径往返不一致: 原始 %q → URI %q → 返回 %q", chinesePath, chineseURI, back)
	}
}

// TestIntegration_ToolsLSP_URIToFilePath 模拟 tools/lsp_mcp_notebook.go 的 URIToFilePath 调用模式
// tools/lsp_mcp_notebook.go 在 definition/hover/references 等 5 处调用 URIToFilePath
func TestIntegration_ToolsLSP_URIToFilePath(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	// 模拟 LSP 返回的 location URI
	cases := []struct {
		name     string
		uri      string
		expected string
	}{
		{
			name:     "普通路径",
			uri:      "file:///home/user/project/main.go",
			expected: "/home/user/project/main.go",
		},
		{
			name:     "中文路径",
			uri:      "file:///home/user/%E4%B8%AD%E6%96%87/%E6%B5%8B%E8%AF%95.go",
			expected: "/home/user/中文/测试.go",
		},
		{
			name:     "含空格路径",
			uri:      "file:///home/my%20project/main.go",
			expected: "/home/my project/main.go",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, err := URIToFilePath(c.uri)
			if err != nil {
				t.Fatalf("URIToFilePath 失败: %v", err)
			}
			if path != c.expected {
				t.Errorf("URIToFilePath(%q) = %q，期望 %q", c.uri, path, c.expected)
			}
		})
	}
}

// TestIntegration_Mention_IsPathAllowed 模拟 mention/mention.go 的 IsPathAllowed 调用模式
// mention.go:151 调用 platform.IsPathAllowed(absPath, []string{absWorkDir})
func TestIntegration_Mention_IsPathAllowed(t *testing.T) {
	workDir := t.TempDir()

	// 创建工作目录内文件
	innerFile := filepath.Join(workDir, "src", "main.go")
	os.MkdirAll(filepath.Dir(innerFile), 0755)
	os.WriteFile(innerFile, []byte("hello"), 0644)

	// 工作目录内文件应允许
	if !IsPathAllowed(innerFile, []string{workDir}) {
		t.Error("mention 场景：工作目录内文件应允许")
	}

	// 工作目录外文件应拒绝
	outsideFile := filepath.Join(t.TempDir(), "outside.go")
	os.WriteFile(outsideFile, []byte("hello"), 0644)
	if IsPathAllowed(outsideFile, []string{workDir}) {
		t.Error("mention 场景：工作目录外文件应拒绝")
	}
}

// TestIntegration_ConfigMagicDocs_IsPathAllowed 模拟 config/magic_docs_v2.go 的 IsPathAllowed 调用模式
// magic_docs_v2.go:206 调用 platform.IsPathAllowed(path, roots)
func TestIntegration_ConfigMagicDocs_IsPathAllowed(t *testing.T) {
	projectRoot := t.TempDir()
	docsDir := filepath.Join(projectRoot, "docs")
	os.MkdirAll(docsDir, 0755)

	docFile := filepath.Join(docsDir, "readme.md")
	os.WriteFile(docFile, []byte("# Hello"), 0644)

	// 配置中的根目录列表
	roots := []string{projectRoot}

	if !IsPathAllowed(docFile, roots) {
		t.Error("config 场景：项目根目录内文档文件应允许")
	}

	// 试图访问根目录外
	etcFile := "/etc/passwd"
	if IsPathAllowed(etcFile, roots) {
		t.Error("config 场景：根目录外系统文件应拒绝")
	}
}

// TestIntegration_Keybindings_IsPathAllowed 模拟 keybindings/loader.go 的 IsPathAllowed 调用模式
// loader.go:73 调用 platform.IsPathAllowed(path, roots)
func TestIntegration_Keybindings_IsPathAllowed(t *testing.T) {
	configDir := t.TempDir()
	keybindingFile := filepath.Join(configDir, "keybindings.json")
	os.WriteFile(keybindingFile, []byte("{}"), 0644)

	roots := []string{configDir}

	if !IsPathAllowed(keybindingFile, roots) {
		t.Error("keybindings 场景：配置目录内文件应允许")
	}

	// symlink 逃逸
	if IsLinux() {
		escapeLink := filepath.Join(configDir, "escape")
		os.Symlink("/etc", escapeLink)
		if IsPathAllowed(escapeLink, roots) {
			t.Error("keybindings 场景：symlink 逃逸应拒绝")
		}
	}
}

// TestIntegration_LSP_RoundTrip_FilePathToURI_URIToFilePath 端到端验证 LSP 双向转换
// 模拟完整 LSP 流程：发送 filePath → URI → 服务器 → 返回 URI → filePath
func TestIntegration_LSP_RoundTrip_FilePathToURI_URIToFilePath(t *testing.T) {
	if !IsLinux() {
		t.Skip("仅 Linux 测试")
	}

	tmpDir := t.TempDir()

	testPaths := []string{
		filepath.Join(tmpDir, "normal.go"),
		filepath.Join(tmpDir, "中文文件.go"),
		filepath.Join(tmpDir, "space name.go"),
		filepath.Join(tmpDir, "deep", "nested", "path.go"),
	}

	for _, original := range testPaths {
		// 确保路径存在（EvalSymlinks 需要）
		os.MkdirAll(filepath.Dir(original), 0755)
		os.WriteFile(original, []byte("package main"), 0644)

		// 模拟 LSP 发送：filePath → URI
		uri := FilePathToURI(original)

		// 模拟 LSP 接收：URI → filePath
		back, err := URIToFilePath(uri)
		if err != nil {
			t.Errorf("LSP 往返失败 (%s): %v", original, err)
			continue
		}
		if back != original {
			t.Errorf("LSP 往返不一致: 原始 %q → URI %q → 返回 %q", original, uri, back)
		}
	}
}

// ========== L3 契约验证 ==========

// TestContract_ExportedAPI_Stability 验证导出 API 签名未意外变更
func TestContract_ExportedAPI_Stability(t *testing.T) {
	// 验证所有文档承诺的导出函数存在且可调用
	// 根据 02-跨方向冲突预防与接口契约.md §2 和 H6 开发 prompt

	// platform 包的导出函数清单
	_ = OS
	_ = IsWindows
	_ = IsDarwin
	_ = IsLinux
	_ = CaseInsensitiveFS
	_ = NormalizePath
	_ = ResolvePath
	_ = IsAbsolute
	_ = ToSlash
	_ = FromSlash
	_ = ExpandTilde
	_ = NormalizeUnicode
	_ = CleanAndNormalize
	_ = PathEqual
	_ = CaseInsensitiveCompare
	_ = FilePathToURI
	_ = URIToFilePath
	_ = IsPathAllowed
	_ = ContainsTraversal
	_ = IsUNCPath
	_ = SafeName
}

// TestContract_IsPathAllowed_Signature 验证 IsPathAllowed 签名与契约一致
func TestContract_IsPathAllowed_Signature(t *testing.T) {
	// 契约：IsPathAllowed(p string, roots []string) bool
	// 调用方式必须匹配下游模块的使用模式
	var f func(string, []string) bool = IsPathAllowed
	if f == nil {
		t.Error("IsPathAllowed 签名与契约不一致")
	}
}

// TestContract_FilePathToURI_Signature 验证 FilePathToURI 签名与契约一致
func TestContract_FilePathToURI_Signature(t *testing.T) {
	// 契约：FilePathToURI(p string) string
	var f func(string) string = FilePathToURI
	if f == nil {
		t.Error("FilePathToURI 签名与契约不一致")
	}
}

// TestContract_URIToFilePath_Signature 验证 URIToFilePath 签名与契约一致
func TestContract_URIToFilePath_Signature(t *testing.T) {
	// 契约：URIToFilePath(uri string) (string, error)
	var f func(string) (string, error) = URIToFilePath
	if f == nil {
		t.Error("URIToFilePath 签名与契约不一致")
	}
}
