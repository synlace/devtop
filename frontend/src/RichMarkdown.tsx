import { lazy, Suspense, type ComponentProps } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import 'highlight.js/styles/github-dark.css'

// Mermaid is ~2MB; load it only when a document actually contains a diagram.
const MermaidDiagram = lazy(() => import('./MermaidDiagram'))

type Node = {
  type?: string
  children?: Node[]
  properties?: { className?: string | string[] }
  childrenValue?: string
  value?: string
}

// Flatten the highlighted <code> node back to its source text. rehype-highlight
// may leave mermaid as a plain text node or, if it ever tokenizes it, break it
// into hljs spans — walk all text leaves so the diagram source is never lost.
function mermaidCode(codeNode?: Node): string {
  if (!codeNode) return ''
  let out = ''
  const walk = (n: Node) => {
    if (n.type === 'text' && typeof n.value === 'string') out += n.value
    for (const c of n.children ?? []) walk(c)
  }
  walk(codeNode)
  return out.trimEnd()
}

function MermaidPre({ node, children }: ComponentProps<'pre'> & { node?: Node }) {
  const child = node?.children?.[0]
  const cls = child?.properties?.className
  const langs = Array.isArray(cls) ? cls : cls ? [cls] : []
  const lang = langs.find((c) => (c as string).startsWith?.('language-'))
  if (lang === 'language-mermaid') {
    const code = mermaidCode(child)
    return (
      <pre className="mdx-pre mermaid-container">
        <Suspense fallback={<div className="mermaid-loading">Loading diagram…</div>}>
          <MermaidDiagram code={code} />
        </Suspense>
      </pre>
    )
  }
  return <pre className="mdx-pre">{children}</pre>
}

export default function RichMarkdown({ source }: { source: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      rehypePlugins={[rehypeHighlight]}
      components={{ pre: MermaidPre }}
    >
      {source}
    </ReactMarkdown>
  )
}