// State/Since column coloring and the selected row's whole-line
// highlight are both handled by github.com/luiul/loam, the rendering
// substrate this and understory's own internal/tui/colorize.go share
// (see loam's package doc for why post-processing an already-rendered
// bubbles/table view, rather than styling table.Row values directly, is
// necessary at all). This file only holds what's specific to canopy:
// which words map to which color, the blink-marker suffix handling, and
// the row highlight's own look.

package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/luiul/loam"
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

// rowHighlightStyle marks the entire selected row rather than a leading
// marker glyph: same rationale, and the same look, as understory's own
// rowHighlightStyle (see its doc there) — a muted grey background band,
// consistent across both dashboards, rather than the full-invert
// Reverse(true) either tool used to rely on (which also inverted
// State's own color coding on that one row, not just the background).
// AdaptiveColor picks a shade lighter on a light terminal and a shade
// darker on a dark one, rather than a single fixed grey that could wash
// out on one theme or the other.
var rowHighlightStyle = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "254", Dark: "237"})

// recolorState is the State column's WordColumn.Style: it strips a
// trailing blinkMarker before looking up the word's color, then renders
// the *original* word (marker and all, so the marker itself still shows)
// in a reverse-video variant of that color whenever the marker was
// present — in practice only ever seen on a "done" row's visible blink
// phase, but this stays state-word-agnostic since nothing here needs to
// know that.
func recolorState(trimmed string) lipgloss.Style {
	word := strings.TrimSuffix(trimmed, blinkMarker)
	style := stateStyle(word)
	if word != trimmed { // mid-blink, visible phase: word carries the marker
		style = style.Reverse(true)
	}
	return style
}

// colorizeRows recolors the State and Since columns of a table's
// already rendered view and highlights the whole line of whichever row
// carries cursorSentinel (see app.go's doc on it), by delegating
// straight to loam.ColorizeRows. Pass sinceCol < 0 to skip Since
// coloring (e.g. a table built without that column).
func colorizeRows(view string, cols []table.Column, stateCol, sinceCol int) string {
	wordCols := []loam.WordColumn{{Index: stateCol, Style: recolorState}}
	if sinceCol >= 0 {
		wordCols = append(wordCols, loam.WordColumn{
			Index: sinceCol,
			Style: func(string) lipgloss.Style { return subtleStyle },
		})
	}
	return loam.ColorizeRows(view, cols, wordCols, rowHighlightStyle)
}
