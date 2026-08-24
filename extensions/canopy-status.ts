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
 *     within a turn, not just the start of the very first one), plus a
 *     recurring heartbeat (every WORKING_HEARTBEAT_MS) that keeps
 *     rewriting "working" for as long as it stays true. Those three events
 *     each fire once, not repeatedly, so without the heartbeat a single
 *     slow tool call or turn (a long bash command, a web fetch, a
 *     subagent, a slow LLM response) would let the status file's
 *     timestamp go stale past canopy's own internal/pistatus.MaxAge (10s)
 *     mid-turn, and canopy would silently fall back to its CPU heuristic
 *     for this pid — which reads any I/O-bound wait as "idle", showing
 *     "idle" while pi is still genuinely working, with nothing to correct
 *     it short of the next before_agent_start/agent_start/
 *     tool_execution_start.
 *   - agent_settled (turn over, pi will not continue on its own) -> "done",
 *     unconditionally. No focus/frontmost detection here at all: canopy's
 *     own dashboard already treats "done" as sticky (see
 *     docs/agent-state-machine.md in the canopy repo) and requires an
 *     explicit enter or c on the row in canopy itself before it displays
 *     anything else, regardless of whether the user was already looking
 *     at this exact terminal when the turn ended. Trying to guess that
 *     here (this extension used to run its own osascript-based
 *     frontmost/window-title check) couldn't change what canopy displays
 *     either way, so it was pure complexity with no payoff — dropped.
 *
 * macOS only (matches canopy's own AppleScript-based jump-to); a no-op
 * everywhere else, same as before.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const STATUS_DIR = path.join(os.homedir(), ".pi", "agent", "canopy-status");
// Must stay comfortably under canopy's internal/pistatus.MaxAge (10s): that's
// how long canopy trusts this file before falling back to its CPU heuristic,
// which reads any I/O-bound work (a slow bash command, a web fetch, a
// subagent, an LLM still streaming) as "idle" (see internal/state's own doc
// comment: "idling on a prompt or a slow network read does not [show CPU]").
// A single tool call or turn can easily run past 10s, so "working" needs a
// heartbeat refreshing this file's timestamp for the whole time it's true,
// not just a write at the start — see the workingWatch interval below.
const WORKING_HEARTBEAT_MS = 3000;

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

export default function (pi: ExtensionAPI) {
	if (process.platform !== "darwin") return;

	let enabled = true;
	let workingWatch: ReturnType<typeof setInterval> | undefined;

	const stopWorkingWatch = () => {
		if (workingWatch) {
			clearInterval(workingWatch);
			workingWatch = undefined;
		}
	};

	const working = (ctx: ExtensionContext) => {
		stopWorkingWatch();
		if (!enabled) return;
		writeStatus(ctx.cwd, "working");
		// See WORKING_HEARTBEAT_MS: before_agent_start/agent_start/
		// tool_execution_start each fire once, not repeatedly, so without this
		// the status file's timestamp goes stale mid-turn on anything slower
		// than pistatus.MaxAge and canopy silently starts guessing from CPU
		// instead, showing "idle" while pi is still genuinely working.
		workingWatch = setInterval(() => writeStatus(ctx.cwd, "working"), WORKING_HEARTBEAT_MS);
		workingWatch.unref?.();
	};

	const settled = (ctx: ExtensionContext) => {
		stopWorkingWatch();
		if (!enabled) return;
		writeStatus(ctx.cwd, "done");
	};

	pi.on("session_start", async (_event, ctx) => writeStatus(ctx.cwd, "idle"));
	pi.on("before_agent_start", async (_event, ctx) => working(ctx));
	pi.on("agent_start", async (_event, ctx) => working(ctx));
	pi.on("tool_execution_start", async (_event, ctx) => working(ctx));
	pi.on("agent_settled", async (_event, ctx) => settled(ctx));

	pi.on("session_shutdown", async () => {
		stopWorkingWatch();
		removeStatus();
	});
	process.on("exit", removeStatus);

	pi.registerCommand("canopy-status", {
		description: "Toggle writing canopy's ~/.pi/agent/canopy-status/<pid>.json status file",
		handler: async (_args, ctx) => {
			enabled = !enabled;
			if (!enabled) {
				stopWorkingWatch();
				removeStatus();
			} else {
				writeStatus(ctx.cwd, "idle");
			}
			ctx.ui.notify(enabled ? "canopy-status enabled" : "canopy-status disabled", "info");
		},
	});
}
