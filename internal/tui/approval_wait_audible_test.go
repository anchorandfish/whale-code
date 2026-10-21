package tui

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// approval_wait_audible_test.go — C-1 / 「等，但出声」（发布1版分派清单块 C · 2026-10-10）
//
// 缺陷（真机实锤，总账）：批准面板弹出后用户未作答期间 —— 日志零行（151.9s 空白）、
// tick 停摆、无任何「我在等人」提示。拍板修法：① 等待开始/持续写日志（对齐三件套）；
// ② 面板计时；**不做**超时自决。
//
// 判据面（全部行为可观测，不读新增字段）：
// 日志面 —— slog.Default 换成内存 handler 捕获 whalelog 输出（whalelog.Info → slog）；
// 面板面 —— renderConfirmUI 渲染文本含计时行。
// 判别性：本组判据在旧语义（等待期 tick 直接 return nil，零日志零计时）下必然全红；
// 另用 -overlay mutant（把等待分支退回 return m, nil）做打红自证，见回执。
//
// ★ Update 是值接收者：测试必须**链式**接住返回的 Model（生产由 bubbletea 链式持有），
// 否则 approvalWaitStart 等值字段在下一发 tick 时仍是零值。

type syncLogBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newApprovalWaitModel(t *testing.T) Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.state = StateWaitingConfirm
	m.pendingConfirm = &PendingConfirm{Kind: ApprovalKindExec, ToolName: "bash", ToolInput: "sleep 100"}
	m.tickSeq = 42
	return *m
}

// withCapturedSlog 把 slog 默认 handler 换成内存捕获，返回还原函数。
func withCapturedSlog(t *testing.T) *syncLogBuf {
	t.Helper()
	buf := &syncLogBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestApprovalWait_StartAndPeriodicLogs 等待开始 + 周期日志（10s 间隔，3s 处不得出周期行）。
func TestApprovalWait_StartAndPeriodicLogs(t *testing.T) {
	m := newApprovalWaitModel(t)
	buf := withCapturedSlog(t)

	base := time.Now()
	updated, _ := m.Update(tickMsg{at: base, seq: 42})
	cur := updated.(Model)
	updated, _ = cur.Update(tickMsg{at: base.Add(3 * time.Second), seq: 42})
	cur = updated.(Model)
	updated, _ = cur.Update(tickMsg{at: base.Add(13 * time.Second), seq: 42})
	_ = updated.(Model)

	logs := buf.String()
	if !strings.Contains(logs, "awaiting user approval") || !strings.Contains(logs, "event=start") {
		t.Fatalf("等待开始应写 [audit] approval: awaiting user approval（event=start）日志:\n%s", logs)
	}
	if !strings.Contains(logs, "tool=bash") {
		t.Fatalf("等待日志应带工具名 tool=bash:\n%s", logs)
	}
	if n := strings.Count(logs, "awaiting user approval"); n != 2 {
		t.Fatalf("base/3s/13s 三发 tick 应恰好 start+periodic 各一条（3s 处不得出周期行），实际 %d 条:\n%s", n, logs)
	}
	if !strings.Contains(logs, "elapsed_s=13") {
		t.Fatalf("13s 处应补一条周期日志且带 elapsed_s=13:\n%s", logs)
	}
}

// TestApprovalWait_PanelShowsTimer 面板显示「Waiting for confirmation (Ns)」实时计时。
func TestApprovalWait_PanelShowsTimer(t *testing.T) {
	m := newApprovalWaitModel(t)
	withCapturedSlog(t) // 挂空捕获，防日志漏到测试输出

	base := time.Now()
	updated, _ := m.Update(tickMsg{at: base, seq: 42})
	cur := updated.(Model)
	updated, _ = cur.Update(tickMsg{at: base.Add(13 * time.Second), seq: 42})
	cur = updated.(Model)

	ui := ansi.Strip(cur.renderConfirmUI())
	if !strings.Contains(ui, "Waiting for confirmation (13s)") {
		t.Fatalf("审批面板应显示英文主行计时（13s）:\n%.500s", ui)
	}
	if !strings.Contains(ui, "等待确认中（已等 13s）") {
		t.Fatalf("审批面板应显示中文辅助行计时:\n%.500s", ui)
	}
}

// TestApprovalWait_EndLogOnDecision 决策离开等待态后，下一发 tick 落结束日志并复位。
func TestApprovalWait_EndLogOnDecision(t *testing.T) {
	m := newApprovalWaitModel(t)
	buf := withCapturedSlog(t)

	base := time.Now()
	updated, _ := m.Update(tickMsg{at: base, seq: 42})
	cur := updated.(Model)

	// 用户决策（此处直接模拟决策后的状态：pendingConfirm 清空 + 回 Idle；
	// 生产路径 confirmResultMsg 分支会 SetPendingConfirm(nil) 并重启 tick）
	cur.pendingConfirm = nil
	cur.state = StateIdle

	updated, _ = cur.Update(tickMsg{at: base.Add(20 * time.Second), seq: 42})
	_ = updated.(Model)

	logs := buf.String()
	if !strings.Contains(logs, "wait ended") || !strings.Contains(logs, "total_s=20") {
		t.Fatalf("等待结束应落一条带总时长的结束日志（total_s=20）:\n%s", logs)
	}
}
