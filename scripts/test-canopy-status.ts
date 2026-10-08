import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { mock, test } from "node:test";
import type { TestContext } from "node:test";

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

type Handler = (event: any, ctx: ExtensionContext) => unknown;

class MockPi {
	handlers = new Map<string, Handler[]>();
	commands = new Map<string, { handler: Handler }>();
	sessionName: string | undefined = "test session";

	on(event: string, handler: Handler) {
		const handlers = this.handlers.get(event) ?? [];
		handlers.push(handler);
		this.handlers.set(event, handlers);
	}

	registerCommand(name: string, command: { handler: Handler }) {
		this.commands.set(name, command);
	}

	getSessionName() {
		return this.sessionName;
	}

	async fire(event: string, ctx: ExtensionContext, payload: unknown = {}) {
		for (const handler of this.handlers.get(event) ?? []) await handler(payload, ctx);
	}
}

const mainModel = { name: "GPT-6.1 Sol", provider: "ai-model-router" };
const helperModel = { name: "GPT-6 Luna", provider: "ai-model-router" };

function context(mode: string | undefined, hasUI: boolean, model = mainModel): ExtensionContext {
	return { mode, hasUI, cwd: "/projects/canopy", model, ui: { notify() {} } } as unknown as ExtensionContext;
}

async function harness(t: TestContext) {
	const home = fs.mkdtempSync(path.join(os.tmpdir(), "canopy-status-test-"));
	const oldHome = process.env.HOME;
	process.env.HOME = home;
	const { default: canopyStatus } = await import(`../extensions/canopy-status.ts?${path.basename(home)}`);
	const oldListeners = new Set(process.listeners("exit"));
	const timers = new Set<{ tick: () => void; unref: () => void }>();
	mock.method(globalThis, "setInterval", (tick: () => void) => {
		const timer = { tick, unref() {} };
		timers.add(timer);
		return timer;
	});
	mock.method(globalThis, "clearInterval", (timer: any) => timers.delete(timer));
	t.after(() => {
		mock.restoreAll();
		for (const listener of process.listeners("exit")) {
			if (!oldListeners.has(listener)) process.off("exit", listener);
		}
		if (oldHome === undefined) delete process.env.HOME;
		else process.env.HOME = oldHome;
		fs.rmSync(home, { recursive: true, force: true });
	});
	const dir = path.join(home, ".pi", "agent", "canopy-status");
	const status = path.join(dir, `${process.pid}.json`);
	const model = path.join(dir, `${process.pid}.model.json`);
	const create = () => {
		const pi = new MockPi();
		canopyStatus(pi as unknown as ExtensionAPI);
		return pi;
	};
	return { create, status, model, timers, oldListeners };
}

function readStatus(file: string): { state: string; message?: string; updatedAt: string } {
	return JSON.parse(fs.readFileSync(file, "utf8"));
}

test("interactive session owns status, model, heartbeats, and cleanup", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	assert.equal(process.listeners("exit").length, h.oldListeners.size, "factory must not add exit handlers");
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	assert.equal(readStatus(h.status).state, "idle");
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, mainModel.name);
	assert.equal(h.timers.size, 2); // status heartbeat + model heartbeat
	await pi.fire("agent_start", ctx);
	assert.deepEqual(readStatus(h.status), {
		pid: process.pid,
		cwd: "/projects/canopy",
		state: "working",
		message: "test session",
		updatedAt: readStatus(h.status).updatedAt,
	});
	const stateBeforeModel = fs.readFileSync(h.status, "utf8");
	await pi.fire("model_select", ctx, { model: helperModel });
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, helperModel.name);
	assert.equal(fs.readFileSync(h.status, "utf8"), stateBeforeModel);
	for (const timer of h.timers) timer.tick();
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, helperModel.name);
	// The status heartbeat refreshes a live working write's timestamp.
	assert.ok(Date.now() - Date.parse(readStatus(h.status).updatedAt) < 1000);
	await pi.fire("agent_settled", ctx, { aborted: false });
	assert.equal(readStatus(h.status).state, "done");
	await pi.fire("session_shutdown", ctx);
	await pi.fire("session_shutdown", ctx);
	assert.equal(fs.existsSync(h.status), false);
	assert.equal(fs.existsSync(h.model), false);
	assert.equal(h.timers.size, 0);
	assert.equal(process.listeners("exit").length, h.oldListeners.size);
});

test("SDK helpers and non-interactive sessions never touch the parent's records", async (t) => {
	const h = await harness(t);
	const parent = h.create();
	const ctx = context("tui", true);
	await parent.fire("session_start", ctx);
	await parent.fire("agent_start", ctx);
	const initialStatus = fs.readFileSync(h.status, "utf8");
	const initialModel = fs.readFileSync(h.model, "utf8");
	const initialTimers = h.timers.size;
	const initialListeners = process.listeners("exit").length;
	for (const childCtx of [context("print", false, helperModel), context("json", false, helperModel), context("rpc", true, helperModel), context(undefined, false, helperModel)]) {
		const child = h.create();
		await child.fire("session_start", childCtx);
		await child.fire("model_select", childCtx, { model: helperModel });
		for (const event of ["before_agent_start", "agent_start", "tool_execution_start", "agent_settled"]) {
			await child.fire(event, childCtx, { aborted: false });
		}
		await child.commands.get("canopy-status")!.handler("", childCtx);
		await child.fire("session_shutdown", childCtx);
		assert.equal(fs.readFileSync(h.status, "utf8"), initialStatus);
		assert.equal(fs.readFileSync(h.model, "utf8"), initialModel);
		assert.equal(h.timers.size, initialTimers);
		assert.equal(process.listeners("exit").length, initialListeners);
	}
	for (const timer of h.timers) timer.tick();
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, mainModel.name);
	await parent.fire("session_shutdown", ctx);
});

test("toggle and reload release and restore only the interactive owner's resources", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	const toggle = pi.commands.get("canopy-status")!.handler;
	await toggle("", ctx);
	assert.equal(fs.existsSync(h.status), false);
	assert.equal(fs.existsSync(h.model), false);
	assert.equal(h.timers.size, 0);
	await pi.fire("agent_start", ctx);
	assert.equal(fs.existsSync(h.status), false);
	await toggle("", ctx);
	// Events fired while disabled are still tracked, only not written, so
	// re-enabling reveals the true current state (a run started above).
	assert.equal(readStatus(h.status).state, "working");
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, mainModel.name);
	assert.equal(h.timers.size, 2);
	await pi.fire("session_shutdown", ctx);
	await pi.fire("session_start", ctx);
	assert.equal(h.timers.size, 2);
	assert.equal(process.listeners("exit").length, h.oldListeners.size + 1);
	await pi.fire("session_shutdown", ctx);
});

test("an aborted run settles to idle, not done", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	await pi.fire("agent_start", ctx);
	assert.equal(readStatus(h.status).state, "working");
	await pi.fire("agent_settled", ctx, { aborted: true });
	const status = readStatus(h.status);
	assert.equal(status.state, "idle");
	assert.equal(status.message, undefined);
	await pi.fire("session_shutdown", ctx);
});

test("an unretried assistant error settles to error with its first line, a retried one to done", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	await pi.fire("agent_start", ctx);
	await pi.fire("message_end", ctx, { message: { role: "assistant", stopReason: "error", errorMessage: "boom\nstack line two" } });
	// The error is only the run's outcome: still working until it settles.
	assert.equal(readStatus(h.status).state, "working");
	await pi.fire("agent_settled", ctx, { aborted: false });
	assert.deepEqual(readStatus(h.status), {
		pid: process.pid,
		cwd: "/projects/canopy",
		state: "error",
		message: "boom",
		updatedAt: readStatus(h.status).updatedAt,
	});
	// A new run whose last message succeeds settles to done (retry semantics).
	await pi.fire("agent_start", ctx);
	await pi.fire("message_end", ctx, { message: { role: "assistant", stopReason: "error", errorMessage: "boom" } });
	await pi.fire("message_end", ctx, { message: { role: "assistant", stopReason: "stop" } });
	await pi.fire("agent_settled", ctx, { aborted: false });
	assert.equal(readStatus(h.status).state, "done");
	assert.equal(readStatus(h.status).message, "test session");
	await pi.fire("session_shutdown", ctx);
});

test("done and error are one-shot writes: the heartbeat never refreshes them", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	await pi.fire("agent_start", ctx);
	await pi.fire("agent_settled", ctx, { aborted: false });
	const settled = fs.readFileSync(h.status, "utf8");
	for (const timer of h.timers) timer.tick();
	assert.equal(fs.readFileSync(h.status, "utf8"), settled, "done must not be heartbeated (updatedAt is canopy's settle identity)");
	await pi.fire("agent_start", ctx);
	await pi.fire("message_end", ctx, { message: { role: "assistant", stopReason: "error", errorMessage: "boom" } });
	await pi.fire("agent_settled", ctx, { aborted: false });
	const failed = fs.readFileSync(h.status, "utf8");
	for (const timer of h.timers) timer.tick();
	assert.equal(fs.readFileSync(h.status, "utf8"), failed, "error must not be heartbeated either");
	await pi.fire("session_shutdown", ctx);
});

test("an extension dialog blocks, outranks working, and clears back to the underlying state", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	await pi.fire("ui_prompt_start", ctx, { kind: "confirm", title: "Allow this?" });
	assert.deepEqual(readStatus(h.status), {
		pid: process.pid,
		cwd: "/projects/canopy",
		state: "blocked",
		message: "Allow this?",
		updatedAt: readStatus(h.status).updatedAt,
	});
	await pi.fire("ui_prompt_end", ctx, { kind: "confirm", title: "Allow this?" });
	assert.equal(readStatus(h.status).state, "idle");
	// Blocked during a run reports blocked, then returns to working.
	await pi.fire("agent_start", ctx);
	await pi.fire("ui_prompt_start", ctx, { kind: "select", title: "Pick one" });
	assert.equal(readStatus(h.status).state, "blocked");
	await pi.fire("ui_prompt_end", ctx, { kind: "select", title: "Pick one" });
	assert.equal(readStatus(h.status).state, "working");
	// A prompt without a title falls back to its kind.
	await pi.fire("ui_prompt_start", ctx, { kind: "custom" });
	assert.deepEqual(readStatus(h.status).message, "custom");
	await pi.fire("ui_prompt_end", ctx, { kind: "custom" });
	// Working (and blocked while it lasts) is heartbeated: it can sit for
	// minutes and must not go stale past canopy's pistatus.MaxAge.
	const stale = readStatus(h.status);
	stale.updatedAt = new Date(Date.now() - 11000).toISOString();
	fs.writeFileSync(h.status, JSON.stringify(stale));
	for (const timer of h.timers) timer.tick();
	assert.ok(Date.now() - Date.parse(readStatus(h.status).updatedAt) < 1000);
	await pi.fire("session_shutdown", ctx);
});

test("compaction reads working, then resolves per pi's rules", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	// Manual compaction at rest: working while it runs, done when it lands.
	await pi.fire("session_before_compact", ctx);
	assert.deepEqual(readStatus(h.status), {
		pid: process.pid,
		cwd: "/projects/canopy",
		state: "working",
		message: "Compacting context",
		updatedAt: readStatus(h.status).updatedAt,
	});
	await pi.fire("session_compact", ctx, { reason: "manual", willRetry: false });
	assert.equal(readStatus(h.status).state, "done");
	// A failed manual compaction at rest reads error.
	await pi.fire("session_before_compact", ctx);
	await pi.fire("session_compact_failed", ctx, { reason: "manual", aborted: false, errorMessage: "no summary\nmore", willRetry: false });
	assert.deepEqual(readStatus(h.status), {
		pid: process.pid,
		cwd: "/projects/canopy",
		state: "error",
		message: "no summary",
		updatedAt: readStatus(h.status).updatedAt,
	});
	// An aborted manual compaction at rest reads idle.
	await pi.fire("session_before_compact", ctx);
	await pi.fire("session_compact_failed", ctx, { reason: "manual", aborted: true, willRetry: false });
	assert.equal(readStatus(h.status).state, "idle");
	// Mid-run threshold compaction: still working afterwards, run decides the outcome.
	await pi.fire("agent_start", ctx);
	await pi.fire("session_before_compact", ctx);
	assert.equal(readStatus(h.status).state, "working");
	await pi.fire("session_compact", ctx, { reason: "threshold", willRetry: false });
	assert.equal(readStatus(h.status).state, "working");
	await pi.fire("agent_settled", ctx, { aborted: false });
	assert.equal(readStatus(h.status).state, "done");
	await pi.fire("session_shutdown", ctx);
});

test("model heartbeat refreshes stale metadata without faking a state transition", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	const model = JSON.parse(fs.readFileSync(h.model, "utf8"));
	model.updatedAt = new Date(Date.now() - 89000).toISOString();
	fs.writeFileSync(h.model, JSON.stringify(model));
	assert.equal(h.timers.size, 2);
	for (const timer of h.timers) timer.tick();
	// The model heartbeat refreshed the model file, and the state still
	// reads exactly what it did (idle; only its freshness timestamp moved).
	assert.ok(Date.now() - Date.parse(JSON.parse(fs.readFileSync(h.model, "utf8")).updatedAt) < 1000);
	assert.equal(readStatus(h.status).state, "idle");
	await pi.fire("session_shutdown", ctx);
});

test("process exit cleans up only the interactive owner's records", async (t) => {
	const h = await harness(t);
	const parent = h.create();
	const child = h.create();
	await parent.fire("session_start", context("tui", true));
	await child.fire("session_start", context("print", false, helperModel));
	const listeners = process.listeners("exit").filter((listener) => !h.oldListeners.has(listener));
	assert.equal(listeners.length, 1);
	listeners[0](0);
	assert.equal(fs.existsSync(h.status), false);
	assert.equal(fs.existsSync(h.model), false);
	assert.equal(h.timers.size, 0);
	assert.equal(process.listeners("exit").length, h.oldListeners.size);
});

test("older interactive hosts without mode still report", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context(undefined, true);
	await pi.fire("session_start", ctx);
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, mainModel.name);
	await pi.fire("session_shutdown", ctx);
});

test("a session without a name omits the message", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	pi.sessionName = undefined;
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	await pi.fire("agent_start", ctx);
	const status = readStatus(h.status);
	assert.equal(status.state, "working");
	assert.equal(status.message, undefined);
	await pi.fire("session_shutdown", ctx);
});
