package keybindings

import (
	"testing"

	"github.com/charmbracelet/bubbletea"
)

func TestParseString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"single char j", "j", "j", false},
		{"single char J", "J", "j", false},
		{"ctrl+d", "ctrl+d", "ctrl+d", false},
		{"ctrl+a", "ctrl+a", "ctrl+a", false},
		{"alt+x", "alt+x", "alt+x", false},
		{"esc", "esc", "esc", false},
		{"enter", "enter", "enter", false},
		{"space", "space", "space", false},
		{"tab", "tab", "tab", false},
		{"backspace", "backspace", "backspace", false},
		{"delete", "delete", "delete", false},
		{"up", "up", "up", false},
		{"down", "down", "down", false},
		{"left", "left", "left", false},
		{"right", "right", "right", false},
		{"home", "home", "home", false},
		{"end", "end", "end", false},
		{"pgup", "pgup", "pgup", false},
		{"pgdown", "pgdown", "pgdown", false},
		{"empty", "", "", true},
		{"invalid part", "ctrl+invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseString(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.String() != tt.want {
				t.Errorf("ParseString() = %v, want %v", got.String(), tt.want)
			}
		})
	}
}

func TestKeyString(t *testing.T) {
	tests := []struct {
		name string
		key  Key
		want string
	}{
		{"rune j", Key{Type: tea.KeyRunes, Runes: []rune{'j'}}, "j"},
		{"rune J", Key{Type: tea.KeyRunes, Runes: []rune{'J'}}, "J"},
		{"ctrl+d", Key{Type: tea.KeyCtrlD}, "ctrl+d"},
		{"ctrl+c", Key{Type: tea.KeyCtrlC}, "ctrl+c"},
		{"alt+j", Key{Type: tea.KeyRunes, Runes: []rune{'j'}, Alt: true}, "alt+j"},
		{"esc", Key{Type: tea.KeyEsc}, "esc"},
		{"enter", Key{Type: tea.KeyEnter}, "enter"},
		{"up", Key{Type: tea.KeyUp}, "up"},
		{"down", Key{Type: tea.KeyDown}, "down"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.String(); got != tt.want {
				t.Errorf("Key.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeyEqual(t *testing.T) {
	tests := []struct {
		name string
		a    Key
		b    Key
		want bool
	}{
		{"same j", Key{Type: tea.KeyRunes, Runes: []rune{'j'}}, Key{Type: tea.KeyRunes, Runes: []rune{'j'}}, true},
		{"different char", Key{Type: tea.KeyRunes, Runes: []rune{'j'}}, Key{Type: tea.KeyRunes, Runes: []rune{'k'}}, false},
		{"same ctrl+d", Key{Type: tea.KeyCtrlD}, Key{Type: tea.KeyCtrlD}, true},
		{"different ctrl", Key{Type: tea.KeyCtrlD}, Key{Type: tea.KeyCtrlC}, false},
		{"alt vs no alt", Key{Type: tea.KeyRunes, Runes: []rune{'j'}, Alt: true}, Key{Type: tea.KeyRunes, Runes: []rune{'j'}, Alt: false}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.want {
				t.Errorf("Key.Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}
