package main

import (
	"reflect"
	"runtime"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// startupOptionsValue intentionally uses reflection because Bubble Tea keeps its
// startup bitset private. This lets the test assert that the default enables
// mouse capture without coupling production code to Bubble Tea internals.
func startupOptionsValue(opts ...tea.ProgramOption) int64 {
	p := tea.NewProgram(nil, opts...)
	return reflect.ValueOf(p).Elem().FieldByName("startupOptions").Int()
}

func TestTUIProgramOptionsDefaultToMouseCapture(t *testing.T) {
	defaultValue := startupOptionsValue(newTUIProgramOptions(true, resolveAltScreen(altScreenAuto))...)
	baseOpts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithFPS(120), tea.WithoutSignalHandler()}
	if runtime.GOOS != "windows" {
		baseOpts = append(baseOpts, tea.WithInputTTY())
	}
	if got := startupOptionsValue(baseOpts...); got == defaultValue {
		t.Fatalf("base TUI options unexpectedly enable mouse capture: got startup options %d", got)
	}

	noMouseValue := startupOptionsValue(newTUIProgramOptions(false, resolveAltScreen(altScreenAuto))...)
	if noMouseValue == defaultValue {
		t.Fatal("--no-mouse options must disable Bubble Tea mouse mode")
	}
}

func TestDefaultMouseModeEnablesWheelScrolling(t *testing.T) {
	capture, autoDuringActive := defaultMouseMode()
	if !capture || autoDuringActive {
		t.Fatalf("default mode should retain persistent mouse capture: capture=%v auto=%v", capture, autoDuringActive)
	}
}

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"host", "--mouse"}, "--mouse") {
		t.Fatal("hasFlag should find an exact flag")
	}
	if hasFlag([]string{"--mouse=true"}, "--mouse") {
		t.Fatal("hasFlag should not match a flag prefix")
	}
}
