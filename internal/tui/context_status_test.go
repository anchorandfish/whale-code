package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestEstimateCurrentInputContextIncludesStreamingText 实时性：流式响应期间
// 状态栏的 ctx 估算必须包含正在生成的流式文本（finishTurn 后它会成为 assistant
// 消息并计入下次请求），否则流式期间百分比冻结不动、turn 结束才跳变——
// 对标同类 CLI 状态栏在流式期间实时爬升的行为。
// thinking 不计入：assistant 消息只持久化文本（stream.go:101 AssistantText），
// thinking 块不会随下次请求重发。
func TestEstimateCurrentInputContextIncludesStreamingText(t *testing.T) {
	m := createMinimalModel()
	m.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 1000, Tools: 2000, Model: "test-model", Valid: true,
	}
	m.sessionService.CurrentSession().AddMessage(session.UserText("hi"))
	base := m.estimateCurrentInputContextTokens()

	m.sessionService.SetState(StateStreaming)
	m.sessionService.SetCurrentText(strings.Repeat("streaming answer content ", 40))
	m.sessionService.SetCurrentThinking("thinking draft")

	withStream := m.estimateCurrentInputContextTokens()
	if withStream <= base {
		t.Fatalf("流式期间估算(%d)应大于基线(%d)：流式文本未计入下次请求估算", withStream, base)
	}
	// 流式文本估算应有可感知的量级（40 次重复 ≈ 1000+ tokens）
	if withStream-base < 100 {
		t.Fatalf("流式增量过小(%d tokens)，疑似未完整计入流式文本", withStream-base)
	}

	// 空闲状态不受影响：turn 结束后流文本已持久化为消息，currentText 清空
	m.sessionService.SetState(StateIdle)
	m.sessionService.SetCurrentText("")
	idle := m.estimateCurrentInputContextTokens()
	if idle != base {
		t.Fatalf("空闲状态估算(%d)应回到基线(%d)", idle, base)
	}
}

// TestEstimateCurrentInputContextIncludesActiveCellText 生产路径：TextDeltaEvent
// 写入的是 activeCell（流式文本真相源），CurrentText 仅是 activeCell 未初始化时的
// 降级路径。若估算只读 CurrentText，流式期间 activeCell 累积的文本不会被计入
// （用户实测：状态栏流式期间冻结、turn 结束才跳变）。本测试钉死 activeCell 优先。
func TestEstimateCurrentInputContextIncludesActiveCellText(t *testing.T) {
	m := createMinimalModel()
	m.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 1000, Tools: 2000, Model: "test-model", Valid: true,
	}
	m.sessionService.CurrentSession().AddMessage(session.UserText("hi"))
	base := m.estimateCurrentInputContextTokens()

	m.sessionService.SetState(StateStreaming)
	m.activeCell = NewActiveCell()
	for i := 0; i < 40; i++ {
		m.activeCell.AppendText("streaming answer content ")
	}

	withStream := m.estimateCurrentInputContextTokens()
	if withStream <= base {
		t.Fatalf("流式期间估算(%d)应大于基线(%d)：activeCell 文本未计入", withStream, base)
	}
	if withStream-base < 100 {
		t.Fatalf("流式增量过小(%d tokens)，疑似未完整计入 activeCell 文本", withStream-base)
	}

	// Flush（turn 结束）后 activeCell 清空，估算回落
	m.activeCell.Flush()
	if got := m.estimateCurrentInputContextTokens(); got != base {
		t.Fatalf("Flush 后估算(%d)应回到基线(%d)", got, base)
	}
}

// TestEstimateCurrentInputContextAppliesCalibration 估算偏差校准：provider 上报
// usage 与本地估算的 EMA 偏差（RecordRemoteUsage 回流）应应用到 TUI 上下文估算，
// 使状态栏/预警/自动压缩的判定基于校准后的值而非裸启发式估算。
func TestEstimateCurrentInputContextAppliesCalibration(t *testing.T) {
	m := createMinimalModel()
	m.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 1000, Tools: 2000, Model: "test-model", Valid: true,
	}
	m.sessionService.CurrentSession().AddMessage(session.UserText("hello world"))
	raw := m.estimateCurrentInputContextTokens()

	// 注入校准样本：实际 input 比估算高 500 → 混合校准首轮 ratio 全额吸纳
	// calibrated = raw × (raw+500)/raw = raw+500（B-P3 混合语义，int 截断容差 ±2）
	ts := ai.NewTokenSource(ai.NewTokenEstimator(), nil, false, 30*time.Minute)
	ts.RecordRemoteUsage("test-model", ai.UnifiedUsage{InputTokens: raw + 500}, raw)
	m.calibrationSource = ts

	calibrated := m.estimateCurrentInputContextTokens()
	if calibrated < raw+498 || calibrated > raw+502 {
		t.Fatalf("校准后估算(%d)应≈ raw(%d)+500，校准未生效", calibrated, raw)
	}

	// 无校准数据时不改变估算（nil 防御）
	m.calibrationSource = nil
	if got := m.estimateCurrentInputContextTokens(); got != raw {
		t.Fatalf("nil 校准源时估算(%d)应等于 raw(%d)", got, raw)
	}
}

// TestRenderStatusBarUsesFullContextEstimate 口径一致性：状态栏 ctx 数字必须与
// 预警/自动压缩判定（estimateCurrentInputContextTokens，含 system+tools+messages）一致，
// 否则出现"状态栏 10% 但预警说 75%"的自相矛盾（用户实测 W=8000 时 ctx 10/8000）。
// 对标同类 CLI：footer 显示 last_token_usage.total_tokens（服务端上报的完整请求 token）。
func TestRenderStatusBarUsesFullContextEstimate(t *testing.T) {
	m := createMinimalModel()
	m.width = 300
	m.sessionService.CurrentSession().AddMessage(session.UserText("hello world"))
	m.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 1000, Tools: 2000, Model: "test-model", Valid: true,
	}

	full := m.estimateCurrentInputContextTokens()
	msgOnly := m.estimateCurrentTokens()
	if full <= msgOnly {
		t.Fatalf("测试前置失败：完整估算(%d)应大于消息估算(%d)", full, msgOnly)
	}

	bar := m.renderStatusBar()
	if !strings.Contains(bar, fmt.Sprintf("Context: %d/", full)) {
		t.Errorf("状态栏应显示完整口径估算 %d（含 system+tools），got: %s", full, bar)
	}
	if strings.Contains(bar, fmt.Sprintf("Context: %d/", msgOnly)) {
		t.Errorf("状态栏不应显示仅消息口径 %d，got: %s", msgOnly, bar)
	}
}

func TestFormatContextStatusSeparatesCurrentContextFromCumulativeUsage(t *testing.T) {
	sess := session.NewSession()
	sess.AddMessage(session.UserText("CONTEXT-A"))
	sess.AddMessage(session.AssistantText("CONTEXT-A", "model", nil))
	cfg := config.DefaultConfig()
	cfg.ContextWindow = 131072

	got := formatContextStatus(
		sess, &cfg, ai.NewTokenEstimator(),
		contextRequestSnapshot{SystemPrompt: 2000, Tools: 3000, Valid: true},
		40866, 151, 7, 1, "balanced", 0, true,
		nil, "",
	)
	for _, want := range []string{
		"系统提示词: ~2,000", "工具定义: ~3,000", "对话消息:", "2 条",
		"总容量: 131,072（model config 当前模型配置）", "未扣除本次输出预留",
		"输入: 40,866 | Output 输出: 151（provider usage）", "预计节省: not calculated 未计算",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "已使用: ~41,017") {
		t.Fatalf("cumulative API traffic must not be shown as current context:\n%s", got)
	}
}

// TestFormatContextStatusLabelsUsedAsNextRequestEstimateAndCountsRequests 用户反馈：
// "Used 已使用"被误读为"累计已消耗的 API token"，实际它是下一次请求将携带的
// 上下文估算（system+tools+messages）；且看不到"到目前为止发了多少次请求"。
// 修改：Used 行标注"下次请求估算"，API Usage 区块新增 "Requests 请求数" 行。
func TestFormatContextStatusLabelsUsedAsNextRequestEstimateAndCountsRequests(t *testing.T) {
	sess := session.NewSession()
	sess.AddMessage(session.UserText("hello"))
	cfg := config.DefaultConfig()
	cfg.ContextWindow = 131072

	got := formatContextStatus(
		sess, &cfg, ai.NewTokenEstimator(),
		contextRequestSnapshot{SystemPrompt: 2000, Tools: 3000, Valid: true},
		40866, 151, 12, 1, "balanced", 0, false,
		nil, "",
	)
	if !strings.Contains(got, "Used 已使用（下次请求估算 next-request estimate）: ~") {
		t.Fatalf("Used 行应标注为下次请求估算:\n%s", got)
	}
	if !strings.Contains(got, "Requests 请求数: 12") {
		t.Fatalf("API Usage 区块应显示请求数:\n%s", got)
	}
}

// TestFormatContextStatusUsedAppliesCalibration 口径一致性（用户实测 bug）：
// 状态栏 Context 用 estimateCurrentInputContextTokens（含 EMA 校准），
// 而 /context 的 Used 走 estimateCurrentInputContext（无校准）→ 两条路径
// 显示不一致（实测状态栏 6290 vs Used 5674，6290 = 5674×1.109 校准后）。
// 修复后：formatContextStatus 的 Used/Remaining/percent 均应用校准，与状态栏同源。
func TestFormatContextStatusUsedAppliesCalibration(t *testing.T) {
	sess := session.NewSession()
	sess.AddMessage(session.UserText("hello world"))
	cfg := config.DefaultConfig()
	cfg.ContextWindow = 131072
	estimator := ai.NewTokenEstimator()
	snapshot := contextRequestSnapshot{SystemPrompt: 2000, Tools: 3000, Valid: true}

	raw := estimateCurrentInputContext(sess, estimator, snapshot)
	if raw.Total <= 0 {
		t.Fatalf("测试前置失败：估算 Total=%d", raw.Total)
	}

	// 注入校准：actual=5500/estimated=5000 → ratio=1.1（首轮全额吸纳，offset=0）
	ts := ai.NewTokenSource(ai.NewTokenEstimator(), nil, false, 30*time.Minute)
	ts.RecordRemoteUsage("test-model", ai.UnifiedUsage{InputTokens: 5500}, 5000)

	got := formatContextStatus(
		sess, &cfg, estimator, snapshot,
		0, 0, 0, 0, "balanced", 0, false,
		ts, "test-model",
	)

	wantCalibrated := int(float64(raw.Total) * 1.1)
	if !strings.Contains(got, fmt.Sprintf("Used 已使用（下次请求估算 next-request estimate）: ~%s", formatIntWithComma(wantCalibrated))) {
		t.Fatalf("Used 应显示校准后估算 ~%s（原始 %d × ratio 1.1），actual:\n%s", formatIntWithComma(wantCalibrated), raw.Total, got)
	}
	// 不应显示未校准原值（校准生效时）
	if strings.Contains(got, fmt.Sprintf("Used 已使用（下次请求估算 next-request estimate）: ~%s (", formatIntWithComma(raw.Total))) {
		t.Fatalf("Used 仍显示未校准原值 ~%s，校准未应用:\n%s", formatIntWithComma(raw.Total), got)
	}
}

// TestFormatContextStatusNilCalibrationSourceKeepsRawEstimate nil 校准源
// （无 TokenSource 注入的降级场景）时 Used 保持原始估算，不崩溃。
func TestFormatContextStatusNilCalibrationSourceKeepsRawEstimate(t *testing.T) {
	sess := session.NewSession()
	sess.AddMessage(session.UserText("hello"))
	cfg := config.DefaultConfig()
	cfg.ContextWindow = 131072
	estimator := ai.NewTokenEstimator()
	snapshot := contextRequestSnapshot{SystemPrompt: 2000, Tools: 3000, Valid: true}

	raw := estimateCurrentInputContext(sess, estimator, snapshot)
	got := formatContextStatus(
		sess, &cfg, estimator, snapshot,
		0, 0, 0, 0, "balanced", 0, false,
		nil, "",
	)
	if !strings.Contains(got, fmt.Sprintf("Used 已使用（下次请求估算 next-request estimate）: ~%s", formatIntWithComma(raw.Total))) {
		t.Fatalf("nil 校准源时 Used 应保持原始估算 ~%s:\n%s", formatIntWithComma(raw.Total), got)
	}
}

func TestFormatContextStatusUnknownConfiguredWindow(t *testing.T) {
	sess := session.NewSession()
	cfg := config.DefaultConfig()
	cfg.ContextWindow = 0
	got := formatContextStatus(
		sess, &cfg, ai.NewTokenEstimator(),
		contextRequestSnapshot{SystemPrompt: 10, Tools: 20, Valid: true},
		0, 0, 0, 0, "balanced", 0, false,
		nil, "",
	)
	for _, want := range []string{"总容量: unknown 未知", "已使用（下次请求估算 next-request estimate）: ~30", "剩余: unknown 未知"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatContextStatusWithoutSnapshotShowsLowerBound(t *testing.T) {
	sess := session.NewSession()
	sess.AddMessage(session.UserText("hello"))
	cfg := config.DefaultConfig()

	got := formatContextStatus(sess, &cfg, ai.NewTokenEstimator(), contextRequestSnapshot{}, 0, 0, 0, 0, "preserve_user", 0, false, nil, "")
	for _, want := range []string{"at least", "系统提示词: unknown 未知", "工具定义: unknown 未知", "剩余: unknown 未知", "preserve-user"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatContextStatusReplacesSnapshottedSessionSummaryInsteadOfDoubleCounting(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	sess := session.NewSession()
	summary := session.Message{
		Role:   session.RoleSystem,
		Blocks: []session.ContentBlock{{Type: "text", Text: "summary CONTEXT-A"}},
	}
	sess.SetMessages([]session.Message{summary, session.UserText("recent")})
	sessionTokens := estimateSessionSystemPromptTokens(estimator, sess.GetMessages())
	snapshot := contextRequestSnapshot{
		SystemPrompt:  2000 + sessionTokens,
		SessionSystem: sessionTokens,
		Tools:         3000,
		Valid:         true,
	}

	got := formatContextStatus(sess, &config.Config{ContextWindow: 131072}, estimator, snapshot, 0, 0, 0, 1, "balanced", 0, false, nil, "")
	wantSystemLine := "系统提示词: ~" + formatIntWithComma(2000+sessionTokens)
	if !strings.Contains(got, wantSystemLine) {
		t.Fatalf("session summary was not replaced exactly once; want %q:\n%s", wantSystemLine, got)
	}
}

func TestCurrentInputContextEstimateIncludesSystemToolsAndMessages(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	sess := session.NewSession()
	sess.SetMessages([]session.Message{session.UserText("recent")})
	snapshot := contextRequestSnapshot{SystemPrompt: 39000, Tools: 2000, Model: "model", Valid: true}

	estimate := estimateCurrentInputContext(sess, estimator, snapshot)
	if !estimate.KnownTotal || estimate.SystemPrompt != 39000 || estimate.Tools != 2000 {
		t.Fatalf("estimate lost request snapshot: %#v", estimate)
	}
	if estimate.Messages <= 0 || estimate.Total != 39000+2000+estimate.Messages {
		t.Fatalf("estimate total excludes live messages: %#v", estimate)
	}
	if state := ctxpkg.DefaultBudget(8000).CheckState(estimate.Total, 0); state != ctxpkg.StateBlocked {
		t.Fatalf("system/tools-heavy request state=%v, want blocked", state)
	}
}

// TestFormatContextStatusShowsMessageComposition 钉第 0 批「仪表」的**接线**：
// 会话里有一段大 tool_result 时，/context 的产物必须出现「按内容类型」归因，
// 且 Tool results 一行给出非零量。光有纯函数单测不够 —— 判据要落在生产入口
// formatContextStatus 的真实输出上（R2：红测落在生产入口层）。
//
// 判别性：把 formatContextStatus 里那段渲染删掉，本用例转红（"未出现归因段"）。
func TestFormatContextStatusShowsMessageComposition(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	sess := session.NewSession()
	sess.SetMessages([]session.Message{
		session.UserText("please read it"),
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "t1", ToolName: "read_file", Output: strings.Repeat("x", 8000)},
		}},
	})
	cfg := &config.Config{ContextWindow: 200000}

	got := formatContextStatus(sess, cfg, estimator,
		contextRequestSnapshot{SystemPrompt: 1000, Tools: 500, Valid: true},
		0, 0, 0, 0, "balanced", 0, false, nil, "")

	if !strings.Contains(got, "By content type 按内容类型") {
		t.Fatalf("/context 未出现内容类型归因段：\n%s", got)
	}
	if !strings.Contains(got, "Tool results 工具结果: ~") {
		t.Fatalf("归因段缺少 Tool results 行：\n%s", got)
	}
	// Messages 行本身必须仍在（本批是"加法"，不得吃掉既有行）
	if !strings.Contains(got, "Messages 对话消息: ~") {
		t.Fatalf("Messages 行被吃掉（本批只应新增，不应替换）：\n%s", got)
	}

	// 空会话不得渲染该段（避免占位噪声）
	empty := formatContextStatus(session.NewSession(), cfg, estimator,
		contextRequestSnapshot{SystemPrompt: 1000, Tools: 500, Valid: true},
		0, 0, 0, 0, "balanced", 0, false, nil, "")
	if strings.Contains(empty, "By content type") {
		t.Fatalf("空会话不应渲染归因段：\n%s", empty)
	}
}

// TestFormatMessageComposition 钉归因段渲染的纯函数行为：零值桶不渲染、
// 小桶显示 <1% 而不是"0%"（0% 看起来像空桶）、小计等于各桶之和。
func TestFormatMessageComposition(t *testing.T) {
	comp := ai.SessionMessageTokens{
		ToolResults: 4200, Conversation: 22, ToolUse: 13, Thinking: 555, Total: 4790,
	}
	got := formatMessageComposition(comp)
	for _, want := range []string{
		"Tool results 工具结果: ~4,200 (88%)",
		"Thinking 思考: ~555 (12%)",
		"Conversation 对话正文: ~22 (<1%)",
		"Subtotal 小计: ~4,790",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("归因段缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Images 图片") {
		t.Errorf("零值桶（Images=0）不应渲染：\n%s", got)
	}
	if got := formatMessageComposition(ai.SessionMessageTokens{}); got != "" {
		t.Errorf("Total=0 时应返回空串，得 %q", got)
	}
}

// TestShouldShowApproachingWarning 验证"接近压缩阈值"一次性预警的门控（问题①）。
// 直接跳到 ShouldCompact/Blocked 时不应显示"接近"文案（此时语义误导，
// 后续分支会直接宣布压缩动作），仅在恰好处于 Warning 档且此前未达 Warning 时提示。
func TestShouldShowApproachingWarning(t *testing.T) {
	cases := []struct {
		name      string
		state     ctxpkg.State
		lastState ctxpkg.State
		want      bool
	}{
		{"OK→Warning 首次进入应提示", ctxpkg.StateWarning, ctxpkg.StateOk, true},
		{"OK→ShouldCompact 直跳应跳过", ctxpkg.StateShouldCompact, ctxpkg.StateOk, false},
		{"OK→Blocked 直跳应跳过（用户实测 4127% 场景）", ctxpkg.StateBlocked, ctxpkg.StateOk, false},
		{"Warning→Warning 已提示过不再重复", ctxpkg.StateWarning, ctxpkg.StateWarning, false},
		{"ShouldCompact→Blocked 升档不提示", ctxpkg.StateBlocked, ctxpkg.StateShouldCompact, false},
		{"OK→OK 正常不提示", ctxpkg.StateOk, ctxpkg.StateOk, false},
	}
	for _, tc := range cases {
		if got := shouldShowApproachingWarning(tc.state, tc.lastState); got != tc.want {
			t.Errorf("%s: shouldShowApproachingWarning(%v, %v) = %v, want %v",
				tc.name, tc.state, tc.lastState, got, tc.want)
		}
	}
}

// TestFixedRequestOverhead 验证固定开销计算（静态 system prompt + 工具定义）。
func TestFixedRequestOverhead(t *testing.T) {
	cases := []struct {
		name     string
		snapshot contextRequestSnapshot
		want     int
	}{
		{"常规快照", contextRequestSnapshot{SystemPrompt: 1835, SessionSystem: 0, Tools: 4338, Valid: true}, 6173},
		{"含会话摘要需扣除", contextRequestSnapshot{SystemPrompt: 2000, SessionSystem: 165, Tools: 4338, Valid: true}, 6173},
		{"异常快照（SessionSystem > SystemPrompt）防御回退", contextRequestSnapshot{SystemPrompt: 100, SessionSystem: 200, Tools: 50, Valid: true}, 150},
	}
	for _, tc := range cases {
		if got := fixedRequestOverhead(tc.snapshot); got != tc.want {
			t.Errorf("%s: fixedRequestOverhead = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestFixedOverheadExceedsWindow 验证"窗口小于固定开销"检测（问题②）。
// 用户实测场景：window=150，system+tools=6173，压缩 messages 2->0 后仍 4115%。
func TestFixedOverheadExceedsWindow(t *testing.T) {
	m := createMinimalModel()
	m.AIModel.budget = ctxpkg.DefaultBudget(150)
	m.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 1835, SessionSystem: 0, Tools: 4338, Model: "test-model", Valid: true,
	}

	fixed, window, exceeded := m.fixedOverheadExceedsWindow()
	if !exceeded {
		t.Fatalf("fixed=6173 window=150 时应报告 exceeded")
	}
	if fixed != 6173 || window != 150 {
		t.Errorf("fixed=%d window=%d, want 6173/150", fixed, window)
	}

	warning := formatFixedOverheadWarning(fixed, window)
	for _, want := range []string{"6,173", "150", "固定开销", "context_window"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning 缺少 %q:\n%s", want, warning)
		}
	}

	// 快照无效（尚未发请求）→ 不报告
	m2 := createMinimalModel()
	m2.AIModel.budget = ctxpkg.DefaultBudget(150)
	if _, _, exceeded := m2.fixedOverheadExceedsWindow(); exceeded {
		t.Error("快照无效时不应报告 exceeded")
	}

	// 窗口充足 → 不报告
	m3 := createMinimalModel()
	m3.requestSnapshot = contextRequestSnapshot{
		SystemPrompt: 1835, SessionSystem: 0, Tools: 4338, Model: "test-model", Valid: true,
	}
	if _, _, exceeded := m3.fixedOverheadExceedsWindow(); exceeded {
		t.Error("window=200000 固定开销 6173 时不应报告 exceeded")
	}
}

func TestFormatCompactResultReportsEstimatedIncrease(t *testing.T) {
	result := &ctxpkg.CompactResult{
		Changed: true, RemovedCount: 2, PreTokens: 56, PostTokens: 73,
		PreMessageCount: 12, PostMessageCount: 11, MessagesSummarized: 2,
	}
	got := formatCompactResult(result, ctxpkg.DefaultCompactionConfig())
	// 批 3：文案改为「英文主行 + 缩进中文辅行」（形态铁律 §0）。
	// 断言意图不变（语义事实全覆盖），只是把英文主行与中文辅行拆开取。
	for _, want := range []string{
		"Historical messages summarized: 2 -> 1 summary", "历史消息已摘要：2 -> 1 条摘要",
		"Messages: 12 -> 11", "消息数：12 -> 11",
		"~56 -> ~73", "+17; context increased",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "removed 2") || strings.Contains(got, "estimated reduction") {
		t.Fatalf("growth must not be described as deletion or savings: %s", got)
	}
}
