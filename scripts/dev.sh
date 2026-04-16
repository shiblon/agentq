#!/usr/bin/env bash
# scripts/dev.sh -- start agentq services for local development.
#
# Starts:
#   agentq serve  (in-memory EntroQ gRPC server, localhost:37706)
#   agentq api    (REST API + web UI, localhost:8080)
#   npm run dev   (Vite dev server with /api proxy, localhost:5173)
#
# Usage:
#   ./scripts/dev.sh
#
# Environment overrides:
#   AGENTQ_EQ_ADDR   -- queue server address (default: localhost:37706)
#   AGENTQ_API_ADDR  -- API server address   (default: :8080)
#   AGENTQ_CONFIG    -- agents.yaml path     (default: ./config/agents.yaml)

set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$PWD"
EQ_ADDR="${AGENTQ_EQ_ADDR:-localhost:37706}"
API_ADDR="${AGENTQ_API_ADDR:-:8080}"
API_PORT="${API_ADDR##*:}"
VITE_PORT=5173
CONFIG="${AGENTQ_CONFIG:-$REPO_ROOT/config/agents.yaml}"

pids=()

cleanup() {
  echo ""
  echo "Shutting down..."
  for pid in "${pids[@]+"${pids[@]}"}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Build agentq.
echo "Building agentq..."
cd "$REPO_ROOT"
go build -o "$REPO_ROOT/agentq" ./cmd/agentq

# Install web dependencies if needed.
if [ ! -d "$REPO_ROOT/web/node_modules" ]; then
  echo "Installing web dependencies..."
  npm --prefix "$REPO_ROOT/web" install
fi

# Start EntroQ in-memory queue server.
echo "Starting queue server on $EQ_ADDR..."
"$REPO_ROOT/agentq" serve --journal "" >"$REPO_ROOT/.eq.log" 2>&1 &
pids+=($!)

# Wait for the queue server to be ready.
for i in $(seq 1 20); do
  if "$REPO_ROOT/agentq" --eq-addr "$EQ_ADDR" sessions list >/dev/null 2>&1; then
    break
  fi
  sleep 0.3
done

# Start agentq API server.
echo "Starting API server on $API_ADDR..."
"$REPO_ROOT/agentq" api \
  --addr "$API_ADDR" \
  --eq-addr "$EQ_ADDR" \
  --config "$CONFIG" \
  >"$REPO_ROOT/.api.log" 2>&1 &
pids+=($!)

# Start Vite dev server.
echo "Starting web UI on port $VITE_PORT..."
npm --prefix "$REPO_ROOT/web" run dev -- --port "$VITE_PORT" >"$REPO_ROOT/.vite.log" 2>&1 &
pids+=($!)

# Wait for Vite to be ready.
for i in $(seq 1 30); do
  if curl -sf "http://localhost:$VITE_PORT/" >/dev/null 2>&1; then
    break
  fi
  sleep 0.3
done

echo ""
echo "Dev environment ready."
echo ""
echo "  Web UI:  http://localhost:$VITE_PORT"
echo "  API:     http://localhost:$API_PORT/api/v1/health"
echo ""
echo "Start a worker (in another terminal):"
echo "  ./agentq run --config $CONFIG"
echo ""
echo "Logs: .eq.log  .api.log  .vite.log  (tail -f to follow)"
echo "Press Ctrl-C to stop."
echo ""

wait
