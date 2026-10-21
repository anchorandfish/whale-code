package keybindings

import (
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"empty config", Config{}, false},
		{"valid vim mode", Config{Mode: "vim"}, false},
		{"valid emacs mode", Config{Mode: "emacs"}, false},
		{"valid custom mode", Config{Mode: "custom"}, false},
		{"invalid mode", Config{Mode: "invalid"}, true},
		{"valid binding", Config{Bindings: []Binding{{Key: "j", Action: "cursor_down", Mode: "normal"}}}, false},
		{"empty key", Config{Bindings: []Binding{{Key: "", Action: "cursor_down", Mode: "normal"}}}, true},
		{"invalid key", Config{Bindings: []Binding{{Key: "ctrl+invalid", Action: "cursor_down", Mode: "normal"}}}, true},
		{"invalid action", Config{Bindings: []Binding{{Key: "j", Action: "invalid_action", Mode: "normal"}}}, true},
		{"invalid mode in binding", Config{Bindings: []Binding{{Key: "j", Action: "cursor_down", Mode: "invalid"}}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateConfig(tt.cfg)
			hasErr := len(errs) > 0
			if hasErr != tt.wantErr {
				t.Errorf("ValidateConfig() has error = %v, wantErr %v, errs = %v", hasErr, tt.wantErr, errs)
			}
		})
	}
}

func TestDetectConflicts(t *testing.T) {
	tests := []struct {
		name      string
		bindings  []Binding
		wantCount int
	}{
		{"no conflicts", []Binding{
			{Key: "j", Action: "cursor_down", Mode: "normal"},
			{Key: "k", Action: "cursor_up", Mode: "normal"},
		}, 0},
		{"same key different action", []Binding{
			{Key: "j", Action: "cursor_down", Mode: "normal"},
			{Key: "j", Action: "cursor_up", Mode: "normal"},
		}, 1},
		{"same key same action no conflict", []Binding{
			{Key: "j", Action: "cursor_down", Mode: "normal"},
			{Key: "j", Action: "cursor_down", Mode: "normal"},
		}, 0},
		{"different modes no conflict", []Binding{
			{Key: "j", Action: "cursor_down", Mode: "normal"},
			{Key: "j", Action: "cursor_up", Mode: "insert"},
		}, 0},
		{"empty mode conflicts with normal", []Binding{
			{Key: "j", Action: "cursor_down", Mode: ""},
			{Key: "j", Action: "cursor_up", Mode: "normal"},
		}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflicts := DetectConflicts(tt.bindings)
			if len(conflicts) != tt.wantCount {
				t.Errorf("DetectConflicts() = %v, want count %d", conflicts, tt.wantCount)
			}
		})
	}
}

func TestCheckReserved(t *testing.T) {
	tests := []struct {
		name      string
		bindings  []Binding
		wantCount int
	}{
		{"no reserved", []Binding{
			{Key: "j", Action: "cursor_down", Mode: "normal"},
		}, 0},
		{"override ctrl+c", []Binding{
			{Key: "ctrl+c", Action: "cursor_down", Mode: "normal"},
		}, 1},
		{"override ctrl+d", []Binding{
			{Key: "ctrl+d", Action: "cursor_down", Mode: "normal"},
		}, 1},
		{"override both", []Binding{
			{Key: "ctrl+c", Action: "cursor_down", Mode: "normal"},
			{Key: "ctrl+d", Action: "cursor_down", Mode: "normal"},
		}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overridden := CheckReserved(tt.bindings)
			if len(overridden) != tt.wantCount {
				t.Errorf("CheckReserved() = %v, want count %d", overridden, tt.wantCount)
			}
		})
	}
}

func TestReservedKeys(t *testing.T) {
	keys := ReservedKeys()
	if len(keys) != 2 {
		t.Errorf("ReservedKeys() = %v, want 2 keys", keys)
	}
}
