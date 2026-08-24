// Package ack lets multiple concurrently running canopy TUI instances
// converge on the same "done episode acknowledged" fact for a session —
// the one piece of internal/tui's dashboard state that isn't already
// derived from a shared, externally observable source. RegistryEntry.State
// itself is safe: every instance reads it independently but identically
// from `ps`/`lsof` and internal/pistatus's per-pid status files, so two
// instances already agree on it for free. Acknowledgment doesn't work that
// way — pressing enter/c in one instance only ever mutates that instance's
// own in-memory internal/tui.Model.done map, and nothing external changes
// as a result, so a second instance has no way to learn about it on its
// own.
//
// This package is the side-channel that closes that gap: one small JSON
// file per acknowledged episode, written by whichever instance the user
// actually acted in, read by every instance (including the one that wrote
// it) on its own poll cadence. No locking, no daemon, and no change to how
// canopy discovers or classifies sessions in the first place — see
// internal/tui/done.go's updateDoneTracking (read side) and
// Model.acknowledge (write side).
//
// One file per key, rather than one shared file for every episode, means
// two instances acknowledging the same episode at the same moment race
// harmlessly (both write the same fact, atomically — write a .tmp file,
// then rename over the real one, the same pattern
// extensions/canopy-status.ts already uses), and acknowledging two
// different episodes never contends on the same file at all.
package ack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxAge is a defensive backstop against an orphaned ack file outliving
// its usefulness forever. In the common case a file lives for at most a
// few poll intervals: internal/tui/done.go removes an episode's ack file
// itself the moment it notices the episode close (the session ends, or an
// acknowledged episode's raw source moves on — see updateDoneTracking).
// But that cleanup only runs from *some* canopy instance's own poll loop;
// if every canopy instance was closed before any of them ever noticed a
// particular episode close, its ack file would otherwise sit on disk
// indefinitely. Read simply stops trusting (and best-effort removes)
// anything older than this, so a stale file self-heals the next time
// anything happens to read it, rather than needing a dedicated sweep.
// Deliberately generous: a done episode can legitimately sit
// unacknowledged for hours if the user's away, and this only needs to
// outlive that.
const MaxAge = 24 * time.Hour

// Record is one acknowledged episode, as written to disk.
type Record struct {
	// Key is registry.RegistryEntry.Key() (e.g. "1234:pi") for the
	// acknowledged row. Redundant with the filename itself; kept in the
	// body too so a reader never has to trust the filename alone.
	Key string `json:"key"`
	// RawAt is the episode's own identity anchor: the raw source's report
	// timestamp for the settle this acknowledgment covers
	// (registry.RegistryEntry.RealStateReportedAt, i.e.
	// pistatus.Status.UpdatedAt). A reader matches this, not just Key,
	// before applying the record to its own episode — see
	// internal/tui/done.go's updateDoneTracking — so a stale record left
	// over from an earlier, already-superseded episode for the same key can
	// never be mistaken for acknowledging a genuinely new one.
	RawAt time.Time `json:"rawAt"`
	// At is when the acknowledging instance actually pressed enter/c.
	At time.Time `json:"at"`
}

// Dir is where every canopy instance writes and reads ack records: a
// subfolder of the same ~/.pi/agent/canopy-status directory
// extensions/canopy-status.ts already owns, but this particular subfolder
// is canopy's own — nothing else writes here — so there's no risk of
// colliding with the extension's own per-pid status files.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "canopy-status", "acks")
}

// fileName turns a registry key ("1234:pi") into a safe filename
// ("1234-pi.json"): ':' is valid in a Unix filename, but there's no reason
// to rely on that holding on every filesystem canopy might ever run on.
func fileName(key string) string {
	return strings.ReplaceAll(key, ":", "-") + ".json"
}

// Read returns key's ack record from Dir(), if one exists and is still
// trusted (see MaxAge).
func Read(key string) (Record, bool) {
	return ReadDir(Dir(), key, time.Now())
}

// ReadDir is Read with an explicit dir and "now", so tests can point it at
// a temp directory and a fixed clock instead of the real home directory
// and wall-clock time.
func ReadDir(dir, key string, now time.Time) (Record, bool) {
	if dir == "" {
		return Record{}, false
	}
	path := filepath.Join(dir, fileName(key))
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, false
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false
	}
	if rec.At.IsZero() || now.Sub(rec.At) > MaxAge {
		_ = os.Remove(path) // best-effort: self-heal an orphaned record the next time anything reads it
		return Record{}, false
	}
	return rec, true
}

// Write persists key's acknowledgment (rawAt identifies which episode; see
// Record.RawAt) so every other running canopy instance picks it up on its
// own next poll. Best-effort: a write failure (e.g. the directory isn't
// creatable) never blocks the local acknowledgment that already happened
// in internal/tui/done.go's Model.acknowledge — only cross-instance sync
// degrades, exactly like every other best-effort signal in canopy.
func Write(key string, rawAt time.Time) error {
	return WriteDir(Dir(), key, rawAt, time.Now())
}

// WriteDir is Write with an explicit dir and "at" timestamp, so tests can
// point it at a temp directory and a fixed clock.
func WriteDir(dir, key string, rawAt, at time.Time) error {
	if dir == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(Record{Key: key, RawAt: rawAt, At: at})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fileName(key))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path) // same filesystem: a reader never sees a half-written file
}

// Remove best-effort deletes key's ack file: called once
// internal/tui/done.go's updateDoneTracking notices the episode it
// belonged to has closed (acknowledged and the raw source has moved off
// done, or the session ended outright), so the acks directory doesn't
// accumulate one file per episode forever. Safe to call from multiple
// instances racing on the same key, and safe to call for a key with no
// file at all (the common case: most episodes are never acknowledged
// through this path in the first place).
func Remove(key string) {
	RemoveDir(Dir(), key)
}

// RemoveDir is Remove with an explicit dir, so tests can point it at a
// temp directory.
func RemoveDir(dir, key string) {
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, fileName(key)))
}
