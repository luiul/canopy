"""jump_to must never touch the real OS: every external call (herdr,
osascript, `code`, `open`) is monkeypatched at the module boundary, same
pattern coppice's tests use for `wt`.
"""

from canopy import applescript, herdr, jump
from canopy.ancestry import Surface
from canopy.registry import RegistryEntry


def _entry(surface: Surface, **kwargs) -> RegistryEntry:
    defaults = dict(pid=1, kind="pi", tty="s017", cwd="/Users/x/dotfiles", surface=surface, state="working")
    defaults.update(kwargs)
    return RegistryEntry(**defaults)


def test_jump_to_herdr_focuses_workspace_tab_and_pane(monkeypatch):
    calls = []
    monkeypatch.setattr(herdr, "focus_workspace", lambda wid: calls.append(("workspace", wid)) or True)
    monkeypatch.setattr(herdr, "focus_tab", lambda tid: calls.append(("tab", tid)) or True)
    monkeypatch.setattr(herdr, "focus_pane", lambda pid: calls.append(("pane", pid)) or True)
    monkeypatch.setattr(applescript, "activate_ghostty", lambda: True)

    entry = _entry(Surface.HERDR, workspace_id="wG", tab_id="wG:t1", pane_id="wG:p1")
    result = jump.jump_to(entry)

    assert result.ok
    assert calls == [("workspace", "wG"), ("tab", "wG:t1"), ("pane", "wG:p1")]


def test_jump_to_herdr_reports_failure_when_herdr_rejects_every_focus_call(monkeypatch):
    monkeypatch.setattr(herdr, "focus_workspace", lambda wid: False)
    monkeypatch.setattr(herdr, "focus_tab", lambda tid: False)
    monkeypatch.setattr(herdr, "focus_pane", lambda pid: False)

    entry = _entry(Surface.HERDR, workspace_id="wG", tab_id="wG:t1", pane_id="wG:p1")
    result = jump.jump_to(entry)

    assert not result.ok


def test_jump_to_herdr_raises_ghostty_as_a_fallback_since_it_cant_pick_a_specific_window(monkeypatch):
    monkeypatch.setattr(herdr, "focus_workspace", lambda wid: True)
    monkeypatch.setattr(herdr, "focus_tab", lambda tid: True)
    monkeypatch.setattr(herdr, "focus_pane", lambda pid: True)
    calls = []
    monkeypatch.setattr(applescript, "activate_ghostty", lambda: calls.append("activate") or True)

    jump.jump_to(_entry(Surface.HERDR, workspace_id="wG", tab_id="wG:t1", pane_id="wG:p1"))

    assert calls == ["activate"]


def test_jump_to_vscode_uses_the_code_cli_when_available(monkeypatch):
    calls = []
    monkeypatch.setattr(jump.shutil, "which", lambda name: "/usr/local/bin/code")

    class FakeProc:
        returncode = 0

    monkeypatch.setattr(jump.subprocess, "run", lambda args, **kw: calls.append(args) or FakeProc())

    result = jump.jump_to(_entry(Surface.VSCODE, cwd="/Users/x/dotfiles"))

    assert result.ok
    assert calls == [["/usr/local/bin/code", "--reuse-window", "/Users/x/dotfiles"]]


def test_jump_to_vscode_falls_back_to_open_when_code_cli_missing(monkeypatch):
    calls = []
    monkeypatch.setattr(jump.shutil, "which", lambda name: None)

    class FakeProc:
        returncode = 0
        stderr = ""

    monkeypatch.setattr(jump.subprocess, "run", lambda args, **kw: calls.append(args) or FakeProc())

    result = jump.jump_to(_entry(Surface.VSCODE, cwd="/Users/x/dotfiles"))

    assert result.ok
    assert calls == [["open", "-a", "Visual Studio Code", "/Users/x/dotfiles"]]


def test_jump_to_vscode_without_a_cwd_fails_clearly():
    result = jump.jump_to(_entry(Surface.VSCODE, cwd=None))
    assert not result.ok
    assert "working directory" in result.message


def test_jump_to_ghostty_focuses_by_cwd(monkeypatch):
    calls = []
    monkeypatch.setattr(applescript, "ghostty_focus_by_cwd", lambda cwd: calls.append(cwd) or True)

    result = jump.jump_to(_entry(Surface.GHOSTTY, cwd="/Users/x/dotfiles"))

    assert result.ok
    assert calls == ["/Users/x/dotfiles"]


def test_jump_to_ghostty_without_a_cwd_fails_clearly():
    result = jump.jump_to(_entry(Surface.GHOSTTY, cwd=None))
    assert not result.ok


def test_jump_to_ghostty_reports_when_no_terminal_matches(monkeypatch):
    monkeypatch.setattr(applescript, "ghostty_focus_by_cwd", lambda cwd: False)

    result = jump.jump_to(_entry(Surface.GHOSTTY, cwd="/Users/x/dotfiles"))

    assert not result.ok


def test_jump_to_ghostty_surfaces_automation_permission_errors(monkeypatch):
    def _raise(cwd):
        raise applescript.AutomationPermissionError("grant Automation permission")

    monkeypatch.setattr(applescript, "ghostty_focus_by_cwd", _raise)

    result = jump.jump_to(_entry(Surface.GHOSTTY, cwd="/Users/x/dotfiles"))

    assert not result.ok
    assert "Automation permission" in result.message


def test_jump_to_unknown_surface_says_so():
    result = jump.jump_to(_entry(Surface.UNKNOWN))
    assert not result.ok
