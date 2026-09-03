package registry

import (
	"errors"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/scan"
)

// fakeVSCodeSnapshot implements the vscodeSnapshot seam with a canned
// open set (or a listing error), so the poll's VS Code enrichment is
// testable without osascript; mycelium's own suite covers the matching
// itself.
type fakeVSCodeSnapshot struct {
	open   map[string]bool
	err    error
	noCall bool // set when IsOpen must never be called (a failed listing)
}

func (f fakeVSCodeSnapshot) Err() error { return f.err }

func (f fakeVSCodeSnapshot) IsOpen(path, branch string) bool {
	if f.noCall {
		panic("IsOpen called on a failed listing; markVSCodeWindows must not query it")
	}
	return f.open[path]
}

// withSnapshotVSCode and withResolveBranch swap in a canned seam for the
// duration of a test, restoring the real one on cleanup — the same
// pattern registry_test.go's withResolveCwds/withPistatusRead use.
func withSnapshotVSCode(t *testing.T, snapshot vscodeSnapshot) {
	t.Helper()
	previous := snapshotVSCode
	snapshotVSCode = func() vscodeSnapshot { return snapshot }
	t.Cleanup(func() { snapshotVSCode = previous })
}

func withResolveBranch(t *testing.T, fn func(string) string) {
	t.Helper()
	previous := resolveBranch
	resolveBranch = fn
	t.Cleanup(func() { resolveBranch = previous })
}

func TestStampBranchesResolvesABrandNewEntry(t *testing.T) {
	withResolveBranch(t, func(cwd string) string { return "main" })
	now := time.Now()

	fresh := stampBranches(nil, []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a"}}, now)

	if fresh[0].Branch != "main" || fresh[0].BranchResolvedAt != now {
		t.Fatalf("got %+v, want Branch resolved to \"main\" at now", fresh[0])
	}
}

func TestStampBranchesCarriesOverAFreshResolution(t *testing.T) {
	now := time.Now()
	resolvedAt := now.Add(-branchTTL / 2)
	previous := []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a", Branch: "feature", BranchResolvedAt: resolvedAt}}
	withResolveBranch(t, func(cwd string) string {
		t.Fatalf("resolveBranch called for a still-fresh entry; want the carry-over to skip git")
		return ""
	})

	fresh := stampBranches(previous, []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a"}}, now)

	if fresh[0].Branch != "feature" || fresh[0].BranchResolvedAt != resolvedAt {
		t.Fatalf("got %+v, want the previous resolution carried over untouched", fresh[0])
	}
}

func TestStampBranchesReResolvesAfterExpiry(t *testing.T) {
	now := time.Now()
	previous := []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a", Branch: "old", BranchResolvedAt: now.Add(-2 * branchTTL)}}
	withResolveBranch(t, func(cwd string) string { return "new" })

	fresh := stampBranches(previous, []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a"}}, now)

	if fresh[0].Branch != "new" || fresh[0].BranchResolvedAt != now {
		t.Fatalf("got %+v, want an expired branch re-resolved to \"new\"", fresh[0])
	}
}

func TestStampBranchesReResolvesWhenTheCwdChanged(t *testing.T) {
	// A pid's cwd changing underneath it (the process chdir'd, or the pid
	// was recycled) invalidates the carried-over branch: it was resolved
	// for a different directory.
	now := time.Now()
	previous := []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a", Branch: "main", BranchResolvedAt: now}}
	withResolveBranch(t, func(cwd string) string { return "feature" })

	fresh := stampBranches(previous, []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/b"}}, now)

	if fresh[0].Branch != "feature" {
		t.Fatalf("got %+v, want a fresh resolution for the new cwd", fresh[0])
	}
}

func TestStampBranchesMemoizesASharedCwdWithinAPoll(t *testing.T) {
	calls := 0
	withResolveBranch(t, func(cwd string) string { calls++; return "main" })
	now := time.Now()

	fresh := stampBranches(nil, []RegistryEntry{
		{Pid: 1, Kind: "pi", Cwd: "/w/a"},
		{Pid: 2, Kind: "pi", Cwd: "/w/a"},
	}, now)

	if calls != 1 {
		t.Fatalf("resolveBranch called %d times, want 1 for two entries sharing a cwd", calls)
	}
	if fresh[0].Branch != "main" || fresh[1].Branch != "main" {
		t.Fatalf("got %+v, want both entries stamped", fresh)
	}
}

func TestStampBranchesSkipsAnUnknownCwd(t *testing.T) {
	withResolveBranch(t, func(cwd string) string {
		t.Fatal("resolveBranch called for an entry with no cwd")
		return ""
	})

	fresh := stampBranches(nil, []RegistryEntry{{Pid: 1, Kind: "pi"}}, time.Now())

	if fresh[0].Branch != "" || !fresh[0].BranchResolvedAt.IsZero() {
		t.Fatalf("got %+v, want Branch untouched for an unknown cwd", fresh[0])
	}
}

func TestMarkVSCodeWindowsStampsOpenAndClosed(t *testing.T) {
	snapshot := fakeVSCodeSnapshot{open: map[string]bool{"/w/a": true}}
	entries := []RegistryEntry{
		{Pid: 1, Kind: "pi", Cwd: "/w/a", Branch: "main"},
		{Pid: 2, Kind: "pi", Cwd: "/w/b", Branch: "main"},
	}

	marked := markVSCodeWindows(entries, snapshot)

	if marked[0].VSCode != VSCodeOpen || marked[1].VSCode != VSCodeClosed {
		t.Fatalf("got %v, %v; want open, closed", marked[0].VSCode, marked[1].VSCode)
	}
}

func TestMarkVSCodeWindowsPassesTheBranchToTheMatch(t *testing.T) {
	// The branch is what tells same-named worktree folders apart in the
	// window match (see mycelium's matchVSCodeWindowTitle); the entry's
	// resolved Branch must reach IsOpen, not "".
	var gotBranch string
	snapshot := fakeVSCodeSnapshot{}
	entries := []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a", Branch: "fix-x"}}
	spy := &spySnapshot{inner: snapshot, seen: &gotBranch}

	markVSCodeWindows(entries, spy)

	if gotBranch != "fix-x" {
		t.Fatalf("IsOpen saw branch %q, want %q", gotBranch, "fix-x")
	}
}

type spySnapshot struct {
	inner vscodeSnapshot
	seen  *string
}

func (s *spySnapshot) Err() error { return s.inner.Err() }

func (s *spySnapshot) IsOpen(path, branch string) bool {
	*s.seen = branch
	return s.inner.IsOpen(path, branch)
}

func TestMarkVSCodeWindowsLeavesEverythingUnknownOnAListingError(t *testing.T) {
	snapshot := fakeVSCodeSnapshot{err: errors.New("not authorized"), noCall: true}
	entries := []RegistryEntry{{Pid: 1, Kind: "pi", Cwd: "/w/a", Branch: "main"}}

	marked := markVSCodeWindows(entries, snapshot)

	if marked[0].VSCode != VSCodeUnknown {
		t.Fatalf("got %v, want VSCodeUnknown (a failed listing can't claim closed)", marked[0].VSCode)
	}
}

func TestMarkVSCodeWindowsLeavesAnUnknownCwdUnknown(t *testing.T) {
	snapshot := fakeVSCodeSnapshot{open: map[string]bool{"/w/a": true}}
	entries := []RegistryEntry{{Pid: 1, Kind: "pi"}}

	marked := markVSCodeWindows(entries, snapshot)

	if marked[0].VSCode != VSCodeUnknown {
		t.Fatalf("got %v, want VSCodeUnknown for an entry with no cwd", marked[0].VSCode)
	}
}

func TestPollOnceStampsTheVSCodeColumn(t *testing.T) {
	// The full-poll wiring: the snapshot is taken once, concurrently with
	// the ps scans, and every entry comes out stamped.
	previousScan, previousTable := scanAgentProcesses, scanProcessTable
	t.Cleanup(func() { scanAgentProcesses, scanProcessTable = previousScan, previousTable })
	scanAgentProcesses = func(string) ([]scan.ProcessMatch, error) {
		return []scan.ProcessMatch{{Pid: 1, Tty: "s001", Kind: "pi", Args: "pi"}}, nil
	}
	scanProcessTable = func() map[int]scan.ProcessInfo { return map[int]scan.ProcessInfo{} }
	withResolveCwds(t, func([]int) map[int]string { return map[int]string{1: "/w/a"} })
	withResolveBranch(t, func(string) string { return "main" })
	withSnapshotVSCode(t, fakeVSCodeSnapshot{open: map[string]bool{"/w/a": true}})

	result := PollOnce("someuser", nil)

	if len(result.Entries) != 1 {
		t.Fatalf("got %+v, want one entry", result.Entries)
	}
	if got := result.Entries[0].VSCode; got != VSCodeOpen {
		t.Fatalf("got VSCode %v, want VSCodeOpen", got)
	}
	if got := result.Entries[0].Branch; got != "main" {
		t.Fatalf("got Branch %q, want %q", got, "main")
	}
}
