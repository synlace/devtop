import { test, expect } from '@playwright/test'

test('tickets board lists fixture tickets and opens a detail', async ({ page }) => {
  await page.goto('/')

  await page.getByRole('link', { name: 'Board' }).click()
  await expect(page).toHaveURL(/#\/tickets/)
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()
  await expect(page.getByText('3 tickets')).toBeVisible()

  for (const title of ['Fix the login button', 'Add dark mode toggle', 'Improve docs search']) {
    await expect(page.getByRole('cell', { name: title })).toBeVisible()
  }

  await page.getByRole('cell', { name: 'Fix the login button' }).click()
  await expect(page).toHaveURL(/#\/tickets\/001/)
  const main = page.locator('main')
  // "dk-001" also appears in the chat panel's context label — scope to main
  await expect(main.getByText('dk-001', { exact: true }).first()).toBeVisible()
  await expect(main.getByRole('heading', { name: 'Fix the login button' })).toBeVisible()
  await expect(main.getByText('open', { exact: true })).toBeVisible()
  // "alice" appears both as assignee and as the fixture comment author
  await expect(main.getByText('alice', { exact: true }).first()).toBeVisible()
})
