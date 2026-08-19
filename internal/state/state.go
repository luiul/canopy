// Package state provides best-effort idle/working classification for agent
// processes herdr does not track (so herdr's own, presumably
// shell-integration-based, agent_status isn't available for them).
//
// There's no pty herdr doesn't already own for canopy to read output from;
// per-process CPU% deltas across polls are the only signal available from
// outside the terminal. This is coarser than herdr's own status (it cannot
// distinguish "idle" from "blocked on user input", both look like ~0% CPU),
// so both collapse to AgentState Idle here.
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

// ClassifyState classifies a single `ps -o pcpu=` sample, i.e. the process's
// average CPU usage since it started, not an instantaneous rate. That is
// still a usable signal here because `ps -A` runs on every poll: a process
// that has been quietly idling for a while has a low, slowly-drifting
// average, while one mid-generation pulls that average up within a few poll
// cycles. It is a heuristic, not a measurement of "is it doing something
// right now": a long-idle process during a brief burst of work may take a
// couple of polls to cross the threshold, and vice versa on stopping. A nil
// pcpu means the pid disappeared from the last `ps` snapshot (already gone
// by the time we sampled).
func ClassifyState(pcpu *float64, threshold float64) AgentState {
	if pcpu == nil {
		return Unknown
	}
	if *pcpu >= threshold {
		return Working
	}
	return Idle
}

// ClassifyStateDefault is ClassifyState with DefaultThreshold, the way
// canopy's registry uses it for real polls.
func ClassifyStateDefault(pcpu *float64) AgentState {
	return ClassifyState(pcpu, DefaultThreshold)
}
