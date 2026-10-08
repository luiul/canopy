// Package registry holds the in-memory model of every tracked agent-kind
// process on the machine right now: which app surface is actually hosting
// it (VS Code / a bare Ghostty tab / unknown), and its state
// (canopy-status.ts's real working/blocked/done/error/idle, straight from
// pi's own program-status state machine, for a `pi` process that has it
// installed, see internal/pistatus; "unknown" for anything else — canopy
// tracks pi sessions on this machine only, so there is no CPU-usage
// fallback to guess from).
//
// No file is written here, canopy holds this only for as long as its own
// process (the TUI) is running; there is no background daemon, no
// LaunchAgent. PollOnce is meant to be called on a timer from canopy's tui
// package.
package registry

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/pistatus"
	"github.com/luiul/canopy/internal/scan"
)

// pistatusRead is a package-level seam onto pistatus.Read, swapped out in
// tests (see registry_test.go) so externalEntries' pi-status merge logic
// (RealState/RealStateReportedAt handling) can be exercised against a
// canned Status without a real ~/.pi/agent/canopy-status/<pid>.json file
// on disk, the same seam pattern internal/jump already uses for mycelium's
// OpenVSCode/OpenGhostty.
var pistatusRead = pistatus.Read

// pistatusReadModel is independent of pistatusRead: the model report stays
// fresh while pi is idle, without changing the state write's timestamp.
var pistatusReadModel = pistatus.ReadModel

// resolveCwds is a package-level seam onto scan.ResolveCwds, swapped out in
// tests so externalEntries can be exercised without shelling out to lsof.
var resolveCwds = scan.ResolveCwds

// scanAgentProcesses and scanProcessTable are package-level seams onto
// scan's own exec.Command wrappers, swapped out in tests so PollOnce's
// warning-surfacing and merge logic (see PollResult) can be exercised
// without a real `ps` subprocess.
var (
	scanAgentProcesses = scan.ScanAgentProcesses
	scanProcessTable   = scan.ScanProcessTable
)

// MissLimit is how many consecutive missed polls a row survives before
// being dropped. Smooths over a single transient ps/pistatus hiccup instead
// of a row flickering away and back while someone is about to press Enter on
// it.
const MissLimit = 1

// RegistryEntry is one row of the dashboard.
type RegistryEntry struct {
	Pid     int
	Kind    string
	Tty     string
	Cwd     string // "" means unknown
	Surface ancestry.Surface
	State   string
	// ModelName and ModelProvider are pi's selected model, supplied by the
	// optional companion extension. Empty for other agent kinds or when the
	// model report is missing or stale; independent of RealState.
	ModelName     string
	ModelProvider string
	// StateSince is when State last changed, not when this entry was last
	// seen. Stamped by stampStateSince on every poll: carried over unchanged
	// while State stays the same, reset to the poll time the moment it
	// flips. Used by the TUI to show "how long in this state" and to blink
	// a row that just became done.
	StateSince time.Time
	// CPUPercent is the raw macOS `ps` %cpu sample for this entry's most
	// recent poll (see scan.ProcessInfo.Pcpu): a decaying average over up to
	// a minute of real time, kept purely for the TUI's CPU column — showing
	// the same number `top`/`ps` would. It plays no part in State, which
	// comes from pi's own reports or stays "unknown". Zero for an entry
	// with no sample this poll (already gone from the whole-machine `ps`
	// snapshot by the time it was taken), indistinguishable from a real 0%
	// sample.
	CPUPercent float64
	// RSSKb and Uptime are scan.ProcessInfo's RssKb/Etime, carried straight
	// through for the TUI's RAM and Uptime columns: resident memory in KB,
	// and wall-clock time since the process itself started (not to be
	// confused with StateSince, which is time in the *current state*). Both
	// come from the same whole-machine `ps` snapshot canopy already takes
	// every poll for ancestry/CPU purposes, so displaying them costs nothing
	// extra. Zero when there's no sample for this pid this poll, same caveat
	// as CPUPercent.
	RSSKb  int
	Uptime time.Duration
	// RealState is true when State this poll came from canopy-status.ts (see
	// internal/pistatus): pi self-reporting its own
	// working/blocked/done/error/idle straight from its program-status state
	// machine, rather than the "unknown" anything without a fresh report
	// gets.
	RealState bool
	// RealStateReportedAt is pistatus.Status.UpdatedAt for a RealState entry:
	// the moment canopy-status.ts itself wrote this State, not the moment
	// canopy polled it. Zero for anything else (an "unknown" entry has no
	// such source timestamp). This is the one piece of information that can
	// tell a genuinely new "done"/"error" write apart from the same
	// still-fresh one repeating across polls when the State string alone
	// can't: the extension's terminal writes are one-shot but pistatus.Read
	// keeps returning them for as long as the process lives (they are
	// exempt from pistatus.MaxAge), and if a second turn starts and settles
	// again without canopy ever sampling a "working" poll in between,
	// State reads "done" on both sides with nothing to tell them apart —
	// except this timestamp, which advances on the second write even though
	// the string doesn't. internal/tui's updateDoneTracking/needsBell use
	// it for exactly that.
	RealStateReportedAt time.Time

	// Stopped is true when the process itself is currently stopped (SIGSTOP,
	// ps state "T" — see scan.ProcessInfo.Stopped), e.g. paused via canopy's
	// own p keybind. The TUI overlays this as a synthetic "stopped" display
	// state; the raw State field keeps whatever pistatus last said (or
	// "unknown"), exactly like the done overlay keeps display and raw apart
	// (see internal/tui's displayState).
	Stopped bool

	Misses int
}

// Key identifies an entry across polls. pids get reused by the OS; scoping
// the key by kind too avoids two genuinely different processes colliding if
// a pid is recycled between polls faster than the debounce window notices.
func (e RegistryEntry) Key() string {
	return fmt.Sprintf("%d:%s", e.Pid, e.Kind)
}

// externalEntries classifies which app surface hosts every scanned agent
// process, and merges pi's own self-reported state for the pids that have
// one (everything else reads "unknown").
//
// table is the whole-machine process snapshot (see scan.ScanProcessTable)
// used for ancestry classification and the CPU/RAM/Uptime columns; it's a
// parameter rather than fetched here directly so PollOnce can take that
// snapshot concurrently with the agent-kind scan (see PollOnce), and so
// tests can hand externalEntries a small, hand-built table instead of a
// live `ps -A` snapshot.
func externalEntries(matches []scan.ProcessMatch, table map[int]scan.ProcessInfo) []RegistryEntry {
	if len(matches) == 0 {
		return nil
	}

	pids := make([]int, len(matches))
	for i, m := range matches {
		pids[i] = m.Pid
	}
	cwdByPid := resolveCwds(pids)

	entries := make([]RegistryEntry, 0, len(matches))
	for _, m := range matches {
		surface := ancestry.ClassifySurface(m.Pid, table)
		var cpuPercent float64
		var rssKb int
		var uptime time.Duration
		var stopped bool
		if info, ok := table[m.Pid]; ok {
			cpuPercent = info.Pcpu
			rssKb = info.RssKb
			uptime = info.Etime
			stopped = info.Stopped()
		}
		entry := RegistryEntry{
			Pid:        m.Pid,
			Kind:       m.Kind,
			Tty:        m.Tty,
			Cwd:        cwdByPid[m.Pid],
			Surface:    surface,
			State:      "unknown",
			CPUPercent: cpuPercent,
			RSSKb:      rssKb,
			Uptime:     uptime,
			Stopped:    stopped,
		}
		// `pi` is the one agent kind canopy can ask directly: canopy-status.ts
		// (see internal/pistatus) writes pi's own real
		// working/blocked/done/error/idle straight from its program-status
		// state machine when it's installed. No file (extension not
		// installed, stale, or this pid isn't actually `pi`) just leaves the
		// "unknown" above in place.
		if m.Kind == "pi" {
			if st, ok := pistatusRead(m.Pid); ok {
				entry.State = st.State
				entry.RealState = true
				entry.RealStateReportedAt = st.UpdatedAt
				if entry.Cwd == "" {
					entry.Cwd = st.Cwd
				}
			}
		}
		// Model reporting has its own freshness window: an idle pi session can
		// still have a valid model even after its state report expires. Read
		// it independently rather than gating it on RealState.
		if m.Kind == "pi" {
			if model, ok := pistatusReadModel(m.Pid); ok {
				entry.ModelName = model.Name
				entry.ModelProvider = model.Provider
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// stampStateSince sets StateSince on every fresh entry: carried over from
// the previous entry with the same key when State hasn't changed, or reset
// to now for a brand new entry or one whose State just flipped. Stopped is
// part of the comparison too: pausing/resuming a process leaves raw State
// alone (a stopped process reads 0% CPU, i.e. "idle" either way), but the
// dashboard displays it as its own synthetic state (see RegistryEntry.
// Stopped), so the Since column must restart at the pause/resume moment
// rather than silently inheriting however long the row had already been
// idle. Runs before MergeRegistry so a debounced (momentarily-missing)
// entry that survives via MergeRegistry keeps whatever StateSince it
// already had, untouched.
func stampStateSince(previous, fresh []RegistryEntry, now time.Time) []RegistryEntry {
	prevByKey := make(map[string]RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	for i := range fresh {
		if prev, ok := prevByKey[fresh[i].Key()]; ok && prev.State == fresh[i].State && prev.Stopped == fresh[i].Stopped && !prev.StateSince.IsZero() {
			fresh[i].StateSince = prev.StateSince
		} else {
			fresh[i].StateSince = now
		}
	}
	return fresh
}

// MergeRegistry keeps entries from previous that are momentarily missing
// from fresh (within MissLimit), and otherwise prefers the fresh copy.
func MergeRegistry(previous, fresh []RegistryEntry) []RegistryEntry {
	freshByKey := map[string]bool{}
	for _, e := range fresh {
		freshByKey[e.Key()] = true
	}

	merged := make([]RegistryEntry, 0, len(previous)+len(fresh))
	for _, prev := range previous {
		if freshByKey[prev.Key()] {
			continue // fresh entry for this key is added below, in fresh's own order
		}
		prev.Misses++
		if prev.Misses <= MissLimit {
			merged = append(merged, prev)
		}
	}

	merged = append(merged, fresh...)
	return merged
}

// PollResult is one full poll's outcome: the merged entries, plus a
// non-empty Warning whenever the primary agent-kind scan (scan.
// ScanAgentProcesses) itself failed to run at all — missing binary,
// sandboxed environment, permissions, a hung `ps` past scan.execTimeout —
// as opposed to running fine and simply finding zero matching processes.
// On a failure Entries is the previous snapshot verbatim (see PollOnce),
// and Warning is what tells the user the table is stale rather than
// silently looking identical to "no sessions".
type PollResult struct {
	Entries []RegistryEntry
	Warning string
}

// PollOnce takes one full snapshot of every tracked agent-kind process,
// merged against the previous snapshot so a single transient miss doesn't
// flicker a row away. If the scan itself fails, the previous snapshot is
// returned verbatim instead (with a Warning): a failed scan is no
// evidence anything exited, so nothing is aged toward MissLimit eviction.
// kinds is the complete tracked set of executable basenames (see
// internal/config), threaded straight through to scan.ScanAgentProcesses.
//
// The agent-kind scan (scan.ScanAgentProcesses) and the whole-machine
// process table (scan.ScanProcessTable) are independent `ps` invocations —
// neither's output feeds the other — so they run concurrently rather than
// back to back; only scan.ResolveCwds (inside externalEntries) has to wait
// for the agent-kind scan's pids first.
func PollOnce(user string, kinds map[string]bool, previous []RegistryEntry) PollResult {
	now := time.Now()

	var (
		matches []scan.ProcessMatch
		scanErr error
		table   map[int]scan.ProcessInfo
	)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		matches, scanErr = scanAgentProcesses(user, kinds)
	}()
	go func() {
		defer wg.Done()
		table = scanProcessTable()
	}()
	wg.Wait()

	if scanErr != nil {
		// A failed scan says nothing about whether the agents exited: they
		// may be perfectly alive, we simply don't know. Return the
		// previous snapshot verbatim — no MergeRegistry, no Misses aging —
		// so a transient `ps` hang doesn't evict rows that MissLimit
		// would otherwise drop after two failed polls (4s at the default
		// interval). The warning banner is the signal that the table is
		// stale; the next successful poll re-baselines from these
		// preserved entries as usual.
		var warning string
		if errors.Is(scanErr, scan.ErrScanTimeout) {
			warning = "agent process scan timed out (system overloaded?); showing last known sessions"
		} else {
			warning = fmt.Sprintf("agent process scan failed: %v", scanErr)
		}
		return PollResult{Entries: previous, Warning: warning}
	}

	rows := externalEntries(matches, table)
	rows = stampStateSince(previous, rows, now)
	return PollResult{Entries: MergeRegistry(previous, rows)}
}
