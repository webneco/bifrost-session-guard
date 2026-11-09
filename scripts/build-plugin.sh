#!/usr/bin/env bash
# Builds the session guard as a Go plugin (build/session-guard.so) and, when the gateway
# binary is present, checks that the two were built from the same dependency versions.
# A mismatch is the usual reason Bifrost refuses to load a plugin, so it is caught here
# with a readable message instead of at gateway start.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${WORK:-$ROOT/.work}"
GATEWAY="${GATEWAY:-$WORK/bin/bifrost-http}"
OUT="${OUT:-$ROOT/build/session-guard.so}"
cd "$ROOT"

mkdir -p "$(dirname "$OUT")"
echo "==> building $OUT"
CGO_ENABLED=1 GOWORK=off go build -buildmode=plugin -ldflags="-w -s" -trimpath -o "$OUT" ./cmd/sessionguard

if [ ! -x "$GATEWAY" ]; then
  echo "note: $GATEWAY not found, skipping the version check (run scripts/build-gateway.sh first)"
  exit 0
fi

echo "==> checking the plugin against the gateway"
deps() { go version -m "$1" | awk '$1=="dep"{v=$3} $1=="=>"{v=$2"@"$3} $1=="dep"||$1=="=>"{ if($1=="dep"){name=$2} m[name]=v } END{for(k in m) print k, m[k]}' | sort; }
gover() { go version "$1" | awk '{print $NF}'; }

if [ "$(gover "$OUT")" != "$(gover "$GATEWAY")" ]; then
  echo "MISMATCH: plugin built with $(gover "$OUT"), gateway with $(gover "$GATEWAY")" >&2
  exit 1
fi
deps "$GATEWAY" > "$WORK/gateway.deps"
deps "$OUT" > "$WORK/plugin.deps"
BAD="$(join "$WORK/plugin.deps" "$WORK/gateway.deps" | awk '$2!=$3{print "  "$1": plugin "$2", gateway "$3}')"
if [ -n "$BAD" ]; then
  echo "MISMATCH: these packages differ between the plugin and the gateway:" >&2
  echo "$BAD" >&2
  echo "Run scripts/sync-deps.sh <bifrost version> and rebuild." >&2
  exit 1
fi
echo "ok: $(wc -l < "$WORK/plugin.deps" | tr -d ' ') shared-or-own packages checked, Go $(gover "$OUT")"
