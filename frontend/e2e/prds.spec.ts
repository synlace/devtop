import { test, expect } from '@playwright/test'

// The engine renders any config-declared kind with a "list" nav view via the
// generic artifact endpoints. The default config ships the prds kind, and the
// fixture seeds one PRD — this spec proves the list/detail views work without
// any per-kind frontend code.
async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

test('PRD list renders from the config-declared prds kind', async ({ page }) => {
  await page.goto('/#/prds')
  await hideWebInspector(page)

  // Nav section exists (config-driven).
  await expect(page.locator('nav').getByRole('link', { name: 'PRDs' })).toBeVisible()

  // List page shows the seeded PRD with its draft status badge.
  await expect(page.getByRole('heading', { name: 'PRDs' })).toBeVisible()
  await expect(page.getByText('Onboarding Flow Redesign')).toBeVisible()
  await expect(page.getByText('draft')).toBeVisible()
})

test('PRD detail renders markdown via the generic artifact endpoint', async ({ page }) => {
  await page.goto('/#/prds/onboarding-flow')
  await hideWebInspector(page)

  await expect(page.locator('main')).toContainText('Onboarding Flow Redesign')
  await expect(page.locator('main')).toContainText('Step progress indicator')
})
