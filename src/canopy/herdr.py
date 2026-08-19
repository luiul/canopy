"""Thin subprocess wrapper around the `herdr` binary.

canopy does not reimplement anything herdr already owns: pane discovery,
process introspection, and pane/tab/workspace focus all stay herdr's job.
This module only shells out to `herdr` and parses its JSON output, the
same relationship coppice has with `wt`. canopy never writes anything
back into herdr (no metadata tokens, no config changes); herdr is a data
source and a focus target here, not something canopy extends.
"""

from __future__ import annotations

import json
import shutil
import subprocess
from typing import Any


class HerdrNotFoundError(RuntimeError):
    """The `herdr` binary isn't on PATH."""


class HerdrCommandError(RuntimeError):
    """A `herdr` invocation failed or returned unparseable output."""

    def __init__(self, args: list[str], returncode: int, stderr: str) -> None:
        self.herdr_args = args
        self.returncode = returncode
        self.stderr = stderr
        super().__init__(stderr.strip() or f"herdr {' '.join(args)} exited {returncode}")


def require_herdr() -> str:
    path = shutil.which("herdr")
    if path is None:
        raise HerdrNotFoundError("'herdr' is not installed or not on PATH. See https://herdr.dev")
    return path


def run(args: list[str], check: bool = True, timeout: float = 5.0) -> subprocess.CompletedProcess[str]:
    herdr_bin = require_herdr()
    proc = subprocess.run([herdr_bin, *args], capture_output=True, text=True, timeout=timeout)
    if check and proc.returncode != 0:
        raise HerdrCommandError(args, proc.returncode, proc.stderr)
    return proc


def run_json(args: list[str], check: bool = True, timeout: float = 5.0) -> Any:
    proc = run(args, check=check, timeout=timeout)
    if not proc.stdout.strip():
        return None
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError:
        return None


def pane_list() -> list[dict[str, Any]]:
    """Every pane in the current session: agent kind, agent_status
    (herdr's own idle/working/blocked/done/unknown), cwd, and its
    workspace_id/tab_id/pane_id, all in one call.
    """
    data = run_json(["pane", "list"], check=False)
    if not data:
        return []
    return data.get("result", {}).get("panes", [])


def pane_process_info(pane_id: str) -> dict[str, Any] | None:
    """Shell + foreground process pids/cwds for one pane.

    Returns `.result.process_info` (shell_pid, foreground_process_group_id,
    foreground_processes[]), or None if the pane vanished mid-poll or the
    call failed for any other reason. This is what lets canopy's own `ps`
    scan tell "this pid is already inside a herdr pane" apart from an
    agent running anywhere else, since `pane list` alone doesn't expose
    the underlying OS pid.
    """
    data = run_json(["pane", "process-info", "--pane", pane_id], check=False)
    if not data:
        return None
    return data.get("result", {}).get("process_info")


def focus_workspace(workspace_id: str) -> bool:
    proc = run(["workspace", "focus", workspace_id], check=False)
    return proc.returncode == 0


def focus_tab(tab_id: str) -> bool:
    proc = run(["tab", "focus", tab_id], check=False)
    return proc.returncode == 0


def focus_pane(pane_id: str) -> bool:
    proc = run(["pane", "focus", "--pane", pane_id], check=False)
    return proc.returncode == 0
