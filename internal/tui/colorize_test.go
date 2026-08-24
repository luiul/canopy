package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withForcedColor forces lipgloss to emit real ANSI (tests otherwise run
// with stdout not a tty, which lipgloss auto-detects and downgrades to no
// color), restoring the original profile afterward so this doesn't leak
// into other tests.
func withForcedColor(t *testing.T) {
	t.Helper()
	original := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(original) })
}

func TestColumnOffsetsAccountForOneSpacePaddingOnBothSidesOfEachCell(t *testing.T) {
	cols := []table.Column{{Title: "A", Width: 3}, {Title: "B", Width: 5}}

	offsets := columnOffsets(cols)

	if offsets[0] != (colOffset{start: 1, width: 3}) {
		t.Fatalf("got %+v, want start=1 width=3", offsets[0])
	}
	// 1 (leading pad) + 3 (A) + 2 (A's trailing pad + B's leading pad) = 6
	if offsets[1] != (colOffset{start: 6, width: 5}) {
		t.Fatalf("got %+v, want start=6 width=5", offsets[1])
	}
}

func TestColorizeRowsAppliesTheStateStyleToAnUnselectedRow(t *testing.T) {
	withForcedColor(t)
	cols := []table.Column{
		{Title: "Kind", Width: 4},
		{Title: "State", Width: 9},
	}
	tbl := table.New(table.WithColumns(cols), table.WithHeight(3))
	tbl.SetRows([]table.Row{{"pi", "working"}, {"pi", "done"}})
	tbl.SetCursor(0) // row 0 selected; row 1 (the one we check) is not

	got := colorizeRows(tbl.View(), tbl.Columns(), 1, -1)

	want := stateStyle("done").Render("done")
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want it to contain the styled word %q", got, want)
	}
}

func TestColorizeRowsSkipsTheCurrentlySelectedRow(t *testing.T) {
	withForcedColor(t)
	cols := []table.Column{
		{Title: "Kind", Width: 4},
		{Title: "State", Width: 9},
	}
	tbl := table.New(table.WithColumns(cols), table.WithHeight(3))
	tbl.SetRows([]table.Row{{"pi", "done"}})
	tbl.SetCursor(0)

	rendered := tbl.View()
	got := colorizeRows(rendered, tbl.Columns(), 1, -1)

	// The selected row is already wrapped whole in the table's own Selected
	// style; colorizeRows must leave that line untouched rather than
	// recolor a sub-span of it (which would inject a reset that cuts the
	// outer highlight short).
	if got != rendered {
		t.Fatalf("got a modified selected row:\n%q\nwant it unchanged from:\n%q", got, rendered)
	}
}

func TestColorizeRowsBlinksAJustTransitionedRowInReverseVideo(t *testing.T) {
	withForcedColor(t)
	cols := []table.Column{
		{Title: "Kind", Width: 4},
		{Title: "State", Width: 9},
	}
	tbl := table.New(table.WithColumns(cols), table.WithHeight(3))
	tbl.SetRows([]table.Row{{"pi", "working"}, {"pi", "done" + blinkMarker}})
	tbl.SetCursor(0) // row 0 selected; row 1 (the blinking one) is not

	got := colorizeRows(tbl.View(), tbl.Columns(), 1, -1)

	want := stateStyle("done").Reverse(true).Render("done" + blinkMarker)
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want it to contain the reverse-video blink %q", got, want)
	}
}
