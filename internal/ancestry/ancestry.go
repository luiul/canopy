// Package ancestry classifies which app is actually hosting a terminal
// process, by walking its parent chain through a whole-machine `ps`
// snapshot.
//
// A bare Ghostty tab's shell is a child of `ghostty` itself, and a VS Code
// integrated terminal's shell is a child of one of VS Code's `Code Helper`
// processes. canopy's jump package picks the actual jump mechanism
// (AppleScript for Ghostty, `code --reuse-window` for VS Code) from this
// classification.
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

// ClassifySurface reports which app is hosting pid by walking its ancestor
// chain and looking for VS Code or Ghostty process names.
func ClassifySurface(pid int, table map[int]scan.ProcessInfo) Surface {

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
