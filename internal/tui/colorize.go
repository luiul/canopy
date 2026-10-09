// State/Since column coloring and the selected row's whole-line
// highlight are built on github.com/luiul/dashkit/loam, the rendering
// substrate this and understory's own internal/tui/colorize.go share
// (see loam's package doc for why post-processing an already-rendered
// bubbles/table view, rather than styling table.Row values directly, is
// necessary at all). This file only holds what's specific to canopy:
// which words map to which color, the Since column's per-state hue, the
// quiet rows' whole-line grey-out, the blink-marker suffix handling,
// and the row highlight's own look. The per-line pass itself moved to
// message.go's tableView when rows gained their injected message detail
// lines (see message.go for why); the detail lines' own tints live
// there too.

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/luiul/dashkit/loam"
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

// sinceStyles is the Since column's palette: the same hues as
// stateStyles (Since is how long the row has been in its current state,
// so the first two columns read as one color block per state, and same-
// state neighbors read as one group) but never bold — bold stays
// reserved for the done/error State words themselves, the same split
// messageStyles keeps for the detail lines. This replaces the constant
// subtle grey the column wore regardless of state, which read as
// "disabled" for no apparent reason: grey now means a quiet state
// (idle, unknown), not "this column is off".
var sinceStyles = map[string]lipgloss.Style{
	"blocked": lipgloss.NewStyle().Foreground(lipgloss.Color("208")),
	"error":   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	"done":    lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	"working": lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
	"idle":    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	"stopped": lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
	"unknown": lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("238")),
}

func sinceStyle(state string) lipgloss.Style {
	if s, ok := sinceStyles[state]; ok {
		return s
	}
	return lipgloss.NewStyle()
}

// NOTE: the per-line recolor pass (State/Since word coloring, quiet-row
// grey-out, cursor highlight, sentinel stripping) is tableView in
// message.go — it gained line-kind awareness (data line vs. injected
// message detail line) when the detail lines were added.

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

// isQuiet reports whether a state belongs to the quiet tier: idle
// (waiting on a prompt) and unknown (no real status) never need
// attention, so their whole data line recedes behind a faint grey-out
// (see dimQuietRow) and the rows that do need you (blocked, error,
// done, working) pop against the grey — the same two-tier split
// understory gets from greying out its parked rows. stopped is
// deliberately NOT quiet: pausing is user-deliberate but easy to
// forget, and a forgotten stopped session is a zombie, so the row keeps
// its full-brightness cyan.
func isQuiet(state string) bool {
	return state == "idle" || state == "unknown"
}

// quietRowStyle is a quiet row's grey-out: the whole line faint, the
// same treatment (and the same reasoning) as understory's
// parkedRowStyle. Faint rather than a grey foreground so the line's
// nested colors (the state-hued State/Since cells) dim with it, the way
// rich's dim tints the spans it wraps — a foreground would leave those
// at full brightness inside the grey.
var quietRowStyle = lipgloss.NewStyle().Faint(true)

// dimQuietRow greys out one already-recolored, already-band-highlighted
// data line of a quiet-state row. It must run after the per-word
// recolors and the selection band, not before: a dimmed-first line
// would be skipped by RecolorWord's ANSI check, while dimming last
// nests the State/Since colors inside the faint instead (the ordering
// understory's greyOutParkedRows documents for the same composition).
//
// The State word is the one exception to the dim: SGR 22 ("normal
// intensity", clearing the faint) is spliced into the word's own
// opening sequence, so the state signal survives the grey-out at full
// brightness — understory keeps its parked word bright inside the dim
// the same way. The word's span is the line's first styled one (State
// is column 0), so the first-occurrence splice lands on it and not on
// the Since cell, which shares the hue for idle/unknown. For "unknown"
// this also drops the word's own faint (it renders 238 at normal
// intensity): the uniform rule is the State word never faints.
// Splicing beats splitting the line into dimmed flanks around the word:
// a selected quiet row's highlight band then keeps flowing behind the
// word instead of breaking for an undimmed gap. lipgloss can't emit SGR
// 22 itself (styles only ever set attributes, never clear them), so
// it's spliced in by hand, once.
func dimQuietRow(line string, state string) string {
	stateOpen, _ := loam.StyleSequences(stateStyle(state))
	line = loam.HighlightRow(line, quietRowStyle)
	if stateOpen != "" {
		line = strings.Replace(line, stateOpen, stateOpen+"\x1b[22m", 1)
	}
	return line
}

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
