import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import { ExternalLink, FileText, Info } from 'lucide-react'

interface PipelineEdge {
  from: string
  to: string
  transform: string
  gate?: string
  agent?: string
  classifier?: string
}
interface PipelineTicket {
  id: string
  title: string
  status: string
}
interface PipelinePRD {
  id: string
  title: string
  status: string
  reqs: number
  slug: string
}
interface PipelineItem {
  doc_id: string
  title: string
  slug: string
  path: string
  dir: string
  summary?: string
  prospect?: string
  prospect_by?: string
  prd?: PipelinePRD
  tickets: PipelineTicket[]
  stale: boolean
}
interface PipelineResponse {
  edges: PipelineEdge[]
  items: PipelineItem[]
}

const PRD_STYLES: Record<string, string> = {
  draft: 'bg-blue-500/10 border-blue-500/20 text-blue-400',
  reviewing: 'bg-amber-500/10 border-amber-500/20 text-amber-400',
  approved: 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400',
  archived: 'bg-purple-500/10 border-purple-500/20 text-purple-400',
}
const TKT_STYLES: Record<string, string> = {
  open: 'bg-blue-500/10 border-blue-500/20 text-blue-400',
  'in-progress': 'bg-amber-500/10 border-amber-500/20 text-amber-400',
  done: 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400',
}

function chip(cls: string, label: string) {
  return <span className={`inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium border capitalize flex-shrink-0 ${cls}`}>{label}</span>
}

// ---- grouping: eligibility sections, then directory sub-groups ----
const SECTION_ORDER = ['eligible', 'unassessed', 'not-eligible'] as const
const SECTION_LABEL: Record<string, string> = { eligible: 'Eligible', unassessed: 'Unassessed', 'not-eligible': 'Not eligible' }
const SECTION_DOT: Record<string, string> = { eligible: 'bg-emerald-400', unassessed: 'bg-slate-500', 'not-eligible': 'bg-rose-400' }
const SECTION_TEXT: Record<string, string> = {
  eligible: 'text-emerald-300',
  unassessed: 'text-slate-400',
  'not-eligible': 'text-rose-300',
}

function groupOf(item: PipelineItem): string {
  if (item.prospect === 'eligible') return 'eligible'
  if (item.prospect === 'not-eligible') return 'not-eligible'
  return 'unassessed'
}

function titleCase(s: string) {
  return s.split('-').map(w => w.charAt(0).toUpperCase() + w.slice(1)).join(' ')
}

function prospectChip(item: PipelineItem) {
  const by = item.prospect_by
  if (!item.prospect) {
    return <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[9px] font-medium border border-slate-600/40 text-slate-500 bg-slate-500/5">unassessed</span>
  }
  const eligible = item.prospect === 'eligible'
  // Only an explicit user verdict is "verified"; a model or unprovenanced
  // verdict must not masquerade as a sealed one.
  const byUser = by === 'user'
  const label = (eligible ? 'eligible' : 'not eligible') + (byUser ? ' · verified' : ' · suggested')
  const cls = eligible
    ? (byUser ? 'border-emerald-500/40 bg-emerald-500/15 text-emerald-200' : 'border-emerald-500/25 bg-emerald-500/10 text-emerald-300')
    : (byUser ? 'border-rose-500/40 bg-rose-500/15 text-rose-200' : 'border-rose-500/25 bg-rose-500/10 text-rose-300')
  return (
    <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[9px] font-medium border ${cls}`}>
      {byUser
        ? <svg className="w-2.5 h-2.5 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z" /></svg>
        : <span className="w-1 h-1 rounded-full bg-current"></span>}
      <span className="font-mono">{label}</span>
    </span>
  )
}

function openLink(href: string, label: string) {
  return (
    <a href={href} target="_blank" rel="noopener" title={`Open ${label} in a new tab`}
      className="inline-flex items-center justify-center w-5 h-5 rounded-md text-slate-500 hover:text-slate-200 hover:bg-borderDark/40 transition-colors flex-shrink-0">
      <ExternalLink className="w-3 h-3" />
    </a>
  )
}

function spinner(text: string) {
  return (
    <span className="inline-flex items-center gap-2 text-xs text-slate-400">
      <svg className="w-3.5 h-3.5 spin" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" /></svg>
      {text}
    </span>
  )
}

const ACTION_BTN = 'px-3 py-1.5 rounded-lg text-xs font-medium transition-colors disabled:opacity-60 disabled:cursor-not-allowed'
const BTN_PRIMARY = `${ACTION_BTN} bg-accentBlue text-slate-100 hover:bg-accentBlue/80`
const BTN_GHOST = `${ACTION_BTN} text-slate-300 border border-borderDark hover:bg-borderDark/20`

export default function PipelineView({ refreshKey }: { refreshKey?: number }) {
  const [data, setData] = useState<PipelineResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [progress, setProgress] = useState('')
  const [closed, setClosed] = useState<Set<string>>(() => new Set(['unassessed', 'not-eligible']))

  const load = useCallback(async () => {
    try {
      const r = await fetch(api('/api/pipeline'))
      if (!r.ok) throw new Error('pipeline ' + r.status)
      setData(await r.json())
    } catch (e) {
      setError(String(e))
    }
  }, [])
  useEffect(() => { void load() }, [load, refreshKey])

  const edge = (from: string, to: string) => data?.edges.find(e => e.from === from && e.to === to)

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
            if (d.type === 'prospect') setProgress(d.written ? `prospect: ${d.prospect} (${d.prospect_by})` : `prospect: ${d.note || 'no write detected'}`)
            else if (d.type === 'tool_call') setProgress('step: ' + d.name)
            else if (d.type === 'text' && d.content) setProgress(d.content.slice(0, 72))
          } catch { /* ignore partial frames */ }
        }
      }
    }
  }

  const derive = async (slug: string, e?: PipelineEdge, busyKey?: string) => {
    if (!e || busy) return
    setBusy(busyKey ?? slug)
    setProgress('requesting derivation…')
    try {
      const res = await fetch(api('/api/derive'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ from: e.from, to: e.to, slug }),
      })
      if (!res.ok || !res.body) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('derive ' + res.status))
      }
      await stream(res)
      await load()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(null)
      setProgress('')
    }
  }

  const suggest = async (item: PipelineItem) => {
    if (busy) return
    setBusy(item.slug + ':classify')
    setProgress('classify-doc is reading…')
    try {
      const res = await fetch(api('/api/pipeline/prospect/classify'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind: 'docs', slug: item.slug }),
      })
      if (!res.ok || !res.body) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('classify ' + res.status))
      }
      await stream(res)
      await load()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(null)
      setProgress('')
    }
  }

  const verdict = async (item: PipelineItem, v: 'eligible' | 'not-eligible') => {
    if (busy) return
    setBusy(item.slug + ':verdict')
    try {
      const res = await fetch(api('/api/pipeline/prospect'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind: 'docs', slug: item.slug, verdict: v }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('verdict ' + res.status))
      }
      await load()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(null)
    }
  }

  const setStatus = async (slug: string, status: string) => {
    if (busy) return
    setBusy(slug + ':status')
    try {
      const res = await fetch(api(`/api/pipeline/prds/${slug}/status`), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      })
      if (!res.ok) {
        const j = await res.json().catch(() => null)
        throw new Error(j?.error ?? ('status ' + res.status))
      }
      await load()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(null)
    }
  }

  const toggle = (k: string) => {
    setClosed(prev => {
      const n = new Set(prev)
      if (n.has(k)) n.delete(k); else n.add(k)
      return n
    })
  }

  interface Grouped {
    section: string
    dir: string
    items: PipelineItem[]
  }
  const groups = useMemo<Grouped[]>(() => {
    const out: Grouped[] = []
    for (const section of SECTION_ORDER) {
      const items = data
        ? data.items
            .filter(i => groupOf(i) === section)
            .sort((a, b) => (a.dir === b.dir ? a.path.localeCompare(b.path) : a.dir.localeCompare(b.dir)))
        : []
      let current: Grouped | null = null
      for (const item of items) {
        if (!current || current.section !== section || current.dir !== item.dir) {
          current = { section, dir: item.dir, items: [] }
          out.push(current)
        }
        current.items.push(item)
      }
    }
    return out
  }, [data])

  const stats = useMemo(() => {
    const items = data?.items ?? []
    return {
      total: items.length,
      eligible: items.filter(i => i.prospect === 'eligible').length,
      excluded: items.filter(i => i.prospect === 'not-eligible').length,
      unassessed: items.filter(i => !i.prospect).length,
      derived: items.filter(i => !!i.prd).length,
      approved: items.filter(i => i.prd?.status === 'approved').length,
      open: items.reduce((n, i) => n + i.tickets.filter(t => t.status !== 'done').length, 0),
      stale: items.filter(i => i.stale).length,
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
    return <div className="max-w-5xl mx-auto pt-10 px-6 text-xs text-slate-500">Loading pipeline…</div>
  }

  const prdActions = (item: PipelineItem) => {
    if (item.prospect !== 'eligible') return null
    const prd = item.prd
    if (busy === item.slug) return spinner(progress || 'deriving…')
    if (!prd) {
      return <button onClick={() => void derive(item.slug, edge('docs', 'prds'))} className={BTN_PRIMARY}>Derive PRD</button>
    }
    if (prd.status === 'draft') {
      return <button onClick={() => void setStatus(item.slug, 'reviewing')} className={BTN_GHOST}>Review PRD</button>
    }
    if (prd.status === 'reviewing') {
      return (
        <>
          <button onClick={() => void setStatus(item.slug, 'approved')} className={BTN_PRIMARY}>Approve</button>
          <button onClick={() => void setStatus(item.slug, 'draft')} className={BTN_GHOST}>Request changes</button>
        </>
      )
    }
    const re = item.stale
      ? <button onClick={() => void derive(item.slug, edge('docs', 'prds'))} className={BTN_PRIMARY}>Re-derive delta</button>
      : <button onClick={() => void derive(item.slug, edge('docs', 'prds'))} className={`${BTN_GHOST} ${item.prd?.slug === item.slug ? '' : 'opacity-60'}`}>Re-derive</button>
    return re
  }

  const ticketActions = (item: PipelineItem) => {
    const prd = item.prd
    if (prd?.status !== 'approved') return null
    if (busy === item.slug + ':tickets') return spinner(progress || 'creating tickets…')
    return item.tickets.length === 0
      ? <button onClick={() => void derive(item.slug, edge('prds', 'tickets'), item.slug + ':tickets')} className={BTN_PRIMARY}>Create tickets</button>
      : <button onClick={() => void derive(item.slug, edge('prds', 'tickets'), item.slug + ':tickets')} className={BTN_GHOST}>New ticket</button>
  }

  const prospectActions = (item: PipelineItem) => {
    if (busy === item.slug + ':classify') {
      return <div className="mt-2.5 pt-2 border-t border-borderDark/30 flex items-center gap-2">{spinner(progress || 'checking…')}</div>
    }
    if (!item.prospect) {
      return (
        <div className="mt-2.5 pt-2 border-t border-borderDark/30 flex items-center gap-2 flex-wrap">
          <button onClick={() => void suggest(item)} className={BTN_PRIMARY}>Suggest eligibility</button>
          <span className="text-[10px] text-slate-600">classify-doc reads the doc, writes <span className="font-mono">derive_prospects</span>.</span>
        </div>
      )
    }
    if (item.prospect_by === 'model') {
      return (
        <div className="mt-2.5 pt-2 border-t border-borderDark/30 flex items-center gap-2 flex-wrap">
          {item.prospect === 'eligible'
            ? <>
                <button onClick={() => void verdict(item, 'eligible')} className={BTN_GHOST}>Confirm</button>
                <button onClick={() => void verdict(item, 'not-eligible')} className={BTN_GHOST}>Exclude</button>
              </>
            : <button onClick={() => void verdict(item, 'eligible')} className={BTN_GHOST}>Override</button>}
          <span className="text-[10px] text-slate-600">Model verdict — the user decides.</span>
        </div>
      )
    }
    return null
  }

  const sectionOpen = (s: string) => !closed.has(s)
  const dirOpen = (s: string, d: string) => !closed.has(s + '/' + d)

  return (
    <div className="max-w-5xl mx-auto pt-8 px-6 fade-in">
      <h1 className="text-2xl font-bold text-slate-100">Derivation pipeline</h1>
      <p className="text-xs text-slate-400 mt-1">
        document TL;DR → PRD → tickets. Docs qualify via <span className="font-mono">derive_prospects</span>: classify-doc suggests, the user verifies. Gate: <span className="font-mono">prds.status == approved</span>.
      </p>

      <div className="pt-6 pb-2">
        <div className="rounded-xl border border-borderDark/40 bg-surfaceDark/40 shadow-2xl overflow-hidden">
          <div className="px-6 pt-5 flex flex-wrap items-center justify-between gap-3 border-b border-borderDark/30">
            <div className="flex flex-wrap items-center gap-2">
              {statChip('bg-borderDark/40 border-borderDark/50 text-slate-300', 'bg-slate-500', stats.total, 'docs')}
              {statChip('border-emerald-500/25 text-emerald-300', 'bg-emerald-400', `${stats.eligible}/${stats.total}`, 'eligible')}
              {stats.excluded > 0 && statChip('border-rose-500/25 text-rose-300', 'bg-rose-400', stats.excluded, 'excluded')}
              {stats.unassessed > 0 && statChip('border-slate-500/30 text-slate-400', 'bg-slate-400', stats.unassessed, 'unassessed')}
              {statChip('border-blue-500/25 text-blue-300', 'bg-blue-400', stats.derived, 'PRDs')}
              {statChip('border-emerald-500/25 text-emerald-300', 'bg-emerald-400', stats.approved, 'approved')}
              {statChip('border-sky-500/25 text-sky-300', 'bg-sky-400', stats.open, 'open tickets')}
              {stats.stale
                ? statChip('border-rose-500/30 bg-rose-500/5 text-rose-300', 'bg-rose-400', stats.stale, 'stale')
                : statChip('border-borderDark/50 text-slate-500', 'bg-slate-600', 0, 'stale')}
            </div>
            <span className="text-[10px] text-slate-600">Actions write the same files the agent would, then commit.</span>
          </div>

          {data.items.length === 0 && (
            <div className="px-6 py-10 text-center">
              <FileText className="w-8 h-8 mx-auto mb-2 text-slate-600" />
              <p className="text-sm text-slate-400 font-medium">No documents yet</p>
              <p className="text-xs text-slate-600 mt-1">Create a doc in .devtop/docs to see its chain here.</p>
            </div>
          )}

          {groups.map(g => {
            const sectionLabel = SECTION_LABEL[g.section]
            const firstOfSection = g === groups.find(x => x.section === g.section)
            return (
              <div key={g.section + '/' + g.dir}>
                {firstOfSection && (
                  <button
                    onClick={() => toggle(g.section)}
                    className={`w-full flex items-center gap-3 px-6 py-2.5 border-b border-borderDark/30 bg-bgDark/20 hover:bg-bgDark/40 transition-colors ${SECTION_TEXT[g.section]}`}
                  >
                    <span className={`w-1 h-4 rounded-full ${SECTION_DOT[g.section]}`}></span>
                    <span className="text-[10px] font-semibold uppercase tracking-widest">{sectionLabel}</span>
                    <span className="font-mono text-[10px] text-slate-500 bg-borderDark/30 border border-borderDark/40 rounded-md px-1.5 py-0.5">{g.items.length}</span>
                    <svg className={`w-3.5 h-3.5 ml-auto text-slate-500`} fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d={sectionOpen(g.section) ? 'M19 9l-7 7-7-7' : 'M9 5l7 7-7 7'} />
                    </svg>
                  </button>
                )}
                {sectionOpen(g.section) && (
                  <>
                    {g.dir !== '' && (
                      <button
                        onClick={() => toggle(g.section + '/' + g.dir)}
                        className="w-full flex items-center gap-2 px-6 py-1.5 text-left text-slate-500 hover:text-slate-300 transition-colors"
                      >
                        <svg className="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.8" d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V7z" /></svg>
                        <span className="text-[9px] font-mono font-semibold uppercase tracking-widest">{g.dir}/</span>
                        <span className="text-[9px] font-mono text-slate-600">· {g.items.length}</span>
                        <svg className="w-3 h-3 ml-auto" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d={dirOpen(g.section, g.dir) ? 'M19 9l-7 7-7-7' : 'M9 5l7 7-7 7'} />
                        </svg>
                      </button>
                    )}
                    {dirOpen(g.section, g.dir) && g.items.map(item => {
                      const prd = item.prd
                      const crumbs = item.path.replace(/\.mdx?$/, '').split('/')
                      const leaf = crumbs.pop() ?? item.title
                      return (
                        <div key={item.doc_id} className="border-b border-borderDark/30 last:border-b-0 px-6 py-5 fade-in">
                          <div className="flex items-center gap-2.5 min-w-0">
                            <span className="flex items-center justify-center w-7 h-7 rounded-lg bg-borderDark/40 text-slate-300 flex-shrink-0"><FileText className="w-3.5 h-3.5" /></span>
                            <div className="min-w-0">
                              <div className="flex items-center gap-2 text-sm font-medium text-slate-100 truncate">
                                {crumbs.map(c => (
                                  <span key={c} className="flex items-center gap-2 min-w-0">
                                    <span className="text-slate-500">{titleCase(c)}</span>
                                    <span className="text-slate-600">›</span>
                                  </span>
                                ))}
                                <span className="text-slate-100">{titleCase(leaf)}</span>
                                {item.stale && prd && (
                                  <span className="inline-flex items-center px-1.5 py-px rounded text-[9px] font-semibold uppercase tracking-wider bg-rose-500/10 border border-rose-500/30 text-rose-400">stale</span>
                                )}
                              </div>
                              <p className="text-[10px] font-mono text-slate-600 truncate mt-0.5">.devtop/docs/{item.path}</p>
                            </div>
                          </div>

                          <div className="stage-grid gap-2 mt-4">
                            <div className="rounded-xl border border-borderDark/40 bg-surfaceDark/60 p-4 h-full flex flex-col">
                              <div className="flex items-center justify-between gap-2 mb-1.5">
                                <div className="text-[9px] uppercase tracking-widest font-semibold text-slate-500">Document</div>
                                <span className="flex items-center gap-1.5">{prospectChip(item)}
                                  {openLink(`#/docs/${item.slug}`, 'doc')}
                                </span>
                              </div>
                              {item.summary
                                ? <>
                                    <div className="text-[9px] font-semibold uppercase tracking-widest text-accentBlue">tl;dr</div>
                                    <p className="text-xs text-slate-300 leading-6 mt-1">{item.summary}</p>
                                  </>
                                : <p className="text-xs text-slate-600">No summary yet. Derive a PRD to generate one.</p>}
                              <div className="mt-auto">{prospectActions(item)}</div>
                            </div>

                            <div className="flex items-center justify-center text-xs text-slate-600 select-none">→</div>

                            {!prd ? (
                              <div className={`rounded-xl border border-dashed p-4 h-full flex flex-col ${item.prospect === 'not-eligible' ? 'border-rose-500/20 bg-rose-500/5' : item.prospect === 'unassessed' || !item.prospect ? 'border-borderDark/70 bg-bgDark/40' : 'border-borderDark/70 bg-bgDark/40'}`}>
                                <div className="text-[9px] uppercase tracking-widest font-semibold text-slate-500">PRD</div>
                                {item.prospect === 'not-eligible'
                                  ? <>
                                      <p className="text-xs text-rose-300/80">Excluded — {item.prospect_by === 'user' ? 'user' : 'suggested'} verdict.</p>
                                      <p className="text-[10px] text-slate-600 mt-1">Frontmatter: <span className="font-mono">prds: not-eligible</span>.</p>
                                    </>
                                  : !item.prospect
                                    ? <>
                                        <p className="text-xs text-slate-600">Not assessed yet.</p>
                                        <p className="text-[10px] text-slate-700 mt-1">Suggest eligibility to qualify this doc.</p>
                                      </>
                                    : <>
                                        <p className="text-xs text-slate-600">Not derived yet.</p>
                                        <p className="text-[10px] text-slate-700 mt-1">Derive a draft, review it, approve it.</p>
                                      </>}
                                <div className="mt-auto pt-2">{prdActions(item)}</div>
                              </div>
                            ) : (
                              <div className="rounded-xl border border-borderDark/40 bg-surfaceDark/60 p-4 h-full flex flex-col">
                                <div className="flex items-center justify-between">
                                  <div className="text-[9px] uppercase tracking-widest font-semibold text-slate-500">PRD</div>
                                  {openLink(`#/prds/${prd.slug}`, 'PRD')}
                                </div>
                                <div className="flex items-center gap-2 flex-wrap mt-1">{chip(PRD_STYLES[prd.status] ?? PRD_STYLES.draft, prd.status)}</div>
                                <p className="text-[11px] text-slate-400 mt-2">{prd.reqs} requirements</p>
                                <p className="text-[10px] font-mono text-slate-600 truncate mt-0.5">prds/{prd.slug}/index.mdx</p>
                                <div className="mt-auto pt-2">{prdActions(item)}</div>
                              </div>
                            )}

                            <div className="flex items-center justify-center text-xs text-slate-600 select-none">→</div>

                            <div className="rounded-xl border border-borderDark/40 bg-surfaceDark/60 p-4 h-full flex flex-col">
                              <div className="flex items-center justify-between">
                                <div className="text-[9px] uppercase tracking-widest font-semibold text-slate-500">Tickets{prd ? ` · ${item.tickets.length}` : ''}</div>
                                {item.tickets.length > 0 && openLink('#/tickets', 'tickets')}
                              </div>
                              {!prd ? (
                                <p className="text-xs text-slate-600">Locked until the PRD is approved.</p>
                              ) : prd.status !== 'approved' ? (
                                <div>
                                  <p className="text-xs text-slate-600">Awaiting approval.</p>
                                  <p className="text-[10px] text-slate-700 mt-1">Gate: <span className="font-mono">prds.status == approved</span></p>
                                </div>
                              ) : item.tickets.length === 0 ? (
                                <p className="text-xs text-slate-600">No tickets yet.</p>
                              ) : (
                                <div className="max-h-36 overflow-y-auto -mr-2 pr-2 space-y-1.5">
                                  {item.tickets.map(t => (
                                    <div key={t.id} className={`flex items-center gap-2.5 rounded-lg border px-2.5 py-1.5 min-w-0 ${t.status === 'done' ? 'border-borderDark/30 bg-transparent opacity-70' : 'border-borderDark/60 bg-bgDark/40'}`}>
                                      <span className="font-mono text-[10px] text-slate-600 flex-shrink-0">{t.id}</span>
                                      <a href={`#/tickets/${t.id}`} key={t.id} className={`text-[11px] truncate flex-1 min-w-0 hover:underline ${t.status === 'done' ? 'text-slate-600 line-through' : 'text-slate-200'}`}>{t.title}</a>
                                      {chip(TKT_STYLES[t.status] ?? TKT_STYLES.open, t.status)}
                                    </div>
                                  ))}
                                </div>
                              )}
                              <div className="mt-auto pt-2">{ticketActions(item)}</div>
                            </div>
                          </div>
                        </div>
                      )
                    })}
                  </>
                )}
              </div>
            )
          })}
        </div>

        <div className="flex items-start gap-2 mt-4 text-[10px] text-slate-600">
          <Info className="w-3 h-3 mt-0.5 flex-shrink-0" />
          <p>Derive, verdict, and status actions are user-initiated generation. Eligibility and the ticket gate are enforced by the server before any model runs; lifecycle stays external.</p>
        </div>
      </div>
    </div>
  )
}
