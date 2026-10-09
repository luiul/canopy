# Agent state machine

This document specifies the state canopy displays per agent session (one
row of the dashboard), and the exact events allowed to drive each
transition.

Status: **implemented** as described here: the displayed state is the raw
source signal, plus one display overlay for a paused process. See
["Where this lives in code"](#where-this-lives-in-code) for the exact
mapping.

## Scope: which "state" this describes

What a row shows is computed in two layers:

1. A **raw source signal** (`RegistryEntry.State`): `working` /
   `blocked` / `done` / `error` / `idle` / `unknown`. For a `pi` process
   with the `extensions/canopy-status.ts` companion installed, the raw
   signal comes straight from pi's own program-status state machine (pi
   v1.1.0, mirrored by the extension event for event; see
   `internal/pistatus`). Anything else, including a `pi` process whose
   status file is missing or stale, reads `unknown`. There is
   deliberately no CPU-usage fallback: canopy tracks pi sessions on this
   machine only, and a guess that cannot tell "a turn just finished"
   from "idle for an hour" is worse than an honest `unknown` (see
   ["Removed: the CPU heuristic"](#removed-the-cpu-heuristic) below).
2. A **single display overlay** (`displayState()`): a process paused
   with SIGSTOP (the `p` keybind) displays as the synthetic `stopped`,
   which the raw signal cannot express at all (pistatus keeps reporting
   whatever pi last wrote, paused or not). That is the only overlay.
   There is no sticky treatment for `done`/`error`: the raw signal
   already moves on the moment the session does anything new, so acting
   on the session is itself the acknowledgment and the display follows
   it directly (see ["Removed: the sticky done/error
   overlay"](#removed-the-sticky-doneerror-overlay) below).

Each row also has a lifecycle wrapper around these states: `removed`,
once the underlying process is gone. That's a separate, orthogonal
concern (tracked vs. not tracked), not a "state the agent is in", so
it's modeled as the machine's entry/exit rather than another peer state.

## States

| State     | Meaning                                                                                  |
|-----------|------------------------------------------------------------------------------------------|
| `unknown` | No real status: the extension is not installed, its file is stale, or the kind is not pi. |
| `idle`    | Not doing work, nothing pending for the user. Also reported for an aborted run (Escape).  |
| `working` | Actively processing (tool call, generation, streaming, compaction).                       |
| `blocked` | pi is waiting on the user in an extension dialog. Transient: clears on its own.           |
| `done`    | A turn finished. Reads that way until the session does something new.                     |
| `error`   | A turn ended with an unretried error. Reads that way until the session does something new.|
| `stopped` | Synthetic overlay: the process is paused (SIGSTOP via the `p` keybind).                   |

## Events

| Event             | Source                                                                                                  |
|-------------------|----------------------------------------------------------------------------------------------------------|
| `discovered`      | `ps`/`lsof` scan sees this pid for the first time.                                                        |
| `pi_working`      | `canopy-status.ts` writes `working` (`agent_start`, `session_before_compact`, or its heartbeat).           |
| `pi_idle`         | `canopy-status.ts` writes `idle` (session start, or `agent_settled` with `aborted`).                       |
| `pi_blocked`      | `canopy-status.ts` writes `blocked` (`ui_prompt_start`, an extension dialog opened).                       |
| `pi_unblocked`    | `canopy-status.ts` writes whatever state the dialog was covering (`ui_prompt_end`).                        |
| `pi_settled`      | `canopy-status.ts` writes `done` (`agent_settled`, not aborted, last assistant message not an error).      |
| `pi_error`        | `canopy-status.ts` writes `error` (`agent_settled` where the last assistant message had `stopReason: "error"`). |
| `sigstop_cont`    | User presses `p` on the row in canopy: SIGSTOP/SIGCONT, toggling the `stopped` overlay.                    |
| `miss_exceeded`   | The process is absent from more than `MissLimit` (currently 1) consecutive polls, or has genuinely exited. |

Every displayed transition is one of these events applied verbatim. In
particular, `done` and `error` exit on exactly the events pi itself
reports next: `pi_working` (a fresh turn started), `pi_idle` (Escape, or
a new session in the same process), and `miss_exceeded` (the process
exited). There is no canopy-side acknowledgment event.

## The message line

Each raw report can carry three optional payloads, rendered as one
tinted detail line directly under the row: a second line only where
there is something to say, never a column, because three long-text
columns cannot share one viewport. Measured with real data at 120
cells, a Message column crushed Location and Model to ~7 cells each
*and* still truncated the error line, while the detail line shows it in
full at any width.

The payloads:

- **message**: pi's own program-status semantics: the session name for
  `working`/`done`, the dialog title for `blocked`, the first line of
  the error for `error`.
- **detail**: canopy's enrichment (pi's reporter has no equivalent):
  the last tool call while `working` (`edit: internal/tui/rows.go`), the
  first non-empty line of pi's final assistant message once settled
  (usually pi's own one-line summary of what it did).
- **task**: the first prompt of the session, riding every write
  including settles: session identity, not state payload.

The composition per display state: `blocked` and `error` show their
message alone (the actionable content); `working` shows "name ·
activity" and `done` "name · outcome", either part droppable; `idle`,
`stopped`, and `unknown` show the task. Any state whose parts come out
empty falls back to the task, so an unnamed row before its first tool
call still says what it's doing; a pi that was never prompted renders
no line.

The line follows the raw report, same as the state word on screen: when
the session starts a fresh turn, the line shows that turn's own name and
activity, and a still-settled row keeps its outcome line for as long as
the settle stands.

The line is tinted in its state's hue without bold (bold stays reserved
for the `done`/`error` State words), never blinks (blink stays confined
to the State word's `*`), and truncates only at the terminal's right
edge. It is part of the filter's matched text, so `/rate limit` finds
the throttled row and `/go test` the one running it. Each visible detail
line costs one table row of height; `resizeTableHeight` hands exactly
that many rows back to the header/footer budget so the footer never gets
pushed off screen (see `internal/tui/message.go`).

## The bell

The terminal bell rings on exactly three kinds of transition, all against
the raw signal:

- a row enters `done` (new settle),
- a row enters `error` (new failure),
- a row is `blocked` on **two consecutive polls** (the debounce: a dialog
  you answer within one poll interval, the common case when you are
  sitting in that very terminal, never rings). One blocked spell rings at
  most once; the next spell re-arms.

A settle rings once, at its transition, and never again for as long as
the same write keeps standing: see ["Telling two settles
apart"](#telling-two-settles-apart-when-the-raw-string-doesnt-change)
below for how `newSettles` tells a genuinely new settle apart from the
same one-shot write repeating. See `internal/tui/bell.go`.

## The blink

A row whose latest poll saw a genuinely new settle (the same transitions
the bell rings for) blinks its State word: a real on/off toggle for a
few seconds right away, then steady. One burst per settle, no repeating
reminders: with the display following the raw signal there is nothing
left to remind about, since a row still reading `done` an hour later is
simply the truth of a session that has been sitting settled since lunch.
See `internal/tui/blink.go`.

## Removed: the sticky done/error overlay

Until October 2026, `done` and `error` were sticky: once a row's raw
signal read `done`/`error`, canopy latched an "attention episode" and
kept displaying that word, regardless of what the raw signal reported on
any later poll, until the user pressed `enter` or `c` on the row (`C`
for all rows at once). The episode latched the settle's message and
detail alongside the word, blinked every five minutes while
unacknowledged, and synced acknowledgments across concurrently running
canopy instances via a small shared file per row (`internal/ack`).

It existed so a turn that finished while the user was away could not
vanish from the screen before anyone saw it, even if the session started
a new turn in the meantime.

It is gone because the pi v1.1.0 program-status integration made it
redundant. Every way a user acts on a settled session already moves the
raw signal: a fresh prompt reports `working` at `agent_start`, Escape
reports `idle`, a new session in the same process reports `idle`. The
canopy-side keypress was a second acknowledgment of something the user
had already done in pi itself, and while it was pending the row actively
lied (displaying `done` for a session already `working` again). The
trade it protected against is accepted instead: a settle that lands and
gets superseded by a new turn entirely between two polls is never
displayed as `done` at all (the row goes straight back to `working`,
then to the newer settle's `done`).

Going with it: the `c`/`C` keybinds (`enter` keeps only its jump side
effect), the episode's latched message/detail (the detail line follows
the raw report), the five-minute blink reminders (one burst per settle),
the ack-relative Since clock (Since is always the raw state age), and
`internal/ack` wholesale (with no local overlay state, two canopy
instances derive identical displays from the same shared files, so there
is nothing left to sync; see ["Cross-instance
consistency"](#cross-instance-consistency) below).

## Removed: frontmost/focus detection

`extensions/canopy-status.ts` used to try to tell whether the user was
already looking at a session's terminal when its turn ended (an
`osascript`-based frontmost/window-title check, mirroring
`notifications.ts`'s own desktop-notification suppression), writing
`idle` instead of `done` if so. That distinction is gone: settles write
`done`/`error` unconditionally, no focus check at all.

It was removed while the sticky overlay existed, because the overlay
already required an explicit keypress before an attention row displayed
as anything else, so the focus check's only remaining effect was
suppressing the bell/blink for a turn the user had watched finish live.
Removing it traded that suppression for losing the extension's only
subprocess/AppleScript call and its Accessibility-permission dependency
entirely. The trade stands on its own under the raw-following display:
the bell/blink fires on every settled turn, including ones watched live,
and the display needs no focus guesswork at all, since the raw signal is
the display.

## Removed: the CPU heuristic

Before the pi v1.1.0 program-status integration, rows without a real
status were classified by a poll-to-poll CPU-delta heuristic
(`internal/state`, `registry.refineExternalStates`): working if the
process burned enough CPU, idle otherwise. It is gone, not just demoted,
because canopy tracks pi sessions on this machine only, and for pi a
guessed state is strictly worse than pi's own report:

- It structurally could only produce `working`/`idle`/`unknown`: CPU
  usage alone can't tell "a turn just finished" from "has been idle for
  an hour", so `done`/`error` could never come from it.
- It read any I/O-bound wait (a slow bash command, a web fetch, an LLM
  still streaming) as `idle` mid-turn, and any post-turn CPU decay as
  `working` for up to a minute after the turn actually ended.
- It could not express `blocked` at all: a dialog waiting on the user
  reads ~0% CPU, same as idle.

What replaced it is the honest split above: pi's real five-state signal
where available, `unknown` where not. The CPU column itself stays: it
still shows the raw `ps` %cpu per row (now purely informational, no
longer feeding any state), along with RAM and Uptime.

## Diagram

See [`agent-state-machine.drawio`](agent-state-machine.drawio) (open
with [draw.io](https://app.diagrams.net) or the VS Code draw.io
extension) for the editable source. Rendered:

![Agent state machine diagram](agent-state-machine.svg)

## Where this lives in code

The raw signal comes from `internal/pistatus` (reading
`extensions/canopy-status.ts`'s status file for a `pi` process);
anything without a fresh report stays `unknown` (`internal/registry`'s
`externalEntries`). The display layer is deliberately thin:

- `displayState(e)` in `internal/tui/rows.go`: the raw `State`, overlaid
  with the synthetic `stopped` when `e.Stopped` is set. Sorting,
  coloring, the summary line, and the State/Since cells all go through
  this instead of `e.State` directly.
- `detailLineText(e)` in `internal/tui/message.go`: the per-state
  composition of message/detail/task into the tinted detail line (see
  ["The message line"](#the-message-line)).
- `newSettles(previous, fresh)` in `internal/tui/bell.go`: the keys with
  a genuinely new settle this poll (the bell's `done`/`error` side, and
  the blink bursts' trigger). `newlyBlocked(previous, fresh, rung)` is
  the blocked side: rings once per spell, only after `blocked` survives
  two consecutive polls.
- `Model.blinks`, `startBlinks`/`pruneBlinks`/`tickBlinks` in
  `internal/tui/blink.go`: one on/off blink burst per settle, purely
  visual (see ["The blink"](#the-blink)).

### Telling two settles apart when the raw string doesn't change

`extensions/canopy-status.ts` writes `done`/`error` once, at the
transition, with no heartbeat (unlike `working`/`blocked`/`idle`, whose
`STATUS_HEARTBEAT_MS` refresh keeps their timestamp inside
`pistatus.MaxAge`). `pistatus.Read` keeps returning that same literal
string indefinitely afterward, since nothing else has overwritten the
file yet and terminal writes are exempt from `pistatus.MaxAge` (see
below). If a *second* turn starts and settles again without canopy's
poll cadence ever happening to sample a `pi_working` reading in between
(plausible for a fast, tool-free turn), `RegistryEntry.State` reads the
literal string `"done"` on both polls, with nothing in the string
itself to tell the two settles apart.

`newSettles` resolves this with `RegistryEntry.RealStateReportedAt`
(`pistatus.Status.UpdatedAt`, the moment `canopy-status.ts` itself wrote
the file, not the moment canopy polled it): a poll where the State
string stayed `done`/`error` still counts as a new settle, with a fresh
bell and blink burst, once this timestamp has actually advanced.
Without it, the second settle would be silently swallowed: no bell, no
blink, the row just sitting at the same steady `done` as if the second
turn had never finished.

This one-shot design is also why `pistatus` exempts `done`/`error` from
its `MaxAge` staleness check: a refreshed terminal write would move
`updatedAt` forward and impersonate a brand-new settle to the comparison
above, re-ringing the bell for a turn that finished long ago. `working`,
`blocked`, and `idle` carry no such identity burden, so they heartbeat
and expire normally; a dead extension's row falls back to `unknown`
within `MaxAge`, while a settled row keeps reading its (still true)
terminal state (and process liveness is the ps scan's job, not the
file's).

Regression coverage in `internal/tui/app_test.go` exercises both halves
of the rule ("same write across polls never re-rings" and "a genuinely
new write with no intervening `working` poll rings again"), plus the
core raw-following contract (a `done` row reads `done` for as long as
the raw write stands and follows the signal to `working`/`idle` the
moment it moves on). `internal/tui/attention_test.go` covers the `error`
side of the same machinery and the blocked debounce.

## Cross-instance consistency

Canopy has no daemon and no notion of "the" dashboard: running it in two
terminal windows at once starts two fully independent processes. They
show the same thing anyway, because everything on screen is derived
independently but identically from the same shared, externally
observable sources (`ps`/`lsof`, and `internal/pistatus`'s per-pid
status file). With no per-instance display state at all, there is
nothing left to sync.
