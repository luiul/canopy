package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/parked"
	"github.com/luiul/canopy/internal/registry"
)

// parkedModel is New with the mark store pointed at a temp dir: every
// park test needs it, because New alone leaves the store off (see
// Model.parkedDir's doc for why that's the default).
func parkedModel(t *testing.T) Model {
	t.Helper()
	return New(999, nil).WithParkedDir(t.TempDir())
}

// parkedEntry builds a RealState "done" entry that is already parked:
// the mark file on disk plus, for tests that drive applyEntries, the
// matching decoration.
func parkEntry(t *testing.T, m *Model, e registry.RegistryEntry, parkedAt time.Time) registry.RegistryEntry {
	t.Helper()
	if _, err := parked.Toggle(m.parkedDir, e, parkedAt); err != nil {
		t.Fatal(err)
	}
	e.Parked = &registry.ParkMark{State: e.State, ParkedAt: parkedAt, ReportedAt: e.RealStateReportedAt}
	return e
}

func TestPParksAndUnparksTheSelectedRow(t *testing.T) {
	m := parkedModel(t)
	m.applyEntries([]registry.RegistryEntry{entry(7, ancestry.Ghostty, "done")})

	updated, _ := m.Update(keyMsg("p"))
	m = updated.(Model)
	if !strings.Contains(m.notification, "parked pi (pid 7)") {
		t.Fatalf("got notification %q, want the parked confirmation", m.notification)
	}
	if m.entries[0].Parked == nil {
		t.Fatal("want the in-memory entry stamped parked on this very frame")
	}
	if got := displayState(m.entries[0]); got != "parked" {
		t.Fatalf("displayState = %q, want parked", got)
	}
	if marks := parked.Load(m.parkedDir); len(marks) != 1 {
		t.Fatalf("got %d mark files, want 1", len(marks))
	}

	updated, _ = m.Update(keyMsg("p"))
	m = updated.(Model)
	if !strings.Contains(m.notification, "unparked pi (pid 7)") {
		t.Fatalf("got notification %q, want the unparked confirmation", m.notification)
	}
	if m.entries[0].Parked != nil {
		t.Fatal("want the mark cleared on the in-memory entry too")
	}
	if got := displayState(m.entries[0]); got != "done" {
		t.Fatalf("displayState = %q, want done again", got)
	}
	if marks := parked.Load(m.parkedDir); len(marks) != 0 {
		t.Fatalf("got %d mark files, want none", len(marks))
	}
}

func TestPWithoutAConfiguredStoreJustNotifies(t *testing.T) {
	m := New(999, nil) // no WithParkedDir: the store is off by default
	m.applyEntries([]registry.RegistryEntry{entry(7, ancestry.Ghostty, "done")})

	updated, _ := m.Update(keyMsg("p"))
	m = updated.(Model)
	if m.entries[0].Parked != nil {
		t.Fatal("want no mark stamped when the store is off")
	}
	if !strings.Contains(m.notification, "not configured") {
		t.Fatalf("got notification %q, want the store-off explanation", m.notification)
	}
}

func TestParkedRowSortsBelowEverythingLive(t *testing.T) {
	m := parkedModel(t)
	e := entry(2, ancestry.Ghostty, "done")
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.VSCode, "unknown"),
		parkEntry(t, &m, e, time.Now()),
		entry(3, ancestry.VSCode, "blocked"),
	})

	if got := displayState(m.entries[len(m.entries)-1]); got != "parked" {
		t.Fatalf("last row = %q, want the parked row shelved below blocked and unknown", got)
	}
	if m.entries[len(m.entries)-1].Pid != 2 {
		t.Fatalf("last row pid = %d, want 2", m.entries[len(m.entries)-1].Pid)
	}
}

func TestSummaryLineCountsParkedSeparately(t *testing.T) {
	m := parkedModel(t)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "done"),
		parkEntry(t, &m, entry(2, ancestry.Ghostty, "done"), time.Now()),
	})

	summary := summaryLine(m.entries)
	if !strings.Contains(summary, "1 done") || !strings.Contains(summary, "1 parked") {
		t.Fatalf("summary = %q, want \"1 done\" and \"1 parked\" counted separately", summary)
	}
}

func TestFollowUpReactivatesAParkedRowAtReadTime(t *testing.T) {
	m := parkedModel(t)
	parkedAt := time.Now()
	settledAt := parkedAt.Add(-time.Minute)
	m.applyEntries([]registry.RegistryEntry{
		parkEntry(t, &m, pistatusEntry(1, ancestry.Ghostty, settledAt), parkedAt),
	})
	if got := displayState(m.entries[0]); got != "parked" {
		t.Fatalf("displayState = %q, want parked before any follow-up", got)
	}

	// A fresh settle lands: same "done" string, newer report timestamp.
	// The mark file stays on disk (reactivation is read-time, no writes)
	// but the row reads live again.
	fresh := pistatusEntry(1, ancestry.Ghostty, parkedAt.Add(time.Minute))
	bell := m.applyEntries([]registry.RegistryEntry{fresh})
	if got := displayState(m.entries[0]); got != "done" {
		t.Fatalf("displayState = %q, want done after follow-up", got)
	}
	if !bell {
		t.Fatal("want the bell for a parked row's new settle: the ring is the wake-up")
	}
	if marks := parked.Load(m.parkedDir); len(marks) != 1 {
		t.Fatal("want the mark file kept (read-time reactivation writes nothing)")
	}
}

func TestHeartbeatNeverWakesAParkedRow(t *testing.T) {
	m := parkedModel(t)
	parkedAt := time.Now()
	idle := entry(1, ancestry.Ghostty, "idle")
	idle.RealState = true
	idle.RealStateReportedAt = parkedAt.Add(-time.Minute)
	m.applyEntries([]registry.RegistryEntry{
		parkEntry(t, &m, idle, parkedAt),
	})

	// The extension's heartbeat rewrites idle every few seconds with a
	// fresh updatedAt, pure liveness, same string. Two polls of that
	// must leave the row parked: only done/error get to use the
	// timestamp comparison (see parkFollowedUp's doc).
	for i := range 2 {
		beat := entry(1, ancestry.Ghostty, "idle")
		beat.RealState = true
		beat.RealStateReportedAt = parkedAt.Add(time.Duration(i+1) * 6 * time.Second)
		bell := m.applyEntries([]registry.RegistryEntry{beat})
		if got := displayState(m.entries[0]); got != "parked" {
			t.Fatalf("beat %d: displayState = %q, want parked (a heartbeat is not follow-up)", i, got)
		}
		if bell {
			t.Fatalf("beat %d: want no bell for a heartbeat", i)
		}
	}
	// ...and its kill protection survives heartbeats too.
	if !EffectivelyParked(m.entries[0]) {
		t.Fatal("want the row still effectively parked after heartbeats")
	}
}

func TestStateChangeWakesAParkedRow(t *testing.T) {
	m := parkedModel(t)
	parkedAt := time.Now()
	idle := entry(1, ancestry.Ghostty, "idle")
	idle.RealState = true
	idle.RealStateReportedAt = parkedAt.Add(-time.Minute)
	m.applyEntries([]registry.RegistryEntry{
		parkEntry(t, &m, idle, parkedAt),
	})

	working := entry(1, ancestry.Ghostty, "working")
	working.RealState = true
	working.RealStateReportedAt = parkedAt.Add(30 * time.Second)
	m.applyEntries([]registry.RegistryEntry{working})
	if got := displayState(m.entries[0]); got != "working" {
		t.Fatalf("displayState = %q, want working: a real state change ends the park", got)
	}
}

func TestStoppedOverridesParked(t *testing.T) {
	e := entry(1, ancestry.Ghostty, "done")
	e.Parked = &registry.ParkMark{State: "done", ParkedAt: time.Now()}
	e.Stopped = true
	if got := displayState(e); got != "stopped" {
		t.Fatalf("displayState = %q, want stopped to win over parked (a frozen row's follow-up detection is dead)", got)
	}
}

func TestParkedRowIsKillProtected(t *testing.T) {
	m := parkedModel(t)
	m.applyEntries([]registry.RegistryEntry{
		parkEntry(t, &m, entry(7, ancestry.Ghostty, "done"), time.Now()),
	})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)
	if m.pendingKill.Active() {
		t.Fatal("want no kill prompt armed for a parked row")
	}
	if !strings.Contains(m.notification, "unpark first") {
		t.Fatalf("got notification %q, want the unpark-first refusal", m.notification)
	}
}

func TestFollowedUpParkedRowKillsNormally(t *testing.T) {
	m := parkedModel(t)
	parkedAt := time.Now()
	m.applyEntries([]registry.RegistryEntry{
		parkEntry(t, &m, pistatusEntry(7, ancestry.Ghostty, parkedAt.Add(-time.Minute)), parkedAt),
	})
	// Follow-up lands, so the row is live again even though the mark file exists.
	m.applyEntries([]registry.RegistryEntry{pistatusEntry(7, ancestry.Ghostty, parkedAt.Add(time.Minute))})

	updated, _ := m.Update(keyMsg("x"))
	m = updated.(Model)
	if !m.pendingKill.Active() {
		t.Fatal("want the kill prompt armed: a followed-up row is live, not parked")
	}
}

func TestDSkipsParkedDoneRows(t *testing.T) {
	m := parkedModel(t)
	m.applyEntries([]registry.RegistryEntry{
		entry(1, ancestry.Ghostty, "done"),
		parkEntry(t, &m, entry(2, ancestry.Ghostty, "done"), time.Now()),
	})

	updated, _ := m.Update(keyMsg("D"))
	m = updated.(Model)
	if !m.pendingKill.Active() {
		t.Fatal("want the bulk prompt armed for the one live done row")
	}
	if len(m.pendingKill.Payload.entries) != 1 || m.pendingKill.Payload.entries[0].Pid != 1 {
		t.Fatalf("got targets %+v, want only pid 1 (the parked row skipped)", m.pendingKill.Payload.entries)
	}
}

func TestParkedBlockedRowStaysSilent(t *testing.T) {
	m := parkedModel(t)
	blocked := entry(1, ancestry.Ghostty, "blocked")
	m.applyEntries([]registry.RegistryEntry{blocked})

	// Second consecutive blocked poll would normally ring (see
	// newlyBlocked's debounce) — unless the row is parked.
	fresh := entry(1, ancestry.Ghostty, "blocked")
	fresh.Parked = &registry.ParkMark{State: "blocked", ParkedAt: time.Now()}
	if bell := m.applyEntries([]registry.RegistryEntry{fresh}); bell {
		t.Fatal("want no bell for a parked blocked row: parking a dialog shelves its ring too")
	}
}

func TestSweepRemovesMarksWhoseSessionExited(t *testing.T) {
	m := parkedModel(t)
	if _, err := parked.Toggle(m.parkedDir, entry(99, ancestry.Ghostty, "done"), time.Now()); err != nil {
		t.Fatal(err)
	}
	m.applyEntries([]registry.RegistryEntry{entry(1, ancestry.Ghostty, "working")})

	if marks := parked.Load(m.parkedDir); len(marks) != 0 {
		t.Fatalf("got %d marks, want pid 99's swept now that its session is gone", len(marks))
	}
}

func TestStaleMarkFromARecycledPidIsDropped(t *testing.T) {
	m := parkedModel(t)
	// A mark written an hour ago by pid 7's previous, long-dead owner
	// (the session exited with no canopy poll in between to sweep it).
	if _, err := parked.Toggle(m.parkedDir, entry(7, ancestry.Ghostty, "done"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The process now sitting on pid 7 started 10 seconds ago: it
	// cannot be the parked session, so the mark must not attach to it.
	e := entry(7, ancestry.Ghostty, "idle")
	e.Uptime = 10 * time.Second
	m.applyEntries([]registry.RegistryEntry{e})

	if m.entries[0].Parked != nil {
		t.Fatal("want the recycled pid's fresh process undecorated, not shelved by a stranger's mark")
	}
	if marks := parked.Load(m.parkedDir); len(marks) != 0 {
		t.Fatal("want the stale mark file removed, not left for the next poll")
	}
}

func TestMarkKeepsAProcessThatOutlivesIt(t *testing.T) {
	m := parkedModel(t)
	// The mirror case: a process started long before its mark was
	// written is provably the marked session itself (same identity
	// guard kill.Process uses), and keeps its park.
	e := entry(7, ancestry.Ghostty, "done")
	e.Uptime = 2 * time.Hour
	m.applyEntries([]registry.RegistryEntry{
		parkEntry(t, &m, e, time.Now()),
	})

	if m.entries[0].Parked == nil {
		t.Fatal("want the mark kept: this process outlived it, so it is the parked session")
	}
	if got := displayState(m.entries[0]); got != "parked" {
		t.Fatalf("displayState = %q, want parked", got)
	}
}

func TestParkedSinceCellCountsFromParkedAt(t *testing.T) {
	parkedAt := time.Now().Add(-5 * time.Minute)
	e := entry(1, ancestry.Ghostty, "done")
	e.Parked = &registry.ParkMark{State: "done", ParkedAt: parkedAt}

	if got := sinceCellText(e, time.Now()); got != "5m" {
		t.Fatalf("sinceCellText = %q, want 5m (time on the shelf, not time in state)", got)
	}
}

func TestParkedStateCellNeverBlinks(t *testing.T) {
	e := entry(1, ancestry.Ghostty, "done")
	e.Parked = &registry.ParkMark{State: "done", ParkedAt: time.Now()}
	blinks := map[string]time.Time{e.Key(): time.Now()}

	if got := stateCellText(e, time.Now(), blinks); got != "parked" {
		t.Fatalf("stateCellText = %q, want a plain parked word (no blink marker on a shelved row)", got)
	}
}
