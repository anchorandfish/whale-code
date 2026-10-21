package agentguard

// S16 审计 · Snapshot  characterization（agentguard 包内）
//
// 目的：钉死「预算/熔断」当前行为，便于后续 Fix 阶段用 A/B 证明行为真的变了。
// 这些测试只新增、不改源码；生产源码零改动。绿了才算钉住。
//
// 覆盖的审查命题：
//   - I4 状态机完备：到限/熔断停机是「粘性」的，未经 BeginRequest 重置不会静默恢复。
//   - I11 有界资源：轮次上限 + 连续失败熔断是真实硬上限。
//
// 后续若改「停机可被某路径绕过重发模型调用」或「熔断后自动恢复」，对应测试将由绿转红，
// 即可证伪地证明行为变了（这正是 A/B 闭环要的「可判别」基线）。

import (
	"testing"
)

// 钉死：轮次预算耗尽后停机是粘性的，唯一恢复路径是 BeginRequest。
// 对应 G5/I4：一旦交回控制权，主循环不得静默续跑；恢复必须显式重置。
func TestChar_TurnBudgetExhaustionIsStickyUntilBeginRequest(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTurns = 2
	g := NewGuard(cfg)
	g.BeginRequest()

	for i := 1; i <= cfg.MaxTurns; i++ {
		if d := g.BeforeModelCall(); d.Verdict != VerdictProceed {
			t.Fatalf("轮 %d 应 Proceed, got %v", i, d.Verdict)
		}
		g.AfterToolResults(false)
		g.AfterTurn(TurnObservation{FilesChanged: []string{"/repo/a.go"}})
	}

	stop := g.BeforeModelCall()
	if stop.Verdict != VerdictStop {
		t.Fatalf("到限应 Stop, got %v", stop.Verdict)
	}

	// 粘性：即便不再 AfterTurn、也不重置，后续 BeforeModelCall 仍 Stop（无静默恢复）。
	for i := 0; i < 3; i++ {
		if d := g.BeforeModelCall(); d.Verdict != VerdictStop {
			t.Fatalf("停机后应持续 Stop（粘性），但第 %d 次恢复为 %v", i+1, d.Verdict)
		}
	}

	// 唯一恢复路径：BeginRequest 重置后重新 Proceed。
	g.BeginRequest()
	if d := g.BeforeModelCall(); d.Verdict != VerdictProceed {
		t.Fatalf("BeginRequest 后应重新 Proceed, got %v", d.Verdict)
	}
}

// 钉死：连续整轮工具全失败熔断后，停机粘性（P0-2 与 I11 硬上限）。
// 对标同类 CLI guardian MAX_CONSECUTIVE_GUARDIAN_DENIALS_PER_TURN=3。
func TestChar_BreakerTripIsSticky(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	for i := 1; i <= 3; i++ {
		d := g.AfterToolResults(true)
		if i < 3 && d.Verdict != VerdictProceed {
			t.Fatalf("第 %d 轮全失败不应立即停, got %v", i, d.Verdict)
		}
	}
	if d := g.AfterToolResults(true); d.Verdict != VerdictStop {
		t.Fatalf("连续第 3 轮全失败应熔断停机, got %v", d.Verdict)
	}

	// 熔断后即使有一轮成功结果，仍持续 Stop（已停机，不自动恢复）。
	g.AfterToolResults(false)
	for i := 0; i < 3; i++ {
		if d := g.BeforeModelCall(); d.Verdict != VerdictStop {
			t.Fatalf("熔断后应持续 Stop, 但第 %d 次为 %v", i+1, d.Verdict)
		}
	}
}

// 注：子 agent（Fork）迭代预算属 I11 相邻面，由另一套机制承担——
//   tools/agent.go 的 AgentContext.MaxRounds（默认 5，web.go:619 注入 ForkMaxTurns=200），
//   强制点为 AgentContext.Run 的 `for round < a.MaxRounds`（agent.go:432/618）。
// 该路径在 tools 包，与 agentguard 无导入环，其「硬上限」由代码读取核实，
// 不在此包做单元钉死（避免 agentguard→tools 重型依赖）；Fix/手测阶段如需可移步 tools 包补 characterization。
