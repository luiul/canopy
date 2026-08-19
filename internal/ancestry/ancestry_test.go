package ancestry

import (
	"testing"

	"github.com/luiul/canopy/internal/scan"
)

type row struct {
	pid, ppid int
	comm      string
}

func table(rows ...row) map[int]scan.ProcessInfo {
	t := map[int]scan.ProcessInfo{}
	for _, r := range rows {
		t[r.pid] = scan.ProcessInfo{Pid: r.pid, Ppid: r.ppid, Pcpu: 0.0, Tty: "s000", Comm: r.comm}
	}
	return t
}

func TestClassifySurfaceTrustsHerdrTrackedWithoutWalkingTheTable(t *testing.T) {
	// herdr routes a pane's shell through its own headless server process,
	// not through whatever terminal a herdr *client* happens to be attached
	// from, so the ancestor chain wouldn't show anything herdr-related
	// anyway; herdrTracked=true must short-circuit the walk.
	tbl := table(row{1, 0, "/sbin/launchd"})
	if got := ClassifySurface(999, tbl, true); got != Herdr {
		t.Fatalf("got %v, want Herdr", got)
	}
}

func TestClassifySurfaceDetectsVSCodeAFewHopsUp(t *testing.T) {
	tbl := table(
		row{56621, 53610, "/Users/luis.aceituno/.local/bin/pi"},
		row{53610, 1350, "/bin/zsh"},
		row{1350, 797, "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper"},
		row{797, 1, "/Applications/Visual Studio Code.app/Contents/MacOS/Code"},
	)
	if got := ClassifySurface(56621, tbl, false); got != VSCode {
		t.Fatalf("got %v, want VSCode", got)
	}
}

func TestClassifySurfaceDetectsVSCodeInsiders(t *testing.T) {
	tbl := table(
		row{10, 5, "/Users/x/.local/bin/pi"},
		row{5, 2, "/bin/zsh"},
		row{2, 1, "/Applications/Visual Studio Code - Insiders.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper"},
	)
	if got := ClassifySurface(10, tbl, false); got != VSCode {
		t.Fatalf("got %v, want VSCode", got)
	}
}

func TestClassifySurfaceDetectsBareGhosttyTab(t *testing.T) {
	tbl := table(
		row{78424, 78245, "/Users/luis.aceituno/.local/bin/pi"},
		row{78245, 8028, "-/bin/zsh"},
		row{8028, 1, "/Applications/Ghostty.app/Contents/MacOS/ghostty"},
	)
	if got := ClassifySurface(78424, tbl, false); got != Ghostty {
		t.Fatalf("got %v, want Ghostty", got)
	}
}

func TestClassifySurfaceUnknownWhenNoRecognizedAncestor(t *testing.T) {
	tbl := table(
		row{10, 5, "/usr/local/bin/claude"},
		row{5, 1, "/sbin/launchd"},
	)
	if got := ClassifySurface(10, tbl, false); got != Unknown {
		t.Fatalf("got %v, want Unknown", got)
	}
}

func TestClassifySurfaceUnknownWhenPidMissingFromTable(t *testing.T) {
	if got := ClassifySurface(999999, map[int]scan.ProcessInfo{}, false); got != Unknown {
		t.Fatalf("got %v, want Unknown", got)
	}
}

func TestClassifySurfaceDoesNotLoopForeverOnACycle(t *testing.T) {
	// ppid pointing back at a pid already visited must terminate the walk
	// instead of spinning; MaxAncestorHops is the other backstop.
	tbl := map[int]scan.ProcessInfo{
		1: {Pid: 1, Ppid: 2, Pcpu: 0.0, Tty: "s000", Comm: "a"},
		2: {Pid: 2, Ppid: 1, Pcpu: 0.0, Tty: "s000", Comm: "b"},
	}
	if got := ClassifySurface(1, tbl, false); got != Unknown {
		t.Fatalf("got %v, want Unknown", got)
	}
}
