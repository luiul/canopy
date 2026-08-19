"""canopy: read-only visibility into agent CLI sessions running outside herdr.

herdr's own Agents section only ever lists agents running inside a pane
herdr itself created; there is no socket method to register an agent
unbound to a pane. `canopy watch` polls the current user's processes for
herdr's known agent kinds, cross-references herdr's own pane/process-info
to find the ones herdr doesn't track, matches each one's cwd to a herdr
workspace by git-worktree-aware repo identity, and reports a short-TTL
workspace metadata badge for the match. `canopy status` shows the result.

`canopy` does not reimplement anything herdr already owns: pane discovery
and workspace metadata stay herdr's job, this only shells out to it (see
`canopy.herdr`), the same relationship coppice has with `wt`.
"""

from __future__ import annotations

import signal
import time
from importlib.metadata import PackageNotFoundError, version
from typing import Annotated

import typer
from rich import box
from rich.console import Console
from rich.live import Live
from rich.table import Table

from canopy import badges, herdr, launchd, paths, registry

APP_HELP = """\
Read-only visibility into agent CLI sessions running outside [bold]herdr[/].

Requires [bold]herdr[/] on PATH, see https://herdr.dev.

Run [cyan]canopy install[/] once to run the watcher continuously via a
macOS LaunchAgent, then [cyan]canopy status --watch[/] to see it live.
"""

app = typer.Typer(
    help=APP_HELP,
    no_args_is_help=True,
    add_completion=True,
    context_settings={"help_option_names": ["-h", "--help"]},
    rich_markup_mode="rich",
)

console = Console()
err = Console(stderr=True)


def _fail(message: str) -> typer.Exit:
    err.print(f"[red]Error:[/] {message}")
    return typer.Exit(1)


def _version_callback(show_version: bool) -> None:
    if not show_version:
        return
    try:
        console.print(f"canopy {version('canopy')}")
    except PackageNotFoundError:
        console.print("canopy (version unknown, not installed as a package)")
    raise typer.Exit()


@app.callback()
def _main(
    version_: Annotated[
        bool,
        typer.Option("--version", callback=_version_callback, is_eager=True, help="Show the version and exit."),
    ] = False,
) -> None:
    pass


def _status_row(entry: registry.RegistryEntry) -> tuple[str, str, str, str, str, str]:
    if entry.tracked:
        status = "[dim]tracked[/]"
    elif entry.stale:
        status = "[yellow]stale[/]"
    else:
        status = "[green]external[/]"
    workspaces = ",".join(entry.workspace_ids) or "-"
    return str(entry.kind), str(entry.pid), entry.tty, status, workspaces, entry.cwd or "?"


def _render_table() -> Table:
    table = Table(box=box.SIMPLE_HEAVY, header_style="bold")
    for col in ("KIND", "PID", "TTY", "STATUS", "WORKSPACE", "CWD"):
        table.add_column(col)

    entries = registry.load_registry(paths.REGISTRY_PATH)
    if not entries:
        table.add_row(
            "[dim]no agent-kind processes detected, or `canopy watch` isn't running yet[/]", "", "", "", "", ""
        )
        return table

    for entry in sorted(entries, key=lambda e: (e.tracked, e.kind, e.pid)):
        table.add_row(*_status_row(entry))
    return table


@app.command("status", rich_help_panel="Inspect")
def cmd_status(
    watch: Annotated[bool, typer.Option("--watch", "-w", help="Redraw continuously instead of printing once.")] = False,
    interval: Annotated[float, typer.Option("--interval", help="Redraw interval in seconds, with --watch.")] = 2.0,
) -> None:
    """Show the current registry: every known agent-kind process, tracked
    vs external, and which herdr workspace (if any) an external one
    matches. Read-only: reads what `canopy watch` last wrote, does not
    scan anything itself.
    """
    if not watch:
        console.print(_render_table())
        return

    with Live(_render_table(), console=console, refresh_per_second=4) as live:
        while True:
            time.sleep(interval)
            live.update(_render_table())


@app.command("watch", rich_help_panel="Run")
def cmd_watch(
    interval: Annotated[float, typer.Option("--interval", help="Poll interval in seconds.")] = 7.0,
    ttl_ms: Annotated[
        int | None, typer.Option("--ttl-ms", help="Workspace badge TTL. Defaults to interval * 3.")
    ] = None,
    dry_run: Annotated[
        bool, typer.Option("--dry-run", help="Log intended badge changes without calling herdr.")
    ] = False,
) -> None:
    """Run the watcher loop in the foreground: scan, match, write the
    registry, report/clear workspace badges. This is what the LaunchAgent
    installed by `canopy install` runs; run it directly for testing.
    """
    try:
        herdr.require_herdr()
    except herdr.HerdrNotFoundError as exc:
        raise _fail(str(exc)) from exc

    paths.ensure_state_dir()
    effective_ttl_ms = ttl_ms if ttl_ms is not None else int(interval * 3 * 1000)

    import getpass

    user = getpass.getuser()
    entries = registry.load_registry(paths.REGISTRY_PATH)
    badge_state = badges.load_badge_state(paths.BADGES_PATH)

    stopping = False

    def _handle_signal(signum: int, _frame: object) -> None:
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, _handle_signal)
    signal.signal(signal.SIGINT, _handle_signal)

    console.print(f"canopy watch: user={user} interval={interval}s ttl_ms={effective_ttl_ms} dry_run={dry_run}")

    try:
        while not stopping:
            try:
                entries = registry.poll_once(user, entries)
                registry.save_registry(paths.REGISTRY_PATH, entries, poll_interval_s=interval, dry_run=dry_run)
                matched = registry.matched_by_workspace(entries)
                badge_state = badges.apply_badges(matched, badge_state, ttl_ms=effective_ttl_ms, dry_run=dry_run)
                badges.save_badge_state(paths.BADGES_PATH, badge_state)
            except Exception as exc:
                err.print(f"[yellow]poll error:[/] {exc}")

            for _ in range(int(interval * 10)):
                if stopping:
                    break
                time.sleep(0.1)
    finally:
        console.print("canopy watch: shutting down, clearing badges")
        badges.clear_all(badge_state, dry_run=dry_run)


@app.command("install", rich_help_panel="Run")
def cmd_install() -> None:
    """Install and start a macOS LaunchAgent that runs `canopy watch`
    continuously, restarting it if it ever crashes.
    """
    paths.ensure_state_dir()
    try:
        plist_path = launchd.install(paths.LOG_PATH)
    except (launchd.CanopyNotFoundError, launchd.HerdrNotFoundForInstallError) as exc:
        raise _fail(str(exc)) from exc
    console.print(f"Installed and started: [bold]{plist_path}[/]")
    console.print(f"Logs: [bold]{paths.LOG_PATH}[/]")


@app.command("uninstall", rich_help_panel="Run")
def cmd_uninstall() -> None:
    """Stop and remove the LaunchAgent, and clear any workspace badges
    canopy currently holds so nothing lingers past its TTL.
    """
    badge_state = badges.load_badge_state(paths.BADGES_PATH)
    badges.clear_all(badge_state, dry_run=False)

    removed = launchd.uninstall()
    if removed:
        console.print("LaunchAgent stopped and removed.")
    else:
        console.print("No LaunchAgent was installed.")
