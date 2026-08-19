"""herdr is canopy's hard prerequisite, same relationship coppice has with
wt. `watch` must fail with a clear, actionable message when it's missing
from PATH, never a raw traceback.
"""

from pathlib import Path

from typer.testing import CliRunner

from canopy import herdr, paths, registry
from canopy.cli import app

runner = CliRunner()


def _hide_herdr(monkeypatch):
    monkeypatch.setattr(herdr.shutil, "which", lambda name: None)


def test_watch_without_herdr_fails_clearly(monkeypatch):
    _hide_herdr(monkeypatch)

    result = runner.invoke(app, ["watch"])

    assert result.exit_code != 0
    assert "herdr" in result.output
    assert "herdr.dev" in result.output
    assert "Traceback" not in result.output


def test_version_flag_prints_something_and_exits_cleanly():
    result = runner.invoke(app, ["--version"])

    assert result.exit_code == 0
    assert "canopy" in result.output


def test_status_without_a_registry_says_so(monkeypatch, tmp_path):
    monkeypatch.setattr(paths, "REGISTRY_PATH", tmp_path / "registry.json")

    result = runner.invoke(app, ["status"])

    assert result.exit_code == 0
    assert "canopy watch" in result.output


def test_status_renders_entries_from_the_registry(monkeypatch, tmp_path):
    registry_path = tmp_path / "registry.json"
    monkeypatch.setattr(paths, "REGISTRY_PATH", registry_path)
    entries = [
        registry.RegistryEntry(pid=1, kind="pi", tty="ttys000", cwd="/x", tracked=False, workspace_ids=["wA"]),
    ]
    registry.save_registry(registry_path, entries, poll_interval_s=7.0, dry_run=False)

    result = runner.invoke(app, ["status"])

    assert result.exit_code == 0
    assert "pi" in result.output
    assert "wA" in result.output


def test_uninstall_without_an_install_says_so(monkeypatch, tmp_path):
    monkeypatch.setattr(paths, "BADGES_PATH", tmp_path / "badges.json")
    monkeypatch.setattr("canopy.launchd.PLIST_PATH", Path(tmp_path / "does-not-exist.plist"))

    result = runner.invoke(app, ["uninstall"])

    assert result.exit_code == 0
    assert "No LaunchAgent was installed." in result.output
