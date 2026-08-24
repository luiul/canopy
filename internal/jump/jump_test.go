package jump

import (
	"testing"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/mycelium"
)

func entry(surface ancestry.Surface, mutate func(*registry.RegistryEntry)) registry.RegistryEntry {
	e := registry.RegistryEntry{Pid: 1, Kind: "pi", Tty: "s017", Cwd: "/Users/x/dotfiles", Surface: surface, State: "working"}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

// withFakes swaps openVSCode/openGhostty for the duration of fn, restoring
// the real mycelium functions afterward, so no test here ever shells out
// to osascript or the real `code` CLI. mycelium's own test suite already
// covers the window-detection logic these fakes stand in for; these tests
// only need to verify that To() dispatches to the right one, with the
// right argument, and maps its Result straight through.
func withFakes(t *testing.T, vscode func(path string) mycelium.Result, ghostty func(path string) mycelium.Result) {
	t.Helper()
	origVSCode, origGhostty := openVSCode, openGhostty
	if vscode != nil {
		openVSCode = vscode
	}
	if ghostty != nil {
		openGhostty = ghostty
	}
	t.Cleanup(func() {
		openVSCode = origVSCode
		openGhostty = origGhostty
	})
}

func TestJumpToVSCodeDelegatesToMyceliumWithTheEntrysCwd(t *testing.T) {
	var gotPath string
	withFakes(t, func(path string) mycelium.Result {
		gotPath = path
		return mycelium.Result{OK: true, Message: "Focused VS Code window for " + path + "."}
	}, nil)

	result := To(entry(ancestry.VSCode, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if gotPath != "/Users/x/dotfiles" {
		t.Fatalf("got path %q, want /Users/x/dotfiles", gotPath)
	}
	if result.Message != "Focused VS Code window for /Users/x/dotfiles." {
		t.Fatalf("got message %q", result.Message)
	}
}

func TestJumpToVSCodeSurfacesAFailureFromMycelium(t *testing.T) {
	withFakes(t, func(path string) mycelium.Result {
		return mycelium.Result{OK: false, Message: "couldn't open VS Code"}
	}, nil)

	result := To(entry(ancestry.VSCode, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if result.Message != "couldn't open VS Code" {
		t.Fatalf("got message %q", result.Message)
	}
}

func TestJumpToVSCodeWithoutACwdFailsClearlyWithoutCallingMycelium(t *testing.T) {
	called := false
	withFakes(t, func(path string) mycelium.Result {
		called = true
		return mycelium.Result{OK: true}
	}, nil)

	result := To(entry(ancestry.VSCode, func(e *registry.RegistryEntry) { e.Cwd = "" }))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if !contains(result.Message, "working directory") {
		t.Fatalf("got message %q, want it to mention working directory", result.Message)
	}
	if called {
		t.Fatalf("want mycelium never called without a known cwd")
	}
}

func TestJumpToGhosttyDelegatesToMyceliumWithTheEntrysCwd(t *testing.T) {
	var gotPath string
	withFakes(t, nil, func(path string) mycelium.Result {
		gotPath = path
		return mycelium.Result{OK: true, Message: "Focused in Ghostty."}
	})

	result := To(entry(ancestry.Ghostty, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if gotPath != "/Users/x/dotfiles" {
		t.Fatalf("got path %q, want /Users/x/dotfiles", gotPath)
	}
}

func TestJumpToGhosttyWithoutACwdFailsClearlyWithoutCallingMycelium(t *testing.T) {
	called := false
	withFakes(t, nil, func(path string) mycelium.Result {
		called = true
		return mycelium.Result{OK: true}
	})

	result := To(entry(ancestry.Ghostty, func(e *registry.RegistryEntry) { e.Cwd = "" }))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if called {
		t.Fatalf("want mycelium never called without a known cwd")
	}
}

func TestJumpToUnknownSurfaceSaysSoWithoutCallingMycelium(t *testing.T) {
	vscodeCalled, ghosttyCalled := false, false
	withFakes(t, func(path string) mycelium.Result {
		vscodeCalled = true
		return mycelium.Result{OK: true}
	}, func(path string) mycelium.Result {
		ghosttyCalled = true
		return mycelium.Result{OK: true}
	})

	result := To(entry(ancestry.Unknown, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if vscodeCalled || ghosttyCalled {
		t.Fatalf("want neither mycelium function called for an unknown surface")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
