package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy-go/internal/ancestry"
	"github.com/luiul/canopy-go/internal/jump"
	"github.com/luiul/canopy-go/internal/registry"
)

func entry(pid int, surface ancestry.Surface, state string) registry.RegistryEntry {
	return registry.RegistryEntry{Pid: pid, Kind: "pi", Tty: "s017", Cwd: "/Users/x/dotfiles", Surface: surface, State: state}
}

func TestDashboardRendersARowPerEntry(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.VSCode, "idle"),
	})

	if got := len(m.table.Rows()); got != 2 {
		t.Fatalf("got %d rows, want 2", got)
	}
}

func TestDashboardShowsAPlaceholderRowWhenNothingIsFound(t *testing.T) {
	m := New(999)
	m.applyEntries(nil)

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("got %d rows, want 1 placeholder row", got)
	}
	if _, ok := m.selectedEntry(); ok {
		t.Fatal("want no selected entry when only the placeholder row is showing")
	}
}

func TestEnterOnARowTriggersJumpToTheSelectedEntry(t *testing.T) {
	target := entry(42, ancestry.Ghostty, "working")
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{target})
	m.table.SetCursor(0)

	selected, ok := m.selectedEntry()
	if !ok {
		t.Fatal("want a selected entry")
	}
	if selected != target {
		t.Fatalf("got %+v, want %+v", selected, target)
	}
}

func TestApplyEntriesPreservesCursorOnTheSameKeyAfterAReorder(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "idle"),
	})
	m.table.SetCursor(1) // pid 2 selected

	// A fresh poll where pid 2 is now "working" sorts it to the front.
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "working"),
	})

	selected, ok := m.selectedEntry()
	if !ok || selected.Pid != 2 {
		t.Fatalf("got %+v, want pid 2 to stay selected across the reorder", selected)
	}
}

func TestJumpResultMsgSetsNotificationAndSchedulesClear(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(jumpResultMsg{result: jump.Result{OK: true, Message: "Focused in Ghostty."}})
	mm := updated.(Model)

	if mm.notification != "Focused in Ghostty." {
		t.Fatalf("got notification %q", mm.notification)
	}
	if mm.notifyIsError {
		t.Fatal("want notifyIsError=false for a successful jump")
	}
	if cmd == nil {
		t.Fatal("want a clear-notification command to be scheduled")
	}

	// Simulate the scheduled clear firing for the current token.
	cleared, _ := mm.Update(clearNotifyMsg{token: mm.notifyToken})
	if cleared.(Model).notification != "" {
		t.Fatal("want notification cleared once its own token fires")
	}
}

func TestStaleClearNotifyMsgIsIgnored(t *testing.T) {
	m := New(999)
	updated, _ := m.Update(jumpResultMsg{result: jump.Result{OK: false, Message: "nope"}})
	mm := updated.(Model)

	// A clear message carrying an old token (e.g. from a jump before this
	// one) must not wipe out a newer notification.
	stale, _ := mm.Update(clearNotifyMsg{token: mm.notifyToken - 1})
	if stale.(Model).notification != "nope" {
		t.Fatal("want notification to survive a stale clear token")
	}
}

func TestQuitKeyStopsTheProgram(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !updated.(Model).quitting {
		t.Fatal("want quitting=true after q")
	}
	if cmd == nil {
		t.Fatal("want tea.Quit to be returned")
	}
}
