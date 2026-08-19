from canopy.scan import (
    SECOND_TOKEN_DENYLIST,
    ProcessInfo,
    ProcessMatch,
    parse_lsof_cwd_output,
    parse_process_table_output,
    parse_ps_output,
)


def test_parse_ps_output_matches_known_kind_with_tty():
    output = "78424 ttys006 pi\n"
    matches = parse_ps_output(output)
    assert matches == [ProcessMatch(pid=78424, tty="ttys006", kind="pi", args="pi")]


def test_parse_ps_output_skips_processes_without_a_tty():
    output = "111 ?? codex mcp\n222 ttys003 codex\n"
    matches = parse_ps_output(output)
    assert [m.pid for m in matches] == [222]


def test_parse_ps_output_skips_unknown_kinds():
    output = "1 ttys000 bun /some/server.bundle.mjs\n2 ttys001 zsh\n"
    assert parse_ps_output(output) == []


def test_parse_ps_output_resolves_full_path_argv0_to_basename():
    output = "5 ttys002 /usr/local/bin/claude --resume\n"
    matches = parse_ps_output(output)
    assert matches[0].kind == "claude"


def test_parse_ps_output_denylists_helper_subcommands():
    for token in sorted(SECOND_TOKEN_DENYLIST):
        output = f"9 ttys004 codex {token}\n"
        assert parse_ps_output(output) == [], f"{token!r} should be filtered out"


def test_parse_ps_output_ignores_blank_and_malformed_lines():
    output = "\n   \nnot a valid ps line\n42 ttys005 pi\n"
    matches = parse_ps_output(output)
    assert [m.pid for m in matches] == [42]


def test_parse_lsof_cwd_output_pairs_pid_and_path():
    output = "p123\nfcwd\nn/Users/luis/dotfiles\np456\nfcwd\nn/Users/luis/projects\n"
    assert parse_lsof_cwd_output(output) == {
        123: "/Users/luis/dotfiles",
        456: "/Users/luis/projects",
    }


def test_parse_lsof_cwd_output_empty_input():
    assert parse_lsof_cwd_output("") == {}


def test_parse_process_table_output_parses_pid_ppid_pcpu_tty_comm():
    output = "56621   53610   3.2 s017 /Users/luis.aceituno/.local/bin/pi\n"
    table = parse_process_table_output(output)
    assert table[56621] == ProcessInfo(
        pid=56621, ppid=53610, pcpu=3.2, tty="s017", comm="/Users/luis.aceituno/.local/bin/pi"
    )


def test_parse_process_table_output_preserves_spaces_in_comm():
    # macOS `comm` is the full executable path, and paths like VS Code's
    # helper processes contain literal spaces; comm must stay the last,
    # greedily-parsed column or this truncates.
    output = (
        "52562 1350 0.5 ?? /Applications/Visual Studio Code.app/Contents/Frameworks/"
        "Code Helper (Renderer).app/Contents/MacOS/Code Helper (Renderer) --type=renderer\n"
    )
    table = parse_process_table_output(output)
    assert table[52562].comm.endswith("Code Helper (Renderer) --type=renderer")


def test_parse_process_table_output_skips_malformed_lines():
    output = "\nnot enough fields\n1 0 0.0 ?? launchd\n"
    table = parse_process_table_output(output)
    assert list(table) == [1]
