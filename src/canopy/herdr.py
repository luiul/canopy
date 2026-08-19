"""Thin subprocess wrapper around the `herdr` binary.

`canopy` does not reimplement anything herdr already owns: pane discovery,
process introspection, and workspace metadata all stay `herdr`'s job. This
module only shells out to `herdr` and parses its JSON output; every side
effect (writing a workspace metadata token, reading pane process info) is
`herdr`'s own API, called over its CLI rather than its raw socket, so this
works the same on every platform herdr runs on.

Note: `herdr plugin list` is the one herdr subcommand that prints
human-readable text by default; every other command used here already
returns JSON without a flag. Not needed here (canopy has no plugin
lifecycle to check), but worth remembering if this module grows.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from typing import Any

HERDR_BIN_ENV = "CANOPY_HERDR_PATH"


class HerdrNotFoundError(RuntimeError):
    """The `herdr` binary isn't on PATH."""


class HerdrCommandError(RuntimeError):
    """A `herdr` invocation failed or returned unparseable output."""

    def __init__(self, args: list[str], returncode: int, stderr: str) -> None:
        self.herdr_args = args
        self.returncode = returncode
        self.stderr = stderr
        super().__init__(stderr.strip() or f"herdr {' '.join(args)} exited {returncode}")


def _herdr_bin() -> str | None:
    """Resolve which `herdr` binary to run.

    Checks `CANOPY_HERDR_PATH` first: launchd runs `canopy watch` with a
    minimal default PATH (`/usr/bin:/bin:/usr/sbin:/sbin`), which does not
    include wherever `herdr` actually lives (`~/.local/bin` here). `canopy
    install` resolves herdr's absolute path once and bakes it into the
    LaunchAgent's plist as this env var, so the watcher doesn't depend on
    launchd's PATH at all. An interactive shell's normal PATH still works
    for `canopy status`/`canopy watch` run by hand.
    """
    return os.environ.get(HERDR_BIN_ENV) or shutil.which("herdr")


def require_herdr() -> str:
    path = _herdr_bin()
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
    """Every pane in the current session, with its cwd and workspace_id."""
    data = run_json(["pane", "list"], check=False)
    if not data:
        return []
    return data.get("result", {}).get("panes", [])


def pane_process_info(pane_id: str) -> dict[str, Any] | None:
    """Shell + foreground process pids/cwds for one pane.

    Returns `.result.process_info` (shell_pid, foreground_process_group_id,
    foreground_processes[]), or None if the pane vanished mid-poll or the
    call failed for any other reason.
    """
    data = run_json(["pane", "process-info", "--pane", pane_id], check=False)
    if not data:
        return None
    return data.get("result", {}).get("process_info")


def report_workspace_metadata(workspace_id: str, source: str, tokens: dict[str, str], ttl_ms: int) -> None:
    """Set display-only metadata tokens on a workspace, TTL-limited.

    Self-expires on its own if canopy stops refreshing it (crash, `canopy
    uninstall`, LaunchAgent unloaded), so a missed cycle never leaves a
    stale badge behind indefinitely even without an explicit clear.
    """
    args = ["workspace", "report-metadata", workspace_id, "--source", source, "--ttl-ms", str(ttl_ms)]
    for key, value in tokens.items():
        args += ["--token", f"{key}={value}"]
    run(args, check=False)


def clear_workspace_metadata(workspace_id: str, source: str, token_names: list[str]) -> None:
    """Clear metadata tokens immediately, instead of waiting out their TTL.

    Used on a match -> no-match transition, so the badge disappears right
    away, and on `canopy uninstall`/shutdown, so nothing stale lingers.
    """
    args = ["workspace", "report-metadata", workspace_id, "--source", source]
    for name in token_names:
        args += ["--clear-token", name]
    run(args, check=False)
