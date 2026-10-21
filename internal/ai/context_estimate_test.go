package ai

import "testing"

func TestEstimateRequestTokensIncludesSystemToolsMessages(t *testing.T) {
	estimator := NewTokenEstimator()
	req := Request{
		SystemPrompt: []string{"system instructions"},
		Messages:     []MessageEntry{{Role: "user", Content: "hello"}},
		Tools: []ToolDefinition{{
			Name: "Read", Description: "read a file", InputSchema: map[string]interface{}{"type": "object"},
		}},
	}
	got := EstimateRequestTokens(estimator, req)
	if got.SystemPrompt <= 0 || got.Tools <= 0 || got.Messages <= 0 {
		t.Fatalf("expected all request components, got %+v", got)
	}
	if got.Total != got.SystemPrompt+got.Tools+got.Messages {
		t.Fatalf("Total=%d, components=%d", got.Total, got.SystemPrompt+got.Tools+got.Messages)
	}
}
