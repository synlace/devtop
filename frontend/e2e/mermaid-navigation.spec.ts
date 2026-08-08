import { test, expect } from '@playwright/test'

test('SPA navigation between mermaid and plain docs does not crash', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(String(e)))

  // The app routes on hashchange without a full reload (SPA), which is what
  // exercises the pre/<Suspense> host-element swap that threw Node.removeChild.
  await page.goto('/#/docs/rich-render')
  await expect(page.locator('.prose-custom .mermaid svg')).toBeVisible({ timeout: 15_000 })

  const go = (hash: string) => page.evaluate((h) => { location.hash = h }, hash)

  await go('#/docs/architecture/overview')
  await expect(page.locator('main').getByRole('heading', { name: 'Architecture Overview' })).toBeVisible()

  await go('#/docs/rich-render')
  await expect(page.locator('.prose-custom .mermaid svg')).toBeVisible({ timeout: 15_000 })

  await go('#/docs/architecture/overview')
  await expect(page.locator('main').getByRole('heading', { name: 'Architecture Overview' })).toBeVisible()

  expect(errors).toEqual([])
})