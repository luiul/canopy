package jump

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/luiul/canopy/internal/ancestry"
	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/dashkit/mycelium"
)

func entry(surface ancestry.Surface, mutate func(*registry.RegistryEntry)) registry.RegistryEntry {
	e := registry.RegistryEntry{Pid: 1, Kind: "pi", Tty: "s017", Cwd: "/Users/x/dotfiles", Surface: surface, State: "working"}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

// withFakes swaps openVSCode/openGhostty for the duration of fn, restoring
// the real mycelium functions afterward, so no test here ever shells out
// to osascript or the real `code` CLI. mycelium's own test suite already
// covers the window-detection logic these fakes stand in for; these tests
// only need to verify that To() dispatches to the right one, with the
// right argument, and maps its Result straight through. branchOf is
// stubbed to "" so dispatch tests never shell out to git either; a test
// that cares about the resolution overrides branchOf itself afterward.
func withFakes(t *testing.T, vscode func(path, branch string) mycelium.Result, ghostty func(path string) mycelium.Result) {
	t.Helper()
	origVSCode, origGhostty, origBranchOf := openVSCode, openGhostty, branchOf
	if vscode != nil {
		openVSCode = vscode
	}
	if ghostty != nil {
		openGhostty = ghostty
	}
	branchOf = func(cwd string) string { return "" }
	t.Cleanup(func() {
		openVSCode = origVSCode
		openGhostty = origGhostty
		branchOf = origBranchOf
	})
}

func TestJumpToVSCodeDelegatesToMyceliumWithTheEntrysCwd(t *testing.T) {
	var gotPath, gotBranch string
	withFakes(t, func(path, branch string) mycelium.Result {
		gotPath, gotBranch = path, branch
		return mycelium.Result{OK: true, Message: "Focused VS Code window for " + path + "."}
	}, nil)

	result := To(entry(ancestry.VSCode, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if gotPath != "/Users/x/dotfiles" {
		t.Fatalf("got path %q, want /Users/x/dotfiles", gotPath)
	}
	if gotBranch != "" {
		t.Fatalf("got branch %q, want \"\" (a RegistryEntry doesn't know the branch)", gotBranch)
	}
	if result.Message != "Focused VS Code window for /Users/x/dotfiles." {
		t.Fatalf("got message %q", result.Message)
	}
}

func TestJumpToVSCodePassesTheCwdThroughWithTheResolvedBranch(t *testing.T) {
	// The cwd goes to mycelium unmodified — the window to reuse may be
	// open on exactly that folder (a monorepo package the agent runs in),
	// and mycelium falls back to the work-tree root on its own — while
	// the branch still comes from git, since a RegistryEntry doesn't
	// record it and mycelium's rootName+branch matching needs it.
	var gotPath, gotBranch string
	withFakes(t, func(path, branch string) mycelium.Result {
		gotPath, gotBranch = path, branch
		return mycelium.Result{OK: true}
	}, nil)
	branchOf = func(cwd string) string {
		if cwd != "/x/worktrees/ISA-18436/global-ops/pipelines" {
			t.Fatalf("got branchOf cwd %q, want the entry's own cwd", cwd)
		}
		return "ISA-18436"
	}

	To(entry(ancestry.VSCode, func(e *registry.RegistryEntry) {
		e.Cwd = "/x/worktrees/ISA-18436/global-ops/pipelines"
	}))

	if gotPath != "/x/worktrees/ISA-18436/global-ops/pipelines" {
		t.Fatalf("got path %q, want the entry's cwd passed through unmodified", gotPath)
	}
	if gotBranch != "ISA-18436" {
		t.Fatalf("got branch %q, want %q", gotBranch, "ISA-18436")
	}
}

func TestGitBranchResolvesASubdirToItsCheckedOutBranch(t *testing.T) {
	root := initTestRepo(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if branch := gitBranch(sub); branch != "main" {
		t.Fatalf("got branch %q, want %q", branch, "main")
	}
}

func TestGitBranchIsEmptyOutsideAWorkTree(t *testing.T) {
	dir := t.TempDir() // no git init: not a repo

	if branch := gitBranch(dir); branch != "" {
		t.Fatalf("got branch %q, want \"\" outside a work tree", branch)
	}
}

func TestGitBranchTreatsADetachedHEADAsBranchless(t *testing.T) {
	root := initTestRepo(t)
	runGit(t, root, "checkout", "--detach", "HEAD")

	if branch := gitBranch(root); branch != "" {
		t.Fatalf("got branch %q, want \"\" for a detached HEAD", branch)
	}
}

// initTestRepo creates a git repo with one commit on branch "main" in a
// temp dir and returns its root, resolved through any symlinks the way
// git itself reports it (macOS's /var -> /private/var, notably).
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "-c", "user.email=test@test", "-c", "user.name=test", "commit", "-q", "--allow-empty", "-m", "init")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestJumpToVSCodeSurfacesAFailureFromMycelium(t *testing.T) {
	withFakes(t, func(path, branch string) mycelium.Result {
		return mycelium.Result{OK: false, Message: "couldn't open VS Code"}
	}, nil)

	result := To(entry(ancestry.VSCode, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if result.Message != "couldn't open VS Code" {
		t.Fatalf("got message %q", result.Message)
	}
}

func TestJumpToVSCodeWithoutACwdFailsClearlyWithoutCallingMycelium(t *testing.T) {
	called := false
	withFakes(t, func(path, branch string) mycelium.Result {
		called = true
		return mycelium.Result{OK: true}
	}, nil)

	result := To(entry(ancestry.VSCode, func(e *registry.RegistryEntry) { e.Cwd = "" }))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if !contains(result.Message, "working directory") {
		t.Fatalf("got message %q, want it to mention working directory", result.Message)
	}
	if called {
		t.Fatalf("want mycelium never called without a known cwd")
	}
}

func TestJumpToGhosttyDelegatesToMyceliumWithTheEntrysCwd(t *testing.T) {
	var gotPath string
	withFakes(t, nil, func(path string) mycelium.Result {
		gotPath = path
		return mycelium.Result{OK: true, Message: "Focused in Ghostty."}
	})

	result := To(entry(ancestry.Ghostty, nil))

	if !result.OK {
		t.Fatalf("want ok, got %+v", result)
	}
	if gotPath != "/Users/x/dotfiles" {
		t.Fatalf("got path %q, want /Users/x/dotfiles", gotPath)
	}
}

func TestJumpToGhosttyWithoutACwdFailsClearlyWithoutCallingMycelium(t *testing.T) {
	called := false
	withFakes(t, nil, func(path string) mycelium.Result {
		called = true
		return mycelium.Result{OK: true}
	})

	result := To(entry(ancestry.Ghostty, func(e *registry.RegistryEntry) { e.Cwd = "" }))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if called {
		t.Fatalf("want mycelium never called without a known cwd")
	}
}

func TestJumpToUnknownSurfaceSaysSoWithoutCallingMycelium(t *testing.T) {
	vscodeCalled, ghosttyCalled := false, false
	withFakes(t, func(path, branch string) mycelium.Result {
		vscodeCalled = true
		return mycelium.Result{OK: true}
	}, func(path string) mycelium.Result {
		ghosttyCalled = true
		return mycelium.Result{OK: true}
	})

	result := To(entry(ancestry.Unknown, nil))

	if result.OK {
		t.Fatalf("want not ok, got %+v", result)
	}
	if vscodeCalled || ghosttyCalled {
		t.Fatalf("want neither mycelium function called for an unknown surface")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
