package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// The "C" keybind (acknowledgeAll) is the bulk form of "c": one keypress
// closes every open done episode at once. These tests cover its own
// contract — what it closes, what it must leave alone, what it writes to
// the shared ack store — on top of the per-row behavior acknowledge
// already guarantees (and app_test.go/done_sync_test.go already cover).

// TestAcknowledgeAllClosesEveryOpenEpisode is the feature itself: a
// screen full of done rows, one "C", and every one of them displays as
// idle from that instant, without waiting for the next poll.
func TestAcknowledgeAllClosesEveryOpenEpisode(t *testing.T) {
	withAckStore(t)
	settledAt := time.Now()

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		pistatusEntry(1, ancestry.Ghostty, settledAt),
		pistatusEntry(2, ancestry.Ghostty, settledAt),
		pistatusEntry(3, ancestry.Ghostty, settledAt),
	})
	for i := range m.table.Rows() {
		if got := m.table.Rows()[i][colState]; got != "done" {
			t.Fatalf("row %d: got %q before C, want done", i, got)
		}
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	m = updated.(Model)

	for i := range m.table.Rows() {
		if got := m.table.Rows()[i][colState]; got != "idle" {
			t.Fatalf("row %d: got %q right after C, want idle", i, got)
		}
	}
	for key, ep := range m.done {
		if ep.Acked.IsZero() {
			t.Errorf("episode %s: want Acked set by C, still open", key)
		}
	}
}

// TestAcknowledgeAllLeavesAlreadyAcknowledgedEpisodesUntouched guards the
// skip-don't-re-stamp rule: an episode the user already dismissed has an
// Acked timestamp driving sinceCellText's "idle since" clock, and a later
// bulk dismissal must not bump it (or the row would misleadingly read as
// just-acknowledged).
func TestAcknowledgeAllLeavesAlreadyAcknowledgedEpisodesUntouched(t *testing.T) {
	withAckStore(t)
	settledAt := time.Now()

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		pistatusEntry(1, ancestry.Ghostty, settledAt),
		pistatusEntry(2, ancestry.Ghostty, settledAt),
	})

	// Acknowledge the first row individually (cursor starts on row 0, and
	// all-done entries sort by pid: row 0 is pid 1). This is the episode
	// the bulk dismissal must leave exactly as it found it.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	ackedBefore := m.done["1:pi"].Acked
	if ackedBefore.IsZero() {
		t.Fatal("setup: want c to have acknowledged the first row")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	m = updated.(Model)

	if got := m.done["1:pi"].Acked; !got.Equal(ackedBefore) {
		t.Errorf("already-acknowledged episode: Acked moved from %v to %v, want untouched", ackedBefore, got)
	}
	if got := m.done["2:pi"].Acked; got.IsZero() {
		t.Error("open episode: want Acked set by C")
	}
}

// TestAcknowledgeAllWritesAnAckRecordPerOpenEpisode covers the
// cross-instance side: one record per open episode, each anchored to that
// episode's own RawAt, so other instances' syncAcksFromOtherInstances can
// match them the same way they match a single c's record.
func TestAcknowledgeAllWritesAnAckRecordPerOpenEpisode(t *testing.T) {
	store := withAckStore(t)
	first := time.Now()
	second := first.Add(time.Second)

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		pistatusEntry(1, ancestry.Ghostty, first),
		pistatusEntry(2, ancestry.Ghostty, second),
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	m = updated.(Model)

	for key, rawAt := range map[string]time.Time{"1:pi": first, "2:pi": second} {
		rec, ok := store[key]
		if !ok {
			t.Errorf("want an ack record for %s", key)
			continue
		}
		if !rec.RawAt.Equal(rawAt) {
			t.Errorf("%s: got RawAt %v, want %v (this episode's own settle)", key, rec.RawAt, rawAt)
		}
	}
}

// TestAcknowledgeAllWithNothingDoneIsANoOp pins down the degenerate
// cases: no entries at all (the placeholder row) and entries with no open
// episodes. Both must be safe, change nothing, and not invent episodes
// for rows that were never done — C only ever closes episodes that
// updateDoneTracking already opened.
func TestAcknowledgeAllWithNothingDoneIsANoOp(t *testing.T) {
	withAckStore(t)

	empty := New(999) // no poll at all: nil done map, placeholder row
	if updated, _ := empty.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")}); updated.(Model).done != nil {
		t.Fatal("want C with no entries to leave the done map nil, not allocate one")
	}

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.Ghostty, "idle"),
	})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	m = updated.(Model)
	if len(m.done) != 0 {
		t.Fatalf("got %d tracked episodes, want none: C must not invent episodes for non-done rows", len(m.done))
	}
	if got := m.table.Rows()[0][colState]; got != "working" {
		t.Fatalf("got %q, want working untouched by C", got)
	}
}

// TestAcknowledgeAllStopsAMidBurstBlink guards the blink interaction: a
// burst in flight when C lands must stop immediately (blinkActive keys
// off Acked.IsZero()), not run out its remaining phases on a row that no
// longer reads done.
func TestAcknowledgeAllStopsAMidBurstBlink(t *testing.T) {
	withAckStore(t)
	settledAt := time.Now()

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	// What a poll does after opening the episode: start its first burst.
	m.tickBlinks(time.Now())
	if !blinkActive(m.done["1:pi"], time.Now()) {
		t.Fatal("setup: want the fresh episode mid-burst")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	m = updated.(Model)

	if blinkActive(m.done["1:pi"], time.Now()) {
		t.Fatal("want the burst stopped the instant C lands")
	}
	if cmd := m.tickBlinks(time.Now()); cmd != nil {
		t.Fatal("want no further blink frames scheduled once nothing is open")
	}
}

// TestAcknowledgeAllInOneModelSyncsToAnotherModel is the bulk analogue of
// TestAcknowledgeInOneModelSyncsToAnotherModelOnItsNextPoll: pressing "C"
// in one canopy instance must settle every open episode in a second
// instance too, on that instance's own next poll, via the shared ack
// store alone.
func TestAcknowledgeAllInOneModelSyncsToAnotherModel(t *testing.T) {
	withAckStore(t)
	settledAt := time.Now()
	fresh := []registry.RegistryEntry{
		pistatusEntry(1, ancestry.Ghostty, settledAt),
		pistatusEntry(2, ancestry.Ghostty, settledAt),
	}

	a := New(999)
	b := New(999)
	a.applyEntries(fresh)
	b.applyEntries(fresh)

	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	a = updated.(Model)
	for i := range a.table.Rows() {
		if got := a.table.Rows()[i][colState]; got != "idle" {
			t.Fatalf("instance a row %d: got %q right after C, want idle", i, got)
		}
	}

	// Instance b never saw the keypress; its next poll must pick up both
	// of a's acknowledgments from the shared store.
	b.applyEntries(fresh)
	for i := range b.table.Rows() {
		if got := b.table.Rows()[i][colState]; got != "idle" {
			t.Fatalf("instance b row %d: got %q on its next poll, want idle synced in from instance a", i, got)
		}
	}
}
