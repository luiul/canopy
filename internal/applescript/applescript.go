// Package applescript is a thin `osascript` wrapper for the things canopy
// needs from other apps that nothing else can give it: focusing the
// specific window/tab running a given agent, raising it above whatever
// else is on screen, and (for VS Code) telling which folder a window is
// already open on in the first place.
//
// Ghostty's own scripting dictionary
// (https://github.com/ghostty-org/ghostty/blob/main/macos/Ghostty.sdef)
// declares a `tty` property per terminal surface, which would be the exact
// match canopy wants (it already resolves every agent's tty via `ps`). In
// practice, on the shipped 1.3.1 build, `tty` (and `pid`) reliably fails:
// "Can't make tty of terminal id ... into type specifier", while string
// properties like `working directory` and `name` work fine. So this matches
// by working directory instead. That's a weaker key: two tabs `cd`-ed to the
// same directory are indistinguishable, canopy just focuses whichever one
// Ghostty's script bridge returns first. If tty ever starts working
// reliably, this should switch back to matching by tty, which canopy
// already resolves for every agent anyway.
package applescript

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const timeout = 6 * time.Second

// AutomationError means the scripted app didn't do what was asked (focus a
// terminal, raise a window, ...).
type AutomationError struct {
	Msg string
}

func (e *AutomationError) Error() string { return e.Msg }

// AutomationPermissionError means the macOS Automation permission for
// scripting the target app (Ghostty, System Events, ...) hasn't been
// granted yet. The first attempt normally pops a system permission dialog;
// if nothing is there to click it (e.g. this is being run
// non-interactively), osascript times out or errors instead of prompting,
// which is surfaced as this instead of a generic error so canopy's jump
// package can print something actionable.
type AutomationPermissionError struct {
	AutomationError
}

func newPermissionError(msg string) error {
	return &AutomationPermissionError{AutomationError{Msg: msg}}
}

func runOsascript(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return "", newPermissionError(
			"The scripted app didn't respond to AppleScript in time. macOS may be waiting on an " +
				"Automation permission prompt you haven't seen/answered yet: System Settings -> " +
				"Privacy & Security -> Automation, allow your terminal to control it.",
		)
	}

	if err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		lowered := strings.ToLower(stderrStr)
		if strings.Contains(stderrStr, "-1743") || strings.Contains(lowered, "not allowed") || strings.Contains(lowered, "not authorized") {
			return "", newPermissionError(
				"macOS hasn't granted Automation permission for scripting yet. Go to System " +
					"Settings -> Privacy & Security -> Automation and allow your terminal to control " +
					"it, then try again.",
			)
		}
		if strings.Contains(stderrStr, "-1712") || strings.Contains(lowered, "timed out") {
			return "", newPermissionError(
				"The scripted app didn't respond to AppleScript in time (same permission prompt as " +
					"above, or it isn't running).",
			)
		}
		msg := stderrStr
		if msg == "" {
			msg = "osascript failed: " + err.Error()
		}
		return "", &AutomationError{Msg: msg}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// GhosttyFocusByCwd focuses a Ghostty terminal surface whose working
// directory is cwd, bringing its window to the front. Returns false (not an
// error) if no open terminal currently matches, e.g. it already closed, or
// the process has since `cd`-ed elsewhere since canopy last resolved it.
func GhosttyFocusByCwd(cwd string) (bool, error) {
	escaped := escapeForAppleScript(cwd)
	script := `
    tell application "Ghostty"
        repeat with t in terminals
            if (working directory of t) is "` + escaped + `" then
                focus t
                return "true"
            end if
        end repeat
    end tell
    return "false"
    `
	out, err := runOsascript(script)
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// GhosttyOpenNewWindow opens a brand-new Ghostty window with its initial
// working directory set to cwd. This is the create-a-new-instance half of
// canopy's switch-or-create behavior: when GhosttyFocusByCwd finds nothing
// to focus (e.g. the agent's tab has since been closed), this opens a fresh
// one at the same location instead of just reporting failure.
func GhosttyOpenNewWindow(cwd string) error {
	escaped := escapeForAppleScript(cwd)
	script := `
    tell application "Ghostty"
        new window with configuration {initial working directory:"` + escaped + `"}
    end tell
    `
	_, err := runOsascript(script)
	return err
}

func escapeForAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// VSCodeWindowTitles lists every currently open VS Code window's title, via
// System Events. Returns an empty slice, no error, if VS Code isn't running
// at all: that's the ordinary "nothing to switch to" case, not a failure.
//
// This exists because `code --reuse-window <path>` alone isn't enough to
// get real switch-or-create behavior out of the `code` CLI: it only reuses
// the right window when one already has that exact folder open, and
// silently hijacks whichever window was last active otherwise, rather than
// opening a fresh one. Confirmed both empirically and in upstream reports
// (microsoft/vscode#121926, #216602, #215749). Checking which window (if
// any) already has a given folder open ourselves, via its title, and only
// ever falling back to the CLI once that's ruled out, avoids ever handing
// `code` a chance to guess wrong about which window to reuse.
func VSCodeWindowTitles() ([]string, error) {
	out, err := runOsascript(`
if application "Visual Studio Code" is running then
	tell application "System Events"
		tell process "Code"
			get name of every window
		end tell
	end tell
else
	return ""
end if
`)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	// osascript joins an AppleScript list with ", " when coerced to text.
	titles := strings.Split(out, ", ")
	for i := range titles {
		titles[i] = strings.TrimSpace(titles[i])
	}
	return titles, nil
}

// VSCodeRaiseWindow brings the VS Code window with this exact title to the
// front, activating the app itself first (a window can't be raised above
// other apps' windows until its own app is). Returns false, no error, if no
// window with that title exists anymore (e.g. it was closed between
// VSCodeWindowTitles finding it and this call).
func VSCodeRaiseWindow(title string) (bool, error) {
	script := `
tell application "Visual Studio Code" to activate
tell application "System Events"
	tell process "Code"
		set matches to (every window whose name is "` + escapeForAppleScript(title) + `")
		if (count of matches) is 0 then
			return "false"
		end if
		perform action "AXRaise" of (item 1 of matches)
		return "true"
	end tell
end tell
`
	out, err := runOsascript(script)
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// VSCodeMatchWindowTitle finds the first title that's already showing
// path, going by this ecosystem's `window.title` convention (folder
// basename first, then a separator and the branch, e.g.
// "understory — main", or the plain basename on its own with nothing open
// in it yet). A title matches when it equals the basename exactly, or
// starts with the basename followed by a space: that's a real word
// boundary (so "understory-lab — main" does NOT match a search for
// "understory", since the character right after the shared prefix is
// "-", not a space), tolerant of whatever separator glyph sits between
// folder name and branch (em dash, plain hyphen, ...) without hardcoding
// one. Weak key, same class of limitation as GhosttyFocusByCwd's own
// cwd match: two different paths that happen to share a leaf folder name
// are indistinguishable by title alone.
func VSCodeMatchWindowTitle(titles []string, path string) (string, bool) {
	base := filepath.Base(path)
	if base == "" {
		return "", false
	}
	for _, title := range titles {
		if title == base {
			return title, true
		}
		if strings.HasPrefix(title, base+" ") {
			return title, true
		}
	}
	return "", false
}
