# devtop

A local, AI-assisted workspace for a repository's documentation and tickets. The
Go backend serves the React app, the REST API, and proxies the CopilotKit AI
runtime — everything on a single port.

## Quick start (Docker — no repo, no toolchain)

```console
# from any repository:
docker run --rm -it \
  -u "$(id -u):$(id -g)" \
  -v "$PWD:/workspace" \
  -v devtop-ai-config:/etc/devtop \
  -p 8000:8000 \
  ghcr.io/synlace/devtop:latest
```

Open http://127.0.0.1:8000. Classic single-repo mode seeds `./.devtop/`
(docs, tickets, threads) in your repo on first run.

Notes:

- `-u "$(id -u):$(id -g)"` runs the container as your user, so files it
  creates in the mounted repo are owned by you, not root.
- The `devtop-ai-config` volume holds the instance's state: the AI key
  (`/etc/devtop/.env`) **and the registered-repo list**
  (`/etc/devtop/repos.json`). If the volume was created by an older
  root-running container, fix its ownership once:
  `docker run --rm -v devtop-ai-config:/etc/devtop alpine chown -R "$(id -u):$(id -g)" /etc/devtop`
  (or use a host bind instead of a named volume:
  `-v "$HOME/.config/devtop:/etc/devtop"`).
- With no volume, the registry is held in the container's ephemeral config
  dir and is lost on `docker rm`.

### Multiple repositories

```console
# one instance, several repos (run from a parent folder):
docker run --rm -it \
  -u "$(id -u):$(id -g)" \
  -v "$PWD:/workspace" \
  -v "$HOME/.config/devtop:/etc/devtop" \
  -p 8000:8000 \
  ghcr.io/synlace/devtop:latest
```

The header chip lists every registered repo — pick one, or **Manage repos… →
Add repo…** to browse `/workspace` and register roots. Registered roots
persist in `/etc/devtop/repos.json`, so `docker rm`/`docker run` keeps your
repos. A fresh launch in a plain folder (no repo, no `.git`) boots with zero
repos and nothing written: the first-run page offers **Add repo…**, and
`.devtop/` is scaffolded only when a repo is initialized.

### AI assistant key

The AI key is entered through the UI only — never on the command line, so it
never appears in shell history or `docker inspect`:

- Open the chat panel → **AI assistant is not configured** → paste your
  OpenRouter key → **Save**.
- With the `devtop-ai-config` volume mounted, the key, base URL, and model are
  persisted as a 0600 `.env` file (`/etc/devtop/.env`) and remembered across
  restarts. The same `.env` format is used in dev (repo-root `.env`).
- Without the volume it is **session-only** (held in the runtime's memory, gone
  on restart). The panel explains how to add the volume.
- Use the key button in the chat header (or the settings panel) to remove the
  key at any time.
- CI/scripting: `docker run --env-file ~/.devtop.env ...` (no key on the
  command line).

## Development (in this repo)

```console
just devtop build   # build the React app + devtop-bin
just devtop serve   # run the built app + API on :8000
just devtop dev     # dev loop: Vite HMR (:5173) + CopilotKit (:4000) + Go (:8000)
just devtop docker  # build the image and run it against the current dir
just devtop image   # build devtop:latest
just devtop run     # docker run (existing image) against the current dir
just devtop push    # build + push the image to the registry
just devtop init    # create ./.devtop structure
just devtop clean   # remove build artifacts and ./.devtop
```

`just devtop dev` runs on the host with no Docker: frontend edits hot-reload via
Vite; Go changes need a restart (the Go API serves only `/api/*`, so dev uses
the Vite dev server at :5173).

## How it's wired

- Go binary (`main.go`): `/api/*` routes + the built React app from
  `frontend/dist` (SPA fallback to `index.html`) + a reverse proxy for
  `/api/copilotkit/*` → the Node runtime.
- **Docs are stored as raw Markdown + frontmatter and served as-is** — the API
  returns the file body, never rendered HTML (`renderMD` is gone from the
  content endpoints). The source of truth is always the file on disk.
- Rendering is client-side in the browser via the unified pipeline
  (`frontend/src/RichMarkdown.tsx`): `react-markdown` + `remark-gfm` (tables,
  task lists) + `rehype-highlight` (code). Mermaid diagrams are rendered by a
  lazily-loaded component (`MermaidDiagram.tsx`) so ~2MB of mermaid only loads
  when a doc actually contains a diagram.
- CopilotKit runtime (`frontend/copilot-server.js`): the AI chat, agent tools
  (docs, tickets, and workspace file read/list), thread persistence, and the
  `ai-status` / `ai-key` endpoints that power the UI key prompt. The key lives
  in this process; the volume is optional.
- The old server-rendered (Alpine) frontend and Go templates were removed — the
  React app is the only frontend.

## Tests

```console
just test ui        # hermetic Playwright specs (no model calls)
just test ai        # hermetic specs, then live @ai specs (paid model calls)
```
