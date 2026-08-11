import express from "express";
import dotenv from "dotenv";
import fs from "fs/promises";
import { readFileSync } from "fs";
import path from "path";
import { exec } from "child_process";
import { promisify } from "util";
import { z } from "zod";
import { createOpenAI } from "@ai-sdk/openai";
import { CopilotRuntime, BuiltInAgent, defineTool } from "@copilotkit/runtime/v2";
import { createCopilotExpressHandler } from "@copilotkit/runtime/v2/express";
import { PersistentAgentRunner } from "./persistent-runner.mjs";

const execAsync = promisify(exec);

// Load parent .env configuration with override to replace empty docker-passed variables
dotenv.config({ path: "../.env", override: true });

const app = express();

// Readiness probe for orchestrators (e.g. Playwright webServer checks).
app.get("/health", (req, res) => {
  res.send("ok");
});

// ---------------------------------------------------------------------------
// AI config management.
//
// The key is entered through the UI only — never on the command line. It is
// held in this process's memory. When a writable config volume is mounted (a
// named volume at /etc/devtop, detected via /proc/self/mountinfo) and
// DEVTOP_AI_REMEMBER is not "0", it is also persisted as a 0600 .env file so it
// survives restarts. Without a volume it stays session-only and is never
// written to disk. The Go server proxies /api/copilotkit/* to this runtime in
// prod; in dev the Vite proxy does the same.
//
// The same .env format is used in dev (repo-root .env, loaded above) and in
// prod (the volume's /etc/devtop/.env, sourced by entrypoint.sh and rewritten
// here on save). Precedence: persisted volume file > env vars > hardcoded
// default.
// ---------------------------------------------------------------------------
const aiEnvFile = process.env.DEVTOP_AI_ENV_FILE || "/etc/devtop/.env";
const aiRememberDisabled = process.env.DEVTOP_AI_REMEMBER === "0";

let baseURL = process.env.AI_BASE_URL || "https://openrouter.ai/api/v1";
let model = process.env.AI_MODEL || "deepseek/deepseek-v4-flash-0731";

function parseEnvFile(content) {
  const vars = {};
  for (const rawLine of String(content).split("\n")) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq === -1) continue;
    const key = line.slice(0, eq).trim();
    let value = line.slice(eq + 1).trim();
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }
    if (key) vars[key] = value;
  }
  return vars;
}

function serializeEnvFile(vars) {
  return Object.entries(vars)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n") + "\n";
}

function isWritableMount(dir) {
  if (!dir.startsWith("/")) return false;
  try {
    const mountinfo = readFileSync("/proc/self/mountinfo", "utf-8");
    return mountinfo.split("\n").some((line) => line.includes(dir));
  } catch {
    return false;
  }
}

let aiRemembered = !aiRememberDisabled && isWritableMount(path.dirname(aiEnvFile));
let aiKey = process.env.AI_API_KEY || "";

let currentRuntime = null;

// Per-repo CopilotKit runtimes. The chat panel is always the active repo's
// default agent (.devtop/agents/<slug>.mdx, `agent_runtime.default` in
// config.yml): each runtime is built lazily from that agent's descriptor
// (system prompt + model override) on the first chat request for the repo,
// then cached. applyAiConfig clears the cache when the key/provider changes.
const repoRuntimes = new Map();

// agentForRepo fetches the active repo's default agent descriptor from the Go
// server (/api/agent). A 409 (agent not deployed) or any error returns null:
// the chat must not run with built-in defaults.
async function agentForRepo(name) {
  try {
    const r = await fetch(`${goURL}/api/agent`, {
      headers: { "X-Devtop-Repo": name || "" },
    });
    if (!r.ok) return null;
    return await r.json();
  } catch {
    return null;
  }
}

async function buildRuntimeFor(agent, name) {
  // Chat history persists into the active repo's own .devtop/threads — the
  // same directory the Go thread list reads — so a conversation belongs to
  // the repo it was started in and survives a restart there. repoPaths
  // resolves per-repo from the Go server; in classic single-repo mode it
  // returns the workspace's own paths (== THREADS_DIR).
  let threadsDir = THREADS_DIR;
  const paths = await repoPaths(name);
  if (paths?.threads) threadsDir = paths.threads;

  const agentToolNames = new Set(Array.isArray(agent.tools) ? agent.tools : []);
  const chatTools = agentToolNames.size === 0 ? tools : tools.filter((t) => agentToolNames.has(t.name));

  const provider = createOpenAI({
    apiKey: aiKey,
    baseURL: baseURL,
    compatibility: "compatible",
  });
  const runtime = new CopilotRuntime({
    agents: {
      default: new BuiltInAgent({
        model: provider.chat(agent.model || model),
        tools: chatTools,
        maxSteps: 10,
        prompt: agent.prompt || undefined,
      }),
    },
    runner: new PersistentAgentRunner({ threadsDir }),
  });
  return runtime;
}

async function agentMiddlewareForRepo(name) {
  if (repoRuntimes.has(name)) return repoRuntimes.get(name);
  const agent = await agentForRepo(name);
  if (!agent) return null;
  const runtime = await buildRuntimeFor(agent, name);
  const mw = createCopilotExpressHandler({
    runtime,
    basePath: "/api/copilotkit",
    mode: "single-route",
  });
  repoRuntimes.set(name, mw);
  return mw;
}

function isConfigured() {
  return !!(aiKey && aiKey !== "not-needed");
}

// (Re)build the runtime for the current key + provider config. CopilotKit's
// runtime and express handler are immutable once created, so a config change
// tears the old one down and swaps in a fresh handler.
async function applyAiConfig(config) {
  const key = String(config?.key ?? aiKey).trim();
  aiKey = key;
  if (config?.baseURL) baseURL = config.baseURL;
  if (config?.model) model = config.model;
  process.env.OPENAI_API_KEY = aiKey;
  process.env.OPENAI_BASE_URL = baseURL;

  await persistAiEnv();

  try {
    if (currentRuntime && typeof currentRuntime.disconnect === "function") {
      await currentRuntime.disconnect();
    }
  } catch {}

  currentRuntime = null;
  // The key/baseURL/model changed: per-repo runtimes are rebuilt lazily on
  // the next chat request.
  repoRuntimes.clear();
}

// Persist the current key + provider config into the volume's .env file
// (0600), preserving any unrelated entries. Removing the key deletes only
// AI_API_KEY; base URL and model are kept for the next key.
async function persistAiEnv() {
  if (!aiRemembered) return;
  try {
    await fs.mkdir(path.dirname(aiEnvFile), { recursive: true });
    let vars = {};
    try {
      vars = parseEnvFile(await fs.readFile(aiEnvFile, "utf-8"));
    } catch {}
    if (isConfigured()) {
      vars.AI_API_KEY = aiKey;
    } else {
      delete vars.AI_API_KEY;
    }
    if (baseURL) vars.AI_BASE_URL = baseURL;
    if (model) vars.AI_MODEL = model;
    await fs.writeFile(aiEnvFile, serializeEnvFile(vars), { mode: 0o600 });
  } catch {
    // Volume not writable after all — fall back to session-only.
    aiRemembered = false;
  }
}

app.get("/api/copilotkit/ai-status", (req, res) => {
  res.json({ configured: isConfigured(), remembered: aiRemembered, baseURL, model });
});

app.post("/api/copilotkit/ai-key", express.json(), async (req, res) => {
  const key = String(req.body?.key || "").trim();
  if (!key) {
    return res.status(400).json({ error: "key is required" });
  }
  await applyAiConfig({ key, baseURL: req.body?.baseURL, model: req.body?.model });
  res.json({ configured: isConfigured(), remembered: aiRemembered, baseURL, model });
});

app.delete("/api/copilotkit/ai-key", async (req, res) => {
  await applyAiConfig({ key: "" });
  res.json({ configured: false, remembered: aiRemembered, baseURL, model });
});

// UI tests run this server against a throwaway fixture; the git_commit tool
// would otherwise create real commits in the enclosing repo.
const gitDisabled = process.env.DEVTOP_GIT_DISABLED === "1";

// Propagate base URL for CopilotKit/Vercel AI SDK compatibility (the key is
// applied dynamically in applyAiConfig).
process.env.OPENAI_BASE_URL = baseURL;

const DEVTOP_DIR = process.env.DEVTOP_DIR || path.resolve("../.devtop");
const THREADS_DIR = path.join(DEVTOP_DIR, "threads");

console.log("Initializing CopilotKit Runtime with OpenRouter and Zod tools:");
console.log(`- Model: ${model}`);
console.log(`- Base URL: ${baseURL}`);
console.log(`- Data directory: ${DEVTOP_DIR}`);

// The runners create their directories lazily on first write. Nothing under
// DEVTOP_DIR is touched at boot: a fresh, zero-repo instance must not grow a
// workspace .devtop until a repo is added and initialized.

// The frontend sends the active repo on every request (X-Devtop-Repo, set by
// the React provider and forwarded by the Go proxy — or Vite in dev). Tools
// are scoped to that repo; all file access is delegated to the Go server's
// /api/internal/tool endpoint so there is exactly one enforcement point for
// path containment (plus a Landlock sandbox on Linux).
const goURL = process.env.DEVTOP_GO_URL || "http://127.0.0.1:8000";
let currentRepoName = "";
const repoCache = new Map();

// repoPaths resolves the active repo's filesystem layout from the Go server.
async function repoPaths(name) {
  if (!name || !name.trim()) return null;
  name = name.trim();
  if (!repoCache.has(name)) {
    try {
      const r = await fetch(`${goURL}/api/repos/${encodeURIComponent(name)}`);
      if (!r.ok) return null;
      repoCache.set(name, await r.json());
    } catch {
      return null;
    }
  }
  return repoCache.get(name);
}

// delegateTool runs a tool server-side, scoped to the active repo. The tool
// result string is passed straight back to the model.
async function delegateTool(toolName, args) {
  try {
    const r = await fetch(`${goURL}/api/internal/tool`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Devtop-Repo": currentRepoName || "",
      },
      body: JSON.stringify({ name: toolName, args: args || {} }),
    });
    if (!r.ok) return `Error: tool dispatch failed (${r.status})`;
    const data = await r.json();
    return data.result ?? "Error: empty tool result";
  } catch {
    return "Error: cannot reach the devtop server";
  }
}

// Capture the active repo per request so in-flight tool calls are scoped to
// the chat's repo. Single-user server: a request goroutine-style race between
// two concurrent chats in different repos is accepted, matching the Go side's
// toolCtx design.
const captureRepo = (req, res, next) => {
  const h = req.headers["x-devtop-repo"];
  currentRepoName = typeof h === "string" ? h.trim() : "";
  next();
};
app.use(captureRepo);

// Define CopilotKit Backend Tools / Actions using Zod validation schema.
// All file access is delegated to the Go server so containment lives in one
// place; only git_commit runs here (scoped to the repo's git root) to keep
// the DEVTOP_GIT_DISABLED e2e escape hatch local.
const tools = [
  defineTool({
    name: "read_doc",
    description: "Read a documentation file from the docs/ directory.",
    parameters: z.object({
      path: z.string().describe("Path relative to docs/ (e.g. 'architecture.mdx')")
    }),
    execute: async (args) => delegateTool("read_doc", args)
  }),
  defineTool({
    name: "write_doc",
    description: "Write or overwrite a documentation file in the docs/ directory. Use .mdx extension.",
    parameters: z.object({
      path: z.string().describe("Path relative to docs/ (e.g. 'architecture.mdx')"),
      content: z.string().describe("Full MDX content (YAML frontmatter + markdown body)")
    }),
    execute: async (args) => delegateTool("write_doc", args)
  }),
  defineTool({
    name: "read_doc_at",
    description: "Read a documentation file as it existed at a given git commit. Commit may be a full or short sha, or HEAD. Returns the doc at that revision, or notes the file was deleted there.",
    parameters: z.object({
      path: z.string().describe("Path relative to docs/"),
      commit: z.string().describe("Git commit sha or HEAD")
    }),
    execute: async (args) => delegateTool("read_doc_at", args)
  }),
  defineTool({
    name: "list_doc_revisions",
    description: "List the git history of a documentation file: each commit that changed it, newest first, with sha, message, author, and date. The most recent is marked current.",
    parameters: z.object({
      path: z.string().describe("Path relative to docs/")
    }),
    execute: async (args) => delegateTool("list_doc_revisions", args)
  }),
  defineTool({
    name: "list_docs",
    description: "List all available documentation files with their slugs/titles.",
    parameters: z.object({}),
    execute: async () => delegateTool("list_docs", {})
  }),
  defineTool({
    name: "read_artifact",
    description: "Read an artifact of any config-declared kind (e.g. prds, docs, tickets) from the repo's .devtop/ directory.",
    parameters: z.object({
      kind: z.string().describe("Artifact kind, declared in config.yml (e.g. 'prds')"),
      id: z.string().describe("Artifact id, e.g. 'architecture/migrations'")
    }),
    execute: async (args) => delegateTool("read_artifact", args)
  }),
  defineTool({
    name: "write_artifact",
    description: "Write or overwrite an artifact of a config-declared, agent-writable kind (e.g. prds). Content includes YAML frontmatter and a markdown body.",
    parameters: z.object({
      kind: z.string().describe("Artifact kind, declared in config.yml (e.g. 'prds')"),
      id: z.string().describe("Artifact id, e.g. 'architecture/migrations'"),
      content: z.string().describe("Full file content (YAML frontmatter + markdown body)")
    }),
    execute: async (args) => delegateTool("write_artifact", args)
  }),
  defineTool({
    name: "list_tickets",
    description: "List all tickets with their status, priority, and assignee.",
    parameters: z.object({}),
    execute: async () => delegateTool("list_tickets", {})
  }),
  defineTool({
    name: "read_ticket",
    description: "Read a single ticket's full content including comments.",
    parameters: z.object({
      id: z.string().describe("Ticket ID (e.g. '001')")
    }),
    execute: async (args) => delegateTool("read_ticket", args)
  }),
  defineTool({
    name: "create_ticket",
    description: "Create a new ticket.",
    parameters: z.object({
      title: z.string().describe("Ticket title"),
      description: z.string().describe("Markdown description"),
      priority: z.enum(["urgent", "high", "medium", "low"]).describe("Priority level"),
      assignee: z.string().optional().describe("Assignee username (optional)")
    }),
    execute: async (args) => delegateTool("create_ticket", {
      title: args.title || "",
      description: args.description || "",
      priority: args.priority || "medium",
      assignee: args.assignee || ""
    })
  }),
  defineTool({
    name: "update_ticket",
    description: "Update a ticket's status, priority, or assignee.",
    parameters: z.object({
      id: z.string().describe("Ticket ID (e.g. '001')"),
      status: z.enum(["open", "in-progress", "done", "wontfix"]).optional().describe("New status"),
      priority: z.enum(["urgent", "high", "medium", "low"]).optional().describe("New priority"),
      assignee: z.string().optional().describe("New assignee")
    }),
    execute: async (args) => delegateTool("update_ticket", args)
  }),
  defineTool({
    name: "add_comment",
    description: "Add a comment to a ticket.",
    parameters: z.object({
      id: z.string().describe("Ticket ID (e.g. '001')"),
      body: z.string().describe("Comment text")
    }),
    execute: async (args) => delegateTool("add_comment", args)
  }),
  defineTool({
    name: "read_workspace_file",
    description: "Read a text file from the workspace repository. Path is relative to the workspace root (e.g. 'README.md', 'src/main.go'). Use list_workspace_files to discover paths.",
    parameters: z.object({
      path: z.string().describe("Path relative to the workspace root")
    }),
    execute: async (args) => delegateTool("read_workspace_file", args)
  }),
  defineTool({
    name: "list_workspace_files",
    description: "List files and directories in the workspace repository. Path is relative to the workspace root; omit or pass '' for the root. Skips .git, node_modules, and other generated directories.",
    parameters: z.object({
      path: z.string().optional().describe("Directory path relative to the workspace root")
    }),
    execute: async (args) => delegateTool("list_workspace_files", args)
  }),
  defineTool({
    name: "git_commit",
    description: "Commit all changes made so far to git. Call this after every write/create/update operation.",
    parameters: z.object({
      message: z.string().describe("Commit message describing the changes")
    }),
    execute: async ({ message }) => {
      if (gitDisabled) {
        return "Git commits are disabled in this environment (DEVTOP_GIT_DISABLED=1).";
      }
      try {
        const p = await repoPaths(currentRepoName);
        const cwd = (p && (p.git_root || p.root)) || path.dirname(DEVTOP_DIR);
        const relFromRoot = path.relative(cwd, (p && p.devtop) || DEVTOP_DIR);
        const relForAdd = relFromRoot === "" ? "." : relFromRoot;
        await execAsync(`git add "${relForAdd.replace(/"/g, '\\"')}"`, { cwd });
        const { stdout } = await execAsync(`git commit -m "${message}" --allow-empty`, { cwd });
        return "Committed: " + stdout.split("\n")[0];
      } catch (err) {
        if (err.message.includes("nothing to commit")) {
          return "Nothing to commit — no changes detected.";
        }
        return `Git commit error: ${err.message}`;
      }
    }
  })
];
// isInfoProbe detects CopilotKit's runtime discovery: GET /info
// (multi-runtime transport) or a POST envelope { method: "info" }
// (single-route transport). It consumes the request body and MUST only be
// called in branches that never forward the request to the CopilotKit handler.
async function isInfoProbe(req) {
  if (req.method === "GET") return req.path.endsWith("/info");
  if (req.method !== "POST") return false;
  try {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const parsed = JSON.parse(raw);
    return parsed?.method === "info";
  } catch {
    return false;
  }
}

// emptyRuntimeInfo is what CopilotKit's client needs from /info when the
// active repo has no runtime: the connection resolves with zero remote agents
// instead of failing. It must at least carry `agents` (the client calls
// Object.entries on it) and `mode`; the rest of the real descriptor is
// optional and nullish-coalesced by the client.
function emptyRuntimeInfo() {
  return { version: "1.0.0", agents: {}, mode: "sse" };
}

// Runtimes are created per repo from the agent descriptor (see
// agentMiddlewareForRepo). Requests are routed by the active repo captured
// above; a key-less instance keeps the 502, and a repo without its default
// agent deployed gets a 409 instead of a built-in fallback. Runtime info
// probes are the exception: they answer 200 with an empty descriptor so the
// client resolves the connection instead of retrying forever.
app.use(async (req, res, next) => {
  if (!isConfigured()) {
    if (await isInfoProbe(req)) return res.status(200).json(emptyRuntimeInfo());
    // Plain-text 502 (not a JSON body): the CopilotKit client treats an
    // unreachable runtime like the Go proxy's "runtime down" case and shows a
    // connection error instead of crashing on a parsed-but-empty response.
    return res.status(502).send("AI not configured");
  }
  let mw;
  try {
    mw = await agentMiddlewareForRepo(currentRepoName);
  } catch {
    if (await isInfoProbe(req)) return res.status(200).json(emptyRuntimeInfo());
    return res.status(502).send("AI runtime unavailable");
  }
  if (!mw) {
    if (await isInfoProbe(req)) return res.status(200).json(emptyRuntimeInfo());
    return res
      .status(409)
      .send("No agent configured: initialize the repo to scaffold .devtop/agents");
  }
  return mw(req, res, next);
});

// Load the persisted .env config from the volume (if any) and build the
// initial runtime. This overrides env vars / hardcoded defaults so a saved
// UI config wins across restarts.
if (aiRemembered) {
  try {
    const vars = parseEnvFile(await fs.readFile(aiEnvFile, "utf-8"));
    if (vars.AI_API_KEY) aiKey = vars.AI_API_KEY;
    if (vars.AI_BASE_URL) baseURL = vars.AI_BASE_URL;
    if (vars.AI_MODEL) model = vars.AI_MODEL;
  } catch {}
}
await applyAiConfig({});
if (isConfigured()) {
  console.log(`CopilotKit runtime online (model ${model})`);
} else {
  console.log("CopilotKit runtime waiting for an AI key (enter one from the chat panel)");
}

const port = process.env.PORT || 4000;
app.listen(port, "0.0.0.0", () => {
  console.log(`CopilotKit runtime listening at http://0.0.0.0:${port}/api/copilotkit`);
});
