package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/jump"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/loam"
)

func entry(pid int, surface ancestry.Surface, state string) registry.RegistryEntry {
	return registry.RegistryEntry{Pid: pid, Kind: "pi", Tty: "s017", Cwd: "/Users/x/dotfiles", Surface: surface, State: state}
}

// pistatusEntry is entry, but for a RealState "done" reading with an
// explicit reportedAt: what a real canopy-status.ts write looks like on
// the wire, as opposed to entry's bare State string with no source
// timestamp at all. Used by the tests below that need to distinguish two
// separate settle events that both read the literal string "done".
func pistatusEntry(pid int, surface ancestry.Surface, reportedAt time.Time) registry.RegistryEntry {
	e := entry(pid, surface, "done")
	e.RealState = true
	e.RealStateReportedAt = reportedAt
	return e
}

func TestSortEntriesRanksDoneAboveWorking(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.Ghostty, "idle"),
		entry(3, ancestry.Ghostty, "done"),
		entry(4, ancestry.Ghostty, "unknown"),
	}

	sortEntries(entries, nil)

	var got []string
	for _, e := range entries {
		got = append(got, e.State)
	}
	want := []string{"done", "working", "idle", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got order %v, want %v", got, want)
		}
	}
}

// done's own attention treatment isn't a static flash: it's a real on/off
// blink for blinkPhases phases, then steady until the next reminder. This
// exercises that directly against stateCellText, without going through a
// poll. There is no "blocked" state to test here at all — see
// docs/agent-state-machine.md: nothing in canopy has ever produced one.
func TestStateCellTextBlinksDoneOnAndOffDuringABurst(t *testing.T) {
	now := time.Now()
	e := entry(1, ancestry.Ghostty, "done")

	onBurst := now.Add(-blinkToggleInterval / 2) // first ("on") phase
	done := map[string]doneEpisode{e.Key(): {Since: onBurst, BurstStart: onBurst}}
	if got := stateCellText(e, now, done); got != "done"+blinkMarker {
		t.Fatalf("got %q, want a blinking (on) done cell early in the burst", got)
	}

	offBurst := now.Add(-blinkToggleInterval - blinkToggleInterval/2) // second ("off") phase
	done = map[string]doneEpisode{e.Key(): {Since: offBurst, BurstStart: offBurst}}
	if got := stateCellText(e, now, done); got != "done" {
		t.Fatalf("got %q, want a steady (off) done cell mid-burst", got)
	}

	staleBurst := now.Add(-blinkBurstDuration - time.Second)
	done = map[string]doneEpisode{e.Key(): {Since: staleBurst, BurstStart: staleBurst}}
	if got := stateCellText(e, now, done); got != "done" {
		t.Fatalf("got %q, want a steady done cell once the burst has settled", got)
	}
}

func TestStateCellTextNeverBlinksAnAcknowledgedDoneRowEvenMidBurst(t *testing.T) {
	now := time.Now()
	e := entry(1, ancestry.Ghostty, "done")
	done := map[string]doneEpisode{e.Key(): {Since: now, BurstStart: now, Acked: now}}

	if got := stateCellText(e, now, done); got != "idle" {
		t.Fatalf("got %q, want idle (via displayState) with no blink marker once acknowledged", got)
	}
}

// working/idle/unknown never get any attention-getting marker at all —
// done is the only state stateCellText treats specially.
func TestStateCellTextNeverMarksNonDoneStates(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"working", "idle", "unknown"} {
		e := entry(1, ancestry.Ghostty, state)
		e.StateSince = now.Add(-time.Second)
		if got := stateCellText(e, now, nil); got != state {
			t.Fatalf("got %q, want %q to never carry a marker", got, state)
		}
	}
}

func TestBlinkActiveAndBlinkOnToggleAcrossTheBurst(t *testing.T) {
	burstStart := time.Now()
	ep := doneEpisode{BurstStart: burstStart}

	if !blinkActive(ep, burstStart) || !blinkOn(ep, burstStart) {
		t.Fatal("want active and on right at BurstStart")
	}
	if blinkOn(ep, burstStart.Add(blinkToggleInterval)) {
		t.Fatal("want off one toggle interval in")
	}
	if !blinkOn(ep, burstStart.Add(2*blinkToggleInterval)) {
		t.Fatal("want on again two toggle intervals in")
	}
	if !blinkActive(ep, burstStart.Add(blinkBurstDuration-time.Millisecond)) {
		t.Fatal("want still active just before blinkBurstDuration elapses")
	}
	if blinkActive(ep, burstStart.Add(blinkBurstDuration)) {
		t.Fatal("want inactive once blinkBurstDuration has fully elapsed")
	}
}

func TestBlinkActiveIsFalseOnceAcknowledgedEvenMidBurst(t *testing.T) {
	burstStart := time.Now()
	ep := doneEpisode{BurstStart: burstStart, Acked: burstStart}
	if blinkActive(ep, burstStart) {
		t.Fatal("want an acknowledged episode to never report as blinking, even at the exact moment its burst started")
	}
}

func TestAdvanceBlinksStartsABurstImmediatelyAndReschedulesFiveMinutesOut(t *testing.T) {
	now := time.Now()
	m := New(999)
	m.done = map[string]doneEpisode{"k": {Since: now, NextBlinkAt: now}}

	m.advanceBlinks(now)

	ep := m.done["k"]
	if ep.BurstStart != now {
		t.Fatalf("got BurstStart %v, want %v (burst starts immediately)", ep.BurstStart, now)
	}
	if want := now.Add(blinkReminderInterval); ep.NextBlinkAt != want {
		t.Fatalf("got NextBlinkAt %v, want %v (next reminder five minutes later)", ep.NextBlinkAt, want)
	}
}

func TestAdvanceBlinksLeavesAnEpisodeAloneBeforeItsNextBlinkAtArrives(t *testing.T) {
	now := time.Now()
	m := New(999)
	notYet := doneEpisode{Since: now, NextBlinkAt: now.Add(time.Minute)}
	m.done = map[string]doneEpisode{"k": notYet}

	m.advanceBlinks(now)

	if got := m.done["k"]; got != notYet {
		t.Fatalf("got %+v, want untouched %+v", got, notYet)
	}
}

func TestAdvanceBlinksSkipsAcknowledgedEpisodesEvenIfNextBlinkAtHasArrived(t *testing.T) {
	now := time.Now()
	m := New(999)
	acked := doneEpisode{Since: now.Add(-time.Hour), Acked: now.Add(-time.Minute), NextBlinkAt: now.Add(-time.Second)}
	m.done = map[string]doneEpisode{"k": acked}

	m.advanceBlinks(now)

	if got := m.done["k"]; got != acked {
		t.Fatalf("got %+v, want an acknowledged episode left untouched (no restart)", got)
	}
}

func TestAnyBlinkActiveReportsTrueOnlyWhileSomeEpisodeIsMidBurst(t *testing.T) {
	now := time.Now()
	m := New(999)
	if m.anyBlinkActive(now) {
		t.Fatal("want no active blink with an empty done map")
	}

	m.done = map[string]doneEpisode{"k": {BurstStart: now}}
	if !m.anyBlinkActive(now) {
		t.Fatal("want an active blink right at BurstStart")
	}

	m.done["k"] = doneEpisode{BurstStart: now.Add(-blinkBurstDuration - time.Second)}
	if m.anyBlinkActive(now) {
		t.Fatal("want no active blink once the only burst has settled")
	}
}

func TestSinceCellTextHumanizesElapsedTimeOrIsEmptyWhenUnknown(t *testing.T) {
	now := time.Now()

	e := entry(1, ancestry.Ghostty, "idle")
	e.StateSince = now.Add(-90 * time.Second)
	if got := sinceCellText(e, now, nil); got != "1m" {
		t.Fatalf("got %q, want 1m", got)
	}

	unknown := entry(2, ancestry.Ghostty, "idle") // StateSince left zero
	if got := sinceCellText(unknown, now, nil); got != "" {
		t.Fatalf("got %q, want empty when StateSince is unknown", got)
	}
}

func TestSummaryLineCountsByStateInPriorityOrder(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.Ghostty, "idle"),
		entry(3, ancestry.Ghostty, "done"),
	}

	got := summaryLine(entries, nil)

	for _, want := range []string{"3 sessions", "1 done", "1 working", "1 idle"} {
		if !strings.Contains(got, want) {
			t.Fatalf("got %q, want it to contain %q", got, want)
		}
	}
	if strings.Index(got, "done") > strings.Index(got, "working") {
		t.Fatalf("got %q, want done listed before working", got)
	}
}

func TestSummaryLineIsEmptyWhenThereAreNoEntries(t *testing.T) {
	if got := summaryLine(nil, nil); got != "" {
		t.Fatalf("got %q, want empty summary for no entries", got)
	}
}

func TestDashboardRendersARowPerEntry(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "working"),
		entry(2, ancestry.VSCode, "idle"),
	})

	if got := len(m.table.Rows()); got != 2 {
		t.Fatalf("got %d rows, want 2", got)
	}
}

func TestDashboardShowsAPlaceholderRowWhenNothingIsFound(t *testing.T) {
	m := New(999)
	m.applyEntries(nil)

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("got %d rows, want 1 placeholder row", got)
	}
	if _, ok := m.selectedEntry(); ok {
		t.Fatal("want no selected entry when only the placeholder row is showing")
	}
}

func TestViewMarksColumnBordersOnTheHeaderRowSoThereIsSomethingToDrag(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})

	lines := strings.Split(m.View(), "\n")
	var headerLine string
	for _, line := range lines {
		if strings.Contains(line, "State") {
			headerLine = line
			break
		}
	}
	if headerLine == "" {
		t.Fatalf("View() = %q, want a header line containing \"State\"", m.View())
	}
	// 9 columns, so 8 internal borders.
	if n := strings.Count(headerLine, loam.BorderGlyph); n != 8 {
		t.Fatalf("header line has %d border glyphs, want 8 (one per internal column border): %q", n, headerLine)
	}
}

func TestEnterOnARowTriggersJumpToTheSelectedEntry(t *testing.T) {
	target := entry(42, ancestry.Ghostty, "working")
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{target})
	m.table.SetCursor(0)

	selected, ok := m.selectedEntry()
	if !ok {
		t.Fatal("want a selected entry")
	}
	if selected != target {
		t.Fatalf("got %+v, want %+v", selected, target)
	}
}

func TestApplyEntriesPreservesCursorOnTheSameKeyAfterAReorder(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "idle"),
	})
	m.table.SetCursor(1) // pid 2 selected

	// A fresh poll where pid 2 is now "working" sorts it to the front.
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "working"),
	})

	selected, ok := m.selectedEntry()
	if !ok || selected.Pid != 2 {
		t.Fatalf("got %+v, want pid 2 to stay selected across the reorder", selected)
	}
}

func TestJumpResultMsgSetsNotificationAndSchedulesClear(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(jumpResultMsg{result: jump.Result{OK: true, Message: "Focused in Ghostty."}})
	mm := updated.(Model)

	if mm.notification != "Focused in Ghostty." {
		t.Fatalf("got notification %q", mm.notification)
	}
	if mm.notifyIsError {
		t.Fatal("want notifyIsError=false for a successful jump")
	}
	if cmd == nil {
		t.Fatal("want a clear-notification command to be scheduled")
	}

	// Simulate the scheduled clear firing for the current token.
	cleared, _ := mm.Update(clearNotifyMsg{token: mm.notifyToken})
	if cleared.(Model).notification != "" {
		t.Fatal("want notification cleared once its own token fires")
	}
}

func TestStaleClearNotifyMsgIsIgnored(t *testing.T) {
	m := New(999)
	updated, _ := m.Update(jumpResultMsg{result: jump.Result{OK: false, Message: "nope"}})
	mm := updated.(Model)

	// A clear message carrying an old token (e.g. from a jump before this
	// one) must not wipe out a newer notification.
	stale, _ := mm.Update(clearNotifyMsg{token: mm.notifyToken - 1})
	if stale.(Model).notification != "nope" {
		t.Fatal("want notification to survive a stale clear token")
	}
}

func TestQuitKeyStopsTheProgram(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !updated.(Model).quitting {
		t.Fatal("want quitting=true after q")
	}
	if cmd == nil {
		t.Fatal("want tea.Quit to be returned")
	}
}

func TestShortenHomeReplacesTheHomePrefixWithATilde(t *testing.T) {
	cases := []struct {
		path, home, want string
	}{
		{"/Users/luis/projects/canopy", "/Users/luis", "~/projects/canopy"},
		{"/Users/luis", "/Users/luis", "~"},
		{"/Users/luisandro/projects", "/Users/luis", "/Users/luisandro/projects"}, // no false-positive on a prefix match that isn't a path boundary
		{"/Users/luis/projects", "", "/Users/luis/projects"},                      // unknown home: leave untouched
	}
	for _, c := range cases {
		if got := shortenHome(c.path, c.home); got != c.want {
			t.Errorf("shortenHome(%q, %q) = %q, want %q", c.path, c.home, got, c.want)
		}
	}
}

func TestBuildRowsTagsOnlyTheCursorRowsSinceCell(t *testing.T) {
	entries := []registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "working"),
	}

	rows := buildRows(entries, 1, "", time.Now(), nil)

	if strings.Contains(rows[0][colSince], cursorSentinel) {
		t.Fatalf("got cursorSentinel on non-cursor row 0's Since cell %q, want it absent", rows[0][colSince])
	}
	if !strings.Contains(rows[1][colSince], cursorSentinel) {
		t.Fatalf("got %q, want row 1's Since cell to carry cursorSentinel (it's the cursor row)", rows[1][colSince])
	}
}

func TestCursorSentinelFollowsArrowKeysBetweenPolls(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "idle"),
		entry(2, ancestry.Ghostty, "idle"),
	})
	if got := m.table.Rows()[0][colSince]; !strings.Contains(got, cursorSentinel) {
		t.Fatalf("got %q, want cursorSentinel on row 0's Since cell right after applyEntries", got)
	}

	// Moving down (without a poll in between) must move the tag too, not
	// just bubbles/table's own internal cursor.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := updated.(Model)
	if got := mm.table.Rows()[0][colSince]; strings.Contains(got, cursorSentinel) {
		t.Fatalf("got %q, want row 0's Since cell cleared of cursorSentinel after moving down", got)
	}
	if got := mm.table.Rows()[1][colSince]; !strings.Contains(got, cursorSentinel) {
		t.Fatalf("got %q, want row 1 to carry cursorSentinel after moving down", got)
	}
}

func TestNeedsBellFiresOnlyOnATransitionIntoDone(t *testing.T) {
	cases := []struct {
		name     string
		previous []registry.RegistryEntry
		fresh    []registry.RegistryEntry
		want     bool
	}{
		{
			name:     "brand new entry already done rings the bell",
			previous: nil,
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")},
			want:     true,
		},
		{
			name:     "working flipping to done rings the bell",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")},
			want:     true,
		},
		{
			name:     "staying done across polls does not re-ring",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")},
			want:     false,
		},
		{
			name:     "done flipping to idle does not ring",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "idle")},
			want:     false,
		},
		{
			name:     "a plain working/idle churn never rings",
			previous: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "idle")},
			fresh:    []registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")},
			want:     false,
		},
		{
			name:     "no entries at all never rings",
			previous: nil,
			fresh:    nil,
			want:     false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsBell(c.previous, c.fresh, nil); got != c.want {
				t.Fatalf("needsBell() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestApplyEntriesReportsBellOnlyOnANewAttentionTransition(t *testing.T) {
	m := New(999)
	if bell := m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")}); bell {
		t.Fatal("want no bell on the first poll landing on working")
	}

	if bell := m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}); !bell {
		t.Fatal("want a bell the poll working flips to done")
	}

	// Still done on the next poll: already rang for this transition.
	if bell := m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}); bell {
		t.Fatal("want no repeat bell while an entry stays done across polls")
	}
}

func TestPollResultMsgReturnsBellCmdOnlyWhenBellIsEnabledAndNeeded(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})

	// bellEnabled defaults to true (see New): a fresh done row must batch a
	// bell command together with the blink-start command that also fires
	// for it.
	updated, cmd := m.Update(pollResultMsg{entries: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("want a non-nil command when bellEnabled and a row just went done")
	}
	if _, ok := cmd().(tea.BatchMsg); !ok {
		t.Fatal("want the bell command batched alongside the blink-start command")
	}

	// Reset back to a neutral state, then disable the bell and repeat the
	// same transition: the blink-start command still fires on its own —
	// blinking isn't gated by --no-bell, only the bell itself is — but with
	// no bell command left to batch it with, it comes back unbatched.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})
	m = m.WithBell(false)
	updated, cmd = m.Update(pollResultMsg{entries: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}})
	if cmd == nil {
		t.Fatal("want the blink-start command even once WithBell(false) disables the bell")
	}
	if _, ok := cmd().(tea.BatchMsg); ok {
		t.Fatal("want no bell command batched in once WithBell(false) disables it, even on a real transition")
	}
	_ = updated
}

func TestPollResultMsgStartsABlinkBurstAndKeepsTickingUntilItSettles(t *testing.T) {
	m := New(999)
	updated, cmd := m.Update(pollResultMsg{entries: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("want a blink-tick command for a brand new done row")
	}

	// The burst is mid-flight right after it opens: the row renders in its
	// visible ("on") phase.
	if got := m.table.Rows()[0][colState]; got != "done"+blinkMarker {
		t.Fatalf("got %q, want a blinking done cell right after it opens", got)
	}

	// Fast-forward past the whole burst by rewriting BurstStart directly —
	// waiting out blinkBurstDuration for real would make this test slow for
	// no benefit. The next animation frame should settle the row and stop
	// rescheduling itself.
	for key, ep := range m.done {
		ep.BurstStart = time.Now().Add(-blinkBurstDuration - time.Second)
		m.done[key] = ep
	}
	updated, cmd = m.Update(blinkTickMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("want no further blink-tick command once the burst has settled")
	}
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want a steady (non-blinking) done cell once the burst settles", got)
	}
}

func TestAcknowledgingStopsAnInProgressBlinkBurstImmediately(t *testing.T) {
	m := New(999)
	updated, _ := m.Update(pollResultMsg{entries: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}})
	m = updated.(Model)
	if got := m.table.Rows()[0][colState]; got != "done"+blinkMarker {
		t.Fatalf("got %q, want a blinking done cell before acknowledging", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	mm := updated.(Model)
	if got := mm.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want idle immediately, mid-burst, once acknowledged", got)
	}
}

func TestDoneRowBlinksAgainAfterTheReminderIntervalIfStillUnacknowledged(t *testing.T) {
	m := New(999)
	updated, _ := m.Update(pollResultMsg{entries: []registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}})
	m = updated.(Model)

	// Let the first burst settle, and simulate blinkReminderInterval having
	// passed with no enter/c in between.
	var key string
	for k, ep := range m.done {
		key = k
		ep.BurstStart = time.Now().Add(-blinkBurstDuration - time.Second)
		ep.NextBlinkAt = time.Now().Add(-time.Second)
		m.done[k] = ep
	}

	updated, cmd := m.Update(blinkTickMsg{})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("want a fresh blink-tick command once the reminder interval has passed")
	}
	if got := m.table.Rows()[0][colState]; got != "done"+blinkMarker {
		t.Fatalf("got %q, want the row blinking again for the reminder burst", got)
	}
	if got := m.done[key].NextBlinkAt; !got.After(time.Now().Add(blinkReminderInterval - time.Second)) {
		t.Fatalf("got NextBlinkAt %v, want it rescheduled roughly blinkReminderInterval from now", got)
	}
}

func TestCKeyAcknowledgesADoneRowWithoutJumping(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})

	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q before acknowledging, want a done cell", got)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if cmd != nil {
		t.Fatal("want no jump command from the c keybind")
	}
	mm := updated.(Model)

	if got := mm.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want the row to display idle immediately after pressing c", got)
	}
	// The raw entry itself must stay done: needsBell/registry rely on it.
	if got, ok := mm.selectedEntry(); !ok || got.State != "done" {
		t.Fatalf("got %+v, want the underlying entry's raw State to remain done", got)
	}
}

func TestEnterAcknowledgesADoneRowAndStillJumps(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("want enter to still return a jump command for a done row")
	}
	mm := updated.(Model)

	if got := mm.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want the row to display idle immediately after pressing enter on it", got)
	}
}

func TestAcknowledgingANonDoneRowIsANoOp(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	mm := updated.(Model)

	if got := mm.table.Rows()[0][colState]; got != "working" {
		t.Fatalf("got %q, want c on a working row to change nothing", got)
	}
}

func TestAcknowledgedDoneStaysAcknowledgedAcrossPollsUntilTheRawStateMovesOn(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)

	// Still reported done by the source on the next poll (the raw source
	// stays "done" until a fresh working turn overwrites it — nothing flips
	// it back to "idle" on its own): stays displayed as idle.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want an acknowledged done row to keep displaying idle across polls", got)
	}

	// A fresh working turn starts: the acknowledgment is spent.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})
	if got := m.table.Rows()[0][colState]; got != "working" {
		t.Fatalf("got %q, want working once the source itself moves on", got)
	}

	// It goes done again later: a brand new, unacknowledged episode.
	if bell := m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")}); !bell {
		t.Fatal("want a fresh bell for a new done episode after an earlier one was acknowledged")
	}
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want the new done episode to display as done again, unacknowledged", got)
	}
}

// TestAcknowledgedRealStateDoneStaysAcknowledgedForTheSameStillFreshWrite
// guards the non-buggy half of the RawAt comparison introduced for the two
// tests below: pistatus.Read legitimately keeps returning the exact same
// "done" write (same RealStateReportedAt) for up to pistatus.MaxAge after a
// turn settles, with nothing else happening in between. That must not
// look like a fresh settle just because RealState is now populated.
func TestAcknowledgedRealStateDoneStaysAcknowledgedForTheSameStillFreshWrite(t *testing.T) {
	settledAt := time.Now()
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)

	if bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, settledAt)}); bell {
		t.Error("want no bell: this is the exact same settle already acknowledged, not a new one")
	}
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want the same still-fresh done write to keep displaying idle after acknowledgment", got)
	}
}

// TestAcknowledgedRealStateDoneReopensForAGenuinelyNewSettleWithNoInterveningPoll
// is the regression test for the bug this fix addresses: canopy-status.ts
// only writes "done" once at settle time (no heartbeat, unlike "working"),
// and pistatus.Read keeps returning that literal string for up to
// pistatus.MaxAge afterward. If a second turn starts and settles again
// inside that same window — plausible for a fast, tool-free turn — with
// canopy's poll cadence never happening to catch a "working" sample in
// between, the raw State reads "done" on both sides of an acknowledgment
// with nothing to tell the two settles apart except pistatus's own write
// timestamp (RealStateReportedAt). Before this fix, updateDoneTracking only
// ever compared the State *string*, so this second, genuinely new "done"
// was silently swallowed: no new episode, no bell, the row just kept
// reading the acknowledged "idle" as if the second turn had never
// finished at all.
func TestAcknowledgedRealStateDoneReopensForAGenuinelyNewSettleWithNoInterveningPoll(t *testing.T) {
	firstSettle := time.Now()
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, firstSettle)})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("after ack: got %q, want idle", got)
	}

	// A second turn settles before the next poll — no "working" sample in
	// between, and the raw State string is still literally "done" both
	// times, but pistatus's own write timestamp has moved on.
	secondSettle := firstSettle.Add(time.Second)
	bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, secondSettle)})
	if !bell {
		t.Error("want a fresh bell: a second, genuinely new settle finished, even though the State string never changed")
	}
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want the second settle to surface as done again, unacknowledged", got)
	}
}

// TestASecondSettleWhileTheEpisodeIsStillOpenDoesNotRingTwice covers the
// other side of the same RealStateReportedAt comparison: a second turn
// settling again *before* the first done was ever acknowledged must not
// double-ring, since updateDoneTracking silently absorbs it into the same
// still-open episode — the row already reads "done" and nothing on screen
// changes as a result, so ringing again there would reintroduce exactly
// the drowning-out needsBell's "still is" skip exists to avoid, just
// triggered by a second settle instead of a poll timer. Once finally
// acknowledged, that acknowledgment must cover the *latest* settle (not
// get stuck comparing against the first one) — the same still-fresh write
// afterward must not falsely reopen, but a further genuinely new one
// still must.
func TestASecondSettleWhileTheEpisodeIsStillOpenDoesNotRingTwice(t *testing.T) {
	t1 := time.Now()
	m := New(999)

	if bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, t1)}); !bell {
		t.Fatal("want a bell for the first settle")
	}

	t2 := t1.Add(time.Second)
	if bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, t2)}); bell {
		t.Error("want no second bell: still the same unacknowledged episode, nothing new on screen")
	}
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want still done (unacknowledged)", got)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want idle right after ack", got)
	}

	// The latest settle (t2) was already covered by the acknowledgment:
	// seeing it again must not falsely reopen the row.
	if bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, t2)}); bell {
		t.Error("want no bell: t2 was already covered by the acknowledgment")
	}
	if got := m.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want idle to stick for the settle already covered by acknowledgment", got)
	}

	// A genuinely new settle after acknowledgment still must reopen.
	t3 := t2.Add(time.Second)
	if bell := m.applyEntries([]registry.RegistryEntry{pistatusEntry(1, ancestry.Ghostty, t3)}); !bell {
		t.Error("want a bell: t3 is a genuinely new settle after acknowledgment")
	}
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want done for the genuinely new t3 settle", got)
	}
}

func TestDoneStaysDisplayedUntilTheUserActsEvenIfTheRawSourceQuietlyDropsToIdleOnItsOwn(t *testing.T) {
	// This is the scenario the invariant in docs/agent-state-machine.md
	// exists for: nothing in canopy's current sources can actually flip raw
	// State from "done" back to "idle" on its own anymore (canopy-status.ts
	// writes "done" unconditionally and only a fresh working turn overwrites
	// it — see docs/agent-state-machine.md's "Removed: frontmost/focus
	// detection"), but the dashboard must keep enforcing this defensively
	// regardless of what any given raw source does. The dashboard must still
	// show done until the user actually acts on it here.
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want done on the first poll", got)
	}

	// Raw source moves to idle on its own; still no enter/c pressed.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "idle")})
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want done to keep displaying even though the raw source moved to idle on its own", got)
	}

	// Same again a few polls later: still no user action, still done.
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "idle")})
	if got := m.table.Rows()[0][colState]; got != "done" {
		t.Fatalf("got %q, want done to stay latched across further polls with no user action", got)
	}

	// The user finally presses c (works just as well with enter): only now
	// does the row actually leave done.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	mm := updated.(Model)
	if got := mm.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want idle once the user actually acknowledges it", got)
	}

	// And it stays idle afterward: the closed episode must not resurrect
	// "done" on a later poll just because it once was.
	mm.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "idle")})
	if got := mm.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want idle to stick after acknowledgment", got)
	}
}

func TestEnterAcknowledgesAnOpenDoneEpisodeEvenAfterRawStateAlreadyMovedToIdle(t *testing.T) {
	m := New(999)
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "done")})
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "idle")}) // moved on its own

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("want enter to still return a jump command for a row that's displaying done")
	}
	mm := updated.(Model)
	if got := mm.table.Rows()[0][colState]; got != "idle" {
		t.Fatalf("got %q, want idle immediately after pressing enter on it", got)
	}
}

func TestDisplayStateFoldsAckedDoneIntoIdleWithoutTouchingRawState(t *testing.T) {
	e := entry(1, ancestry.Ghostty, "done")
	done := map[string]doneEpisode{e.Key(): {Since: time.Now(), Acked: time.Now()}}

	if got := displayState(e, done); got != "idle" {
		t.Fatalf("got %q, want idle for an acked done entry", got)
	}
	if e.State != "done" {
		t.Fatalf("got %q, want displayState to leave the raw State field alone", e.State)
	}
	if got := displayState(e, nil); got != "done" {
		t.Fatalf("got %q, want done with no acknowledgment recorded", got)
	}
}

func TestDisplayStateKeepsReportingDoneForAnOpenEpisodeEvenIfRawStateMovesToIdleOnItsOwn(t *testing.T) {
	// Simulates the raw source already having flipped back to idle on its
	// own (a defensive scenario: nothing today actually does this, see
	// docs/agent-state-machine.md's "Removed: frontmost/focus detection"),
	// but the episode is still open (Acked zero): no enter/c has happened in
	// canopy yet, so displayState must keep reporting done regardless of
	// what the raw State says this poll.
	e := entry(1, ancestry.Ghostty, "idle")
	done := map[string]doneEpisode{e.Key(): {Since: time.Now()}}

	if got := displayState(e, done); got != "done" {
		t.Fatalf("got %q, want an open episode to keep reporting done even though raw State already reads idle", got)
	}
}

func TestPollResultMsgWarningIsShownInTheHeaderAndPersistsAcrossPolls(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40

	updated, _ := m.Update(pollResultMsg{entries: nil, warning: "agent process scan failed: ps: exit status 1"})
	m2 := updated.(Model)

	if !strings.Contains(m2.View(), "agent process scan failed") {
		t.Fatalf("View() = %q, want it to contain the scan warning", m2.View())
	}

	// A later poll that succeeds (empty warning) must clear the banner, not
	// leave a stale one on screen forever.
	updated2, _ := m2.Update(pollResultMsg{entries: nil, warning: ""})
	m3 := updated2.(Model)

	if strings.Contains(m3.View(), "agent process scan failed") {
		t.Fatalf("View() = %q, want the warning cleared after a clean poll", m3.View())
	}
}

// stateBorderX returns the on-screen X of the State column's own
// right-hand border, given cols in the same order/widths New builds them.
func stateBorderX(cols []table.Column) int {
	off := loam.ColumnOffsets(cols)[colState]
	return off.Start + off.Width
}

// surfaceBorderX returns the on-screen X of the Surface column's own
// right-hand border — the Surface/Location border, the one actually
// adjacent to Location — given cols in the same order/widths New builds
// them.
func surfaceBorderX(cols []table.Column) int {
	off := loam.ColumnOffsets(cols)[colSurface]
	return off.Start + off.Width
}

// locationBorderX returns the on-screen X of Location's own right-hand
// border — the Location/CPU border. Under the old flex-column design
// this never responded to a drag at all (see trellis' package doc for
// why); it's an ordinary border like any other now.
func locationBorderX(cols []table.Column) int {
	off := loam.ColumnOffsets(cols)[colLocation]
	return off.Start + off.Width
}

// TestRenderHeaderOriginYMatchesTheTablesActualHeaderRow is a regression
// test for an off-by-one that once made every mouse-drag test in this
// file pass while resizing was completely broken in a real terminal:
// renderHeader's own tableOriginY must equal the index (0-based, within
// View()'s full output) of the line View() builds from m.table.View()'s
// own first line — not one more than that, however plausible the extra
// "+1" reads in isolation (View() joins header and the table with a
// single "\n\n", i.e. exactly one blank separator line, not two).
// Handle's own Y check (tea.MouseActionPress) is exact-match, not a
// range, so being off by even one line silently drops every click on
// the table's own header row — a drag can never even start, with no
// error or other symptom besides "nothing happens".
func TestRenderHeaderOriginYMatchesTheTablesActualHeaderRow(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})

	lines := strings.Split(m.View(), "\n")
	tableHeaderLine := strings.Split(m.table.View(), "\n")[0]

	want := -1
	for i, line := range lines {
		if strings.Contains(line, tableHeaderLine) || (i > 0 && strings.Contains(line, "State") && strings.Contains(line, "PID")) {
			want = i
			break
		}
	}
	if want < 0 {
		t.Fatalf("could not find the table's own header row in View()'s output: %q", m.View())
	}

	_, got := m.renderHeader()
	if got != want {
		t.Fatalf("renderHeader's tableOriginY = %d, want %d (the actual line index of the table's header row in View()'s output)", got, want)
	}
}

// TestRenderHeaderOriginYMatchesTheTablesActualHeaderRowWithAWarningLine
// is the same check as above with a 3-line header block (title, summary,
// scan warning), so a fix that only special-cases the common 1-or-2-line
// case can't sneak back in undetected.
func TestRenderHeaderOriginYMatchesTheTablesActualHeaderRowWithAWarningLine(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})
	m.scanWarning = "agent process scan failed: ps: exit status 1"

	lines := strings.Split(m.View(), "\n")
	want := -1
	for i, line := range lines {
		if strings.Contains(line, "State") && strings.Contains(line, "PID") {
			want = i
			break
		}
	}
	if want < 0 {
		t.Fatalf("could not find the table's own header row in View()'s output: %q", m.View())
	}

	_, got := m.renderHeader()
	if got != want {
		t.Fatalf("renderHeader's tableOriginY = %d, want %d", got, want)
	}
}

func TestMouseDragOnlyResizesTheTwoColumnsStraddlingTheDraggedBorder(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()

	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := surfaceBorderX(cols)
	oldSurfaceWidth, oldLocationWidth := cols[colSurface].Width, cols[colLocation].Width
	oldStateWidth, oldCPUWidth := cols[colState].Width, cols[colCPU].Width

	updated, _ := m.Update(tea.MouseMsg{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMsg{X: borderX + 4, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = updated.(Model)

	gotCols := m.table.Columns()
	if got, want := gotCols[colSurface].Width, oldSurfaceWidth+4; got != want {
		t.Fatalf("Surface width = %d, want %d", got, want)
	}
	if got, want := gotCols[colLocation].Width, oldLocationWidth-4; got != want {
		t.Fatalf("Location width = %d, want %d (its own left-hand neighbor absorbs the drag)", got, want)
	}
	// Every column not touching the dragged border must stay put — unlike
	// the old flex-column design, where *any* drag anywhere in the table
	// silently resized Location instead of whichever column actually sat
	// next to the border being dragged (see trellis' package doc).
	if got := gotCols[colState].Width; got != oldStateWidth {
		t.Fatalf("State width = %d, want unchanged %d", got, oldStateWidth)
	}
	if got := gotCols[colCPU].Width; got != oldCPUWidth {
		t.Fatalf("CPU width = %d, want unchanged %d", got, oldCPUWidth)
	}
}

func TestMouseDragNowWorksOnLocationsOwnRightHandBorder(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()

	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := locationBorderX(cols)
	oldLocationWidth, oldCPUWidth := cols[colLocation].Width, cols[colCPU].Width

	updated, _ := m.Update(tea.MouseMsg{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	// Drag left: Location gives width to CPU, its right-hand neighbor.
	updated, _ = m.Update(tea.MouseMsg{X: borderX - 4, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = updated.(Model)

	gotCols := m.table.Columns()
	if got, want := gotCols[colLocation].Width, oldLocationWidth-4; got != want {
		t.Fatalf("Location width = %d, want %d (its own border must respond to a drag now)", got, want)
	}
	if got, want := gotCols[colCPU].Width, oldCPUWidth+4; got != want {
		t.Fatalf("CPU width = %d, want %d", got, want)
	}
}

func TestMouseDragBetweenTwoAlreadyMinimalColumnsIsANoOp(t *testing.T) {
	// State and Since are both already at their own tightest legible
	// width (columnMinWidths mirrors each one's own default), so their
	// shared border has nothing to give in either direction — and,
	// crucially, no longer silently resizes Location instead the way the
	// old flex-column design did for every border that wasn't already
	// adjacent to it.
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()

	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := stateBorderX(cols)
	oldLocationWidth := cols[colLocation].Width

	updated, _ := m.Update(tea.MouseMsg{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMsg{X: borderX + 4, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = updated.(Model)

	gotCols := m.table.Columns()
	if got, want := gotCols[colState].Width, cols[colState].Width; got != want {
		t.Fatalf("State width = %d, want unchanged %d (Since has nothing to give)", got, want)
	}
	if got, want := gotCols[colLocation].Width, oldLocationWidth; got != want {
		t.Fatalf("Location width = %d, want unchanged %d (it isn't this border's neighbor)", got, want)
	}
}

func TestResizeColumnsNeverOverflowsTheTerminal(t *testing.T) {
	// The fixed columns sum to 54, plus 18 of cell padding: below a
	// terminal width of 92, flooring Location at 20 used to push the
	// table past the terminal's right edge, clipping Kind/PID entirely.
	// Location dips below its floor instead, down to the hard floor of 8.
	m := New(999 * time.Second)
	m.width, m.height = 80, 40
	m.resizeColumns()

	cols := m.table.Columns()
	total := 2 * len(cols)
	for _, c := range cols {
		total += c.Width
	}
	if total > m.width {
		t.Fatalf("got total table width %d, want <= terminal width %d", total, m.width)
	}
	if got, want := cols[colLocation].Width, 8; got != want {
		t.Fatalf("Location width = %d, want %d (below its floor of 20, but the table fits)", got, want)
	}

	// Past the hard floor the overflow is accepted rather than crushing
	// Location to nothing.
	m.width = 60
	m.resizeColumns()
	if got := m.table.Columns()[colLocation].Width; got != 8 {
		t.Fatalf("Location width = %d at width 60, want the hard floor 8", got)
	}
}

func TestMouseDragSurvivesTheNextTerminalResizeAtTheSameWidth(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()

	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := surfaceBorderX(cols)

	updated, _ := m.Update(tea.MouseMsg{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMsg{X: borderX + 4, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	resized := m.table.Columns()[colSurface].Width

	// resizeColumns runs unconditionally on every WindowSizeMsg; calling it
	// again directly (same width) must reapply the override rather than
	// silently reverting to Surface's own fixed default.
	m.resizeColumns()
	if got := m.table.Columns()[colSurface].Width; got != resized {
		t.Fatalf("Surface width = %d after resizeColumns, want %d (the drag override, undiscarded)", got, resized)
	}
}

func TestWindowSizeMsgClearsColumnOverrides(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()

	cols := m.table.Columns()
	_, originY := m.renderHeader()
	borderX := surfaceBorderX(cols)

	updated, _ := m.Update(tea.MouseMsg{X: borderX, Y: originY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMsg{X: borderX + 4, Y: originY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	if m.colOverrides[colSurface] == 0 {
		t.Fatal("want a Surface override recorded after the drag")
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if m.colOverrides != nil {
		t.Fatalf("colOverrides = %v after a terminal resize, want nil", m.colOverrides)
	}
}

func TestMouseClickOffTheHeaderRowDoesNotStartADrag(t *testing.T) {
	m := New(999 * time.Second)
	m.width, m.height = 120, 40
	m.resizeColumns()

	cols := m.table.Columns()
	borderX := stateBorderX(cols)
	oldStateWidth := cols[colState].Width

	// Well below the header row, e.g. a click on a data row.
	updated, _ := m.Update(tea.MouseMsg{X: borderX, Y: 50, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMsg{X: borderX + 4, Y: 50, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m = updated.(Model)

	if got := m.table.Columns()[colState].Width; got != oldStateWidth {
		t.Fatalf("State width = %d, want unchanged %d (click was off the header row)", got, oldStateWidth)
	}
}
