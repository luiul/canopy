"""Workspace metadata badges, with immediate clear on a match -> no-match
transition instead of waiting out the TTL.

Kept separate from `registry` so the transition-tracking state (which
workspace currently carries a badge canopy set) has its own small file,
independent of the full process registry.
"""

from __future__ import annotations

import json
from pathlib import Path

from canopy import herdr

SOURCE = "canopy"
TOKEN_KIND = "external_agent"
TOKEN_COUNT = "external_agent_count"


def load_badge_state(path: Path) -> dict[str, str]:
    try:
        return json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return {}


def save_badge_state(path: Path, state: dict[str, str]) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(state, indent=2))
    tmp.replace(path)


def apply_badges(
    matched_by_workspace: dict[str, set[str]],
    previous_state: dict[str, str],
    *,
    ttl_ms: int,
    dry_run: bool,
) -> dict[str, str]:
    """Report a badge for every currently-matched workspace, clear it for
    every workspace that was matched last cycle but isn't anymore, and
    return the new state to persist.
    """
    current_state: dict[str, str] = {}

    for workspace_id, kinds in matched_by_workspace.items():
        value = ",".join(sorted(kinds))
        current_state[workspace_id] = value
        if dry_run:
            continue
        herdr.report_workspace_metadata(
            workspace_id,
            source=SOURCE,
            tokens={TOKEN_KIND: value, TOKEN_COUNT: str(len(kinds))},
            ttl_ms=ttl_ms,
        )

    for workspace_id in previous_state:
        if workspace_id in current_state:
            continue
        if dry_run:
            continue
        herdr.clear_workspace_metadata(workspace_id, source=SOURCE, token_names=[TOKEN_KIND, TOKEN_COUNT])

    return current_state


def clear_all(state: dict[str, str], *, dry_run: bool) -> None:
    """Clear every badge canopy currently holds, e.g. on a clean shutdown
    (`canopy uninstall`, SIGTERM) so nothing lingers past its TTL.
    """
    if dry_run:
        return
    for workspace_id in state:
        herdr.clear_workspace_metadata(workspace_id, source=SOURCE, token_names=[TOKEN_KIND, TOKEN_COUNT])
