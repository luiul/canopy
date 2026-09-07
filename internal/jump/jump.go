// Package jump brings whichever window is actually running a given agent
// process to the front. It maps a row's Surface (VS Code / Ghostty /
// unknown) onto github.com/luiul/dashkit/mycelium's shared open-or-focus logic —
// switch to an already-open window when one matches the row's working
// directory, or open a brand-new one when none does — rather than
// re-implementing that registry-backed detection here: understory
// needs the exact same behavior for a worktree row with no agent
// connection of its own, so it lives in mycelium once instead of being
// duplicated in both trees. Nothing to jump to for Surface Unknown,
// canopy doesn't know what's hosting it.
package jump

import (
	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/mycelium"
)

// Result reports whether a jump succeeded and a human-readable message
// about it.
type Result struct {
	OK      bool
	Message string
}

// openVSCode and openGhostty are package-level seams onto mycelium's
// exported functions, swapped out in tests so jump_test.go can verify
// the Surface -> mycelium dispatch without shelling out to the real
// `code` CLI or osascript; mycelium's own test suite already covers the
// window-detection logic itself in depth.
var (
	openVSCode  = mycelium.OpenVSCode
	openGhostty = mycelium.OpenGhostty
)

// To jumps to whichever window is running entry, using the real OS.
func To(entry registry.RegistryEntry) Result {
	switch entry.Surface {
	case ancestry.VSCode:
		if entry.Cwd == "" {
			return Result{false, "no known working directory to open"}
		}
		// The cwd goes to mycelium unmodified: the window to reuse may
		// be open on exactly that folder (a monorepo package the agent
		// runs in, opened directly as its own window), and mycelium's
		// registry match falls back to the work-tree root on its own
		// when nothing is open on the cwd itself.
		return fromMycelium(openVSCode(entry.Cwd))
	case ancestry.Ghostty:
		if entry.Cwd == "" {
			return Result{false, "no known working directory to focus"}
		}
		return fromMycelium(openGhostty(entry.Cwd))
	default:
		return Result{false, "don't know how to jump to this process's window yet"}
	}
}

func fromMycelium(r mycelium.Result) Result {
	return Result{OK: r.OK, Message: r.Message}
}
