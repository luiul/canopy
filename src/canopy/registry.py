"""In-memory model of every known-kind agent process on the machine right
now: which app surface is actually hosting it (herdr / VS Code / a bare
Ghostty tab / unknown), and its state (herdr's own idle/working/blocked
/done/unknown for a pane it tracks, a CPU-delta idle/working heuristic
for anything it doesn't).

No file is written here, canopy holds this only for as long as its own
process (the Textual app) is running; there is no background daemon, no
LaunchAgent, and nothing is reported back into herdr. `poll_once` is
meant to be called on a timer from `canopy.cli`.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from canopy import herdr
from canopy.ancestry import Surface, classify_surface
from canopy.scan import ProcessMatch, resolve_cwds, scan_agent_processes, scan_process_table
from canopy.state import classify_state

# How many consecutive missed polls a row survives before being dropped.
# Smooths over a single transient `ps`/herdr hiccup instead of a row
# flickering away and back while someone is about to press Enter on it.
MISS_LIMIT = 1


@dataclass
class RegistryEntry:
    pid: int
    kind: str
    tty: str
    cwd: str | None
    surface: Surface
    state: str
    workspace_id: str | None = None
    tab_id: str | None = None
    pane_id: str | None = None
    misses: int = field(default=0, compare=False)

    @property
    def key(self) -> str:
        return f"{self.pid}:{self.kind}"


def _herdr_entries() -> tuple[set[int], list[RegistryEntry]]:
    """Panes herdr already tracks: build rows straight from herdr's own
    data (its `agent_status` is authoritative there) and the pids to
    exclude from the plain `ps` scan below, so nothing is listed twice.
    """
    tracked_pids: set[int] = set()
    entries: list[RegistryEntry] = []
    for pane in herdr.pane_list():
        agent_kind = pane.get("agent")
        pane_id = pane.get("pane_id")
        if not agent_kind or not pane_id:
            continue
        info = herdr.pane_process_info(pane_id)
        fg_pid = info.get("foreground_process_group_id") if info else None
        if fg_pid is None:
            continue
        tracked_pids.add(fg_pid)
        entries.append(
            RegistryEntry(
                pid=fg_pid,
                kind=agent_kind,
                tty="",
                cwd=pane.get("cwd"),
                surface=Surface.HERDR,
                state=pane.get("agent_status") or "unknown",
                workspace_id=pane.get("workspace_id"),
                tab_id=pane.get("tab_id"),
                pane_id=pane_id,
            )
        )
    return tracked_pids, entries


def _external_entries(matches: list[ProcessMatch], exclude_pids: set[int]) -> list[RegistryEntry]:
    """Every scanned agent process herdr doesn't already track: classify
    which app surface hosts it and estimate idle/working from CPU usage,
    since there's no pty here for canopy to read a real status from.
    """
    candidates = [m for m in matches if m.pid not in exclude_pids]
    if not candidates:
        return []

    table = scan_process_table()
    cwd_by_pid = resolve_cwds([m.pid for m in candidates])

    entries: list[RegistryEntry] = []
    for m in candidates:
        surface = classify_surface(m.pid, table, herdr_tracked=False)
        pcpu = table[m.pid].pcpu if m.pid in table else None
        entries.append(
            RegistryEntry(
                pid=m.pid,
                kind=m.kind,
                tty=m.tty,
                cwd=cwd_by_pid.get(m.pid),
                surface=surface,
                state=classify_state(pcpu).value,
            )
        )
    return entries


def merge_registry(previous: list[RegistryEntry], fresh: list[RegistryEntry]) -> list[RegistryEntry]:
    fresh_by_key = {e.key: e for e in fresh}
    merged: list[RegistryEntry] = []

    for prev in previous:
        if prev.key in fresh_by_key:
            continue  # fresh entry for this key is added below, in fresh's own order
        prev.misses += 1
        if prev.misses <= MISS_LIMIT:
            merged.append(prev)

    merged.extend(fresh)
    return merged


def poll_once(user: str, previous: list[RegistryEntry]) -> list[RegistryEntry]:
    tracked_pids, herdr_rows = _herdr_entries()
    matches = scan_agent_processes(user)
    external_rows = _external_entries(matches, tracked_pids)
    return merge_registry(previous, herdr_rows + external_rows)


def to_jsonable(entries: list[RegistryEntry]) -> list[dict[str, Any]]:
    """Debug/test helper: plain dicts, `Surface` unwrapped to its string
    value so this round-trips through `json.dumps` without a custom
    encoder.
    """
    return [
        {
            "pid": e.pid,
            "kind": e.kind,
            "tty": e.tty,
            "cwd": e.cwd,
            "surface": e.surface.value,
            "state": e.state,
            "workspace_id": e.workspace_id,
            "tab_id": e.tab_id,
            "pane_id": e.pane_id,
        }
        for e in entries
    ]
