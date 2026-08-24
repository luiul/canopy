package ack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteDirThenReadDirRoundTrips(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rawAt := now.Add(-5 * time.Second)

	if err := WriteDir(dir, "1234:pi", rawAt, now); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}

	got, ok := ReadDir(dir, "1234:pi", now)
	if !ok {
		t.Fatalf("got ok=false, want true")
	}
	if got.Key != "1234:pi" || !got.RawAt.Equal(rawAt) || !got.At.Equal(now) {
		t.Fatalf("got %+v, want Key=1234:pi RawAt=%v At=%v", got, rawAt, now)
	}
}

func TestFileNameSanitizesColonsSoTheKeyIsAValidFilename(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := WriteDir(dir, "1234:pi", now, now); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1234-pi.json")); err != nil {
		t.Fatalf("want a file named 1234-pi.json, got: %v", err)
	}
}

func TestReadDirReturnsFalseForAMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadDir(dir, "999:pi", time.Now()); ok {
		t.Fatalf("got ok=true for a key with no ack file, want false")
	}
}

func TestReadDirReturnsFalseForAnEmptyDir(t *testing.T) {
	if _, ok := ReadDir("", "1234:pi", time.Now()); ok {
		t.Fatalf("got ok=true for an empty dir, want false")
	}
}

func TestReadDirReturnsFalseForMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1234-pi.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, ok := ReadDir(dir, "1234:pi", time.Now()); ok {
		t.Fatalf("got ok=true for malformed JSON, want false")
	}
}

func TestReadDirRejectsAndRemovesAStaleRecord(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	staleAt := now.Add(-(MaxAge + time.Second))
	if err := WriteDir(dir, "1234:pi", staleAt, staleAt); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}

	if _, ok := ReadDir(dir, "1234:pi", now); ok {
		t.Fatalf("got ok=true for a record past MaxAge, want false")
	}
	if _, err := os.Stat(filepath.Join(dir, "1234-pi.json")); !os.IsNotExist(err) {
		t.Fatalf("want the stale record file removed as a side effect, stat err=%v", err)
	}
}

func TestWriteDirCreatesTheDirectoryIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	now := time.Now()
	if err := WriteDir(dir, "1234:pi", now, now); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if _, ok := ReadDir(dir, "1234:pi", now); !ok {
		t.Fatal("want the record readable back after WriteDir created its own directory")
	}
}

func TestRemoveDirDeletesTheFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := WriteDir(dir, "1234:pi", now, now); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}

	RemoveDir(dir, "1234:pi")

	if _, ok := ReadDir(dir, "1234:pi", now); ok {
		t.Fatal("want the record gone after RemoveDir")
	}
}

func TestRemoveDirIsANoOpForAMissingFile(t *testing.T) {
	dir := t.TempDir()
	RemoveDir(dir, "999:pi") // must not panic or error out loud
}

func TestRemoveDirIsANoOpForAnEmptyDir(t *testing.T) {
	RemoveDir("", "1234:pi") // must not panic
}
