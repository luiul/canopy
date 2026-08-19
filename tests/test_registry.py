from canopy.registry import RegistryEntry, matched_by_workspace, merge_registry


def _entry(pid: int, kind: str = "pi", tracked: bool = False, workspace_ids: list[str] | None = None) -> RegistryEntry:
    return RegistryEntry(
        pid=pid, kind=kind, tty="ttys000", cwd="/x", tracked=tracked, workspace_ids=workspace_ids or []
    )


def test_merge_registry_keeps_a_still_present_entry_and_resets_misses():
    previous = [_entry(1)]
    previous[0].misses = 1
    fresh = [_entry(1)]

    merged = merge_registry(previous, fresh)

    assert len(merged) == 1
    assert merged[0].misses == 0
    assert merged[0].stale is False


def test_merge_registry_marks_a_missing_entry_stale_before_dropping_it():
    previous = [_entry(1)]

    merged = merge_registry(previous, [])

    assert len(merged) == 1
    assert merged[0].stale is True
    assert merged[0].misses == 1


def test_merge_registry_drops_an_entry_after_the_debounce_window():
    previous = [_entry(1)]
    previous[0].misses = 1  # already missed once

    merged = merge_registry(previous, [])

    assert merged == []


def test_merge_registry_adds_a_newly_seen_entry():
    merged = merge_registry([], [_entry(2)])
    assert [e.pid for e in merged] == [2]
    assert merged[0].misses == 0


def test_merge_registry_preserves_first_seen_across_polls():
    previous = [_entry(1)]
    previous[0].first_seen = 100.0
    fresh = [_entry(1)]
    fresh[0].first_seen = 999.0  # would be wrong if not overwritten by the merge

    merged = merge_registry(previous, fresh)

    assert merged[0].first_seen == 100.0


def test_matched_by_workspace_ignores_tracked_and_stale_entries():
    entries = [
        _entry(1, tracked=True, workspace_ids=["wA"]),
        _entry(2, workspace_ids=["wB"]),
        _entry(3, workspace_ids=["wB"]),
    ]
    entries[1].stale = True

    result = matched_by_workspace(entries)

    assert result == {"wB": {"pi"}}


def test_matched_by_workspace_groups_multiple_kinds_per_workspace():
    entries = [
        _entry(1, kind="pi", workspace_ids=["wA"]),
        _entry(2, kind="claude", workspace_ids=["wA"]),
    ]

    result = matched_by_workspace(entries)

    assert result == {"wA": {"pi", "claude"}}


def test_matched_by_workspace_ignores_unmatched_entries():
    result = matched_by_workspace([_entry(1, workspace_ids=[])])
    assert result == {}
