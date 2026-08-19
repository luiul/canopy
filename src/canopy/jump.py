"""Bring whichever window is actually running a given agent process to the
front: herdr's own socket focus for a pane it tracks, `code --reuse-window`
for a VS Code integrated terminal, Ghostty's own AppleScript for a bare
Ghostty tab. Nothing to jump to for `Surface.UNKNOWN`, canopy doesn't
know what's hosting it.
"""

from __future__ import annotations

import shutil
import subprocess
from dataclasses import dataclass

from canopy import applescript, herdr
from canopy.ancestry import Surface
from canopy.registry import RegistryEntry


@dataclass(frozen=True)
class JumpResult:
    ok: bool
    message: str


def _jump_herdr(entry: RegistryEntry) -> JumpResult:
    focused_any = False
    if entry.workspace_id:
        focused_any = herdr.focus_workspace(entry.workspace_id) or focused_any
    if entry.tab_id:
        focused_any = herdr.focus_tab(entry.tab_id) or focused_any
    if entry.pane_id:
        focused_any = herdr.focus_pane(entry.pane_id) or focused_any
    if not focused_any:
        return JumpResult(False, "herdr didn't accept the focus request (pane may be gone).")

    # herdr is a client/server pair: focusing above only changes what an
    # *already attached* herdr client renders. There's no reliable way
    # from out here to tell which of possibly several open Ghostty
    # windows is currently showing that client (see module docstring:
    # Ghostty's own `tty`/`pid` scripting properties don't work on the
    # installed build), so this can only raise Ghostty in general, not
    # the specific window.
    try:
        applescript.activate_ghostty()
    except applescript.AutomationError as exc:
        return JumpResult(True, f"Focused in herdr, but couldn't raise Ghostty: {exc}")
    return JumpResult(True, "Focused in herdr and raised Ghostty (switch tabs/windows there if needed).")


def _jump_vscode(entry: RegistryEntry) -> JumpResult:
    if not entry.cwd:
        return JumpResult(False, "No known working directory to open.")

    code_bin = shutil.which("code")
    if code_bin:
        proc = subprocess.run([code_bin, "--reuse-window", entry.cwd], capture_output=True, text=True)
        if proc.returncode == 0:
            return JumpResult(True, f"Focused VS Code window for {entry.cwd}.")

    # Fall back to just raising the app if the `code` shell command isn't
    # installed; this can't target the right *window*, only the app.
    proc = subprocess.run(["open", "-a", "Visual Studio Code", entry.cwd], capture_output=True, text=True)
    if proc.returncode == 0:
        return JumpResult(True, f"Opened {entry.cwd} in VS Code (install the 'code' CLI for exact-window focus).")
    return JumpResult(False, (proc.stderr or "Couldn't open VS Code.").strip())


def _jump_ghostty(entry: RegistryEntry) -> JumpResult:
    if not entry.cwd:
        return JumpResult(False, "No known working directory to focus.")
    try:
        found = applescript.ghostty_focus_by_cwd(entry.cwd)
    except applescript.AutomationError as exc:
        return JumpResult(False, str(exc))
    if found:
        return JumpResult(True, "Focused in Ghostty.")
    return JumpResult(False, "No open Ghostty terminal matches that working directory anymore.")


def jump_to(entry: RegistryEntry) -> JumpResult:
    if entry.surface == Surface.HERDR:
        return _jump_herdr(entry)
    if entry.surface == Surface.VSCODE:
        return _jump_vscode(entry)
    if entry.surface == Surface.GHOSTTY:
        return _jump_ghostty(entry)
    return JumpResult(False, "Don't know how to jump to this process's window yet.")
