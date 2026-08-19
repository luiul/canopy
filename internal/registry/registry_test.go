package registry

import (
	"testing"

	"github.com/luiul/canopy-go/internal/ancestry"
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
