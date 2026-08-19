"""Thin `osascript` wrapper for the one thing canopy needs from Ghostty
that nothing else can give it: focusing the specific window/tab running a
given agent, raising it above whatever else is on screen.

Ghostty's own scripting dictionary
(https://github.com/ghostty-org/ghostty/blob/main/macos/Ghostty.sdef)
declares a `tty` property per terminal surface, which would be the exact
match canopy wants (it already resolves every agent's tty via `ps`). In
practice, on the shipped 1.3.1 build, `tty` (and `pid`) reliably fails:
`Can't make tty of terminal id "..." into type specifier`, while string
properties like `working directory` and `name` work fine. So this
matches by working directory instead. That's a weaker key: two tabs `cd`
-ed to the same directory are indistinguishable, canopy just focuses
whichever one Ghostty's script bridge returns first. If tty ever starts
working reliably, this should switch back to matching by tty, which
canopy already resolves for every agent anyway (see `RegistryEntry.tty`).
"""

from __future__ import annotations

import subprocess

_TIMEOUT_S = 6.0


class AutomationError(RuntimeError):
    """Ghostty didn't focus the requested terminal."""


class AutomationPermissionError(AutomationError):
    """macOS Automation permission for scripting Ghostty hasn't been
    granted yet. The first attempt normally pops a system permission
    dialog; if nothing is there to click it (e.g. this is being run
    non-interactively), `osascript` times out or errors instead of
    prompting, which is bubbled up here as this instead of a generic
    error so `canopy.jump` can print something actionable.
    """


def _run_osascript(script: str) -> str:
    try:
        proc = subprocess.run(
            ["osascript", "-e", script],
            capture_output=True,
            text=True,
            timeout=_TIMEOUT_S,
        )
    except subprocess.TimeoutExpired as exc:
        raise AutomationPermissionError(
            "Ghostty didn't respond to AppleScript in time. If this is the first jump, macOS "
            "may be waiting on an Automation permission prompt you haven't seen/answered yet: "
            "System Settings -> Privacy & Security -> Automation, allow your terminal to control Ghostty."
        ) from exc

    if proc.returncode != 0:
        stderr = proc.stderr.strip()
        lowered = stderr.lower()
        if "-1743" in stderr or "not allowed" in lowered or "not authorized" in lowered:
            raise AutomationPermissionError(
                "macOS hasn't granted Automation permission for Ghostty yet. Go to System "
                "Settings -> Privacy & Security -> Automation and allow your terminal to control "
                "Ghostty, then try again."
            )
        if "-1712" in stderr or "timed out" in lowered:
            raise AutomationPermissionError(
                "Ghostty didn't respond to AppleScript in time (same permission prompt as above, "
                "or Ghostty isn't running)."
            )
        raise AutomationError(stderr or f"osascript exited {proc.returncode}")
    return proc.stdout.strip()


def ghostty_focus_by_cwd(cwd: str) -> bool:
    """Focus a Ghostty terminal surface whose working directory is `cwd`,
    bringing its window to the front. Returns False (not an error) if no
    open terminal currently matches, e.g. it already closed, or the
    process has since `cd`-ed elsewhere since canopy last resolved it.
    """
    escaped = cwd.replace("\\", "\\\\").replace('"', '\\"')
    script = f"""
    tell application "Ghostty"
        repeat with t in terminals
            if (working directory of t) is "{escaped}" then
                focus t
                return "true"
            end if
        end repeat
    end tell
    return "false"
    """
    return _run_osascript(script) == "true"


def activate_ghostty() -> bool:
    """Best-effort: just raise Ghostty as an app, without picking a
    specific window. Used as a fallback when there's no reliable way to
    identify *which* Ghostty window matters (e.g. finding the window
    showing a live herdr session, see `canopy.jump._jump_herdr`).
    """
    proc = subprocess.run(["open", "-a", "Ghostty"], capture_output=True, text=True)
    return proc.returncode == 0
