package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

const askUserStringifiedOptionsInput = `{
	"question": "想写什么主题的长作文？",
	"options": "\n[{\"label\": \"技术类\", \"description\": \"编程心得\", \"value\": \"tech\"}, {\"label\": \"叙事类\", \"description\": \"人物故事\", \"value\": \"narrative\"}], \"default_idx\":\n-1\n",
	"category": "dialogue-clarify",
	"allow_free_text": true
}`

func TestNormalizeAskUserInputStringifiedOptions(t *testing.T) {
	normalized, ok := normalizeAskUserInput(askUserStringifiedOptionsInput)
	if !ok {
		t.Fatal("expected stringified options to be normalized")
	}

	var input AskUserInput
	if err := json.Unmarshal([]byte(normalized), &input); err != nil {
		t.Fatalf("normalized input is invalid: %v", err)
	}
	if len(input.Options) != 2 {
		t.Fatalf("got %d options, want 2", len(input.Options))
	}
	if input.Options[0].Value != "tech" {
		t.Fatalf("first option value = %q, want tech", input.Options[0].Value)
	}
	if input.DefaultIdx != -1 {
		t.Fatalf("default_idx = %d, want -1", input.DefaultIdx)
	}
}

func TestAskUserPipelineRepairsStringifiedOptions(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	executor.InitPipeline(
		hook.NewManager(t.TempDir()),
		permission.NewPolicy(permission.LevelDangerFullAccess),
		t.TempDir(),
	)

	result := executor.ExecuteWithConfirm(context.Background(), Call{
		ID:    "ask-1",
		Name:  "AskUser",
		Input: askUserStringifiedOptionsInput,
	})
	if !result.Proceed {
		t.Fatalf("pipeline rejected repairable AskUser input: %s", result.Rejection)
	}
	if result.Result == nil || result.Result.IsError {
		t.Fatalf("AskUser execution failed: %+v", result.Result)
	}

	var askResult AskUserResult
	if err := json.Unmarshal([]byte(result.Result.Output), &askResult); err != nil {
		t.Fatalf("AskUser result is invalid: %v", err)
	}
	if len(askResult.Options) != 2 {
		t.Fatalf("got %d options, want 2", len(askResult.Options))
	}
	if askResult.DefaultIdx != -1 {
		t.Fatalf("default_idx = %d, want -1", askResult.DefaultIdx)
	}
}

func TestAskUserPipelineRejectsPlainStringOptions(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, t.TempDir())
	executor.InitPipeline(
		hook.NewManager(t.TempDir()),
		permission.NewPolicy(permission.LevelDangerFullAccess),
		t.TempDir(),
	)

	result := executor.ExecuteWithPipeline(context.Background(), Call{
		ID:    "ask-2",
		Name:  "AskUser",
		Input: `{"question":"请选择","options":"技术类,叙事类","category":"dialogue-clarify"}`,
	})
	if result.Proceed {
		t.Fatal("pipeline accepted non-JSON string options")
	}
	if !strings.Contains(result.Rejection, `expected array, got string`) {
		t.Fatalf("unexpected rejection: %s", result.Rejection)
	}
}
