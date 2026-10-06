import { test, expect } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

// Run the extension in a fresh Bun process so its os.homedir() points to
// a disposable HOME, never to the user's real canopy-status directory.
test("selected model changes and heartbeats do not change the state timestamp", () => {
	const home = mkdtempSync(join(tmpdir(), "canopy-model-"));
	try {
		const child = Bun.spawnSync({
			cmd: ["bun", "-e", `
				import assert from "node:assert/strict";
				import * as fs from "node:fs";
				import * as path from "node:path";
				const watches = [];
				globalThis.setInterval = (fn, delay) => {
					watches.push({ fn, delay });
					return { unref() {} };
				};
				globalThis.clearInterval = () => {};
				const { default: install } = await import(process.env.CANOPY_EXTENSION_PATH);
				const handlers = {};
				const commands = {};
				install({
					on(name, fn) { handlers[name] = fn; },
					registerCommand(name, command) { commands[name] = command; },
				});
				const dir = path.join(process.env.HOME, ".pi", "agent", "canopy-status");
				const statusPath = path.join(dir, process.pid + ".json");
				const modelPath = path.join(dir, process.pid + ".model.json");
				const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
				const ctx = { cwd: "/project", model: { name: "GPT-6 Luna", provider: "ai-model-router" }, ui: { notify() {} } };
				await handlers.session_start({}, ctx);
				assert.equal(read(statusPath).state, "idle");
				assert.equal(read(modelPath).model, "GPT-6 Luna");
				const stateBefore = fs.readFileSync(statusPath, "utf8");
				await handlers.model_select({ model: { name: "GPT-6 Sol", provider: "ai-model-router" } });
				assert.equal(read(modelPath).model, "GPT-6 Sol");
				assert.equal(fs.readFileSync(statusPath, "utf8"), stateBefore);
				const old = read(statusPath);
				old.updatedAt = new Date(Date.now() - 11000).toISOString();
				fs.writeFileSync(statusPath, JSON.stringify(old));
				fs.writeFileSync(modelPath, JSON.stringify({ ...read(modelPath), updatedAt: new Date(Date.now() - 89000).toISOString() }));
				watches.find(w => w.delay === 30000).fn();
				assert.equal(read(modelPath).model, "GPT-6 Sol");
				assert.ok(Date.now() - Date.parse(read(modelPath).updatedAt) < 1000);
				assert.equal(read(statusPath).updatedAt, old.updatedAt);
				await commands["canopy-status"].handler("", ctx);
				assert.equal(fs.existsSync(statusPath), false);
				assert.equal(fs.existsSync(modelPath), false);
				await handlers.session_shutdown({}, ctx);
			`],
			env: { ...process.env, HOME: home, CANOPY_EXTENSION_PATH: fileURLToPath(new URL("./canopy-status.ts", import.meta.url)) },
			stdout: "pipe",
			stderr: "pipe",
		});
		expect(child.exitCode, new TextDecoder().decode(child.stderr)).toBe(0);
	} finally {
		rmSync(home, { recursive: true, force: true });
	}
});
