"""Classify which app is actually hosting a terminal process, by walking
its parent chain through a whole-machine `ps` snapshot.

This is what makes "jump to the window that has it open" possible without
needing anything from herdr: a bare Ghostty tab's shell is a child of
`ghostty` itself, herdr routes its panes' shells through the headless
`herdr server` process (not through whatever terminal a herdr client
happens to be attached from), and a VS Code integrated terminal's shell
is a child of one of VS Code's `Code Helper` processes. `canopy.jump`
picks the actual jump mechanism (AppleScript, `code -r`, herdr's own
socket focus) from this.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

from canopy.scan import ProcessInfo

MAX_ANCESTOR_HOPS = 12


class Surface(StrEnum):
    HERDR = "herdr"
    VSCODE = "vscode"
    GHOSTTY = "ghostty"
    UNKNOWN = "unknown"


@dataclass(frozen=True)
class AncestorHop:
    pid: int
    comm: str


def ancestor_chain(pid: int, table: dict[int, ProcessInfo]) -> list[AncestorHop]:
    """Walk `pid`'s own entry and then its ancestors up to `pid` 1 or a
    missing/cyclic link, capped at `MAX_ANCESTOR_HOPS` so a corrupt table
    can't spin forever.
    """
    chain: list[AncestorHop] = []
    seen: set[int] = set()
    current = pid
    for _ in range(MAX_ANCESTOR_HOPS):
        info = table.get(current)
        if info is None or current in seen:
            break
        seen.add(current)
        chain.append(AncestorHop(pid=info.pid, comm=info.comm))
        if info.pid == info.ppid or info.ppid <= 1:
            break
        current = info.ppid
    return chain


def classify_surface(pid: int, table: dict[int, ProcessInfo], herdr_tracked: bool) -> Surface:
    """`herdr_tracked` short-circuits the walk: herdr already told us this
    pid is inside one of its own panes (via `herdr pane process-info`),
    which is authoritative and cheaper to trust than re-deriving it from
    `comm` paths, since herdr's own client/server split means a herdr
    pane's ancestor chain does not lead to whatever terminal a herdr
    client happens to be attached from.
    """
    if herdr_tracked:
        return Surface.HERDR

    for hop in ancestor_chain(pid, table):
        comm = hop.comm
        if "Visual Studio Code.app" in comm or "Visual Studio Code - Insiders.app" in comm:
            return Surface.VSCODE
        if "/Ghostty.app/" in comm or comm.endswith("/ghostty"):
            return Surface.GHOSTTY
    return Surface.UNKNOWN
