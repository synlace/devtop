import { useEffect, useRef, useState } from 'react'

export default function MermaidDiagram({ code }: { code: string }) {
  const [svg, setSvg] = useState<string>('')
  const [state, setState] = useState<'loading' | 'error' | 'done'>('loading')
  const idRef = useRef(`mermaid-${Math.random().toString(36).slice(2, 10)}`)
  const id = idRef.current

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const { default: mermaid } = await import('mermaid')
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          theme: 'dark',
          fontFamily: 'inherit',
          themeVariables: {
            fontFamily: 'inherit',
          },
        })
        const { svg } = await mermaid.render(id, code)
        if (!cancelled) {
          setSvg(svg)
          setState('done')
        }
      } catch {
        if (!cancelled) setState('error')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code, id])

  if (state === 'error') {
    return <pre className="mermaid-error">Mermaid diagram could not render — check the syntax.</pre>
  }
  if (state !== 'done') {
    return <div className="mermaid-loading">Loading diagram…</div>
  }
  return <div className="mermaid" dangerouslySetInnerHTML={{ __html: svg }} />
}