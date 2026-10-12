package parked

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

func testEntry(pid int) registry.RegistryEntry {
	return registry.RegistryEntry{
		Pid:     pid,
		Kind:    "pi",
		Surface: ancestry.Ghostty,
		State:   "done",
	}
}

func TestToggleWritesThenRemovesTheMarkFile(t *testing.T) {
	dir := t.TempDir()
	e := testEntry(7)
	e.RealState = true
	e.RealStateReportedAt = time.Now().Add(-time.Minute)
	parkedAt := time.Now()

	parkedNow, err := Toggle(dir, e, parkedAt)
	if err != nil || !parkedNow {
		t.Fatalf("first Toggle = (%v, %v), want (true, nil)", parkedNow, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "7.json"))
	if err != nil {
		t.Fatalf("want 7.json written: %v", err)
	}
	var w wireMark
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatalf("mark file is not valid JSON: %v", err)
	}
	if w.Pid != 7 || w.State != "done" || !w.ParkedAt.Equal(parkedAt) || !w.ReportedAt.Equal(e.RealStateReportedAt) {
		t.Fatalf("mark = %+v, want pid 7, state done, and the passed timestamps", w)
	}
	// The atomic write must not leave its staging file behind.
	if _, err := os.Stat(filepath.Join(dir, "7.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("want no .tmp file left behind")
	}

	parkedNow, err = Toggle(dir, e, time.Now())
	if err != nil || parkedNow {
		t.Fatalf("second Toggle = (%v, %v), want (false, nil)", parkedNow, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "7.json")); !os.IsNotExist(err) {
		t.Fatal("want 7.json removed")
	}
}

func TestLoadRoundTripsAndSkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Toggle(dir, testEntry(7), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-pid.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	marks := Load(dir)
	if len(marks) != 1 {
		t.Fatalf("got %d marks, want just the valid one", len(marks))
	}
	if _, ok := marks[7]; !ok {
		t.Fatalf("marks keyed by %v, want pid 7 present", marks)
	}
}

func TestLoadOnAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	if marks := Load(filepath.Join(t.TempDir(), "nope")); len(marks) != 0 {
		t.Fatalf("got %v, want empty for a missing dir", marks)
	}
}

func TestSweepRemovesOnlyDeadPids(t *testing.T) {
	dir := t.TempDir()
	for _, pid := range []int{7, 8, 9} {
		if _, err := Toggle(dir, testEntry(pid), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	Sweep(dir, map[int]bool{8: true})

	marks := Load(dir)
	if len(marks) != 1 {
		t.Fatalf("got %d marks after sweep, want only the live pid's", len(marks))
	}
	if _, ok := marks[8]; !ok {
		t.Fatal("want pid 8's mark kept, 7 and 9 swept")
	}
}
