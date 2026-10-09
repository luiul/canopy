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

// newSettles returns the keys of the entries in fresh that show a
// genuinely new settle compared to previous: a brand new entry that's
// already done or error the first time canopy sees it, or an existing
// one whose State just flipped into done/error from something else. An
// entry that was already done/error last poll and still is doesn't count
// — otherwise a session sitting done for an hour would ring the bell on
// every single poll for that whole hour, drowning out the one moment
// that actually mattered: the transition itself.
//
// "Still is" is decided by RealStateReportedAt, not the State string:
// canopy-status.ts writes done/error exactly once per settle and
// pistatus keeps returning that one-shot write indefinitely (done/error
// are exempt from pistatus.MaxAge), so a second turn that starts and
// settles again without canopy ever sampling a "working" poll in between
// reads the same literal string on both polls. pistatus's own write
// timestamp advancing is the one signal that tells that second settle
// apart from the exact same still-fresh write repeating.
//
// done and error are the only states this checks: blocked gets its own
// debounced check below (newlyBlocked), and working/idle/stopped/unknown
// are never worth ringing a bell over. The caller both rings the bell
// and starts the rows' blink bursts (see startBlinks) for the returned
// keys.
func newSettles(previous, fresh []registry.RegistryEntry) []string {
	prevByKey := make(map[string]registry.RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	var keys []string
	for _, f := range fresh {
		if !isAttention(f.State) {
			continue
		}
		if was, ok := prevByKey[f.Key()]; ok && isAttention(was.State) {
			if !f.RealState || f.RealStateReportedAt.Equal(was.RealStateReportedAt) {
				continue // the exact same settle we already rang for
			}
		}
		keys = append(keys, f.Key())
	}
	return keys
}

// newlyBlocked returns the keys that newly deserve a ring for being
// "blocked": raw State reading blocked on *both* sides of this poll (the
// debounce — a dialog the user answers within one poll interval, the
// common case when they're sitting in that very terminal, never rings)
// and not already rung for this blocked spell (one ring per spell, not
// one per poll for as long as the dialog stays open). The caller adds the
// returned keys to its rung set and prunes keys whose fresh State has
// left blocked, so a later, separate dialog rings again.
func newlyBlocked(previous, fresh []registry.RegistryEntry, rung map[string]bool) []string {
	prevByKey := make(map[string]registry.RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	var keys []string
	for _, f := range fresh {
		if f.State != "blocked" || rung[f.Key()] {
			continue
		}
		if was, ok := prevByKey[f.Key()]; ok && was.State == "blocked" {
			keys = append(keys, f.Key())
		}
	}
	return keys
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
