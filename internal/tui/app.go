// Package tui is canopy's interactive dashboard: a table of every known
// agent-kind process on this machine, polled on a timer, with jump-to-window
// on Enter.
package tui

import (
	"fmt"
	"os/user"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/luiul/canopy-go/internal/ancestry"
	"github.com/luiul/canopy-go/internal/jump"
	"github.com/luiul/canopy-go/internal/registry"
)

// DefaultInterval is the poll interval used when none is given.
const DefaultInterval = 2 * time.Second

const notifyDuration = 4 * time.Second

var (
	surfaceLabels = map[ancestry.Surface]string{
		ancestry.Herdr:   "herdr",
		ancestry.VSCode:  "VS Code",
		ancestry.Ghostty: "Ghostty",
		ancestry.Unknown: "unknown",
	}

	titleStyle  = lipgloss.NewStyle().Bold(true)
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

func location(e registry.RegistryEntry) string {
	if e.Surface == ancestry.Herdr && e.WorkspaceID != "" {
		return "herdr:" + e.WorkspaceID
	}
	if e.Cwd != "" {
		return e.Cwd
	}
	return "?"
}

// sortEntries orders working agents first (most likely to need attention or
// be the one you're looking for), then grouped by surface, then stable by
// pid.
func sortEntries(entries []registry.RegistryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		aWorking, bWorking := a.State != "working", b.State != "working" // false (0) sorts first
		if aWorking != bWorking {
			return !aWorking
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
		{Title: "Kind", Width: 10},
		{Title: "PID", Width: 8},
		{Title: "Surface", Width: 9},
		{Title: "State", Width: 9},
		{Title: "Location", Width: 40},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(15),
	)
	styles := table.DefaultStyles()
	styles.Selected = styles.Selected.Bold(true)
	t.SetStyles(styles)

	return Model{
		interval: interval,
		user:     currentUser(),
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

	rows := make([]table.Row, 0, len(fresh))
	if len(fresh) == 0 {
		rows = append(rows, table.Row{"no known agent-kind processes found on this machine", "", "", "", ""})
	}
	for _, e := range fresh {
		rows = append(rows, table.Row{
			e.Kind,
			fmt.Sprintf("%d", e.Pid),
			surfaceLabel(e.Surface),
			e.State,
			location(e),
		})
	}
	m.table.SetRows(rows)

	if previousKey != "" {
		for i, e := range fresh {
			if e.Key() == previousKey {
				m.table.SetCursor(i)
				break
			}
		}
	}
}

func surfaceLabel(s ancestry.Surface) string {
	if label, ok := surfaceLabels[s]; ok {
		return label
	}
	return string(s)
}

func (m *Model) resizeColumns() {
	cols := m.table.Columns()
	if len(cols) != 5 {
		return
	}
	fixed := cols[0].Width + cols[1].Width + cols[2].Width + cols[3].Width
	remaining := m.width - fixed - 10
	if remaining < 20 {
		remaining = 20
	}
	cols[4].Width = remaining
	m.table.SetColumns(cols)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	header := titleStyle.Render("canopy") + subtleStyle.Render(" — agent sessions on this machine")

	footer := subtleStyle.Render("↑/↓ move · enter jump · r refresh · q quit")
	if m.notification != "" {
		style := okStyle
		if m.notifyIsError {
			style = errorStyle
		}
		footer = style.Render(m.notification)
	}

	return header + "\n\n" + m.table.View() + "\n\n" + footer + "\n"
}

// Run starts the dashboard program and blocks until the user quits.
func Run(interval time.Duration) error {
	p := tea.NewProgram(New(interval), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
