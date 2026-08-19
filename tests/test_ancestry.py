from canopy.ancestry import Surface, classify_surface
from canopy.scan import ProcessInfo


def _table(*rows: tuple[int, int, str]) -> dict[int, ProcessInfo]:
    return {pid: ProcessInfo(pid=pid, ppid=ppid, pcpu=0.0, tty="s000", comm=comm) for pid, ppid, comm in rows}


def test_classify_surface_trusts_herdr_tracked_without_walking_the_table():
    # herdr routes a pane's shell through its own headless server process,
    # not through whatever terminal a herdr *client* happens to be
    # attached from, so the ancestor chain wouldn't show anything herdr
    # -related anyway; herdr_tracked=True must short-circuit the walk.
    table = _table((1, 0, "/sbin/launchd"))
    assert classify_surface(pid=999, table=table, herdr_tracked=True) == Surface.HERDR


def test_classify_surface_detects_vscode_a_few_hops_up():
    table = _table(
        (56621, 53610, "/Users/luis.aceituno/.local/bin/pi"),
        (53610, 1350, "/bin/zsh"),
        (
            1350,
            797,
            "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper",
        ),
        (797, 1, "/Applications/Visual Studio Code.app/Contents/MacOS/Code"),
    )
    assert classify_surface(pid=56621, table=table, herdr_tracked=False) == Surface.VSCODE


def test_classify_surface_detects_vscode_insiders():
    table = _table(
        (10, 5, "/Users/x/.local/bin/pi"),
        (5, 2, "/bin/zsh"),
        (
            2,
            1,
            "/Applications/Visual Studio Code - Insiders.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper",
        ),
    )
    assert classify_surface(pid=10, table=table, herdr_tracked=False) == Surface.VSCODE


def test_classify_surface_detects_bare_ghostty_tab():
    table = _table(
        (78424, 78245, "/Users/luis.aceituno/.local/bin/pi"),
        (78245, 8028, "-/bin/zsh"),
        (8028, 1, "/Applications/Ghostty.app/Contents/MacOS/ghostty"),
    )
    assert classify_surface(pid=78424, table=table, herdr_tracked=False) == Surface.GHOSTTY


def test_classify_surface_unknown_when_no_recognized_ancestor():
    table = _table(
        (10, 5, "/usr/local/bin/claude"),
        (5, 1, "/sbin/launchd"),
    )
    assert classify_surface(pid=10, table=table, herdr_tracked=False) == Surface.UNKNOWN


def test_classify_surface_unknown_when_pid_missing_from_table():
    assert classify_surface(pid=999999, table={}, herdr_tracked=False) == Surface.UNKNOWN


def test_classify_surface_does_not_loop_forever_on_a_cycle():
    # ppid pointing back at a pid already visited must terminate the walk
    # instead of spinning; MAX_ANCESTOR_HOPS is the other backstop.
    table = {
        1: ProcessInfo(pid=1, ppid=2, pcpu=0.0, tty="s000", comm="a"),
        2: ProcessInfo(pid=2, ppid=1, pcpu=0.0, tty="s000", comm="b"),
    }
    assert classify_surface(pid=1, table=table, herdr_tracked=False) == Surface.UNKNOWN
