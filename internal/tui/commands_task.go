package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	imgproc "github.com/anchorandfish/whale-code/internal/image"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// handleContextInfo returns current context info
func (m *Model) handleContextInfo() string {
	var cacheHitRate float64
	if m.sessionService.UsageTracker() != nil {
		cacheHitRate = m.sessionService.UsageTracker().CacheHitRate()
	}
	snapshot := m.SessionModel.requestSnapshot
	if snapshot.Valid && snapshot.Model != "" && snapshot.Model != m.sessionService.ModelName() {
		snapshot = contextRequestSnapshot{}
	}
	report := formatContextStatus(
		m.sessionService.CurrentSession(), m.sessionService.Config(), m.aiService.Estimator(),
		snapshot, m.sessionService.InputTokens(), m.sessionService.OutputTokens(),
		m.sessionService.RequestCount(),
		m.sessionService.CompactCount(), string(m.sessionService.Config().CompactionStrategy), cacheHitRate,
		m.aiService.AICompactor() != nil,
		m.calibrationSource, m.sessionService.ModelName(),
	)
	return report + formatCalibrationStatus(m.calibrationSource, m.sessionService.ModelName())
}

// formatCalibrationStatus 渲染 EMA 混合校准状态段（B-P4 可观测性）。
//
// /context 展示当前 model 的 ratio/offset/samples，用户可据此判断校准是否
// 在工作（ratio→1+offset→恒差 表示恒差场景收敛；ratio 偏离 1 表示比例误差）。
// 无 calibration source 或无采样数据时返回空串（不显示）。
func formatCalibrationStatus(source ai.TokenSource, model string) string {
	if source == nil {
		return ""
	}
	info, ok := source.CalibrationInfo(model)
	if !ok || info.SampleCount == 0 {
		return "\nCalibration 校准: no samples yet 尚无采样（对话 1 轮后建立）\n"
	}
	return fmt.Sprintf("\nCalibration 校准 (EMA hybrid 混合): ratio=%.3f offset=%.0f samples=%d\n"+
		"（校准公式: calibrated = estimated × ratio + offset；estimated 为完整口径原始估算）\n",
		info.Ratio, info.Offset, info.SampleCount)
}

// estimateCurrentTokens estimate current token count for all content blocks
func (m *Model) estimateCurrentTokens() int {
	total := 0
	estimator := m.aiService.Estimator()
	for _, msg := range m.sessionService.CurrentSession().GetMessages() {
		for _, block := range msg.Blocks {
			switch block.Type {
			case "text", "thinking":
				if block.Text != "" {
					total += estimator.EstimateTextTokens(block.Text)
				}
			case "tool_use":
				if block.Text != "" {
					total += estimator.EstimateTextTokens(block.Text)
				}
				if block.Input != "" {
					total += estimator.EstimateTextTokens(block.Input)
				}
			case "tool_result":
				if block.Output != "" {
					total += estimator.EstimateTextTokens(block.Output)
				}
			case "image":
				// Image token estimation: base64字符数 × 0.125
				if len(block.Base64Data) > 0 {
					total += imgproc.EstimateTokens(block.Base64Data)
				}
			default:
				// Fallback: estimate by text content length
				if block.Text != "" {
					total += estimator.EstimateTextTokens(block.Text)
				}
				if block.Output != "" {
					total += estimator.EstimateTextTokens(block.Output)
				}
				if block.Input != "" {
					total += estimator.EstimateTextTokens(block.Input)
				}
			}
		}
	}
	return total
}

// estimateCurrentInputContextTokens 估算下次请求的完整上下文 token
// （system + tools + messages）。
//
// 两项纠偏（对标同类 CLI footer 实时性）：
// 1. 流式实时性：StateStreaming 期间把当前流式文本计入估算（turn 结束后它会
// 成为 assistant 消息随下次请求重发），否则状态栏百分比在流式期间冻结、
// turn 结束才跳变。thinking 不计入：assistant 消息只持久化文本块
// （stream.go AssistantText），thinking 不会重发。
// 2. 估算偏差校准：provider 上报 usage 与本地估算的 EMA 偏差
// （RecordRemoteUsage 回流）通过 ApplyCalibration 应用到完整口径估算，
// 使状态栏/预警/自动压缩判定收敛到真实 usage 而非裸启发式值。
func (m *Model) estimateCurrentInputContextTokens() int {
	snapshot := m.SessionModel.requestSnapshot
	if snapshot.Valid && snapshot.Model != "" && snapshot.Model != m.sessionService.ModelName() {
		snapshot = contextRequestSnapshot{}
	}
	total := estimateCurrentInputContext(
		m.sessionService.CurrentSession(), m.aiService.Estimator(), snapshot,
	).Total
	if m.sessionService.State() == StateStreaming {
		// 流式文本真相源优先级：activeCell（生产路径，TextDeltaEvent 写入）>
		// sessionService.CurrentText（activeCell 未初始化时的兼容降级路径）。
		// 两者取非空者（activeCell Flush 后短暂为空时会回落到 CurrentText）。
		text := ""
		if m.activeCell != nil {
			text = m.activeCell.Text()
		}
		if text == "" {
			text = m.sessionService.CurrentText()
		}
		if text != "" {
			total += m.aiService.Estimator().EstimateTextTokens(text)
		}
	}
	if m.calibrationSource != nil {
		total = m.calibrationSource.ApplyCalibration(m.sessionService.ModelName(), total)
	}
	return total
}

// handleUsage handles /usage command — show token usage details
// handleUsage 处理 /usage 命令 — 显示 token 用量详情
func (m *Model) handleUsage() string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	if m.sessionService.UsageTracker() == nil {
		return "📊 Token 用量: 追踪器未初始化"
	}
	report := m.sessionService.UsageTracker().FormatUsageReport()
	if report == "" {
		return "📊 Token 用量: 暂无数据\n（对话后将显示用量统计）"
	}
	return report
}

// handleTasks handles /tasks command — show background task list
// handleTasks 处理 /tasks 命令 — 显示后台任务列表
func (m *Model) handleTasks(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	if m.cmdRegistry.OnDaemon != nil {
		result := m.cmdRegistry.OnDaemon("list" + " " + args)
		if result != "" {
			return result
		}
	}
	if m.toolService.Daemon() != nil {
		status := m.toolService.Daemon().Status()
		if status.Running {
			pending := status.PendingTasks
			running := status.RunningTasks
			if pending == 0 && running == 0 {
				return "📋 后台任务: 无运行中任务\n\n使用 /daemon add <提示词> 添加后台任务"
			}
			return fmt.Sprintf("📋 后台任务\n\n 运行中: %d\n 队列中: %d\n 工作线程: %d\n\n使用 /daemon log 查看日志",
				running, pending, status.Workers)
		}
	}
	return "📋 后台任务服务未启动\n\n使用 /daemon start 启动守护进程以运行后台任务"
}

// handleTaskCommand 处理 /task 子命令（N1 subagent / Task tool，）。
//
// 子命令：
// - /task list：列出所有 Agent 子任务（委托给 RenderTaskList）
// - /task cancel <id>：取消指定子任务（传播 cancel 到后代）
// - /task show <id>：显示单个子任务详情
//
// 与 handleTasks（daemon 后台任务）区分：本方法处理 Agent 编排子任务。
// AgentTaskMgr 未初始化时返回提示信息（不 panic）。
func (m *Model) handleTaskCommand(args string) string {
	m.reassignServices()

	// 获取 AgentTaskManager（通过 toolService.Executor().AgentTaskMgr）
	exec := m.toolService.Executor()
	if exec == nil || exec.AgentTaskMgr == nil {
		return "🤖 Sub-Agent Tasks: 管理器未初始化（请重启 Whale Code）"
	}
	mgr := exec.AgentTaskMgr

	// 解析子命令
	parts := strings.Fields(args)
	if len(parts) == 0 {
		// 默认 list
		return RenderTaskList(mgr)
	}

	switch parts[0] {
	case "list":
		return RenderTaskList(mgr)
	case "cancel":
		if len(parts) < 2 {
			return "用法: /task cancel <id>\n\n使用 /task list 查看任务 ID"
		}
		taskID := parts[1]
		// 支持短 ID（前 8 位）匹配：遍历 tasks 查找前缀
		fullID := resolveTaskID(mgr, taskID)
		if fullID == "" {
			return fmt.Sprintf("❌ 未找到任务: %s\n\n使用 /task list 查看所有任务", taskID)
		}
		if err := mgr.Cancel(fullID); err != nil {
			return fmt.Sprintf("❌ 取消失败: %v", err)
		}
		return fmt.Sprintf("🚫 已取消任务 %s（cancel 已传播到后代）", shortTaskID(fullID))
	case "show":
		if len(parts) < 2 {
			return "用法: /task show <id>\n\n使用 /task list 查看任务 ID"
		}
		taskID := parts[1]
		fullID := resolveTaskID(mgr, taskID)
		if fullID == "" {
			return fmt.Sprintf("❌ 未找到任务: %s\n\n使用 /task list 查看所有任务", taskID)
		}
		info, err := mgr.Get(fullID)
		if err != nil {
			return fmt.Sprintf("❌ 查询失败: %v", err)
		}
		return formatTaskDetail(info)
	default:
		return fmt.Sprintf("未知子命令: %s\n\n可用子命令: list / cancel <id> / show <id>", parts[0])
	}
}

// resolveTaskID 将短 ID（前 8 位）解析为完整任务 ID。
// 若 taskID 本身是完整 ID 则直接返回；否则在 manager.List() 中查找前缀匹配。
// 无匹配时返回空字符串。
func resolveTaskID(mgr *tools.AgentTaskManager, taskID string) string {
	// 先尝试直接 Get（完整 ID）
	if _, err := mgr.Get(taskID); err == nil {
		return taskID
	}
	// 短 ID 前缀匹配
	for _, info := range mgr.List() {
		if strings.HasPrefix(info.ID, taskID) {
			return info.ID
		}
	}
	return ""
}

// shortTaskID 截取任务 ID 前 8 位用于显示（与 task_list.go 的 shortID 保持一致）。
func shortTaskID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// formatTaskDetail 格式化单个任务详情（供 /task show <id> 使用）。
func formatTaskDetail(info tools.AgentTaskInfo) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🤖 Task %s\n\n", shortTaskID(info.ID)))
	sb.WriteString(fmt.Sprintf("  ID:       %s\n", info.ID))
	sb.WriteString(fmt.Sprintf("  ParentID: %s\n", orDash(info.ParentID)))
	sb.WriteString(fmt.Sprintf("  Depth:    %d\n", info.Depth))
	sb.WriteString(fmt.Sprintf("  Status:   %s\n", info.Status.String()))
	sb.WriteString(fmt.Sprintf("  Created:  %s\n", info.CreatedAt.Format("2006-01-02 15:04:05")))
	if !info.StartedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("  Started:  %s\n", info.StartedAt.Format("2006-01-02 15:04:05")))
	}
	if !info.EndedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("  Ended:    %s\n", info.EndedAt.Format("2006-01-02 15:04:05")))
	}
	return sb.String()
}

// orDash 空字符串返回 "-" 用于显示。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// handleContext handles /context command — show context details
// handleContext 处理 /context 命令 — 显示上下文详情
func (m *Model) handleContext() string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	return m.handleContextInfo()
}

// formatIntWithComma formats integer with comma separators
func formatIntWithComma(n int) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	var result []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, s[i])
	}
	return string(result)
}

// formatTokensForCount formats token count for stats display
// formatTokensForCount 将 token 数量格式化为带单位的简短字符串（截断不四舍五入）
func formatTokensForCount(n int) string {
	if n >= 1_000_000 {
		v := math.Floor(float64(n)/1_000_000*10) / 10
		return fmt.Sprintf("%.1fM", v)
	}
	if n >= 1_000 {
		v := math.Floor(float64(n)/1_000*10) / 10
		return fmt.Sprintf("%.1fk", v)
	}
	return fmt.Sprintf("%d", n)
}

// handleCost 已移除：provider 不返回费用详情，本地价格表估算与真实账单不符。
// token 用量统计保留在 /usage（UsageTracker.FormatUsageReport）。

// handleMark mark current position
func (m *Model) handleMark(args string) string {
	args = strings.TrimSpace(args)

	if args == "list" {
		if len(m.sessionService.Marks()) == 0 {
			return "No marks"
		}
		var sb strings.Builder
		sb.WriteString("📍 Session marks:\n")
		for i, mark := range m.sessionService.Marks() {
			sb.WriteString(fmt.Sprintf(" %d. %s — messages #%d (%s)\n",
				i+1, mark.Label, mark.MsgIndex, mark.CreatedAt.Format("15:04:05")))
		}
		return sb.String()
	}

	if args == "clear" {
		count := len(m.sessionService.Marks())
		m.sessionService.SetMarks(nil)
		return fmt.Sprintf("Cleared %d marks", count)
	}

	label := args
	if label == "" {
		label = fmt.Sprintf("mark-%d", len(m.sessionService.Marks())+1)
	}

	msgIndex := len(m.sessionService.CurrentSession().GetMessages())
	m.sessionService.SetMarks(append(m.sessionService.Marks(), sessionMark{
		Label:     label,
		MsgIndex:  msgIndex,
		CreatedAt: time.Now(),
	}))

	return fmt.Sprintf("📍 Marked position: %s (message #%d)", label, msgIndex)
}

// handleUnmark cancel marks
func (m *Model) handleUnmark(args string) string {
	args = strings.TrimSpace(args)

	if args == "all" {
		count := len(m.sessionService.Marks())
		m.sessionService.SetMarks(nil)
		return fmt.Sprintf("Cancelled all marks (%d)", count)
	}

	if args == "" {
		return "Usage: /unmark <label> or /unmark all"
	}

	for i, mark := range m.sessionService.Marks() {
		if mark.Label == args {
			marks := m.sessionService.Marks()
			m.sessionService.SetMarks(append(marks[:i], marks[i+1:]...))
			return fmt.Sprintf("Unmarked: %s", args)
		}
	}

	return fmt.Sprintf("Mark not found: %s", args)
}

// handleReview code review
func (m *Model) handleReview(args string) string {
	args = strings.TrimSpace(args)

	var prompt string
	if args == "" {
		prompt = "Please review the code changes in the current git diff, analyzing from four dimensions: security, performance, code style, and best practices"
	} else if args == "full" {
		prompt = "Please review the entire project code, performing a comprehensive analysis from four dimensions: security, performance, code style, and best practices"
	} else {
		prompt = fmt.Sprintf("Please review the code in file %s, analyzing from four dimensions: security, performance, code style, and best practices", args)
	}

	m.addMessage(cmdStyle.Render("🔍 " + prompt))
	m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserText(prompt)))
	m.sessionService.SetState(StateStreaming)
	m.resetStreamState()
	m.aiService.SetRequestID(fmt.Sprintf("%d", time.Now().UnixNano()))
	return ""
}

// handleTag tag management
func (m *Model) handleTag(args string) string {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "Usage: /tag add|remove|list|search <tag>"
	}

	subCmd := fields[0]
	switch subCmd {
	case "add":
		if len(fields) < 2 {
			return "Usage: /tag add <tag>"
		}
		tag := fields[1]
		m.sessionService.CurrentSession().Metadata.Tags = append(m.sessionService.CurrentSession().Metadata.Tags, tag)
		return fmt.Sprintf("Label added: %s", tag)
	case "remove":
		if len(fields) < 2 {
			return "Usage: /tag remove <tag>"
		}
		tag := fields[1]
		var newTags []string
		for _, t := range m.sessionService.CurrentSession().Metadata.Tags {
			if t != tag {
				newTags = append(newTags, t)
			}
		}
		m.sessionService.CurrentSession().Metadata.Tags = newTags
		return fmt.Sprintf("Label removed: %s", tag)
	case "list":
		if len(m.sessionService.CurrentSession().Metadata.Tags) == 0 {
			return "Current session has no tags"
		}
		return "Label: " + strings.Join(m.sessionService.CurrentSession().Metadata.Tags, ", ")
	case "search":
		if len(fields) < 2 {
			return "Usage: /tag search <tag>"
		}
		tag := fields[1]
		summaries, err := m.sessionService.Store().List()
		if err != nil {
			return fmt.Sprintf("Tag search failed: %v", err)
		}
		var matches []session.SessionSummary
		for _, s := range summaries {
			if strings.Contains(strings.ToLower(s.Name), strings.ToLower(tag)) {
				matches = append(matches, s)
			}
		}
		if len(matches) == 0 {
			return fmt.Sprintf("No sessions found containing \"%s\"", tag)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Search \"%s\" (%d):\n", tag, len(matches)))
		for _, r := range matches {
			sb.WriteString(fmt.Sprintf(" %s (%d turns)\n", r.Name, r.TurnCount))
		}
		return sb.String()
	default:
		return fmt.Sprintf("Unknown subcommand: %s\nUsage: /tag add|remove|list|search <tag>", subCmd)
	}
}

// estimateCost/formatCostStr 已随 /cost 命令移除（本地费用估算不可靠）。
