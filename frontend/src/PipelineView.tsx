import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'

// The derivation view, work-item shaped. The backend reconstructs each
// intent's chain from the `work_item` frontmatter edge: one intent, its
// derived documentation, the semantic artifacts (requirements, decisions,
// open questions), and the tickets. Review happens per artifact; deriving a
// stage applies the configured agent with the edge's prompt to the approved
// source artifact.

interface PipelineEdge {
  from: string
  to: string
  transform: string
  gate?: string
  agent?: string
}
interface PipelineArtifact {
  id: string
  kind: string
  title: string
  review: string
  stale: boolean
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
}
interface PipelineResponse {
  edges: PipelineEdge[]
  items: PipelineItem[]
}

const REVIEW_STYLES: Record<string, string> = {
  pending: 'bg-amber-500/10 border-amber-500/20 text-amber-400',
  approved: 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400',
  rejected: 'bg-rose-500/10 border-rose-500/20 text-rose-400',
}

function chip(cls: string, label: string) {
  return <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium border ${cls}`}>{label}</span>
}

const ACTION_BTN = 'px-3 py-1.5 rounded-lg text-xs font-medium transition-colors disabled:opacity-60 disabled:cursor-not-allowed'
const BTN_GHOST = `${ACTION_BTN} text-slate-300 border border-borderDark hover:bg-borderDark/20`
const BTN_ROW = 'w-6 h-6 grid place-items-center rounded-md border transition-colors disabled:opacity-40 disabled:cursor-not-allowed'

function Pill({ value }: { value: string }) {
  return <span className={`inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[10px] font-medium border ${REVIEW_STYLES[value] ?? REVIEW_STYLES.pending}`}>{value}</span>
}

function spinner(text: string) {
  return (
    <span className="inline-flex items-center gap-2 text-xs text-slate-400">
      <svg className="w-3.5 h-3.5 spin" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" /></svg>
      {text}
    </span>
  )
}

export default function PipelineView({ refreshKey }: { refreshKey?: number }) {
  const [data, setData] = useState<PipelineResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Action failures render inline next to their row and never replace the
  // view; load() clears them so a successful refetch means state moved on.
  const [actionError, setActionError] = useState<{ key: string; msg: string } | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [progress, setProgress] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const [title, setTitle] = useState('')
  const [intentText, setIntentText] = useState('')

  const load = useCallback(async (clearAction = true) => {
    try {
      const r = await fetch(api('/api/pipeline'))
      if (!r.ok) throw new Error('pipeline ' + r.status)
      setData(await r.json())
      if (clearAction) setActionError(null)
    } catch (e) {
      setError(String(e))
    }
  }, [])
  useEffect(() => { void load() }, [load, refreshKey])

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
            else if (d.type === 'text' && d.content) setProgress(d.content.slice(0, 72))
          } catch { /* ignore partial frames */ }
        }
      }
    }
  }

  const run = async (key: string, init: RequestInit) => {
    setBusy(key)
    setProgress('requesting…')
    let ok = false
    try {
      const res = await fetch(api('/api/derive'), init)
      if (!res.ok || !res.body) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('derive ' + res.status))
      }
      await stream(res)
      ok = true
    } catch (e) {
      setActionError({ key, msg: String(e) })
    } finally {
      await load(ok)
      setBusy(null)
      setProgress('')
    }
  }

  const derive = (item: PipelineItem, edge: PipelineEdge | undefined, slug: string) => {
    if (!edge || busy) return
    void run(`${item.id}:${edge.from}->${edge.to}:${slug}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from: edge.from, to: edge.to, slug }),
    })
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
      await load(ok)
      setBusy(null)
    }
  }

  const createIntent = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy || !title.trim()) return
    setBusy('new-intent')
    try {
      const res = await fetch(api('/api/intents'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: title.trim(), intent: intentText.trim() }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('create ' + res.status))
      }
      setTitle('')
      setIntentText('')
    } catch (e) {
      setActionError({ key: 'new-intent', msg: String(e) })
    } finally {
      await load(true)
      setBusy(null)
    }
  }

  const toggleItem = (id: string) => {
    setExpanded(prev => {
      const n = new Set(prev)
      if (n.has(id)) n.delete(id); else { n.add(id); }
      return n
    })
  }

  const stats = useMemo(() => {
    const items = data?.items ?? []
    return {
      total: items.length,
      ready: items.filter(i => i.ready).length,
      pending: items.filter(i => !i.ready).length,
      stale: items.filter(i => i.stale).length,
      uncovered: items.reduce((n, i) => n + i.uncovered, 0),
    }
  }, [data])
  const statChip = (cls: string, dot: string, count: React.ReactNode, label: string) => (
    <span className={`inline-flex items-center gap-2 px-2.5 py-1.5 rounded-lg text-[11px] font-medium border ${cls}`}>
      <span className={`w-1.5 h-1.5 rounded-full ${dot}`}></span>
      <span className="font-semibold">{count}</span> {label}
    </span>
  )

  if (error) {
    return (
      <div className="max-w-5xl mx-auto pt-10 px-6 text-xs text-rose-400">
        <p>Pipeline error: {error}</p>
        <button onClick={() => { setError(null); void load() }} className="mt-2 text-accentBlue hover:underline">Retry</button>
      </div>
    )
  }
  if (!data) {
    return <div className="max-w-5xl mx-auto pt-10 px-6 text-xs text-slate-500">Loading work items…</div>
  }

  const edge = (from: string, to: string) => data.edges.find(e => e.from === from && e.to === to)
  const artifactsOf = (item: PipelineItem, kind: string): PipelineArtifact[] => item.stages[kind] ?? []

  return (
    <div className="max-w-5xl mx-auto pt-4 px-6 pb-12">
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <h2 className="text-sm font-semibold text-slate-200">Work items</h2>
          {progress && <span className="text-xs text-slate-500 font-mono">{progress}</span>}
        </div>
        <div className="flex items-center gap-2">
          {statChip('border-slate-600/40 bg-slate-500/5 text-slate-300', 'bg-slate-400', stats.total, 'intents')}
          {statChip('border-emerald-500/30 bg-emerald-500/5 text-emerald-300', 'bg-emerald-400', stats.ready, 'ready')}
          {statChip('border-amber-500/30 bg-amber-500/5 text-amber-300', 'bg-amber-400', stats.pending, 'pending')}
          {stats.uncovered > 0 && statChip('border-rose-500/30 bg-rose-500/5 text-rose-300', 'bg-rose-400', stats.uncovered, 'uncovered reqs')}
        </div>
      </div>

      {data.items.length === 0 && (
        <div className="text-xs text-slate-500 border border-borderDark rounded-xl p-6 mb-4">
          No work items yet. An intent is the seed: enter it, approve it, and derive the documentation.
        </div>
      )}

      <form onSubmit={(e) => void createIntent(e)} className="flex gap-2 items-center mb-4">
        <input
          className="flex-1 px-3 py-2 rounded-lg bg-[#0b1220] border border-borderDark text-sm text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-accentBlue/60"
          placeholder="New intent (the seed of a work item)…"
          value={title}
          onChange={e => setTitle(e.target.value)}
        />
        <input
          className="flex-1 px-3 py-2 rounded-lg bg-[#0b1220] border border-borderDark text-sm text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-accentBlue/60"
          placeholder="Intent context (optional)…"
          value={intentText}
          onChange={e => setIntentText(e.target.value)}
        />
        <button type="submit" className={`${ACTION_BTN} bg-accentBlue text-slate-100 hover:bg-accentBlue/80`} disabled={!!busy || !title.trim()}>
          {busy === 'new-intent' ? 'Creating…' : 'Add work item'}
        </button>
      </form>
      {actionError?.key === 'new-intent' && (
        <div className="text-xs text-rose-400 font-mono mb-4">{actionError.msg}</div>
      )}

      <div className="space-y-4">
        {data.items.map(item => {
          const open = expanded.has(item.id)
          const kinds = Object.keys(item.stages)
          const totalArtifacts = kinds.reduce((n, k) => n + (item.stages[k]?.length ?? 0), 0)
          const intentKey = `intents/${item.id}:review`
          const intentErr = actionError?.key === intentKey ? actionError.msg : null
          const intentArt: PipelineArtifact = { id: item.id, kind: 'intents', title: item.title, review: item.review, stale: item.stale }
          return (
            <div key={item.id} className="border border-borderDark rounded-xl overflow-hidden bg-[#0b1220]/40">
              <div
                className="flex items-center gap-3 px-4 py-3 cursor-pointer select-none hover:bg-borderDark/10"
                onClick={() => toggleItem(item.id)}
              >
                <svg className={`w-3 h-3 text-slate-500 transition-transform ${open ? 'rotate-90' : ''}`} fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M9 5l7 7-7 7" /></svg>
                <div className="font-mono text-xs text-slate-400 flex-shrink-0">{item.id}</div>
                <div className="text-sm font-medium text-slate-200 truncate">{item.title}</div>
                {item.stale && chip('bg-orange-500/10 border-orange-500/20 text-orange-300', 'stale')}
                <div className="flex items-center gap-1.5 flex-shrink-0">
                  <Pill value={item.review} />
                  <button
                    className={`${BTN_ROW} ${item.review === 'approved' ? 'bg-emerald-500/15 border-emerald-500/40 text-emerald-300' : 'border-borderDark text-slate-500 hover:text-emerald-300'}`}
                    title="Approve intent (click again to mark needs review)"
                    disabled={!!busy}
                    onClick={(e) => { e.stopPropagation(); void review(intentArt, 'approved') }}
                  >
                    <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M5 13l4 4L19 7" /></svg>
                  </button>
                  <button
                    className={`${BTN_ROW} ${item.review === 'rejected' ? 'bg-rose-500/15 border-rose-500/40 text-rose-300' : 'border-borderDark text-slate-500 hover:text-rose-300'}`}
                    title="Reject intent (click again to mark needs review)"
                    disabled={!!busy}
                    onClick={(e) => { e.stopPropagation(); void review(intentArt, 'rejected') }}
                  >
                    <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M6 6l12 12M18 6L6 18" /></svg>
                  </button>
                </div>
                <div className="ml-auto flex items-center gap-3">
                  <span className="text-[11px] text-slate-500">{totalArtifacts} artifacts</span>
                  {item.uncovered > 0 && chip('bg-rose-500/10 border-rose-500/20 text-rose-300', `${item.uncovered} uncovered`)}
                  {item.ready
                    ? chip('bg-emerald-500/10 border-emerald-500/20 text-emerald-300', 'ready')
                    : chip('bg-amber-500/10 border-amber-500/20 text-amber-300', 'pending')}
                </div>
              </div>
              {intentErr && <div className="px-4 pb-2 text-[11px] text-rose-400 font-mono">{intentErr}</div>}

              {open && (
                <div className="border-t border-borderDark px-4 py-3 space-y-5">
                  <div>
                    <div className="flex items-center justify-between mb-2">
                      <div className="text-[11px] font-semibold uppercase tracking-wide text-slate-500">Derive</div>
                      {actionError && actionError.key.startsWith(`${item.id}:intents->documentation`) && (
                        <span className="text-xs text-rose-400 font-mono">{actionError.msg}</span>
                      )}
                    </div>
                    <div className="flex flex-wrap gap-2">
                      {data.edges.map(e => {
                        if (e.from !== 'intents') return null
                        const key = `${item.id}:${e.from}->${e.to}:${item.id}`
                        const b = busy === key
                        return (
                          <button key={key} className={BTN_GHOST} disabled={!!busy || b}
                            onClick={() => derive(item, e, item.id)}>
                            {b ? e.to + '…' : `Describe ${e.to}`}
                          </button>
                        )
                      })}
                    </div>
                  </div>

                  {kinds.map(kind => {
                    const arts = artifactsOf(item, kind)
                    const sourceKind = kind === 'tickets' ? 'requirements' : 'documentation'
                    const derEdge = edge(sourceKind, kind)
                    const source = artifactsOf(item, sourceKind)
                    const canDerive = !!derEdge && source.length > 0 && !busy
                    return (
                      <div key={kind}>
                        <div className="flex items-center justify-between mb-2">
                          <div className="flex items-center gap-2">
                            <span className="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{kind}</span>
                            <span className="text-[10px] text-slate-600">{arts.length} artifacts</span>
                          </div>
                          {derEdge && (
                            <button
                              className={BTN_GHOST}
                              disabled={!canDerive}
                              title={source.length ? undefined : `approve the source ${sourceKind} first`}
                              onClick={() => derive(item, derEdge, source[source.length - 1].id)}
                            >
                              {busy?.startsWith(`${item.id}:${derEdge.from}->${derEdge.to}`) ? 'Deriving…' : 'Derive ' + kind}
                            </button>
                          )}
                        </div>
                        {arts.length === 0 && (
                          <p className="text-xs text-slate-600">Nothing derived yet.</p>
                        )}
                        <div className="space-y-1.5">
                          {arts.map(a => {
                            const rk = `${a.kind}/${a.id}:review`
                            const err = actionError?.key === rk ? actionError.msg : null
                            return (
                              <div key={a.id} className="flex items-center gap-2 px-3 py-1.5 rounded-lg border border-borderDark/60 bg-[#0f172a]/30">
                                <div className="w-1.5 h-1.5 rounded-full bg-slate-600 flex-shrink-0"></div>
                                <span className="font-mono text-[11px] text-slate-500 flex-shrink-0">{a.id}</span>
                                <span className="text-xs text-slate-300 truncate">{a.title}</span>
                                {a.stale && chip('bg-orange-500/10 border-orange-500/20 text-orange-300', 'stale')}
                                <div className="ml-auto flex items-center gap-2 flex-shrink-0">
                                  <Pill value={a.review} />
                                  <button
                                    className={`${BTN_ROW} ${a.review === 'approved' ? 'bg-emerald-500/15 border-emerald-500/40 text-emerald-300' : 'border-borderDark text-slate-500 hover:text-emerald-300'}`}
                                    title="Approve (click again to mark needs review)"
                                    disabled={!!busy}
                                    onClick={() => void review(a, 'approved')}
                                  >
                                    <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M5 13l4 4L19 7" /></svg>
                                  </button>
                                  <button
                                    className={`${BTN_ROW} ${a.review === 'rejected' ? 'bg-rose-500/15 border-rose-500/40 text-rose-300' : 'border-borderDark text-slate-500 hover:text-rose-300'}`}
                                    title="Reject (click again to mark needs review)"
                                    disabled={!!busy}
                                    onClick={() => void review(a, 'rejected')}
                                  >
                                    <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M6 6l12 12M18 6L6 18" /></svg>
                                  </button>
                                </div>
                                {err && <span className="text-[11px] text-rose-400 font-mono">{err}</span>}
                              </div>
                            )
                          })}
                        </div>
                      </div>
                    )
                  })}
                  {busy?.startsWith(`${item.id}:`) && !kinds.length && spinner('deriving…')}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}