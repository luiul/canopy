package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/loam"
	"github.com/luiul/dashkit/trellis"
)

func TestColumnOrderKeepsAgentIdentityBeforeLocationAndResources(t *testing.T) {
	m := New(time.Second, nil)
	var titles []string
	for _, c := range m.table.Columns() {
		titles = append(titles, c.Title)
	}
	want := []string{"State", "Since", "Kind", "Model", "Surface", "Location", "CPU", "RAM", "Uptime", "PID"}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("column order = %v, want %v", titles, want)
	}
	e := entry(54321, ancestry.VSCode, "working")
	e.Kind, e.ModelName, e.ModelProvider = "pig", "GPT-6.1 Sol (US)", "amazon-bedrock"
	m.applyEntries([]registry.RegistryEntry{e})
	row := m.table.Rows()[0]
	checks := map[int]string{
		colState: "working", colKind: "pig", colModel: modelCellText(e),
		colSurface: "VS Code", colLocation: location(e, m.home),
		colCPU: cpuCellText(e), colRAM: ramCellText(e), colUptime: uptimeCellText(e), colPID: "54321",
	}
	for i, want := range checks {
		if got := row[i]; got != want {
			t.Errorf("%s cell = %q, want %q", titles[i], got, want)
		}
	}
}

func layoutWidth(cols []table.Column) int {
	total := loam.CellPadding * len(cols)
	for _, c := range cols {
		total += c.Width
	}
	return total
}

func sendLayoutMouse(m Model, x, y int, action tea.MouseAction, button tea.MouseButton) Model {
	updated, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: action, Button: button})
	return updated.(Model)
}

func TestModelWidthFitsReportedLabelsAtNormalTerminalWidths(t *testing.T) {
	for _, width := range []int{120, 140, 180, 240} {
		for _, model := range []struct{ name, provider string }{
			{"GPT-6.1 Sol", "ai-model-router"},
			{"GPT-6.1 Sol (US)", "amazon-bedrock"},
			{"模型模型模型模型模型模型模型模型", "provider"},
		} {
			t.Run(fmt.Sprintf("%d/%s", width, model.name), func(t *testing.T) {
				m := New(time.Second, nil)
				m.width, m.height = width, 35
				e := entry(54321, ancestry.VSCode, "working")
				e.Cwd = m.home + "/" + strings.Repeat("long-path/", 20)
				e.ModelName, e.ModelProvider = model.name, model.provider
				m.applyEntries([]registry.RegistryEntry{e})
				label := modelCellText(e)
				if !strings.Contains(m.View(), label) {
					t.Fatalf("path starved useful model %q: %q", label, m.View())
				}
				if got := layoutWidth(m.table.Columns()); got != width {
					t.Fatalf("table width = %d, want %d", got, width)
				}
				if !strings.Contains(m.View(), "54321") {
					t.Fatalf("View clipped final PID: %q", m.View())
				}
			})
		}
	}
}

func TestFullWidthLayoutAndReadableBoundaries(t *testing.T) {
	hard := loam.CellPadding * len(columnMinWidths())
	for _, floor := range columnMinWidths() {
		hard += floor
	}
	for _, width := range []int{hard - 1, hard, hard + 1, 100, 110, 120, 140, 180, 240} {
		for _, populated := range []bool{false, true} {
			m := New(time.Second, nil)
			m.width, m.height = width, 35
			if populated {
				e := entry(54321, ancestry.VSCode, "working")
				e.ModelName, e.ModelProvider = strings.Repeat("模型", 40), "long-provider"
				e.Cwd = m.home + "/" + strings.Repeat("路径/", 40)
				m.applyEntries([]registry.RegistryEntry{e})
			} else {
				m.resetRows("")
			}
			if m.tooNarrow != (width < hard) {
				t.Fatalf("width %d: tooNarrow %v, hard %d", width, m.tooNarrow, hard)
			}
			want := max(width, hard)
			if got := layoutWidth(m.table.Columns()); got != want {
				t.Fatalf("width %d: table %d want %d", width, got, want)
			}
			if m.tooNarrow && !strings.Contains(m.View(), "terminal too narrow") {
				t.Fatal("missing narrow notice")
			}
			for i, c := range m.table.Columns() {
				if c.Width < columnMinWidths()[i] {
					t.Fatalf("column %s below floor", c.Title)
				}
			}
			view := loam.DrawHeaderBorders(m.table.View(), m.table.Columns(), subtleStyle)
			lines := strings.Split(view, "\n")
			if got := ansi.StringWidth(lines[0]); got != want {
				t.Fatalf("width %d: rendered header width %d", width, got)
			}
			if width >= hard && strings.Count(lines[0], loam.BorderGlyph) != colPID {
				t.Fatalf("width %d: missing header dividers", width)
			}
		}
	}
}

func TestCompactFieldsStayCompactAndBothTextColumnsGrow(t *testing.T) {
	m := New(time.Second, nil)
	m.width = 180
	m.resizeColumns()
	first := append([]table.Column(nil), m.table.Columns()...)
	m.width = 240
	m.resizeColumns()
	for i, c := range m.table.Columns() {
		if i == colModel || i == colLocation {
			if c.Width-first[i].Width != 30 {
				t.Fatalf("equal surplus weights: %s grew %d", c.Title, c.Width-first[i].Width)
			}
		} else if c.Width != defaultColumns()[i].Width {
			t.Fatalf("compact %s grew to %d", c.Title, c.Width)
		}
	}
	if m.columnPolicies()[colModel].Preferred != modelContentWidth+1 {
		t.Fatal("empty placeholder drove preferred width")
	}
}

func TestCompactContentFitsLargeNumericSamplesWithoutBlankGrowth(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 240, 35
	e := entry(123456, ancestry.VSCode, "working")
	e.CPUPercent, e.RSSKb = 1234, 12345*1024*1024
	e.StateSince = time.Now().Add(-23*time.Hour - 59*time.Minute)
	e.Uptime = 23*time.Hour + 59*time.Minute
	m.applyEntries([]registry.RegistryEntry{e})
	for i, label := range map[int]string{
		colCPU: "1234%", colRAM: ramCellText(e), colPID: "123456", colSince: "23h59m", colUptime: "23h59m",
	} {
		want := max(defaultColumns()[i].Width, trellis.ContentWidth(label))
		if got := m.table.Columns()[i].Width; got != want {
			t.Fatalf("compact %s = %d, want fitted content %d", defaultColumns()[i].Title, got, want)
		}
	}
}

func TestAutomaticPollRefitsButManualPollKeepsProportions(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 180, 35
	e := entry(54321, ancestry.Ghostty, "working")
	m.applyEntries([]registry.RegistryEntry{e})
	initial := m.table.Columns()[colModel].Width
	e.ModelName, e.ModelProvider = strings.Repeat("model", 10), "provider"
	m.applyEntries([]registry.RegistryEntry{e})
	if m.table.Columns()[colModel].Width <= initial {
		t.Fatal("automatic poll did not refit longer model")
	}
	cols := m.table.Columns()
	_, originY := m.renderHeader()
	border := modelBorderX(cols)
	m = sendLayoutMouse(m, border, originY, tea.MouseActionPress, tea.MouseButtonLeft)
	m = sendLayoutMouse(m, border-3, originY, tea.MouseActionMotion, tea.MouseButtonLeft)
	m = sendLayoutMouse(m, border-3, originY, tea.MouseActionRelease, tea.MouseButtonLeft)
	want := append([]table.Column(nil), m.table.Columns()...)
	e.ModelName = strings.Repeat("long-model", 30)
	e.Cwd = strings.Repeat("/long-path", 30)
	m.applyEntries([]registry.RegistryEntry{e})
	if !reflect.DeepEqual(m.table.Columns(), want) {
		t.Fatalf("manual poll moved borders: %v want %v", m.table.Columns(), want)
	}
	for range 10 {
		for _, width := range []int{100, 70, 240, 180} {
			updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 35})
			m = updated.(Model)
		}
		if !reflect.DeepEqual(m.table.Columns(), want) {
			t.Fatalf("resize cycle drift: %v want %v", m.table.Columns(), want)
		}
	}
}

func TestActiveGestureFreezesPollAndEndsOnReleaseOrLostButton(t *testing.T) {
	for _, button := range []tea.MouseButton{tea.MouseButtonLeft, tea.MouseButtonNone} {
		m := New(time.Second, nil)
		m.width, m.height = 180, 35
		m.resizeColumns()
		_, originY := m.renderHeader()
		border := modelBorderX(m.table.Columns())
		m = sendLayoutMouse(m, border, originY, tea.MouseActionPress, tea.MouseButtonLeft)
		want := append([]table.Column(nil), m.table.Columns()...)
		e := entry(54321, ancestry.Ghostty, "working")
		e.ModelName, e.ModelProvider = strings.Repeat("long-model", 30), "provider"
		m.applyEntries([]registry.RegistryEntry{e})
		if !reflect.DeepEqual(m.table.Columns(), want) || len(m.table.Rows()) != 1 {
			t.Fatal("poll must update rows with frozen geometry")
		}
		action := tea.MouseActionRelease
		if button == tea.MouseButtonNone {
			action = tea.MouseActionMotion
		}
		m = sendLayoutMouse(m, border, originY, action, button)
		if m.resizer.Dragging() || reflect.DeepEqual(m.table.Columns(), want) {
			t.Fatal("gesture end did not allocate fresh content")
		}
	}
}

func TestWidthChangeCancelsDragButKeepsLatestPreferences(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 180, 35
	m.resizeColumns()
	_, y := m.renderHeader()
	x := surfaceBorderX(m.table.Columns())
	m = sendLayoutMouse(m, x, y, tea.MouseActionPress, tea.MouseButtonLeft)
	m = sendLayoutMouse(m, x+4, y, tea.MouseActionMotion, tea.MouseButtonLeft)
	want := append([]table.Column(nil), m.table.Columns()...)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	m = updated.(Model)
	if m.resizer.Dragging() || !m.preferences.Manual() {
		t.Fatal("width change must cancel drag, not preferences")
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 180, Height: 35})
	m = updated.(Model)
	if !reflect.DeepEqual(m.table.Columns(), want) {
		t.Fatal("width change lost latest motion")
	}
}

func TestNoOpMouseMotionNeverSelectsManualLayout(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 180, 35
	m.resizeColumns()
	x := loam.ColumnOffsets(m.table.Columns())[colCPU].Start + m.table.Columns()[colCPU].Width
	_, y := m.renderHeader()
	m = sendLayoutMouse(m, x, y, tea.MouseActionPress, tea.MouseButtonLeft)
	m = sendLayoutMouse(m, x, y, tea.MouseActionMotion, tea.MouseButtonLeft)
	m = sendLayoutMouse(m, x+20, y, tea.MouseActionMotion, tea.MouseButtonLeft)
	m = sendLayoutMouse(m, x+20, y, tea.MouseActionRelease, tea.MouseButtonLeft)
	if m.preferences.Manual() {
		t.Fatal("zero-delta or fully clamped motion captured preferences")
	}
}

func TestModalOpeningSettlesAnExistingGesture(t *testing.T) {
	for _, key := range []string{"?", "x"} {
		m := New(time.Second, nil)
		m.width, m.height = 180, 35
		e := entry(54321, ancestry.VSCode, "working")
		m.applyEntries([]registry.RegistryEntry{e})
		_, y := m.renderHeader()
		x := modelBorderX(m.table.Columns())
		m = sendLayoutMouse(m, x, y, tea.MouseActionPress, tea.MouseButtonLeft)
		updated, _ := m.Update(keyMsg(key))
		m = updated.(Model)
		if m.resizer.Dragging() {
			t.Fatalf("%s left a gesture active behind modal", key)
		}
		m = sendLayoutMouse(m, x, y, tea.MouseActionRelease, tea.MouseButtonLeft)
		updated, _ = m.Update(keyMsg("esc"))
		m = updated.(Model)
		e.ModelName, e.ModelProvider = strings.Repeat("long-model", 20), "provider"
		before := m.table.Columns()[colModel].Width
		m.applyEntries([]registry.RegistryEntry{e})
		if m.table.Columns()[colModel].Width <= before {
			t.Fatalf("%s froze automatic sizing after modal closed", key)
		}
	}
}

func TestNarrowNoticeReservesHeightAndHeaderMouseOrigin(t *testing.T) {
	m := New(time.Second, nil)
	m.applyEntries([]registry.RegistryEntry{entry(54321, ancestry.VSCode, "working")})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 76, Height: 35})
	m = updated.(Model)
	if m.table.Height() != 27 { // 28 total table rows, including its header.
		t.Fatalf("narrow table height %d, want 27", m.table.Height())
	}
	if got := len(strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n")); got > 35 {
		t.Fatalf("narrow view has %d rows, exceeds terminal", got)
	}
	_, y := m.renderHeader()
	x := stateBorderX(m.table.Columns())
	m = sendLayoutMouse(m, x, y, tea.MouseActionPress, tea.MouseButtonLeft)
	if !m.resizer.Dragging() {
		t.Fatal("narrow visible header is not grabbable")
	}
}

func TestEveryCanopyBorderIsGrabbableAtFullWidth(t *testing.T) {
	for border := 0; border < colPID; border++ {
		m := New(time.Second, nil)
		m.width, m.height = 180, 35
		m.resizeColumns()
		cols := append([]table.Column(nil), m.table.Columns()...)
		offset := loam.ColumnOffsets(cols)[border]
		x := offset.Start + offset.Width
		_, y := m.renderHeader()
		m = sendLayoutMouse(m, x, y, tea.MouseActionPress, tea.MouseButtonLeft)
		if !m.resizer.Dragging() || m.resizer.DragColumn() != border {
			t.Fatalf("border %d not grabbable", border)
		}
		delta := 1
		if cols[border+1].Width == columnMinWidths()[border+1] {
			delta = -1
			if cols[border].Width == columnMinWidths()[border] {
				delta = 0 // Both readable floors are already funded exactly.
			}
		}
		m = sendLayoutMouse(m, x+delta, y, tea.MouseActionMotion, tea.MouseButtonLeft)
		for i, got := range m.table.Columns() {
			want := cols[i].Width
			switch i {
			case border:
				want += delta
			case border + 1:
				want -= delta
			}
			if got.Width != want {
				t.Fatalf("border %d column %d: got %d want %d", border, i, got.Width, want)
			}
		}
		m = sendLayoutMouse(m, x+delta, y, tea.MouseActionRelease, tea.MouseButtonLeft)
		if layoutWidth(m.table.Columns()) != 180 || m.resizer.Dragging() {
			t.Fatalf("border %d release changed total or left gesture active", border)
		}
	}
}

func TestFilterDoesNotMoveBordersAndHomeLabelUsesDisplayCells(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 180, 35
	m.home = "/Users/test"
	e := entry(54321, ancestry.VSCode, "working")
	e.Cwd = m.home + "/模型/path"
	e.ModelName, e.ModelProvider = "模型", "provider"
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.columnPolicies()[colLocation].Preferred; got != max(20, trellis.ContentWidth("~/模型/path")) {
		t.Fatalf("home label width = %d", got)
	}
	want := append([]table.Column(nil), m.table.Columns()...)
	for _, query := range []string{"", "not-matching", "模", "working", ""} {
		m.filterQuery = query
		m.resetRows("")
		if !reflect.DeepEqual(m.table.Columns(), want) {
			t.Fatal("filter changed geometry")
		}
	}
}
