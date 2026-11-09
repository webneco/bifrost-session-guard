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

A client sending an unchanged conversation again after it was answered. Only answered requests count, so a client retrying after a provider error is not treated as looping.

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | |
| `threshold` | `8` | Times the same conversation is sent. |
| `window` | `"2m"` | The resends must fall within this time. |
| `action` | `"block"` | `"block"` or `"warn"`. |

## `block`

| Setting | Default | Meaning |
| --- | --- | --- |
| `loop_status_code` | `403` | HTTP status for a loop. Must be a 4xx. Avoid 408, 409 and 429, which client libraries retry. |
| `limit_status_code` | `402` | HTTP status for a session limit. Bifrost uses 402 for an exceeded budget. |

## `nudge`

| Setting | Default | Meaning |
| --- | --- | --- |
| `suffix` | `""` | Text added to the end of the notice, for a house instruction such as "Ask the user before retrying." |

The notice starts with `[Bifrost session guard]`, names what is repeating and how many times, and ends with "Do not repeat it. Try a different approach, or stop and tell the user what is blocking you."

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

Guard only requests that carry a session header:

```json
{ "session": { "fallback": "none" } }
```
