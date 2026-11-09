#!/usr/bin/env bash
# demo       run the before/after demo, then keep the guarded gateway up (default)
# demo-once  run the demo and exit
# e2e        run the end-to-end tests
# gateway    run the gateway with the config in /app/data (mount your own config.json there)
set -euo pipefail
cd /app
case "${1:-demo}" in
  demo)
    exec demo/run.sh --keep
    ;;
  demo-once)
    exec demo/run.sh
    ;;
  e2e)
    exec node --test --test-concurrency=1 test/e2e.test.mjs
    ;;
  gateway)
    shift
    exec .work/bin/bifrost-http -host "${GW_HOST:-0.0.0.0}" -port "${GW_PORT:-8080}" -app-dir /app/data "$@"
    ;;
  *)
    exec "$@"
    ;;
esac
