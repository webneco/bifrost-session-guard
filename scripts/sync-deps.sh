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

# Start from an empty requirement list. Leftover pins from another release would otherwise
# hold shared packages at newer versions than this release uses.
MODULE="$(go list -m)"
cat > go.mod <<MOD
module $MODULE

go $GOVER

require github.com/maximhq/bifrost/core $CORE
MOD
rm -f go.sum
go get "github.com/maximhq/bifrost/transports/v2@$VERSION" >/dev/null 2>&1

# Copy the release's replace directives.
{ grep '=>' "$TMP/go.mod" || true; } | sed 's/^replace[[:space:]]*//; s/^[[:space:]]*//' | while read -r old _arrow new newver; do
  go mod edit -replace="$old=$new@$newver"
done
go mod download >/dev/null 2>&1
go build ./... # completes go.sum for this module's own packages
go vet ./... >/dev/null

echo "$VERSION" > BIFROST_VERSION
echo "go.mod now matches Bifrost $VERSION. Rebuild the gateway and the plugin for this version."
