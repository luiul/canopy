// Package pistatus reads the small per-pid status file canopy's optional
// companion pi extension (see ../../extensions/canopy-status.ts, installed
// at ~/.pi/agent/extensions/canopy-status.ts) writes for a running `pi`
// process: working/blocked/done/error/idle sourced straight from pi's own
// program-status state machine (pi v1.1.0's reporter, mirrored by the
// extension event for event).
//
// Without the extension installed, Read simply never finds a file and the
// entry reads "unknown": canopy tracks pi sessions on this machine only,
// so there is deliberately no CPU-usage fallback to guess from (the old
// internal/state heuristic was removed — it structurally could not tell
// "a turn just finished" from "idle for an hour", or "blocked on a
// dialog" from "idle", and pi is the one agent kind that can report the
// truth directly).
package pistatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxAge is how stale a status file can be before Read stops trusting it,
// so the entry reads "unknown" instead: covers a `pi` process whose
// extension stopped heartbeating (a crashed handler, a half-dead
// session), whose working/blocked/idle file would otherwise freeze at
// whatever state it last wrote. done and error are exempt (see parse):
// the extension writes them exactly once, at the transition, and never
// heartbeats them, because updatedAt doubling as canopy's "is this a
// genuinely new settle" identity anchor (internal/tui's done.go/bell.go)
// means a refreshed terminal write would impersonate a brand-new one.
const MaxAge = 10 * time.Second

// Status is one pid's last self-reported state ("working", "blocked",
// "done", "error", or "idle"; see canopy-status.ts for the exact
// transitions, and docs/agent-state-machine.md for how the two terminal
// ones are displayed). Message is the state's optional payload, mirroring
// pi's own program-status reports: the session name for working/done,
// the dialog title for blocked, the first line of the error for error,
// empty otherwise. Detail and Task are canopy's own enrichment (pi's
// reporter has no equivalent): Detail is the rolling activity signal
// (last tool call while working, last assistant line at settle), Task
// the first prompt of the session. All empty when absent.
type Status struct {
	Pid       int
	Cwd       string
	State     string
	Message   string
	Detail    string
	Task      string
	UpdatedAt time.Time
}

// wireStatus mirrors canopy-status.ts's JSON.stringify shape exactly.
type wireStatus struct {
	Pid       int       `json:"pid"`
	Cwd       string    `json:"cwd"`
	State     string    `json:"state"`
	Message   string    `json:"message,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Task      string    `json:"task,omitempty"`
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
// the entry reads "unknown", the same as if the extension weren't
// installed at all.
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
	// The TUI renders Message/Detail/Task on a single line under the row
	// (see internal/tui/message.go); a stray newline from a session name
	// or dialog title would silently become an extra line and desync the
	// line-to-row mapping there. The extension already takes first lines
	// for errors and clips its detail/task fragments; this is
	// belt-and-braces for everything else.
	w.Message = singleLine(w.Message)
	w.Detail = singleLine(w.Detail)
	w.Task = singleLine(w.Task)
	// Terminal states never expire: the extension's done/error writes are
	// one-shot by design (see MaxAge), so staleness is their normal state
	// of being, not a sign of a dead extension. Process liveness is
	// already canopy's job upstream (a dead pi drops out of the ps scan
	// and its row is removed), and a live pi with a done/error file is
	// exactly a session sitting settled — the common case.
	if w.State != "done" && w.State != "error" && now.Sub(w.UpdatedAt) > MaxAge {
		return Status{}, false
	}
	return Status(w), true
}

// singleLine flattens embedded newlines to spaces (see parse's caller
// note): the detail line under a row is exactly one terminal line.
func singleLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
}
