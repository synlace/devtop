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
  MessageSquare,
  Plus,
  Trash2,
  Key,
  Settings,
  X,
  Palette,
  Database,
  Info
} from 'lucide-react'
import { CopilotKit, CopilotChat } from '@copilotkit/react-core/v2'
import { useAgentContext } from '@copilotkit/react-core/v2'
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
  created: string
  description?: string
  raw_description?: string
  comments?: Array<{
    date: string
    author: string
    text: string
  }>
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
    docs: {
      path: 'docs',
      extension: '.mdx',
      agent_writable: true,
      view: 'mdx',
      nav: { label: 'Docs', icon: 'file', order: 1, view: 'tree' },
    },
    tickets: {
      path: 'tickets',
      extension: '.md',
      agent_writable: true,
      view: 'board',
      nav: { label: 'Tickets', icon: 'board', order: 2, view: 'board' },
    },
    prds: {
      path: 'prds',
      extension: '.mdx',
      agent_writable: true,
      requires_approval: true,
      view: 'list',
      nav: { label: 'PRDs', icon: 'doc', order: 3, view: 'list' },
    },
  },
  derivation: [
    { from: 'docs', to: 'prds', transform: 'breakdown' },
    { from: 'prds', to: 'tickets', transform: 'derive_tickets', gate: 'prds.status == approved' },
  ],
  replan: { detect: 'git_diff', stale_badge: true },
  handoff: { contract: 'tickets/*.md + this config', grabbable: [], lifecycle_owner: 'external' },
}

// Provider presets shown in the AI config wizard. LM Studio is keyless (the
// runtime still wants a non-empty key, so "lm-studio" is sent as a sentinel).
const AI_PROVIDERS = {
  openrouter: { label: 'OpenRouter', baseURL: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini' },
  lmstudio: { label: 'LM Studio (local)', baseURL: 'http://localhost:1234/v1', model: 'lmstudio-community/llama-3.2-3b-instruct' },
  custom: { label: 'Custom (OpenAI-compatible)', baseURL: '', model: '' },
} as const
type AIProviderKey = keyof typeof AI_PROVIDERS

// Sections in the settings dialog (left rail). AI is the only populated pane
// today; the others are placeholders until they gain real controls.
type SettingsPane = 'ai' | 'appearance' | 'data' | 'about'
type AiStatus = { configured: boolean; remembered: boolean; baseURL?: string; model?: string } | null

function App() {
  // Routing / UI State
  const [activePage, setActivePage] = useState<PageLocation>({ kind: 'docs', id: 'index' })
  const [docTitle, setDocTitle] = useState<string>('Home')
  const [docContent, setDocContent] = useState<string>('')
  const [docMissing, setDocMissing] = useState<boolean>(false)
  const [docSlugs, setDocSlugs] = useState<DocSlug[]>([])
  const [collapsedSections, setCollapsedSections] = useState<Set<string>>(new Set())
  
  const [tickets, setTickets] = useState<Ticket[]>([])
  const [activeTicket, setActiveTicket] = useState<Ticket | null>(null)
  // Generic kind → artifact rows for list-view kinds (e.g. PRDs).
  const [artifactLists, setArtifactLists] = useState<Record<string, ArtifactItem[]>>({})
  
  // Chat Panel Resizing & Layout
  const [chatWidth, setChatWidth] = useState<number>(384)
  const [isFullscreen, setIsFullscreen] = useState<boolean>(false)
  
  // Thread State
  const [threads, setThreads] = useState<ThreadSummary[]>([])
  const [activeThreadId, setActiveThreadId] = useState<string | undefined>(undefined)
  const [showThreadList, setShowThreadList] = useState<boolean>(false)
  const contextThreadState = useRef<Record<string, {activeThreadId?: string; showThreadList: boolean}>>({})
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

  useEffect(() => {
    let cancelled = false
    fetch('/api/engine-config')
      .then(r => r.ok ? r.json() : Promise.reject(new Error(`engine-config ${r.status}`)))
      .then((cfg: EngineConfig) => {
        if (!cancelled && cfg && cfg.artifact_kinds && Object.keys(cfg.artifact_kinds).length > 0) {
          setEngineConfig(cfg)
        }
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

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
    fetchTicketsList()
    handleHashChange()

    return () => {
      window.removeEventListener('hashchange', handleHashChange)
    }
  }, [])

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
  // artifact. Threads and viewstate are keyed by this.
  const contextKey = useMemo(() => {
    if (activePage.id) return activePage.kind + '/' + activePage.id
    return activePage.kind
  }, [activePage])

  const breadcrumbItems = useMemo(() => {
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
      const r = await fetch('/api/docs')
      if (r.ok) {
        const data = await r.json()
        setDocSlugs(data)
      }
    } catch (e) {
      console.error('Failed to fetch doc slugs:', e)
    }
  }

  const fetchTicketsList = async () => {
    try {
      const r = await fetch('/api/tickets')
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
      const r = await fetch(`/api/docs/${slug}`)
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

  const fetchTicketDetail = async (id: string) => {
    try {
      const r = await fetch(`/api/tickets/${id}`)
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
      const r = await fetch(`/api/artifacts/${encodeURIComponent(kind)}`)
      if (r.ok) {
        const data = await r.json()
        setArtifactLists(prev => ({ ...prev, [kind]: data }))
      }
    } catch (e) {
      console.error('Failed to fetch artifact list:', e)
    }
  }

  const fetchArtifactDetail = async (kind: string, id: string) => {
    try {
      const r = await fetch(`/api/artifacts/${encodeURIComponent(kind)}/${id}`)
      if (r.ok) {
        const data = await r.json()
        setDocTitle(data.title)
        setDocContent(data.content)
      } else {
        setDocTitle('Not Found')
        setDocContent('')
        setDocMissing(true)
      }
    } catch (e) {
      console.error('Failed to load artifact:', e)
    }
  }

  // -------------------------------------------------------------
  // 5b. Thread API Operations
  // -------------------------------------------------------------
  const fetchThreads = useCallback(async (context: string) => {
    try {
      const r = await fetch(`/api/threads?context=${encodeURIComponent(context)}`)
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
      await fetch('/api/viewstate', {
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
      const r = await fetch('/api/viewstate')
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
    console.log('[thread] createNewThread', { context, contextLabel })
    try {
      const r = await fetch('/api/threads', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ context, title: contextLabel + ' discussion' }),
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
  }, [contextLabel, fetchThreads])

  const deleteThread = useCallback(async (threadId: string) => {
    console.log('[thread] delete', { threadId, wasActive: threadId === activeThreadId })
    try {
      await fetch(`/api/threads/${threadId}`, { method: 'DELETE' })
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
    console.log('[thread] autoCreate', { context, reason: 'no saved state found' })
    try {
      const r = await fetch('/api/threads', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ context, title: context + ' discussion' }),
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
  }, [fetchThreads])

useEffect(() => {
    console.log('[effect] contextSwitch', { contextKey, viewStateLoaded, isNavigating: prevContextKey.current !== '' && prevContextKey.current !== contextKey, prev: prevContextKey.current })
    // Do nothing until disk-persisted view state has been loaded —
    // prevents wiping/overwriting saved state with default values.
    if (!viewStateLoaded) return

    const isNavigation = prevContextKey.current !== '' && prevContextKey.current !== contextKey
    if (isNavigation) {
      console.log('[effect] flushing context', { from: prevContextKey.current, to: contextKey, state: { activeThreadId, showThreadList } })
      // Flush the context we're leaving with its live values
      saveChatScrollPos()
      contextThreadState.current[prevContextKey.current] = { activeThreadId, showThreadList }
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
      autoCreateThread(contextKey)
    }
    fetchThreads(contextKey)
  }, [contextKey, viewStateLoaded, fetchThreads, saveChatScrollPos])

  // Save view state to disk whenever thread state changes within the same context
  useEffect(() => {
    if (viewStateLoaded) {
      console.log('[effect] saveOnChange', { contextKey, activeThreadId, showThreadList })
      contextThreadState.current[contextKey] = { activeThreadId, showThreadList }
      saveViewState()
    }
  }, [activeThreadId, showThreadList, viewStateLoaded])

  // Refresh the thread list every time it is opened, so message counts and
  // previews reflect the conversation that just happened.
  useEffect(() => {
    if (showThreadList) {
      fetchThreads(contextKey)
    }
  }, [showThreadList, contextKey, fetchThreads])

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

  const chatPanel = (
        <aside 
          ref={chatPanelRef}
          data-testid="copilot-chat-panel"
          className={`bg-[#090c15] border-l border-borderDark/60 flex-shrink-0 flex flex-col z-20 shadow-2xl ${
            isFullscreen ? 'fixed inset-0 w-full h-full z-50' : 'relative'
          }`}
          style={!isFullscreen ? { width: `${chatWidth}px` } : {}}
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
                {contextLabel}
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
  )

  // Nav sections are data-driven from the engine config: one section per kind
  // that declares nav. Views are engine capabilities (tree, board); unknown
  // views render nothing until the engine implements them.
  const navSections = useMemo(() =>
    Object.entries(engineConfig.artifact_kinds)
      .map(([kind, def]) => ({ kind, def }))
      .filter(e => !!e.def.nav)
      .sort((a, b) => (a.def.nav!.order ?? 99) - (b.def.nav!.order ?? 99)),
    [engineConfig],
  )

  const renderNavSection = (kind: string, nav: EngineNav) => {
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
                    <a
                      key={node.slug}
                      href={`#/docs/${node.slug}`}
                      onClick={(e) => { e.preventDefault(); navigateTo(`/docs/${node.slug}`) }}
                      style={{ paddingLeft: `${12 + depth * 16}px` }}
                      className={`flex items-center gap-2.5 py-1.5 rounded-lg text-xs font-medium transition-all duration-150 border ${
                        activePage.id === node.slug && isDocPage
                          ? 'bg-accentBlue/10 text-slate-100 border-accentBlue/20'
                          : 'text-slate-500 hover:text-slate-200 hover:bg-borderDark/20 border-transparent'
                      }`}
                    >
                      <FileText className="w-4 h-4 text-slate-600 flex-shrink-0" />
                      <span className="truncate">{node.title}</span>
                    </a>
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
                          className="truncate hover:text-slate-100 transition-colors"
                        >
                          {label}
                        </a>
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
            <div className="flex items-center gap-2 text-xs font-medium text-slate-400">
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
          </header>

          <div className="flex-1 p-8 overflow-y-auto">
            
            {/* 1. DOC / ARTIFACT DETAIL VIEW */}
            {isDocumentView && (
              <div className="max-w-3xl mx-auto prose prose-invert fade-in">
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
                ) : (
                  <div className="text-slate-300 space-y-4 prose-custom">
                    <RichMarkdown source={docContent} />
                  </div>
                )}
              </div>
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
                          <td className="px-5 py-3.5 font-mono text-[11px] text-slate-400">{tkt.assignee || '—'}</td>
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

          </div>
        </main>

        {/* ===== COPILOT CHAT PANEL ===== */}
        {chatReady ? (
          <CopilotKit runtimeUrl="/api/copilotkit" threadId={activeThreadId}>
            <PageContextProvider activePage={activePage} docTitle={docTitle} docContent={docContent} activeTicket={activeTicket} tickets={tickets} contextLabel={contextLabel} />
            {chatPanel}
          </CopilotKit>
        ) : (
          chatPanel
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
      </div>
  )
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
      return `Title: ${docTitle}\n\nContent:\n${docContent}`
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
