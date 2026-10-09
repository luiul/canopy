// The settle blink animation: a short on/off blink a row's State word
// shows the moment a poll sees a genuinely new settle (raw State flipped
// to done/error — see newSettles in bell.go), so a freshly finished row
// is hard to miss while the bell rings. Purely visual: the displayed
// state itself is always the raw signal (see displayState in rows.go),
// and the bell decision lives in bell.go. Split out of app.go, which
// otherwise mixed this with the Bubble Tea plumbing itself; see app.go's
// package doc for the full file layout.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/registry"
)

// isAttention reports whether state is one of the two terminal states a
// settle lands in: "done" (a turn finished) or "error" (a turn ended with
// an unretried error). Both come straight from pi's own program-status
// semantics (pi v1.1.0, mirrored by canopy-status.ts); "blocked" is
// deliberately not among them — it is transient by definition (it clears
// the moment the dialog closes), so there is nothing to signal about.
func isAttention(state string) bool {
	return state == "done" || state == "error"
}

// blinkToggleInterval is how long each on/off phase of a blink lasts:
// short enough to read as genuinely blinking rather than one long flash,
// long enough to still be legible.
const blinkToggleInterval = 300 * time.Millisecond

// blinkPhases is how many on/off phases make up one blink burst (3 full
// on-off blinks) — unmistakable that a row just went done/error, without
// blinking so long it turns into noise.
const blinkPhases = 6

// blinkBurstDuration is how long a single blink burst runs before
// settling back to a steady (still colored, just no longer toggling)
// attention cell. One burst per settle, no repeating reminders: with the
// display following the raw signal there is nothing left to remind
// about — a row still reading done an hour later is simply the truth.
const blinkBurstDuration = blinkPhases * blinkToggleInterval

// blinkTickInterval drives the animation's own redraw cadence while a
// burst is active: a few samples per blinkToggleInterval, so no on/off
// transition is ever skipped over between two redraws purely by unlucky
// sampling phase. Independent of, and much shorter than, the dashboard's
// own poll interval.
const blinkTickInterval = blinkToggleInterval / 3

// blinkTickMsg is the animation frame for an in-progress blink burst (see
// tickBlinks/blinkTickCmd): fired every blinkTickInterval, much faster
// than the dashboard's own poll tick, for exactly as long as some entry
// is still mid-burst.
type blinkTickMsg struct{}

// blinkActive reports whether a burst started at start is still running
// at now.
func blinkActive(start, now time.Time) bool {
	return !start.IsZero() && now.Sub(start) < blinkBurstDuration
}

// blinkOn reports which half of the current on/off toggle a still-active
// burst is in at now: true for the visible ("on") half, alternating every
// blinkToggleInterval since start. Only meaningful once blinkActive has
// already reported true for the same start/now — callers check that
// first.
func blinkOn(start, now time.Time) bool {
	return (now.Sub(start)/blinkToggleInterval)%2 == 0
}

// startBlinks opens a fresh blink burst — right now — for every key in
// keys (the genuinely new settles this poll, see newSettles). Re-seeding
// a key whose previous burst already expired just restarts the animation,
// which is exactly right: a new settle deserves a new burst.
func (m *Model) startBlinks(keys []string, now time.Time) {
	if len(keys) == 0 {
		return
	}
	if m.blinks == nil {
		m.blinks = map[string]time.Time{}
	}
	for _, key := range keys {
		m.blinks[key] = now
	}
}

// pruneBlinks drops bursts for keys no longer present in fresh (the
// session ended): the map would otherwise grow by one dead entry per
// settled session forever. Bursts whose raw state moved on (working
// again) need no pruning: blinkActive's time window expires on its own,
// and stateCellText only ever marks an attention word anyway.
func (m *Model) pruneBlinks(fresh []registry.RegistryEntry) {
	if len(m.blinks) == 0 {
		return
	}
	present := make(map[string]bool, len(fresh))
	for _, e := range fresh {
		present[e.Key()] = true
	}
	for key := range m.blinks {
		if !present[key] {
			delete(m.blinks, key)
		}
	}
}

// anyBlinkActive reports whether at least one burst in m.blinks is
// mid-burst right now — what tickBlinks uses to decide whether the
// animation needs another frame. Once every burst has settled, there is
// nothing left to redraw until the next settle starts one, which the
// regular poll path handles on its own (see Update's pollResultMsg case)
// without any dedicated long-duration timer for it.
func (m Model) anyBlinkActive(now time.Time) bool {
	for _, start := range m.blinks {
		if blinkActive(start, now) {
			return true
		}
	}
	return false
}

// tickBlinks rebuilds the table's rows so any on/off change actually
// renders, and returns a command to redraw again shortly if a burst is
// still running — nil once every burst has settled.
//
// A no-op, with no row rebuild at all, when m.blinks is empty: the common
// case on most polls (nothing recently settled), where there is by
// definition nothing that could be blinking and so nothing that needs
// re-rendering on top of the rows applyEntries just built.
func (m *Model) tickBlinks(now time.Time) tea.Cmd {
	if len(m.blinks) == 0 {
		return nil
	}
	m.refreshCursorTag()
	if m.anyBlinkActive(now) {
		return blinkTickCmd()
	}
	return nil
}

// blinkTickCmd schedules the next animation frame for an in-progress
// blink burst. blinkTickInterval is deliberately shorter than
// blinkToggleInterval (a few samples per toggle, not one) so no on/off
// transition is ever skipped over between two redraws purely by unlucky
// sampling phase.
func blinkTickCmd() tea.Cmd {
	return tea.Tick(blinkTickInterval, func(time.Time) tea.Msg { return blinkTickMsg{} })
}
