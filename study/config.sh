#!/usr/bin/env bash
# Writes a Bifrost config.json that sends requests to a real provider, with the session guard on.
#
#   study/config.sh <app-dir> <on|dry-run> <plugin.so> [status-addr]
#
# The provider key is read by Bifrost from the ANTHROPIC_API_KEY environment variable; it is
# never written to disk.
set -euo pipefail
APP_DIR="${1:?app dir}"
MODE="${2:?on or dry-run}"
PLUGIN_SO="${3:?path to session-guard.so}"
STATUS_ADDR="${4:-127.0.0.1:9797}"
DRY=false
[ "$MODE" = "dry-run" ] && DRY=true
mkdir -p "$APP_DIR"
cat > "$APP_DIR/config.json" <<JSON
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": { "enable_logging": true, "drop_excess_requests": false },
  "providers": {
    "anthropic": {
      "network_config": { "max_retries": 1 },
      "keys": [ { "id": "anthropic-study", "name": "anthropic-study", "value": "env.ANTHROPIC_API_KEY", "models": ["*"], "weight": 1.0 } ]
    }
  },
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-study",
        "name": "study",
        "value": "sk-bf-study",
        "is_active": true,
        "provider_configs": [ { "provider": "anthropic", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1 } ]
      }
    ]
  },
  "plugins": [
    {
      "name": "session-guard",
      "enabled": true,
      "path": "$PLUGIN_SO",
      "config": { "dry_run": $DRY, "status": { "listen": "$STATUS_ADDR" } }
    }
  ]
}
JSON
echo "wrote $APP_DIR/config.json (guard: $MODE)"
