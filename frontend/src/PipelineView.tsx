import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from './api'

// The work-item view, aligned with mocks/work-item-view. One work item
// expands at a time into a flattened artifact timeline with kind chips,
// filter pills, in-place review and editing; a single derive button advances
// the current stage; a work item publishes only when every artifact is
// approved and tickets exist.

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
  review: string
  stages: Record<string, PipelineArtifact[]>
  ready: boolean
  stale: boolean
  uncovered: number
  published: boolean
}
interface PipelineResponse {
  edges: { from: string; to: string }[]
  items: PipelineItem[]
}
type Screen = { name: 'overview' } | { name: 'map'; wi: string } | { name: 'detail'; wi: string; id: string }

const KIND_CODE: Record<string, string> = {
  intents: 'INT', documentation: 'DOC', requirements: 'REQ',
  decisions: 'DEC', open_questions: 'OQ', tickets: 'DEV',
}
const KIND_NAME: Record<string, string> = {
  intents: 'Intent', documentation: 'Feature document', requirements: 'Requirement',
  decisions: 'Decision', open_questions: 'Open question', tickets: 'Ticket',
}
const KIND_CHIP: Record<string, string> = {
  INT: 'text-amber-400 bg-amber-500/10 border-amber-500/30',
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
const STAGE_ORDER = ['intents', 'documentation', 'requirements', 'open_questions', 'decisions', 'tickets']
const ACTION_BTN = 'px-3 py-1.5 rounded-lg text-xs font-medium transition-colors disabled:opacity-60 disabled:cursor-not-allowed'
const BTN_ROW = 'w-6 h-6 grid place-items-center rounded-md border transition-colors disabled:opacity-40 disabled:cursor-not-allowed'

function codeOf(kind: string) {
  return KIND_CODE[kind] ?? kind.slice(0, 3).toUpperCase()
}
function kindChipCls(kind: string) {
  return `inline-flex items-center font-mono text-[10px] font-semibold px-1.5 py-0.5 rounded border ${KIND_CHIP[codeOf(kind)] ?? KIND_CHIP.DEV}`
}
function countOf(item: PipelineItem, kind: string) {
  return item.stages[kind]?.length ?? 0
}
function stageOf(item: PipelineItem) {
  if (countOf(item, 'documentation') === 0) return 'doc'
  if (countOf(item, 'requirements') === 0 && countOf(item, 'decisions') === 0 && countOf(item, 'open_questions') === 0) return 'semantic'
  if (countOf(item, 'tickets') === 0) return 'tickets'
  return 'complete'
}
function stageLabel(st: string) {
  if (st === 'doc') return 'Derive documentation'
  if (st === 'semantic') return 'Derive artifacts'
  if (st === 'tickets') return 'Derive tickets'
  return 'Complete'
}
function flattenItem(item: PipelineItem): PipelineArtifact[] {
  const out: PipelineArtifact[] = []
  for (const kind of STAGE_ORDER) {
    if (kind === 'intents') {
      out.push({ id: item.id, kind, title: item.title, review: item.review, stale: item.stale, edited: false })
      continue
    }
    for (const a of item.stages[kind] ?? []) out.push({ ...a, kind })
  }
  return out
}
function gateBlock(item: PipelineItem, stage: string) {
  if (stage === 'doc') return item.review === 'approved' ? null : [item.id]
  if (stage === 'semantic') {
    const bad = (item.stages.documentation ?? []).filter(d => d.review !== 'approved').map(d => d.id)
    return bad.length ? bad : null
  }
  if (stage === 'tickets') {
    const bad = (item.stages.requirements ?? []).filter(r => r.review !== 'approved').map(r => r.id)
    return bad.length ? bad : null
  }
  return null
}
function srcKindOf(kind: string) {
  if (kind === 'documentation') return 'intents'
  if (kind === 'tickets') return 'requirements'
  if (kind === 'requirements' || kind === 'decisions' || kind === 'open_questions') return 'documentation'
  return ''
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

export default function PipelineView({ refreshKey }: { refreshKey?: number }) {
  const [data, setData] = useState<PipelineResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [progress, setProgress] = useState('')
  const [actionError, setActionError] = useState<{ key: string; msg: string } | null>(null)
  const [viewMode, setViewMode] = useState<'list' | 'grid'>('list')
  const [expandedWi, setExpandedWi] = useState<string | null>(null)
  const [openRows, setOpenRows] = useState<Set<string>>(() => new Set())
  const [filters, setFilters] = useState<Set<string>>(() => new Set())
  const [editingId, setEditingId] = useState<string | null>(null)
  const [newTitle, setNewTitle] = useState('')
  const [newOpen, setNewOpen] = useState(false)
  const [freshIds, setFreshIds] = useState<Set<string>>(() => new Set())
  const [bodyCache, setBodyCache] = useState<Record<string, string>>({})
  const [screen, setScreen] = useState<Screen>({ name: 'overview' })
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
        for (const kind of STAGE_ORDER) {
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
    } catch (e) {
      setError(String(e))
    }
  }, [])
  useEffect(() => { void loads() }, [loads, refreshKey])

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
            if (d.type === 'tool_call') setProgress('step: ' + d.name)
            else if (d.type === 'text' && d.content) setProgress(d.content.slice(0, 80))
          } catch { /* ignore partial frames */ }
        }
      }
    }
  }

  const runDerive = async (w: PipelineItem, job: { from: string; to: string; slug: string }) => {
    const key = `${w.id}:${job.from}->${job.to}:${job.slug}`
    setBusy(key)
    setProgress('requesting…')
    let ok = false
    try {
      const res = await fetch(api('/api/derive'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(job),
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
      setProgress('')
    }
  }

  const deriveStage = async (w: PipelineItem) => {
    if (busy) return
    const st = stageOf(w)
    if (st === 'complete') return
    const gate = gateBlock(w, st)
    if (gate) {
      setActionError({ key: `${w.id}:gate`, msg: 'Approve first: ' + gate.join(', ') })
      return
    }
    let jobs: { from: string; to: string; slug: string }[] = []
    if (st === 'doc') {
      jobs = [{ from: 'intents', to: 'documentation', slug: w.id }]
    } else if (st === 'semantic') {
      const slug = w.stages.documentation?.[0]?.id ?? w.id
      jobs = [
        { from: 'documentation', to: 'requirements', slug },
        { from: 'documentation', to: 'decisions', slug },
        { from: 'documentation', to: 'open_questions', slug },
      ]
    } else {
      for (const r of w.stages.requirements ?? []) jobs.push({ from: 'requirements', to: 'tickets', slug: r.id })
    }
    for (const job of jobs) await runDerive(w, job)
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
    if (busy || !w.ready || countOf(w, 'tickets') === 0) return
    setBusy(`${w.id}:publish`)
    let ok = false
    try {
      const res = await fetch(api(`/api/intents/${encodeURIComponent(w.id)}/publish`), { method: 'POST' })
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

  const publishReadyAll = async () => {
    if (!data) return
    for (const it of data.items) {
      if (it.ready && countOf(it, 'tickets') > 0 && !it.published && !busy) await publishOne(it)
    }
  }

  const createIntent = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !newTitle.trim()) return
    setBusy('new-intent')
    try {
      const res = await fetch(api('/api/intents'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: newTitle.trim(), intent: newTitle.trim() }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('create ' + res.status))
      }
      setNewTitle('')
      setNewOpen(false)
    } catch (e) {
      setActionError({ key: 'new-intent', msg: String(e) })
    } finally {
      await loads(true)
      setBusy(null)
    }
  }

  const itemById = (id: string): PipelineItem =>
    data?.items.find(i => i.id === id) ?? { id, title: id, review: 'pending', stages: {}, ready: false, stale: false, uncovered: 0, published: false }

  const readyCount = useMemo(() => {
    if (!data) return 0
    return data.items.filter(i => i.ready && countOf(i, 'tickets') > 0 && !i.published).length
  }, [data])

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

  // ---------------------------------------------------------------- header

  const headerRow = () => {
    const back = screen.name === 'map' || screen.name === 'detail'
    const title = screen.name === 'overview' ? 'Work items' : screen.name === 'map' ? itemById(screen.wi).title : 'Artifact detail'
    return (
      <div className="flex items-center justify-between mb-4 gap-3 flex-wrap">
        <div className="min-w-0 flex items-center gap-1.5">
          {back && (
            <button
              className="text-slate-400 hover:text-slate-200 transition-colors p-1"
              onClick={() => setScreen(screen.name === 'detail' ? { name: 'map', wi: screen.wi } : { name: 'overview' })}
            >←</button>
          )}
          <div className="min-w-0">
            <h2 className="text-base font-semibold text-slate-100 leading-tight">{title}</h2>
            <div className="text-xs text-slate-500 mt-0.5 truncate">
              {screen.name === 'overview' ? 'Start broad. Expand a work item to reveal its documentation, then open a document in place.' : 'Zoomed into one work item. Each stage shows the artifacts derived from the previous level.'}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2 flex-shrink-0">
          {screen.name === 'overview' && (
            <>
              <div className="inline-flex gap-0.5 p-0.5 rounded-lg bg-slate-800/60 border border-borderDark">
                <button className={`px-3 py-1 rounded-md text-xs font-medium transition ${viewMode === 'list' ? 'bg-slate-700 text-slate-100' : 'text-slate-400 hover:text-slate-200'}`} onClick={() => setViewMode('list')}>List</button>
                <button className={`px-3 py-1 rounded-md text-xs font-medium transition ${viewMode === 'grid' ? 'bg-slate-700 text-slate-100' : 'text-slate-400 hover:text-slate-200'}`} onClick={() => setViewMode('grid')}>Cards</button>
              </div>
              <button
                className={`${ACTION_BTN} inline-flex items-center gap-1.5 bg-emerald-600 text-white hover:bg-emerald-500 ${readyCount === 0 ? 'opacity-50 cursor-not-allowed' : ''}`}
                disabled={readyCount === 0 || busy !== null}
                onClick={() => void publishReadyAll()}
              >
                <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M5 13l4 4L19 7" /></svg>
                {readyCount ? `Publish ready (${readyCount})` : 'Publish ready'}
              </button>
            </>
          )}
          {progress && <span className="text-[11px] text-slate-500 font-mono">{progress}</span>}
        </div>
      </div>
    )
  }

  // ------------------------------------------------------------- capture
  const captureForm = () => (
    <form onSubmit={(e) => void createIntent(e)} className="flex items-center gap-2 px-4 py-2.5 border-t border-dashed border-slate-700">
      <button
        type="button"
        className="text-[13px] text-accentBlue hover:underline font-medium flex-shrink-0"
        onClick={() => setNewOpen(v => !v)}
      >{newOpen ? 'Cancel' : '+ Capture new intent'}</button>
      {newOpen && (
        <>
          <input
            autoFocus
            className="flex-1 px-2.5 py-1.5 rounded-lg bg-[#0b1220] border border-borderDark text-sm text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-accentBlue/60"
            placeholder="Title (the seed of a work item)…"
            value={newTitle}
            onChange={e => setNewTitle(e.target.value)}
          />
          <button type="submit" className={`${ACTION_BTN} bg-accentBlue text-white hover:bg-accentBlue/80`} disabled={!!busy || !newTitle.trim()}>
            {busy === 'new-intent' ? 'Creating…' : 'Add'}
          </button>
        </>
      )}
      {actionError?.key === 'new-intent' && <span className="text-[11px] text-rose-400 font-mono">{actionError.msg}</span>}
    </form>
  )

  // ---------------------------------------------------------- filter pills
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

  // ---------------------------------------------------------- artifact row
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
            <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M6 6l12 12M18 6L6 18" /></svg>
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

  // -------------------------------------------------------- work item row
  const workRow = (w: PipelineItem) => {
    const open = expandedWi === w.id
    const st = stageOf(w)
    const gate = gateBlock(w, st)
    const fl = open ? flattenItem(w) : []
    const pending = fl.filter(a => a.review !== 'approved').length
    const all = fl.length
    const canPublish = w.ready && countOf(w, 'tickets') > 0 && !w.published
    const publishTip = w.published ? 'Published'
      : canPublish ? 'Publish ' + w.id
      : pending > 0 ? 'Approve every artifact in ' + w.id + ' first'
      : 'Derive tickets for ' + w.id + ' first'
    const deriveTip = gate ? 'Approve first: ' + gate.join(', ') : st === 'complete' ? 'All artifacts derived' : ''
    const statePill = w.published
      ? <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border bg-emerald-500/10 border-emerald-500/30 text-emerald-400"><span className="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>Published</span>
      : all === 0 ? null
      : pending > 0
        ? <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border border-amber-500/30 bg-amber-500/10 text-amber-400"><span className="w-1.5 h-1.5 rounded-full bg-amber-400"></span>{pending} of {all} pending</span>
        : <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border border-amber-500/30 bg-amber-500/10 text-amber-400"><span className="w-1.5 h-1.5 rounded-full bg-amber-400"></span>Needs tickets</span>
    return (
      <div className={`border-b border-borderDark last:border-b-0 ${freshIds.has(w.id) ? 'bg-emerald-500/5' : ''}`}>
        <div
          className="grid grid-cols-[96px_minmax(150px,1.2fr)_minmax(200px,1.8fr)_44px_44px_44px_52px_150px] gap-3 items-center px-4 py-2.5 cursor-pointer select-none hover:bg-white/5 transition-colors"
          onClick={() => {
            if (expandedWi === w.id) { setExpandedWi(null); return }
            setExpandedWi(w.id)
            setOpenRows(new Set())
            setFilters(new Set())
            setEditingId(null)
          }}
        >
          <div className="flex items-center gap-1.5 font-mono text-xs text-slate-400">
            <svg className={`w-3 h-3 text-slate-500 transition-transform ${open ? 'rotate-90' : ''}`} fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M9 5l7 7-7 7" /></svg>
            {w.id}
          </div>
          <div className="text-sm font-medium text-slate-100 truncate">{w.title}</div>
          <div className="text-xs text-slate-500 truncate">{w.summary || w.title}</div>
          <div className="text-center text-xs text-slate-400">{countOf(w, 'documentation')}</div>
          <div className="text-center text-xs text-slate-400">{countOf(w, 'requirements')}</div>
          <div className="text-center text-xs text-slate-400">{countOf(w, 'decisions') + countOf(w, 'open_questions')}</div>
          <div className="text-center text-xs text-slate-400">{countOf(w, 'tickets')}</div>
          <div className="flex items-center justify-end gap-2" onClick={e => e.stopPropagation()}>
            {statePill}
            {!w.published && (
              <button
                className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-semibold transition-colors ${canPublish ? 'bg-accentBlue text-white hover:bg-accentBlue/80' : 'bg-slate-800 text-slate-500 border border-borderDark cursor-not-allowed'}`}
                title={publishTip}
                disabled={!canPublish || !!busy}
                onClick={() => void publishOne(w)}
              >Publish</button>
            )}
          </div>
        </div>
        {actionError?.key === `${w.id}:publish` && <div className="px-4 pb-1.5 text-[11px] text-rose-400 font-mono">{actionError.msg}</div>}
        {open && (
          <div className="border-t border-borderDark px-4 py-3 space-y-2 bg-white/[0.02]">
            {filterPills(fl)}
            {fl.filter(a => filters.size === 0 || filters.has(codeOf(a.kind))).map(a => (
              <div key={`${a.kind}/${a.id}`}>{artifactRow(a)}</div>
            ))}
            <div className="flex items-center justify-between pt-1">
              <div className="text-[11px] text-slate-500">
                {filters.size === 0 ? `Showing ${fl.length} artifacts` : `Showing ${fl.filter(a => filters.has(codeOf(a.kind))).length} of ${fl.length}`}
              </div>
              <div className="flex items-center gap-2">
                {actionError?.key === `${w.id}:gate` && <span className="text-[11px] text-rose-400 font-mono">{actionError.msg}</span>}
                {busy?.startsWith(`${w.id}:`) && <span className="text-[11px] text-slate-400 font-mono">{progress || 'deriving…'}</span>}
                <button
                  className={`${ACTION_BTN} inline-flex items-center gap-1.5 ${st === 'complete' || busy || gate ? 'bg-slate-800 text-slate-400 border border-borderDark cursor-not-allowed' : 'bg-accentBlue text-white hover:bg-accentBlue/80'}`}
                  title={deriveTip}
                  disabled={st === 'complete' || !!busy || !!gate}
                  onClick={() => void deriveStage(w)}
                >
                  <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M12 5v14M5 12l7 7 7-7" /></svg>
                  {st === 'complete' ? 'Complete' : stageLabel(st)}
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    )
  }

  // ------------------------------------------------------------- list view
  const listView = () => (
    <div className="border border-borderDark rounded-xl overflow-hidden bg-[#0b1220]/50">
      <div className="grid grid-cols-[96px_minmax(150px,1.2fr)_minmax(200px,1.8fr)_44px_44px_44px_52px_150px] gap-3 items-center px-4 py-2 bg-slate-800/40 border-b border-borderDark text-[10px] uppercase tracking-wider text-slate-500 font-semibold">
        <span>ID</span><span>Title</span><span>Intent</span><span className="text-center">Docs</span><span className="text-center">Reqs</span><span className="text-center">Decis</span><span className="text-center">Ticks</span><span className="text-right">Publish</span>
      </div>
      {data.items.map(w => <div key={w.id}>{workRow(w)}</div>)}
      {captureForm()}
    </div>
  )

  // ------------------------------------------------------------ grid view
  const gridView = () => (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-3.5">
      {data.items.map(w => (
        <article
          key={w.id}
          className="border border-borderDark rounded-xl bg-[#0b1220]/50 p-4 cursor-pointer hover:border-slate-500 hover:shadow-lg transition-all"
          onClick={() => setScreen({ name: 'map', wi: w.id })}
        >
          <div className="font-mono text-[11px] font-semibold text-slate-500 mb-1.5">{w.id}</div>
          <h3 className="text-sm font-semibold text-slate-100 mb-1.5">{w.title}</h3>
          <p className="text-xs text-slate-500 leading-relaxed mb-3 line-clamp-3">{w.summary || w.title}</p>
          <div className="flex gap-1.5 flex-wrap">
            <span className="inline-flex px-2 py-0.5 rounded-full text-[10px] bg-slate-500/10 border border-borderDark text-slate-400">{countOf(w, 'documentation')} doc</span>
            <span className="inline-flex px-2 py-0.5 rounded-full text-[10px] bg-slate-500/10 border border-borderDark text-slate-400">{countOf(w, 'requirements')} req</span>
            <span className="inline-flex px-2 py-0.5 rounded-full text-[10px] bg-slate-500/10 border border-borderDark text-slate-400">{countOf(w, 'tickets')} ticket</span>
          </div>
          <div className="mt-3 flex items-center gap-1 text-[11px] font-medium text-accentBlue">
            Open to zoom into work item
            <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M9 5l7 7-7 7" /></svg>
          </div>
        </article>
      ))}
      <button
        className="border border-dashed border-borderDark rounded-xl p-4 text-left hover:border-accentBlue/60 transition-colors bg-transparent cursor-pointer"
        onClick={() => { setViewMode('list'); setNewOpen(true) }}
      >
        <div className="font-mono text-[11px] text-slate-500 mb-1.5">New work</div>
        <h3 className="text-sm font-semibold text-accentBlue">+ Capture intent</h3>
        <p className="text-xs text-slate-500 mt-1">Start with a title. Devtop expands the intent before deriving documentation.</p>
      </button>
    </div>
  )

  // -------------------------------------------------------------- map view
  const mapView = () => {
    if (screen.name !== 'map') return null
    const w = itemById(screen.wi)
    const fl = flattenItem(w)
    const intent = fl.filter(a => a.kind === 'intents')
    const doc = fl.filter(a => a.kind === 'documentation')
    const semantics = fl.filter(a => a.kind === 'requirements' || a.kind === 'decisions' || a.kind === 'open_questions')
    const tickets = fl.filter(a => a.kind === 'tickets')
    const card = (a: PipelineArtifact) => (
      <article
        key={`${a.kind}/${a.id}`}
        className="bg-[#0b1220]/60 border border-borderDark rounded-lg p-3 cursor-pointer hover:border-slate-500 transition-colors"
        onClick={() => setScreen({ name: 'detail', wi: w.id, id: a.id })}
      >
        <div className="font-mono text-[10px] text-slate-500 mb-1 flex items-center gap-1.5"><span className={kindChipCls(a.kind)}>{codeOf(a.kind)}</span>{a.id}</div>
        <h4 className="text-xs font-semibold text-slate-200 mb-2">{a.title}</h4>
        <ReviewPill value={a.review} />
      </article>
    )
    const connector = () => (
      <div className="flex items-center justify-center text-slate-600 py-0.5">
        <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M12 5v14M5 12l7 7 7-7" /></svg>
      </div>
    )
    const stageBox = (title: string, meta: string, arts: PipelineArtifact[]) => (
      <section className="bg-slate-800/30 border border-borderDark rounded-xl p-3">
        <div className="flex items-center justify-between mb-2">
          <div className="text-[13px] font-bold text-slate-200">{title}</div>
          <div className="text-[11px] text-slate-500 font-medium">{meta}</div>
        </div>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-2">
          {arts.length ? arts.map(card) : <div className="text-xs text-slate-600 p-2">Nothing derived yet.</div>}
        </div>
      </section>
    )
    return (
      <div className="grid grid-cols-[240px_1fr] gap-5 items-start">
        <section className="bg-slate-900 rounded-xl p-4 border border-slate-700/60 sticky top-0">
          <div className="font-mono text-[10px] text-slate-500 mb-1.5">{w.id} · Work item</div>
          <h3 className="text-[15px] font-bold text-white mb-2">{w.title}</h3>
          <p className="text-xs text-slate-400 leading-relaxed mb-3">{w.summary || w.title}</p>
          <span className={`inline-flex px-2 py-0.5 rounded-full text-[10px] font-medium border ${w.ready ? 'bg-emerald-600/20 border-emerald-600/30 text-emerald-300' : 'bg-amber-600/20 border-amber-600/30 text-amber-300'}`}>{w.ready ? 'Ready' : 'In progress'}</span>
          <div className="flex items-center gap-1 text-[11px] font-medium text-accentBlue mt-3">Stable container for the entire change</div>
        </section>
        <div className="space-y-1">
          {stageBox('Intent', 'The seed', intent)}
          {connector()}
          {stageBox('Documentation', 'Derived from intent', doc)}
          {doc.length > 0 && connector()}
          {stageBox('Semantic artifacts', 'Derived from documentation', semantics)}
          {semantics.length > 0 && connector()}
          {stageBox('Implementation', 'Derived from requirements', tickets)}
        </div>
      </div>
    )
  }

  // ---------------------------------------------------------- detail view
  const detailView = () => {
    if (screen.name !== 'detail') return null
    const w = itemById(screen.wi)
    const fl = flattenItem(w)
    const a = fl.find(x => x.id === screen.id) ?? null
    if (!a) return <p className="text-xs text-slate-500">Artifact not found.</p>
    if (bodyCache[a.id] === undefined) void fetchBody(a.kind, a.id)
    const srcKind = srcKindOf(a.kind)
    const sources = srcKind ? fl.filter(x => x.kind === srcKind) : []
    return (
      <div className="grid grid-cols-[1fr_260px] gap-4">
        <section className="border border-borderDark rounded-xl p-5 bg-[#0b1220]/40">
          <div className="flex items-center gap-2 mb-1">
            <span className={kindChipCls(a.kind)}>{codeOf(a.kind)}</span>
            <span className="font-mono text-xs text-slate-500">{a.id}</span>
          </div>
          <h3 className="text-lg font-bold text-slate-100 mb-2">{a.title}</h3>
          <div className="mb-3"><ReviewPill value={a.review} /></div>
          <p className="text-[13px] text-slate-400 leading-relaxed whitespace-pre-wrap">{bodyCache[a.id] ?? '—'}</p>
        </section>
        <aside className="border border-borderDark rounded-xl p-4 bg-[#0c1220]/40 h-fit">
          <h4 className="text-[11px] uppercase tracking-wider text-slate-500 font-semibold mb-3">Lineage</h4>
          <div className="space-y-2">
            <div className="rounded-lg bg-slate-800/40 border border-borderDark p-2.5">
              <div className="text-[10px] uppercase text-slate-500 font-semibold mb-0.5">Work item</div>
              <div className="text-xs text-slate-200">{w.title} <span className="font-mono text-slate-500">({w.id})</span></div>
            </div>
            {srcKind && (
              <div className="rounded-lg bg-slate-800/40 border border-borderDark p-2.5">
                <div className="text-[10px] uppercase text-slate-500 font-semibold mb-0.5">Source · {KIND_NAME[srcKind] ?? srcKind}</div>
                {sources.length
                  ? sources.map(s => <div key={s.id} className="text-xs text-slate-200">{s.title} <span className="font-mono text-slate-500">({s.id})</span></div>)
                  : <div className="text-xs text-slate-500">—</div>}
              </div>
            )}
            <div className="rounded-lg bg-slate-800/40 border border-borderDark p-2.5">
              <div className="text-[10px] uppercase text-slate-500 font-semibold mb-0.5">Current artifact</div>
              <div className="text-xs text-slate-200">{a.id} · {KIND_NAME[a.kind] ?? a.kind}</div>
            </div>
          </div>
        </aside>
      </div>
    )
  }

  // ---------------------------------------------------------------- root
  return (
    <div className="max-w-6xl mx-auto pt-4 px-6 pb-12">
      {headerRow()}
      {screen.name === 'overview' && (
        <>
          {data.items.length === 0 && (
            <p className="text-xs text-slate-500 border border-dashed border-borderDark rounded-xl p-6 mb-4 text-center">
              No work items yet. Capture an intent below — it is the seed of a work item.
            </p>
          )}
          {viewMode === 'list' ? listView() : gridView()}
        </>
      )}
      {screen.name === 'map' && mapView()}
      {screen.name === 'detail' && detailView()}
    </div>
  )
}