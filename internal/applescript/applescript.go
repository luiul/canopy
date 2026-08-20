// Package applescript is a thin `osascript` wrapper for the one thing
// canopy needs from Ghostty that nothing else can give it: focusing the
// specific window/tab running a given agent, raising it above whatever else
// is on screen.
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
	"strings"
	"time"
)

const timeout = 6 * time.Second

// AutomationError means Ghostty didn't focus the requested terminal.
type AutomationError struct {
	Msg string
}

func (e *AutomationError) Error() string { return e.Msg }

// AutomationPermissionError means the macOS Automation permission for
// scripting Ghostty hasn't been granted yet. The first attempt normally
// pops a system permission dialog; if nothing is there to click it (e.g.
// this is being run non-interactively), osascript times out or errors
// instead of prompting, which is surfaced as this instead of a generic
// error so canopy's jump package can print something actionable.
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
			"Ghostty didn't respond to AppleScript in time. If this is the first jump, macOS " +
				"may be waiting on an Automation permission prompt you haven't seen/answered yet: " +
				"System Settings -> Privacy & Security -> Automation, allow your terminal to control Ghostty.",
		)
	}

	if err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		lowered := strings.ToLower(stderrStr)
		if strings.Contains(stderrStr, "-1743") || strings.Contains(lowered, "not allowed") || strings.Contains(lowered, "not authorized") {
			return "", newPermissionError(
				"macOS hasn't granted Automation permission for Ghostty yet. Go to System " +
					"Settings -> Privacy & Security -> Automation and allow your terminal to control " +
					"Ghostty, then try again.",
			)
		}
		if strings.Contains(stderrStr, "-1712") || strings.Contains(lowered, "timed out") {
			return "", newPermissionError(
				"Ghostty didn't respond to AppleScript in time (same permission prompt as above, " +
					"or Ghostty isn't running).",
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
	escaped := strings.ReplaceAll(cwd, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
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

// ActivateGhostty is a best-effort attempt to just raise Ghostty as an app,
// without picking a specific window. Used as a fallback when there's no
// reliable way to identify *which* Ghostty window matters.
func ActivateGhostty() bool {
	err := exec.Command("open", "-a", "Ghostty").Run()
	return err == nil
}
