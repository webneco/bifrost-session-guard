# Configuration reference

The session guard reads its settings from the `config` object of its entry in Bifrost's `config.json`. Every setting is optional. Durations are strings such as `"90s"`, `"30m"` or `"4h"`.

```json
{
  "plugins": [
    {
      "name": "session-guard",
      "enabled": true,
      "path": "/path/to/session-guard.so",
      "config": {
        "dry_run": false,
        "session": { "idle_ttl": "2h", "max_sessions": 50000, "fallback": "fingerprint", "scope_by_virtual_key": true },
        "limits": { "max_requests": 0, "max_tokens": 0, "max_cost_usd": 0, "max_duration": "0s", "action": "block" },
        "loops": {
          "window": 160,
          "repeat_tool_call": { "enabled": true, "threshold": 4, "action": "nudge", "escalate_after": 3 },
          "repeat_tool_call_any_result": { "enabled": true, "threshold": 10, "action": "nudge", "escalate_after": 0 },
          "tool_cycle": { "enabled": true, "max_period": 6, "repeats": 3, "action": "nudge", "escalate_after": 2 },
          "repeat_output": { "enabled": true, "threshold": 4, "action": "nudge", "escalate_after": 3 },
          "repeat_request": { "enabled": true, "threshold": 8, "window": "2m", "action": "block" },
          "no_progress": { "enabled": true, "threshold": 5, "action": "nudge", "escalate_after": 3 },
          "repeat_failure": { "enabled": true, "threshold": 8, "action": "nudge", "escalate_after": 0 },
          "ignore_tools": []
        },
        "block": { "loop_status_code": 403, "limit_status_code": 402 },
        "nudge": { "suffix": "" },
        "status": { "listen": "", "token": "" }
      }
    }
  ]
}
```

The values above are the defaults. An invalid configuration stops the plugin from loading, with a message naming the setting.

## Actions

| Action | What happens |
| --- | --- |
| `warn` | The request passes. The finding is reported in the response headers, Bifrost's request log and the status page. |
| `nudge` | The request passes with a notice added to the system prompt, telling the model what it is repeating. |
| `block` | The request is refused before it reaches a provider. |

## Top level

| Setting | Default | Meaning |
| --- | --- | --- |
| `dry_run` | `false` | Turns every `nudge` and `block` into a `warn`. The response header `x-bf-session-guard-would` says what was held back. Use it to see what the guard would do on live traffic. |

## `session`

| Setting | Default | Meaning |
| --- | --- | --- |
| `idle_ttl` | `"2h"` | A session is forgotten this long after its last request. Its counters and any limit it hit go with it. |
| `max_sessions` | `50000` | Most sessions tracked at once. When full, the least recently used tenth is dropped. |
| `fallback` | `"fingerprint"` | For a request with no session id: `"fingerprint"` derives one from the system prompt and first user message; `"none"` leaves the request unguarded. |
| `scope_by_virtual_key` | `true` | Keeps the same session id under two virtual keys apart. |

With `"fingerprint"`, requests that open identically share a session. That is what makes an agent's turns one session. It also means a service that sends the same system prompt and first message for every call is counted as one session by `limits`. Use `"none"` if you set limits and have traffic like that.

Bifrost resolves the session id before the plugin runs. `x-bf-session-id` wins. Otherwise it uses the header a coding agent sends by itself: `x-claude-code-session-id`, `x-session-affinity`, `x-session-id`, `session-id`, `session_id`, `thread-id` or `conversation_id`.

## `limits`

Caps on what one session may consume. `0` turns a cap off. A cap is checked before each request against what the session has already used, so the request that crosses a cap completes and the next one is refused.

| Setting | Default | Meaning |
| --- | --- | --- |
| `max_requests` | `0` | Requests forwarded to a provider. |
| `max_tokens` | `0` | Total tokens, prompt plus completion. |
| `max_cost_usd` | `0` | Spend, as Bifrost calculates it from its model catalog. |
| `max_duration` | `"0s"` | Time since the session's first request. |
| `action` | `"block"` | `"block"` or `"warn"`. |

A session that has hit a limit stays refused until it is reset through the status API or forgotten after `idle_ttl`.

## `loops`

| Setting | Default | Meaning |
| --- | --- | --- |
| `window` | `160` | How many trailing conversation items are examined. One agent step is usually three items: assistant text, a tool call and its result. |
| `ignore_tools` | `[]` | Tool names that may repeat freely, for polling or waiting tools. |

The first four rules look for something repeating back to back at the end of the conversation. `no_progress` and `repeat_failure` look further back, for loops in which nothing repeats back to back: the agent rewords its messages, tries a new variation each time, or is told to continue by the program driving it. They were added after running real models through the gateway; see [study/](../study/README.md).

### `repeat_tool_call`

The same tool call returning the same result, several times in a row.

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | |
| `threshold` | `4` | Repeats in a row that trigger the rule. |
| `action` | `"nudge"` | `"warn"`, `"nudge"` or `"block"`. |
| `escalate_after` | `3` | Block once the repeats reach `threshold + escalate_after`. `0` never escalates. |

With the defaults, a request whose history holds four identical steps is nudged, and one holding seven is blocked.

### `repeat_tool_call_any_result`

The same tool call repeating whatever it returns. It catches results that differ only by noise, such as a timestamp. Same settings as above; defaults are `threshold` 10, `action` `"nudge"`, `escalate_after` 0. Add polling tools to `ignore_tools` instead of raising this.

### `tool_cycle`

A sequence of two or more steps repeating back to back, such as edit, test, edit, test.

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | |
| `max_period` | `6` | Longest sequence considered, in steps. |
| `repeats` | `3` | Times the sequence must repeat. |
| `action` | `"nudge"` | |
| `escalate_after` | `2` | Block once it has repeated `repeats + escalate_after` times. |

`window` must be at least `3 × max_period × (repeats + escalate_after)`.

### `repeat_output`

The model giving the same text reply several times in a row, with or without an automated "continue" between replies. Same settings as `repeat_tool_call`; defaults are `threshold` 4, `action` `"nudge"`, `escalate_after` 3.

### `repeat_request`

A client sending an unchanged agent conversation again after it was answered. Only answered requests count, so a client retrying after a provider error is not treated as looping. Only conversations that contain tool calls count, so a service that sends one fixed prompt many times is left alone.

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | |
| `threshold` | `8` | Times the same conversation is sent. |
| `window` | `"2m"` | The resends must fall within this time. |
| `action` | `"block"` | `"block"` or `"warn"`. |

### `no_progress`

Several turns in a row in which nothing new happened.

A turn makes progress when at least one of its tool calls is new (that call has not returned that result before) and did not fail. Two kinds of turn are counted as being without progress. Both are read from the structure of the conversation, not from its wording:

- **The turn's calls only returned what the same calls had already returned.** They need not be back to back: other calls, and the model's own messages, may sit between them.
- **The model ended its turn, and the reply was answered with a message the caller had already sent, word for word.** This is an unattended harness that answers every "I am done" or "I am stuck" with the same "continue". The model is not looping here; the program driving it is.

Three more things the rule knows:

- **An edit that leaves the same failure in place is not progress.** When a call fails with exactly the result it failed with the last time it was made, whatever was done in between did not fix it. Edit, run the tests, same failure, edit again: every edit is new and "works", and the rule still counts each round. A failure that changes from run to run (fewer failing tests each time) is left alone.
- **A new call that fails is not counted, and does not reset the count.** See `repeat_failure` below for why.
- **A message from a person starts again.** The count only runs back to the last user message that says something new. A user message that repeats an earlier one word for word is treated as automated and resets nothing.

A conversation with no tool calls at all is never counted: a person asking a chat model to "continue" a long answer is not a stalled agent. A call to a tool in `ignore_tools` counts as progress. Calls made together in one turn count as one turn.

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | |
| `threshold` | `5` | Turns in a row without progress that trigger the rule. |
| `action` | `"nudge"` | `"warn"`, `"nudge"` or `"block"`. |
| `escalate_after` | `3` | Block once the count reaches `threshold + escalate_after`. `0` never escalates. |

With the defaults, the model is told after five turns in which nothing new happened, and the session is blocked after eight.

### `repeat_failure`

One tool tried in a new way and failing, turn after turn: guessing one file name after another. A turn in which the tool succeeds ends the count. Calls made in the same turn count once. Only calls that had not been made before count, so the same test command failing differently on every run is not this rule's business.

Same settings as `repeat_tool_call`; defaults are `threshold` 8, `action` `"warn"`, `escalate_after` 0.

**By default this rule only reports.** The finding appears in the response headers, the request log and the status page; the model is not told and nothing is blocked. There are two reasons:

- The guard reads failure from the text of the tool result, because the OpenAI formats carry no failure flag. A result counts as a failure when it opens with a word such as `error`, `failed`, `fatal` or `traceback`, when its first 160 characters contain a phrase such as `no such file or directory` or `command not found`, or when it is a JSON object whose first key is a non-empty `error`. That can be wrong.
- A string of failed attempts is also what honest exploration looks like. When an earlier version of the guard sent the model a notice on this evidence, a real model gave up on a task it could have finished ([study](../study/README.md)).

Set `action` to `"nudge"` to tell the model, and `escalate_after` to block.

## `block`

| Setting | Default | Meaning |
| --- | --- | --- |
| `loop_status_code` | `403` | HTTP status for a loop. Must be a 4xx. Avoid 408, 409 and 429, which client libraries retry. |
| `limit_status_code` | `402` | HTTP status for a session limit. Bifrost uses 402 for an exceeded budget. |

## `nudge`

| Setting | Default | Meaning |
| --- | --- | --- |
| `suffix` | `""` | Text added to the end of the notice, for a house instruction such as "Ask the user before retrying." |

The notice starts with `[Bifrost session guard]`, names what is repeating and how many times, and ends with "Try a different approach, or stop and tell the user what is blocking you."

## `status`

| Setting | Default | Meaning |
| --- | --- | --- |
| `listen` | `""` | Address for the status page and API, such as `"127.0.0.1:9797"`. Empty leaves it off. |
| `token` | `""` | When set, API calls must send `Authorization: Bearer <token>`. Set it whenever `listen` is not a loopback address. |

## Recipes

Watch only, on live traffic:

```json
{ "dry_run": true, "status": { "listen": "127.0.0.1:9797" } }
```

Cap every session at $5 and four hours:

```json
{ "limits": { "max_cost_usd": 5, "max_duration": "4h" } }
```

Block at once, with no notice first:

```json
{ "loops": { "repeat_tool_call": { "threshold": 5, "action": "block" } } }
```

Let a scheduler that sends the same "check again" message poll for as long as it likes:

```json
{ "loops": { "ignore_tools": ["check_job"] } }
```

Stop a harness-driven loop sooner, or never block on it:

```json
{ "loops": { "no_progress": { "threshold": 4, "escalate_after": 2 } } }
```

```json
{ "loops": { "no_progress": { "escalate_after": 0 } } }
```

Guard only requests that carry a session header:

```json
{ "session": { "fallback": "none" } }
```
