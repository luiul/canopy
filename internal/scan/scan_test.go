package scan

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// testKinds is the tracked set the parsing tests run against. It
// deliberately includes a name no hardcoded list would ever contain
// ("myagent"), so the tests prove the set is caller-driven: nothing
// matches unless the caller put it in.
var testKinds = map[string]bool{
	"pi": true, "pig": true, "claude": true, "codex": true, "myagent": true,
}

func TestParsePsOutputMatchesKnownKindWithTty(t *testing.T) {
	got := ParsePsOutput("78424 ttys006 pi\n", testKinds)
	want := []ProcessMatch{{Pid: 78424, Tty: "ttys006", Kind: "pi", Args: "pi"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParsePsOutputTracksExactlyTheGivenSet(t *testing.T) {
	// A custom kind matches, and a name absent from this particular set
	// (pi) does not: there is no hidden built-in list underneath.
	kinds := map[string]bool{"myagent": true}
	got := ParsePsOutput("1 ttys000 myagent\n2 ttys001 pi\n", kinds)
	if len(got) != 1 || got[0].Kind != "myagent" {
		t.Fatalf("got %+v, want only the myagent row", got)
	}
}

func TestParsePsOutputMatchesPigNativeBinaryButNotItsLauncher(t *testing.T) {
	// A live pig session's process shape: an npm launcher (argv0 `node`,
	// never tracked), the real agent binary underneath it (basename
	// `pig`, the row canopy tracks), and pig's node extension host (argv0
	// `node` again).
	out := "11 ttys010 node /opt/homebrew/bin/pig\n" +
		"12 ttys010 /opt/homebrew/lib/node_modules/@pi-in-go/pig-darwin-arm64/pig\n" +
		"13 ttys010 node /opt/homebrew/lib/node_modules/@pi-in-go/pig/cells/host.js\n"
	got := ParsePsOutput(out, testKinds)
	if len(got) != 1 || got[0].Pid != 12 || got[0].Kind != "pig" {
		t.Fatalf("got %+v, want only pid 12 (the native pig binary)", got)
	}
}

func TestParsePsOutputSkipsProcessesWithoutATty(t *testing.T) {
	got := ParsePsOutput("111 ?? codex mcp\n222 ttys003 codex\n", testKinds)
	if len(got) != 1 || got[0].Pid != 222 {
		t.Fatalf("got %+v, want only pid 222", got)
	}
}

func TestParsePsOutputSkipsUnknownKinds(t *testing.T) {
	got := ParsePsOutput("1 ttys000 bun /some/server.bundle.mjs\n2 ttys001 zsh\n", testKinds)
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestParsePsOutputResolvesFullPathArgv0ToBasename(t *testing.T) {
	got := ParsePsOutput("5 ttys002 /usr/local/bin/claude --resume\n", testKinds)
	if len(got) != 1 || got[0].Kind != "claude" {
		t.Fatalf("got %+v, want kind claude", got)
	}
}

func TestParsePsOutputDenylistsHelperSubcommands(t *testing.T) {
	tokens := make([]string, 0, len(SecondTokenDenylist))
	for tok := range SecondTokenDenylist {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)
	for _, tok := range tokens {
		out := "9 ttys004 codex " + tok + "\n"
		if got := ParsePsOutput(out, testKinds); len(got) != 0 {
			t.Fatalf("token %q should be filtered out, got %+v", tok, got)
		}
	}
}

func TestParsePsOutputIgnoresBlankAndMalformedLines(t *testing.T) {
	got := ParsePsOutput("\n   \nnot a valid ps line\n42 ttys005 pi\n", testKinds)
	if len(got) != 1 || got[0].Pid != 42 {
		t.Fatalf("got %+v, want only pid 42", got)
	}
}

func TestParseLsofCwdOutputPairsPidAndPath(t *testing.T) {
	out := "p123\nfcwd\nn/Users/luis/dotfiles\np456\nfcwd\nn/Users/luis/projects\n"
	got := ParseLsofCwdOutput(out)
	want := map[int]string{123: "/Users/luis/dotfiles", 456: "/Users/luis/projects"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseLsofCwdOutputEmptyInput(t *testing.T) {
	got := ParseLsofCwdOutput("")
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestParseProcessTableOutputParsesPidPpidPcpuRssEtimeTtyStateComm(t *testing.T) {
	out := "56621   53610   3.2 40656 04-09:19:45 s017 S+ /Users/luis.aceituno/.local/bin/pi\n"
	table := ParseProcessTableOutput(out)
	want := ProcessInfo{
		Pid: 56621, Ppid: 53610, Pcpu: 3.2, RssKb: 40656,
		Etime: 4*24*time.Hour + 9*time.Hour + 19*time.Minute + 45*time.Second,
		Tty:   "s017", State: "S+", Comm: "/Users/luis.aceituno/.local/bin/pi",
	}
	if got := table[56621]; got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestProcessInfoStoppedOnlyForALeadingT(t *testing.T) {
	cases := map[string]bool{
		"T": true, "Ts": true, // stopped, plain and session-leader
		"S": false, "Ss": false, "R+": false, "U": false, "": false,
	}
	for state, want := range cases {
		if got := (ProcessInfo{State: state}).Stopped(); got != want {
			t.Errorf("ProcessInfo{State: %q}.Stopped() = %v, want %v", state, got, want)
		}
	}
}

func TestParseProcessTableOutputPreservesSpacesInComm(t *testing.T) {
	// macOS `comm` is the full executable path, and paths like VS Code's
	// helper processes contain literal spaces; comm must stay the last,
	// greedily-parsed column or this truncates.
	out := "52562 1350 0.5 15120 00:05 ?? S /Applications/Visual Studio Code.app/Contents/Frameworks/" +
		"Code Helper (Renderer).app/Contents/MacOS/Code Helper (Renderer) --type=renderer\n"
	table := ParseProcessTableOutput(out)
	got := table[52562].Comm
	want := "Code Helper (Renderer) --type=renderer"
	if len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Fatalf("got comm %q, want suffix %q", got, want)
	}
}

func TestParseProcessTableOutputSkipsMalformedLines(t *testing.T) {
	out := "\nnot enough fields\n1 0 0.0 22528 04-20:12:53 ?? Ss launchd\n"
	table := ParseProcessTableOutput(out)
	if len(table) != 1 {
		t.Fatalf("got %+v, want only pid 1", table)
	}
	if _, ok := table[1]; !ok {
		t.Fatalf("missing pid 1 in %+v", table)
	}
}

func TestParsePsEtime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"00:05", 5 * time.Second},
		{"01:02", time.Minute + 2*time.Second},
		{"09:19:45", 9*time.Hour + 19*time.Minute + 45*time.Second},
		// leading "<days>-" prefix for anything running a day or longer.
		{"04-20:12:53", 4*24*time.Hour + 20*time.Hour + 12*time.Minute + 53*time.Second},
		{"1-00:00", 24*time.Hour + 0 + 0},
	}
	for _, c := range cases {
		got, err := parsePsEtime(c.in)
		if err != nil {
			t.Errorf("parsePsEtime(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePsEtime(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "abc", "1:2:3:4", "x-00:00"} {
		if _, err := parsePsEtime(bad); err == nil {
			t.Errorf("parsePsEtime(%q): want an error, got none", bad)
		}
	}
}

// stubExec swaps in a fake runCommand (and shrunk timing knobs) for the
// duration of a test. The real execTimeout is 5s; exercising the timeout
// path against it would take seconds per test.
func stubExec(t *testing.T, fn func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	t.Helper()
	previousRun, previousTimeout, previousBackoff := runCommand, execTimeout, retryBackoff
	runCommand = fn
	execTimeout, retryBackoff = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { runCommand, execTimeout, retryBackoff = previousRun, previousTimeout, previousBackoff })
}

func TestScanAgentProcessesRetriesOnceAndRecovers(t *testing.T) {
	calls := 0
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("ps: boom")
		}
		return []byte("  123 ttys000 pi\n"), nil
	})

	matches, err := ScanAgentProcesses("someuser", testKinds)
	if err != nil {
		t.Fatalf("got error %v, want the retry to recover", err)
	}
	if len(matches) != 1 || matches[0].Pid != 123 {
		t.Fatalf("got %+v, want the retried ps output parsed", matches)
	}
	if calls != 2 {
		t.Fatalf("got %d calls, want exactly 2 (fail, retry)", calls)
	}
}

func TestScanAgentProcessesReportsATypedTimeoutWhenPsHangs(t *testing.T) {
	calls := 0
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		<-ctx.Done() // hang until the deadline kills us, like a wedged ps
		return nil, ctx.Err()
	})

	_, err := ScanAgentProcesses("someuser", testKinds)
	if !errors.Is(err, ErrScanTimeout) {
		t.Fatalf("got error %v, want it to wrap ErrScanTimeout", err)
	}
	if calls != 2 {
		t.Fatalf("got %d calls, want exactly 2 (hang, retried hang)", calls)
	}
}

func TestScanAgentProcessesDoesNotRetryASuccess(t *testing.T) {
	calls := 0
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return []byte(""), nil
	})

	if _, err := ScanAgentProcesses("someuser", testKinds); err != nil {
		t.Fatalf("got error %v, want none", err)
	}
	if calls != 1 {
		t.Fatalf("got %d calls, want exactly 1 (no retry on success)", calls)
	}
}
