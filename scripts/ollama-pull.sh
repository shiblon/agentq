#!/usr/bin/env bash
# Pull the default test model into the local Ollama instance.
# Run once after `docker compose up -d ollama`.
# Override OLLAMA_HOST or MODEL as needed.
set -euo pipefail

OLLAMA_HOST=${OLLAMA_HOST:-http://localhost:11434}
MODEL=${MODEL:-qwen2.5:0.5b}

echo "Pulling ${MODEL} from ${OLLAMA_HOST} ..."
curl -fsSL "${OLLAMA_HOST}/api/pull" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"${MODEL}\"}" | grep -o '"status":"[^"]*"' | sed 's/"status":"//;s/"//'

echo "Done."
