// tool_render.go — 工具调用紧凑渲染（对标同类 CLI AssistantToolUseMessage + 每工具 UI.tsx）。
// 设计哲学（REF-外部-同类 CLI figures.ts / AssistantToolUseMessage.tsx）：
//
//	符号(⏺) + 工具名 + 人可读参数，绝不暴露原始 JSON。
//
// AskUser 调用阶段返回空字符串（由交互选择 UI 接管，对标 AskUserQuestionTool.tsx:189 renderToolUseMessage 返回 null）。
// 本文件为 lite 版（按工具名映射提取关键字段）；完整"每工具自渲染"架构列为后续 。
package tui

import (
	"encoding/json"
	"strings"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/charmbracelet/x/ansi"
)

// toolCallSymbol 工具调用行前导符号（对标同类 CLI BLACK_CIRCLE）。
// U+23FA，Windows Terminal 支持；cmd.exe 旧字体退化显示为方框，功能不受影响。
const toolCallSymbol = "⏺"

// bashCommandMaxDisplay bash 命令展示截断长度（对标同类 CLI BashTool/UI.tsx MAX_COMMAND_DISPLAY_CHARS=160）。
const bashCommandMaxDisplay = 160

// defaultPreviewMax 默认兜底参数预览截断长度（runes）。
const defaultPreviewMax = 60

// renderToolUseCompact 把工具调用渲染为人可读紧凑形式。
// 返回值已含前导符号与工具名，调用方直接拼接即可。
// AskUser 返回空字符串（调用阶段不显示，由 renderChoiceUI 交互 UI 接管）。
// 解析失败时降级为截断的紧凑 JSON，绝不展示原始完整 JSON。
func renderToolUseCompact(name, inputJSON string) string {
	rendered, _ := renderToolUseCompactSource(name, inputJSON)
	return rendered
}

// toolLineWithBudget 按 rune 预算生成「显示字面 + 完整原文」这一对。
//
// limit <= 0 ⇒ 不截断（full == rendered）。
func toolLineWithBudget(prefix, payload string, limit int) (rendered, full string) {
	full = prefix + payload
	if limit > 0 {
		if truncated := truncateRunes(payload, limit); truncated != payload {
			return prefix + truncated, full
		}
	}
	return full, full
}

// renderToolUseCompactSource 在 renderToolUseCompact 之上，额外给出**未截断的完整行**。
//
// 为什么必须有：命令行在 160 rune 处被截断、自由格式参数预览在 60 rune 处被截断，
// 截断后的字面**不含被截掉的那半截**，而旧拖选复制读的正是屏幕字面 ⇒ 用户拿不到完整命令。
// 本函数在**同一次渲染**里同时产出「显示字面」与「完整原文」，让调用方（format.go）
// 就地登记 source map（谁省略谁登记）—— 复制层无需事后按屏幕字面反查。
//
// full == rendered ⇒ 本行未发生省略，调用方不必登记。
func renderToolUseCompactSource(name, inputJSON string) (rendered, full string) {
	// AskUser 调用阶段不显示参数（对标同类 CLI 返回 null）。
	if name == "AskUser" {
		return "", ""
	}

	// 解析 inputJSON 为通用 map；失败则降级。
	var fields map[string]interface{}
	if inputJSON == "" {
		return toolCallSymbol + " " + name, toolCallSymbol + " " + name
	}
	if err := json.Unmarshal([]byte(inputJSON), &fields); err != nil {
		// 降级：截断的紧凑 JSON（去空白后截断），仍不展示原始完整 JSON。
		// W-029③：同为自由格式预览 ⇒ 压单行 + 显式分隔（见下方 default 分支说明）。
		return toolLineWithBudget(toolCallSymbol+" "+name+" · ", collapseToSingleLine(inputJSON), defaultPreviewMax)
	}

	// ★ P-002（2026-09-22 反缝补扫描）：命令载体的判据唯一来源 =
	// `permission.IsCommandBearingTool`（= {bash, monitor}）。修复前是手抄名对
	// `case "bash", "PowerShell":` ⇒ Monitor 的预览退化成整段 JSON ——
	// 而这是用户**唯一能看到"到底要跑什么命令"**的地方。
	if permission.IsCommandBearingTool(name) {
		// 对标同类 CLI BashTool/UI.tsx：提取 command 字段，截断 160 字符。
		cmd := stringField(fields, "command")
		if cmd == "" {
			cmd = stringField(fields, "cmd")
		}
		if cmd == "" {
			return toolCallSymbol + " " + name, toolCallSymbol + " " + name
		}
		return toolLineWithBudget(toolCallSymbol+" "+name+" ", cmd, bashCommandMaxDisplay)
	}

	switch name {
	case "Read", "View", "FileRead", "read_file":
		// 对标同类 CLI FileReadTool/UI.tsx：提取 file_path。
		p := fileField(fields)
		if p == "" {
			return toolCallSymbol + " " + name, toolCallSymbol + " " + name
		}
		return toolLineWithBudget(toolCallSymbol+" "+name+" ", p, 0)

	case "Write", "Edit", "Update", "ApplyPatch", "write_file", "edit_file":
		// 写/编辑类工具：提取路径字段。
		p := fileField(fields)
		if p == "" {
			return toolCallSymbol + " " + name, toolCallSymbol + " " + name
		}
		return toolLineWithBudget(toolCallSymbol+" "+name+" ", p, 0)

	case "Grep", "Search":
		// 对标同类 CLI GrepTool/UI.tsx：pattern: "xxx"。
		pattern := stringField(fields, "pattern")
		if pattern == "" {
			pattern = stringField(fields, "query")
		}
		if pattern == "" {
			return toolCallSymbol + " " + name, toolCallSymbol + " " + name
		}
		return toolLineWithBudget(toolCallSymbol+" "+name+" ", `pattern: "`+pattern+`"`, defaultPreviewMax)

	case "Glob":
		pattern := stringField(fields, "pattern")
		if pattern == "" {
			return toolCallSymbol + " " + name, toolCallSymbol + " " + name
		}
		return toolLineWithBudget(toolCallSymbol+" "+name+" ", pattern, 0)

	default:
		// 兜底：提取首个字符串型字段作为预览，无则截断紧凑 JSON。
		//
		// 第 3 件（2026-10-07 真机 T-14a/T-14b）：自由格式参数（如
		// apply_unified_diff 的整段 patch）可能**含换行**；原实现把首行直接拼在工具名后
		// ⇒ 屏幕上是 `⏺ apply_unified_diff --- a/t14_side.txt` 后面另起 `+++ …` 行，
		// 读起来像一条 shell 命令（工具名被误读为可执行程序、`---` 被误读为参数）。
		// 修法：预览**压成单行**（头不再被拆开），并用显式分隔符 ` · ` 把「工具名」与
		// 「自由格式参数」分开 —— 与 `🧠 Thinking · …` 同一处视觉语汇（本仓既有惯例）。
		if preview := firstStringField(fields); preview != "" {
			return toolLineWithBudget(toolCallSymbol+" "+name+" · ", collapseToSingleLine(preview), defaultPreviewMax)
		}
		return toolLineWithBudget(toolCallSymbol+" "+name+" · ", compactJSONPreview(inputJSON), defaultPreviewMax)
	}
}

// renderToolUseStreaming 流式期间的工具调用渲染（对标同类 CLI ToolUseLoader：spinner 替代 ⏺ 作为活动指示）。
// 返回 "{spinner} {工具名} {人可读参数}"；AskUser 返回空字符串（调用阶段不显示）。
// inputBuf 可能是未完整的 JSON 片段（流式累积中），renderToolUseCompact 已处理解析失败降级。
func renderToolUseStreaming(spinner, name, inputBuf string) string {
	rendered, _ := renderToolUseStreamingSource(spinner, name, inputBuf)
	return rendered
}

// renderToolUseStreamingSource 是 renderToolUseStreaming 的带 source 版本。
//
// 流式期间命令行同样按 160 rune 截断（renderToolUseCompactSource）⇒ 拖选同样拿不到
// 完整命令，故这里也要给出完整行（与定稿路径同一份事实，不另立口径）。
func renderToolUseStreamingSource(spinner, name, inputBuf string) (rendered, full string) {
	if name == "AskUser" {
		return "", ""
	}
	rendered, full = renderToolUseCompactSource(name, inputBuf)
	// 去掉前导 ⏺，改用 spinner 作为活动指示
	return spinner + " " + strings.TrimPrefix(rendered, toolCallSymbol+" "),
		spinner + " " + strings.TrimPrefix(full, toolCallSymbol+" ")
}

// stringField 从 map 中安全提取字符串字段（兼容 string 与 json.Number）。
func stringField(fields map[string]interface{}, key string) string {
	v, ok := fields[key]
	if !ok {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return ""
	}
}

// fileField 从 map 中提取文件路径字段（兼容 file_path/path/filepath/file 等命名）。
func fileField(fields map[string]interface{}) string {
	for _, k := range []string{"file_path", "path", "filepath", "file", "fileName"} {
		if s := stringField(fields, k); s != "" {
			return s
		}
	}
	return ""
}

// firstStringField 返回 map 中第一个字符串型字段的值（用于兜底预览）。
func firstStringField(fields map[string]interface{}) string {
	for _, v := range fields {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// toolFoldHintFor 是 TUI 侧「折叠省略行提示」的**唯一构造点**。
//
// 为什么必须是函数而不是两处各写一遍字面量：
// - 渲染侧用它产出屏幕上的省略行（formatToolResultMessage）；
// - 复制侧用它**重建**同一条省略行的字面，以便在渲染产物里定位折叠点、
// 把被省略的中段还原成原文（mouseSelectionCopyOverrides）。
//
// 两处只要 hint 稍有不同（命令名、序号口径），复制侧就会**静默失配** ——
// 表现为「代码改了、测试也绿，但拖选还是复制不到中段」。收敛到这一个函数，
// 让它们不可能分叉。
func toolFoldHintFor(seq int) tools.ToolFoldHint {
	return tools.ToolFoldHint{Command: "/tool", Seq: seq}
}

// formatToolResultMessage 把一条 RoleTool 会话消息渲染为折叠显示（head+tail 5 行 +
// 省略号 + /tool 提示），是「工具结果显示」的唯一收敛点。
//
// 它从 session 权威源（msg.Blocks 里的 tool_result 块，含完整 Output）构造 tools.Result，
// 再交给 FormatToolResultStruct 做 head+tail 折叠。resume/fork 派生与实时路径共用此函数，
// 消除「实时走折叠、恢复走 truncateRunes(200)」的双渲染公式（切口①）。
//
// 不变式 I4：工具结果显示折叠函数有且只有一个（FormatToolResultStruct）；本函数只是
// 从 session.Message 到 tools.Result 的适配器，不另写折叠逻辑。
//
// seq 是这条工具结果在会话中的 1-based 序号，来自 SSOT `toolResultSeq`（与 /tool 同源）。
// seq <= 0 = 无序号上下文 ⇒ 退化为不带序号的提示（不猜测、不编造）。
func formatToolResultMessage(msg session.Message, seq int) string {
	text, _ := formatToolResultMessageSource(msg, seq)
	return text
}

// formatToolResultMessageSource 在 formatToolResultMessage 之上，登记折叠省略行的 source map。
//
// 省略行的**真值** = 被 head/tail 折叠吞掉的中段原文，取自**同一次** foldToolResult 计算
// （tools.ToolFoldMarkerFor），与渲染侧同一份事实 ⇒ 不存在"渲染怎么折的、复制怎么还原"
// 两处各写一份而漂移。
//
// 定位省略行用的是"本函数刚产出的文本里哪一行 == 本次算出的 marker"：这是**产地自定位**
// （marker 由同一调用产出、必然命中），不是旧实现的跨层字面反查 —— 后者的失败形态是
// 「屏幕字面稍变（窄视口 wrap、compact 后序号漂移）⇒ 静默失配 ⇒ 退回修前」。
func formatToolResultMessageSource(msg session.Message, seq int) (string, []lineSource) {
	for _, b := range msg.Blocks {
		if b.Type != "tool_result" {
			continue
		}
		result := tools.Result{
			ID:      b.ToolUseID,
			Name:    b.ToolName,
			Output:  b.Output,
			IsError: b.IsError,
		}
		text := tools.FormatToolResultStructWithHint(result, tools.MaxToolResultLength, toolFoldHintFor(seq))
		marker, hidden := tools.ToolFoldMarkerFor(result, tools.MaxToolResultLength, toolFoldHintFor(seq))
		if marker == "" || len(hidden) == 0 {
			return text, nil
		}
		overrides := map[int]string{}
		for i, line := range strings.Split(text, "\n") {
			if strings.TrimRight(ansi.Strip(line), " ") == marker {
				overrides[i] = strings.Join(hidden, "\n")
				break
			}
		}
		return text, lineSourcesFromOverrides(lineCountOf(text), overrides)
	}
	// 兜底：无 tool_result 块时退回文本（历史上该分支几乎不可达，RoleTool 必有 tool_result 块）。
	return truncateRunes(msg.TextContent(), 200), nil
}

// compactJSONPreview 把原始 JSON 压缩为单行预览（去换行与多余空白）。
func compactJSONPreview(inputJSON string) string {
	compact := strings.ReplaceAll(inputJSON, "\n", " ")
	compact = strings.ReplaceAll(compact, "\t", " ")
	// 合并连续空白
	for strings.Contains(compact, "  ") {
		compact = strings.ReplaceAll(compact, "  ", " ")
	}
	return strings.TrimSpace(compact)
}
