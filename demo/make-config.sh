#!/usr/bin/env bash
# Writes a Bifrost config.json for the demo into an app directory.
#
#   demo/make-config.sh <app-dir> <on|off|dry-run> [plugin.so]
#
# Environment: MOCK_URL, STATUS_ADDR, BUDGET_USD, GUARD_CONFIG (JSON for the plugin's config).
#
# The config points the "openai" provider at the mock provider, so model names and catalog
# prices are real while no request leaves the machine. It defines one virtual key with a
# small daily budget, shared by every agent in the demo.
set -euo pipefail

APP_DIR="${1:?app dir}"
MODE="${2:?on, off or dry-run}"
PLUGIN_SO="${3:-}"
MOCK_URL="${MOCK_URL:-http://127.0.0.1:7300}"
STATUS_ADDR="${STATUS_ADDR:-127.0.0.1:9797}"
BUDGET_USD="${BUDGET_USD:-0.05}"

mkdir -p "$APP_DIR"

# GUARD_CONFIG, when set, is used as the plugin's whole "config" object.
PLUGINS='[]'
if [ "$MODE" != "off" ]; then
  [ -n "$PLUGIN_SO" ] || { echo "plugin .so path required when the guard is on" >&2; exit 1; }
  DRY=false
  [ "$MODE" = "dry-run" ] && DRY=true
  if [ -n "${GUARD_CONFIG:-}" ]; then
    CONFIG="$GUARD_CONFIG"
  else
    CONFIG="{ \"dry_run\": $DRY, \"status\": { \"listen\": \"$STATUS_ADDR\" } }"
  fi
  PLUGINS=$(cat <<JSON
[
    {
      "name": "session-guard",
      "enabled": true,
      "path": "$PLUGIN_SO",
      "config": $CONFIG
    }
  ]
JSON
)
fi

cat > "$APP_DIR/config.json" <<JSON
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": { "enable_logging": true, "drop_excess_requests": false },
  "providers": {
    "openai": {
      "network_config": { "base_url": "$MOCK_URL", "max_retries": 0 },
      "keys": [ { "id": "mock-openai", "name": "mock-openai", "value": "sk-mock-not-a-real-key", "models": ["*"], "weight": 1.0 } ]
    }
  },
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-demo-team",
        "name": "demo-team",
        "value": "sk-bf-demo-team",
        "is_active": true,
        "provider_configs": [ { "provider": "openai", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1 } ]
      }
    ],
    "budgets": [
      { "id": "budget-demo-team-daily", "max_limit": $BUDGET_USD, "reset_duration": "1d", "virtual_key_id": "vk-demo-team" }
    ]
  },
  "plugins": $PLUGINS
}
JSON
echo "wrote $APP_DIR/config.json (guard: $MODE)"
