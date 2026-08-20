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
		{Title: "PID", Width: 6}, // narrow on purpose; truncates rare 6+ digit pids
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
		interval: interval,
		user:     currentUser(),
		home:     homeDir(),
		table:    t,
	}
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
		m.applyEntries(msg.entries)
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
// the same key-based cursor-preservation canopy's Python original does.
func (m *Model) applyEntries(fresh []registry.RegistryEntry) {
	var previousKey string
	if entry, ok := m.selectedEntry(); ok {
		previousKey = entry.Key()
	}

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
			stateCellText(e, now),
			sinceCellText(e, now),
			surfaceLabel(e.Surface),
			location(e, home),
			e.Kind,
			fmt.Sprintf("%d", e.Pid),
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
func Run(interval time.Duration) error {
	p := tea.NewProgram(New(interval), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
