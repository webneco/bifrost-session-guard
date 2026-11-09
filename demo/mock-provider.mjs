#!/usr/bin/env node
// A scripted stand-in for a model provider, so the demo and the end-to-end test run with no
// API keys and no spend. It speaks the OpenAI Chat Completions and Responses APIs, which is
// what Bifrost sends when a provider's base_url points here.
//
// It plays the model side of a coding agent. The scenario is named in the system prompt as
// "demo-scenario: <name>":
//
//   loop      keeps calling read_file on a file that does not exist, and changes course once
//             the session guard's notice appears in its prompt
//   stubborn  the same loop, but it ignores the notice (so the guard has to block it)
//   cycle     alternates edit_file and run_tests without making progress, until the notice
//   healthy   lists files, reads one, then answers
//
// Usage: node demo/mock-provider.mjs [port]      (default 7300)

import http from "node:http";

const port = Number(process.argv[2] || process.env.MOCK_PORT || 7300);
// Tokens added to every prompt to stand in for a coding agent's large context.
const BASE_PROMPT_TOKENS = Number(process.env.MOCK_PROMPT_TOKENS || 18000);
const GUARD_MARKER = "[Bifrost session guard]";

let calls = 0;

function textOf(content) {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) return content.map((p) => (typeof p === "string" ? p : (p?.text ?? ""))).join("\n");
  return "";
}

// decide plays the model: given the system text and how many tool results the conversation
// holds, it returns either a tool call or a final answer.
function decide(system, toolResults) {
  const scenario = system.match(/demo-scenario:\s*([a-z]+)/)?.[1] ?? "loop";
  const nudged = system.includes(GUARD_MARKER);
  const call = (name, args) => ({ tool: { name, arguments: JSON.stringify(args) } });

  switch (scenario) {
    case "healthy":
      if (toolResults === 0) return call("list_files", {});
      if (toolResults === 1) return call("read_file", { path: "README.md" });
      return { text: "The project is a small demo service. README.md describes how to run it; main.go holds the entry point." };
    case "cycle":
      if (nudged) return { text: "The same edit keeps failing the same test, so I will stop. TestParse expects a different input format; how should I handle it?" };
      return toolResults % 2 === 0 ? call("edit_file", { path: "parser.go", patch: "+ return nil" }) : call("run_tests", {});
    case "stubborn":
      return call("read_file", { path: "config.yaml" });
    case "loop":
    default:
      if (nudged) return { text: "config.yaml does not exist, and reading it again will not help. Which file holds the configuration?" };
      return call("read_file", { path: "config.yaml" });
  }
}

function usageFor(chars) {
  const usage = { prompt_tokens: BASE_PROMPT_TOKENS + Math.ceil(chars / 4), completion_tokens: 120 };
  usage.total_tokens = usage.prompt_tokens + usage.completion_tokens;
  return usage;
}

// The Responses API shape, which is also what Bifrost sends for Anthropic-format requests.
function respondResponses(res, body) {
  const input = Array.isArray(body.input) ? body.input : [];
  const system = [body.instructions ?? "", ...input.filter((i) => i.role === "system" || i.role === "developer").map((i) => textOf(i.content))].join("\n");
  const toolResults = input.filter((i) => typeof i.type === "string" && i.type.endsWith("_output")).length;
  const d = decide(system, toolResults);
  calls++;
  const u = usageFor(JSON.stringify(input).length);
  const output = d.tool
    ? [{ type: "function_call", id: "fc_" + calls, call_id: "call_" + calls, name: d.tool.name, arguments: d.tool.arguments, status: "completed" }]
    : [{ type: "message", id: "msg_" + calls, status: "completed", role: "assistant", content: [{ type: "output_text", text: d.text, annotations: [] }] }];
  res.writeHead(200, { "content-type": "application/json" });
  res.end(
    JSON.stringify({
      id: "resp_mock_" + calls,
      object: "response",
      created_at: Math.floor(Date.now() / 1000),
      status: "completed",
      model: typeof body.model === "string" ? body.model : "mock",
      output,
      usage: { input_tokens: u.prompt_tokens, output_tokens: u.completion_tokens, total_tokens: u.total_tokens },
    }),
  );
}

function respond(req, res, body) {
  const messages = Array.isArray(body.messages) ? body.messages : [];
  const system = messages.filter((m) => m.role === "system" || m.role === "developer").map((m) => textOf(m.content)).join("\n");
  const d = decide(system, messages.filter((m) => m.role === "tool").length);
  calls++;
  const usage = usageFor(messages.reduce((n, m) => n + textOf(m.content).length + JSON.stringify(m.tool_calls ?? "").length, 0));

  const id = "chatcmpl-mock-" + calls;
  const model = typeof body.model === "string" ? body.model : "mock";
  const created = Math.floor(Date.now() / 1000);
  const message = d.tool
    ? { role: "assistant", content: null, tool_calls: [{ id: "call_" + calls, type: "function", function: d.tool }] }
    : { role: "assistant", content: d.text };
  const finish = d.tool ? "tool_calls" : "stop";

  if (body.stream) {
    res.writeHead(200, { "content-type": "text/event-stream" });
    const send = (o) => res.write("data: " + JSON.stringify({ id, object: "chat.completion.chunk", created, model, ...o }) + "\n\n");
    const delta = d.tool
      ? { role: "assistant", tool_calls: [{ index: 0, id: "call_" + calls, type: "function", function: d.tool }] }
      : { role: "assistant", content: d.text };
    send({ choices: [{ index: 0, delta, finish_reason: null }] });
    send({ choices: [{ index: 0, delta: {}, finish_reason: finish }] });
    send({ choices: [], usage });
    res.write("data: [DONE]\n\n");
    return res.end();
  }
  res.writeHead(200, { "content-type": "application/json" });
  res.end(JSON.stringify({ id, object: "chat.completion", created, model, choices: [{ index: 0, message, finish_reason: finish }], usage }));
}

http
  .createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", () => {
      if (req.url === "/healthz") {
        res.writeHead(200, { "content-type": "application/json" });
        return res.end(JSON.stringify({ status: "ok", calls }));
      }
      if (req.url.endsWith("/models")) {
        res.writeHead(200, { "content-type": "application/json" });
        return res.end(JSON.stringify({ object: "list", data: [{ id: "gpt-4o-mini", object: "model", owned_by: "mock" }] }));
      }
      const isChat = req.url.endsWith("/chat/completions");
      const isResponses = req.url.endsWith("/responses");
      if (req.method === "POST" && (isChat || isResponses)) {
        let body = {};
        try {
          body = JSON.parse(raw || "{}");
        } catch {
          res.writeHead(400, { "content-type": "application/json" });
          return res.end(JSON.stringify({ error: { message: "invalid JSON", type: "invalid_request_error" } }));
        }
        return isChat ? respond(req, res, body) : respondResponses(res, body);
      }
      res.writeHead(404, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { message: "the mock provider implements chat completions and responses only", type: "invalid_request_error" } }));
    });
  })
  .listen(port, () => console.log(`mock provider listening on :${port}`));
