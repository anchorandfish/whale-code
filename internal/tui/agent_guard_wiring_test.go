package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// newGuardTestModel 构造带 P0 防护的测试 Model（复用既有测试基建）。
// 返回指针并在构造后立即 reassignServices：让服务指针指向本实例的嵌入
// 结构体，避免值复制导致 resetStreamState 内的 reassignServices 把状态
// 分裂到两个结构体（service 指针别名问题，见 service.go 注释）。
func newGuardTestModel(t *testing.T) *Model {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	m := NewModel(&cfg, newTestClient("test-model"), newTestRegistry())
	m.reassignServices()
	return &m
}

// renderedMessagesText 追加消息后可断言的渲染文本。
func renderedMessagesText(m *Model) string {
	msgs := m.sessionService.Messages()
	var sb strings.Builder
	for _, s := range msgs {
		sb.WriteString(s)
		sb.WriteString("\n")
	}
	return sb.String()
}

// TestNewModelInitializesGuard NewModel 必须创建 guard（接线断言：
// 生产入口与测试入口共用 NewModel，不存在"测试专用初始化路径"）。
func TestNewModelInitializesGuard(t *testing.T) {
	m := newGuardTestModel(t)
	if m.guard == nil {
		t.Fatal("NewModel 未初始化 agentguard（P0 防护未接线，生产路径裸奔）")
	}
	if d := m.guard.BeforeModelCall(); d.Verdict != agentguard.VerdictProceed {
		t.Errorf("新请求首轮应放行，实际 Verdict=%d Reason=%s", d.Verdict, d.Reason)
	}
}

// TestStartAIStreamStopsAtTurnLimitAndRendersReport 结果断言：
// 轮次到限后 startAIStream 不发起模型调用（状态回 Idle），
// 且用户可见消息中出现进度快照（"已暂停" + 轮次上限原因）。
func TestStartAIStreamStopsAtTurnLimitAndRendersReport(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 2
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()
	m.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	m.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	m.sessionService.SetState(StateStreaming)

	cmd := m.startAIStream()
	_ = cmd

	if m.sessionService.State() != StateIdle {
		t.Errorf("轮次到限后状态应回 Idle（停止模型调用），实际 %v", m.sessionService.State())
	}
	text := renderedMessagesText(m)
	if !strings.Contains(text, "已暂停") {
		t.Errorf("应渲染进度快照（含「已暂停」），实际输出：%.200s", text)
	}
	if !strings.Contains(text, "轮次达到上限 2") {
		t.Errorf("快照应说明停机原因（轮次达到上限 2），实际输出：%.200s", text)
	}
}

// TestSendInputResetsGuardOnNewRequest 结果断言：
// 停机后用户发送新输入 → guard 状态归零（新请求获得完整预算，
// 而非永久卡死在 stopped）。
func TestSendInputResetsGuardOnNewRequest(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 1
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()
	m.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	if d := m.guard.BeforeModelCall(); d.Verdict != agentguard.VerdictStop {
		t.Fatalf("前置条件：应已停机，实际 Verdict=%d", d.Verdict)
	}

	m.textarea.SetValue("继续处理剩余步骤")
	_, _ = m.sendInput()

	if m.guard.TotalTurns() != 0 {
		t.Errorf("新用户请求应归零轮次计数，实际 TotalTurns=%d", m.guard.TotalTurns())
	}
	if d := m.guard.BeforeModelCall(); d.Verdict != agentguard.VerdictProceed {
		t.Errorf("新请求应恢复放行，实际 Verdict=%d Reason=%s", d.Verdict, d.Reason)
	}
}

// TestHandleToolResultsTripsFailureBreaker 结果断言：
// 连续 3 轮工具全失败 → 熔断停机（状态 Idle、不再发模型调用、
// 用户可见消息包含熔断原因），且 tool_result 已入会话（上下文一致性）。
func TestHandleToolResultsTripsFailureBreaker(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxConsecutiveToolFailures = 3
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()

	fail := []tools.Result{{
		ID:      "t1",
		Name:    "bash",
		Output:  "exit status 1",
		IsError: true,
	}}
	for i := 1; i <= 3; i++ {
		m.toolService.SetPendingTools([]session.ContentBlock{{
			Type:  "tool_use",
			ID:    "t1",
			Name:  "bash",
			Input: `{"command":"false"}`,
		}})
		m.sessionService.SetState(StateToolExec)
		_ = m.handleToolResults(fail)
		if i < 3 && m.sessionService.State() != StateStreaming {
			t.Fatalf("第 %d 轮失败未达熔断阈值应继续（State=Streaming），实际 %v", i, m.sessionService.State())
		}
	}

	if m.sessionService.State() != StateIdle {
		t.Errorf("连续 3 轮全失败应熔断停机（Idle），实际 %v", m.sessionService.State())
	}
	if d := m.guard.BeforeModelCall(); d.Verdict != agentguard.VerdictStop {
		t.Errorf("熔断后应拒绝继续模型调用，实际 Verdict=%d", d.Verdict)
	}
	text := renderedMessagesText(m)
	if !strings.Contains(text, "全部失败") {
		t.Errorf("应渲染熔断原因（连续 N 轮工具调用全部失败），实际输出：%.200s", text)
	}
	// 上下文一致性：assistant tool_use 必须有配对的 tool_result 入会话
	msgs := m.sessionService.CurrentSession().GetMessages()
	toolResults := 0
	for _, msg := range msgs {
		for _, b := range msg.Blocks {
			if b.Type == "tool_result" {
				toolResults++
			}
		}
	}
	if toolResults != 3 {
		t.Errorf("3 轮失败工具结果应全部入会话（防 orphan tool_use），实际 %d 条", toolResults)
	}
}

// TestHandleToolResultsObservesFileChanges 结果断言：
// 工具调用中的 read_file/edit_file 路径被提取进观测器——
// 停机快照的「已完成」包含 edit 过的文件（生产数据流：
// tool Input JSON → TurnObservation → StopReport）。
func TestHandleToolResultsObservesFileChanges(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 1
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()

	m.toolService.SetPendingTools([]session.ContentBlock{
		{Type: "tool_use", ID: "r1", Name: "read_file", Input: `{"path":"a.go"}`},
		{Type: "tool_use", ID: "e1", Name: "edit_file", Input: `{"file_path":"b.go"}`},
	})
	m.sessionService.SetState(StateToolExec)
	_ = m.handleToolResults([]tools.Result{
		{ID: "r1", Name: "read_file", Output: "ok"},
		{ID: "e1", Name: "edit_file", Output: "ok"},
	})

	d := m.guard.BeforeModelCall()
	if d.Verdict != agentguard.VerdictStop {
		t.Fatalf("MaxTurns=1 应在下一轮停机，实际 Verdict=%d", d.Verdict)
	}
	found := false
	for _, f := range d.Report.Completed {
		if strings.HasSuffix(f, "b.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("快照「已完成」应包含 edit_file 改动的 b.go，实际 %v", d.Report.Completed)
	}
}

// TestTurnObservationExtractsFiles 单元断言：工具调用 JSON 中的
// path/file_path 提取与 bash 失败测试计数口径（0 失败 vs 未跑测试）。
//
// ⚠️ G9-③**有意翻转的既有 characterization**：本用例原先用
// **无 ID 的调用**配**无关 ID 的结果**，断言"照样把路径提取出来" —— 那等于把被修的
// 缺陷写成了断言（turnObservation 从不看 results 的成败）。新契约：调用必须按 ID
// 配对到一条 `!IsError` 的结果才算进展（移交单 G9-002「保守方向」硬约束）。
// 故夹具补齐 ID 与成功结果；"被拒 / 无配对一律不计"由
// g9_rejection_facts_test.go 的 TestG9_DeniedWriteNotCountedAsProgress 正面覆盖。
func TestTurnObservationExtractsFiles(t *testing.T) {
	calls := []session.ContentBlock{
		{Type: "tool_use", ID: "r1", Name: "read_file", Input: `{"path":"x/a.go"}`},
		{Type: "tool_use", ID: "e1", Name: "edit_file", Input: `{"file_path":"y/b.go"}`},
		{Type: "tool_use", ID: "w1", Name: "write_file", Input: `{"path":"z/c.go"}`},
	}
	results := []tools.Result{
		{ID: "r1", Name: "read_file", Output: "ok"},
		{ID: "e1", Name: "edit_file", Output: "ok"},
		{ID: "w1", Name: "write_file", Output: "ok"},
		{ID: "b", Name: "bash", Output: "--- FAIL: TestX\n--- FAIL: TestY"},
	}
	obs := turnObservation(calls, results, "done")
	if len(obs.FilesRead) != 1 || obs.FilesRead[0] != "x/a.go" {
		t.Errorf("FilesRead 提取错误：%v", obs.FilesRead)
	}
	if len(obs.FilesChanged) != 2 {
		t.Errorf("FilesChanged 应含 b.go/c.go，实际 %v", obs.FilesChanged)
	}
	if obs.FailingTests != 2 {
		t.Errorf("bash 输出中 2 个 --- FAIL: 应计为 FailingTests=2，实际 %d", obs.FailingTests)
	}

	// 口径：成功跑过测试 0 失败 = 已知 0（最强收敛证据）；未跑测试 = -1（未知）。
	allGreen := turnObservation(nil, []tools.Result{{ID: "b", Name: "bash", Output: "PASS\nok  pkg 0.5s"}}, "")
	if allGreen.FailingTests != 0 {
		t.Errorf("成功跑测试 0 失败应为已知 0，实际 %d", allGreen.FailingTests)
	}
	unknown := turnObservation(nil, []tools.Result{{ID: "r", Name: "read_file", Output: "ok"}}, "")
	if unknown.FailingTests != -1 {
		t.Errorf("未跑测试应为 -1（未知），实际 %d", unknown.FailingTests)
	}
}

// TestGuardStopDrainsQueuedInput 结果断言（对抗式审查问题 1 回归）：
// 停机时 startAIStream 返回的 cmd 执行后产生 drainQueuedInputMsg——
// 排队输入不滞留队列（排队输入 = 新用户请求，drain 后 guard 归零续跑）。
func TestGuardStopDrainsQueuedInput(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 1
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()
	m.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	if !m.EnqueueInput(QueuedUserMessage{Text: "接着做第二步"}) {
		t.Fatal("入队失败（前置条件）")
	}
	m.sessionService.SetState(StateStreaming)

	cmd := m.startAIStream()
	if cmd == nil {
		t.Fatal("停机且有排队输入时应返回 drain cmd，实际 nil（cmd 被丢弃，排队输入将滞留）")
	}
	msg := cmd()
	if _, ok := msg.(drainQueuedInputMsg); !ok {
		t.Fatalf("cmd 执行应产生 drainQueuedInputMsg，实际 %T", msg)
	}
}

// TestPlainRunnerGuardWiring 结果断言（plain 路径接线，对抗式审查覆盖缺口）：
// 归零放行 → 轮次到限闸门停机（状态回 idle，不再发起模型调用）。
func TestPlainRunnerGuardWiring(t *testing.T) {
	r := newTestPlainRunner()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r.Store = store
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 2
	r.guard = agentguard.NewGuard(gCfg)

	r.guardBeginRequest()
	if r.guardBeforeModelCall() {
		t.Fatal("新请求首轮应放行")
	}

	r.setState(stateStreaming)
	r.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	r.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	if !r.guardBeforeModelCall() {
		t.Fatal("轮次到限应停机")
	}
	if r.getState() != stateIdle {
		t.Errorf("停机后状态应回 idle，实际 %v", r.getState())
	}
}

// TestNewPlainRunnerInitializesGuard 接线断言：生产 NewPlainRunner 必须
// 创建 guard（测试基建 newTestPlainRunner 不构造 guard，若生产初始化被
// 误删，仅靠 wiring 测试（手动赋 guard）会照过、生产裸奔且无 canary）。
func TestNewPlainRunnerInitializesGuard(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	r := NewPlainRunner(&cfg, newTestClient("test-model"), newTestRegistry())
	if r.guard == nil {
		t.Fatal("NewPlainRunner 未初始化 agentguard（plain 路径 P0 防护裸奔）")
	}
}

// TestNewAgentGuardPropagatesMaxTurns 结果断言：config.MaxTurnsPerRequest
// 必须传播进 guard（WHALE_MAX_TURNS_PER_REQUEST > config > 默认 40 的桥接点）。
func TestNewAgentGuardPropagatesMaxTurns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxTurnsPerRequest = 7
	g := newAgentGuard(&cfg)
	g.BeginRequest()
	// 每轮带 ChangedFiles（=有改动活动），避免先触发"审查子循环超限"
	// （判定顺序：审查超限 > 轮次到限，不加 ChangedFiles 第 4 轮就命审查）。
	for i := 0; i < 7; i++ {
		g.AfterTurn(agentguard.TurnObservation{
			FailingTests: -1,
			FilesChanged: []string{"internal/foo/bar.go"},
		})
	}
	d := g.BeforeModelCall()
	if d.Verdict != agentguard.VerdictStop {
		t.Fatalf("MaxTurnsPerRequest=7 应在第 8 次模型调用前停机，实际 Verdict=%d", d.Verdict)
	}
	if !strings.Contains(d.Reason, "上限 7") {
		t.Errorf("停机原因应说明上限 7，实际 %q", d.Reason)
	}
}

// TestCountFailingTestsFromFailedBash 生产口径回归（对抗式审查必须修复项）：
// go test 失败 → exit≠0 → Result.IsError=true，但输出含 --- FAIL:，
// 仍是有效测试失败证据——若跳过 IsError 结果，"失败测试数单调下降"这一
// 最强收敛信号在真实失败循环中永远不可观测。
func TestCountFailingTestsFromFailedBash(t *testing.T) {
	n, ran := countFailingTests([]tools.Result{
		{ID: "b1", Name: "bash", Output: "--- FAIL: TestA\n--- FAIL: TestB", IsError: true},
	})
	if !ran || n != 2 {
		t.Fatalf("失败 bash 输出的 --- FAIL: 应计为测试失败（ran=true n=2），实际 ran=%v n=%d", ran, n)
	}
	// 无 --- FAIL: 标记的命令失败 = 未知（非 0 失败），不参与收敛判定
	_, ranCmdFail := countFailingTests([]tools.Result{
		{ID: "b2", Name: "bash", Output: "exit status 127", IsError: true},
	})
	if ranCmdFail {
		t.Error("无 --- FAIL: 标记的命令失败不应计为测试运行")
	}
}

// TestAskUserMainPathRecordsGuardTurn 结果断言（对抗式审查必须修复项）：
// AskUser 被 checkAskUserChoice 拦截进入 WaitingChoice 时走的是 app.go
// toolResultMsg 主分支（不经 handleToolResults），该轮必须照样记入 guard
// 轮次——否则轮次上限/审查子循环上限可被 AskUser 交错规避。
func TestAskUserMainPathRecordsGuardTurn(t *testing.T) {
	m := newGuardTestModel(t)
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 2
	m.guard = agentguard.NewGuard(gCfg)
	m.guard.BeginRequest()

	m.toolService.SetPendingTools([]session.ContentBlock{{
		Type: "tool_use", ID: "a1", Name: "AskUser",
		Input: `{"question":"选哪个","options":[{"label":"A","value":"a"},{"label":"B","value":"b"}]}`,
	}})
	results := []tools.Result{{
		ID: "a1", Name: "AskUser", IsError: false,
		Output: `{"question":"选哪个","options":[{"label":"A","value":"a"},{"label":"B","value":"b"}]}`,
	}}

	// Update 是值接收者：State 是值嵌入体上的副本字段，reassignServices
	// 会把副本的 sessionService 指向副本自身的 SessionModel；guard 是
	// 指针，所以仍落在原实例。因此 State 断言必须读返回值 mm。
	mm, _ := m.Update(toolResultMsg{results: results})
	mOut := mm.(Model)

	if mOut.sessionService.State() != StateWaitingChoice {
		t.Fatalf("前置条件：应进入 WaitingChoice（证明命中主拦截路径），实际 %v",
			mOut.sessionService.State())
	}
	// guard 是指针：原始 m 与副本 mOut 共享同一个 Guard 实例，读哪边都一样。
	if m.guard.TotalTurns() != 1 {
		t.Fatalf("AskUser 主拦截路径应记 1 轮，实际 %d（上限可被 AskUser 交错规避）",
			m.guard.TotalTurns())
	}
}

// TestNewBridgeRunnerInitializesGuard 接线断言（对抗式审查必须修复项）：
// BridgeRunner（IDE 模式）此前零 guard 接线，agent 循环无界。
func TestNewBridgeRunnerInitializesGuard(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	runner, err := NewBridgeRunner(&cfg, newTestClient("test-model"), newTestRegistry(), false)
	if err != nil {
		t.Fatalf("NewBridgeRunner: %v", err)
	}
	if runner.guard == nil {
		t.Fatal("NewBridgeRunner 未初始化 agentguard（IDE 模式 P0 防护裸奔）")
	}
}

// TestBridgeRunnerGuardWiring 结果断言（bridge 路径接线）：
// 归零放行 → 轮次到限闸门停机（状态回 idle）。
func TestBridgeRunnerGuardWiring(t *testing.T) {
	r, _ := newTestBridgeRunner()
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 2
	r.guard = agentguard.NewGuard(gCfg)

	r.guardBeginRequest()
	if r.guardBeforeModelCall() {
		t.Fatal("新请求首轮应放行")
	}

	r.setState(stateStreaming)
	r.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	r.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	if !r.guardBeforeModelCall() {
		t.Fatal("轮次到限应停机")
	}
	if r.getState() != stateIdle {
		t.Errorf("停机后状态应回 idle，实际 %v", r.getState())
	}
}

// TestBridgeRunnerGuardBreaker 结果断言（bridge 路径 P0-2 熔断）：
// 连续 3 轮工具全失败 → 第 3 轮停机（状态 idle），前 2 轮继续。
func TestBridgeRunnerGuardBreaker(t *testing.T) {
	r, _ := newTestBridgeRunner()
	r.guard = agentguard.NewGuard(agentguard.DefaultConfig())
	r.guardBeginRequest()

	fail := []tools.Result{{ID: "t1", Name: "bash", Output: "exit status 1", IsError: true}}
	calls := []session.ContentBlock{{Type: "tool_use", ID: "t1", Name: "bash", Input: `{"command":"false"}`}}
	for i := 1; i <= 3; i++ {
		stopped := r.guardAfterToolRound(calls, fail)
		if i < 3 && stopped {
			t.Fatalf("第 %d 轮未达熔断阈值不应停机", i)
		}
		if i == 3 && !stopped {
			t.Fatal("第 3 轮全失败应熔断停机")
		}
	}
	if r.getState() != stateIdle {
		t.Errorf("熔断后状态应回 idle，实际 %v", r.getState())
	}
}

// TestBridgeRunnerStreamStopsAtTurnLimit 结果断言（bridge 路径生产路径）：
// 轮次到限后 streamAIRequest 顶部闸门直接停机——不发模型调用（无 error
// 通知）、状态回 idle、快照文本发往 IDE。
func TestBridgeRunnerStreamStopsAtTurnLimit(t *testing.T) {
	r, buf := newTestBridgeRunner()
	gCfg := agentguard.DefaultConfig()
	gCfg.MaxTurns = 1
	r.guard = agentguard.NewGuard(gCfg)
	r.guardBeginRequest()
	r.guard.AfterTurn(agentguard.TurnObservation{FailingTests: -1})
	r.setState(stateStreaming)

	r.streamAIRequest(nil)

	out := buf.String()
	if !strings.Contains(out, "已暂停") {
		t.Errorf("停机快照应通过 JSON-RPC 发往 IDE（含「已暂停」），实际输出：%.200s", out)
	}
	if strings.Contains(out, `"type":"error"`) {
		t.Errorf("停机不应发起模型调用（不应出现 error 通知），实际输出：%.200s", out)
	}
	if r.getState() != stateIdle {
		t.Errorf("停机后状态应回 idle，实际 %v", r.getState())
	}
}

// TestPlainRunnerGuardBreaker 结果断言（plain 路径 P0-2 熔断）：
// 连续 3 轮工具全失败 → 第 3 轮停机（状态 idle），前 2 轮继续。
func TestPlainRunnerGuardBreaker(t *testing.T) {
	r := newTestPlainRunner()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r.Store = store
	r.guard = agentguard.NewGuard(agentguard.DefaultConfig())
	r.guardBeginRequest()

	fail := []tools.Result{{ID: "t1", Name: "bash", Output: "exit status 1", IsError: true}}
	calls := []session.ContentBlock{{Type: "tool_use", ID: "t1", Name: "bash", Input: `{"command":"false"}`}}
	for i := 1; i <= 3; i++ {
		stopped := r.guardAfterToolRound(calls, fail)
		if i < 3 && stopped {
			t.Fatalf("第 %d 轮未达熔断阈值不应停机", i)
		}
		if i == 3 && !stopped {
			t.Fatal("第 3 轮全失败应熔断停机")
		}
	}
	if r.getState() != stateIdle {
		t.Errorf("熔断后状态应回 idle，实际 %v", r.getState())
	}
}
