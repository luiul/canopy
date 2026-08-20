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

// fakeDeps returns deps with every field faked to safe no-op defaults (no
// window already open, code CLI present, every command "succeeds", every
// Ghostty call is a no-op), so each test only needs to override the one
// or two fields it cares about instead of restating the whole struct, and
// so unit tests never shell out to osascript or the real `code` CLI.
func fakeDeps() deps {
	return deps{
		lookPathCode:         func() (string, bool) { return "/usr/local/bin/code", true },
		runCommand:           func(args []string) (bool, string) { return true, "" },
		windowTitles:         func() ([]string, error) { return nil, nil },
		matchWindowTitle:     func(titles []string, path string) (string, bool) { return "", false },
		raiseWindow:          func(title string) (bool, error) { return false, nil },
		ghosttyFocusByCwd:    func(cwd string) (bool, error) { return false, nil },
		ghosttyOpenNewWindow: func(cwd string) error { return nil },
	}
}

func TestJumpToVSCodeRaisesTheExistingWindowInsteadOfShellingOutToCode(t *testing.T) {
	// The switch-to-already-open half: when a window already has this
	// path's folder open, jumpVSCode should raise it directly and never
	// touch the `code` CLI at all (that's the whole point of checking
	// first, instead of trusting `code --reuse-window` to guess right).
	d := fakeDeps()
	d.windowTitles = func() ([]string, error) { return []string{"dotfiles — main"}, nil }
	d.matchWindowTitle = func(titles []string, path string) (string, bool) { return "dotfiles — main", true }
	var raisedTitle string
	d.raiseWindow = func(title string) (bool, error) { raisedTitle = title; return true, nil }
	codeCalled := false
	d.runCommand = func(args []string) (bool, string) { codeCalled = true; return true, "" }

	result := jumpWith(d, entry(ancestry.VSCode, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if raisedTitle != "dotfiles — main" {
		t.Fatalf("got raised title %q, want %q", raisedTitle, "dotfiles — main")
	}
	if codeCalled {
		t.Fatalf("want the code CLI never invoked once an existing window was raised")
	}
}

func TestJumpToVSCodeFallsThroughToTheCodeCLIWhenNoWindowMatches(t *testing.T) {
	var gotArgs []string
	d := fakeDeps()
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

func TestJumpToVSCodeFallsThroughToTheCodeCLIWhenTheMatchedWindowIsGone(t *testing.T) {
	// windowTitles can be stale: the matched window may have closed
	// between that check and the raise attempt. raiseWindow reporting
	// "not found" (false, nil) should fall through to the CLI, not
	// report failure.
	d := fakeDeps()
	d.windowTitles = func() ([]string, error) { return []string{"dotfiles — main"}, nil }
	d.matchWindowTitle = func(titles []string, path string) (string, bool) { return "dotfiles — main", true }
	d.raiseWindow = func(title string) (bool, error) { return false, nil }
	var gotArgs []string
	d.runCommand = func(args []string) (bool, string) { gotArgs = args; return true, "" }

	result := jumpWith(d, entry(ancestry.VSCode, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	found := false
	for _, a := range gotArgs {
		if a == "--reuse-window" {
			found = true
		}
	}
	if !found {
		t.Fatalf("got args %v, want --reuse-window", gotArgs)
	}
}

func TestJumpToVSCodeFallsBackToReuseWindowWhenTheAlreadyOpenCheckItselfErrors(t *testing.T) {
	// windowTitles erroring (e.g. the Automation permission for
	// scripting VS Code hasn't been granted yet) means jumpVSCode
	// genuinely doesn't know whether a window is already open; falling
	// through to --reuse-window keeps this at least as functional as
	// before this check existed.
	d := fakeDeps()
	d.windowTitles = func() ([]string, error) { return nil, errors.New("not authorized") }
	var gotArgs []string
	d.runCommand = func(args []string) (bool, string) { gotArgs = args; return true, "" }

	jumpWith(d, entry(ancestry.VSCode, nil))

	found := false
	for _, a := range gotArgs {
		if a == "--reuse-window" {
			found = true
		}
	}
	if !found {
		t.Fatalf("got args %v, want --reuse-window", gotArgs)
	}
}

func TestJumpToVSCodeFallsBackToOpenWhenCodeCLIMissing(t *testing.T) {
	var gotArgs []string
	d := fakeDeps()
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
	result := jumpWith(fakeDeps(), entry(ancestry.VSCode, func(e *registry.RegistryEntry) { e.Cwd = "" }))
	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if !contains(result.Message, "working directory") {
		t.Fatalf("got message %q, want it to mention working directory", result.Message)
	}
}

func TestJumpToGhosttyFocusesByCwd(t *testing.T) {
	var gotCwd string
	d := fakeDeps()
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
	result := jumpWith(fakeDeps(), entry(ancestry.Ghostty, func(e *registry.RegistryEntry) { e.Cwd = "" }))
	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
}

func TestJumpToGhosttyOpensNewWindowWhenNoTerminalMatches(t *testing.T) {
	d := fakeDeps()
	d.ghosttyFocusByCwd = func(string) (bool, error) { return false, nil }
	var gotCwd string
	d.ghosttyOpenNewWindow = func(cwd string) error { gotCwd = cwd; return nil }

	result := jumpWith(d, entry(ancestry.Ghostty, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if gotCwd != "/Users/x/dotfiles" {
		t.Fatalf("got cwd %q", gotCwd)
	}
}

func TestJumpToGhosttyFailsClearlyWhenNewWindowFails(t *testing.T) {
	d := fakeDeps()
	d.ghosttyFocusByCwd = func(string) (bool, error) { return false, nil }
	d.ghosttyOpenNewWindow = func(string) error { return errors.New("couldn't open a new window") }

	result := jumpWith(d, entry(ancestry.Ghostty, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if !contains(result.Message, "couldn't open a new window") {
		t.Fatalf("got message %q", result.Message)
	}
}

func TestJumpToGhosttySurfacesAutomationPermissionErrors(t *testing.T) {
	d := fakeDeps()
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
	result := jumpWith(fakeDeps(), entry(ancestry.Unknown, nil))
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
