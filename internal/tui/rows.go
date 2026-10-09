// Row/summary rendering: sorting entries by urgency, building the table's
// plain-text rows, and the header's per-state summary line. Split out of
// app.go; see app.go's package doc for the full file layout.
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/loam"
)

var surfaceLabels = map[ancestry.Surface]string{
	ancestry.VSCode:  "VS Code",
	ancestry.Ghostty: "Ghostty",
	ancestry.Unknown: "unknown",
}

func location(e registry.RegistryEntry, home string) string {
	if e.Cwd == "" {
		return "?"
	}
	return shortenHome(e.Cwd, home)
}

// shortenHome replaces a leading home-directory prefix with "~", the same
// shorthand every shell prompt uses, so Location has more room left over
// for the part of the path that actually varies row to row.
func shortenHome(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// locationCellText is the Location column's cell: the path pre-truncated
// to the column's current width *keeping the tail* ("…speed-up-ci/global-ops"
// rather than bubbles/table's own head-keeping "~/worktrees/hello…").
// The head is the least varying part across rows — on this machine nearly
// every session lives under ~/worktrees or ~/projects — while the tail is
// what actually identifies the session, so the tail is what survives a
// tight column. Pre-truncating (rather than letting the table truncate at
// render) is what makes the cut point controllable; the width comes from
// the live column via buildRows, rebuilt on every poll, resize, and drag,
// and the filter still matches the full untruncated path (see filterCells).
func locationCellText(e registry.RegistryEntry, home string, width int) string {
	return loam.TruncateHead(location(e, home), width)
}

// statePriority ranks states by how much attention they need: blocked
// (pi is waiting on you in a dialog, mid-run or not) ranks highest, then
// error (a turn failed, needs a look) and done (finished, ready to
// check), then working (busy, nothing for you to do), then idle, then
// stopped (paused via the p keybind — user-deliberate, so it needs no
// attention, but it shouldn't scatter to the bottom either), then unknown
// (no real status: extension missing/stale, or not pi).
var statePriority = map[string]int{
	"blocked": 0,
	"error":   1,
	"done":    2,
	"working": 3,
	"idle":    4,
	"stopped": 5,
	"unknown": 6,
}

// stateOrder is statePriority's states in display order, used for the
// header's per-state summary counts too.
var stateOrder = []string{"blocked", "error", "done", "working", "idle", "stopped", "unknown"}

func statePriorityOf(state string) int {
	if p, ok := statePriority[state]; ok {
		return p
	}
	return len(statePriority) // a state outside the known vocabulary sorts last
}

// displayState is the state actually shown for e: the raw State, with
// exactly one overlay — a stopped process (SIGSTOP, e.g. via the p
// keybind) reports the synthetic "stopped", which the raw State can't
// express at all (pistatus keeps reporting whatever pi last wrote,
// paused or not). There is deliberately no sticky done/error treatment:
// the raw signal already moves on the moment the session does anything
// else (a fresh turn reads working, Escape reads idle), so acting on the
// session needs no separate canopy-side acknowledgment. Sorting,
// coloring, the summary line, and the State/Since cells all go through
// this instead of e.State directly.
func displayState(e registry.RegistryEntry) string {
	if e.Stopped {
		return "stopped"
	}
	return e.State
}

// sortEntries orders entries by statePriority, most actionable first, then
// grouped by surface, then stable by pid. Ranks by displayState, so a
// paused row sorts as stopped rather than by the state pistatus last
// reported for it.
func sortEntries(entries []registry.RegistryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if pa, pb := statePriorityOf(displayState(a)), statePriorityOf(displayState(b)); pa != pb {
			return pa < pb
		}
		if a.Surface != b.Surface {
			return a.Surface < b.Surface
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Pid < b.Pid
	})
}

// refreshCursorTag rebuilds the table's rows so the cursor's Since cell
// carries cursorSentinel immediately after it moves (arrow keys, page up/down,
// etc.), instead of waiting for the next poll. Also reused by tickBlinks
// (blink.go) to render each on/off flip of a blink burst immediately, for
// the same reason: don't wait for the next poll to show it. Cursor
// movement changes WHICH rows are visible, hence which message detail
// lines render, so the height must be re-fit here too (see
// resizeTableHeight) — otherwise scrolling a messaged row into view
// would push the footer off the terminal.
func (m *Model) refreshCursorTag() {
	displayed := m.displayedEntries()
	if len(displayed) == 0 {
		return
	}
	m.table.SetRows(buildRows(displayed, m.table.Cursor(), m.home, m.locationWidth(), time.Now(), m.blinks, m.filterQuery))
	m.ensureCursorVisible()
	m.resizeTableHeight()
}

// filterCells are the cell strings a filterQuery is matched against (see
// github.com/luiul/dashkit/sieve): the row's stable text columns, which
// are State's display word, Surface, Location (the full path, not the
// tail-truncated cell), Kind, PID, and the composed detail line (so
// "/rate limit" finds the throttled row and "/go test" the row running
// it). The volatile columns (Since, CPU, RAM, Uptime) are deliberately
// excluded: their values tick over under the user's fingers, so a row
// would match-or-not from one poll to the next for reasons invisible in
// the query.
func filterCells(e registry.RegistryEntry, home string) []string {
	return []string{
		displayState(e),
		surfaceLabel(e.Surface),
		location(e, home),
		e.Kind,
		fmt.Sprintf("%d", e.Pid),
		detailLineText(e),
	}
}

// buildRows constructs the table's rows from already-sorted entries.
// cursor picks which row's Since cell gets tagged with cursorSentinel (see
// loam's doc); it's a plain parameter (rather than read from the table
// itself) so this same helper builds rows both right after a poll
// (applyEntries) and on every cursor move in between polls (refreshCursorTag),
// so the tag tracks the highlighted row immediately rather than only once
// every poll interval. locationWidth is the Location column's current
// width, for locationCellText's tail-keeping pre-truncation.
func buildRows(entries []registry.RegistryEntry, cursor int, home string, locationWidth int, now time.Time, blinks map[string]time.Time, filterQuery string) []table.Row {
	if len(entries) == 0 {
		// Keep empty-state messages in Location, next to the session paths.
		// An active filter says why no rows remain and how to clear it.
		placeholder := table.Row{"", "", "", "", "", "", "", "", "", ""}
		if filterQuery != "" {
			placeholder[colLocation] = fmt.Sprintf("no sessions match filter %q (esc clears)", filterQuery)
		} else {
			placeholder[colLocation] = "no known agent-kind processes found on this machine"
		}
		return []table.Row{placeholder}
	}
	rows := make([]table.Row, len(entries))
	for i, e := range entries {
		rows[i] = table.Row{
			stateCellText(e, now, blinks),
			loam.Tag(sinceCellText(e, now), i == cursor),
			e.Kind,
			surfaceLabel(e.Surface),
			locationCellText(e, home, locationWidth),
			modelCellText(e),
			cpuCellText(e),
			ramCellText(e),
			uptimeCellText(e),
			fmt.Sprintf("%d", e.Pid),
		}
	}
	return rows
}

// modelCellText displays pi's selected model and provider. Unknown models
// (including all other agent kinds) use the same single-cell placeholder.
func modelCellText(e registry.RegistryEntry) string {
	if e.ModelName == "" || e.ModelProvider == "" {
		return "—"
	}
	return fmt.Sprintf("%s [%s]", e.ModelName, e.ModelProvider)
}

// stateCellText is the State column's plain-text cell value:
// displayState's word, with a trailing blinkMarker whenever the row has a
// blink burst currently running and in its visible ("on") half (see
// blinkActive/blinkOn in blink.go — toggles on and off as the burst
// runs). done and error are the only states with any attention-getting
// treatment at all; every other word (including "unknown") renders as-is.
// Actual coloring/reverse-video happens later, in View, by
// post-processing the rendered table (see colorize.go).
func stateCellText(e registry.RegistryEntry, now time.Time, blinks map[string]time.Time) string {
	word := displayState(e)
	if start, ok := blinks[e.Key()]; ok && isAttention(word) && blinkActive(start, now) && blinkOn(start, now) {
		return word + blinkMarker
	}
	return word
}

// sinceCellText is the Since column's plain-text cell value: how long the
// entry has been in its current state, or "" if that's not known yet (a
// StateSince hasn't been stamped, e.g. in tests that build entries by
// hand).
func sinceCellText(e registry.RegistryEntry, now time.Time) string {
	if e.StateSince.IsZero() {
		return ""
	}
	return humanizeSince(now.Sub(e.StateSince))
}

// summaryLine is a one-line "N sessions: N done · N working · ..."
// breakdown, colored to match the State column and ordered the same way
// (most actionable first), skipping any state with a zero count. Empty
// when there are no entries, since the placeholder row already says so.
func summaryLine(entries []registry.RegistryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[displayState(e)]++
	}

	parts := make([]string, 0, len(stateOrder))
	seen := map[string]bool{}
	for _, s := range stateOrder {
		if n := counts[s]; n > 0 {
			parts = append(parts, stateStyle(s).Render(fmt.Sprintf("%d %s", n, s)))
			seen[s] = true
		}
	}
	// A state outside the known vocabulary shouldn't happen, but this keeps
	// its count from silently vanishing from the summary if one ever shows
	// up.
	for s, n := range counts {
		if !seen[s] {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}

	label := "sessions"
	if len(entries) == 1 {
		label = "session"
	}
	return subtleStyle.Render(fmt.Sprintf("%d %s: ", len(entries), label)) +
		strings.Join(parts, subtleStyle.Render(" · "))
}

func surfaceLabel(s ancestry.Surface) string {
	if label, ok := surfaceLabels[s]; ok {
		return label
	}
	return string(s)
}
