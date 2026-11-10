#!/usr/bin/env bash
# Starts two gateways (guard on, and dry run as the baseline), runs the study against real
# models, and stops the gateways. Extra arguments go to study.mjs, for example:
#
#   study/run.sh --trials 1 --models anthropic/claude-haiku-5-5
#
# Needs ANTHROPIC_API_KEY in the environment. Bifrost reads it from there; it is not written
# to disk.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATEWAY="${GATEWAY:-$ROOT/.work/bin/bifrost-http}"
PLUGIN_SO="${PLUGIN_SO:-$ROOT/build/session-guard.so}"
: "${ANTHROPIC_API_KEY:?set ANTHROPIC_API_KEY first}"
[ -x "$GATEWAY" ] && [ -f "$PLUGIN_SO" ] || { echo "run 'make gateway plugin' first" >&2; exit 1; }

DIR="$(mktemp -d)"
pids=()
cleanup() { kill "${pids[@]}" 2>/dev/null || true; rm -rf "$DIR"; }
trap cleanup EXIT

"$ROOT/study/config.sh" "$DIR/on" on "$PLUGIN_SO" 127.0.0.1:9791
"$ROOT/study/config.sh" "$DIR/dry" dry-run "$PLUGIN_SO" 127.0.0.1:9792
"$GATEWAY" -host 127.0.0.1 -port 8091 -app-dir "$DIR/on" >"$DIR/on.log" 2>&1 & pids+=($!)
"$GATEWAY" -host 127.0.0.1 -port 8092 -app-dir "$DIR/dry" >"$DIR/dry.log" 2>&1 & pids+=($!)
for port in 8091 8092; do
  for _ in $(seq 1 120); do
    curl -fsS -o /dev/null "http://127.0.0.1:$port/health" 2>/dev/null && break
    sleep 0.5
  done
done

node "$ROOT/study/study.mjs" --on http://127.0.0.1:8091 --dry http://127.0.0.1:8092 "$@"
