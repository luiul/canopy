package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/luiul/dashkit/loam"
	"github.com/muesli/termenv"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

// withForcedColor forces lipgloss to emit real ANSI (tests otherwise run
// with stdout not a tty, which lipgloss auto-detects and downgrades to no
// color), restoring the original profile afterward so this doesn't leak
// into other tests.
func withForcedColor(t *testing.T) {
	t.Helper()
	original := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(original) })
}

// messagedEntry is entry plus a state payload (see
// registry.RegistryEntry.Message), e.g. the first line of an error for
// an "error" row or the dialog title for a "blocked" one.
func messagedEntry(pid int, surface ancestry.Surface, state, message string) registry.RegistryEntry {
	e := entry(pid, surface, state)
	e.Message = message
	return e
}

func TestRecolorStateStylesWordsAndBlinkMarker(t *testing.T) {
	withForcedColor(t)
	if got, want := recolorState("working"), stateStyle("working"); got.GetForeground() != want.GetForeground() {
		t.Fatalf("recolorState(working) = %v, want the working style", got)
	}
	plain := recolorState("done")
	blink := recolorState("done" + blinkMarker)
	if !blink.GetReverse() || plain.GetReverse() {
		t.Fatalf("blink-marked word must render reverse-video, unmarked must not")
	}
}

func TestTableViewColorsStateWordsAndSince(t *testing.T) {
	withForcedColor(t)
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.Ghostty, "idle"),
	})
	got := m.View()
	if want := stateStyle("working").Render("working"); !strings.Contains(got, want) {
		t.Fatalf("got %q, want it to contain the styled word %q", got, want)
	}
}

func TestTableViewInjectsMessageLineUnderItsRow(t *testing.T) {
	withForcedColor(t)
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	m.applyEntries([]registry.RegistryEntry{
		messagedEntry(1, ancestry.Ghostty, "error", "rate limit exceeded"),
		entry(2, ancestry.Ghostty, "working"),
	})
	lines := strings.Split(m.View(), "\n")
	rowAt, msgAt := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "error") && !strings.Contains(line, "rate limit") {
			rowAt = i
		}
		if strings.Contains(line, "rate limit exceeded") {
			msgAt = i
		}
	}
	if rowAt < 0 || msgAt != rowAt+1 {
		t.Fatalf("message line must directly follow its row (row %d, message %d):\n%s", rowAt, msgAt, m.View())
	}
	if !strings.Contains(lines[msgAt], strings.TrimSpace(messageIndent)) {
		t.Fatalf("message line %q lacks the indent glyph", lines[msgAt])
	}
	if want := messageStyle("error").Render("rate limit exceeded"); !strings.Contains(lines[msgAt], want) {
		t.Fatalf("message line %q lacks the state tint %q", lines[msgAt], want)
	}
}

func TestTableViewHighlightsCursorRowAndItsMessageLine(t *testing.T) {
	withForcedColor(t)
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		messagedEntry(2, ancestry.Ghostty, "blocked", "Allow Bash: go test ./...?"),
	})
	// blocked sorts first; move the cursor onto it.
	m.table.SetCursor(0)
	m.refreshCursorTag()

	open, closeSeq := loam.StyleSequences(rowHighlightStyle)
	if open == "" {
		t.Fatal("StyleSequences returned no escape codes; withForcedColor isn't taking effect")
	}
	var rowLine, msgLine string
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "blocked") && !strings.Contains(line, "Allow Bash") {
			rowLine = line
		}
		if strings.Contains(line, "Allow Bash") {
			msgLine = line
		}
	}
	for name, line := range map[string]string{"row": rowLine, "message": msgLine} {
		if line == "" {
			t.Fatalf("%s line not found in:\n%s", name, m.View())
		}
		if !strings.HasPrefix(line, open) || !strings.HasSuffix(line, closeSeq) {
			t.Fatalf("%s line %q must be wrapped start-to-end in the highlight's open/close sequences", name, line)
		}
	}
	if strings.Contains(m.View(), cursorSentinel) {
		t.Fatal("cursorSentinel leaked into the final output")
	}
}

func TestTableViewKeepsMessagesOnTheirRowsWhenScrolled(t *testing.T) {
	m := New(time.Second, nil)
	m.width, m.height = 150, 12 // small window: not all rows fit
	var entries []registry.RegistryEntry
	for i := 0; i < 10; i++ {
		entries = append(entries, entry(i+1, ancestry.Ghostty, "working"))
	}
	// One message deep in the list: its line must stay glued to its row
	// even when the window starts above row 0.
	entries[7].Message = "scroll-target-session"
	m.applyEntries(entries)
	m.table.SetCursor(9)
	m.refreshCursorTag()

	lines := strings.Split(m.View(), "\n")
	rowAt, msgAt := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "8") && strings.Contains(line, "working") {
			rowAt = i // the pid-8 row (entry index 7)
		}
		if strings.Contains(line, "scroll-target-session") {
			msgAt = i
		}
	}
	if msgAt < 0 {
		t.Fatalf("scrolled-off message should be visible with its row:\n%s", m.View())
	}
	if rowAt < 0 || msgAt != rowAt+1 {
		t.Fatalf("message must directly follow its row when scrolled (row %d, message %d):\n%s", rowAt, msgAt, m.View())
	}
}
