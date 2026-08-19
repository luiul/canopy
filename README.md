# canopy

[![CI](https://github.com/luiul/canopy/actions/workflows/ci.yml/badge.svg)](https://github.com/luiul/canopy/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

An interactive dashboard for every agent CLI session (`pi`, `claude`,
`codex`, ...) running on this machine, wherever it actually is, a
[herdr](https://herdr.dev) pane, a VS Code integrated terminal, or a bare
Ghostty tab, with its live state and jump-to-window on Enter or click.

## What it looks like

```
  Kind   PID     Surface   State      Location
 ─────────────────────────────────────────────────────────────────────
  pi     89778   herdr     working    herdr:wG
  pi     78424   VS Code   idle       /Users/luis/worktrees/.../isa-orchestration
  pi     53331   VS Code   idle       /Users/luis/projects/personal/sandbox
  pi     56621   VS Code   idle       /Users/luis/dotfiles
```

Arrow keys to move, `Enter` or click a row to jump straight to that
window, `q` to quit, `r` to refresh immediately.

## Why

herdr's Agents section only ever lists agents running inside a pane
herdr itself created; every agent-related method in herdr's own socket
API takes or resolves a pane id, there's no way to register one unbound
to a pane. Start `pi` or `claude` in a plain Ghostty tab or a VS Code
integrated terminal instead, and herdr has no way to know it exists.

canopy is a standalone dashboard, not a herdr plugin and not a herdr
wrapper: it does its own process discovery (`ps`), its own "which app is
actually hosting this terminal" classification (walking each process's
parent chain), and its own idle/working heuristic for anything herdr
doesn't track. It calls out to `herdr`, `osascript`, and `code` only for
the one thing each is actually needed for: reading herdr's own
`agent_status` for panes it tracks, focusing a Ghostty window, and
focusing a VS Code window, respectively. Nothing is written back into
herdr; it's a foreground tool you run in its own pane next to whatever
else you're doing, not a background daemon.

## How it decides what's hosting a process

- **herdr**: cross-referenced directly via `herdr pane process-info`.
  State comes straight from herdr's own `agent_status` (idle / working /
  blocked / done / unknown), the most accurate source available since
  herdr owns the pty.
- **VS Code**: the process's parent chain passes through one of VS
  Code's `Code Helper` processes a couple of hops up.
- **Ghostty**: the process's parent chain passes through `ghostty`
  itself.
- **unknown**: anything else (iTerm2, Terminal.app, tmux, a remote SSH
  session, ...). Shown, but there's no jump implemented for it yet.

For non-herdr surfaces, state is a coarse heuristic: a process's CPU%
(from `ps`) above a threshold is called `working`, otherwise `idle`.
There's no pty canopy can read output from for these, so it can't tell
"idle" apart from "waiting on the user's next keystroke", both look the
same from outside the terminal.

## Jumping to a window

- **herdr** pane: `herdr workspace/tab/pane focus`, then raises Ghostty
  as an app (it can't reliably pick *which* Ghostty window is showing
  that particular herdr client, herdr's client/server split means the
  pane's own process tree doesn't lead back to it).
- **VS Code**: `code --reuse-window <cwd>` (falls back to
  `open -a "Visual Studio Code" <cwd>` if the `code` CLI isn't
  installed, which can only raise the app, not the specific window).
- **Ghostty**: AppleScript, matching by working directory. Ghostty's own
  scripting dictionary declares a `tty` property, which would be the
  precise match, but on the shipped 1.3.1 build `tty` (and `pid`)
  reliably error (`Can't make tty of terminal id "..." into type
  specifier`), while `working directory` works fine. Two tabs `cd`-ed to
  the same directory are indistinguishable this way; canopy just
  focuses whichever one Ghostty's script bridge returns first.

First use needs a one-time macOS Automation permission grant for
scripting Ghostty (System Settings -> Privacy & Security -> Automation).

## Install

Requires `herdr` on PATH for herdr-pane visibility (see https://herdr.dev);
everything else works without it, herdr-tracked rows just won't show up.

```bash
uv tool install --editable .
canopy
```

## Limitations

- Same machine, same user only. An agent in a container, over SSH on
  another host, or under a different local user is invisible to a local
  `ps` scan.
- Idle/working for non-herdr surfaces is a CPU% heuristic, not a real
  status; it can't distinguish idle from blocked-on-input, and a brief
  burst of work may take a poll cycle or two to register.
- Ghostty jump-to matches by working directory, not tty/pid (see above);
  ambiguous if two tabs share a cwd.
- VS Code jump-to raises the right *window* (one per open folder) but
  not necessarily the specific integrated-terminal tab within it, VS
  Code has no scripting hook for that.
- No row inside herdr's own native Agents section, only this dashboard.
  That would need an API herdr doesn't have (there's nothing to register
  an agent unbound to a pane).

## Development

```bash
uv sync --group dev
uv run pytest
uv run ruff check .
uv run ruff format .
uv run ty check
```
