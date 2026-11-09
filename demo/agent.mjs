#!/usr/bin/env node
// A minimal tool-using agent: it sends the conversation to the gateway, runs whatever tool
// the model asks for, appends the result and goes again. It has no loop protection of its
// own, which is the point: the gateway is the only thing that can stop it.
//
// Usage:
//   node demo/agent.mjs --session agent-a --scenario stubborn [--max-turns 25]
//                       [--base-url http://localhost:8080] [--model openai/gpt-4o-mini]
//                       [--key sk-bf-...] [--stream] [--no-session-header] [--json]

const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = args.indexOf("--" + name);
  return i === -1 ? fallback : args[i + 1];
};
const has = (name) => args.includes("--" + name);

const baseUrl = flag("base-url", process.env.BIFROST_URL || "http://localhost:8080");
const model = flag("model", process.env.MODEL || "openai/gpt-4o-mini");
const session = flag("session", "agent-" + Math.random().toString(36).slice(2, 8));
const scenario = flag("scenario", "loop");
const maxTurns = Number(flag("max-turns", "25"));
const key = flag("key", process.env.BIFROST_VK || "");
const stream = has("stream");
const asJson = has("json");

// The agent's tools. A real agent would touch the file system; these return canned results.
const tools = {
  list_files: () => "README.md\nmain.go\nconfig.yml",
  read_file: ({ path }) =>
    path === "README.md" ? "# Demo service\nRun `go run .` to start." : `error: ${path}: no such file or directory`,
  edit_file: () => "ok",
  run_tests: () => "FAIL: TestParse (1 of 12 tests failed)",
};
const toolSpecs = Object.keys(tools).map((name) => ({
  type: "function",
  function: { name, description: name.replace("_", " "), parameters: { type: "object", properties: { path: { type: "string" }, patch: { type: "string" } } } },
}));

const messages = [
  { role: "system", content: `You are a coding agent. Use the tools to complete the task.\ndemo-scenario: ${scenario}` },
  { role: "user", content: "Read the project configuration and summarise it." },
];

const log = (...a) => {
  if (!asJson) console.log(...a);
};

async function readStream(res) {
  // Collect a streamed chat completion into one assistant message.
  const msg = { role: "assistant", content: "", tool_calls: [] };
  const decoder = new TextDecoder();
  let buf = "";
  for await (const chunk of res.body) {
    buf += decoder.decode(chunk, { stream: true });
    let nl;
    while ((nl = buf.indexOf("\n")) !== -1) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line.startsWith("data:")) continue;
      const data = line.slice(5).trim();
      if (data === "[DONE]") continue;
      const delta = JSON.parse(data).choices?.[0]?.delta;
      if (!delta) continue;
      if (delta.content) msg.content += delta.content;
      for (const tc of delta.tool_calls ?? []) msg.tool_calls.push({ id: tc.id, type: "function", function: tc.function });
    }
  }
  if (msg.tool_calls.length === 0) delete msg.tool_calls;
  if (!msg.content) msg.content = null;
  return msg;
}

const result = { session, sessionId: null, scenario, turns: 0, outcome: "max-turns", guard: [], final: null, error: null, status: 200 };

for (let turn = 1; turn <= maxTurns; turn++) {
  const headers = { "content-type": "application/json" };
  if (!has("no-session-header")) headers["x-bf-session-id"] = session;
  if (key) headers["x-bf-vk"] = key;

  const res = await fetch(baseUrl + "/v1/chat/completions", {
    method: "POST",
    headers,
    body: JSON.stringify({ model, messages, tools: toolSpecs, stream, ...(stream ? { stream_options: { include_usage: true } } : {}) }),
  });
  const guard = res.headers.get("x-bf-session-guard") ?? "off";
  const rule = res.headers.get("x-bf-session-guard-rule");
  result.turns = turn;
  result.status = res.status;
  const would = res.headers.get("x-bf-session-guard-would");
  result.guard.push([guard, rule, would && "would=" + would].filter(Boolean).join(":"));
  result.sessionId = res.headers.get("x-bf-session-guard-session") ?? result.sessionId;
  const tag = guard === "off" ? "" : `   [guard: ${guard}${rule ? " · " + rule : ""}${would ? " · dry run, would " + would : ""}]`;

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const err = body.error ?? body;
    result.outcome = String(err.type ?? "").startsWith("session_guard") ? "stopped-by-guard" : "refused";
    result.error = { status: res.status, type: err.type, code: err.code, message: err.message };
    log(`  turn ${String(turn).padStart(2)}  HTTP ${res.status}${tag}`);
    log(`           ${err.message ?? JSON.stringify(err)}`);
    break;
  }

  const msg = stream ? await readStream(res) : (await res.json()).choices[0].message;
  messages.push(msg);

  if (!msg.tool_calls?.length) {
    result.outcome = "answered";
    result.final = msg.content;
    log(`  turn ${String(turn).padStart(2)}  answer: ${msg.content}${tag}`);
    break;
  }
  for (const tc of msg.tool_calls) {
    let parsed = {};
    try {
      parsed = JSON.parse(tc.function.arguments || "{}");
    } catch {
      /* leave the arguments empty */
    }
    const out = (tools[tc.function.name] ?? (() => "error: unknown tool"))(parsed);
    messages.push({ role: "tool", tool_call_id: tc.id, content: out });
    log(`  turn ${String(turn).padStart(2)}  ${tc.function.name}(${tc.function.arguments})  ->  ${out.split("\n")[0]}${tag}`);
  }
}

if (asJson) console.log(JSON.stringify(result));
else log(`  outcome: ${result.outcome} after ${result.turns} turn(s)`);
