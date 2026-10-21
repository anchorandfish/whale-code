// commands_alias_history.go — 命令系统自身管理命令（/alias + /history）
// M09-01/02 改造：注册 /alias 和 /history 命令，对应处理器复用
// Registry.aliasStore 和 Registry.historyStore（通过 SetAliasStore / SetHistoryStore 注入）。
package command

import (
	"fmt"
	"strconv"
	"strings"
)

// commandCommands 返回命令系统自身管理命令（M09-01/02 改造，）
// 包含 /alias（别名管理）和 /history（命令历史查看）两个 Core 类命令
// 依赖：Registry.aliasStore（nil 降级为未初始化提示）+ Registry.historyStore（同上）
func (r *Registry) commandCommands() []Command {
	return []Command{
		{
			Name: "alias", Hidden: true,
			Description: "管理命令别名（add/list/remove）",
			Usage:       "/alias add <name> <target> [args] [description...] | /alias list | /alias remove <name>",
			Category:    "Core",
			Handler: func(args string) (string, error) {
				return r.handleAlias(args)
			},
		},
		{
			Name:        "history",
			Description: "显示命令历史（最近优先）",
			Usage:       "/history [prefix] [--limit N]",
			Category:    "Core",
			Handler: func(args string) (string, error) {
				return r.handleHistory(args)
			},
		},
	}
}

// handleAlias 处理 /alias 子命令（M09-02 改造，）
// 子命令：add <name> <target> [args] [description...] / list / remove <name>
// args 模板支持 $1/$2/$* 占位符（单层展开，不递归，由 AliasStore.Expand 实现）
// aliasStore 为 nil 时返回未初始化提示（降级，不 panic）
func (r *Registry) handleAlias(args string) (string, error) {
	if r.aliasStore == nil {
		return "Alias store not initialized", nil
	}
	parts := strings.Fields(args)
	if len(parts) == 0 || parts[0] == "list" {
		aliases := r.aliasStore.List()
		if len(aliases) == 0 {
			return "No aliases defined. Use /alias add <name> <target> [args] to create one.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Aliases (%d):\n", len(aliases)))
		for _, a := range aliases {
			sb.WriteString(fmt.Sprintf("  /%s → /%s", a.Name, a.Target))
			if a.Args != "" {
				sb.WriteString(fmt.Sprintf(" %s", a.Args))
			}
			if a.Description != "" {
				sb.WriteString(fmt.Sprintf("  # %s", a.Description))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
	switch parts[0] {
	case "add":
		if len(parts) < 3 {
			return "Usage: /alias add <name> <target> [args] [description...]", nil
		}
		name := parts[1]
		target := parts[2]
		aliasArgs := ""
		desc := ""
		// 第 4 段起，若以 $ 开头视为 args 模板，否则视为 description
		if len(parts) >= 4 {
			if strings.HasPrefix(parts[3], "$") {
				aliasArgs = parts[3]
				if len(parts) > 4 {
					desc = strings.Join(parts[4:], " ")
				}
			} else {
				desc = strings.Join(parts[3:], " ")
			}
		}
		if err := r.aliasStore.Add(name, target, aliasArgs, desc); err != nil {
			return fmt.Sprintf("Failed to add alias: %v", err), nil
		}
		return fmt.Sprintf("✅ Alias added: /%s → /%s %s", name, target, aliasArgs), nil
	case "remove":
		if len(parts) < 2 {
			return "Usage: /alias remove <name>", nil
		}
		if err := r.aliasStore.Remove(parts[1]); err != nil {
			return fmt.Sprintf("Failed to remove alias: %v", err), nil
		}
		return fmt.Sprintf("✅ Alias removed: %s", parts[1]), nil
	default:
		return "Unknown subcommand. Usage: /alias [add|list|remove]", nil
	}
}

// handleHistory 处理 /history 命令（M09-01 改造，）
// 参数：[prefix] [--limit N]，prefix 为命令前缀过滤（不区分大小写），limit 默认 20
// historyStore 为 nil 时返回未初始化提示（降级，不 panic）
// 查询结果按时间倒序（最近优先），由 HistoryStore.Query 实现
func (r *Registry) handleHistory(args string) (string, error) {
	if r.historyStore == nil {
		return "History store not initialized", nil
	}
	// 解析参数：前缀 + --limit N
	parts := strings.Fields(args)
	prefix := ""
	limit := 20
	for i := 0; i < len(parts); i++ {
		if parts[i] == "--limit" && i+1 < len(parts) {
			if n, err := strconv.Atoi(parts[i+1]); err == nil && n > 0 {
				limit = n
				i++
			}
		} else if !strings.HasPrefix(parts[i], "--") {
			prefix = parts[i]
		}
	}
	entries := r.historyStore.Query(prefix, limit)
	if len(entries) == 0 {
		if prefix != "" {
			return fmt.Sprintf("No history matching prefix '%s'", prefix), nil
		}
		return "No command history yet", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Command History (showing %d, prefix=%q):\n", len(entries), prefix))
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("  [%s] %s\n", e.Timestamp.Format("2006-01-02 15:04:05"), e.Command))
	}
	return sb.String(), nil
}
