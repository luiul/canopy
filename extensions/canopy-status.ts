/**
 * canopy-status: writes this pi process's real agent-lifecycle state to a
 * small per-pid JSON file so canopy (https://github.com/luiul/canopy) can
 * read pi's actual working/idle/done state directly, instead of guessing it
 * from CPU usage the way it has to for every agent kind it doesn't own a
 * pty for (see canopy's internal/state and internal/pistatus packages).
 *
 * Install: symlink or copy this file to ~/.pi/agent/extensions/canopy-status.ts
 * (or .pi/extensions/canopy-status.ts for a single project). No config
 * needed; canopy's internal/pistatus package falls back to its CPU
 * heuristic automatically when this isn't installed.
 *
 * File written: ~/.pi/agent/canopy-status/<pid>.json
 *   { "pid": 12345, "cwd": "/path", "state": "working"|"idle"|"done", "updatedAt": "<ISO>" }
 *
 * State transitions:
 *   - before_agent_start / agent_start / tool_execution_start -> "working"
 *     (covers the prompt-just-submitted moment and every tool-call burst
 *     within a turn, not just the start of the very first one)
 *   - agent_settled (turn over, pi will not continue on its own) -> "idle"
 *     if this terminal is already frontmost (you're watching it), or
 *     "done" if it's not (finished while you were looking elsewhere) — the
 *     same frontmost-app check notifications.ts uses to suppress desktop
 *     notifications when you're already looking at this session.
 *   - while state is "done", a short poll (every 2s) watches for you to
 *     bring this terminal to the front and flips it to "idle" the instant
 *     you do, so a pane you've already checked doesn't sit "done" forever
 *     just because you never happened to send another prompt.
 *
 * "blocked" (pi stopped mid-task and needs a decision, e.g. a permission
 * prompt) isn't written here: vanilla pi has no built-in permission-gate/
 * confirm() pause to detect it from (see docs/usage.md — permission popups
 * are opt-in via extensions, not core). If a permission-gate extension is
 * added later, have its own tool_call handler write {state: "blocked"}
 * before awaiting ctx.ui.confirm and {state: "working"} after — canopy
 * already renders and sorts "blocked" ahead of everything else for any
 * State string, herdr-tracked or not, so no canopy-side change is needed
 * for that to show up correctly.
 *
 * macOS only (matches canopy's own AppleScript-based jump-to and
 * notifications.ts's focus detection); a no-op everywhere else.
 */

import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { promisify } from "node:util";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const execFileAsync = promisify(execFile);

const STATUS_DIR = path.join(os.homedir(), ".pi", "agent", "canopy-status");
const DONE_POLL_MS = 2000;

type State = "working" | "idle" | "done";

function statusFile(pid: number): string {
	return path.join(STATUS_DIR, `${pid}.json`);
}

function writeStatus(cwd: string, state: State) {
	try {
		fs.mkdirSync(STATUS_DIR, { recursive: true });
		const file = statusFile(process.pid);
		const tmp = `${file}.tmp`;
		fs.writeFileSync(
			tmp,
			JSON.stringify({ pid: process.pid, cwd, state, updatedAt: new Date().toISOString() }),
		);
		fs.renameSync(tmp, file); // same filesystem: canopy never reads a half-written file
	} catch {
		// Best-effort only: never let status reporting break the actual session.
	}
}

function removeStatus() {
	try {
		fs.unlinkSync(statusFile(process.pid));
	} catch {
		// Already gone, or never written (e.g. -p / --mode json / --mode rpc); fine either way.
	}
}

// Mirrors notifications.ts's terminal-focus detection: is *this* terminal
// app currently frontmost? Good enough to tell "done" (finished elsewhere)
// from "idle" (you were already looking at it) without tracking specific
// tabs/panes the way notifications.ts's iTerm2 branch does.
function myBundleId(): string {
	const env = process.env;
	if (env.ITERM_SESSION_ID) return "com.googlecode.iterm2";
	switch (env.TERM_PROGRAM) {
		case "Apple_Terminal":
			return "com.apple.Terminal";
		case "ghostty":
			return "com.mitchellh.ghostty";
		case "WarpTerminal":
			return "dev.warp.Warp-Stable";
		case "zed":
			return "dev.zed.Zed";
		case "vscode":
			return env.__CFBundleIdentifier || "com.microsoft.VSCode";
		default:
			return "";
	}
}

async function isFrontmost(): Promise<boolean> {
	const mine = myBundleId();
	if (!mine) return false; // unknown terminal: assume not watching, the safer default (report "done")
	try {
		const { stdout } = await execFileAsync(
			"osascript",
			[
				"-e",
				'tell application "System Events" to get bundle identifier of first process whose frontmost is true',
			],
			{ timeout: 1500 },
		);
		return stdout.trim() === mine;
	} catch {
		return false;
	}
}

export default function (pi: ExtensionAPI) {
	if (process.platform !== "darwin") return;

	let enabled = true;
	let doneWatch: ReturnType<typeof setInterval> | undefined;

	const stopDoneWatch = () => {
		if (doneWatch) {
			clearInterval(doneWatch);
			doneWatch = undefined;
		}
	};

	const working = (ctx: ExtensionContext) => {
		stopDoneWatch();
		if (enabled) writeStatus(ctx.cwd, "working");
	};

	const settled = (ctx: ExtensionContext) => {
		if (!enabled) return;
		void isFrontmost().then((frontmost) => {
			if (frontmost) {
				writeStatus(ctx.cwd, "idle");
				return;
			}
			writeStatus(ctx.cwd, "done");
			doneWatch = setInterval(() => {
				void isFrontmost().then((nowFrontmost) => {
					if (nowFrontmost) {
						writeStatus(ctx.cwd, "idle");
						stopDoneWatch();
					}
				});
			}, DONE_POLL_MS);
			doneWatch.unref?.();
		});
	};

	pi.on("session_start", async (_event, ctx) => writeStatus(ctx.cwd, "idle"));
	pi.on("before_agent_start", async (_event, ctx) => working(ctx));
	pi.on("agent_start", async (_event, ctx) => working(ctx));
	pi.on("tool_execution_start", async (_event, ctx) => working(ctx));
	pi.on("agent_settled", async (_event, ctx) => settled(ctx));

	pi.on("session_shutdown", async () => {
		stopDoneWatch();
		removeStatus();
	});
	process.on("exit", removeStatus);

	pi.registerCommand("canopy-status", {
		description: "Toggle writing canopy's ~/.pi/agent/canopy-status/<pid>.json status file",
		handler: async (_args, ctx) => {
			enabled = !enabled;
			if (!enabled) {
				stopDoneWatch();
				removeStatus();
			} else {
				writeStatus(ctx.cwd, "idle");
			}
			ctx.ui.notify(enabled ? "canopy-status enabled" : "canopy-status disabled", "info");
		},
	});
}
