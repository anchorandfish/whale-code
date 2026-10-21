// Package tools — MCP 元工具的能力面装配（· W5）。
//
// 与 WebSearch / GitHub 同一条链路：
//
//	启动期装配（本文件，唯一"写"点）→ tools 的唯一谓词（tools.SetToolAvailability）
//
// → 三面各自"读"它：① 注册点（tools.go registerBuiltinTools 不建 spec）
// ② 档位目录（toollevel.go toolLevelsSnapshot 出口过滤）
// ③ 执行门（executor.go registerBuiltinHandlers 不注册 handler）
//
// 本文件只负责**写**，不含第二套判断。判据是"启动时刻是否配置了任何 MCP server"——
// 这一语义与 github.ApplyToolAvailability / WebSearchChannel 同形同源（"启动时刻的配置承诺"），
// 也与 DiscoverAndRegisterMCPTools（启动期一次性发现动态工具）一致，与 `/mcp add` 明说要
// restart 一致。会话中途改 mcp.json 不实时反映，是**刻意**的（与同族三面一致），
// 由 restart 语义兜底。
package tools

import (
	"github.com/anchorandfish/whale-code/internal/mcp"
)

// mcpMetaToolNames 是 6 个 MCP 元工具的**唯一**名单。
//
// 它同时是「能力面装配」与「deferredToolNames 里的 MCP 段」的唯一权威：
// 新增/删除 MCP 元工具只改这一处，装配与延迟标记自动跟随（否则会造第二份名单漂移，
// 正是本仓 「同一事实跨入口必须同源」要消灭的形态）。
//
// 名单与 tools.go registerBuiltinTools 里实际注册的 MCP spec 一一对应（守卫见
// mcp_lsp_availability_test.go 的名单漂移守卫）。
var mcpMetaToolNames = []string{
	"McpListTools",
	"McpCallTool",
	"McpListPrompts",
	"McpGetPrompt",
	"McpListResources",
	"McpReadResource",
}

// ApplyMCPToolAvailability 把「启动时刻是否配置了任何 MCP server」这一个事实写进工具面唯一谓词。
//
// 生产调用点唯一：main.go 的 applyEnvDerivedToolAvailability（与 WebSearch/GitHub 同一咽喉点）。
// 判据经 loadMCPConfig 读 <workDir>/.whale/mcp.json + ~/.whale/mcp.json，零 server ⇒ 6 个
// 元工具全部不可用（不注册、不进档位目录、不注册 handler），调用也不再落到"软失败"假成功。
func ApplyMCPToolAvailability(workDir string) {
	applyMCPToolAvailabilityFrom(workDir, loadMCPConfig)
}

// applyMCPToolAvailabilityFrom 是可注入形态：判据用固定的配置加载函数驱动、不依赖进程真实文件
// （否则判据会随开发机 ~/.whale/mcp.json 而飘，属"环境相关假绿"—— 见 webSearchChannelFrom 同款理由）。
func applyMCPToolAvailabilityFrom(workDir string, loadConfig func(string) *mcp.Config) {
	available := len(loadConfig(workDir).Servers) > 0
	for _, name := range mcpMetaToolNames {
		SetToolAvailability(name, available)
	}
}
