// Package mcp — 名称归一化与工具全限定名构建（M10）
//
// 对标同类 CLI 与 mcpStringUtils.ts：
//
//   - NormalizeNameForMCP: 将任意名称归一化为匹配 ^[a-zA-Z0-9_-]{1,64}$ 的安全形式
//   - BuildMcpToolName: 构建 mcp__<server>__<tool> 全限定名，防止跨 server 工具名冲突
//   - ParseMcpToolName: 解析全限定名回 (server, tool)
//
// 权限检查用途：MCP 工具用全限定名做 deny 匹配，防止同名内置工具误匹配。
package mcp

import (
	"fmt"
	"regexp"
	"strings"
)

// mcpToolNamePrefix 是全限定工具名的前缀。
const mcpToolNamePrefix = "mcp__"

// mcpToolNameSeparator 是 server 名与 tool 名之间的分隔符。
const mcpToolNameSeparator = "__"

// validNameRe 校验名称是否匹配 ^[a-zA-Z0-9_-]{1,64}$（MCP 安全字符集）。
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// invalidCharRe 匹配所有非法字符（非 [a-zA-Z0-9_-]），用于替换为下划线。
var invalidCharRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// NormalizeNameForMCP 将任意名称归一化为 MCP 安全名称。
//
// 规则（对标同类 CLI normalization.ts）：
//  1. 若名称已匹配 ^[a-zA-Z0-9_-]{1,64}$，原样返回
//  2. 非法字符替换为下划线 _
//  3. 截断到 64 字符
//
// 用于 server 名和 tool 名的归一化，防止注入字符破坏 mcp__server__tool 格式。
func NormalizeNameForMCP(name string) string {
	if validNameRe.MatchString(name) {
		return name
	}
	// 非法字符替换为下划线
	normalized := invalidCharRe.ReplaceAllString(name, "_")
	// 截断到 64 字符
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return normalized
}

// BuildMcpToolName 构建 MCP 工具全限定名：mcp__<server>__<tool>。
//
// server 和 tool 名均经过 NormalizeNameForMCP 归一化，
// 确保全限定名格式稳定，可用于权限 deny 匹配。
//
// 示例：BuildMcpToolName("my-server", "read_file") → "mcp__my_server__read_file"
func BuildMcpToolName(serverName, toolName string) string {
	return fmt.Sprintf("%s%s%s%s",
		mcpToolNamePrefix,
		NormalizeNameForMCP(serverName),
		mcpToolNameSeparator,
		NormalizeNameForMCP(toolName))
}

// ParseMcpToolName 解析 mcp__<server>__<tool> 全限定名，返回 (server, tool, ok)。
//
// ok=false 表示输入不是合法的全限定名（前缀错误或缺少分隔符）。
// 注意：tool 名本身可能包含 __，故仅按第一个 __ 分割 server 和 tool 部分。
// server 部分是前缀后到第一个 __ 的内容，tool 部分是剩余全部。
func ParseMcpToolName(fullName string) (serverName, toolName string, ok bool) {
	if !strings.HasPrefix(fullName, mcpToolNamePrefix) {
		return "", "", false
	}
	rest := fullName[len(mcpToolNamePrefix):]
	// 按第一个 __ 分割：server 部分不含 __，tool 部分可含 __
	idx := strings.Index(rest, mcpToolNameSeparator)
	if idx <= 0 || idx == len(rest)-len(mcpToolNameSeparator) {
		return "", "", false
	}
	serverName = rest[:idx]
	toolName = rest[idx+len(mcpToolNameSeparator):]
	if serverName == "" || toolName == "" {
		return "", "", false
	}
	return serverName, toolName, true
}

// QualifiedToolName 返回该工具在指定 server 下的全限定名 mcp__<server>__<tool>。
//
// 便捷方法，等价于 BuildMcpToolName(serverName, tool.Name)。
func QualifiedToolName(serverName string, tool ToolInfo) string {
	return BuildMcpToolName(serverName, tool.Name)
}
