package hook

import (
	"strings"
	"sync"
	"testing"
)

// TestRemoveHookByID_Normal tests removing an existing hook by ID
func TestRemoveHookByID_Normal(t *testing.T) {
	m := NewManager(t.TempDir())

	// Add a hook
	h := &HookCommand{
		HookBase: HookBase{
			ID:      generateID(),
			Matcher: "*",
			Action:  ActionAllow,
			Type:    HookTypeCommand,
			Enabled: true,
		},
		Command: "echo hello",
	}
	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook failed: %v", err)
	}

	// Verify hook was added
	hooks, err := m.GetHooksByEvent("PreToolUse")
	if err != nil {
		t.Fatalf("GetHooksByEvent failed: %v", err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	hookID := hooks[0].Base().ID

	// Remove by ID
	if err := m.RemoveHookByID("PreToolUse", hookID); err != nil {
		t.Fatalf("RemoveHookByID failed: %v", err)
	}

	// Verify hook was removed
	hooks, _ = m.GetHooksByEvent("PreToolUse")
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks after removal, got %d", len(hooks))
	}
}

// TestRemoveHookByID_NotFound tests removing a non-existent hook ID returns error
func TestRemoveHookByID_NotFound(t *testing.T) {
	m := NewManager(t.TempDir())

	// Add a hook
	h := &HookCommand{
		HookBase: HookBase{
			ID:      generateID(),
			Matcher: "*",
			Action:  ActionAllow,
			Type:    HookTypeCommand,
			Enabled: true,
		},
		Command: "echo hello",
	}
	m.AddHook("PreToolUse", h)

	// Try to remove a non-existent ID
	err := m.RemoveHookByID("PreToolUse", "hook_nonexist")
	if err == nil {
		t.Error("expected error for non-existent hook ID, got nil")
	}
}

// TestRemoveHookByID_Concurrent tests concurrent removal of different hooks
func TestRemoveHookByID_Concurrent(t *testing.T) {
	m := NewManager(t.TempDir())

	// Add two hooks
	h1 := &HookCommand{
		HookBase: HookBase{
			ID:      "hook_aaaa1111",
			Matcher: "*",
			Action:  ActionAllow,
			Type:    HookTypeCommand,
			Enabled: true,
		},
		Command: "echo first",
	}
	h2 := &HookCommand{
		HookBase: HookBase{
			ID:      "hook_bbbb2222",
			Matcher: "*",
			Action:  ActionBlock,
			Type:    HookTypeCommand,
			Enabled: true,
		},
		Command: "echo second",
	}
	m.AddHook("PreToolUse", h1)
	m.AddHook("PreToolUse", h2)

	// Verify both hooks were added
	hooks, _ := m.GetHooksByEvent("PreToolUse")
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}

	// Remove both concurrently
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)

	go func() {
		defer wg.Done()
		err1 = m.RemoveHookByID("PreToolUse", "hook_aaaa1111")
	}()
	go func() {
		defer wg.Done()
		err2 = m.RemoveHookByID("PreToolUse", "hook_bbbb2222")
	}()
	wg.Wait()

	if err1 != nil {
		t.Errorf("goroutine 1 RemoveHookByID failed: %v", err1)
	}
	if err2 != nil {
		t.Errorf("goroutine 2 RemoveHookByID failed: %v", err2)
	}

	// Verify both hooks were removed
	hooks, _ = m.GetHooksByEvent("PreToolUse")
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks after concurrent removal, got %d", len(hooks))
	}
}

// TestHooksListShowsID tests that hooks listed include their IDs
func TestHooksListShowsID(t *testing.T) {
	m := NewManager(t.TempDir())

	// Add hooks with known IDs
	h1 := &HookCommand{
		HookBase: HookBase{
			ID:      "hook_test1234",
			Matcher: "write_file",
			Action:  ActionBlock,
			Type:    HookTypeCommand,
			Enabled: true,
			Reason:  "block writes",
		},
		Command: "echo blocked",
	}
	h2 := &HookCommand{
		HookBase: HookBase{
			ID:      "hook_test5678",
			Matcher: "*",
			Action:  ActionAllow,
			Type:    HookTypeCommand,
			Enabled: true,
		},
		Command: "echo allowed",
	}
	m.AddHook("PreToolUse", h1)
	m.AddHook("PreToolUse", h2)

	// Get hooks and verify IDs are present
	hooks, err := m.GetHooksByEvent("PreToolUse")
	if err != nil {
		t.Fatalf("GetHooksByEvent failed: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}

	foundIDs := map[string]bool{}
	for _, h := range hooks {
		if h.Base().ID == "" {
			t.Error("hook has empty ID")
		}
		foundIDs[h.Base().ID] = true
	}

	if !foundIDs["hook_test1234"] {
		t.Error("expected hook_test1234 ID not found in list")
	}
	if !foundIDs["hook_test5678"] {
		t.Error("expected hook_test5678 ID not found in list")
	}
}

// TestNewHook tests the NewHook constructor
func TestNewHook(t *testing.T) {
	h := NewHook("PreToolUse", "echo hello")
	if h.Base().ID == "" {
		t.Error("NewHook should auto-generate ID")
	}
	if !strings.HasPrefix(h.Base().ID, "hook_") {
		t.Errorf("expected ID to start with 'hook_', got %s", h.Base().ID)
	}
	if h.(*HookCommand).Command != "echo hello" {
		t.Errorf("expected command 'echo hello', got %s", h.(*HookCommand).Command)
	}
	if h.Base().Matcher != "*" {
		t.Errorf("expected default matcher '*', got %s", h.Base().Matcher)
	}
	if h.Base().Action != ActionAllow {
		t.Errorf("expected default action Allow, got %s", h.Base().Action)
	}
	if h.Base().Type != HookTypeCommand {
		t.Errorf("expected default type HookTypeCommand, got %s", h.Base().Type)
	}
	if !h.Base().Enabled {
		t.Error("expected hook to be enabled by default")
	}
}

// TestGenerateID tests the generateID function
func TestGenerateID(t *testing.T) {
	id := generateID()
	if !strings.HasPrefix(id, "hook_") {
		t.Errorf("expected ID to start with 'hook_', got %s", id)
	}
	// "hook_" (5 chars) + 8 hex chars = 13 chars total
	if len(id) != 13 {
		t.Errorf("expected ID length 13, got %d", len(id))
	}

	// Verify uniqueness
	id2 := generateID()
	if id == id2 {
		t.Error("two generated IDs should not be equal")
	}
}
