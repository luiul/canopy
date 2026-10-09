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

func TestDetailLineTextComposesPerState(t *testing.T) {
	// blocked/error show their message (title / error line) alone.
	b := messagedEntry(1, ancestry.Ghostty, "blocked", "Allow Bash: go test?")
	b.Detail, b.Task = "bash: go test", "plan the thing"
	if got := detailLineText(b); got != "Allow Bash: go test?" {
		t.Fatalf("blocked = %q", got)
	}

	// working/done join name and activity/outcome, either part droppable.
	w := messagedEntry(2, ancestry.Ghostty, "working", "sprint-planning")
	w.Detail, w.Task = "edit: internal/tui/rows.go", "plan the thing"
	if got := detailLineText(w); got != "sprint-planning · edit: internal/tui/rows.go" {
		t.Fatalf("working = %q", got)
	}
	w.Message = ""
	if got := detailLineText(w); got != "edit: internal/tui/rows.go" {
		t.Fatalf("working unnamed = %q", got)
	}
	w.Detail = ""
	if got := detailLineText(w); got != "plan the thing" {
		t.Fatalf("working with neither name nor activity falls back to the task, got %q", got)
	}

	// idle/stopped/unknown show the task; a pi never prompted shows nothing.
	i := entry(3, ancestry.Ghostty, "idle")
	i.Task = "plan the thing"
	if got := detailLineText(i); got != "plan the thing" {
		t.Fatalf("idle = %q", got)
	}
	if got := detailLineText(entry(4, ancestry.Ghostty, "idle")); got != "" {
		t.Fatalf("idle without a task must render no line, got %q", got)
	}
}

// The detail line belongs to whatever the raw signal says right now: a
// settled row shows its name and outcome, and the moment the session
// starts a fresh turn the line follows it — nothing latches the old
// settle's content onto the screen.
func TestDetailLineFollowsTheRawSignal(t *testing.T) {
	m := New(time.Second, nil)
	e := messagedEntry(1, ancestry.Ghostty, "done", "sprint-planning")
	e.Detail = "TLDR: fixed the filter."
	e.RealState = true
	e.RealStateReportedAt = time.Now()
	m.applyEntries([]registry.RegistryEntry{e})
	if got := detailLineText(m.entries[0]); got != "sprint-planning · TLDR: fixed the filter." {
		t.Fatalf("done line = %q", got)
	}

	e.State, e.Detail = "working", "bash: go test ./..."
	m.applyEntries([]registry.RegistryEntry{e})
	if got := detailLineText(m.entries[0]); got != "sprint-planning · bash: go test ./..." {
		t.Fatalf("working line = %q, want the fresh turn's own line", got)
	}
}

// Same rule on the error side: a newer failure's first line replaces the
// older one's as soon as the raw write lands.
func TestDetailLineRefreshesWithTheRawReport(t *testing.T) {
	m := New(time.Second, nil)
	e := messagedEntry(1, ancestry.Ghostty, "error", "first failure")
	e.RealState = true
	e.RealStateReportedAt = time.Now()
	m.applyEntries([]registry.RegistryEntry{e})
	if got := detailLineText(m.entries[0]); got != "first failure" {
		t.Fatalf("error line = %q", got)
	}

	e.RealStateReportedAt = e.RealStateReportedAt.Add(time.Second)
	e.Message = "second failure"
	m.applyEntries([]registry.RegistryEntry{e})
	if got := detailLineText(m.entries[0]); got != "second failure" {
		t.Fatalf("refreshed error line = %q, want the newer settle's", got)
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
