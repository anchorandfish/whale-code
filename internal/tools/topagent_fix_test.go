package tools

// RED-before test for S4-F6: a TOP-LEVEL Agent tool execution must route
// through AgentTaskMgr.Spawn so the breadth guards (maxSpawn=20 /
// maxConcurrent=5 / depth) cover top-level parallel delegation (per user
// decision: align with a comparable CLI max_threads).
//
// Current (unpatched web.go agentHandler): directly agentCtx.Run, bypassing
// the manager -> List() does not grow -> this test FAILS (RED).
// After S4-F6 overlay (agentHandler -> AgentTaskMgr.Spawn): List() grows.

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

func TestFix_S4_F6_TopLevelAgentRoutesThroughManager(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	if executor.AgentTaskMgr == nil {
		t.Fatal("executor.AgentTaskMgr must be wired")
	}
	before := len(executor.AgentTaskMgr.List())

	// mock: the child (top-level Agent) finishes in one text-only round.
	r1 := make(chan ai.Event, 3)
	r1 <- ai.TextDeltaEvent{Content: "child done"}
	r1 <- ai.MessageStopEvent{}
	close(r1)
	executor.AIClient = &subagentCaptureClient{responses: []<-chan ai.Event{r1}}

	res := executor.Execute(context.Background(), Call{
		ID:    "top-1",
		Name:  "Agent",
		Input: `{"prompt":"explore README first line","agent_type":"explore"}`,
	})
	if res.IsError {
		t.Fatalf("top-level Agent errored: %s", res.Output)
	}

	after := len(executor.AgentTaskMgr.List())
	if after <= before {
		t.Fatalf("RED-F6: top-level Agent did NOT route through AgentTaskMgr (before=%d after=%d)", before, after)
	}
}
