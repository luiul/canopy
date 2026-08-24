package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/luiul/loam"
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

func TestColorizeRowsHighlightsTheWholeCursorTaggedRow(t *testing.T) {
	withForcedColor(t)
	cols := []table.Column{
		{Title: "State", Width: 9},
		{Title: "Since", Width: 6},
	}
	tbl := table.New(table.WithColumns(cols), table.WithHeight(3))
	styles := table.DefaultStyles()
	styles.Selected = lipgloss.NewStyle()
	tbl.SetStyles(styles)
	tbl.SetRows([]table.Row{
		{"idle", "3d"},
		{"working", cursorSentinel + "12s"},
	})

	got := colorizeRows(tbl.View(), tbl.Columns(), 0, 1)
	lines := strings.Split(got, "\n")

	open, closeSeq := loam.StyleSequences(rowHighlightStyle)
	if open == "" {
		t.Fatal("StyleSequences returned no escape codes; withForcedColor isn't taking effect")
	}
	if strings.Contains(lines[1], open) {
		t.Fatalf("got the row highlight on the non-tagged row %q, want it left alone", lines[1])
	}
	if !strings.HasPrefix(lines[2], open) || !strings.HasSuffix(lines[2], closeSeq) {
		t.Fatalf("got tagged row %q, want it wrapped start-to-end in the highlight's open/close sequences", lines[2])
	}
	if strings.Contains(got, cursorSentinel) {
		t.Fatalf("got %q, want cursorSentinel stripped out of the final output entirely", got)
	}
	if want := stateStyle("working").Render("working"); !strings.Contains(lines[2], want) {
		t.Fatalf("got tagged row %q, want it to still contain the styled word %q", lines[2], want)
	}
}
