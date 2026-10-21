package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMouseSelectionTextHandlesReverseDragAndWideRunes(t *testing.T) {
	m := Model{}
	m.mouseSelection = mouseSelectionState{
		start: mouseSelectionPoint{x: 4, y: 1},
		end:   mouseSelectionPoint{x: 1, y: 0},
		lines: []string{"abc     ", "中文 beta "},
	}
	if got, want := m.mouseSelectionText(), "bc\n中文"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
}

func TestMouseSelectionReleaseUsesFinalCoordinates(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 20
	m.viewport.Height = 4
	m.setViewportContent("first line\nsecond line\nthird line")
	m.lastInputAreaTop = m.viewport.Height

	updatedModel, _ := m.Update(tea.MouseMsg{
		X: 1, Y: 0, Shift: true, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	updated := updatedModel.(Model)
	updatedModel, _ = updated.Update(tea.MouseMsg{
		X: 2, Y: 1, Shift: true, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion,
	})
	updated = updatedModel.(Model)
	updatedModel, cmd := updated.Update(tea.MouseMsg{
		X: 6, Y: 1, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease,
	})
	updated = updatedModel.(Model)
	if cmd == nil {
		t.Fatal("completed selection should start a clipboard command")
	}
	if updated.mouseSelection.dragging {
		t.Fatal("mouse release should end dragging")
	}
	if got, want := updated.mouseSelectionText(), "irst line\nsecond"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
}

func TestMouseSelectionDirectDragWithoutShift(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 20
	m.viewport.Height = 4
	m.setViewportContent("first line\nsecond line\nthird line")
	m.lastInputAreaTop = m.viewport.Height

	updatedModel, _ := m.Update(tea.MouseMsg{
		X: 1, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	updated := updatedModel.(Model)
	if !updated.mouseSelection.dragging {
		t.Fatal("plain left press in chat area should start a selection without Shift")
	}
	updatedModel, _ = updated.Update(tea.MouseMsg{
		X: 2, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion,
	})
	updated = updatedModel.(Model)
	updatedModel, cmd := updated.Update(tea.MouseMsg{
		X: 6, Y: 1, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease,
	})
	updated = updatedModel.(Model)
	if cmd == nil {
		t.Fatal("completed selection should start a clipboard command")
	}
	if got, want := updated.mouseSelectionText(), "irst line\nsecond"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
}

func TestMouseSelectionEdgeAutoScrollExtendsBeyondViewport(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 20
	m.viewport.Height = 4
	lines := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	m.setViewportContent(strings.Join(lines, "\n"))
	m.viewport.SetYOffset(8) // visible window: lines 8..11
	m.lastInputAreaTop = m.viewport.Height

	// Press at the top visible row (line 08), at its end, without Shift.
	updatedModel, _ := m.Update(tea.MouseMsg{
		X: 7, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	updated := updatedModel.(Model)
	if !updated.mouseSelection.dragging {
		t.Fatal("plain left press in chat area should start a selection")
	}
	if updated.mouseSelection.start.y != 8 {
		t.Fatalf("selection start should be content line 8, got %d", updated.mouseSelection.start.y)
	}

	// Motion at the top edge schedules the auto-scroll tick chain.
	updatedModel, cmd := updated.Update(tea.MouseMsg{
		X: 0, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion,
	})
	updated = updatedModel.(Model)
	if cmd == nil {
		t.Fatal("dragging at the top edge should schedule an auto-scroll tick")
	}

	// Execute the tick command and feed the message back into Update.
	tickMsg := cmd()
	updatedModel, _ = updated.Update(tickMsg)
	updated = updatedModel.(Model)
	if got, want := updated.viewport.YOffset, 8-mouseSelectionScrollStep; got != want {
		t.Fatalf("auto-scroll should move YOffset from 8 to %d, got %d", want, got)
	}
	if updated.mouseSelection.end.y != updated.viewport.YOffset {
		t.Fatalf("selection end should extend to content line %d, got %d",
			updated.viewport.YOffset, updated.mouseSelection.end.y)
	}

	// Release at the start of the scrolled-to top row (line 05):
	// copied text must include the lines that were above the visible window.
	updatedModel, _ = updated.Update(tea.MouseMsg{
		X: 0, Y: 0, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease,
	})
	updated = updatedModel.(Model)
	want := "line 05\nline 06\nline 07\nline 08"
	if got := updated.mouseSelectionText(); got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
	if updated.mouseSelection.dragging {
		t.Fatal("release should end dragging")
	}
}

func TestMouseSelectionAutoScrollStopsAtContentBoundary(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 20
	m.viewport.Height = 4
	m.setViewportContent("a\nb\nc\nd\ne\nf")
	m.viewport.SetYOffset(2) // visible: c..f, already at bottom
	m.lastInputAreaTop = m.viewport.Height

	updatedModel, _ := m.Update(tea.MouseMsg{
		X: 0, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	updated := updatedModel.(Model)
	updatedModel, cmd := updated.Update(tea.MouseMsg{
		X: 0, Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, // bottom edge
	})
	updated = updatedModel.(Model)
	if cmd == nil {
		t.Fatal("dragging at the bottom edge should schedule an auto-scroll tick")
	}
	// Bottom edge: viewport is already at the bottom, tick must not scroll
	// and must stop the tick chain.
	updatedModel, next := updated.Update(cmd())
	updated = updatedModel.(Model)
	if next != nil {
		t.Fatal("auto-scroll at the content boundary should stop the tick chain")
	}
	if got := updated.viewport.YOffset; got != 2 {
		t.Fatalf("YOffset should stay 2 at the bottom boundary, got %d", got)
	}
	if updated.mouseSelection.autoScrolling {
		t.Fatal("autoScrolling should be cleared at the boundary")
	}
}

func TestMouseSelectionClickInInputAreaDoesNotSelect(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.viewport.Width = 20
	m.viewport.Height = 4
	m.setViewportContent("first line\nsecond line")
	m.lastInputAreaTop = m.viewport.Height

	// Click inside the input area (below the chat area): must not start a
	// selection; existing selection must be cleared instead.
	updatedModel, _ := m.Update(tea.MouseMsg{
		X: 1, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	updated := updatedModel.(Model)
	if !updated.mouseSelection.dragging {
		t.Fatal("press in chat area should start a selection")
	}
	updatedModel, _ = updated.Update(tea.MouseMsg{
		X: 1, Y: m.lastInputAreaTop + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	updated = updatedModel.(Model)
	if updated.mouseSelection.dragging || updated.mouseSelection.hasText {
		t.Fatal("press in the input area should clear the mouse selection")
	}
}
