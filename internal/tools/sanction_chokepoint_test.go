package tools

// —— TUI 面板批准后命令仍被沙箱拒绝：批准标记没到执行链。判据。
//
// 三方互证（取证回执 2026-10-08）：账本 seq10 `approved/user` @09:26:29.399 →
// 3ms 后 execute done is_error=true elapsed_ms=0。根因： 的批准转述
// （ctx 标记）唯一产生点绑死在 plain 路径的 OnConfirm 分支（executor.go:997）；
// TUI 面板批准的重跑（tool_exec.go:201 `Approved: i==startIndex`）与子代理
// 批准恢复（agent.go:1023 `call.Approved = true`）都走 Pipeline.Execute 重入，
// 不经过那个分支 ⇒ 标记消失 ⇒ prepareSandbox 用 legacy Sandbox.Validate 再拒一次。
//
// 修法（事实挂在真身、转述放在咽喉）：产生点收口到 ExecuteStage（三入口必经的
// handler 调用点），判据 = **原始入口的** call.Approved（规则门/记忆改写之前算好，
// 存 PipelineState）&& !ApprovedByRule && Classify==TierConfirm。
//
// 入口形态覆盖：
//
//	B1 plain（ExecuteWithConfirm + OnConfirm 重跑）⇒ 既有 TestN122 主判据覆盖；
//	B2 TUI 重跑 / 子代理恢复（两者同构：Pipeline.Execute + Approved=true）
// ⇒ 本文件主判据，改前基线上必须**红**；
//	B3 反向守卫：未批准（首次运行）必须仍询问 ⇒ 防止修成"自动放行"。
//
// 样本选 IFS 而非 $(date)： 修 A 会把无害 $(date) 翻成放行档，
// 本判据需要一个**修 A 之后仍是确认档**的命令，否则前提自检即失效。

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// w044ConfirmCmd 命中 IFS 注入规则（TierConfirm），执行无害、输出可观测。
const w044ConfirmCmd = `IFS=:; echo tick`

func w044Input() string {
	return mustMarshal(map[string]interface{}{"command": w044ConfirmCmd})
}

// TestW048_PanelApprovalReachesExecutionChain 主判据（B2 形态）。
func TestW048_PanelApprovalReachesExecutionChain(t *testing.T) {
	e := newProdExecutor(t, t.TempDir())

	// 前提自检 ①：样本确实是可批准档（修 A 之后仍成立）。
	tier, rule, _ := e.Sandbox.Classify(w044ConfirmCmd)
	if tier != sandbox.TierConfirm {
		t.Fatalf("前提不成立：样本应落确认档，实际 tier=%v rule=%s", tier, rule)
	}
	// 前提自检 ②：执行链确实会拦它（否则"批准放行"无从谈起）。
	if ok, reason := e.Sandbox.Validate(w044ConfirmCmd); ok {
		t.Fatalf("前提不成立：执行链本应拒绝（reason=%q）", reason)
	}

	// B3 反向守卫：未批准的首次运行必须询问（不得被修成自动放行）。
	pr1 := e.Pipeline.Execute(context.Background(), Call{
		ID: "w048-first", Name: "bash", Input: w044Input(),
	})
	if !pr1.NeedConfirm {
		t.Fatalf("未批准的首次运行应 NeedConfirm（B3 守卫）：%+v", pr1)
	}

	// B2 主判据：TUI 面板批准后的重跑形态（tool_exec.go:201 / agent.go:1023 同款）。
	pr2 := e.Pipeline.Execute(context.Background(), Call{
		ID: "w048-rerun", Name: "bash", Input: w044Input(), Approved: true,
	})
	if !pr2.Proceed {
		t.Fatalf("面板批准后重跑仍被拒 —— 这正是  \"批了白批\":\n"+
			"  rejection=%q stageLogs=%v", pr2.Rejection, pr2.StageLogs)
	}
	if pr2.Result == nil {
		t.Fatalf("批准后命令应真的执行，却无执行结果：%+v", pr2)
	}
	if pr2.Result.IsError {
		t.Fatalf("批准后命令执行报错（执行链仍在校验批准过的命令）：%s", pr2.Result.Output)
	}
	if !strings.Contains(pr2.Result.Output, "tick") {
		t.Fatalf("命令应真的跑出 tick，实际输出=%q", pr2.Result.Output)
	}
}

// TestW048_MemoryApprovalDoesNotSanction 纪律守卫：会话记忆批准
// （policy.Has 推导，非本次用户按下 Yes）不得放大成沙箱豁免。
//
// 结构性保证：咽喉判据用的是**规则门改写之前**的原始 call.Approved，
// 记忆批准发生在管线内部 ⇒ 到不了执行链。本判据从结果面钉住：
// 记忆批准的调用仍应走沙箱内执行（结果面等价 —— 命令照跑，只是不经豁免）。
// 注：本形态下命令会被沙箱**放行与否**取决于引擎判定，这里只钉
// "不因记忆批准而在执行链获得豁免"这一件事 —— 用硬拒样本观察执行失败。
func TestW048_MemoryApprovalDoesNotSanction(t *testing.T) {
	e := newProdExecutor(t, t.TempDir())

	// 用硬拒样本：即使带 Approved=true，执行链也不得豁免硬拒档。
	// （precheck 层在 Classify==TierHardDeny 时直接 Proceed:false ——
	// 已覆盖；这里钉的是**执行链**那一半：
	// 若有人把咽喉判据放宽成"凡 Approved 即注入"，硬拒样本在
	// ValidateSanctioned 仍会被拒 ⇒ 该测试主要防判据面继续放宽。）
	pr := e.Pipeline.Execute(context.Background(), Call{
		ID: "w048-hard", Name: "bash", Approved: true,
		Input: mustMarshal(map[string]interface{}{"command": n122HardCmd}),
	})
	if pr.Proceed {
		t.Fatalf("硬拒档不得因 Approved 标记放行（deny-wins）：%+v", pr)
	}
	if pr.Result != nil {
		t.Errorf("硬拒档不应有执行结果：%+v", pr.Result)
	}
}
