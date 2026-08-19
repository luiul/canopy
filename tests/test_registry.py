from canopy.ancestry import Surface
from canopy.registry import RegistryEntry, merge_registry


def _entry(pid: int, kind: str = "pi", surface: Surface = Surface.GHOSTTY, state: str = "idle") -> RegistryEntry:
    return RegistryEntry(pid=pid, kind=kind, tty="s000", cwd="/x", surface=surface, state=state)


def test_merge_registry_prefers_the_fresh_copy_of_a_still_present_entry():
    previous = [_entry(1, state="idle")]
    previous[0].misses = 1
    fresh = [_entry(1, state="working")]

    merged = merge_registry(previous, fresh)

    assert len(merged) == 1
    assert merged[0].state == "working"
    assert merged[0].misses == 0


def test_merge_registry_keeps_a_missing_entry_within_the_debounce_window():
    previous = [_entry(1)]

    merged = merge_registry(previous, [])

    assert len(merged) == 1
    assert merged[0].misses == 1


def test_merge_registry_drops_an_entry_once_past_the_debounce_window():
    previous = [_entry(1)]
    previous[0].misses = 1  # already missed once, at MISS_LIMIT

    merged = merge_registry(previous, [])

    assert merged == []


def test_merge_registry_adds_a_newly_seen_entry():
    merged = merge_registry([], [_entry(2)])
    assert [e.pid for e in merged] == [2]


def test_registry_entry_key_disambiguates_same_pid_different_kind():
    # pids get reused by the OS; scoping the key by kind too avoids two
    # genuinely different processes colliding if a pid is recycled
    # between polls faster than the debounce window notices.
    a = _entry(1, kind="pi")
    b = _entry(1, kind="claude")
    assert a.key != b.key
