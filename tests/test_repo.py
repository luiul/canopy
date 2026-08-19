"""repo_identity must resolve a main checkout and one of its own linked
worktrees to the *same* identity (both share one .git), and must not raise
on a path that isn't a git repository at all.
"""

import subprocess
from pathlib import Path

from canopy.repo import repo_identity


def _run(args: list[str], cwd: Path) -> None:
    subprocess.run(args, cwd=cwd, check=True, capture_output=True)


def _init_repo(path: Path) -> Path:
    path.mkdir(parents=True, exist_ok=True)
    _run(["git", "init", "-q", "-b", "main"], path)
    _run(["git", "config", "user.email", "test@example.com"], path)
    _run(["git", "config", "user.name", "Test"], path)
    _run(["git", "commit", "--allow-empty", "-q", "-m", "init"], path)
    return path


def test_repo_identity_for_plain_checkout(tmp_path):
    repo_dir = _init_repo(tmp_path / "repo")
    assert repo_identity(repo_dir) == repo_dir.resolve()


def test_repo_identity_matches_across_a_linked_worktree(tmp_path):
    repo_dir = _init_repo(tmp_path / "repo")
    worktree_dir = tmp_path / "worktrees" / "repo-feature"
    worktree_dir.parent.mkdir(parents=True, exist_ok=True)
    _run(["git", "worktree", "add", "-q", "-b", "feature", str(worktree_dir)], repo_dir)

    assert repo_identity(worktree_dir) == repo_identity(repo_dir)


def test_repo_identity_falls_back_to_resolved_path_outside_a_repo(tmp_path):
    plain_dir = tmp_path / "not-a-repo"
    plain_dir.mkdir()
    assert repo_identity(plain_dir) == plain_dir.resolve()


def test_repo_identity_from_a_subdirectory_matches_the_repo_root(tmp_path):
    repo_dir = _init_repo(tmp_path / "repo")
    subdir = repo_dir / "src" / "pkg"
    subdir.mkdir(parents=True)
    assert repo_identity(subdir) == repo_dir.resolve()
