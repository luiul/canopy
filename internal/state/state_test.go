package state

import "testing"

func f(v float64) *float64 { return &v }

func TestClassifyStateWorkingAtOrAboveThreshold(t *testing.T) {
	if got := ClassifyState(f(5.0), 1.5); got != Working {
		t.Fatalf("got %v, want Working", got)
	}
	if got := ClassifyState(f(1.5), 1.5); got != Working {
		t.Fatalf("got %v, want Working", got)
	}
}

func TestClassifyStateIdleBelowThreshold(t *testing.T) {
	if got := ClassifyState(f(0.0), 1.5); got != Idle {
		t.Fatalf("got %v, want Idle", got)
	}
	if got := ClassifyState(f(1.4), 1.5); got != Idle {
		t.Fatalf("got %v, want Idle", got)
	}
}

func TestClassifyStateUnknownWhenPidHasNoSample(t *testing.T) {
	if got := ClassifyState(nil, DefaultThreshold); got != Unknown {
		t.Fatalf("got %v, want Unknown", got)
	}
}

func TestAgentStateValuesArePlainStringsForDisplay(t *testing.T) {
	if string(Working) != "working" {
		t.Fatalf("got %q", Working)
	}
	if string(Idle) != "idle" {
		t.Fatalf("got %q", Idle)
	}
	if string(Unknown) != "unknown" {
		t.Fatalf("got %q", Unknown)
	}
}

func TestClassifyStateFromRateWorkingAtOrAboveThreshold(t *testing.T) {
	if got := ClassifyStateFromRate(5.0, 1.5); got != Working {
		t.Fatalf("got %v, want Working", got)
	}
	if got := ClassifyStateFromRate(1.5, 1.5); got != Working {
		t.Fatalf("got %v, want Working", got)
	}
}

func TestClassifyStateFromRateIdleBelowThreshold(t *testing.T) {
	if got := ClassifyStateFromRate(0.0, 1.5); got != Idle {
		t.Fatalf("got %v, want Idle", got)
	}
	if got := ClassifyStateFromRate(1.4, 1.5); got != Idle {
		t.Fatalf("got %v, want Idle", got)
	}
}
