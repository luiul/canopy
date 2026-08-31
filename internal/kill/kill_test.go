package kill

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/canopy/internal/scan"
)

// withSeams swaps both package-level seams for fakes and restores them on
// test cleanup, returning a recorder for the signals "sent".
func withSeams(t *testing.T, table map[int]scan.ProcessInfo) (sent *[]syscall.Signal) {
	t.Helper()
	var got []syscall.Signal
	origSignal, origTable := signalProcess, processTable
	signalProcess = func(pid int, sig syscall.Signal) error {
		got = append(got, sig)
		return nil
	}
	processTable = func() map[int]scan.ProcessInfo { return table }
	t.Cleanup(func() { signalProcess, processTable = origSignal, origTable })
	return &got
}

func entry(pid int, uptime time.Duration) registry.RegistryEntry {
	return registry.RegistryEntry{Pid: pid, Kind: "pi", Uptime: uptime}
}

func TestProcessSignalsAProcessWhoseIdentityChecksOut(t *testing.T) {
	sent := withSeams(t, map[int]scan.ProcessInfo{
		123: {Pid: 123, Etime: time.Hour + 2*time.Second}, // 2s past the sample, well within slack
	})

	r := Process(entry(123, time.Hour), syscall.SIGTERM)

	if !r.OK {
		t.Fatalf("got OK=false (%q), want success", r.Message)
	}
	if len(*sent) != 1 || (*sent)[0] != syscall.SIGTERM {
		t.Fatalf("got signals %v, want exactly one SIGTERM", *sent)
	}
	if !strings.Contains(r.Message, "terminated pi (pid 123)") {
		t.Fatalf("got message %q", r.Message)
	}
}

func TestProcessRefusesAMissingPidWithoutSignaling(t *testing.T) {
	sent := withSeams(t, map[int]scan.ProcessInfo{})

	r := Process(entry(123, time.Hour), syscall.SIGKILL)

	if r.OK || !strings.Contains(r.Message, "already exited") {
		t.Fatalf("got %+v, want an already-exited refusal", r)
	}
	if len(*sent) != 0 {
		t.Fatalf("got signals %v, want none sent", *sent)
	}
}

func TestProcessRefusesARecycledPidWithoutSignaling(t *testing.T) {
	// The pid exists again, but its lifetime (5s) is far below the sample
	// the row was built from (1h): a different, younger process now owns it.
	sent := withSeams(t, map[int]scan.ProcessInfo{
		123: {Pid: 123, Etime: 5 * time.Second},
	})

	r := Process(entry(123, time.Hour), syscall.SIGKILL)

	if r.OK || !strings.Contains(r.Message, "no longer belongs") {
		t.Fatalf("got %+v, want a recycled-pid refusal", r)
	}
	if len(*sent) != 0 {
		t.Fatalf("got signals %v, want none sent", *sent)
	}
}

func TestProcessRefusesAnEtimeThatAdvancedImplausiblyFar(t *testing.T) {
	// Same direction as a legit sample (older than Uptime), but further
	// than any poll interval plus jitter could explain.
	sent := withSeams(t, map[int]scan.ProcessInfo{
		123: {Pid: 123, Etime: time.Hour + identitySlack + time.Minute},
	})

	r := Process(entry(123, time.Hour), syscall.SIGKILL)

	if r.OK || !strings.Contains(r.Message, "no longer belongs") {
		t.Fatalf("got %+v, want a refusal", r)
	}
	if len(*sent) != 0 {
		t.Fatalf("got signals %v, want none sent", *sent)
	}
}

func TestProcessWithNoUptimeSampleDegradesToExistenceOnly(t *testing.T) {
	// Uptime 0 means the poll that built the row had no process-table
	// sample for it (see RegistryEntry.Uptime): nothing to compare against,
	// so an existing pid is signaled rather than refused.
	sent := withSeams(t, map[int]scan.ProcessInfo{
		123: {Pid: 123, Etime: 500 * time.Hour},
	})

	r := Process(entry(123, 0), syscall.SIGKILL)

	if !r.OK {
		t.Fatalf("got OK=false (%q), want success", r.Message)
	}
	if len(*sent) != 1 {
		t.Fatalf("got signals %v, want exactly one", *sent)
	}
}

func TestProcessSurfacesASignalError(t *testing.T) {
	origSignal, origTable := signalProcess, processTable
	signalProcess = func(pid int, sig syscall.Signal) error { return errors.New("operation not permitted") }
	processTable = func() map[int]scan.ProcessInfo {
		return map[int]scan.ProcessInfo{123: {Pid: 123, Etime: time.Hour}}
	}
	t.Cleanup(func() { signalProcess, processTable = origSignal, origTable })

	r := Process(entry(123, time.Hour), syscall.SIGKILL)

	if r.OK || !strings.Contains(r.Message, "operation not permitted") {
		t.Fatalf("got %+v, want the EPERM-style error surfaced", r)
	}
	if !strings.Contains(r.Message, "SIGKILL") {
		t.Fatalf("got %q, want the signal named in the message", r.Message)
	}
}

func TestProcessVerbsFollowTheSignal(t *testing.T) {
	sent := withSeams(t, map[int]scan.ProcessInfo{
		123: {Pid: 123, Etime: time.Hour},
	})

	wants := map[syscall.Signal]string{
		syscall.SIGSTOP: "paused",
		syscall.SIGCONT: "resumed",
		syscall.SIGTERM: "terminated",
		syscall.SIGKILL: "killed",
	}
	for sig, want := range wants {
		r := Process(entry(123, time.Hour), sig)
		if !r.OK || !strings.HasPrefix(r.Message, want+" ") {
			t.Errorf("signal %v: got %q, want it to start with %q", sig, r.Message, want)
		}
	}
	if len(*sent) != 4 {
		t.Fatalf("got %d signals, want 4", len(*sent))
	}
}

func TestNameAndVerbCoverTheWholeSignalVocabulary(t *testing.T) {
	wants2 := map[syscall.Signal][3]string{
		syscall.SIGTERM: {"SIGTERM", "terminated", "Terminate"},
		syscall.SIGKILL: {"SIGKILL", "killed", "Kill"},
		syscall.SIGSTOP: {"SIGSTOP", "paused", "Pause"},
		syscall.SIGCONT: {"SIGCONT", "resumed", "Resume"},
	}
	for sig, want := range wants2 {
		if got := Name(sig); got != want[0] {
			t.Errorf("Name(%v) = %q, want %q", sig, got, want[0])
		}
		if got := Verb(sig); got != want[1] {
			t.Errorf("Verb(%v) = %q, want %q", sig, got, want[1])
		}
		if got := PromptVerb(sig); got != want[2] {
			t.Errorf("PromptVerb(%v) = %q, want %q", sig, got, want[2])
		}
	}
	if got := Name(syscall.SIGHUP); got != "signal 1" {
		t.Errorf("Name(SIGHUP) = %q, want a numeric fallback for signals outside the vocabulary", got)
	}
	if got := PromptVerb(syscall.SIGHUP); got != "signal 1" {
		t.Errorf("PromptVerb(SIGHUP) = %q, want the Name fallback for signals outside the vocabulary", got)
	}
}
