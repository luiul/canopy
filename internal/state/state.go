// Package state provides best-effort idle/working classification for agent
// processes herdr does not track (so herdr's own, presumably
// shell-integration-based, agent_status isn't available for them).
//
// There's no pty herdr doesn't already own for canopy to read output from;
// per-process CPU usage is the only signal available from outside the
// terminal. macOS's own `ps` %cpu is a decaying average over up to a
// minute of real time (see `man ps`), so a single sample lags well behind a
// process actually going idle after a burst of work; ClassifyStateFromRate
// exists so canopy's registry package can compute its own rate from two
// poll samples instead, bounded by canopy's own poll interval rather than
// that ~60s window. ClassifyState (a single raw sample) is only the
// fallback for a process's first poll, when there's no previous sample yet
// to diff against. Both are coarser than herdr's own status either way (
// neither can distinguish "idle" from "blocked on user input", both look
// like ~0% CPU), so both collapse to AgentState Idle here.
package state

// DefaultThreshold: a process actively doing agent work (tool calls,
// generation, streaming a response) reliably shows non-trivial CPU over a
// multi-second window; idling on a prompt or a slow network read does not.
const DefaultThreshold = 1.5

// AgentState is the coarse idle/working/unknown classification.
type AgentState string

const (
	Working AgentState = "working"
	Idle    AgentState = "idle"
	Unknown AgentState = "unknown"
)

// ClassifyState classifies a single `ps -o pcpu=` sample. Used only as the
// bootstrap fallback, for a process's very first poll, before there's a
// previous sample to compute a real rate from (see ClassifyStateFromRate
// and registry.refineExternalStates); a raw macOS %cpu sample is a decaying
// average over up to a minute of real time, not an instantaneous rate, so
// relying on it past that first poll is exactly what let a process read as
// "working" long after it had actually gone idle. A nil pcpu means the pid
// disappeared from the last `ps` snapshot (already gone by the time we
// sampled).
func ClassifyState(pcpu *float64, threshold float64) AgentState {
	if pcpu == nil {
		return Unknown
	}
	if *pcpu >= threshold {
		return Working
	}
	return Idle
}

// ClassifyStateFromRate classifies a CPU rate (percent, e.g. 100.0 == one
// full core saturated) the same threshold-based way ClassifyState does, but
// for a rate canopy computed itself from two of its own poll samples (see
// registry.refineExternalStates), rather than a single macOS `ps` %cpu
// sample.
func ClassifyStateFromRate(percent, threshold float64) AgentState {
	if percent >= threshold {
		return Working
	}
	return Idle
}

// ClassifyStateDefault is ClassifyState with DefaultThreshold, the way
// canopy's registry uses it for real polls.
func ClassifyStateDefault(pcpu *float64) AgentState {
	return ClassifyState(pcpu, DefaultThreshold)
}
