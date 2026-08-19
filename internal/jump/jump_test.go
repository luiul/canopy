package jump

import (
	"errors"
	"testing"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
)

func entry(surface ancestry.Surface, mutate func(*registry.RegistryEntry)) registry.RegistryEntry {
	e := registry.RegistryEntry{Pid: 1, Kind: "pi", Tty: "s017", Cwd: "/Users/x/dotfiles", Surface: surface, State: "working"}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

func herdrEntry() registry.RegistryEntry {
	return entry(ancestry.Herdr, func(e *registry.RegistryEntry) {
		e.WorkspaceID, e.TabID, e.PaneID = "wG", "wG:t1", "wG:p1"
	})
}

func TestJumpToHerdrFocusesWorkspaceTabAndPane(t *testing.T) {
	type call struct{ kind, id string }
	var calls []call
	d := defaultDeps()
	d.focusWorkspace = func(id string) bool { calls = append(calls, call{"workspace", id}); return true }
	d.focusTab = func(id string) bool { calls = append(calls, call{"tab", id}); return true }
	d.focusPane = func(id string) bool { calls = append(calls, call{"pane", id}); return true }
	d.activateGhostty = func() bool { return true }

	result := jumpWith(d, herdrEntry())

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	want := []call{{"workspace", "wG"}, {"tab", "wG:t1"}, {"pane", "wG:p1"}}
	if len(calls) != len(want) {
		t.Fatalf("got %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("got %+v, want %+v", calls, want)
		}
	}
}

func TestJumpToHerdrReportsFailureWhenHerdrRejectsEveryFocusCall(t *testing.T) {
	d := defaultDeps()
	d.focusWorkspace = func(string) bool { return false }
	d.focusTab = func(string) bool { return false }
	d.focusPane = func(string) bool { return false }

	result := jumpWith(d, herdrEntry())

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
}

func TestJumpToHerdrRaisesGhosttyAsAFallback(t *testing.T) {
	d := defaultDeps()
	d.focusWorkspace = func(string) bool { return true }
	d.focusTab = func(string) bool { return true }
	d.focusPane = func(string) bool { return true }
	activated := false
	d.activateGhostty = func() bool { activated = true; return true }

	jumpWith(d, herdrEntry())

	if !activated {
		t.Fatal("want activateGhostty to be called")
	}
}

func TestJumpToVSCodeUsesTheCodeCLIWhenAvailable(t *testing.T) {
	var gotArgs []string
	d := defaultDeps()
	d.lookPathCode = func() (string, bool) { return "/usr/local/bin/code", true }
	d.runCommand = func(args []string) (bool, string) { gotArgs = args; return true, "" }

	result := jumpWith(d, entry(ancestry.VSCode, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	want := []string{"/usr/local/bin/code", "--reuse-window", "/Users/x/dotfiles"}
	if len(gotArgs) != len(want) {
		t.Fatalf("got %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("got %v, want %v", gotArgs, want)
		}
	}
}

func TestJumpToVSCodeFallsBackToOpenWhenCodeCLIMissing(t *testing.T) {
	var gotArgs []string
	d := defaultDeps()
	d.lookPathCode = func() (string, bool) { return "", false }
	d.runCommand = func(args []string) (bool, string) { gotArgs = args; return true, "" }

	result := jumpWith(d, entry(ancestry.VSCode, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	want := []string{"open", "-a", "Visual Studio Code", "/Users/x/dotfiles"}
	if len(gotArgs) != len(want) {
		t.Fatalf("got %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("got %v, want %v", gotArgs, want)
		}
	}
}

func TestJumpToVSCodeWithoutACwdFailsClearly(t *testing.T) {
	result := To(entry(ancestry.VSCode, func(e *registry.RegistryEntry) { e.Cwd = "" }))
	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if !contains(result.Message, "working directory") {
		t.Fatalf("got message %q, want it to mention working directory", result.Message)
	}
}

func TestJumpToGhosttyFocusesByCwd(t *testing.T) {
	var gotCwd string
	d := defaultDeps()
	d.ghosttyFocusByCwd = func(cwd string) (bool, error) { gotCwd = cwd; return true, nil }

	result := jumpWith(d, entry(ancestry.Ghostty, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if gotCwd != "/Users/x/dotfiles" {
		t.Fatalf("got cwd %q", gotCwd)
	}
}

func TestJumpToGhosttyWithoutACwdFailsClearly(t *testing.T) {
	result := To(entry(ancestry.Ghostty, func(e *registry.RegistryEntry) { e.Cwd = "" }))
	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
}

func TestJumpToGhosttyReportsWhenNoTerminalMatches(t *testing.T) {
	d := defaultDeps()
	d.ghosttyFocusByCwd = func(string) (bool, error) { return false, nil }

	result := jumpWith(d, entry(ancestry.Ghostty, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
}

func TestJumpToGhosttySurfacesAutomationPermissionErrors(t *testing.T) {
	d := defaultDeps()
	d.ghosttyFocusByCwd = func(string) (bool, error) { return false, errors.New("grant Automation permission") }

	result := jumpWith(d, entry(ancestry.Ghostty, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if !contains(result.Message, "Automation permission") {
		t.Fatalf("got message %q", result.Message)
	}
}

func TestJumpToUnknownSurfaceSaysSo(t *testing.T) {
	result := To(entry(ancestry.Unknown, nil))
	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
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
