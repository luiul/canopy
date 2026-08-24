package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ack"
	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// TestAcknowledgeInOneModelSyncsToAnotherModelOnItsNextPoll is the
// end-to-end scenario this whole feature exists for: two Models sharing
// the same ack store (withAckStore), standing in for two concurrently
// running canopy instances sharing the same
// ~/.pi/agent/canopy-status/acks directory. Pressing "c" in one must be
// reflected in the other on its very next poll, with no jump and no
// direct communication between the two Models at all — only the shared
// store.
func TestAcknowledgeInOneModelSyncsToAnotherModelOnItsNextPoll(t *testing.T) {
	withAckStore(t)
	settledAt := time.Now()

	a := New(999)
	b := New(999)
	a.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	b.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	if got := a.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("instance a: got %q before any acknowledgment, want done", got)
	}
	if got := b.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("instance b: got %q before any acknowledgment, want done", got)
	}

	// The user acknowledges in instance a only (no jump: the "c" keybind).
	updated, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	a = updated.(Model)
	if got := a.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("instance a: got %q right after acknowledging, want idle", got)
	}

	// Instance b never saw a keypress; the raw source hasn't moved either
	// (still the exact same settle). Its next poll must still pick up a's
	// acknowledgment via the shared ack store.
	b.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	if got := b.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("instance b: got %q on its next poll, want idle synced in from instance a", got)
	}
}

// TestSyncDoesNotApplyAStaleAckToAGenuinelyNewEpisode guards the identity
// check in syncAcksFromOtherInstances: an ack record left over for an
// earlier settle of the same key must never be mistaken for acknowledging
// a brand new one that happens to reuse it, the cross-instance analogue
// of TestAcknowledgedRealStateDoneReopensForAGenuinelyNewSettleWithNoInterveningPoll.
func TestSyncDoesNotApplyAStaleAckToAGenuinelyNewEpisode(t *testing.T) {
	store := withAckStore(t)
	firstSettle := time.Now()

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, firstSettle)})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	if _, ok := store["1:pi"]; !ok {
		t.Fatal("want acknowledge to have written an ack record for the first settle")
	}

	// A second, genuinely new settle arrives — same key, different RawAt —
	// before this instance's own m.done entry (or the stale ack record
	// still sitting in the store) has any chance to be mistaken for
	// covering it.
	secondSettle := firstSettle.Add(time.Second)
	if bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, secondSettle)}); !bell {
		t.Error("want a fresh bell for the new, unacknowledged episode")
	}
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want the new episode to display as done, not swallowed by the stale ack record", got)
	}
}

// TestSyncIgnoresAnAckRecordForADifferentStillOpenEpisode is the same
// guard from the other side: a fabricated ack record for some other RawAt
// (as if another instance had acknowledged a different, earlier episode
// for this same key) must not close an episode it doesn't actually match.
func TestSyncIgnoresAnAckRecordForADifferentStillOpenEpisode(t *testing.T) {
	store := withAckStore(t)
	settledAt := time.Now()

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})

	store["1:pi"] = ack.Record{Key: "1:pi", RawAt: settledAt.Add(-time.Minute), At: time.Now()}

	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want the mismatched ack record left unapplied", got)
	}
}

// TestUpdateDoneTrackingRemovesTheAckRecordOnceTheEpisodeCloses covers
// cleanup: once an acknowledged episode's raw source moves off "done", or
// the underlying session disappears outright, the shared ack store must
// not keep the now-irrelevant record around forever.
func TestUpdateDoneTrackingRemovesTheAckRecordOnceTheEpisodeCloses(t *testing.T) {
	store := withAckStore(t)
	settledAt := time.Now()

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	if _, ok := store["1:pi"]; !ok {
		t.Fatal("want an ack record written right after acknowledging")
	}

	// The raw source moves on to a fresh working turn: the acknowledged
	// episode closes.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})
	if _, ok := store["1:pi"]; ok {
		t.Fatal("want the ack record removed once the acknowledged episode closes")
	}
}

// TestUpdateDoneTrackingRemovesTheAckRecordWhenTheSessionEnds is the
// other cleanup trigger: the row disappears from a poll outright (process
// exited), before ever being acknowledged locally at all — exactly the
// case a different instance's acknowledgment, synced in, needs cleaning
// up too.
func TestUpdateDoneTrackingRemovesTheAckRecordWhenTheSessionEnds(t *testing.T) {
	store := withAckStore(t)
	settledAt := time.Now()
	store["1:pi"] = ack.Record{Key: "1:pi", RawAt: settledAt, At: time.Now()}

	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want the episode synced in as acknowledged from the pre-seeded ack record", got)
	}

	m.applyEntries(nil) // the session ended
	if _, ok := store["1:pi"]; ok {
		t.Fatal("want the ack record removed once the session that owned it disappears")
	}
}
