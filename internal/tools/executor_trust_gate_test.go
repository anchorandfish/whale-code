package tools

// PR-A 信任门的**生产接线断言**（production-path-tdd R1/R2）。
//
// 作用：用可执行的方式钉死「信任门在生产路径上真的被打开了」。
// hook 包里的信任门测试只能证明「门本身有效」；如果生产忘了调用 EnableTrustGate()，
// 那道门就是死代码 —— 这条测试正是为了防止那种情况。

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

func TestProduction_TrustGateEnabledAfterInitPipelineWithHooks(t *testing.T) {
	workDir := t.TempDir()

	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(&permission.Policy{}, workDir)

	if e.hookMgr == nil {
		t.Fatalf("hookMgr not initialized by InitPipelineWithHooks")
	}
	if !e.hookMgr.TrustGateEnabled() {
		t.Fatalf(
			"FAIL: trust gate NOT enabled after InitPipelineWithHooks — " +
				"the gate in hook.ExecuteHookWithPayload would be dead code in production. " +
				"EnableTrustGate() must be called in InitPipelineWithHooks (executor.go).",
		)
	}
}
