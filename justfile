devtop_src := justfile_directory()

# Default: print available recipes instead of launching anything.
default:
    @just --list
    @echo ""
    @echo "  devtop subcommands: build | serve | dev | image | docker | run | push | init | install | clean"
    @echo "  tip: run 'just devtop install' first (installs Go + frontend deps)"

devtop *args:
    #!/usr/bin/env bash
    set -euo pipefail
    set -- {{args}}
    SRC="{{devtop_src}}"
    PROJECT_DIR="$(pwd)"
    DEVTOP_DIR="${DEVTOP_DIR:-$PROJECT_DIR/.devtop}"
    IMAGE="${DEVTOP_IMAGE:-ghcr.io/synlace/devtop:latest}"

    require_frontend_deps() {
        if [ ! -d "$SRC/frontend/node_modules" ]; then
            echo "✘ Frontend dependencies missing — run: just devtop install"
            exit 1
        fi
    }

    # Export the repo-root .env (if any) so both the Go server and the Node
    # runtime inherit identical config, matching prod where entrypoint.sh
    # sources the volume's .env.
    load_env() {
        if [ -f "$SRC/.env" ]; then
            set -a
            . "$SRC/.env"
            set +a
        fi
    }

    case "${1:-}" in
        build)
            require_frontend_deps
            cd "$SRC/frontend"
            npm run build
            cd "$SRC"
            go build -o devtop-bin .
            echo "✔ devtop-bin ready (serves the built React app + API)"
            ;;
        serve)
            mkdir -p "$DEVTOP_DIR/docs" "$DEVTOP_DIR/tickets" "$DEVTOP_DIR/threads" "$DEVTOP_DIR/data"
            cd "$SRC"
            load_env
            export DEVTOP_DIR="$DEVTOP_DIR"
            export PORT="${PORT:-8000}"
            go run . -port "$PORT"
            ;;
        dev)
            require_frontend_deps
            mkdir -p "$DEVTOP_DIR/docs" "$DEVTOP_DIR/tickets" "$DEVTOP_DIR/threads" "$DEVTOP_DIR/data"
            cd "$SRC"
            load_env
            export DEVTOP_DIR="$DEVTOP_DIR"
            PORT="${PORT:-8000}"
            echo "  devtop dev loop: Go API :$PORT · CopilotKit :4000 · Vite :5173"
            echo "  (frontend edits hot-reload; Ctrl+C stops all three)"
            # setsid: run the Go server in its own process group so the trap can
            # kill the whole tree (go run would otherwise orphan its compiled
            # child and leave :$PORT bound after Ctrl+C).
            setsid go run . -port "$PORT" &
            GO_PID=$!
            # copilot-server.js falls back to process.env.PORT; pin it to 4000 so
            # the recipe-level PORT does not leak into the CopilotKit runtime.
            (cd "$SRC/frontend" && PORT=4000 DEVTOP_GO_URL="http://127.0.0.1:$PORT" node copilot-server.js) &
            NODE_PID=$!
            trap 'kill -TERM -"$GO_PID" 2>/dev/null || true; kill "$NODE_PID" 2>/dev/null || true' EXIT INT TERM
            cd "$SRC/frontend"
            npm run dev -- --host
            ;;
        image)
            cd "$SRC"
            docker build -t devtop:latest .
            echo "✔ Built devtop:latest"
            ;;
        docker)
            cd "$SRC"
            docker build -t devtop:latest .
            docker run --rm -it \
              -v "$PROJECT_DIR:/workspace" \
              -v devtop-ai-config:/etc/devtop \
              -p 8000:8000 \
              -e AI_API_KEY="${AI_API_KEY:-}" \
              devtop:latest
            ;;
        run)
            docker run --rm -it \
              -v "$PROJECT_DIR:/workspace" \
              -v devtop-ai-config:/etc/devtop \
              -p 8000:8000 \
              -e AI_API_KEY="${AI_API_KEY:-}" \
              "$IMAGE"
            ;;
        push)
            cd "$SRC"
            docker build -t "$IMAGE" .
            docker push "$IMAGE"
            ;;
        init)
            mkdir -p "$DEVTOP_DIR/docs" "$DEVTOP_DIR/tickets" "$DEVTOP_DIR/threads" "$DEVTOP_DIR/data"
            echo "✔ Created $DEVTOP_DIR/ directory structure"
            ;;
        install)
            cd "$SRC"
            go mod download
            echo "✔ Downloaded devtop Go dependencies"
            cd "$SRC/frontend"
            npm install
            echo "✔ Installed devtop frontend dependencies"
            ;;
        clean)
            rm -rf "$SRC/frontend/dist" "$SRC/devtop-bin"
            rm -rf "$DEVTOP_DIR"
            echo "✔ Removed frontend/dist, devtop-bin, and $DEVTOP_DIR"
            ;;
        *)
            echo "Unknown devtop subcommand: ${1:-}"
            echo "Usage: just devtop build|serve|dev|image|docker|run|push|init|install|clean"
            echo "Hint: run 'just devtop install' first (Go + frontend deps)"
            exit 1
            ;;
    esac

# Run UI tests with Playwright (headless Chromium).
#
# Tests are hermetic: playwright.config.cjs launches a Go backend pointed at a
# fixture .devtop directory plus a Vite dev server on port 5174.
#
# NixOS: uses the system Playwright from the Nix store (browsers already
# installed via maxos.tools.playwright) — no downloads.
# Non-NixOS: falls back to the locally installed @playwright/test.
#
# Usage:
#   just test ui              → run all hermetic UI specs
#   just test ui smoke        → run one spec file
#   just test ui --headed     → watch the browser
#   just test ai              → hermetic specs, then the @ai specs (live, paid
#                              model calls using the key in devtop/.env)
#   just test ai ai-chat      → only the AI specs
#
# The @ai specs write into a throwaway fixture clone, so they run serially and
# in a separate invocation from the hermetic specs — the hermetic run always
# sees the pristine 3-ticket seed.
test *args:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{devtop_src}}/frontend"
    # The NixOS env var PLAYWRIGHT_LAUNCH_OPTIONS_EXECUTABLE_PATH points at
    # Firefox; UI tests run Chromium, so unset it to avoid hijacking launches.
    unset PLAYWRIGHT_LAUNCH_OPTIONS_EXECUTABLE_PATH
    # "ui" is the recipe's subcommand, not a Playwright file filter.
    set -- {{args}}
    if [ "${1:-}" = "ui" ]; then
        shift
    fi
    # "ai" enables the real CopilotKit runtime webServer + @ai specs.
    AI=0
    if [ "${1:-}" = "ai" ]; then
        AI=1
        shift
    fi

    # The system Playwright from the Nix store can drift out of sync with
    # node_modules (e.g. a Docker `just devtop dev` runs npm install inside the
    # container and replaces the postinstall symlinks with a real install).
    # Prefer the local copy when present: it always resolves to the same
    # instance as the spec imports. Fall back to the system binary otherwise.
    if [ -x node_modules/.bin/playwright ]; then
        echo "Running UI tests with local Playwright..."
        RUN=(node_modules/.bin/playwright)
    elif command -v playwright > /dev/null 2>&1; then
        echo "Running UI tests with system Playwright..."
        RUN=(playwright)
    else
        echo "Running UI tests via npx Playwright..."
        RUN=(npx playwright)
    fi

    if [ "$AI" = "1" ]; then
        export DEVTOP_AI_TESTS=1
        if [ "$#" -gt 0 ]; then
            # Explicit spec filter: run it in AI mode (serial).
            "${RUN[@]}" test --workers 1 "$@"
        else
            echo "Phase 1: real-AI specs (serial)..."
            "${RUN[@]}" test --grep @ai --workers 1
            echo "Phase 2: hermetic specs (pristine fixture)..."
            unset DEVTOP_AI_TESTS
            "${RUN[@]}" test
        fi
    else
        "${RUN[@]}" test "$@"
    fi
