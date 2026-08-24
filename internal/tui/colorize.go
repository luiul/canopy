// Coloring the State and Since columns cannot be done by putting
// ANSI-styled strings directly into table.Row values: bubbles/table v1's
// cell truncation (runewidth.Truncate) is not ANSI-aware, so escape codes
// get counted as extra visible width and sliced mid-sequence, corrupting
// the row (verified empirically against bubbles/table v1.0.0: a styled
// "working" in a 6-wide column gets truncated to "wor…" with a dangling
// escape code). Post-processing the table's already-rendered plain-text
// view instead sidesteps that entirely: the widths/padding/truncation the
// table computes are always over plain text, and only the final display
// string gets colored.

package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

// blinkMarker is appended, as plain text, to a "done" State cell's value
// whenever that row is mid-blink-burst and on its visible ("on") phase
// (see stateCellText in app.go for exactly when that applies — done is
// the only state with any attention-getting treatment at all). It's a
// real, visible character rather than just an ANSI signal, so blinking
// still reads under --no-color: the marker itself appears and disappears
// between redraws.
const blinkMarker = "*"

var stateStyles = map[string]lipgloss.Style{
	"done":    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")),   // finished, ready to check
	"working": lipgloss.NewStyle().Foreground(lipgloss.Color("11")),              // busy, nothing for you to do yet
	"idle":    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),             // waiting on a prompt
	"unknown": lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("238")), // heuristic couldn't tell
}

func stateStyle(word string) lipgloss.Style {
	if s, ok := stateStyles[word]; ok {
		return s
	}
	return lipgloss.NewStyle()
}

// colOffset is a column's start position and width within a rendered row
// line, accounting for bubbles/table's fixed 1-space padding on both sides
// of every cell (table.DefaultStyles()'s Cell/Header Padding(0, 1)).
type colOffset struct {
	start, width int
}

// columnOffsets computes each column's start/width within a rendered line,
// given cols in the same order the table was built with. Only correct for
// bubbles/table's default (no border) layout, which is all canopy uses.
func columnOffsets(cols []table.Column) []colOffset {
	offsets := make([]colOffset, len(cols))
	pos := 1 // leading pad of the first cell
	for i, c := range cols {
		offsets[i] = colOffset{start: pos, width: c.Width}
		pos += c.Width + 2 // this cell's trailing pad + the next cell's leading pad
	}
	return offsets
}

// colorizeRows recolors the State and Since columns of a table's already
// rendered view. cols must be the exact columns the view was rendered
// with; stateCol/sinceCol are indexes into cols. Pass sinceCol < 0 to skip
// Since coloring (e.g. a table built without that column).
//
// The header line and any line that already contains an escape sequence
// (only the single currently-selected row, wrapped whole by the table's
// own Selected style) are left untouched: recoloring a sub-span of a line
// that already carries its own color would inject a reset code that cuts
// the outer style short for the rest of that line. The selected row is
// already visually distinct via that highlight, so skipping it here costs
// nothing.
func colorizeRows(view string, cols []table.Column, stateCol, sinceCol int) string {
	if stateCol >= len(cols) || sinceCol >= len(cols) {
		return view
	}
	offsets := columnOffsets(cols)
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if i == 0 || strings.Contains(line, "\x1b") {
			continue
		}
		// Rightmost column first: recoloring inserts bytes, which would
		// shift the start offset of any column to its right if done first.
		if sinceCol >= 0 {
			line = recolor(line, offsets[sinceCol], subtleStyle)
		}
		line = recolorState(line, offsets[stateCol])
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// recolor wraps the fixed-width slice of line at off in style, preserving
// line's total length. Assumes line is plain ASCII at and before off (true
// for canopy's Kind/PID/Surface/State/Since columns).
func recolor(line string, off colOffset, style lipgloss.Style) string {
	if off.start+off.width > len(line) {
		return line
	}
	slice := line[off.start : off.start+off.width]
	return line[:off.start] + style.Render(slice) + line[off.start+off.width:]
}

// recolorState is recolor specialized for the State column: the style
// depends on which state word the slice holds, and a trailing blinkMarker
// (still shown, marker and all) gets a reverse-video variant of that
// state's color for extra pop — in practice only ever seen on a "done"
// row's visible blink phase (see stateCellText in app.go), but this stays
// state-word-agnostic since nothing here needs to know that.
func recolorState(line string, off colOffset) string {
	if off.start+off.width > len(line) {
		return line
	}
	slice := line[off.start : off.start+off.width]
	trimmed := strings.TrimRight(slice, " ")
	word := strings.TrimSuffix(trimmed, blinkMarker)
	if word == "" {
		return line // blank filler row below the real data, or the placeholder row
	}
	style := stateStyle(word)
	if trimmed != word { // mid-blink, visible phase: word carries the marker
		style = style.Reverse(true)
	}
	pad := strings.Repeat(" ", len(slice)-len(trimmed))
	return line[:off.start] + style.Render(trimmed) + pad + line[off.start+off.width:]
}
