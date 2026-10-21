package tui

// b3_notify_compact_bilingual_guard_test.go — 批 3 守卫：
// 钉 `/notify` 与 `/compact`（log / purge / 结果回执）两组命令输出的**文案形态**。
//
// # 形态契约（SSOT = internal/util/bilingual.go，见 bilingual_guard_test.go 文件头）
//
//	English main line ← 主行：可独立读懂的英文，不得含汉字
// Chinese auxiliary line ← 辅助行：以 copyIndent 开头，中文只允许出现在这里
//
// # 为什么这里用「产物形态门」而不是 AST「字面量必须在 Bi 实参内」门
//
// 本文件对应的实现是**② 型（整句式双语）**：多行块先把英文/中文分别拼进
// `strings.Builder`，最后一次性交给 `bi(enBlock, zhBlock)`（见 commands_notify.go
// `handleNotifyList` / `handleCompactLog` / `formatCompactResult`）。
// 中文片段（`zh.WriteString(fmt.Sprintf("消息数：%d", …))`）在**语法上并不位于** `bi(...)`
// 实参子树内 ⇒ 强门会把合法片段误红（技能 §7「结构门要按文件结构选」已记载该坑）。
// 故本文件改为对**真实输出**施加形态门：含汉字的行必须以 copyIndent 开头。
//
// # 判别性（自证）
//
// `TestW083B3_FormGateIsDiscriminating` 亲手喂入改造前的行内并列形态
// （`Historical messages summarized 历史消息已摘要: …`），门**必须**判红；
// 同时 `bi("English", "中文")` 必须判绿 —— 不红 = 门是假的。
// 扫描逻辑本地独立（不调用生产的 `util.AssertChineseOnlyOnIndentedLines`）：
// 实现与判据同源会让实现写错时判据跟着错（共享 bug ⇒ 假绿）。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/anchorandfish/whale-code/internal/session"
)

// firstFormViolation 返回第一条「含汉字却不在缩进行」的行（空串 = 合规）。
// 字符集走 SSOT（hasHan），扫描循环本地独立 —— 与 bilingual_guard_test.go 同分工。
func firstFormViolation(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if !hasHan(line) {
			continue
		}
		if !strings.HasPrefix(line, copyIndent) {
			return line
		}
	}
	return ""
}

// TestW083B3_FormGateIsDiscriminating 证明本门的判别力：旧形态必红、合法形态必绿。
func TestW083B3_FormGateIsDiscriminating(t *testing.T) {
	if firstFormViolation("Historical messages summarized 历史消息已摘要: 2 -> 1 summary") == "" {
		t.Fatal("形态门失效：改造前的行内并列形态未被判红（判别力为零 ⇒ 门是假的）")
	}
	if got := firstFormViolation(bi("English main line", "中文辅助行")); got != "" {
		t.Fatalf("形态门误红合法的 bi 形态：%q", got)
	}
}

// TestW083B3_PriorityLabelIsEnglishOnly 钉 `priorityLabel`：标签进单行列表
// （`[label] title: message`），容不下缩进辅行 ⇒ 必须英文单侧（无汉字）。
func TestW083B3_PriorityLabelIsEnglishOnly(t *testing.T) {
	for _, p := range []notify.Priority{
		notify.PriorityImmediate, notify.PriorityHigh, notify.PriorityMedium,
		notify.PriorityLow, notify.Priority(99),
	} {
		if got := priorityLabel(p); hasHan(got) {
			t.Errorf("priorityLabel(%d)=%q 含汉字；列表行容不下辅行 ⇒ 必须英文单侧", int(p), got)
		}
	}
}

// TestW083B3_NotifyOutputs_ChineseOnlyOnIndentedLines 钉 `/notify` 全家（TUI 真实入口）。
func TestW083B3_NotifyOutputs_ChineseOnlyOnIndentedLines(t *testing.T) {
	m := newTUIRegressionModel(t)

	// 先打空态（current==nil 分支）。
	if out := m.handleCommand("/notify list"); firstFormViolation(out) != "" {
		t.Errorf("空态 /notify list：中文出现在非缩进行 ⇒ %q\n%s", firstFormViolation(out), out)
	}
	if out := m.handleCommand("/notify dismiss"); firstFormViolation(out) != "" {
		t.Errorf("无通知时 /notify dismiss：%q\n%s", firstFormViolation(out), out)
	}

	// ★ 用**英文数据**构造通知：通知的标题/正文是调用方数据（不属本门管辖），
	// 用英文数据才能把「我们自己写的 copy」隔离出来判。
	m.notifier.Push(notify.Notification{Key: "b3-k1", Priority: notify.PriorityHigh, Title: "Build finished", Message: "3 tasks done"})
	m.notifier.Push(notify.Notification{Key: "b3-k2", Priority: notify.PriorityLow, Title: "Cache warmed", Message: "ok", Count: 2})

	for _, cmd := range []string{"/notify list", "/notify dismiss", "/notify clear"} {
		out := m.handleCommand(cmd)
		if v := firstFormViolation(out); v != "" {
			t.Errorf("%s：中文出现在非缩进行（行内并列？）⇒ %q\n输出：\n%s", cmd, v, out)
		}
	}
}

// TestW083B3_CompactResult_ChineseOnlyOnIndentedLines 钉 `formatCompactResult` 的全部形态分支，
// 并**反向锁**：改造前的行内并列子串不得复现。
func TestW083B3_CompactResult_ChineseOnlyOnIndentedLines(t *testing.T) {
	cases := map[string]*ctxpkg.CompactResult{
		"growth_summarized": {Changed: true, RemovedCount: 2, PreTokens: 56, PostTokens: 73, PreMessageCount: 12, PostMessageCount: 11, MessagesSummarized: 2},
		"reduction":         {Changed: true, PreTokens: 200, PostTokens: 120, PreMessageCount: 20, PostMessageCount: 11},
		"no_token_change":   {Changed: true, PreTokens: 100, PostTokens: 100, PreMessageCount: 12, PostMessageCount: 12},
		"with_debug_info": {Changed: true, PreTokens: 100, PostTokens: 80, MessagesSummarized: 3,
			FallbackLevel: "rule_extract", StagesExecuted: []string{"micro", "ai"}},
	}
	for name, r := range cases {
		out := formatCompactResult(r, ctxpkg.DefaultCompactionConfig())
		if v := firstFormViolation(out); v != "" {
			t.Errorf("formatCompactResult(%s)：中文出现在非缩进行 ⇒ %q\n输出：\n%s", name, v, out)
		}
		// 反向锁：改造前的「英文+中文同行」子串必须已消失。
		for _, banned := range []string{
			"Historical messages summarized 历史消息已摘要", "Messages 消息数", "context increased 上下文增加",
		} {
			if strings.Contains(out, banned) {
				t.Errorf("formatCompactResult(%s) 仍含行内并列子串 %q：\n%s", name, banned, out)
			}
		}
	}
}

// TestW083B3_CompactLog_ChineseOnlyOnIndentedLines 钉 `/compact log` 的空态与记录态。
func TestW083B3_CompactLog_ChineseOnlyOnIndentedLines(t *testing.T) {
	m := newCompactTestModel(t)
	if out := m.handleCompactLog(); firstFormViolation(out) != "" {
		t.Errorf("空态 /compact log：%q\n%s", firstFormViolation(out), out)
	}

	// 经**真实压缩**产生 compact_boundary 记录，再读 log（不手工构造元数据）。
	m.session.SetMessages(append(m.session.GetMessages(), markerMessagesForTUI(6)...))
	_ = m.handleCompact("")
	out := m.handleCompactLog()
	if v := firstFormViolation(out); v != "" {
		t.Errorf("记录态 /compact log：中文出现在非缩进行 ⇒ %q\n输出：\n%s", v, out)
	}
	if !strings.Contains(out, "trigger=") {
		t.Fatalf("记录态 /compact log 应含记录行（trigger=…），实际：\n%s", out)
	}
}

// TestW083B3_PurgeCopy_ChineseOnlyOnIndentedLines 钉 `/compact purge` 的共享实现
// （TUI 与 plain runner 同源，见 handleCompactPurgeShared）。
func TestW083B3_PurgeCopy_ChineseOnlyOnIndentedLines(t *testing.T) {
	cases := map[string]string{
		"nil_store":  handleCompactPurgeShared(nil, "s"),
		"no_session": handleCompactPurgeShared(m2Store(t), ""),
	}
	for name, out := range cases {
		if v := firstFormViolation(out); v != "" {
			t.Errorf("handleCompactPurgeShared(%s)：中文出现在非缩进行 ⇒ %q\n输出：\n%s", name, v, out)
		}
	}
}

// TestW083B3_GoalOutputs_ChineseOnlyOnIndentedLines 钉 `/goal` 全子命令输出的形态。
//
// ★ /goal 的两条入口（`Model.handleGoalCmd` 与 `runnerCore.handleRunnerGoalCmd`）于本批
// 收口为共享 `goalCmdOutput`（“：原为两份逐字重复的实现）⇒ 钉共享函数即同时覆盖
// 两条路径（薄转发不含文案）。
//
// 数据用**英文**（objective="Buy milk"），把"我们自己写的 copy"从调用方数据里隔离出来判 ——
// 若用中文 objective，其数据行本就不缩进，会污染本门。
func TestW083B3_GoalOutputs_ChineseOnlyOnIndentedLines(t *testing.T) {
	// ① nil manager 分支（未初始化）。
	out := goalCmdOutput(nil, "t", "")
	if v := firstFormViolation(out); v != "" {
		t.Errorf("nil manager：中文出现在非缩进行 ⇒ %q\n%s", v, out)
	}
	if !strings.Contains(out, "Goal manager not initialized") {
		t.Errorf("nil manager 应含英文主行，实际 %q", out)
	}

	// ② 真实 manager（t.TempDir 隔离；nil client/metrics/tracker 对本命令无涉）。
	mgr := newGoalManager(t.TempDir(), nil, nil, nil, nil, nil, nil)

	// 不依赖既有 goal 的分支：默认 status（无 goal）/ help / 未知 / usage / list（空）。
	for _, args := range []string{"", "status", "help", "bogus", "set", "edit", "list"} {
		if o := goalCmdOutput(mgr, "t", args); firstFormViolation(o) != "" {
			t.Errorf("/goal %q：中文出现在非缩进行（行内并列？）⇒ %q\n输出：\n%s", args, firstFormViolation(o), o)
		}
	}

	// 第一次 set：无旧 goal ⇒ "Goal set"。
	if o := goalCmdOutput(mgr, "t", "set Buy milk"); firstFormViolation(o) != "" {
		t.Errorf("/goal set（首次）：%q\n%s", firstFormViolation(o), o)
	}
	// 第二次 set：旧 goal 仍 active ⇒ "Replaced the previous goal" 分支。
	if o := goalCmdOutput(mgr, "t", "set Buy oat milk"); firstFormViolation(o) != "" {
		t.Errorf("/goal set（替换）：%q\n%s", firstFormViolation(o), o)
	}
	// 有 goal 后的多行 status / 非空 list / 状态机三分支 / done --force（Unverified 分支）/ edit / clear。
	for _, args := range []string{"status", "list", "pause", "resume", "done --force", "edit Buy soy milk", "clear"} {
		if o := goalCmdOutput(mgr, "t", args); firstFormViolation(o) != "" {
			t.Errorf("/goal %q：%q\n%s", args, firstFormViolation(o), o)
		}
	}
}

// TestW083B3_MiscCommandOutputs_ChineseOnlyOnIndentedLines 钉 commands_misc.go 的八条命令输出形态：
// `/plan` `/privacy` `/stats` `/summary` `/thinkback` `/break-cache` `/onboarding`
// （`/think` 与其余英文命令本无汉字，不在本门范围）。
//
// 数据（思考原文 / 文件名 / 会话名 / objective）一律用**英文**，
// 把"我们自己写的 copy"从调用方数据里隔离出来判。
func TestW083B3_MiscCommandOutputs_ChineseOnlyOnIndentedLines(t *testing.T) {
	assert := func(t *testing.T, cmd, out string) {
		t.Helper()
		if v := firstFormViolation(out); v != "" {
			t.Errorf("%s：中文出现在非缩进行（行内并列？）⇒ %q\n输出：\n%s", cmd, v, out)
		}
	}

	t.Run("plan", func(t *testing.T) {
		m := createMinimalModel()
		for _, args := range []string{"", "on", "off", "toggle"} {
			assert(t, "/plan "+args, m.handlePlan(args))
		}
	})

	t.Run("privacy", func(t *testing.T) {
		m := createMinimalModel()
		for _, args := range []string{"", "show", "analytics-on", "analytics-off", "bogus"} {
			assert(t, "/privacy "+args, m.handlePrivacy(args))
		}
	})

	t.Run("stats", func(t *testing.T) {
		m := createMinimalModel()
		m.usageTracker = ai.NewUsageTracker("") // 覆盖「累计用量」分支
		m.usageTracker.RecordUsage("test-model", 1000, 2000, 100, 500)
		assert(t, "/stats", m.handleStats())
	})

	t.Run("summary", func(t *testing.T) {
		m := createMinimalModel()
		// 不足两条 ⇒ 早期返回分支。
		assert(t, "/summary(insufficient)", m.handleSummary(""))
		// 英文正文 ⇒ topic 落到 "(unnamed)"，文件名是数据（app.go）。
		m.session.SetMessages([]session.Message{
			{Role: "user", Blocks: []session.ContentBlock{{Type: "text", Text: "fix the bug in app.go"}}},
			{Role: "assistant", Blocks: []session.ContentBlock{{Type: "text", Text: "done"}}},
		})
		assert(t, "/summary", m.handleSummary(""))
	})

	t.Run("thinkback", func(t *testing.T) {
		// 无记录分支（含三态 hint）。
		m := createMinimalModel()
		assert(t, "/thinkback(empty)", m.handleThinkback(""))
		assert(t, "/thinkback all(empty)", m.handleThinkback("all"))

		// 有记录分支：思考原文用英文（数据），框架 copy 是我们自己的。
		m2 := createMinimalModel()
		s := session.NewSession()
		s.SetMessages([]session.Message{
			session.UserText("how to optimize"),
			session.AssistantWithTools(session.WithThinkingBlock(nil, "consider time and space complexity"), "test-model", nil),
			session.UserText("more"),
			session.AssistantWithTools(session.WithThinkingBlock(nil, "use a factory pattern"), "test-model", nil),
		})
		m2.sessionService.SetCurrentSession(s)
		assert(t, "/thinkback(latest)", m2.handleThinkback(""))
		assert(t, "/thinkback all", m2.handleThinkback("all"))
		assert(t, "/thinkback clear", m2.handleThinkback("clear"))
	})

	t.Run("break-cache", func(t *testing.T) {
		m := createMinimalModel()
		assert(t, "/break-cache status(normal)", m.handleBreakCache("status"))
		assert(t, "/break-cache", m.handleBreakCache("")) // 触发 + 计数
		assert(t, "/break-cache status(triggered)", m.handleBreakCache("status"))
	})

	t.Run("onboarding", func(t *testing.T) {
		m := createMinimalModel()
		assert(t, "/onboarding", m.handleOnboarding(""))
		assert(t, "/onboarding skip", m.handleOnboarding("skip"))
	})
}

// TestW083B3_DaemonOutputs_ChineseOnlyOnIndentedLines 钉 `/daemon` 全子命令
// 及 `formatDaemonStatus` / `formatDaemonTaskList` / `handleDaemonLog` 的形态。
//
// 走真实守护进程接口（newTestModel / newTestModelStarted → localDaemonController）；
// 任务 prompt 用英文，隔离出我们自己的 copy。表头是单行表格 ⇒ 英文单侧（同 priorityLabel）。
func TestW083B3_DaemonOutputs_ChineseOnlyOnIndentedLines(t *testing.T) {
	// 未运行态：usage / 未知 / 未运行分支 / log（文件未生成）。
	m := newTestModel(t)
	for _, args := range []string{"", "bogus", "stop", "add write tests", "list", "cancel zzz", "status", "log"} {
		if out := m.handleDaemon(args); firstFormViolation(out) != "" {
			t.Errorf("/daemon %q(idle)：中文出现在非缩进行 ⇒ %q\n%s", args, firstFormViolation(out), out)
		}
	}

	// 运行态：start（已运行）/ status / add / list（非空）/ cancel / log。
	ms := newTestModelStarted(t)
	for _, args := range []string{"start", "status"} {
		if out := ms.handleDaemon(args); firstFormViolation(out) != "" {
			t.Errorf("/daemon %q(running)：%q\n%s", args, firstFormViolation(out), out)
		}
	}
	if out := ms.handleDaemon("add write a unit test"); firstFormViolation(out) != "" {
		t.Errorf("/daemon add：%q\n%s", firstFormViolation(out), out)
	}
	if out := ms.handleDaemon("list"); firstFormViolation(out) != "" {
		t.Errorf("/daemon list：%q\n%s", firstFormViolation(out), out)
	}
	if out := ms.handleDaemon("cancel nonexistent"); firstFormViolation(out) != "" {
		t.Errorf("/daemon cancel：%q\n%s", firstFormViolation(out), out)
	}
}

// TestW083B3_DaemonPlainPath_EnglishOnly 反向保「两侧都验」（“）：
// `PlainRunner.handleDaemon` 是 `/daemon` 的**第二份实现**（plain/bridge 路径）。
// 它当前是**英文单侧** ⇒ 本门钉住这一现状：一旦退化成纯中文或行内并列，立即打红。
//
// ⚠️ 两路径形态**已不同**（TUI = 英文主 + 中文辅；plain = 英文单侧）——
// 属「双实现待收口」项（已登记总账 “），**不在本批顺手改**（改它会动 plain 的用户可见输出）。
func TestW083B3_DaemonPlainPath_EnglishOnly(t *testing.T) {
	r := newTestPlainRunner()
	r.daemonControl = &localDaemonController{daemon: newTestDaemon(t)}

	for _, args := range []string{"", "bogus", "stop", "add write tests", "list", "cancel zzz", "status", "log"} {
		if out := r.handleDaemon(args); hasHan(out) {
			t.Errorf("plain /daemon %q 含汉字（plain 路径应保持英文单侧）⇒ %q", args, out)
		}
	}
}
