# canopy

[![CI](https://github.com/luiul/canopy/actions/workflows/ci.yml/badge.svg)](https://github.com/luiul/canopy/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Read-only visibility into agent CLI sessions running outside
[herdr](https://herdr.dev).

Built on top of `herdr`, which owns terminal workspaces, panes, and agent
detection. `canopy` adds what herdr doesn't: a view of agent processes
herdr can't see, because they were started outside any herdr pane.

## Why

Herdr's Agents section only ever lists agents running inside a pane herdr
itself created. Every agent-related method in herdr's own socket API
(`agent.*`, `pane.*`) takes or resolves a pane id; there is no method to
register an agent unbound to one. Start `claude` or `pi` in a plain
terminal tab instead of a herdr pane, and herdr has no way to know it
exists.

`canopy` polls the current user's processes for herdr's known agent kinds,
cross-references herdr's own `pane process-info` to find the ones herdr
doesn't already track, and matches each one's cwd to a herdr workspace by
git-worktree-aware repo identity (so a linked worktree checkout still
matches the right workspace, even at a different path). Matches show up
two ways:

- A short-TTL workspace metadata badge (`herdr workspace report-metadata`),
  visible in herdr's existing sidebar today.
- `canopy status` / `canopy status --watch`, a live table of everything:
  tracked, external, and which workspace (if any) an external one matches.

There is no control surface: an externally started process has no pty
`canopy` or herdr can attach to. This is visibility only, not a way to run
more agents through herdr.

## Install

Requires `herdr` on PATH, see https://herdr.dev.

```bash
uv tool install --editable .
canopy install   # starts a LaunchAgent that runs `canopy watch` continuously
canopy status --watch
```

`canopy install` writes and loads `~/Library/LaunchAgents/com.luiul.canopy.plist`
with `RunAtLoad` and `KeepAlive`, so the watcher survives login and restarts
itself on crash. `canopy uninstall` stops it and clears any badge it is
currently holding.

## Commands

- `canopy watch` — the watcher loop itself: scan, match, write the
  registry, report/clear workspace badges. Runs in the foreground; this is
  what the LaunchAgent runs. Useful directly for testing (`--dry-run` logs
  intended badge changes without calling herdr).
- `canopy status [--watch]` — read-only view of the registry `canopy
  watch` last wrote. Does not scan anything itself.
- `canopy install` / `canopy uninstall` — manage the LaunchAgent.

## Limitations

- Matching is a heuristic: known kind name, a controlling tty, and a
  second argv token that isn't an obvious subcommand/flag (`codex mcp`,
  `claude --version`). A renamed binary or an unusual wrapper can be
  missed or misclassified.
- Same machine, same user only. An agent in a container, over SSH on
  another host, or under a different local user is invisible to a local
  `ps` scan.
- Some kind names (`amp`, `cursor`) are common words; an unrelated binary
  with the same name and a real tty could false-positive.
- No row inside herdr's own native Agents section, only a sidebar badge
  plus this CLI's own view. That needs an API herdr does not have yet
  (there is nothing to register an agent unbound to a pane).

## Development

```bash
uv sync --group dev
uv run pytest
uv run ruff check .
uv run ruff format .
uv run ty check
```
