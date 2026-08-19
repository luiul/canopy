"""canopy: an interactive dashboard for every agent CLI process on this
machine, wherever it's running (a herdr pane, a VS Code integrated
terminal, or a bare Ghostty tab), with jump-to-window on Enter or click.

canopy is its own tool, not a herdr wrapper: it does its own process
discovery and its own idle/working heuristic. It calls out to `herdr`
(see `canopy.herdr`) only for the one thing genuinely herdr's to know,
which panes it's already tracking and their own agent_status, the same
way it calls out to `osascript`/`code` to jump into Ghostty/VS Code.
"""

from __future__ import annotations

import getpass
from importlib.metadata import PackageNotFoundError, version
from typing import Annotated

import typer
from rich.text import Text
from textual.app import App, ComposeResult
from textual.coordinate import Coordinate
from textual.widgets import DataTable, Footer, Header

from canopy import jump
from canopy.ancestry import Surface
from canopy.registry import RegistryEntry, poll_once

DEFAULT_INTERVAL_S = 2.0

_STATE_STYLE = {
    "working": "bold green",
    "idle": "yellow",
    "blocked": "bold red",
    "done": "dim",
    "unknown": "dim",
}

_SURFACE_LABEL = {
    Surface.HERDR: "herdr",
    Surface.VSCODE: "VS Code",
    Surface.GHOSTTY: "Ghostty",
    Surface.UNKNOWN: "unknown",
}

COLUMNS = ("kind", "pid", "surface", "state", "location")


def _location(entry: RegistryEntry) -> str:
    if entry.surface == Surface.HERDR and entry.workspace_id:
        return f"herdr:{entry.workspace_id}"
    if entry.cwd:
        return entry.cwd
    return "?"


def _sort_key(entry: RegistryEntry) -> tuple:
    # Working agents first (most likely to need attention or be the one
    # you're looking for), then grouped by surface, then stable by pid.
    working_first = 0 if entry.state == "working" else 1
    return (working_first, entry.surface.value, entry.kind, entry.pid)


class CanopyApp(App[None]):
    TITLE = "canopy"
    SUB_TITLE = "agent sessions on this machine"

    CSS = """
    DataTable {
        height: 1fr;
    }
    """

    BINDINGS = [  # noqa: RUF012 - Textual's own convention for this attribute
        ("q", "quit", "Quit"),
        ("r", "refresh_now", "Refresh"),
        ("enter", "jump_selected", "Jump"),
    ]

    def __init__(self, interval: float = DEFAULT_INTERVAL_S) -> None:
        super().__init__()
        self.interval = interval
        self.user = getpass.getuser()
        self.entries: list[RegistryEntry] = []
        self._entries_by_key: dict[str, RegistryEntry] = {}

    def compose(self) -> ComposeResult:
        yield Header()
        table: DataTable[Text] = DataTable(id="table", cursor_type="row", zebra_stripes=True)
        yield table
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_column("Kind", key="kind")
        table.add_column("PID", key="pid")
        table.add_column("Surface", key="surface")
        table.add_column("State", key="state")
        table.add_column("Location", key="location")
        self.refresh_entries()
        self.set_interval(self.interval, self.refresh_entries)

    def action_refresh_now(self) -> None:
        self.refresh_entries()

    def refresh_entries(self) -> None:
        self.run_worker(self._poll_and_render(), exclusive=True, group="poll")

    async def _poll_and_render(self) -> None:
        entries = await self.run_worker_blocking(poll_once, self.user, self.entries)
        self.entries = sorted(entries, key=_sort_key)
        self._entries_by_key = {e.key: e for e in self.entries}
        self._render_table()

    async def run_worker_blocking(self, fn, *args):
        import asyncio

        return await asyncio.to_thread(fn, *args)

    def _render_table(self) -> None:
        table = self.query_one(DataTable)
        previous_cursor_key = None
        if table.row_count and table.cursor_row is not None:
            try:
                previous_cursor_key = table.coordinate_to_cell_key(Coordinate(table.cursor_row, 0)).row_key.value
            except Exception:
                previous_cursor_key = None

        table.clear()
        if not self.entries:
            table.add_row(Text("no known agent-kind processes found on this machine", style="dim"), "", "", "", "")
        for entry in self.entries:
            state_style = _STATE_STYLE.get(entry.state, "")
            table.add_row(
                entry.kind,
                str(entry.pid),
                _SURFACE_LABEL.get(entry.surface, entry.surface.value),
                Text(entry.state, style=state_style),
                _location(entry),
                key=entry.key,
            )

        if previous_cursor_key and previous_cursor_key in self._entries_by_key:
            try:
                row_index = table.get_row_index(previous_cursor_key)
                table.move_cursor(row=row_index)
            except Exception:
                pass

    def action_jump_selected(self) -> None:
        table = self.query_one(DataTable)
        if not table.row_count or table.cursor_row is None:
            return
        try:
            row_key = table.coordinate_to_cell_key(Coordinate(table.cursor_row, 0)).row_key.value
        except Exception:
            return
        entry = self._entries_by_key.get(row_key) if row_key else None
        if entry is None:
            return
        result = jump.jump_to(entry)
        self.notify(result.message, severity="information" if result.ok else "error", timeout=4)

    def on_data_table_row_selected(self, message: DataTable.RowSelected) -> None:
        self.action_jump_selected()


APP_HELP = """\
Interactive dashboard for every [bold]pi[/]/[bold]claude[/]/[bold]codex[/]/... session on
this machine: herdr panes, VS Code integrated terminals, and bare Ghostty tabs alike.

Arrow keys to move, [cyan]Enter[/] or click a row to jump to its window, [cyan]q[/] to quit.
"""

app = typer.Typer(
    help=APP_HELP,
    add_completion=False,
    context_settings={"help_option_names": ["-h", "--help"]},
    rich_markup_mode="rich",
)


def _version_callback(show_version: bool) -> None:
    if not show_version:
        return
    try:
        typer.echo(f"canopy {version('canopy')}")
    except PackageNotFoundError:
        typer.echo("canopy (version unknown, not installed as a package)")
    raise typer.Exit()


@app.command()
def main(
    interval: Annotated[float, typer.Option("--interval", help="Poll interval in seconds.")] = DEFAULT_INTERVAL_S,
    version_: Annotated[
        bool,
        typer.Option("--version", callback=_version_callback, is_eager=True, help="Show the version and exit."),
    ] = False,
) -> None:
    """Launch the dashboard."""
    CanopyApp(interval=interval).run()
