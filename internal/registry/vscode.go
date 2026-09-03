package registry

// This file holds the poll's VS Code enrichment: the VS Code column's
// per-entry answer to "is a VS Code window already open on this agent's
// working directory?". Split out of registry.go because it's a separate
// enrichment source (mycelium's AppleScript window listing plus a git
// branch resolution) layered onto the process-table model registry.go
// owns, the same way pistatus is pi's own separate source.
//
// The answer comes from mycelium.SnapshotVSCode, which runs the exact
// match cascade jump.To's OpenVSCode call runs on Enter: a row reads
// "open" precisely when Enter would focus an existing window instead of
// opening a new one. One snapshot is taken per poll (a single osascript
// call), shared across every entry, with git work-tree lookups memoized
// inside it.

import (
	"os/exec"
	"strings"
	"time"

	"github.com/luiul/dashkit/mycelium"
)

// VSCodeState is the tri-state answer to "is a VS Code window open on
// this entry's working directory?" for the TUI's VS Code column.
type VSCodeState int

const (
	// VSCodeUnknown is the zero value on purpose: an entry the poll
	// couldn't answer for (the window listing failed — most likely the
	// macOS Automation permission not granted yet — or the entry has no
	// known cwd to match on) renders "?", never "not open", because
	// neither situation can claim closed.
	VSCodeUnknown VSCodeState = iota
	VSCodeClosed              // checked, no window open on the cwd's tree
	VSCodeOpen                // a window is open on the cwd (or its work-tree root, or inside its tree)
)

// vscodeSnapshot is the slice of mycelium.VSCodeSnapshot a poll needs,
// kept to an interface so tests can feed markVSCodeWindows a fake
// without osascript (see registry_test.go).
type vscodeSnapshot interface {
	Err() error
	IsOpen(path, branch string) bool
}

// snapshotVSCode and resolveBranch are package-level seams onto the real
// window listing and branch resolution, swapped out in tests — the same
// seam pattern registry.go uses for its scan functions.
var (
	snapshotVSCode = func() vscodeSnapshot { return mycelium.SnapshotVSCode() }
	resolveBranch  = gitBranch
)

// branchTTL is how long a resolved branch stays trusted on an entry
// before stampBranches re-resolves it. jump.go resolves the branch fresh
// at jump time (one subprocess, only for the row jumped to); the poll
// can't afford that per row per 2s tick, and a branch only changes on a
// checkout, so a minute of staleness is the right trade — a checkout is
// reflected by the next expiry at the latest.
const branchTTL = time.Minute

// gitBranch resolves the branch currently checked out in the git work
// tree containing cwd ("" for a cwd outside any work tree, or a detached
// HEAD, which rev-parse reports as "HEAD"). The same resolution jump.go
// does at jump time; duplicated here because registry can't import jump
// (jump imports registry for RegistryEntry) and the resolution itself is
// one git invocation either way.
func gitBranch(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		branch = ""
	}
	return branch
}

// stampBranches sets Branch/BranchResolvedAt on every fresh entry with a
// known cwd: carried over unchanged from the previous poll's entry while
// still fresh (same key, same cwd, resolved less than branchTTL ago), or
// (re-)resolved from git otherwise — on first sight, on a cwd change, on
// expiry. A cwd outside any git work tree resolves to "" and is carried
// over exactly like a real branch, so a non-repo cwd doesn't fork git on
// every poll either. Resolutions are additionally memoized across the
// entries of a single poll, so several agents sharing one cwd cost one
// git call per expiry window, not one per entry.
func stampBranches(previous, fresh []RegistryEntry, now time.Time) []RegistryEntry {
	prevByKey := make(map[string]RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	resolved := map[string]string{}
	for i := range fresh {
		if fresh[i].Cwd == "" {
			continue // nothing to resolve; Branch stays ""
		}
		prev, ok := prevByKey[fresh[i].Key()]
		if ok && prev.Cwd == fresh[i].Cwd && !prev.BranchResolvedAt.IsZero() && now.Sub(prev.BranchResolvedAt) < branchTTL {
			fresh[i].Branch = prev.Branch
			fresh[i].BranchResolvedAt = prev.BranchResolvedAt
			continue
		}
		branch, memoized := resolved[fresh[i].Cwd]
		if !memoized {
			branch = resolveBranch(fresh[i].Cwd)
			resolved[fresh[i].Cwd] = branch
		}
		fresh[i].Branch = branch
		fresh[i].BranchResolvedAt = now
	}
	return fresh
}

// markVSCodeWindows stamps every fresh entry's VSCode field from the
// poll's window snapshot, keyed on the entry's cwd and branch (see
// stampBranches). A failed listing (snapshot.Err) leaves every entry at
// VSCodeUnknown: the snapshot can't tell open from closed, so no entry
// may claim either. An entry with no known cwd is VSCodeUnknown too —
// nothing solid to match on, the same "?" Location shows for it.
//
// Debounced entries surviving a missed poll via MergeRegistry keep
// whatever VSCode value their last fresh poll stamped (at most MissLimit
// polls stale): a window state from two seconds ago is a better answer
// than a blanket "?" flickering in and out over a transient ps hiccup.
func markVSCodeWindows(entries []RegistryEntry, snapshot vscodeSnapshot) []RegistryEntry {
	if snapshot.Err() != nil {
		return entries // zero value VSCodeUnknown everywhere
	}
	for i := range entries {
		if entries[i].Cwd == "" {
			continue // VSCodeUnknown
		}
		if snapshot.IsOpen(entries[i].Cwd, entries[i].Branch) {
			entries[i].VSCode = VSCodeOpen
		} else {
			entries[i].VSCode = VSCodeClosed
		}
	}
	return entries
}
