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

func TestSinceStyleSharesStateHueWithoutBold(t *testing.T) {
	withForcedColor(t)
	for _, state := range []string{"blocked", "error", "done", "working", "idle", "stopped", "unknown"} {
		got, want := sinceStyle(state), stateStyle(state)
		if got.GetForeground() != want.GetForeground() {
			t.Fatalf("sinceStyle(%s) foreground = %v, want stateStyle's %v (the two columns read as one block)", state, got.GetForeground(), want.GetForeground())
		}
		if got.GetBold() {
			t.Fatalf("sinceStyle(%s) must never be bold: bold stays reserved for the done/error State words", state)
		}
	}
	if got := sinceStyle("bogus").Render("x"); got != "x" {
		t.Fatalf("sinceStyle must fall back to the zero style for a state outside the vocabulary, got %q", got)
	}
}

func TestIsQuietPinsTheQuietTier(t *testing.T) {
	for _, s := range []string{"idle", "unknown"} {
		if !isQuiet(s) {
			t.Fatalf("%s must be quiet (it never needs attention)", s)
		}
	}
	for _, s := range []string{"blocked", "error", "done", "working", "stopped"} {
		if isQuiet(s) {
			t.Fatalf("%s must NOT be quiet: stopped deliberately stays bright, a forgotten paused session is a zombie", s)
		}
	}
}

func TestTableViewColorsSinceByState(t *testing.T) {
	withForcedColor(t)
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	working := entry(1, ancestry.Ghostty, "working")
	working.StateSince = time.Now().Add(-72 * time.Hour) // renders "3d", stable across the test
	idle := entry(2, ancestry.Ghostty, "idle")
	idle.StateSince = time.Now().Add(-72 * time.Hour)
	m.applyEntries([]registry.RegistryEntry{working, idle})
	// "3d" appears in no other cell (Uptime is empty for these entries),
	// and the Since cell is the only place it renders in the state's hue.
	if got, want := m.View(), sinceStyle("working").Render("3d"); !strings.Contains(got, want) {
		t.Fatalf("got %q, want the working row's Since cell rendered as %q", got, want)
	}
}

func TestTableViewDimsQuietRowsAndKeepsTheirStateWordBright(t *testing.T) {
	withForcedColor(t)
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "working"),
		entry(3, ancestry.Ghostty, "unknown"),
		entry(4, ancestry.Ghostty, "stopped"),
	})
	quietOpen, quietClose := loam.StyleSequences(quietRowStyle)
	if quietOpen == "" {
		t.Fatal("StyleSequences returned nothing; withForcedColor isn't taking effect")
	}
	// Data lines, not the summary line: they alone carry the location.
	lines := map[string]string{}
	for _, line := range strings.Split(m.View(), "\n") {
		if !strings.Contains(line, "/Users/x") {
			continue
		}
		for _, state := range []string{"idle", "unknown", "stopped", "working"} {
			if strings.Contains(line, state) {
				lines[state] = line
			}
		}
	}
	for _, state := range []string{"idle", "unknown"} {
		line := lines[state]
		if line == "" {
			t.Fatalf("%s data line not found in:\n%s", state, m.View())
		}
		if !strings.HasPrefix(line, quietOpen) || !strings.HasSuffix(line, quietClose) {
			t.Fatalf("%s line %q must be wrapped start-to-end in the quiet faint", state, line)
		}
		stateOpen, _ := loam.StyleSequences(stateStyle(state))
		if !strings.Contains(line, stateOpen+"\x1b[22m") {
			t.Fatalf("%s line %q must splice SGR 22 into the State word's opening sequence, keeping it bright inside the faint", state, line)
		}
	}
	for _, state := range []string{"working", "stopped"} {
		if line := lines[state]; line == "" {
			t.Fatalf("%s data line not found in:\n%s", state, m.View())
		} else if strings.HasPrefix(line, quietOpen) {
			t.Fatalf("%s line %q must NOT be faint-wrapped (only the quiet tier recedes)", state, line)
		}
	}
}

func TestTableViewGreysIdleAndUnknownDetailLines(t *testing.T) {
	withForcedColor(t)
	m := New(time.Second, nil)
	m.width, m.height = 150, 35
	idle := entry(1, ancestry.Ghostty, "idle")
	idle.Task = "write the launch plan"
	unknown := entry(2, ancestry.Ghostty, "unknown")
	unknown.Task = "triage the queue"
	stopped := entry(3, ancestry.Ghostty, "stopped")
	stopped.Task = "kept plain on purpose"
	m.applyEntries([]registry.RegistryEntry{idle, unknown, stopped})
	view := m.View()
	if want := subtleStyle.Render("write the launch plan"); !strings.Contains(view, want) {
		t.Fatalf("idle task line must render in subtle grey (it recedes with its faint row); got %q, want it to contain %q", view, want)
	}
	if want := subtleStyle.Render("triage the queue"); !strings.Contains(view, want) {
		t.Fatalf("unknown task line must render in subtle grey (it recedes with its faint row); got %q, want it to contain %q", view, want)
	}
	if !strings.Contains(view, "kept plain on purpose") || strings.Contains(view, subtleStyle.Render("kept plain on purpose")) {
		t.Fatalf("stopped task line must stay plain (its row is not quiet): %q", view)
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
