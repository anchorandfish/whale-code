package context

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/util"
)

// H13-G3 Feature Flag 环境变量。
//
// EnvMicroCompactDynamic 控制 G3 MicroCompact 阈值动态化是否启用。
// 默认 DISABLED：保持现有硬编码默认值（PreserveRecent=5, TruncationThreshold=500）。
// 显式设置 "1"/"true" 启用动态阈值，调用方可通过 CalcDynamicThreshold(contextWindow)
// 根据模型上下文窗口大小获取适配的 MicroCompactConfig。
//
// 仲裁依据（方案 4.4 节 Q3）：G3 仅影响 Micro 层阈值，不影响 AI 层；
// 阈值设置上限（TruncationThreshold ≤ 5000）防止过大导致压缩不足。
const EnvMicroCompactDynamic = "WHALE_MICRO_COMPACT_DYNAMIC"

// IsMicroCompactDynamicEnabled 返回是否启用 G3 MicroCompact 阈值动态化（H13-G3）。
//
// 默认 DISABLED：保持现有硬编码默认值不变。
// 显式设置 "1"/"true" 启用动态阈值，由 CalcDynamicThreshold 根据 contextWindow 计算适配配置。
// 与 DefaultMicroCompactConfig 互补：disabled 时调用方使用默认值，enabled 时使用动态值。
func IsMicroCompactDynamicEnabled() bool {
	v := strings.ToLower(os.Getenv(EnvMicroCompactDynamic))
	return v == "1" || v == "true"
}

// MicroCompactConfig micro-compaction config
// used for progressive compaction strategy, preserves recent tool results, truncates old verbose content
type MicroCompactConfig struct {
	// PreserveRecent preserves the most recent N tool results from truncation
	PreserveRecent int
	// TimeThreshold: messages older than this are considered old and can be truncated
	TimeThreshold time.Duration
	// TruncationThreshold: tool result content exceeding this character count triggers truncation
	TruncationThreshold int
}

// DefaultMicroCompactConfig returns the default micro compact config
func DefaultMicroCompactConfig() MicroCompactConfig {
	return MicroCompactConfig{
		PreserveRecent:      5,
		TimeThreshold:       30 * time.Minute,
		TruncationThreshold: 500,
	}
}

// CalcDynamicThreshold 根据上下文窗口动态计算 MicroCompact 阈值（H13-G3）。
//
// 对标同类 CLI timeBasedMCConfig.ts 动态配置（REF-外部: @2026-03-31快照，.ts 加密无法读实现）。
// 根据模型上下文窗口大小适配阈值，避免小窗口模型过早触发压缩、大窗口模型压缩不足：
// - 200K 窗口 → PreserveRecent=10, TruncationThreshold=1000（大窗口放宽，保留更多上下文）
// - 8K 窗口 → PreserveRecent=3, TruncationThreshold=200（小窗口收紧，更早压缩避免溢出）
// - 默认 → PreserveRecent=5, TruncationThreshold=500（与 DefaultMicroCompactConfig 一致）
//
// 阈值上限约束（方案 4.4 节 Q3 仲裁）：
// - TruncationThreshold 上限 5000（防止过大导致压缩不足）
// - PreserveRecent 上限 20（防止保留过多消息）
// - TimeThreshold 固定 30min（与默认值一致，时间维度不动态化）
//
// 参数：
// - contextWindow: 模型上下文窗口大小（token 数）
//
// 返回：适配的 MicroCompactConfig。contextWindow <= 0 时返回默认值（向后兼容）。
func CalcDynamicThreshold(contextWindow int) MicroCompactConfig {
	if contextWindow <= 0 {
		return DefaultMicroCompactConfig()
	}
	cfg := MicroCompactConfig{
		TimeThreshold: 30 * time.Minute, // 时间维度不动态化
	}
	switch {
	case contextWindow >= 150000:
		// 大窗口模型（200K 级别）：放宽阈值，保留更多上下文
		cfg.PreserveRecent = 10
		cfg.TruncationThreshold = 1000
	case contextWindow <= 16000:
		// 小窗口模型（8K 级别）：收紧阈值，更早压缩避免溢出
		cfg.PreserveRecent = 3
		cfg.TruncationThreshold = 200
	default:
		// 中等窗口（32K/128K）：使用默认值
		cfg.PreserveRecent = 5
		cfg.TruncationThreshold = 500
	}
	// 阈值上限 clamp（方案 4.4 节 Q3 仲裁：防止过大导致压缩不足）
	if cfg.TruncationThreshold > 5000 {
		cfg.TruncationThreshold = 5000
	}
	if cfg.PreserveRecent > 20 {
		cfg.PreserveRecent = 20
	}
	if cfg.PreserveRecent < 1 {
		cfg.PreserveRecent = 1
	}
	if cfg.TruncationThreshold < 100 {
		cfg.TruncationThreshold = 100
	}
	return cfg
}

// MicroCompact performs micro-compaction
// traverses from latest message to oldest, preserves recent tool results, replaces old tool results exceeding truncation threshold with summary placeholders
func MicroCompact(messages []session.Message, config MicroCompactConfig) []session.Message {
	if len(messages) == 0 {
		return messages
	}

	// count of preserved tool results
	preservedCount := 0
	now := time.Now().UnixMilli()
	result := make([]session.Message, len(messages))
	copy(result, messages)

	// Iterate from newest to oldest
	for i := len(result) - 1; i >= 0; i-- {
		msg := result[i]

		// Only process tool result messages
		if msg.Role != session.RoleTool {
			continue
		}

		// determine if message is recent
		// N11 修复 G4（P0-8）：Timestamp==0 视为远古（截断），与 02 实施计划 P0-8 契约一致。
		// 修改前（错误）：msgTime == 0 被视为"近期"（保留），导致无时间戳的旧消息被错误保留。
		// 修改后（正确）：msgTime != 0 且在阈值内才视为近期；msgTime == 0 视为远古（截断）。
		msgTime := msg.Timestamp
		isRecent := msgTime != 0 && (time.Duration(now-msgTime)*time.Millisecond < config.TimeThreshold)

		// Recent messages or not exceeding preserve count, keep directly
		if isRecent || preservedCount < config.PreserveRecent {
			preservedCount++
			continue
		}

		// Truncate old tool results processing
		newBlocks := make([]session.ContentBlock, len(msg.Blocks))
		for j, block := range msg.Blocks {
			if block.Type == "tool_result" && len(block.Output) > config.TruncationThreshold {
				charCount := len(block.Output)
				// W-046a：走唯一语义源 util.CountLines（编辑器语义）——
				// 旧 +1 写法在末尾带换行的输出上多报 1 行，与 read_file 对同一内容矛盾。
				lineCount := util.CountLines(block.Output)
				newBlocks[j] = session.ContentBlock{
					Type:      block.Type,
					ToolUseID: block.ToolUseID,
					ToolName:  block.ToolName,
					Output:    fmt.Sprintf("[Old tool result content cleared — %d chars, %d lines. Use read_file to re-examine.]", charCount, lineCount),
					IsError:   block.IsError,
				}
			} else {
				newBlocks[j] = block
			}
		}
		result[i] = session.Message{
			Role:      msg.Role,
			Blocks:    newBlocks,
			Usage:     msg.Usage,
			Model:     msg.Model,
			Timestamp: msg.Timestamp,
			Tags:      msg.Tags,
		}
	}

	return result
}

// formatConversationHistory formats message list as conversation history text
// shared by BuildCompactPrompt and BuildCompactPromptV2 to avoid duplication
func formatConversationHistory(messages []session.Message) string {
	var sb strings.Builder

	for i, msg := range messages {
		roleLabel := ""
		switch msg.Role {
		case session.RoleUser:
			roleLabel = "User"
		case session.RoleAssistant:
			roleLabel = "Assistant"
		case session.RoleSystem:
			roleLabel = "system"
		case session.RoleTool:
			roleLabel = "tool"
		}

		for _, block := range msg.Blocks {
			text := ""
			switch block.Type {
			case "text":
				text = block.Text
			case "tool_use":
				text = fmt.Sprintf("[tool call: %s] %s", block.Name, block.Input)
			case "tool_result":
				text = fmt.Sprintf("[toolresult] %s", truncateRunes(block.Output, 200))
			case "thinking":
				continue
			}

			if text == "" {
				continue
			}

			if len(text) > 2000 {
				text = truncateRunes(text, 2000) + "..."
			}

			sb.WriteString(fmt.Sprintf("[%s] %s\n", roleLabel, text))
		}

		if i > 0 && i%5 == 0 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// BuildCompactPromptV2 builds the nine-section compact prompt
// Deprecated: Use BuildCompactPrompt in ai_compact.go instead.
// This function is kept for reference and testing; the nine-section format
// will be merged into BuildCompactPrompt in a future version.
func BuildCompactPromptV2(messages []session.Message) string {
	var sb strings.Builder

	sb.WriteString("Please generate a structured summary of the following conversation history。\n\n")
	sb.WriteString("<analysis>\n")
	sb.WriteString("Analyze the main content and direction of the conversation, identify key info, technical decisions and current state。\n")
	sb.WriteString("</analysis>\n\n")

	sb.WriteString("<summary>\n")
	sb.WriteString("1. main request: what is the user's core request and goal\n")
	sb.WriteString("2. technical concepts: key technical concepts and frameworks involved\n")
	sb.WriteString("3. files and code: which files were operated on, what modifications were made\n")
	sb.WriteString("4. errors and fixes: what errors were encountered, how they were fixed\n")
	sb.WriteString("5. problem resolution: what problems were resolved, what approaches were used\n")
	sb.WriteString("6. all user messages: list the core content of each user message\n")
	sb.WriteString("7. pending tasks: what tasks remain unfinished\n")
	sb.WriteString("8. current work: what work is currently in progress\n")
	sb.WriteString("9. optional next steps: recommended next actions\n")
	sb.WriteString("</summary>\n\n")

	sb.WriteString("--- Conversation History Start ---\n\n")
	sb.WriteString(formatConversationHistory(messages))
	sb.WriteString("\n--- Conversation History End ---\n\n")
	sb.WriteString("Please generate a structured summary following the <analysis> and <summary> format above：")

	return sb.String()
}

// PostCompactRecovery post-compactrecoverconfig
// used to recover recently read file content post-compaction, avoiding context loss
type PostCompactRecovery struct {
	// MaxRecoveredFiles is the max number of files to recover
	MaxRecoveredFiles int
	// MaxFileChars is the max characters to recover per file
	MaxFileChars int
	// RecoverPlan indicates whether to recover pending plan info
	RecoverPlan bool
}

// DefaultPostCompactRecovery returns the default post-compact recovery config
func DefaultPostCompactRecovery() PostCompactRecovery {
	return PostCompactRecovery{
		MaxRecoveredFiles: 5,
		MaxFileChars:      5000,
		RecoverPlan:       true,
	}
}

// Recover executepost-compactrecover
// recovers file content from recently read file list, truncates oversized files, returns recovered message list
// readFiles parameter is the list of recently read file paths, avoiding recursive dependency
func Recover(readFiles []string, sess *session.Session, config PostCompactRecovery) []session.Message {
	var recovered []session.Message

	// limitrecoverfilecount
	fileCount := len(readFiles)
	if fileCount > config.MaxRecoveredFiles {
		fileCount = config.MaxRecoveredFiles
	}

	// recover content from recently read files
	// take files from the end of the list (most recently read)
	startIdx := len(readFiles) - fileCount
	if startIdx < 0 {
		startIdx = 0
	}
	recentFiles := readFiles[startIdx:]

	for _, filePath := range recentFiles {
		// try to actually read file content, fall back to placeholder message on failure
		var content string
		data, err := os.ReadFile(filePath)
		if err == nil {
			fileContent := string(data)
			if len(fileContent) > config.MaxFileChars {
				fileContent = fileContent[:config.MaxFileChars] + "\n... (truncated)"
			}
			content = fmt.Sprintf("[Recovered file: %s]\n%s", filePath, fileContent)
		} else {
			content = fmt.Sprintf("[Recovered file: %s — content needs to be re-read via read_file]", filePath)
		}
		msg := session.Message{
			Role: session.RoleSystem,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: content},
			},
			Timestamp: time.Now().UnixMilli(),
		}
		recovered = append(recovered, msg)
	}

	// if plan recovery is enabled, extract pending tasks from session
	if config.RecoverPlan && sess != nil {
		planMsg := extractPendingPlan(sess.GetMessages())
		if planMsg != nil {
			recovered = append(recovered, *planMsg)
		}
	}

	return recovered
}

// extractPendingPlan extracts pending plan from message history
// scans assistant messages for to-do items and plan info, generates recovery message
func extractPendingPlan(messages []session.Message) *session.Message {
	var pendingItems []string

	for _, msg := range messages {
		if msg.Role != session.RoleAssistant {
			continue
		}
		for _, block := range msg.Blocks {
			if block.Type != "text" {
				continue
			}
			text := block.Text
			// extract to-do item markers
			extractPendingFromText(text, &pendingItems)
		}
	}

	if len(pendingItems) == 0 {
		return nil
	}

	// limit to-do item count
	if len(pendingItems) > 10 {
		pendingItems = pendingItems[:10]
	}

	var sb strings.Builder
	sb.WriteString("[Recovered pending plan]\n")
	for i, item := range pendingItems {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, item))
	}

	return &session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: sb.String()},
		},
		Timestamp: time.Now().UnixMilli(),
	}
}

// extractPendingFromText extracts to-do items from text
// identifies TODO, FIXME, pending, remaining and similar markers
func extractPendingFromText(text string, items *[]string) {
	lines := strings.Split(text, "\n")
	keywords := []string{"TODO", "FIXME", "pending", "remaining", "next step"}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, kw := range keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				item := truncateRunes(trimmed, 120)
				if len(*items) < 10 {
					*items = append(*items, item)
				}
				break
			}
		}
	}
}

// TimeBasedMicroCompactConfig time-based 微压缩配置（N11 新增，借鉴同类 CLI
// timeBasedMCConfig.ts:6-17 + microCompact.ts:422-510）。
//
// 触发条件：距上次 assistant 消息超过 GapThreshold（默认 60min，对齐服务端 1h cache TTL）。
// 行为：清除旧 tool_result 内容缩小重写量（cache TTL 已冷，重写不增加成本）。
type TimeBasedMicroCompactConfig struct {
	// Enabled 是否启用 time-based 微压缩（默认 true）
	Enabled bool
	// GapThreshold 距上次 assistant 消息的触发阈值（默认 60min，对齐 cache 1h TTL）
	GapThreshold time.Duration
	// KeepRecent 保留最近 N 条 tool_result 不被清除
	KeepRecent int
	// TruncationThreshold tool_result 内容超过此字符数才清除
	TruncationThreshold int
}

// DefaultTimeBasedMicroCompactConfig 返回默认 time-based 微压缩配置。
// 默认值对齐业内既有实现 timeBasedMCConfig.ts：60min 阈值 + 保留 5 条。
func DefaultTimeBasedMicroCompactConfig() TimeBasedMicroCompactConfig {
	return TimeBasedMicroCompactConfig{
		Enabled:             true,
		GapThreshold:        60 * time.Minute,
		KeepRecent:          5,
		TruncationThreshold: 500,
	}
}

// MaybeTimeBasedMicroCompact 若距上次 assistant 消息超过阈值，触发 time-based 微压缩。
//
// 借鉴同类 CLI：cache TTL 已冷（距上次 assistant > 60min），
// 清除旧 tool_result 内容缩小重写量。
//
// 参数：
// - messages：当前消息列表
// - config：time-based 微压缩配置
//
// 返回：
// - compacted：压缩后的消息列表（未触发时为原列表）
// - triggered：是否触发了 time-based 微压缩
func MaybeTimeBasedMicroCompact(messages []session.Message, config TimeBasedMicroCompactConfig) ([]session.Message, bool) {
	if !config.Enabled || len(messages) == 0 {
		return messages, false
	}

	// 查找最后一条 assistant 消息的时间戳
	var lastAssistantTime int64
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == session.RoleAssistant && messages[i].Timestamp != 0 {
			lastAssistantTime = messages[i].Timestamp
			break
		}
	}
	if lastAssistantTime == 0 {
		// 无 assistant 消息或时间戳为 0，不触发
		return messages, false
	}

	// 检查距上次 assistant 消息是否超过阈值
	now := time.Now().UnixMilli()
	gap := time.Duration(now-lastAssistantTime) * time.Millisecond
	if gap < config.GapThreshold {
		return messages, false
	}

	// 触发 time-based 微压缩：清除旧 tool_result 内容
	// 复用 MicroCompact 的截断逻辑，但使用更宽松的 TimeThreshold（覆盖所有 > 阈值的消息）
	microConfig := MicroCompactConfig{
		PreserveRecent:      config.KeepRecent,
		TimeThreshold:       config.GapThreshold, // 超过此阈值视为"远古"，清除 tool_result
		TruncationThreshold: config.TruncationThreshold,
	}
	compacted := MicroCompact(messages, microConfig)
	return compacted, !messagesSliceEqual(messages, compacted)
}
