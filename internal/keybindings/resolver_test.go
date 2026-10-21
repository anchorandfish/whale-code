package keybindings

import (
	"testing"

	"github.com/charmbracelet/bubbletea"
)

func TestResolverParse(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	tests := []struct {
		name       string
		key        Key
		mode       string
		wantType   ActionType
		wantMotion string
	}{
		{"j cursor_down", Key{Type: tea.KeyRunes, Runes: []rune{'j'}}, "normal", ActionVimMotion, "cursor_down"},
		{"k cursor_up", Key{Type: tea.KeyRunes, Runes: []rune{'k'}}, "normal", ActionVimMotion, "cursor_up"},
		{"h cursor_left", Key{Type: tea.KeyRunes, Runes: []rune{'h'}}, "normal", ActionVimMotion, "cursor_left"},
		{"l cursor_right", Key{Type: tea.KeyRunes, Runes: []rune{'l'}}, "normal", ActionVimMotion, "cursor_right"},
		{"i mode_insert", Key{Type: tea.KeyRunes, Runes: []rune{'i'}}, "normal", ActionVimModeSwitch, "mode_insert"},
		{"esc mode_normal", Key{Type: tea.KeyEsc}, "insert", ActionVimModeSwitch, "mode_normal"},
		{"x delete_char", Key{Type: tea.KeyRunes, Runes: []rune{'x'}}, "normal", ActionVimDelete, "delete_char"},
		{"0 line_start", Key{Type: tea.KeyRunes, Runes: []rune{'0'}}, "normal", ActionVimMotion, "line_start"},
		{"$ line_end", Key{Type: tea.KeyRunes, Runes: []rune{'$'}}, "normal", ActionVimMotion, "line_end"},
		{"G goto_bottom", Key{Type: tea.KeyRunes, Runes: []rune{'G'}}, "normal", ActionVimMotion, "goto_bottom"},
		{"ctrl+c reserved", Key{Type: tea.KeyCtrlC}, "normal", ActionReserved, ""},
		{"ctrl+d reserved", Key{Type: tea.KeyCtrlD}, "normal", ActionReserved, ""},
		{"unknown key passthrough", Key{Type: tea.KeyRunes, Runes: []rune{'z'}}, "normal", ActionPassthrough, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolver.Parse(tt.key, tt.mode)
			if got.Type != tt.wantType {
				t.Errorf("Resolver.Parse() Type = %v, want %v", got.Type, tt.wantType)
			}
			if got.Motion != tt.wantMotion {
				t.Errorf("Resolver.Parse() Motion = %v, want %v", got.Motion, tt.wantMotion)
			}
		})
	}
}

func TestResolverSequence(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	t.Run("dd sequence", func(t *testing.T) {
		d1 := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		d2 := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}

		action1 := resolver.Parse(d1, "normal")
		if action1.Type != ActionSequencePending {
			t.Errorf("First d: Type = %v, want ActionSequencePending", action1.Type)
		}

		action2 := resolver.Parse(d2, "normal")
		if action2.Type != ActionVimDelete {
			t.Errorf("Second d: Type = %v, want ActionVimDelete", action2.Type)
		}
		if action2.Motion != "delete_line" {
			t.Errorf("Second d: Motion = %v, want delete_line", action2.Motion)
		}
	})

	t.Run("gg sequence", func(t *testing.T) {
		g1 := Key{Type: tea.KeyRunes, Runes: []rune{'g'}}
		g2 := Key{Type: tea.KeyRunes, Runes: []rune{'g'}}

		action1 := resolver.Parse(g1, "normal")
		if action1.Type != ActionSequencePending {
			t.Errorf("First g: Type = %v, want ActionSequencePending", action1.Type)
		}

		action2 := resolver.Parse(g2, "normal")
		if action2.Type != ActionVimMotion {
			t.Errorf("Second g: Type = %v, want ActionVimMotion", action2.Type)
		}
		if action2.Motion != "goto_top" {
			t.Errorf("Second g: Motion = %v, want goto_top", action2.Motion)
		}
	})

	t.Run("d followed by non-d", func(t *testing.T) {
		d := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		j := Key{Type: tea.KeyRunes, Runes: []rune{'j'}}

		resolver.Parse(d, "normal")
		action2 := resolver.Parse(j, "normal")

		if action2.Type != ActionVimMotion {
			t.Errorf("j after d: Type = %v, want ActionVimMotion", action2.Type)
		}
		if action2.Motion != "cursor_down" {
			t.Errorf("j after d: Motion = %v, want cursor_down", action2.Motion)
		}
	})
}

func TestResolverInsertMode(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	t.Run("j in insert mode passthrough", func(t *testing.T) {
		j := Key{Type: tea.KeyRunes, Runes: []rune{'j'}}
		action := resolver.Parse(j, "insert")
		if action.Type != ActionPassthrough {
			t.Errorf("j in insert: Type = %v, want ActionPassthrough", action.Type)
		}
	})

	t.Run("esc in insert mode", func(t *testing.T) {
		esc := Key{Type: tea.KeyEsc}
		action := resolver.Parse(esc, "insert")
		if action.Type != ActionVimModeSwitch {
			t.Errorf("esc in insert: Type = %v, want ActionVimModeSwitch", action.Type)
		}
		if action.Motion != "mode_normal" {
			t.Errorf("esc in insert: Motion = %v, want mode_normal", action.Motion)
		}
	})
}
