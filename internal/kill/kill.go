// Package kill delivers a signal (SIGTERM, SIGKILL, SIGSTOP, SIGCONT) to
// the agent process behind a dashboard row: the side effect behind the
// TUI's x/X (terminate/kill with confirmation), p (pause/resume), and D
// (terminate every done row) keybinds.
//
// Every signal goes through a process-identity guard first. Between the
// poll that produced a row and the user confirming the prompt, the process
// may have exited and its pid been recycled by the OS for an unrelated
// process; signaling on pid alone would then hit an innocent bystander.
// The guard compares process *identity* instead: same pid AND an etime
// consistent with the lifetime the row was built from (same pid whose
// clock has advanced by roughly the wall time since that sample is the
// same process, guaranteed by the OS; a recycled pid belongs to a younger
// process and fails the check). This mirrors jump's package role (the one
// other row-level side effect), with the same package-level seams swapped
// out in tests.
package kill

import (
	"fmt"
	"syscall"
	"time"

	"github.com/luiul/canopy/internal/registry"
	"github.com/luiul/canopy/internal/scan"
)

// Result reports whether the signal was delivered, plus a human-readable
// message about it (shown in the TUI's footer). Same shape as jump.Result.
type Result struct {
	OK      bool
	Message string
}

// signalProcess and processTable are package-level seams onto the real OS
// calls, swapped out in tests so the identity guard and error paths can be
// exercised without signaling real processes; see registry.go's own seams
// for the same pattern.
var (
	signalProcess = syscall.Kill
	processTable  = scan.ScanProcessTable
)

// identitySlack bounds how far a process's current etime may have advanced
// past the entry's Uptime sample before Process refuses to treat them as
// the same process. The TUI re-stamps an armed prompt's entries from every
// poll (see Model.pendingKill), so at confirm time Uptime is at most one
// poll interval old; the slack only has to cover that interval plus
// scheduling jitter, not the time the prompt sat open. The lower bound
// (current etime must not be *below* the sampled Uptime) is what actually
// catches a recycled pid: a replacement process is always younger than the
// original's lifetime. The generous upper bound exists so a slow
// --interval never falsely refuses to signal a legitimate, still-running
// process.
const identitySlack = 90 * time.Second

// Name is sig's conventional name ("SIGTERM", ...), for prompt and result
// messages.
func Name(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGSTOP:
		return "SIGSTOP"
	case syscall.SIGCONT:
		return "SIGCONT"
	default:
		return fmt.Sprintf("signal %d", int(sig))
	}
}

// Verb is sig's past-tense verb for result notifications ("terminated",
// ...), lowercase to match the status line's terse fragment style.
func Verb(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "terminated"
	case syscall.SIGKILL:
		return "killed"
	case syscall.SIGSTOP:
		return "paused"
	case syscall.SIGCONT:
		return "resumed"
	default:
		return "signaled"
	}
}

// PromptVerb is sig's imperative verb for confirmation prompts
// ("Terminate", "Kill"). Prompts lead with a plain verb rather than the
// raw signal name ("Terminate pi (pid 42, ~/path)? [y/N]"); signal names
// themselves stay in result notifications and the help overlay.
func PromptVerb(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "Terminate"
	case syscall.SIGKILL:
		return "Kill"
	case syscall.SIGSTOP:
		return "Pause"
	case syscall.SIGCONT:
		return "Resume"
	default:
		return Name(sig)
	}
}

// Process sends sig to the process behind entry, but only after
// re-verifying its identity against a fresh process-table snapshot (see
// the package doc). A pid that no longer exists, or one whose lifetime
// can't be reconciled with the entry's Uptime sample (a recycled pid), is
// refused with OK=false and no signal sent.
//
// An entry with Uptime == 0 had no process-table sample on the poll that
// built it (see RegistryEntry.Uptime), so there is no lifetime to compare
// against; the guard then degrades to existence-only, documented here
// rather than silently tightening.
func Process(entry registry.RegistryEntry, sig syscall.Signal) Result {
	info, ok := processTable()[entry.Pid]
	if !ok {
		return Result{false, fmt.Sprintf("%s (pid %d) already exited", entry.Kind, entry.Pid)}
	}
	if entry.Uptime > 0 && (info.Etime < entry.Uptime || info.Etime-entry.Uptime > identitySlack) {
		return Result{false, fmt.Sprintf("pid %d no longer belongs to the %s session it did at the last poll (recycled pid?); not signaling it", entry.Pid, entry.Kind)}
	}
	if err := signalProcess(entry.Pid, sig); err != nil {
		return Result{false, fmt.Sprintf("%s %s (pid %d) failed: %v", Name(sig), entry.Kind, entry.Pid, err)}
	}
	return Result{true, fmt.Sprintf("%s %s (pid %d)", Verb(sig), entry.Kind, entry.Pid)}
}
