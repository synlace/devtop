import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import Tree from 'rc-tree'
import { Database, Folder, FolderOpen, Info, X } from 'lucide-react'

// Add-repo folder browser. The backend owns the filesystem: GET /api/fs/list
// returns the directories of a path (with a has_git flag), and POST /api/repos
// registers the selected root. The browser never sees an absolute path beyond
// what the server serves, and rc-tree loads children lazily per expand.

interface FSEntry {
  name: string
  dir: boolean
  has_git: boolean
  has_devtop: boolean
  has_subdirs: boolean
}

interface FSResponse {
  path: string
  name: string
  entries: FSEntry[]
}

interface TreeNode {
  key: string
  title: string
  isLeaf: boolean
  has_git?: boolean
  has_devtop?: boolean
  children?: TreeNode[]
}

interface AddRepoModalProps {
  onClose: () => void
  onAdded: (name: string) => void
}

const CHEVRON = 'M9 5l7 7-7 7'

// findNodeByKey locates a tree node by its absolute path key. Module-level:
// it is pure and used from a memoized lookup.
function findNodeByKey(nodes: TreeNode[], key: string): TreeNode | undefined {
  for (const n of nodes) {
    if (n.key === key) return n
    if (n.children) {
      const hit = findNodeByKey(n.children, key)
      if (hit) return hit
    }
  }
  return undefined
}

function AddRepoModal({ onClose, onAdded }: AddRepoModalProps) {
  const [treeData, setTreeData] = useState<TreeNode[]>([])
  const [selected, setSelected] = useState('')
  const [manualPath, setManualPath] = useState('')
  const [error, setError] = useState('')
  const [adding, setAdding] = useState(false)
  const [justAdded, setJustAdded] = useState(false)
  // A browsed folder with neither .git nor .devtop is most likely a subfolder
  // pick — registering it as a repo root would surface an immediate "not
  // initialized" dead end. Require an explicit second click for those.
  const [addConfirm, setAddConfirm] = useState(false)

  const dataRef = useRef<TreeNode[]>(treeData)
  dataRef.current = treeData

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const listDir = useCallback(async (path: string): Promise<TreeNode[]> => {
    try {
      const r = await fetch(`/api/fs/list?path=${encodeURIComponent(path)}`)
      if (!r.ok) return []
      const resp: FSResponse = await r.json()
      return resp.entries
        .filter(e => e.dir)
        .map(e => ({ key: resp.path === '/' ? '/' + e.name : resp.path + '/' + e.name, title: e.name, isLeaf: !e.has_subdirs, has_git: e.has_git, has_devtop: e.has_devtop }))
    } catch {
      return []
    }
  }, [])

  const loadData = useCallback(async (node: { key: React.Key }) => {
    // rc-tree hands loadData a shallow copy of the node, so mutations are
    // lost; rebuild the tree immutably instead.
    const children = await listDir(String(node.key))
    const patch = (nodes: TreeNode[]): TreeNode[] => nodes.map(n =>
      n.key === node.key
        ? { ...n, children, isLeaf: children.length === 0 }
        : n.children ? { ...n, children: patch(n.children) } : n,
    )
    const next = patch(dataRef.current)
    dataRef.current = next
    setTreeData(next)
  }, [listDir])

  // Seed the browser at the home directory on open.
  useEffect(() => {
    let cancelled = false
    listDir('').then(children => {
      if (!cancelled) {
        dataRef.current = children
        setTreeData(children)
      }
    })
    return () => { cancelled = true }
  }, [listDir])

  const effectivePath = (manualPath.trim() || selected).replace(/\/+$/, '')
  const segments = useMemo(() => selected ? selected.split('/') : [], [selected])
  const canAdd = effectivePath.length > 0 && !adding

  // The browsed node that matches the effective path, if any. A manual path
  // has no metadata and is never gated.
  const selectedNode = useMemo(() => findNodeByKey(treeData, effectivePath), [treeData, effectivePath])
  const needsConfirm = !!selectedNode && !selectedNode.has_git && !selectedNode.has_devtop

  // Any selection or path change resets the armed confirmation.
  useEffect(() => { setAddConfirm(false) }, [selected, manualPath])

  const doAdd = async () => {
    if (!canAdd) return
    if (needsConfirm && !addConfirm) {
      setAddConfirm(true)
      return
    }
    setAddConfirm(false)
    setAdding(true)
    setError('')
    try {
      const r = await fetch('/api/repos', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: effectivePath }),
      })
      if (r.ok) {
        const data = await r.json().catch(() => null)
        const added = data && typeof data.name === 'string' ? data.name : ''
        setJustAdded(true)
        setTimeout(() => onAdded(added), 350)
      } else {
        const data = await r.json().catch(() => null)
        setError((data && data.error) ? String(data.error) : `Failed to register (${r.status})`)
        setAdding(false)
      }
    } catch {
      setError('Failed to reach the devtop server')
      setAdding(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm flex items-center justify-center p-6"
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="doc-menu-pop w-[600px] max-w-full rounded-2xl border border-borderDark/60 bg-[#0a0e1c] shadow-2xl overflow-hidden">
        {/* header */}
        <div className="px-6 pt-5 pb-4 border-b border-borderDark/40 flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-slate-100">Add repository</h2>
            <p className="text-[11px] text-slate-500 mt-1 leading-relaxed">
              Pick the repo’s root folder. The instance stores the absolute path — the browser never touches the filesystem directly.
            </p>
          </div>
          <button
            onClick={onClose}
            className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 text-slate-400 hover:text-slate-100 transition-colors flex items-center justify-center flex-shrink-0"
            title="Close"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>

        {/* path bar */}
        <div className="px-6 pt-4 pb-2">
          <div className="flex items-center gap-2">
            <button
              onClick={() => {
                const up = segments.slice(0, -1).join('/')
                setSelected(up.length > 0 ? up : '')
              }}
              disabled={segments.length === 0}
              className="h-7 w-7 rounded-lg border border-borderDark/50 hover:bg-borderDark/40 text-slate-400 hover:text-slate-100 transition-colors flex items-center justify-center flex-shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
              title="Up one level"
            >
              <svg className="w-3 h-3 -rotate-90" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d={CHEVRON} />
              </svg>
            </button>
            <div className="flex-1 flex items-center gap-1 font-mono text-[11px] bg-bgDark/60 border border-borderDark/60 rounded-lg px-3 py-1.5 min-w-0 overflow-x-auto">
              {segments.length === 0
                ? <span className="text-slate-500">~ / pick a folder below</span>
                : segments.map((seg, i) => (
                    <span key={i} className="flex items-center gap-1 whitespace-nowrap">
                      {i > 0 && <span className="text-slate-600">/</span>}
                      {i < segments.length - 1
                        ? <button
                            onClick={() => setSelected(segments.slice(0, i + 1).join('/'))}
                            className="text-slate-400 hover:text-accentBlue transition-colors"
                          >{seg}</button>
                        : <span className="text-accentBlue">{seg}</span>}
                    </span>
                  ))}
            </div>
          </div>
        </div>

        {/* tree */}
        <div className="px-6 pb-3">
          <div className="h-64 rounded-xl border border-borderDark/60 bg-bgDark/40 p-2 overflow-y-auto">
            <Tree
              treeData={treeData}
              loadData={loadData}
              selectedKeys={selected ? [selected] : []}
              onSelect={(_keys, info) => {
                if (info && info.node) setSelected(String(info.node.key))
              }}
              icon={({ expanded, isLeaf }) => isLeaf
                ? <Folder className="w-3.5 h-3.5 text-slate-500 flex-shrink-0" />
                : expanded
                  ? <FolderOpen className="w-3.5 h-3.5 text-amber-400/90 flex-shrink-0" />
                  : <Folder className="w-3.5 h-3.5 text-slate-400 flex-shrink-0" />}
              switcherIcon={({ expanded, loading, isLeaf }) => isLeaf
                ? <span />
                : loading
                  ? <span className="rc-tree-switcher-loading-icon" />
                  : <svg
                      className={`w-3 h-3 transition-transform ${expanded ? 'rotate-90' : ''}`}
                      fill="none" stroke="currentColor" viewBox="0 0 24 24"
                    >
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d={CHEVRON} />
                    </svg>}
              titleRender={(node) => (
                <span className="flex items-center gap-1.5 min-w-0">
                  <span className="truncate">{node.title}</span>
                  {'has_git' in node && (node as { has_git?: boolean }).has_git && (
                    <span className="ml-auto flex-shrink-0 inline-flex items-center gap-1 px-1.5 py-px rounded-full text-[8px] font-medium border font-mono border-emerald-500/30 bg-emerald-500/10 text-emerald-300">
                      git
                    </span>
                  )}
                </span>
              )}
              motion={null}
            />
          </div>
        </div>

        {/* manual path */}
        <div className="px-6 pb-4">
          <div className="flex items-center gap-2 bg-bgDark/60 border border-borderDark/60 rounded-lg px-3 py-2 focus-within:border-accentBlue/60 transition-colors">
            <Info className="w-3.5 h-3.5 text-slate-600 flex-shrink-0" />
            <input
              value={manualPath}
              onChange={(e) => setManualPath(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') doAdd() }}
              placeholder="or type a path, e.g. ~/dev/new-project"
              className="flex-1 bg-transparent text-[11px] font-mono text-slate-200 placeholder:text-slate-600 focus:outline-none"
            />
          </div>
        </div>

        {/* footer */}
        <div className="px-6 py-3.5 border-t border-borderDark/40 bg-[#06080e]/40 flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 min-w-0">
            {error ? (
              <span className="text-[10px] text-rose-300 font-mono truncate">{error}</span>
            ) : canAdd ? (
              <>
                <span className="font-mono text-[10px] text-slate-500 truncate max-w-[220px]">{effectivePath}</span>
                {justAdded
                  ? <span className="text-[10px] text-emerald-300 font-mono flex items-center gap-1">
                      <Database className="w-3 h-3" /> added
                    </span>
                  : addConfirm
                  ? <span className="flex-shrink-0 inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[9px] font-medium border border-amber-500/40 bg-amber-500/10 text-amber-300">
                      no git or .devtop — confirm?
                    </span>
                  : <span className="flex-shrink-0 inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[9px] font-medium border capitalize border-slate-500/40 bg-slate-500/10 text-slate-400">
                      will register
                    </span>}
              </>
            ) : (
              <span className="text-[10px] text-slate-600">Select a folder to see its status</span>
            )}
          </div>
          <div className="flex items-center gap-2 flex-shrink-0">
            <button
              onClick={onClose}
              className="px-3 py-1.5 rounded-lg text-xs font-medium text-slate-300 border border-borderDark hover:bg-borderDark/20 transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={doAdd}
              disabled={!canAdd}
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-accentBlue text-slate-100 hover:bg-accentBlue/80 transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
            >
              {adding ? 'Adding…' : addConfirm ? 'Add anyway' : 'Add repo'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

export default AddRepoModal