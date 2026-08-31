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

// statePriority ranks states by how much attention they need: done
// (finished, ready to check) ranks highest, then working (busy, nothing
// for you to do), then idle, then stopped (paused via the p keybind —
// user-deliberate, so it needs no attention, but it shouldn't scatter to
// the bottom either), then unknown (heuristic couldn't tell). There
// is deliberately no "blocked" entry: nothing in canopy ever produces that
// state (see docs/agent-state-machine.md), so it isn't part of the
// vocabulary here either.
var statePriority = map[string]int{
	"done":    0,
	"working": 1,
	"idle":    2,
	"stopped": 3,
	"unknown": 4,
}

// stateOrder is statePriority's states in display order, used for the
// header's per-state summary counts too.
var stateOrder = []string{"done", "working", "idle", "stopped", "unknown"}

func statePriorityOf(state string) int {
	if p, ok := statePriority[state]; ok {
		return p
	}
	return len(statePriority) // a state outside the known vocabulary sorts last
}

// sortEntries orders entries by statePriority, most actionable first, then
// grouped by surface, then stable by pid. Ranks by displayState (which
// folds in done), not the raw State, so an acknowledged done row sorts
// back down with the rest of idle rather than staying pinned at the top.
func sortEntries(entries []registry.RegistryEntry, done map[string]doneEpisode) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if pa, pb := statePriorityOf(displayState(a, done)), statePriorityOf(displayState(b, done)); pa != pb {
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
// etc.), instead of waiting for the next poll. Also reused by acknowledge
// (done.go) to reflect a dismissal immediately, for the same reason: don't
// wait for the next poll to show it.
func (m *Model) refreshCursorTag() {
	if len(m.entries) == 0 {
		return
	}
	m.table.SetRows(buildRows(m.entries, m.table.Cursor(), m.home, time.Now(), m.done))
}

// buildRows constructs the table's rows from already-sorted entries.
// cursor picks which row's Since cell gets tagged with cursorSentinel (see
// loam's doc); it's a plain parameter (rather than read from the table
// itself) so this same helper builds rows both right after a poll
// (applyEntries) and on every cursor move in between polls (refreshCursorTag),
// so the tag tracks the highlighted row immediately rather than only once
// every poll interval.
func buildRows(entries []registry.RegistryEntry, cursor int, home string, now time.Time, done map[string]doneEpisode) []table.Row {
	if len(entries) == 0 {
		// Placeholder message goes in Location: the widest column, and the
		// only one guaranteed to have room for it regardless of terminal width.
		placeholder := table.Row{"", "", "", "", "", "", "", "", ""}
		placeholder[colLocation] = "no known agent-kind processes found on this machine"
		return []table.Row{placeholder}
	}
	rows := make([]table.Row, len(entries))
	for i, e := range entries {
		rows[i] = table.Row{
			stateCellText(e, now, done),
			loam.Tag(sinceCellText(e, now, done), i == cursor),
			surfaceLabel(e.Surface),
			location(e, home),
			cpuCellText(e),
			ramCellText(e),
			uptimeCellText(e),
			e.Kind,
			fmt.Sprintf("%d", e.Pid),
		}
	}
	return rows
}

// stateCellText is the State column's plain-text cell value: displayState's
// word (see displayState — "idle" rather than "done" once acknowledged),
// with a trailing blinkMarker whenever it's a "done" row currently
// mid-blink-burst and in its visible ("on") half (see blinkActive/blinkOn
// in done.go — toggles on and off as the burst runs). done is the only
// state with any attention-getting treatment at all; every other word
// (including "unknown") renders as-is. Actual coloring/reverse-video
// happens later, in View, by post-processing the rendered table (see
// colorize.go).
func stateCellText(e registry.RegistryEntry, now time.Time, done map[string]doneEpisode) string {
	word := displayState(e, done)
	if ep, ok := done[e.Key()]; word == "done" && ok && blinkActive(ep, now) && blinkOn(ep, now) {
		return word + blinkMarker
	}
	return word
}

// sinceCellText is the Since column's plain-text cell value: how long the
// entry has been in its current state, or "" if that's not known yet (a
// StateSince hasn't been stamped, e.g. in tests that build entries by
// hand). Two special cases, both driven by done rather than e.StateSince
// directly:
//   - an *open* (unacknowledged) episode reports since its own Since (when
//     updateDoneTracking first saw it go done), not e.StateSince, which the
//     raw source may have already overwritten with some later transition
//     of its own.
//   - an *acknowledged* episode whose raw State is still literally "done"
//     reports since it was acknowledged, not since the underlying source
//     originally went done — otherwise a row sitting done for an hour
//     before you dismissed it would misleadingly read as "idle 1h" the
//     instant you did.
func sinceCellText(e registry.RegistryEntry, now time.Time, done map[string]doneEpisode) string {
	if ep, ok := done[e.Key()]; ok {
		if ep.Acked.IsZero() {
			return humanizeSince(now.Sub(ep.Since))
		}
		if e.State == "done" {
			return humanizeSince(now.Sub(ep.Acked))
		}
	}
	if e.StateSince.IsZero() {
		return ""
	}
	return humanizeSince(now.Sub(e.StateSince))
}

// summaryLine is a one-line "N sessions: N done · N working · ..."
// breakdown, colored to match the State column and ordered the same way
// (most actionable first), skipping any state with a zero count. Empty
// when there are no entries, since the placeholder row already says so.
func summaryLine(entries []registry.RegistryEntry, done map[string]doneEpisode) string {
	if len(entries) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[displayState(e, done)]++
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
