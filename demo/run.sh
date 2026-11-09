#!/usr/bin/env bash
# The demo: one stuck agent, with and without the session guard.
#
#   demo/run.sh          # runs both acts and exits
#   demo/run.sh --keep   # leaves the guarded gateway running afterwards, to explore:
#                        #   session guard dashboard  http://127.0.0.1:9797
#                        #   Bifrost dashboard        http://127.0.0.1:8080
#
# Everything is local. The "openai" provider points at a scripted mock, so the model name
# and prices are gpt-4o-mini's while nothing is sent to OpenAI and nothing is spent.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATEWAY="${GATEWAY:-$ROOT/.work/bin/bifrost-http}"
PLUGIN_SO="${PLUGIN_SO:-$ROOT/build/session-guard.so}"
DATA="$ROOT/demo/data"
GW_PORT="${GW_PORT:-8080}"
export MOCK_URL="http://127.0.0.1:${MOCK_PORT:-7300}"
export STATUS_ADDR="${STATUS_ADDR:-127.0.0.1:9797}"
export BUDGET_USD="${BUDGET_USD:-0.06}"
VK="sk-bf-demo-team"
KEEP=""
[ "${1:-}" = "--keep" ] && KEEP=1

[ -x "$GATEWAY" ] || { echo "gateway not built: run 'make gateway'"; exit 1; }
[ -f "$PLUGIN_SO" ] || { echo "plugin not built: run 'make plugin'"; exit 1; }

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

wait_for() { # url, name
  for _ in $(seq 1 240); do curl -sf -m 2 "$1" >/dev/null 2>&1 && return 0; sleep 0.25; done
  echo "$2 did not start; see $DATA/*.log" >&2; exit 1
}

start_gateway() { # mode
  rm -rf "$DATA/$1" && mkdir -p "$DATA/$1"
  "$ROOT/demo/make-config.sh" "$DATA/$1" "$1" "$PLUGIN_SO" >/dev/null
  "$GATEWAY" -port "$GW_PORT" -app-dir "$DATA/$1" > "$DATA/gateway-$1.log" 2>&1 &
  GW_PID=$!
  PIDS+=("$GW_PID")
  wait_for "http://127.0.0.1:$GW_PORT/health" "gateway"
}
stop_gateway() { kill "$GW_PID" 2>/dev/null || true; wait "$GW_PID" 2>/dev/null || true; }

agent() { node "$ROOT/demo/agent.mjs" --base-url "http://127.0.0.1:$GW_PORT" --key "$VK" "$@"; }

mkdir -p "$DATA"
node "$ROOT/demo/mock-provider.mjs" "${MOCK_PORT:-7300}" > "$DATA/mock.log" 2>&1 &
PIDS+=("$!")
wait_for "$MOCK_URL/healthz" "mock provider"

bold "Setup"
echo "  One virtual key, 'demo-team', with a budget of \$$BUDGET_USD a day."
echo "  Two agents share it: one that gets stuck re-reading a missing file, and one doing normal work."

bold "Act 1: without the session guard"
start_gateway off
echo "  Agent A (stuck):"
agent --session agent-a --scenario stubborn --max-turns 40 | sed 's/^/  /'
echo
echo "  Agent B (normal work), on the same key:"
agent --session agent-b --scenario healthy | sed 's/^/  /'
stop_gateway
echo
echo "  The stuck agent ran until the key's budget was gone, and the budget then refused everyone on the key."

bold "Act 2: with the session guard"
start_gateway on
wait_for "http://$STATUS_ADDR/healthz" "session guard"
echo "  Agent A (stuck):"
agent --session agent-a --scenario stubborn --max-turns 40 | sed 's/^/  /'
echo
echo "  Agent B (normal work), on the same key:"
agent --session agent-b --scenario healthy | sed 's/^/  /'
echo
echo "  Agent C (stuck, but it listens when told it is repeating itself):"
agent --session agent-c --scenario loop | sed 's/^/  /'

bold "What the guard recorded"
curl -s "http://$STATUS_ADDR/api/sessions" | node -e '
  let d = ""; process.stdin.on("data", (c) => (d += c)).on("end", () => {
    const rows = JSON.parse(d).sort((a, b) => a.id.localeCompare(b.id));
    console.log("  session   state     requests  blocked  nudges  tokens    spent");
    for (const s of rows) console.log("  " + [s.id.padEnd(9), s.state.padEnd(9), String(s.requests).padEnd(9), String(s.blocked).padEnd(8), String(s.nudges).padEnd(7), String(s.total_tokens).padEnd(9), "$" + s.cost_usd.toFixed(4)].join(" "));
    const total = rows.reduce((n, s) => n + s.cost_usd, 0);
    console.log("\n  Total spent on the key: $" + total.toFixed(4) + " of a $'"$BUDGET_USD"' budget. The key stayed usable.");
    if (total === 0) console.log("  (Spend shows as $0 because the gateway could not load model prices. Act 1 needs them for the budget to trip.)");
  });'

if [ -n "$KEEP" ]; then
  bold "Still running"
  echo "  Session guard dashboard:  http://$STATUS_ADDR"
  echo "  Bifrost dashboard:        http://127.0.0.1:$GW_PORT"
  echo "  Press Ctrl+C to stop."
  wait "$GW_PID"
fi
