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
	"os"
	"os/user"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/luiul/canopy/internal/jump"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/loam"
	"github.com/luiul/dashkit/trellis"
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
type clearNotifyMsg struct{ token int }

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
	// github.com/luiul/dashkit/trellis); colOverrides remembers the resulting width
	// of whichever column(s) the user has actually dragged, by column
	// index (see the Column indexes above), so resizeColumns' own recompute
	// on every terminal resize doesn't silently discard an unrelated
	// column's earlier resize. Cleared whenever a WindowSizeMsg arrives
	// (see Update): a genuinely new terminal width invalidates the old
	// distribution of space entirely, so resizeColumns starts fresh rather
	// than fighting stale overrides sized for a different width.
	colOverrides map[int]int
	resizer      trellis.Model

	// bellEnabled gates the terminal-bell side effect in applyEntries/Update
	// (see needsBell in bell.go): on by default (set in New), off via
	// --no-bell (see cmd/canopy) for anyone who finds an audible alert
	// intrusive. Coloring and done's blinking (colorize.go, stateCellText)
	// happen regardless of this flag.
	bellEnabled bool

	width, height int
	quitting      bool
}

// New builds the dashboard model, polling at interval.
func New(interval time.Duration) Model {
	columns := []table.Column{
		{Title: "State", Width: 9},
		{Title: "Since", Width: 6},
		{Title: "Surface", Width: 9},
		{Title: "Location", Width: 40},
		{Title: "CPU", Width: 4},
		{Title: "RAM", Width: 6},
		{Title: "Uptime", Width: 6},
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
		_, originY := m.renderHeader()
		cols := m.table.Columns()
		widths, changed := m.resizer.Handle(msg, cols, columnMinWidths(), colLocation, 0, originY)
		if changed {
			if m.colOverrides == nil {
				m.colOverrides = map[int]int{}
			}
			m.colOverrides[m.resizer.DragColumn()] = widths[m.resizer.DragColumn()]
			m.table.SetColumns(trellis.Apply(cols, widths))
		}
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
			m.refreshCursorTag()
			return m, cmd
		}

	case tickMsg:
		return m, tea.Batch(pollCmd(m.user, m.entries), tickCmd(m.interval))

	case pollResultMsg:
		m.scanWarning = msg.warning
		bell := m.applyEntries(msg.entries)
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

// columnMinWidths returns each column's own minimum width, in the same
// order/index New builds them (see the Column indexes above), for
// trellis' mouse-resize handling (see Update's tea.MouseMsg case): every
// fixed column floors at its own current default width, since every
// value it ever shows already fits exactly there (State/Kind's own
// longest words, Since/CPU/RAM/Uptime/PID's own widest realistic
// numbers) and shrinking further would only truncate it; Location floors
// at 20, the same floor resizeColumns' own leftover-space computation
// already respects.
func columnMinWidths() []int {
	return []int{9, 6, 9, 20, 4, 6, 6, 7, 6}
}

// resizeColumns rebuilds Location's width (the only one that depends on
// terminal width) against m.width, applying any of colOverrides first —
// see Model.colOverrides' own doc for why a fixed column might carry one
// — to whichever fixed columns have one, then giving Location whatever's
// left after every other column's own effective (possibly overridden)
// width is accounted for. That's the same invariant a mouse drag itself
// already keeps (see trellis.Model.Handle's doc): the table's total width
// never changes, no matter which column a user actually resized.
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
		remaining = 20
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

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	header, _ := m.renderHeader()

	footer := subtleStyle.Render("↑/↓ move · enter jump · c complete · drag column border to resize · r refresh · q quit")
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
	p := tea.NewProgram(New(interval).WithBell(bellEnabled), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
