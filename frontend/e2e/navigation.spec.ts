import { test, expect } from '@playwright/test'

// CopilotKit auto-attaches its <cpk-web-inspector> overlay, which can swallow
// pointer events. Hidden so sidebar/nav clicks stay deterministic.
async function hideWebInspector(page: import('@playwright/test').Page) {
  await page.addStyleTag({ content: 'cpk-web-inspector, cpk-thread-inspector { display: none !important; }' })
}

test('deep-link to a doc renders on cold load', async ({ page }) => {
  await page.goto('/#/docs/getting-started/configuration')
  await hideWebInspector(page)
  await expect(page.locator('main')).toContainText('Fixture configuration content.')
})

test('reload preserves the current route', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)
  await page.locator('nav').getByRole('link', { name: 'Board' }).click()
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()

  await page.reload()
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()
})

test('browser back and forward walk the hash history', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)

  await page.locator('nav').getByRole('link', { name: 'Board' }).click()
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()
  await page.locator('nav').getByRole('link', { name: 'Configuration' }).click()
  await expect(page.locator('main')).toContainText('Fixture configuration content.')

  await page.goBack()
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()
  await page.goForward()
  await expect(page.locator('main')).toContainText('Fixture configuration content.')
})

test('active nav item is highlighted', async ({ page }) => {
  await page.goto('/')
  await hideWebInspector(page)
  const nav = page.locator('nav')
  await expect(nav.getByRole('link', { name: 'Home' })).toHaveClass(/bg-accentBlue\/10/)

  await nav.getByRole('link', { name: 'Board' }).click()
  await expect(nav.getByRole('link', { name: 'Board' })).toHaveClass(/bg-accentPurple\/10/)
})

test('back-to-board link returns from ticket detail', async ({ page }) => {
  await page.goto('/#/tickets/001')
  await hideWebInspector(page)
  await expect(page.getByRole('heading', { name: 'Fix the login button' })).toBeVisible()

  await page.getByRole('link', { name: 'Back to Board' }).click()
  await expect(page.getByRole('heading', { name: 'Ticket Board' })).toBeVisible()
})
