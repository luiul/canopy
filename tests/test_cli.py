"""CLI wiring: the Typer `--version` flag, and a couple of Textual-level
smoke tests using `App.run_test()`'s headless pilot (real terminal
rendering, no subprocess calls, `poll_once`/`jump_to` are monkeypatched
at the `canopy.cli` module boundary so nothing here touches ps/lsof/herdr).
"""

import asyncio

from typer.testing import CliRunner

from canopy import cli
from canopy.ancestry import Surface
from canopy.cli import CanopyApp, app
from canopy.registry import RegistryEntry

runner = CliRunner()


def _entry(pid: int = 1, surface: Surface = Surface.GHOSTTY, state: str = "working") -> RegistryEntry:
    return RegistryEntry(pid=pid, kind="pi", tty="s017", cwd="/Users/x/dotfiles", surface=surface, state=state)


def test_version_flag_prints_something_and_exits_cleanly():
    result = runner.invoke(app, ["--version"])

    assert result.exit_code == 0
    assert "canopy" in result.output


def test_dashboard_renders_a_row_per_entry(monkeypatch):
    monkeypatch.setattr(cli, "poll_once", lambda user, previous: [_entry(1), _entry(2, surface=Surface.VSCODE)])

    async def _run():
        dashboard = CanopyApp(interval=999)  # keep the timer from firing mid-test
        async with dashboard.run_test() as pilot:
            await pilot.pause()
            table = dashboard.query_one("DataTable")
            return table.row_count

    assert asyncio.run(_run()) == 2


def test_dashboard_shows_a_placeholder_row_when_nothing_is_found(monkeypatch):
    monkeypatch.setattr(cli, "poll_once", lambda user, previous: [])

    async def _run():
        dashboard = CanopyApp(interval=999)
        async with dashboard.run_test() as pilot:
            await pilot.pause()
            table = dashboard.query_one("DataTable")
            return table.row_count

    assert asyncio.run(_run()) == 1


def test_enter_on_a_row_calls_jump_to_with_the_selected_entry(monkeypatch):
    target = _entry(pid=42, surface=Surface.GHOSTTY)
    monkeypatch.setattr(cli, "poll_once", lambda user, previous: [target])

    calls = []
    monkeypatch.setattr(cli.jump, "jump_to", lambda entry: calls.append(entry) or cli.jump.JumpResult(True, "ok"))

    async def _run():
        dashboard = CanopyApp(interval=999)
        async with dashboard.run_test() as pilot:
            await pilot.pause()
            dashboard.query_one("DataTable").focus()
            await pilot.press("enter")
            await pilot.pause()

    asyncio.run(_run())

    assert calls == [target]
