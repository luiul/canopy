// Package jump brings whichever window is actually running a given agent
// process to the front. It maps a row's Surface (VS Code / Ghostty /
// unknown) onto github.com/luiul/dashkit/mycelium's shared open-or-focus logic —
// switch to an already-open window when one matches the row's working
// directory, or open a brand-new one when none does — rather than
// re-implementing that AppleScript-backed detection here: understory
// needs the exact same behavior for a worktree row with no agent
// connection of its own, so it lives in mycelium once instead of being
// duplicated in both trees. Nothing to jump to for Surface Unknown,
// canopy doesn't know what's hosting it.
package jump

import (
	"os/exec"
	"strings"

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
// the Surface -> mycelium dispatch without shelling out to osascript or
// the real `code` CLI; mycelium's own test suite already covers the
// window-detection logic itself in depth. branchOf is a seam too,
// since its real implementation shells out to git.
var (
	openVSCode  = mycelium.OpenVSCode
	openGhostty = mycelium.OpenGhostty
	branchOf    = gitBranch
)

// gitBranch resolves the branch currently checked out in the git work
// tree containing cwd — the one signal mycelium's rootName+branch
// window matching still needs from here, since a RegistryEntry doesn't
// record it (this ecosystem's layout gives every worktree of a repo the
// repo's own leaf folder name, so the basename alone can't tell
// same-named worktree windows apart). The cwd itself goes to mycelium
// unmodified: the window to reuse may be open on exactly that folder
// (a monorepo package the agent runs in, opened directly as its own
// window, e.g. tardis-community/pipelines/…/dbt titled
// "dbt — master"), and resolving the cwd up to the work-tree root here
// instead would make such a window unmatchable — no title carries the
// root's folder name, and a window with no file focused is invisible to
// mycelium's nested-path check. mycelium falls back to the work-tree
// root on its own when nothing is open on the cwd itself, so an agent
// running in a subdirectory still finds a window open on the checkout
// as a whole. A cwd outside any git work tree, or a detached HEAD
// (rev-parse reports the branch as "HEAD"), has no branch to key on:
// "", and mycelium's branch-less matching applies.
func gitBranch(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		branch = ""
	}
	return branch
}

// To jumps to whichever window is running entry, using the real OS.
func To(entry registry.RegistryEntry) Result {
	switch entry.Surface {
	case ancestry.VSCode:
		if entry.Cwd == "" {
			return Result{false, "no known working directory to open"}
		}
		// A RegistryEntry knows the agent's cwd but not its branch, and
		// mycelium needs rootName+branch together to tell same-named
		// worktree windows apart — resolve the branch from git here, at
		// jump time (one subprocess, only for the row actually jumped
		// to). The cwd goes to mycelium unmodified: that's the folder the
		// window to reuse may be scoped to exactly (see gitBranch's doc).
		return fromMycelium(openVSCode(entry.Cwd, branchOf(entry.Cwd)))
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
