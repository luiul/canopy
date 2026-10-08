package tui

// Tests for the two attention-state additions on top of the original
// done-only machinery: "error" episodes (sticky, bell, blink, ack exactly
// like done) and the debounced "blocked" bell. Split from app_test.go;
// see done.go/bell.go for the machinery under test.

import (
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// realEntry is entry plus the RealState markers a pi self-report carries:
// needsBell/updateDoneTracking tell a genuinely new settle apart from a
// repeating one via RealStateReportedAt, which only RealState entries
// have.
func realEntry(pid int, surface ancestry.Surface, state string, reportedAt time.Time) registry.RegistryEntry {
	e := entry(pid, surface, state)
	e.RealState = true
	e.RealStateReportedAt = reportedAt
	return e
}

func TestUpdateDoneTrackingOpensAnErrorEpisode(t *testing.T) {
	m := New(999, nil)
	e := entry(1, ancestry.Ghostty, "error")
	m.updateDoneTracking([]registry.RegistryEntry{e})

	ep, ok := m.done[e.Key()]
	if !ok {
		t.Fatalf("got no episode for a raw error entry, want one opened")
	}
	if ep.State != "error" || !ep.Acked.IsZero() {
		t.Fatalf("got episode %+v, want an open error episode", ep)
	}
	if got := displayState(e, m.done); got != "error" {
		t.Fatalf("got %q, want error displayed for the open episode", got)
	}
}

func TestDisplayStateFoldsAnAckedErrorIntoIdle(t *testing.T) {
	m := New(999, nil)
	e := entry(1, ancestry.Ghostty, "error")
	m.updateDoneTracking([]registry.RegistryEntry{e})
	m.acknowledge(e)

	if got := displayState(e, m.done); got != "idle" {
		t.Fatalf("got %q, want idle for an acknowledged error episode", got)
	}
	if e.State != "error" {
		t.Fatalf("got raw State %q, want acknowledge to leave it alone", e.State)
	}
}

func TestOpenDoneEpisodeRelatchesToErrorOnANewSettle(t *testing.T) {
	// A turn settles done; before the user ever acknowledges it in canopy,
	// the next turn starts and fails fast — all between two polls, so the
	// raw source goes done -> error with no working poll in between. The
	// still-open episode must follow the raw word (the row now reads
	// error), stay the same open episode (no re-bell for something already
	// flagged), and keep its original Since.
	m := New(999, nil)
	t1 := time.Now().Add(-time.Minute)
	t2 := time.Now()
	m.updateDoneTracking([]registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "done", t1)})
	key := entry(1, ancestry.Ghostty, "").Key()
	opened := m.done[key]

	m.updateDoneTracking([]registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "error", t2)})

	ep := m.done[key]
	if ep.State != "error" {
		t.Fatalf("got episode word %q, want re-latched to error", ep.State)
	}
	if !ep.Acked.IsZero() {
		t.Fatalf("got Acked set, want the episode to stay open (nothing was acknowledged)")
	}
	if !ep.Since.Equal(opened.Since) {
		t.Fatalf("got Since %v, want the original open time %v", ep.Since, opened.Since)
	}
	if !ep.RawAt.Equal(t2) {
		t.Fatalf("got RawAt %v, want kept current at %v", ep.RawAt, t2)
	}
	if got := displayState(realEntry(1, ancestry.Ghostty, "error", t2), m.done); got != "error" {
		t.Fatalf("got %q, want error displayed", got)
	}
}

func TestNeedsBellErrorTransitions(t *testing.T) {
	t1 := time.Now().Add(-time.Minute)
	t2 := time.Now()

	cases := []struct {
		name     string
		previous []registry.RegistryEntry
		fresh    []registry.RegistryEntry
		done     map[string]doneEpisode
		want     bool
	}{
		{
			name:     "working flipping to error rings",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "error")},
			want:     true,
		},
		{
			name:     "staying error across polls does not re-ring",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "error")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "error")},
			want:     false,
		},
		{
			name:     "a new error write landing on a still-open done episode is absorbed silently",
			previous: []registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "done", t1)},
			fresh:    []registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "error", t2)},
			done:     map[string]doneEpisode{entry(1, ancestry.Ghostty, "").Key(): {State: "done", Since: t1, RawAt: t1}},
			want:     false,
		},
		{
			name:     "a new error write after the done was acknowledged rings again",
			previous: []registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "done", t1)},
			fresh:    []registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "error", t2)},
			done:     map[string]doneEpisode{entry(1, ancestry.Ghostty, "").Key(): {State: "done", Since: t1, Acked: t1, RawAt: t1}},
			want:     true,
		},
		{
			name:     "error flipping to working does not ring",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "error")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")},
			want:     false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsBell(c.previous, c.fresh, c.done); got != c.want {
				t.Fatalf("needsBell() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNewlyBlockedDebouncesAndRingsOncePerSpell(t *testing.T) {
	blocked := entry(1, ancestry.Ghostty, "blocked")
	working := entry(1, ancestry.Ghostty, "working")
	rung := map[string]bool{}

	// First poll seeing blocked: nothing yet — this is exactly a dialog
	// the user opened and is answering right now.
	if keys := newlyBlocked(nil, []registry.RegistryEntry{blocked}, rung); len(keys) != 0 {
		t.Fatalf("got %v, want no ring on the first blocked poll", keys)
	}
	// Still blocked one full poll later: ring once.
	keys := newlyBlocked([]registry.RegistryEntry{blocked}, []registry.RegistryEntry{blocked}, rung)
	if len(keys) != 1 || keys[0] != blocked.Key() {
		t.Fatalf("got %v, want the blocked key after two consecutive polls", keys)
	}
	rung[keys[0]] = true
	// Third poll of the same spell: already rung, silent.
	if keys := newlyBlocked([]registry.RegistryEntry{blocked}, []registry.RegistryEntry{blocked}, rung); len(keys) != 0 {
		t.Fatalf("got %v, want no repeat ring within one spell", keys)
	}
	// The dialog closes (raw leaves blocked) and a new one opens later: a
	// fresh spell rings again once the caller prunes the old key.
	delete(rung, blocked.Key())
	if keys := newlyBlocked([]registry.RegistryEntry{blocked}, []registry.RegistryEntry{blocked}, rung); len(keys) != 1 {
		t.Fatalf("got %v, want a fresh spell to ring again", keys)
	}
	// A row that flips blocked -> working within one poll interval never rings.
	if keys := newlyBlocked([]registry.RegistryEntry{blocked}, []registry.RegistryEntry{working}, rung); len(keys) != 0 {
		t.Fatalf("got %v, want no ring once the dialog is gone", keys)
	}
}

func TestApplyEntriesRingsBlockedOnlyAfterTheSecondPollAndReArms(t *testing.T) {
	m := New(999, nil)
	blocked := entry(1, ancestry.Ghostty, "blocked")
	working := entry(1, ancestry.Ghostty, "working")

	if bell := m.applyEntries([]registry.RegistryEntry{blocked}); bell {
		t.Fatal("want no bell on the first blocked poll (debounce)")
	}
	if bell := m.applyEntries([]registry.RegistryEntry{blocked}); !bell {
		t.Fatal("want a bell once blocked survives two consecutive polls")
	}
	if bell := m.applyEntries([]registry.RegistryEntry{blocked}); bell {
		t.Fatal("want no repeat bell within the same blocked spell")
	}
	if bell := m.applyEntries([]registry.RegistryEntry{working}); bell {
		t.Fatal("want no bell when the dialog closes")
	}
	// A later dialog is a fresh spell: debounce applies again, then it rings.
	if bell := m.applyEntries([]registry.RegistryEntry{blocked}); bell {
		t.Fatal("want the fresh spell to debounce its first poll too")
	}
	if bell := m.applyEntries([]registry.RegistryEntry{blocked}); !bell {
		t.Fatal("want a bell for the fresh spell's second poll")
	}
}

func TestBlockedNeverOpensAnAttentionEpisode(t *testing.T) {
	// blocked is transient by definition (it clears the moment the dialog
	// closes), so there is nothing to acknowledge: no episode, nothing
	// sticky, displayState passes it straight through.
	m := New(999, nil)
	e := entry(1, ancestry.Ghostty, "blocked")
	m.updateDoneTracking([]registry.RegistryEntry{e})
	if len(m.done) != 0 {
		t.Fatalf("got %d episodes for a blocked entry, want none", len(m.done))
	}
	if got := displayState(e, m.done); got != "blocked" {
		t.Fatalf("got %q, want blocked displayed as-is", got)
	}
}

func TestErrorSortingRanksAboveDone(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "done"),
		entry(2, ancestry.Ghostty, "working"),
		entry(3, ancestry.Ghostty, "error"),
		entry(4, ancestry.Ghostty, "blocked"),
		entry(5, ancestry.Ghostty, "idle"),
	}
	sortEntries(entries, nil)
	want := []string{"blocked", "error", "done", "working", "idle"}
	for i, w := range want {
		if got := entries[i].State; got != w {
			t.Fatalf("got order[%d] %q, want %q (full order %v)", i, got, w, entries)
		}
	}
}
