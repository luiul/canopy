"""Git-worktree-aware repo identity, for matching an agent's cwd to a herdr
workspace even when the two paths differ (a checkout under ~/projects vs a
linked worktree of the same repo under ~/worktrees, for example).

Adapted from coppice's `repo.resolve_repo_root`: same git-common-dir logic,
same bare-repo nuance, but non-fatal on a path that isn't a git repo at all
(canopy's callers want a stable identity for *any* cwd, herdr workspaces and
plain shells included, not just registered git repos).
"""

from __future__ import annotations

import subprocess
from pathlib import Path


def repo_identity(path: str | Path) -> Path:
    """A stable identity for PATH: its repo's git-common-dir when PATH is
    inside a git repository (main checkout or linked worktree alike), or
    PATH itself, resolved, otherwise.

    Uses git-common-dir rather than `rev-parse --show-toplevel` so a linked
    worktree resolves to the *same* identity as the main checkout: both
    share one .git, so both should match the same herdr workspace.
    """
    target = Path(path).expanduser()

    proc = subprocess.run(
        ["git", "-C", str(target), "rev-parse", "--path-format=absolute", "--git-common-dir"],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        return target.resolve()

    common_dir = Path(proc.stdout.strip())

    # A *bare* repo's git-common-dir already points at the repo root itself,
    # not a `.git` subdirectory inside it, even resolved from one of its
    # linked worktrees. Taking `.parent` unconditionally would walk up to an
    # unrelated parent directory in that case, so only do it when the repo
    # isn't bare.
    bare_proc = subprocess.run(
        ["git", "-C", str(common_dir), "rev-parse", "--is-bare-repository"],
        capture_output=True,
        text=True,
    )
    is_bare = bare_proc.returncode == 0 and bare_proc.stdout.strip() == "true"
    identity = common_dir if is_bare else common_dir.parent
    return identity.resolve()
