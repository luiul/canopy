from canopy.scan import (
    SECOND_TOKEN_DENYLIST,
    ProcessMatch,
    parse_lsof_cwd_output,
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
