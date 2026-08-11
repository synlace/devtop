const fs = require('node:fs')
const path = require('node:path')
const { execSync } = require('node:child_process')

// Reset the hermetic fixture's generated state before every run.
// The Go backend auto-seeds welcome threads, writes viewstate.json, and
// creates sqlite data during a run — without cleanup, that accumulated
// state leaks into the next run (e.g. the app restores a previously
// active chat thread, making the panel nondeterministic).
//
// AI chat runs (DEVTOP_AI_TESTS=1) go one step further: the whole stack
// runs against a fresh throwaway clone of the fixture, so even write-side
// agent tools (write_doc, create_ticket, ...) can never mutate the pristine
// devtop-data seed that the hermetic specs assert against.
module.exports = async () => {
  const seed = path.join(__dirname, 'fixtures', 'devtop-data')
  const aiEnabled = process.env.DEVTOP_AI_TESTS === '1'
  const fixture = aiEnabled ? path.join(__dirname, 'fixtures', '.devtop-ai-runtime') : seed

  if (aiEnabled) {
    fs.rmSync(fixture, { recursive: true, force: true })
    fs.cpSync(seed, fixture, { recursive: true })
  }

  for (const sub of ['threads', 'data']) {
    fs.rmSync(path.join(fixture, sub), { recursive: true, force: true })
    fs.mkdirSync(path.join(fixture, sub), { recursive: true })
  }
  fs.rmSync(path.join(fixture, 'viewstate.json'), { force: true })
  fs.rmSync(path.join(fixture, 'favourites.json'), { force: true })
  // config.yml is generated scaffolding (materialized from the bundled default
  // on first run) — reset it so the fixture always tracks the shipped default.
  fs.rmSync(path.join(fixture, 'config.yml'), { force: true })

  // Give the fixture deterministic git history so the revision endpoints
  // (and the history rail) can be e2e-tested hermetically. The backend
  // resolves the repo per-request (findRepoRoot), so a repo created here is
  // picked up even though the servers started earlier. The history-e2e doc is
  // committed twice so a real adjacent diff exists.
  fs.rmSync(path.join(fixture, '.git'), { recursive: true, force: true })
  const runIn = (args) => execSync(args, { cwd: fixture, stdio: 'pipe' })
  runIn('git init -q')
  runIn('git config user.email e2e@example.com')
  runIn('git config user.name "devtop e2e"')
  const histDoc = [
    '---',
    'title: "History E2E"',
    '---',
    '',
    '# History',
    '',
    'First version of the history fixture.',
  ].join('\n') + '\n'
  const histPath = path.join(fixture, 'docs', 'history-e2e.mdx')
  fs.writeFileSync(histPath, histDoc)
  runIn('git add -A')
  runIn('git commit -q -m "Add history e2e doc"')
  fs.writeFileSync(histPath, histDoc + '\nSecond revision: the stack changed.\n')
  runIn('git add -A')
  runIn('git commit -q -m "Update history e2e doc"')
}
