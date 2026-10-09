package registry

import (
	"fmt"
	"reflect"
	"strings"
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

func withPistatusReadModel(t *testing.T, fn func(int) (pistatus.Model, bool)) {
	t.Helper()
	previous := pistatusReadModel
	pistatusReadModel = fn
	t.Cleanup(func() { pistatusReadModel = previous })
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
		42: {Pid: 42, Pcpu: 5.0, RssKb: 1024, Etime: time.Minute},
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
	if e.State != "unknown" {
		t.Fatalf("got state %q, want unknown (only pi self-reports; there is no CPU fallback)", e.State)
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

func TestExternalEntriesPrefersPistatusForPi(t *testing.T) {
	// pi is the one agent kind canopy can ask directly; a pistatusRead hit
	// must win over the default "unknown".
	withResolveCwds(t, func(pids []int) map[int]string { return map[int]string{} })
	reportedAt := time.Now()
	withPistatusRead(t, func(pid int) (pistatus.Status, bool) {
		return pistatus.Status{Pid: pid, Cwd: "/pi-cwd", State: "done", Message: "sprint-planning", UpdatedAt: reportedAt}, true
	})

	table := map[int]scan.ProcessInfo{9: {Pid: 9, Pcpu: 0}}
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
	if e.Message != "sprint-planning" {
		t.Fatalf("got Message %q, want the report's message carried verbatim", e.Message)
	}
}

func TestExternalEntriesReadsPiModelWithoutARealState(t *testing.T) {
	withResolveCwds(t, func([]int) map[int]string { return nil })
	withPistatusRead(t, func(int) (pistatus.Status, bool) { return pistatus.Status{}, false })
	withPistatusReadModel(t, func(pid int) (pistatus.Model, bool) {
		return pistatus.Model{Pid: pid, Name: "GPT-6 Sol", Provider: "ai-model-router"}, true
	})

	got := externalEntries([]scan.ProcessMatch{{Pid: 9, Tty: "ttys000", Kind: "pi"}}, nil)
	if len(got) != 1 || got[0].ModelName != "GPT-6 Sol" || got[0].ModelProvider != "ai-model-router" || got[0].RealState {
		t.Fatalf("got %+v, want model/provider even though state is unknown", got)
	}
}

func TestExternalEntriesLeavesModelUnknownWhenReportIsMissing(t *testing.T) {
	withResolveCwds(t, func([]int) map[int]string { return nil })
	withPistatusRead(t, func(int) (pistatus.Status, bool) { return pistatus.Status{}, false })
	withPistatusReadModel(t, func(int) (pistatus.Model, bool) { return pistatus.Model{}, false })

	got := externalEntries([]scan.ProcessMatch{{Pid: 9, Tty: "ttys000", Kind: "pi"}}, nil)
	if len(got) != 1 || got[0].ModelName != "" || got[0].ModelProvider != "" {
		t.Fatalf("got %+v, want no model for a missing report", got)
	}
}

func TestExternalEntriesLeavesNonPiKindsUntouchedByPistatus(t *testing.T) {
	withResolveCwds(t, func(pids []int) map[int]string { return map[int]string{} })
	withPistatusRead(t, func(int) (pistatus.Status, bool) {
		t.Fatalf("pistatusRead should never be consulted for a non-pi kind")
		return pistatus.Status{}, false
	})
	withPistatusReadModel(t, func(int) (pistatus.Model, bool) {
		t.Fatalf("pistatusReadModel should never be consulted for a non-pi kind")
		return pistatus.Model{}, false
	})

	table := map[int]scan.ProcessInfo{7: {Pid: 7, Pcpu: 0}}
	matches := []scan.ProcessMatch{{Pid: 7, Tty: "ttys000", Kind: "claude", Args: "claude"}}

	got := externalEntries(matches, table)

	if len(got) != 1 || got[0].RealState {
		t.Fatalf("got %+v, want a plain unknown-state entry", got)
	}
}

func TestPollOnceSurfacesAWarningWhenTheAgentScanFails(t *testing.T) {
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	scanAgentProcesses = func(string, map[string]bool) ([]scan.ProcessMatch, error) {
		return nil, fmt.Errorf("ps: exit status 1")
	}
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }

	prev := entry(1, "pi", ancestry.Ghostty, "idle")
	result := PollOnce("someuser", nil, []RegistryEntry{prev})

	if result.Warning == "" {
		t.Fatalf("got empty Warning, want a non-empty one when the agent scan itself failed")
	}
	// A failed scan is no evidence the agent exited: the previous entry
	// comes back verbatim, not aged toward MissLimit eviction.
	if len(result.Entries) != 1 || result.Entries[0].Pid != 1 || result.Entries[0].Misses != 0 {
		t.Fatalf("got %+v, want the previous entry preserved with Misses untouched", result.Entries)
	}
}

func TestPollOnceWordsATimeoutWarningWithoutTheRawKillSignal(t *testing.T) {
	// A deadline-killed `ps` surfaces as "signal: killed", which reads
	// like canopy itself crashed; the banner should say what happened.
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	scanAgentProcesses = func(string, map[string]bool) ([]scan.ProcessMatch, error) {
		return nil, fmt.Errorf("ps: %w after 5s (killed the hung process)", scan.ErrScanTimeout)
	}
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }

	result := PollOnce("someuser", nil, nil)

	if !strings.Contains(result.Warning, "timed out") || strings.Contains(result.Warning, "signal: killed") {
		t.Fatalf("got Warning %q, want a plain timeout message without the raw kill signal", result.Warning)
	}
}

func TestPollOnceRebaselinesFromPreservedEntriesAfterTheScanRecovers(t *testing.T) {
	previousScan, previousTable, previousCwds := scanAgentProcesses, scanProcessTable, resolveCwds
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable, resolveCwds = previousScan, previousTable, previousCwds })

	failing := func(string, map[string]bool) ([]scan.ProcessMatch, error) {
		return nil, fmt.Errorf("ps: exit status 1")
	}
	scanAgentProcesses = failing
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }
	resolveCwds = func([]int) map[int]string { return map[int]string{} }

	prev := entry(1, "pi", ancestry.Ghostty, "idle")
	failed := PollOnce("someuser", nil, []RegistryEntry{prev})
	if len(failed.Entries) != 1 {
		t.Fatalf("got %+v, want the previous entry preserved through the outage", failed.Entries)
	}

	// The scan recovers and finds only a different agent: the preserved
	// entry is merged against the fresh snapshot like any other previous
	// entry, so it gets its first genuine miss, not an eviction.
	scanAgentProcesses = func(string, map[string]bool) ([]scan.ProcessMatch, error) {
		return []scan.ProcessMatch{{Pid: 2, Tty: "s001", Kind: "claude", Args: "claude"}}, nil
	}
	recovered := PollOnce("someuser", nil, failed.Entries)
	if recovered.Warning != "" {
		t.Fatalf("got Warning %q, want empty once the scan recovers", recovered.Warning)
	}
	if len(recovered.Entries) != 2 || recovered.Entries[0].Pid != 1 || recovered.Entries[0].Misses != 1 || recovered.Entries[1].Pid != 2 {
		t.Fatalf("got %+v, want the preserved entry with one miss plus the fresh one", recovered.Entries)
	}
}

func TestPollOnceHasNoWarningWhenTheAgentScanSucceedsWithZeroMatches(t *testing.T) {
	// The whole point of PollResult.Warning: a successful scan that simply
	// found nothing must not look like a failed one.
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	scanAgentProcesses = func(string, map[string]bool) ([]scan.ProcessMatch, error) { return nil, nil }
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }

	result := PollOnce("someuser", nil, nil)

	if result.Warning != "" {
		t.Fatalf("got Warning %q, want empty", result.Warning)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("got %+v, want no entries", result.Entries)
	}
}

func TestPollOnceThreadsTheKindSetToTheAgentScan(t *testing.T) {
	// The kind set canopy resolved from config (or the defaults) must
	// reach the scan unchanged: the scan, not registry, decides what
	// matches, and it can only do that if the set actually gets there.
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	var got map[string]bool
	scanAgentProcesses = func(_ string, kinds map[string]bool) ([]scan.ProcessMatch, error) {
		got = kinds
		return nil, nil
	}
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }

	kinds := map[string]bool{"myagent": true}
	PollOnce("someuser", kinds, nil)

	if !reflect.DeepEqual(got, kinds) {
		t.Fatalf("the scan saw kinds %v, want %v", got, kinds)
	}
}
