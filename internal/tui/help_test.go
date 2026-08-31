package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

func TestQuestionMarkOpensAHelpOverlayListingEveryKeybinding(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries(nil)

	updated, _ := m.Update(keyMsg("?"))
	m = updated.(Model)

	if !m.showHelp {
		t.Fatal("want showHelp=true after ?")
	}
	view := m.View()
	for _, want := range []string{
		"keyboard shortcuts",
		"enter", "c / C", "x / X", "p", "D", "r", "?", "q, ctrl+c", "mouse drag",
		"SIGTERM", "SIGKILL", "SIGSTOP", "SIGCONT",
		"press any key to close",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view = %q, want it to contain %q", view, want)
		}
	}
	// The table is swapped out entirely while the overlay is open.
	if strings.Contains(view, "Location") {
		t.Fatalf("help view = %q, want the table itself replaced, not overlaid", view)
	}
}

func TestAnyKeyClosesTheHelpOverlayWithoutActing(t *testing.T) {
	for _, key := range []string{"?", "esc", "enter", "q", "x", "j"} {
		m := New(999)
		m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
		updated, _ := m.Update(keyMsg("?"))
		m = updated.(Model)

		updated, _ = m.Update(keyMsg(key))
		m = updated.(Model)

		if m.showHelp {
			t.Fatalf("key %q: want the overlay closed", key)
		}
		if m.quitting {
			t.Fatalf("key %q: want q intercepted (close only), not quitting, while the overlay is open", key)
		}
		if m.pendingKill != nil {
			t.Fatalf("key %q: want x intercepted (close only), not arming a kill prompt", key)
		}
	}
}

func TestKillPromptInterceptsQuestionMarkBeforeHelpCanOpen(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)
	updated, _ = m.Update(keyMsg("?"))
	m = updated.(Model)

	if m.showHelp {
		t.Fatal("want the kill prompt's any-key-cancels intercept to win over the help overlay")
	}
	if m.pendingKill != nil {
		t.Fatal("want ? to have cancelled the armed prompt")
	}
	if m.notification != "SIGTERM cancelled." {
		t.Fatalf("got notification %q, want the cancellation notice", m.notification)
	}
}

func TestFooterPointsAtTheHelpOverlay(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries(nil)

	if !strings.Contains(m.View(), "? help") {
		t.Fatalf("View() = %q, want the footer to point at the ? overlay", m.View())
	}
}
