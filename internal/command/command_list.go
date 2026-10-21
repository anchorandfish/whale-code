package command

import (
	"strings"
)

// CommandCompletion defines completion info for a command's sub-commands and arguments
// [SNAPSHOT-COMPLETION] M09-03：新增 Children 字段支持多层嵌套子命令
type CommandCompletion struct {
	SubCommands  []string                      // 子命令列表（向后兼容，1 层）
	Children     map[string]*CommandCompletion // [SNAPSHOT] 子命令的子命令（多层嵌套，新增，零值 nil 兼容）
	ArgCompleter func(partial string) []string // 参数补全函数
}

// commandCompletions defines completion rules for commands with sub-commands
var commandCompletions = map[string]CommandCompletion{
	"plugin": {
		// P0-修复（5维度审查致命漏洞）：SubCommands 必须与 commands_plugin.go 注册一致
		// 注册的是 list/install/enable/disable + 顶层 reload-plugins（无 uninstall）
		SubCommands: []string{"install", "enable", "disable", "list"},
		ArgCompleter: func(partial string) []string {
			return []string{"./my-plugin", "whale-code/plugin-example"}
		},
	},
	"mcp": {
		SubCommands: []string{"add", "remove", "list", "restart"},
		ArgCompleter: func(partial string) []string {
			return listKnownMCPServers()
		},
	},
	"daemon": {
		SubCommands: []string{"start", "stop", "restart", "status", "add", "list", "cancel", "log"},
	},
	"hooks": {
		SubCommands: []string{"list", "add", "remove"},
	},
	"loop": {
		SubCommands: []string{"list", "pause", "resume", "cancel"},
	},
	"checkpoint": {
		SubCommands: []string{"save", "list", "restore", "delete", "show"},
	},
	"task": {
		SubCommands: []string{"list", "cancel", "show"},
	},
	// 声明面与**实际接受**的动词对齐（2026-09-30 随 /monitors 取消隐藏一并补登记）：
	// `stop`（唯一停止出口）与 `tail`（看近期输出）已注册为独立命令名；
	// `show` 是 handleMonitors 里 tail 的**兼容别名**，有意不登记 ⇒ 不在此声明、不进弹窗，
	// 避免"声明了却没有任何入口宣传它"的漂移。
	//
	// ⚠️ 本表**当前不驱动真机弹窗**：真机走 completion_popup.go 的 registry.All() +
	// typeahead.SuggestCommands，`Registry.Completion` 全仓无生产调用点（只有测试）。
	// 「两套补全实现、其中一套是死码」已在回执 §5 单列，本单不拆。
	"monitors": {
		SubCommands: []string{"stop", "tail"},
	},
	"cron": {
		SubCommands: []string{"add", "list", "remove"},
	},
	"bughunter": {
		SubCommands: []string{"start", "stop", "status"},
	},
	"worktree": {
		SubCommands: []string{"add", "remove", "list", "prune"},
	},
	"pr": {
		SubCommands: []string{"create", "review", "list", "merge"},
		// M09-03 演示：pr create 支持二级子命令 draft/ready（多层嵌套）
		Children: map[string]*CommandCompletion{
			"create": {SubCommands: []string{"draft", "ready"}},
		},
	},
	"issue": {
		SubCommands: []string{"create", "list", "view"},
	},
	"actions": {
		SubCommands: []string{"list", "view", "trigger", "rerun", "logs"},
	},
	"github": {
		SubCommands: []string{"watch", "stop", "status"},
	},
	"session": {
		SubCommands: []string{"list", "save", "switch", "merge", "pick"},
	},
	"memory": {
		// H11/N27 Phase B B6：扩展 confirm/probe/invalidate 子命令（Q5）
		// H11/N27 Phase C C1/C2：追加 reflect/compact 子命令（02 §2.5.1 命令注册）
		SubCommands: []string{"list", "edit", "show", "add", "delete", "confirm", "probe", "invalidate", "reflect", "compact"},
	},
	"model": {
		ArgCompleter: func(partial string) []string {
			return listAvailableModels()
		},
	},
	"think": {
		ArgCompleter: func(partial string) []string {
			return filterPrefix([]string{"on", "off", "default"}, partial)
		},
	},
	"fast": {
		ArgCompleter: func(partial string) []string {
			return filterPrefix([]string{"on", "off", "status"}, partial)
		},
	},
	"compact-strategy": {
		ArgCompleter: func(partial string) []string {
			return filterPrefix([]string{"balanced", "preserve-user", "aggressive"}, partial)
		},
	},
	"output-style": {
		ArgCompleter: func(partial string) []string {
			return filterPrefix([]string{"default", "concise", "verbose", "technical"}, partial)
		},
	},
	"privacy": {
		ArgCompleter: func(partial string) []string {
			return filterPrefix([]string{"show", "analytics-off", "analytics-on"}, partial)
		},
	},
	"export-format": {
		ArgCompleter: func(partial string) []string {
			return filterPrefix([]string{"json", "markdown", "html"}, partial)
		},
	},
	"notify": {
		SubCommands: []string{"list", "clear", "dismiss"},
	},
	// N26：goal 长期目标系统（/goal 单入口 + args 解析，避免 commandCompletions 爆炸）
	"goal": {
		SubCommands: []string{"set", "status", "pause", "resume", "clear", "edit", "done", "list", "help"},
	},
	// H10：智能编排系统（/orchestrate 单入口 + args 解析，02 §2.5.1 命令注册）
	"orchestrate": {
		SubCommands: []string{"start", "status", "list", "resume", "abort"},
	},
	// H10 Phase 2：工作流模板（/workflow 单入口 + args 解析，编排层 §6.4 命令注册）
	"workflow": {
		SubCommands: []string{"list", "run", "save-as"},
	},
}

// filterPrefix filters a list to only include items starting with the given prefix
func filterPrefix(items []string, prefix string) []string {
	var result []string
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return result
}

// listKnownMCPServers 返回硬编码的已知 MCP 服务器名称列表
// 当 Registry.OnListMCPServers 回调为 nil 时作为 fallback 使用
func listKnownMCPServers() []string {
	return []string{"filesystem", "github", "postgres", "brave-search"}
}

// defaultAvailableModels 内置默认可用模型名称列表（H8：原硬编码字面量提取为变量）
// 作为 OnListModels 回调和 Config.AvailableModels 均未提供时的最终 fallback
var defaultAvailableModels = []string{
	"claude-3.5-sonnet", "claude-3-opus", "claude-3-haiku",
	"gpt-4o", "gpt-4-turbo", "gpt-3.5-turbo",
	"deepseek-chat", "deepseek-reasoner",
	"qwen-max", "qwen-plus", "qwen-turbo",
}

// listAvailableModels 返回可用模型名称列表
// H8：仅返回内置默认列表，作为最终 fallback。
// 优先级链（OnListModels > Config.AvailableModels > 本函数）在 Registry.Completion 中处理。
func listAvailableModels() []string {
	return defaultAvailableModels
}
