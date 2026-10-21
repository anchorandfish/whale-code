package command

import (
	"sort"
	"testing"
)

// newTestRegistry creates a minimal registry with a few commands for testing
func newTestRegistry() *Registry {
	r := &Registry{
		commands: make(map[string]Command),
	}
	r.commands["plugin"] = Command{Name: "plugin", Description: "Manage plugins", Category: "Plugin"}
	r.commands["model"] = Command{Name: "model", Description: "Switch model", Category: "Model"}
	r.commands["think"] = Command{Name: "think", Description: "Toggle thinking mode", Category: "Config"}
	r.commands["mcp"] = Command{Name: "mcp", Description: "Manage MCP servers", Category: "Plugin"}
	r.commands["help"] = Command{Name: "help", Description: "Show help", Category: "Core"}
	r.commands["clear"] = Command{Name: "clear", Description: "Clear screen", Category: "Core"}
	r.commands["pr"] = Command{Name: "pr", Description: "Pull request operations", Category: "GitHub"}
	r.commands["privacy"] = Command{Name: "privacy", Description: "Privacy settings", Category: "Config"}
	r.commands["export-format"] = Command{Name: "export-format", Description: "Set export format", Category: "Export"}
	r.commands["output-style"] = Command{Name: "output-style", Description: "Set output style", Category: "Config"}
	r.commands["compact-strategy"] = Command{Name: "compact-strategy", Description: "Set compact strategy", Category: "Config"}
	return r
}

// TestCompletionFirstLevel tests first-level command name completion
func TestCompletionFirstLevel(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/pl")
	if len(matches) == 0 {
		t.Fatal("expected at least one match for '/pl', got none")
	}
	found := false
	for _, m := range matches {
		if m == "plugin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'plugin' in matches, got %v", matches)
	}
}

// TestCompletionSubCommand tests sub-command completion
func TestCompletionSubCommand(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/plugin in")
	if len(matches) == 0 {
		t.Fatal("expected at least one match for '/plugin in', got none")
	}
	if len(matches) != 1 || matches[0] != "install" {
		t.Errorf("expected ['install'], got %v", matches)
	}
}

// TestCompletionSubCommandMultiple tests sub-command completion returning multiple matches
func TestCompletionSubCommandMultiple(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/mcp r")
	if len(matches) == 0 {
		t.Fatal("expected at least one match for '/mcp r', got none")
	}
	sort.Strings(matches)
	expected := []string{"remove", "restart"}
	sort.Strings(expected)
	if len(matches) != len(expected) {
		t.Errorf("expected %v, got %v", expected, matches)
	}
	for i, n := range expected {
		if matches[i] != n {
			t.Errorf("expected %v, got %v", expected, matches)
			break
		}
	}
}

// TestCompletionArgument tests argument completion for commands with ArgCompleter
func TestCompletionArgument(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/model cl")
	if len(matches) == 0 {
		t.Fatal("expected at least one match for '/model cl', got none")
	}
	found := false
	for _, m := range matches {
		if m == "claude-3.5-sonnet" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'claude-3.5-sonnet' in matches, got %v", matches)
	}
}

// TestCompletionArgumentMultiple tests argument completion for /model returning all models
func TestCompletionArgumentMultiple(t *testing.T) {
	r := newTestRegistry()

	// model ArgCompleter uses listAvailableModels() which returns all models
	matches := r.Completion("/model gpt")
	if len(matches) == 0 {
		t.Fatalf("expected matches for '/model gpt', got %d", len(matches))
	}
	// listAvailableModels returns all models regardless of partial input
	// Verify at least the expected models are present
	foundGPT4o := false
	foundGPT4Turbo := false
	for _, n := range matches {
		if n == "gpt-4o" {
			foundGPT4o = true
		}
		if n == "gpt-4-turbo" {
			foundGPT4Turbo = true
		}
	}
	if !foundGPT4o || !foundGPT4Turbo {
		t.Errorf("expected gpt-4o and gpt-4-turbo in matches, got %v", matches)
	}
}

// TestCompletionNoMatch tests that no matches are returned for unknown commands
func TestCompletionNoMatch(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/xyz")
	if len(matches) != 0 {
		t.Errorf("expected no matches for '/xyz', got %v", matches)
	}
}

// TestCompletionEmptyInput tests that all commands are returned for "/" input
func TestCompletionEmptyInput(t *testing.T) {
	r := newTestRegistry()

	// "/" should return all command names
	matches := r.Completion("/")
	if len(matches) == 0 {
		t.Errorf("expected all commands for '/', got %v", matches)
	}

	// Empty string should also return all commands
	matches = r.Completion("")
	if len(matches) == 0 {
		t.Errorf("expected all commands for empty string, got %v", matches)
	}
}

// TestCompletionMultipleCommands tests that multiple commands are returned for a prefix
func TestCompletionMultipleCommands(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/p")
	if len(matches) < 2 {
		t.Fatalf("expected at least 2 matches for '/p', got %d", len(matches))
	}
	foundPlugin := false
	foundPr := false
	foundPrivacy := false
	for _, n := range matches {
		if n == "plugin" {
			foundPlugin = true
		}
		if n == "pr" {
			foundPr = true
		}
		if n == "privacy" {
			foundPrivacy = true
		}
	}
	if !foundPlugin || !foundPr || !foundPrivacy {
		t.Errorf("expected plugin, pr, privacy in matches, got %v", matches)
	}
}

// TestCompletionNoSubCommands tests commands without sub-command definitions
func TestCompletionNoSubCommands(t *testing.T) {
	r := newTestRegistry()

	// "help" has no sub-commands defined in commandCompletions
	matches := r.Completion("/help something")
	if len(matches) != 0 {
		t.Errorf("expected no matches for '/help something', got %v", matches)
	}
}

// TestCompletionThinkArgument tests argument completion for /think command
func TestCompletionThinkArgument(t *testing.T) {
	r := newTestRegistry()

	matches := r.Completion("/think o")
	if len(matches) == 0 {
		t.Fatal("expected at least one match for '/think o', got none")
	}
	sort.Strings(matches)
	if len(matches) != 2 || matches[0] != "off" || matches[1] != "on" {
		t.Errorf("expected ['off', 'on'], got %v", matches)
	}
}

// TestFilterPrefix tests the filterPrefix helper function
func TestFilterPrefix(t *testing.T) {
	items := []string{"apple", "apricot", "banana", "application", "cherry"}

	tests := []struct {
		prefix   string
		expected []string
	}{
		{"ap", []string{"apple", "apricot", "application"}},
		{"app", []string{"apple", "application"}},
		{"appl", []string{"apple", "application"}},
		{"b", []string{"banana"}},
		{"z", nil},
		{"", []string{"apple", "apricot", "banana", "application", "cherry"}},
	}

	for _, tt := range tests {
		result := filterPrefix(items, tt.prefix)
		if len(result) != len(tt.expected) {
			t.Errorf("filterPrefix(%v, %q) = %v, expected %v", items, tt.prefix, result, tt.expected)
			continue
		}
		for i, v := range result {
			if v != tt.expected[i] {
				t.Errorf("filterPrefix(%v, %q) = %v, expected %v", items, tt.prefix, result, tt.expected)
				break
			}
		}
	}
}

// TestListKnownMCPServers tests the listKnownMCPServers helper function
func TestListKnownMCPServers(t *testing.T) {
	servers := listKnownMCPServers()
	if len(servers) == 0 {
		t.Error("expected non-empty MCP server list")
	}
	found := false
	for _, s := range servers {
		if s == "filesystem" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'filesystem' in MCP servers, got %v", servers)
	}
}

// TestListAvailableModels tests the listAvailableModels helper function
func TestListAvailableModels(t *testing.T) {
	models := listAvailableModels()
	if len(models) == 0 {
		t.Error("expected non-empty model list")
	}
	found := false
	for _, m := range models {
		if m == "claude-3.5-sonnet" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'claude-3.5-sonnet' in models, got %v", models)
	}
}

// ---- CMD-P1-002: 动态回调测试 ----

// TestCompletion_OnListModelsCallback 测试 OnListModels 回调优先于硬编码列表
func TestCompletion_OnListModelsCallback(t *testing.T) {
	r := newTestRegistry()
	r.OnListModels = func() []string {
		return []string{"custom-model-1", "custom-model-2"}
	}

	matches := r.Completion("/model x")
	if len(matches) != 2 {
		t.Fatalf("expected 2 custom models, got %d: %v", len(matches), matches)
	}
	found := false
	for _, m := range matches {
		if m == "custom-model-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'custom-model-1' in matches, got %v", matches)
	}
}

// TestCompletion_OnListModelsNilFallback 测试 OnListModels 为 nil 时使用硬编码 fallback
func TestCompletion_OnListModelsNilFallback(t *testing.T) {
	r := newTestRegistry()
	// OnListModels 未设置，应使用硬编码 fallback

	matches := r.Completion("/model x")
	if len(matches) == 0 {
		t.Fatal("expected non-empty model list from fallback")
	}
	found := false
	for _, m := range matches {
		if m == "claude-3.5-sonnet" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'claude-3.5-sonnet' in fallback matches, got %v", matches)
	}
}

// TestCompletion_OnListMCPServersCallback 测试 OnListMCPServers 回调优先于硬编码列表
func TestCompletion_OnListMCPServersCallback(t *testing.T) {
	r := newTestRegistry()
	r.OnListMCPServers = func() []string {
		return []string{"custom-mcp-server"}
	}

	// mcp 有 SubCommands，需要 3 段输入才能触发 ArgCompleter
	matches := r.Completion("/mcp add x")
	if len(matches) != 1 {
		t.Fatalf("expected 1 custom MCP server, got %d: %v", len(matches), matches)
	}
	if matches[0] != "custom-mcp-server" {
		t.Errorf("expected 'custom-mcp-server', got %v", matches)
	}
}

// TestCompletion_OnListMCPServersNilFallback 测试 OnListMCPServers 为 nil 时使用硬编码 fallback
func TestCompletion_OnListMCPServersNilFallback(t *testing.T) {
	r := newTestRegistry()
	// OnListMCPServers 未设置，应使用硬编码 fallback

	// mcp 有 SubCommands，需要 3 段输入才能触发 ArgCompleter
	matches := r.Completion("/mcp add x")
	if len(matches) == 0 {
		t.Fatal("expected non-empty MCP server list from fallback")
	}
	found := false
	for _, m := range matches {
		if m == "filesystem" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'filesystem' in fallback matches, got %v", matches)
	}
}
