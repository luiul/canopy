// Package tui is canopy's interactive dashboard: a table of every known
// agent-kind process on this machine, polled on a timer, with jump-to-window
// on Enter.
package tui

import (
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/jump"
	"github.com/luiul/canopy/internal/registry"
)

// DefaultInterval is the poll interval used when none is given. Also sets
// the resolution of refineExternalStates' CPU-time delta: a shorter
// interval means a tighter, more responsive "has this actually done
// anything recently" window, at the cost of polling ps/lsof more often.
const DefaultInterval = 2 * time.Second

const notifyDuration = 4 * time.Second

// Column indexes, in the order New builds them. Used both for column-width
// bookkeeping (resizeColumns) and for locating the State/Since columns
// within an already-rendered line (colorizeRows).
//
// Order is deliberately urgency-first: State and Since (what needs you, and
// for how long) come immediately after the cursor marker so the most
// action-relevant columns are also the leftmost ones, matching the
// top-to-bottom state-priority sort. Surface and Location (where a session
// lives) follow. Kind and PID are last and narrow: useful context, but
// rarely the thing you're scanning for, so they're the columns that give up
// width first and truncate hardest on a narrow terminal.
const (
	colCursor = iota
	colState
	colSince
	colSurface
	colLocation
	colKind
	colPID
)

var (
	surfaceLabels = map[ancestry.Surface]string{
		ancestry.VSCode:  "VS Code",
		ancestry.Ghostty: "Ghostty",
		ancestry.Unknown: "unknown",
	}

	titleStyle  = lipgloss.NewStyle().Bold(true)
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

// cursorMarker is the plain-text glyph shown in the leftmost column of the
// currently selected row. It replaces bubbles/table's own Selected style
// (a whole-row background/foreground highlight), which hid State's color
// coding on whichever row happened to be selected. Deliberately a plain
// ASCII character rather than a fancier Unicode arrow: colorizeRows slices
// rendered lines by byte offset assuming 1 byte per display column, which
// only holds if every column left of State/Since (including this one) is
// plain ASCII.
const cursorMarker = ">"

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

// homeDir is the current user's home directory, used to shorten Location
// paths to "~". "" (meaning: don't shorten) if it can't be determined.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
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
// for you to do), then idle, then unknown (heuristic couldn't tell). There
// is deliberately no "blocked" entry: nothing in canopy ever produces that
// state (see docs/agent-state-machine.md), so it isn't part of the
// vocabulary here either.
var statePriority = map[string]int{
	"done":    0,
	"working": 1,
	"idle":    2,
	"unknown": 3,
}

// stateOrder is statePriority's states in display order, used for the
// header's per-state summary counts too.
var stateOrder = []string{"done", "working", "idle", "unknown"}

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

type tickMsg struct{}
type pollResultMsg struct{ entries []registry.RegistryEntry }
type jumpResultMsg struct{ result jump.Result }
type clearNotifyMsg struct{ token int }

// blinkTickMsg is the animation frame for an in-progress done blink burst
// (see tickBlinks/blinkTickCmd): fired every blinkTickInterval, much
// faster than the dashboard's own poll tick, for exactly as long as some
// entry is still mid-burst.
type blinkTickMsg struct{}

// Model is the bubbletea model backing the dashboard.
type Model struct {
	interval time.Duration
	user     string
	home     string

	entries []registry.RegistryEntry // sorted, parallel to the table's real rows
	table   table.Model

	// done tracks each entry's current "done" episode by Key() (see
	// doneEpisode): open (Acked zero) until the user actually acts on it —
	// pressing enter or c, see acknowledge — closed (Acked set) from that
	// instant on. displayState/sortEntries/stateCellText/sinceCellText/
	// summaryLine all read this instead of e.State directly; the raw State
	// field itself is left completely untouched so needsBell and
	// registry.stampStateSince keep comparing real poll-to-poll transitions,
	// not what the user has dismissed on screen (or what's still awaiting
	// dismissal) on screen. updateDoneTracking (run every poll, before
	// sorting) is what opens and closes these episodes; deliberately does
	// *not* close an open one just because the raw source moves off "done"
	// by itself (e.g. the same session starting a fresh working turn before
	// the user ever acknowledged the previous done episode in canopy) —
	// only acknowledge() or the key vanishing from a fresh poll outright
	// (session ended) does that. See updateDoneTracking's own doc comment
	// for the full rationale.
	done map[string]doneEpisode

	notification  string
	notifyIsError bool
	notifyToken   int

	// bellEnabled gates the terminal-bell side effect in applyEntries/Update
	// (see needsBell): on by default (set in New), off via --no-bell (see
	// cmd/canopy) for anyone who finds an audible alert intrusive. Coloring,
	// flashing, and done's blinking (colorize.go, stateCellText) happen
	// regardless of this flag.
	bellEnabled bool

	width, height int
	quitting      bool
}

// New builds the dashboard model, polling at interval.
func New(interval time.Duration) Model {
	columns := []table.Column{
		{Title: "", Width: 1}, // cursorMarker
		{Title: "State", Width: 9},
		{Title: "Since", Width: 6},
		{Title: "Surface", Width: 9},
		{Title: "Location", Width: 40},
		{Title: "Kind", Width: 7}, // narrow on purpose; truncates long kinds (e.g. "mastracode")
		{Title: "PID", Width: 6},  // narrow on purpose; truncates rare 6+ digit pids
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(15),
	)
	styles := table.DefaultStyles()
	// No whole-row highlight for the selected row: cursorMarker (the
	// leftmost column) shows selection instead, without covering up State's
	// color coding on that row.
	styles.Selected = lipgloss.NewStyle()
	t.SetStyles(styles)

	return Model{
		interval:    interval,
		user:        currentUser(),
		home:        homeDir(),
		table:       t,
		bellEnabled: true,
	}
}

// WithBell sets whether canopy rings the terminal bell (see bellCmd) the
// moment any row newly needs attention. Chainable off New, e.g.
// New(interval).WithBell(false), so Run can wire --no-bell through without
// changing New's signature (and breaking every existing New(999) call in
// this package's tests).
func (m Model) WithBell(enabled bool) Model {
	m.bellEnabled = enabled
	return m
}

// Init kicks off the first poll and the recurring timer.
func (m Model) Init() tea.Cmd {
	return tea.Batch(pollCmd(m.user, nil), tickCmd(m.interval))
}

func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg { return tickMsg{} })
}

func pollCmd(user string, previous []registry.RegistryEntry) tea.Cmd {
	return func() tea.Msg {
		return pollResultMsg{entries: registry.PollOnce(user, previous)}
	}
}

func jumpCmd(entry registry.RegistryEntry) tea.Cmd {
	return func() tea.Msg {
		return jumpResultMsg{result: jump.To(entry)}
	}
}

func clearNotifyCmd(token int) tea.Cmd {
	return tea.Tick(notifyDuration, func(time.Time) tea.Msg { return clearNotifyMsg{token: token} })
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.table.SetWidth(msg.Width)
		m.table.SetHeight(clampInt(msg.Height-6, 3, 1000))
		m.resizeColumns()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, pollCmd(m.user, m.entries)
		case "enter":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			// canopy-status.ts writes "done" unconditionally at settle time and
			// only ever overwrites it once a fresh working turn starts (see
			// docs/agent-state-machine.md's "Removed: frontmost/focus
			// detection") — nothing flips it back to "idle" on its own anymore.
			// Acknowledging right here is what actually clears it: the row
			// stops reading done the instant the user acts on it, whether or
			// not the jump itself lands.
			m.acknowledge(entry)
			return m, jumpCmd(entry)
		case "c":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			// Dismiss in place, no jump: for a done row you've already dealt
			// with (or don't need to jump to at all) without bringing its
			// terminal to the front, which is the only other way a done row
			// currently stops reading done.
			m.acknowledge(entry)
			return m, nil
		default:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			m.refreshCursorMarker()
			return m, cmd
		}

	case tickMsg:
		return m, tea.Batch(pollCmd(m.user, m.entries), tickCmd(m.interval))

	case pollResultMsg:
		bell := m.applyEntries(msg.entries)
		// tickBlinks is unconditional, unlike the bell: blinking is a visual
		// treatment (like coloring/flashing), not an audible one, so --no-bell
		// doesn't touch it. It starts a fresh burst for any episode newly due
		// (just opened, or blinkReminderInterval since its last one) and
		// returns a follow-up command only while a burst is still running.
		blink := m.tickBlinks(time.Now())
		if bell && m.bellEnabled {
			return m, tea.Batch(bellCmd(), blink)
		}
		return m, blink

	case blinkTickMsg:
		// Purely an animation frame: no entries changed, just possibly which
		// half of an in-progress burst's on/off toggle is showing.
		return m, m.tickBlinks(time.Now())

	case jumpResultMsg:
		m.notification = msg.result.Message
		m.notifyIsError = !msg.result.OK
		m.notifyToken++
		return m, clearNotifyCmd(m.notifyToken)

	case clearNotifyMsg:
		if msg.token == m.notifyToken {
			m.notification = ""
		}
		return m, nil
	}
	return m, nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// selectedEntry returns the RegistryEntry backing the currently highlighted
// row, or ok=false if there are no real entries (e.g. only the placeholder
// row is showing).
func (m Model) selectedEntry() (registry.RegistryEntry, bool) {
	if len(m.entries) == 0 {
		return registry.RegistryEntry{}, false
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.entries) {
		return registry.RegistryEntry{}, false
	}
	return m.entries[idx], true
}

// applyEntries sorts fresh entries, rebuilds the table's rows, and restores
// the cursor to whichever entry was selected before the refresh (by key),
// the same key-based cursor-preservation canopy's Python original does. It
// returns whether this refresh introduced a row that newly needs attention
// (see needsBell), computed against the entries from before this call, so
// Update can ring the bell on exactly the poll where that happened.
func (m *Model) applyEntries(fresh []registry.RegistryEntry) bool {
	var previousKey string
	if entry, ok := m.selectedEntry(); ok {
		previousKey = entry.Key()
	}

	// Bell decisions are always made against the raw State (see needsBell),
	// never displayState: an acknowledged row that's still raw done and
	// stays that way must not re-ring just because a user dismissed it.
	bell := needsBell(m.entries, fresh)
	m.updateDoneTracking(fresh)

	sortEntries(fresh, m.done)
	m.entries = fresh

	cursor := m.table.Cursor()
	if previousKey != "" {
		for i, e := range fresh {
			if e.Key() == previousKey {
				cursor = i
				break
			}
		}
	}
	if len(fresh) == 0 {
		cursor = 0
	} else {
		cursor = clampInt(cursor, 0, len(fresh)-1)
	}

	m.table.SetRows(buildRows(fresh, cursor, m.home, time.Now(), m.done))
	m.table.SetCursor(cursor)
	return bell
}

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
func (m *Model) tickBlinks(now time.Time) tea.Cmd {
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

// needsBell reports whether any entry in fresh newly needs attention
// compared to previous: either a brand new entry that's already done the
// first time canopy sees it, or an existing one whose State just flipped
// into done from something else. An entry that was already done last poll
// and still is doesn't re-trigger it — otherwise a session sitting done
// for an hour would ring the bell on every single poll for that whole
// hour, drowning out the one moment that actually mattered: the
// transition itself. "Still is" means a genuinely unchanged settle, not
// just a repeated "done" string: for a RealState entry, RealStateReportedAt
// advancing between the two polls means the raw source wrote a brand-new
// "done" in between (a second turn settling within the same
// pistatus.MaxAge window a first one's write was still fresh for, with no
// "working" poll ever sampled in between) — see updateDoneTracking's doc
// comment for the full rationale — and that still counts as a fresh
// transition worth ringing for. done is the only state this checks: there
// is no "blocked" in canopy's vocabulary (see docs/agent-state-machine.md),
// and working/idle/unknown are never worth ringing a bell over.
func needsBell(previous, fresh []registry.RegistryEntry) bool {
	prevByKey := make(map[string]registry.RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	for _, f := range fresh {
		if f.State != "done" {
			continue
		}
		if was, ok := prevByKey[f.Key()]; ok && was.State == "done" {
			if !f.RealState || f.RealStateReportedAt.Equal(was.RealStateReportedAt) {
				continue // the exact same settle we already rang for
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
// which a color change or flash (colorize.go) by definition cannot.
func bellCmd() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stderr, "\a")
		return nil
	}
}

// refreshCursorMarker rebuilds the table's rows so the leftmost
// cursorMarker cell follows the cursor immediately after it moves (arrow
// keys, page up/down, etc.), instead of waiting for the next poll. Also
// reused by acknowledge to reflect a dismissal immediately, for the same
// reason: don't wait for the next poll to show it.
func (m *Model) refreshCursorMarker() {
	if len(m.entries) == 0 {
		return
	}
	m.table.SetRows(buildRows(m.entries, m.table.Cursor(), m.home, time.Now(), m.done))
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

// buildRows constructs the table's rows from already-sorted entries.
// cursor picks which row's leading cell carries cursorMarker; it's a plain
// parameter (rather than read from the table itself) so this same helper
// builds rows both right after a poll (applyEntries) and on every cursor
// move in between polls (refreshCursorMarker), so the arrow tracks the
// highlighted row immediately rather than only once every poll interval.
func buildRows(entries []registry.RegistryEntry, cursor int, home string, now time.Time, done map[string]doneEpisode) []table.Row {
	if len(entries) == 0 {
		// Placeholder message goes in Location: the widest column, and the
		// only one guaranteed to have room for it regardless of terminal width.
		placeholder := table.Row{"", "", "", "", "", "", ""}
		placeholder[colLocation] = "no known agent-kind processes found on this machine"
		return []table.Row{placeholder}
	}
	rows := make([]table.Row, len(entries))
	for i, e := range entries {
		marker := ""
		if i == cursor {
			marker = cursorMarker
		}
		rows[i] = table.Row{
			marker,
			stateCellText(e, now, done),
			sinceCellText(e, now, done),
			surfaceLabel(e.Surface),
			location(e, home),
			e.Kind,
			fmt.Sprintf("%d", e.Pid),
		}
	}
	return rows
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

// stateCellText is the State column's plain-text cell value: displayState's
// word (see displayState — "idle" rather than "done" once acknowledged),
// with a trailing flashMarker whenever it's a "done" row currently
// mid-blink-burst and in its visible ("on") half (see blinkActive/blinkOn
// — toggles on and off as the burst runs). done is the only state with any
// attention-getting treatment at all; every other word (including
// "unknown") renders as-is. Actual coloring/reverse-video happens later,
// in View, by post-processing the rendered table (see colorize.go).
func stateCellText(e registry.RegistryEntry, now time.Time, done map[string]doneEpisode) string {
	word := displayState(e, done)
	if ep, ok := done[e.Key()]; word == "done" && ok && blinkActive(ep, now) && blinkOn(ep, now) {
		return word + flashMarker
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

func (m *Model) resizeColumns() {
	cols := m.table.Columns()
	if len(cols) != 7 {
		return
	}
	fixed := cols[colCursor].Width + cols[colState].Width + cols[colSince].Width + cols[colSurface].Width + cols[colKind].Width + cols[colPID].Width
	remaining := m.width - fixed - 14 // 2 chars of padding per cell, 7 cells
	if remaining < 20 {
		remaining = 20
	}
	cols[colLocation].Width = remaining
	m.table.SetColumns(cols)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	header := titleStyle.Render("canopy") + subtleStyle.Render(" — agent sessions on this machine")
	if summary := summaryLine(m.entries, m.done); summary != "" {
		header += "\n" + summary
	}

	footer := subtleStyle.Render("↑/↓ move · enter jump · c complete · r refresh · q quit")
	if m.notification != "" {
		style := okStyle
		if m.notifyIsError {
			style = errorStyle
		}
		footer = style.Render(m.notification)
	}

	tableView := colorizeRows(m.table.View(), m.table.Columns(), colState, colSince)
	return header + "\n\n" + tableView + "\n\n" + footer + "\n"
}

// Run starts the dashboard program and blocks until the user quits.
func Run(interval time.Duration, bellEnabled bool) error {
	p := tea.NewProgram(New(interval).WithBell(bellEnabled), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
