const path = require('node:path')

// devtop repo root (parent of frontend/)
const devtopRoot = path.resolve(__dirname, '..')
// AI chat tests are opt-in (just test ai): they make live, paid model calls.
// When enabled, the whole stack runs against a fresh throwaway clone of the
// fixture so any docs/tickets the agent writes cannot mutate the pristine seed.
const aiEnabled = process.env.DEVTOP_AI_TESTS === '1'
const fixtureDir = aiEnabled
  ? path.join(__dirname, 'e2e', 'fixtures', '.devtop-ai-runtime')
  : path.join(__dirname, 'e2e', 'fixtures', 'devtop-data')
// CopilotKit runtime (copilot-server.js) port; must match vite.config.ts default
const copilotPort = process.env.DEVTOP_COPILOT_PORT || '4000'
// Test backend port — distinct from the live devtop stack on :8000 so tests
// never clash with a running `just devtop`.
const backendPort = process.env.DEVTOP_TEST_BACKEND_PORT || '8134'

module.exports = {
  testDir: './e2e',
  timeout: 60_000,
  fullyParallel: true,
  // AI-mode runs make live model calls through one shared runtime, so they are
  // serialized (workers=1) to avoid concurrent requests and cross-test writes
  // to the throwaway fixture. `just test ai` further splits @ai specs from the
  // hermetic specs into two separate invocations.
  workers: process.env.PLAYWRIGHT_WORKERS
    ? parseInt(process.env.PLAYWRIGHT_WORKERS, 10)
    : (aiEnabled ? 1 : 4),
  reporter: [['list'], ['html', { open: 'never' }]],

  // Reset the fixture's generated state (threads, sqlite, viewstate) each run
  globalSetup: './e2e/global-setup.cjs',

  use: {
    baseURL: 'http://127.0.0.1:5174',
    browserName: 'chromium',
    headless: true,
    colorScheme: 'dark',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    trace: 'retain-on-failure',
  },

  // Both servers are launched fresh per run on ports that don't collide with
  // the live stack (backend :8134, Vite :5174). reuseExistingServer is false
  // so tests never silently hit real data from a locally running `just devtop`.
  // With DEVTOP_AI_TESTS=1 a third server starts the real CopilotKit runtime,
  // which talks to OpenRouter using the key from devtop/.env.
  webServer: [
    {
      command: `go run . -port ${backendPort}`,
      cwd: devtopRoot,
      env: {
        DEVTOP_DIR: fixtureDir,
        PORT: backendPort,
        // Disable MCP server connections at startup for hermetic tests
        MCP_SERVERS: '',
        // Never load the real .env AI credentials into the test backend
        AI_API_KEY: '',
        AI_BASE_URL: '',
        AI_MODEL: '',
      },
      url: `http://127.0.0.1:${backendPort}/api/config`,
      reuseExistingServer: false,
      timeout: 90_000,
    },
    {
      // 5174 deliberately avoids colliding with a dev server on 5173.
      // --host 127.0.0.1: Vite's default 'localhost' can bind ::1 only, which
      // Playwright's http://127.0.0.1 URL probe cannot reach.
      command: 'npm run dev -- --host 127.0.0.1 --port 5174 --strictPort',
      cwd: __dirname,
      env: {
        // Vite proxies /api to the hermetic test backend
        DEVTOP_BACKEND_PORT: backendPort,
        // Avoid the stale root-owned node_modules/.vite cache (Docker stack)
        DEVTOP_VITE_CACHE_DIR: path.join(require('node:os').tmpdir(), 'devtop-vite-cache'),
      },
      url: 'http://127.0.0.1:5174',
      reuseExistingServer: false,
      timeout: 60_000,
    },
    ...(aiEnabled
      ? [
          {
            // Real CopilotKit runtime: reads the OpenRouter key from devtop/.env
            // itself. DEVTOP_GIT_DISABLED stops the agent's git_commit tool from
            // committing into the real repo during tests.
            command: 'node copilot-server.js',
            cwd: __dirname,
            env: {
              DEVTOP_DIR: fixtureDir,
              DEVTOP_GIT_DISABLED: '1',
              DEVTOP_GO_URL: `http://127.0.0.1:${backendPort}`,
            },
            url: `http://127.0.0.1:${copilotPort}/health`,
            reuseExistingServer: false,
            timeout: 60_000,
          },
        ]
      : []),
  ],

  outputDir: 'test-results/',
  preserveOutput: 'always',
}
