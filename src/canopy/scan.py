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
