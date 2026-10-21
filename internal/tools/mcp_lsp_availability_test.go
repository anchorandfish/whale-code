// Package tools — MCP/LSP 能力面装配（/ · W5）的判别器。
//
// 纪律（与 search_surface_test.go 同）：
// - 全部离线、走生产入口（NewRegistry/NewExecutor/ExecuteBatch），不手工构造前提；
// - 每条"隐藏"用例都显式 ResetToolAvailability + 注入，Cleanup 复位（防包级状态互串）；
// - 每条判据都配一条"反例注入应当打红"的说明（见各用例注释）。
//
// ★ 关键：MCP/LSP 的能力判据是**注入的配置/探针**，不依赖开发机真实环境
// （否则"零 server"用例会因开发机装了 gopls / 配了 mcp.json 而假绿或假红）。
package tools

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/lsp"
	"github.com/anchorandfish/whale-code/internal/mcp"
)

// ---------------------------------------------------------------- 面①：装配判据（可注入形态）

// TestN045_MCPAssembly_WritesPredicate 判据：applyMCPToolAvailabilityFrom 把"零/非零 server"
// 这一个事实写进 toolAvailable 谓词。
// 反例注入（应打红）：把 available 改成 len(...) >= 0（恒 true）或把名单删掉一个名。
func TestN045_MCPAssembly_WritesPredicate(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)

	zeroServer := func(string) *mcp.Config { return &mcp.Config{Servers: map[string]mcp.ServerConfig{}} }
	oneServer := func(string) *mcp.Config {
		return &mcp.Config{Servers: map[string]mcp.ServerConfig{"srv": {Command: "node"}}}
	}

	// 零 server ⇒ 6 个元工具全部不可用。
	applyMCPToolAvailabilityFrom("", zeroServer)
	for _, name := range mcpMetaToolNames {
		if ToolAvailable(name) {
			t.Fatalf("零 server 时 %s 必须不可用", name)
		}
	}

	// 有 server ⇒ 6 个全部可用。
	applyMCPToolAvailabilityFrom("", oneServer)
	for _, name := range mcpMetaToolNames {
		if !ToolAvailable(name) {
			t.Fatalf("有 server 时 %s 必须可用", name)
		}
	}
}

// TestN046_LSPAssembly_WritesPredicate 判据：applyLSPToolAvailabilityFrom 把"零/非零语言服务器"
// 这一个事实写进 toolAvailable 谓词。
// 反例注入（应打红）：把探针判据改成恒 true，或把名单删掉一个名。
func TestN046_LSPAssembly_WritesPredicate(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)

	// 注入探针：不依赖开发机真实 PATH。available=false 模拟"零 server"，true 模拟"有 server"。
	applyLSPToolAvailabilityFrom(nil, func(*lsp.LSPConfig) bool { return false })
	for _, name := range lspToolNames {
		if ToolAvailable(name) {
			t.Fatalf("零语言服务器时 %s 必须不可用", name)
		}
	}

	applyLSPToolAvailabilityFrom(nil, func(*lsp.LSPConfig) bool { return true })
	for _, name := range lspToolNames {
		if !ToolAvailable(name) {
			t.Fatalf("有语言服务器时 %s 必须可用", name)
		}
	}
}

// ---------------------------------------------------------------- 面②：名单漂移守卫

// TestN045_N046_NoServer_HidesMCPLSPOnAllFaces 判据（对准 MCP/LSP 工具是 **deferred** 这一事实）：
// 零 server ⇒ MCP/LSP 工具在 ① 注册点（Get 返回 false，spec 未建）② 延迟目录
// （DeferredToolDirectory 不列出）③ 档位目录（toolLevelsSnapshot 过滤）④ 子 agent 白名单
// （getAllowedToolSet 不含）四面皆无。
//
// ★ 注意：MCP/LSP 是 ExposureDeferred，**不进 AllDirect()**（那是 Direct 面）。所以这里不测
// AllDirect（测它恒真 = 假绿）；真正的暴露面是延迟目录 + Get + 档位目录 + 子 agent 白名单。
// 反例注入（应打红）：删掉 registerBuiltinTools 的 toolAvailable 短路，或删掉 toollevel.go 的过滤循环。
func TestN045_N046_NoServer_HidesMCPLSPOnAllFaces(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)
	for _, name := range mcpMetaToolNames {
		SetToolAvailability(name, false)
	}
	for _, name := range lspToolNames {
		SetToolAvailability(name, false)
	}

	registry := NewRegistry()
	all := append(append([]string{}, mcpMetaToolNames...), lspToolNames...)

	// 面①：注册点 —— spec 未建，Get 必须返回 false。
	for _, name := range all {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("零 server 时 %s 不得在注册表里（Get 应返回 false）", name)
		}
	}

	// 面②：延迟目录 —— 不列出被隐藏的 MCP/LSP 工具。
	dir := registry.DeferredToolDirectory()
	for _, name := range all {
		if containsLine(dir, name) {
			t.Fatalf("零 server 时 DeferredToolDirectory 仍列出 %s", name)
		}
	}

	// 面③：档位目录（静态快照"只增不减"，必须显式过滤）。
	snapshot := toolLevelsSnapshot()
	for _, name := range all {
		if _, ok := snapshot[name]; ok {
			t.Fatalf("零 server 时 toolLevelsSnapshot() 仍含 %s", name)
		}
	}

	// 面④：子 agent 下发面。
	e := newWebSearchProdExecutor(t)
	a := NewAgentContext(e, "probe", "", t.TempDir(), 1)
	a.AgentType = AgentTypeCoder
	for _, name := range all {
		if a.getAllowedToolSet()[name] {
			t.Fatalf("零 server 时 getAllowedToolSet() 仍含 %s", name)
		}
	}
}

// containsLine 判断目录文本是否列出来该工具名（目录行格式 "- 工具名 — 描述"）。
func containsLine(dir, name string) bool {
	for _, line := range strings.Split(dir, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- "+name+" ") || strings.HasPrefix(trimmed, "- "+name+" —") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 面④：执行门（非管线路径）

// TestN045_N046_NoServer_ExecuteGateFailsClosed 判据：零 server 时非管线路径（Pipeline==nil）
// 调用 MCP/LSP 工具必须落到"不可用"语义，而不是退化成软失败文本。
// 反例注入（应打红）：把 executor.go 的逐名 toolAvailable 闸门去掉。
func TestN045_N046_NoServer_ExecuteGateFailsClosed(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)
	for _, name := range mcpMetaToolNames {
		SetToolAvailability(name, false)
	}
	for _, name := range lspToolNames {
		SetToolAvailability(name, false)
	}

	// 非管线 Executor：不 InitPipeline ⇒ Pipeline == nil ⇒ executeDirect 只查 e.handlers。
	e := NewExecutor(NewRegistry(), t.TempDir())

	probe := func(name, input string) {
		t.Helper()
		results := e.ExecuteBatch(t.Context(), []Call{{ID: "w5-" + name, Name: name, Input: input}})
		if len(results) != 1 {
			t.Fatalf("[%s] 期望 1 个结果，得到 %d", name, len(results))
		}
		r := results[0]
		if !strings.Contains(r.Output, "unknown tool") && !strings.Contains(r.Output, "not registered") {
			t.Errorf("[%s] 零 server 时执行门必须落到『未知工具/未注册』，实际：%q", name, r.Output)
		}
	}

	// MCP 代表性工具
	probe("McpListTools", `{"server":"s"}`)
	probe("McpCallTool", `{"server":"s","tool_name":"t"}`)
	// LSP 代表性工具
	probe("LspDefinition", `{"file_path":"a.go","line":1,"character":1}`)
	probe("LspReferences", `{"file_path":"a.go","line":1,"character":1}`)
}

// ---------------------------------------------------------------- 面⑤：名单漂移守卫

// TestN045_N046_NameListMatchesRegistry 判据：唯一名单 mcpMetaToolNames / lspToolNames 必须与
// 注册表里实际注册的 MCP/LSP spec、以及执行门的 handler 工厂表**一一对应**。
//
// 反例注入（应打红）：在 tools.go registerBuiltinTools 新增一个 MCP/LSP 工具但漏进本名单；
// 或往 lspHandlerFactories/mcpHandlerFactories 漏一个键。
func TestN045_N046_NameListMatchesRegistry(t *testing.T) {
	ResetToolAvailability()
	t.Cleanup(ResetToolAvailability)

	registry := NewRegistry()
	got := map[string]bool{}
	for _, name := range registry.List() {
		got[name] = true
	}

	// 名单里的每个名都必须真在注册表里。
	for _, name := range append(append([]string{}, mcpMetaToolNames...), lspToolNames...) {
		if !got[name] {
			t.Fatalf("名单里的 %s 不在注册表里（名单漂移：删了工具没删名单）", name)
		}
	}

	// 反向：注册表里的 MCP/LSP 工具都必须被名单覆盖（防"注册了但装配名单漏掉 ⇒ 永不隐藏"）。
	mcpSet := toSet(mcpMetaToolNames)
	lspSet := toSet(lspToolNames)
	for name := range got {
		if isMCPName(name) && !mcpSet[name] {
			t.Fatalf("注册表里的 MCP 元工具 %s 不在 mcpMetaToolNames 名单里（装配会漏它）", name)
		}
		if isLSPName(name) && !lspSet[name] {
			t.Fatalf("注册表里的 LSP 工具 %s 不在 lspToolNames 名单里（装配会漏它）", name)
		}
	}

	// handler 工厂表键 == 名单（执行门逐名判谓词时不会漏注册/漏隐藏）。
	e := NewExecutor(NewRegistry(), t.TempDir())
	if !sameKeySet(lspHandlerFactories(e), lspSet) {
		t.Fatalf("lspHandlerFactories 键与 lspToolNames 不一致")
	}
	if !sameKeySet(mcpHandlerFactories(e), mcpSet) {
		t.Fatalf("mcpHandlerFactories 键与 mcpMetaToolNames 不一致")
	}
}

// ---------------------------------------------------------------- 辅助

func toSet(names []string) map[string]bool {
	s := make(map[string]bool, len(names))
	for _, n := range names {
		s[n] = true
	}
	return s
}

func sameKeySet(m map[string]func() Handler, names map[string]bool) bool {
	if len(m) != len(names) {
		return false
	}
	for k := range m {
		if !names[k] {
			return false
		}
	}
	return true
}

func isMCPName(name string) bool {
	return len(name) >= 3 && name[:3] == "Mcp"
}

func isLSPName(name string) bool {
	return len(name) >= 3 && name[:3] == "Lsp"
}
