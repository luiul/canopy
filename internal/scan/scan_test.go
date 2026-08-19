package scan

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestParsePsOutputMatchesKnownKindWithTty(t *testing.T) {
	got := ParsePsOutput("78424 ttys006 pi\n")
	want := []ProcessMatch{{Pid: 78424, Tty: "ttys006", Kind: "pi", Args: "pi"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParsePsOutputSkipsProcessesWithoutATty(t *testing.T) {
	got := ParsePsOutput("111 ?? codex mcp\n222 ttys003 codex\n")
	if len(got) != 1 || got[0].Pid != 222 {
		t.Fatalf("got %+v, want only pid 222", got)
	}
}

func TestParsePsOutputSkipsUnknownKinds(t *testing.T) {
	got := ParsePsOutput("1 ttys000 bun /some/server.bundle.mjs\n2 ttys001 zsh\n")
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestParsePsOutputResolvesFullPathArgv0ToBasename(t *testing.T) {
	got := ParsePsOutput("5 ttys002 /usr/local/bin/claude --resume\n")
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
		if got := ParsePsOutput(out); len(got) != 0 {
			t.Fatalf("token %q should be filtered out, got %+v", tok, got)
		}
	}
}

func TestParsePsOutputIgnoresBlankAndMalformedLines(t *testing.T) {
	got := ParsePsOutput("\n   \nnot a valid ps line\n42 ttys005 pi\n")
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

func TestParseProcessTableOutputParsesPidPpidPcpuTtyComm(t *testing.T) {
	out := "56621   53610   3.2 s017 14:28.08 /Users/luis.aceituno/.local/bin/pi\n"
	table := ParseProcessTableOutput(out)
	want := ProcessInfo{Pid: 56621, Ppid: 53610, Pcpu: 3.2, CPUTime: 14*time.Minute + 28*time.Second + 80*time.Millisecond, Tty: "s017", Comm: "/Users/luis.aceituno/.local/bin/pi"}
	if got := table[56621]; got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseProcessTableOutputPreservesSpacesInComm(t *testing.T) {
	// macOS `comm` is the full executable path, and paths like VS Code's
	// helper processes contain literal spaces; comm must stay the last,
	// greedily-parsed column or this truncates.
	out := "52562 1350 0.5 ?? 0:00.00 /Applications/Visual Studio Code.app/Contents/Frameworks/" +
		"Code Helper (Renderer).app/Contents/MacOS/Code Helper (Renderer) --type=renderer\n"
	table := ParseProcessTableOutput(out)
	got := table[52562].Comm
	want := "Code Helper (Renderer) --type=renderer"
	if len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Fatalf("got comm %q, want suffix %q", got, want)
	}
}

func TestParseProcessTableOutputSkipsMalformedLines(t *testing.T) {
	out := "\nnot enough fields\n1 0 0.0 ?? 0:00.01 launchd\n"
	table := ParseProcessTableOutput(out)
	if len(table) != 1 {
		t.Fatalf("got %+v, want only pid 1", table)
	}
	if _, ok := table[1]; !ok {
		t.Fatalf("missing pid 1 in %+v", table)
	}
}

func TestParsePsCPUTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"0:00.00", 0},
		{"0:00.01", 10 * time.Millisecond},
		{"14:28.08", 14*time.Minute + 28*time.Second + 80*time.Millisecond},
		// macOS never rolls a long-running process's cputime over into an
		// hours component; minutes just keep growing.
		{"1876:29.89", 1876*time.Minute + 29*time.Second + 890*time.Millisecond},
		// tolerated in case some other ps ever does format it this way.
		{"1:02:03.50", time.Hour + 2*time.Minute + 3*time.Second + 500*time.Millisecond},
	}
	for _, c := range cases {
		got, err := parsePsCPUTime(c.in)
		if err != nil {
			t.Errorf("parsePsCPUTime(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePsCPUTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "abc", "1:2:3:4"} {
		if _, err := parsePsCPUTime(bad); err == nil {
			t.Errorf("parsePsCPUTime(%q): want an error, got none", bad)
		}
	}
}
