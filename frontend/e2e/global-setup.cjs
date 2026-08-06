const fs = require('node:fs')
const path = require('node:path')

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
  // config.yml is generated scaffolding (materialized from the bundled default
  // on first run) — reset it so the fixture always tracks the shipped default.
  fs.rmSync(path.join(fixture, 'config.yml'), { force: true })
}
