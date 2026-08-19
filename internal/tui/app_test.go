package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/jump"
	"github.com/luiul/canopy/internal/registry"
)

func entry(pid int, surface ancestry.Surface, state string) registry.RegistryEntry {
	return registry.RegistryEntry{Pid: pid, Kind: "pi", Tty: "s017", Cwd: "/Users/x/dotfiles", Surface: surface, State: state}
}

func TestSortEntriesRanksBlockedAndDoneAboveWorking(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.Ghostty, "idle"),
		entry(3, ancestry.Ghostty, "blocked"),
		entry(4, ancestry.Ghostty, "done"),
		entry(5, ancestry.Ghostty, "unknown"),
	}

	sortEntries(entries)

	var got []string
	for _, e := range entries {
		got = append(got, e.State)
	}
	want := []string{"blocked", "done", "working", "idle", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got order %v, want %v", got, want)
		}
	}
}

func TestStateCellTextFlashesOnlyBlockedOrDoneWithinTheFlashWindow(t *testing.T) {
	now := time.Now()

	blocked := entry(1, ancestry.Ghostty, "blocked")
	blocked.StateSince = now.Add(-time.Second)
	if got := stateCellText(blocked, now); got != "blocked"+flashMarker {
		t.Fatalf("got %q, want a flashing blocked cell", got)
	}

	staleBlocked := entry(2, ancestry.Ghostty, "blocked")
	staleBlocked.StateSince = now.Add(-flashDuration - time.Second)
	if got := stateCellText(staleBlocked, now); got != "blocked" {
		t.Fatalf("got %q, want a steady (non-flashing) blocked cell once past flashDuration", got)
	}

	working := entry(3, ancestry.Ghostty, "working")
	working.StateSince = now.Add(-time.Second)
	if got := stateCellText(working, now); got != "working" {
		t.Fatalf("got %q, want working to never flash", got)
	}
}

func TestSinceCellTextHumanizesElapsedTimeOrIsEmptyWhenUnknown(t *testing.T) {
	now := time.Now()

	e := entry(1, ancestry.Ghostty, "idle")
	e.StateSince = now.Add(-90 * time.Second)
	if got := sinceCellText(e, now); got != "1m" {
		t.Fatalf("got %q, want 1m", got)
	}

	unknown := entry(2, ancestry.Ghostty, "idle") // StateSince left zero
	if got := sinceCellText(unknown, now); got != "" {
		t.Fatalf("got %q, want empty when StateSince is unknown", got)
	}
}

func TestSummaryLineCountsByStateInPriorityOrder(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.Ghostty, "idle"),
		entry(3, ancestry.Ghostty, "blocked"),
	}

	got := summaryLine(entries)

	for _, want := range []string{"3 sessions", "1 blocked", "1 working", "1 idle"} {
		if !strings.Contains(got, want) {
			t.Fatalf("got %q, want it to contain %q", got, want)
		}
	}
	if strings.Index(got, "blocked") > strings.Index(got, "working") {
		t.Fatalf("got %q, want blocked listed before working", got)
	}
}

func TestSummaryLineIsEmptyWhenThereAreNoEntries(t *testing.T) {
	if got := summaryLine(nil); got != "" {
		t.Fatalf("got %q, want empty summary for no entries", got)
	}
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

func TestShortenHomeReplacesTheHomePrefixWithATilde(t *testing.T) {
	cases := []struct {
		path, home, want string
	}{
		{"/Users/luis/projects/canopy", "/Users/luis", "~/projects/canopy"},
		{"/Users/luis", "/Users/luis", "~"},
		{"/Users/luisandro/projects", "/Users/luis", "/Users/luisandro/projects"}, // no false-positive on a prefix match that isn't a path boundary
		{"/Users/luis/projects", "", "/Users/luis/projects"},                      // unknown home: leave untouched
	}
	for _, c := range cases {
		if got := shortenHome(c.path, c.home); got != c.want {
			t.Errorf("shortenHome(%q, %q) = %q, want %q", c.path, c.home, got, c.want)
		}
	}
}

func TestBuildRowsPutsCursorMarkerOnlyOnTheCursorRow(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "working"),
	}

	rows := buildRows(entries, 1, "", time.Now())

	if rows[0][colCursor] != "" {
		t.Fatalf("got marker %q on row 0, want no marker", rows[0][colCursor])
	}
	if rows[1][colCursor] != cursorMarker {
		t.Fatalf("got marker %q on row 1, want %q", rows[1][colCursor], cursorMarker)
	}
}

func TestCursorMarkerFollowsArrowKeysBetweenPolls(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "idle"),
	})
	if got := m.table.Rows()[0][colCursor]; got != cursorMarker {
		t.Fatalf("got %q, want the marker on row 0 right after applyEntries", got)
	}

	// Moving down (without a poll in between) must move the marker too, not
	// just bubbles/table's own internal cursor.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := updated.(Model)
	if got := mm.table.Rows()[0][colCursor]; got != "" {
		t.Fatalf("got %q, want row 0's marker cleared after moving down", got)
	}
	if got := mm.table.Rows()[1][colCursor]; got != cursorMarker {
		t.Fatalf("got %q, want row 1 to carry the marker after moving down", got)
	}
}
