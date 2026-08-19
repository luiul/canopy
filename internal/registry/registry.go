// Package registry holds the in-memory model of every known-kind agent
// process on the machine right now: which app surface is actually hosting
// it (herdr / VS Code / a bare Ghostty tab / unknown), and its state
// (herdr's own idle/working/blocked/done/unknown for a pane it tracks, a
// CPU-delta idle/working heuristic for anything it doesn't).
//
// No file is written here, canopy holds this only for as long as its own
// process (the TUI) is running; there is no background daemon, no
// LaunchAgent, and nothing is reported back into herdr. PollOnce is meant
// to be called on a timer from canopy's tui package.
package registry

import (
	"fmt"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/herdrclient"
	"github.com/luiul/canopy/internal/scan"
	"github.com/luiul/canopy/internal/state"
)

// MissLimit is how many consecutive missed polls a row survives before
// being dropped. Smooths over a single transient ps/herdr hiccup instead of
// a row flickering away and back while someone is about to press Enter on
// it.
const MissLimit = 1

// RegistryEntry is one row of the dashboard.
type RegistryEntry struct {
	Pid         int
	Kind        string
	Tty         string
	Cwd         string // "" means unknown
	Surface     ancestry.Surface
	State       string
	WorkspaceID string
	TabID       string
	PaneID      string
	Misses      int
}

// Key identifies an entry across polls. pids get reused by the OS; scoping
// the key by kind too avoids two genuinely different processes colliding if
// a pid is recycled between polls faster than the debounce window notices.
func (e RegistryEntry) Key() string {
	return fmt.Sprintf("%d:%s", e.Pid, e.Kind)
}

// herdrEntries builds rows straight from herdr's own data (its
// agent_status is authoritative there) for panes herdr already tracks, and
// the pids to exclude from the plain ps scan, so nothing is listed twice.
func herdrEntries() (trackedPids map[int]bool, entries []RegistryEntry) {
	trackedPids = map[int]bool{}
	for _, pane := range herdrclient.PaneList() {
		if pane.Agent == "" || pane.PaneID == "" {
			continue
		}
		info := herdrclient.PaneProcessInfo(pane.PaneID)
		if info == nil || info.ForegroundProcessGroupID == nil {
			continue
		}
		fgPid := int(*info.ForegroundProcessGroupID)
		trackedPids[fgPid] = true
		entries = append(entries, RegistryEntry{
			Pid:         fgPid,
			Kind:        pane.Agent,
			Tty:         "",
			Cwd:         pane.Cwd,
			Surface:     ancestry.Herdr,
			State:       orDefault(pane.AgentStatus, "unknown"),
			WorkspaceID: pane.WorkspaceID,
			TabID:       pane.TabID,
			PaneID:      pane.PaneID,
		})
	}
	return trackedPids, entries
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// externalEntries classifies which app surface hosts every scanned agent
// process herdr doesn't already track, and estimates idle/working from CPU
// usage, since there's no pty here for canopy to read a real status from.
func externalEntries(matches []scan.ProcessMatch, excludePids map[int]bool) []RegistryEntry {
	var candidates []scan.ProcessMatch
	for _, m := range matches {
		if !excludePids[m.Pid] {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	table := scan.ScanProcessTable()
	pids := make([]int, len(candidates))
	for i, m := range candidates {
		pids[i] = m.Pid
	}
	cwdByPid := scan.ResolveCwds(pids)

	entries := make([]RegistryEntry, 0, len(candidates))
	for _, m := range candidates {
		surface := ancestry.ClassifySurface(m.Pid, table, false)
		var pcpu *float64
		if info, ok := table[m.Pid]; ok {
			v := info.Pcpu
			pcpu = &v
		}
		entries = append(entries, RegistryEntry{
			Pid:     m.Pid,
			Kind:    m.Kind,
			Tty:     m.Tty,
			Cwd:     cwdByPid[m.Pid],
			Surface: surface,
			State:   string(state.ClassifyStateDefault(pcpu)),
		})
	}
	return entries
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

// PollOnce takes one full snapshot: herdr-tracked panes plus every other
// known-kind agent process, merged against the previous snapshot so a
// single transient miss doesn't flicker a row away.
func PollOnce(user string, previous []RegistryEntry) []RegistryEntry {
	trackedPids, herdrRows := herdrEntries()
	matches := scan.ScanAgentProcesses(user)
	externalRows := externalEntries(matches, trackedPids)
	fresh := append(herdrRows, externalRows...)
	return MergeRegistry(previous, fresh)
}
