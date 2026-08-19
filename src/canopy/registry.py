"""Build and persist the agent registry: every known-kind process, split
into tracked (already visible in herdr's own Agents section) vs external
(the gap canopy exists to surface), matched to herdr workspaces by
git-worktree-aware repo identity, with a debounce window so one bad `ps`/
`lsof` snapshot doesn't flicker an entry in and out.
"""

from __future__ import annotations

import json
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

from canopy import herdr, repo, scan

MISS_LIMIT = 2  # consecutive misses before an entry is dropped


@dataclass
class RegistryEntry:
    pid: int
    kind: str
    tty: str
    cwd: str | None
    tracked: bool
    workspace_ids: list[str] = field(default_factory=list)
    first_seen: float = 0.0
    last_seen: float = 0.0
    misses: int = 0
    stale: bool = False

    @property
    def key(self) -> str:
        return f"{self.pid}:{self.kind}"


def herdr_tracked_pids() -> tuple[set[int], list[dict[str, Any]]]:
    """Pids herdr already has under management (visible in its Agents
    section today), plus the raw pane list, so callers don't have to fetch
    it twice.
    """
    panes = herdr.pane_list()
    tracked: set[int] = set()
    for pane in panes:
        info = herdr.pane_process_info(pane["pane_id"])
        if not info:
            continue
        shell_pid = info.get("shell_pid")
        if shell_pid:
            tracked.add(shell_pid)
        for proc in info.get("foreground_processes", []):
            if proc.get("pid"):
                tracked.add(proc["pid"])
    return tracked, panes


def build_workspace_identity_map(panes: list[dict[str, Any]]) -> dict[Path, set[str]]:
    """identity -> set(workspace_id), from every pane's own cwd."""
    identity_map: dict[Path, set[str]] = {}
    for pane in panes:
        cwd = pane.get("cwd")
        workspace_id = pane.get("workspace_id")
        if not cwd or not workspace_id:
            continue
        identity = repo.repo_identity(cwd)
        identity_map.setdefault(identity, set()).add(workspace_id)
    return identity_map


def merge_registry(previous: list[RegistryEntry], fresh: list[RegistryEntry]) -> list[RegistryEntry]:
    """Debounced merge: an entry missing from `fresh` survives up to
    MISS_LIMIT - 1 extra polls (marked stale) before it's dropped, so a
    single missed `ps`/`lsof` read doesn't flicker it out of the registry
    or the workspace badge it may be backing.
    """
    now = time.time()
    fresh_by_key = {e.key: e for e in fresh}
    merged: dict[str, RegistryEntry] = {}

    for prev in previous:
        current = fresh_by_key.pop(prev.key, None)
        if current is not None:
            current.first_seen = prev.first_seen
            current.misses = 0
            merged[prev.key] = current
        else:
            misses = prev.misses + 1
            if misses < MISS_LIMIT:
                prev.misses = misses
                prev.stale = True
                merged[prev.key] = prev
            # else: dropped, exceeded the debounce window

    for key, entry in fresh_by_key.items():
        entry.first_seen = now
        entry.misses = 0
        merged[key] = entry

    return list(merged.values())


def poll_once(user: str, previous: list[RegistryEntry]) -> list[RegistryEntry]:
    """One full scan-match-debounce cycle. Pure aside from the subprocess
    calls inside `scan`/`herdr`/`repo`.
    """
    now = time.time()
    matches = scan.scan_agent_processes(user)
    cwd_by_pid = scan.resolve_cwds([m.pid for m in matches])
    tracked_pids, panes = herdr_tracked_pids()
    identity_map = build_workspace_identity_map(panes)

    fresh: list[RegistryEntry] = []
    for m in matches:
        cwd = cwd_by_pid.get(m.pid)
        is_tracked = m.pid in tracked_pids
        workspace_ids: list[str] = []
        if not is_tracked and cwd:
            identity = repo.repo_identity(cwd)
            workspace_ids = sorted(identity_map.get(identity, set()))
        fresh.append(
            RegistryEntry(
                pid=m.pid,
                kind=m.kind,
                tty=m.tty,
                cwd=cwd,
                tracked=is_tracked,
                workspace_ids=workspace_ids,
                last_seen=now,
            )
        )

    return merge_registry(previous, fresh)


def matched_by_workspace(entries: list[RegistryEntry]) -> dict[str, set[str]]:
    """workspace_id -> set(kind), for entries that are external, currently
    seen (not stale), and matched to at least one workspace. Only these
    are worth a badge; tracked agents are already visible natively, and a
    stale entry shouldn't refresh a badge it may be about to lose.
    """
    result: dict[str, set[str]] = {}
    for entry in entries:
        if entry.tracked or entry.stale:
            continue
        for workspace_id in entry.workspace_ids:
            result.setdefault(workspace_id, set()).add(entry.kind)
    return result


def load_registry(path: Path) -> list[RegistryEntry]:
    try:
        raw = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return []
    return [RegistryEntry(**e) for e in raw.get("entries", [])]


def save_registry(path: Path, entries: list[RegistryEntry], *, poll_interval_s: float, dry_run: bool) -> None:
    payload = {
        "generated_at": time.time(),
        "poll_interval_s": poll_interval_s,
        "dry_run": dry_run,
        "entries": [asdict(e) for e in entries],
    }
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(payload, indent=2))
    tmp.replace(path)
