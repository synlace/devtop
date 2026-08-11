import type { ReactToolCallRenderer } from '@copilotkit/react-core/v2'
import { ToolCallView } from './ToolCallView'

// Renderers for the devtop agent's built-in tools. Each shows a compact,
// expandable card in the chat stream: name + status badge, argument summary,
// and the tool's result when complete. Named renderers let the common agent
// tools read as first-class UI (instead of opaque JSON), while the wildcard
// renderer in App.tsx covers anything unregistered (e.g. MCP tools).

const AGENT_TOOLS = [
  'read_doc',
  'read_doc_at',
  'list_doc_revisions',
  'write_doc',
  'list_docs',
  'read_workspace_file',
  'list_workspace_files',
  'list_tickets',
  'read_ticket',
  'create_ticket',
  'update_ticket',
  'add_comment',
  'git_commit',
  'ask_user',
]

export const toolCallRenderers: ReactToolCallRenderer<any>[] = AGENT_TOOLS.map(name => ({
  name,
  render: (props: { name: string; args: Record<string, unknown> | undefined; status: string; result?: string }) => (
    <ToolCallView {...props} />
  ),
}))