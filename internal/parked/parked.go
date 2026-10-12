// Package parked persists canopy's parked session marks: one small JSON
// file per parked pid, so the mark survives a canopy restart and syncs
// across concurrently running canopy instances the same way pistatus's
// own status files do (read on every poll, last writer wins). A mark
// dies with its process: Sweep removes files whose pid no longer scans
// live, and pid reuse then can't resurrect one (Key is pid+kind, and
// macOS recycles pids slowly enough for the sweep window).
//
// The mark is deliberately file-backed rather than held in the TUI: two
// canopy windows must agree on what is parked, and a restarted canopy
// must not silently unshelve rows the user already dealt with. The TUI
// owns all writes (Toggle); Load/Sweep run read-side every poll.
package parked

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/luiul/canopy/internal/pistatus"
	"github.com/luiul/canopy/internal/registry"
)

// Dir is where the mark files live: a "parked" subdirectory of the same
// canopy-status directory the companion extension writes its per-pid
// status files into, keeping every canopy runtime file under one roof.
func Dir() string {
	return filepath.Join(pistatus.Dir(), "parked")
}

// wireMark mirrors the on-disk JSON shape exactly (registry.ParkMark
// carries its own json tags, so this is just those fields plus the pid,
// which lives in the filename on disk but travels inside the file for
// debugging).
type wireMark struct {
	Pid int `json:"pid"`
	registry.ParkMark
}

func markPath(dir string, pid int) string {
	return filepath.Join(dir, fmt.Sprintf("%d.json", pid))
}

// Toggle flips the parked mark for e's pid: parked if no mark file
// exists (writing one atomically, .tmp+rename, so a concurrent reader
// never sees a half-written file), unparked if one does (removing it).
// now stamps ParkedAt; State/ReportedAt snapshot e's current lifecycle
// signal for later follow-up detection (see registry.ParkMark).
// Returns whether the session is parked after the call.
func Toggle(dir string, e registry.RegistryEntry, now time.Time) (bool, error) {
	path := markPath(dir, e.Pid)
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return true, fmt.Errorf("unpark %d: %w", e.Pid, err)
		}
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("park %d: %w", e.Pid, err)
	}
	w := wireMark{
		Pid: e.Pid,
		ParkMark: registry.ParkMark{
			State:      e.State,
			ParkedAt:   now,
			ReportedAt: e.RealStateReportedAt,
		},
	}
	data, err := json.Marshal(w)
	if err != nil {
		return false, fmt.Errorf("park %d: %w", e.Pid, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, fmt.Errorf("park %d: %w", e.Pid, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("park %d: %w", e.Pid, err)
	}
	return true, nil
}

// Load reads every mark in dir, keyed by pid. Unreadable files (corrupt
// JSON, bad names) are skipped rather than failing the poll: a parked
// row rendering live once beats the dashboard erroring out over one bad
// file. A missing directory is normal (nothing parked yet), not an
// error.
func Load(dir string) map[int]registry.ParkMark {
	marks := map[int]registry.ParkMark{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return marks
	}
	for _, f := range entries {
		pid, err := strconv.Atoi(strings.TrimSuffix(f.Name(), ".json"))
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			continue
		}
		var w wireMark
		if json.Unmarshal(data, &w) != nil {
			continue
		}
		marks[pid] = w.ParkMark
	}
	return marks
}

// Sweep removes mark files whose pid is not in live (the session
// exited): a parked row's mark must never outlive its process, or a
// recycled pid would inherit a stranger's shelf. Best-effort like Load:
// a file that can't be removed just gets another chance next poll.
func Sweep(dir string, live map[int]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, f := range entries {
		pid, err := strconv.Atoi(strings.TrimSuffix(f.Name(), ".json"))
		if err != nil {
			continue
		}
		if !live[pid] {
			_ = Remove(dir, pid)
		}
	}
}

// Remove deletes pid's mark file if it exists. Best-effort: a missing
// file or a failed remove just gets another chance on the next sweep.
func Remove(dir string, pid int) error {
	err := os.Remove(markPath(dir, pid))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
