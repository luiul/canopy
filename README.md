# canopy

An interactive dashboard for every agent CLI session (`pi`, `claude`,
`codex`, ...) running on this machine, wherever it actually is, a
[herdr](https://herdr.dev) pane, a VS Code integrated terminal, or a bare
Ghostty tab, with its live state and jump-to-window on Enter.

This is the Go implementation, and the one actively developed going
forward. An earlier Python/Textual prototype lives at `../canopy-python`
(kept for reference, no longer installed); this version has the same
behavior, ported to a single static binary: no interpreter, no venv,
instant startup.

## What it looks like

```
canopy — agent sessions on this machine
3 sessions: 1 blocked · 1 working · 1 idle

   Surface    State      Since   Kind        PID       Location
>  VS Code    working    12s     pi          86872     ~/projects/personal/canopy
   VS Code    blocked    3m      pi          9514      ~/worktrees/.../isa-orchestration
   Ghostty    idle       1h20m   pi          65834     herdr:wG

↑/↓ move · enter jump · r refresh · q quit
```

The currently selected row is marked with a `>` in the leftmost column
(rather than a full-row highlight, which would hide State's color coding
on whichever row happens to be selected). Location shortens a leading
home-directory prefix to `~`, same as your shell prompt.

State is color-coded (red for `blocked`, green for `done`, yellow for
`working`, dim for `idle`/`unknown`), and a row that just transitioned into
`blocked` or `done` flashes briefly (a trailing `*` plus a reverse-video
highlight) so it's hard to miss. Sessions are sorted most-actionable
first: `blocked`, then `done`, then `working`, then `idle`/`unknown`. Pass
`--no-color` (or set `NO_COLOR`) to disable all of that and get plain text.

## Why Go, not Python

canopy is 100% process discovery, subprocess orchestration, and a polling
TUI, no real computation. That profile, plus the fact that
[herdr](https://herdr.dev) itself (the sibling tool this complements) is a
single compiled binary, made a compiled language a better fit: no
interpreter/venv to install or drift across Python versions, near-instant
startup for a tool you re-launch constantly, and `os/exec` maps almost
line-for-line onto every subprocess call the original Python prototype made.
Measured against that prototype: ~34x faster startup, ~3.4x less idle RSS,
~23x smaller install footprint (single 3.4 MB binary vs. an interpreter +
venv).

## Architecture

One Go package per concern:

- `internal/scan`: shells out to `ps`/`lsof`, parses their output into
  typed rows.
- `internal/state`: CPU%-based idle/working heuristic for processes herdr
  doesn't track.
- `internal/ancestry`: walks a process's parent chain to classify which app
  (herdr / VS Code / Ghostty) is hosting it.
- `internal/herdrclient`: thin JSON-over-subprocess client for the `herdr`
  binary. Degrades to "no herdr rows" if `herdr` isn't installed, rather
  than erroring the whole poll.
- `internal/applescript`: `osascript` wrapper for focusing a Ghostty window
  by working directory, with macOS Automation-permission error detection.
- `internal/jump`: picks the actual jump mechanism (herdr's socket focus,
  `code --reuse-window`, or Ghostty AppleScript) per row.
- `internal/registry`: merges a fresh poll against the previous one so a
  single missed `ps`/herdr poll doesn't flicker a row away.
- `internal/tui`: the Bubble Tea dashboard (table, polling timer,
  jump-on-Enter, notifications).
- `cmd/canopy`: the CLI entry point (flags, version).

## Install

```bash
cd canopy
go build -o /tmp/canopy-build ./cmd/canopy
install -m 0755 /tmp/canopy-build ~/.local/bin/canopy   # or anywhere on PATH
```

Or, if `$(go env GOPATH)/bin` (usually `~/go/bin`) is on your `PATH`:

```bash
go install ./cmd/canopy
```

Requires `herdr` on PATH for herdr-pane visibility (see https://herdr.dev);
everything else works without it, herdr-tracked rows just won't show up.

## Development

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l .   # should print nothing
```

## Limitations

- Same machine, same user only.
- Idle/working for non-herdr surfaces is a CPU% heuristic, not a real
  status.
- Ghostty jump-to matches by working directory, not tty/pid; ambiguous if
  two tabs share a cwd.
- VS Code jump-to raises the right window but not necessarily the specific
  integrated-terminal tab within it.
- Mouse click-to-jump isn't implemented (keyboard only: arrow keys +
  Enter); Bubble Tea's table widget doesn't ship row-click handling out of
  the box the way Textual's `DataTable` does.
