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
 *   - agent_settled (turn over, pi will not continue on its own) -> "idle"
 *     if this terminal is already frontmost (you're watching it), or
 *     "done" if it's not (finished while you were looking elsewhere) — the
 *     same frontmost-app check notifications.ts uses to suppress desktop
 *     notifications when you're already looking at this session. Frontmost
 *     is checked at the app level first (cheap, needs no extra permission),
 *     then, where possible, narrowed to the specific window/tab so bringing
 *     an *unrelated* window of the same app to the front (a different VS
 *     Code project, another Ghostty tab, ...) doesn't also mark every other
 *     "done" pi session in that app "idle". iTerm2 gets an exact match (its
 *     active session id, same as notifications.ts); everything else falls
 *     back to a window-title heuristic that requires the Accessibility
 *     permission (System Settings -> Privacy & Security -> Accessibility,
 *     allow your terminal) — without it, or for an app we have no window
 *     lookup for, this degrades to the plain app-level check instead of
 *     getting stuck reporting "done" forever.
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
 * State string for any agent kind, so no canopy-side change is needed
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

interface TermInfo {
	bundleId: string;
	// System Events process name, used only for the window-title fallback
	// below; left empty for terminals we have no mapping for (isFrontmost
	// then stops at the app-level check, same as before this existed).
	processName: string;
	// iTerm2 only: lets us tell tabs/panes apart exactly instead of guessing
	// from a window title (mirrors notifications.ts's iTerm2 branch).
	itermSessionId?: string;
}

// Mirrors notifications.ts's terminal-focus detection: is *this* terminal
// app currently frontmost? Good enough to tell "done" (finished elsewhere)
// from "idle" (you were already looking at it) for the common case of one
// window per app. See isFrontmost below for how multiple windows of the
// same app (two VS Code projects, two Ghostty tabs, ...) get disambiguated.
function termInfo(): TermInfo {
	const env = process.env;
	if (env.ITERM_SESSION_ID) {
		return { bundleId: "com.googlecode.iterm2", processName: "iTerm2", itermSessionId: env.ITERM_SESSION_ID };
	}
	switch (env.TERM_PROGRAM) {
		case "Apple_Terminal":
			return { bundleId: "com.apple.Terminal", processName: "Terminal" };
		case "ghostty":
			return { bundleId: "com.mitchellh.ghostty", processName: "Ghostty" };
		case "WarpTerminal":
			return { bundleId: "dev.warp.Warp-Stable", processName: "Warp" };
		case "zed":
			return { bundleId: "dev.zed.Zed", processName: "Zed" };
		case "vscode": {
			const bundle = env.__CFBundleIdentifier || "com.microsoft.VSCode";
			const processName = bundle === "com.todesktop.230313mzl4w4u92" ? "Cursor" : "Code";
			return { bundleId: bundle, processName };
		}
		default:
			return { bundleId: "", processName: "" };
	}
}

async function frontmostBundleId(): Promise<string> {
	try {
		const { stdout } = await execFileAsync(
			"osascript",
			[
				"-e",
				'tell application "System Events" to get bundle identifier of first process whose frontmost is true',
			],
			{ timeout: 1500 },
		);
		return stdout.trim();
	} catch {
		return "";
	}
}

// The window-title heuristic's positive signal: terminal/editor windows are
// conventionally titled after the folder they're in (VS Code's workspace
// name, Ghostty/Terminal's default title, ...). Not authoritative — just
// good enough to tell "this window" from "some other window of the same
// app", which is all isFrontmost needs from it.
function windowHint(cwd: string): string {
	return path.basename(cwd);
}

// Requires the calling process to have the Accessibility permission (System
// Settings -> Privacy & Security -> Accessibility); throws without it, or
// if the app doesn't expose window titles the way System Events expects.
// Callers must treat that as "can't disambiguate", not "not frontmost".
async function frontWindowTitle(processName: string): Promise<string> {
	const { stdout } = await execFileAsync(
		"osascript",
		["-e", `tell application "System Events" to tell process "${processName}" to get name of front window`],
		{ timeout: 1500 },
	);
	return stdout.trim();
}

// Is the terminal running *this* pi process the one you're actually looking
// at? App-level frontmost is checked first: cheap, needs no extra macOS
// permission, and the only signal available for terminals we can't look
// inside. But it's app-wide, so on its own it conflates every window of
// that app, bringing an unrelated VS Code project or Ghostty tab to the
// front would wrongly flip *every* "done" pi session running under that
// same app to "idle", not just the one you actually brought forward. Where
// possible this narrows to the specific window/tab instead: exactly for
// iTerm2 (active session id), heuristically (window title) for everything
// else we have a process-name mapping for. Either narrowing step falls back
// to the plain app-level result if it can't run (no Accessibility
// permission, unmapped app, ...) rather than getting stuck reporting "done"
// forever.
async function isFrontmost(cwd: string): Promise<boolean> {
	const term = termInfo();
	if (!term.bundleId) return false; // unknown terminal: assume not watching, the safer default (report "done")
	if ((await frontmostBundleId()) !== term.bundleId) return false; // a different app entirely is focused

	if (term.itermSessionId) {
		try {
			const { stdout } = await execFileAsync(
				"osascript",
				["-e", 'tell application "iTerm2" to tell current session of current window to return id'],
				{ timeout: 1500 },
			);
			return (term.itermSessionId.split(":").pop() ?? "") === stdout.trim();
		} catch {
			return true; // couldn't disambiguate tabs; fall back to the app-level match
		}
	}

	if (term.processName) {
		try {
			return (await frontWindowTitle(term.processName)).includes(windowHint(cwd));
		} catch {
			return true; // no Accessibility permission (or unsupported app); fall back to the app-level match
		}
	}

	return true;
}

export default function (pi: ExtensionAPI) {
	if (process.platform !== "darwin") return;

	let enabled = true;
	let doneWatch: ReturnType<typeof setInterval> | undefined;
	let workingWatch: ReturnType<typeof setInterval> | undefined;

	const stopDoneWatch = () => {
		if (doneWatch) {
			clearInterval(doneWatch);
			doneWatch = undefined;
		}
	};

	const stopWorkingWatch = () => {
		if (workingWatch) {
			clearInterval(workingWatch);
			workingWatch = undefined;
		}
	};

	const working = (ctx: ExtensionContext) => {
		stopDoneWatch();
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
		// Guard against a second agent_settled landing before a prior one's
		// isFrontmost() check resolves (e.g. no working() in between to reset
		// it): without this, the old interval leaks (its reference is dropped
		// below, but nothing ever clears it) and keeps polling forever.
		stopWorkingWatch();
		stopDoneWatch();
		if (!enabled) return;
		void isFrontmost(ctx.cwd).then((frontmost) => {
			if (frontmost) {
				writeStatus(ctx.cwd, "idle");
				return;
			}
			writeStatus(ctx.cwd, "done");
			doneWatch = setInterval(() => {
				void isFrontmost(ctx.cwd).then((nowFrontmost) => {
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
		stopWorkingWatch();
		stopDoneWatch();
		removeStatus();
	});
	process.on("exit", removeStatus);

	pi.registerCommand("canopy-status", {
		description: "Toggle writing canopy's ~/.pi/agent/canopy-status/<pid>.json status file",
		handler: async (_args, ctx) => {
			enabled = !enabled;
			if (!enabled) {
				stopWorkingWatch();
				stopDoneWatch();
				removeStatus();
			} else {
				writeStatus(ctx.cwd, "idle");
			}
			ctx.ui.notify(enabled ? "canopy-status enabled" : "canopy-status disabled", "info");
		},
	});
}
