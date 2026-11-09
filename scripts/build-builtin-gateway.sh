#!/usr/bin/env bash
# Builds a Bifrost gateway with the session guard compiled in as a built-in plugin.
#
# This is what an upstream merge would give: no shared object, no dynamic linking, no
# version matching. It applies upstream/bifrost-<version>-builtin.patch (20 added lines in
# two files of the gateway) without touching the Bifrost source tree, using Go's -overlay
# and -modfile flags.
#
#   scripts/build-builtin-gateway.sh            # builds .work/bin/bifrost-http-builtin
#   STATIC=1 scripts/build-builtin-gateway.sh   # statically linked, like Bifrost's releases (Linux)
#
# Then enable it in config.json with a plugin entry that has no "path":
#   { "name": "session-guard", "enabled": true, "config": {} }
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIFROST_VERSION="${BIFROST_VERSION:-$(cat "$ROOT/BIFROST_VERSION")}"
WORK="${WORK:-$ROOT/.work}"
SRC="$WORK/bifrost-$BIFROST_VERSION"
PATCH="$ROOT/upstream/bifrost-$BIFROST_VERSION-builtin.patch"
OV="$WORK/builtin"
OUT="$WORK/bin/bifrost-http-builtin"

[ -f "$PATCH" ] || { echo "no patch for Bifrost $BIFROST_VERSION at $PATCH" >&2; exit 1; }
# The source tree and the web UI come from the ordinary gateway build.
[ -d "$SRC/transports/bifrost-http/ui" ] || "$ROOT/scripts/build-gateway.sh"

FILES="transports/bifrost-http/lib/config.go transports/bifrost-http/server/plugins.go"
rm -rf "$OV" && mkdir -p "$OV/tree"
for f in $FILES; do
  mkdir -p "$OV/tree/$(dirname "$f")"
  cp "$SRC/$f" "$OV/tree/$f"
done
# GIT_CEILING_DIRECTORIES stops git from noticing the repository this directory sits in.
# Inside a repository, "git apply" quietly skips files outside the current directory.
(cd "$OV/tree" && GIT_CEILING_DIRECTORIES="$OV" git apply -p1 "$PATCH")
for f in $FILES; do
  grep -q "sessionguard" "$OV/tree/$f" || { echo "the patch did not apply to $f" >&2; exit 1; }
done

cp "$SRC/transports/go.mod" "$OV/go.mod"
cp "$SRC/transports/go.sum" "$OV/go.sum"
go mod edit \
  -require=github.com/webneco/bifrost-session-guard@v0.0.0 \
  -replace="github.com/webneco/bifrost-session-guard=$ROOT" \
  "$OV/go.mod"

{
  printf '{"Replace":{'
  sep=""
  for f in $FILES; do
    printf '%s"%s":"%s"' "$sep" "$SRC/$f" "$OV/tree/$f"
    sep=","
  done
  printf '}}\n'
} > "$OV/overlay.json"

LDFLAGS="-w -s -X main.Version=$BIFROST_VERSION-session-guard"
TAGS=()
if [ -n "${STATIC:-}" ]; then
  LDFLAGS="$LDFLAGS -extldflags '-static'"
  TAGS=(-tags sqlite_static)
fi

echo "==> building the gateway with the session guard built in"
(
  cd "$SRC/transports/bifrost-http"
  CGO_ENABLED=1 GOWORK=off go build -mod=mod -modfile="$OV/go.mod" -overlay="$OV/overlay.json" \
    -ldflags="$LDFLAGS" "${TAGS[@]}" -trimpath -o "$OUT" . 2>&1 | { grep -v "statically linked applications requires\|in function \`" || true; }
)
[ -x "$OUT" ] || { echo "build failed" >&2; exit 1; }
echo "built $OUT"
