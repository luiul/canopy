package tui

import (
	"os"
	"testing"
	"time"

	"github.com/luiul/canopy/internal/ack"
)

// TestMain stubs the package-level ack seams (see done.go) to safe no-ops
// before running any test in this package. Every test that predates
// cross-instance ack sync knows nothing about it; without this default,
// acknowledge()/updateDoneTracking would hit the real
// ~/.pi/agent/canopy-status/acks directory on disk on every test run,
// leaking state both across separate `go test` invocations and across
// unrelated tests within the same run (many of them reuse the same
// synthetic pid, e.g. entry(1, ...)). Tests that actually want to
// exercise the sync behavior call withAckStore, which swaps in an
// in-memory fake and restores this no-op default afterward via
// t.Cleanup — the same seam pattern registry_test.go already uses for
// pistatusRead/resolveCwds.
func TestMain(m *testing.M) {
	ackRead = func(string) (ack.Record, bool) { return ack.Record{}, false }
	ackWrite = func(string, time.Time) error { return nil }
	ackRemove = func(string) {}
	os.Exit(m.Run())
}

// withAckStore swaps ackRead/ackWrite/ackRemove for an in-memory fake
// backed by the returned map, scoped to t and restored to TestMain's
// no-op default on cleanup. Two Models sharing the same store (as
// returned by a single call to withAckStore, used by both) is exactly
// what stands in for "two separate canopy instances" in the tests below:
// each Model only ever calls the package-level ackRead/ackWrite/ackRemove
// vars, with no notion of which Model called them, the same way two real
// canopy processes only ever agree via the file on disk.
func withAckStore(t *testing.T) map[string]ack.Record {
	t.Helper()
	store := map[string]ack.Record{}
	prevRead, prevWrite, prevRemove := ackRead, ackWrite, ackRemove
	ackRead = func(key string) (ack.Record, bool) {
		rec, ok := store[key]
		return rec, ok
	}
	ackWrite = func(key string, rawAt time.Time) error {
		store[key] = ack.Record{Key: key, RawAt: rawAt, At: time.Now()}
		return nil
	}
	ackRemove = func(key string) { delete(store, key) }
	t.Cleanup(func() {
		ackRead, ackWrite, ackRemove = prevRead, prevWrite, prevRemove
	})
	return store
}
