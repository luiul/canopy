// The message detail line: rendering each row's optional state payload
// (see registry.RegistryEntry.Message — session name, dialog title, or
// first error line) as a tinted second line directly under its row,
// instead of as another column. Split out of app.go for the same reason
// rows.go and colorize.go are: this is one concern, and it is the only
// place that turns one table row into two terminal lines.
//
// Why a second line and not an 11th column: three long-text columns
// (Message, Location, Model) cannot share one viewport — prototyped with
// realistic data (a 95-cell worktree path, the real model labels, a
// 66-char error line): at 120 cells a Message column crushed Location
// and Model to ~7 cells each and still truncated the error, while the
// detail line shows it in full at any width and costs nothing on rows
// without a message (idle, unknown, unnamed sessions — the common case).
// See https://github.com/luiul/canopy/issues/12.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/luiul/dashkit/loam"
	"github.com/mattn/go-runewidth"

	"github.com/luiul/canopy/internal/registry"
)

// messageIndent leads every detail line: deep enough to read as attached
// to the row above (not a row of its own), shallow enough to leave
// almost the full width for the message itself.
const messageIndent = "  ↳ "

// messageStyles tints a detail line by the state it belongs to: the same
// hues as stateStyles but never bold — bold stays reserved for the
// done/error State words themselves, and a whole line of bold error text
// reads poorly. idle/unknown detail lines (the session's first task, not
// an actionable payload) wear subtleStyle's grey, so they recede along
// with their faint data line (see dimQuietRow) instead of reading white
// under a greyed-out row. stopped is the one state left plain: its row
// stays bright (see isQuiet), so its task line does too.
var messageStyles = map[string]lipgloss.Style{
	"blocked": lipgloss.NewStyle().Foreground(lipgloss.Color("208")),
	"error":   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	"done":    lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	"working": lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
	"idle":    subtleStyle,
	"unknown": subtleStyle,
}

func messageStyle(state string) lipgloss.Style {
	if s, ok := messageStyles[state]; ok {
		return s
	}
	return lipgloss.NewStyle()
}

// sessionName is the row's pi session name when the raw report's message
// carries one — a working/done report's payload — and "" for every other
// state: blocked's dialog title and error's first line describe an
// event, not the session's identity, so they never name a row. The
// kill confirmation prompt uses this to disambiguate its target (see
// killPromptText). "Compacting context" is the extension's fixed working
// payload during compaction (canopy-status.ts), not a name — excluded.
func sessionName(e registry.RegistryEntry) string {
	switch displayState(e) {
	case "working", "done":
		if e.Message != "Compacting context" {
			return e.Message
		}
	}
	return ""
}

// detailLineText is the full text of the detail line under e's row,
// composed per display state from the three optional payloads (see
// registry.RegistryEntry's Message/Detail/Task):
//
//	blocked → the dialog title (message); error → the first error line
//	(message) — both are the actionable content, shown alone.
//	working → "name · activity"; done → "name · outcome" — either part
//	droppable when absent.
//	everything else (idle, stopped, unknown) → the task: the first
//	prompt of the session, the identity an unnamed session otherwise
//	lacks.
//
// Any state whose composed line comes out empty falls back to the task,
// so an unnamed working row before its first tool call still says what
// it's doing. "" overall means no line at all (unknown kinds, or a pi
// that has never been prompted).
func detailLineText(e registry.RegistryEntry) string {
	var line string
	switch displayState(e) {
	case "blocked", "error":
		line = e.Message
	case "working", "done":
		name, detail := e.Message, e.Detail
		switch {
		case name != "" && detail != "":
			line = name + " · " + detail
		case name != "":
			line = name
		default:
			line = detail
		}
	}
	if line == "" {
		line = e.Task
	}
	return line
}

// firstVisibleRow derives which entry index the table's first rendered
// data line shows, from the one landmark bubbles/table v1 exposes in its
// output: the selection sentinel (see loam.Tag). The cursor row is
// meant to be inside the rendered window at all times (see
// ensureCursorVisible), so if the sentinel sits on data line p (0-based
// after the header), the window starts at cursor-p. This is why injected
// detail lines never desync from their rows even when the table is
// scrolled — no mirroring of the viewport's internal scroll state.
// ok=false means no sentinel was rendered: callers must skip message
// work entirely that frame rather than guess a window (a wrong guess
// would glue a detail line onto the wrong row, far worse than a frame
// without one).
func firstVisibleRow(lines []string, cursor int) (first int, ok bool) {
	for j := 1; j < len(lines); j++ {
		if strings.Contains(lines[j], loam.Sentinel) {
			if v := cursor - (j - 1); v >= 0 {
				return v, true
			}
			return 0, true
		}
	}
	return 0, false
}

// ensureCursorVisible scrolls the table until the cursor row is actually
// inside the rendered window (or all rows fit, in which case there is
// nothing to repair). bubbles/table v1 keeps it there on its own
// for keyboard movement (MoveUp/MoveDown adjust the viewport), but not
// for SetCursor: a poll that reorders the selected entry far up or down
// (a row above it newly needing attention, say) restores the cursor to a
// row index the current scroll offset doesn't show — leaving the
// selection highlight silently gone (pre-existing quirk) and, now,
// nothing for firstVisibleRow to anchor to. One down-then-up pair
// re-centers the viewport on the cursor in both directions (via the
// offset clamps inside MoveUp/MoveDown) while returning the cursor to
// its row; the loop is only belt-and-braces against bubbles' case logic.
func (m *Model) ensureCursorVisible() {
	target := m.table.Cursor()
	for range 3 {
		lines := strings.Split(m.table.View(), "\n")
		if len(lines)-1 >= len(m.table.Rows()) {
			return // every row is on screen; nothing to scroll to
		}
		for j := 1; j < len(lines); j++ {
			if strings.Contains(lines[j], loam.Sentinel) {
				return
			}
		}
		m.table.MoveDown(1)
		m.table.MoveUp(1)
		m.table.SetCursor(target)
	}
}

// messageLine builds the one detail line under e's row: the indent in
// subtle, the message in its state's tint (truncated only at the table's
// own right edge — the one place it can still overflow — with the full
// text one enter-jump away), padded to the table width so a selection
// highlight spans the whole band. The line never blinks: blink stays
// confined to the State word's marker (see stateCellText), and a
// blinking paragraph would be hostile.
func messageLine(e registry.RegistryEntry, msg string, selected bool, width int) string {
	text := runewidth.Truncate(msg, max(width-runewidth.StringWidth(messageIndent), 0), "…")
	pad := max(width-runewidth.StringWidth(messageIndent)-runewidth.StringWidth(text), 0)
	line := subtleStyle.Render(messageIndent) +
		messageStyle(displayState(e)).Render(text) +
		strings.Repeat(" ", pad)
	if selected {
		line = loam.HighlightRow(line, rowHighlightStyle)
	}
	return line
}

// tableRenderWidth is the full display width of one rendered row:
// bubbles/table's two padding cells per column plus the column widths.
// Detail lines pad to exactly this so the selected row's highlight band
// (loam.HighlightRow wraps the whole line) reaches the same right edge
// as the padded data rows above and below it.
func tableRenderWidth(cols []table.Column) int {
	w := loam.CellPadding * len(cols)
	for _, c := range cols {
		w += c.Width
	}
	return w
}

// tableView renders the table's body: bubbles/table's own view, with
// each visible row's detail line (if it has one) injected beneath it,
// the State/Since recoloring, quiet-row grey-out, and cursor highlight
// that colorize.go defines applied to the data lines, and the header
// border marks drawn last. Replaces the plain colorizeRows pass View
// used before rows had detail lines; the line-to-entry mapping comes
// from firstVisibleRow, so it stays correct when the table is scrolled
// and when the window holds fewer rows than displayedEntries. Since's
// state hue and the quiet-row grey-out both key off that same mapping;
// on an unmapped frame Since keeps its former constant grey rather than
// flash unstyled for one frame.
func (m Model) tableView() string {
	cols := m.table.Columns()
	lines := strings.Split(m.table.View(), "\n")
	if len(lines) == 0 {
		return ""
	}
	offsets := loam.ColumnOffsets(cols)
	width := tableRenderWidth(cols)
	displayed := m.displayedEntries()
	cursor := m.table.Cursor()
	first, mapped := firstVisibleRow(lines, cursor)

	out := make([]string, 0, len(lines)+len(displayed))
	out = append(out, lines[0]) // the header line is never recolored
	for j := 1; j < len(lines); j++ {
		line := lines[j]
		isSelected := strings.Contains(line, loam.Sentinel)
		// Resolve this data line's entry once: Since's state hue, the
		// quiet row's grey-out, and the detail line below all key off
		// it. haveEntry is false on an unmapped frame (see
		// firstVisibleRow) and on the placeholder row, both of which
		// keep the fallback styling.
		idx := first + j - 1
		haveEntry := mapped && idx >= 0 && idx < len(displayed)
		sinceLookup := func(string) lipgloss.Style { return subtleStyle }
		if haveEntry {
			state := displayState(displayed[idx])
			sinceLookup = func(string) lipgloss.Style { return sinceStyle(state) }
		}
		// Rightmost column first: inserting a style's bytes would shift
		// the start offset of any column to its right.
		line = loam.RecolorWord(line, offsets[colSince], sinceLookup)
		line = loam.RecolorWord(line, offsets[colState], recolorState)
		if isSelected {
			line = loam.HighlightRow(line, rowHighlightStyle)
		}
		// The grey-out goes last: it must nest the State/Since colors
		// (and flow over the selection band), and RecolorWord's column
		// math only works on a still-unstyled line (see dimQuietRow's
		// doc).
		if haveEntry && isQuiet(displayState(displayed[idx])) {
			line = dimQuietRow(line, displayState(displayed[idx]))
		}
		out = append(out, strings.ReplaceAll(line, loam.Sentinel, ""))
		if !mapped {
			continue // no anchor this frame: no detail lines rather than wrong ones
		}
		if haveEntry {
			e := displayed[idx]
			if msg := detailLineText(e); msg != "" {
				out = append(out, messageLine(e, msg, idx == cursor, width))
			}
		}
	}
	return loam.DrawHeaderBorders(strings.Join(out, "\n"), cols, subtleStyle)
}

// visibleMessageLines counts how many detail lines the current render
// would inject: one per visible entry with a display message.
// resizeTableHeight's verify step subtracts exactly this from the
// table's height budget, so the injected lines never push the footer
// off the terminal. When the window can't be mapped (no sentinel this
// frame, see firstVisibleRow) it returns the worst case — every visible
// row could carry a detail line — because the height budget must err on
// the side of too few rows, never too many.
func (m Model) visibleMessageLines() int {
	displayed := m.displayedEntries()
	if len(displayed) == 0 {
		return 0
	}
	lines := strings.Split(m.table.View(), "\n")
	first, ok := firstVisibleRow(lines, m.table.Cursor())
	if !ok {
		return min(countMessages(displayed), m.table.Height())
	}
	count := 0
	for i := first; i < len(displayed) && i < first+m.table.Height(); i++ {
		if detailLineText(displayed[i]) != "" {
			count++
		}
	}
	return count
}

func countMessages(entries []registry.RegistryEntry) int {
	count := 0
	for _, e := range entries {
		if detailLineText(e) != "" {
			count++
		}
	}
	return count
}
