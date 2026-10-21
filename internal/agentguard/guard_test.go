package agentguard

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// P0-1 交互模式轮次上限：默认 40 轮到限暂停交回控制权（非报错退出）
func TestTurnLimitStopsWithProgressSnapshot(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	var last Decision
	for i := 0; i < 40; i++ {
		last = g.BeforeModelCall()
		if last.Verdict != VerdictProceed {
			t.Fatalf("round %d: verdict = %v, want Proceed（40 轮内不应停）", i+1, last.Verdict)
		}
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FilesChanged: []string{"/repo/a.go"}})
	}

	d := g.BeforeModelCall()
	if d.Verdict != VerdictStop {
		t.Fatalf("第 41 轮 verdict = %v, want Stop", d.Verdict)
	}
	if d.Report == nil {
		t.Fatal("到限停机必须附带进度快照 Report（交回控制权非报错退出）")
	}
	rendered := d.Report.Render()
	for _, want := range []string{"任务进度快照", "已完成", "下一步", "继续"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("进度快照缺少 %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(rendered, "/repo/a.go") {
		t.Errorf("进度快照应列出已完成改动的文件:\n%s", rendered)
	}
}

// P0-1 BeginRequest 重置：新用户请求从零计数
func TestBeginRequestResetsTurnCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTurns = 2
	g := NewGuard(cfg)
	g.BeginRequest()
	g.BeforeModelCall()
	g.AfterToolResults(false)
	g.AfterTurn(TurnObservation{})
	g.BeforeModelCall()
	g.AfterToolResults(false)
	g.AfterTurn(TurnObservation{})
	if d := g.BeforeModelCall(); d.Verdict != VerdictStop {
		t.Fatalf("2 轮后应 Stop, got %v", d.Verdict)
	}

	// 新请求重置
	g.BeginRequest()
	if d := g.BeforeModelCall(); d.Verdict != VerdictProceed {
		t.Fatalf("新请求应从 Proceed 开始, got %v", d.Verdict)
	}
	if g.TotalTurns() != 0 {
		t.Fatalf("新请求后 TotalTurns = %d, want 0", g.TotalTurns())
	}
}

// P0-2 无进展熔断：连续 3 轮工具调用全失败 → 停下问用户
func TestConsecutiveToolFailuresTripBreaker(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	for i := 1; i <= 2; i++ {
		d := g.AfterToolResults(true)
		if d.Verdict != VerdictProceed {
			t.Fatalf("第 %d 轮全失败不应立即停, got %v", i, d.Verdict)
		}
	}
	d := g.AfterToolResults(true)
	if d.Verdict != VerdictStop {
		t.Fatal("连续第 3 轮全失败必须停机问用户")
	}
	if d.Report == nil || !strings.Contains(d.Report.Render(), "连续") {
		t.Fatal("熔断停机必须说明连续失败原因")
	}
}

// P0-2 成功工具结果重置熔断计数
func TestToolSuccessResetsFailureCounter(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()
	g.AfterToolResults(true)
	g.AfterToolResults(true)
	// 一轮部分成功（非全失败）→ 重置
	g.AfterToolResults(false)
	g.AfterTurn(TurnObservation{})
	d := g.AfterToolResults(true)
	if d.Verdict != VerdictProceed {
		t.Fatalf("重置后单轮失败不应停, got %v", d.Verdict)
	}
}

// P0-4 默认（自主模式关闭）：到限不自动续跑，直接停
func TestTurnLimitStopsWhenAutonomyOff(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTurns = 1
	g := NewGuard(cfg)
	g.BeginRequest()
	g.BeforeModelCall()
	g.AfterToolResults(false)
	// 收敛信号（失败测试数下降）
	g.AfterTurn(TurnObservation{FailingTests: 2, FilesChanged: []string{"/repo/a.go"}})

	d := g.BeforeModelCall()
	if d.Verdict != VerdictStop {
		t.Fatalf("自主模式默认关闭，到限应停（不静默续跑）, got %v", d.Verdict)
	}
}

// P0-4 自主模式开启 + 收敛：静默续跑下一预算段（段+1、段内轮次归零）
func TestAutonomyExtendsWhenConverging(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTurns = 3
	cfg.Autonomy = "full"
	g := NewGuard(cfg)
	g.BeginRequest()

	// 第 1~3 轮：失败测试数单调下降（收敛信号需 3 轮观测）
	for _, failing := range []int{8, 5, 2} {
		if d := g.BeforeModelCall(); d.Verdict != VerdictProceed {
			t.Fatalf("段内 Proceed 失败: %v", d.Verdict)
		}
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FailingTests: failing, FilesChanged: []string{"/repo/a.go"}})
	}

	d := g.BeforeModelCall()
	if d.Verdict != VerdictExtend {
		t.Fatalf("收敛 + autonomy=full 应续跑, got %v", d.Verdict)
	}
	if d.Segment != 2 {
		t.Errorf("续跑后段号 = %d, want 2", d.Segment)
	}
	// 续跑后新段从 0 计数（累计轮次保留）
	if d2 := g.BeforeModelCall(); d2.Verdict != VerdictProceed {
		t.Fatalf("新段第 1 轮应 Proceed, got %v", d2.Verdict)
	}
	if got := g.TotalTurns(); got != 3 {
		t.Errorf("累计轮次应跨段保留, got %d, want 3", got)
	}
}

// P0-4 自主模式开启 + 空转：到限不续跑，停机且附"卡在哪、试过什么"
func TestAutonomyStopsWhenSpinning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTurns = 3
	cfg.Autonomy = "full"
	g := NewGuard(cfg)
	g.BeginRequest()

	output := "我在检查 runner_core.go 的实现是否符合预期，暂时没有发现新问题"
	for i := 0; i < 3; i++ {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		// 空转信号：连续 3 轮输出高度相似 + 同一文件反复读
		g.AfterTurn(TurnObservation{FilesRead: []string{"/repo/runner_core.go"}, Output: output})
	}

	d := g.BeforeModelCall()
	if d.Verdict != VerdictStop {
		t.Fatalf("空转 + 到限应停, got %v", d.Verdict)
	}
	rendered := d.Report.Render()
	for _, want := range []string{"卡在", "试过"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("空转停机摘要缺少 %q:\n%s", want, rendered)
		}
	}
}

// P0-4 收敛续跑受 MaxSegments 封顶（防无限续跑）
func TestAutonomyExtensionCappedByMaxSegments(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTurns = 3
	cfg.MaxSegments = 2
	cfg.Autonomy = "full"
	g := NewGuard(cfg)
	g.BeginRequest()

	// 段 1：3 轮收敛 → 续跑到段 2
	for _, failing := range []int{9, 6, 3} {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FailingTests: failing, FilesChanged: []string{"/repo/a.go"}})
	}
	if d := g.BeforeModelCall(); d.Verdict != VerdictExtend {
		t.Fatalf("首次到限且收敛应续跑, got %v", d.Verdict)
	}
	// 段 2：继续收敛但已达 MaxSegments=2，无法再续
	for _, failing := range []int{6, 4, 2} {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FailingTests: failing, FilesChanged: []string{"/repo/b.go"}})
	}
	if d := g.BeforeModelCall(); d.Verdict != VerdictStop {
		t.Fatalf("超过 MaxSegments 应停, got %v", d.Verdict)
	}
}

// P0-3 审查子循环上限：反复重读同一文件（原地打转、无文件改动）→ 停机输出分歧摘要。
// 语义（2026-09-22 修正）：第 1 次读某文件算「前进」（收集信息，清零），
// 连续重读同一文件才算「空转」（累计）。故触发需重读 4 次（第 1 次读 + 4 次重读）。
// P0-3 审查子循环达到提醒间隔 → 注入**软提醒**，不停机。
//
// 改判依据（2026-09-22 方案 A）：「无文件改动」只是空转的**有偏代理**
// —— 合法的只读研究会被误判，而「改了文件但空转」反而抓不到。
// 不可靠信号不得拥有停机这种不可逆权力，故降级为提醒；
// 强制停机交给 P0-1（轮次）与 P0-4（重复检测）。
func TestReviewLoopCapEmitsReminderNotStop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReviewRounds = 2
	g := NewGuard(cfg)
	g.BeginRequest()

	// 场景：反复读**同一批已读过的**文件（无新文件 ⇒ 计入 P0-3），
	// 但每轮输出**明显不同**、且读取次数不超过 P0-4 的 maxFileReads=5
	// （否则 P0-4 会抢先停机，测不到 P0-3 本身）。
	plan := []struct {
		file   string
		output string
	}{
		{"/repo/a.go", "读取配置，确认连接串指向 localhost"},
		{"/repo/b.go", "检查中间件链，鉴权顺序在日志之后"},
		{"/repo/a.go", "复查配置，发现超时字段未设置"},
		{"/repo/b.go", "复查中间件，新增分支缺少覆盖"},
		{"/repo/a.go", "汇总以上，准备给出修改建议"},
	}
	for _, step := range plan {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FilesRead: []string{step.file}, Output: step.output})
	}

	// 计数 3 > 2 ⇒ 发提醒；**不得停机**
	d := g.BeforeModelCall()
	if d.Verdict != VerdictRemind {
		t.Fatalf("P0-3 超限应发软提醒而非停机, got %v", d.Verdict)
	}
	if strings.TrimSpace(d.Reminder) == "" {
		t.Fatal("软提醒必须携带 Reminder 文本（供注入模型），不得为空")
	}
	if !strings.Contains(d.Reminder, "no file changes") {
		t.Fatalf("Reminder 应说明「无文件改动」并给出收敛建议，实际 %q", d.Reminder)
	}
	if d.Report != nil {
		t.Fatal("软提醒不得附停机快照（它本就不是停机）")
	}
	// 提醒后计数归零 ⇒ 下一轮不再提醒（周期性 nudge，不是每轮刷屏）
	if n := g.BeforeModelCall(); n.Verdict != VerdictProceed {
		t.Fatalf("提醒后计数应归零，下一轮不应再提醒, got %v", n.Verdict)
	}
}

// P0-3 文件改动重置审查计数（改动 = 不是纯审查轮）
func TestFileChangeResetsReviewCounter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReviewRounds = 3
	g := NewGuard(cfg)
	g.BeginRequest()

	for i := 1; i <= 3; i++ {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FilesRead: []string{"/repo/a.go"}, Output: fmt.Sprintf("第 %d 轮分析", i)})
	}
	// 一轮有文件改动 → 重置
	g.BeforeModelCall()
	g.AfterToolResults(false)
	g.AfterTurn(TurnObservation{FilesChanged: []string{"/repo/a.go"}, FilesRead: []string{"/repo/b.go"}, Output: "修复了问题"})

	// 再来 3 轮纯读也不应停（计数已重置；轮次远未到限、无空转信号）
	for i := 1; i <= 3; i++ {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FilesRead: []string{fmt.Sprintf("/repo/c%d.go", i)}, Output: fmt.Sprintf("第 %d 轮复查", i)})
	}
	// 第 7 轮前：轮次 7 << 40、审查计数 3（不 > 3）、读取均 ≤ 阈值、输出互不相同 → Proceed
	if d := g.BeforeModelCall(); d.Verdict != VerdictProceed {
		t.Fatalf("审查计数重置后不应停, got %v (reason: %s)", d.Verdict, d.Reason)
	}
}

// 可观测性（canary）：每轮 AfterTurn 落一行 guard 状态日志（turns/failing/changed），
// 人工验证失败时可从日志复盘防护状态轨迹。断言的是结果（日志行真实存在且数值正确），
// 非仅“执行了”。
func TestAfterTurnEmitsStateLogLine(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	g := NewGuard(DefaultConfig())
	g.BeginRequest()
	for i := 1; i <= 3; i++ {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{
			FailingTests: 6 - i, // 5,4,3 单调下降
			FilesChanged: []string{fmt.Sprintf("/repo/f%d.go", i)},
			FilesRead:    []string{"/repo/a.go"},
		})
	}

	out := buf.String()
	lines := strings.Count(strings.TrimSpace(out), "\n") + 1
	if lines != 3 {
		t.Fatalf("3 轮应产生 3 行状态日志, got %d 行:\n%s", lines, out)
	}
	// 第 2 行应含第 2 轮的准确状态
	for _, want := range []string{
		"turns=2", "total=2", "segment=1",
		"failing=4", "changed=1", "read=1", "review_rounds=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guard 状态日志缺少 %q:\n%s", want, out)
		}
	}
}

// TestLegitReadOnlyTaskNotCountedAsReviewSpin 判别：合法的只读研究任务
// （每轮读的是**从未读过的新文件**、输出各不相同）不应被 P0-3 审查子循环
// 上限误判为「自我审查空转」而停机。
//
// 反例（修复前）：P0-3 用「本轮无文件改动」作为空转代理，会把这个合法任务
// 在连续 >3 轮后误停 —— 真机踩坑：让模型「依次读 f1/f2/f3」三文件即触发
// 「连续 N 轮无文件改动（疑似自我审查空转）」停机。
func TestLegitReadOnlyTaskNotCountedAsReviewSpin(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReviewRounds = 3
	g := NewGuard(cfg)
	g.BeginRequest()

	// 连续 4 轮纯读，但每轮读的是不同的新文件、输出各不相同（实质前进）。
	for i := 1; i <= 4; i++ {
		g.BeforeModelCall()
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{
			FilesRead: []string{fmt.Sprintf("/repo/new_file_%d.go", i)},
			Output:    fmt.Sprintf("读到 %d 号文件的独有内容：字段 %d 定义在 42 行", i, i),
		})
	}

	// 修复后：读新文件 = 实质前进，不应累计为审查空转 → 第 5 轮仍 Proceed。
	if d := g.BeforeModelCall(); d.Verdict != VerdictProceed {
		t.Fatalf("合法只读研究任务（每轮读新文件）不应被判审查空转停机, got %v (reason: %s)", d.Verdict, d.Reason)
	}
}

// TestRereadSameFileStillCountsAsReviewSpin 判别：反复读**同一个**文件（原地打转）
// 仍应被判停 —— 但**接住它的必须是 P0-4（重复检测），不是 P0-3**。
//
// ★ 这是「P0-3 降级」的**不退化守卫**：降级后真空转绝不能逃脱。
// 反复读同一文件 7 次 ⇒ readCounts=7 > maxFileReads=5 ⇒ P0-4 Spinning 成立 ⇒ 停机。
func TestRereadSameFileStillCaughtBySpinningNotP0_3(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReviewRounds = 3
	g := NewGuard(cfg)
	g.BeginRequest()

	for i := 1; i <= 7; i++ {
		if d := g.BeforeModelCall(); d.Verdict == VerdictStop {
			if d.Report == nil || !strings.Contains(d.Report.Cause, "空转") {
				t.Fatalf("停机原因应来自 P0-4 重复检测（含「空转」），实际 %v", d.Report)
			}
			if strings.Contains(d.Report.Cause, "无文件改动") {
				t.Fatalf("不得再由 P0-3 停机（已降级为提醒），实际 %q", d.Report.Cause)
			}
			return // P0-4 接住真空转 ⇒ 守卫成立
		}
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{
			FilesRead: []string{"/repo/same_file.go"}, // 每轮重读同一文件
			Output:    fmt.Sprintf("第 %d 轮审查：仍在检查同一处", i),
		})
	}

	t.Fatal("反复重读同一文件 7 轮仍未被判停 —— P0-3 降级导致真空转逃脱（保护退化）")
}
