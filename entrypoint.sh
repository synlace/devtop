#!/bin/sh
# devtop container entrypoint: load the persisted AI key (if a volume is
# mounted), start the CopilotKit runtime, then run the Go server (API + React
# + /api/copilotkit proxy).

set -e

# The docker recipe mounts the host project at /workspace; default the data
# directory there so docs/tickets/threads from the mounted repo are served
# (an explicit DEVTOP_DIR override still wins).
if [ -d /workspace ]; then
  export DEVTOP_DIR="${DEVTOP_DIR:-/workspace/.devtop}"
fi
DEVTOP_DIR="${DEVTOP_DIR:-/app/.devtop}"
export DEVTOP_DIR
mkdir -p "$DEVTOP_DIR/docs" "$DEVTOP_DIR/tickets" "$DEVTOP_DIR/threads" "$DEVTOP_DIR/data"

# Load the persisted AI config from the key volume. It is a single .env-format
# file (AI_API_KEY / AI_BASE_URL / AI_MODEL) written by the runtime on save.
# Sourcing it exports all three to BOTH the Go server and the CopilotKit
# runtime so they always agree. If DEVTOP_AI_REMEMBER=0 the stored config is
# ignored (session-only).

# One-time migration from the legacy two-file layout (ai-key +
# ai-provider.json) to the unified .env file.
if [ "${DEVTOP_AI_REMEMBER:-1}" != "0" ] && [ -f /etc/devtop/ai-key ] && [ ! -f /etc/devtop/.env ]; then
  {
    echo "AI_API_KEY=$(cat /etc/devtop/ai-key)"
    if [ -f /etc/devtop/ai-provider.json ]; then
      _url=$(sed -n 's/.*"baseURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /etc/devtop/ai-provider.json)
      _model=$(sed -n 's/.*"model"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /etc/devtop/ai-provider.json)
      [ -n "$_url" ] && printf 'AI_BASE_URL=%s\n' "$_url"
      [ -n "$_model" ] && printf 'AI_MODEL=%s\n' "$_model"
    fi
  } > /etc/devtop/.env
  chmod 600 /etc/devtop/.env
  echo "  Migrated legacy AI config to /etc/devtop/.env"
fi

if [ "${DEVTOP_AI_REMEMBER:-1}" != "0" ] && [ -f /etc/devtop/.env ]; then
  set -a
  . /etc/devtop/.env
  set +a
  if [ -n "${AI_API_KEY:-}" ] && [ "${AI_API_KEY:-}" != "not-needed" ]; then
    echo "  AI assistant: configured (key from devtop-ai-config volume)"
  else
    echo "  AI assistant: not configured — add a key from the chat panel"
  fi
else
  echo "  AI assistant: not configured — add a key from the chat panel"
fi

# Start the CopilotKit runtime in the background.
if [ -f /app/frontend/copilot-server.js ]; then
  (cd /app/frontend && PORT=4000 node copilot-server.js) &
fi

# Run the Go server (API + SPA + /api/copilotkit proxy to the runtime above).
exec /app/devtop-bin -port 8000 -host 0.0.0.0
