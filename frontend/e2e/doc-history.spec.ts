import { test, expect } from '@playwright/test'

// The fixture's git history is seeded in global-setup.cjs: docs/history-e2e.mdx
// is committed twice ("Add history e2e doc" then "Update history e2e doc").
test('revision history rail lists commits and reads a past revision', async ({ page }) => {
  await page.goto('/#/docs/history-e2e')
  await expect(page.locator('main').getByRole('heading', { name: 'History' })).toBeVisible()

  // The clock button opens the rail from the header, left of the breadcrumbs.
  const clock = page.getByTitle('Revision history')
  await expect(clock).toBeVisible()
  await clock.click()
  await expect(page.getByText('Revisions', { exact: true })).toBeVisible()
  await expect(page.getByText('Update history e2e doc', { exact: true })).toBeVisible()
  await expect(page.getByText('Add history e2e doc', { exact: true })).toBeVisible()

  // Select the older commit: its body renders dimmed behind a diff panel.
  await page.getByText('Add history e2e doc', { exact: true }).click()
  await expect(page.getByText('What this commit changed', { exact: true })).toBeVisible()
  // The unified diff of that commit (the older body added) renders.
  await expect(page.locator('main').getByText('+First version of the history fixture.')).toBeVisible()
  // Historical content — "View current →" reveals the working copy again.
  await expect(page.getByText('View current →', { exact: true })).toBeVisible()
  await page.getByText('View current →', { exact: true }).click()
  await expect(page.getByText('Second revision: the stack changed.', { exact: true })).toBeVisible()
})