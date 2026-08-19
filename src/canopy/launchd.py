"""Install/uninstall a macOS LaunchAgent so `canopy watch` actually gets
restarted on crash, RunAtLoad on login/reboot, and stopped cleanly on
uninstall.

This is the piece herdr's plugin `[[startup]]` hook could not give: herdr's
own docs are explicit that startup hooks are one-shot, not supervised
daemons, with no stop hook either. launchd's `KeepAlive` does what "run
this forever, restart it if it dies" actually needs.
"""

from __future__ import annotations

import shutil
import subprocess
from pathlib import Path

from canopy import herdr

LABEL = "com.luiul.canopy"
PLIST_PATH = Path.home() / "Library" / "LaunchAgents" / f"{LABEL}.plist"

_PLIST_TEMPLATE = """<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{label}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{canopy_path}</string>
        <string>watch</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>{herdr_bin_env}</key>
        <string>{herdr_path}</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{log_path}</string>
    <key>StandardErrorPath</key>
    <string>{log_path}</string>
</dict>
</plist>
"""


class CanopyNotFoundError(RuntimeError):
    """The `canopy` binary isn't on PATH (needed so the plist can point at it)."""


class HerdrNotFoundForInstallError(RuntimeError):
    """`herdr` isn't on PATH at install time, so its path can't be baked
    into the LaunchAgent's environment (see `canopy.herdr._herdr_bin` for
    why launchd's own minimal PATH can't be relied on instead).
    """


class LaunchctlError(RuntimeError):
    def __init__(self, args: list[str], returncode: int, stderr: str) -> None:
        self.args_ = args
        self.returncode = returncode
        self.stderr = stderr
        super().__init__(stderr.strip() or f"launchctl {' '.join(args)} exited {returncode}")


def _uid() -> str:
    import os

    return str(os.getuid())


def _launchctl(args: list[str], check: bool = True) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(["launchctl", *args], capture_output=True, text=True)
    if check and proc.returncode != 0:
        raise LaunchctlError(args, proc.returncode, proc.stderr)
    return proc


def render_plist(canopy_path: str, herdr_path: str, log_path: Path) -> str:
    return _PLIST_TEMPLATE.format(
        label=LABEL,
        canopy_path=canopy_path,
        herdr_bin_env=herdr.HERDR_BIN_ENV,
        herdr_path=herdr_path,
        log_path=str(log_path),
    )


def install(log_path: Path) -> Path:
    canopy_path = shutil.which("canopy")
    if canopy_path is None:
        raise CanopyNotFoundError("'canopy' isn't on PATH; install it first (e.g. `uv tool install .`).")
    herdr_path = shutil.which("herdr")
    if herdr_path is None:
        raise HerdrNotFoundForInstallError("'herdr' is not installed or not on PATH. See https://herdr.dev")

    PLIST_PATH.parent.mkdir(parents=True, exist_ok=True)
    PLIST_PATH.write_text(render_plist(canopy_path, herdr_path, log_path))

    # bootout is a no-op (best-effort) if it wasn't loaded before, so a
    # reinstall over a running instance doesn't need a separate check.
    _launchctl(["bootout", f"gui/{_uid()}/{LABEL}"], check=False)
    _launchctl(["bootstrap", f"gui/{_uid()}", str(PLIST_PATH)])
    return PLIST_PATH


def uninstall() -> bool:
    """Stop and remove the LaunchAgent. Returns False if it wasn't installed."""
    if not PLIST_PATH.exists():
        return False
    _launchctl(["bootout", f"gui/{_uid()}/{LABEL}"], check=False)
    PLIST_PATH.unlink(missing_ok=True)
    return True


def is_installed() -> bool:
    return PLIST_PATH.exists()
