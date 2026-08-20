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
const (
	colCursor = iota
	colSurface
	colState
	colSince
	colKind
	colPID
	colLocation
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

// statePriority ranks states by how much attention they need: blocked
// (waiting on you right now) and done (finished, ready to check) rank
// highest, then working (busy, nothing for you to do), then idle, then
// unknown (heuristic couldn't tell).
var statePriority = map[string]int{
	"blocked": 0,
	"done":    1,
	"working": 2,
	"idle":    3,
	"unknown": 4,
}

// stateOrder is statePriority's states in display order, used for the
// header's per-state summary counts too.
var stateOrder = []string{"blocked", "done", "working", "idle", "unknown"}

func statePriorityOf(state string) int {
	if p, ok := statePriority[state]; ok {
		return p
	}
	return len(statePriority) // a state outside the known vocabulary sorts last
}

// sortEntries orders entries by statePriority, most actionable first, then
// grouped by surface, then stable by pid.
func sortEntries(entries []registry.RegistryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if pa, pb := statePriorityOf(a.State), statePriorityOf(b.State); pa != pb {
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

type tickMsg struct{}
type pollResultMsg struct{ entries []registry.RegistryEntry }
type jumpResultMsg struct{ result jump.Result }
type clearNotifyMsg struct{ token int }

// Model is the bubbletea model backing the dashboard.
type Model struct {
	interval time.Duration
	user     string
	home     string

	entries []registry.RegistryEntry // sorted, parallel to the table's real rows
	table   table.Model

	notification  string
	notifyIsError bool
	notifyToken   int

	// bellEnabled gates the terminal-bell side effect in applyEntries/Update
	// (see needsBell): on by default (set in New), off via --no-bell (see
	// cmd/canopy) for anyone who finds an audible alert intrusive. Coloring
	// and flashing in colorize.go happen regardless of this flag.
	bellEnabled bool

	width, height int
	quitting      bool
}

// New builds the dashboard model, polling at interval.
func New(interval time.Duration) Model {
	columns := []table.Column{
		{Title: "", Width: 1}, // cursorMarker
		{Title: "Surface", Width: 9},
		{Title: "State", Width: 9},
		{Title: "Since", Width: 6},
		{Title: "Kind", Width: 10},
		{Title: "PID", Width: 8},
		{Title: "Location", Width: 40},
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
			return m, jumpCmd(entry)
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
		if bell && m.bellEnabled {
			return m, bellCmd()
		}
		return m, nil

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

	bell := needsBell(m.entries, fresh)

	sortEntries(fresh)
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

	m.table.SetRows(buildRows(fresh, cursor, m.home, time.Now()))
	m.table.SetCursor(cursor)
	return bell
}

// needsAttention is the two states worth ringing the bell for: the same
// pair colorize.go flashes and sortEntries ranks above working/idle/
// unknown (see statePriority) — blocked (needs you right now) and done
// (finished, ready to check).
func needsAttention(state string) bool {
	return state == "blocked" || state == "done"
}

// needsBell reports whether any entry in fresh newly needs attention
// compared to previous: either a brand new entry that's already blocked or
// done the first time canopy sees it, or an existing one whose State just
// flipped into one of those from something else. An entry that was already
// blocked/done last poll and still is doesn't re-trigger it — otherwise a
// session sitting blocked for an hour would ring the bell on every single
// poll for that whole hour, drowning out the one moment that actually
// mattered: the transition itself.
func needsBell(previous, fresh []registry.RegistryEntry) bool {
	prevState := make(map[string]string, len(previous))
	for _, p := range previous {
		prevState[p.Key()] = p.State
	}
	for _, f := range fresh {
		if !needsAttention(f.State) {
			continue
		}
		if was, ok := prevState[f.Key()]; ok && needsAttention(was) {
			continue
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
// keys, page up/down, etc.), instead of waiting for the next poll.
func (m *Model) refreshCursorMarker() {
	if len(m.entries) == 0 {
		return
	}
	m.table.SetRows(buildRows(m.entries, m.table.Cursor(), m.home, time.Now()))
}

// buildRows constructs the table's rows from already-sorted entries.
// cursor picks which row's leading cell carries cursorMarker; it's a plain
// parameter (rather than read from the table itself) so this same helper
// builds rows both right after a poll (applyEntries) and on every cursor
// move in between polls (refreshCursorMarker), so the arrow tracks the
// highlighted row immediately rather than only once every poll interval.
func buildRows(entries []registry.RegistryEntry, cursor int, home string, now time.Time) []table.Row {
	if len(entries) == 0 {
		return []table.Row{{"", "", "", "", "", "", "no known agent-kind processes found on this machine"}}
	}
	rows := make([]table.Row, len(entries))
	for i, e := range entries {
		marker := ""
		if i == cursor {
			marker = cursorMarker
		}
		rows[i] = table.Row{
			marker,
			surfaceLabel(e.Surface),
			stateCellText(e, now),
			sinceCellText(e, now),
			e.Kind,
			fmt.Sprintf("%d", e.Pid),
			location(e, home),
		}
	}
	return rows
}

// flashDuration is how long a row that just transitioned into blocked or
// done carries flashMarker and its reverse-video highlight, independent of
// the poll interval so it stays legible whether polling every second or
// every ten.
const flashDuration = 8 * time.Second

// stateCellText is the State column's plain-text cell value: the state
// word, with a trailing flashMarker if it just transitioned into blocked or
// done (the two states worth calling out) within flashDuration. Actual
// coloring happens later, in View, by post-processing the rendered table
// (see colorize.go).
func stateCellText(e registry.RegistryEntry, now time.Time) string {
	if (e.State == "blocked" || e.State == "done") && !e.StateSince.IsZero() && now.Sub(e.StateSince) < flashDuration {
		return e.State + flashMarker
	}
	return e.State
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

// summaryLine is a one-line "N sessions: N blocked · N working · ..."
// breakdown, colored to match the State column and ordered the same way
// (most actionable first), skipping any state with a zero count. Empty
// when there are no entries, since the placeholder row already says so.
func summaryLine(entries []registry.RegistryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.State]++
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
	fixed := cols[colCursor].Width + cols[colSurface].Width + cols[colState].Width + cols[colSince].Width + cols[colKind].Width + cols[colPID].Width
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
	if summary := summaryLine(m.entries); summary != "" {
		header += "\n" + summary
	}

	footer := subtleStyle.Render("↑/↓ move · enter jump · r refresh · q quit")
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
