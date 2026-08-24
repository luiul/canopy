// The done-episode state machine and its blink animation: the display
// overlay described in docs/agent-state-machine.md that keeps a row
// reading "done" until the user actually acts on it (enter or c), plus the
// on/off blink that makes a newly-done row hard to miss. Split out of
// app.go, which otherwise mixed this with the Bubble Tea plumbing itself;
// see app.go's package doc for the full file layout.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/registry"
)

// doneEpisode is one open-or-closed "done" episode for a single entry key
// (see Model.done). Since is when updateDoneTracking first saw this
// episode's raw State read "done"; Acked is zero while the episode is
// still open (unacknowledged) and set to the moment enter or c actually
// fired for it (see acknowledge). NextBlinkAt/BurstStart drive the blink
// animation (see advanceBlinks/blinkActive/blinkOn) — the thing that makes
// an open episode hard to miss, both the moment it opens and again every
// blinkReminderInterval for as long as it stays open.
type doneEpisode struct {
	Since time.Time
	Acked time.Time

	// RawAt is the raw source's own report timestamp
	// (registry.RegistryEntry.RealStateReportedAt) for the "done" write this
	// episode currently reflects. Kept fresh every poll while the episode is
	// open (see updateDoneTracking), so that once it's acknowledged, a later
	// poll can tell a genuinely new settle apart from the same still-fresh
	// "done" string repeating: pistatus.Read keeps returning literal "done"
	// for up to pistatus.MaxAge after a turn settles, so the State string
	// alone can't make that distinction if a second turn settles again
	// inside that same window. Zero for anything not RealState (nothing
	// there can ever legitimately mask a second done this way, since the CPU
	// heuristic never produces "done" in the first place).
	RawAt time.Time

	// NextBlinkAt is when this episode's next blink burst should start:
	// seeded to the moment the episode opens (see updateDoneTracking), so
	// the very first burst fires immediately, then pushed forward by
	// blinkReminderInterval each time advanceBlinks actually starts one.
	NextBlinkAt time.Time

	// BurstStart is when the current (or most recently started) blink burst
	// began. blinkActive/blinkOn derive everything about "is this row
	// blinking, and which half of the on/off toggle is it in" from just
	// this and now — no separate on/off flag to keep in sync.
	BurstStart time.Time
}

// displayState is the state actually shown for e. Three cases, checked in
// order:
//
//  1. e has an open (unacknowledged) episode in done: report "done"
//     unconditionally, regardless of what e.State (the raw source) says
//     right now. This is the one deliberately sticky case — see
//     updateDoneTracking's doc comment for why an episode has to survive a
//     raw source that quietly moves off "done" on its own, with no enter/c
//     ever happening in canopy.
//  2. e has an acknowledged episode in done, and e.State is still
//     literally "done" (the common case: the user acted before the raw
//     source moved on by itself): report the synthetic "idle".
//  3. Anything else (no episode at all, or an acknowledged one whose raw
//     source has already independently moved past "done"): report e.State
//     directly.
//
// e.State itself (which needsBell and registry.stampStateSince must keep
// comparing poll to poll — see Model.acknowledge's doc comment) is never
// overwritten by any of this. Sorting, coloring, the summary line, and the
// State/Since cells all go through this instead of e.State directly, so
// acknowledging a row is reflected everywhere at once.
func displayState(e registry.RegistryEntry, done map[string]doneEpisode) string {
	if ep, ok := done[e.Key()]; ok {
		if ep.Acked.IsZero() {
			return "done"
		}
		if e.State == "done" {
			return "idle"
		}
	}
	return e.State
}

// blinkTickMsg is the animation frame for an in-progress done blink burst
// (see tickBlinks/blinkTickCmd): fired every blinkTickInterval, much
// faster than the dashboard's own poll tick, for exactly as long as some
// entry is still mid-burst.
type blinkTickMsg struct{}

// updateDoneTracking maintains m.done (see its own doc comment) against a
// fresh poll, before sorting/rendering. Two independent things happen here:
//
//  1. Opening a new episode: any key whose raw State reads "done" and has
//     no *open* entry in done yet gets one, stamped with this poll's time.
//     From this instant, displayState reports "done" for that key on every
//     subsequent poll — even one where the raw source has already moved on
//     to something else by itself (e.g. a fresh working turn starting on
//     the same session before the user ever acknowledged the previous
//     episode in canopy) — until acknowledge() actually fires for it, or
//     the key disappears from fresh outright (session ended). This is
//     deliberate and is the entire point of this map: without it, a row
//     could silently stop reading "done" with no enter/c ever happening in
//     canopy, which the dashboard's whole "done stays done until you act
//     on it" contract depends on not happening.
//
//     An *acknowledged* entry also gets a brand-new episode here if the raw
//     source has written a genuinely new "done" since the acknowledgment
//     (e.RealStateReportedAt has advanced past the episode's own RawAt) —
//     not just whenever e.State is still literally the string "done".
//     canopy-status.ts's file keeps reading "done" for up to
//     pistatus.MaxAge after a turn settles, so a second turn that starts
//     and settles again inside that same window, without canopy ever
//     sampling a "working" poll in between, would otherwise be
//     indistinguishable from the exact same still-fresh write the user
//     already dismissed — and silently swallowed: no new episode, no bell,
//     displayState falling through to the acknowledged "idle" the whole
//     time. RealStateReportedAt is the one signal (pistatus's own write
//     timestamp, not canopy's poll time) that can tell those two cases
//     apart when the State string alone can't.
//
//  2. Closing a stale, already-acknowledged episode: one whose raw source
//     has independently moved off "done" (nothing left for it to mask —
//     displayState already falls through to the real State once Acked is
//     set and raw isn't literally "done"), or whose key is no longer in
//     fresh at all. An *open* (unacknowledged) episode is never closed
//     here just because raw moved off "done" — see point 1. Its RawAt is
//     still kept current every poll while it stays open, though (see the
//     loop below), so that whenever it does eventually get acknowledged,
//     the comparison above is against the latest settle it already
//     covered, not a stale one from earlier in the same still-open episode.
func (m *Model) updateDoneTracking(fresh []registry.RegistryEntry) {
	now := time.Now()
	byKey := make(map[string]registry.RegistryEntry, len(fresh))
	for _, e := range fresh {
		byKey[e.Key()] = e
	}

	for _, e := range fresh {
		if e.State != "done" {
			continue
		}
		key := e.Key()
		if ep, ok := m.done[key]; ok {
			if ep.Acked.IsZero() {
				// Still open: nothing new to decide (see point 1's doc comment),
				// but keep RawAt current so a *later* acknowledgment compares
				// against this episode's most recent settle, not its first one.
				if e.RealState {
					ep.RawAt = e.RealStateReportedAt
					m.done[key] = ep
				}
				continue
			}
			if !e.RealState || e.RealStateReportedAt.Equal(ep.RawAt) {
				continue // acknowledged, and still the exact same write: leave it exactly as is
			}
			// acknowledged, but the raw source has written a genuinely new
			// "done" since then: falls through to open a fresh episode below.
		}
		if m.done == nil {
			m.done = map[string]doneEpisode{}
		}
		// NextBlinkAt seeded to now, not left zero: advanceBlinks treats a
		// zero NextBlinkAt as "nothing scheduled", and the whole point of a
		// freshly opened episode is that its first blink burst fires right
		// away, on this same poll.
		m.done[key] = doneEpisode{Since: now, NextBlinkAt: now, RawAt: e.RealStateReportedAt}
	}

	for key, ep := range m.done {
		e, present := byKey[key]
		if !present {
			delete(m.done, key) // session ended before this episode was ever resolved
			continue
		}
		if !ep.Acked.IsZero() && e.State != "done" {
			delete(m.done, key) // acknowledged, and raw independently resolved off "done": stale bookkeeping
		}
	}
}

// blinkToggleInterval is how long each on/off phase of a done blink lasts:
// short enough to read as genuinely blinking rather than one long flash,
// long enough to still be legible.
const blinkToggleInterval = 300 * time.Millisecond

// blinkPhases is how many on/off phases make up one blink burst (3 full
// on-off blinks) — unmistakable that a row just went done, without
// blinking so long it turns into noise.
const blinkPhases = 6

// blinkBurstDuration is how long a single blink burst runs before
// settling back to a steady (still colored, just no longer toggling) done
// cell.
const blinkBurstDuration = blinkPhases * blinkToggleInterval

// blinkReminderInterval is how long an unacknowledged done row goes quiet
// between blink bursts. If enter or c still hasn't happened by then, it
// blinks again — a repeating nudge for as long as a row stays done, not a
// one-shot animation you could miss once and then forget about.
const blinkReminderInterval = 5 * time.Minute

// blinkTickInterval drives the animation's own redraw cadence while a
// burst is active: a few samples per blinkToggleInterval, so no on/off
// transition is ever skipped over between two redraws purely by unlucky
// sampling phase. Independent of, and much shorter than, the dashboard's
// own poll interval.
const blinkTickInterval = blinkToggleInterval / 3

// advanceBlinks starts a fresh blink burst — from scratch, right now — for
// every open (unacknowledged) done episode whose NextBlinkAt has arrived:
// immediately the first time (updateDoneTracking seeds NextBlinkAt to the
// instant the episode opens), then every blinkReminderInterval after that
// for as long as it stays unacknowledged. Acknowledged episodes are
// skipped outright — acknowledge() doesn't need to touch NextBlinkAt/
// BurstStart itself, since displayState never reports "done" for an acked
// episode again anyway (see its own doc comment), so this can never
// restart one after the fact.
func (m *Model) advanceBlinks(now time.Time) {
	for key, ep := range m.done {
		if !ep.Acked.IsZero() || ep.NextBlinkAt.IsZero() || now.Before(ep.NextBlinkAt) {
			continue
		}
		ep.BurstStart = now
		ep.NextBlinkAt = now.Add(blinkReminderInterval)
		m.done[key] = ep
	}
}

// blinkActive reports whether ep is mid-burst at now: unacknowledged, and
// still within blinkBurstDuration of its most recent BurstStart. Checking
// Acked here (not just at advanceBlinks) means a burst stops the instant
// the user acknowledges it mid-blink, rather than running out its full
// duration first.
func blinkActive(ep doneEpisode, now time.Time) bool {
	return ep.Acked.IsZero() && !ep.BurstStart.IsZero() && now.Sub(ep.BurstStart) < blinkBurstDuration
}

// blinkOn reports which half of the current on/off toggle a still-active
// burst is in at now: true for the visible ("on") half, alternating every
// blinkToggleInterval since BurstStart. Only meaningful once blinkActive
// has already reported true for the same ep/now — callers check that
// first.
func blinkOn(ep doneEpisode, now time.Time) bool {
	return (now.Sub(ep.BurstStart)/blinkToggleInterval)%2 == 0
}

// anyBlinkActive reports whether at least one entry in m.done is mid-burst
// right now — what tickBlinks uses to decide whether the animation needs
// another frame. Once every open episode's burst has settled, there's
// nothing left to redraw until the next one's NextBlinkAt arrives, which
// the regular poll tick notices on its own (see Update's pollResultMsg
// case) without any dedicated long-duration timer for it.
func (m Model) anyBlinkActive(now time.Time) bool {
	for _, ep := range m.done {
		if blinkActive(ep, now) {
			return true
		}
	}
	return false
}

// tickBlinks advances every open done episode's blink schedule against
// now (see advanceBlinks), rebuilds the table's rows so any resulting
// on/off change actually renders, and returns a command to redraw again
// shortly if a burst is still running — nil once every burst has settled,
// leaving the next reminder to start a new one on some later poll instead
// of ticking indefinitely. Called from both the poll path (pollResultMsg,
// where a burst can newly start) and the animation path (blinkTickMsg,
// which exists purely to keep an already-running burst visibly toggling).
//
// A no-op, with no row rebuild at all, when m.done is empty: the common
// case on most polls (nothing currently done), where there is by
// definition nothing that could be blinking and so nothing that needs
// re-rendering on top of the rows applyEntries just built.
func (m *Model) tickBlinks(now time.Time) tea.Cmd {
	if len(m.done) == 0 {
		return nil
	}
	m.advanceBlinks(now)
	m.refreshCursorMarker()
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

// acknowledge closes entry's current "done" episode: from this instant on,
// displayState treats it as idle everywhere (sorting, coloring, the
// summary line, the State/Since cells). That's exactly what the "c"
// keybind is for: dismissing a row without bringing its terminal to the
// front, and it's also why this closes an *open* episode in m.done
// (entry.Key() present, Acked still zero — see updateDoneTracking) rather
// than only ever looking at entry.State: the raw source may have already
// moved on to something else by the time the user presses enter/c (e.g. a
// fresh working turn starting on the same session before it was
// acknowledged), and this must still count as acknowledging the earlier
// done episode — that's the entire point of m.done latching an episode
// open across exactly that kind of poll.
//
// A no-op for anything neither currently (raw) done nor already tracked as
// an open episode — nothing to acknowledge — so it's safe to call
// unconditionally from both "enter" and "c".
//
// This only ever writes to m.done; it never touches entry's raw State
// field (see the done field's own doc comment on Model for why that has to
// stay untouched for needsBell/registry.stampStateSince to keep working
// across polls).
func (m *Model) acknowledge(entry registry.RegistryEntry) {
	key := entry.Key()
	ep, tracked := m.done[key]
	if entry.State != "done" && !tracked {
		return
	}
	if m.done == nil {
		m.done = map[string]doneEpisode{}
	}
	if !tracked {
		// Not already tracked by updateDoneTracking (only expected from tests
		// driving acknowledge directly): seed RawAt from entry too, so a later
		// poll's genuinely-new-settle check in updateDoneTracking has
		// something real to compare against instead of a zero value that
		// would equal a legitimately-zero RealStateReportedAt and mask a
		// following poll's comparison.
		ep = doneEpisode{Since: entry.StateSince, RawAt: entry.RealStateReportedAt}
	}
	ep.Acked = time.Now()
	m.done[key] = ep
	m.refreshCursorMarker()
}
