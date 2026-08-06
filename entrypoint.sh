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

# Load a persisted key from the key volume. The runtime is the real consumer;
# exporting AI_API_KEY also lets the Go /api/config report has_key for legacy
# callers. If DEVTOP_AI_REMEMBER=0 the stored key is ignored (session-only).
if [ "${DEVTOP_AI_REMEMBER:-1}" != "0" ] && [ -f /etc/devtop/ai-key ]; then
  AI_API_KEY="$(cat /etc/devtop/ai-key)"
  export AI_API_KEY
  echo "  AI assistant: configured (key from devtop-ai-config volume)"
else
  echo "  AI assistant: not configured — add a key from the chat panel"
fi

# Start the CopilotKit runtime in the background.
if [ -f /app/frontend/copilot-server.js ]; then
  (cd /app/frontend && PORT=4000 node copilot-server.js) &
fi

# Run the Go server (API + SPA + /api/copilotkit proxy to the runtime above).
exec /app/devtop-bin -port 8000 -host 0.0.0.0
