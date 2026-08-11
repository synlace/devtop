import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ChevronRight, Clock3, Copy, Download, Star, Trash2 } from 'lucide-react'

// The fast-path actions a row's ⋯ menu exposes. PDF/DOCX shipping is deferred
// (they'd need a real render pipeline), so those formats stay disabled.
export type DocExportFormat = 'pdf' | 'docx' | 'mdx' | 'txt'

export interface DocMenuAnchor {
  slug: string
  title: string
  /** Right edge / top of the dots trigger, in viewport coordinates. */
  x: number
  y: number
}

interface DocActionsMenuProps {
  anchor: DocMenuAnchor
  isFav: boolean
  onToggleFav: (slug: string) => void
  onHistory: (slug: string) => void
  onCopyPath: (slug: string) => void
  onExport: (slug: string, format: DocExportFormat) => void
  onDelete: (slug: string) => void
  onClose: () => void
}

const MENU_W = 248
const itemCls = 'flex items-center gap-2.5 w-full text-left px-2.5 py-1.5 text-[12px] text-slate-300 hover:text-slate-100 hover:bg-borderDark/30 transition-colors'
const subItemCls = `${itemCls} justify-between`
const iconCls = 'w-3.5 h-3.5 text-slate-500 flex-shrink-0'

export default function DocActionsMenu({ anchor, isFav, onToggleFav, onHistory, onCopyPath, onExport, onDelete, onClose }: DocActionsMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)
  const exportItemRef = useRef<HTMLButtonElement>(null)
  const subRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  const [subOpen, setSubOpen] = useState(false)
  const [subPos, setSubPos] = useState<{ left: number; top: number } | null>(null)

  // Anchor the menu at the dots, flipping to stay in the viewport.
  useLayoutEffect(() => {
    const el = menuRef.current
    if (!el) return
    const h = el.offsetHeight
    let left = anchor.x + 6
    if (left + MENU_W > window.innerWidth - 8) left = Math.max(8, anchor.x - 6 - MENU_W)
    const top = anchor.y + h > window.innerHeight - 8 ? Math.max(8, anchor.y - h) : anchor.y
    setPos({ left, top })
  }, [anchor.x, anchor.y])

  // Position the export flyout next to its item once mounted.
  useLayoutEffect(() => {
    if (!subOpen) { setSubPos(null); return }
    const item = exportItemRef.current
    const sub = subRef.current
    if (!item || !sub) return
    const ir = item.getBoundingClientRect()
    const sw = sub.offsetWidth || 176
    let left = ir.right + 4
    if (left + sw > window.innerWidth - 8) left = Math.max(8, ir.left - sw - 4)
    setSubPos({ left, top: Math.max(8, ir.top) })
  }, [subOpen])

  // Close on outside click or Escape; rove with the arrow keys.
  useEffect(() => {
    const onMouseDown = (e: MouseEvent) => {
      const el = e.target as Node
      if (menuRef.current?.contains(el) || subRef.current?.contains(el)) return
      onClose()
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (subOpen) setSubOpen(false)
        else onClose()
        e.preventDefault()
        return
      }
      const items = Array.from(document.querySelectorAll<HTMLElement>('[data-doc-menu]'))
        .filter(el => getComputedStyle(el).display !== 'none' && !(el as HTMLButtonElement).disabled)
      if (items.length === 0) return
      const idx = items.indexOf(document.activeElement as HTMLElement)
      if (e.key === 'ArrowRight') {
        if (document.activeElement === exportItemRef.current) {
          e.preventDefault()
          setSubOpen(true)
          subRef.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
        }
      } else if (e.key === 'ArrowLeft') {
        if (subOpen) { e.preventDefault(); setSubOpen(false); exportItemRef.current?.focus() }
      } else if (e.key === 'ArrowDown') {
        e.preventDefault(); items[(idx + 1) % items.length].focus()
      } else if (e.key === 'ArrowUp') {
        e.preventDefault(); items[(idx - 1 + items.length) % items.length].focus()
      } else if (e.key === 'Enter') {
        const a = document.activeElement
        if (a && a.hasAttribute('data-doc-menu')) (a as HTMLElement).click()
      }
    }
    document.addEventListener('mousedown', onMouseDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onMouseDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [subOpen, onClose])

  useEffect(() => { setSubOpen(false) }, [anchor.slug])

  return (
    <>
      <div
        ref={menuRef}
        role="menu"
        aria-label={`Actions for ${anchor.title}`}
        className={`fixed z-40 w-[248px] rounded-xl border border-borderDark/60 bg-[#121826]/95 backdrop-blur-md shadow-2xl shadow-black/40 overflow-hidden doc-menu-pop ${pos ? '' : 'invisible'}`}
        style={pos ? { left: pos.left, top: pos.top } : undefined}
      >
        {/* Quick actions: favourite / history / delete */}
        <div className="flex items-stretch divide-x divide-borderDark/30 border-b border-borderDark/30">
          <button
            type="button"
            onClick={() => onToggleFav(anchor.slug)}
            title={isFav ? 'Remove from favourites' : 'Add to favourites'}
            className={`flex-1 flex items-center justify-center h-9 transition-colors ${isFav ? 'text-amber-400 hover:bg-borderDark/30' : 'text-slate-300 hover:bg-borderDark/30'}`}
          >
            <Star className="w-4 h-4" fill={isFav ? 'currentColor' : 'none'} />
          </button>
          <button
            type="button"
            onClick={() => onHistory(anchor.slug)}
            title="Revision history"
            className="flex-1 flex items-center justify-center h-9 text-slate-300 hover:bg-borderDark/30 transition-colors"
          >
            <Clock3 className="w-4 h-4" />
          </button>
          <button
            type="button"
            onClick={() => onDelete(anchor.slug)}
            title="Delete"
            className="flex-1 flex items-center justify-center h-9 text-red-400 hover:bg-red-500/10 hover:text-red-300 transition-colors"
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>

        <div className="py-1.5">
          <button
            type="button"
            data-doc-menu
            role="menuitem"
            onClick={() => { onCopyPath(anchor.slug); onClose() }}
            className={itemCls}
          >
            <Copy className={iconCls} />
            <span>Copy path</span>
          </button>

          <button
            type="button"
            ref={exportItemRef}
            data-doc-menu
            role="menuitem"
            aria-haspopup="menu"
            aria-expanded={subOpen}
            onClick={() => setSubOpen(o => !o)}
            onMouseLeave={(e) => {
              // Only auto-close when moving somewhere other than the flyout —
              // closing on a transient leave (e.g. the pop animation shifting
              // bounds) would defeat hover-free clicking.
              if (subRef.current?.contains(e.relatedTarget as Node)) return
              setSubOpen(false)
            }}
            className={`${itemCls} ${subOpen ? 'bg-borderDark/30 text-slate-100' : ''}`}
          >
            <Download className={iconCls} />
            <span>Export as</span>
            <ChevronRight className="w-3 h-3 text-slate-500 ml-auto flex-shrink-0" />
          </button>
        </div>
      </div>

      {/* Export formats — PDF/DOCX deferred, Markdown/Plain text download now.
          Sibling to (not inside) the menu so backdrop-filter on the menu can
          never become this fixed element's containing block. */}
      {subOpen && (
        <div
          ref={subRef}
          role="menu"
          aria-label="Export as"
          onMouseLeave={(e) => {
            // Closing when the pointer leaves the flyout itself (not when it
            // slips back onto the Export item).
            if (exportItemRef.current?.contains(e.relatedTarget as Node)) return
            setSubOpen(false)
          }}
          className={`fixed z-40 w-44 rounded-xl border border-borderDark/60 bg-[#121826]/95 backdrop-blur-md shadow-2xl shadow-black/40 overflow-hidden py-1 doc-menu-pop ${subPos ? '' : 'invisible'}`}
          style={subPos ? { left: subPos.left, top: subPos.top } : undefined}
        >
          <button type="button" data-doc-menu role="menuitem" disabled title="Not available in the local build yet" className={`${subItemCls} disabled:opacity-40 disabled:text-slate-500 disabled:hover:bg-transparent disabled:hover:text-slate-500`}>
            <span>PDF</span><span className="text-[9px] font-mono text-slate-600">.pdf</span>
          </button>
          <button type="button" data-doc-menu role="menuitem" disabled title="Not available in the local build yet" className={`${subItemCls} disabled:opacity-40 disabled:text-slate-500 disabled:hover:bg-transparent disabled:hover:text-slate-500`}>
            <span>Word</span><span className="text-[9px] font-mono text-slate-600">.docx</span>
          </button>
          <button type="button" data-doc-menu role="menuitem" onClick={() => { onExport(anchor.slug, 'mdx'); onClose() }} className={subItemCls}>
            <span>Markdown</span><span className="text-[9px] font-mono text-slate-500">.mdx</span>
          </button>
          <button type="button" data-doc-menu role="menuitem" onClick={() => { onExport(anchor.slug, 'txt'); onClose() }} className={subItemCls}>
            <span>Plain text</span><span className="text-[9px] font-mono text-slate-500">.txt</span>
          </button>
        </div>
      )}
    </>
  )
}