// Package registry holds the in-memory model of every known-kind agent
// process on the machine right now: which app surface is actually hosting
// it (VS Code / a bare Ghostty tab / unknown), and its state
// (canopy-status.ts's real working/idle/done, straight from pi's own
// agent-lifecycle events, for a `pi` process that has it installed, see
// internal/pistatus; a poll-to-poll CPU-delta idle/working heuristic for
// any other agent kind).
//
// No file is written here, canopy holds this only for as long as its own
// process (the TUI) is running; there is no background daemon, no
// LaunchAgent. PollOnce is meant to be called on a timer from canopy's tui
// package.
package registry

import (
	"fmt"
	"sync"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/pistatus"
	"github.com/luiul/canopy/internal/scan"
	"github.com/luiul/canopy/internal/state"
)

// pistatusRead is a package-level seam onto pistatus.Read, swapped out in
// tests (see registry_test.go) so externalEntries' pi-status merge logic
// (RealState/RealStateReportedAt handling) can be exercised against a
// canned Status without a real ~/.pi/agent/canopy-status/<pid>.json file
// on disk, the same seam pattern internal/jump already uses for mycelium's
// OpenVSCode/OpenGhostty.
var pistatusRead = pistatus.Read

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
	// StateSince is when State last changed, not when this entry was last
	// seen. Stamped by stampStateSince on every poll: carried over unchanged
	// while State stays the same, reset to the poll time the moment it
	// flips. Used by the TUI to show "how long in this state" and to blink
	// a row that just became done.
	StateSince time.Time
	// CPUTime and CPUSampledAt back refineExternalStates' delta-based
	// idle/working correction: macOS's own `ps` %cpu is a decaying average
	// over up to a minute of real time (see `man ps`), so a process that was
	// genuinely busy a while ago can still read as "working" for up to a
	// minute after it's actually gone idle. Comparing this process's
	// cumulative CPU time against the previous poll's sample, over the actual
	// wall-clock gap between those two polls, gives a rate bounded by
	// canopy's own poll interval instead.
	CPUTime      time.Duration
	CPUSampledAt time.Time
	// CPUPercent is the raw macOS `ps` %cpu sample for this entry's most
	// recent poll (see scan.ProcessInfo.Pcpu): a decaying average over up to
	// a minute of real time, exactly like the one refineExternalStates
	// corrects for its own idle/working guess. Kept separately, unrefined,
	// purely for the TUI's CPU column: showing the same number `top`/`ps`
	// would, not canopy's own tightened delta. Zero for an entry with no
	// sample this poll (already gone from the whole-machine `ps` snapshot by
	// the time it was taken), indistinguishable from a real 0% sample — the
	// same tradeoff CPUTime already makes.
	CPUPercent float64
	// RSSKb and Uptime are scan.ProcessInfo's RssKb/Etime, carried straight
	// through for the TUI's RAM and Uptime columns: resident memory in KB,
	// and wall-clock time since the process itself started (not to be
	// confused with StateSince, which is time in the *current state*). Both
	// come from the same whole-machine `ps` snapshot canopy already takes
	// every poll for ancestry/CPU purposes, so displaying them costs nothing
	// extra. Zero when there's no sample for this pid this poll, same caveat
	// as CPUPercent/CPUTime.
	RSSKb  int
	Uptime time.Duration
	// RealState is true when State this poll came from canopy-status.ts (see
	// internal/pistatus) rather than the CPU heuristic: pi self-reporting its
	// own working/idle/done straight from its agent-lifecycle events.
	// refineExternalStates leaves State alone for any entry with RealState
	// set, rather than second-guessing it with a CPU-time delta.
	RealState bool
	// RealStateReportedAt is pistatus.Status.UpdatedAt for a RealState entry:
	// the moment canopy-status.ts itself wrote this State, not the moment
	// canopy polled it. Zero for anything else (CPU-heuristic entries have no
	// such source timestamp). This is the one piece of information that can
	// tell a genuinely new "done" write apart from the same still-fresh one
	// repeating across polls when the State string alone can't: pistatus.Read
	// keeps returning the literal string "done" for up to pistatus.MaxAge
	// after a turn settles, and if a second turn starts and settles again
	// within that same window without canopy ever sampling a "working" poll
	// in between, State reads "done" on both sides with nothing to tell them
	// apart — except this timestamp, which advances on the second write even
	// though the string doesn't. internal/tui's updateDoneTracking/needsBell
	// use it for exactly that.
	RealStateReportedAt time.Time
	// WorkingStreak counts consecutive poll-to-poll samples that read at or
	// above state.DefaultThreshold. refineExternalStates only reports
	// Working once this reaches workingConfirmPolls: a single qualifying
	// poll on its own is exactly a brief CPU blip look like (a heartbeat
	// tick, a GC pause, a terminal redraw), not sustained agent work, and
	// reporting Working for just that one poll is what flickered a
	// genuinely idle session into "working" and back. Dropping back to
	// Idle needs no such debounce: it resets to 0 (and State to Idle) the
	// moment a sample reads below threshold.
	WorkingStreak int

	Misses int
}

// Key identifies an entry across polls. pids get reused by the OS; scoping
// the key by kind too avoids two genuinely different processes colliding if
// a pid is recycled between polls faster than the debounce window notices.
func (e RegistryEntry) Key() string {
	return fmt.Sprintf("%d:%s", e.Pid, e.Kind)
}

// externalEntries classifies which app surface hosts every scanned agent
// process, and guesses idle/working from a single macOS `ps` %cpu sample.
// refineExternalStates corrects that guess with a real poll-to-poll delta
// for every entry that survives to a second poll.
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
		var pcpu *float64
		var cpuTime time.Duration
		var cpuPercent float64
		var rssKb int
		var uptime time.Duration
		if info, ok := table[m.Pid]; ok {
			v := info.Pcpu
			pcpu = &v
			cpuTime = info.CPUTime
			cpuPercent = info.Pcpu
			rssKb = info.RssKb
			uptime = info.Etime
		}
		entry := RegistryEntry{
			Pid:        m.Pid,
			Kind:       m.Kind,
			Tty:        m.Tty,
			Cwd:        cwdByPid[m.Pid],
			Surface:    surface,
			State:      string(state.ClassifyStateDefault(pcpu)),
			CPUTime:    cpuTime,
			CPUPercent: cpuPercent,
			RSSKb:      rssKb,
			Uptime:     uptime,
		}
		// `pi` is the one agent kind canopy can ask directly instead of
		// guessing from CPU: canopy-status.ts (see internal/pistatus) writes
		// pi's own real working/idle/done straight from its agent-lifecycle
		// events when it's installed. No file (extension not installed,
		// stale, or this pid isn't actually `pi`) just leaves the CPU guess
		// above in place.
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
		entries = append(entries, entry)
	}
	return entries
}

// workingConfirmPolls is how many consecutive poll-to-poll samples must
// read at or above state.DefaultThreshold before refineExternalStates
// reports Working, filtering a single-poll CPU blip that isn't sustained
// work (see RegistryEntry.WorkingStreak).
const workingConfirmPolls = 2

// refineExternalStates replaces externalEntries' single-sample State guess
// with one computed from a real delta: how much CPU time this process
// consumed between the previous poll and now, divided by the wall-clock
// gap between those two polls. That's bounded by canopy's own poll
// interval, not macOS's ~60s decaying-average window, so it reflects
// recent activity far more tightly and doesn't keep reporting "working"
// long after a burst of work has actually finished.
//
// It additionally requires workingConfirmPolls consecutive qualifying
// samples before actually reporting Working (see WorkingStreak): even a
// tight poll-to-poll delta still can't tell a real burst of agent work
// apart from a single brief CPU blip (a heartbeat tick, a GC pause, a
// terminal redraw) on an otherwise idle process, and it's exactly those
// one-off blips that flickered a genuinely idle session into "working" for
// one poll and back. Dropping to Idle is immediate; only the climb into
// Working is debounced.
//
// A brand new entry (no previous sample to diff against yet) keeps
// externalEntries' single-sample guess for this one poll; there's nothing
// to compute a delta from until the next poll. Same for a negative delta
// (a reused pid, or `ps`'s own counter doing something unexpected): rather
// than report a nonsensical rate, keep the existing guess and streak.
func refineExternalStates(previous, fresh []RegistryEntry, now time.Time) []RegistryEntry {
	prevByKey := make(map[string]RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	for i := range fresh {
		if fresh[i].RealState {
			// pistatus already told us the truth this poll (see externalEntries);
			// don't let the CPU-time heuristic, built for every agent kind that
			// can't do that, second-guess it.
			fresh[i].CPUSampledAt = now
			continue
		}
		prev, ok := prevByKey[fresh[i].Key()]
		if !ok || prev.CPUSampledAt.IsZero() {
			fresh[i].CPUSampledAt = now
			continue // bootstrap: no previous sample to diff against yet
		}
		elapsed := now.Sub(prev.CPUSampledAt)
		delta := fresh[i].CPUTime - prev.CPUTime
		if elapsed <= 0 || delta < 0 {
			fresh[i].WorkingStreak = prev.WorkingStreak // can't compute a sane rate this poll; leave the guess and streak alone
			fresh[i].CPUSampledAt = now
			continue
		}
		rate := delta.Seconds() / elapsed.Seconds() * 100
		if state.ClassifyStateFromRate(rate, state.DefaultThreshold) == state.Working {
			fresh[i].WorkingStreak = prev.WorkingStreak + 1
		} else {
			fresh[i].WorkingStreak = 0
		}
		if fresh[i].WorkingStreak >= workingConfirmPolls {
			fresh[i].State = string(state.Working)
		} else {
			fresh[i].State = string(state.Idle)
		}
		fresh[i].CPUSampledAt = now
	}
	return fresh
}

// stampStateSince sets StateSince on every fresh entry: carried over from
// the previous entry with the same key when State hasn't changed, or reset
// to now for a brand new entry or one whose State just flipped. Runs before
// MergeRegistry so a debounced (momentarily-missing) entry that survives
// via MergeRegistry keeps whatever StateSince it already had, untouched.
func stampStateSince(previous, fresh []RegistryEntry, now time.Time) []RegistryEntry {
	prevByKey := make(map[string]RegistryEntry, len(previous))
	for _, p := range previous {
		prevByKey[p.Key()] = p
	}
	for i := range fresh {
		if prev, ok := prevByKey[fresh[i].Key()]; ok && prev.State == fresh[i].State && !prev.StateSince.IsZero() {
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
// Those two situations render identically in Entries (nil either way);
// Warning is what lets the tui package tell them apart instead of
// silently showing the same empty-table placeholder for both.
type PollResult struct {
	Entries []RegistryEntry
	Warning string
}

// PollOnce takes one full snapshot of every known-kind agent process,
// merged against the previous snapshot so a single transient miss doesn't
// flicker a row away.
//
// The agent-kind scan (scan.ScanAgentProcesses) and the whole-machine
// process table (scan.ScanProcessTable) are independent `ps` invocations —
// neither's output feeds the other — so they run concurrently rather than
// back to back; only scan.ResolveCwds (inside externalEntries) has to wait
// for the agent-kind scan's pids first.
func PollOnce(user string, previous []RegistryEntry) PollResult {
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
		matches, scanErr = scanAgentProcesses(user)
	}()
	go func() {
		defer wg.Done()
		table = scanProcessTable()
	}()
	wg.Wait()

	var warning string
	if scanErr != nil {
		warning = fmt.Sprintf("agent process scan failed: %v", scanErr)
	}

	rows := externalEntries(matches, table)
	rows = refineExternalStates(previous, rows, now)
	rows = stampStateSince(previous, rows, now)
	return PollResult{Entries: MergeRegistry(previous, rows), Warning: warning}
}
