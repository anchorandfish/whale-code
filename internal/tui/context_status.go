package tui

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
)

// contextRequestSnapshot stores only the token breakdown of the last request.
// Keeping the rendered prompt out of this snapshot avoids retaining sensitive
// prompt text solely for /context.
type contextRequestSnapshot struct {
	SystemPrompt  int
	SessionSystem int
	Tools         int
	Model         string
	Valid         bool
}

type currentContextEstimate struct {
	SystemPrompt int
	Tools        int
	Messages     int
	Total        int
	KnownTotal   bool
}

func snapshotRequest(estimator *ai.TokenEstimator, req ai.Request, sessionSystemTokens int) contextRequestSnapshot {
	estimate := ai.EstimateRequestTokens(estimator, req)
	return contextRequestSnapshot{
		SystemPrompt:  estimate.SystemPrompt,
		SessionSystem: sessionSystemTokens,
		Tools:         estimate.Tools,
		Model:         req.Model,
		Valid:         true,
	}
}

// fixedRequestOverhead 从请求快照计算固定开销 = 静态 system prompt + 工具定义。
// 会话摘要（SessionSystem）随压缩产生/变化，不计入固定部分；
// 压缩只能作用于对话消息，因此 fixed >= context_window 时压缩无法缓解。
// 防御：异常快照（SessionSystem > SystemPrompt）时退化为全量 system+tools，避免负值。
func fixedRequestOverhead(snapshot contextRequestSnapshot) int {
	fixed := snapshot.SystemPrompt - snapshot.SessionSystem + snapshot.Tools
	if fixed < 0 {
		fixed = snapshot.SystemPrompt + snapshot.Tools
	}
	return fixed
}

// formatFixedOverheadWarning 生成"窗口小于固定开销"提示（压缩无法缓解的场景）。
// 触发条件见 fixedOverheadExceedsWindow；文案同时给出中英文与修复建议。
func formatFixedOverheadWarning(fixed, window int) string {
	return fmt.Sprintf(
		"⚠️ Fixed overhead %s tokens (system prompt + tools) ≥ context_window %s: compaction cannot free context and this will re-trigger every turn.\n"+
			"   固定开销（系统提示词+工具定义）已达到窗口上限，压缩无法缓解，每轮对话都会重复触发。\n"+
			"   Fix 建议: 增大 context_window（settings.json 或 WHALE_CONTEXT_WINDOW）。",
		formatIntWithComma(fixed), formatIntWithComma(window))
}

func estimateCurrentInputContext(sess *session.Session, estimator *ai.TokenEstimator, snapshot contextRequestSnapshot) currentContextEstimate {
	if sess == nil || estimator == nil {
		return currentContextEstimate{KnownTotal: snapshot.Valid}
	}
	messages := sess.GetMessages()
	conversationTokens := estimator.EstimateMessagesTokens(sessionMessagesToAPI(messages))
	sessionSystemTokens := estimateSessionSystemPromptTokens(estimator, messages)
	currentSystemPromptTokens := sessionSystemTokens
	if snapshot.Valid {
		baseSystemPromptTokens := snapshot.SystemPrompt - snapshot.SessionSystem
		if baseSystemPromptTokens < 0 {
			baseSystemPromptTokens = 0
		}
		currentSystemPromptTokens += baseSystemPromptTokens
	}
	result := currentContextEstimate{
		SystemPrompt: currentSystemPromptTokens,
		Messages:     conversationTokens,
		KnownTotal:   snapshot.Valid,
	}
	if snapshot.Valid {
		result.Tools = snapshot.Tools
		result.Total = result.SystemPrompt + result.Tools + result.Messages
	} else {
		result.Total = sessionSystemTokens + conversationTokens
	}
	return result
}

// formatMessageComposition 渲染对话内容 token 的**块级构成**（第 0 批「仪表」）。
//
// 与上面 Messages 行的关系必须说清，否则用户会拿子项去和那一行对账：
//
//   - Messages 行按**请求形态**估算（EstimateMessagesTokens(sessionMessagesToAPI(…))）——
//     它把 tool_result 折成 text、且不单独计 tool_use / thinking；
//   - 本段按**会话块类型**归因（EstimateSessionMessageTokens）——**另一把尺子**，
//     用途是看**比例**（"我的上下文里，工具结果占多少"）。
//
// 因此这里只报各类型的绝对量与占比，并给一行**小计**（= 各桶之和，等于 Messages 行
// 减去每消息的 4 token 信封开销），免得用户自己加、又以为对不上。
// 占比才是有用的那部分：工具结果是编码 agent 里最大、也最可过滤的一块。
//
// 纯函数（无 I/O、无全局态）：便于单测钉"零值桶不渲染 / 百分比正确"。
func formatMessageComposition(comp ai.SessionMessageTokens) string {
	if comp.Total <= 0 {
		return ""
	}
	rows := []struct {
		label  string
		tokens int
	}{
		{"Tool results 工具结果", comp.ToolResults},
		{"Conversation 对话正文", comp.Conversation},
		{"Tool calls 工具调用", comp.ToolUse},
		{"Thinking 思考", comp.Thinking},
		{"Images 图片", comp.Images},
	}
	var sb strings.Builder
	for _, r := range rows {
		if r.tokens <= 0 {
			continue // 零值桶不渲染，避免面板被 0% 行塞满
		}
		sb.WriteString(fmt.Sprintf("   %s: ~%s (%s)\n",
			r.label, formatIntWithComma(r.tokens), formatShare(float64(r.tokens)/float64(comp.Total))))
	}
	// 小计：让用户不必自己去加，也把"与上面的 Messages 行为何差一点点"讲在明处
	// （差额 = sessionMessagesToAPI 每条消息的 4 token 信封开销）。
	sb.WriteString(fmt.Sprintf("   Subtotal 小计: ~%s（不含每消息信封开销 envelope overhead excluded）\n",
		formatIntWithComma(comp.Total)))
	return sb.String()
}

// formatShare 把占比渲染成人类可读形态：<1% 不显示为"0%"（否则小桶看起来像空），
// 其余取整。
func formatShare(frac float64) string {
	pct := frac * 100
	if pct > 0 && pct < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", pct)
}

func formatContextStatus(sess *session.Session, cfg *config.Config, estimator *ai.TokenEstimator, snapshot contextRequestSnapshot, inputTokens, outputTokens, requestCount, compactCount int, strategy string, cacheHitRate float64, aiReady bool, calSrc ai.TokenSource, model string) string {
	if sess == nil {
		return "Context info:\n  No active session"
	}
	messages := sess.GetMessages()
	estimate := estimateCurrentInputContext(sess, estimator, snapshot)
	messageTokens := estimate.Messages
	window := 0
	if cfg != nil {
		window = cfg.ContextWindow
	}
	knownTotal := estimate.KnownTotal
	currentSystemPromptTokens := estimate.SystemPrompt
	// 口径一致性修复：Used 与状态栏同源——应用 EMA 混合校准（calibrated = raw × ratio + offset）。
	// 此前 Used 显示原始估算而状态栏显示校准值，两条路径不一致（实测 6290 vs 5674）。
	// Composition 明细（system/tools/messages）保持原始本地估算（分项无独立校准意义）。
	total := estimate.Total
	if calSrc != nil {
		total = calSrc.ApplyCalibration(model, total)
	}
	remaining := window - total
	if remaining < 0 {
		remaining = 0
	}
	percent := 0.0
	if window > 0 {
		percent = float64(total) / float64(window) * 100
	}

	var sb strings.Builder
	sb.WriteString("📐 Context Info 上下文信息\n\nWindow 窗口:\n")
	if window > 0 {
		sb.WriteString(fmt.Sprintf(" Total Capacity 总容量: %s（model config 当前模型配置）\n", formatIntWithComma(window)))
	} else {
		sb.WriteString(" Total Capacity 总容量: unknown 未知（model has no context window configured 当前模型未配置上下文窗口）\n")
	}
	if knownTotal {
		sb.WriteString(fmt.Sprintf(" Used 已使用（下次请求估算 next-request estimate）: ~%s (%.1f%%)\n", formatIntWithComma(total), percent))
		if window > 0 {
			sb.WriteString(fmt.Sprintf(" Remaining 剩余: ~%s（theoretical, output reserve not deducted 理论值，未扣除本次输出预留）\n", formatIntWithComma(remaining)))
		} else {
			sb.WriteString(" Remaining 剩余: unknown 未知（window capacity not configured 窗口容量未配置）\n")
		}
		if percent > 80 {
			sb.WriteString(" ⚠️ Context usage is high, consider /compact 上下文占用较高，建议使用 /compact 压缩\n")
		}
	} else {
		sb.WriteString(fmt.Sprintf(" Used 已使用（下次请求估算）: at least ~%s（no request sent yet, system prompt/tools unknown 尚未发送请求，系统提示词/工具未知）\n", formatIntWithComma(total)))
		sb.WriteString(" Remaining 剩余: unknown 未知（only lower bound available 仅能给出下限）\n")
	}

	sb.WriteString("\nComposition 组成:\n")
	if knownTotal {
		sb.WriteString(fmt.Sprintf(" System Prompt 系统提示词: ~%s（static part of last request + current session summary, local estimate 最近一次请求的静态部分 + 当前会话摘要，本地估算）\n", formatIntWithComma(currentSystemPromptTokens)))
		sb.WriteString(fmt.Sprintf(" Tool Definitions 工具定义: ~%s（last request, local estimate 最近一次请求，本地估算）\n", formatIntWithComma(estimate.Tools)))
	} else {
		sb.WriteString(" System Prompt 系统提示词: unknown 未知（no request sent yet 尚未发送请求）\n")
		sb.WriteString(" Tool Definitions 工具定义: unknown 未知（no request sent yet 尚未发送请求）\n")
	}
	sb.WriteString(fmt.Sprintf(" Messages 对话消息: ~%s（local estimate, %d msgs; system summary counted in system prompt 本地估算，%d 条；system 摘要计入系统提示词）\n", formatIntWithComma(messageTokens), len(messages), len(messages)))

	// 第 0 批「仪表」：把 Messages 这一整块拆到"可据以行动"的粒度。
	// 只加显示、不改任何驱动压缩/预算的数（Total 仍来自 estimateCurrentInputContext）。
	if estimator != nil {
		if comp := estimator.EstimateSessionMessageTokens(messages); comp.Total > 0 {
			sb.WriteString("   By content type 按内容类型（block-level estimate 块级估算，用于看占比 proportions）:\n")
			sb.WriteString(formatMessageComposition(comp))
		}
	}

	sb.WriteString("\nCompaction 压缩:\n")
	sb.WriteString(fmt.Sprintf(" Strategy 策略: %s\n", displayCompactionStrategy(strategy)))
	sb.WriteString(fmt.Sprintf(" Compacted 已压缩: %d times 次\n", compactCount))
	sb.WriteString(" Estimated Savings 预计节省: not calculated 未计算（summary size varies, may increase token count 每次摘要大小不同，可能不降反升）\n")
	if aiReady {
		sb.WriteString("\nAI Compactor AI 压缩器: ready 就绪\n")
	}
	sb.WriteString("\nAPI Usage (cumulative) 本进程当前会话 API 用量（累计）:\n")
	sb.WriteString(fmt.Sprintf(" Requests 请求数: %d\n", requestCount))
	sb.WriteString(fmt.Sprintf(" Input 输入: %s | Output 输出: %s（provider usage）\n", formatIntWithComma(inputTokens), formatIntWithComma(outputTokens)))
	if cacheHitRate > 0 {
		sb.WriteString(fmt.Sprintf(" Cache Hit Rate 缓存命中率: %.1f%%\n", cacheHitRate*100))
	}
	return sb.String()
}

func displayCompactionStrategy(strategy string) string {
	switch strategy {
	case "preserve_user", "preserve-user":
		return "保留用户消息（preserve-user）"
	case "aggressive":
		return "激进（aggressive）"
	default:
		return "均衡（balanced）"
	}
}
