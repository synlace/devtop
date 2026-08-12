// Active repo scope for API calls. The app always resolves an active repo —
// single-repo mode included, where the classic entry is a registered repo like
// any other — and every request carries ?repo=<name>, which the backend
// resolves per call (Resolve("") stays the backwards-compatible default).
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