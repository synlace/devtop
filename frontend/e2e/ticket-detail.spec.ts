import { test, expect } from '@playwright/test'

// CopilotKit auto-attaches its <cpk-web-inspector> overlay, which can swallow
// pointer events. Hidden so clicks stay deterministic.
async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

test('board rows show status and priority badges', async ({ page }) => {
  await page.goto('/#/tickets')
  await hideWebInspector(page)
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()

  const openRow = page.getByRole('row', { name: /Fix the login button/ })
  await expect(openRow).toContainText('open')
  await expect(openRow).toContainText('high')

  const inProgressRow = page.getByRole('row', { name: /Add dark mode toggle/ })
  await expect(inProgressRow).toContainText('in-progress')
  await expect(inProgressRow).toContainText('medium')

  const doneRow = page.getByRole('row', { name: /Improve docs search/ })
  await expect(doneRow).toContainText('done')
  await expect(doneRow).toContainText('low')
})

test('ticket detail renders metadata, description and comments', async ({ page }) => {
  await page.goto('/#/tickets/001')
  await hideWebInspector(page)

  await expect(page.getByRole('heading', { name: 'Fix the login button' })).toBeVisible()

  const main = page.locator('main')
  await expect(main).toContainText('open')
  await expect(main).toContainText('high priority')
  await expect(main).toContainText('alice')
  await expect(main).toContainText('does not validate empty input')

  // Comment parsed from the fixture ticket body by the Go backend
  await expect(main).toContainText('QA noted the empty-input validation is missing.')
})
