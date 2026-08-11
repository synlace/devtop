import { useState } from 'react'

// Compact tool-call card rendered inside the chat stream for the devtop
// agent's built-in tools: a status dot, the tool name, and (when expanded)
// the arguments and result.

type ToolCallViewProps = {
  name: string
  args: Record<string, unknown> | undefined
  status: string
  result?: string
}

export function ToolCallView({ name, args, status, result }: ToolCallViewProps) {
  const [open, setOpen] = useState(false)
  const running = status === 'inProgress' || status === 'executing'

  const argEntries = args ? Object.entries(args) : []

  return (
    <div className="my-1 rounded-lg border border-borderDark/60 bg-surfaceDark/40 overflow-hidden fade-in">
      <button
        onClick={() => setOpen(o => !o)}
        className="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-borderDark/20 transition-colors"
      >
        <span className="flex-shrink-0">
          {running ? (
            <span className="h-2 w-2 rounded-full bg-amber-400 animate-pulse inline-block" />
          ) : status === 'complete' ? (
            <span className="h-2 w-2 rounded-full bg-emerald-500 inline-block" />
          ) : (
            <span className="h-2 w-2 rounded-full bg-slate-500 inline-block" />
          )}
        </span>
        <span className="text-[11px] font-mono text-slate-300 truncate">{name}</span>
        {status === 'complete' && (
          <span className="text-[9px] text-emerald-400/80 shrink-0 ml-auto">done</span>
        )}
        <span
          className={`text-[10px] text-slate-400 transition-transform flex-shrink-0 ${open ? 'rotate-90' : ''}`}
        >
          ›
        </span>
      </button>
      {open && (
        <div className="border-t border-borderDark/40 px-3 py-2 space-y-1.5 text-[11px]">
          {argEntries.length > 0 ? (
            argEntries.map(([k, v]) => (
              <div key={k} className="flex gap-2">
                <span className="text-slate-500 font-mono flex-shrink-0">{k}</span>
                <span className="text-slate-300 font-mono break-all">
                  {typeof v === 'string' ? (v.length > 400 ? v.slice(0, 400) + '…' : v) : JSON.stringify(v)}
                </span>
              </div>
            ))
          ) : (
            <div className="text-slate-500 italic">no arguments</div>
          )}
          {status === 'complete' && result && (
            <div className="text-slate-400 font-mono break-all border-t border-borderDark/40 pt-1.5">
              {result.length > 400 ? result.slice(0, 400) + '…' : result}
            </div>
          )}
        </div>
      )}
    </div>
  )
}