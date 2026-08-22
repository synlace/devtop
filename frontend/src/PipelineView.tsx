import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, PointerEvent as ReactPointerEvent } from 'react'
import { Maximize2, Minimize2, X } from 'lucide-react'
import { api } from './api'

// The work-item view, aligned with mocks/workflow-handsoff-v2: two panes —
// work items on the left, a detail pane on the right with tabs (Artifacts ·
// Clarifications · Run Log), a draggable splitter with a 45/55 default, a
// fullscreen toggle, and the run log fed by the engine's SSE /api/events
// stream. All data operations (derive, review, edit, publish, capture) hit
// the same endpoints as before.

interface PipelineArtifact {
  id: string
  kind: string
  title: string
  review: string
  stale: boolean
  edited: boolean
}
interface PipelineItem {
  id: string
  title: string
  summary?: string
  seed: string
  review: string
  stages: Record<string, PipelineArtifact[]>
  ready: boolean
  stale: boolean
  uncovered: number
  published: boolean
  needs_tickets: boolean
}
interface PipelineEdge {
  from: string
  to: string
  chain?: string
}
interface PipelineResponse {
  edges: PipelineEdge[]
  roots: string[]
  items: PipelineItem[]
}
interface RunEvent {
  seq: number
  ts: string
  type: string
  scope?: string
  payload?: Record<string, unknown>
}
type TabKey = 'artifacts' | 'clarifications' | 'log'

const KIND_CODE: Record<string, string> = {
  intents: 'INT', bugs: 'BUG', spikes: 'SPK', rfcs: 'RFC', chores: 'CHR',
  documentation: 'DOC', requirements: 'REQ', decisions: 'DEC', open_questions: 'OQ', tickets: 'DEV',
}
const KIND_NAME: Record<string, string> = {
  intents: 'Feature intent', bugs: 'Bug', spikes: 'Spike', rfcs: 'RFC', chores: 'Chore',
  documentation: 'Documentation', requirements: 'Requirement', decisions: 'Decision', open_questions: 'Open question', tickets: 'Ticket',
}
const KIND_CHIP: Record<string, string> = {
  INT: 'text-amber-400 bg-amber-500/10 border-amber-500/30',
  BUG: 'text-rose-400 bg-rose-500/10 border-rose-500/30',
  SPK: 'text-violet-400 bg-violet-500/10 border-violet-500/30',
  RFC: 'text-sky-400 bg-sky-500/10 border-sky-500/30',
  CHR: 'text-lime-400 bg-lime-500/10 border-lime-500/30',
  DOC: 'text-blue-400 bg-blue-500/10 border-blue-500/30',
  REQ: 'text-purple-400 bg-purple-500/10 border-purple-500/30',
  DEC: 'text-teal-400 bg-teal-500/10 border-teal-500/30',
  OQ: 'text-sky-400 bg-sky-500/10 border-sky-500/30',
  DEV: 'text-slate-400 bg-slate-500/10 border-slate-500/30',
}
const REVIEW_STYLE: Record<string, string> = {
  pending: 'bg-amber-500/10 border-amber-500/25 text-amber-400',
  approved: 'bg-emerald-500/10 border-emerald-500/25 text-emerald-400',
  rejected: 'bg-rose-500/10 border-rose-500/25 text-rose-400',
}
const ACTION_BTN = 'px-3 py-1.5 rounded-lg text-xs font-medium transition-colors disabled:opacity-60 disabled:cursor-not-allowed'
const BTN_ROW = 'w-6 h-6 grid place-items-center rounded-md border transition-colors disabled:opacity-40 disabled:cursor-not-allowed'

function chainColumns(edges: PipelineEdge[], seed: string): string[][] {
  const cols: string[][] = [[seed]]
  let frontier = [seed]
  const seen = new Set([seed])
  for (;;) {
    const next: string[] = []
    const added = new Set<string>()
    for (const f of frontier) {
      for (const e of edges) {
        if (e.from !== f || (e.chain && e.chain !== seed) || seen.has(e.to) || added.has(e.to)) continue
        next.push(e.to)
        added.add(e.to)
      }
    }
    if (next.length === 0) break
    cols.push(next)
    frontier = next
    for (const k of next) seen.add(k)
  }
  return cols
}

function codeOf(kind: string) {
  return KIND_CODE[kind] ?? kind.slice(0, 3).toUpperCase()
}
function kindChipCls(kind: string) {
  return `inline-flex items-center font-mono text-[10px] font-semibold px-1.5 py-0.5 rounded border ${KIND_CHIP[codeOf(kind)] ?? KIND_CHIP.DEV}`
}
function countOf(item: PipelineItem, kind: string) {
  return item.stages[kind]?.length ?? 0
}

function stageOf(item: PipelineItem, edges: PipelineEdge[]) {
  const cols = chainColumns(edges, item.seed)
  for (let i = 1; i < cols.length; i++) {
    const col = cols[i]
    if (col.every(k => countOf(item, k) > 0)) continue
    return { idx: i, col }
  }
  return null
}
function flattenItem(item: PipelineItem, edges: PipelineEdge[]): PipelineArtifact[] {
  const out: PipelineArtifact[] = []
  for (const kind of chainColumns(edges, item.seed).flat()) {
    if (kind === item.seed) {
      out.push({ id: item.id, kind, title: item.title, review: item.review, stale: item.stale, edited: false })
      continue
    }
    for (const a of item.stages[kind] ?? []) out.push({ ...a, kind })
  }
  return out
}


function ReviewPill({ value }: { value: string }) {
  const label = value === 'approved' ? 'Approved' : value === 'rejected' ? 'Rejected' : 'Needs review'
  return (
    <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-medium border whitespace-nowrap ${REVIEW_STYLE[value] ?? REVIEW_STYLE.pending}`}>
      <span className={`w-1.5 h-1.5 rounded-full ${value === 'approved' ? 'bg-emerald-400' : value === 'rejected' ? 'bg-rose-400' : 'bg-amber-400'}`}></span>
      {label}
    </span>
  )
}

function StatePill({ w, all, pending, deriving }: { w: PipelineItem; all: number; pending: number; deriving: boolean }) {
  if (deriving) {
    return (
      <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border border-accentBlue/30 bg-accentBlue/10 text-slate-200 flex-shrink-0">
        <svg className="w-2.5 h-2.5 animate-spin text-accentBlue" fill="none" viewBox="0 0 24 24"><circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" /><path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v4a4 4 0 00-4 4H4z" /></svg>
        <span className="text-accentBlue font-semibold">Deriving</span>
      </span>
    )
  }
  if (w.published) {
    return (
      <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border bg-emerald-500/10 border-emerald-500/30 text-emerald-400 flex-shrink-0">
        <span className="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>Published
      </span>
    )
  }
  if (all === 0) {
    return (
      <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border border-borderDark text-slate-500 flex-shrink-0">Just captured</span>
    )
  }
  if (pending > 0) {
    return (
      <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border border-amber-500/30 bg-amber-500/10 text-amber-400 flex-shrink-0">
        <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse"></span>{pending} of {all} pending
      </span>
    )
  }
  if (w.needs_tickets && countOf(w, 'tickets') === 0) {
    return (
      <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border border-amber-500/30 bg-amber-500/10 text-amber-400 flex-shrink-0">
        <span className="w-1.5 h-1.5 rounded-full bg-amber-400"></span>Needs tickets
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border bg-emerald-500/10 border-emerald-500/30 text-emerald-400 flex-shrink-0">
      <span className="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>Ready
    </span>
  )
}

const PANEL_KEY = 'devtop.pipeline.panels'
const readPanels = (): { detailW?: number; detailFull?: boolean; activeTab?: TabKey } => {
  try {
    const v = JSON.parse(window.localStorage.getItem(PANEL_KEY) ?? '{}') as Partial<{ detailW: number; detailFull: boolean; activeTab: string }>
    return {
      detailW: typeof v.detailW === 'number' ? v.detailW : undefined,
      detailFull: v.detailFull === true,
      activeTab: v.activeTab === 'artifacts' || v.activeTab === 'clarifications' || v.activeTab === 'log' ? v.activeTab : undefined,
    }
  } catch { return {} }
}

export default function PipelineView({ refreshKey }: { refreshKey?: number }) {
  const [data, setData] = useState<PipelineResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [deriveStatus, setDeriveStatus] = useState<{ label: string; detail?: string } | null>(null)
  const [statusElapsed, setStatusElapsed] = useState(0)
  const [actionError, setActionError] = useState<{ key: string; msg: string } | null>(null)
  const [openRows, setOpenRows] = useState<Set<string>>(() => new Set())
  const [filters, setFilters] = useState<Set<string>>(() => new Set())
  const [editingId, setEditingId] = useState<string | null>(null)
  const [newTitle, setNewTitle] = useState('')
  const [newSeedKind, setNewSeedKind] = useState('intents')
  const [newOpen, setNewOpen] = useState(false)
  const [clarifySens, setClarifySens] = useState(50)
  const [freshIds, setFreshIds] = useState<Set<string>>(() => new Set())
  const [bodyCache, setBodyCache] = useState<Record<string, string>>({})
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [activeTab, setActiveTab] = useState<TabKey>(() => readPanels().activeTab ?? 'artifacts')
  const [detailW, setDetailW] = useState(() => readPanels().detailW ?? 0)
  const [detailFull, setDetailFull] = useState(() => readPanels().detailFull ?? false)
  const [events, setEvents] = useState<RunEvent[]>([])
  const [chainRunning, setChainRunning] = useState<string | null>(null)
  const [deriving, setDeriving] = useState(false)
  const clarifyOf = useRef<Record<string, string>>({})
  const lastSeqRef = useRef(0)
  const workspaceRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{ startX: number; startW: number } | null>(null)
  const prevIds = useRef<Set<string>>(new Set())

  const loads = useCallback(async (clearAction = true) => {
    try {
      const r = await fetch(api('/api/pipeline'))
      if (!r.ok) throw new Error('pipeline ' + r.status)
      const json = (await r.json()) as PipelineResponse
      const nextIds = new Set<string>()
      const fresh: string[] = []
      for (const it of json.items) {
        if (prevIds.current.size > 0 && !prevIds.current.has(it.id)) fresh.push(it.id)
        nextIds.add(it.id)
        for (const kind of chainColumns(json.edges, it.seed).flat()) {
          for (const a of it.stages[kind] ?? []) {
            nextIds.add(a.id)
            if (prevIds.current.size > 0 && !prevIds.current.has(a.id)) fresh.push(a.id)
          }
        }
      }
      if (prevIds.current.size > 0 && fresh.length) {
        setFreshIds(new Set(fresh))
        window.setTimeout(() => setFreshIds(new Set()), 1600)
      }
      prevIds.current = nextIds
      setData(json)
      if (clearAction) setActionError(null)
      return json
    } catch (e) {
      setError(String(e))
      return null
    }
  }, [])
  useEffect(() => { void loads() }, [loads, refreshKey])

  useEffect(() => {
    let disposed = false
    const es = new EventSource(api(`/api/events?after=${lastSeqRef.current}`))
    es.onmessage = (m) => {
      if (disposed) return
      try {
        const ev = JSON.parse(m.data) as RunEvent
        if (ev && typeof ev.seq === 'number') {
          lastSeqRef.current = ev.seq
          setEvents(prev => [...prev.slice(-499), ev])
        }
      } catch { /* partial frame */ }
    }
    return () => { disposed = true; es.close() }
  }, [])

  // Panel layout persists across reloads: splitter width, fullscreen, tab.
  useEffect(() => {
    if (detailW <= 0) return
    window.localStorage.setItem(PANEL_KEY, JSON.stringify({ detailW, detailFull, activeTab }))
  }, [detailW, detailFull, activeTab])

  // Default detail width: 45% of the workspace, clamped like the mock.
  useEffect(() => {
    if (detailW > 0 || detailFull) return
    const root = workspaceRef.current
    if (!root) return
    const w = Math.min(Math.max(Math.round(root.clientWidth * 0.45), 320), root.clientWidth - 420)
    if (w > 0) setDetailW(w)
  }, [data, detailW, detailFull])

  // Keep the detail selection valid as items come and go.
  useEffect(() => {
    if (!data) return
    if (!selectedId && data.items.length > 0) setSelectedId(data.items[0].id)
    if (selectedId && !data.items.some(i => i.id === selectedId)) setSelectedId(data.items[0]?.id ?? null)
  }, [data, selectedId])

  const stream = async (res: Response) => {
    const reader = res.body!.getReader()
    const dec = new TextDecoder()
    let buf = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      let idx: number
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const chunk = buf.slice(0, idx)
        buf = buf.slice(idx + 2)
        for (const line of chunk.split('\n')) {
          if (!line.startsWith('data: ')) continue
          try {
            const d = JSON.parse(line.slice(6))
            if (d.type === 'stage') {
              // Ignore the label when it repeats unchanged (the agent keeps
              // reading the same source): toggling it would flicker the
              // status line, and the run timer must not restart per read.
              setDeriveStatus(prev => prev?.label === (d.label ?? '') && prev?.detail === (d.detail ?? '') ? prev : { label: d.label ?? '', detail: d.detail ?? '' })
            } else if (d.type === 'notice') setDeriveStatus({ label: d.content ?? '', detail: '' })
          } catch { /* ignore partial frames */ }
        }
      }
    }
  }

  useEffect(() => {
    if (!deriving) {
      setStatusElapsed(0)
      return
    }
    const t = window.setInterval(() => setStatusElapsed(e => e + 1), 1000)
    return () => window.clearInterval(t)
  }, [deriving])

  const clarLabel = (n: number) => (n < 34 ? 'low' : n <= 66 ? 'medium' : 'high')
  const fmtElapsed = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`

  const runDerive = async (w: PipelineItem, job: { from: string; to: string; slug: string }) => {
    const key = `${w.id}:${job.from}->${job.to}:${job.slug}`
    setBusy(key)
    setDeriveStatus({ label: 'Starting derivation' })
    setDeriving(true)
    let ok = false
    try {
      const body: Record<string, string> = { ...job }
      const sens = clarifyOf.current[w.id]
      if (sens) body.clarify_sensitivity = sens
      const res = await fetch(api('/api/derive'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok || !res.body) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('derive ' + res.status))
      }
      await stream(res)
      ok = true
    } catch (e) {
      setActionError({ key, msg: String(e) })
    } finally {
      await loads(ok)
      setBusy(null)
      setDeriveStatus(null)
      setDeriving(false)
    }
  }

  const jobsOf = (w: PipelineItem, st: { idx: number; col: string[] }, edges: PipelineEdge[]) => {
    const cols = chainColumns(edges, w.seed)
    const fromCol = cols[st.idx - 1] ?? []
    const es = edges.filter(e => st.col.includes(e.to) && fromCol.includes(e.from) && (!e.chain || e.chain === w.seed))
    const jobs: { from: string; to: string; slug: string }[] = []
    for (const e of es) {
      const ids = e.from === w.seed ? [w.id] : (w.stages[e.from] ?? []).map(a => a.id)
      if (ids.length === 0) continue
      if (e.to === 'tickets') {
        for (const slug of ids) jobs.push({ from: e.from, to: e.to, slug })
      } else {
        jobs.push({ from: e.from, to: e.to, slug: ids[0] })
      }
    }
    return jobs
  }

  // Auto-run: after the documentation is approved, drive every remaining
  // stage and auto-approve the results. The chain stops when open questions
  // were derived (LLM clarifications) — those are the only gate.
  const runChain = async (w: PipelineItem) => {
    if (chainRunning) return
    if (busy && busy !== 'new-seed') return
    setChainRunning(w.id)
    try {
      for (let iter = 0; iter < 8; iter++) {
        const fresh = await loads(false)
        if (!fresh) break
        const it = fresh.items.find(x => x.id === w.id)
        if (!it) break
        const st = stageOf(it, fresh.edges)
        if (!st) break
        if (st.col.includes('documentation')) break
        const jobs = jobsOf(it, st, fresh.edges)
        if (jobs.length === 0) break
        for (const job of jobs) await runDerive(it, job)
        const after = await loads(false)
        if (!after) break
        const it2 = after.items.find(x => x.id === w.id)
        if (!it2) break
        const oq = it2.stages['open_questions'] ?? []
        for (const kind of Object.keys(it2.stages)) {
          if (kind === 'open_questions') continue
          for (const a of it2.stages[kind]) {
            if (a.review !== 'approved') await review(a, 'approved')
          }
        }
        if (oq.length > 0) break
      }
    } finally {
      await loads(true)
      setChainRunning(null)
    }
  }

  const review = async (a: PipelineArtifact, value: 'approved' | 'rejected') => {
    if (busy) return
    const next = a.review === value ? 'pending' : value
    const key = `${a.kind}/${a.id}:review`
    setBusy(key)
    let ok = false
    try {
      const res = await fetch(api(`/api/artifacts/${encodeURIComponent(a.kind)}/${encodeURIComponent(a.id)}/review`), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ review: next }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('review ' + res.status))
      }
      ok = true
    } catch (e) {
      setActionError({ key, msg: String(e) })
    } finally {
      await loads(ok)
      setBusy(null)
    }
    // Approving a document kicks the rest of the chain: requirements,
    // decisions and tickets derive and approve themselves afterwards.
    // Approving the answer to an open question resumes a chain that had
    // stopped at the clarification gate.
    if (next === 'approved' && (a.kind === 'documentation' || a.kind === 'open_questions')) {
      const w0 = data?.items.find(i => i.id === a.id)
        ?? data?.items.find(i => (i.stages[a.kind] ?? []).some(x => x.id === a.id))
      if (w0) void runChain(w0)
    }
  }

  const fetchBody = async (kind: string, id: string) => {
    if (bodyCache[id] !== undefined) return
    try {
      const r = await fetch(api(`/api/artifacts/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`))
      if (!r.ok) return
      const j = (await r.json()) as { content?: string }
      setBodyCache(prev => ({ ...prev, [id]: j.content ?? '' }))
    } catch { /* offline edit still allowed */ }
  }

  const saveEdit = async (a: PipelineArtifact) => {
    if (busy) return
    const key = `${a.kind}/${a.id}:edit`
    setBusy(key)
    let ok = false
    try {
      const res = await fetch(api(`/api/artifacts/${encodeURIComponent(a.kind)}/${encodeURIComponent(a.id)}`), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content: bodyCache[a.id] ?? '' }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('edit ' + res.status))
      }
      ok = true
    } catch (e) {
      setActionError({ key, msg: String(e) })
    } finally {
      setEditingId(null)
      await loads(ok)
      setBusy(null)
    }
  }

  const publishOne = async (w: PipelineItem) => {
    if (busy || !w.ready || (w.needs_tickets && countOf(w, 'tickets') === 0)) return
    setBusy(`${w.id}:publish`)
    let ok = false
    try {
      const res = await fetch(api(`/api/work-items/${encodeURIComponent(w.id)}/publish`), { method: 'POST' })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('publish ' + res.status))
      }
      ok = true
    } catch (e) {
      setActionError({ key: `${w.id}:publish`, msg: String(e) })
    } finally {
      await loads(ok)
      setBusy(null)
    }
  }

  const createWorkItem = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !newTitle.trim()) return
    const kind = newSeedKind
    setBusy('new-seed')
    setActionError(null)
    try {
      const res = await fetch(api(`/api/seeds/${encodeURIComponent(kind)}`), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: newTitle.trim(), intent: newTitle.trim() }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('create ' + res.status))
      }
      const created = (await res.json()) as { id?: string }
      setNewTitle('')
      setNewOpen(false)
      const id = created?.id
      if (!id) throw new Error('create returned no id')
      clarifyOf.current[id] = clarLabel(clarifySens)

      const approve = await fetch(api(`/api/artifacts/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/review`), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ review: 'approved' }),
      })
      if (!approve.ok) {
        const j = await approve.json().catch(() => null)
        throw new Error(j?.error ?? ('approve ' + approve.status))
      }
      // Populate the detail pane and list with the new work item immediately,
      // then derive its first stage. The rest of the chain runs once the
      // documentation is approved.
      setSelectedId(id)
      setActiveTab('artifacts')
      await loads(true)
      const edges0 = data?.edges ?? []
      const col1 = chainColumns(edges0, kind)[1]
      if (col1) {
        const probe = { id, title: newTitle.trim(), seed: kind, stages: {} } as PipelineItem
        for (const job of jobsOf(probe, { idx: 1, col: col1 }, edges0)) await runDerive(probe, job)
      }
    } catch (e) {
      setActionError({ key: 'new-seed', msg: String(e) })
    } finally {
      await loads(true)
      setBusy(null)
    }
  }

  const itemById = (id: string): PipelineItem =>
    data?.items.find(i => i.id === id) ?? { id, title: id, seed: 'intents', review: 'pending', stages: {}, ready: false, stale: false, uncovered: 0, published: false, needs_tickets: true }

  const selected: PipelineItem | null = selectedId ? itemById(selectedId) : null

  // Engine events belonging to this work item: match by the artifact ids in
  // its chain, plus the raw seed slug used by derive jobs.
  const selectedEvents = useMemo(() => {
    if (!selected || !data) return []
    const ids = new Set<string>([selected.id, ...flattenItem(selected, data.edges).map(a => a.id)])
    return events.filter(ev => {
      const p = ev.payload ?? {}
      if (typeof p.slug === 'string' && ids.has(p.slug)) return true
      if (typeof p.id === 'string' && ids.has(p.id)) return true
      if (typeof p.from === 'string' && ids.has(p.from)) return true
      return false
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events, selected, data])

  // Live derive status survives a refresh: the latest matching lifecycle
  // event for the work item says whether a run is still in flight.
  const liveDeriveFor = (w: PipelineItem, edges: PipelineEdge[]): boolean => {
    const ids = new Set<string>([w.id, ...flattenItem(w, edges).map(a => a.id)])
    let latest = ''
    for (const ev of events) {
      if (!['derive.started', 'derive.done', 'derive.failed', 'derive.aborted'].includes(ev.type)) continue
      const p = ev.payload ?? {}
      const hits = (typeof p.slug === 'string' && ids.has(p.slug))
        || (typeof p.id === 'string' && ids.has(p.id))
        || (typeof p.from === 'string' && ids.has(p.from))
      if (hits) latest = ev.type
    }
    return latest === 'derive.started'
  }
  // ------------------------------------------------------------- splitter
  const startDrag = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (detailFull) return
    e.currentTarget.setPointerCapture(e.pointerId)
    dragRef.current = { startX: e.clientX, startW: detailW || 520 }
  }
  const onDrag = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!dragRef.current || detailFull) return
    const root = workspaceRef.current
    const max = root ? root.clientWidth - 420 : 900
    const w = dragRef.current.startW - (e.clientX - dragRef.current.startX)
    setDetailW(Math.min(Math.max(w, 320), max))
  }
  const endDrag = (e: ReactPointerEvent<HTMLDivElement>) => {
    dragRef.current = null
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }

  // ------------------------------------------------------- artifact row
  const artifactRow = (a: PipelineArtifact) => {
    const open = openRows.has(a.id)
    const fresh = freshIds.has(a.id)
    const editing = editingId === a.id
    const cached = bodyCache[a.id]
    const err = actionError?.key === `${a.kind}/${a.id}:review` ? actionError.msg : null
    return (
      <div className={`rounded-lg border ${fresh ? 'border-emerald-500/50 bg-emerald-500/5' : 'border-borderDark bg-[#0f172a]/40'}`}>
        <div
          className="flex items-center gap-2 px-3 py-1.5 cursor-pointer select-none"
          onClick={() => {
            setOpenRows(prev => {
              const nx = new Set(prev)
              if (nx.has(a.id)) {
                nx.delete(a.id)
                return nx
              }
              nx.add(a.id)
              if (bodyCache[a.id] === undefined) void fetchBody(a.kind, a.id)
              return nx
            })
          }}
        >
          <svg className={`w-3 h-3 text-slate-500 transition-transform ${open ? 'rotate-90' : ''}`} fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M9 5l7 7-7 7" /></svg>
          <span className={kindChipCls(a.kind)}>{codeOf(a.kind)}</span>
          <span className="font-mono text-[11px] text-slate-500">{a.id}</span>
          <span className="text-xs text-slate-200 truncate min-w-0 flex-1">{a.title}</span>
          {a.stale && <span className="inline-flex px-1.5 py-0.5 rounded-full text-[9px] font-medium border border-orange-500/30 bg-orange-500/10 text-orange-300">stale</span>}
          {a.edited && <span className="inline-flex px-1.5 py-0.5 rounded-full text-[9px] font-medium border border-amber-500/30 bg-amber-500/10 text-amber-300">edited</span>}
          <ReviewPill value={a.review} />
          <button
            className={`${BTN_ROW} ${a.review === 'approved' ? 'bg-emerald-500/15 border-emerald-500/40 text-emerald-300' : 'border-borderDark text-slate-500 hover:text-emerald-300'}`}
            title="Approve (click again to mark needs review)"
            disabled={!!busy}
            onClick={e => { e.stopPropagation(); void review(a, 'approved') }}
          >
            <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M5 13l4 4L19 7" /></svg>
          </button>
          <button
            className={`${BTN_ROW} ${a.review === 'rejected' ? 'bg-rose-500/15 border-rose-500/40 text-rose-300' : 'border-borderDark text-slate-500 hover:text-rose-300'}`}
            title="Reject (click again to mark needs review)"
            disabled={!!busy}
            onClick={e => { e.stopPropagation(); void review(a, 'rejected') }}
          >
            <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M6 6l12 12M18 6L6 18" /></svg>
          </button>
        </div>
        {err && <div className="px-3 pb-1 text-[11px] text-rose-400 font-mono">{err}</div>}
        {open && (
          <div className="px-3 pb-3">
            <div className="flex items-center justify-between gap-2">
              <div className="text-[10px] uppercase tracking-wider text-slate-500 font-semibold">{KIND_NAME[a.kind] ?? a.kind}</div>
              <button
                className="p-1 rounded-md border border-borderDark text-slate-500 hover:text-slate-200 transition-colors"
                title="Edit"
                onClick={() => {
                  if (editing) setEditingId(null)
                  else { if (bodyCache[a.id] === undefined) void fetchBody(a.kind, a.id); setEditingId(a.id) }
                }}
              >
                <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M11 4H4a1 1 0 00-1 1v14a1 1 0 001 1h14a1 1 0 001-1v-7M18.5 2.5a2.1 2.1 0 013 3L12 15l-4 1 1-4 9.5-9.5z" /></svg>
              </button>
            </div>
            {editing ? (
              <div className="mt-2">
                <textarea
                  className="w-full min-h-[120px] resize-y rounded-lg border border-borderDark bg-[#0b1220] p-2 text-xs text-slate-200 focus:outline-none focus:border-accentBlue/60"
                  value={bodyCache[a.id] ?? ''}
                  onChange={e => setBodyCache(prev => ({ ...prev, [a.id]: e.target.value }))}
                />
                <div className="flex justify-end gap-2 mt-2">
                  <button className={`${ACTION_BTN} border border-borderDark text-slate-300 hover:bg-borderDark/20`} onClick={() => setEditingId(null)}>Cancel</button>
                  <button className={`${ACTION_BTN} bg-accentBlue text-white hover:bg-accentBlue/80`} disabled={!!busy} onClick={() => void saveEdit(a)}>Save</button>
                </div>
              </div>
            ) : (
              <p className="text-xs text-slate-400 mt-1.5 leading-relaxed whitespace-pre-wrap">{cached ?? '—'}</p>
            )}
          </div>
        )}
      </div>
    )
  }

  const filterPills = (fl: PipelineArtifact[]) => {
    const counts = new Map<string, number>()
    for (const a of fl) counts.set(codeOf(a.kind), (counts.get(codeOf(a.kind)) ?? 0) + 1)
    const pill = (code: string, active: boolean, n: number, key: string) => (
      <button
        key={key}
        onClick={() => {
          setFilters(prev => {
            const nx = new Set(prev)
            if (nx.has(code)) nx.delete(code)
            else nx.add(code)
            return nx
          })
        }}
        className={`inline-flex items-center gap-1 px-2.5 py-0.5 rounded-full text-[11px] font-semibold border transition-colors ${active ? 'bg-slate-700 text-white border-slate-500' : 'border-borderDark text-slate-400 hover:text-slate-200'}`}
      >
        {code}<span className={`text-[10px] ${active ? 'text-slate-300' : 'text-slate-600'}`}>{n}</span>
      </button>
    )
    return (
      <div className="flex gap-1.5 items-center flex-wrap pb-1">
        {filters.size === 0
          ? pill('All', true, fl.length, 'all')
          : <button key="all" className="inline-flex items-center gap-1 px-2.5 py-0.5 rounded-full text-[11px] font-semibold border border-borderDark text-slate-400 hover:text-slate-200" onClick={() => setFilters(new Set())}>All <span className="text-[10px] text-slate-600">{fl.length}</span></button>}
        {Array.from(counts.entries()).map(([code, n]) => pill(code, filters.has(code), n, code))}
      </div>
    )
  }

  const seedOptions = data?.roots?.length ? data.roots : ['intents']
  const captureForm = () => (
    <form onSubmit={(e) => void createWorkItem(e)} className="px-4 py-2.5 border-t border-dashed border-slate-700 space-y-2">
      <div className="flex items-center gap-2">
        <button
          type="button"
          className="text-[13px] text-accentBlue hover:underline font-medium flex-shrink-0"
          onClick={() => setNewOpen(v => !v)}
        >{newOpen ? 'Cancel' : '+ Capture new work item'}</button>
        {newOpen && (
          <>
            <select
              className="px-2 py-1.5 rounded-lg bg-[#0b1220] border border-borderDark text-sm text-slate-200 focus:outline-none focus:border-accentBlue/60"
              value={newSeedKind}
              onChange={e => setNewSeedKind(e.target.value)}
              title="Work item type — the derivation chain it will follow"
            >
              {seedOptions.map(k => <option key={k} value={k}>{KIND_NAME[k] ?? k}</option>)}
            </select>
            <input
              autoFocus
              className="flex-1 min-w-0 px-2.5 py-1.5 rounded-lg bg-[#0b1220] border border-borderDark text-sm text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-accentBlue/60"
              placeholder="Title (the seed of a work item)…"
              value={newTitle}
              onChange={e => setNewTitle(e.target.value)}
            />
            <button type="submit" className={`${ACTION_BTN} bg-accentBlue text-white hover:bg-accentBlue/80 flex-shrink-0`} disabled={!!busy || !newTitle.trim()}>
              {busy === 'new-seed' ? 'Creating…' : 'Add'}
            </button>
          </>
        )}
      </div>
      {newOpen && (
        <div className="flex items-center gap-2" title="How eagerly the model pauses to ask open questions and decisions while deriving">
          <span className="text-[10px] text-slate-500 flex-shrink-0 w-[86px]">Clarify gate</span>
          <input
            type="range"
            min={0}
            max={100}
            step={5}
            value={clarifySens}
            onChange={e => setClarifySens(Number(e.target.value))}
            className="w-24 accent-accentBlue flex-shrink-0"
          />
          <span className="text-[10px] font-mono text-slate-400 w-14">{clarLabel(clarifySens)}</span>
          <span className="text-[10px] text-slate-600 truncate">low = fewer questions · high = more</span>
        </div>
      )}
      {actionError?.key === 'new-seed' && <span className="text-[11px] text-rose-400 font-mono">{actionError.msg}</span>}
    </form>
  )

  const workRow = (w: PipelineItem) => {
    const edges = data?.edges ?? []
    const fl = flattenItem(w, edges)
    const pending = fl.filter(a => a.review !== 'approved').length
    const all = fl.length
    const deriving = (busy !== null && busy.startsWith(`${w.id}:`)) || liveDeriveFor(w, edges)
    const sel = selectedId === w.id
    const canPublish = w.ready && (!w.needs_tickets || countOf(w, 'tickets') > 0) && !w.published
    const publishTip = w.published ? 'Published'
      : canPublish ? 'Publish ' + w.id
      : pending > 0 ? 'Approve every artifact in ' + w.id + ' first'
      : w.needs_tickets ? 'Derive tickets for ' + w.id + ' first'
      : 'Approve ' + w.id + ' first'
    return (
      <div
        data-row={w.id}
        className={`wi-row border-b border-borderDark last:border-b-0 px-3 py-2.5 cursor-pointer select-none hover:bg-white/5 transition-colors ${sel ? 'bg-accentBlue/10' : ''} ${pending > 0 && !sel ? 'bg-amber-500/[0.04]' : ''}`}
        onClick={() => { setSelectedId(sel ? null : w.id); setEditingId(null); setFilters(new Set()) }}
      >
        <div className="flex items-center gap-1.5 font-mono text-xs text-slate-400 min-w-0">
          <span className={`inline-flex w-1.5 h-1.5 rounded-full flex-shrink-0 ${deriving ? 'bg-accentBlue animate-pulse shadow-[0_0_6px_rgba(59,130,246,0.8)]' : w.published ? 'bg-emerald-400' : pending > 0 ? 'bg-amber-400 animate-pulse shadow-[0_0_6px_rgba(245,158,11,0.8)]' : 'bg-slate-600'}`}></span>
          <span>{w.id}</span>
        </div>
        <div className="text-sm font-medium text-slate-100 truncate min-w-0">{w.title}</div>
        <div className="wi-count text-center text-xs text-slate-400">{countOf(w, 'documentation')}</div>
        <div className="wi-count text-center text-xs text-slate-400">{countOf(w, 'requirements')}</div>
        <div className="wi-count text-center text-xs text-slate-400">{countOf(w, 'decisions') + countOf(w, 'open_questions')}</div>
        <div className="wi-count text-center text-xs text-slate-400">{countOf(w, 'tickets')}</div>
        <div className="flex items-center justify-end gap-2 min-w-0" onClick={e => e.stopPropagation()}>
          <StatePill w={w} all={all} pending={pending} deriving={deriving} />
          {!w.published && (
            <button
              className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-semibold transition-colors flex-shrink-0 ${canPublish ? 'bg-accentBlue text-white hover:bg-accentBlue/80' : 'bg-slate-800 text-slate-500 border border-borderDark cursor-not-allowed'}`}
              title={publishTip}
              disabled={!canPublish || !!busy}
              onClick={() => void publishOne(w)}
            >Publish</button>
          )}
        </div>
        {actionError?.key === `${w.id}:publish` && <div className="px-4 pb-1.5 text-[11px] text-rose-400 font-mono">{actionError.msg}</div>}
      </div>
    )
  }

  const tabBar = (fl: PipelineArtifact[]) => {
    const clarCount = fl.filter(a => a.review !== 'approved').length
    const btn = (key: TabKey, label: string, count: number | null) => {
      const on = activeTab === key
      return (
        <button
          key={key}
          data-tab={key}
          data-on={on}
          onClick={() => setActiveTab(key)}
          className={`px-3 py-2 text-[11px] font-medium -mb-px border-b-2 transition-colors whitespace-nowrap inline-flex items-center ${on ? 'border-accentBlue text-slate-100' : 'border-transparent text-slate-500 hover:text-slate-200'}`}
        >
          {label}
          {count !== null && (
            <span className={`ml-1.5 font-mono text-[9px] px-1.5 py-0.5 rounded-full ${key === 'clarifications' && clarCount > 0 ? 'bg-amber-500/20 text-amber-300 animate-pulse' : on ? 'bg-accentBlue/15 text-accentBlue' : 'bg-borderDark/60 text-slate-500'}`}>{count}</span>
          )}
        </button>
      )
    }
    return (
      <div className="flex items-center gap-1">
        {btn('artifacts', 'Artifacts', fl.length)}
        {btn('clarifications', 'Clarifications', clarCount)}
        {btn('log', 'Run Log', selectedEvents.length)}
      </div>
    )
  }

  const eventLine = (ev: RunEvent) => {
    const t = ev.ts ? ev.ts.slice(11, 19) : ''
    const p = ev.payload ?? {}
    const detail = typeof p.slug === 'string' ? p.slug : typeof p.id === 'string' ? (String(p.id)) : ''
    return (
      <div key={ev.seq} className="rounded-md border border-borderDark/60 bg-[#0f172a]/40 px-2.5 py-1.5 flex items-baseline gap-2">
        <span className="font-mono text-[9px] text-slate-600 flex-shrink-0">{t}</span>
        <span className="font-mono text-[9px] text-slate-500 flex-shrink-0">#{ev.seq}</span>
        <span className="font-mono text-[10px] text-accentBlue flex-shrink-0">{ev.type}</span>
        {detail && <span className="text-[11px] text-slate-400 truncate">{detail}</span>}
      </div>
    )
  }

  const artifactsTab = () => {
    if (!selected) return null
    const w = selected
    const edges = data?.edges ?? []
    const fl = flattenItem(w, edges)
    const st = stageOf(w, edges)
    const cols = chainColumns(edges, w.seed)
    const canPublish = w.ready && (!w.needs_tickets || countOf(w, 'tickets') > 0) && !w.published
    const stepIdx = st ? st.idx : 99
    return (
      <>
        <div className="text-[9px] uppercase tracking-[0.18em] font-bold text-slate-500 mb-1.5">Derivation run</div>
        <div className="flex gap-1.5 flex-wrap items-center">
          {cols.map((col, i) => (
            <span key={col.join('/')} className={`inline-flex items-center gap-1 px-2 py-1 rounded-lg text-[10px] font-semibold border ${i === 0 ? 'border-accentBlue/30 text-accentBlue' : i < stepIdx ? 'bg-slate-700/50 border-slate-600 text-slate-200' : 'border-borderDark text-slate-500'}`}>
              {col.map(k => KIND_NAME[k] ?? k).join(' + ')}
            </span>
          ))}
        </div>
        {actionError?.key === `${w.id}:gate` && <div className="text-[11px] text-rose-400 font-mono">{actionError.msg}</div>}
        <div className="pt-2">{filterPills(fl)}</div>
        <div className="space-y-2">
          {fl.filter(a => filters.size === 0 || filters.has(codeOf(a.kind))).map(a => (
            <div key={`${a.kind}/${a.id}`}>{artifactRow(a)}</div>
          ))}
        </div>
        <div className="flex items-center justify-between pt-1 text-[11px] text-slate-500">
          <span>{filters.size === 0 ? `${fl.length} artifacts` : `Showing ${fl.filter(a => filters.has(codeOf(a.kind))).length} of ${fl.length}`}</span>
          <button
            className={`${ACTION_BTN} inline-flex items-center gap-1.5 ${!canPublish ? 'bg-slate-800 text-slate-400 border border-borderDark cursor-not-allowed' : 'bg-emerald-600 text-white hover:bg-emerald-500'}`}
            title={!canPublish ? publishTipOf(w) : ''}
            disabled={!canPublish || !!busy}
            onClick={() => void publishOne(w)}
          >Publish work item</button>
        </div>
      </>
    )
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps

  if (error) {
    return (
      <div className="max-w-5xl mx-auto pt-10 px-6 text-xs text-rose-400">
        <p>Pipeline error: {error}</p>
        <button onClick={() => { setError(null); void loads() }} className="mt-2 text-accentBlue hover:underline">Retry</button>
      </div>
    )
  }
  if (!data) {
    return <div className="max-w-5xl mx-auto pt-10 px-6 text-xs text-slate-500">Loading work items…</div>
  }

  const dataEdges = data.edges
  const fl = selected ? flattenItem(selected, dataEdges) : []

  const publishTipOf = (w: PipelineItem) => {
    const pend = flattenItem(w, dataEdges).filter(a => a.review !== 'approved').length
    return w.published ? 'Published'
      : w.ready && (!w.needs_tickets || countOf(w, 'tickets') > 0) ? 'Publish ' + w.id
      : pend > 0 ? 'Approve every artifact in ' + w.id + ' first'
      : w.needs_tickets && countOf(w, 'tickets') === 0 ? 'Derive tickets for ' + w.id + ' first'
      : 'Approve ' + w.id + ' first'
  }

  const clarTab = () => {
    if (!selected) return null
    const pending = fl.filter(a => a.review !== 'approved')
    return (
      <>
        {pending.length === 0 ? (
          <p className="text-[11px] text-slate-600">All caught up — nothing needs you.</p>
        ) : (
          <>
            <div className="text-[9px] uppercase tracking-[0.18em] font-bold text-slate-500 mb-1.5">Waiting on you</div>
            <div className="space-y-1.5">
              {pending.map(a => (
                <div key={`${a.kind}/${a.id}`} className="rounded-md border border-amber-500/25 bg-amber-500/[0.05] px-2.5 py-2 flex items-center gap-2">
                  <span className={kindChipCls(a.kind)}>{codeOf(a.kind)}</span>
                  <span className="text-[11px] text-slate-300 truncate min-w-0 flex-1">{a.title}</span>
                  <ReviewPill value={a.review} />
                  <button
                    className={`${ACTION_BTN} bg-accentBlue text-white hover:bg-accentBlue/80`}
                    disabled={!!busy || chainRunning !== null}
                    onClick={() => void review(a, 'approved')}
                  >Approve</button>
                </div>
              ))}
            </div>
            <p className="text-[10px] text-slate-600 pt-1">Everything else derives and approves on its own.</p>
          </>
        )}
      </>
    )
  }

  const runLog = () => {
    const rows = selectedEvents.length ? [...selectedEvents].reverse() : []
    const deriving = selected !== null && ((busy !== null && busy.startsWith(`${selected.id}:`)) || liveDeriveFor(selected, dataEdges))
    return (
      <>
        {(deriveStatus || deriving) && (
          <div className="px-3 py-1.5 rounded-lg bg-slate-800/40 border border-borderDark text-[11px] text-slate-300 font-medium" aria-live="polite">
            {deriveStatus?.label ?? 'Deriving…'}{deriveStatus?.detail ? ` · ${deriveStatus.detail}` : ''}
            <span className="ml-2 text-[10px] font-mono text-slate-500">{fmtElapsed(statusElapsed)}</span>
          </div>
        )}
        <div className="flex items-center gap-2 text-[10px] text-slate-500">
          <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse"></span>
          <span>{rows.length} events · live</span>
        </div>
        <div className="space-y-1">
          {rows.length === 0 ? (
            <p className="text-[11px] text-slate-600">No events yet for this work item.</p>
          ) : (
            rows.map(ev => eventLine(ev))
          )}
        </div>
      </>
    )
  }

  // ------------------------------------------------------------ tab body
  const tabBody = () => {
    if (!selected) return null
    switch (activeTab) {
      case 'clarifications': return clarTab()
      case 'log': return runLog()
      default: return artifactsTab()
    }
  }

  // ------------------------------------------------------------- detail
  const detailPane = () => {
    if (!selected) return (
      <div className="h-full flex flex-col items-center justify-center px-8 text-center">
        <svg className="w-10 h-10 text-slate-700 mb-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5" d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" /><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5" d="M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z" /></svg>
        <p className="text-sm font-medium text-slate-400">No work item selected</p>
        <p className="text-[11px] text-slate-600 mt-1 max-w-[220px]">Select an intent in the list to inspect its run, artifacts and log here.</p>
      </div>
    )
    const w = selected
    return (
      <div className="h-full flex flex-col min-w-0">
        <div className="border-b border-borderDark/30 flex items-center flex-shrink-0">
          <div className="flex-1 min-w-0 pt-1">{tabBar(fl)}</div>
          <div className="flex items-center gap-0.5 pr-2 pb-1.5 flex-shrink-0">
            <button
              data-full
              className="w-7 h-7 rounded-md grid place-items-center text-slate-500 hover:text-slate-200 hover:bg-borderDark/30 transition-colors"
              title={detailFull ? 'Exit fullscreen' : 'Fullscreen detail'}
              onClick={() => setDetailFull(v => !v)}
            >
              {detailFull ? <Minimize2 className="w-3.5 h-3.5" /> : <Maximize2 className="w-3.5 h-3.5" />}
            </button>
            <button className="w-7 h-7 rounded-md grid place-items-center text-slate-500 hover:text-slate-200 hover:bg-borderDark/30 transition-colors" title="Close" onClick={() => setSelectedId(null)}>
              <X className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
        <div className="px-4 py-3 border-b border-borderDark/20 flex-shrink-0">
          <div className="flex items-center gap-2.5">
            <span className={`font-mono text-[11px] font-semibold flex-shrink-0 ${(KIND_CHIP[codeOf(w.seed)] ?? KIND_CHIP.DEV).split(' ')[0]}`}>{w.id}</span>
            <h2 className="text-base font-semibold text-slate-100 leading-tight truncate flex-1 min-w-0">{w.title}</h2>
            {(busy !== null && busy.startsWith(`${w.id}:`)) || liveDeriveFor(w, dataEdges) ? (
              <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-medium border border-accentBlue/30 bg-accentBlue/10 text-accentBlue flex-shrink-0">
                <svg className="w-2.5 h-2.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" /><path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v4a4 4 0 00-4 4H4z" /></svg>
                Deriving
              </span>
            ) : null}
            <StatePill w={w} all={fl.length} pending={fl.filter(a => a.review !== 'approved').length} deriving={false} />
          </div>
          {w.summary && w.summary !== w.title && (
          <p className="text-[11px] text-slate-500 mt-1">{w.summary}</p>
        )}
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto px-4 py-3">{tabBody()}</div>
      </div>
    )
  }

  // -------------------------------------------------------------- header
  // ------------------------------------------------------------- two panes
  return (
    <div className="h-full min-w-0 flex flex-col">
      <div ref={workspaceRef} className="flex gap-0 items-stretch flex-1 min-h-0">
        {/* Work items list */}
        <section className="wi-list flex-1 min-w-0 overflow-hidden bg-[#0b1220]/50 flex flex-col"
          style={detailFull ? { display: 'none' } : undefined}>
          {data.items.length === 0 && (
            <p className="text-xs text-slate-500 border-b border-dashed border-borderDark p-6 text-center">
              No work items yet. Capture one below — it is the seed of its derivation chain.
            </p>
          )}
          <div className="wi-head gap-3 items-center px-3 py-2 bg-slate-800/40 border-b border-borderDark text-[10px] uppercase tracking-wider text-slate-500 font-semibold">
            <span>Intent</span><span>Title</span><span className="wi-count text-center">Docs</span><span className="wi-count text-center">Reqs</span><span className="wi-count text-center">Decis</span><span className="wi-count text-center">Ticks</span><span className="text-right">State</span>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto">
            {data.items.map(w => <div key={w.id}>{workRow(w)}</div>)}
          </div>
          {captureForm()}
        </section>

        {/* Splitter */}
        <div
          className="w-[10px] flex-shrink-0 cursor-col-resize touch-none flex items-center justify-center relative z-10 group select-none"
          title="Drag to resize"
          style={detailFull ? { display: 'none' } : undefined}
          onPointerDown={startDrag}
          onPointerMove={onDrag}
          onPointerUp={endDrag}
          onPointerCancel={endDrag}
        >
          <div className="w-px h-full bg-borderDark/60 group-hover:bg-accentBlue/40 transition-colors"></div>
        </div>

        {/* Detail pane */}
        <section
          className="flex-shrink-0 bg-[#0b1220]/30 min-h-0 overflow-hidden"
          style={detailFull ? { flex: '1 1 auto' } : { width: detailW }}
        >
          <div className="h-full flex flex-col">{detailPane()}</div>
        </section>
      </div>
    </div>
  )
}