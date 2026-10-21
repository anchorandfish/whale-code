// Package tools — LSP 工具的能力面装配（· W5）。
//
// 与 MCP（mcp_availability.go）/ WebSearch / GitHub 同一条链路，唯一差别在判据：
// LSP 的能力位是**运行期 + per-language**（同一会话 .go 可用、.py 不可用是常态），
// 因此**不得照抄**启动期全局谓词（那样两向都错）。
//
// 本装配采用的判据是**保守能力探针**：lsp.AnyLanguageServerAvailable —— 只有当
// PATH 上**一个**受支持的语言服务器都没有时（此时 10 个 LSP 工具 100% 不可用、且不存在
// "装了 gopls 没装 pyright"的误伤空间）才整体隐藏；只要有任一个 server 就保留，
// per-language 可用性仍交运行期 getLSPClient 判定（正确边界：不隐藏 ⇒ 无假承诺，
// 不误伤 ⇒ 无漏杀）。详见 internal/lsp/config.go 的 AnyLanguageServerAvailable 注释。
package tools

import (
	"github.com/anchorandfish/whale-code/internal/lsp"
)

// lspToolNames 是 10 个 LSP 工具的**唯一**名单。
//
// 它同时是「能力面装配」与「deferredToolNames 里的 LSP 段」的唯一权威：
// 新增/删除 LSP 工具只改这一处，装配与延迟标记自动跟随。名单与 tools.go
// registerBuiltinTools 里实际注册的 LSP spec 一一对应（守卫见 mcp_lsp_availability_test.go）。
var lspToolNames = []string{
	"LspDefinition",
	"LspReferences",
	"LspDiagnostics",
	"LspHover",
	"LspCompletion",
	"LspRename",
	"LspImplementation",
	"LspTypeDefinition",
	"LspCodeActions",
	"LspDocumentSymbols",
}

// ApplyLSPToolAvailability 把「启动时刻 PATH 上是否有任一语言服务器」这一个事实写进工具面唯一谓词。
//
// 生产调用点唯一：main.go 的 applyEnvDerivedToolAvailability（与 WebSearch/GitHub/MCP 同一咽喉点）。
// 判据经 lsp.AnyLanguageServerAvailable 探针，零 server ⇒ 10 个 LSP 工具全部不可用。
func ApplyLSPToolAvailability() {
	applyLSPToolAvailabilityFrom(lsp.DefaultLSPConfig(), lsp.AnyLanguageServerAvailable)
}

// applyLSPToolAvailabilityFrom 是可注入形态：判据用固定的配置与探针驱动、不依赖进程真实 PATH
// （否则判据会随开发机是否装了 gopls 而飘，属"环境相关假绿"—— 见 webSearchChannelFrom 同款理由）。
func applyLSPToolAvailabilityFrom(cfg *lsp.LSPConfig, available func(*lsp.LSPConfig) bool) {
	isAvailable := available(cfg)
	for _, name := range lspToolNames {
		SetToolAvailability(name, isAvailable)
	}
}
