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
// AI key management.
//
// The key is entered through the UI only — never on the command line. It is
// held in this process's memory. When a writable key volume is mounted (a
// named volume at /etc/devtop, detected via /proc/self/mountinfo) and
// DEVTOP_AI_REMEMBER is not "0", it is also persisted as a 0600 file so it
// survives restarts. Without a volume it stays session-only and is never
// written to disk. The Go server proxies /api/copilotkit/* to this runtime in
// prod; in dev the Vite proxy does the same.
// ---------------------------------------------------------------------------
const aiKeyFile = process.env.DEVTOP_AI_KEY_FILE || "/etc/devtop/ai-key";
const aiProviderFile = process.env.DEVTOP_AI_PROVIDER_FILE || "/etc/devtop/ai-provider.json";
const aiRememberDisabled = process.env.DEVTOP_AI_REMEMBER === "0";

let baseURL = process.env.AI_BASE_URL || "https://openrouter.ai/api/v1";
let model = process.env.AI_MODEL || "openai/gpt-4o-mini";

function isWritableMount(dir) {
  if (!dir.startsWith("/")) return false;
  try {
    const mountinfo = readFileSync("/proc/self/mountinfo", "utf-8");
    return mountinfo.split("\n").some((line) => line.includes(dir));
  } catch {
    return false;
  }
}

let aiRemembered = !aiRememberDisabled && isWritableMount(path.dirname(aiKeyFile));
let aiKey = process.env.AI_API_KEY || "";

let currentRuntime = null;
let currentMw = null;

async function buildRuntime() {
  const provider = createOpenAI({
    apiKey: aiKey,
    baseURL: baseURL,
    compatibility: "compatible",
  });
  const runtime = new CopilotRuntime({
    agents: {
      default: new BuiltInAgent({
        model: provider.chat(model),
        tools: tools,
        maxSteps: 10,
      }),
    },
    runner: new PersistentAgentRunner({ threadsDir: THREADS_DIR }),
  });
  return runtime;
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

  if (aiRemembered) {
    try {
      await fs.mkdir(path.dirname(aiKeyFile), { recursive: true });
      if (isConfigured()) {
        await fs.writeFile(aiKeyFile, aiKey, { mode: 0o600 });
        await fs.writeFile(aiProviderFile, JSON.stringify({ baseURL, model }, null, 2), { mode: 0o600 });
      } else {
        await fs.rm(aiKeyFile, { force: true });
        await fs.rm(aiProviderFile, { force: true });
      }
    } catch {
      // Volume not writable after all — fall back to session-only.
      aiRemembered = false;
    }
  }

  try {
    if (currentRuntime && typeof currentRuntime.disconnect === "function") {
      await currentRuntime.disconnect();
    }
  } catch {}

  if (isConfigured()) {
    currentRuntime = await buildRuntime();
    currentMw = createCopilotExpressHandler({
      runtime: currentRuntime,
      basePath: "/api/copilotkit",
      mode: "single-route",
    });
  } else {
    currentRuntime = null;
    currentMw = null;
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
const DOCS_DIR = path.join(DEVTOP_DIR, "docs");
const TICKETS_DIR = path.join(DEVTOP_DIR, "tickets");
const THREADS_DIR = path.join(DEVTOP_DIR, "threads");
const WORKSPACE_ROOT = path.dirname(DEVTOP_DIR);

const MAX_WORKSPACE_READ_BYTES = 512 * 1024;
const IGNORED_WORKSPACE_DIRS = new Set([".git", ".devtop", "node_modules", "dist", "build", "target", "vendor", ".idea", ".vscode"]);

function isPathInside(root, target) {
  const rel = path.relative(root, target);
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

async function resolveWorkspacePath(relPath) {
  const full = path.resolve(WORKSPACE_ROOT, relPath);
  if (!isPathInside(WORKSPACE_ROOT, full)) {
    throw new Error("path escapes the workspace");
  }
  const real = await fs.realpath(full).catch(() => full);
  if (!isPathInside(WORKSPACE_ROOT, real)) {
    throw new Error("path escapes the workspace (symlink)");
  }
  return real;
}

console.log("Initializing CopilotKit Runtime with OpenRouter and Zod tools:");
console.log(`- Model: ${model}`);
console.log(`- Base URL: ${baseURL}`);
console.log(`- Data directory: ${DEVTOP_DIR}`);

// Ensure directories exist
await fs.mkdir(DOCS_DIR, { recursive: true });
await fs.mkdir(TICKETS_DIR, { recursive: true });
await fs.mkdir(THREADS_DIR, { recursive: true });

// Helper to run git commit
async function gitCommit(message) {
  try {
    const relPath = path.relative(path.dirname(DEVTOP_DIR), DEVTOP_DIR);
    await execAsync(`git add ${relPath}`, { cwd: path.dirname(DEVTOP_DIR) });
    const { stdout } = await execAsync(`git commit -m "${message}" --allow-empty`, { cwd: path.dirname(DEVTOP_DIR) });
    return "Committed: " + stdout.split("\n")[0];
  } catch (err) {
    if (err.message.includes("nothing to commit")) {
      return "Nothing to commit — no changes detected.";
    }
    return `Git commit error: ${err.message}`;
  }
}

// Helper to find next ticket ID
async function getNextTicketID() {
  try {
    const files = await fs.readdir(TICKETS_DIR);
    const mdFiles = files.filter(f => f.endsWith(".md"));
    if (mdFiles.length === 0) return "001";
    let maxID = 0;
    for (const f of mdFiles) {
      const id = parseInt(f.replace(".md", ""), 10);
      if (!isNaN(id) && id > maxID) {
        maxID = id;
      }
    }
    return String(maxID + 1).padStart(3, "0");
  } catch {
    return "001";
  }
}

// Helper to parse standard frontmatter
function parseFrontmatter(fileContent) {
  const match = fileContent.match(/^---\s*\n([\s\S]*?)\n---\s*\n([\s\S]*)$/);
  if (!match) return { meta: {}, body: fileContent };
  
  const yaml = match[1];
  const body = match[2];
  const meta = {};
  
  yaml.split("\n").forEach(line => {
    const parts = line.split(":");
    if (parts.length >= 2) {
      const key = parts[0].trim();
      const val = parts.slice(1).join(":").trim().replace(/^"(.*)"$/, "$1");
      meta[key] = val;
    }
  });
  
  return { meta, body };
}

// Define CopilotKit Backend Tools / Actions using Zod validation schema
const tools = [
  defineTool({
    name: "read_doc",
    description: "Read a documentation file from the docs/ directory.",
    parameters: z.object({
      path: z.string().describe("Path relative to docs/ (e.g. 'architecture.mdx')")
    }),
    execute: async ({ path: docPath }) => {
      try {
        const fullPath = path.join(DOCS_DIR, docPath);
        const data = await fs.readFile(fullPath, "utf-8");
        return data;
      } catch (err) {
        return `Error: doc '${docPath}' not found`;
      }
    }
  }),
  defineTool({
    name: "write_doc",
    description: "Write or overwrite a documentation file in the docs/ directory. Use .mdx extension.",
    parameters: z.object({
      path: z.string().describe("Path relative to docs/ (e.g. 'architecture.mdx')"),
      content: z.string().describe("Full MDX content (YAML frontmatter + markdown body)")
    }),
    execute: async ({ path: docPath, content }) => {
      try {
        const fullPath = path.join(DOCS_DIR, docPath);
        await fs.mkdir(path.dirname(fullPath), { recursive: true });
        await fs.writeFile(fullPath, content, "utf-8");
        return `Written to docs/${docPath}`;
      } catch (err) {
        return `Error writing doc: ${err.message}`;
      }
    }
  }),
  defineTool({
    name: "list_docs",
    description: "List all available documentation files with their slugs/paths and titles.",
    parameters: z.object({}),
    execute: async () => {
      try {
        async function getFiles(dir) {
          const subdirs = await fs.readdir(dir, { withFileTypes: true });
          const files = await Promise.all(subdirs.map(async (subdir) => {
            const res = path.resolve(dir, subdir.name);
            return subdir.isDirectory() ? getFiles(res) : res;
          }));
          return files.flat();
        }
        const allFiles = await getFiles(DOCS_DIR);
        const mdxFiles = allFiles.filter(f => f.endsWith(".mdx") || f.endsWith(".md"));
        const list = [];
        for (const f of mdxFiles) {
          const content = await fs.readFile(f, "utf-8");
          const { meta } = parseFrontmatter(content);
          const relPath = path.relative(DOCS_DIR, f);
          list.push({
            slug: relPath.replace(/\.mdx?$/, ""),
            title: meta.title || relPath
          });
        }
        return JSON.stringify(list, null, 2);
      } catch (err) {
        return `Error listing docs: ${err.message}`;
      }
    }
  }),
  defineTool({
    name: "list_tickets",
    description: "List all tickets with their status, priority, and assignee.",
    parameters: z.object({}),
    execute: async () => {
      try {
        const files = await fs.readdir(TICKETS_DIR);
        const mdFiles = files.filter(f => f.endsWith(".md"));
        const list = [];
        for (const f of mdFiles) {
          const content = await fs.readFile(path.join(TICKETS_DIR, f), "utf-8");
          const { meta } = parseFrontmatter(content);
          list.push({
            id: meta.id || f.replace(".md", ""),
            title: meta.title || "Untitled",
            status: meta.status || "open",
            priority: meta.priority || "medium",
            assignee: meta.assignee || ""
          });
        }
        return JSON.stringify(list, null, 2);
      } catch (err) {
        return `Error listing tickets: ${err.message}`;
      }
    }
  }),
  defineTool({
    name: "read_ticket",
    description: "Read a single ticket's full content including comments.",
    parameters: z.object({
      id: z.string().describe("Ticket ID (e.g. '001')")
    }),
    execute: async ({ id }) => {
      try {
        const fullPath = path.join(TICKETS_DIR, `${id}.md`);
        const data = await fs.readFile(fullPath, "utf-8");
        return data;
      } catch (err) {
        return `Error: ticket '${id}' not found`;
      }
    }
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
    execute: async ({ title, description, priority, assignee = "" }) => {
      try {
        const id = await getNextTicketID();
        const created = new Date().toISOString().split("T")[0];
        const fm = `---
id: "${id}"
title: "${title}"
status: "open"
priority: "${priority}"
assignee: "${assignee}"
created: "${created}"
---

${description}`;
        await fs.writeFile(path.join(TICKETS_DIR, `${id}.md`), fm, "utf-8");
        return `Created ticket ${id}: ${title}`;
      } catch (err) {
        return `Error creating ticket: ${err.message}`;
      }
    }
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
    execute: async ({ id, status, priority, assignee }) => {
      try {
        const fullPath = path.join(TICKETS_DIR, `${id}.md`);
        const fileContent = await fs.readFile(fullPath, "utf-8");
        const { meta, body } = parseFrontmatter(fileContent);
        
        if (status !== undefined) meta.status = status;
        if (priority !== undefined) meta.priority = priority;
        if (assignee !== undefined) meta.assignee = assignee;

        const fm = `---
id: "${meta.id || id}"
title: "${meta.title || 'Untitled'}"
status: "${meta.status || 'open'}"
priority: "${meta.priority || 'medium'}"
assignee: "${meta.assignee || ''}"
created: "${meta.created || ''}"
---

${body}`;
        await fs.writeFile(fullPath, fm, "utf-8");
        return `Updated ticket ${id}`;
      } catch (err) {
        return `Error updating ticket: ${err.message}`;
      }
    }
  }),
  defineTool({
    name: "add_comment",
    description: "Add a comment to a ticket.",
    parameters: z.object({
      id: z.string().describe("Ticket ID (e.g. '001')"),
      body: z.string().describe("Comment text")
    }),
    execute: async ({ id, body }) => {
      try {
        const fullPath = path.join(TICKETS_DIR, `${id}.md`);
        const fileContent = await fs.readFile(fullPath, "utf-8");
        const { meta, body: ticketBody } = parseFrontmatter(fileContent);
        
        const nowStr = new Date().toISOString().replace("T", " ").substring(0, 16);
        const commentLine = `\n**${nowStr}** — ${body}\n`;
        const updatedBody = ticketBody.trim() + commentLine;

        const fm = `---
id: "${meta.id || id}"
title: "${meta.title || 'Untitled'}"
status: "${meta.status || 'open'}"
priority: "${meta.priority || 'medium'}"
assignee: "${meta.assignee || ''}"
created: "${meta.created || ''}"
---

${updatedBody}`;
        await fs.writeFile(fullPath, fm, "utf-8");
        return `Comment added to ticket ${id}`;
      } catch (err) {
        return `Error adding comment: ${err.message}`;
      }
    }
  }),
  defineTool({
    name: "read_workspace_file",
    description: "Read a text file from the workspace repository. Path is relative to the workspace root (e.g. 'README.md', 'src/main.go'). Use list_workspace_files to discover paths.",
    parameters: z.object({
      path: z.string().describe("Path relative to the workspace root")
    }),
    execute: async ({ path: relPath }) => {
      try {
        const full = await resolveWorkspacePath(relPath);
        const st = await fs.stat(full);
        if (st.isDirectory()) {
          return `Error: '${relPath}' is a directory — use list_workspace_files`;
        }
        if (st.size > MAX_WORKSPACE_READ_BYTES) {
          return `Error: file too large (${st.size} bytes, max ${MAX_WORKSPACE_READ_BYTES})`;
        }
        const buf = await fs.readFile(full);
        if (buf.includes(0)) {
          return `Binary file (${buf.length} bytes) — content not shown`;
        }
        return buf.toString("utf-8");
      } catch (err) {
        return `Error: ${err.message}`;
      }
    }
  }),
  defineTool({
    name: "list_workspace_files",
    description: "List files and directories in the workspace repository. Path is relative to the workspace root; omit or pass '' for the root. Skips .git, node_modules, and other generated directories.",
    parameters: z.object({
      path: z.string().optional().describe("Directory path relative to the workspace root")
    }),
    execute: async ({ path: relPath }) => {
      try {
        const dir = relPath ? await resolveWorkspacePath(relPath) : WORKSPACE_ROOT;
        const st = await fs.stat(dir);
        if (!st.isDirectory()) {
          return `Error: '${relPath || "."}' is not a directory`;
        }
        const entries = await fs.readdir(dir, { withFileTypes: true });
        const out = [];
        for (const e of entries) {
          if (IGNORED_WORKSPACE_DIRS.has(e.name)) continue;
          if (e.isSymbolicLink()) { out.push({ name: e.name, type: "symlink" }); continue; }
          if (e.isDirectory()) { out.push({ name: e.name, type: "dir" }); continue; }
          if (e.isFile()) {
            const info = await fs.stat(path.join(dir, e.name));
            out.push({ name: e.name, type: "file", size: info.size });
          }
        }
        out.sort((a, b) => a.type === b.type ? a.name.localeCompare(b.name) : a.type.localeCompare(b.type));
        return JSON.stringify(out, null, 2);
      } catch (err) {
        return `Error: ${err.message}`;
      }
    }
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
      return await gitCommit(message);
    }
  })
];

// The runtime is created dynamically from the current key (see applyAiKey).
// Requests reach it through a swap-in middleware so a key change can replace
// the handler without restarting the process.
app.use((req, res, next) => {
  if (currentMw) return currentMw(req, res, next);
  // Plain-text 502 (not a JSON body): the CopilotKit client treats an
  // unreachable runtime like the Go proxy's "runtime down" case and shows a
  // connection error instead of crashing on a parsed-but-empty response.
  return res.status(502).send("AI not configured");
});

// Load persisted key + provider config from the volume (if any) and build the
// initial runtime.
if (aiRemembered) {
  try {
    const stored = await fs.readFile(aiKeyFile, "utf-8");
    if (stored && stored.trim()) aiKey = stored.trim();
  } catch {}
  try {
    const storedProvider = await fs.readFile(aiProviderFile, "utf-8");
    if (storedProvider && storedProvider.trim()) {
      const cfg = JSON.parse(storedProvider);
      if (cfg.baseURL) baseURL = cfg.baseURL;
      if (cfg.model) model = cfg.model;
    }
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
