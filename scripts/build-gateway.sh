#!/usr/bin/env bash
# Builds a plugin-capable Bifrost gateway from Bifrost's own source.
#
# The gateway binary Bifrost publishes is statically linked and cannot load Go plugins, so
# custom plugins need a dynamically linked build. This script follows Bifrost's documented
# steps (docs: plugins/building-dynamic-binary) for one pinned release.
#
#   scripts/build-gateway.sh            # builds .work/bin/bifrost-http
#   SKIP_UI=1 scripts/build-gateway.sh  # skip the web UI (faster; the Bifrost dashboard is then a stub page)
#
# Needs: git, Go 1.21+ (the pinned toolchain is fetched automatically), a C compiler, Node 20+.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIFROST_VERSION="${BIFROST_VERSION:-$(cat "$ROOT/BIFROST_VERSION")}"
WORK="${WORK:-$ROOT/.work}"
SRC="$WORK/bifrost-$BIFROST_VERSION"
OUT="$WORK/bin/bifrost-http"

if [ -x "$OUT" ] && [ "$(cat "$WORK/bin/.version" 2>/dev/null)" = "$BIFROST_VERSION" ] && [ -z "${FORCE:-}" ]; then
  echo "gateway $BIFROST_VERSION already built at $OUT (set FORCE=1 to rebuild)"
  exit 0
fi

for tool in git go cc; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done

mkdir -p "$WORK/bin"
if [ ! -d "$SRC/.git" ]; then
  echo "==> fetching Bifrost $BIFROST_VERSION"
  git clone --quiet --depth 1 --branch "transports/$BIFROST_VERSION" https://github.com/maximhq/bifrost.git "$SRC"
fi

UI_DIR="$SRC/transports/bifrost-http/ui"
if [ -n "${SKIP_UI:-}" ]; then
  echo "==> skipping the web UI"
  rm -rf "$UI_DIR" && mkdir -p "$UI_DIR"
  echo '<!doctype html><title>Bifrost</title><p>This gateway was built without its web UI (SKIP_UI=1).</p>' > "$UI_DIR/index.html"
else
  command -v npm >/dev/null || { echo "missing npm (or set SKIP_UI=1)" >&2; exit 1; }
  echo "==> building the web UI"
  (cd "$SRC/ui" && npm ci --no-audit --no-fund --silent && npx vite build >/dev/null)
  rm -rf "$UI_DIR" && cp -r "$SRC/ui/out" "$UI_DIR"
fi

echo "==> building the gateway with dynamic linking (this takes a few minutes the first time)"
(
  cd "$SRC/transports/bifrost-http"
  CGO_ENABLED=1 GOWORK=off go build \
    -ldflags="-w -s -X main.Version=$BIFROST_VERSION" \
    -trimpath \
    -o "$OUT" .
)
echo "$BIFROST_VERSION" > "$WORK/bin/.version"
echo "built $OUT"
