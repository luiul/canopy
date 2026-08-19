"""Scan the current user's processes for herdr's known agent kinds.

herdr recognizes a fixed set of agent CLIs (`herdr agent start --help`
lists them). canopy uses the same list: an agent process herdr doesn't
know how to detect anyway isn't in scope for "an agent herdr can't see".
"""

from __future__ import annotations

import re
import subprocess
from dataclasses import dataclass

# Kept in sync with `herdr agent start --help`'s `--kind` values.
KNOWN_KINDS = frozenset(
    {
        "pi",
        "claude",
        "codex",
        "gemini",
        "cursor",
        "devin",
        "agy",
        "cline",
        "omp",
        "mastracode",
        "opencode",
        "copilot",
        "kimi",
        "kiro",
        "droid",
        "amp",
        "grok",
        "hermes",
        "kilo",
        "qodercli",
        "maki",
    }
)

# Second-argv-token denylist: filters out subcommand/flag invocations that
# share a kind's executable name but aren't the interactive agent itself
# (e.g. `codex mcp`, `claude --version`).
SECOND_TOKEN_DENYLIST = frozenset({"mcp", "mcp-server", "serve", "server", "--version", "-v", "-V", "--help", "-h"})

_PS_LINE = re.compile(r"^(\d+)\s+(\S+)\s+(.*)$")


@dataclass(frozen=True)
class ProcessMatch:
    pid: int
    tty: str
    kind: str
    args: str


def parse_ps_output(output: str) -> list[ProcessMatch]:
    """Pure parsing/filtering logic for `ps -o pid=,tty=,args=` output,
    split out from `scan_agent_processes` so it's testable without a real
    process table.
    """
    matches: list[ProcessMatch] = []
    for raw_line in output.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        m = _PS_LINE.match(line)
        if not m:
            continue
        pid_str, tty, args = m.groups()
        if tty in ("??", "?"):
            continue  # no controlling terminal: not an interactive agent
        tokens = args.split()
        if not tokens:
            continue
        argv0 = tokens[0]
        kind = argv0.rsplit("/", 1)[-1]
        if kind not in KNOWN_KINDS:
            continue
        if len(tokens) > 1 and tokens[1] in SECOND_TOKEN_DENYLIST:
            continue
        matches.append(ProcessMatch(pid=int(pid_str), tty=tty, kind=kind, args=args))
    return matches


def scan_agent_processes(user: str) -> list[ProcessMatch]:
    proc = subprocess.run(
        ["ps", "-u", user, "-o", "pid=,tty=,args="],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        return []
    return parse_ps_output(proc.stdout)


def parse_lsof_cwd_output(output: str) -> dict[int, str]:
    """Pure parsing logic for `lsof -a -d cwd -Fn` output (`p<pid>` then
    `n<path>` pairs).
    """
    cwd_by_pid: dict[int, str] = {}
    current_pid: int | None = None
    for line in output.splitlines():
        if line.startswith("p"):
            current_pid = int(line[1:])
        elif line.startswith("n") and current_pid is not None:
            cwd_by_pid[current_pid] = line[1:]
    return cwd_by_pid


def resolve_cwds(pids: list[int]) -> dict[int, str]:
    if not pids:
        return {}
    proc = subprocess.run(
        ["lsof", "-a", "-p", ",".join(str(p) for p in pids), "-d", "cwd", "-Fn"],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        return {}
    return parse_lsof_cwd_output(proc.stdout)


@dataclass(frozen=True)
class ProcessInfo:
    """One row of the whole-machine process table: enough to walk an
    ancestor chain (`ppid`, `comm`) and to sample CPU usage (`pcpu`) for
    the idle/working heuristic used on processes herdr doesn't track.
    `tty` is only needed to find which OS terminal window is currently
    running an attached `herdr` client, for `canopy.jump`'s herdr case.
    """

    pid: int
    ppid: int
    pcpu: float
    tty: str
    comm: str


def parse_process_table_output(output: str) -> dict[int, ProcessInfo]:
    """Pure parsing logic for `ps -A -o pid=,ppid=,pcpu=,tty=,comm=`
    output. `comm` is the full executable path on macOS and is always
    last, so it is parsed greedily: paths like `.../Code Helper
    (Plugin).app/.../Code Helper (Plugin)` contain spaces and would
    otherwise be truncated. `comm` is what makes ancestor-chain surface
    detection (`canopy.ancestry`) possible: an agent under VS Code's
    integrated terminal has a `Code Helper` (under `.../Visual Studio
    Code.app/...`) a couple of hops up; one under a bare Ghostty tab has
    `ghostty` (under `.../Ghostty.app/...`) instead.
    """
    table: dict[int, ProcessInfo] = {}
    for raw_line in output.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        parts = line.split(None, 4)
        if len(parts) < 5:
            continue
        pid_str, ppid_str, pcpu_str, tty, comm = parts
        try:
            pid, ppid, pcpu = int(pid_str), int(ppid_str), float(pcpu_str)
        except ValueError:
            continue
        table[pid] = ProcessInfo(pid=pid, ppid=ppid, pcpu=pcpu, tty=tty, comm=comm)
    return table


def scan_process_table() -> dict[int, ProcessInfo]:
    """One whole-machine snapshot, reused for both ancestry classification
    and CPU-based state sampling so a poll only shells out to `ps` twice
    total (once filtered for agent kinds, once for everything).
    """
    proc = subprocess.run(
        ["ps", "-A", "-o", "pid=,ppid=,pcpu=,tty=,comm="],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        return {}
    return parse_process_table_output(proc.stdout)
