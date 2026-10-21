package tools

// · 一次工具调用的「决策」与「执行」必须共用同一关联键 `call_id`
//
// ---------------------------------------------------------------------------
// 缺陷面（修改前）
//
// 一次工具调用的两个事实落在**两个通道**、彼此没有共同键：
//
//	决策面 = `~/.whale/logs/whale.log` 的
// `[audit] tool: rejected`（pipeline.go，G9-② 落）
// `[audit] tool: confirm required`（pipeline.go，-R1 落）
// —— **只有 tool / stage / failure_kind / reason**
//	执行面 = `[audit] tool: execute done`（executor.go）
// —— **只有 tool / is_error / elapsed_ms**
//
// 后果：**同批里出现两条同名工具调用时，事后完全无法把某条"执行"对齐回它对应
// 的那条"决策"**（哪条被问了、哪条被拒了）。这正是 的形态。
// 票面拍板 C：双向 `call_id`（P2）。
//
// ---------------------------------------------------------------------------
// 判别性（可判别性自检）
//
// ★ 反例注入：把 `pipeline.go` 的 `"call_id", call.ID,` 两行 / `executor.go` 的
// 一行删掉 ⇒ 本文件**必须红**（断言 `CallID == 入参 ID`，空串即失败）。
// ★ 为什么不用"两条日志时间相邻"当判据：时间相邻 ≠ 同一调用（同批并发执行时
// 尤其不可靠）—— 票面明确否定该伪判据。
// ★ 观测面刻意选**磁盘日志文件**（生产真实出口），不是"调用了某个 logger"：
// 后者是接线断言，属假绿。
//
// ---------------------------------------------------------------------------
// 为什么三条用例要分别跑：它覆盖**两个通道 × 三种结局**（拒 / 问 / 放）。
// 只测其一会把"某一侧漏填"读成通过。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	whalelog "github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// n078LogRec 一条 JSON 日志记录（只声明本文件用到的字段）。
type n078LogRec struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
	Stage  string `json:"stage"`
}

// n078WithAuditLog 起一套生产形态的文件日志 sink（默认级别 Info）。
func n078WithAuditLog(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	whalelog.SetLevel(slog.LevelInfo)

	logPath := filepath.Join(t.TempDir(), "whale.log")
	if err := whalelog.InitFileLogger(logPath, 10, 2, 1); err != nil {
		t.Fatalf("InitFileLogger: %v", err)
	}
	t.Cleanup(func() {
		whalelog.CloseFileLogger()
		whalelog.SetLevel(slog.LevelInfo)
	})
	return logPath
}

func n078ReadLog(t *testing.T, logPath string) []n078LogRec {
	t.Helper()
	whalelog.CloseFileLogger() // flush
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", logPath, err)
	}
	var out []n078LogRec
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec n078LogRec
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("该行不是 JSON 日志（%v）：%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

func n078Filter(recs []n078LogRec, msgSubstr string) []n078LogRec {
	var out []n078LogRec
	for _, r := range recs {
		if strings.Contains(r.Msg, msgSubstr) {
			out = append(out, r)
		}
	}
	return out
}

func n078Dump(recs []n078LogRec) string {
	var b strings.Builder
	for i, r := range recs {
		fmt.Fprintf(&b, "  [%d] level=%s msg=%q call_id=%q tool=%q stage=%q\n",
			i, r.Level, r.Msg, r.CallID, r.Tool, r.Stage)
	}
	if b.Len() == 0 {
		return "  （日志文件为空）"
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 判据 ① · 决策面（拒绝）：`call_id` 必须是**入参的 ID**
// ---------------------------------------------------------------------------

func TestN078_DecisionChannelRejectionCarriesCallID(t *testing.T) {
	logPath := n078WithAuditLog(t)

	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)

	outside := filepath.Join(filepath.Dir(ws), "n078_outside.txt")
	defer os.Remove(outside)

	const wantID = "n078-decision-reject-001"
	pr := p.Execute(context.Background(), Call{
		ID:       wantID,
		Name:     "write_file",
		Input:    fmt.Sprintf(`{"path":%q,"content":"hi"}`, outside),
		Approved: true,
	})

	// 前置断言：这条调用**真的被管线拒绝了**，否则退化成空测。
	if pr.Proceed {
		t.Fatalf("前提失效：越界写未被拒绝（Proceed=true）——本用例会变成空测。stageLogs=%+v", pr.StageLogs)
	}

	recs := n078ReadLog(t, logPath)
	rejected := n078Filter(recs, "tool: rejected")
	if len(rejected) == 0 {
		t.Fatalf("前提失效：决策面零留痕（`[audit] tool: rejected` 未落盘）。全部记录：\n%s", n078Dump(recs))
	}
	for i, rec := range rejected {
		if rec.CallID != wantID {
			t.Errorf(" 决策面留痕 #%d 的 call_id = %q，期望 %q ⇒ 执行面无法对齐回本条决策。\n全部记录：\n%s",
				i, rec.CallID, wantID, n078Dump(recs))
		}
	}
}

// ---------------------------------------------------------------------------
// 判据 ② · 决策面（确认诉求）：同上
// ---------------------------------------------------------------------------

func TestN078_DecisionChannelConfirmCarriesCallID(t *testing.T) {
	logPath := n078WithAuditLog(t)

	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)

	const wantID = "n078-decision-confirm-001"
	// 危险档命令 ⇒ DangerousGuardStage 命中并留痕 `confirm required`。
	pr := p.Execute(context.Background(), Call{
		ID:    wantID,
		Name:  "bash",
		Input: `{"command":"shutdown -h now"}`,
	})

	if !pr.NeedConfirm {
		t.Fatalf("前提失效：危险档命令应触发确认，实际 Proceed=%v NeedConfirm=%v（stageLogs=%+v）",
			pr.Proceed, pr.NeedConfirm, pr.StageLogs)
	}

	recs := n078ReadLog(t, logPath)
	confirm := n078Filter(recs, "tool: confirm required")
	if len(confirm) == 0 {
		t.Fatalf("前提失效：确认面零留痕（`[audit] tool: confirm required` 未落盘）。全部记录：\n%s", n078Dump(recs))
	}
	for i, rec := range confirm {
		if rec.CallID != wantID {
			t.Errorf(" 确认面留痕 #%d 的 call_id = %q，期望 %q。\n全部记录：\n%s",
				i, rec.CallID, wantID, n078Dump(recs))
		}
	}
}

// ---------------------------------------------------------------------------
// 判据 ③ · 执行面：`execute done` 必须带**同一个** `call_id`
// ---------------------------------------------------------------------------

func TestN078_ExecutionChannelCarriesCallID(t *testing.T) {
	logPath := n078WithAuditLog(t)

	ws := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, ws)
	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	p := NewToolPipeline(r, e, hook.NewManager(ws), policy, ws)

	const wantID = "n078-exec-001"
	target := filepath.Join(ws, "n078_written.txt")
	pr := p.Execute(context.Background(), Call{
		ID:    wantID,
		Name:  "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"hi"}`, target),
	})

	// 前置断言：这条调用**真的被执行了**（否则执行面留痕不存在，用例退化）。
	if !pr.Proceed || pr.Result == nil {
		t.Skipf("前提不成立：工作区内写未被执行（Proceed=%v Result==nil=%v stageLogs=%+v）——"+
			"本用例的前提是「工作区内写免问放行」；若该前提已被改，请改夹具而不是让本用例假绿",
			pr.Proceed, pr.Result == nil, pr.StageLogs)
	}

	recs := n078ReadLog(t, logPath)
	done := n078Filter(recs, "tool: execute done")
	if len(done) == 0 {
		t.Fatalf("前提失效：执行面零留痕（`[audit] tool: execute done` 未落盘）。全部记录：\n%s", n078Dump(recs))
	}
	for i, rec := range done {
		if rec.CallID != wantID {
			t.Errorf(" 执行面留痕 #%d 的 call_id = %q，期望 %q ⇒ 与环境中的决策留痕无法配对。\n全部记录：\n%s",
				i, rec.CallID, wantID, n078Dump(recs))
		}
	}
}
