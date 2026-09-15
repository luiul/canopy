package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// filterEntries returns two entries distinguishable by the filterable
// text columns (see filterCells): a pi session in dotfiles and a claude
// session in canopy's repo.
func filterEntries() []registry.RegistryEntry {
	a := entry(1, ancestry.Ghostty, "idle")
	b := entry(2, ancestry.Ghostty, "working")
	b.Kind = "claude"
	b.Cwd = "/Users/x/canopy"
	return []registry.RegistryEntry{a, b}
}

// typeRunes feeds each rune of s as its own keypress, the way a terminal
// delivers typed text.
func typeRunes(m Model, s string) Model {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	return m
}

func TestSlashEntersFilterModeAndTypingFiltersRows(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())

	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	if !m.filtering {
		t.Fatal("want filtering=true after /")
	}
	if got := m.footerView(); !strings.Contains(got, "filter> ") {
		t.Fatalf("footer = %q, want the filter input prompt", got)
	}

	m = typeRunes(m, "claude")
	rows := m.table.Rows()
	if len(rows) != 1 || rows[0][colKind] != "claude" {
		t.Fatalf("rows = %v, want only the claude row left", rows)
	}
	if got := m.footerView(); !strings.Contains(got, "filter> claude") {
		t.Fatalf("footer = %q, want the query echoed in the prompt", got)
	}
}

func TestFilterModeSwallowsActionKeys(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)

	// "x" would arm a kill, "q" would quit, were they bindings; mid-filter
	// they are query text.
	m = typeRunes(m, "xq")
	if m.pendingKill.Active() {
		t.Fatal("want x swallowed into the query, not arming a kill prompt")
	}
	if m.quitting {
		t.Fatal("want q swallowed into the query, not quitting")
	}
	if m.filterQuery != "xq" {
		t.Fatalf("query = %q, want %q", m.filterQuery, "xq")
	}
}

func TestEscLeavesFilterModeKeepingTheQueryApplied(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "pi")

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(Model)

	if m.filtering {
		t.Fatal("want filtering=false after esc")
	}
	if len(m.table.Rows()) != 1 {
		t.Fatalf("rows = %v, want the pi filter still applied", m.table.Rows())
	}
	if got := m.footerView(); !strings.Contains(got, "filter: pi") {
		t.Fatalf("footer = %q, want the applied-filter readout", got)
	}
}

func TestEscInNormalModeClearsAnAppliedFilter(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "pi")
	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(Model)

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(Model)

	if m.filterQuery != "" {
		t.Fatalf("query = %q, want cleared", m.filterQuery)
	}
	if len(m.table.Rows()) != 2 {
		t.Fatalf("rows = %v, want both rows back", m.table.Rows())
	}
}

func TestEnterMidFilterStillJumps(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "claude")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("want enter to keep its row action (a jump command) mid-filter")
	}
	if m.filtering {
		t.Fatal("want the filter input left once the jump is underway")
	}
	if m.filterQuery != "claude" || len(m.table.Rows()) != 1 {
		t.Fatalf("query = %q, rows = %v, want the filter still applied", m.filterQuery, m.table.Rows())
	}
}

func TestArrowsStillMoveTheCursorMidFilter(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries()) // working sorts first: claude, then pi
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "s") // both locations contain "s"

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)

	if got, ok := m.selectedEntry(); !ok || got.Pid != 1 {
		t.Fatalf("selected = %+v, want the down arrow to have moved to pid 1 mid-filter", got)
	}
}

func TestPollKeepsTheFilterApplied(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "claude")

	updated, _ = m.Update(pollResultMsg{entries: filterEntries()})
	m = updated.(Model)

	if len(m.entries) != 2 {
		t.Fatalf("entries = %v, want the full set kept underneath the filter", m.entries)
	}
	rows := m.table.Rows()
	if len(rows) != 1 || rows[0][colKind] != "claude" {
		t.Fatalf("rows = %v, want the filter re-applied across the poll", rows)
	}
}

func TestFilterCursorFollowsTheSameEntryWhileTyping(t *testing.T) {
	m := New(999)
	entries := filterEntries()
	c := entry(3, ancestry.Ghostty, "idle")
	c.Cwd = "/Users/x/canopy"
	entries = append(entries, c)
	m.applyEntries(entries) // sorted: pid 2 (working), then pid 1, pid 3

	// Select pid 3 (last row), then filter to the canopy rows: pid 3 is
	// still displayed, so the cursor must follow it rather than staying
	// on row index 2 (which no longer exists).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if got, _ := m.selectedEntry(); got.Pid != 3 {
		t.Fatalf("selected = %+v before filtering, want pid 3", got)
	}

	updated, _ = m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "canopy")

	if got, ok := m.selectedEntry(); !ok || got.Pid != 3 {
		t.Fatalf("selected = %+v after filtering, want the cursor to have followed pid 3", got)
	}
}

func TestFilterPlaceholderNamesTheQueryWhenNothingMatches(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "zzz")

	rows := m.table.Rows()
	if len(rows) != 1 || !strings.Contains(rows[0][colLocation], `no sessions match filter "zzz"`) {
		t.Fatalf("rows = %v, want the no-match placeholder naming the query", rows)
	}
	if _, ok := m.selectedEntry(); ok {
		t.Fatal("want no selectable entry while only the placeholder is showing")
	}
}

func TestCtrlUClearsTheQueryMidFilter(t *testing.T) {
	m := New(999)
	m.applyEntries(filterEntries())
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(Model)
	m = typeRunes(m, "claude")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(Model)

	if !m.filtering {
		t.Fatal("want the input still focused after ctrl+u")
	}
	if m.filterQuery != "" || len(m.table.Rows()) != 2 {
		t.Fatalf("query = %q, rows = %v, want the filter cleared in place", m.filterQuery, m.table.Rows())
	}
}
