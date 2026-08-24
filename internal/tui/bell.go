// The bell decision: whether a poll's fresh entries introduce a row that
// newly needs attention, and the terminal-bell side effect itself. Split
// out of app.go; see app.go's package doc for the full file layout.
package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/registry"
)

// needsBell reports whether any entry in fresh newly needs attention
// compared to previous: either a brand new entry that's already done the
// first time canopy sees it, or an existing one whose State just flipped
// into done from something else. An entry that was already done last poll
// and still is doesn't re-trigger it — otherwise a session sitting done
// for an hour would ring the bell on every single poll for that whole
// hour, drowning out the one moment that actually mattered: the
// transition itself. "Still is" means either it's the exact same settle
// (for a RealState entry, RealStateReportedAt hasn't advanced — the raw
// source hasn't written anything new), or it's a genuinely new settle that
// landed on an episode still open in done (unacknowledged): updateDoneTracking
// silently absorbs that second settle into the same still-open episode
// rather than opening a new one, since the row is already displaying "done"
// and nothing changes on screen for it — ringing again there would be
// exactly the drowning-out this skip exists to avoid, just triggered by a
// second settle instead of a poll timer. Only a settle that's both new
// (RealStateReportedAt advanced) *and* lands on an already-acknowledged
// episode (the reopen case in updateDoneTracking) is a fresh transition
// worth ringing for — see updateDoneTracking's doc comment (done.go) for
// the full rationale. done must be the caller's Model.done as it stood
// *before* this poll's updateDoneTracking call, so "still open" reflects
// the previous poll's episode state, not this one's. done is the only
// state this checks: there is no "blocked" in canopy's vocabulary (see
// docs/agent-state-machine.md), and working/idle/unknown are never worth
// ringing a bell over.
func needsBell(previous, fresh []registry.RegistryEntry, done map[string]doneEpisode) bool {
	prevByKey := make(map[string]registry.RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	for _, f := range fresh {
		if f.State != "done" {
			continue
		}
		if was, ok := prevByKey[f.Key()]; ok && was.State == "done" {
			sameWrite := !f.RealState || f.RealStateReportedAt.Equal(was.RealStateReportedAt)
			ep, tracked := done[f.Key()]
			stillOpen := tracked && ep.Acked.IsZero()
			if sameWrite || stillOpen {
				continue // the exact same settle we already rang for, or one already-flagged and unacknowledged
			}
		}
		return true
	}
	return false
}

// bellCmd rings the terminal bell (ASCII BEL, \a) via stderr rather than
// stdout: bubbletea's renderer owns stdout (alt-screen frames get written
// there on its own schedule), so writing there too risks an interleaved
// write landing mid-escape-sequence on unlucky timing. stderr is a separate
// file descriptor pointed at the same tty, so the terminal still receives
// and acts on the byte — a dock bounce, a tab badge, an audible beep,
// whatever that terminal's own bell preference is set to — without
// touching the renderer's channel. This is the one signal in canopy that
// reaches you even if canopy's own pane isn't the one you're looking at,
// which a color change or blink (colorize.go) by definition cannot.
func bellCmd() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stderr, "\a")
		return nil
	}
}
