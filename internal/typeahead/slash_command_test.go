package typeahead

import (
	"reflect"
	"testing"
)

func TestExtractTokenWithTriggerSlash(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantToken string
		wantStart int
		want      Trigger
	}{
		{name: "empty command", input: "/", wantToken: "", wantStart: 0, want: TriggerSlash},
		{name: "prefix", input: "/per", wantToken: "per", wantStart: 0, want: TriggerSlash},
		// ★ （2026-09-20 批 C）：前导空白 = **正式逃逸语义**（对齐 同类 CLI 的 `input_starts_with_space`
		// ⇒ Valid/普通文本），三入口统一 ⇒ 补全侧**不再**对前导空白的 `/x` 弹命令补全。
		// 原值（编码的是 ":398 TrimLeft 后判" 的旧行为，与 TUI / --plain 打架，见 §2.3）：
		// {input: " /PER", wantToken: "PER", wantStart: 2, want: TriggerSlash}
		// {input: "\u00a0/per", wantToken: "per", wantStart: len("\u00a0"), want: TriggerSlash}
		{name: "leading whitespace is escape", input: "  /PER", wantStart: -1, want: TriggerNone},
		{name: "unicode leading whitespace is escape", input: "\u00a0/per", wantStart: -1, want: TriggerNone},
		{name: "multiword command", input: "/session sw", wantToken: "session sw", wantStart: 0, want: TriggerSlash},
		{name: "prose", input: "hello /per", wantStart: -1, want: TriggerNone},
		{name: "url", input: "https://example.com", wantStart: -1, want: TriggerNone},
		{name: "second line", input: "hello\n/per", wantStart: -1, want: TriggerNone},
		{name: "file argument wins", input: "/review @mai", wantToken: "mai", wantStart: 8, want: TriggerAt},
		{name: "skill argument wins", input: "/review #sec", wantToken: "sec", wantStart: 8, want: TriggerHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, start, trigger := ExtractTokenWithTrigger(tt.input, len(tt.input))
			if token != tt.wantToken || start != tt.wantStart || trigger != tt.want {
				t.Fatalf("ExtractTokenWithTrigger(%q) = (%q, %d, %v), want (%q, %d, %v)",
					tt.input, token, start, trigger, tt.wantToken, tt.wantStart, tt.want)
			}
		})
	}
}

func TestSuggestCommandsPriorityThenAlphabetical(t *testing.T) {
	commands := []CommandItem{
		{Name: "zebra", Description: "Z", SecondaryDescription: "中"},
		{Name: "permissions", Description: "Permissions", SecondaryDescription: "权限", Priority: 2},
		{Name: "alpha", Description: "A", SecondaryDescription: "甲"},
		{Name: "model", Description: "Model", SecondaryDescription: "模型", Priority: 1},
	}

	want := []string{"model", "permissions", "alpha", "zebra"}
	for iteration := 0; iteration < 3; iteration++ {
		got := SuggestCommands("", commands)
		gotNames := make([]string, 0, len(got))
		for _, item := range got {
			gotNames = append(gotNames, item.DisplayText)
		}
		if !reflect.DeepEqual(gotNames, want) {
			t.Fatalf("iteration %d names = %v, want %v", iteration, gotNames, want)
		}
	}
}

func TestSuggestCommandsKeepsAllCandidatesAndFilters(t *testing.T) {
	commands := make([]CommandItem, 0, 22)
	for index := 0; index < 20; index++ {
		commands = append(commands, CommandItem{Name: "command-" + string(rune('a'+index))})
	}
	commands = append(commands,
		CommandItem{Name: "session list", Description: "List sessions"},
		CommandItem{Name: "session switch", Description: "Switch sessions"},
		CommandItem{Name: "SESSION LIST", Description: "duplicate"},
	)

	if got := SuggestCommands("", commands); len(got) != 22 {
		t.Fatalf("empty slash returned %d unique commands, want 22", len(got))
	}
	filtered := SuggestCommands("SeSsIoN S", commands)
	if got := completionNames(filtered); !reflect.DeepEqual(got, []string{"session switch"}) {
		t.Fatalf("filtered names = %v", got)
	}
	if got := SuggestCommands("session list ", commands); len(got) != 0 {
		t.Fatalf("exact command followed by space should close suggestions, got %v", completionNames(got))
	}
	if got := SuggestCommands("session list\t", commands); len(got) != 0 {
		t.Fatalf("exact command followed by tab should close suggestions, got %v", completionNames(got))
	}
	if got := completionNames(SuggestCommands("session ", []CommandItem{{Name: "session list"}, {Name: "session switch"}})); !reflect.DeepEqual(got, []string{"session list", "session switch"}) {
		t.Fatalf("multiword command prefix names = %v", got)
	}
}

func TestApplySuggestionSlashCommand(t *testing.T) {
	input := "  /PER"
	newInput, cursor := ApplySuggestion(input, Completion{
		DisplayText: "permissions",
		Trigger:     TriggerSlash,
	}, 2, 3)
	if newInput != "  /permissions " {
		t.Fatalf("new input = %q", newInput)
	}
	if cursor != len(newInput) {
		t.Fatalf("cursor = %d, want %d", cursor, len(newInput))
	}

	withArgument, _ := ApplySuggestion("/per foo", Completion{
		DisplayText: "permissions",
		Trigger:     TriggerSlash,
	}, 0, 3)
	if withArgument != "/permissions foo" {
		t.Fatalf("slash completion should keep one argument separator, got %q", withArgument)
	}

	withLineBreak, _ := ApplySuggestion("/sess\nnotes", Completion{
		DisplayText: "session switch",
		Trigger:     TriggerSlash,
	}, 0, len("sess"))
	if withLineBreak != "/session switch\nnotes" {
		t.Fatalf("slash completion should preserve line breaks, got %q", withLineBreak)
	}

	commonPrefix, _ := ApplySuggestion("/session ", Completion{
		DisplayText: "session ",
		Trigger:     TriggerSlash,
	}, 0, len("session "))
	if commonPrefix != "/session " {
		t.Fatalf("slash common-prefix completion should keep one separator, got %q", commonPrefix)
	}
}

func completionNames(completions []Completion) []string {
	names := make([]string, 0, len(completions))
	for _, completion := range completions {
		names = append(names, completion.DisplayText)
	}
	return names
}
