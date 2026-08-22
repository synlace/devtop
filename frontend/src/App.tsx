import { useState, useEffect, useMemo, useCallback, useRef, Fragment, memo } from 'react'
import RichMarkdown from './RichMarkdown'
import { 
  FileText, 
  ClipboardList, 
  ArrowLeft, 
  Maximize2, 
  Minimize2,
  ChevronRight,
  ChevronDown,
  Sparkles,
  Check,
  MessageSquare,
  Plus,
  Trash2,
  Key,
  Settings,
  X,
  Palette,
  Database,
  Info,
  History,
  MoreVertical,
  Star,
  GitBranch,
  PanelRightClose,
  PanelRightOpen
} from 'lucide-react'
import { CopilotKit, CopilotChat } from '@copilotkit/react-core/v2'
import { useAgentContext } from '@copilotkit/react-core/v2'
import { WildcardToolCallRender } from '@copilotkit/react-core/v2'
import { DiffView, DiffModeEnum } from '@git-diff-view/react'
import '@git-diff-view/react/styles/diff-view-pure.css'
import { toolCallRenderers } from './ToolCalls'
import DocActionsMenu from './DocActionsMenu'
import PipelineView from './PipelineView'
import AddRepoModal from './AddRepoModal'
import { api, setActiveRepo, type RepoStatus } from './api'
import type { DocMenuAnchor, DocExportFormat } from './DocActionsMenu'
import './App.css'

// No auto-greeting in the chat. Kept referentially stable so the memoized chat
// below never re-renders (and never re-triggers the scroll-to-bottom) when the
// app re-renders for unrelated state (e.g. toggling the thread list).
const CHAT_LABELS = { welcomeMessageText: '' }
const MemoizedCopilotChat = memo(CopilotChat)

interface DocSlug {
  slug: string
  title: string
}

interface DocTreeNode {
  name: string
  slug?: string
  title?: string
  children: DocTreeNode[]
}

interface Ticket {
  id: string
  title: string
  status: string
  priority: string
  assignee: string
  claimed_by?: string
  created: string
  description?: string
  raw_description?: string
  comments?: Array<{
    date: string
    author: string
    text: string
  }>
}

// One git commit that touched the current doc/ticket, newest first.
interface DocRevision {
  sha: string
  short: string
  message: string
  author: string
  date: string
  is_current?: boolean
}

// A generic artifact row from the engine's /api/artifacts/<kind> endpoints —
// id + title plus whatever frontmatter the kind declares.
interface ArtifactItem {
  id: string
  title: string
  status?: string
  [key: string]: unknown
}

interface ThreadSummary {
  id: string
  context: string
  title: string
  created_at: string
  updated_at: string
  message_count: number
  preview: string
}

// Engine config — the repo-declared artifact kinds, served by /api/engine-config.
// The engine renders kinds generically; the shipped default is docs + tickets.
interface EngineNav {
  label: string
  icon: string
  order: number
  view: string
}

interface ArtifactKindDef {
  path: string
  extension: string
  agent_writable: boolean
  view: string
  nav?: EngineNav
  requires_approval?: boolean
  schema?: Record<string, unknown>
}

interface EngineConfig {
  artifact_kinds: Record<string, ArtifactKindDef>
  derivation?: unknown[]
  replan?: { detect?: string; stale_badge?: boolean }
  prompts?: Record<string, unknown>
  handoff?: { contract?: string; grabbable?: string[]; lifecycle_owner?: string }
  pipeline?: { nav?: EngineNav }
}

// Generic route location: a config-declared artifact kind plus an optional id.
// The hash routes are already shaped like #/<kind>/<id> (e.g. #/docs/<slug>,
// #/tickets/<id>); the internal page model just catches up.
interface PageLocation {
  kind: string
  id?: string
}

// Built-in fallback, identical to the bundled default the backend serves. Used
// until /api/engine-config answers (or when it fails, e.g. hermetic tests), so
// nav rendering is deterministic regardless of backend availability.
const BUILTIN_ENGINE_CONFIG: EngineConfig = {
  artifact_kinds: {
    intents: {
      path: 'intents',
      extension: '.mdx',
      agent_writable: false,
      requires_approval: true,
      view: 'list',
    },
    bugs: {
      path: 'bugs',
      extension: '.mdx',
      agent_writable: false,
      requires_approval: true,
      view: 'list',
    },
    spikes: {
      path: 'spikes',
      extension: '.mdx',
      agent_writable: false,
      requires_approval: true,
      view: 'list',
    },
    rfcs: {
      path: 'rfcs',
      extension: '.mdx',
      agent_writable: false,
      requires_approval: true,
      view: 'list',
    },
    chores: {
      path: 'chores',
      extension: '.mdx',
      agent_writable: false,
      requires_approval: true,
      view: 'list',
    },
    documentation: {
      path: 'documentation',
      extension: '.mdx',
      agent_writable: true,
      requires_approval: true,
      view: 'list',
      nav: { label: 'Docs', icon: 'file', order: 1, view: 'tree' },
    },
    requirements: {
      path: 'requirements',
      extension: '.mdx',
      agent_writable: true,
      requires_approval: true,
      view: 'list',
    },
    decisions: {
      path: 'decisions',
      extension: '.mdx',
      agent_writable: true,
      requires_approval: true,
      view: 'list',
    },
    open_questions: {
      path: 'open_questions',
      extension: '.mdx',
      agent_writable: true,
      requires_approval: true,
      view: 'list',
    },
    tickets: {
      path: 'tickets',
      extension: '.md',
      agent_writable: true,
      view: 'board',
      nav: { label: 'Tickets', icon: 'board', order: 3, view: 'board' },
    },
  },
  derivation: [
    { from: 'intents', to: 'documentation', transform: 'describe_feature', chain: 'intents' },
    { from: 'documentation', to: 'requirements', transform: 'derive_requirements', chain: 'intents' },
    { from: 'documentation', to: 'decisions', transform: 'derive_decisions', chain: 'intents' },
    { from: 'documentation', to: 'open_questions', transform: 'derive_open_questions', chain: 'intents' },
    { from: 'requirements', to: 'tickets', transform: 'derive_tickets', chain: 'intents', gate: 'requirements.review == approved' },
    { from: 'bugs', to: 'documentation', transform: 'describe_change_record', chain: 'bugs' },
    { from: 'documentation', to: 'decisions', transform: 'derive_fix_direction', chain: 'bugs' },
    { from: 'decisions', to: 'tickets', transform: 'derive_fix_tickets', chain: 'bugs', gate: 'decisions.review == approved' },
    { from: 'spikes', to: 'documentation', transform: 'describe_findings', chain: 'spikes' },
    { from: 'documentation', to: 'decisions', transform: 'derive_recommendation', chain: 'spikes' },
    { from: 'documentation', to: 'open_questions', transform: 'derive_open_questions', chain: 'spikes' },
    { from: 'rfcs', to: 'documentation', transform: 'describe_design', chain: 'rfcs' },
    { from: 'documentation', to: 'decisions', transform: 'derive_proposal_decisions', chain: 'rfcs' },
    { from: 'decisions', to: 'tickets', transform: 'derive_proposal_tickets', chain: 'rfcs', gate: 'decisions.review == approved' },
    { from: 'chores', to: 'tickets', transform: 'derive_chore_tickets', chain: 'chores', gate: 'chores.review == approved' },
  ],
  replan: { detect: 'git_diff', stale_badge: true },
  pipeline: { nav: { label: 'Work items', icon: 'flow', order: 2, view: 'pipeline' } },
  handoff: { contract: 'intents|bugs|spikes|rfcs|chores/*.mdx + each derived artifact work_item/review + this config', grabbable: [], lifecycle_owner: 'external' },
}

// Provider presets shown in the AI config wizard. LM Studio is keyless (the
// runtime still wants a non-empty key, so "lm-studio" is sent as a sentinel).
const AI_PROVIDERS = {
  openrouter: { label: 'OpenRouter', baseURL: 'https://openrouter.ai/api/v1', model: 'deepseek/deepseek-v4-flash-0731' },
  lmstudio: { label: 'LM Studio (local)', baseURL: 'http://localhost:1234/v1', model: 'lmstudio-community/llama-3.2-3b-instruct' },
  custom: { label: 'Custom (OpenAI-compatible)', baseURL: '', model: '' },
} as const
type AIProviderKey = keyof typeof AI_PROVIDERS

// Sections in the settings dialog (left rail). AI is the only populated pane
// today; the others are placeholders until they gain real controls.
type SettingsPane = 'ai' | 'appearance' | 'data' | 'about'

// Repo status badges for the switcher and the Repos overview.
const REPO_STATUS_META: Record<string, { dot: string; label: string }> = {
  ready:  { dot: 'bg-emerald-400', label: 'ready' },
  dirty:  { dot: 'bg-amber-400',  label: 'uncommitted' },
  uninit: { dot: 'bg-slate-500',   label: 'needs init' },
  nogit:  { dot: 'bg-rose-400',    label: 'no git repo' },
}
type AiStatus = { configured: boolean; remembered: boolean; baseURL?: string; model?: string } | null

function App() {
  // Routing / UI State
  const [activePage, setActivePage] = useState<PageLocation>({ kind: 'docs', id: 'index' })
  const [docTitle, setDocTitle] = useState<string>('Home')
  const [docContent, setDocContent] = useState<string>('')
  const [docMissing, setDocMissing] = useState<boolean>(false)
  // PRD detail page: the live status and any docked status-action error, so
  // Approve / Request changes live next to the content being reviewed.
  const [prdStatus, setPrdStatus] = useState<string | null>(null)
  const [prdStatusErr, setPrdStatusErr] = useState<string | null>(null)
  const [docSlugs, setDocSlugs] = useState<DocSlug[]>([])
  const [collapsedSections, setCollapsedSections] = useState<Set<string>>(new Set())
  // Favourites (user-scoped, never committed): slugs the user starred via the
  // doc ⋯ menu. Persisted through GET/PUT /api/favourites; the backend drops
  // slugs whose doc has disappeared, so a refresh stays canonical.
  const [favourites, setFavourites] = useState<string[]>([])
  // Open doc action menu ("⋯"); null when closed. Anchor is viewport px.
  const [menuAnchor, setMenuAnchor] = useState<DocMenuAnchor | null>(null)
  // Pending "open the revision rail" for a nav row's ⋯ → clock; resolved once
  // the navigated-to doc becomes the active page.
  const [historyIntent, setHistoryIntent] = useState<string | null>(null)
  
  const [tickets, setTickets] = useState<Ticket[]>([])
  const [activeTicket, setActiveTicket] = useState<Ticket | null>(null)
  // Generic kind → artifact rows for list-view kinds (e.g. PRDs).
  const [artifactLists, setArtifactLists] = useState<Record<string, ArtifactItem[]>>({})
  
  // Chat Panel Resizing & Layout
  const readChatState = (): { width: number; collapsed: boolean } => {
    try {
      const v = JSON.parse(window.localStorage.getItem('devtop.chat.panel') ?? '{}') as Partial<{ width: number; collapsed: boolean }>
      return { width: typeof v.width === 'number' && v.width >= 200 ? v.width : 384, collapsed: v.collapsed === true }
    } catch { return { width: 384, collapsed: false } }
  }
  const CHAT_STATE = readChatState()
  const [chatWidth, setChatWidth] = useState<number>(CHAT_STATE.width)
  const [chatCollapsed, setChatCollapsed] = useState(CHAT_STATE.collapsed)

  useEffect(() => {
    window.localStorage.setItem('devtop.chat.panel', JSON.stringify({ width: chatWidth, collapsed: chatCollapsed }))
  }, [chatWidth, chatCollapsed])
  const [isFullscreen, setIsFullscreen] = useState<boolean>(false)
  
  // Thread State
  const [threads, setThreads] = useState<ThreadSummary[]>([])
  const [activeThreadId, setActiveThreadId] = useState<string | undefined>(undefined)
  const [showThreadList, setShowThreadList] = useState<boolean>(false)
  // Git-revision history rail. Visible state and the selected revision are
  // per-context (persisted with viewstate); the revision list and the
  // content/diff of the selected revision are fetched on demand.
  const [historyOpen, setHistoryOpen] = useState<boolean>(false)
  const [revisions, setRevisions] = useState<DocRevision[]>([])
  const [historyIdx, setHistoryIdx] = useState<number>(-1)  // -1 = current working copy
  const [historyAt, setHistoryAt] = useState<{ title: string; content: string; deleted: boolean } | null>(null)
  const [historyDiff, setHistoryDiff] = useState<string>('')
  const contextThreadState = useRef<Record<string, {activeThreadId?: string; showThreadList: boolean; historyOpen?: boolean}>>({})
  const prevContextKey = useRef<string>('')
  const [viewStateLoaded, setViewStateLoaded] = useState<boolean>(false)

  // AI key state — the key is entered through the UI only and lives in the
  // CopilotKit runtime (optionally persisted to a mounted volume).
  const [aiStatus, setAiStatus] = useState<{ configured: boolean; remembered: boolean; baseURL?: string; model?: string } | null>(null)
  // true once /ai-status answered (runtime reachable); false when it failed
  // (runtime down — e.g. hermetic tests) or while still unknown (null).
  const [aiReachable, setAiReachable] = useState<boolean | null>(null)
  const [showSettings, setShowSettings] = useState<boolean>(false)
  const [settingsPane, setSettingsPane] = useState<SettingsPane>('ai')
  const [aiKeyInput, setAiKeyInput] = useState<string>('')
  const [aiProvider, setAiProvider] = useState<AIProviderKey>('openrouter')
  const [aiBaseURLInput, setAiBaseURLInput] = useState<string>(AI_PROVIDERS.openrouter.baseURL)
  const [aiModelInput, setAiModelInput] = useState<string>(AI_PROVIDERS.openrouter.model)
  const [aiSaving, setAiSaving] = useState<boolean>(false)
  // Repo-declared artifact kinds; replaced by the backend config when available.
  const [engineConfig, setEngineConfig] = useState<EngineConfig>(BUILTIN_ENGINE_CONFIG)
  // Repo scope: the registry as served by /api/repos. Every API call is
  // scoped through api() to the active repo's name; single-repo mode has one
  // entry with single=true and no switcher.
  const [repos, setRepos] = useState<RepoStatus[]>([])
  const [activeRepo, setActiveRepoState] = useState<string>('')
  const [repoInitBusy, setRepoInitBusy] = useState(false)
  const [repoDropdownOpen, setRepoDropdownOpen] = useState(false)
  const [showAddRepo, setShowAddRepo] = useState(false)
  const [repoRemoveId, setRepoRemoveId] = useState<string | null>(null)
  const [repoRemoveError, setRepoRemoveError] = useState('')
  const activeRepoStatus: RepoStatus | undefined = activeRepo
    ? repos.find(r => r.name === activeRepo)
    : repos.length > 0 ? repos[0] : undefined
  // CopilotKit is mounted only for an initialized repo: its runtime resolves
  // /info with zero agents for a repo without a deployed agent, and
  // useAgentContext (PageContextProvider) then throws "Agent 'default' not
  // found after runtime sync". While the repo state is unknown the mount is
  // deferred; the panel body shows a notice until then.
  const repoUninitialized = repos.length > 0 && activeRepoStatus !== undefined && !activeRepoStatus.initialized
  const repoBooting = repos.length > 0 && activeRepoStatus === undefined
  const isMultiRepo = repos.some(r => !r.single) || repos.length > 1

  useEffect(() => {
    let cancelled = false
    fetch('/api/copilotkit/ai-status')
      .then(r => r.json().catch(() => null))
      .then(s => { if (!cancelled) { setAiStatus(s); setAiReachable(true) } })
      .catch(() => { if (!cancelled) setAiReachable(false) })
    return () => { cancelled = true }
  }, [])

  // When the runtime is reachable but no AI key is configured, open Settings
  // focused on the AI/provider pane so setup is front-and-center. Runs once per
  // mount, so dismissing it (or configuring a key) leaves it closed until the
  // next app launch.
  const settingsAutoOpenedRef = useRef(false)
  useEffect(() => {
    if (aiReachable === true && aiStatus && !aiStatus.configured && !settingsAutoOpenedRef.current) {
      settingsAutoOpenedRef.current = true
      setSettingsPane('ai')
      setShowSettings(true)
    }
  }, [aiReachable, aiStatus])

  // Repo registry on mount; multi-repo mode scopes all API calls through api().
  useEffect(() => {
    let cancelled = false
    fetch('/api/repos')
      .then(r => r.ok ? r.json() : Promise.reject(new Error('repos ' + r.status)))
      .then((list: RepoStatus[]) => {
        if (cancelled || !Array.isArray(list) || list.length === 0) return
        setRepos(list)
        // Always resolve an active repo, single-repo mode included: the
        // classic entry is a registered repo like any other, and its name
        // scopes threads, viewstate, and API calls through one code path —
        // no empty-context special case to drift apart.
        const saved = localStorage.getItem('devtop.activeRepo')
        const next = saved && list.some(r => r.name === saved) ? saved : list[0].name
        setActiveRepoState(next)
        setActiveRepo(next)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    let cancelled = false
    fetch(api('/api/engine-config'))
      .then(r => r.ok ? r.json() : Promise.reject(new Error(`engine-config ${r.status}`)))
      .then((cfg: EngineConfig) => {
        if (!cancelled && cfg && cfg.artifact_kinds && Object.keys(cfg.artifact_kinds).length > 0) {
          setEngineConfig(cfg)
        }
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [activeRepo])

  const isReposPage = activePage.kind === 'repos'
  const showUninitState = isMultiRepo && activeRepoStatus?.status === 'uninit' && !isReposPage && !showSettings

  const refreshRepos = useCallback(async () => {
    try {
      const r = await fetch('/api/repos')
      if (r.ok) {
        const list = await r.json()
        if (Array.isArray(list) && list.length > 0) setRepos(list)
      }
    } catch { /* ignore */ }
  }, [])

  const selectRepo = useCallback((name: string) => {
    setActiveRepoState(name)
    setActiveRepo(name)
    localStorage.setItem('devtop.activeRepo', name)
    setRepoDropdownOpen(false)
    navigateTo('/')
  }, [])

  const removeRepo = useCallback(async (name: string) => {
    setRepoRemoveId(null)
    try {
      const r = await fetch(`/api/repos/${encodeURIComponent(name)}`, { method: 'DELETE' })
      if (!r.ok) {
        const data = await r.json().catch(() => null)
        throw new Error(data && data.error ? String(data.error) : `Failed to remove (${r.status})`)
      }
      await refreshRepos()
      // If the active repo was removed, fall back to the first remaining.
      if (activeRepo === name && repos.length > 1) {
        selectRepo(repos.find(x => x.name !== name)?.name ?? '')
      }
    } catch (e) {
      // Surface the refusal (e.g. removing the last repo) without a dialog.
      setRepoRemoveError(String(e instanceof Error ? e.message : e))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeRepo, repos, refreshRepos, selectRepo])

  const initActiveRepo = useCallback(async () => {
    if (!activeRepo) return
    setRepoInitBusy(true)
    try {
      const r = await fetch(api('/api/repos/init'), { method: 'POST' })
      if (r.ok) await refreshRepos()
    } finally {
      setRepoInitBusy(false)
    }
    routeTo(window.location.hash.replace('#', '') || '/')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeRepo, refreshRepos])

  // Re-fetch per-repo data when the active repo changes.
  useEffect(() => {
    if (!activeRepo) return
    fetchDocSlugs()
    fetchFavourites()
    fetchTicketsList()
    routeTo(window.location.hash.replace('#', '') || '/')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeRepo])

  const onAiProviderChange = (provider: AIProviderKey) => {
    setAiProvider(provider)
    const preset = AI_PROVIDERS[provider]
    setAiBaseURLInput(preset.baseURL)
    setAiModelInput(preset.model)
    if (provider === 'lmstudio') setAiKeyInput('lm-studio')
  }

  const saveAiKey = async () => {
    const key = aiKeyInput.trim()
    if (!key || aiSaving) return
    setAiSaving(true)
    try {
      const r = await fetch('/api/copilotkit/ai-key', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          key,
          baseURL: aiBaseURLInput.trim() || undefined,
          model: aiModelInput.trim() || undefined,
        }),
      })
      if (r.ok) {
        setAiStatus(await r.json())
        setAiReachable(true)
        setAiKeyInput('')
        setShowSettings(false)
      }
    } finally {
      setAiSaving(false)
    }
  }

  const clearAiKey = async () => {
    try {
      const r = await fetch('/api/copilotkit/ai-key', { method: 'DELETE' })
      if (r.ok) { setAiStatus(await r.json()); setAiReachable(true) }
    } catch { /* ignore */ }
  }

  // -------------------------------------------------------------
  // 2. Doc Tree Builder
  // -------------------------------------------------------------
  function buildDocTree(slugs: DocSlug[]): DocTreeNode[] {
    const root: DocTreeNode[] = []
    const sorted = [...slugs].sort((a, b) => a.slug.localeCompare(b.slug))
    for (const doc of sorted) {
      const parts = doc.slug.split('/')
      let level = root
      for (let i = 0; i < parts.length; i++) {
        const isLast = i === parts.length - 1
        const existing = level.find(n => n.name === parts[i])
        if (existing) {
          level = existing.children
        } else if (isLast) {
          level.push({ name: parts[i], slug: doc.slug, title: doc.title, children: [] })
        } else {
          const node: DocTreeNode = { name: parts[i], children: [] }
          level.push(node)
          level = node.children
        }
      }
    }
    return root
  }

  function toggleSection(name: string) {
    setCollapsedSections(prev => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  // -------------------------------------------------------------
  // 3. Initial Boostrap & Navigation Routing
  // -------------------------------------------------------------
  useEffect(() => {
    // Handle manual hash navigation
    const handleHashChange = () => {
      const hash = window.location.hash || '#/'
      const path = hash.startsWith('#') ? hash.slice(1) : hash
      routeTo(path)
    }

    window.addEventListener('hashchange', handleHashChange)
    
    // Initial load
    fetchDocSlugs()
    fetchFavourites()
    fetchTicketsList()
    handleHashChange()

    return () => {
      window.removeEventListener('hashchange', handleHashChange)
    }
  }, [])

  // Agent writes happen server-side (chat tools, derivation): nothing in the
  // UI knows when docs/tickets/PRDs changed. Poll the workspace revision
  // counter per repo and refetch nav data only when it moves. Cheap, covers
  // every writer, and never fires while the tab is hidden.
  const navRevisionRef = useRef(-1)
  const [navRevision, setNavRevision] = useState(0)
  useEffect(() => {
    navRevisionRef.current = -1
    const tick = async () => {
      if (document.hidden) return
      try {
        const r = await fetch(api('/api/workspace/revision'))
        if (!r.ok) return
        const data = await r.json()
        const rev = Number(data.revision)
        if (navRevisionRef.current === -1) {
          navRevisionRef.current = rev
          return
        }
        if (rev !== navRevisionRef.current) {
          navRevisionRef.current = rev
          setNavRevision(rev)
          fetchDocSlugs()
          fetchFavourites()
          fetchTicketsList()
        }
      } catch {
        // server unavailable (e.g. dev HMR restart): retry on the next tick
      }
    }
    void tick()
    const timer = setInterval(tick, 4000)
    return () => clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeRepo])

  // Render Mermaid diagrams when content changes
  useEffect(() => {
    const renderMermaid = async () => {
      // @ts-ignore
      const mermaid = window.mermaid
      if (!mermaid) return

      // Initialize mermaid
      mermaid.initialize({
        startOnLoad: false,
        theme: 'dark',
        securityLevel: 'loose',
        themeVariables: {
          background: '#0c101f',
          primaryColor: '#3b82f6',
          primaryTextColor: '#f8fafc',
          primaryBorderColor: '#1e293b',
          lineColor: '#64748b',
          secondaryColor: '#1e293b',
          tertiaryColor: '#070a13'
        }
      })

      // Find all code blocks with mermaid language
      const blocks = document.querySelectorAll('pre code.language-mermaid, pre.codehilite code.language-mermaid, pre.mermaid code, pre.mermaid')
      if (blocks.length === 0) return

      blocks.forEach((el, index) => {
        const code = el.textContent?.trim()
        if (!code) return

        const id = 'mermaid-svg-' + index + '-' + Date.now()
        const wrapper = document.createElement('div')
        wrapper.className = 'mermaid my-6 flex justify-center bg-surfaceDark/30 p-4 rounded-xl border border-borderDark/40'
        wrapper.id = id
        wrapper.textContent = code

        const target = el.closest('pre') || el
        target.replaceWith(wrapper)
      })

      try {
        await mermaid.run({
          nodes: document.querySelectorAll('.mermaid')
        })
      } catch (e) {
        console.error('Mermaid render error:', e)
      }
    }

    // Give React a small tick to mount the newly set HTML, then run
    const timer = setTimeout(() => {
      renderMermaid()
    }, 100)

    return () => clearTimeout(timer)
  }, [docContent, activeTicket])

  // Derived page predicates — the engine's view dispatch, keyed by kind.
  const activeKindDef = engineConfig.artifact_kinds[activePage.kind]
  const isHomePage = activePage.kind === 'docs' && (activePage.id === undefined || activePage.id === 'index')
  const isDocPage = activePage.kind === 'docs'
  const isTicketBoardPage = activePage.kind === 'tickets' && activePage.id === undefined
  const isTicketDetailPage = activePage.kind === 'tickets' && !!activePage.id
  // Generic views: a "list" kind's overview page, and any kind's detail page
  // (document view) unless it's a board kind with an id (ticket detail).
  const isListOverviewPage = !!activeKindDef && activeKindDef.view === 'list' && !activePage.id
  const isPipelinePage = activePage.kind === 'pipeline' && !activePage.id
  const isDocumentView = isDocPage || (!!activePage.id && activeKindDef?.view !== 'board')
  const activeKindLabel = activeKindDef?.nav?.label || activePage.kind

  // Context computed labels for instructions
  const contextLabel = useMemo(() => {
    if (activePage.kind === 'docs') {
      if (isHomePage) return 'Home (Index)'
      return activePage.id!.split('/').map(p => p.replace(/-/g, ' ').replace(/\b\w/g, c => c.toUpperCase())).join(' / ')
    }
    if (activePage.kind === 'tickets') {
      if (activePage.id) return 'dk-' + activePage.id
      return 'Ticket Board'
    }
    if (activeKindDef) {
      return activeKindLabel + (activePage.id ? ' / ' + activePage.id : '')
    }
    return activePage.kind
  }, [activePage, isHomePage, activeKindDef, activeKindLabel])

  // Generic context key: "<kind>" for a kind's overview, "<kind>/<id>" for an
  // item. Threads and viewstate are scoped per repo, never per page:
  // navigating between docs/kinds must not switch the chat. The active repo
  // name is the scope, single-repo mode included — the synthetic entry is a
  // registered repo like any other.
  const contextKey = activeRepo || ''

  const breadcrumbItems = useMemo(() => {
    if (activePage.kind === 'repos') {
      return [{ label: 'Repositories', href: '#/repos' }]
    }
    if (activePage.kind === 'docs') {
      const slug = activePage.id ?? 'index'
      if (slug === 'index') {
        return [{ label: 'Home', href: '#/' }]
      }
      const parts = slug.split('/')
      return parts.map((part, i) => {
        const isLast = i === parts.length - 1
        const label = isLast ? (docTitle || part.replace(/-/g, ' ').replace(/\b\w/g, c => c.toUpperCase())) : part.replace(/-/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
        if (isLast) {
          return { label, href: `#/docs/${slug}` }
        }
        return { label, href: `#/docs/${parts.slice(0, i + 1).join('/')}/index` }
      })
    }
    if (activePage.kind === 'tickets') {
      if (activePage.id) {
        return [
          { label: 'Tickets', href: '#/tickets' },
          { label: `dk-${activePage.id}`, href: `#/tickets/${activePage.id}` },
        ]
      }
      return [{ label: 'Tickets', href: '#/tickets' }]
    }
    if (activeKindDef) {
      if (activePage.id) {
        return [
          { label: activeKindLabel, href: `#/${activePage.kind}` },
          { label: docTitle || activePage.id, href: `#/${activePage.kind}/${activePage.id}` },
        ]
      }
      return [{ label: activeKindLabel, href: `#/${activePage.kind}` }]
    }
    return []
  }, [activePage, docTitle, activeKindDef, activeKindLabel])

  // -------------------------------------------------------------
  // 4. Navigation Actions
  // -------------------------------------------------------------
  const navigateTo = (path: string) => {
    window.location.hash = path
  }

  const routeTo = async (path: string) => {
    const p = path.replace(/^\/+/, '').replace(/\/+$/, '')
    // "/" (and bare "/docs") is the home page — the index doc.
    if (!p || p === 'docs') {
      setActivePage({ kind: 'docs', id: 'index' })
      await fetchDocPage('index')
      return
    }
    const [kind, ...rest] = p.split('/')
    const id = rest.length ? rest.join('/') : undefined
    setActivePage({ kind, id })
    const kindDef = engineConfig.artifact_kinds[kind]
    if (kind === 'docs') {
      await fetchDocPage(id ?? 'index')
    } else if (kind === 'tickets') {
      if (id) await fetchTicketDetail(id)
      else await fetchTicketsList()
    } else if (kindDef) {
      // Any other config-declared kind: generic artifact endpoints.
      if (id) await fetchArtifactDetail(kind, id)
      else await fetchArtifactList(kind)
    }
    // Unknown kinds set the location so the engine can render whatever view it
    // implements; data fetching for new kinds is added with the kind itself.
  }

  // -------------------------------------------------------------
  // 5. API Fetch Operations
  // -------------------------------------------------------------
  const fetchDocSlugs = async () => {
    try {
      const r = await fetch(api('/api/docs'))
      if (r.ok) {
        const data = await r.json()
        setDocSlugs(data)
      }
    } catch (e) {
      console.error('Failed to fetch doc slugs:', e)
    }
  }

  const fetchFavourites = async () => {
    try {
      const r = await fetch(api('/api/favourites'))
      if (r.ok) {
        const data = await r.json()
        if (Array.isArray(data)) setFavourites(data)
      }
    } catch (e) {
      console.error('Failed to fetch favourites:', e)
    }
  }

  // Slug → title for every listed doc (dots on nav rows don't re-fetch).
  const titleBySlug = useMemo(() => {
    const m = new Map<string, string>()
    for (const d of docSlugs) if (!m.has(d.slug)) m.set(d.slug, d.title)
    return m
  }, [docSlugs])

  const favSet = useMemo(() => new Set(favourites), [favourites])

  // Rows for the Favourites nav section: kept stable by fast-path index,
  // shown off-tree whether or not the doc is currently listed.
  const favouriteRows = useMemo(() =>
    favourites
      .map(slug => ({ slug, title: titleBySlug.get(slug) ?? slug, listed: titleBySlug.has(slug) }))
      .sort((a, b) => a.title.localeCompare(b.title)),
    [favourites, titleBySlug],
  )

  const toggleFavourite = useCallback(async (slug: string) => {
    const isFav = favourites.includes(slug)
    const next = isFav ? favourites.filter(s => s !== slug) : [...favourites, slug]
    setFavourites(next)
    try {
      const r = await fetch(api('/api/favourites'), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(next),
      })
      if (!r.ok) console.error('Failed to save favourites:', r.status)
    } catch (e) {
      console.error('Failed to save favourites:', e)
    }
  }, [favourites])

  // Open the ⋯ menu anchored at the dots that were clicked.
  const openDocMenu = (slug: string, el: HTMLElement) => {
    const r = el.getBoundingClientRect()
    setMenuAnchor({ slug, title: titleBySlug.get(slug) ?? slug, x: r.right, y: r.top })
  }

  // ⋯ → clock: navigate, then open the revision rail once the doc is active.
  const openDocHistory = (slug: string) => {
    setMenuAnchor(null)
    setHistoryIntent(slug)
    navigateTo(`/docs/${slug}`)
  }

  const copyDocPath = (slug: string) => {
    const path = `docs/${slug}`
    navigator.clipboard?.writeText(path).catch(() => undefined)
  }

  const exportDoc = async (slug: string, fmt: DocExportFormat) => {
    const title = titleBySlug.get(slug) ?? slug
    let content: string | null = null
    if (isDocPage && activePage.id === slug) content = docContent
    else {
      try {
        const r = await fetch(api(`/api/docs/${slug}`))
        if (r.ok) { const d = await r.json(); content = d.content }
      } catch (e) { console.error('Failed to load doc for export:', e) }
    }
    if (content == null) return
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${title}.${fmt}`
    a.click()
    URL.revokeObjectURL(url)
  }

  const deleteDocFile = async (slug: string) => {
    const title = titleBySlug.get(slug) ?? slug
    if (!window.confirm(`Delete "${title}"? This removes the doc file from your local workspace.`)) return
    try {
      const r = await fetch(api(`/api/docs/${slug}`), { method: 'DELETE' })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      setMenuAnchor(null)
      fetchDocSlugs()
      fetchFavourites() // backend re-reads and drops the now-stale slug
      if (isDocPage && activePage.id === slug) navigateTo('/')
    } catch (e) {
      console.error('Failed to delete doc:', e)
    }
  }

  const fetchTicketsList = async () => {
    try {
      const r = await fetch(api('/api/tickets'))
      if (r.ok) {
        const data = await r.json()
        setTickets(data)
      }
    } catch (e) {
      console.error('Failed to fetch tickets:', e)
    }
  }

  const fetchDocPage = async (slug: string) => {
    try {
      const r = await fetch(api(`/api/docs/${slug}`))
      if (r.ok) {
        const data = await r.json()
        setDocTitle(data.title)
        setDocContent(data.content)
        setDocMissing(false)
      } else {
        setDocContent('')
        setDocMissing(true)
      }
    } catch (e) {
      console.error('Failed to load doc:', e)
    }
  }

  // The canonical git path of the current artifact, used by all history calls.
  const revisionsPath = useMemo(() => {
    if (activePage.kind === 'docs') return `/api/revisions/docs/${activePage.id ?? 'index'}`
    if (activePage.kind === 'tickets' && activePage.id) return `/api/revisions/tickets/${activePage.id}`
    return null
  }, [activePage])

  // List of commits that touched the current doc/ticket, newest first.
  const fetchRevisions = useCallback(async () => {
    if (!revisionsPath) { setRevisions([]); return }
    try {
      const r = await fetch(api(revisionsPath))
      if (r.ok) {
        const data = await r.json()
        setRevisions(Array.isArray(data) ? data : [])
      } else {
        setRevisions([])
      }
    } catch (e) {
      console.error('Failed to load revisions:', e)
      setRevisions([])
    }
  }, [revisionsPath])

  // Show a revision: fetch its content (and the adjacent diff vs its parent).
  const selectRevision = useCallback(async (idx: number) => {
    if (!revisionsPath || idx < 0) return
    const rev = revisions[idx]
    if (!rev) return
    setHistoryIdx(idx)
    setHistoryAt(null)
    // Diff against the adjacent parent (older) commit — the root commit has
    // none, so compare against git's empty tree to show the whole body added.
    const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf8d69288fbee4904'
    const parentSha = idx + 1 < revisions.length ? revisions[idx + 1].sha : EMPTY_TREE
    try {
      const [atR, diffR] = await Promise.all([
        fetch(api(`${revisionsPath}?at=${encodeURIComponent(rev.sha)}`)),
        fetch(api(`${revisionsPath}?a=${encodeURIComponent(parentSha)}&b=${encodeURIComponent(rev.sha)}`)),
      ])
      if (atR.ok) setHistoryAt(await atR.json())
      if (diffR.ok) setHistoryDiff((await diffR.json()).diff || '')
      else setHistoryDiff('')
    } catch (e) {
      console.error('Failed to load revision:', e)
      setHistoryAt(null)
      setHistoryDiff('')
    }
  }, [revisionsPath, revisions])

  // Back at the working copy: show the live doc.
  const showCurrent = useCallback(() => {
    setHistoryIdx(-1)
    setHistoryAt(null)
    setHistoryDiff('')
  }, [])

  const fetchTicketDetail = async (id: string) => {
    try {
      const r = await fetch(api(`/api/tickets/${id}`))
      if (r.ok) {
        const data = await r.json()
        setActiveTicket(data)
        setDocTitle('dk-' + data.id)
      } else {
        setActiveTicket(null)
        setDocTitle('Not Found')
      }
    } catch (e) {
      console.error('Failed to load ticket:', e)
    }
  }

  // Generic artifact endpoints — the engine serves any config-declared kind.
  const fetchArtifactList = async (kind: string) => {
    try {
      const r = await fetch(api(`/api/artifacts/${encodeURIComponent(kind)}`))
      if (r.ok) {
        const data = await r.json()
        setArtifactLists(prev => ({ ...prev, [kind]: data }))
      }
    } catch (e) {
      console.error('Failed to fetch artifact list:', e)
    }
  }

  const fetchArtifactDetail = async (kind: string, id: string) => {
    setPrdStatusErr(null)
    try {
      const r = await fetch(api(`/api/artifacts/${encodeURIComponent(kind)}/${id}`))
      if (r.ok) {
        const data = await r.json()
        setDocTitle(data.title)
        setDocContent(data.content)
        if (kind === 'prds') {
          setPrdStatus(typeof data.frontmatter?.status === 'string' ? data.frontmatter.status : null)
        }
      } else {
        setDocTitle('Not Found')
        setDocContent('')
        setDocMissing(true)
      }
    } catch (e) {
      console.error('Failed to load artifact:', e)
    }
  }

  // Docked PRD status actions: Approve / Request changes from the PRD itself.
  // Same endpoint and transition rules as the pipeline row.
  const prdSetStatus = async (status: string) => {
    const id = activePage.id
    if (!id) return
    try {
      const r = await fetch(api(`/api/pipeline/prds/${encodeURIComponent(id)}/status`), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      })
      if (r.ok) {
        setPrdStatus(status)
        await fetchArtifactDetail('prds', id)
        await fetchArtifactList('prds')
      } else {
        const j = await r.json().catch(() => null)
        setPrdStatusErr(j?.error ?? ('status ' + r.status))
      }
    } catch (e) {
      setPrdStatusErr(String(e))
    }
  }

  // -------------------------------------------------------------
  // 5b. Thread API Operations
  // -------------------------------------------------------------
  const fetchThreads = useCallback(async (context: string) => {
    try {
      const r = await fetch(api(`/api/threads?context=${encodeURIComponent(context)}`))
      if (r.ok) {
        const data = await r.json()
        console.log('[thread] fetchThreads', { context, count: data.length, ids: data.map((t: any) => t.id) })
        setThreads(data)
      }
    } catch (e) {
      console.error('Failed to fetch threads:', e)
    }
  }, [])

  const saveViewState = useCallback(async () => {
    const payload = { ...contextThreadState.current, scroll: { ...chatScrollPosByThreadRef.current } }
    console.log('[viewstate] save', JSON.stringify(payload))
    try {
      await fetch(api('/api/viewstate'), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
    } catch (e) {
      console.error('Failed to save view state:', e)
    }
  }, [])

  const fetchViewState = useCallback(async () => {
    try {
      const r = await fetch(api('/api/viewstate'))
      if (r.ok) {
        const data = await r.json()
        if (data && typeof data === 'object' && !Array.isArray(data)) {
          console.log('[viewstate] load from disk', JSON.stringify(data))
          const { scroll, ...contexts } = data as { scroll?: Record<string, number> } & Record<string, { activeThreadId?: string; showThreadList: boolean }>
          contextThreadState.current = contexts
          if (scroll && typeof scroll === 'object') {
            chatScrollPosByThreadRef.current = { ...chatScrollPosByThreadRef.current, ...scroll }
          }
        }
      }
    } catch (e) {
      console.error('Failed to fetch view state:', e)
    }
    console.log('[viewstate] loaded=true')
    setViewStateLoaded(true)
  }, [])

  const createNewThread = useCallback(async (context: string) => {
    console.log('[thread] createNewThread', { context })
    try {
      const r = await fetch(api('/api/threads'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ context, title: (context || 'Workspace') + ' discussion' }),
      })
      if (r.ok) {
        const data = await r.json()
        console.log('[thread] created', data.id)
        setActiveThreadId(data.id)
        setShowThreadList(false)
        await fetchThreads(context)
      }
    } catch (e) {
      console.error('Failed to create thread:', e)
    }
  }, [fetchThreads])

  const deleteThread = useCallback(async (threadId: string) => {
    console.log('[thread] delete', { threadId, wasActive: threadId === activeThreadId })
    try {
      await fetch(api(`/api/threads/${threadId}`), { method: 'DELETE' })
      if (activeThreadId === threadId) {
        setActiveThreadId(undefined)
      }
      await fetchThreads(contextKey)
    } catch (e) {
      console.error('Failed to delete thread:', e)
    }
  }, [activeThreadId, contextKey, fetchThreads])

  // Remember where the chat was scrolled per-thread so returning to a thread
  // (via the thread list OR doc navigation) resumes there instead of
  // re-animating to the bottom.
  const chatScrollPosByThreadRef = useRef<Record<string, number>>({})
  const chatPanelRef = useRef<HTMLElement | null>(null)
  const activeThreadIdRef = useRef(activeThreadId)
  activeThreadIdRef.current = activeThreadId
  // With `autoScroll={false}` there is no stick-to-bottom spring; the chat
  // scroll container is a plain div we control. We only restore a saved
  // position on mount and persist it as the user scrolls. We never auto-follow
  // streaming content, so no gesture can be fought by an observer; when the
  // user is above the bottom a "scroll to bottom" button appears instead.
  const chatScrollerRef = useRef<HTMLElement | null>(null)
  const scrollSaveTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const [atChatBottom, setAtChatBottom] = useState(true)
  const atChatBottomRef = useRef(true)

  const saveChatScrollPos = useCallback((threadId?: string) => {
    const id = threadId ?? activeThreadIdRef.current
    if (!id) return
    const root = document.querySelector('[data-testid="copilot-chat"]')
    if (!root) return
    for (const el of root.querySelectorAll('*')) {
      const cs = getComputedStyle(el)
      if ((cs.overflowY === 'auto' || cs.overflowY === 'scroll') && el.scrollHeight > el.clientHeight + 1) {
        chatScrollPosByThreadRef.current[id] = el.scrollTop
        return
      }
    }
  }, [])

  const selectThread = useCallback((threadId: string) => {
    console.log('[thread] select', { threadId })
    setActiveThreadId(threadId)
    setShowThreadList(false)
  }, [])

  const scrollChatToBottom = useCallback(() => {
    const el = chatScrollerRef.current
    if (!el) return
    const id = activeThreadIdRef.current
    if (!id) return
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    setAtChatBottom(true)
    chatScrollPosByThreadRef.current[id] = el.scrollHeight - el.clientHeight
  }, [])

  // Own scroll control (the chat uses `autoScroll={false}`, so there is no
  // stick-to-bottom spring). Restore the saved position whenever the chat
  // (re)mounts — covering doc navigation, thread-list re-entry to a different
  // thread, and page refresh — persist it as the user scrolls, and track
  // whether we are at the bottom (to show the "scroll to bottom" button).
  useEffect(() => {
    if (!activeThreadId) return
    let el: HTMLElement | null = null
    let raf = 0
    let stickyObserver: MutationObserver | null = null

    const findScroller = () => {
      const root = document.querySelector('[data-testid="copilot-chat"]')
      if (!root) return null
      for (const node of root.querySelectorAll('*')) {
        const cs = getComputedStyle(node)
        if ((cs.overflowY === 'auto' || cs.overflowY === 'scroll') && node.scrollHeight > node.clientHeight + 1) {
          return node as HTMLElement
        }
      }
      return null
    }

    const onScroll = () => {
      if (!el) return
      chatScrollPosByThreadRef.current[activeThreadId] = el.scrollTop
      const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 60
      if (nearBottom !== atChatBottomRef.current) {
        atChatBottomRef.current = nearBottom
        setAtChatBottom(nearBottom)
      }
      clearTimeout(scrollSaveTimerRef.current)
      scrollSaveTimerRef.current = setTimeout(() => saveViewState(), 400)
    }

    const attach = () => {
      el = findScroller()
      if (el) {
        chatScrollerRef.current = el
        el.addEventListener('scroll', onScroll, { passive: true })
        // Smart stick-to-bottom: while the user is already at the bottom,
        // follow streamed content (new message nodes mutate the container).
        // The moment they scroll up, onScroll flips atChatBottom and the
        // observer stops re-anchoring — no CopilotKit autoScroll spring, so
        // remounts/restores never fight the user.
        stickyObserver = new MutationObserver(() => {
          if (el && atChatBottomRef.current && el.scrollHeight > el.clientHeight) {
            el.scrollTop = el.scrollHeight
          }
        })
        stickyObserver.observe(el, { childList: true, subtree: true })
        // Seed the at-bottom indicator without recording a position: at mount
        // the container starts at scrollTop 0, and a probe here would overwrite
        // a hydrated saved position before restore runs.
        const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 60
        if (nearBottom !== atChatBottomRef.current) {
          atChatBottomRef.current = nearBottom
          setAtChatBottom(nearBottom)
        }
        const saved = chatScrollPosByThreadRef.current[activeThreadId]
        if (saved) {
          let frames = 0
          // The last scrollTop we set programmatically. If a subsequent frame
          // finds ``el.scrollTop` has moved off it, the user is scrolling and we
          // must stop re-anchoring (otherwise reloading at the bottom pins the
          // view and fights the user for the whole hydration window).
          let lastSet: number | null = null
          const restore = () => {
            if (!el) return
            if (lastSet !== null && el.scrollTop !== lastSet) return
            const max = el.scrollHeight - el.clientHeight
            if (max > 0) {
              lastSet = Math.min(saved, max)
              el.scrollTop = lastSet
            }
            // Keep re-applying as hydration/streaming grows the content: a
            // single shot would clamp to the current fraction and strand the
            // view near the top on page refresh. Stop once the saved position
            // is fully reachable, or after ~5s.
            if (max < saved && ++frames < 300) {
              requestAnimationFrame(restore)
            }
          }
          requestAnimationFrame(restore)
        }
        return true
      }
      return false
    }

    const poll = () => {
      if (!attach() && ++raf < 600) requestAnimationFrame(poll)
    }
    requestAnimationFrame(poll)
    return () => {
      cancelAnimationFrame(raf)
      el?.removeEventListener('scroll', onScroll)
      stickyObserver?.disconnect()
      chatScrollerRef.current = null
    }
  }, [activeThreadId, saveViewState])

  // Best-effort last-write on navigation/refresh so the chat position lands in
  // viewstate even if a debounced scroll-save hadn't fired yet.
  useEffect(() => {
    const flush = () => {
      saveChatScrollPos()
      saveViewState()
    }
    window.addEventListener('pagehide', flush)
    return () => window.removeEventListener('pagehide', flush)
  }, [saveChatScrollPos, saveViewState])

  // Load threads when context changes
  const autoCreateThread = useCallback(async (context: string) => {
    if (repos.length === 0) return // no repo: nothing to attach a thread to
    console.log('[thread] autoCreate', { context, reason: 'no saved state found' })
    try {
      const r = await fetch(api('/api/threads'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ context, title: (context || 'Workspace') + ' discussion' }),
      })
      if (r.ok) {
        const data = await r.json()
        console.log('[thread] autoCreated', data.id)
        setActiveThreadId(data.id)
        await fetchThreads(context)
      }
    } catch (e) {
      console.error('Failed to auto-create thread:', e)
    }
  }, [fetchThreads, repos.length])

useEffect(() => {
    console.log('[effect] contextSwitch', { contextKey, viewStateLoaded, isNavigating: prevContextKey.current !== '' && prevContextKey.current !== contextKey, prev: prevContextKey.current })
    // Do nothing until disk-persisted view state has been loaded —
    // prevents wiping/overwriting saved state with default values.
    if (!viewStateLoaded) return

    const isNavigation = prevContextKey.current !== '' && prevContextKey.current !== contextKey
    if (isNavigation) {
      console.log('[effect] flushing context', { from: prevContextKey.current, to: contextKey, state: { activeThreadId, showThreadList, historyOpen } })
      // Flush the context we're leaving with its live values
      saveChatScrollPos()
      contextThreadState.current[prevContextKey.current] = { activeThreadId, showThreadList, historyOpen }
      saveViewState()
    }
    prevContextKey.current = contextKey

    // Restore (or create) the target context's state
    const saved = contextThreadState.current[contextKey]
    console.log('[effect] restore/create', { contextKey, saved: saved ? JSON.stringify(saved) : 'none' })
    if (saved && saved.activeThreadId) {
      console.log('[effect] restoring thread', saved.activeThreadId)
      setActiveThreadId(saved.activeThreadId)
      setShowThreadList(saved.showThreadList)
    } else if (saved && saved.showThreadList) {
      console.log('[effect] restoring threadList view')
      setActiveThreadId(undefined)
      setShowThreadList(true)
    } else {
      console.log('[effect] no saved state, will autoCreate')
      setActiveThreadId(undefined)
      setShowThreadList(false)
      // An uninitialized repo has no thread store (the runtime and the
      // backend both refuse it): don't fire the auto-create POST — the
      // 409 would just add console noise. Once init lands, this effect
      // re-runs (deps below) and creates the first thread.
      if (!repoUninitialized && !repoBooting) {
        autoCreateThread(contextKey)
      }
    }
    setHistoryOpen(!!saved?.historyOpen)
    fetchThreads(contextKey)
  }, [contextKey, viewStateLoaded, fetchThreads, saveChatScrollPos, repoUninitialized, repoBooting])

  // ⋯ → clock from a nav row: once the target doc is the active page, open the
  // revision rail (and let it do its usual on-demand fetch).
  useEffect(() => {
    if (historyIntent && activePage.kind === 'docs' && activePage.id === historyIntent) {
      setHistoryOpen(true)
      setHistoryIntent(null)
    }
  }, [historyIntent, activePage])

  // Save view state to disk whenever thread state changes within the same context
  useEffect(() => {
    if (viewStateLoaded) {
      console.log('[effect] saveOnChange', { contextKey, activeThreadId, showThreadList, historyOpen })
      contextThreadState.current[contextKey] = { activeThreadId, showThreadList, historyOpen }
      saveViewState()
    }
  }, [activeThreadId, showThreadList, historyOpen, viewStateLoaded, contextKey, saveViewState])

  // Refresh the thread list every time it is opened, so message counts and
  // previews reflect the conversation that just happened.
  useEffect(() => {
    if (showThreadList) {
      fetchThreads(contextKey)
    }
  }, [showThreadList, contextKey, fetchThreads])

  // Load revisions when the rail opens or the current doc changes while open;
  // reset the selection if the doc itself changed.
  const prevHistoryPath = useRef<string | null>(null)
  useEffect(() => {
    if (!historyOpen || !revisionsPath) return
    if (prevHistoryPath.current !== revisionsPath) {
      prevHistoryPath.current = revisionsPath
      showCurrent()
    }
    fetchRevisions()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [historyOpen, revisionsPath, fetchRevisions])

  // Load view state from disk on mount, then flag as loaded
  useEffect(() => {
    fetchViewState().then(() => {
      // The context switch useEffect will restore state now that viewStateLoaded is true
      // It runs next cycle with the correct contextKey
    })
  }, [])

  // -------------------------------------------------------------
  // 6. Resizing & UI Drag handlers
  // -------------------------------------------------------------
  const startResizing = (e: React.MouseEvent) => {
    // Prevent the mousedown from starting a native text selection, and keep
    // text non-selectable while dragging so the re-render on release doesn't
    // flicker a selection over adjacent content.
    e.preventDefault()
    document.body.style.userSelect = 'none'
    // Hold the resize cursor over every element for the whole drag. The
    // data-resizing attribute is matched by a CSS rule in index.css with
    // !important, so content under the pointer that sets its own cursor or
    // user-select (links, buttons) cannot flicker the icon back during the drag.
    document.documentElement.setAttribute('data-resizing', '')
    const startX = e.clientX
    const startWidth = chatWidth
    let finalWidth = startWidth
    const doDrag = (moveEvent: MouseEvent) => {
      const deltaX = moveEvent.clientX - startX
      const newWidth = Math.max(300, Math.min(window.innerWidth - 100, startWidth - deltaX))
      finalWidth = newWidth
      // Apply the width synchronously on the DOM node instead of going through
      // React state. A setState-per-mousemove lags the panel behind the pointer
      // for a frame, so the handle (and the cursor hit-test at the pointer)
      // briefly sit over stale content — that lag is the resize flicker.
      if (chatPanelRef.current) chatPanelRef.current.style.width = `${newWidth}px`
    }

    const stopDrag = () => {
      document.documentElement.removeAttribute('data-resizing')
      document.body.style.userSelect = ''
      document.removeEventListener('mousemove', doDrag)
      document.removeEventListener('mouseup', stopDrag)
      setChatWidth(finalWidth)
    }

    document.addEventListener('mousemove', doDrag)
    document.addEventListener('mouseup', stopDrag)
  }

  // Wizard instead of CopilotChat when the runtime is reachable but has no key.
  // While the runtime is unreachable (hermetic tests) the chat shell still
  // renders, preserving existing behavior.
  const statusLoading = aiReachable === null
  const showAiWizard = aiReachable === true && !!aiStatus && !aiStatus.configured
  const chatReady = !statusLoading && !showAiWizard
  const chatCanMount = chatReady && repos.length > 0 && activeRepoStatus?.initialized

  const chatPanel = (
        <>
        {chatCollapsed && !isFullscreen && (
          <aside className="w-9 bg-[#090c15] border-l border-borderDark/60 flex-shrink-0 flex flex-col items-center py-2 z-20"
            data-testid="chat-collapsed-rail">
            <button
              onClick={() => setChatCollapsed(false)}
              className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 flex items-center justify-center text-slate-400 hover:text-slate-100 transition-colors"
              title="Show chat"
            >
              <PanelRightOpen className="w-3.5 h-3.5" />
            </button>
          </aside>
        )}
        <aside 
          ref={chatPanelRef}
          data-testid="copilot-chat-panel"
          className={`bg-[#090c15] border-l border-borderDark/60 flex-shrink-0 flex flex-col z-20 shadow-2xl ${
            isFullscreen ? 'fixed inset-0 w-full h-full z-50' : 'relative'
          } ${chatCollapsed && !isFullscreen ? 'w-0 overflow-hidden' : ''}`}
          style={!isFullscreen ? { width: chatCollapsed ? '0' : `${chatWidth}px` } : {}}
        >
          {/* Drag Handle */}
          {!isFullscreen && (
            <div 
              data-testid="chat-resize-handle"
              className="absolute top-0 left-0 w-1.5 h-full cursor-col-resize hover:bg-accentBlue/40 active:bg-accentBlue/60 transition-colors z-30"
              onMouseDown={startResizing}
            />
          )}

          {/* Fullscreen Button in Header bar */}
          <div className="h-[57px] px-4 border-b border-borderDark/40 flex items-center justify-between bg-surfaceDark/20 flex-shrink-0">
            <div className="flex items-center gap-2">
              {activeThreadId && !showThreadList ? (
                <button
                  onClick={() => { saveChatScrollPos(); setShowThreadList(true) }}
                  className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 flex items-center justify-center text-slate-400 hover:text-slate-100 transition-colors"
                  title="Back to Thread List"
                >
                  <ArrowLeft className="w-3.5 h-3.5" />
                </button>
              ) : (
                <button
                  onClick={() => { if (!showThreadList) saveChatScrollPos(); setShowThreadList(!showThreadList) }}
                  className={`h-7 w-7 rounded-lg border flex items-center justify-center transition-colors ${
                    showThreadList
                      ? 'bg-accentBlue/20 border-accentBlue/40 text-accentBlue'
                      : 'border-borderDark/50 hover:bg-borderDark/40 text-slate-400 hover:text-slate-100'
                  }`}
                  title="Thread List"
                >
                  <MessageSquare className="w-3.5 h-3.5" />
                </button>
              )}
              <span className="text-xs font-bold text-slate-300 flex items-center gap-1.5 uppercase tracking-wider font-mono">
                <Sparkles className="w-3.5 h-3.5 text-accentBlue animate-pulse" />
                {activeThreadId && !showThreadList ? 'Conversation' : 'Threads'}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-[9px] text-slate-500 bg-borderDark/40 px-2 py-0.5 rounded border border-borderDark/20 truncate max-w-[120px]">
                {(isMultiRepo && activeRepo ? activeRepo + ' · ' : '') + contextLabel}
              </span>
              <button
                onClick={() => { setSettingsPane('ai'); setShowSettings(true) }}
                className={`h-7 w-7 rounded-lg border flex items-center justify-center transition-colors ${
                  aiStatus && !aiStatus.configured
                    ? 'border-amber-500/40 text-amber-400 hover:bg-amber-500/10'
                    : 'border-borderDark/50 hover:bg-borderDark/40 text-slate-400 hover:text-slate-100'
                }`}
                title="AI Settings"
              >
                <Key className="w-3.5 h-3.5" />
              </button>
              <button 
                onClick={() => setIsFullscreen(!isFullscreen)} 
                className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 flex items-center justify-center text-slate-400 hover:text-slate-100 transition-colors"
                title="Toggle Fullscreen"
              >
                {isFullscreen ? <Minimize2 className="w-3.5 h-3.5" /> : <Maximize2 className="w-3.5 h-3.5" />}
              </button>
              <button
                onClick={() => setChatCollapsed(true)}
                className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 flex items-center justify-center text-slate-400 hover:text-slate-100 transition-colors"
                title="Collapse chat"
              >
                <PanelRightClose className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>

          <div className={`absolute inset-x-0 bottom-0 top-[57px] z-30 flex flex-col bg-[#090c15] ${showThreadList ? '' : 'hidden'}`}>
              <div className="p-3 border-b border-borderDark/40">
                <button
                  onClick={() => createNewThread(contextKey)}
                  className="w-full flex items-center gap-2 px-3 py-2 rounded-lg border border-dashed border-borderDark/50 text-xs text-slate-400 hover:text-slate-200 hover:border-accentBlue/40 hover:bg-accentBlue/5 transition-all"
                >
                  <Plus className="w-3.5 h-3.5" />
                  New conversation
                </button>
              </div>
              <div className="flex-1 overflow-y-auto space-y-1 p-2">
                {threads.length === 0 && (
                  <div className="text-xs text-slate-500 text-center py-8 italic">
                    No threads yet for this context
                  </div>
                )}
                {threads.map(t => (
                  <div
                    key={t.id}
                    onClick={() => selectThread(t.id)}
                    className={`group flex items-start gap-2 px-3 py-2.5 rounded-lg cursor-pointer transition-all border ${
                      activeThreadId === t.id
                        ? 'bg-accentBlue/10 border-accentBlue/20 text-slate-100'
                        : 'border-transparent hover:bg-borderDark/20 text-slate-400 hover:text-slate-200'
                    }`}
                  >
                    <MessageSquare className="w-3.5 h-3.5 mt-0.5 flex-shrink-0" />
                    <div className="flex-1 min-w-0">
                      <div className="text-xs font-medium truncate">{t.title}</div>
                      <div className="text-[10px] text-slate-500 mt-0.5 flex items-center gap-2">
                        <span>{t.message_count} messages</span>
                        {t.preview && (
                          <>
                            <span className="text-slate-600">·</span>
                            <span className="truncate">{t.preview}</span>
                          </>
                        )}
                      </div>
                    </div>
                    <button
                      onClick={(e) => { e.stopPropagation(); deleteThread(t.id) }}
                      className="opacity-0 group-hover:opacity-100 h-6 w-6 rounded flex items-center justify-center hover:bg-red-500/20 text-slate-500 hover:text-red-400 transition-all flex-shrink-0"
                      title="Delete thread"
                    >
                      <Trash2 className="w-3 h-3" />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          {showAiWizard ? (
            <div className="flex-1 flex flex-col items-center justify-center p-6 gap-3 text-center">
              <Key className="w-9 h-9 text-amber-400" />
              <div>
                <p className="text-sm font-semibold text-slate-200">AI assistant not configured</p>
                <p className="text-[11px] text-slate-500 mt-1 max-w-[240px]">
                  Add an API key in Settings to let the chat assistant read, create, and update docs and tickets.
                </p>
              </div>
              <button
                onClick={() => { setSettingsPane('ai'); setShowSettings(true) }}
                className="mt-1 px-3 py-1.5 rounded-lg border border-amber-500/40 bg-amber-500/10 text-amber-300 text-[11px] font-medium hover:bg-amber-500/20 transition-colors"
              >
                Open AI settings
              </button>
            </div>
          ) : statusLoading ? (
            <div className="flex-1 flex items-center justify-center p-4">
              <div className="text-xs text-slate-500 text-center">
                <Sparkles className="w-8 h-8 mx-auto mb-2 text-slate-600 animate-pulse" />
                <p className="font-medium text-slate-400">Checking AI configuration…</p>
              </div>
            </div>
          ) : repoUninitialized || repoBooting ? (
            <div className="flex-1 flex items-center justify-center p-4">
              <div className="text-xs text-slate-500 text-center">
                <Sparkles className="w-8 h-8 mx-auto mb-2 text-slate-600" />
                <p className="font-medium text-slate-400">{repoUninitialized ? 'Repository not initialized' : 'Loading repository…'}</p>
                <p className="text-slate-500 mt-1">
                  {repoUninitialized ? 'Initialize it to scaffold .devtop/ and enable the assistant.' : ''}
                </p>
              </div>
            </div>
          ) : activeThreadId ? (
            <div className="flex-1 flex flex-col min-h-0 overflow-hidden relative">
              <MemoizedCopilotChat
                key={activeThreadId}
                agentId="default"
                threadId={activeThreadId}
                labels={CHAT_LABELS}
                // No auto-scroll: CopilotKit's stick-to-bottom would re-anchor
                // to the bottom whenever the chat remounts (doc navigation,
                // thread switch, page refresh). We restore the saved position
                // ourselves; the button appears when the user is above the
                // bottom instead of auto-following streaming content.
                autoScroll={false}
                className="flex-1 min-h-0"
              />
              {!atChatBottom && (
                <button
                  onClick={scrollChatToBottom}
                  aria-label="Scroll to bottom"
                  title="Scroll to bottom"
                  className="absolute bottom-3 right-3 z-10 w-8 h-8 flex items-center justify-center rounded-full bg-accentBlue text-white shadow-lg hover:bg-blue-600 transition-colors"
                >
                  <ChevronDown className="w-4 h-4" />
                </button>
              )}
            </div>
          ) : (
            <div className="flex-1 flex items-center justify-center p-4">
              <div className="text-xs text-slate-500 text-center">
                <MessageSquare className="w-8 h-8 mx-auto mb-2 text-slate-600" />
                <p className="font-medium text-slate-400">No thread selected</p>
                <p className="text-slate-500 mt-1">Select a thread from the list</p>
              </div>
            </div>
          )}
        </aside>
        </>
  )

  // Nav sections are data-driven from the engine config: one section per kind
  // that declares nav. Views are engine capabilities (tree, board); unknown
  // views render nothing until the engine implements them.
  const navSections = useMemo(() => {
    const kinds = Object.entries(engineConfig.artifact_kinds)
      .map(([kind, def]) => ({ kind, def }))
      .filter(e => !!e.def.nav)
    if (engineConfig.pipeline?.nav) {
      kinds.push({ kind: 'pipeline', def: { path: '', extension: '.mdx', agent_writable: false, view: 'pipeline', nav: engineConfig.pipeline.nav } })
    }
    return kinds.sort((a, b) => (a.def.nav!.order ?? 99) - (b.def.nav!.order ?? 99))
  }, [engineConfig])

  const renderNavSection = (kind: string, nav: EngineNav) => {
    if (kind === 'pipeline') {
      return (
        <div>
          <div className="text-[10px] font-semibold text-slate-500 uppercase tracking-widest px-3 mb-1">{nav.label}</div>
          <div className="space-y-px">
            <a
              href="#/pipeline"
              onClick={(e) => { e.preventDefault(); navigateTo('/pipeline') }}
              className={`flex items-center gap-2.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                activePage.kind === 'pipeline'
                  ? 'bg-accentPurple/10 text-slate-100 border-accentPurple/20'
                  : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
              }`}
            >
              <GitBranch className="w-4 h-4 text-slate-600" />
              <span>Derivation</span>
            </a>
          </div>
        </div>
      )
    }
    if (nav.view === 'tree') {
      return (
        <div>
          <div className="text-[10px] font-semibold text-slate-500 uppercase tracking-widest px-3 mb-1">{nav.label}</div>
          <div className="space-y-px">
            <a
              href="#/"
              onClick={(e) => { e.preventDefault(); navigateTo('/') }}
              className={`flex items-center gap-2.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                isHomePage
                  ? 'bg-accentBlue/10 text-slate-100 border-accentBlue/20'
                  : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
              }`}
            >
              <FileText className="w-4 h-4 text-slate-600" />
              <span>Home</span>
            </a>
          </div>
          <div className="space-y-px mt-0.5">
            {(() => {
              const tree = buildDocTree(docSlugs)
              const renderNode = (node: DocTreeNode, depth: number) => {
                const isParent = node.slug && node.children.length > 0
                const isLeaf = node.slug && node.children.length === 0

                if (isLeaf) {
                  return (
                    <div
                      key={node.slug}
                      style={{ paddingLeft: `${12 + depth * 16}px` }}
                      className={`group flex items-center gap-2.5 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                        activePage.id === node.slug && isDocPage
                          ? 'bg-accentBlue/10 text-slate-100 border-accentBlue/20'
                          : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
                      }`}
                    >
                      <a
                        href={`#/docs/${node.slug}`}
                        onClick={(e) => { e.preventDefault(); navigateTo(`/docs/${node.slug}`) }}
                        className="flex items-center gap-2.5 flex-1 min-w-0"
                      >
                        <FileText className="w-4 h-4 text-slate-600 flex-shrink-0" />
                        <span className="truncate">{node.title}</span>
                        {favSet.has(node.slug!) && <Star className="w-3 h-3 text-amber-400 ml-auto flex-shrink-0" fill="currentColor" />}
                      </a>
                      <button
                        type="button"
                        onClick={(e) => openDocMenu(node.slug!, e.currentTarget)}
                        className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 p-1 rounded hover:bg-borderDark/40 text-slate-400 hover:text-slate-100 transition-opacity flex-shrink-0"
                        title={`Actions for ${node.title}`}
                        aria-haspopup="menu"
                      >
                        <MoreVertical className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  )
                }

                const sectionKey = node.slug || node.name
                const isCollapsed = collapsedSections.has(sectionKey)
                const label = node.title || node.name

                return (
                  <div key={sectionKey} className="space-y-px">
                    {isParent ? (
                      <div
                        style={{ paddingLeft: `${12 + depth * 16}px` }}
                        className={`group flex items-center w-full gap-2.5 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                          activePage.id === node.slug && isDocPage
                            ? 'bg-accentBlue/10 text-slate-100 border-accentBlue/20'
                            : 'text-slate-400 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
                        }`}
                      >
                        <button
                          onClick={(e) => { e.stopPropagation(); toggleSection(sectionKey) }}
                          className="flex-shrink-0 rounded hover:bg-borderDark/40 transition-colors"
                        >
                          <FileText className="w-4 h-4 group-hover:hidden" />
                          <span className="hidden group-hover:inline">
                            {isCollapsed ? <ChevronRight className="w-4 h-4" /> : <ChevronDown className="w-4 h-4" />}
                          </span>
                        </button>
                        <a
                          href={`#/docs/${node.slug}`}
                          onClick={(e) => { e.preventDefault(); navigateTo(`/docs/${node.slug}`) }}
                          className="truncate hover:text-slate-100 transition-colors flex-1 min-w-0"
                        >
                          {label}
                        </a>
                        <button
                          type="button"
                          onClick={(e) => openDocMenu(node.slug!, e.currentTarget)}
                          className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 p-1 rounded hover:bg-borderDark/40 text-slate-400 hover:text-slate-100 transition-opacity flex-shrink-0"
                          title={`Actions for ${label}`}
                          aria-haspopup="menu"
                        >
                          <MoreVertical className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    ) : (
                      <button
                        onClick={() => toggleSection(sectionKey)}
                        style={{ paddingLeft: `${12 + depth * 16}px` }}
                        className="flex items-center gap-1.5 w-full py-1 rounded-lg text-xs font-semibold text-slate-500 uppercase tracking-widest hover:text-slate-300 transition-colors duration-150"
                      >
                        {isCollapsed ? <ChevronRight className="w-3 h-3" /> : <ChevronDown className="w-3 h-3" />}
                        {label}
                      </button>
                    )}
                    {!isCollapsed && node.children.map(child => renderNode(child, depth + 1))}
                  </div>
                )
              }
              return tree.map(node => renderNode(node, 0))
            })()}
          </div>
          {/* User-scoped favourites: star any doc via its ⋯ menu. Stale slugs
              (doc deleted) are dropped by the backend on read. */}
          {kind === 'docs' && favouriteRows.length > 0 && (
            <div className="space-y-px mt-2.5">
              <div className="text-[10px] font-semibold text-slate-500 uppercase tracking-widest px-3 mb-1">Favourites</div>
              {favouriteRows.map(({ slug, title, listed }) => (
                <div
                  key={slug}
                  className={`group flex items-center gap-2.5 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                    listed && activePage.id === slug && isDocPage
                      ? 'bg-accentBlue/10 text-slate-100 border-accentBlue/20'
                      : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
                  }`}
                >
                  <a
                    href={`#/docs/${slug}`}
                    onClick={(e) => { e.preventDefault(); navigateTo(`/docs/${slug}`) }}
                    className="flex items-center gap-2.5 flex-1 min-w-0"
                  >
                    <Star className="w-3.5 h-3.5 text-amber-400 flex-shrink-0" fill="currentColor" />
                    <span className="truncate">{title}</span>
                    {!listed && <span className="text-[9px] text-slate-600 font-mono flex-shrink-0">missing</span>}
                  </a>
                  <button
                    type="button"
                    onClick={(e) => openDocMenu(slug, e.currentTarget)}
                    className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 p-1 rounded hover:bg-borderDark/40 text-slate-400 hover:text-slate-100 transition-opacity flex-shrink-0"
                    title={`Actions for ${title}`}
                    aria-haspopup="menu"
                  >
                    <MoreVertical className="w-3.5 h-3.5" />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      )
    }

    if (nav.view === 'board') {
      return (
        <div>
          <div className="flex items-center justify-between px-3 mb-1">
            <span className="text-[10px] font-semibold text-slate-500 uppercase tracking-widest">{nav.label}</span>
            <span className="text-[10px] font-mono text-slate-500 bg-borderDark/40 px-1.5 py-0.2 rounded-full border border-borderDark/30">
              {tickets.length}
            </span>
          </div>
          <div className="space-y-px">
            <a
              href="#/tickets"
              onClick={(e) => { e.preventDefault(); navigateTo('/tickets') }}
              className={`flex items-center gap-2.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                activePage.kind === 'tickets'
                  ? 'bg-accentPurple/10 text-slate-100 border-accentPurple/20'
                  : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
              }`}
            >
              <ClipboardList className="w-4 h-4 text-slate-600" />
              <span>Board</span>
            </a>
          </div>
        </div>
      )
    }

    if (nav.view === 'list') {
      return (
        <div>
          <div className="text-[10px] font-semibold text-slate-500 uppercase tracking-widest px-3 mb-1">{nav.label}</div>
          <div className="space-y-px">
            <a
              href={`#/${kind}`}
              onClick={(e) => { e.preventDefault(); navigateTo(`/${kind}`) }}
              className={`flex items-center gap-2.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                activePage.kind === kind
                  ? 'bg-accentBlue/10 text-slate-100 border-accentBlue/20'
                  : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
              }`}
            >
              <FileText className="w-4 h-4 text-slate-600" />
              <span>{nav.label}</span>
            </a>
          </div>
        </div>
      )
    }

    return null
  }

  return (
    <div className="flex h-screen overflow-hidden bg-bgDark text-slate-300">
        
        {/* ===== LEFT SIDEBAR ===== */}
        <aside className="w-64 bg-[#05070d]/90 border-r border-borderDark/60 flex-shrink-0 flex flex-col z-10">
          <div className="h-[57px] px-4 border-b border-borderDark/40 flex items-center gap-2.5">
            <div className="h-7 w-7 rounded-lg bg-gradient-to-tr from-accentBlue to-accentPurple flex items-center justify-center font-bold text-white text-sm">
              d
            </div>
            <div>
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-semibold text-slate-100 tracking-tight">devtop</span>
                <span className="text-[10px] text-slate-500 font-mono bg-borderDark/40 px-1 py-0.2 rounded border border-borderDark/30">v0.1.0</span>
              </div>
              <div className="text-[10px] text-slate-500 leading-none">local environment</div>
            </div>
          </div>

          <nav className="flex-1 overflow-y-auto p-3.5 space-y-3">
            {navSections.map(s => (
              <Fragment key={s.kind}>
                {renderNavSection(s.kind, s.def.nav!)}
              </Fragment>
            ))}
          </nav>

          <div className="p-2 border-t border-borderDark/40 bg-[#03050a]/40 space-y-1">
            <div className="flex items-center justify-between px-1.5 py-1">
              <div className="flex items-center gap-2">
                <div className="h-2 w-2 rounded-full bg-emerald-500 animate-pulse"></div>
                <span className="text-[11px] text-slate-400 font-medium">Local Server</span>
              </div>
              <code className="text-[10px] text-slate-500 font-mono bg-borderDark/30 px-1.5 py-0.5 rounded border border-borderDark/20">:8000</code>
            </div>
            <button
              onClick={() => { setShowSettings(true); setSettingsPane('ai') }}
              className={`w-full flex items-center gap-2 px-2 py-1.5 rounded-lg border text-[11px] font-medium transition-colors ${
                aiStatus && !aiStatus.configured
                  ? 'border-amber-500/40 text-amber-300 hover:bg-amber-500/10'
                  : 'border-borderDark/40 text-slate-400 hover:text-slate-100 hover:bg-borderDark/20'
              }`}
              title="Settings"
            >
              {aiStatus && !aiStatus.configured ? (
                <Key className="w-3.5 h-3.5 flex-shrink-0" />
              ) : (
                <Settings className="w-3.5 h-3.5 flex-shrink-0" />
              )}
              <span className="truncate">Settings</span>
            </button>
          </div>
        </aside>

        {/* ===== MAIN CONTENT ===== */}
        <main className="flex-1 overflow-y-auto bg-bgDark border-r border-borderDark/60 flex flex-col">
          <header className="h-[57px] flex-shrink-0 border-b border-borderDark/40 px-8 flex items-center justify-between bg-bgDark/80 sticky top-0 z-10">
            <div className="flex items-center gap-3 text-xs font-medium text-slate-400 relative">
              {activeRepoStatus && (
                <>
                  <div className="relative">
                    <button
                      onClick={() => setRepoDropdownOpen(o => !o)}
                      className="flex items-center gap-2.5 h-8 pl-2.5 pr-3 rounded-xl border border-borderDark/60 bg-surfaceDark/60 hover:border-borderDark hover:bg-surfaceDark transition-colors"
                      title="Switch repository"
                    >
                      <span className="flex items-center justify-center h-6 w-6 rounded-md bg-borderDark/40 text-accentBlue flex-shrink-0">
                        <GitBranch className="w-3.5 h-3.5" />
                      </span>
                      <span className="text-xs font-semibold text-slate-100 truncate max-w-[180px]">{activeRepoStatus.name}</span>
                      {activeRepoStatus.branch && (
                        <span className="hidden sm:inline-flex font-mono text-[10px] text-slate-400 bg-borderDark/40 px-1.5 py-0.5 rounded-md border border-borderDark/40">
                          {activeRepoStatus.branch}
                        </span>
                      )}
                      <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${(REPO_STATUS_META[activeRepoStatus.status] ?? REPO_STATUS_META.ready).dot}`} />
                      <ChevronDown className="w-3.5 h-3.5 text-slate-500" />
                    </button>

                    {repoDropdownOpen && (
                      <>
                        <div className="fixed inset-0 z-40" onClick={() => setRepoDropdownOpen(false)} />
                        <div className="absolute left-0 top-[calc(100%+6px)] w-[380px] rounded-xl border border-borderDark/60 bg-[#0a0e1c]/95 backdrop-blur shadow-2xl doc-menu-pop z-50 overflow-hidden">
                          <div className="px-4 pt-3 pb-2 text-[10px] font-semibold text-slate-500 uppercase tracking-widest flex items-center justify-between">
                            <span>Repositories on this instance</span>
                            <span className="font-mono text-slate-600 normal-case">DEVTOP_REPOS</span>
                          </div>
                          <div className="max-h-[320px] overflow-y-auto px-2 pb-1 space-y-0.5">
                            {repos.map(r => {
                              const meta = REPO_STATUS_META[r.status] ?? REPO_STATUS_META.ready
                              const active = r.name === activeRepo
                              return (
                                <button
                                  key={r.name}
                                  onClick={() => selectRepo(r.name)}
                                  className={`w-full flex items-center gap-2.5 px-3 py-2.5 rounded-lg text-left transition-colors ${
                                    active ? 'bg-accentBlue/10 border border-accentBlue/25 text-slate-100' : 'border border-transparent hover:bg-borderDark/20 text-slate-300'
                                  }`}
                                >
                                  <span className="flex items-center justify-center h-7 w-7 rounded-lg bg-borderDark/40 text-slate-400 flex-shrink-0">
                                    <GitBranch className="w-3.5 h-3.5" />
                                  </span>
                                  <span className="flex-1 min-w-0">
                                    <span className="flex items-center gap-2 text-xs font-semibold truncate">
                                      {r.name}
                                      <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${meta.dot}`} />
                                    </span>
                                    <span className="block text-[10px] font-mono text-slate-600 truncate mt-0.5">{r.path}</span>
                                  </span>
                                  <span className="flex-shrink-0 flex items-center gap-1.5">
                                    {r.branch && (
                                      <span className="font-mono text-[9px] text-slate-500 bg-borderDark/40 px-1.5 py-0.5 rounded-md border border-borderDark/40 max-w-[90px] truncate">{r.branch}</span>
                                    )}
                                    <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-[9px] font-medium border capitalize flex-shrink-0 ${
                                      r.status === 'ready' ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
                                      : r.status === 'dirty' ? 'border-amber-500/30 bg-amber-500/10 text-amber-300'
                                      : r.status === 'uninit' ? 'border-slate-500/40 bg-slate-500/10 text-slate-400'
                                      : 'border-rose-500/30 bg-rose-500/10 text-rose-300'
                                    }`}>{meta.label}</span>
                                    {active && <Check className="w-3.5 h-3.5 text-accentBlue" />}
                                  </span>
                                </button>
                              )
                            })}
                          </div>
                          <div className="border-t border-borderDark/40">
                            <button
                              onClick={() => { setRepoDropdownOpen(false); navigateTo('/repos') }}
                              className="w-full flex items-center gap-2 px-4 py-2.5 text-xs font-medium text-accentBlue hover:bg-accentBlue/5 transition-colors"
                            >
                              <Plus className="w-3.5 h-3.5" />
                              Manage repos…
                            </button>
                          </div>
                        </div>
                      </>
                    )}
                  </div>
                </>
              )}
              {revisionsPath && (
                <button
                  onClick={() => setHistoryOpen(o => !o)}
                  title="Revision history"
                  aria-expanded={historyOpen}
                  className={`h-7 w-7 rounded-lg border flex items-center justify-center transition-colors ${
                    historyOpen
                      ? 'bg-accentBlue/10 border-accentBlue/50 text-accentBlue'
                      : 'border-borderDark/50 text-slate-400 hover:text-slate-100 hover:border-accentBlue/50 hover:bg-accentBlue/10'
                  }`}
                >
                  <History className="w-3.5 h-3.5" />
                </button>
              )}
              {breadcrumbItems.map((item, i) => (
                <Fragment key={i}>
                  {i > 0 && <ChevronRight className="w-3.5 h-3.5 text-slate-600" />}
                  <a
                    href={item.href}
                    onClick={(e) => { e.preventDefault(); navigateTo(item.href.replace('#', '')) }}
                    className={`transition-colors ${i < breadcrumbItems.length - 1 ? 'hover:text-slate-200' : 'text-slate-200 font-semibold pointer-events-none cursor-default'}`}
                  >
                    {item.label}
                  </a>
                </Fragment>
              ))}
            </div>
            {activePage.kind === 'pipeline' && (
              <div className="text-right flex-shrink-0 ml-3">
                <div className="text-sm font-semibold text-slate-100 leading-tight">Work items</div>
                <div className="text-[11px] text-slate-500">Select an intent — derive, approve, and publish hands-off. Answer only when asked.</div>
              </div>
            )}
          </header>

          <div className="flex flex-1 min-h-0">
            {/* 0. REVISION HISTORY RAIL (open via the clock icon in the header) */}
            {historyOpen && isDocumentView && revisions.length > 0 && (
              <aside className="w-56 flex-shrink-0 border-r border-borderDark/40 overflow-y-auto p-2 bg-[#080c16]/60">
                <div className="px-2 pt-1 pb-2 text-[9px] font-semibold text-slate-500 uppercase tracking-widest">Revisions</div>
                {revisions.map((r, i) => (
                  <button
                    key={r.sha}
                    onClick={() => (r.is_current ? showCurrent() : selectRevision(i))}
                    className={`w-full flex flex-col gap-0.5 px-3 py-2 rounded-lg text-left border transition-all ${i === historyIdx ? 'bg-accentBlue/10 border-accentBlue/30' : 'border-transparent hover:bg-borderDark/20'}`}
                  >
                    <div className="flex items-center gap-2">
                      <span className={`font-mono text-[10px] ${r.is_current ? 'text-emerald-400' : 'text-blue-400'}`}>{r.short}</span>
                      {r.is_current && <span className="text-[8px] px-1 py-px rounded bg-emerald-500/10 border-emerald-500/30 text-emerald-400">HEAD</span>}
                    </div>
                    <div className="text-[10px] text-slate-300 leading-snug">{r.message}</div>
                    <div className="text-[8px] text-slate-500 font-mono">{formatRevisionDate(r.date)}</div>
                  </button>
                ))}
              </aside>
            )}

            <div className={`flex-1 min-w-0 overflow-y-auto ${isPipelinePage ? '' : 'p-8'}`}>

            {!showSettings && repos.length === 0 ? (
              <div className="h-full flex items-center justify-center fade-in">
                <div className="max-w-md text-center px-6">
                  <div className="mx-auto w-14 h-14 rounded-2xl bg-borderDark/30 border border-borderDark/50 flex items-center justify-center text-accentBlue mb-4">
                    <GitBranch className="w-6 h-6" />
                  </div>
                  <h2 className="text-lg font-semibold text-slate-100">No repositories yet</h2>
                  <p className="text-xs text-slate-500 mt-2 leading-relaxed">
                    devtop serves one repository at a time. Add the folder you work in to get started —
                    nothing is created until you do; the repo keeps its own <span className="font-mono text-slate-400">.devtop/</span>.
                    Configure an AI key in Settings to bring the assistant along.
                  </p>
                  <div className="flex items-center justify-center gap-2.5 mt-5">
                    <button
                      onClick={() => setShowAddRepo(true)}
                      className="px-3 py-1.5 rounded-lg text-xs font-medium bg-accentBlue text-slate-100 hover:bg-accentBlue/80 transition-colors"
                    >
                      Add repo…
                    </button>
                  </div>
                </div>
              </div>
            ) : showUninitState ? (
              <div className="h-full flex items-center justify-center fade-in">
                <div className="max-w-md text-center px-6">
                  <div className="mx-auto w-14 h-14 rounded-2xl bg-borderDark/30 border border-borderDark/50 flex items-center justify-center text-slate-500 mb-4">
                    <Database className="w-6 h-6" />
                  </div>
                  <h2 className="text-lg font-semibold text-slate-100">devtop is not initialized here</h2>
                  <p className="text-xs text-slate-500 mt-2 leading-relaxed">
                    Initialize <span className="font-mono text-slate-400">{activeRepoStatus?.path}</span> to scaffold
                    <span className="font-mono text-slate-400"> .devtop/</span> — config, agents, and templates land in the repo, not on this instance.
                  </p>
                  <div className="flex items-center justify-center gap-2.5 mt-5">
                    <button
                      onClick={() => initActiveRepo()}
                      disabled={repoInitBusy}
                      className="px-3 py-1.5 rounded-lg text-xs font-medium bg-accentBlue text-slate-100 hover:bg-accentBlue/80 transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
                    >
                      {repoInitBusy ? 'Initializing…' : 'Init .devtop'}
                    </button>
                    <button
                      onClick={() => removeRepo(activeRepo)}
                      disabled={repos.length <= 1}
                      title={repos.length <= 1 ? 'Keep at least one registered repo' : 'Unregister this repo'}
                      className="px-3 py-1.5 rounded-lg text-xs font-medium text-rose-300 border border-rose-500/40 hover:bg-rose-500/10 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      Remove
                    </button>
                    <button
                      onClick={() => navigateTo('/repos')}
                      className="px-3 py-1.5 rounded-lg text-xs font-medium text-slate-400 border border-borderDark hover:bg-borderDark/20 transition-colors"
                    >
                      Manage repos…
                    </button>
                  </div>
                  {repoRemoveError && (
                    <p className="text-xs text-rose-300 mt-3 text-center font-mono">{repoRemoveError}</p>
                  )}
                </div>
              </div>
            ) : isReposPage ? (
              <div className="max-w-5xl mx-auto mt-8 fade-in">
                <div className="flex items-center justify-between">
                  <div>
                    <h1 className="text-2xl font-bold text-slate-100">Repositories</h1>
                    <p className="text-xs text-slate-400 mt-1">Registered on this instance only — each repo keeps its own <span className="font-mono">.devtop/</span>.</p>
                  </div>
                  <button
                    onClick={() => setShowAddRepo(true)}
                    className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium bg-accentBlue text-slate-100 hover:bg-accentBlue/80 transition-colors"
                  >
                    <Plus className="w-3.5 h-3.5" />
                    Add repo…
                  </button>
                </div>
                <div className="mt-6 rounded-xl border border-borderDark/40 bg-surfaceDark/40 shadow-2xl overflow-hidden">
                  <table className="w-full text-left">
                    <thead>
                      <tr className="text-[10px] uppercase tracking-widest text-slate-500 border-b border-borderDark/30">
                        <th className="px-6 py-3 font-semibold">Repo</th>
                        <th className="px-4 py-3 font-semibold">Branch</th>
                        <th className="px-4 py-3 font-semibold">Status</th>
                        <th className="px-4 py-3 font-semibold text-right">Docs</th>
                        <th className="px-4 py-3 font-semibold text-right">Pending</th>
                        <th className="px-4 py-3"></th>
                      </tr>
                    </thead>
                    <tbody>
                      {repos.map(r => {
                        const meta = REPO_STATUS_META[r.status] ?? REPO_STATUS_META.ready
                        const active = r.name === activeRepo
                        return (
                          <tr key={r.name} className="border-b border-borderDark/30 last:border-b-0 hover:bg-bgDark/30 transition-colors group">
                            <td className="px-6 py-3.5">
                              <div className="flex items-center gap-2.5">
                                <span className="flex items-center justify-center h-7 w-7 rounded-lg bg-borderDark/40 text-slate-400 flex-shrink-0">
                                  <GitBranch className="w-3.5 h-3.5" />
                                </span>
                                <div className="min-w-0">
                                  <div className="text-xs font-semibold text-slate-100 flex items-center gap-2">
                                    {r.name}
                                    {active && (
                                      <span className="inline-flex items-center gap-1 px-1.5 py-px rounded text-[9px] font-medium uppercase tracking-wider bg-accentBlue/15 border border-accentBlue/30 text-accentBlue">
                                        active <Check className="w-2.5 h-2.5" />
                                      </span>
                                    )}
                                  </div>
                                  <div className="text-[10px] font-mono text-slate-600 truncate">{r.path}</div>
                                </div>
                              </div>
                            </td>
                            <td className="px-4 py-3.5">
                              <span className="font-mono text-[10px] text-slate-400 bg-borderDark/40 px-1.5 py-0.5 rounded-md border border-borderDark/40">{r.branch || '—'}</span>
                            </td>
                            <td className="px-4 py-3.5">
                              <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-medium border capitalize ${
                                r.status === 'ready' ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
                                : r.status === 'dirty' ? 'border-amber-500/30 bg-amber-500/10 text-amber-300'
                                : r.status === 'uninit' ? 'border-slate-500/40 bg-slate-500/10 text-slate-400'
                                : 'border-rose-500/30 bg-rose-500/10 text-rose-300'
                              }`}>
                                <span className={`w-1 h-1 rounded-full ${meta.dot}`} />
                                {meta.label}
                              </span>
                            </td>
                            <td className="px-4 py-3.5 text-right font-mono text-xs text-slate-400">{r.docs || '—'}</td>
                            <td className="px-4 py-3.5 text-right font-mono text-xs text-slate-500">{r.pending || '—'}</td>
                            <td className="px-4 py-3.5 text-right flex items-center justify-end gap-2">
                              {repoRemoveId === r.name ? (
                                <>
                                  <span className="text-[10px] text-slate-500">Remove?</span>
                                  <button
                                    onClick={() => removeRepo(r.name)}
                                    className="px-2 py-1.5 rounded-lg text-[10px] font-medium text-rose-300 border border-rose-500/40 hover:bg-rose-500/10 transition-colors"
                                  >
                                    Yes
                                  </button>
                                  <button
                                    onClick={() => setRepoRemoveId(null)}
                                    className="px-2 py-1.5 rounded-lg text-[10px] font-medium text-slate-400 border border-borderDark hover:bg-borderDark/20 transition-colors"
                                  >
                                    No
                                  </button>
                                </>
                              ) : (
                                <button
                                  onClick={() => { setRepoRemoveError(''); setRepoRemoveId(r.name) }}
                                  disabled={repos.length <= 1}
                                  title={repos.length <= 1 ? 'Keep at least one registered repo' : `Unregister ${r.name}`}
                                  className="h-7 w-7 rounded-lg border border-borderDark/50 text-slate-500 hover:text-rose-300 hover:border-rose-500/40 hover:bg-rose-500/5 transition-colors flex items-center justify-center opacity-0 group-hover:opacity-100 disabled:opacity-0"
                                >
                                  <Trash2 className="w-3.5 h-3.5" />
                                </button>
                              )}
                            </td>
                            <td className="px-4 py-3.5 text-right">
                              <button
                                onClick={() => selectRepo(r.name)}
                                className="px-3 py-1.5 rounded-lg text-xs font-medium text-slate-300 border border-borderDark hover:bg-borderDark/20 transition-colors opacity-0 group-hover:opacity-100"
                              >
                                Open
                              </button>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
                {repoRemoveError && (
                  <p className="text-xs text-rose-300 mt-3 px-1 font-mono">{repoRemoveError}</p>
                )}
                <p className="text-xs text-slate-600 mt-3 px-1">
                  Add repos as paths only. devtop never auto-discovers directories; a repo appears here only when you register it.
                  Registrations persist in <span className="font-mono">~/.config/devtop/repos.json</span> and survive restarts.
                </p>
              </div>
            ) : (
            <Fragment>

            {/* 1. DOC / ARTIFACT DETAIL VIEW */}
            {isDocumentView && (
              <div className="max-w-3xl mx-auto prose prose-invert fade-in">
                {activePage.kind === 'prds' && activePage.id && (
                  <div className="mb-4 not-prose flex items-center gap-2 flex-wrap">
                    <span className={`inline-flex items-center px-2.5 py-1 rounded-full text-xs font-medium border capitalize ${
                      prdStatus === 'reviewing' ? 'bg-amber-500/10 border-amber-500/20 text-amber-400' :
                      prdStatus === 'approved' ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400' :
                      'bg-blue-500/10 border-blue-500/20 text-blue-400'
                    }`}>{prdStatus ?? 'draft'}</span>
                    {prdStatus === 'reviewing' && (
                      <>
                        <button
                          onClick={() => void prdSetStatus('approved')}
                          className="px-3 py-1.5 rounded-lg text-xs font-medium text-slate-100 bg-accentBlue hover:bg-accentBlue/80 transition-colors"
                        >Approve</button>
                        <button
                          onClick={() => void prdSetStatus('draft')}
                          className="px-3 py-1.5 rounded-lg text-xs font-medium text-slate-300 border border-borderDark hover:bg-borderDark/20 transition-colors"
                        >Request changes</button>
                      </>
                    )}
                    {prdStatusErr && (
                      <span className="text-xs text-rose-400 font-mono">{prdStatusErr}</span>
                    )}
                  </div>
                )}
                {docMissing && docSlugs.length === 0 ? (
                  <div className="p-10 text-center bg-surfaceDark/40 border border-borderDark/40 rounded-xl shadow-2xl">
                    <FileText className="w-8 h-8 mx-auto mb-2 text-slate-600" />
                    <h1 className="text-xl font-bold text-slate-100">No docs yet</h1>
                    <p className="text-sm text-slate-400 mt-2">Ask the chat to document this project, or add a file:</p>
                    <code className="inline-block text-xs text-slate-500 mt-3 px-3 py-1.5 bg-borderDark/20 rounded-lg">.devtop/docs/index.mdx</code>
                  </div>
                ) : docMissing ? (
                  <div className="p-10 text-center">
                    <FileText className="w-8 h-8 mx-auto mb-2 text-slate-600" />
                    <h1 className="text-xl font-bold text-slate-100">Document not found</h1>
                    <a
                      href="#/docs"
                      onClick={(e) => { e.preventDefault(); navigateTo('/docs') }}
                      className="inline-block mt-3 text-sm text-cyan-400 hover:text-cyan-300"
                    >
                      Back to docs
                    </a>
                  </div>
                ) : historyIdx >= 0 ? (
                  <div>
                    {!historyAt ? (
                      <div className="p-10 text-center text-xs text-slate-500">Loading revision…</div>
                    ) : (<>
                    <div className="flex items-center justify-between mb-4">
                      <p className="text-[11px] text-slate-500 whitespace-nowrap">
                        <span className="text-accentBlue font-mono">{revisions[historyIdx]?.short}</span> · {formatRevisionDate(revisions[historyIdx]?.date || '')} ·{' '}
                        <button onClick={showCurrent} className="text-accentBlue hover:text-blue-300 underline underline-offset-2">View current →</button>
                      </p>
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => selectRevision(historyIdx + 1)}
                          disabled={historyIdx + 1 >= revisions.length}
                          className="h-7 w-7 rounded-lg border border-borderDark/50 text-slate-400 hover:text-slate-100 hover:border-accentBlue/50 hover:bg-accentBlue/10 flex items-center justify-center transition-colors disabled:opacity-30 disabled:hover:border-borderDark/50 disabled:hover:text-slate-400 disabled:hover:bg-transparent"
                          title="Older"
                        >‹</button>
                        <button
                          onClick={() => selectRevision(historyIdx - 1)}
                          disabled={historyIdx - 1 < 0}
                          className="h-7 w-7 rounded-lg border border-borderDark/50 text-slate-400 hover:text-slate-100 hover:border-accentBlue/50 hover:bg-accentBlue/10 flex items-center justify-center transition-colors disabled:opacity-30 disabled:hover:border-borderDark/50 disabled:hover:text-slate-400 disabled:hover:bg-transparent"
                          title="Newer"
                        >›</button>
                      </div>
                    </div>

                    {historyDiff && (
                      <div className="mb-6 rounded-xl border border-borderDark/40 bg-surfaceDark/30 overflow-hidden not-prose">
                        <div className="flex items-center justify-between px-4 py-2.5 border-b border-borderDark/40 bg-surfaceDark/50">
                          <span className="text-[10px] font-semibold text-slate-400 uppercase tracking-widest">What this commit changed</span>
                        </div>
                        <div className="max-h-56 overflow-y-auto py-1">
                          <DiffView
                            data={{ hunks: [historyDiff] }}
                            diffViewMode={DiffModeEnum.Unified}
                            diffViewTheme="dark"
                            diffViewHighlight={false}
                            diffViewFontSize={12}
                          />
                        </div>
                      </div>
                    )}

                    <div className="text-slate-300 space-y-4 prose-custom prose-history">
                      {historyAt.deleted ? (
                        <p className="text-sm text-red-400">This file was deleted at this commit.</p>
                      ) : (
                        <RichMarkdown source={historyAt.content} />
                      )}
                    </div>
                    </>)}
                  </div>
                ) : (
                  <div className="text-slate-300 space-y-4 prose-custom">
                    <RichMarkdown source={docContent} />
                  </div>
                )}
              </div>
            )}

            {/* 1.45. DERIVATION PIPELINE VIEW — cross-kind, from /api/pipeline */}
            {isPipelinePage && (
              <PipelineView refreshKey={navRevision} />
            )}

            {/* 1.5. LIST OVERVIEW VIEW — any config-declared "list" kind */}
            {isListOverviewPage && (
              <div className="max-w-4xl mx-auto fade-in">
                <div className="flex items-center justify-between mb-6">
                  <div>
                    <h1 className="text-2xl font-bold text-slate-100">{activeKindLabel}</h1>
                    <p className="text-xs text-slate-400 mt-1">{(artifactLists[activePage.kind] || []).length} items</p>
                  </div>
                  <span className="text-xs text-slate-500 italic bg-borderDark/20 px-3 py-1.5 rounded-lg border border-borderDark/40">
                    Declared in .devtop/config.yml
                  </span>
                </div>

                <div className="bg-surfaceDark/40 border border-borderDark/40 rounded-xl overflow-hidden shadow-2xl">
                  {(artifactLists[activePage.kind] || []).length === 0 ? (
                    <div className="p-10 text-center">
                      <FileText className="w-8 h-8 mx-auto mb-2 text-slate-600" />
                      <p className="text-sm text-slate-400 font-medium">No {activeKindLabel} yet</p>
                      <p className="text-xs text-slate-500 mt-1">Create a file in .devtop/{activeKindDef?.path}</p>
                    </div>
                  ) : (
                    (artifactLists[activePage.kind] || []).map(item => (
                      <a
                        key={item.id}
                        href={`#/${activePage.kind}/${item.id}`}
                        onClick={(e) => { e.preventDefault(); navigateTo(`/${activePage.kind}/${item.id}`) }}
                        className="flex items-center justify-between px-5 py-3.5 hover:bg-borderDark/20 cursor-pointer transition-colors border-b border-borderDark/30 last:border-b-0"
                      >
                        <div className="flex items-center gap-3 min-w-0">
                          <FileText className="w-4 h-4 text-slate-600 flex-shrink-0" />
                          <span className="text-slate-200 font-medium truncate">{item.title}</span>
                        </div>
                        {typeof item.status === 'string' && (
                          <span className={`inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border capitalize flex-shrink-0 ${
                            item.status === 'approved' ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400' :
                            item.status === 'reviewing' ? 'bg-amber-500/10 border-amber-500/20 text-amber-400' :
                            item.status === 'archived' ? 'bg-purple-500/10 border-purple-500/20 text-purple-400' :
                            'bg-blue-500/10 border-blue-500/20 text-blue-400'
                          }`}>
                            {item.status}
                          </span>
                        )}
                      </a>
                    ))
                  )}
                </div>
              </div>
            )}

            {/* 2. TICKET BOARD VIEW */}
            {isTicketBoardPage && (
              <div className="max-w-4xl mx-auto fade-in">
                <div className="flex items-center justify-between mb-6">
                  <div>
                    <h1 className="text-2xl font-bold text-slate-100">Ticket Board</h1>
                    <p className="text-xs text-slate-400 mt-1">{tickets.length} tickets</p>
                  </div>
                  <span className="text-xs text-slate-500 italic bg-borderDark/20 px-3 py-1.5 rounded-lg border border-borderDark/40">
                    Use CopilotChat to create, modify or delete tickets
                  </span>
                </div>

                <div className="bg-surfaceDark/40 border border-borderDark/40 rounded-xl overflow-hidden shadow-2xl">
                  <table className="w-full text-left">
                    <thead>
                      <tr className="border-b border-borderDark/50 text-[10px] uppercase font-semibold text-slate-500 tracking-wider bg-surfaceDark/20">
                        <th className="px-5 py-3">ID</th>
                        <th className="px-5 py-3">Title</th>
                        <th className="px-5 py-3">Status</th>
                        <th className="px-5 py-3">Priority</th>
                        <th className="px-5 py-3">Assignee</th>
                        <th className="px-5 py-3">Created</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-borderDark/30 text-xs">
                      {tickets.map(tkt => (
                        <tr 
                          key={tkt.id}
                          onClick={() => navigateTo(`/tickets/${tkt.id}`)}
                          className="hover:bg-borderDark/20 cursor-pointer transition-colors"
                        >
                          <td className="px-5 py-3.5 font-mono text-[11px] text-slate-500">dk-{tkt.id}</td>
                          <td className="px-5 py-3.5 text-slate-200 font-medium">{tkt.title}</td>
                          <td className="px-5 py-3.5">
                            <span className={`inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border capitalize ${
                              tkt.status === 'open' ? 'bg-blue-500/10 border-blue-500/20 text-blue-400' :
                              tkt.status === 'in-progress' ? 'bg-amber-500/10 border-amber-500/20 text-amber-400' :
                              tkt.status === 'done' ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400' :
                              'bg-purple-500/10 border-purple-500/20 text-purple-400'
                            }`}>
                              {tkt.status}
                            </span>
                          </td>
                          <td className="px-5 py-3.5">
                            <div className="flex items-center gap-1.5">
                              <span className={`w-1.5 h-1.5 rounded-full ${
                                tkt.priority === 'urgent' ? 'bg-red-500' :
                                tkt.priority === 'high' ? 'bg-orange-500' :
                                tkt.priority === 'medium' ? 'bg-yellow-500' :
                                'bg-gray-500'
                              }`} />
                              <span className="text-slate-400 capitalize text-[11px]">{tkt.priority}</span>
                            </div>
                          </td>
                          <td className="px-5 py-3.5 font-mono text-[11px] text-slate-400">{tkt.assignee || '—'}{tkt.claimed_by ? <span className="text-cyan-600 ml-1.5">claimed: {tkt.claimed_by}</span> : null}</td>
                          <td className="px-5 py-3.5 text-slate-500">{tkt.created}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )}

            {/* 3. TICKET DETAIL VIEW */}
            {isTicketDetailPage && activeTicket && (
              <div className="max-w-3xl mx-auto fade-in">
                <div className="flex items-center gap-2 mb-4">
                  <a 
                    href="#/tickets" 
                    onClick={(e) => { e.preventDefault(); navigateTo('/tickets') }}
                    className="text-slate-400 hover:text-slate-200 text-xs flex items-center gap-1"
                  >
                    <ArrowLeft className="w-4 h-4" />
                    Back to Board
                  </a>
                  <span className="text-slate-600 text-xs">/</span>
                  <span className="text-slate-400 font-mono text-xs font-semibold">dk-{activeTicket.id}</span>
                </div>

                <div className="border-b border-borderDark/40 pb-5 mb-6">
                  <h1 className="text-2xl font-bold text-white tracking-tight mb-3">{activeTicket.title}</h1>
                  <div className="flex flex-wrap items-center gap-4 text-xs text-slate-400">
                    <span className={`inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border capitalize ${
                      activeTicket.status === 'open' ? 'bg-blue-500/10 border-blue-500/20 text-blue-400' :
                      activeTicket.status === 'in-progress' ? 'bg-amber-500/10 border-amber-500/20 text-amber-400' :
                      activeTicket.status === 'done' ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400' :
                      'bg-purple-500/10 border-purple-500/20 text-purple-400'
                    }`}>
                      {activeTicket.status}
                    </span>
                    <span className="flex items-center gap-1.5">
                      <span className={`w-1.5 h-1.5 rounded-full ${
                        activeTicket.priority === 'urgent' ? 'bg-red-500' :
                        activeTicket.priority === 'high' ? 'bg-orange-500' :
                        activeTicket.priority === 'medium' ? 'bg-yellow-500' :
                        'bg-gray-500'
                      }`} />
                      <span className="capitalize">{activeTicket.priority} priority</span>
                    </span>
                    <span className="text-slate-600">|</span>
                    <span>Assignee: <span className="font-mono text-slate-300">{activeTicket.assignee || 'Unassigned'}</span></span>
                    <span className="text-slate-600">|</span>
                    <span className="text-slate-500">Created {activeTicket.created}</span>
                  </div>
                </div>

                <div className="bg-surfaceDark/30 border border-borderDark/40 rounded-xl p-6 mb-8 prose-custom">
                  <RichMarkdown source={activeTicket.raw_description || ''} />
                </div>

                <div>
                  <h2 className="text-xs font-semibold text-slate-400 uppercase tracking-widest mb-4">Comments</h2>
                  <div className="space-y-4">
                    {(activeTicket.comments || []).map((comment, index) => (
                      <div key={index} className="bg-surfaceDark/20 border border-borderDark/30 rounded-xl p-4">
                        <div className="flex items-center justify-between mb-1.5">
                          <span className="font-semibold text-slate-200 text-xs">{comment.author || 'Agent'}</span>
                          <span className="text-[10px] text-slate-500 font-mono">{comment.date}</span>
                        </div>
                        <p className="text-xs text-slate-400 font-sans">{comment.text}</p>
                      </div>
                    ))}
                    {(!activeTicket.comments || activeTicket.comments.length === 0) && (
                      <div className="text-xs text-slate-500 italic">
                        No comments yet. Ask the Copilot to add one.
                      </div>
                    )}
                  </div>
                </div>
              </div>
            )}

            </Fragment>
            )}
          </div>
          </div>
        </main>

        {/* ===== COPILOT CHAT PANEL ===== */}
        {chatCanMount ? (
          <CopilotKit
            runtimeUrl="/api/copilotkit"
            threadId={activeThreadId}
            headers={activeRepo ? { 'X-Devtop-Repo': activeRepo } : undefined}
            renderToolCalls={[WildcardToolCallRender, ...toolCallRenderers]}
          >
            <PageContextProvider activePage={activePage} docTitle={docTitle} docContent={docContent} activeTicket={activeTicket} tickets={tickets} contextLabel={contextLabel} />
            {chatPanel}
          </CopilotKit>
        ) : (
          chatPanel
        )}

        {/* ===== DOC ACTION MENU (⋯) ===== */}
        {menuAnchor && (
          <DocActionsMenu
            anchor={menuAnchor}
            isFav={favSet.has(menuAnchor.slug)}
            onToggleFav={(slug) => { toggleFavourite(slug); setMenuAnchor(null) }}
            onHistory={openDocHistory}
            onCopyPath={copyDocPath}
            onExport={exportDoc}
            onDelete={deleteDocFile}
            onClose={() => setMenuAnchor(null)}
          />
        )}

        {/* ===== SETTINGS DIALOG ===== */}
        <SettingsModal
          open={showSettings}
          pane={settingsPane}
          onPaneChange={setSettingsPane}
          onClose={() => setShowSettings(false)}
          aiStatus={aiStatus}
          aiProvider={aiProvider}
          aiBaseURLInput={aiBaseURLInput}
          aiModelInput={aiModelInput}
          aiKeyInput={aiKeyInput}
          aiSaving={aiSaving}
          onAiProviderChange={onAiProviderChange}
          onAiBaseURLChange={setAiBaseURLInput}
          onAiModelChange={setAiModelInput}
          onAiKeyChange={setAiKeyInput}
          onSave={saveAiKey}
          onClear={clearAiKey}
        />

      {showAddRepo && (
        <AddRepoModal
          onClose={() => setShowAddRepo(false)}
          onAdded={(name: string) => {
            setShowAddRepo(false)
            refreshRepos().then(() => { if (name) selectRepo(name) })
          }}
        />
      )}
      </div>
  )
}

// Compact display form for git author dates (ISO 8601). The rail is narrow,
// so a full toLocaleString is too wide; monospace "YYYY-MM-DD HH:MM" fits.
function formatRevisionDate(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function PageContextProvider({ activePage, docTitle, docContent, activeTicket, tickets, contextLabel }: {
  activePage: PageLocation
  docTitle: string
  docContent: string
  activeTicket: Ticket | null
  tickets: Ticket[]
  contextLabel: string
}) {
  const contextValue = useMemo(() => {
    if (activePage.kind === 'docs') {
      const slug = activePage.id ?? 'index'
      return `Path: ${slug}\nTitle: ${docTitle}\n\nContent:\n${docContent}`
    }
    if (activePage.kind === 'tickets' && activePage.id && activeTicket) {
      return `Ticket dk-${activeTicket.id}: ${activeTicket.title}\nStatus: ${activeTicket.status}\nPriority: ${activeTicket.priority}\nAssignee: ${activeTicket.assignee || 'Unassigned'}\n\nDescription:\n${activeTicket.raw_description}`
    }
    if (activePage.kind === 'tickets') {
      return `Viewing Ticket Board — ${tickets.length} tickets total`
    }
    return `Viewing page: ${contextLabel}`
  }, [activePage, docTitle, docContent, activeTicket, tickets, contextLabel])

  useAgentContext({
    description: "The currently viewed page content",
    value: contextValue,
  })

  return null
}

// ---------------------------------------------------------------------------
// Settings dialog — a blurred, app-level overlay with a left rail of sections
// and a detail pane to the right (Option B). It is intentionally context-free:
// it does not own a thread, a chat, or any per-context viewstate.
// ---------------------------------------------------------------------------
const SETTINGS_SECTIONS: { key: SettingsPane; label: string; icon: typeof Settings }[] = [
  { key: 'ai', label: 'AI', icon: Key },
  { key: 'appearance', label: 'Appearance', icon: Palette },
  { key: 'data', label: 'Data', icon: Database },
  { key: 'about', label: 'About', icon: Info },
]

function SettingsModal(props: {
  open: boolean
  pane: SettingsPane
  onPaneChange: (p: SettingsPane) => void
  onClose: () => void
  aiStatus: AiStatus
  aiProvider: AIProviderKey
  aiBaseURLInput: string
  aiModelInput: string
  aiKeyInput: string
  aiSaving: boolean
  onAiProviderChange: (p: AIProviderKey) => void
  onAiBaseURLChange: (v: string) => void
  onAiModelChange: (v: string) => void
  onAiKeyChange: (v: string) => void
  onSave: () => void
  onClear: () => void
}) {
  const { open, pane, onPaneChange, onClose, aiStatus } = props
  const keyInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  // Focus the API-key field when the AI pane opens, so setup is one keystroke.
  useEffect(() => {
    if (open && pane === 'ai') {
      const t = setTimeout(() => keyInputRef.current?.focus(), 60)
      return () => clearTimeout(t)
    }
  }, [open, pane])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Frosted backdrop covers nav + doc + chat uniformly */}
      <div className="absolute inset-0 bg-black/50 backdrop-blur-md" onClick={onClose} aria-hidden="true" />

      <div
        role="dialog"
        aria-modal="true"
        aria-label="Settings"
        className="relative flex flex-col w-[min(860px,92vw)] h-[min(620px,80vh)] bg-[#0a0e18] border border-borderDark/80 rounded-2xl shadow-2xl overflow-hidden"
      >
        <div className="h-[57px] px-5 border-b border-borderDark/40 flex items-center justify-between bg-surfaceDark/20 flex-shrink-0">
          <span className="text-sm font-semibold text-slate-100">Settings</span>
          <button
            onClick={onClose}
            aria-label="Close settings"
            title="Close (Esc)"
            className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 flex items-center justify-center text-slate-400 hover:text-slate-100 transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="flex flex-1 min-h-0">
          <nav className="w-44 flex-shrink-0 border-r border-borderDark/40 p-2 flex flex-col gap-1">
            {SETTINGS_SECTIONS.map(s => {
              const Icon = s.icon
              return (
                <button
                  key={s.key}
                  onClick={() => onPaneChange(s.key)}
                  className={`flex items-center gap-2 px-2.5 py-2 rounded-lg text-xs font-medium transition-colors ${
                    pane === s.key
                      ? 'bg-accentBlue/15 text-slate-100 border border-accentBlue/30'
                      : 'text-slate-400 hover:text-slate-100 hover:bg-borderDark/20 border border-transparent'
                  }`}
                >
                  <Icon className="w-4 h-4 flex-shrink-0" />
                  {s.label}
                </button>
              )
            })}
          </nav>

          <div className="flex-1 min-w-0 overflow-y-auto p-6">
            {pane === 'ai' && (
              <div className="max-w-lg mx-auto">
                <div className="flex items-center gap-2 mb-1">
                  <Key className="w-4 h-4 text-accentBlue" />
                  <h3 className="text-sm font-semibold text-slate-100">AI provider</h3>
                </div>
                <p className="text-[11px] text-slate-500 mb-5 leading-relaxed">
                  devtop uses CopilotKit to read, create, and update docs and tickets. Connect a provider to enable the chat assistant.
                </p>

                {aiStatus?.configured ? (
                  <div className="mb-5 flex items-center justify-between rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-3">
                    <div className="text-[11px]">
                      <p className="font-medium text-emerald-300">AI assistant configured</p>
                      <p className="text-slate-500 mt-0.5">
                        {[aiStatus.model, aiStatus.baseURL].filter(Boolean).join(' · ') || '—'}
                      </p>
                    </div>
                    <button onClick={props.onClear} className="text-[10px] text-red-400 hover:text-red-300 flex-shrink-0">
                      Remove key
                    </button>
                  </div>
                ) : (
                  <div className="mb-5 rounded-xl border border-amber-500/30 bg-amber-500/5 p-3 text-[11px] text-slate-400">
                    No AI key configured yet — add one below.
                  </div>
                )}

                <label className="block text-[10px] font-semibold text-slate-500 uppercase tracking-widest mb-1">Provider</label>
                <div className="grid grid-cols-3 gap-1.5 mb-3">
                  {(Object.keys(AI_PROVIDERS) as AIProviderKey[]).map(p => (
                    <button
                      key={p}
                      onClick={() => props.onAiProviderChange(p)}
                      className={`px-2 py-1.5 rounded-lg border text-[11px] font-medium transition-colors ${
                        props.aiProvider === p
                          ? 'bg-accentBlue/15 border-accentBlue/40 text-slate-100'
                          : 'border-borderDark/50 text-slate-400 hover:bg-borderDark/30'
                      }`}
                    >
                      {AI_PROVIDERS[p].label.split(' ')[0]}
                    </button>
                  ))}
                </div>

                <label className="block text-[10px] font-semibold text-slate-500 uppercase tracking-widest mb-1">Base URL</label>
                <input
                  type="text"
                  value={props.aiBaseURLInput}
                  onChange={(e) => props.onAiBaseURLChange(e.target.value)}
                  placeholder={AI_PROVIDERS.openrouter.baseURL}
                  aria-label="AI base URL"
                  className="w-full bg-[#0c101f] border border-borderDark/60 rounded px-2 py-1.5 text-[11px] text-slate-200 placeholder-slate-500 outline-none focus:border-accentBlue/60 mb-2.5 font-mono"
                />

                <label className="block text-[10px] font-semibold text-slate-500 uppercase tracking-widest mb-1">Model</label>
                <input
                  type="text"
                  value={props.aiModelInput}
                  onChange={(e) => props.onAiModelChange(e.target.value)}
                  placeholder={AI_PROVIDERS.openrouter.model}
                  aria-label="AI model"
                  className="w-full bg-[#0c101f] border border-borderDark/60 rounded px-2 py-1.5 text-[11px] text-slate-200 placeholder-slate-500 outline-none focus:border-accentBlue/60 mb-2.5 font-mono"
                />

                <label className="block text-[10px] font-semibold text-slate-500 uppercase tracking-widest mb-1">
                  API key {props.aiProvider === 'lmstudio' && <span className="normal-case tracking-normal">(any value — local server)</span>}
                </label>
                <div className="flex gap-1.5 mb-2.5">
                  <input
                    ref={keyInputRef}
                    type="password"
                    value={props.aiKeyInput}
                    onChange={(e) => props.onAiKeyChange(e.target.value)}
                    onKeyDown={(e) => { if (e.key === 'Enter') props.onSave() }}
                    placeholder="sk-…"
                    aria-label="AI API key"
                    className="flex-1 min-w-0 bg-[#0c101f] border border-borderDark/60 rounded px-2 py-1.5 text-[11px] text-slate-200 placeholder-slate-500 outline-none focus:border-accentBlue/60 font-mono"
                  />
                  <button
                    onClick={props.onSave}
                    disabled={!props.aiKeyInput.trim() || props.aiSaving}
                    className="px-3 rounded bg-accentBlue text-white text-[11px] font-medium hover:bg-blue-600 disabled:opacity-40"
                  >
                    {props.aiSaving ? 'Saving…' : 'Save'}
                  </button>
                </div>

                {aiStatus && !aiStatus.remembered && (
                  <p className="text-[10px] text-slate-500">
                    Session-only — add <code className="font-mono">-v devtop-ai-config:/etc/devtop</code> to remember the key across restarts.
                  </p>
                )}
              </div>
            )}

            {(pane === 'appearance' || pane === 'data' || pane === 'about') && (
              <div className="max-w-lg mx-auto">
                <h3 className="text-sm font-semibold text-slate-100">{SETTINGS_SECTIONS.find(s => s.key === pane)!.label}</h3>
                <p className="text-[11px] text-slate-500 mt-1">
                  This section doesn't have any settings yet. It's a placeholder until it does.
                </p>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

export default App
