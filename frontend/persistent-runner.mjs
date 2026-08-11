import fs from "fs/promises";
import fsSync from "fs";
import path from "path";
import { ReplaySubject } from "rxjs";
import { InMemoryAgentRunner } from "@copilotkit/runtime/v2";

const ROLE_MAP = { user: "human", assistant: "agent", system: "system", tool: "tool" };

function toGoMessage(message) {
  const content = message?.content;
  return {
    id: message?.id,
    role: ROLE_MAP[message?.role] ?? message?.role ?? "agent",
    content: typeof content === "string" ? content : content == null ? "" : JSON.stringify(content),
    timestamp: message?.createdAt ?? new Date().toISOString(),
  };
}

function mergeMessages(existing, incoming) {
  if (!existing || existing.length === 0) return incoming ?? [];
  if (!incoming || incoming.length === 0) return existing;
  const seen = new Set();
  const out = [];
  for (const message of [...incoming, ...existing]) {
    if (!message || typeof message.id !== "string") {
      out.push(message);
      continue;
    }
    if (seen.has(message.id)) continue;
    seen.add(message.id);
    out.push(message);
  }
  return out;
}

/**
 * Thread runner that keeps CopilotKit's in-memory behavior (streaming runs,
 * connect replay) but persists every completed run to the same threads
 * directory the Go backend reads. This makes conversations survive a server
 * restart and lets the Go thread list report real message counts.
 */
export class PersistentAgentRunner extends InMemoryAgentRunner {
  constructor(options = {}) {
    super(options);
    this.threadsDir = options.threadsDir;
    this.context = options.context ?? "";
    this.threadCache = new Map();
  }

  run(request) {
    const observable = super.run(request);
    const threadId = request.threadId;
    observable.subscribe({
      complete: () => this.persistThread(threadId),
      error: () => this.persistThread(threadId),
    });
    return observable;
  }

  connect(request) {
    const threadId = request.threadId;
    if (super.getThreadMessages(threadId).length > 0) {
      return super.connect(request);
    }
    const persisted = this.loadThread(threadId);
    if (!persisted || persisted.events.length === 0) {
      return super.connect(request);
    }
    const subject = new ReplaySubject(Infinity);
    for (const event of persisted.events) {
      subject.next(event);
    }
    subject.complete();
    return subject.asObservable();
  }

  getThreadMessages(threadId) {
    const inMemory = super.getThreadMessages(threadId);
    if (inMemory.length > 0) return inMemory;
    const persisted = this.loadThread(threadId);
    return persisted?.messages ?? [];
  }

  listThreads() {
    const threads = super.listThreads();
    const seen = new Set(threads.map((t) => t.id));
    const fromDisk = [];
    try {
      const entries = fsSync.readdirSync(this.threadsDir, { withFileTypes: true });
      for (const entry of entries) {
        if (!entry.isFile() || !entry.name.endsWith(".json")) continue;
        const id = entry.name.replace(/\.json$/, "");
        if (seen.has(id)) continue;
        const data = this.readThreadFileSync(path.join(this.threadsDir, entry.name));
        if (!data) continue;
        fromDisk.push({
          id,
          name: data.title ?? null,
          agentId: "default",
          organizationId: "",
          createdById: "",
          archived: false,
          createdAt: data.created_at ?? data.updated_at ?? new Date(0).toISOString(),
          updatedAt: data.updated_at ?? new Date(0).toISOString(),
        });
      }
    } catch {
      // threads dir may not exist yet
    }
    return [...threads, ...fromDisk].sort(
      (a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime()
    );
  }

  clearThreads() {
    super.clearThreads();
    this.threadCache.clear();
    try {
      const entries = fsSync.readdirSync(this.threadsDir, { withFileTypes: true });
      for (const entry of entries) {
        if (entry.isFile() && entry.name.endsWith(".json")) {
          fs.unlink(path.join(this.threadsDir, entry.name)).catch(() => {});
        }
      }
    } catch {
      // threads dir may not exist yet
    }
  }

  async persistThread(threadId) {
    try {
      const messages = super.getThreadMessages(threadId);
      if (!messages || messages.length === 0) return;
      const events = super.getThreadEvents(threadId);
      const filePath = path.join(this.threadsDir, `${threadId}.json`);
      const existing = await this.readThreadFile(filePath);
      const now = new Date().toISOString();
      const merged = mergeMessages(existing?.__messages, messages);
      const payload = {
        id: existing?.id ?? threadId,
        context: existing?.context ?? this.context ?? "global",
        title: existing?.title ?? "New conversation",
        created_at: existing?.created_at ?? now,
        updated_at: now,
        messages: merged.map(toGoMessage),
        __events: events,
        __messages: merged,
      };
      await fs.mkdir(this.threadsDir, { recursive: true });
      await fs.writeFile(filePath, JSON.stringify(payload, null, 2), "utf-8");
      this.threadCache.set(threadId, { events, messages: merged });
    } catch (err) {
      console.error(`[PersistentAgentRunner] failed to persist thread ${threadId}:`, err);
    }
  }

  loadThread(threadId) {
    if (this.threadCache.has(threadId)) return this.threadCache.get(threadId);
    const filePath = path.join(this.threadsDir, `${threadId}.json`);
    const data = this.readThreadFileSync(filePath);
    if (!data) return null;
    const result = {
      events: Array.isArray(data.__events) ? data.__events : [],
      messages: Array.isArray(data.__messages) ? data.__messages : [],
    };
    this.threadCache.set(threadId, result);
    return result;
  }

  readThreadFileSync(filePath) {
    try {
      return JSON.parse(fsSync.readFileSync(filePath, "utf-8"));
    } catch {
      return null;
    }
  }

  async readThreadFile(filePath) {
    try {
      return JSON.parse(await fs.readFile(filePath, "utf-8"));
    } catch {
      return null;
    }
  }
}
