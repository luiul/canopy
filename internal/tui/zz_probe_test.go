package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// Probe: does a fresh "done" episode open correctly if the raw source goes
// straight from an *already-acknowledged* done back to "done" again, with
// no intervening poll where State reads anything else (working/idle)?
func TestProbeAckedDoneImmediatelyDoneAgainSamePollBoundary(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("after ack: got %q, want idle", got)
	}

	// NO intervening working/idle poll here — raw goes straight from
	// (acked) done back to done again, as if a very fast next turn settled
	// between two polls.
	bell := m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})
	t.Logf("bell=%v state=%q", bell, m.table.Rows()[0][colState])
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Errorf("got %q, want done: a brand-new done episode should surface even with no intervening non-done poll", got)
	}
	if !bell {
		t.Error("want a bell for this new done episode")
	}
}
