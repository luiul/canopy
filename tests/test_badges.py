"""apply_badges/clear_all must report and clear via `canopy.herdr`, never a
raw `subprocess.run`, so these stub `herdr.report_workspace_metadata`/
`clear_workspace_metadata` directly, same pattern coppice's tests use for
`wt`.
"""

from canopy import badges


def _capture(monkeypatch):
    calls: dict[str, list] = {"report": [], "clear": []}
    monkeypatch.setattr(
        badges.herdr,
        "report_workspace_metadata",
        lambda workspace_id, source, tokens, ttl_ms: calls["report"].append((workspace_id, source, tokens, ttl_ms)),
    )
    monkeypatch.setattr(
        badges.herdr,
        "clear_workspace_metadata",
        lambda workspace_id, source, token_names: calls["clear"].append((workspace_id, source, token_names)),
    )
    return calls


def test_apply_badges_reports_a_matched_workspace(monkeypatch):
    calls = _capture(monkeypatch)

    state = badges.apply_badges({"wA": {"pi"}}, {}, ttl_ms=21000, dry_run=False)

    assert state == {"wA": "pi"}
    assert calls["report"] == [("wA", "canopy", {"external_agent": "pi", "external_agent_count": "1"}, 21000)]
    assert calls["clear"] == []


def test_apply_badges_clears_a_workspace_that_is_no_longer_matched(monkeypatch):
    calls = _capture(monkeypatch)

    state = badges.apply_badges({}, {"wA": "pi"}, ttl_ms=21000, dry_run=False)

    assert state == {}
    assert calls["clear"] == [("wA", "canopy", ["external_agent", "external_agent_count"])]


def test_apply_badges_does_not_reclear_a_still_matched_workspace(monkeypatch):
    calls = _capture(monkeypatch)

    badges.apply_badges({"wA": {"pi"}}, {"wA": "pi"}, ttl_ms=21000, dry_run=False)

    assert calls["clear"] == []


def test_apply_badges_dry_run_calls_nothing(monkeypatch):
    calls = _capture(monkeypatch)

    state = badges.apply_badges({"wA": {"pi"}}, {"wB": "codex"}, ttl_ms=21000, dry_run=True)

    assert state == {"wA": "pi"}
    assert calls["report"] == []
    assert calls["clear"] == []


def test_clear_all_clears_every_held_badge(monkeypatch):
    calls = _capture(monkeypatch)

    badges.clear_all({"wA": "pi", "wB": "codex"}, dry_run=False)

    assert sorted(c[0] for c in calls["clear"]) == ["wA", "wB"]


def test_load_and_save_badge_state_roundtrip(tmp_path):
    path = tmp_path / "badges.json"
    badges.save_badge_state(path, {"wA": "pi"})

    assert badges.load_badge_state(path) == {"wA": "pi"}


def test_load_badge_state_missing_file_returns_empty(tmp_path):
    assert badges.load_badge_state(tmp_path / "missing.json") == {}
