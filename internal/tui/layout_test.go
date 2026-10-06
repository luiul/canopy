package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/loam"
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

func TestModelWidthFitsReportedLabelsAtNormalTerminalWidths(t *testing.T) {
	for _, width := range []int{120, 140, 180, 240} {
		for _, model := range []struct{ name, provider string }{
			{"GPT-6.1 Sol", "ai-model-router"},
			{"GPT-6.1 Sol (US)", "amazon-bedrock"},
			{"模型模型模型模型模型模型模型模型", "provider"},
		} {
			t.Run(fmt.Sprintf("%d/%s", width, model.name), func(t *testing.T) {
				m := New(time.Second, nil)
				updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 35})
				m = updated.(Model)
				e := entry(54321, ancestry.VSCode, "working")
				e.ModelName, e.ModelProvider = model.name, model.provider
				m.applyEntries([]registry.RegistryEntry{e})
				label := modelCellText(e)
				if !strings.Contains(m.View(), label) {
					t.Fatalf("View truncated %q: %q", label, m.View())
				}
				cols := m.table.Columns()
				if got, need := cols[colModel].Width, runewidth.StringWidth(label); got < need {
					t.Fatalf("Model width = %d, want at least %d to fit the label", got, need)
				}
				if cols[colLocation].Width > locationMaxWidth {
					t.Fatalf("Location width = %d, want <= %d", cols[colLocation].Width, locationMaxWidth)
				}
				for _, line := range strings.Split(m.table.View(), "\n") {
					if got := runewidth.StringWidth(line); got > width {
						t.Fatalf("rendered line width = %d, exceeds %d", got, width)
					}
				}
				if !strings.Contains(m.View(), "54321") {
					t.Fatalf("View clipped the final PID column: %q", m.View())
				}
			})
		}
	}
}

func TestModelWidthUpdatesWithPollsWithoutBreakingMouseOverrides(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 140, 35
	m.resizeColumns()
	e := entry(54321, ancestry.Ghostty, "working")
	e.ModelName, e.ModelProvider = "GPT-6.1 Sol (US)", "amazon-bedrock"
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.table.Columns()[colModel].Width; got != runewidth.StringWidth(modelCellText(e))+1 {
		t.Fatalf("Model did not grow on poll: width = %d", got)
	}

	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := modelBorderX(cols)
	for _, msg := range []tea.MouseMsg{
		{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
		{X: borderX - 3, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft},
		{X: borderX - 3, Y: originY, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	dragged := m.table.Columns()
	if dragged[colModel].Width != cols[colModel].Width-3 {
		t.Fatal("Model border drag did not resize Model")
	}
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.table.Columns(); !reflect.DeepEqual(got, dragged) {
		t.Fatalf("poll changed dragged widths: got %v, want %v", got, dragged)
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 35})
	m = updated.(Model)
	if got := m.table.Columns(); !reflect.DeepEqual(got, cols) {
		t.Fatalf("terminal resize did not reset the drag: got %v, want %v", got, cols)
	}
	e.ModelName, e.ModelProvider = "", ""
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.table.Columns()[colModel].Width; got != modelContentWidth+1 {
		t.Fatalf("Model width after metadata vanished = %d, want %d", got, modelContentWidth+1)
	}
}

func TestLocationMouseWidthSurvivesPollAboveAutomaticCap(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 180, 35
	m.resizeColumns()
	e := entry(54321, ancestry.VSCode, "working")
	m.applyEntries([]registry.RegistryEntry{e})
	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := surfaceBorderX(cols)
	for _, msg := range []tea.MouseMsg{
		{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
		{X: borderX - 1, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	m.applyEntries([]registry.RegistryEntry{e})
	if got := m.table.Columns()[colLocation].Width; got != locationMaxWidth+1 {
		t.Fatalf("Location width after poll = %d, want mouse override %d", got, locationMaxWidth+1)
	}
}

func TestLongModelLabelsStayWithinTableBudget(t *testing.T) {
	for width := 104; width <= 240; width++ {
		m := New(time.Second, nil)
		m.width, m.height = width, 35
		e := entry(54321, ancestry.VSCode, "working")
		e.ModelName, e.ModelProvider = strings.Repeat("very-long-model-", 8), "amazon-bedrock"
		m.applyEntries([]registry.RegistryEntry{e})
		cols := m.table.Columns()
		total := 2 * len(cols)
		for _, c := range cols {
			total += c.Width
		}
		if total > width {
			t.Fatalf("table width = %d, exceeds terminal width %d", total, width)
		}
		if got := cols[colModel].Width; got < modelContentWidth || got > modelMaxWidth {
			t.Fatalf("Model width = %d at terminal width %d, want %d..%d", got, width, modelContentWidth, modelMaxWidth)
		}
		if got := cols[colLocation].Width; got < locationHardFloor || got > locationMaxWidth {
			t.Fatalf("Location width = %d at terminal width %d, want %d..%d", got, width, locationHardFloor, locationMaxWidth)
		}
		if got := strings.Count(strings.Split(loam.DrawHeaderBorders(m.table.View(), cols, subtleStyle), "\n")[0], loam.BorderGlyph); got != len(cols)-1 {
			t.Fatalf("header borders = %d at terminal width %d, want %d", got, width, len(cols)-1)
		}
	}
}
