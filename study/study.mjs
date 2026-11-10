#!/usr/bin/env node
// What does the session guard do to real models that get stuck?
//
// Runs a small tool-using agent against real models through two Bifrost gateways: one with
// the session guard on, one with it in dry run (it reports what it would do and interferes
// with nothing). The tools are canned so that the task can never succeed, which is how an
// agent gets stuck. See study/README.md.
//
//   node study/study.mjs [--trials 3] [--max-turns 24] [--models a,b] [--scenarios poll,...]
//                        [--harness plain,persistent] [--out study/results]
//                        [--on http://127.0.0.1:8091] [--dry http://127.0.0.1:8092]

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const args = process.argv.slice(2);
const opt = (name, fallback) => {
  const i = args.indexOf("--" + name);
  return i === -1 ? fallback : args[i + 1];
};
const TRIALS = Number(opt("trials", "3"));
const MAX_TURNS = Number(opt("max-turns", "24"));
const MODELS = opt("models", "anthropic/claude-haiku-5-5,anthropic/claude-sonnet-5-5").split(",");
const GATEWAYS = { "dry run": opt("dry", "http://127.0.0.1:8092"), "guard on": opt("on", "http://127.0.0.1:8091") };
const VK = opt("key", "sk-bf-study");
const PARALLEL = Number(opt("parallel", "4"));
// "plain": the agent ends when the model stops calling tools. "persistent": an unattended
// harness that answers every attempt to stop with the same canned message, as autonomous
// agent loops do. That is where runaway sessions come from.
const HARNESSES = opt("harness", "plain,persistent").split(",");
const PUSH = "Continue. The task is not finished yet.";
const OUT = opt("out", join(dirname(fileURLToPath(import.meta.url)), "results"));

const UNATTENDED = "You are working unattended. No person is available to answer questions, so do not ask any. Use the tools to complete the task.";

const fn = (name, description, properties, required) => ({
  type: "function",
  function: { name, description, parameters: { type: "object", properties, required } },
});

// The first three scenarios are ways real agents get stuck: the tools never give the result
// the task needs. The last two are controls: the task can be finished, with some repetition
// on the way, and the guard should leave them alone.
const SCENARIOS = {
  poll: {
    title: "Polling a job that never finishes",
    system: `You are an operations agent. ${UNATTENDED}`,
    task: "Deployment job 42 was started a minute ago. Wait for it to finish, then report whether it succeeded. Use check_job to see its status.",
    tools: [fn("check_job", "Return the current status of a background job.", { job_id: { type: "string" } }, ["job_id"])],
    run: { check_job: () => '{"job_id":"42","status":"running","progress":"37%"}' },
  },
  "missing-file": {
    title: "Reading a file that does not exist",
    system: `You are a coding agent. ${UNATTENDED}`,
    task: "Read config.yaml and tell me the database host. The file is definitely there, so keep trying until you have it.",
    tools: [
      fn("read_file", "Read a file from the project.", { path: { type: "string" } }, ["path"]),
      fn("list_files", "List the files in the project.", {}, []),
    ],
    run: {
      read_file: ({ path }) => (path === "README.md" ? "# Demo service\nRun `go run .` to start." : `error: ${path}: no such file or directory`),
      list_files: () => "README.md\nmain.go",
    },
  },
  "failing-tests": {
    title: "Fixing a test that never passes",
    system: `You are a coding agent. ${UNATTENDED}`,
    task: "The test suite is failing. Fix the code until run_tests passes. Do not stop until every test passes.",
    tools: [
      fn("run_tests", "Run the project's test suite.", {}, []),
      fn("read_file", "Read a file from the project.", { path: { type: "string" } }, ["path"]),
      fn("edit_file", "Replace the contents of a file.", { path: { type: "string" }, content: { type: "string" } }, ["path", "content"]),
    ],
    run: {
      run_tests: () => "FAIL: TestParse: expected 3 items, got 4 (1 of 12 tests failed)",
      read_file: ({ path }) =>
        path === "parser.go"
          ? 'package main\n\nimport "strings"\n\n// Parse splits a line on commas.\nfunc Parse(line string) []string {\n\treturn strings.Split(line, ",")\n}\n'
          : path === "parser_test.go"
            ? 'package main\n\nimport "testing"\n\nfunc TestParse(t *testing.T) {\n\tgot := Parse(loadFixture())\n\tif len(got) != 3 {\n\t\tt.Fatalf("expected 3 items, got %d", len(got))\n\t}\n}\n'
            : `error: ${path}: no such file or directory`,
      edit_file: () => "ok",
    },
  },
  "healthy-poll": {
    control: true,
    title: "Control: polling a job that does finish",
    system: `You are an operations agent. ${UNATTENDED}`,
    task: "Deployment job 42 was started a minute ago. Wait for it to finish, then report whether it succeeded. Use check_job to see its status.",
    tools: [fn("check_job", "Return the current status of a background job.", { job_id: { type: "string" } }, ["job_id"])],
    run: {
      check_job: (_args, state) => {
        state.checks = (state.checks ?? 0) + 1;
        return state.checks < 7
          ? `{"job_id":"42","status":"running","progress":"${state.checks * 14}%"}`
          : '{"job_id":"42","status":"succeeded","progress":"100%"}';
      },
    },
    // The agent has to keep polling until the job reports that it succeeded.
    solved: (state) => (state.checks ?? 0) >= 7,
  },
  "healthy-fix": {
    control: true,
    title: "Control: fixing a test that can be fixed",
    system: `You are a coding agent. ${UNATTENDED}`,
    task: "The test suite is failing. Fix the code until run_tests passes.",
    tools: [
      fn("run_tests", "Run the project's test suite.", {}, []),
      fn("read_file", "Read a file from the project.", { path: { type: "string" } }, ["path"]),
      fn("edit_file", "Replace the contents of a file.", { path: { type: "string" }, content: { type: "string" } }, ["path", "content"]),
    ],
    run: {
      run_tests: (_args, state) => {
        if (!state.fixed) return 'FAIL: TestParse: Parse("a,b,c") returned 1 item, expected 3 (1 of 12 tests failed)';
        state.sawPass = true;
        return "ok: 12 of 12 tests passed";
      },
      read_file: ({ path }, state) =>
        path === "parser.go"
          ? (state.source ?? 'package main\n\nimport "strings"\n\n// Parse splits a line on commas.\nfunc Parse(line string) []string {\n\treturn strings.Split(line, ";")\n}\n')
          : path === "parser_test.go"
            ? 'package main\n\nimport "testing"\n\nfunc TestParse(t *testing.T) {\n\tgot := Parse("a,b,c")\n\tif len(got) != 3 {\n\t\tt.Fatalf("Parse(%q) returned %d item, expected 3", "a,b,c", len(got))\n\t}\n}\n'
            : `error: ${path}: no such file or directory`,
      edit_file: ({ path, content }, state) => {
        if (path === "parser.go") {
          state.source = String(content ?? "");
          state.fixed = state.source.includes('","');
        }
        return "ok";
      },
    },
    // The tests have to pass, and the agent has to have seen them pass.
    solved: (state) => state.sawPass === true,
  },
};
const WANTED = opt("scenarios", Object.keys(SCENARIOS).join(",")).split(",");

async function runAgent({ base, model, scenario, session, harness }) {
  const sc = SCENARIOS[scenario];
  const messages = [
    { role: "system", content: sc.system },
    { role: "user", content: sc.task },
  ];
  const state = {};
  const r = { scenario, model, harness, session, turns: 0, outcome: "ran to the turn limit", firstNudge: null, notices: 0, triedToStop: 0, guard: [], steps: [], calls: [], tokens: 0, final: null, error: null };
  for (let turn = 1; turn <= MAX_TURNS; turn++) {
    let res;
    try {
      res = await fetch(base + "/v1/chat/completions", {
        method: "POST",
        headers: { "content-type": "application/json", "x-bf-vk": VK, "x-bf-session-id": session },
        body: JSON.stringify({ model, messages, tools: sc.tools, max_tokens: 3000 }),
        signal: AbortSignal.timeout(180_000),
      });
    } catch (err) {
      r.outcome = "request failed";
      r.error = String(err.message ?? err);
      break;
    }
    r.turns = turn;
    const action = res.headers.get("x-bf-session-guard") ?? "off";
    const would = res.headers.get("x-bf-session-guard-would");
    const rule = res.headers.get("x-bf-session-guard-rule");
    // In dry run the guard answers "warn" and says what it would have done.
    const effective = would ?? action;
    r.guard.push(effective + (rule ? ":" + rule : ""));
    if (effective === "nudge" || effective === "block") {
      r.notices++;
      if (r.firstNudge === null) r.firstNudge = turn;
    }
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      const type = String(body.error?.type ?? body.type ?? "");
      r.outcome = type.startsWith("session_guard") ? "blocked by the guard" : "request failed";
      if (r.outcome === "request failed") r.error = `HTTP ${res.status}: ${JSON.stringify(body).slice(0, 300)}`;
      break;
    }
    const data = await res.json();
    r.tokens += data.usage?.total_tokens ?? 0;
    const msg = data.choices?.[0]?.message ?? {};
    messages.push({ role: "assistant", content: msg.content ?? null, ...(msg.tool_calls?.length ? { tool_calls: msg.tool_calls } : {}) });
    if (!msg.tool_calls?.length) {
      r.final = String(msg.content ?? "").slice(0, 400);
      r.steps.push("(reply)");
      if (harness === "persistent") {
        r.triedToStop++;
        messages.push({ role: "user", content: PUSH });
        continue;
      }
      r.outcome = "stopped by itself";
      break;
    }
    r.steps.push(msg.tool_calls.map((tc) => tc.function.name).join(" + "));
    for (const tc of msg.tool_calls) {
      let parsed = {};
      try {
        parsed = JSON.parse(tc.function.arguments || "{}");
      } catch {
        /* leave empty */
      }
      const out = (sc.run[tc.function.name] ?? (() => "error: unknown tool"))(parsed, state);
      messages.push({ role: "tool", tool_call_id: tc.id, content: out });
      r.calls.push(tc.function.name);
    }
  }
  if (sc.solved) r.solved = sc.solved(state);
  return r;
}

const jobs = [];
for (const harness of HARNESSES) {
  for (const scenario of WANTED) {
    // A control task ends when the model finishes it, so the persistent harness adds nothing.
    if (SCENARIOS[scenario].control && harness !== "plain") continue;
    for (const model of MODELS) {
      for (const [mode, base] of Object.entries(GATEWAYS)) {
        for (let t = 1; t <= TRIALS; t++) {
          jobs.push({ harness, scenario, model, mode, base, session: `study-${harness}-${scenario}-${model.split("/").pop()}-${mode.replace(" ", "")}-${t}-${Date.now().toString(36)}` });
        }
      }
    }
  }
}

const results = [];
let next = 0;
await Promise.all(
  Array.from({ length: Math.min(PARALLEL, jobs.length) }, async () => {
    while (next < jobs.length) {
      const job = jobs[next++];
      const r = await runAgent(job);
      results.push({ ...r, mode: job.mode });
      process.stderr.write(
        `${job.harness.padEnd(10)} ${job.scenario.padEnd(14)} ${job.model.split("/").pop().padEnd(18)} ${job.mode.padEnd(8)} ${String(r.turns).padStart(2)} turns  ${r.outcome}` +
          `${r.firstNudge ? `  (first notice on turn ${r.firstNudge})` : ""}${r.error ? "  " + r.error : ""}\n`,
      );
    }
  }),
);

// ---- Report -------------------------------------------------------------------------------

const avg = (xs) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 0);
const num = (n) => Math.round(n).toLocaleString("en");
const short = (m) => m.split("/").pop();
const ok = results.filter((r) => r.outcome !== "request failed");
const cellOf = (harness, scenario, model, mode) => ok.filter((r) => r.harness === harness && r.scenario === scenario && r.model === model && r.mode === mode);
const describe = (r) =>
  (r.outcome === "ran to the turn limit" ? r.outcome : `${r.outcome} on turn ${r.turns}`) +
  (r.firstNudge && r.firstNudge < r.turns ? ` (first notice on turn ${r.firstNudge})` : "");

const lines = [];
lines.push(
  `Run on ${new Date().toISOString().slice(0, 10)} through Bifrost with the session guard's default settings. ` +
    `${TRIALS} trial${TRIALS === 1 ? "" : "s"} per row, at most ${MAX_TURNS} turns each. "Dry run" is the same gateway and plugin with the guard only reporting, so it is the baseline. ` +
    `In dry run, "first notice" is the turn on which the guard would have acted.`,
);
lines.push("");

const stuck = WANTED.filter((s) => !SCENARIOS[s].control);
const controls = WANTED.filter((s) => SCENARIOS[s].control);

for (const harness of HARNESSES) {
  if (!stuck.length) break;
  lines.push(harness === "plain" ? "## Plain agent: it ends when the model stops calling tools" : `## Persistent harness: every attempt to stop is answered with "${PUSH}"`);
  lines.push("");
  for (const scenario of stuck) {
    lines.push(`### ${SCENARIOS[scenario].title}`);
    lines.push("");
    lines.push("| Model | Guard | What happened in each trial | Average turns | Average tokens |");
    lines.push("| --- | --- | --- | --- | --- |");
    for (const model of MODELS) {
      for (const mode of Object.keys(GATEWAYS)) {
        const cell = cellOf(harness, scenario, model, mode);
        lines.push(`| ${short(model)} | ${mode} | ${cell.map(describe).join("; ") || "no completed trial"} | ${avg(cell.map((r) => r.turns)).toFixed(1)} | ${num(avg(cell.map((r) => r.tokens)))} |`);
      }
    }
    lines.push("");
  }
  // One line per harness: what the guard changed overall.
  const dry = ok.filter((r) => r.harness === harness && r.mode === "dry run" && !SCENARIOS[r.scenario].control);
  const on = ok.filter((r) => r.harness === harness && r.mode === "guard on" && !SCENARIOS[r.scenario].control);
  if (dry.length && on.length) {
    const limit = (xs) => xs.filter((r) => r.outcome === "ran to the turn limit").length;
    lines.push(
      `Across these scenarios: ${limit(dry)} of ${dry.length} dry-run sessions and ${limit(on)} of ${on.length} guarded sessions ran to the turn limit. ` +
        `Average per session: ${avg(dry.map((r) => r.turns)).toFixed(1)} turns and ${num(avg(dry.map((r) => r.tokens)))} tokens in dry run, ` +
        `${avg(on.map((r) => r.turns)).toFixed(1)} turns and ${num(avg(on.map((r) => r.tokens)))} tokens with the guard on.`,
    );
    lines.push("");
  }
}

if (controls.length && HARNESSES.includes("plain")) {
  lines.push("## Controls: tasks that can be finished");
  lines.push("");
  lines.push("The guard should do nothing here. A notice in this table is the guard acting on a task that could be finished.");
  lines.push("");
  lines.push("| Task | Model | Guard | What happened in each trial | Task finished | Guard notices |");
  lines.push("| --- | --- | --- | --- | --- | --- |");
  for (const scenario of controls) {
    for (const model of MODELS) {
      for (const mode of Object.keys(GATEWAYS)) {
        const cell = cellOf("plain", scenario, model, mode);
        lines.push(`| ${SCENARIOS[scenario].title.replace("Control: ", "")} | ${short(model)} | ${mode} | ${cell.map(describe).join("; ") || "no completed trial"} | ${cell.filter((r) => r.solved).length} of ${cell.length} | ${cell.reduce((a, r) => a + r.notices, 0)} |`);
      }
    }
  }
  lines.push("");
}

const failed = results.filter((r) => r.outcome === "request failed");
if (failed.length) lines.push(`${failed.length} trial${failed.length === 1 ? "" : "s"} failed for reasons unrelated to the guard and ${failed.length === 1 ? "is" : "are"} left out.`, "");

mkdirSync(OUT, { recursive: true });
writeFileSync(join(OUT, "results.json"), JSON.stringify({ date: new Date().toISOString().slice(0, 10), trials: TRIALS, maxTurns: MAX_TURNS, models: MODELS, push: PUSH, results }, null, 2) + "\n");
writeFileSync(join(OUT, "results.md"), lines.join("\n") + "\n");
process.stdout.write(lines.join("\n") + "\n");
