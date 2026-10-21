package command

import (
	"testing"
	"unicode"
)

func TestBuiltinCommandsHaveBilingualSuggestionMetadata(t *testing.T) {
	registry := NewRegistry()
	commands := registry.All()
	if len(commands) == 0 {
		t.Fatal("command registry is empty")
	}

	for _, cmd := range commands {
		if cmd.DescriptionEN == "" {
			t.Errorf("command %q has no English description", cmd.Name)
		}
		if cmd.Description == "" {
			t.Errorf("command %q has no Chinese description", cmd.Name)
		}
		if containsHan(cmd.DescriptionEN) {
			t.Errorf("command %q English description contains Han characters: %q", cmd.Name, cmd.DescriptionEN)
		}
	}

	for name := range englishCommandDescriptions {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("English description exists for unknown command %q", name)
		}
	}
}

func TestCommandSuggestionPrioritiesAreUnique(t *testing.T) {
	registry := NewRegistry()
	seen := make(map[int]string)
	for _, cmd := range registry.All() {
		if cmd.SuggestionPriority == 0 {
			continue
		}
		if previous, ok := seen[cmd.SuggestionPriority]; ok {
			t.Errorf("commands %q and %q share suggestion priority %d", previous, cmd.Name, cmd.SuggestionPriority)
		}
		seen[cmd.SuggestionPriority] = cmd.Name
	}
	if len(seen) != len(commandSuggestionPriorities) {
		t.Fatalf("registered priorities = %d, metadata priorities = %d", len(seen), len(commandSuggestionPriorities))
	}
}

func containsHan(value string) bool {
	for _, char := range value {
		if unicode.Is(unicode.Han, char) {
			return true
		}
	}
	return false
}
