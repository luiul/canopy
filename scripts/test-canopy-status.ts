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

	on(event: string, handler: Handler) {
		const handlers = this.handlers.get(event) ?? [];
		handlers.push(handler);
		this.handlers.set(event, handlers);
	}

	registerCommand(name: string, command: { handler: Handler }) {
		this.commands.set(name, command);
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

test("interactive session owns status, model, heartbeats, and cleanup", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	assert.equal(process.listeners("exit").length, h.oldListeners.size, "factory must not add exit handlers");
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	assert.equal(JSON.parse(fs.readFileSync(h.status, "utf8")).state, "idle");
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, mainModel.name);
	assert.equal(h.timers.size, 1);
	for (const event of ["before_agent_start", "agent_start", "tool_execution_start"]) {
		await pi.fire(event, ctx);
		assert.equal(JSON.parse(fs.readFileSync(h.status, "utf8")).state, "working");
		assert.equal(h.timers.size, 2);
	}
	const stateBeforeModel = fs.readFileSync(h.status, "utf8");
	await pi.fire("model_select", ctx, { model: helperModel });
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, helperModel.name);
	assert.equal(fs.readFileSync(h.status, "utf8"), stateBeforeModel);
	for (const timer of h.timers) timer.tick();
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, helperModel.name);
	await pi.fire("agent_settled", ctx);
	assert.equal(JSON.parse(fs.readFileSync(h.status, "utf8")).state, "done");
	assert.equal(h.timers.size, 1);
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
			await child.fire(event, childCtx);
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
	assert.equal(JSON.parse(fs.readFileSync(h.model, "utf8")).model, mainModel.name);
	assert.equal(h.timers.size, 1);
	await pi.fire("session_shutdown", ctx);
	await pi.fire("session_start", ctx);
	assert.equal(h.timers.size, 1);
	assert.equal(process.listeners("exit").length, h.oldListeners.size + 1);
	await pi.fire("session_shutdown", ctx);
});

test("model heartbeat refreshes stale metadata without changing the state timestamp", async (t) => {
	const h = await harness(t);
	const pi = h.create();
	const ctx = context("tui", true);
	await pi.fire("session_start", ctx);
	const status = JSON.parse(fs.readFileSync(h.status, "utf8"));
	status.updatedAt = new Date(Date.now() - 11000).toISOString();
	fs.writeFileSync(h.status, JSON.stringify(status));
	const model = JSON.parse(fs.readFileSync(h.model, "utf8"));
	model.updatedAt = new Date(Date.now() - 89000).toISOString();
	fs.writeFileSync(h.model, JSON.stringify(model));
	const stateBefore = fs.readFileSync(h.status, "utf8");
	assert.equal(h.timers.size, 1);
	for (const timer of h.timers) timer.tick();
	assert.equal(fs.readFileSync(h.status, "utf8"), stateBefore);
	assert.ok(Date.now() - Date.parse(JSON.parse(fs.readFileSync(h.model, "utf8")).updatedAt) < 1000);
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
