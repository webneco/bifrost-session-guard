#!/usr/bin/env bash
# Pins this module's dependencies to the exact versions a Bifrost release was built with.
#
# Go refuses to load a plugin unless every package it shares with the host program is the
# same version, and "replace" directives count too. This script copies both from the
# release's transports/go.mod. Run it when moving to another Bifrost version:
#
#   scripts/sync-deps.sh v2.5.0
#
# Do not run "go mod tidy" afterwards: it would drop the pin on the transports module, which
# is what holds the shared dependencies at the gateway's versions.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:-$(cat "$ROOT/BIFROST_VERSION")}"
cd "$ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "https://raw.githubusercontent.com/maximhq/bifrost/transports/$VERSION/transports/go.mod" -o "$TMP/go.mod"

CORE="$(awk '$1=="github.com/maximhq/bifrost/core"{print $2}' "$TMP/go.mod" | head -1)"
GOVER="$(awk '$1=="go"{print $2}' "$TMP/go.mod" | head -1)"
[ -n "$CORE" ] && [ -n "$GOVER" ] || { echo "could not read versions from Bifrost's go.mod" >&2; exit 1; }

echo "Bifrost $VERSION: go $GOVER, core $CORE"
export GOWORK=off GOFLAGS=-mod=mod
go mod edit -go="$GOVER"
go mod edit -require="github.com/maximhq/bifrost/core@$CORE"
go get "github.com/maximhq/bifrost/transports/v2@$VERSION" >/dev/null

# Drop our existing replace directives, then copy the release's.
for old in $(go mod edit -json | python3 -c 'import json,sys; [print(r["Old"]["Path"]) for r in (json.load(sys.stdin).get("Replace") or [])]'); do
  go mod edit -dropreplace="$old"
done
grep '=>' "$TMP/go.mod" | sed 's/^replace[[:space:]]*//; s/^[[:space:]]*//' | while read -r old _arrow new newver; do
  go mod edit -replace="$old=$new@$newver"
done
go mod download >/dev/null
echo "$VERSION" > BIFROST_VERSION
echo "go.mod now matches Bifrost $VERSION"
