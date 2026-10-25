// Package jump opens or focuses a Ghostty window for an agent's working
// directory, regardless of its discovered host. Surface is discovery
// metadata, not an editor selector. Window-title matching lives in
// github.com/luiul/dashkit/mycelium, shared with understory.
package jump

import (
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/mycelium"
)

// Result reports whether a jump succeeded and a human-readable message
// about it.
type Result struct {
	OK      bool
	Message string
}

// openGhostty is a package-level seam onto mycelium.OpenGhostty.
// Tests replace it to check arguments and results without running
// osascript. Mycelium's tests cover the window-title matching itself.
var openGhostty = mycelium.OpenGhostty

// To opens or focuses Ghostty for entry, regardless of its Surface.
func To(entry registry.RegistryEntry) Result {
	if entry.Cwd == "" {
		return Result{false, "no known working directory to open"}
	}
	// Pass cwd unmodified: the matching window may name this exact
	// subdirectory. Mycelium handles work-tree root fallback itself.
	// Prefer the agent's tty in the window title so agents sharing a
	// checkout reach their own windows. Mycelium falls back to the path
	// when no title matches the tty.
	return fromMycelium(openGhostty(entry.Tty, entry.Cwd))
}

func fromMycelium(r mycelium.Result) Result {
	return Result{OK: r.OK, Message: r.Message}
}
