package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/luiul/dashkit/loam"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

func TestDisplayMessageFollowsTheEpisodeOverlay(t *testing.T) {
	// No episode at all: the raw message renders (blocked dialog title,
	// working session name), and states without one render "".
	e := messagedEntry(1, ancestry.Ghostty, "blocked", "Allow Bash: go test ./...?")
	if got := displayMessage(e, nil); got != "Allow Bash: go test ./...?" {
		t.Fatalf("no-episode displayMessage = %q", got)
	}

	// An open episode latches its message even after the raw source moved
	// on to a fresh working turn with a message of its own.
	key := e.Key()
	open := map[string]doneEpisode{key: {State: "error", Message: "rate limit exceeded"}}
	e.State, e.Message = "working", "next-turn"
	if got := displayMessage(e, open); got != "rate limit exceeded" {
		t.Fatalf("open episode must keep its own message, got %q", got)
	}

	// Acknowledged and still settled: the row displays "idle", which
	// carries no message.
	acked := map[string]doneEpisode{key: {State: "error", Message: "rate limit exceeded", Acked: time.Now()}}
	e.State, e.Message = "error", "rate limit exceeded"
	if got := displayMessage(e, acked); got != "" {
		t.Fatalf("acknowledged still-settled row must show no message, got %q", got)
	}

	// Acknowledged and the raw source moved on: the new state's own
	// message is what shows.
	e.State, e.Message = "working", "next-turn"
	if got := displayMessage(e, acked); got != "next-turn" {
		t.Fatalf("moved-on raw state's message = %q", got)
	}
}

func TestUpdateDoneTrackingLatchesAndRefreshesMessage(t *testing.T) {
	m := New(time.Second, nil)
	e := messagedEntry(1, ancestry.Ghostty, "error", "first failure")
	e.RealState = true
	e.RealStateReportedAt = time.Now()
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.done[e.Key()].Message; got != "first failure" {
		t.Fatalf("episode latched message %q", got)
	}

	// A fast second settle while the episode is still open re-latches the
	// message alongside State/RawAt (same rule displayState follows).
	e.RealStateReportedAt = e.RealStateReportedAt.Add(time.Second)
	e.Message = "second failure"
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.done[e.Key()].Message; got != "second failure" {
		t.Fatalf("re-latched message %q, want the newer settle's", got)
	}
}

func TestLocationCellTextKeepsTheTail(t *testing.T) {
	e := entry(1, ancestry.VSCode, "working")
	e.Cwd = "/Users/x/worktrees/hellofresh/speed-up-ci/global-ops"
	full := location(e, "")
	if got := locationCellText(e, "", 200); got != full {
		t.Fatalf("wide column must not truncate, got %q", got)
	}
	got := locationCellText(e, "", 20)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "global-ops") {
		t.Fatalf("truncation must keep the tail with a leading ellipsis, got %q", got)
	}
	if w := ansi.StringWidth(got); w != 20 {
		t.Fatalf("truncated width = %d, want 20", w)
	}
	// Multi-byte runes must not split mid-sequence or overflow the width.
	e.Cwd = "/Users/x/模型模型模型模型模型模型/global-ops"
	got = locationCellText(e, "", 20)
	if w := ansi.StringWidth(got); w > 20 || !strings.HasPrefix(got, "…") {
		t.Fatalf("CJK truncation = %q (width %d)", got, w)
	}
	if got := locationCellText(e, "", 0); got != e.Cwd {
		t.Fatalf("unknown width (0) must leave the path alone, got %q", got)
	}
}

func TestFirstVisibleRowFollowsTheSentinel(t *testing.T) {
	lines := []string{"header", "row-a", "row-b", loam.Sentinel + "row-c", "row-d"}
	first, ok := firstVisibleRow(lines, 5)
	if !ok || first != 3 {
		t.Fatalf("firstVisibleRow = %d, %v, want 3, true (cursor 5 on data line 3)", first, ok)
	}
	if _, ok := firstVisibleRow([]string{"header", "placeholder"}, 0); ok {
		t.Fatal("no sentinel must report ok=false, so callers skip message work")
	}
}

func TestResizeTableHeightHandsBackOneRowPerVisibleMessage(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 150, 14
	var entries []registry.RegistryEntry
	for i := 0; i < 8; i++ {
		entries = append(entries, messagedEntry(i+1, ancestry.Ghostty, "working", "session-alpha"))
	}
	m.applyEntries(entries)
	// 8 rows, all with messages: base = 14-6 = 8, so only 4 data rows
	// survive (4 rows + 4 message lines = 8).
	if got := m.table.Height(); got != 3 { // viewport = SetHeight(4) - 1 header
		t.Fatalf("table height = %d, want 3 (4 rows incl. header)", got)
	}
	if n := len(strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n")); n > m.height {
		t.Fatalf("view has %d lines, exceeds terminal height %d:\n%s", n, m.height, m.View())
	}

	// Scrolling to rows the fit did not account for re-fits the height:
	// the last four rows carry the same messages, so the body must still
	// fit the terminal.
	m.table.SetCursor(7)
	m.refreshCursorTag()
	if n := len(strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n")); n > m.height {
		t.Fatalf("after scrolling: view has %d lines, exceeds %d:\n%s", n, m.height, m.View())
	}
}

func TestViewOmitsMessageLinesEntirelyWhenNoMessages(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.VSCode, "idle"),
	})
	if strings.Contains(m.View(), strings.TrimSpace(messageIndent)) {
		t.Fatalf("no messages must mean no detail lines:\n%s", m.View())
	}
}
