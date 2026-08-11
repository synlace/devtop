import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ToolCallView } from './ToolCallView'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ToolCallView
      name="write_doc"
      args={{ path: 'test-docs/test_doc_2.mdx' }}
      status="complete"
      result="Written to docs/test-docs/test_doc_2.mdx"
    />
  </StrictMode>,
)
