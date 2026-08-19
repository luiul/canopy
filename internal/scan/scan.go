// Package scan finds the current user's processes that match herdr's known
// agent kinds.
//
// herdr recognizes a fixed set of agent CLIs (`herdr agent start --help`
// lists them). canopy uses the same list: an agent process herdr doesn't
// know how to detect anyway isn't in scope for "an agent herdr can't see".
package scan

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// KnownKinds is kept in sync with `herdr agent start --help`'s `--kind`
// values.
var KnownKinds = map[string]bool{
	"pi": true, "claude": true, "codex": true, "gemini": true, "cursor": true,
	"devin": true, "agy": true, "cline": true, "omp": true, "mastracode": true,
	"opencode": true, "copilot": true, "kimi": true, "kiro": true, "droid": true,
	"amp": true, "grok": true, "hermes": true, "kilo": true, "qodercli": true,
	"maki": true,
}

// SecondTokenDenylist filters out subcommand/flag invocations that share a
// kind's executable name but aren't the interactive agent itself (e.g.
// `codex mcp`, `claude --version`).
var SecondTokenDenylist = map[string]bool{
	"mcp": true, "mcp-server": true, "serve": true, "server": true,
	"--version": true, "-v": true, "-V": true, "--help": true, "-h": true,
}

var psLineRe = regexp.MustCompile(`^(\d+)\s+(\S+)\s+(.*)$`)

// ProcessMatch is one process from `ps` that matches a known agent kind.
type ProcessMatch struct {
	Pid  int
	Tty  string
	Kind string
	Args string
}

// ParsePsOutput is the pure parsing/filtering logic for
// `ps -o pid=,tty=,args=` output, split out from ScanAgentProcesses so it's
// testable without a real process table.
func ParsePsOutput(output string) []ProcessMatch {
	var matches []ProcessMatch
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		m := psLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pidStr, tty, args := m[1], m[2], m[3]
		if tty == "??" || tty == "?" {
			continue // no controlling terminal: not an interactive agent
		}
		tokens := strings.Fields(args)
		if len(tokens) == 0 {
			continue
		}
		argv0 := tokens[0]
		kind := argv0
		if idx := strings.LastIndex(argv0, "/"); idx != -1 {
			kind = argv0[idx+1:]
		}
		if !KnownKinds[kind] {
			continue
		}
		if len(tokens) > 1 && SecondTokenDenylist[tokens[1]] {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		matches = append(matches, ProcessMatch{Pid: pid, Tty: tty, Kind: kind, Args: args})
	}
	return matches
}

// ScanAgentProcesses shells out to `ps` and returns every known-kind agent
// process owned by user.
func ScanAgentProcesses(user string) []ProcessMatch {
	out, err := exec.Command("ps", "-u", user, "-o", "pid=,tty=,args=").Output()
	if err != nil {
		return nil
	}
	return ParsePsOutput(string(out))
}

// ParseLsofCwdOutput is the pure parsing logic for `lsof -a -d cwd -Fn`
// output (`p<pid>` then `n<path>` pairs).
func ParseLsofCwdOutput(output string) map[int]string {
	cwdByPid := map[int]string{}
	currentPid := -1
	haveCurrent := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, err := strconv.Atoi(line[1:])
			if err != nil {
				haveCurrent = false
				continue
			}
			currentPid = pid
			haveCurrent = true
		case strings.HasPrefix(line, "n") && haveCurrent:
			cwdByPid[currentPid] = line[1:]
		}
	}
	return cwdByPid
}

// ResolveCwds shells out to `lsof` to resolve the current working directory
// of every pid in pids.
func ResolveCwds(pids []int) map[int]string {
	if len(pids) == 0 {
		return map[int]string{}
	}
	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}
	out, err := exec.Command("lsof", "-a", "-p", strings.Join(pidStrs, ","), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return map[int]string{}
	}
	return ParseLsofCwdOutput(string(out))
}

// ProcessInfo is one row of the whole-machine process table: enough to walk
// an ancestor chain (Ppid, Comm), to sample instantaneous CPU usage (Pcpu)
// for a brand new process canopy has no prior sample for yet, and to
// compute a poll-to-poll CPU delta (CPUTime) for everything else, since
// Pcpu itself is a decaying average macOS computes over up to a minute of
// real time and lags well behind a process actually going idle (see
// registry.refineExternalStates). Tty is only needed to find which OS
// terminal window is currently running an attached `herdr` client, for
// canopy's jump package's herdr case.
type ProcessInfo struct {
	Pid     int
	Ppid    int
	Pcpu    float64
	CPUTime time.Duration
	Tty     string
	Comm    string
}

// parsePsCPUTime parses ps's "time"/"cputime" field: on macOS this is
// M+:SS.ss with unbounded minutes (a long-running process reads e.g.
// "1876:29.89", never rolling over into an hours component), but this
// tolerates an optional leading hours component too (H:MM:SS.ss) in case a
// different ps implementation ever formats it that way. The last
// colon-separated part is seconds (with an optional fractional part);
// anything before it is minutes, then hours.
func parsePsCPUTime(s string) (time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, fmt.Errorf("unrecognized ps time format %q", s)
	}
	seconds, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, err
	}
	var hours, minutes int
	switch len(parts) {
	case 2:
		if minutes, err = strconv.Atoi(parts[0]); err != nil {
			return 0, err
		}
	case 3:
		if hours, err = strconv.Atoi(parts[0]); err != nil {
			return 0, err
		}
		if minutes, err = strconv.Atoi(parts[1]); err != nil {
			return 0, err
		}
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds*float64(time.Second)), nil
}

// pySplitN mimics Python's str.split(sep=None, maxsplit=n): runs of
// whitespace are a single delimiter, leading/trailing whitespace around the
// whole string is stripped once, and once maxSplit fields have been peeled
// off the front, whatever remains (including any internal whitespace) is
// kept verbatim as the final element.
func pySplitN(s string, maxSplit int) []string {
	rest := strings.TrimLeft(s, " \t\n\r\f\v")
	var fields []string
	for len(fields) < maxSplit {
		rest = strings.TrimLeft(rest, " \t\n\r\f\v")
		if rest == "" {
			return fields
		}
		idx := strings.IndexAny(rest, " \t\n\r\f\v")
		if idx == -1 {
			fields = append(fields, rest)
			return fields
		}
		fields = append(fields, rest[:idx])
		rest = rest[idx:]
	}
	rest = strings.TrimLeft(rest, " \t\n\r\f\v")
	if rest != "" {
		fields = append(fields, rest)
	}
	return fields
}

// ParseProcessTableOutput is the pure parsing logic for
// `ps -A -o pid=,ppid=,pcpu=,tty=,time=,comm=` output. comm is the full
// executable path on macOS and is always last, so it is parsed greedily:
// paths like `.../Code Helper (Plugin).app/.../Code Helper (Plugin)`
// contain spaces and would otherwise be truncated. comm is what makes
// ancestor-chain surface detection possible: an agent under VS Code's
// integrated terminal has a `Code Helper` (under
// `.../Visual Studio Code.app/...`) a couple of hops up; one under a bare
// Ghostty tab has `ghostty` (under `.../Ghostty.app/...`) instead.
func ParseProcessTableOutput(output string) map[int]ProcessInfo {
	table := map[int]ProcessInfo{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := pySplitN(line, 5)
		if len(parts) < 6 {
			continue
		}
		pidStr, ppidStr, pcpuStr, tty, timeStr, comm := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
		pid, err1 := strconv.Atoi(pidStr)
		ppid, err2 := strconv.Atoi(ppidStr)
		pcpu, err3 := strconv.ParseFloat(pcpuStr, 64)
		cpuTime, err4 := parsePsCPUTime(timeStr)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		table[pid] = ProcessInfo{Pid: pid, Ppid: ppid, Pcpu: pcpu, CPUTime: cpuTime, Tty: tty, Comm: comm}
	}
	return table
}

// ScanProcessTable takes one whole-machine snapshot, reused for both
// ancestry classification and CPU-based state sampling so a poll only shells
// out to `ps` twice total (once filtered for agent kinds, once for
// everything).
func ScanProcessTable() map[int]ProcessInfo {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,pcpu=,tty=,time=,comm=").Output()
	if err != nil {
		return map[int]ProcessInfo{}
	}
	return ParseProcessTableOutput(string(out))
}
