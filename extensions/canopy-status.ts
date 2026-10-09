/**
 * canopy-status: writes this pi process's real state to a small per-pid
 * JSON file so canopy (https://github.com/luiul/canopy) can read pi's
 * actual working/blocked/done/error/idle state directly (see canopy's
 * internal/pistatus package).
 *
 * The state machine below mirrors pi's own program-status reporter (pi
 * v1.1.0, docs/terminal-setup.md#program-status, source at
 * packages/coding-agent/src/modes/interactive/program-status-reporter.ts)
 * transition for transition, fed by the same session events pi's reporter
 * uses, all reachable through the public extension API:
 *
 *   state    | when (pi semantics, mirrored here)
 *   ---------+------------------------------------------------------------
 *   working  | agent_start (a run is active), or session_before_compact
 *            | (compaction in progress; message "Compacting context").
 *            | Message: the session name.
 *   blocked  | ui_prompt_start (an extension dialog is waiting for input;
 *            | the runner emits this for select/confirm/input/editor/
 *            | custom, already collapsed to the outermost prompt).
 *            | Cleared by ui_prompt_end. Message: the dialog title.
 *   done     | agent_settled, not aborted, last assistant message not an
 *            | error. Message: the session name.
 *   error    | agent_settled where the last assistant message_end had
 *            | stopReason "error" (not retried). Message: first line of
 *            | the error.
 *   idle     | agent_settled aborted (Escape), or session start.
 *
 * Priority when several apply at once (same as pi): blocked > compacting
 * > working > resting (done/error/idle).
 *
 * Fixes this brings over the previous hand-rolled version: an Escape-
 * aborted run no longer reads "done" (it reads "idle"), an unretried
 * error no longer reads "done" (it reads "error", which canopy renders
 * sticky with a bell exactly like "done"), and an open extension dialog
 * is visible at all ("blocked" — before, canopy could only guess).
 *
 * Known gap, same as pi's own reporter's reach: pi's *internal* login
 * flow marks itself blocked without any extension event, so canopy reads
 * "idle" while a login prompt is up. Accepted.
 *
 * Install: symlink or copy this file to ~/.pi/agent/extensions/canopy-status.ts
 * (or .pi/extensions/canopy-status.ts for a single project). No config
 * needed; canopy reads "unknown" for any pi process without it.
 *
 * Files written under ~/.pi/agent/canopy-status/:
 *   <pid>.json: { "pid": 12345, "cwd": "/path", "state": "working"|"blocked"|"done"|"error"|"idle",
 *                 "message": "<session name|dialog title|error first line>",
 *                 "detail": "<last tool call|last assistant line>",
 *                 "task": "<first prompt of the session>", "updatedAt": "<ISO>" }
 *   <pid>.model.json: { "pid": 12345, "model": "GPT-6 Sol", "provider": "ai-model-router", "updatedAt": "<ISO>" }
 * message is omitted when there is none (idle, or no session name set).
 * detail and task are canopy's own enrichment (pi's reporter has no
 * equivalent): detail is the session's rolling activity signal — the last
 * tool call while working ("edit: internal/tui/rows.go"), the first
 * non-empty line of the latest assistant text at settle (pi's own
 * turn-end summary) — and task is the first prompt of the session, the
 * identity an unnamed session otherwise lacks. Both ride the exact same
 * writes as state/message (transitions, heartbeat, one-shot settle);
 * canopy composes the per-state display line from message/detail/task.
 * Events feeding them: tool_call (observe-only), message_end (assistant
 * text), before_agent_start (the prompt).
 * The model has its own timestamp: changing or refreshing it must not
 * look like a new state transition to canopy's done/bell logic.
 *
 * Heartbeats and staleness: canopy's internal/pistatus.MaxAge (10s) stops
 * trusting a status file that stops being refreshed, so a state that can
 * legitimately sit unchanged for minutes (working through one slow tool
 * call, blocked on a dialog while the user is away, idle on a prompt)
 * needs its timestamp rewritten for as long as it stays true — the 3s
 * heartbeat below rewrites the current computed status, but only while it
 * is working/blocked/idle. done and error are written exactly once, at
 * the transition: canopy latches them into its sticky episode overlay the
 * moment they appear (internal/tui's done.go), and internal/pistatus
 * exempts them from MaxAge, so a one-shot write keeps reading done/error
 * for as long as the process lives. Rewriting them on a timer would only
 * move updatedAt forward, which is canopy's one identity anchor for
 * telling a genuinely new settle apart from the same one repeating — a
 * refreshed done would look like a brand-new done and re-ring the bell
 * after every acknowledgment.
 *
 * Only interactive sessions report. SDK subagents share process.pid with
 * their parent, so letting them write would replace the parent's records.
 * Print, JSON, and RPC sessions must not write or remove those files.
 *
 * Older pi versions: the events this relies on that predate v1.1.0 simply
 * never fire there (an EventEmitter accepts any name at registration), so
 * the file keeps the states those versions can produce — no version gate,
 * graceful degradation.
 *
 * macOS only (same as canopy itself); a no-op everywhere else.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const STATUS_DIR = path.join(os.homedir(), ".pi", "agent", "canopy-status");
// Must stay comfortably under canopy's internal/pistatus.MaxAge (10s): that's
// how long canopy trusts a working/blocked/idle file before reading
// "unknown" instead (see the heartbeat note in the header).
const STATUS_HEARTBEAT_MS = 3000;
// Keep the selected model available even if pi sits idle long after the
// state file's 10s freshness window. Stay below pistatus.ModelMaxAge (90s).
const MODEL_HEARTBEAT_MS = 30000;

type State = "working" | "blocked" | "done" | "error" | "idle";

// Status is one computed report: the state plus its optional message (see
// the header for which states carry one) plus canopy's own optional
// detail/task enrichment (see the header for what they carry).
interface Status {
	state: State;
	message?: string;
	detail?: string;
	task?: string;
}

// firstLine mirrors pi's reporter: an error report's message is the first
// line of the error text, never the whole thing.
function firstLine(text: string | undefined): string {
	return text?.split(/\r?\n/, 1)[0]?.trim() || "Error";
}

// clip bounds a detail/task fragment to one line and a sane length: the
// TUI truncates to the terminal width itself, so this only keeps the file
// small and free of embedded newlines.
function clip(text: string | undefined): string | undefined {
	const line = text?.split(/\r?\n/).find((l) => l.trim() !== "")?.trim();
	if (!line) return undefined;
	return line.length > 100 ? line.slice(0, 99) + "…" : line;
}

// activityLabel renders one tool call as a short activity fragment:
// "edit: internal/tui/rows.go", "bash: go test ./...", "grep: displayMessage".
// The target is whichever conventional argument the tool carries (pi's
// built-ins: command for bash, path for read/edit/write/ls, pattern for
// grep/find); anything else (MCP tools, extension tools) shows just the
// tool name. Paths get the cwd prefix stripped: that prefix is already
// the row's Location cell, repeating it would only eat the line.
function activityLabel(toolName: string, input: unknown, cwd: string): string {
	const args = input as Record<string, unknown> | undefined;
	const str = (key: string): string | undefined =>
		typeof args?.[key] === "string" ? (args[key] as string) : undefined;
	let target = str("path") ?? str("command") ?? str("pattern") ?? str("query") ?? str("url");
	if (!target) return toolName;
	if (str("path") && cwd && target.startsWith(cwd + "/")) target = target.slice(cwd.length + 1);
	return `${toolName}: ${firstLine(target)}`;
}

// firstTextLine extracts the first non-empty line of an assistant
// message's text content: pi's final message in a turn usually *is* the
// one-line summary of what it did, which is exactly what a done row
// should show.
function firstTextLine(message: unknown): string | undefined {
	const m = message as { content?: Array<{ type?: string; text?: string }> };
	for (const block of m.content ?? []) {
		if (block.type === "text") {
			const line = clip(block.text);
			if (line) return line;
		}
	}
	return undefined;
}

function statusFile(pid: number): string {
	return path.join(STATUS_DIR, `${pid}.json`);
}

function modelFile(pid: number): string {
	return path.join(STATUS_DIR, `${pid}.model.json`);
}

// This is the selected model, not a physical model chosen for one request
// by a virtual-model router.
type SelectedModel = NonNullable<ExtensionContext["model"]>;

function writeModel(model: SelectedModel | undefined) {
	if (!model?.name || !model.provider) {
		removeModel();
		return;
	}
	try {
		fs.mkdirSync(STATUS_DIR, { recursive: true });
		const file = modelFile(process.pid);
		const tmp = `${file}.tmp`;
		fs.writeFileSync(
			tmp,
			JSON.stringify({ pid: process.pid, model: model.name, provider: model.provider, updatedAt: new Date().toISOString() }),
		);
		fs.renameSync(tmp, file);
	} catch {
		// Best-effort only: never let model reporting break the session.
	}
}

function removeModel() {
	try {
		fs.unlinkSync(modelFile(process.pid));
	} catch {
		// Already gone, or no model was selected.
	}
}

function writeStatus(cwd: string, status: Status) {
	try {
		fs.mkdirSync(STATUS_DIR, { recursive: true });
		const file = statusFile(process.pid);
		const tmp = `${file}.tmp`;
		fs.writeFileSync(
			tmp,
			JSON.stringify({ pid: process.pid, cwd, state: status.state, message: status.message, detail: status.detail, task: status.task, updatedAt: new Date().toISOString() }),
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
	let ownsStatus = false;
	let statusWatch: ReturnType<typeof setInterval> | undefined;
	let modelWatch: ReturnType<typeof setInterval> | undefined;
	let selectedModel: SelectedModel | undefined;

	// The mirrored reporter state (see the header's transition table).
	let runActive = false;
	let compacting = false;
	// Outcome of the current run, reported once it settles (pi's runResult).
	let runResult: Status = { state: "done" };
	// Status while no run is active (pi's restingStatus).
	let resting: Status = { state: "idle" };
	// Title of the extension dialog currently waiting for input, if any.
	let blockedTitle: string | undefined;
	// The rolling enrichment signals (see the header): the last tool call,
	// the first non-empty line of the latest assistant text, and the first
	// prompt of the session. All three reset on session_start, exactly like
	// the mirrored run state.
	let lastActivity: string | undefined;
	let lastOutcome: string | undefined;
	let task: string | undefined;
	// The last context any event arrived with, so the heartbeat can
	// recompute the current status (and refresh the session name inside a
	// working/done message) without an event of its own.
	let activeCtx: ExtensionContext | undefined;
	// Dedup for event-driven writes: identical consecutive reports don't
	// rewrite the file. The heartbeat bypasses this on purpose — refreshing
	// updatedAt is its whole job — but never for done/error (see header).
	let lastWritten: string | undefined;

	const sessionName = (): string | undefined => {
		try {
			return pi.getSessionName?.() || undefined;
		} catch {
			return undefined; // older hosts without getSessionName
		}
	};

	// currentStatus mirrors pi's currentStatus(): blocked outranks
	// compacting outranks an active run, and the resting status carries the
	// session name when it's done (pi attaches it to working and done).
	// The detail field then adds canopy's rolling activity signal per state
	// (blocked/error carry all their content in message; idle carries only
	// the task), and task rides every write: it's session identity, not
	// state payload, and it must survive a settle overwriting detail.
	const currentStatus = (): Status => {
		const withEnrichment = (s: Status): Status => ({ ...s, detail: currentDetail(s.state), task });
		if (blockedTitle !== undefined) return withEnrichment({ state: "blocked", message: blockedTitle });
		if (compacting) return withEnrichment({ state: "working", message: "Compacting context" });
		if (runActive) return withEnrichment({ state: "working", message: sessionName() });
		if (resting.state === "done") return withEnrichment({ ...resting, message: sessionName() });
		return withEnrichment(resting);
	};

	// currentDetail picks the rolling signal that belongs to a state: the
	// last tool call while working (the task until the first tool call
	// lands — a fresh run's only known activity), the latest assistant
	// line once settled (pi's own turn-end summary), nothing for the two
	// states whose message already says it all.
	const currentDetail = (state: State): string | undefined => {
		switch (state) {
			case "working":
				return lastActivity;
			case "done":
				return lastOutcome ?? lastActivity;
			default:
				return undefined;
		}
	};

	const startStatusWatch = () => {
		if (statusWatch) clearInterval(statusWatch);
		statusWatch = setInterval(() => {
			if (!ownsStatus || !enabled || !activeCtx) return;
			const current = currentStatus();
			// done/error are one-shot writes (see the header): canopy latches
			// them, pistatus never lets them go stale, and a refreshed
			// updatedAt would only impersonate a brand-new settle to canopy's
			// done/bell identity check.
			if (current.state === "done" || current.state === "error") return;
			writeStatus(activeCtx.cwd, current);
		}, STATUS_HEARTBEAT_MS);
		statusWatch.unref?.();
	};

	const stopStatusWatch = () => {
		if (statusWatch) {
			clearInterval(statusWatch);
			statusWatch = undefined;
		}
	};

	const startModelWatch = () => {
		if (modelWatch) clearInterval(modelWatch);
		modelWatch = setInterval(() => writeModel(selectedModel), MODEL_HEARTBEAT_MS);
		modelWatch.unref?.();
	};

	const stopModelWatch = () => {
		if (modelWatch) {
			clearInterval(modelWatch);
			modelWatch = undefined;
		}
	};

	// update recomputes the current status after an event and writes it if
	// it changed. done/error go through here too — the transition itself is
	// exactly their one allowed write.
	const update = (ctx: ExtensionContext) => {
		activeCtx = ctx;
		if (!ownsStatus || !enabled) return;
		const current = currentStatus();
		const key = JSON.stringify(current);
		if (key === lastWritten) return;
		lastWritten = key;
		writeStatus(ctx.cwd, current);
	};

	const cleanup = () => {
		if (!ownsStatus) return;
		stopStatusWatch();
		stopModelWatch();
		removeStatus();
		removeModel();
		ownsStatus = false;
		process.off("exit", cleanup);
	};

	pi.on("session_start", async (_event, ctx) => {
		// Older hosts may omit mode; hasUI still excludes SDK subagents.
		if (!ctx.hasUI || (ctx.mode !== undefined && ctx.mode !== "tui")) return;
		if (!ownsStatus) {
			ownsStatus = true;
			process.once("exit", cleanup);
		}
		// A fresh session forgets the previous session's run (pi's reset()).
		runActive = false;
		compacting = false;
		runResult = { state: "done" };
		resting = { state: "idle" };
		blockedTitle = undefined;
		lastActivity = undefined;
		lastOutcome = undefined;
		task = undefined;
		lastWritten = undefined;
		activeCtx = ctx;
		if (!enabled) return;
		writeStatus(ctx.cwd, currentStatus());
		lastWritten = JSON.stringify(currentStatus());
		selectedModel = ctx.model;
		writeModel(selectedModel);
		startModelWatch();
		startStatusWatch();
	});
	pi.on("model_select", async (event) => {
		if (!ownsStatus) return;
		selectedModel = event.model;
		if (enabled) writeModel(selectedModel);
	});

	pi.on("agent_start", async (_event, ctx) => {
		runActive = true;
		runResult = { state: "done" };
		update(ctx);
	});
	pi.on("before_agent_start", async (event, _ctx) => {
		// The FIRST prompt of the session is the task (identity): later
		// prompts ("continue", "go ahead") say nothing about what the
		// session is for, and a subagent's before_agent_start must never
		// overwrite the user's own prompt. No update() here: agent_start
		// fires immediately after and writes.
		if (task === undefined) task = clip((event as { prompt?: string }).prompt);
	});
	pi.on("tool_call", async (event, ctx) => {
		// Observe-only (no block/mutate): the tool call is the session's
		// live activity signal, written on the spot so canopy's working
		// rows show what is happening without waiting for the heartbeat.
		const e = event as { toolName?: string; input?: unknown };
		if (e.toolName) lastActivity = clip(activityLabel(e.toolName, e.input, ctx.cwd));
		update(ctx);
	});
	pi.on("message_end", async (event, ctx) => {
		// The latest response decides the outcome, so a retried error is
		// replaced by its successful retry (pi's exact rule).
		const message = event.message as { role?: string; stopReason?: string; errorMessage?: string };
		if (message.role !== "assistant") return;
		runResult = message.stopReason === "error" ? { state: "error", message: firstLine(message.errorMessage) } : { state: "done" };
		if (message.stopReason !== "error") {
			const line = firstTextLine(message);
			if (line) lastOutcome = line;
		}
		update(ctx);
	});
	pi.on("session_before_compact", async (_event, ctx) => {
		compacting = true;
		update(ctx);
	});
	pi.on("session_compact", async (event, ctx) => {
		compacting = false;
		if (!runActive && event.reason === "manual") {
			resting = { state: "done" };
		}
		update(ctx);
	});
	pi.on("session_compact_failed", async (event, ctx) => {
		compacting = false;
		if (event.aborted) {
			// A failed recovery compaction ends the run unless a later
			// response succeeds (pi's exact rule).
			if (runActive) runResult = { state: "idle" };
			else resting = { state: "idle" };
		} else if (event.errorMessage) {
			if (runActive) runResult = { state: "error", message: firstLine(event.errorMessage) };
			else if (event.reason === "manual") resting = { state: "error", message: firstLine(event.errorMessage) };
		}
		update(ctx);
	});
	pi.on("agent_settled", async (event, ctx) => {
		runActive = false;
		compacting = false; // backstop: no compaction outlives its run
		resting = event.aborted ? { state: "idle" } : runResult;
		update(ctx);
	});
	pi.on("ui_prompt_start", async (event, ctx) => {
		blockedTitle = event.title ?? event.kind;
		update(ctx);
	});
	pi.on("ui_prompt_end", async (_event, ctx) => {
		blockedTitle = undefined;
		update(ctx);
	});
	pi.on("session_info_changed", async (_event, ctx) => {
		// The session name is part of working and done reports.
		update(ctx);
	});

	pi.on("session_shutdown", async () => cleanup());

	pi.registerCommand("canopy-status", {
		description: "Toggle writing canopy's ~/.pi/agent/canopy-status/<pid>.json status file",
		handler: async (_args, ctx) => {
			if (!ownsStatus) {
				ctx.ui.notify("canopy-status is available only in interactive sessions", "info");
				return;
			}
			enabled = !enabled;
			if (!enabled) {
				stopStatusWatch();
				stopModelWatch();
				removeStatus();
				removeModel();
			} else {
				activeCtx = ctx;
				lastWritten = undefined;
				update(ctx);
				selectedModel = ctx.model;
				writeModel(selectedModel);
				startModelWatch();
				startStatusWatch();
			}
			ctx.ui.notify(enabled ? "canopy-status enabled" : "canopy-status disabled", "info");
		},
	});
}
