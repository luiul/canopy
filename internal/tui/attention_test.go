package tui

// Tests for the attention states: "error" (bell, blink, and raw-following
// display exactly like done) and the debounced "blocked" bell. Split from
// app_test.go; see blink.go/bell.go for the machinery under test.

import (
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// realEntry is entry plus the RealState markers a pi self-report carries:
// newSettles tells a genuinely new settle apart from a repeating one via
// RealStateReportedAt, which only RealState entries have.
func realEntry(pid int, surface ancestry.Surface, state string, reportedAt time.Time) registry.RegistryEntry {
	e := entry(pid, surface, state)
	e.RealState = true
	e.RealStateReportedAt = reportedAt
	return e
}

func TestErrorRingsAndDisplaysLikeDone(t *testing.T) {
	m := New(999, nil)
	if bell := m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "error")}); !bell {
		t.Fatal("want a bell for a fresh error")
	}
	if got := m.table.Rows()[0][colState]; got != "error"+blinkMarker && got != "error" {
		t.Fatalf("got %q, want error on screen", got)
	}

	// And like done, it leaves the screen only when the raw signal moves on.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})
	if got := m.table.Rows()[0][colState]; got != "working" {
		t.Fatalf("got %q, want working once the raw signal moves on", got)
	}
}

func TestDoneRelatchesToErrorOnANewSettle(t *testing.T) {
	// A turn settles done; the next turn starts and fails fast — all
	// between two polls, so the raw source goes done -> error with no
	// working poll in between. The row must follow the raw word (it now
	// reads error), and the newer write must ring again: a second settle
	// really did happen (see newSettles's RealStateReportedAt rule).
	m := New(999, nil)
	t1 := time.Now().Add(-time.Minute)
	t2 := time.Now()
	if bell := m.applyEntries([]registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "done", t1)}); !bell {
		t.Fatal("want a bell for the first settle")
	}

	if bell := m.applyEntries([]registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "error", t2)}); !bell {
		t.Fatal("want a bell for the error settle too — it is a genuinely new write")
	}
	if got := m.table.Rows()[0][colState]; got != "error"+blinkMarker && got != "error" {
		t.Fatalf("got %q, want the row to read error now", got)
	}
}

func TestNewSettlesErrorTransitions(t *testing.T) {
	t1 := time.Now().Add(-time.Minute)
	t2 := time.Now()

	cases := []struct {
		name     string
		previous []registry.RegistryEntry
		fresh    []registry.RegistryEntry
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
			name:     "a new error write after a done rings again (a second settle really happened)",
			previous: []registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "done", t1)},
			fresh:    []registry.RegistryEntry{realEntry(1, ancestry.Ghostty, "error", t2)},
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
			if got := len(newSettles(c.previous, c.fresh)) > 0; got != c.want {
				t.Fatalf("newSettles() non-empty = %v, want %v", got, c.want)
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

func TestBlockedDisplaysAsIsAndNeverBlinks(t *testing.T) {
	// blocked is transient by definition (it clears the moment the dialog
	// closes), so there is nothing to signal beyond the word itself: no
	// settle burst, displayState passes it straight through.
	m := New(999, nil)
	e := entry(1, ancestry.Ghostty, "blocked")
	m.applyEntries([]registry.RegistryEntry{e})
	if len(m.blinks) != 0 {
		t.Fatalf("got %d bursts for a blocked entry, want none", len(m.blinks))
	}
	if got := displayState(e); got != "blocked" {
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
	sortEntries(entries)
	want := []string{"blocked", "error", "done", "working", "idle"}
	for i, w := range want {
		if got := entries[i].State; got != w {
			t.Fatalf("got order[%d] %q, want %q (full order %v)", i, got, w, entries)
		}
	}
}
