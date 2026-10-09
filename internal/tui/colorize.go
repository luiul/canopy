// State/Since column coloring and the selected row's whole-line
// highlight are built on github.com/luiul/dashkit/loam, the rendering
// substrate this and understory's own internal/tui/colorize.go share
// (see loam's package doc for why post-processing an already-rendered
// bubbles/table view, rather than styling table.Row values directly, is
// necessary at all). This file only holds what's specific to canopy:
// which words map to which color, the blink-marker suffix handling, and
// the row highlight's own look. The per-line pass itself moved to
// message.go's tableView when rows gained their injected message detail
// lines (see message.go for why); the detail lines' own tints live
// there too.

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// blinkMarker is appended, as plain text, to a done/error State cell's
// value whenever that row is mid-blink-burst and on its visible ("on")
// phase (see stateCellText in rows.go for exactly when that applies —
// done and error are the only states with any attention-getting treatment
// at all). It's a real, visible character rather than just an ANSI
// signal, so blinking still reads under --no-color: the marker itself
// appears and disappears between redraws.
const blinkMarker = "*"

var stateStyles = map[string]lipgloss.Style{
	"blocked": lipgloss.NewStyle().Foreground(lipgloss.Color("208")),             // waiting on you in a dialog, mid-run or not
	"error":   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),    // turn failed, needs a look
	"done":    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")),   // finished, ready to check
	"working": lipgloss.NewStyle().Foreground(lipgloss.Color("11")),              // busy, nothing for you to do yet
	"idle":    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),             // waiting on a prompt
	"stopped": lipgloss.NewStyle().Foreground(lipgloss.Color("14")),              // paused via SIGSTOP (the p keybind)
	"unknown": lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("238")), // no real status (extension missing/stale, or not pi)
}

func stateStyle(word string) lipgloss.Style {
	if s, ok := stateStyles[word]; ok {
		return s
	}
	return lipgloss.NewStyle()
}

// NOTE: the per-line recolor pass (State/Since word coloring, cursor
// highlight, sentinel stripping) is tableView in message.go — it gained
// line-kind awareness (data line vs. injected message detail line) when
// the detail lines were added.

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
// present — in practice only ever seen on a done/error row's visible
// blink phase, but this stays state-word-agnostic since nothing here
// needs to know that.
func recolorState(trimmed string) lipgloss.Style {
	word := strings.TrimSuffix(trimmed, blinkMarker)
	style := stateStyle(word)
	if word != trimmed { // mid-blink, visible phase: word carries the marker
		style = style.Reverse(true)
	}
	return style
}
