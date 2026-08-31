package tui

import (
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/kill"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/confirm"
)

// killCall records one invocation of the killProcess seam.
type killCall struct {
	entry registry.RegistryEntry
	sig   syscall.Signal
}

// withKillProcess swaps the killProcess seam for a recorder returning a
// fake result, and restores it on cleanup — so the x/X/p/D keybind flows
// can run end to end without signaling real processes.
func withKillProcess(t *testing.T, ok bool) *[]killCall {
	t.Helper()
	var calls []killCall
	orig := killProcess
	killProcess = func(e registry.RegistryEntry, sig syscall.Signal) kill.Result {
		calls = append(calls, killCall{e, sig})
		if ok {
			return kill.Result{OK: true, Message: "fake success"}
		}
		return kill.Result{OK: false, Message: "fake failure"}
	}
	t.Cleanup(func() { killProcess = orig })
	return &calls
}

// keyMsg builds the tea.KeyMsg a real keypress produces, for the keys the
// kill flows care about (runes, plus enter/esc).
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestXArmsASIGTERMPromptAndXArmsSIGKILL(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)
	if !m.pendingKill.Active() {
		t.Fatal("want x to arm a kill prompt")
	}
	if m.pendingKill.Payload.sig != syscall.SIGTERM {
		t.Fatalf("got sig %v, want SIGTERM for x", m.pendingKill.Payload.sig)
	}
	if len(m.pendingKill.Payload.entries) != 1 || m.pendingKill.Payload.entries[0].Pid != 42 {
		t.Fatalf("got targets %+v, want pid 42", m.pendingKill.Payload.entries)
	}

	// X arms SIGKILL instead. On a fresh model: while a prompt is armed,
	// any key other than an answer is swallowed rather than re-arming (the
	// intercept is what makes stacking two prompts impossible).
	m = New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
	updated, _ = m.Update(keyMsg("X"))
	m = updated.(Model)
	if !m.pendingKill.Active() || m.pendingKill.Payload.sig != syscall.SIGKILL {
		t.Fatalf("got %+v, want X to arm SIGKILL", m.pendingKill)
	}
}

func TestArmingAPromptSchedulesItsAutoCancelTick(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})

	_, cmd := m.Update(keyMsg("x"))
	if cmd == nil {
		t.Fatal("want arming a prompt to schedule its auto-cancel tick")
	}
}

func TestXOnThePlaceholderRowIsANoOp(t *testing.T) {
	m := New(999)
	m.applyEntries(nil)

	updated, _ := m.Update(keyMsg("x"))
	if updated.(Model).pendingKill.Active() {
		t.Fatal("want no prompt when there is no selected entry")
	}
}

func TestTheArmedPromptRendersInTheFooterWithTargetDetails(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "idle")})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "Terminate pi (pid 42, /Users/x/dotfiles)? [y/N]") {
		t.Fatalf("View() = %q, want the armed prompt naming verb, kind, pid, and location", view)
	}
}

func TestThePromptWarnsWhenTheTargetIsMidTurn(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})

	updated, _ := m.Update(keyMsg("X"))
	m = updated.(Model)

	if !strings.Contains(m.View(), "Kill pi (pid 42, /Users/x/dotfiles)? Currently working. [y/N]") {
		t.Fatalf("View() = %q, want the force-kill verb and a mid-turn consequence sentence", m.View())
	}
}

func TestYConfirmsAnArmedPrompt(t *testing.T) {
	calls := withKillProcess(t, true)
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)
	updated, cmd := m.Update(keyMsg("y"))
	m = updated.(Model)

	if m.pendingKill.Active() {
		t.Fatal("want the prompt cleared once y confirms it")
	}
	if cmd == nil {
		t.Fatal("want a kill command from y")
	}
	msg, ok := cmd().(killResultMsg)
	if !ok {
		t.Fatalf("got %T, want killResultMsg", msg)
	}
	if len(msg.results) != 1 || !msg.results[0].OK || msg.sig != syscall.SIGTERM {
		t.Fatalf("got %+v, want one successful SIGTERM result", msg)
	}
	if len(*calls) != 1 || (*calls)[0].entry.Pid != 42 || (*calls)[0].sig != syscall.SIGTERM {
		t.Fatalf("got calls %+v, want exactly one SIGTERM to pid 42", *calls)
	}
}

// TestNEscAndEnterCancelAnArmedPromptSilently pins the one answer set
// both dashboards share: y confirms; n, esc, or enter cancel (enter
// honors the prompt's [y/N]). An explicit cancel is silent: only the
// auto-cancel timeout notifies.
func TestNEscAndEnterCancelAnArmedPromptSilently(t *testing.T) {
	calls := withKillProcess(t, true)

	for _, key := range []string{"n", "esc", "enter"} {
		m := New(999)
		m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
		updated, _ := m.Update(keyMsg("x"))
		m = updated.(Model)

		updated, cmd := m.Update(keyMsg(key))
		m = updated.(Model)

		if m.pendingKill.Active() {
			t.Fatalf("key %q: want the prompt cancelled", key)
		}
		if cmd != nil {
			t.Fatalf("key %q: want no command from an explicit cancel", key)
		}
		if m.notification != "" {
			t.Fatalf("key %q: got notification %q, want an explicit cancel to stay silent", key, m.notification)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("got calls %+v, want no process ever signaled by a cancel", *calls)
	}
}

// TestAnArmedPromptSwallowsAnyNonAnswer pins the modal discipline: a key
// that is neither confirm nor cancel must not act on the table
// underneath (q quitting, x re-arming, ? opening help) nor cancel the
// prompt by accident — the state the prompt describes could have shifted
// by the time the user realizes what happened.
func TestAnArmedPromptSwallowsAnyNonAnswer(t *testing.T) {
	calls := withKillProcess(t, true)

	for _, key := range []string{"N", "q", "c", "x", "?", "j"} {
		m := New(999)
		m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
		updated, _ := m.Update(keyMsg("x"))
		m = updated.(Model)

		updated, cmd := m.Update(keyMsg(key))
		m = updated.(Model)

		if !m.pendingKill.Active() {
			t.Fatalf("key %q: want the prompt still armed (swallowed, not cancelled)", key)
		}
		if m.pendingKill.Payload.sig != syscall.SIGTERM {
			t.Fatalf("key %q: got sig %v, want the original SIGTERM prompt untouched", key, m.pendingKill.Payload.sig)
		}
		if m.quitting {
			t.Fatalf("key %q: want q swallowed, not quitting, while a prompt is armed", key)
		}
		if m.showHelp {
			t.Fatalf("key %q: want ? swallowed, not opening help, while a prompt is armed", key)
		}
		if cmd != nil {
			t.Fatalf("key %q: want no command from a swallowed key", key)
		}
		if m.notification != "" {
			t.Fatalf("key %q: got notification %q, want none from a swallowed key", key, m.notification)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("got calls %+v, want no process ever signaled by a swallowed key", *calls)
	}
}

// TestCtrlCQuitsFromAnArmedPrompt pins the one exception to the modal's
// key swallowing: ctrl+c always quits, from anywhere.
func TestCtrlCQuitsFromAnArmedPrompt(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)
	if !m.quitting {
		t.Fatal("want ctrl+c to quit even with a prompt armed")
	}
	if cmd == nil {
		t.Fatal("want tea.Quit returned")
	}
}

func TestAnArmedPromptAutoCancelsAfterTheTimeout(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)

	updated, cmd := m.Update(confirm.Msg{Token: m.pendingKill.Token()})
	m = updated.(Model)
	if m.pendingKill.Active() {
		t.Fatal("want the prompt cancelled once its own token fires")
	}
	if m.notification != "cancelled: no answer within 10s" || m.notifyIsError {
		t.Fatalf("got notification %q (err=%v), want the timeout cancellation note", m.notification, m.notifyIsError)
	}
	if cmd == nil {
		t.Fatal("want the notification's clear scheduled")
	}
}

func TestAStaleCancelConfirmTokenIsIgnored(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})
	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)

	updated, _ = m.Update(confirm.Msg{Token: m.pendingKill.Token() - 1})
	m = updated.(Model)
	if !m.pendingKill.Active() {
		t.Fatal("want the prompt to survive a stale cancel token")
	}
}

func TestDArmsABulkPromptForDoneRowsOnly(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "done"),
		entry(2, ancestry.Ghostty, "working"),
		entry(3, ancestry.Ghostty, "done"),
	})

	updated, _ := m.Update(keyMsg("D"))
	m = updated.(Model)

	if !m.pendingKill.Active() {
		t.Fatal("want D to arm a bulk prompt")
	}
	if m.pendingKill.Payload.sig != syscall.SIGTERM {
		t.Fatalf("got sig %v, want SIGTERM for the bulk cleanup", m.pendingKill.Payload.sig)
	}
	if len(m.pendingKill.Payload.entries) != 2 {
		t.Fatalf("got %d targets, want exactly the 2 done rows", len(m.pendingKill.Payload.entries))
	}
	for _, e := range m.pendingKill.Payload.entries {
		if e.Pid == 2 {
			t.Fatalf("got targets %+v, want the working row excluded", m.pendingKill.Payload.entries)
		}
	}
	if !strings.Contains(m.View(), "Terminate 2 done sessions? [y/N]") {
		t.Fatalf("View() = %q, want the bulk prompt with the done count", m.View())
	}
}

func TestDWithNoDoneRowsJustNotifies(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})

	updated, _ := m.Update(keyMsg("D"))
	m = updated.(Model)

	if m.pendingKill.Active() {
		t.Fatal("want no prompt when nothing reads done")
	}
	if m.notification != "no done sessions to kill" {
		t.Fatalf("got notification %q", m.notification)
	}
}

func TestPPausesARunningRowAndResumesAStoppedOne(t *testing.T) {
	calls := withKillProcess(t, true)
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(7, ancestry.Ghostty, "working")})

	updated, cmd := m.Update(keyMsg("p"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("want p to signal immediately, without a confirmation prompt")
	}
	cmd()
	if len(*calls) != 1 || (*calls)[0].sig != syscall.SIGSTOP {
		t.Fatalf("got calls %+v, want one SIGSTOP", *calls)
	}

	// The same row, now stopped (as the next poll would report it): p
	// resumes instead.
	e := entry(7, ancestry.Ghostty, "idle")
	e.Stopped = true
	m.applyEntries([]registry.RegistryEntry{e})
	updated, cmd = m.Update(keyMsg("p"))
	m = updated.(Model)
	cmd()
	if len(*calls) != 2 || (*calls)[1].sig != syscall.SIGCONT {
		t.Fatalf("got calls %+v, want a SIGCONT for the stopped row", *calls)
	}
}

func TestPOnThePlaceholderRowIsANoOp(t *testing.T) {
	calls := withKillProcess(t, true)
	m := New(999)
	m.applyEntries(nil)

	updated, cmd := m.Update(keyMsg("p"))
	if cmd != nil {
		t.Fatal("want no command when there is no selected entry")
	}
	_ = updated
	if len(*calls) != 0 {
		t.Fatalf("got calls %+v, want none", *calls)
	}
}

func TestKillResultMsgShowsASingleEntrysOwnMessage(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(killResultMsg{
		results: []kill.Result{{OK: true, Message: "killed pi (pid 42)"}},
		sig:     syscall.SIGKILL,
	})
	m = updated.(Model)

	if m.notification != "killed pi (pid 42)" {
		t.Fatalf("got notification %q", m.notification)
	}
	if m.notifyIsError {
		t.Fatal("want notifyIsError=false for a successful kill")
	}
	if cmd == nil {
		t.Fatal("want the clear command batched with a repoll on success")
	}
	if _, ok := cmd().(tea.BatchMsg); !ok {
		t.Fatal("want a batch (clear + repoll) when at least one signal landed")
	}
}

func TestKillResultMsgSummarizesABulkKill(t *testing.T) {
	m := New(999)
	updated, _ := m.Update(killResultMsg{
		results: []kill.Result{{OK: true}, {OK: true}, {OK: false, Message: "pid 3 already exited"}},
		sig:     syscall.SIGTERM,
	})
	m = updated.(Model)

	if !strings.Contains(m.notification, "terminated 2 of 3 sessions") || !strings.Contains(m.notification, "pid 3 already exited") {
		t.Fatalf("got notification %q, want a count summary naming the first failure", m.notification)
	}
	if !m.notifyIsError {
		t.Fatal("want notifyIsError=true when not every signal landed")
	}
}

func TestKillResultMsgWithOnlyFailuresDoesNotRepoll(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(killResultMsg{
		results: []kill.Result{{OK: false, Message: "nope"}},
		sig:     syscall.SIGKILL,
	})
	m = updated.(Model)

	if !m.notifyIsError || m.notification != "nope" {
		t.Fatalf("got notification %q (error %v)", m.notification, m.notifyIsError)
	}
	// cmd is the bare clear-notification timer here (no repoll batched in);
	// don't execute it — it's a tea.Tick and would block.
	if cmd == nil {
		t.Fatal("want at least the clear-notification command")
	}
}

func TestAPollKeepsAnArmedPromptsTargetsFresh(t *testing.T) {
	m := New(999)
	e := entry(42, ancestry.Ghostty, "working")
	e.Uptime = 100 * time.Second
	m.applyEntries([]registry.RegistryEntry{e})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)

	fresher := entry(42, ancestry.Ghostty, "working")
	fresher.Uptime = 104 * time.Second
	updated, _ = m.Update(pollResultMsg{entries: []registry.RegistryEntry{fresher}})
	m = updated.(Model)

	if !m.pendingKill.Active() {
		t.Fatal("want the prompt to survive a poll still containing its target")
	}
	if got := m.pendingKill.Payload.entries[0].Uptime; got != 104*time.Second {
		t.Fatalf("got target Uptime %v, want the fresh sample 104s (kill.Process's identity guard compares against it)", got)
	}
}

func TestAPollCancelsAnArmedPromptWhoseTargetsAllVanished(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(42, ancestry.Ghostty, "working")})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)
	updated, _ = m.Update(pollResultMsg{entries: nil})
	m = updated.(Model)

	if m.pendingKill.Active() {
		t.Fatal("want the prompt cancelled once its target exited on its own")
	}
}

func TestAPollPrunesVanishedTargetsFromABulkPrompt(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "done"),
		entry(2, ancestry.Ghostty, "done"),
	})

	updated, _ := m.Update(keyMsg("D"))
	m = updated.(Model)
	if len(m.pendingKill.Payload.entries) != 2 {
		t.Fatalf("got %d targets, want 2", len(m.pendingKill.Payload.entries))
	}

	// Pid 1 exited on its own; pid 2 is still there (and still done).
	updated, _ = m.Update(pollResultMsg{entries: []registry.RegistryEntry{entry(2, ancestry.Ghostty, "done")}})
	m = updated.(Model)

	if !m.pendingKill.Active() {
		t.Fatal("want the bulk prompt to survive while any target remains")
	}
	if len(m.pendingKill.Payload.entries) != 1 || m.pendingKill.Payload.entries[0].Pid != 2 {
		t.Fatalf("got targets %+v, want only pid 2 left", m.pendingKill.Payload.entries)
	}
}

func TestDisplayStateReportsStoppedAsItsOwnSyntheticState(t *testing.T) {
	e := entry(1, ancestry.Ghostty, "idle")
	e.Stopped = true
	if got := displayState(e, nil); got != "stopped" {
		t.Fatalf("got %q, want stopped", got)
	}

	// An open done episode still outranks it: done needs the user's
	// enter/c, paused or not.
	done := map[string]doneEpisode{e.Key(): {Since: time.Now()}}
	if got := displayState(e, done); got != "done" {
		t.Fatalf("got %q, want an open done episode to outrank stopped", got)
	}

	// Acknowledged done whose raw State still reads done: stopped is the
	// more informative reading, not the synthetic idle.
	acked := entry(1, ancestry.Ghostty, "done")
	acked.Stopped = true
	done = map[string]doneEpisode{acked.Key(): {Since: time.Now(), Acked: time.Now()}}
	if got := displayState(acked, done); got != "stopped" {
		t.Fatalf("got %q, want stopped for an acknowledged done row that is paused", got)
	}
}

func TestStateCellTextNeverMarksAStoppedRow(t *testing.T) {
	e := entry(1, ancestry.Ghostty, "idle")
	e.Stopped = true
	if got := stateCellText(e, time.Now(), nil); got != "stopped" {
		t.Fatalf("got %q, want a plain stopped cell with no blink marker", got)
	}
}

func TestSortEntriesRanksStoppedBetweenIdleAndUnknown(t *testing.T) {
	stopped := entry(4, ancestry.Ghostty, "idle")
	stopped.Stopped = true
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "unknown"),
		stopped,
		entry(2, ancestry.Ghostty, "idle"),
		entry(3, ancestry.Ghostty, "working"),
	}

	sortEntries(entries, nil)

	want := []int{3, 2, 4, 1} // working, idle, stopped, unknown
	for i, w := range want {
		if entries[i].Pid != w {
			t.Fatalf("got pid order %v, want %v", []int{entries[0].Pid, entries[1].Pid, entries[2].Pid, entries[3].Pid}, want)
		}
	}
}

func TestSummaryLineCountsStoppedSessions(t *testing.T) {
	stopped := entry(2, ancestry.Ghostty, "idle")
	stopped.Stopped = true
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		stopped,
	}

	got := summaryLine(entries, nil)

	if !strings.Contains(got, "1 stopped") {
		t.Fatalf("got %q, want it to contain %q", got, "1 stopped")
	}
}
