package registry

import (
	"fmt"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/pistatus"
	"github.com/luiul/canopy/internal/scan"
)

func entry(pid int, kind string, surface ancestry.Surface, state string) RegistryEntry {
	return RegistryEntry{Pid: pid, Kind: kind, Tty: "s000", Cwd: "/x", Surface: surface, State: state}
}

func TestMergeRegistryPrefersTheFreshCopyOfAStillPresentEntry(t *testing.T) {
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].Misses = 1
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}

	merged := MergeRegistry(previous, fresh)

	if len(merged) != 1 {
		t.Fatalf("got %+v, want 1 entry", merged)
	}
	if merged[0].State != "working" {
		t.Fatalf("got state %q, want working", merged[0].State)
	}
	if merged[0].Misses != 0 {
		t.Fatalf("got misses %d, want 0", merged[0].Misses)
	}
}

func TestMergeRegistryKeepsAMissingEntryWithinTheDebounceWindow(t *testing.T) {
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}

	merged := MergeRegistry(previous, nil)

	if len(merged) != 1 {
		t.Fatalf("got %+v, want 1 entry", merged)
	}
	if merged[0].Misses != 1 {
		t.Fatalf("got misses %d, want 1", merged[0].Misses)
	}
}

func TestMergeRegistryDropsAnEntryOncePastTheDebounceWindow(t *testing.T) {
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].Misses = 1 // already missed once, at MissLimit

	merged := MergeRegistry(previous, nil)

	if len(merged) != 0 {
		t.Fatalf("got %+v, want empty", merged)
	}
}

func TestMergeRegistryAddsANewlySeenEntry(t *testing.T) {
	merged := MergeRegistry(nil, []RegistryEntry{entry(2, "pi", ancestry.Ghostty, "idle")})
	if len(merged) != 1 || merged[0].Pid != 2 {
		t.Fatalf("got %+v, want pid 2", merged)
	}
}

func TestStampStateSinceCarriesOverAnUnchangedState(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Minute)
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].StateSince = earlier
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}

	got := stampStateSince(previous, fresh, now)

	if !got[0].StateSince.Equal(earlier) {
		t.Fatalf("got StateSince %v, want carried-over %v", got[0].StateSince, earlier)
	}
}

func TestStampStateSinceResetsOnAStateChange(t *testing.T) {
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].StateSince = now.Add(-time.Hour)
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}

	got := stampStateSince(previous, fresh, now)

	if !got[0].StateSince.Equal(now) {
		t.Fatalf("got StateSince %v, want %v (now, since the state changed)", got[0].StateSince, now)
	}
}

func TestStampStateSinceResetsWhenStoppedFlips(t *testing.T) {
	// Pausing/resuming leaves raw State alone (a stopped process reads 0%
	// CPU, "idle" either way), but the TUI displays "stopped" as its own
	// synthetic state — so the Since clock must restart at the flip, not
	// inherit however long the row had already been idle.
	now := time.Now()
	earlier := now.Add(-time.Minute)
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].StateSince = earlier
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	fresh[0].Stopped = true

	got := stampStateSince(previous, fresh, now)

	if !got[0].StateSince.Equal(now) {
		t.Fatalf("got StateSince %v, want %v (now, since Stopped flipped)", got[0].StateSince, now)
	}

	// ...and back: resuming is the same kind of display-state change.
	got = stampStateSince(got, []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}, now)
	if !got[0].StateSince.Equal(now) {
		t.Fatalf("got StateSince %v on resume, want %v", got[0].StateSince, now)
	}
}

func TestStampStateSinceStampsABrandNewEntry(t *testing.T) {
	now := time.Now()
	fresh := []RegistryEntry{entry(2, "pi", ancestry.Ghostty, "idle")}

	got := stampStateSince(nil, fresh, now)

	if !got[0].StateSince.Equal(now) {
		t.Fatalf("got StateSince %v, want %v", got[0].StateSince, now)
	}
}

func TestRegistryEntryKeyDisambiguatesSamePidDifferentKind(t *testing.T) {
	// pids get reused by the OS; scoping the key by kind too avoids two
	// genuinely different processes colliding if a pid is recycled between
	// polls faster than the debounce window notices.
	a := entry(1, "pi", ancestry.Ghostty, "idle")
	b := entry(1, "claude", ancestry.Ghostty, "idle")
	if a.Key() == b.Key() {
		t.Fatalf("keys should differ: %q vs %q", a.Key(), b.Key())
	}
}

func TestRefineExternalStatesKeepsTheSingleSampleGuessForABrandNewEntry(t *testing.T) {
	// No previous poll to diff against yet: nothing to refine.
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}

	got := refineExternalStates(nil, fresh, time.Now())

	if got[0].State != "working" {
		t.Fatalf("got state %q, want the untouched bootstrap guess working", got[0].State)
	}
}

func TestRefineExternalStatesCorrectsAStaleWorkingGuessBackToIdle(t *testing.T) {
	// This is the reported bug: macOS's own `ps` %cpu is a decaying average
	// over up to a minute of real time, so externalEntries' single-sample
	// guess can still say "working" well after a process has actually gone
	// idle. Two samples five seconds apart with an unchanged CPUTime (no
	// CPU actually consumed in that window) must correct that guess.
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	previous[0].CPUTime = 30 * time.Second
	previous[0].CPUSampledAt = now.Add(-5 * time.Second)

	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")} // externalEntries' stale guess
	fresh[0].CPUTime = 30 * time.Second                                   // unchanged: no CPU consumed since the last poll

	got := refineExternalStates(previous, fresh, now)

	if got[0].State != "idle" {
		t.Fatalf("got state %q, want idle (no CPU consumed since the last poll)", got[0].State)
	}
	if !got[0].CPUSampledAt.Equal(now) {
		t.Fatalf("got CPUSampledAt %v, want %v", got[0].CPUSampledAt, now)
	}
}

func TestRefineExternalStatesConfirmsWorkingOnlyAfterTwoConsecutiveQualifyingPolls(t *testing.T) {
	// A single qualifying poll (WorkingStreak going 0 -> 1) is exactly what
	// a brief CPU blip on an otherwise idle process looks like (a heartbeat
	// tick, a GC pause, a terminal redraw) -- not sustained agent work. It
	// must not flip the display to working by itself.
	now := time.Now()
	previousIdle := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previousIdle[0].CPUTime = 10 * time.Second
	previousIdle[0].CPUSampledAt = now.Add(-5 * time.Second)

	firstPoll := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	firstPoll[0].CPUTime = 14 * time.Second // 4s of CPU time over a 5s window: ~80% > DefaultThreshold

	afterFirstPoll := refineExternalStates(previousIdle, firstPoll, now)

	if afterFirstPoll[0].State != "idle" {
		t.Fatalf("got state %q after one qualifying poll, want idle (needs %d consecutive)", afterFirstPoll[0].State, workingConfirmPolls)
	}
	if afterFirstPoll[0].WorkingStreak != 1 {
		t.Fatalf("got WorkingStreak %d, want 1", afterFirstPoll[0].WorkingStreak)
	}

	// A second consecutive qualifying poll must confirm it.
	now2 := now.Add(5 * time.Second)
	secondPoll := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	secondPoll[0].CPUTime = 18 * time.Second // another 4s of CPU time over another 5s window

	afterSecondPoll := refineExternalStates(afterFirstPoll, secondPoll, now2)

	if afterSecondPoll[0].State != "working" {
		t.Fatalf("got state %q after two consecutive qualifying polls, want working", afterSecondPoll[0].State)
	}
	if afterSecondPoll[0].WorkingStreak != 2 {
		t.Fatalf("got WorkingStreak %d, want 2", afterSecondPoll[0].WorkingStreak)
	}
}

func TestRefineExternalStatesDropsBackToIdleImmediatelyAndResetsTheStreak(t *testing.T) {
	// Climbing into working is debounced; dropping back to idle is not.
	confirmedWorking := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	confirmedWorking[0].CPUTime = 20 * time.Second
	confirmedWorking[0].CPUSampledAt = time.Now().Add(-5 * time.Second)
	confirmedWorking[0].WorkingStreak = 2

	now := confirmedWorking[0].CPUSampledAt.Add(5 * time.Second)
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	fresh[0].CPUTime = 20 * time.Second // unchanged: no CPU consumed since the last poll

	got := refineExternalStates(confirmedWorking, fresh, now)

	if got[0].State != "idle" {
		t.Fatalf("got state %q, want idle immediately, no debounce", got[0].State)
	}
	if got[0].WorkingStreak != 0 {
		t.Fatalf("got WorkingStreak %d, want reset to 0", got[0].WorkingStreak)
	}
}

func TestRefineExternalStatesLeavesARealStateEntryUntouched(t *testing.T) {
	// canopy-status.ts (internal/pistatus) already told externalEntries the
	// truth for this pid; even a CPU delta that would otherwise scream
	// "idle" must not override it.
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "done")}
	previous[0].RealState = true
	previous[0].CPUSampledAt = now.Add(-5 * time.Second)

	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "done")}
	fresh[0].RealState = true
	// No CPU activity at all between polls; the heuristic would call this
	// idle, but RealState means refineExternalStates must not even look.
	fresh[0].CPUTime = previous[0].CPUTime

	got := refineExternalStates(previous, fresh, now)

	if got[0].State != "done" {
		t.Fatalf("got state %q, want done left untouched by the CPU heuristic", got[0].State)
	}
	if got[0].CPUSampledAt != now {
		t.Fatalf("got CPUSampledAt %v, want it still stamped to now", got[0].CPUSampledAt)
	}
}

func TestRefineExternalStatesIgnoresANegativeDelta(t *testing.T) {
	// A pid the OS recycled between polls (or any other counter hiccup)
	// would otherwise produce a nonsensical negative rate; keep the
	// existing guess instead of acting on it.
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	previous[0].CPUTime = 30 * time.Second
	previous[0].CPUSampledAt = now.Add(-5 * time.Second)

	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	fresh[0].CPUTime = 2 * time.Second // less than previous: negative delta

	got := refineExternalStates(previous, fresh, now)

	if got[0].State != "working" {
		t.Fatalf("got state %q, want the untouched guess working when the delta is negative", got[0].State)
	}
}

// withResolveCwds and withPistatusRead swap in a canned seam for the
// duration of a test, restoring the real one on cleanup, so
// externalEntries can be exercised without a live lsof call or a real
// ~/.pi/agent/canopy-status/<pid>.json file.
func withResolveCwds(t *testing.T, fn func([]int) map[int]string) {
	t.Helper()
	previous := resolveCwds
	resolveCwds = fn
	t.Cleanup(func() { resolveCwds = previous })
}

func withPistatusRead(t *testing.T, fn func(int) (pistatus.Status, bool)) {
	t.Helper()
	previous := pistatusRead
	pistatusRead = fn
	t.Cleanup(func() { pistatusRead = previous })
}

func TestExternalEntriesReturnsNilForNoMatches(t *testing.T) {
	if got := externalEntries(nil, nil); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestExternalEntriesClassifiesFromTheInjectedProcessTable(t *testing.T) {
	withResolveCwds(t, func(pids []int) map[int]string { return map[int]string{42: "/x"} })
	withPistatusRead(t, func(int) (pistatus.Status, bool) { return pistatus.Status{}, false })

	table := map[int]scan.ProcessInfo{
		42: {Pid: 42, Pcpu: 5.0, RssKb: 1024, Etime: time.Minute, CPUTime: 3 * time.Second},
	}
	matches := []scan.ProcessMatch{{Pid: 42, Tty: "ttys000", Kind: "claude", Args: "claude"}}

	got := externalEntries(matches, table)

	if len(got) != 1 {
		t.Fatalf("got %+v, want 1 entry", got)
	}
	e := got[0]
	if e.Pid != 42 || e.Kind != "claude" || e.Cwd != "/x" {
		t.Fatalf("got %+v, want pid 42, kind claude, cwd /x", e)
	}
	if e.State != "working" {
		t.Fatalf("got state %q, want working (5%% >= DefaultThreshold on the bootstrap sample)", e.State)
	}
	if e.CPUPercent != 5.0 || e.RSSKb != 1024 || e.Uptime != time.Minute {
		t.Fatalf("got %+v, want CPUPercent/RSSKb/Uptime straight from the injected table", e)
	}
	if e.RealState {
		t.Fatalf("got RealState true, want false: no pi status was injected")
	}
}

func TestExternalEntriesMarksStoppedProcesses(t *testing.T) {
	withResolveCwds(t, func(pids []int) map[int]string { return map[int]string{} })
	withPistatusRead(t, func(int) (pistatus.Status, bool) { return pistatus.Status{}, false })

	table := map[int]scan.ProcessInfo{
		7: {Pid: 7, Pcpu: 0, Etime: time.Hour, State: "T"},  // stopped (SIGSTOP)
		8: {Pid: 8, Pcpu: 0, Etime: time.Hour, State: "Ss"}, // plain sleeping
	}
	matches := []scan.ProcessMatch{
		{Pid: 7, Tty: "ttys000", Kind: "pi", Args: "pi"},
		{Pid: 8, Tty: "ttys001", Kind: "pi", Args: "pi"},
	}

	got := externalEntries(matches, table)

	if len(got) != 2 {
		t.Fatalf("got %+v, want 2 entries", got)
	}
	if !got[0].Stopped {
		t.Fatalf("got Stopped=false for a T-state process, want true: %+v", got[0])
	}
	if got[1].Stopped {
		t.Fatalf("got Stopped=true for an Ss-state process, want false: %+v", got[1])
	}
}

func TestExternalEntriesPrefersPistatusOverTheCPUHeuristicForPi(t *testing.T) {
	// pi is the one agent kind canopy can ask directly instead of guessing
	// from CPU; a pistatusRead hit must win even when the CPU sample alone
	// would say something else.
	withResolveCwds(t, func(pids []int) map[int]string { return map[int]string{} })
	reportedAt := time.Now()
	withPistatusRead(t, func(pid int) (pistatus.Status, bool) {
		return pistatus.Status{Pid: pid, Cwd: "/pi-cwd", State: "done", UpdatedAt: reportedAt}, true
	})

	table := map[int]scan.ProcessInfo{9: {Pid: 9, Pcpu: 0}} // CPU heuristic alone would say idle
	matches := []scan.ProcessMatch{{Pid: 9, Tty: "ttys000", Kind: "pi", Args: "pi"}}

	got := externalEntries(matches, table)

	if len(got) != 1 {
		t.Fatalf("got %+v, want 1 entry", got)
	}
	e := got[0]
	if e.State != "done" || !e.RealState {
		t.Fatalf("got %+v, want State done and RealState true from pistatus", e)
	}
	if !e.RealStateReportedAt.Equal(reportedAt) {
		t.Fatalf("got RealStateReportedAt %v, want %v", e.RealStateReportedAt, reportedAt)
	}
	if e.Cwd != "/pi-cwd" {
		t.Fatalf("got Cwd %q, want pistatus's cwd used as a fallback since lsof found none", e.Cwd)
	}
}

func TestExternalEntriesLeavesNonPiKindsUntouchedByPistatus(t *testing.T) {
	withResolveCwds(t, func(pids []int) map[int]string { return map[int]string{} })
	withPistatusRead(t, func(int) (pistatus.Status, bool) {
		t.Fatalf("pistatusRead should never be consulted for a non-pi kind")
		return pistatus.Status{}, false
	})

	table := map[int]scan.ProcessInfo{7: {Pid: 7, Pcpu: 0}}
	matches := []scan.ProcessMatch{{Pid: 7, Tty: "ttys000", Kind: "claude", Args: "claude"}}

	got := externalEntries(matches, table)

	if len(got) != 1 || got[0].RealState {
		t.Fatalf("got %+v, want a plain CPU-heuristic entry", got)
	}
}

func TestPollOnceSurfacesAWarningWhenTheAgentScanFails(t *testing.T) {
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	scanAgentProcesses = func(string) ([]scan.ProcessMatch, error) {
		return nil, fmt.Errorf("ps: exit status 1")
	}
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }
	withSnapshotVSCode(t, fakeVSCodeSnapshot{err: fmt.Errorf("not authorized"), noCall: true})

	result := PollOnce("someuser", nil)

	if result.Warning == "" {
		t.Fatalf("got empty Warning, want a non-empty one when the agent scan itself failed")
	}
	if len(result.Entries) != 0 {
		t.Fatalf("got %+v, want no entries", result.Entries)
	}
}

func TestPollOnceHasNoWarningWhenTheAgentScanSucceedsWithZeroMatches(t *testing.T) {
	// The whole point of PollResult.Warning: a successful scan that simply
	// found nothing must not look like a failed one.
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	scanAgentProcesses = func(string) ([]scan.ProcessMatch, error) { return nil, nil }
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }
	withSnapshotVSCode(t, fakeVSCodeSnapshot{})

	result := PollOnce("someuser", nil)

	if result.Warning != "" {
		t.Fatalf("got Warning %q, want empty", result.Warning)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("got %+v, want no entries", result.Entries)
	}
}
