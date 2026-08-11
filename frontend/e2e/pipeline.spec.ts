import { test, expect } from '@playwright/test'

// The derivation page (PipelineView) tests, served via route interception so
// the eligibility flow is hermetic: no model calls, no disk writes.

const EDGES = [
  { from: 'docs', to: 'prds', transform: 'breakdown', agent: 'prd-builder', classifier: 'classify-doc' },
  { from: 'prds', to: 'tickets', transform: 'derive_tickets', agent: 'ticket-deriver', gate: 'prds.status == approved' },
]

function baseItems() {
  return [
    {
      doc_id: 'architecture/agent-engine',
      title: 'Agent Engine',
      slug: 'architecture/agent-engine',
      path: 'architecture/agent-engine.mdx',
      dir: 'architecture',
      summary: 'How the embedded agent works.',
      prospect: 'eligible',
      prospect_by: 'user',
      prd: { id: 'architecture/agent-engine', title: 'Agent Engine PRD', status: 'approved', reqs: 3, slug: 'architecture/agent-engine' },
      tickets: [{ id: '006', title: 'Document agent tool set', status: 'open' }],
      stale: false,
    },
    {
      doc_id: 'reference/api',
      title: 'API',
      slug: 'reference/api',
      path: 'reference/api.mdx',
      dir: 'reference',
      summary: 'The HTTP surface.',
      prospect: 'eligible',
      prospect_by: 'model',
      prd: null,
      tickets: [],
      stale: false,
    },
    {
      doc_id: 'reference/deployment',
      title: 'Deployment',
      slug: 'reference/deployment',
      path: 'reference/deployment.mdx',
      dir: 'reference',
      summary: 'Building and running.',
      prospect: 'not-eligible',
      prospect_by: 'model',
      prd: null,
      tickets: [],
      stale: false,
    },
    {
      doc_id: 'roadmap/product-notes',
      title: 'Product Notes',
      slug: 'roadmap/product-notes',
      path: 'roadmap/product-notes.mdx',
      dir: 'roadmap',
      summary: 'Raw notes.',
      prd: null,
      tickets: [],
      stale: false,
    },
  ]
}

async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

test('groups docs by eligibility and gates derive actions per cell', async ({ page }) => {
  const items = baseItems()
  await page.route('**/api/pipeline', route => route.fulfill({ json: { edges: EDGES, items } }))
  await page.goto('/#/pipeline')
  await hideWebInspector(page)

  // Sections: Eligible open, Unassessed and Not eligible collapsed.
  await expect(page.getByRole('button', { name: /Eligible/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /Not eligible/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /Unassessed/ })).toBeVisible()

  // Derive is gated: only the model-suggested eligible doc (no PRD) offers it.
  expect(await page.getByRole('button', { name: 'Derive PRD' }).count()).toBe(1)
  // User-verified eligible with an approved PRD offers a new ticket, in the
  // tickets cell, not in a header action.
  expect(await page.getByRole('button', { name: 'New ticket' }).count()).toBe(1)
  expect(await page.getByRole('button', { name: 'Approve' }).count()).toBe(0)

  // Prospect actions live in the document cell of each section. Unassessed
  // and Not eligible sections are collapsed by default; open both.
  await page.getByRole('button', { name: /Unassessed/ }).click()
  await expect(page.getByRole('button', { name: 'Suggest eligibility' })).toBeVisible()
  await page.getByRole('button', { name: /Not eligible/ }).click()
  await expect(page.getByRole('button', { name: 'Override' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Confirm' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Exclude' })).toBeVisible()

  // Provenance chips render with model/user distinction.
  await expect(page.getByText('eligible · verified', { exact: true })).toBeVisible()
  await expect(page.getByText('eligible · suggested', { exact: true })).toBeVisible()
  await expect(page.getByText('not eligible · suggested', { exact: true })).toBeVisible()

  // Directory sub-groups are collapsible; a card shows breadcrumb + path.
  await expect(page.getByText('Architecture', { exact: true })).toBeVisible()
  await expect(page.getByText('.devtop/docs/architecture/agent-engine.mdx')).toBeVisible()
})

test('user verdict seals the doc: Confirm moves it to verified', async ({ page }) => {
  const items = baseItems()
  await page.route('**/api/pipeline', route => route.fulfill({ json: { edges: EDGES, items } }))
  await page.route('**/api/pipeline/prospect', async route => {
    const body = route.request().postDataJSON()
    const item = items.find(i => i.slug === body.slug)
    if (item) {
      item.prospect = body.verdict
      item.prospect_by = 'user'
    }
    route.fulfill({ json: { id: body.slug, prospect: body.verdict, prospect_by: 'user' } })
  })

  await page.goto('/#/pipeline')
  await hideWebInspector(page)

  // Confirm the model-suggested eligible doc: it becomes a second verified
  // chip and the suggested one disappears.
  await page.getByRole('button', { name: 'Confirm' }).click()
  await expect(page.getByText('eligible · verified').first()).toBeVisible()
  expect(await page.getByText('eligible · verified').count()).toBe(2)
  expect(await page.getByText('eligible · suggested').count()).toBe(0)
})

test('classify failure surfaces instead of silently leaving the doc unassessed', async ({ page }) => {
  const items = baseItems()
  await page.route('**/api/pipeline', route => route.fulfill({ json: { edges: EDGES, items } }))
  await page.route('**/api/pipeline/prospect/classify', route =>
    route.fulfill({ status: 502, json: { error: 'AI_API_KEY not configured' } }))

  await page.goto('/#/pipeline')
  await hideWebInspector(page)

  await page.getByRole('button', { name: /Unassessed/ }).click()
  await page.getByRole('button', { name: 'Suggest eligibility' }).click()

  // The failure is visible on the error panel, and nothing claims a sealed
  // verdict: no verified chips appear.
  await expect(page.getByText(/Pipeline error/)).toBeVisible()
  expect(await page.getByText('eligible · verified', { exact: true }).count()).toBe(0)
})