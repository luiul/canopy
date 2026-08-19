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
