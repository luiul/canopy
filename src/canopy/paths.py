"""Where canopy keeps its own state, independent of any repo/workspace."""

from __future__ import annotations

from pathlib import Path

STATE_DIR = Path.home() / ".local" / "state" / "canopy"
REGISTRY_PATH = STATE_DIR / "registry.json"
BADGES_PATH = STATE_DIR / "badges.json"
LOG_PATH = STATE_DIR / "canopy.log"


def ensure_state_dir() -> Path:
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    return STATE_DIR
