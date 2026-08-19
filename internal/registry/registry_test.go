package registry

import (
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ancestry"
)

func entry(pid int, kind string, surface ancestry.Surface, state string) RegistryEntry {
	return RegistryEntry{Pid: pid, Kind: kind, Tty: "s000", Cwd: "/x", Surface: surface, State: state}
}

func TestMergeRegistryPrefersTheFreshCopyOfAStillPresentEntry(t *testing.T) {
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].Misses = 1
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}

	merged := MergeRegistry(previous, fresh)

	if len(merged) != 1 {
		t.Fatalf("got %+v, want 1 entry", merged)
	}
	if merged[0].State != "working" {
		t.Fatalf("got state %q, want working", merged[0].State)
	}
	if merged[0].Misses != 0 {
		t.Fatalf("got misses %d, want 0", merged[0].Misses)
	}
}

func TestMergeRegistryKeepsAMissingEntryWithinTheDebounceWindow(t *testing.T) {
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}

	merged := MergeRegistry(previous, nil)

	if len(merged) != 1 {
		t.Fatalf("got %+v, want 1 entry", merged)
	}
	if merged[0].Misses != 1 {
		t.Fatalf("got misses %d, want 1", merged[0].Misses)
	}
}

func TestMergeRegistryDropsAnEntryOncePastTheDebounceWindow(t *testing.T) {
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].Misses = 1 // already missed once, at MissLimit

	merged := MergeRegistry(previous, nil)

	if len(merged) != 0 {
		t.Fatalf("got %+v, want empty", merged)
	}
}

func TestMergeRegistryAddsANewlySeenEntry(t *testing.T) {
	merged := MergeRegistry(nil, []RegistryEntry{entry(2, "pi", ancestry.Ghostty, "idle")})
	if len(merged) != 1 || merged[0].Pid != 2 {
		t.Fatalf("got %+v, want pid 2", merged)
	}
}

func TestStampStateSinceCarriesOverAnUnchangedState(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Minute)
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].StateSince = earlier
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}

	got := stampStateSince(previous, fresh, now)

	if !got[0].StateSince.Equal(earlier) {
		t.Fatalf("got StateSince %v, want carried-over %v", got[0].StateSince, earlier)
	}
}

func TestStampStateSinceResetsOnAStateChange(t *testing.T) {
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previous[0].StateSince = now.Add(-time.Hour)
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}

	got := stampStateSince(previous, fresh, now)

	if !got[0].StateSince.Equal(now) {
		t.Fatalf("got StateSince %v, want %v (now, since the state changed)", got[0].StateSince, now)
	}
}

func TestStampStateSinceStampsABrandNewEntry(t *testing.T) {
	now := time.Now()
	fresh := []RegistryEntry{entry(2, "pi", ancestry.Ghostty, "idle")}

	got := stampStateSince(nil, fresh, now)

	if !got[0].StateSince.Equal(now) {
		t.Fatalf("got StateSince %v, want %v", got[0].StateSince, now)
	}
}

func TestRegistryEntryKeyDisambiguatesSamePidDifferentKind(t *testing.T) {
	// pids get reused by the OS; scoping the key by kind too avoids two
	// genuinely different processes colliding if a pid is recycled between
	// polls faster than the debounce window notices.
	a := entry(1, "pi", ancestry.Ghostty, "idle")
	b := entry(1, "claude", ancestry.Ghostty, "idle")
	if a.Key() == b.Key() {
		t.Fatalf("keys should differ: %q vs %q", a.Key(), b.Key())
	}
}

func TestRefineExternalStatesKeepsTheSingleSampleGuessForABrandNewEntry(t *testing.T) {
	// No previous poll to diff against yet: nothing to refine.
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}

	got := refineExternalStates(nil, fresh, time.Now())

	if got[0].State != "working" {
		t.Fatalf("got state %q, want the untouched bootstrap guess working", got[0].State)
	}
}

func TestRefineExternalStatesCorrectsAStaleWorkingGuessBackToIdle(t *testing.T) {
	// This is the reported bug: macOS's own `ps` %cpu is a decaying average
	// over up to a minute of real time, so externalEntries' single-sample
	// guess can still say "working" well after a process has actually gone
	// idle. Two samples five seconds apart with an unchanged CPUTime (no
	// CPU actually consumed in that window) must correct that guess.
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	previous[0].CPUTime = 30 * time.Second
	previous[0].CPUSampledAt = now.Add(-5 * time.Second)

	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")} // externalEntries' stale guess
	fresh[0].CPUTime = 30 * time.Second                                   // unchanged: no CPU consumed since the last poll

	got := refineExternalStates(previous, fresh, now)

	if got[0].State != "idle" {
		t.Fatalf("got state %q, want idle (no CPU consumed since the last poll)", got[0].State)
	}
	if !got[0].CPUSampledAt.Equal(now) {
		t.Fatalf("got CPUSampledAt %v, want %v", got[0].CPUSampledAt, now)
	}
}

func TestRefineExternalStatesConfirmsWorkingOnlyAfterTwoConsecutiveQualifyingPolls(t *testing.T) {
	// A single qualifying poll (WorkingStreak going 0 -> 1) is exactly what
	// a brief CPU blip on an otherwise idle process looks like (a heartbeat
	// tick, a GC pause, a terminal redraw) -- not sustained agent work. It
	// must not flip the display to working by itself.
	now := time.Now()
	previousIdle := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	previousIdle[0].CPUTime = 10 * time.Second
	previousIdle[0].CPUSampledAt = now.Add(-5 * time.Second)

	firstPoll := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	firstPoll[0].CPUTime = 14 * time.Second // 4s of CPU time over a 5s window: ~80% > DefaultThreshold

	afterFirstPoll := refineExternalStates(previousIdle, firstPoll, now)

	if afterFirstPoll[0].State != "idle" {
		t.Fatalf("got state %q after one qualifying poll, want idle (needs %d consecutive)", afterFirstPoll[0].State, workingConfirmPolls)
	}
	if afterFirstPoll[0].WorkingStreak != 1 {
		t.Fatalf("got WorkingStreak %d, want 1", afterFirstPoll[0].WorkingStreak)
	}

	// A second consecutive qualifying poll must confirm it.
	now2 := now.Add(5 * time.Second)
	secondPoll := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "idle")}
	secondPoll[0].CPUTime = 18 * time.Second // another 4s of CPU time over another 5s window

	afterSecondPoll := refineExternalStates(afterFirstPoll, secondPoll, now2)

	if afterSecondPoll[0].State != "working" {
		t.Fatalf("got state %q after two consecutive qualifying polls, want working", afterSecondPoll[0].State)
	}
	if afterSecondPoll[0].WorkingStreak != 2 {
		t.Fatalf("got WorkingStreak %d, want 2", afterSecondPoll[0].WorkingStreak)
	}
}

func TestRefineExternalStatesDropsBackToIdleImmediatelyAndResetsTheStreak(t *testing.T) {
	// Climbing into working is debounced; dropping back to idle is not.
	confirmedWorking := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	confirmedWorking[0].CPUTime = 20 * time.Second
	confirmedWorking[0].CPUSampledAt = time.Now().Add(-5 * time.Second)
	confirmedWorking[0].WorkingStreak = 2

	now := confirmedWorking[0].CPUSampledAt.Add(5 * time.Second)
	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	fresh[0].CPUTime = 20 * time.Second // unchanged: no CPU consumed since the last poll

	got := refineExternalStates(confirmedWorking, fresh, now)

	if got[0].State != "idle" {
		t.Fatalf("got state %q, want idle immediately, no debounce", got[0].State)
	}
	if got[0].WorkingStreak != 0 {
		t.Fatalf("got WorkingStreak %d, want reset to 0", got[0].WorkingStreak)
	}
}

func TestRefineExternalStatesIgnoresANegativeDelta(t *testing.T) {
	// A pid the OS recycled between polls (or any other counter hiccup)
	// would otherwise produce a nonsensical negative rate; keep the
	// existing guess instead of acting on it.
	now := time.Now()
	previous := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	previous[0].CPUTime = 30 * time.Second
	previous[0].CPUSampledAt = now.Add(-5 * time.Second)

	fresh := []RegistryEntry{entry(1, "pi", ancestry.Ghostty, "working")}
	fresh[0].CPUTime = 2 * time.Second // less than previous: negative delta

	got := refineExternalStates(previous, fresh, now)

	if got[0].State != "working" {
		t.Fatalf("got state %q, want the untouched guess working when the delta is negative", got[0].State)
	}
}
