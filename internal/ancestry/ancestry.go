// Package ancestry classifies which app is actually hosting a terminal
// process, by walking its parent chain through a whole-machine `ps`
// snapshot.
//
// This is what makes "jump to the window that has it open" possible without
// needing anything from herdr: a bare Ghostty tab's shell is a child of
// `ghostty` itself, herdr routes its panes' shells through the headless
// `herdr server` process (not through whatever terminal a herdr client
// happens to be attached from), and a VS Code integrated terminal's shell is
// a child of one of VS Code's `Code Helper` processes. canopy's jump package
// picks the actual jump mechanism (AppleScript, `code -r`, herdr's own
// socket focus) from this.
package ancestry

import (
	"strings"

	"github.com/luiul/canopy/internal/scan"
)

// MaxAncestorHops caps the ancestor walk so a corrupt table can't spin
// forever.
const MaxAncestorHops = 12

// Surface is the app actually hosting a terminal process.
type Surface string

const (
	Herdr   Surface = "herdr"
	VSCode  Surface = "vscode"
	Ghostty Surface = "ghostty"
	Unknown Surface = "unknown"
)

// AncestorHop is one link in a process's ancestor chain.
type AncestorHop struct {
	Pid  int
	Comm string
}

// AncestorChain walks pid's own entry and then its ancestors up to pid 1 or
// a missing/cyclic link, capped at MaxAncestorHops.
func AncestorChain(pid int, table map[int]scan.ProcessInfo) []AncestorHop {
	var chain []AncestorHop
	seen := map[int]bool{}
	current := pid
	for i := 0; i < MaxAncestorHops; i++ {
		info, ok := table[current]
		if !ok || seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, AncestorHop{Pid: info.Pid, Comm: info.Comm})
		if info.Pid == info.Ppid || info.Ppid <= 1 {
			break
		}
		current = info.Ppid
	}
	return chain
}

// ClassifySurface reports which app is hosting pid. herdrTracked
// short-circuits the walk: herdr already told us this pid is inside one of
// its own panes (via `herdr pane process-info`), which is authoritative and
// cheaper to trust than re-deriving it from comm paths, since herdr's own
// client/server split means a herdr pane's ancestor chain does not lead to
// whatever terminal a herdr client happens to be attached from.
func ClassifySurface(pid int, table map[int]scan.ProcessInfo, herdrTracked bool) Surface {
	if herdrTracked {
		return Herdr
	}

	for _, hop := range AncestorChain(pid, table) {
		comm := hop.Comm
		if strings.Contains(comm, "Visual Studio Code.app") || strings.Contains(comm, "Visual Studio Code - Insiders.app") {
			return VSCode
		}
		if strings.Contains(comm, "/Ghostty.app/") || strings.HasSuffix(comm, "/ghostty") {
			return Ghostty
		}
	}
	return Unknown
}
