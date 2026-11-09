// End-to-end tests: a real Bifrost gateway with the plugin loaded, the mock provider behind
// it, and scripted agents in front. Run with `make e2e` (needs `make gateway plugin` first).
//
// Each suite starts its own gateway with a different plugin configuration.

import { spawn, execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const GATEWAY = process.env.GATEWAY || join(ROOT, ".work/bin/bifrost-http");
const PLUGIN = process.env.PLUGIN_SO || join(ROOT, "build/session-guard.so");
const MOCK_PORT = 17300;
const GW_PORT = 18080;
const STATUS_PORT = 19797;
const BASE = `http://127.0.0.1:${GW_PORT}`;
const STATUS = `http://127.0.0.1:${STATUS_PORT}`;
const VK = "sk-bf-demo-team";

for (const [what, path] of [["gateway", GATEWAY], ["plugin", PLUGIN]]) {
  if (!existsSync(path)) {
    console.error(`missing ${what} at ${path}. Run: make gateway plugin`);
    process.exit(1);
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(url, what, tries = 120) {
  for (let i = 0; i < tries; i++) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch {
      /* not up yet */
    }
    await sleep(250);
  }
  throw new Error(`${what} did not come up at ${url}`);
}

let mock;
before(async () => {
  mock = spawn("node", [join(ROOT, "demo/mock-provider.mjs"), String(MOCK_PORT)], { stdio: "ignore" });
  await waitFor(`http://127.0.0.1:${MOCK_PORT}/healthz`, "mock provider");
});
after(() => mock?.kill());

// startGateway writes a config for the given plugin configuration and starts the gateway.
async function startGateway(mode, guardConfig) {
  const dir = mkdtempSync(join(tmpdir(), "session-guard-e2e-"));
  const env = {
    ...process.env,
    MOCK_URL: `http://127.0.0.1:${MOCK_PORT}`,
    STATUS_ADDR: `127.0.0.1:${STATUS_PORT}`,
    BUDGET_USD: "50", // high enough that governance never interferes with these tests
  };
  if (guardConfig) env.GUARD_CONFIG = JSON.stringify({ status: { listen: `127.0.0.1:${STATUS_PORT}` }, ...guardConfig });
  execFileSync(join(ROOT, "demo/make-config.sh"), [dir, mode, PLUGIN], { env, stdio: "ignore" });
  const proc = spawn(GATEWAY, ["-port", String(GW_PORT), "-app-dir", dir], { stdio: "ignore" });
  try {
    await waitFor(`${BASE}/health`, "gateway");
    if (mode !== "off") await waitFor(`${STATUS}/healthz`, "session guard status server");
  } catch (e) {
    proc.kill();
    throw e;
  }
  return {
    async stop() {
      proc.kill();
      await new Promise((r) => proc.once("exit", r));
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

function runAgent(...args) {
  const out = execFileSync("node", [join(ROOT, "demo/agent.mjs"), "--json", "--base-url", BASE, "--key", VK, ...args], { encoding: "utf8" });
  return JSON.parse(out.trim().split("\n").pop());
}

async function sessions() {
  return (await fetch(`${STATUS}/api/sessions`)).json();
}

// loopHistory builds a conversation that already holds n identical failed steps.
function chatLoopBody(n, scenario = "stubborn") {
  const messages = [
    { role: "system", content: `You are a coding agent.\ndemo-scenario: ${scenario}` },
    { role: "user", content: "Read the project configuration and summarise it." },
  ];
  for (let i = 1; i <= n; i++) {
    messages.push({ role: "assistant", content: null, tool_calls: [{ id: "c" + i, type: "function", function: { name: "read_file", arguments: '{"path":"config.yaml"}' } }] });
    messages.push({ role: "tool", tool_call_id: "c" + i, content: "error: config.yaml: no such file or directory" });
  }
  return { model: "openai/gpt-4o-mini", messages };
}

async function post(path, body, headers = {}) {
  const res = await fetch(BASE + path, { method: "POST", headers: { "content-type": "application/json", "x-bf-vk": VK, ...headers }, body: JSON.stringify(body) });
  return { status: res.status, guard: res.headers.get("x-bf-session-guard"), rule: res.headers.get("x-bf-session-guard-rule"), session: res.headers.get("x-bf-session-guard-session"), body: await res.json().catch(() => null) };
}

describe("guard on, default configuration", () => {
  let gw;
  before(async () => {
    gw = await startGateway("on");
  });
  after(() => gw?.stop());

  test("a healthy agent is never touched", () => {
    const r = runAgent("--session", "e2e-healthy", "--scenario", "healthy");
    assert.equal(r.outcome, "answered");
    assert.equal(r.turns, 3);
    assert.deepEqual(r.guard, ["allow", "allow", "allow"]);
  });

  test("a looping agent is nudged and recovers", () => {
    const r = runAgent("--session", "e2e-loop", "--scenario", "loop");
    assert.equal(r.outcome, "answered");
    assert.equal(r.turns, 5);
    assert.deepEqual(r.guard, ["allow", "allow", "allow", "allow", "nudge:repeat_tool_call"]);
    assert.match(r.final, /does not exist/);
  });

  test("a two-step cycle is nudged and recovers", () => {
    const r = runAgent("--session", "e2e-cycle", "--scenario", "cycle");
    assert.equal(r.outcome, "answered");
    assert.equal(r.turns, 7);
    assert.equal(r.guard.at(-1), "nudge:tool_cycle");
  });

  test("an agent that ignores the nudge is blocked with a non-retryable error", () => {
    const r = runAgent("--session", "e2e-stubborn", "--scenario", "stubborn");
    assert.equal(r.outcome, "stopped-by-guard");
    assert.equal(r.turns, 8);
    assert.deepEqual(r.guard.slice(4), ["nudge:repeat_tool_call", "nudge:repeat_tool_call", "nudge:repeat_tool_call", "block:repeat_tool_call"]);
    assert.equal(r.error.status, 403);
    assert.equal(r.error.type, "session_guard_loop_detected");
    assert.equal(r.error.code, "repeat_tool_call");
    assert.match(r.error.message, /read_file/);
  });

  test("streamed requests are guarded the same way", () => {
    const r = runAgent("--session", "e2e-stream", "--scenario", "stubborn", "--stream");
    assert.equal(r.outcome, "stopped-by-guard");
    assert.equal(r.turns, 8);
  });

  test("with no session header, the conversation fingerprint is the session", () => {
    const r = runAgent("--scenario", "stubborn", "--no-session-header");
    assert.equal(r.outcome, "stopped-by-guard");
    assert.equal(r.turns, 8);
    assert.match(r.sessionId, /^fp-[0-9a-f]+$/);
  });

  test("other sessions on the same virtual key keep working after a block", () => {
    const r = runAgent("--session", "e2e-neighbour", "--scenario", "healthy");
    assert.equal(r.outcome, "answered");
  });

  test("a blocked conversation stays blocked when resent, and passes once a person steps in", async () => {
    const body = chatLoopBody(8);
    const h = { "x-bf-session-id": "e2e-resend" };
    assert.equal((await post("/v1/chat/completions", body, h)).status, 403);
    assert.equal((await post("/v1/chat/completions", body, h)).status, 403);
    body.messages.push({ role: "user", content: "The file is called config.yml, not config.yaml." });
    const r = await post("/v1/chat/completions", body, h);
    assert.equal(r.status, 200);
    assert.equal(r.guard, "allow");
  });

  test("Responses API requests are guarded", async () => {
    const input = [{ type: "message", role: "user", content: "Read the project configuration and summarise it." }];
    for (let i = 1; i <= 4; i++) {
      input.push({ type: "function_call", call_id: "c" + i, name: "read_file", arguments: '{"path":"config.yaml"}' });
      input.push({ type: "function_call_output", call_id: "c" + i, output: "error: config.yaml: no such file or directory" });
    }
    const body = { model: "openai/gpt-4o-mini", instructions: "You are a coding agent.\ndemo-scenario: loop", input };
    const r = await post("/v1/responses", body, { "x-bf-session-id": "e2e-responses" });
    assert.equal(r.status, 200);
    assert.equal(r.guard, "nudge");
    assert.equal(r.rule, "repeat_tool_call");
    // The mock answers in words only when it sees the guard's notice in its instructions.
    assert.match(JSON.stringify(r.body), /does not exist/);
  });

  test("Anthropic Messages API requests are guarded", async () => {
    const messages = [{ role: "user", content: "Read the project configuration and summarise it." }];
    for (let i = 1; i <= 4; i++) {
      messages.push({ role: "assistant", content: [{ type: "text", text: "Attempt " + i }, { type: "tool_use", id: "tu" + i, name: "read_file", input: { path: "config.yaml" } }] });
      messages.push({ role: "user", content: [{ type: "tool_result", tool_use_id: "tu" + i, content: "error: config.yaml: no such file or directory" }] });
    }
    const body = { model: "openai/gpt-4o-mini", max_tokens: 256, system: "You are a coding agent.\ndemo-scenario: loop", messages };
    const r = await post("/anthropic/v1/messages", body, { "x-bf-session-id": "e2e-anthropic" });
    assert.equal(r.status, 200);
    assert.equal(r.guard, "nudge");
    assert.equal(r.rule, "repeat_tool_call");
    assert.match(JSON.stringify(r.body), /does not exist/);
  });

  test("the status API reports sessions, usage and totals, and can reset a session", async () => {
    const all = await sessions();
    const byId = Object.fromEntries(all.map((s) => [s.id, s]));
    assert.equal(byId["e2e-stubborn"].state, "blocked");
    assert.equal(byId["e2e-stubborn"].requests, 7);
    assert.equal(byId["e2e-stubborn"].blocked, 1);
    assert.equal(byId["e2e-stubborn"].nudges, 3);
    assert.equal(byId["e2e-stubborn"].virtual_key, "demo-team");
    assert.ok(byId["e2e-stubborn"].total_tokens > 7 * 18000, "tokens are recorded");
    assert.ok(byId["e2e-stream"].total_tokens > 7 * 18000, "tokens are recorded for streamed responses");
    assert.equal(byId["e2e-healthy"].state, "ok");
    assert.equal(byId["e2e-loop"].state, "nudged");

    const summary = await (await fetch(`${STATUS}/api/summary`)).json();
    assert.ok(summary.totals.blocked >= 3);
    assert.ok(summary.totals.nudges >= 8);
    assert.equal(summary.totals.dry_run, false);

    const reset = await fetch(`${STATUS}/api/reset`, { method: "POST", body: JSON.stringify({ key: byId["e2e-stubborn"].key }) });
    assert.equal(reset.status, 200);
    assert.ok(!(await sessions()).some((s) => s.id === "e2e-stubborn"));
  });
});

describe("dry run", () => {
  let gw;
  before(async () => {
    gw = await startGateway("dry-run");
  });
  after(() => gw?.stop());

  test("nothing is blocked or nudged, and the headers say what would have happened", () => {
    const r = runAgent("--session", "e2e-dry", "--scenario", "stubborn", "--max-turns", "10");
    assert.equal(r.outcome, "max-turns");
    assert.equal(r.turns, 10);
    assert.equal(r.guard[3], "allow");
    assert.equal(r.guard[4], "warn:repeat_tool_call:would=nudge");
    assert.equal(r.guard[7], "warn:repeat_tool_call:would=block");
  });
});

describe("session limits", () => {
  let gw;
  before(async () => {
    gw = await startGateway("on", { limits: { max_requests: 3 } });
  });
  after(() => gw?.stop());

  test("a session is stopped at its request limit while a new session starts fresh", () => {
    const r = runAgent("--session", "e2e-limit", "--scenario", "stubborn");
    assert.equal(r.outcome, "stopped-by-guard");
    assert.equal(r.turns, 4);
    assert.equal(r.error.status, 402);
    assert.equal(r.error.type, "session_guard_limit_exceeded");
    assert.equal(r.error.code, "limit_requests");

    const fresh = runAgent("--session", "e2e-limit-other", "--scenario", "healthy");
    assert.equal(fresh.outcome, "answered");
  });
});
