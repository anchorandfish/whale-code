package tools

// —— rm 降为可批准档：生产入口端到端判据。
//
// 拍板：2026-10-14 用户（推翻 10-05 拍板中"rm 保持硬拒"的部分）。
// 用户原话："rm 这些也可以走审批……你最多提醒这是高危操作，但是直接不让操作是不是不对。"
//
// 业内对标（本地快照实测）：同类实现 ——
// "We prefer to prompt the user rather than outright forbid the command"；
// rm -f/-rf ⇒ Decision::Prompt；仅 AskForApproval::Never ⇒ Forbidden。
//
// 生产入口（R1，与 同一套装配）：`InitPipelineWithHooks` + `ExecuteWithConfirm`
// —— 用户在 TUI 按下 Yes 后带 Approved=true 重跑整条管线（executor.go:983），
// 经 sandbox_sanction.go 的 sanctioned 通道在**沙箱外**执行这一条。
//
// 分层判据（R2 唯一例外形态说明）：
// - TestW099_RmAtCommandPosition_UserApprovalTakesEffect：删除目标是
// t.TempDir() 下**不存在**的探针名 + `echo ran-w098` 哨兵 ⇒ 即使执行也零副作用。
// - TestW099_RmCatastrophicForm_NeverPrompted：被测命令是 `rm -rf /`（真不可逆）
// ⇒ 按 R2"被测动作不可逆时只掐 sink"：bash handler 换成哨兵桩
// （⚠️ 安全：防 rm -rf / 真执行），断言只打在决策面
// （asked / NeedConfirm / Proceed / FailureKind），桩返回值作"未被执行"的旁证。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestW099_RmAtCommandPosition_UserApprovalTakesEffect 主判据。
//
// before（rm 硬拒）预期红：asked==0（用户连批准框都看不到）、FailureKind=sandbox_denied。
// after 预期绿：弹审批 → 批准 → 命令真的执行（哨兵可见）。
func TestW099_RmAtCommandPosition_UserApprovalTakesEffect(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	rmCmd := `rm -f ` + workDir + `/w099-no-such-probe && echo ran-w098`

	// 前提自检：rm 仍命中 deny 规则（拦下来了），只是"能不能被批准"变了。
	if allowed, reason := e.Sandbox.Validate(rmCmd); allowed {
		t.Fatalf("前提不成立：rm 仍应被沙箱规则命中（reason=%q）", reason)
	}

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "w099-rm-approve",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": rmCmd}),
	})

	if asked == 0 {
		t.Fatalf("用户从未被询问 —— rm 已拍板降为可批准档，必须给出确认框:\n"+
			"  proceed=%v failureKind=%q rejection=%q stageLogs=%v",
			pr.Proceed, pr.FailureKind, pr.Rejection, pr.StageLogs)
	}
	if !pr.Proceed {
		t.Fatalf("用户已批准，rm 仍被拒（\"批准了还拒\"）:\n"+
			"  failureKind=%q rejection=%q stageLogs=%v", pr.FailureKind, pr.Rejection, pr.StageLogs)
	}
	if pr.FailureKind == FailureKindSandboxDenied {
		t.Fatalf("批准后仍报 sandbox_denied：%+v", pr)
	}
	if pr.Result == nil {
		t.Fatalf("批准后命令应真的执行，却无执行结果：%+v", pr)
	}
	if out := pr.Result.Output; !strings.Contains(out, "ran-w098") {
		t.Fatalf("哨兵未出现，命令可能没真跑：output=%q", out)
	}
}

// TestW099_RmAtCommandPosition_UserDenialStillBlocks 配对反例：
// 分档不等于放宽 —— 用户按 No 时必须照样拦住、不执行。
// before 态也绿（before 连问都不问就拒）⇒ 不具判别性，钉"不变成自动放行"。
func TestW099_RmAtCommandPosition_UserDenialStillBlocks(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	e.OnConfirm = func(string, string) bool { return false }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "w099-rm-deny",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": `rm -f ` + workDir + `/w099-no-such-probe`}),
	})

	if pr.Proceed {
		t.Fatalf("用户按 No，rm 不得执行：%+v", pr)
	}
	if pr.Result != nil {
		t.Fatalf("用户按 No，却拿到了执行结果：%+v", pr.Result)
	}
	if pr.FailureKind != FailureKindPolicyDenied {
		t.Errorf("用户拒绝的 FailureKind 应为 %q，实际 %q（rejection=%q）",
			FailureKindPolicyDenied, pr.FailureKind, pr.Rejection)
	}
}

// TestW099_RmCatastrophicForm_NeverPrompted 灾难形态守卫（R2 sink 桩例外）。
//
// `rm -rf /` 必须连批准框都不弹（validator.AssessRmRfDangerousTarget 钉回硬拒，
// 与执行链 RM-RF-AST Critical 同源）—— 否则会出现"弹框→批准→仍被拒"。
// ⚠️ 安全：bash handler 已替换为哨兵桩，rm -rf / 物理上不可能执行；
// 断言全部打在决策面。
func TestW099_RmCatastrophicForm_NeverPrompted(t *testing.T) {
	workDir := t.TempDir()
	e := newProdExecutor(t, workDir)

	// ★ 只掐 sink（R2 唯一例外）：被测动作不可逆。裁决链 100% 是生产码。
	sinkRan := false
	e.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		sinkRan = true
		return "W098-SINK-STUBBED", nil
	}

	asked := 0
	e.OnConfirm = func(string, string) bool { asked++; return true }

	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "w099-rm-catastrophic",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": `rm -rf /`}),
	})

	if asked != 0 {
		t.Fatalf("灾难形态 rm -rf / 不该弹确认框（批准框会被白收一次）：asked=%d", asked)
	}
	if pr.NeedConfirm {
		t.Fatalf("灾难形态不得降级成确认：%+v", pr)
	}
	if pr.Proceed {
		t.Fatalf("rm -rf / 应被拒，实际放行：%+v", pr)
	}
	if pr.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("FailureKind 应为 %q，实际 %q（rejection=%q）",
			FailureKindSandboxDenied, pr.FailureKind, pr.Rejection)
	}
	if sinkRan {
		t.Fatalf("哨兵桩被执行 —— rm -rf / 穿透了预检，这是安全事故：%+v", pr)
	}
	_ = workDir
}
