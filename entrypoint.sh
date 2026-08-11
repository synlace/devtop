#!/bin/sh
# devtop container entrypoint: fix the config volume ownership for the runtime
# user, start the CopilotKit runtime, then run the Go server (API + React +
# /api/copilotkit proxy).
#
# Started as root (the default `docker run` without -u), it makes a fresh
# devtop-ai-config volume — or a recreated bind source — writable by the
# requested uid:gid, then drops privileges for every app process. Started with
# -u, it skips the bootstrap and runs as the given user.

set -e

TARGET_UID="${TARGET_UID:-$(id -u)}"
TARGET_GID="${TARGET_GID:-$(id -g)}"

# The docker recipe mounts the host project at /workspace; default the data
# directory there so docs/tickets/threads from the mounted repo are served
# (an explicit DEVTOP_DIR override still wins).
if [ -d /workspace ]; then
  export DEVTOP_DIR="${DEVTOP_DIR:-/workspace/.devtop}"
fi
DEVTOP_DIR="${DEVTOP_DIR:-/app/.devtop}"
export DEVTOP_DIR

# The workspace must not be touched at boot: a fresh folder-of-repos mount
# writes nothing until a repo is added and initialized. The Go seed creates
# the classic-mode dirs, and every write path MkdirAlls its own directory, so
# nothing here pre-creates them.

# Fix the config volume once, then drop privileges. Docker creates a fresh
# named volume — or a recreated bind source — owned by root, which would break
# AI-key and registry persistence for the unprivileged runtime user. Running
# the bootstrap as root and chowning to $TARGET_UID:$TARGET_GID makes the
# volume usable for any host uid without permissive modes.
if [ "$(id -u)" -eq 0 ]; then
  mkdir -p /etc/devtop

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

  chown -R "$TARGET_UID:$TARGET_GID" /etc/devtop
  chmod 700 /etc/devtop
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

# Run everything as the configured user. When the container started as root,
# drop to $TARGET_UID:$TARGET_GID first so both the CopilotKit runtime and the
# Go server write only user-owned files (workspace mounts and the config
# volume). The exported env (DEVTOP_DIR, AI_*) carries through su-exec.
if [ "$(id -u)" -eq 0 ] && command -v su-exec >/dev/null 2>&1; then
  exec su-exec "$TARGET_UID:$TARGET_GID" /bin/sh -c \
    '(cd /app/frontend && PORT=4000 exec node copilot-server.js) & exec /app/devtop-bin -port 8000 -host 0.0.0.0'
fi
(cd /app/frontend && PORT=4000 exec node copilot-server.js) &
exec /app/devtop-bin -port 8000 -host 0.0.0.0