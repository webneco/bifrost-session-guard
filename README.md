# Bifrost session guard

A plugin for the [Bifrost](https://github.com/maximhq/bifrost) AI gateway that stops a runaway agent session before it spends a whole team's budget.

Built for the Global AI Hackathon, Bifrost track, problem statement 04 ("Build a feature the open-source gateway is missing").

![The session guard dashboard: three sessions, one blocked, one nudged, one fine](docs/dashboard.png)

## The problem

Agents get stuck. A coding agent re-reads a file that does not exist, or edits a file, runs the tests, sees the same failure and edits again. Unattended agents are worse: the program driving the agent answers every "I cannot finish this" with "continue", and the session never ends. Each turn resends the whole conversation, so a stuck session is also an expensive one.

Bifrost can cap spend, but its budgets and rate limits belong to a virtual key, a team, a provider or a model. None of them belongs to a session. So when one agent loops:

1. Nothing stops it until the shared budget is gone.
2. Once the budget trips, Bifrost refuses **everyone** on that key, including the agents that were working normally.

Bifrost already knows which session a request belongs to. It reads `x-bf-session-id`, and the header that Claude Code, Codex CLI and OpenCode send on their own. Today it uses that only to keep a session on the same provider key.

## What the session guard does

It watches each session on its own and steps in for that session only.

- **Finds loops.** The same tool call returning the same result, a sequence of steps going round in circles, the model giving the same reply, or a client resending an unchanged conversation.
- **Finds sessions that are going nowhere.** Turn after turn with nothing new: calls that return what they returned before, a fix that leaves the same failure in place, or a model that keeps saying it is finished while its harness keeps telling it to continue. Nothing in such a session repeats back to back, so a simple repeat counter misses it.
- **Tells the model first.** At the first sign of a loop it adds a short notice to the system prompt, saying what is repeating and that repeating it will not help. That gives the model a chance to change course by itself.
- **Blocks if that fails.** If the loop continues, the session gets an error that client libraries do not retry by default. Other sessions on the same key carry on.
- **Caps a session.** Optional limits on requests, tokens, cost and duration for one session.
- **Shows what it did.** Response headers on every request, a line in Bifrost's own request log, and a small status page.

No client changes are needed. Sessions come from the headers Bifrost already understands. When a request has none, the guard identifies the conversation by how it opened.

## Tested on real models

The demo below uses a scripted model so that it costs nothing. The guard was also run against Claude Haiku 5.5 and Claude Sonnet 5.5 through a real Bifrost gateway: 96 sessions, with the guard on and in dry run as the baseline. The full write-up is in [study/](study/README.md).

| What was tested | Without the guard (dry run) | With the guard |
| --- | --- | --- |
| A stuck agent, left to itself (36 sessions) | All 18 stopped by themselves | All 18 stopped by themselves |
| A stuck agent under a harness that answers every stop with "Continue" (36 sessions) | 0 of 18 stopped; all ran to the 24-turn limit | 16 of 18 blocked |
| Tokens per session under that harness | 82,122 | 42,792 (48% fewer) |
| Healthy tasks that can be finished (24 sessions) | 12 of 12 finished | 0 notices, 0 blocks; 11 of 12 finished |

Three things came out of it:

- **These models stop by themselves.** A stuck tool alone did not produce a runaway session. The runaway sessions came from the harness: the models tried to stop nine times per session on average and were told to continue each time.
- **The rules this project started with did not stop those sessions.** Nothing in them repeats back to back. The `no_progress` rule was written because of that result, and all 16 blocks came from it.
- **The guard is slow on an agent that keeps trying new things that fail.** In the failing-test scenario it blocked 4 of 6 sessions, and late. It can only tell that a call failed by reading the result text, so it reports those failures and does not act on them. An earlier version that did act on them talked a model out of a task it could have finished; the study has the numbers. Session limits are the backstop.

## Run the demo

The demo needs no API keys. A scripted mock stands in for the model provider, so no request reaches a real model and nothing is spent. Prices are the catalog prices for `gpt-4o-mini`, which the gateway downloads when it starts.

With Docker, on any system:

```bash
docker build -t bifrost-session-guard .
docker run --rm -it -p 8080:8080 -p 9797:9797 bifrost-session-guard
```

Or directly on Linux or macOS, with Go 1.21+, Node 20+, git and a C compiler:

```bash
make demo
```

On Windows use Docker or WSL, because Go plugins are not supported there. The first build compiles the gateway from source and takes about five minutes on a clean machine (about nine with Docker). After that the demo runs in about 20 seconds.

The demo runs the same two agents twice on one virtual key with a $0.06 daily budget. Agent A is stuck re-reading a missing file. Agent B does normal work.

Without the guard, agent A runs for 22 turns until the budget is gone, and agent B is then refused on its first request:

```
Agent A (stuck):
  turn  1  read_file({"path":"config.yaml"})  ->  error: config.yaml: no such file or directory
  ...
  turn 22  read_file({"path":"config.yaml"})  ->  error: config.yaml: no such file or directory
  turn 23  HTTP 402
           Budget exceeded: vk_model_config * budget exceeded: 0.0625 >= 0.0600 dollars

Agent B (normal work), on the same key:
  turn  1  HTTP 402
           Budget exceeded: vk_model_config * budget exceeded: 0.0625 >= 0.0600 dollars
```

With the guard, agent A is told it is repeating itself on turns 5 to 7 and stopped on turn 8. Agent B finishes its work:

```
Agent A (stuck):
  turn  4  read_file({"path":"config.yaml"})  ->  error: ...   [guard: allow]
  turn  5  read_file({"path":"config.yaml"})  ->  error: ...   [guard: nudge · repeat_tool_call]
  turn  6  read_file({"path":"config.yaml"})  ->  error: ...   [guard: nudge · repeat_tool_call]
  turn  7  read_file({"path":"config.yaml"})  ->  error: ...   [guard: nudge · repeat_tool_call]
  turn  8  HTTP 403   [guard: block · repeat_tool_call]
           Session guard stopped session agent-a: the same call to read_file returned the
           same result 7 times in a row. Change the approach or ask the user how to proceed;
           sending the same conversation again will be blocked again.

Agent B (normal work), on the same key:
  turn  3  answer: The project is a small demo service. ...   [guard: allow]

session   state     requests  blocked  nudges  tokens    spent
agent-a   blocked   7         1        3       128146    $0.0196
agent-b   ok        3         0        0       54564     $0.0083
agent-c   nudged    5         0        1       91246     $0.0140
```

Agent C is a third agent that stops by itself as soon as it reads the notice. The stuck agent cost $0.0196 where it had cost $0.0625, and the key stayed usable.

The Docker command leaves the guarded gateway running, as does `demo/run.sh --keep`. The status page is at http://127.0.0.1:9797 and Bifrost's own dashboard at http://127.0.0.1:8080.

## How it meets the brief

| Requirement in problem statement 04 | How this project meets it |
| --- | --- |
| Real operator problem, solved end to end | One stuck session can drain a shared budget and lock out a team. The guard detects it, intervenes, reports it and lets an operator reset it. |
| Public extension points | A standard Bifrost plugin. It uses `PreLLMHook`, `PostLLMHook` and the HTTP transport hooks, and nothing private. It imports only Bifrost's `core/schemas` package. |
| Reproducible integration with clear setup | One command builds a plugin-capable gateway from Bifrost's own source at a pinned release (v2.5.0), builds the plugin and checks the two against each other. `make demo` and `make e2e` then run against that gateway. See the note on builds below. |
| Tests, documentation, ready for an upstream pull request | 69 unit tests, 16 end-to-end tests against the real gateway, benchmarks, a [study with real models](study/README.md), this README, a [configuration reference](docs/configuration.md) and an [upstream plan](docs/upstream.md). A 20-line patch registers the guard as a built-in; with it, the same 16 tests pass on a statically linked gateway. |

**A note on builds.** The gateway binary Bifrost publishes is statically linked and cannot load any custom plugin. Bifrost's documentation asks plugin users to build the gateway with dynamic linking, and that is what `make gateway` and the Dockerfile do, from unmodified Bifrost source. Go also refuses to load a plugin unless every package it shares with the gateway is the same version. `scripts/build-plugin.sh` compares the two binaries and names any package that differs, so that mistake fails at build time with a clear message.

There is a second route with none of those constraints: `make gateway-builtin` compiles the guard into the gateway, the way an upstream merge would. See [Upstream](#upstream).

## Use it on your gateway

Build the gateway and the plugin for your Bifrost version:

```bash
make all            # .work/bin/bifrost-http and build/session-guard.so
```

Add the plugin to Bifrost's `config.json`:

```json
{
  "plugins": [
    {
      "name": "session-guard",
      "enabled": true,
      "path": "/path/to/session-guard.so",
      "config": {
        "limits": { "max_cost_usd": 5, "max_duration": "4h" },
        "status": { "listen": "127.0.0.1:9797" }
      }
    }
  ]
}
```

Every setting has a default, so `"config": {}` works. To try it safely on live traffic, start with `"dry_run": true`: the guard reports what it would do and never interferes.

To move to another Bifrost release, run `scripts/sync-deps.sh v2.x.y` and rebuild. Everything here was tested end to end on v2.5.0. The plugin also compiles and passes its unit tests against v2.4.0.

## Configuration

The settings you are most likely to change. The [configuration reference](docs/configuration.md) has all of them.

| Setting | Default | What it does |
| --- | --- | --- |
| `dry_run` | `false` | Report only: never block or nudge. |
| `limits.max_requests`, `max_tokens`, `max_cost_usd`, `max_duration` | off | Caps for one session. |
| `loops.repeat_tool_call.threshold` | `4` | Identical tool calls with identical results, in a row, before the guard acts. |
| `loops.repeat_tool_call.action` | `"nudge"` | `"warn"`, `"nudge"` or `"block"`. |
| `loops.repeat_tool_call.escalate_after` | `3` | Block after this many more repeats. `0` never blocks. |
| `loops.tool_cycle.repeats` | `3` | Times a sequence of steps must repeat. |
| `loops.no_progress.threshold` | `5` | Turns in a row with nothing new before the guard tells the model. |
| `loops.no_progress.escalate_after` | `3` | Block after this many more. `0` never blocks. |
| `loops.repeat_failure.action` | `"warn"` | One tool failing on new attempts, turn after turn. Reported only, unless set to `"nudge"`. |
| `loops.ignore_tools` | none | Tools that may repeat freely, such as `wait` or a status poll. |
| `session.fallback` | `"fingerprint"` | What to do with a request that has no session header. `"none"` leaves it unguarded. |
| `session.idle_ttl` | `"2h"` | How long an idle session is remembered. |
| `status.listen` | off | Address for the status page and API. |

## How it works

**Sessions.** The guard uses the session id Bifrost has already resolved from the request headers. With no header, it derives an id from the system prompt and the first user message. Agents resend their history on every turn, so that opening is the same for the whole conversation. Sessions are kept apart per virtual key.

**Loop detection reads the conversation, not a counter.** Every request carries the conversation so far, including each tool call and its result. The guard pairs calls with results and checks whether the end of that sequence repeats. That has three useful consequences:

- It needs no stored state, so every gateway replica reaches the same verdict.
- A person stepping in clears it naturally. A new user message means the end of the conversation no longer repeats.
- A blocked conversation stays blocked if the client simply sends it again.

A tool call counts as "the same" when the tool name matches and the JSON arguments differ only in whitespace or in the order of top-level keys. The text an assistant writes around a tool call is ignored, because models reword it ("let me try again") while repeating the same call.

**Progress, not only repetition.** Real sessions that go nowhere rarely repeat back to back, so a second pass walks the conversation backwards and counts the turns since something new last worked. A turn makes progress when one of its tool calls is new and did not fail. Three details matter:

- A reply that ends the turn, answered by a message the caller has sent before word for word, is a turn with nothing new. That is the signature of an unattended harness, and it is how the guard sees a loop that is in the harness and not in the model.
- A new call that works but leaves a failure exactly as it was is not progress. If the tests fail with the same output after an edit, the edit did not fix them, however new it was.
- Evidence read from the wording of a tool result (that it "looks like an error") is never enough to interrupt the model or block. Only the structure of the conversation is.

The count restarts at the last message a person wrote, and a conversation with no tool calls is never counted.

**The nudge.** For a chat request the notice is appended to the system message. For a Responses request it goes into `instructions`. The caller's own messages are never modified; the guard works on a copy. Anthropic-format requests arrive in Bifrost as Responses requests and are handled the same way.

**The block.** A loop is refused with HTTP 403 and a limit with 402, the status Bifrost's own budgets use. Client libraries retry 429 and 5xx responses, so those would only make a stuck agent hammer the gateway. Fallback to other providers is switched off for a blocked request.

**Accounting.** After each call the guard adds the tokens and the cost Bifrost calculated to the session. A streamed response is counted once, on its final chunk.

## Technologies used

| Part | Technology |
| --- | --- |
| Gateway | [Bifrost](https://github.com/maximhq/bifrost) v2.5.0 by Maxim AI, built from its unmodified source |
| Plugin | Go 1.27, built as a Go plugin (`-buildmode=plugin`). Its only dependency outside the standard library is Bifrost's `core/schemas` package. |
| Loop detection | Plain Go with FNV-1a and `hash/maphash` signatures. No model, database or external service is involved. |
| Status page and API | Go's `net/http`, with one embedded page of plain HTML, CSS and JavaScript. No front-end framework. |
| Demo and end-to-end tests | Node.js 22 with its built-in test runner and no npm packages: a scripted OpenAI-compatible mock provider and a small tool-calling agent |
| Study with real models | Claude Haiku 5.5 and Claude Sonnet 5.5 through Anthropic's API, routed by Bifrost; the agent and its report are one Node.js script |
| Build and packaging | Bash scripts, Make and a multi-stage Dockerfile |
| Continuous integration | GitHub Actions: unit tests, end-to-end tests, the demo and the Docker image on every push |
| AI coding tool | Claude (Anthropic). See [How AI was used](#how-ai-was-used). |

## What a client sees

Every guarded response carries these headers:

| Header | Value |
| --- | --- |
| `x-bf-session-guard` | `allow`, `warn`, `nudge` or `block` |
| `x-bf-session-guard-rule` | The rule that fired, such as `repeat_tool_call` or `limit_cost` |
| `x-bf-session-guard-would` | In dry run, the action that was held back |
| `x-bf-session-guard-session` | The session id the guard used |
| `x-bf-session-guard-requests` | Requests the session has made |
| `x-bf-session-guard-cost-usd` | What the session has spent |

A blocked request gets this body, with status 403 for a loop or 402 for a session limit:

```json
{
  "type": "session_guard_loop_detected",
  "is_bifrost_error": true,
  "status_code": 403,
  "error": {
    "type": "session_guard_loop_detected",
    "code": "repeat_tool_call",
    "message": "Session guard stopped session agent-a: the same call to read_file returned the same result 8 times in a row. Change the approach or ask the user how to proceed; sending the same conversation again will be blocked again."
  }
}
```

Bifrost adds its usual `extra_fields` (provider, model, request type) to that body.

## Status page and API

Set `status.listen` to turn them on. They run on their own listener, so they work on any Bifrost build. Set `status.token` whenever the address is not a loopback address.

| Endpoint | What it returns |
| --- | --- |
| `GET /` | The status page |
| `GET /api/sessions` | Every tracked session |
| `GET /api/session?key=...` | One session |
| `GET /api/summary` | Totals and the configuration in force |
| `POST /api/reset` with `{"key": "..."}` | Forgets a session, clearing its counters and any limit it hit |

## Tests

```bash
make test          # 69 unit tests with the race detector, no gateway needed
make e2e           # 16 tests against the real gateway with the plugin loaded
make e2e-builtin   # the same 16 tests with the guard compiled into the gateway
make bench
```

The end-to-end tests start Bifrost v2.5.0 with the plugin loaded and drive it with scripted agents. They cover OpenAI-style chat, streamed chat, the Responses API, Anthropic-format requests, header and fingerprint sessions, isolation between sessions, resending a blocked conversation, a loop driven by a harness, a fix that leaves the same failure, dry run, session limits and the status API.

The [study](study/README.md) is separate, because it needs an API key: real models, stuck and healthy tasks, guard on against dry run.

Measured on a 2-vCPU cloud VM (Xeon, 2.1 GHz): the pre-hook takes about 55 microseconds for a 400-message conversation whose tool results are 2 KB each, and the decision engine alone about 16 microseconds. Only the last 160 items of a conversation are examined, so the cost does not grow with conversation length.

## Limits

- **Tested on two models from one vendor.** The [study](study/README.md) used Claude Haiku 5.5 and Claude Sonnet 5.5, three trials per case, with synthetic tools. It shows a pattern, not a rate, and other models may behave differently.
- **An agent that keeps trying new things is not stopped quickly.** Guessing 70 file names is 70 new calls. The guard reports the run of failures and leaves the model alone, because it reads failure from free text and acting on that did harm in the study. Set a session limit for this.
- **A harness is recognised by its repeated message.** The guard treats a user message that repeats an earlier one word for word as automated. A supervisor that writes a different message each time looks like a person, so the count restarts and only session limits apply. A person who sends the same short message several times to an agent that is doing nothing new will, in time, be treated as a harness.
- **Counters live in memory, per gateway process.** Loop detection is stateless and works across replicas. Session limits and the status page see only the requests that reached that replica, so exact per-session limits across replicas need sticky routing at the load balancer. A restart clears the counters.
- **Fingerprint sessions group identical openings.** Without a session header, requests with the same system prompt and first user message count as one session. Loop detection is unaffected, because it reads each request's own history. Session limits would add such requests together, so set `session.fallback` to `"none"` if you use limits and have a service that sends one fixed prompt.
- **Results that differ only by noise.** A tool result with a timestamp or request id in it is a different result each time. A second, looser rule catches the same call repeating ten times whatever it returns.
- **Stateful Responses API.** A client that sends `previous_response_id` instead of its history gives the guard nothing to read, so only session limits apply.
- **Nested key order.** Arguments that differ only in the order of keys inside a nested object count as different calls.
- **Raw-body passthrough.** When Bifrost forwards the caller's body untouched, the guard cannot add a notice, so a nudge becomes a warning.
- **Go plugin constraints.** Linux and macOS only, and the plugin must be rebuilt for each Bifrost release. Both come from Go's plugin system.

## Upstream

As a built-in plugin, the guard would work on Bifrost's standard binary with one line of configuration. `upstream/bifrost-v2.5.0-builtin.patch` is that change: 20 added lines in two files of the gateway.

```bash
make gateway-builtin          # a gateway with the guard compiled in
STATIC=1 make e2e-builtin     # the 16 end-to-end tests, on a statically linked build
```

[docs/upstream.md](docs/upstream.md) covers what a pull request would contain and the questions it leaves to the maintainers.

## How AI was used

The code, tests, demo and documentation in this repository were written with Claude (Anthropic), directed and reviewed by Aditya Vikram Singh. The plugin itself calls no model: it reads the conversations that pass through the gateway.

## Licence

Apache-2.0, the same licence as Bifrost.
