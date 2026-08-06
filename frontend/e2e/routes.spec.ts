import { test, expect } from '@playwright/test'

// Every primary route must render its content without uncaught exceptions.
const routes = [
  { url: '/', content: 'Fixture overview for UI tests.' },
  { url: '/#/docs/getting-started/configuration', content: 'Fixture configuration content.' },
  { url: '/#/tickets', content: 'Ticket Board' },
  { url: '/#/tickets/001', content: 'Fix the login button' },
]

for (const { url, content } of routes) {
  test(`no uncaught errors on ${url}`, async ({ page }) => {
    const pageErrors: string[] = []
    page.on('pageerror', (err) => pageErrors.push(String(err)))

    await page.goto(url)
    await expect(page.locator('main')).toContainText(content)

    expect(pageErrors).toEqual([])
  })
}
