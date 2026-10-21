package tools

// F1-only probe (compiles on unpatched source) to prove RED-before for the
// nested-Agent-through-manager change.

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

func TestProbe_F1_NestedRoutesThroughManager_Before(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	if executor.AgentTaskMgr == nil {
		t.Fatal("executor.AgentTaskMgr must be wired")
	}
	before := len(executor.AgentTaskMgr.List())

	r1 := make(chan ai.Event, 6)
	r1 <- ai.ToolUseEvent{ID: "a1", Name: "Agent", Input: `{"prompt":"child"}`}
	r1 <- ai.MessageStopEvent{}
	close(r1)
	r2 := make(chan ai.Event, 3)
	r2 <- ai.TextDeltaEvent{Content: "child done"}
	r2 <- ai.MessageStopEvent{}
	close(r2)
	r3 := make(chan ai.Event, 3)
	r3 <- ai.TextDeltaEvent{Content: "parent done"}
	r3 <- ai.MessageStopEvent{}
	close(r3)

	executor.AIClient = &subagentCaptureClient{responses: []<-chan ai.Event{r1, r2, r3}}
	p := NewAgentContext(executor, "parent", "", executor.WorkDir(), 1)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after := len(executor.AgentTaskMgr.List())
	if after <= before {
		t.Fatalf("RED-MARKER-F1: nested Agent did not route through AgentTaskManager (before=%d after=%d)", before, after)
	}
}
