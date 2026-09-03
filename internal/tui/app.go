// Package tui is canopy's interactive dashboard: a table of every known
// agent-kind process on this machine, polled on a timer, with jump-to-window
// on Enter.
//
// This file holds the Bubble Tea plumbing itself (Model, Init/Update/View,
// the tea.Cmd constructors, and the column/layout constants they all
// share). The done-episode state machine and its blink animation live in
// done.go, the bell decision in bell.go, and row/summary rendering in
// rows.go — see each file's own package doc for why it's split out.
package tui

import (
	"fmt"
	"os"
	"os/user"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/luiul/canopy/internal/jump"
	"github.com/luiul/canopy/internal/kill"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/confirm"
	"github.com/luiul/dashkit/loam"
	"github.com/luiul/dashkit/trellis"
)

// killProcess is a package-level seam onto kill.Process — the same seam
// pattern registry.go uses for its scan functions — swapped out in tests
// so the keybind flows (x/X/p/D) can be exercised without signaling real
// processes.
var killProcess = kill.Process

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
// for how long) come first, matching the top-to-bottom state-priority sort.
// Surface and Location (where a session lives) follow. CPU/RAM/Uptime (how a
// session is doing, resource-wise) come next: useful for spotting a runaway
// or forgotten session, but not the first thing anyone scans for, so they sit
// to the right of Location rather than competing with State/Since for
// leftmost attention. Kind and PID are last and narrow: useful context, but
// rarely the thing you're scanning for, so they're the columns that give up
// width first and truncate hardest on a narrow terminal.
// Note: there is no leading cursor column. Selected rows are highlighted
// via loam.ColorizeRows' post-render row highlight; see loam's doc.
const (
	colState = iota
	colSince
	colSurface
	colLocation
	colCPU
	colRAM
	colUptime
	colKind
	colPID
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	// promptStyle renders an armed confirmation's prompt: yellow for the
	// ordinary destructive kind (SIGTERM via x/D), while errorStyle's
	// louder red marks the force tier (SIGKILL via X). See footerView.
	promptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
)

// cursorSentinel is an alias for loam.Sentinel: the zero-width Unicode
// marker prepended to the Since cell of the selected row, so colorizeRows
// knows which line to highlight. See loam's doc for why this approach.
var cursorSentinel = loam.Sentinel

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

type tickMsg struct{}
type pollResultMsg struct {
	entries []registry.RegistryEntry
	// warning mirrors registry.PollResult.Warning: non-empty exactly when
	// the primary agent-kind scan itself failed to run (see registry.
	// PollOnce's doc comment), as opposed to running fine and finding zero
	// matches. Displayed by View regardless of --no-bell/--no-color, since
	// it's diagnostic text, not the color/blink attention treatment those
	// flags exist to suppress.
	warning string
}
type jumpResultMsg struct{ result jump.Result }
type killResultMsg struct {
	results []kill.Result
	sig     syscall.Signal
}
type clearNotifyMsg struct{ token int }

// killPrompt is an armed, not-yet-confirmed kill action's payload (see
// confirm.State): the entries to signal and the signal to send, held
// until the user answers or the prompt cancels itself (auto-cancel
// timeout, or every target vanishing from a poll). Entries are
// re-stamped from every poll while the prompt is open (see Update's
// pollResultMsg case), so a row that vanishes mid-prompt drops out of
// the target set — and the survivors' Uptime samples stay fresh enough
// for kill.Process's process-identity guard to trust.
type killPrompt struct {
	entries []registry.RegistryEntry
	sig     syscall.Signal
}

// Model is the bubbletea model backing the dashboard.
type Model struct {
	interval time.Duration
	user     string
	home     string

	entries []registry.RegistryEntry // sorted, parallel to the table's real rows
	table   table.Model

	// done tracks each entry's current "done" episode by Key() (see
	// doneEpisode in done.go): open (Acked zero) until the user actually
	// acts on it — pressing enter or c, see acknowledge — closed (Acked
	// set) from that instant on. displayState/sortEntries/stateCellText/
	// sinceCellText/summaryLine all read this instead of e.State directly;
	// the raw State field itself is left completely untouched so needsBell
	// and registry.stampStateSince keep comparing real poll-to-poll
	// transitions, not what's currently displayed on screen (dismissed, or
	// still awaiting dismissal). updateDoneTracking (run every poll, before
	// sorting) is what opens and closes these episodes; deliberately does
	// *not* close an open one just because the raw source moves off "done"
	// by itself (e.g. the same session starting a fresh working turn before
	// the user ever acknowledged the previous done episode in canopy) —
	// only acknowledge() or the key vanishing from a fresh poll outright
	// (session ended) does that. See updateDoneTracking's own doc comment
	// (done.go) for the full rationale.
	done map[string]doneEpisode

	notification  string
	notifyIsError bool
	notifyToken   int

	// scanWarning mirrors the most recent poll's pollResultMsg.warning: set
	// whenever the primary agent-kind scan itself failed to run, cleared
	// the moment a later poll succeeds again. Shown in the header
	// (View), unlike notification/notifyIsError, which are for the
	// jump-result footer message and auto-clear after notifyDuration —
	// scanWarning persists for as long as the underlying condition does,
	// since "canopy can't scan right now" is not a one-shot event the way
	// a jump attempt is.
	scanWarning string

	// resizer tracks an in-progress mouse column-border drag (see
	// github.com/luiul/dashkit/trellis); colOverrides remembers the resulting
	// width of every column a drag has actually touched (a drag always
	// moves two adjacent columns at once; see trellis.Model.Handle's own
	// doc), by column index (see the Column indexes above), so
	// resizeColumns' own recompute on every terminal resize doesn't
	// silently discard an earlier resize. Cleared whenever a WindowSizeMsg
	// arrives (see Update): a genuinely new terminal width invalidates the
	// old distribution of space entirely, so resizeColumns starts fresh
	// rather than fighting stale overrides sized for a different width.
	colOverrides map[int]int
	resizer      trellis.Model

	// bellEnabled gates the terminal-bell side effect in applyEntries/Update
	// (see needsBell in bell.go): on by default (set in New), off via
	// --no-bell (see cmd/canopy) for anyone who finds an audible alert
	// intrusive. Coloring and done's blinking (colorize.go, stateCellText)
	// happen regardless of this flag.
	bellEnabled bool

	// pendingKill is the armed kill confirmation prompt's state machine
	// (see github.com/luiul/dashkit/confirm): the prompt's payload (a
	// killPrompt, nil while closed) plus the auto-cancel token
	// discipline. Set by x/X/D, resolved by the shared answer
	// discipline, and pruned/cancelled by polls that no longer contain
	// its targets or by the auto-cancel timeout. While armed, every
	// keypress is intercepted before any other binding runs and any
	// non-answer is swallowed, so an armed prompt can never accidentally
	// jump, quit, or stack with a second prompt; ctrl+c is the one
	// exception, quitting from anywhere as always.
	pendingKill confirm.State[killPrompt]

	// showHelp swaps the table for the full keybinding listing (the ?
	// overlay, see helpView): the footer only has room for the few most
	// used bindings, so the rest live there. While open, any keypress
	// closes it without acting — the same intercept discipline as
	// pendingKill, so an overlay can never swallow a binding the user
	// thought they were aiming at the table.
	showHelp bool

	width, height int
	quitting      bool
}

// New builds the dashboard model, polling at interval.
func New(interval time.Duration) Model {
	columns := []table.Column{
		// Every fixed column's default fits its widest value without
		// truncating and is at least its title's width plus one: the
		// header's column-border glyph (loam.DrawHeaderBorders) sits
		// immediately right of the content area, so a column exactly as
		// wide as its title renders "Title│" with the border touching
		// the text. How far each one may NARROW on a drag is a separate
		// question — see the content-floor constants by columnMinWidths.
		{Title: "State", Width: 8},
		{Title: "Since", Width: 6},
		{Title: "Surface", Width: 9},
		{Title: "Location", Width: 40},
		{Title: "CPU", Width: 4},
		{Title: "RAM", Width: 6},
		{Title: "Uptime", Width: 7},
		{Title: "Kind", Width: 7}, // narrow on purpose; truncates long kinds (e.g. "mastracode")
		{Title: "PID", Width: 6},  // narrow on purpose; truncates rare 6+ digit pids
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(15),
	)
	styles := table.DefaultStyles()
	// The selected row is highlighted via loam.ColorizeRows' post-render
	// row highlight; see loam's doc for why this approach.
	styles.Selected = lipgloss.NewStyle()
	t.SetStyles(styles)

	return Model{
		interval:    interval,
		user:        currentUser(),
		home:        homeDir(),
		table:       t,
		bellEnabled: true,
		resizer:     trellis.New(),
	}
}

// WithBell sets whether canopy rings the terminal bell (see bellCmd in
// bell.go) the moment any row newly needs attention. Chainable off New,
// e.g. New(interval).WithBell(false), so Run can wire --no-bell through
// without changing New's signature (and breaking every existing New(999)
// call in this package's tests).
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
		result := registry.PollOnce(user, previous)
		return pollResultMsg{entries: result.Entries, warning: result.Warning}
	}
}

func jumpCmd(entry registry.RegistryEntry) tea.Cmd {
	return func() tea.Msg {
		return jumpResultMsg{result: jump.To(entry)}
	}
}

// killCmd signals every entry in turn and collects the per-entry results.
// Sequential on purpose: even a bulk kill is a handful of kill(2) calls,
// and keeping them in one command keeps their footer summary atomic.
func killCmd(entries []registry.RegistryEntry, sig syscall.Signal) tea.Cmd {
	return func() tea.Msg {
		results := make([]kill.Result, len(entries))
		for i, e := range entries {
			results[i] = killProcess(e, sig)
		}
		return killResultMsg{results: results, sig: sig}
	}
}

// setNotify records a footer notification (see Model.notification) and
// returns the command that auto-clears it after notifyDuration, the one
// thing every notification path (jump results, kill results, kill-prompt
// cancels, empty bulk kills) used to spell out by hand.
func (m *Model) setNotify(message string, isError bool) tea.Cmd {
	m.notification = message
	m.notifyIsError = isError
	m.notifyToken++
	return clearNotifyCmd(m.notifyToken)
}

func clearNotifyCmd(token int) tea.Cmd {
	return tea.Tick(notifyDuration, func(time.Time) tea.Msg { return clearNotifyMsg{token: token} })
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// A new terminal width invalidates whatever distribution of space a
		// prior drag settled on — resizeColumns is about to recompute every
		// column from scratch against the new width, so any stale override
		// is dropped first rather than fighting that recompute.
		m.colOverrides = nil
		m.table.SetWidth(msg.Width)
		m.table.SetHeight(clampInt(msg.Height-6, 3, 1000))
		m.resizeColumns()
		return m, nil

	case tea.MouseMsg:
		// No column drags while a modal is up: the confirmation prompt
		// owns the footer and the help overlay replaces the table, so a
		// drag's target row isn't even on screen.
		if m.showHelp || m.pendingKill.Active() {
			return m, nil
		}
		_, originY := m.renderHeader()
		cols := m.table.Columns()
		widths, changed := m.resizer.Handle(msg, cols, columnMinWidths(), 0, originY)
		if changed {
			if m.colOverrides == nil {
				m.colOverrides = map[int]int{}
			}
			// A drag always moves the dragged column and its right-hand
			// neighbor together (see trellis.Model.Handle's own doc), so
			// both of their new widths need remembering — and no others:
			// recording every column's width would pin columns this drag
			// never touched (see colOverrides' own doc), the same
			// pair-only rule understory's handler follows.
			dragged := m.resizer.DragColumn()
			m.colOverrides[dragged] = widths[dragged]
			m.colOverrides[dragged+1] = widths[dragged+1]
			m.table.SetColumns(trellis.Apply(cols, widths))
		}
		return m, nil

	case tea.KeyMsg:
		// An armed kill prompt intercepts every key before any binding
		// below runs; the answer discipline itself (y confirms,
		// n/esc/enter cancel, everything else swallowed, ctrl+c quits)
		// lives in dashkit's confirm package, shared with understory so
		// the two can't drift apart. An explicit cancel is silent; only
		// the auto-cancel timeout notifies.
		if m.pendingKill.Active() {
			switch confirm.Classify(msg) {
			case confirm.Confirm:
				pending := m.pendingKill.Payload
				m.pendingKill.Resolve()
				return m, killCmd(pending.entries, pending.sig)
			case confirm.Cancel:
				m.pendingKill.Resolve()
				return m, nil
			case confirm.Quit:
				m.quitting = true
				return m, tea.Quit
			default:
				return m, nil
			}
		}
		// The help overlay is read-only: any keypress closes it without
		// acting on the table underneath (see Model.showHelp), except
		// ctrl+c, which quits like it does from everywhere.
		if m.showHelp {
			if msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			m.showHelp = false
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, pollCmd(m.user, m.entries)
		case "?":
			m.showHelp = true
			return m, nil
		case "x", "X":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			// The graceful/forceful ladder: x asks the process to terminate
			// itself (SIGTERM — pi, for example, persists session state on a
			// clean exit), X forces it (SIGKILL) for when SIGTERM isn't
			// enough. Both arm the same confirmation prompt.
			sig := syscall.SIGTERM
			if msg.String() == "X" {
				sig = syscall.SIGKILL
			}
			return m, m.pendingKill.Arm(killPrompt{entries: []registry.RegistryEntry{entry}, sig: sig})
		case "p":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			// Pause/resume needs no confirmation: unlike killing it is fully
			// reversible (SIGCONT undoes SIGSTOP), and the row itself shows
			// the resulting "stopped" state on the very next poll.
			sig := syscall.SIGSTOP
			if entry.Stopped {
				sig = syscall.SIGCONT
			}
			return m, killCmd([]registry.RegistryEntry{entry}, sig)
		case "D":
			// Bulk form of x for cleanup: SIGTERM every row currently
			// reading done (what the user actually sees as finished — the
			// display state, open episodes included, not the raw State).
			var targets []registry.RegistryEntry
			for _, e := range m.entries {
				if displayState(e, m.done) == "done" {
					targets = append(targets, e)
				}
			}
			if len(targets) == 0 {
				return m, m.setNotify("no done sessions to kill", false)
			}
			return m, m.pendingKill.Arm(killPrompt{entries: targets, sig: syscall.SIGTERM})
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
		case "C":
			// Dismiss every open done episode at once, no jump, no per-row
			// selection: the bulk form of c for clearing a screen full of
			// done rows (see acknowledgeAll in done.go).
			m.acknowledgeAll()
			return m, nil
		default:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			m.refreshCursorTag()
			return m, cmd
		}

	case tickMsg:
		return m, tea.Batch(pollCmd(m.user, m.entries), tickCmd(m.interval))

	case tea.FocusMsg:
		// The user just switched to this window, which is almost always
		// the moment they read the table — and the typical flow is
		// starting a session in another window and then switching here to
		// check it showed up. Poll immediately rather than leaving even
		// one interval's staleness on screen (the same fix understory got
		// after a freshly created worktree read as "not listed" until the
		// next tick). Passing m.entries like the tick does keeps the
		// poll-to-poll diffing (done detection, bell, blink) on the same
		// raw-state baseline; this is an extra poll on top of the tick
		// chain, which keeps its own cadence either way.
		return m, pollCmd(m.user, m.entries)

	case pollResultMsg:
		m.scanWarning = msg.warning
		bell := m.applyEntries(msg.entries)
		// Keep an armed kill prompt in sync with reality: targets whose key
		// survived the poll are re-stamped with their fresh copy (a current
		// Uptime is what kill.Process's identity guard compares against);
		// targets that vanished exited on their own, so they drop out — and
		// with none left there is nothing to confirm, cancelling the prompt
		// rather than leaving a stale one aimed at a row that no longer
		// means what the user thinks.
		if m.pendingKill.Active() {
			byKey := make(map[string]registry.RegistryEntry, len(msg.entries))
			for _, e := range msg.entries {
				byKey[e.Key()] = e
			}
			m.pendingKill.Payload.entries = confirm.Refresh(m.pendingKill.Payload.entries, func(p registry.RegistryEntry) (registry.RegistryEntry, bool) {
				e, ok := byKey[p.Key()]
				return e, ok
			})
			if len(m.pendingKill.Payload.entries) == 0 {
				m.pendingKill.Resolve()
			}
		}
		// tickBlinks is unconditional, unlike the bell: blinking is a visual
		// treatment (like coloring), not an audible one, so --no-bell doesn't
		// touch it. It starts a fresh burst for any episode newly due (just
		// opened, or blinkReminderInterval since its last one) and returns a
		// follow-up command only while a burst is still running.
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
		return m, m.setNotify(msg.result.Message, !msg.result.OK)

	case killResultMsg:
		clear := m.setNotify(summarizeKillResults(msg.results, msg.sig))
		// Repoll right away on any success so a killed row drops promptly
		// (instead of lingering for the MissLimit debounce window) and a
		// paused/resumed row's "stopped" display catches up immediately.
		for _, r := range msg.results {
			if r.OK {
				return m, tea.Batch(clear, pollCmd(m.user, m.entries))
			}
		}
		return m, clear

	case confirm.Msg:
		if m.pendingKill.Tick(msg) {
			return m, m.setNotify(confirm.TimeoutText(), false)
		}
		return m, nil

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

// summarizeKillResults folds per-entry kill results into the single footer
// notification and its error flag. A single entry (the x/X/p case) shows
// its own message verbatim; a bulk kill (D) shows a count summary, naming
// the first failure when not every signal landed.
func summarizeKillResults(results []kill.Result, sig syscall.Signal) (string, bool) {
	if len(results) == 1 {
		return results[0].Message, !results[0].OK
	}
	ok := 0
	firstErr := ""
	for _, r := range results {
		if r.OK {
			ok++
		} else if firstErr == "" {
			firstErr = r.Message
		}
	}
	if ok == len(results) {
		return fmt.Sprintf("%s %d sessions", kill.Verb(sig), ok), false
	}
	return fmt.Sprintf("%s %d of %d sessions; first failure: %s", kill.Verb(sig), ok, len(results), firstErr), true
}

// killPromptText builds the armed kill prompt's footer line, following
// the one prompt template both dashboards share: "<Verb> <target>?
// <consequence sentence>. [y/N]", with a plain verb (Terminate/Kill, see
// kill.PromptVerb) rather than the raw signal name. A single target names
// kind, pid, and location (the things that disambiguate one pi session
// from the four others on screen), plus a consequence sentence when the
// session is mid-turn; a bulk prompt just names the verb and the count.
func (m Model) killPromptText() string {
	p := m.pendingKill.Payload
	verb := kill.PromptVerb(p.sig)
	if len(p.entries) == 1 {
		e := p.entries[0]
		prompt := fmt.Sprintf("%s %s (pid %d, %s)?", verb, e.Kind, e.Pid, location(e, m.home))
		if displayState(e, m.done) == "working" {
			prompt += " Currently working."
		}
		return prompt + " [y/N]"
	}
	return fmt.Sprintf("%s %d done sessions? [y/N]", verb, len(p.entries))
}

// applyEntries sorts fresh entries, rebuilds the table's rows, and restores
// the cursor to whichever entry was selected before the refresh (by key),
// the same key-based cursor-preservation canopy's Python original does. It
// returns whether this refresh introduced a row that newly needs attention
// (see needsBell in bell.go), computed against the entries from before this
// call, so Update can ring the bell on exactly the poll where that
// happened.
func (m *Model) applyEntries(fresh []registry.RegistryEntry) bool {
	var previousKey string
	if entry, ok := m.selectedEntry(); ok {
		previousKey = entry.Key()
	}

	// Bell decisions are always made against the raw State (see needsBell),
	// never displayState: an acknowledged row that's still raw done and
	// stays that way must not re-ring just because a user dismissed it.
	// Passed m.done as it stood at the *end of the previous* poll (before
	// updateDoneTracking mutates it for this one), so needsBell can tell a
	// settle that's landing on an already-open, still-unacknowledged episode
	// (silently absorbed by updateDoneTracking, nothing new on screen, must
	// not ring again) apart from one that's genuinely new since the last
	// acknowledgment (the reopen case, which must ring).
	bell := needsBell(m.entries, fresh, m.done)
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

// stateContentWidth, surfaceContentWidth, ramContentWidth,
// uptimeContentWidth, and pidContentWidth are the widest values those
// columns ever display: the states top out at "working"/"unknown", the
// surfaces at "VS Code"/"Ghostty"/"unknown", RAM at "1023M"/"99.9G"
// (see ramCellText), Uptime at "23h59m" (humanizeSince's longest form
// before it switches to "%dd"), and PIDs at five digits. kindDragFloor
// is the exception: Kind's default already truncates long kinds on
// purpose ("mastracode"), so its floor only keeps the short kinds
// ("pi", "grok", "kimi") fully visible rather than fitting every value.
const (
	stateContentWidth   = 7
	surfaceContentWidth = 7
	ramContentWidth     = 5
	uptimeContentWidth  = 6
	kindDragFloor       = 4
	pidContentWidth     = 5
)

// columnMinWidths returns each column's own minimum width, in the same
// order/index New builds them (see the Column indexes above), for
// trellis' mouse-resize handling (see Update's tea.MouseMsg case). The
// fixed columns floor at their CONTENT widths (see the constants above),
// not their defaults: their values are bounded and always fit there,
// while their defaults only add room for the title — so a drag can
// narrow them past the default (truncating the title, never a value) to
// make room for Location or a neighbor, the same deal understory's fixed
// columns get. Flooring at the defaults instead would freeze every fixed
// column in place: each one always sits exactly at its default, leaving
// zero room to trade in either direction. Since and CPU are the
// exceptions: their defaults ARE their content widths ("23h59m",
// "100%"), so they have nothing to give and their borders move only via
// their neighbors. Location floors at 20, the same floor resizeColumns'
// own leftover-space computation already respects. Trellis itself treats
// every column, Location included, identically — there's no column
// singled out as a drag sink any more (see the trellis package's own
// doc); this slice only says how far each one may shrink.
func columnMinWidths() []int {
	return []int{
		stateContentWidth,
		6, // Since: its default is already its content width ("23h59m")
		surfaceContentWidth,
		20,
		4, // CPU: its default is already its content width ("100%")
		ramContentWidth,
		uptimeContentWidth,
		kindDragFloor,
		pidContentWidth,
	}
}

// resizeColumns rebuilds Location's width (the only one that depends on
// terminal width) against m.width, applying any of colOverrides first —
// see Model.colOverrides' own doc for why a fixed column might carry one
// — to whichever fixed columns have one, then giving Location whatever's
// left after every other column's own effective (possibly overridden)
// width is accounted for. This, not trellis, is where Location's role as
// the one column that absorbs a *terminal* resize's leftover space
// actually lives — a policy entirely separate from how a mouse drag
// divides width between two columns (trellis.Model.Handle's own doc),
// which no longer treats Location specially in any way.
//
// Location's floor is 20 whenever there's genuinely room for it, but on
// a terminal too narrow for even that it dips below the floor (down to
// 8) rather than pushing the table past the terminal's right edge — a
// wider-than-terminal table just gets its rightmost columns (Kind, PID)
// clipped away entirely, which is worse than a truncated Location. Past
// 8 the terminal is simply too narrow for nine columns; the remaining
// overflow is accepted.
func (m *Model) resizeColumns() {
	cols := m.table.Columns()
	if len(cols) != 9 {
		return
	}
	fixed := 0
	for i := range cols {
		if i == colLocation {
			continue
		}
		if w, ok := m.colOverrides[i]; ok {
			cols[i].Width = w
		}
		fixed += cols[i].Width
	}
	remaining := m.width - fixed - 18 // 2 chars of padding per cell, 9 cells
	if remaining < 20 {
		remaining = max(remaining, 8)
	}
	cols[colLocation].Width = remaining
	m.table.SetColumns(cols)
}

// renderHeader builds the header block (title, plus an optional summary
// line and scan-warning banner) and reports how many terminal rows
// precede the table's own header row: the header block's own line count,
// plus the blank separator line View always inserts before the table.
// View and the tea.MouseMsg case in Update both need exactly this — View
// to render the text, mouse handling to know whether a click landed on
// the table's own header row (see trellis.Model.Handle's doc) — so both
// call this one helper rather than keeping two copies of the same
// line-counting logic in sync by hand.
func (m Model) renderHeader() (text string, tableOriginY int) {
	text = titleStyle.Render("canopy") + subtleStyle.Render(" — agent sessions on this machine")
	lines := 1
	if summary := summaryLine(m.entries, m.done); summary != "" {
		text += "\n" + summary
		lines++
	}
	if m.scanWarning != "" {
		// Persists across polls for as long as the underlying condition
		// does (see Model.scanWarning's own doc comment), unlike the
		// footer's notification, which auto-clears after notifyDuration.
		text += "\n" + errorStyle.Render("⚠ "+m.scanWarning)
		lines++
	}
	return text, lines + 1 // +1 for the blank separator line View puts before the table
}

// helpEntries is the ? overlay's content: every binding Update's
// KeyMsg case handles, the inherited bubbles/table navigation set, and
// the one mouse-only interaction (column-border drag), since that one
// has no key to list under. The navigation entries, the mouse row, the
// close hint, and the title are deliberately identical to understory's
// own overlay, and the rendering itself is loam.HelpView in both (the
// two dashboards share one set of conventions); only the action rows in
// the middle differ, being domain-specific.
var helpEntries = []loam.HelpBinding{
	{Key: "↑/↓, k/j", Desc: "move the selection"},
	{Key: "pgup/pgdn, b/f", Desc: "page up/down"},
	{Key: "u/d", Desc: "half page up/down (lowercase d; the uppercase D below terminates every done session)"},
	{Key: "g/G, home/end", Desc: "jump to the top/bottom"},
	{Key: "enter", Desc: "jump to the session's window (dismisses a done row)"},
	{Key: "c / C", Desc: "dismiss this done row / every done row"},
	{Key: "x / X", Desc: "terminate (SIGTERM) / force-kill (SIGKILL) the selected session, with confirmation"},
	{Key: "p", Desc: "pause (SIGSTOP) / resume (SIGCONT) the selected session"},
	{Key: "D", Desc: "terminate every done session (SIGTERM), with confirmation"},
	{Key: "r", Desc: "refresh now"},
	{Key: "mouse", Desc: "drag a column border on the header row to resize the two columns it joins"},
	{Key: "?", Desc: "this help"},
	{Key: "q, ctrl+c", Desc: "quit"},
}

// helpView renders the ? overlay's body: the full keybinding list in
// place of the table, with the header staying visible above it and the
// footer carrying the close hint below (the same layout understory's own
// overlay uses), delegating the actual layout to loam.HelpView.
func (m Model) helpView() string {
	return loam.HelpView("keybindings", helpEntries, titleStyle, subtleStyle)
}

// footerView renders the bottom line: the confirmation prompt while one
// is armed (it's modal and swallows all other keys, so it replaces
// everything else), else the latest notification, else the help overlay's
// close hint, else the default keybinding hints, kept to the essentials
// now that ? opens the full list.
func (m Model) footerView() string {
	if m.pendingKill.Active() {
		style := promptStyle
		if m.pendingKill.Payload.sig == syscall.SIGKILL {
			style = errorStyle
		}
		return style.Render(m.killPromptText())
	}
	if m.notification != "" {
		style := okStyle
		if m.notifyIsError {
			style = errorStyle
		}
		return style.Render(m.notification)
	}
	if m.showHelp {
		return subtleStyle.Render("press any key to close")
	}
	return subtleStyle.Render("↑/↓ move · enter jump · c dismiss · x kill · ? help · q quit")
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	header, _ := m.renderHeader()

	body := m.helpView()
	if !m.showHelp {
		tableView := colorizeRows(m.table.View(), m.table.Columns(), colState, colSince)
		// Marks each column border on the header row with a visible divider
		// (see loam.DrawHeaderBorders' own doc) — otherwise the only cue for
		// where a mouse drag needs to land is bubbles/table's own blank
		// 2-space inter-cell gap, which doesn't look any different from the
		// padding inside a cell. Runs after colorizeRows, not before: the
		// header line is the one line ColorizeRows never touches at all (see
		// its own doc), so the two passes can run in either order without
		// interfering with each other; this order just keeps "recolor first,
		// mark structure second" consistent regardless.
		body = loam.DrawHeaderBorders(tableView, m.table.Columns(), subtleStyle)
	}
	return header + "\n\n" + body + "\n\n" + m.footerView() + "\n"
}

// Run starts the dashboard program and blocks until the user quits.
func Run(interval time.Duration, bellEnabled bool) error {
	// WithReportFocus so the tea.FocusMsg case in Update ever fires at all:
	// without it the terminal never sends focus-in events. Terminals that
	// don't support focus reporting simply never deliver the message, which
	// degrades to tick-only behavior.
	p := tea.NewProgram(New(interval).WithBell(bellEnabled), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	_, err := p.Run()
	return err
}
