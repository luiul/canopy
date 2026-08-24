// Package pistatus reads the small per-pid status file canopy's optional
// companion pi extension (see ../../extensions/canopy-status.ts, installed
// at ~/.pi/agent/extensions/canopy-status.ts) writes for a running `pi`
// process: working/idle/done sourced straight from pi's own agent-lifecycle
// events (before_agent_start, agent_start, tool_execution_start,
// agent_settled), not guessed from CPU usage the way internal/state has to
// for every agent kind canopy has no pty for.
//
// This is the one gap README.md's Limitations section calls out ("Idle/
// working for non-`pi` surfaces is a CPU% heuristic, not a real status"):
// true for every kind except `pi`, once this extension is installed, since
// `pi` is the one agent canopy can actually ask directly instead of
// guessing from the outside. Without the extension installed, Read simply
// never finds a file and registry falls back to the CPU heuristic exactly
// as before; nothing here changes behavior for anyone who hasn't opted in.
package pistatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// MaxAge is how stale a status file can be before Read stops trusting it,
// so callers fall back to the CPU heuristic instead: covers a `pi` process
// that died without running its session_shutdown/process-exit cleanup
// (SIGKILL, a closed terminal tab, a crashed Node/Bun process), whose
// status file was never removed and would otherwise read as whatever state
// it was in when it died, forever.
const MaxAge = 10 * time.Second

// Status is one pid's last self-reported state ("working", "idle", or
// "done"; see canopy-status.ts for the exact transitions, and
// docs/agent-state-machine.md for why "blocked" isn't among them).
type Status struct {
	Pid       int
	Cwd       string
	State     string
	UpdatedAt time.Time
}

// wireStatus mirrors canopy-status.ts's JSON.stringify shape exactly.
type wireStatus struct {
	Pid       int       `json:"pid"`
	Cwd       string    `json:"cwd"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Dir is where canopy-status.ts writes one <pid>.json file per running `pi`
// process it's attached to. Exported so the extension's own docs and
// canopy's tests have one canonical path to point at.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "canopy-status")
}

// Read returns pid's status from Dir(), if canopy-status.ts wrote one
// recently enough to trust (see MaxAge). ok=false (no file, unreadable,
// malformed, empty state, or stale) means exactly one thing to callers:
// fall back to the CPU heuristic for this entry, the same as if the
// extension weren't installed at all.
func Read(pid int) (Status, bool) {
	return ReadDir(Dir(), pid, time.Now())
}

// ReadDir is Read with an explicit dir and "now", so tests can point it at
// a temp directory and a fixed clock instead of the real home directory
// and wall-clock time.
func ReadDir(dir string, pid int, now time.Time) (Status, bool) {
	if dir == "" {
		return Status{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".json"))
	if err != nil {
		return Status{}, false
	}
	return parse(data, now)
}

func parse(data []byte, now time.Time) (Status, bool) {
	var w wireStatus
	if err := json.Unmarshal(data, &w); err != nil {
		return Status{}, false
	}
	if w.State == "" {
		return Status{}, false
	}
	if now.Sub(w.UpdatedAt) > MaxAge {
		return Status{}, false
	}
	return Status(w), true
}
