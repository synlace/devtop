// Active repo scope for API calls. Single-repo mode uses "" and never sends
// the param (backwards compatible); multi-repo mode appends ?repo=<name> to
// every request so the backend resolves the owning repo per call.
let activeRepo = ''

export function setActiveRepo(name: string) {
  activeRepo = name
}

export function getActiveRepo() {
  return activeRepo
}

export function api(path: string): string {
  if (!activeRepo) return path
  const sep = path.includes('?') ? '&' : '?'
  return `${path}${sep}repo=${encodeURIComponent(activeRepo)}`
}

// The status shape of one registered repo, as served by GET /api/repos.
export interface RepoStatus {
  name: string
  path: string
  branch: string
  status: string // ready | dirty | uninit | nogit
  dirty?: number
  docs: number
  initialized: boolean
  single: boolean
  has_git: boolean
  pending: number
}