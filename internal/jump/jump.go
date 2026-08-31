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
// window-detection logic itself in depth. repoContext is a seam too,
// since its real implementation shells out to git.
var (
	openVSCode  = mycelium.OpenVSCode
	openGhostty = mycelium.OpenGhostty
	repoContext = gitRepoContext
)

// gitRepoContext resolves the git work tree containing cwd: its root
// and the branch currently checked out there, the two signals
// mycelium's rootName+branch window matching needs to tell same-named
// worktree folders apart (this ecosystem's layout gives every worktree
// of a repo the repo's own leaf folder name, so the basename alone
// can't — without the branch, a jump to any of several same-named
// worktree windows always raises whichever enumerates first). A cwd in
// a subdirectory of a checkout resolves to the checkout's root, so the
// jump targets the window open on the repo rather than opening a
// redundant new window on the subdir. A cwd outside any git work tree
// comes back as (cwd, "") and mycelium's branch-less matching applies,
// same as before; so does a detached HEAD (rev-parse reports the branch
// as "HEAD"), which has no branch to key on.
func gitRepoContext(cwd string) (path, branch string) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return cwd, ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) != 2 || lines[0] == "" {
		return cwd, ""
	}
	branch = lines[1]
	if branch == "HEAD" {
		branch = ""
	}
	return lines[0], branch
}

// To jumps to whichever window is running entry, using the real OS.
func To(entry registry.RegistryEntry) Result {
	switch entry.Surface {
	case ancestry.VSCode:
		if entry.Cwd == "" {
			return Result{false, "No known working directory to open."}
		}
		// A RegistryEntry knows the agent's cwd but not its branch, and
		// mycelium needs rootName+branch together to tell same-named
		// worktree windows apart — resolve both from git here, at jump
		// time (one subprocess, only for the row actually jumped to).
		path, branch := repoContext(entry.Cwd)
		return fromMycelium(openVSCode(path, branch))
	case ancestry.Ghostty:
		if entry.Cwd == "" {
			return Result{false, "No known working directory to focus."}
		}
		return fromMycelium(openGhostty(entry.Cwd))
	default:
		return Result{false, "Don't know how to jump to this process's window yet."}
	}
}

func fromMycelium(r mycelium.Result) Result {
	return Result{OK: r.OK, Message: r.Message}
}
