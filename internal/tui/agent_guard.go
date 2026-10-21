package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// agent_guard.go — P0 agent 防护体系（2026-08-31 移交文档）接线层。
//
// 生产数据流（三条链路共用，无测试专用路径）：
// - 新用户请求：sendInput/processInput → guardBeginRequest（状态归零）
// - 每次模型调用前：startAIStream/streamAIStream 顶部统一闸门 →
// BeforeModelCall（轮次上限/审查超限/空转/已停机），Stop 时渲染快照交回控制权
// - 每轮工具落定：handleToolResults/executeAndContinue →
// AfterTurn（观测）+ AfterToolResults（无进展熔断）

// newAgentGuard 从主配置构建 P0 防护器。
// 轮次上限复用 config.MaxTurnsPerRequest（env WHALE_MAX_TURNS_PER_REQUEST >
// config > 默认 40）；WHALE_AUTONOMY 插口默认关闭（P0-5）。
func newAgentGuard(cfg *config.Config) *agentguard.Guard {
	agCfg := agentguard.DefaultConfig()
	if cfg != nil && cfg.MaxTurnsPerRequest > 0 {
		agCfg.MaxTurns = cfg.MaxTurnsPerRequest
	}
	return agentguard.NewGuard(agentguard.ConfigFromEnv(agCfg))
}

// guardBeginRequest 新用户请求开始：P0 防护状态归零。
func guardBeginRequest(g *agentguard.Guard) {
	if g != nil {
		g.BeginRequest()
	}
}

// turnObservation 从本轮工具调用与结果提取进度观测（P0-4 数据源）。
// output 为本轮 AI 输出正文；toolCalls 为本轮 pending 工具调用（Input JSON）。
//
// G9-③：**只计成功**——调用必须按 ID 配对到一条 `!IsError` 的结果才算进展
// （结果缺失或失败一律不计，见下方注释与移交单 G9-002 的"保守方向"硬约束）。
func turnObservation(toolCalls []session.ContentBlock, results []tools.Result, output string) agentguard.TurnObservation {
	obs := agentguard.TurnObservation{
		FailingTests: -1, // 未知（本轮没跑测试）不参与收敛判定
		Output:       output,
	}
	// G9-③（S13）：先按 Result.ID 与调用配对，**只把确实成功执行的路径**计入观测。
	//
	// 修复前只遍历 toolCalls 的 Input 提路径、**完全不看 results 的成败** ⇒ 被拒绝的写
	// （沙箱越界 / 权限拒绝 / 补丁冲突）也被算成"改动了文件"。真机证据（2026-09-16 02:03:33）：
	// `agentguard: turn … "changed":1`，而同一时刻被拒的目标文件并不存在。
	//
	// 为什么这是安全缺陷而不是计数美观问题 —— FilesChanged 在下游被当作**进展**：
	// · progress.go:163 changedCountStabilized（无进展熔断 / 收敛判定）
	// · guard.go:167 纯读轮判定 ⇒ reviewRounds（审查子循环上限）
	// 被拒的写被计成进展 ⇒ 空转被伪装成有进展 ⇒ **熔断失效**（族E「上限必须真实生效」被失真输入架空）。
	//
	// 保守方向（硬约束）：**结果缺失或失败一律不计** —— 宁可少计，不得虚高。
	// 少计只是让熔断更早触发（安全方向）；虚高会让熔断永远不触发（危险方向）。
	// 被拒的读同理：它没有读到文件，把它记成"重读"是虚假信号。
	resultOK := make(map[string]bool, len(results))
	for _, r := range results {
		resultOK[r.ID] = !r.IsError
	}
	for _, c := range toolCalls {
		succeeded, paired := resultOK[c.ID]
		if !paired || !succeeded {
			continue // 无配对结果 / 配对结果失败 ⇒ 本轮该调用**没有产生进展**
		}
		// 只读面识别（唯一真相源 = permission 的能力分桶 CapRead，本处不另立名单）。
		// 提前算一次，供「记进展」与「记重复指纹」两处共用。
		readTarget, isReadOnly := readOnlyToolTarget(c.Name, c.Input)
		path := extractToolFilePath(c.Input)
		switch c.Name {
		case "read_file":
			if path != "" {
				obs.FilesRead = append(obs.FilesRead, path)
			}
		case "write_file", "edit_file":
			if path != "" {
				obs.FilesChanged = append(obs.FilesChanged, path)
			}
		default:
			// 其余**只读**工具（glob_search / grep_search / git_log / lsp* …）与
			// read_file 同属「获取信息」= 实质进展，必须计入。
			//
			// 2026-09-22 15:22 真机（会话存档 01a0c516）：此处原先只认 read_file ⇒
			// 连续只读搜索被算作「无文件改动的纯读轮」，4 轮（3×AskUser + 1×glob_search）
			// 即触发 P0-3 审查子循环上限停机（文案「疑似自我审查空转」，实为只读研究）。
			//
			// 识别面 = permission 的能力分桶 CapRead（toolCapabilities 是唯一真相源），
			// 本处**不另立工具名单** —— 否则新增/改名只读工具时两边必然漂移
			// （本仓 双实现坑；capability.go 的 sandboxBoundedTools 是同一纪律的先例）。
			if isReadOnly {
				obs.FilesRead = append(obs.FilesRead, readTarget)
			}
		}
		// P0-4 第四判据的数据源：非只读工具的 (工具, 输入) 指纹。
		//
		// ★ 只读工具**不填** —— 它们的重复由 FilesRead（rereadExceeded 按文件路径）
		// 负责；同一语义若有两个阈值（一个按路径、一个按输入），必然漂移（本仓）。
		//
		// 2026-09-30 真机 I-8.2：bash 的 `echo hi > out/x.txt` 连发 4 次，既不读文件、
		// 输出又为空 ⇒ 既有三条空转判据全不命中，强制面形同不存在。本行补上该盲区，
		// 判据见 internal/agentguard/repeated_calls_test.go。
		if !isReadOnly {
			obs.ToolCalls = append(obs.ToolCalls, agentguard.ToolCallKey{Tool: c.Name, Input: c.Input})
		}
	}
	if n, ran := countFailingTests(results); ran {
		// 本轮跑过测试（成功执行的 bash/PowerShell）：0 个失败也是已知值，
		// 是最强的收敛证据；未跑测试保持 -1（未知）。
		obs.FailingTests = n
	}
	return obs
}

// extractToolFilePath 从工具调用 Input JSON 提取文件路径。
// 兼容 path / file_path / file 三种字段名（覆盖 pipeline.go 的 path/file_path
// 两键口径；file 为补充键，覆盖部分工具用 file 传路径的场景）。
func extractToolFilePath(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "file"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// readOnlyToolTarget 为一个「只读（向模型披露本机既有内容/结构）」工具调用生成
// 「信息获取目标」键；第二个返回值报告该工具是否属于只读面。
//
// 键的语义（P0-3 判据）：同一键再次出现 = 原地打转（反复读/搜同一个目标）；
// 出现新键 = 有实质进展。glob_search / grep_search 的 pattern 计入键 ⇒
// 换搜索词算前进、反复搜同一个词算打转。
//
// 覆盖面的唯一真相源是 permission.CapabilitiesOf 的 CapRead：新增只读工具
// 只需登记 capability.go，本函数自动跟上（不在本处另立名单）。
func readOnlyToolTarget(name, input string) (string, bool) {
	if !hasReadCapability(name) {
		return "", false
	}
	var m map[string]interface{}
	if strings.TrimSpace(input) != "" {
		_ = json.Unmarshal([]byte(input), &m)
	}
	var parts []string
	for _, key := range []string{"path", "file_path", "file", "directory"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, s)
			break
		}
	}
	for _, key := range []string{"pattern", "glob", "query"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		// 无路径/模式参数的只读工具（git_status / lspdocumentsymbols 的无参调用…）：
		// 以工具名作键 ⇒ 反复同类调用仍判为「重复（打转）」，不会退化成"永远算进展"。
		return name, true
	}
	return name + ":" + strings.Join(parts, "\x00"), true
}

// hasReadCapability 报告工具是否具备「披露本机既有内容/结构」能力（CapRead）。
func hasReadCapability(name string) bool {
	for _, c := range permission.CapabilitiesOf(name) {
		if c == permission.CapRead {
			return true
		}
	}
	return false
}

// countFailingTests 从 bash/PowerShell 结果输出统计失败测试数
// （go test 失败用 "--- FAIL:" 标记）。
// 口径（对抗式审查修正）：go test 失败时 exit≠0 → Result.IsError=true，
// 但输出含 "--- FAIL:"，仍是有效的测试失败证据，必须统计——否则
// "失败测试数单调下降"这一最强收敛信号在真实失败循环中永远不可观测
// （生产失败循环全部是 IsError=true 的结果）。
// "--- FAIL:" 标记本身足以区分"测试失败"与"命令本身失败"：
// 无标记的失败（如 exit status 127）计为未知，不参与收敛判定。
// ran = 本轮是否跑了测试命令：true 时 0 也是有效值（最强收敛证据），
// false 表示未知（未跑测试），调用方应保持 -1。
func countFailingTests(results []tools.Result) (n int, ran bool) {
	for _, r := range results {
		// ★ P-002（2026-09-22 反缝补扫描）：命令载体的判据唯一来源 =
		// `permission.IsCommandBearingTool`（= {bash, monitor}）。修复前是手抄名对
		// `case "bash", "PowerShell":` ⇒ 用 Monitor 跑测试时本函数恒返回 (0,false)，
		// 调用方保持 -1「未知」⇒ "失败测试数单调下降"这一最强收敛信号在
		// "用 Monitor 跑测试"的循环里**永远不可观测**（与 P0-3 同族的判定偏差）。
		if permission.IsCommandBearingTool(r.Name) {
			if strings.Contains(r.Output, "--- FAIL:") {
				ran = true
				n += strings.Count(r.Output, "--- FAIL:")
			} else if !r.IsError {
				// 成功执行且无失败标记：跑过命令，0 失败也是已知值
				ran = true
			}
		}
	}
	return n, ran
}

// allToolsFailed 本轮存在工具调用且全部失败（P0-2 熔断输入）。
func allToolsFailed(results []tools.Result) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !r.IsError {
			return false
		}
	}
	return true
}

// lastAssistantText 返回会话中最后一条 assistant 消息的正文
// （进度观测 Output 数据源；空输出不参与相似度判定）。
func lastAssistantText(msgs []session.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			return msgs[i].TextContent()
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// TUI Model 接线
// ---------------------------------------------------------------------------

// guardNilWarnOnce guard 为 nil 时的 canary：只警告一次（每进程），
// 避免每轮刷日志；nil 意味着全部防线静默失效，必须可观测。
var guardNilWarnOnce sync.Once

// guardNilStopReason 是"防护器自身未初始化"的停机原因（A6 · 族A T3）。
//
// A6（S16-G2）拍板 = **A**：`guard == nil` ⇒ 闸门返回 **stopped=true**。
// 族A 口诀「缺的是**保护器** → T3」：保护器不存在 ⇒ 不许开工，
// 而不是"退回放行"（改前正是放行，主循环因此在防护缺失下**无界运行**）。
//
// 生产路径行为不变：`NewModel` / `NewPlainRunner` / `NewBridgeRunner` 都经
// `newAgentGuard` 初始化（各自有接线断言测试守），nil 只存在于零值构造。
const guardNilStopReason = "agentguard.Guard 未初始化 —— 防护器缺失即拒绝继续（fail-closed · T3）；" +
	"请检查启动接线（A6：guard==nil 不得退回放行，否则主循环无界运行）"

// guardNilStop 构造"防护器缺失"的停机判定（A6 · T3），并打一次 canary。
func guardNilStop(scope string) agentguard.Decision {
	guardNilWarnOnce.Do(func() {
		log.Printf("[%s] agentguard not initialized → fail-closed (A6/T3): refusing to continue", scope)
	})
	return agentguard.Decision{Verdict: agentguard.VerdictStop, Reason: guardNilStopReason}
}

// guardBeginRequest 新用户请求归零（TUI 路径）。
func (m *Model) guardBeginRequest() {
	guardBeginRequest(m.guard)
}

// guardAfterToolRound 每轮工具落定后的判定（TUI 路径）。
// 返回 (cmd, stopped)：stopped=true 时 cmd 为停机收尾命令（drain 排队
// 输入 / 异步自动压缩），调用方必须透传给 Bubble Tea，不得丢弃——
// 丢弃会让排队输入滞留、自动压缩 cooldown 占窗口却不执行。
//
// A6：guard 缺失 ⇒ stopped=true（T3，不得退回放行）。
func (m *Model) guardAfterToolRound(results []tools.Result) (tea.Cmd, bool) {
	if m.guard == nil {
		return m.renderGuardStop(guardNilStop("tui")), true
	}
	toolCalls := m.toolService.PendingTools()
	obs := turnObservation(toolCalls, results, lastAssistantText(m.sessionService.CurrentSession().GetMessages()))
	m.guard.AfterTurn(obs)
	if d := m.guard.AfterToolResults(allToolsFailed(results)); d.Verdict == agentguard.VerdictStop {
		return m.renderGuardStop(d), true
	}
	return nil, false
}

// guardRecordAskUserTurn AskUser 主拦截路径的 P0 记账（app.go toolResultMsg
// case）。该路径不走 handleToolResults（直接进入 WaitingChoice），若不在此
// 记账，轮次上限/审查子循环上限可被 AskUser 交错规避（与 tool_exec.go
// hasPendingChoice 兜底分支同口径）。
func (m *Model) guardRecordAskUserTurn(results []tools.Result) {
	if m.guard == nil {
		return
	}
	obs := turnObservation(m.toolService.PendingTools(), results,
		lastAssistantText(m.sessionService.CurrentSession().GetMessages()))
	m.guard.AfterTurn(obs)
}

// guardBeforeModelCall 模型调用前的统一闸门（TUI 路径）。
// 返回 (cmd, stopped)：stopped=true 时调用方返回 cmd（含 drain/自动压缩），
// 不得发起模型调用。
//
// A6：guard 缺失 ⇒ stopped=true（T3，不得退回放行）。
func (m *Model) guardBeforeModelCall() (tea.Cmd, bool) {
	if m.guard == nil {
		return m.renderGuardStop(guardNilStop("tui")), true
	}
	d := m.guard.BeforeModelCall()
	switch d.Verdict {
	case agentguard.VerdictStop:
		return m.renderGuardStop(d), true
	case agentguard.VerdictRemind:
		// P0-3 软提醒：**不停机**。① 注入模型上下文（让它有机会自行收敛）
		// ② 给用户可见提示。两者缺一即假修复。
		injectReviewNudge(m.sessionService.CurrentSession(), d.Reminder)
		m.addMessage(toolStyle.Render(fmt.Sprintf("⏱ %s", d.Reason)))
	case agentguard.VerdictExtend:
		// 自主模式收敛续跑 canary：静默续跑不可见，用户会误以为失控。
		m.addMessage(toolStyle.Render(fmt.Sprintf("🔁 %s", d.Reason)))
	}
	return nil, false
}

// injectReviewNudge 把 P0-3 软提醒注入会话上下文，模型下一轮即可见。
//
// ★ 三条运行路径（TUI / plain / bridge）**共用此函数** —— 提醒注入是同一语义，
// 不得在三处各写一份（双实现坑，见）。
//
// UI 侧：Subtype="review_nudge" ≠ "compact_boundary"，resume/fork 时
// 不渲染进窗口（见 lifecycle.go 的 RoleSystem 分支），不污染历史显示。
func injectReviewNudge(s *session.Session, text string) {
	if s == nil || text == "" {
		return
	}
	s.AddMessage(session.Message{
		Role:    session.RoleSystem,
		Subtype: "review_nudge",
		Blocks:  []session.ContentBlock{{Type: "text", Text: text}},
	})
}

// renderGuardStop 渲染停机快照并交回控制权（TUI 路径）。
// 非报错退出：关闭用户 turn（含持久化/钩子/自动压缩检查），
// 有排队输入时 drain（排队输入 = 新用户请求，guard 自动归零）。
func (m *Model) renderGuardStop(d agentguard.Decision) tea.Cmd {
	text := "⏸ " + d.Reason
	if d.Report != nil {
		text = d.Report.Render()
	}
	m.addMessage(toolStyle.Render(text))
	m.sessionService.SetState(StateIdle)
	m.uiService.SetInputFocusedOnSend(false)
	m.uiService.SetInputFocusedOnStop(false)
	m.uiService.Textarea().Focus()
	m.recalcViewportHeight()
	m.finishTurn(true)

	autoCompactCmd := m.checkAutoCompact()
	if m.HasQueuedInputs() {
		drainCmd := func() tea.Msg { return drainQueuedInputMsg{} }
		if autoCompactCmd != nil {
			return tea.Sequence(autoCompactCmd, drainCmd)
		}
		return drainCmd
	}
	return autoCompactCmd
}

// ---------------------------------------------------------------------------
// PlainRunner 接线
// ---------------------------------------------------------------------------

// guardBeginRequest 新用户请求归零（plain 路径）。
func (r *PlainRunner) guardBeginRequest() {
	guardBeginRequest(r.guard)
}

// guardBeforeModelCall 模型调用前的统一闸门（plain 路径）。
// 返回 true 表示已停机（调用方不得发起模型调用）。
//
// A6：guard 缺失 ⇒ 停机（T3）。
func (r *PlainRunner) guardBeforeModelCall() bool {
	if r.guard == nil {
		r.renderGuardStop(guardNilStop("plain"))
		return true
	}
	d := r.guard.BeforeModelCall()
	switch d.Verdict {
	case agentguard.VerdictStop:
		r.renderGuardStop(d)
		return true
	case agentguard.VerdictRemind:
		// P0-3 软提醒：不停机。注入上下文（让模型有机会收敛）+ 打印（无人值守时需可见）。
		injectReviewNudge(r.Session, d.Reminder)
		fmt.Printf("⏱ %s\n", d.Reason)
	case agentguard.VerdictExtend:
		// 自主模式收敛续跑 canary：静默续跑不可见，用户会误以为失控。
		fmt.Printf("🔁 %s\n", d.Reason)
	}
	return false
}

// guardAfterToolRound 每轮工具落定后的判定（plain 路径）。
// 返回 true 表示已停机。
//
// A6：guard 缺失 ⇒ 停机（T3）。
func (r *PlainRunner) guardAfterToolRound(toolCalls []session.ContentBlock, results []tools.Result) bool {
	if r.guard == nil {
		r.renderGuardStop(guardNilStop("plain"))
		return true
	}
	obs := turnObservation(toolCalls, results, lastAssistantText(r.Session.GetMessages()))
	r.guard.AfterTurn(obs)
	if d := r.guard.AfterToolResults(allToolsFailed(results)); d.Verdict == agentguard.VerdictStop {
		r.renderGuardStop(d)
		return true
	}
	return false
}

// renderGuardStop 渲染停机快照并交回控制权（plain 路径）：
// 打印快照、关闭 turn（与 handleStreamEvents 空工具分支同口径）。
func (r *PlainRunner) renderGuardStop(d agentguard.Decision) {
	if d.Report != nil {
		fmt.Printf("\n%s\n", d.Report.Render())
	} else {
		fmt.Printf("\n⏸ %s\n", d.Reason)
	}
	r.setState(stateIdle)
	r.Session.IncrementTurn()
	_ = r.saveSession()
	r.autoSnapshotOnTurnEnd()
	r.runTurnEndHooks(context.Background())
	r.checkAutoCompact()
}

// ---------------------------------------------------------------------------
// BridgeRunner 接线（IDE 模式；对抗式审查必须修复项：此前零接线，
// streamAIRequest → executeAndContinue → streamAIRequest 为无界循环）
// ---------------------------------------------------------------------------

// guardBeginRequest 新用户请求归零（bridge 路径）。
func (r *BridgeRunner) guardBeginRequest() {
	guardBeginRequest(r.guard)
}

// guardBeforeModelCall 模型调用前的统一闸门（bridge 路径）。
// 返回 true 表示已停机（调用方不得发起模型调用）。
//
// A6：guard 缺失 ⇒ 停机（T3）。
func (r *BridgeRunner) guardBeforeModelCall() bool {
	if r.guard == nil {
		r.renderGuardStop(guardNilStop("bridge"))
		return true
	}
	d := r.guard.BeforeModelCall()
	switch d.Verdict {
	case agentguard.VerdictStop:
		r.renderGuardStop(d)
		return true
	case agentguard.VerdictRemind:
		// P0-3 软提醒：不停机。注入上下文（让模型有机会收敛）+ 流式提示给 IDE。
		injectReviewNudge(r.Session, d.Reminder)
		r.sendStream("text", fmt.Sprintf("⏱ %s", d.Reason), "", nil, "", 0, 0, 0)
	case agentguard.VerdictExtend:
		// 自主模式收敛续跑 canary：静默续跑不可见，用户会误以为失控。
		r.sendStream("text", fmt.Sprintf("🔁 %s", d.Reason), "", nil, "", 0, 0, 0)
	}
	return false
}

// guardAfterToolRound 每轮工具落定后的判定（bridge 路径）。
// 返回 true 表示已停机。
//
// A6：guard 缺失 ⇒ 停机（T3）。
func (r *BridgeRunner) guardAfterToolRound(toolCalls []session.ContentBlock, results []tools.Result) bool {
	if r.guard == nil {
		r.renderGuardStop(guardNilStop("bridge"))
		return true
	}
	obs := turnObservation(toolCalls, results, lastAssistantText(r.Session.GetMessages()))
	r.guard.AfterTurn(obs)
	if d := r.guard.AfterToolResults(allToolsFailed(results)); d.Verdict == agentguard.VerdictStop {
		r.renderGuardStop(d)
		return true
	}
	return false
}

// renderGuardStop 渲染停机快照并交回控制权（bridge 路径）：
// 快照经 JSON-RPC 发往 IDE，关闭 turn（与 handleStreamEvents 空工具
// 分支同口径：IncrementTurn/saveSession/快照/钩子/自动压缩检查）。
func (r *BridgeRunner) renderGuardStop(d agentguard.Decision) {
	text := "⏸ " + d.Reason
	if d.Report != nil {
		text = d.Report.Render()
	}
	r.sendStream("text", text, "", nil, "", 0, 0, 0)
	r.sendStream("done", "", "", nil, "", 0, 0, 0)
	r.setState(stateIdle)
	r.Session.IncrementTurn()
	_ = r.saveSession()
	r.autoSnapshotOnTurnEnd()
	r.runTurnEndHooks(context.Background())
	r.checkAutoCompact()
}
