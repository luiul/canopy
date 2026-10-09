package pistatus

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir string, pid int, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestReadDirReturnsAFreshStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-2 * time.Second)
	writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"working","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)

	got, ok := ReadDir(dir, 123, now)
	if !ok {
		t.Fatalf("got ok=false, want true")
	}
	want := Status{Pid: 123, Cwd: "/x", State: "working", UpdatedAt: updatedAt}
	if got.Pid != want.Pid || got.Cwd != want.Cwd || got.State != want.State || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReadDirRejectsAStaleStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-(MaxAge + time.Second)) // just past MaxAge
	writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"working","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)

	if _, ok := ReadDir(dir, 123, now); ok {
		t.Fatalf("got ok=true for a status past MaxAge, want false")
	}
}

func TestReadDirReturnsFalseForAMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadDir(dir, 999, time.Now()); ok {
		t.Fatalf("got ok=true for a pid with no status file, want false")
	}
}

func TestReadDirReturnsFalseForMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, 123, `not json`)
	if _, ok := ReadDir(dir, 123, time.Now()); ok {
		t.Fatalf("got ok=true for malformed JSON, want false")
	}
}

func TestReadDirReturnsFalseForAnEmptyState(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"","updatedAt":"`+now.Format(time.RFC3339Nano)+`"}`)
	if _, ok := ReadDir(dir, 123, now); ok {
		t.Fatalf("got ok=true for an empty state field, want false")
	}
}

func TestReadDirReturnsFalseForAnEmptyDir(t *testing.T) {
	if _, ok := ReadDir("", 123, time.Now()); ok {
		t.Fatalf("got ok=true for an empty dir, want false")
	}
}

func TestReadDirReturnsTheMessage(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-2 * time.Second)
	writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"blocked","message":"Allow this?","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)

	got, ok := ReadDir(dir, 123, now)
	if !ok {
		t.Fatalf("got ok=false, want true")
	}
	if got.State != "blocked" || got.Message != "Allow this?" {
		t.Fatalf("got %+v, want state blocked with message Allow this?", got)
	}
}

func TestReadDirReturnsDetailAndTaskSingleLined(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-2 * time.Second)
	writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"done","message":"sprint-planning","detail":"TLDR: done\nsecond line","task":"plan the thing","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)

	got, ok := ReadDir(dir, 123, now)
	if !ok {
		t.Fatalf("got ok=false, want true")
	}
	if got.Detail != "TLDR: done second line" || got.Task != "plan the thing" || got.Message != "sprint-planning" {
		t.Fatalf("got %+v, want message/detail/task carried (detail single-lined)", got)
	}
}

func TestReadDirReadsEveryStateTheExtensionWrites(t *testing.T) {
	// The full canopy-status.ts vocabulary (pi's program-status states):
	// parse passes any non-empty state through; these pin the five the
	// extension actually produces.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-time.Second)
	for _, state := range []string{"working", "blocked", "done", "error", "idle"} {
		dir := t.TempDir()
		writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"`+state+`","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)
		got, ok := ReadDir(dir, 123, now)
		if !ok || got.State != state {
			t.Errorf("state %q: got %+v, ok=%v", state, got, ok)
		}
	}
}

func TestReadDirNeverRejectsAStaleTerminalState(t *testing.T) {
	// done/error are one-shot writes (the extension never heartbeats them,
	// because updatedAt is canopy's settle-identity anchor), so staleness
	// is their normal state of being: they must stay readable for as long
	// as the process lives.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-time.Hour) // far past MaxAge
	for _, state := range []string{"done", "error"} {
		dir := t.TempDir()
		writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"`+state+`","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)
		got, ok := ReadDir(dir, 123, now)
		if !ok || got.State != state {
			t.Errorf("state %q: got %+v, ok=%v, want the terminal write to never expire", state, got, ok)
		}
	}
}

func TestReadDirRejectsAStaleBlockedStatus(t *testing.T) {
	// blocked is heartbeated exactly because it can sit for minutes; one
	// that stopped refreshing means the extension is gone, so the entry
	// must fall back to "unknown" like any other non-terminal state.
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-(MaxAge + time.Second))
	writeFile(t, dir, 123, `{"pid":123,"cwd":"/x","state":"blocked","updatedAt":"`+updatedAt.Format(time.RFC3339Nano)+`"}`)

	if _, ok := ReadDir(dir, 123, now); ok {
		t.Fatalf("got ok=true for a blocked status past MaxAge, want false")
	}
}
