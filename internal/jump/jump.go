// Package jump brings whichever window is actually running a given agent
// process to the front: `code --reuse-window` for a VS Code integrated
// terminal, Ghostty's own AppleScript for a bare Ghostty tab, falling back
// to opening a brand-new window/instance at the same working directory when
// nothing open matches the row anymore. Nothing to jump to for Surface
// Unknown, canopy doesn't know what's hosting it.
package jump

import (
	"os/exec"
	"strings"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/applescript"
	"github.com/luiul/canopy/internal/registry"
)

// Result reports whether a jump succeeded and a human-readable message
// about it.
type Result struct {
	OK      bool
	Message string
}

// deps groups every external side effect jump.To makes, so tests can swap
// each one out (the same "monkeypatch the module boundary" pattern the
// Python original's tests use), without touching the real OS.
type deps struct {
	lookPathCode         func() (string, bool)
	runCommand           func(args []string) (exitOK bool, stderr string)
	ghosttyFocusByCwd    func(cwd string) (bool, error)
	ghosttyOpenNewWindow func(cwd string) error
}

func defaultDeps() deps {
	return deps{
		lookPathCode: func() (string, bool) {
			p, err := exec.LookPath("code")
			return p, err == nil
		},
		runCommand: func(args []string) (bool, string) {
			cmd := exec.Command(args[0], args[1:]...)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			err := cmd.Run()
			return err == nil, strings.TrimSpace(stderr.String())
		},
		ghosttyFocusByCwd:    applescript.GhosttyFocusByCwd,
		ghosttyOpenNewWindow: applescript.GhosttyOpenNewWindow,
	}
}

// To jumps to whichever window is running entry, using the real OS.
func To(entry registry.RegistryEntry) Result {
	return jumpWith(defaultDeps(), entry)
}

func jumpWith(d deps, entry registry.RegistryEntry) Result {
	switch entry.Surface {
	case ancestry.VSCode:
		return jumpVSCode(d, entry)
	case ancestry.Ghostty:
		return jumpGhostty(d, entry)
	default:
		return Result{false, "Don't know how to jump to this process's window yet."}
	}
}

func jumpVSCode(d deps, entry registry.RegistryEntry) Result {
	if entry.Cwd == "" {
		return Result{false, "No known working directory to open."}
	}

	if codeBin, ok := d.lookPathCode(); ok {
		if exitOK, _ := d.runCommand([]string{codeBin, "--reuse-window", entry.Cwd}); exitOK {
			return Result{true, "Focused VS Code window for " + entry.Cwd + "."}
		}
	}

	// Fall back to just raising the app if the `code` shell command isn't
	// installed; this can't target the right *window*, only the app.
	exitOK, stderr := d.runCommand([]string{"open", "-a", "Visual Studio Code", entry.Cwd})
	if exitOK {
		return Result{true, "Opened " + entry.Cwd + " in VS Code (install the 'code' CLI for exact-window focus)."}
	}
	if stderr == "" {
		stderr = "Couldn't open VS Code."
	}
	return Result{false, stderr}
}

func jumpGhostty(d deps, entry registry.RegistryEntry) Result {
	if entry.Cwd == "" {
		return Result{false, "No known working directory to focus."}
	}
	found, err := d.ghosttyFocusByCwd(entry.Cwd)
	if err != nil {
		return Result{false, err.Error()}
	}
	if found {
		return Result{true, "Focused in Ghostty."}
	}

	// Nothing open matches this row anymore (e.g. the tab closed since
	// canopy last resolved it). Rather than dead-ending, create a fresh
	// instance at the same cwd, mirroring jumpVSCode's reuse-or-create
	// behavior via `--reuse-window`.
	if err := d.ghosttyOpenNewWindow(entry.Cwd); err != nil {
		return Result{false, err.Error()}
	}
	return Result{true, "Opened a new Ghostty window for " + entry.Cwd + "."}
}
