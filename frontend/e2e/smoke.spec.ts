import { test, expect } from '@playwright/test'

test('home page renders with fixture data and no uncaught errors', async ({ page }) => {
  const pageErrors: string[] = []
  page.on('pageerror', (err) => pageErrors.push(String(err)))

  await page.goto('/')

  await expect(page).toHaveTitle(/devtop/)
  await expect(page.getByRole('heading', { name: 'devtop — Docs + Tickets with AI' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Quick Start' })).toBeVisible()

  const sidebar = page.locator('aside').first()
  // Logo badge "devtop" and version render as adjacent text nodes (no space)
  await expect(sidebar.getByText('devtop', { exact: true })).toBeVisible()
  await expect(sidebar.getByText('v0.1.0', { exact: true })).toBeVisible()
  await expect(sidebar).toContainText('Local Server')
  await expect(sidebar).toContainText(':8000')
  // Fixture tickets loaded from the Go backend
  await expect(sidebar.getByText('3', { exact: true })).toBeVisible()

  expect(pageErrors).toEqual([])
})
